package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Agent run kinds and statuses (CHECK constraints in 0001_init.sql).
const (
	AgentProbe   = "probe"
	AgentExplore = "explore"
	AgentTriage  = "triage"
	AgentHeal    = "heal"

	AgentQueued         = "queued"
	AgentRunning        = "running"
	AgentSucceeded      = "succeeded"
	AgentFailed         = "failed"
	AgentBudgetExceeded = "budget_exceeded"
	AgentCancelled      = "cancelled"
)

// ValidAgentKind reports whether kind is a known agent kind.
func ValidAgentKind(kind string) bool {
	switch kind {
	case AgentProbe, AgentExplore, AgentTriage, AgentHeal:
		return true
	}
	return false
}

// FinalAgentStatus reports whether status ends an agent run (the statuses a
// done event may carry).
func FinalAgentStatus(status string) bool {
	switch status {
	case AgentSucceeded, AgentFailed, AgentBudgetExceeded, AgentCancelled:
		return true
	}
	return false
}

// AgentRun is one execution of an agent CLI.
type AgentRun struct {
	ID          int64
	AppID       int64
	Kind        string
	Status      string
	InputJSON   string // the kind-specific input, a JSON object
	SummaryJSON string // the done event's summary; empty until finished
	Steps       int
	TokensIn    int64
	TokensOut   int64
	CostUSD     float64
	StartedAt   *time.Time
	FinishedAt  *time.Time
	Error       string
}

// Finished reports whether the run has a final status.
func (r AgentRun) Finished() bool {
	return FinalAgentStatus(r.Status)
}

// AgentEvent is one stored protocol event. Seq is assigned by the server
// and is the SSE event ID.
type AgentEvent struct {
	AgentRunID  int64
	Seq         int64
	Type        string
	PayloadJSON string // the event's data object
	CreatedAt   time.Time
}

// Usage is what one event adds to its run's counters (llm_call events).
type Usage struct {
	Steps     int
	TokensIn  int64
	TokensOut int64
	CostUSD   float64
}

// AgentRunFilter narrows ListAgentRuns. Zero values match everything.
type AgentRunFilter struct {
	AppID int64
	Kind  string
	Limit int
}

const agentRunColumns = `id, app_id, kind, status, input_json, summary_json, steps,
	tokens_in, tokens_out, cost_usd, started_at, finished_at, error`

// CreateAgentRun inserts a run with status queued or running. A running run
// gets its started_at now.
func (s *Store) CreateAgentRun(ctx context.Context, appID int64, kind, status, inputJSON string) (AgentRun, error) {
	var startedAt any
	if status == AgentRunning {
		startedAt = nowMillis()
	}
	row := s.db.QueryRowContext(ctx,
		`INSERT INTO agent_runs (app_id, kind, status, input_json, started_at)
		 VALUES (?, ?, ?, ?, ?)
		 RETURNING `+agentRunColumns,
		appID, kind, status, inputJSON, startedAt)
	run, err := scanAgentRun(row)
	if err != nil {
		return AgentRun{}, fmt.Errorf("store: create agent run: %w", err)
	}
	return run, nil
}

// StartQueuedAgentRun moves a queued run to running. It returns ErrNotFound
// if the run is no longer queued (for example, it was cancelled).
func (s *Store) StartQueuedAgentRun(ctx context.Context, id int64) (AgentRun, error) {
	row := s.db.QueryRowContext(ctx,
		`UPDATE agent_runs SET status = 'running', started_at = ?
		 WHERE id = ? AND status = 'queued'
		 RETURNING `+agentRunColumns, nowMillis(), id)
	return scanAgentRun(row)
}

// OldestQueuedAgentRun returns the queued run that has waited longest, or
// ErrNotFound.
func (s *Store) OldestQueuedAgentRun(ctx context.Context) (AgentRun, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+agentRunColumns+` FROM agent_runs WHERE status = 'queued' ORDER BY id LIMIT 1`)
	return scanAgentRun(row)
}

// FinishAgentRun writes the final status, summary and error of a run that
// is still queued or running. It returns ErrNotFound if the run doesn't
// exist or already finished, so a run is finished exactly once.
func (s *Store) FinishAgentRun(ctx context.Context, id int64, status, summaryJSON, errMsg string) (AgentRun, error) {
	row := s.db.QueryRowContext(ctx,
		`UPDATE agent_runs SET status = ?, summary_json = ?, error = ?, finished_at = ?
		 WHERE id = ? AND status IN ('queued', 'running')
		 RETURNING `+agentRunColumns,
		status, nullIfEmpty(summaryJSON), nullIfEmpty(errMsg), nowMillis(), id)
	return scanAgentRun(row)
}

