package scheduler

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ShivaliWaghmare/sentinel/server/internal/runner"
	"github.com/ShivaliWaghmare/sentinel/server/internal/runs"
	"github.com/ShivaliWaghmare/sentinel/server/internal/store"
)

func kolkata(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	return loc
}

func TestParse(t *testing.T) {
	tests := []struct {
		spec    string
		wantErr string // "" means valid
	}{
		{"0 9 * * 1,4", ""},
		{"*/2 * * * *", ""},
		{"  30 18 * * MON-FRI  ", ""},
		{"@daily", ""},
		{"@weekly", ""},
		{"@hourly", ""},
		{"", "required"},
		{"   ", "required"},
		{"0 9 * *", "expected exactly 5 fields"},
		{"0 0 9 * * 1", "expected exactly 5 fields"},
		{"60 * * * *", "above maximum"},
		{"0 24 * * *", "above maximum"},
		{"0 9 * * 8", "above maximum"},
		{"abc * * * *", "failed to parse"},
		{"@fortnightly", "unrecognized descriptor"},
		{"@every 1h", "@every is not supported"},
		{"CRON_TZ=UTC 0 9 * * *", "time zone prefixes"},
		{"TZ=UTC", "time zone prefixes"}, // would panic inside robfig's parser
	}
	for _, tt := range tests {
		t.Run(tt.spec, func(t *testing.T) {
			_, err := Parse(tt.spec)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Parse(%q) = %v, want valid", tt.spec, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Parse(%q) err = %v, want it to contain %q", tt.spec, err, tt.wantErr)
			}
		})
	}
}

func TestPreviewInKolkata(t *testing.T) {
	loc := kolkata(t)
	// Tuesday 27 October 2026, 10:00 IST.
	now := time.Date(2026, 10, 27, 10, 0, 0, 0, loc)

	tests := []struct {
		spec string
		want []time.Time
	}{
		{"0 9 * * 1,4", []time.Time{
			time.Date(2026, 10, 29, 9, 0, 0, 0, loc), // Thursday
			time.Date(2026, 11, 2, 9, 0, 0, 0, loc),  // Monday
			time.Date(2026, 11, 5, 9, 0, 0, 0, loc),
			time.Date(2026, 11, 9, 9, 0, 0, 0, loc),
			time.Date(2026, 11, 12, 9, 0, 0, 0, loc),
		}},
		{"*/2 * * * *", []time.Time{
			time.Date(2026, 10, 27, 10, 2, 0, 0, loc),
			time.Date(2026, 10, 27, 10, 4, 0, 0, loc),
			time.Date(2026, 10, 27, 10, 6, 0, 0, loc),
			time.Date(2026, 10, 27, 10, 8, 0, 0, loc),
			time.Date(2026, 10, 27, 10, 10, 0, 0, loc),
		}},
		{"@daily", []time.Time{ // midnight in Kolkata, not UTC
			time.Date(2026, 10, 28, 0, 0, 0, 0, loc),
			time.Date(2026, 10, 29, 0, 0, 0, 0, loc),
			time.Date(2026, 10, 30, 0, 0, 0, 0, loc),
			time.Date(2026, 10, 31, 0, 0, 0, 0, loc),
			time.Date(2026, 11, 1, 0, 0, 0, 0, loc),
		}},
		{"0 0 30 2 *", []time.Time{}}, // never fires
	}
	for _, tt := range tests {
		t.Run(tt.spec, func(t *testing.T) {
			s := New(context.Background(), nil, nil, loc, discardLogger())
			s.now = func() time.Time { return now.UTC() } // the clock's zone must not matter

			got, err := s.Preview(tt.spec, 5)
			if err != nil {
				t.Fatalf("Preview: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d times %v, want %v", len(got), got, tt.want)
			}
			for i := range got {
				if !got[i].Equal(tt.want[i]) || got[i].Location() != loc {
					t.Errorf("time %d = %v, want %v in Asia/Kolkata", i, got[i], tt.want[i])
				}
			}
		})
	}

	s := New(context.Background(), nil, nil, loc, discardLogger())
	if _, err := s.Preview("0 9 * *", 5); err == nil {
		t.Error("Preview of an invalid spec returned no error")
	}
}

// blockingRunner keeps every run active until the test ends.
type blockingRunner struct{}

func (blockingRunner) Run(ctx context.Context, _ runner.Spec) (runner.Report, error) {
	<-ctx.Done()
	return runner.Report{}, ctx.Err()
}

type testEnv struct {
	sched *Scheduler
	store *store.Store
	app   store.App
}

