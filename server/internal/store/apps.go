package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// App is a web application under test.
type App struct {
	ID               int64
	Name             string
	Slug             string
	BaseURL          string
	Notes            string
	AllowDestructive bool
	CreatedAt        time.Time
}

// NewApp holds the fields set when an app is created.
type NewApp struct {
	Name             string
	Slug             string
	BaseURL          string
	Notes            string
	AllowDestructive bool
}

// AppUpdate holds optional changes; nil fields are left as they are.
// The slug is not updatable because it names folders on disk.
type AppUpdate struct {
	Name             *string
	BaseURL          *string
	Notes            *string
	AllowDestructive *bool
}

const appColumns = `id, name, slug, base_url, notes, allow_destructive, created_at`

// CreateApp inserts an app. It returns ErrConflict if the slug is taken.
func (s *Store) CreateApp(ctx context.Context, in NewApp) (App, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO apps (name, slug, base_url, notes, allow_destructive, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		in.Name, in.Slug, in.BaseURL, in.Notes, boolToInt(in.AllowDestructive), nowMillis())
	if isUniqueViolation(err) {
		return App{}, fmt.Errorf("store: create app %q: %w", in.Slug, ErrConflict)
	}
	if err != nil {
		return App{}, fmt.Errorf("store: create app: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return App{}, fmt.Errorf("store: create app: %w", err)
	}
	return s.GetApp(ctx, id)
}

// GetApp returns the app with the given ID or ErrNotFound.
func (s *Store) GetApp(ctx context.Context, id int64) (App, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+appColumns+` FROM apps WHERE id = ?`, id)
	return scanApp(row)
}

// GetAppBySlug returns the app with the given slug or ErrNotFound.
func (s *Store) GetAppBySlug(ctx context.Context, slug string) (App, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+appColumns+` FROM apps WHERE slug = ?`, slug)
	return scanApp(row)
}

// ListApps returns all apps ordered by name.
func (s *Store) ListApps(ctx context.Context) ([]App, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+appColumns+` FROM apps ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("store: list apps: %w", err)
	}
	defer rows.Close()

	apps := []App{}
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		apps = append(apps, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list apps: %w", err)
	}
	return apps, nil
}

// UpdateApp applies the non-nil fields of u and returns the updated app.
// COALESCE keeps the current value for every NULL (nil) parameter.
func (s *Store) UpdateApp(ctx context.Context, id int64, u AppUpdate) (App, error) {
	var allowDestructive *int
	if u.AllowDestructive != nil {
		v := boolToInt(*u.AllowDestructive)
		allowDestructive = &v
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE apps SET
			name = COALESCE(?, name),
			base_url = COALESCE(?, base_url),
			notes = COALESCE(?, notes),
			allow_destructive = COALESCE(?, allow_destructive)
		 WHERE id = ?`,
		u.Name, u.BaseURL, u.Notes, allowDestructive, id)
	if err != nil {
		return App{}, fmt.Errorf("store: update app %d: %w", id, err)
	}
	if err := requireOneRow(res, "update app", id); err != nil {
		return App{}, err
	}
	return s.GetApp(ctx, id)
}

// DeleteApp deletes an app; foreign keys cascade to its rows in other tables.
func (s *Store) DeleteApp(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM apps WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete app %d: %w", id, err)
	}
	return requireOneRow(res, "delete app", id)
}

// SlugExists reports whether an app already uses slug.
func (s *Store) SlugExists(ctx context.Context, slug string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM apps WHERE slug = ?`, slug).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("store: check slug: %w", err)
	}
	return n > 0, nil
}

// scanner is satisfied by *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanApp(sc scanner) (App, error) {
	var a App
	var allowDestructive int
	var createdAt int64
	err := sc.Scan(&a.ID, &a.Name, &a.Slug, &a.BaseURL, &a.Notes, &allowDestructive, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return App{}, fmt.Errorf("store: app: %w", ErrNotFound)
	}
	if err != nil {
		return App{}, fmt.Errorf("store: scan app: %w", err)
	}
	a.AllowDestructive = allowDestructive == 1
	a.CreatedAt = fromMillis(createdAt)
	return a, nil
}

// requireOneRow turns "zero rows affected" into ErrNotFound.
func requireOneRow(res sql.Result, op string, id int64) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: %s %d: %w", op, id, err)
	}
	if n == 0 {
		return fmt.Errorf("store: %s %d: %w", op, id, ErrNotFound)
	}
	return nil
}
