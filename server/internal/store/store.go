// Package store is Sentinel's SQLite access layer: it opens the database,
// applies the embedded migrations and holds the hand-written queries.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// Sentinel errors returned by store methods.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("already exists")
)

// Store wraps the database handle.
type Store struct {
	db *sql.DB
}

// DBFile is the database file name inside DATA_DIR.
const DBFile = "sentinel.db"

// Open creates dataDir if needed, opens dataDir/sentinel.db and applies
// pending migrations.
func Open(ctx context.Context, dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("store: create data dir: %w", err)
	}
	return openPath(ctx, filepath.Join(dataDir, DBFile))
}

func openPath(ctx context.Context, path string) (*Store, error) {
	// Pragmas in the DSN run on every new connection.
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	dsn := "file:" + path + "?" + q.Encode()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	// SQLite has a single writer; one connection avoids "database is locked"
	// errors between our own connections (ADR-0003).
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// Ping checks that the database answers.
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func nowMillis() int64 {
	return time.Now().UnixMilli()
}

func fromMillis(ms int64) time.Time {
	return time.UnixMilli(ms).UTC()
}

// isUniqueViolation reports whether err is a SQLite UNIQUE constraint error.
// Matching the message keeps us independent of the driver's error types.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
