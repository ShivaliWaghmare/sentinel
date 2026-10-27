package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Schedule starts runs of an app's suite on a cron expression.
type Schedule struct {
	ID        int64
	AppID     int64
	Cron      string
	Enabled   bool
	CreatedAt time.Time
}

const scheduleColumns = `id, app_id, cron, enabled, created_at`

// CreateSchedule inserts a schedule. It returns ErrNotFound if the app does
// not exist. The cron expression must be validated by the caller.
func (s *Store) CreateSchedule(ctx context.Context, appID int64, cron string, enabled bool) (Schedule, error) {
	row := s.db.QueryRowContext(ctx,
		`INSERT INTO schedules (app_id, cron, enabled, created_at) VALUES (?, ?, ?, ?)
		 RETURNING `+scheduleColumns,
		appID, cron, boolToInt(enabled), nowMillis())
	sch, err := scanSchedule(row)
	if isForeignKeyViolation(err) {
		return Schedule{}, fmt.Errorf("store: create schedule for app %d: app: %w", appID, ErrNotFound)
	}
	if err != nil {
		return Schedule{}, fmt.Errorf("store: create schedule: %w", err)
	}
	return sch, nil
}

// GetSchedule returns the schedule with the given ID or ErrNotFound.
func (s *Store) GetSchedule(ctx context.Context, id int64) (Schedule, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE id = ?`, id)
	return scanSchedule(row)
}

// ListSchedules returns schedules ordered by ID. appID 0 matches every app.
func (s *Store) ListSchedules(ctx context.Context, appID int64) ([]Schedule, error) {
	query := `SELECT ` + scheduleColumns + ` FROM schedules`
	args := []any{}
	if appID != 0 {
		query += ` WHERE app_id = ?`
		args = append(args, appID)
	}
	return s.querySchedules(ctx, query+` ORDER BY id`, args...)
}

// ListEnabledSchedules returns every enabled schedule; the scheduler loads
// them at boot.
func (s *Store) ListEnabledSchedules(ctx context.Context) ([]Schedule, error) {
	return s.querySchedules(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE enabled = 1 ORDER BY id`)
}

// SetScheduleEnabled turns a schedule on or off and returns it.
func (s *Store) SetScheduleEnabled(ctx context.Context, id int64, enabled bool) (Schedule, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE schedules SET enabled = ? WHERE id = ?`, boolToInt(enabled), id)
	if err != nil {
		return Schedule{}, fmt.Errorf("store: update schedule %d: %w", id, err)
	}
	if err := requireOneRow(res, "update schedule", id); err != nil {
		return Schedule{}, err
	}
	return s.GetSchedule(ctx, id)
}

// DeleteSchedule deletes a schedule or returns ErrNotFound.
func (s *Store) DeleteSchedule(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM schedules WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete schedule %d: %w", id, err)
	}
	return requireOneRow(res, "delete schedule", id)
}

func (s *Store) querySchedules(ctx context.Context, query string, args ...any) ([]Schedule, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list schedules: %w", err)
	}
	defer rows.Close()

	list := []Schedule{}
	for rows.Next() {
		sch, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, sch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list schedules: %w", err)
	}
	return list, nil
}

func scanSchedule(sc scanner) (Schedule, error) {
	var sch Schedule
	var enabled int
	var createdAt int64
	err := sc.Scan(&sch.ID, &sch.AppID, &sch.Cron, &enabled, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Schedule{}, fmt.Errorf("store: schedule: %w", ErrNotFound)
	}
	if err != nil {
		return Schedule{}, fmt.Errorf("store: scan schedule: %w", err)
	}
	sch.Enabled = enabled == 1
	sch.CreatedAt = fromMillis(createdAt)
	return sch, nil
}

// isForeignKeyViolation reports whether err is a SQLite FOREIGN KEY error.
func isForeignKeyViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "FOREIGN KEY constraint failed")
}
