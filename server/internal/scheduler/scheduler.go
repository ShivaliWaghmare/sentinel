// Package scheduler starts runs on cron schedules (ADR-0007). Every schedule
// fires in the TIMEZONE location; a firing for an app that already has an
// active run is skipped, never queued.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/ShivaliWaghmare/sentinel/server/internal/runs"
	"github.com/ShivaliWaghmare/sentinel/server/internal/store"
)

// parser accepts standard 5-field cron plus descriptors like @daily.
var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// Parse validates a cron expression. Besides robfig's own rules it rejects
// @every (not tied to the wall clock) and TZ=/CRON_TZ= prefixes (every
// schedule uses TIMEZONE). Errors are readable and safe to show to users.
func Parse(spec string) (cron.Schedule, error) {
	spec = strings.TrimSpace(spec)
	switch {
	case spec == "":
		return nil, errors.New("cron expression is required")
	case strings.HasPrefix(spec, "@every"):
		return nil, errors.New("@every is not supported; use a 5-field expression such as */15 * * * *")
	case strings.HasPrefix(spec, "TZ=") || strings.HasPrefix(spec, "CRON_TZ="):
		// Checked before robfig's parser, which also panics on "TZ=x" without a space.
		return nil, errors.New("time zone prefixes are not supported; schedules use the server's TIMEZONE")
	}
	sched, err := parser.Parse(spec)
	if err != nil {
		return nil, err
	}
	return sched, nil
}

// RunStarter starts test runs. *runs.Service implements it.
type RunStarter interface {
	StartRun(ctx context.Context, appID int64, trigger string) (store.Run, error)
}

type entry struct {
	id       cron.EntryID
	schedule cron.Schedule
}

// Scheduler keeps one cron entry per enabled schedule.
type Scheduler struct {
	ctx    context.Context // cancelled when the server stops
	store  *store.Store
	runs   RunStarter
	loc    *time.Location
	cron   *cron.Cron
	logger *slog.Logger
	now    func() time.Time // replaced in tests

	mu      sync.Mutex
	entries map[int64]entry // by schedule ID
}

// New returns a stopped Scheduler that fires in loc. Call Load, then Start.
func New(ctx context.Context, st *store.Store, rs RunStarter, loc *time.Location, logger *slog.Logger) *Scheduler {
	return &Scheduler{
		ctx:     ctx,
		store:   st,
		runs:    rs,
		loc:     loc,
		cron:    cron.New(cron.WithLocation(loc), cron.WithParser(parser)),
		logger:  logger,
		now:     time.Now,
		entries: map[int64]entry{},
	}
}

// Location is the time zone schedules fire in.
func (s *Scheduler) Location() *time.Location {
	return s.loc
}

// Load adds every enabled schedule from the database. A stored expression
// that no longer parses is logged and skipped rather than stopping the boot.
func (s *Scheduler) Load(ctx context.Context) error {
	list, err := s.store.ListEnabledSchedules(ctx)
	if err != nil {
		return fmt.Errorf("scheduler: load: %w", err)
	}
	for _, sch := range list {
		if err := s.Add(sch); err != nil {
			s.logger.Error("schedule not loaded", "schedule_id", sch.ID, "cron", sch.Cron, "err", err)
		}
	}
	s.logger.Info("schedules loaded", "count", len(s.entries), "timezone", s.loc.String())
	return nil
}

// Start begins firing entries in the background.
func (s *Scheduler) Start() {
	s.cron.Start()
}

// Stop stops firing and waits for firings in progress. A firing only starts
// a run, so this returns quickly; the run itself is owned by runs.Service.
func (s *Scheduler) Stop() {
	<-s.cron.Stop().Done()
}

// Add creates or replaces the cron entry for sch. A disabled schedule is
// removed instead.
func (s *Scheduler) Add(sch store.Schedule) error {
	if !sch.Enabled {
		s.Remove(sch.ID)
		return nil
	}
	parsed, err := Parse(sch.Cron)
	if err != nil {
		return fmt.Errorf("scheduler: schedule %d: %w", sch.ID, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.entries[sch.ID]; ok {
		s.cron.Remove(old.id)
	}
	id := sch.ID
	entryID := s.cron.Schedule(parsed, cron.FuncJob(func() { s.fire(id) }))
	s.entries[sch.ID] = entry{id: entryID, schedule: parsed}
	return nil
}

// Remove deletes the cron entry for a schedule, if there is one.
func (s *Scheduler) Remove(scheduleID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[scheduleID]; ok {
		s.cron.Remove(e.id)
		delete(s.entries, scheduleID)
	}
}

// NextRuns returns the next n fire times of a loaded schedule, in the
// scheduler's location. It returns nil if the schedule is not loaded
// (disabled or unknown).
func (s *Scheduler) NextRuns(scheduleID int64, n int) []time.Time {
	s.mu.Lock()
	e, ok := s.entries[scheduleID]
	s.mu.Unlock()
	if !ok {
		return nil
	}
	return s.next(e.schedule, n)
}

// Preview validates spec and returns its next n fire times without adding it.
func (s *Scheduler) Preview(spec string, n int) ([]time.Time, error) {
	parsed, err := Parse(spec)
	if err != nil {
		return nil, err
	}
	return s.next(parsed, n), nil
}

// next lists fire times after now. Schedules without a time zone are
// evaluated in the location of the time they are given.
func (s *Scheduler) next(sched cron.Schedule, n int) []time.Time {
	times := make([]time.Time, 0, n)
	t := s.now().In(s.loc)
	for range n {
		t = sched.Next(t)
		if t.IsZero() { // e.g. "0 0 30 2 *": February 30th never comes
			break
		}
		times = append(times, t)
	}
	return times
}

// fire starts a scheduled run. It re-reads the schedule so a firing that
// races with a delete or disable does nothing, and it skips (never queues)
// when the app already has an active run.
func (s *Scheduler) fire(scheduleID int64) {
	sch, err := s.store.GetSchedule(s.ctx, scheduleID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !sch.Enabled) {
		s.logger.Info("schedule gone or disabled; entry removed", "schedule_id", scheduleID)
		s.Remove(scheduleID)
		return
	}
	if err != nil {
		s.logger.Error("scheduled run not started", "schedule_id", scheduleID, "err", err)
		return
	}

	run, err := s.runs.StartRun(s.ctx, sch.AppID, store.TriggerSchedule)
	var active *runs.ActiveRunError
	switch {
	case errors.As(err, &active):
		s.logger.Warn("scheduled run skipped: app already has an active run",
			"schedule_id", sch.ID, "app_id", sch.AppID, "active_run_id", active.RunID)
	case errors.Is(err, store.ErrNotFound):
		s.logger.Info("schedule's app is gone; entry removed", "schedule_id", sch.ID, "app_id", sch.AppID)
		s.Remove(sch.ID)
	case err != nil:
		s.logger.Error("scheduled run not started", "schedule_id", sch.ID, "app_id", sch.AppID, "err", err)
	default:
		s.logger.Info("scheduled run started", "schedule_id", sch.ID, "app_id", sch.AppID, "run_id", run.ID)
	}
}
