// Package runs orchestrates a test run end to end (lifecycle 1 in
// ARCHITECTURE.md): create the run, execute the suite, store results and
// test cases, finish the run and notify.
package runs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ShivaliWaghmare/sentinel/server/internal/runner"
	"github.com/ShivaliWaghmare/sentinel/server/internal/store"
)

// Runner executes a suite. *runner.Runner implements it; tests use a fake.
type Runner interface {
	Run(ctx context.Context, spec runner.Spec) (runner.Report, error)
}

// ActiveRunError is returned by StartRun when the app already has an active
// run. The API answers 409 with its message.
type ActiveRunError struct {
	AppID, RunID int64
}

func (e *ActiveRunError) Error() string {
	return fmt.Sprintf("app %d already has an active run (run %d)", e.AppID, e.RunID)
}

// Service starts runs and executes them in the background.
type Service struct {
	ctx      context.Context // cancelled when the server stops
	store    *store.Store
	runner   Runner
	notifier Notifier
	dataDir  string
	logger   *slog.Logger
	wg       sync.WaitGroup
}

// New returns a Service. Cancelling ctx stops in-flight runs; they finish
// with status error.
func New(ctx context.Context, st *store.Store, r Runner, n Notifier, dataDir string, logger *slog.Logger) *Service {
	return &Service{ctx: ctx, store: st, runner: r, notifier: n, dataDir: dataDir, logger: logger}
}

// Recover marks runs left queued or running by a previous process as error.
// Call it once at boot, before StartRun.
func (s *Service) Recover(ctx context.Context) error {
	n, err := s.store.FailInterruptedRuns(ctx, "server restarted")
	if err != nil {
		return err
	}
	if n > 0 {
		s.logger.Warn("marked interrupted runs as error", "count", n)
	}
	return nil
}

// StartRun creates a running run for the app and executes it in the
// background. It returns store.ErrNotFound for an unknown app and
// *ActiveRunError if the app already has an active run.
func (s *Service) StartRun(ctx context.Context, appID int64, trigger string) (store.Run, error) {
	app, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return store.Run{}, err
	}
	run, err := s.store.CreateRun(ctx, appID, trigger)
	if errors.Is(err, store.ErrActiveRun) {
		active, activeErr := s.store.ActiveRun(ctx, appID)
		if activeErr != nil {
			return store.Run{}, fmt.Errorf("find active run: %w", activeErr)
		}
		return store.Run{}, &ActiveRunError{AppID: appID, RunID: active.ID}
	}
	if err != nil {
		return store.Run{}, err
	}

	s.logger.Info("run started", "run_id", run.ID, "app", app.Slug, "trigger", trigger)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.execute(app, run)
	}()
	return run, nil
}

// Wait blocks until every started run has finished.
func (s *Service) Wait() {
	s.wg.Wait()
}

// RunDir is where a run's report, log and artifacts live.
func RunDir(dataDir string, runID int64) string {
	return filepath.Join(dataDir, "runs", strconv.FormatInt(runID, 10))
}

func (s *Service) execute(app store.App, run store.Run) {
	start := time.Now()
	// Writes after the run must happen even when the server is stopping.
	writeCtx := context.WithoutCancel(s.ctx)

	rep, err := s.runner.Run(s.ctx, runner.Spec{
		Slug:    app.Slug,
		BaseURL: app.BaseURL,
		RunDir:  RunDir(s.dataDir, run.ID),
	})

	var outcome store.RunOutcome
	var results []store.TestResult
	if err == nil {
		results, err = s.storeResults(writeCtx, app, run.ID, rep.Results)
	}
	if err != nil {
		if s.ctx.Err() != nil {
			err = errors.New("server stopped during the run")
		}
		outcome = store.RunOutcome{Status: store.RunError, Error: err.Error()}
	} else {
		outcome = summarize(results)
	}
	outcome.DurationMS = time.Since(start).Milliseconds()

	finished, err := s.store.FinishRun(writeCtx, run.ID, outcome)
	if err != nil {
		s.logger.Error("finish run", "run_id", run.ID, "err", err)
		return
	}
	s.logger.Info("run finished", "run_id", run.ID, "app", app.Slug, "status", finished.Status,
		"total", finished.Total, "failed", finished.Failed, "flaky", finished.Flaky,
		"duration_ms", finished.DurationMS)

	if err := s.notifier.RunFinished(writeCtx, app, finished, results); err != nil {
		s.logger.Error("notify run finished", "run_id", run.ID, "err", err)
	}
}

// storeResults upserts a test case for each result whose origin is known,
// then stores all results linked to their test case.
func (s *Service) storeResults(ctx context.Context, app store.App, runID int64, in []runner.Result) ([]store.TestResult, error) {
	results := make([]store.TestResult, 0, len(in))
	for _, r := range in {
		tr := store.TestResult{
			RunID:      runID,
			Title:      r.Title,
			FilePath:   r.File,
			Line:       r.Line,
			Status:     r.Status,
			DurationMS: r.DurationMS,
			Error:      r.Error,
			Retries:    r.Retries,
			Artifacts:  r.Artifacts,
		}
		if origin := originOf(r.File); origin != "" {
			tc, err := s.store.UpsertTestCase(ctx, store.TestCaseInput{
				AppID: app.ID, FilePath: r.File, Title: r.Title, Origin: origin,
			})
			if err != nil {
				return nil, fmt.Errorf("store test case: %w", err)
			}
			tr.TestCaseID = &tc.ID
		} else {
			s.logger.Warn("test outside manual/ and generated/; not tracked as a test case",
				"run_id", runID, "file", r.File)
		}
		results = append(results, tr)
	}
	if err := s.store.InsertTestResults(ctx, runID, results); err != nil {
		return nil, fmt.Errorf("store test results: %w", err)
	}
	return results, nil
}

// originOf reads the origin from a path like tests/<slug>/manual/x.spec.ts.
// It returns "" for files outside manual/ and generated/.
func originOf(file string) string {
	parts := strings.Split(file, "/")
	if len(parts) < 4 || parts[0] != "tests" {
		return ""
	}
	if store.ValidOrigin(parts[2]) {
		return parts[2]
	}
	return ""
}

// summarize counts results. A run fails if any test failed; flaky tests are
// counted separately but don't fail the run.
func summarize(results []store.TestResult) store.RunOutcome {
	o := store.RunOutcome{Total: len(results)}
	for _, r := range results {
		switch r.Status {
		case store.ResultPassed:
			o.Passed++
		case store.ResultFailed:
			o.Failed++
		case store.ResultFlaky:
			o.Flaky++
		case store.ResultSkipped:
			o.Skipped++
		}
	}
	o.Status = store.RunPassed
	if o.Failed > 0 {
		o.Status = store.RunFailed
	}
	return o
}
