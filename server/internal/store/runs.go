package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrActiveRun is returned by CreateRun when the app already has a queued or
// running run. At most one run per app is active (ARCHITECTURE.md).
var ErrActiveRun = errors.New("app already has an active run")

// Run triggers and statuses (CHECK constraints in 0001_init.sql).
const (
	TriggerManual   = "manual"
	TriggerSchedule = "schedule"
	TriggerEval     = "eval"
	TriggerHeal     = "heal"

	RunQueued  = "queued"
	RunRunning = "running"
	RunPassed  = "passed"
	RunFailed  = "failed"
	RunError   = "error"
)

// Run is one execution of an app's Playwright suite.
type Run struct {
	ID         int64
	AppID      int64
	Trigger    string
	Status     string
	Total      int
	Passed     int
	Failed     int
	Flaky      int
	Skipped    int
	DurationMS int64
	Error      string // empty when there is none
	StartedAt  *time.Time
	FinishedAt *time.Time
}

// RunOutcome holds the final values FinishRun writes.
type RunOutcome struct {
	Status     string
	Total      int
	Passed     int
	Failed     int
	Flaky      int
	Skipped    int
	DurationMS int64
	Error      string
}

// RunFilter narrows ListRuns. AppID 0 matches every app.
type RunFilter struct {
	AppID int64
	Limit int
}

const runColumns = `id, app_id, trigger, status, total, passed, failed, flaky, skipped,
	duration_ms, error, started_at, finished_at`

// CreateRun inserts a run with status running, unless the app already has an
// active run, in which case it returns ErrActiveRun. The check and the insert
// are one statement, so two concurrent callers can't both succeed.
func (s *Store) CreateRun(ctx context.Context, appID int64, trigger string) (Run, error) {
	row := s.db.QueryRowContext(ctx,
		`INSERT INTO runs (app_id, trigger, status, started_at)
		 SELECT ?, ?, 'running', ?
		 WHERE NOT EXISTS (
			SELECT 1 FROM runs WHERE app_id = ? AND status IN ('queued', 'running')
		 )
		 RETURNING `+runColumns,
		appID, trigger, nowMillis(), appID)
	run, err := scanRun(row)
	if errors.Is(err, ErrNotFound) {
		// No row inserted: the NOT EXISTS guard matched.
		return Run{}, fmt.Errorf("store: create run for app %d: %w", appID, ErrActiveRun)
	}
	if err != nil {
		return Run{}, fmt.Errorf("store: create run: %w", err)
	}
	return run, nil
}

// ActiveRun returns the app's queued or running run, or ErrNotFound.
func (s *Store) ActiveRun(ctx context.Context, appID int64) (Run, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+runColumns+` FROM runs
		 WHERE app_id = ? AND status IN ('queued', 'running')
		 ORDER BY id DESC LIMIT 1`, appID)
	return scanRun(row)
}

// FinishRun writes a run's final status, counts and finish time.
func (s *Store) FinishRun(ctx context.Context, id int64, o RunOutcome) (Run, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE runs SET status = ?, total = ?, passed = ?, failed = ?, flaky = ?, skipped = ?,
			duration_ms = ?, error = ?, finished_at = ?
		 WHERE id = ?`,
		o.Status, o.Total, o.Passed, o.Failed, o.Flaky, o.Skipped,
		o.DurationMS, nullIfEmpty(o.Error), nowMillis(), id)
	if err != nil {
		return Run{}, fmt.Errorf("store: finish run %d: %w", id, err)
	}
	if err := requireOneRow(res, "finish run", id); err != nil {
		return Run{}, err
	}
	return s.GetRun(ctx, id)
}

// FailInterruptedRuns marks every queued or running run as error with msg.
// Called at boot: no run can still be in progress after a restart.
func (s *Store) FailInterruptedRuns(ctx context.Context, msg string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE runs SET status = 'error', error = ?, finished_at = ?
		 WHERE status IN ('queued', 'running')`, msg, nowMillis())
	if err != nil {
		return 0, fmt.Errorf("store: fail interrupted runs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: fail interrupted runs: %w", err)
	}
	return n, nil
}

// GetRun returns the run with the given ID or ErrNotFound.
func (s *Store) GetRun(ctx context.Context, id int64) (Run, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id = ?`, id)
	return scanRun(row)
}

// ListRuns returns runs newest first.
func (s *Store) ListRuns(ctx context.Context, f RunFilter) ([]Run, error) {
	query := `SELECT ` + runColumns + ` FROM runs`
	args := []any{}
	if f.AppID != 0 {
		query += ` WHERE app_id = ?`
		args = append(args, f.AppID)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, f.Limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list runs: %w", err)
	}
	defer rows.Close()

	runs := []Run{}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list runs: %w", err)
	}
	return runs, nil
}

func scanRun(sc scanner) (Run, error) {
	var r Run
	var errMsg sql.NullString
	var startedAt, finishedAt sql.NullInt64
	err := sc.Scan(&r.ID, &r.AppID, &r.Trigger, &r.Status, &r.Total, &r.Passed, &r.Failed,
		&r.Flaky, &r.Skipped, &r.DurationMS, &errMsg, &startedAt, &finishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, fmt.Errorf("store: run: %w", ErrNotFound)
	}
	if err != nil {
		return Run{}, fmt.Errorf("store: scan run: %w", err)
	}
	r.Error = errMsg.String
	r.StartedAt = optionalTime(startedAt)
	r.FinishedAt = optionalTime(finishedAt)
	return r, nil
}

func optionalTime(ms sql.NullInt64) *time.Time {
	if !ms.Valid {
		return nil
	}
	t := fromMillis(ms.Int64)
	return &t
}

// nullIfEmpty stores "" as NULL for optional text columns.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
