package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAgentRunLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	app := createTestApp(t, s, "shoplite")

	running, err := s.CreateAgentRun(ctx, app.ID, AgentProbe, AgentRunning, `{}`)
	if err != nil {
		t.Fatalf("CreateAgentRun: %v", err)
	}
	if running.Status != AgentRunning || running.StartedAt == nil {
		t.Errorf("running run = %+v, want status running with started_at", running)
	}
	queued, err := s.CreateAgentRun(ctx, app.ID, AgentProbe, AgentQueued, `{"x":1}`)
	if err != nil {
		t.Fatalf("CreateAgentRun queued: %v", err)
	}
	if queued.StartedAt != nil {
		t.Errorf("queued run has started_at %v", queued.StartedAt)
	}

	oldest, err := s.OldestQueuedAgentRun(ctx)
	if err != nil || oldest.ID != queued.ID {
		t.Fatalf("OldestQueuedAgentRun = %d, %v; want %d", oldest.ID, err, queued.ID)
	}
	started, err := s.StartQueuedAgentRun(ctx, queued.ID)
	if err != nil || started.Status != AgentRunning || started.StartedAt == nil {
		t.Fatalf("StartQueuedAgentRun = %+v, %v", started, err)
	}
	if _, err := s.StartQueuedAgentRun(ctx, queued.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second StartQueuedAgentRun err = %v, want ErrNotFound", err)
	}

	finished, err := s.FinishAgentRun(ctx, running.ID, AgentSucceeded, `{"ok":true}`, "")
	if err != nil {
		t.Fatalf("FinishAgentRun: %v", err)
	}
	if finished.Status != AgentSucceeded || finished.SummaryJSON != `{"ok":true}` || finished.FinishedAt == nil {
		t.Errorf("finished = %+v", finished)
	}
	if _, err := s.FinishAgentRun(ctx, running.ID, AgentFailed, "", "again"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second FinishAgentRun err = %v, want ErrNotFound", err)
	}
}

func TestAppendAgentEventUpdatesUsage(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	app := createTestApp(t, s, "shoplite")
	run, err := s.CreateAgentRun(ctx, app.ID, AgentProbe, AgentRunning, `{}`)
	if err != nil {
		t.Fatalf("CreateAgentRun: %v", err)
	}

	now := time.Now()
	events := []struct {
		typ   string
		usage Usage
	}{
		{"run_started", Usage{}},
		{"llm_call", Usage{Steps: 1, TokensIn: 100, TokensOut: 10, CostUSD: 0.25}},
		{"llm_call", Usage{Steps: 1, TokensIn: 50, TokensOut: 5, CostUSD: 0.5}},
	}
	for i, e := range events {
		ev := AgentEvent{AgentRunID: run.ID, Seq: int64(i + 1), Type: e.typ, PayloadJSON: `{}`, CreatedAt: now}
		if err := s.AppendAgentEvent(ctx, ev, e.usage); err != nil {
			t.Fatalf("AppendAgentEvent %d: %v", i, err)
		}
	}
	dup := AgentEvent{AgentRunID: run.ID, Seq: 2, Type: "warning", PayloadJSON: `{}`, CreatedAt: now}
	if err := s.AppendAgentEvent(ctx, dup, Usage{Steps: 1}); err == nil {
		t.Error("duplicate seq accepted")
	}

	got, err := s.GetAgentRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetAgentRun: %v", err)
	}
	// The failed duplicate must not have changed the counters.
	if got.Steps != 2 || got.TokensIn != 150 || got.TokensOut != 15 || got.CostUSD != 0.75 {
		t.Errorf("usage = steps %d, in %d, out %d, cost %v", got.Steps, got.TokensIn, got.TokensOut, got.CostUSD)
	}

	after, err := s.ListAgentEvents(ctx, run.ID, 1)
	if err != nil {
		t.Fatalf("ListAgentEvents: %v", err)
	}
	if len(after) != 2 || after[0].Seq != 2 || after[1].Seq != 3 {
		t.Errorf("events after 1 = %+v", after)
	}
}

