package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Test case origins and statuses (CHECK constraints in 0001_init.sql).
const (
	OriginManual    = "manual"
	OriginGenerated = "generated"

	StatusActive      = "active"
	StatusQuarantined = "quarantined"
	StatusRetired     = "retired"
)

// ValidOrigin reports whether s is a known test case origin.
func ValidOrigin(s string) bool {
	return s == OriginManual || s == OriginGenerated
}

// ValidTestStatus reports whether s is a known test case status.
func ValidTestStatus(s string) bool {
	return s == StatusActive || s == StatusQuarantined || s == StatusRetired
}

// TestCase is one Playwright test, identified by app, file and title.
type TestCase struct {
	ID         int64
	AppID      int64
	FilePath   string
	Title      string
	Origin     string
	Status     string
	AgentRunID *int64
	CreatedAt  time.Time
}

// TestCaseInput identifies a test case for UpsertTestCase.
type TestCaseInput struct {
	AppID      int64
	FilePath   string
	Title      string
	Origin     string
	AgentRunID *int64
}

// TestCaseFilter narrows ListTestCases; empty fields match everything.
type TestCaseFilter struct {
	Origin string
	Status string
}

const testCaseColumns = `id, app_id, file_path, title, origin, status, agent_run_id, created_at`

// UpsertTestCase inserts a test case or, if (app_id, file_path, title) exists,
// updates its origin and (when given) agent_run_id. ID, status and created_at
// are kept, so a quarantined test stays quarantined across runs.
func (s *Store) UpsertTestCase(ctx context.Context, in TestCaseInput) (TestCase, error) {
	row := s.db.QueryRowContext(ctx,
		`INSERT INTO test_cases (app_id, file_path, title, origin, status, agent_run_id, created_at)
		 VALUES (?, ?, ?, ?, 'active', ?, ?)
		 ON CONFLICT (app_id, file_path, title) DO UPDATE SET
			origin = excluded.origin,
			agent_run_id = COALESCE(excluded.agent_run_id, test_cases.agent_run_id)
		 RETURNING `+testCaseColumns,
		in.AppID, in.FilePath, in.Title, in.Origin, in.AgentRunID, nowMillis())
	tc, err := scanTestCase(row)
	if err != nil {
		return TestCase{}, fmt.Errorf("store: upsert test case: %w", err)
	}
	return tc, nil
}

// GetTestCase returns the test case with the given ID or ErrNotFound.
func (s *Store) GetTestCase(ctx context.Context, id int64) (TestCase, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+testCaseColumns+` FROM test_cases WHERE id = ?`, id)
	return scanTestCase(row)
}

// ListTestCases returns an app's test cases ordered by file and title.
func (s *Store) ListTestCases(ctx context.Context, appID int64, f TestCaseFilter) ([]TestCase, error) {
	where := []string{"app_id = ?"}
	args := []any{appID}
	if f.Origin != "" {
		where = append(where, "origin = ?")
		args = append(args, f.Origin)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	query := `SELECT ` + testCaseColumns + ` FROM test_cases WHERE ` +
		strings.Join(where, " AND ") + ` ORDER BY file_path, title`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list test cases: %w", err)
	}
	defer rows.Close()

	tcs := []TestCase{}
	for rows.Next() {
		tc, err := scanTestCase(rows)
		if err != nil {
			return nil, err
		}
		tcs = append(tcs, tc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list test cases: %w", err)
	}
	return tcs, nil
}

// UpdateTestCaseStatus sets a test case's status and returns the updated row.
func (s *Store) UpdateTestCaseStatus(ctx context.Context, id int64, status string) (TestCase, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE test_cases SET status = ? WHERE id = ?`, status, id)
	if err != nil {
		return TestCase{}, fmt.Errorf("store: update test case %d: %w", id, err)
	}
	if err := requireOneRow(res, "update test case", id); err != nil {
		return TestCase{}, err
	}
	return s.GetTestCase(ctx, id)
}

func scanTestCase(sc scanner) (TestCase, error) {
	var tc TestCase
	var agentRunID sql.NullInt64
	var createdAt int64
	err := sc.Scan(&tc.ID, &tc.AppID, &tc.FilePath, &tc.Title, &tc.Origin, &tc.Status, &agentRunID, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return TestCase{}, fmt.Errorf("store: test case: %w", ErrNotFound)
	}
	if err != nil {
		return TestCase{}, fmt.Errorf("store: scan test case: %w", err)
	}
	if agentRunID.Valid {
		tc.AgentRunID = &agentRunID.Int64
	}
	tc.CreatedAt = fromMillis(createdAt)
	return tc, nil
}
