package store

import (
	"context"
	"errors"
	"testing"
)

func insertAgentRun(t *testing.T, s *Store, appID int64) int64 {
	t.Helper()
	res, err := s.db.Exec(`INSERT INTO agent_runs (app_id, kind, status) VALUES (?, 'explore', 'succeeded')`, appID)
	if err != nil {
		t.Fatalf("insert agent run: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestUpsertTestCase(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	app := createTestApp(t, s, "shoplite")
	runID := insertAgentRun(t, s, app.ID)

	first, err := s.UpsertTestCase(ctx, TestCaseInput{AppID: app.ID, FilePath: "generated/cart.spec.ts", Title: "adds to cart", Origin: OriginGenerated, AgentRunID: &runID})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if first.Status != StatusActive || first.AgentRunID == nil || *first.AgentRunID != runID {
		t.Errorf("first upsert = %+v, want active with agent run %d", first, runID)
	}

	if _, err := s.UpdateTestCaseStatus(ctx, first.ID, StatusQuarantined); err != nil {
		t.Fatalf("UpdateTestCaseStatus: %v", err)
	}

	// Same key again, without an agent run (as a test run would upsert it).
	second, err := s.UpsertTestCase(ctx, TestCaseInput{AppID: app.ID, FilePath: "generated/cart.spec.ts", Title: "adds to cart", Origin: OriginGenerated})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("ID changed: %d → %d", first.ID, second.ID)
	}
	if second.Status != StatusQuarantined {
		t.Errorf("status = %q, want quarantined to be kept", second.Status)
	}
	if second.AgentRunID == nil || *second.AgentRunID != runID {
		t.Errorf("agent_run_id = %v, want %d kept", second.AgentRunID, runID)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("created_at changed: %v → %v", first.CreatedAt, second.CreatedAt)
	}

	// A different title in the same file is a different test case.
	other, err := s.UpsertTestCase(ctx, TestCaseInput{AppID: app.ID, FilePath: "generated/cart.spec.ts", Title: "removes from cart", Origin: OriginGenerated})
	if err != nil {
		t.Fatalf("third upsert: %v", err)
	}
	if other.ID == first.ID {
		t.Error("different title reused the same ID")
	}
}

func TestUpsertTestCaseUnknownApp(t *testing.T) {
	s := newTestStore(t)
	_, err := s.UpsertTestCase(context.Background(), TestCaseInput{AppID: 42, FilePath: "a.spec.ts", Title: "t", Origin: OriginManual})
	if err == nil {
		t.Fatal("want foreign key error, got nil")
	}
}

func TestListTestCasesFilters(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	app := createTestApp(t, s, "shoplite")
	other := createTestApp(t, s, "other")

	seed := []struct {
		appID  int64
		file   string
		origin string
		status string
	}{
		{app.ID, "manual/a.spec.ts", OriginManual, StatusActive},
		{app.ID, "manual/b.spec.ts", OriginManual, StatusQuarantined},
		{app.ID, "generated/c.spec.ts", OriginGenerated, StatusActive},
		{app.ID, "generated/d.spec.ts", OriginGenerated, StatusRetired},
		{other.ID, "manual/e.spec.ts", OriginManual, StatusActive},
	}
	for _, sd := range seed {
		tc, err := s.UpsertTestCase(ctx, TestCaseInput{AppID: sd.appID, FilePath: sd.file, Title: "t", Origin: sd.origin})
		if err != nil {
			t.Fatalf("seed %s: %v", sd.file, err)
		}
		if sd.status != StatusActive {
			if _, err := s.UpdateTestCaseStatus(ctx, tc.ID, sd.status); err != nil {
				t.Fatalf("seed status %s: %v", sd.file, err)
			}
		}
	}

	tests := []struct {
		name   string
		filter TestCaseFilter
		want   []string
	}{
		{"no filter, sorted by file", TestCaseFilter{}, []string{"generated/c.spec.ts", "generated/d.spec.ts", "manual/a.spec.ts", "manual/b.spec.ts"}},
		{"origin manual", TestCaseFilter{Origin: OriginManual}, []string{"manual/a.spec.ts", "manual/b.spec.ts"}},
		{"status active", TestCaseFilter{Status: StatusActive}, []string{"generated/c.spec.ts", "manual/a.spec.ts"}},
		{"origin and status", TestCaseFilter{Origin: OriginGenerated, Status: StatusRetired}, []string{"generated/d.spec.ts"}},
		{"no match", TestCaseFilter{Origin: OriginManual, Status: StatusRetired}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.ListTestCases(ctx, app.ID, tt.filter)
			if err != nil {
				t.Fatalf("ListTestCases: %v", err)
			}
			if got == nil {
				t.Fatal("ListTestCases returned nil slice")
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d test cases, want %d (%v)", len(got), len(tt.want), tt.want)
			}
			for i, tc := range got {
				if tc.FilePath != tt.want[i] {
					t.Errorf("[%d] = %q, want %q", i, tc.FilePath, tt.want[i])
				}
			}
		})
	}
}

func TestTestCaseNotFound(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.GetTestCase(ctx, 99); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetTestCase err = %v, want ErrNotFound", err)
	}
	if _, err := s.UpdateTestCaseStatus(ctx, 99, StatusRetired); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateTestCaseStatus err = %v, want ErrNotFound", err)
	}
}

func TestValidOriginAndStatus(t *testing.T) {
	tests := []struct {
		value      string
		wantOrigin bool
		wantStatus bool
	}{
		{"manual", true, false},
		{"generated", true, false},
		{"active", false, true},
		{"quarantined", false, true},
		{"retired", false, true},
		{"", false, false},
		{"Active", false, false},
	}
	for _, tt := range tests {
		if got := ValidOrigin(tt.value); got != tt.wantOrigin {
			t.Errorf("ValidOrigin(%q) = %v, want %v", tt.value, got, tt.wantOrigin)
		}
		if got := ValidTestStatus(tt.value); got != tt.wantStatus {
			t.Errorf("ValidTestStatus(%q) = %v, want %v", tt.value, got, tt.wantStatus)
		}
	}
}