func newTestEnv(t *testing.T) testEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	svc := runs.New(ctx, st, blockingRunner{}, runs.NopNotifier{}, dir, discardLogger())
	t.Cleanup(func() {
		cancel()
		svc.Wait()
		st.Close()
	})
	app, err := st.CreateApp(context.Background(), store.NewApp{Name: "Shop", Slug: "shop", BaseURL: "http://localhost:4000"})
	if err != nil {
		t.Fatalf("CreateApp: %v", err)
	}
	return testEnv{sched: New(ctx, st, svc, kolkata(t), discardLogger()), store: st, app: app}
}

func (e testEnv) createSchedule(t *testing.T, cron string, enabled bool) store.Schedule {
	t.Helper()
	sch, err := e.store.CreateSchedule(context.Background(), e.app.ID, cron, enabled)
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	return sch
}

func (e testEnv) runs(t *testing.T) []store.Run {
	t.Helper()
	list, err := e.store.ListRuns(context.Background(), store.RunFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	return list
}

func TestLoadAddsOnlyEnabledSchedules(t *testing.T) {
	e := newTestEnv(t)
	on := e.createSchedule(t, "0 9 * * 1,4", true)
	off := e.createSchedule(t, "@daily", false)

	if err := e.sched.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := e.sched.NextRuns(on.ID, 2); len(got) != 2 {
		t.Errorf("NextRuns(enabled) = %v, want 2 times", got)
	}
	if got := e.sched.NextRuns(off.ID, 2); got != nil {
		t.Errorf("NextRuns(disabled) = %v, want nil", got)
	}
	if n := len(e.sched.cron.Entries()); n != 1 {
		t.Errorf("cron entries = %d, want 1", n)
	}
}

func TestAddReplacesAndRemoves(t *testing.T) {
	e := newTestEnv(t)
	sch := e.createSchedule(t, "0 9 * * *", true)

	if err := e.sched.Add(sch); err != nil {
		t.Fatalf("Add: %v", err)
	}
	sch.Cron = "0 10 * * *"
	if err := e.sched.Add(sch); err != nil {
		t.Fatalf("Add again: %v", err)
	}
	if n := len(e.sched.cron.Entries()); n != 1 {
		t.Fatalf("cron entries after replace = %d, want 1", n)
	}
	if next := e.sched.NextRuns(sch.ID, 1); len(next) != 1 || next[0].Hour() != 10 {
		t.Errorf("NextRuns after replace = %v, want 10:00", next)
	}

	sch.Enabled = false
	if err := e.sched.Add(sch); err != nil {
		t.Fatalf("Add disabled: %v", err)
	}
	if n := len(e.sched.cron.Entries()); n != 0 {
		t.Errorf("cron entries after disable = %d, want 0", n)
	}

	sch.Enabled, sch.Cron = true, "nope"
	if err := e.sched.Add(sch); err == nil {
		t.Error("Add with invalid cron returned no error")
	}
	e.sched.Remove(12345) // unknown IDs are ignored
}

func TestFireStartsScheduledRun(t *testing.T) {
	e := newTestEnv(t)
	sch := e.createSchedule(t, "*/2 * * * *", true)

	e.sched.fire(sch.ID)

	list := e.runs(t)
	if len(list) != 1 || list[0].Trigger != store.TriggerSchedule || list[0].AppID != e.app.ID {
		t.Fatalf("runs = %+v, want one schedule run for app %d", list, e.app.ID)
	}
}

func TestFireSkipsWhenAppHasActiveRun(t *testing.T) {
	e := newTestEnv(t)
	sch := e.createSchedule(t, "*/2 * * * *", true)

	e.sched.fire(sch.ID) // starts a run that stays active
	e.sched.fire(sch.ID) // must be skipped, not queued
	e.sched.fire(sch.ID)

	if list := e.runs(t); len(list) != 1 {
		t.Fatalf("runs = %d, want 1 (later firings skipped)", len(list))
	}
}

func TestFireRemovesEntryForDisabledOrDeletedSchedule(t *testing.T) {
	ctx := context.Background()
	e := newTestEnv(t)
	disabled := e.createSchedule(t, "*/2 * * * *", true)
	deleted := e.createSchedule(t, "*/2 * * * *", true)
	if err := e.sched.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Change the database behind the scheduler's back, as a race with the API would.
	if _, err := e.store.SetScheduleEnabled(ctx, disabled.ID, false); err != nil {
		t.Fatalf("SetScheduleEnabled: %v", err)
	}
	if err := e.store.DeleteSchedule(ctx, deleted.ID); err != nil {
		t.Fatalf("DeleteSchedule: %v", err)
	}

	e.sched.fire(disabled.ID)
	e.sched.fire(deleted.ID)

	if list := e.runs(t); len(list) != 0 {
		t.Errorf("runs = %d, want 0", len(list))
	}
	if n := len(e.sched.cron.Entries()); n != 0 {
		t.Errorf("cron entries = %d, want 0", n)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