func TestAppendDoneEventFinishesRunOnce(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	app := createTestApp(t, s, "shoplite")
	run, err := s.CreateAgentRun(ctx, app.ID, AgentProbe, AgentRunning, `{}`)
	if err != nil {
		t.Fatalf("CreateAgentRun: %v", err)
	}

	done := AgentEvent{AgentRunID: run.ID, Seq: 1, Type: "done", PayloadJSON: `{"status":"failed"}`, CreatedAt: time.Now()}
	got, err := s.AppendDoneEvent(ctx, done, AgentFailed, `{"usage":{}}`, "quota exceeded")
	if err != nil {
		t.Fatalf("AppendDoneEvent: %v", err)
	}
	if got.Status != AgentFailed || got.Error != "quota exceeded" || got.SummaryJSON != `{"usage":{}}` || got.FinishedAt == nil {
		t.Errorf("finished run = %+v", got)
	}

	// A second done neither changes the run nor stores an event.
	again := AgentEvent{AgentRunID: run.ID, Seq: 2, Type: "done", PayloadJSON: `{}`, CreatedAt: time.Now()}
	if _, err := s.AppendDoneEvent(ctx, again, AgentSucceeded, `{}`, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("second AppendDoneEvent err = %v, want ErrNotFound", err)
	}
	evs, err := s.ListAgentEvents(ctx, run.ID, 0)
	if err != nil || len(evs) != 1 {
		t.Errorf("events = %+v, %v; want only the first done", evs, err)
	}
}

func TestListAgentRunsAndFailInterrupted(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	app := createTestApp(t, s, "shoplite")
	other := createTestApp(t, s, "other")

	mustCreate := func(appID int64, kind, status string) AgentRun {
		t.Helper()
		r, err := s.CreateAgentRun(ctx, appID, kind, status, `{}`)
		if err != nil {
			t.Fatalf("CreateAgentRun: %v", err)
		}
		return r
	}
	a := mustCreate(app.ID, AgentProbe, AgentRunning)
	b := mustCreate(app.ID, AgentExplore, AgentQueued)
	c := mustCreate(other.ID, AgentProbe, AgentRunning)
	if _, err := s.FinishAgentRun(ctx, c.ID, AgentSucceeded, `{}`, ""); err != nil {
		t.Fatalf("FinishAgentRun: %v", err)
	}

	tests := []struct {
		name   string
		filter AgentRunFilter
		want   []int64
	}{
		{"all, newest first", AgentRunFilter{Limit: 10}, []int64{c.ID, b.ID, a.ID}},
		{"by app", AgentRunFilter{AppID: app.ID, Limit: 10}, []int64{b.ID, a.ID}},
		{"by kind", AgentRunFilter{Kind: AgentProbe, Limit: 10}, []int64{c.ID, a.ID}},
		{"limit", AgentRunFilter{Limit: 1}, []int64{c.ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			list, err := s.ListAgentRuns(ctx, tt.filter)
			if err != nil {
				t.Fatalf("ListAgentRuns: %v", err)
			}
			var ids []int64
			for _, r := range list {
				ids = append(ids, r.ID)
			}
			if len(ids) != len(tt.want) {
				t.Fatalf("ids = %v, want %v", ids, tt.want)
			}
			for i := range ids {
				if ids[i] != tt.want[i] {
					t.Fatalf("ids = %v, want %v", ids, tt.want)
				}
			}
		})
	}

	n, err := s.FailInterruptedAgentRuns(ctx, "server restarted")
	if err != nil || n != 2 {
		t.Fatalf("FailInterruptedAgentRuns = %d, %v; want 2", n, err)
	}
	got, _ := s.GetAgentRun(ctx, b.ID)
	if got.Status != AgentFailed || got.Error != "server restarted" {
		t.Errorf("queued run after recovery = %+v", got)
	}
}
