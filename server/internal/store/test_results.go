package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// Test result statuses (CHECK constraint in 0001_init.sql).
const (
	ResultPassed  = "passed"
	ResultFailed  = "failed"
	ResultFlaky   = "flaky"
	ResultSkipped = "skipped"
)

// TestResult is one test's outcome in a run.
type TestResult struct {
	ID         int64
	RunID      int64
	TestCaseID *int64
	Title      string
	FilePath   string
	Line       int
	Status     string
	DurationMS int64
	Error      string // empty when there is none
	Retries    int
	// Artifacts maps a kind (screenshot, trace, url, aria_snapshot) to a path
	// relative to DATA_DIR. Stored as artifacts_json.
	Artifacts map[string]string
}

const testResultColumns = `id, run_id, test_case_id, title, file_path, line, status,
	duration_ms, error, retries, artifacts_json`

// InsertTestResults stores a run's results in one transaction, so a run never
// shows only part of its results. ID and RunID of the inputs are ignored.
func (s *Store) InsertTestResults(ctx context.Context, runID int64, results []TestResult) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: insert test results: %w", err)
	}
	defer tx.Rollback() // no-op after Commit

	for _, r := range results {
		artifacts := r.Artifacts
		if artifacts == nil {
			artifacts = map[string]string{}
		}
		artifactsJSON, err := json.Marshal(artifacts)
		if err != nil {
			return fmt.Errorf("store: encode artifacts: %w", err)
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO test_results (run_id, test_case_id, title, file_path, line, status,
				duration_ms, error, retries, artifacts_json)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			runID, r.TestCaseID, r.Title, r.FilePath, r.Line, r.Status,
			r.DurationMS, nullIfEmpty(r.Error), r.Retries, string(artifactsJSON))
		if err != nil {
			return fmt.Errorf("store: insert test result %q: %w", r.Title, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: insert test results: %w", err)
	}
	return nil
}

// ListTestResults returns a run's results in the order they were stored.
func (s *Store) ListTestResults(ctx context.Context, runID int64) ([]TestResult, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+testResultColumns+` FROM test_results WHERE run_id = ? ORDER BY id`, runID)
	if err != nil {
		return nil, fmt.Errorf("store: list test results: %w", err)
	}
	defer rows.Close()

	results := []TestResult{}
	for rows.Next() {
		var r TestResult
		var testCaseID sql.NullInt64
		var errMsg sql.NullString
		var artifactsJSON string
		err := rows.Scan(&r.ID, &r.RunID, &testCaseID, &r.Title, &r.FilePath, &r.Line, &r.Status,
			&r.DurationMS, &errMsg, &r.Retries, &artifactsJSON)
		if err != nil {
			return nil, fmt.Errorf("store: scan test result: %w", err)
		}
		if testCaseID.Valid {
			r.TestCaseID = &testCaseID.Int64
		}
		r.Error = errMsg.String
		if err := json.Unmarshal([]byte(artifactsJSON), &r.Artifacts); err != nil {
			return nil, fmt.Errorf("store: decode artifacts of result %d: %w", r.ID, err)
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list test results: %w", err)
	}
	return results, nil
}
