package runs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/ShivaliWaghmare/sentinel/server/internal/runner"
	"github.com/ShivaliWaghmare/sentinel/server/internal/store"
)

// fakeRunner returns a fixed report or error. If release is set, Run blocks
// until it is closed (or ctx is cancelled), so a run can be kept active.
type fakeRunner struct {
	report  runner.Report
	err     error
	release chan struct{}

	mu    sync.Mutex
	specs []runner.Spec
}

func (f *fakeRunner) Run(ctx context.Context, spec runner.Spec) (runner.Report, error) {
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	f.mu.Unlock()
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return runner.Report{}, ctx.Err()
		}
	}
	return f.report, f.err
}

// recordingNotifier remembers the runs it was told about.
type recordingNotifier struct {
	mu   sync.Mutex
	runs []store.Run
	n    []int // number of results per run
}

func (r *recordingNotifier) RunFinished(_ context.Context, _ store.App, run store.Run, results []store.TestResult) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs = append(r.runs, run)
	r.n = append(r.n, len(results))
	return nil
}

type env struct {
	svc      *Service
	store    *store.Store
	runner   *fakeRunner
	notifier *recordingNotifier
	app      store.App
	dataDir  string
}

func newEnv(t *testing.T, ctx context.Context, fr *fakeRunner) env {
	t.Helper()
	dataDir := t.TempDir()
	st, err := store.Open(context.Background(), dataDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	app, err := st.CreateApp(context.Background(), store.NewApp{
		Name: "ShopLite", Slug: "shoplite", BaseURL: "http://localhost:4000",
	})
	if err != nil {
		t.Fatalf("CreateApp: %v", err)
	}
	n := &recordingNotifier{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := New(ctx, st, fr, n, dataDir, logger)
	return env{svc: svc, store: st, runner: fr, notifier: n, app: app, dataDir: dataDir}
}

func sampleReport() runner.Report {
	return runner.Report{Results: []runner.Result{
		{Title: "logs in", File: "tests/shoplite/manual/auth.spec.ts", Line: 4, Status: "passed", DurationMS: 100},
		{Title: "badge", File: "tests/shoplite/manual/cart.spec.ts", Line: 4, Status: "failed", DurationMS: 200,
			Error: "Error: expected 1", Retries: 1,
			Artifacts: map[string]string{"screenshot": "runs/1/artifacts/x/test-failed-1.png"}},
		{Title: "Search › finds mugs", File: "tests/shoplite/generated/search.spec.ts", Line: 9, Status: "flaky", Retries: 1},
		{Title: "skipped one", File: "tests/shoplite/manual/auth.spec.ts", Line: 30, Status: "skipped"},
		{Title: "stray", File: "tests/shoplite/other.spec.ts", Line: 1, Status: "passed"},
	}}
}

func TestStartRunStoresResultsAndTestCases(t *testing.T) {
	e := newEnv(t, context.Background(), &fakeRunner{report: sampleReport()})
	ctx := context.Background()

	run, err := e.svc.StartRun(ctx, e.app.ID, store.TriggerManual)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if run.Status != store.RunRunning {
		t.Errorf("returned status = %q, want running", run.Status)
	}
	e.svc.Wait()

	got, err := e.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != store.RunFailed || got.Total != 5 || got.Passed != 2 || got.Failed != 1 ||
		got.Flaky != 1 || got.Skipped != 1 || got.FinishedAt == nil || got.Error != "" {
		t.Errorf("finished run = %+v", got)
	}

	spec := e.runner.specs[0]
	if spec.Slug != "shoplite" || spec.BaseURL != "http://localhost:4000" || spec.RunDir != RunDir(e.dataDir, run.ID) {
		t.Errorf("runner spec = %+v", spec)
	}

	results, err := e.store.ListTestResults(ctx, run.ID)
	if err != nil || len(results) != 5 {
		t.Fatalf("ListTestResults = %d, %v", len(results), err)
	}
	if results[1].Error != "Error: expected 1" || results[1].Artifacts["screenshot"] == "" {
		t.Errorf("failed result = %+v", results[1])
	}
	if results[4].TestCaseID != nil {
		t.Errorf("result outside manual/ and generated/ linked to test case %d", *results[4].TestCaseID)
	}

	tcs, err := e.store.ListTestCases(ctx, e.app.ID, store.TestCaseFilter{})
	if err != nil {
		t.Fatalf("ListTestCases: %v", err)
	}
	origins := map[string]string{}
	ids := map[string]int64{}
	for _, tc := range tcs {
		origins[tc.Title] = tc.Origin
		ids[tc.Title] = tc.ID
	}
	if len(tcs) != 4 || origins["logs in"] != store.OriginManual || origins["Search › finds mugs"] != store.OriginGenerated {
		t.Errorf("test cases = %v", origins)
	}
	for _, r := range results[:4] {
		if r.TestCaseID == nil || *r.TestCaseID != ids[r.Title] {
			t.Errorf("result %q not linked to its test case", r.Title)
		}
	}

	if len(e.notifier.runs) != 1 || e.notifier.runs[0].Status != store.RunFailed || e.notifier.n[0] != 5 {
		t.Errorf("notifier got %+v (results %v)", e.notifier.runs, e.notifier.n)
	}

	// A second run reuses the same test cases.
	if _, err := e.svc.StartRun(ctx, e.app.ID, store.TriggerManual); err != nil {
		t.Fatalf("second StartRun: %v", err)
	}
	e.svc.Wait()
	tcs, _ = e.store.ListTestCases(ctx, e.app.ID, store.TestCaseFilter{})
	if len(tcs) != 4 {
		t.Errorf("test cases after second run = %d, want 4", len(tcs))
	}
}

func TestStartRunRefusesActiveRun(t *testing.T) {
	fr := &fakeRunner{report: sampleReport(), release: make(chan struct{})}
	e := newEnv(t, context.Background(), fr)
	ctx := context.Background()

	first, err := e.svc.StartRun(ctx, e.app.ID, store.TriggerManual)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	_, err = e.svc.StartRun(ctx, e.app.ID, store.TriggerSchedule)
	var active *ActiveRunError
	if !errors.As(err, &active) || active.RunID != first.ID || active.AppID != e.app.ID {
		t.Fatalf("second StartRun err = %v, want ActiveRunError for run %d", err, first.ID)
	}

	close(fr.release)
	e.svc.Wait()
	if _, err := e.svc.StartRun(ctx, e.app.ID, store.TriggerManual); err != nil {
		t.Errorf("StartRun after finish: %v", err)
	}
	e.svc.Wait()
}

func TestStartRunUnknownApp(t *testing.T) {
	e := newEnv(t, context.Background(), &fakeRunner{})
	if _, err := e.svc.StartRun(context.Background(), 999, store.TriggerManual); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestRunnerErrorFinishesAsError(t *testing.T) {
	e := newEnv(t, context.Background(), &fakeRunner{err: errors.New("playwright wrote no report (exit status 1)")})
	run, err := e.svc.StartRun(context.Background(), e.app.ID, store.TriggerManual)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	e.svc.Wait()

	got, _ := e.store.GetRun(context.Background(), run.ID)
	if got.Status != store.RunError || got.Error != "playwright wrote no report (exit status 1)" || got.Total != 0 {
		t.Errorf("run = %+v", got)
	}
	if len(e.notifier.runs) != 1 || e.notifier.runs[0].Status != store.RunError {
		t.Errorf("notifier got %+v", e.notifier.runs)
	}
}

func TestServerStopFinishesAsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	e := newEnv(t, ctx, &fakeRunner{release: make(chan struct{})})
	run, err := e.svc.StartRun(context.Background(), e.app.ID, store.TriggerManual)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	cancel()
	e.svc.Wait()

	got, _ := e.store.GetRun(context.Background(), run.ID)
	if got.Status != store.RunError || got.Error != "server stopped during the run" {
		t.Errorf("run = %+v", got)
	}
}

func TestRecover(t *testing.T) {
	e := newEnv(t, context.Background(), &fakeRunner{})
	ctx := context.Background()
	// A run left running by a previous process.
	stale, err := e.store.CreateRun(ctx, e.app.ID, store.TriggerManual)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	if err := e.svc.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	got, _ := e.store.GetRun(ctx, stale.ID)
	if got.Status != store.RunError || got.Error != "server restarted" {
		t.Errorf("stale run = %+v", got)
	}
	// The app is free to run again.
	if _, err := e.svc.StartRun(ctx, e.app.ID, store.TriggerManual); err != nil {
		t.Errorf("StartRun after Recover: %v", err)
	}
	e.svc.Wait()
}

func TestOriginOf(t *testing.T) {
	tests := map[string]string{
		"tests/shoplite/manual/auth.spec.ts":      "manual",
		"tests/shoplite/generated/search.spec.ts": "generated",
		"tests/shoplite/generated/deep/x.spec.ts": "generated",
		"tests/shoplite/other.spec.ts":            "",
		"tests/shoplite/manual":                   "",
		"elsewhere/shoplite/manual/auth.spec.ts":  "",
		"tests/shoplite/manualish/auth.spec.ts":   "",
	}
	for file, want := range tests {
		if got := originOf(file); got != want {
			t.Errorf("originOf(%q) = %q, want %q", file, got, want)
		}
	}
}

func TestSummarize(t *testing.T) {
	tests := []struct {
		name     string
		statuses []string
		want     string
	}{
		{"all passed", []string{"passed", "passed"}, store.RunPassed},
		{"flaky does not fail the run", []string{"passed", "flaky"}, store.RunPassed},
		{"skipped does not fail the run", []string{"skipped"}, store.RunPassed},
		{"one failure fails the run", []string{"passed", "failed", "flaky"}, store.RunFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var results []store.TestResult
			for _, s := range tt.statuses {
				results = append(results, store.TestResult{Status: s})
			}
			if got := summarize(results); got.Status != tt.want || got.Total != len(tt.statuses) {
				t.Errorf("summarize = %+v, want status %q", got, tt.want)
			}
		})
	}
}
