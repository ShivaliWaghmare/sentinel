package store

import (
	"context"
	"errors"
	"testing"
)

func TestCreateRunRefusesSecondActiveRun(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	app := createTestApp(t, s, "shop")
	other := createTestApp(t, s, "other")

	first, err := s.CreateRun(ctx, app.ID, TriggerManual)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if first.Status != RunRunning || first.StartedAt == nil || first.FinishedAt != nil {
		t.Errorf("new run = %+v, want running with started_at and no finished_at", first)
	}

	if _, err := s.CreateRun(ctx, app.ID, TriggerSchedule); !errors.Is(err, ErrActiveRun) {
		t.Fatalf("second CreateRun err = %v, want ErrActiveRun", err)
	}
	// Another app is not affected.
	if _, err := s.CreateRun(ctx, other.ID, TriggerManual); err != nil {
		t.Fatalf("CreateRun for other app: %v", err)
	}

	active, err := s.ActiveRun(ctx, app.ID)
	if err != nil || active.ID != first.ID {
		t.Fatalf("ActiveRun = %+v, %v; want run %d", active, err, first.ID)
	}

	// Once finished, a new run may start.
	if _, err := s.FinishRun(ctx, first.ID, RunOutcome{Status: RunPassed}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	if _, err := s.ActiveRun(ctx, app.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("ActiveRun after finish err = %v, want ErrNotFound", err)
	}
	if _, err := s.CreateRun(ctx, app.ID, TriggerManual); err != nil {
		t.Fatalf("CreateRun after finish: %v", err)
	}
}

func TestFinishRun(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	app := createTestApp(t, s, "shop")
	run, err := s.CreateRun(ctx, app.ID, TriggerManual)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	got, err := s.FinishRun(ctx, run.ID, RunOutcome{
		Status: RunFailed, Total: 4, Passed: 1, Failed: 1, Flaky: 1, Skipped: 1, DurationMS: 1234,
	})
	if err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	want := Run{ID: run.ID, AppID: app.ID, Trigger: TriggerManual, Status: RunFailed,
		Total: 4, Passed: 1, Failed: 1, Flaky: 1, Skipped: 1, DurationMS: 1234}
	if got.ID != want.ID || got.Status != want.Status || got.Total != want.Total ||
		got.Passed != want.Passed || got.Failed != want.Failed || got.Flaky != want.Flaky ||
		got.Skipped != want.Skipped || got.DurationMS != want.DurationMS || got.Error != "" {
		t.Errorf("FinishRun = %+v, want %+v", got, want)
	}
	if got.FinishedAt == nil {
		t.Error("FinishedAt is nil")
	}

	errored, err := s.FinishRun(ctx, run.ID, RunOutcome{Status: RunError, Error: "boom"})
	if err != nil || errored.Error != "boom" {
		t.Errorf("FinishRun with error = %+v, %v", errored, err)
	}

	if _, err := s.FinishRun(ctx, 999, RunOutcome{Status: RunPassed}); !errors.Is(err, ErrNotFound) {
		t.Errorf("FinishRun(999) err = %v, want ErrNotFound", err)
	}
}

func TestFailInterruptedRuns(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a := createTestApp(t, s, "a")
	b := createTestApp(t, s, "b")
	running, _ := s.CreateRun(ctx, a.ID, TriggerManual)
	done, _ := s.CreateRun(ctx, b.ID, TriggerManual)
	if _, err := s.FinishRun(ctx, done.ID, RunOutcome{Status: RunPassed}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	n, err := s.FailInterruptedRuns(ctx, "server restarted")
	if err != nil || n != 1 {
		t.Fatalf("FailInterruptedRuns = %d, %v; want 1", n, err)
	}
	got, _ := s.GetRun(ctx, running.ID)
	if got.Status != RunError || got.Error != "server restarted" || got.FinishedAt == nil {
		t.Errorf("interrupted run = %+v", got)
	}
	if got, _ := s.GetRun(ctx, done.ID); got.Status != RunPassed {
		t.Errorf("finished run status = %q, want passed", got.Status)
	}
}

func TestListRuns(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a := createTestApp(t, s, "a")
	b := createTestApp(t, s, "b")
	var ids []int64
	for _, app := range []App{a, b, a} {
		run, err := s.CreateRun(ctx, app.ID, TriggerManual)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if _, err := s.FinishRun(ctx, run.ID, RunOutcome{Status: RunPassed}); err != nil {
			t.Fatalf("FinishRun: %v", err)
		}
		ids = append(ids, run.ID)
	}

	tests := []struct {
		name   string
		filter RunFilter
		want   []int64
	}{
		{"all newest first", RunFilter{Limit: 10}, []int64{ids[2], ids[1], ids[0]}},
		{"by app", RunFilter{AppID: a.ID, Limit: 10}, []int64{ids[2], ids[0]}},
		{"limit", RunFilter{Limit: 1}, []int64{ids[2]}},
		{"unknown app", RunFilter{AppID: 999, Limit: 10}, []int64{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runs, err := s.ListRuns(ctx, tt.filter)
			if err != nil {
				t.Fatalf("ListRuns: %v", err)
			}
			got := []int64{}
			for _, r := range runs {
				got = append(got, r.ID)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("ids = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("ids = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestInsertAndListTestResults(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	app := createTestApp(t, s, "shop")
	run, _ := s.CreateRun(ctx, app.ID, TriggerManual)
	tc, err := s.UpsertTestCase(ctx, TestCaseInput{
		AppID: app.ID, FilePath: "tests/shop/manual/a.spec.ts", Title: "a", Origin: OriginManual,
	})
	if err != nil {
		t.Fatalf("UpsertTestCase: %v", err)
	}

	in := []TestResult{
		{TestCaseID: &tc.ID, Title: "a", FilePath: "tests/shop/manual/a.spec.ts", Line: 3,
			Status: ResultPassed, DurationMS: 10},
		{Title: "b", FilePath: "tests/shop/other/b.spec.ts", Line: 7, Status: ResultFailed,
			DurationMS: 20, Error: "Error: boom", Retries: 1,
			Artifacts: map[string]string{"screenshot": "runs/1/artifacts/b/test-failed-1.png"}},
	}
	if err := s.InsertTestResults(ctx, run.ID, in); err != nil {
		t.Fatalf("InsertTestResults: %v", err)
	}

	got, err := s.ListTestResults(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListTestResults: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2", len(got))
	}
	if got[0].TestCaseID == nil || *got[0].TestCaseID != tc.ID || got[0].Error != "" ||
		len(got[0].Artifacts) != 0 || got[0].Artifacts == nil {
		t.Errorf("result 0 = %+v", got[0])
	}
	if got[1].TestCaseID != nil || got[1].Error != "Error: boom" || got[1].Retries != 1 ||
		got[1].Artifacts["screenshot"] != "runs/1/artifacts/b/test-failed-1.png" {
		t.Errorf("result 1 = %+v", got[1])
	}

	// Deleting the app cascades to runs and results.
	if err := s.DeleteApp(ctx, app.ID); err != nil {
		t.Fatalf("DeleteApp: %v", err)
	}
	if got, _ := s.ListTestResults(ctx, run.ID); len(got) != 0 {
		t.Errorf("results after app delete = %d, want 0", len(got))
	}
}