// FailInterruptedAgentRuns marks every queued or running agent run as
// failed with msg. Called at boot, like FailInterruptedRuns.
func (s *Store) FailInterruptedAgentRuns(ctx context.Context, msg string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE agent_runs SET status = 'failed', error = ?, finished_at = ?
		 WHERE status IN ('queued', 'running')`, msg, nowMillis())
	if err != nil {
		return 0, fmt.Errorf("store: fail interrupted agent runs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: fail interrupted agent runs: %w", err)
	}
	return n, nil
}

// GetAgentRun returns the run with the given ID or ErrNotFound.
func (s *Store) GetAgentRun(ctx context.Context, id int64) (AgentRun, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+agentRunColumns+` FROM agent_runs WHERE id = ?`, id)
	return scanAgentRun(row)
}

// ListAgentRuns returns runs newest first.
func (s *Store) ListAgentRuns(ctx context.Context, f AgentRunFilter) ([]AgentRun, error) {
	query := `SELECT ` + agentRunColumns + ` FROM agent_runs WHERE 1 = 1`
	args := []any{}
	if f.AppID != 0 {
		query += ` AND app_id = ?`
		args = append(args, f.AppID)
	}
	if f.Kind != "" {
		query += ` AND kind = ?`
		args = append(args, f.Kind)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, f.Limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list agent runs: %w", err)
	}
	defer rows.Close()

	list := []AgentRun{}
	for rows.Next() {
		run, err := scanAgentRun(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list agent runs: %w", err)
	}
	return list, nil
}

// AppendAgentEvent stores an event and adds its usage to the run's counters
// in one transaction, so the counters always match the stored events.
func (s *Store) AppendAgentEvent(ctx context.Context, ev AgentEvent, u Usage) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: append agent event: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO agent_events (agent_run_id, seq, type, payload_json, created_at) VALUES (?, ?, ?, ?, ?)`,
		ev.AgentRunID, ev.Seq, ev.Type, ev.PayloadJSON, ev.CreatedAt.UnixMilli()); err != nil {
		return fmt.Errorf("store: insert agent event: %w", err)
	}
	if u != (Usage{}) {
		if _, err := tx.ExecContext(ctx,
			`UPDATE agent_runs SET steps = steps + ?, tokens_in = tokens_in + ?,
				tokens_out = tokens_out + ?, cost_usd = cost_usd + ?
			 WHERE id = ?`,
			u.Steps, u.TokensIn, u.TokensOut, u.CostUSD, ev.AgentRunID); err != nil {
			return fmt.Errorf("store: update agent run usage: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: append agent event: %w", err)
	}
	return nil
}

// AppendDoneEvent stores a run's done event and finishes the run (status,
// summary, error) in one transaction. Whoever reads a done event can
// therefore rely on the run already showing its final status. It returns
// ErrNotFound, and stores nothing, if the run already finished.
func (s *Store) AppendDoneEvent(ctx context.Context, ev AgentEvent, status, summaryJSON, errMsg string) (AgentRun, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentRun{}, fmt.Errorf("store: append done event: %w", err)
	}
	defer tx.Rollback()

	row := tx.QueryRowContext(ctx,
		`UPDATE agent_runs SET status = ?, summary_json = ?, error = ?, finished_at = ?
		 WHERE id = ? AND status IN ('queued', 'running')
		 RETURNING `+agentRunColumns,
		status, nullIfEmpty(summaryJSON), nullIfEmpty(errMsg), nowMillis(), ev.AgentRunID)
	run, err := scanAgentRun(row)
	if err != nil {
		return AgentRun{}, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO agent_events (agent_run_id, seq, type, payload_json, created_at) VALUES (?, ?, ?, ?, ?)`,
		ev.AgentRunID, ev.Seq, ev.Type, ev.PayloadJSON, ev.CreatedAt.UnixMilli()); err != nil {
		return AgentRun{}, fmt.Errorf("store: insert done event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return AgentRun{}, fmt.Errorf("store: append done event: %w", err)
	}
	return run, nil
}

// ListAgentEvents returns a run's events with seq > afterSeq, in order.
func (s *Store) ListAgentEvents(ctx context.Context, runID, afterSeq int64) ([]AgentEvent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT agent_run_id, seq, type, payload_json, created_at FROM agent_events
		 WHERE agent_run_id = ? AND seq > ? ORDER BY seq`, runID, afterSeq)
	if err != nil {
		return nil, fmt.Errorf("store: list agent events: %w", err)
	}
	defer rows.Close()

	list := []AgentEvent{}
	for rows.Next() {
		var ev AgentEvent
		var created int64
		if err := rows.Scan(&ev.AgentRunID, &ev.Seq, &ev.Type, &ev.PayloadJSON, &created); err != nil {
			return nil, fmt.Errorf("store: scan agent event: %w", err)
		}
		ev.CreatedAt = fromMillis(created)
		list = append(list, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list agent events: %w", err)
	}
	return list, nil
}

func scanAgentRun(sc scanner) (AgentRun, error) {
	var r AgentRun
	var summary, errMsg sql.NullString
	var startedAt, finishedAt sql.NullInt64
	err := sc.Scan(&r.ID, &r.AppID, &r.Kind, &r.Status, &r.InputJSON, &summary, &r.Steps,
		&r.TokensIn, &r.TokensOut, &r.CostUSD, &startedAt, &finishedAt, &errMsg)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentRun{}, fmt.Errorf("store: agent run: %w", ErrNotFound)
	}
	if err != nil {
		return AgentRun{}, fmt.Errorf("store: scan agent run: %w", err)
	}
	r.SummaryJSON = summary.String
	r.Error = errMsg.String
	r.StartedAt = optionalTime(startedAt)
	r.FinishedAt = optionalTime(finishedAt)
	return r, nil
}
