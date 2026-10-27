package api

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/ShivaliWaghmare/sentinel/server/internal/config"
	"github.com/ShivaliWaghmare/sentinel/server/internal/runner"
)

func runsPath(appID int64) string {
	return "/api/apps/" + strconv.FormatInt(appID, 10) + "/runs"
}

func TestStartRunThenConflict(t *testing.T) {
	env := newTestEnv(t, nil)
	a := env.createApp(t, `{"name":"ShopLite","base_url":"http://localhost:4000"}`)

	rec := env.do(t, "POST", runsPath(a.ID), "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	var run runJSON
	decode(t, rec, &run)
	if run.AppID != a.ID || run.Status != "running" || run.Trigger != "manual" ||
		run.StartedAt == nil || run.FinishedAt != nil || run.Error != nil {
		t.Errorf("run = %+v", run)
	}

	// The first run is still active, so a second POST conflicts.
	rec = env.do(t, "POST", runsPath(a.ID), "")
	wantError(t, rec, http.StatusConflict, "already has an active run (run "+strconv.FormatInt(run.ID, 10)+")")
}

func TestStartRunUnknownApp(t *testing.T) {
	env := newTestEnv(t, nil)
	for _, path := range []string{runsPath(999), "/api/apps/abc/runs"} {
		wantError(t, env.do(t, "POST", path, ""), http.StatusNotFound, "app not found")
	}
}

func TestGetRunWithResults(t *testing.T) {
	env := newTestEnv(t, nil)
	env.runner.report = runner.Report{Results: []runner.Result{
		{Title: "logs in", File: "tests/shoplite/manual/auth.spec.ts", Line: 4, Status: "passed", DurationMS: 120,
			Artifacts: map[string]string{}},
		{Title: "badge", File: "tests/shoplite/manual/cart.spec.ts", Line: 4, Status: "failed", DurationMS: 900,
			Error: "Error: expected 1", Retries: 1,
			Artifacts: map[string]string{"screenshot": "runs/1/artifacts/badge/test-failed-1.png"}},
	}}
	a := env.createApp(t, `{"name":"ShopLite","base_url":"http://localhost:4000"}`)

	var started runJSON
	decode(t, env.do(t, "POST", runsPath(a.ID), ""), &started)
	close(env.runner.release)
	env.runs.Wait()

	rec := env.do(t, "GET", "/api/runs/"+strconv.FormatInt(started.ID, 10), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body.String())
	}
	var got runDetailJSON
	decode(t, rec, &got)
	if got.Status != "failed" || got.Total != 2 || got.Passed != 1 || got.Failed != 1 || got.FinishedAt == nil {
		t.Errorf("run = %+v", got.runJSON)
	}
	if len(got.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(got.Results))
	}
	pass, fail := got.Results[0], got.Results[1]
	if pass.Error != nil || pass.Artifacts == nil || len(pass.Artifacts) != 0 || pass.TestCaseID == nil {
		t.Errorf("passing result = %+v", pass)
	}
	if fail.Error == nil || *fail.Error != "Error: expected 1" || fail.Retries != 1 ||
		fail.Artifacts["screenshot"] != "runs/1/artifacts/badge/test-failed-1.png" {
		t.Errorf("failing result = %+v", fail)
	}

	wantError(t, env.do(t, "GET", "/api/runs/999", ""), http.StatusNotFound, "run not found")
	wantError(t, env.do(t, "GET", "/api/runs/x", ""), http.StatusNotFound, "run not found")
}

func TestListRuns(t *testing.T) {
	env := newTestEnv(t, nil)
	close(env.runner.release) // runs finish immediately
	a := env.createApp(t, `{"name":"Alpha","base_url":"http://a"}`)
	b := env.createApp(t, `{"name":"Beta","base_url":"http://b"}`)
	var ids []int64
	for _, app := range []appJSON{a, b, a} {
		var run runJSON
		decode(t, env.do(t, "POST", runsPath(app.ID), ""), &run)
		env.runs.Wait()
		ids = append(ids, run.ID)
	}

	tests := []struct {
		query string
		want  []int64
	}{
		{"", []int64{ids[2], ids[1], ids[0]}},
		{"?app_id=" + strconv.FormatInt(a.ID, 10), []int64{ids[2], ids[0]}},
		{"?limit=1", []int64{ids[2]}},
		{"?app_id=999", []int64{}},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			rec := env.do(t, "GET", "/api/runs"+tt.query, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			var body map[string][]map[string]any
			decode(t, rec, &body)
			list, ok := body["runs"]
			if !ok || list == nil {
				t.Fatalf("body = %s, want a runs list", rec.Body.String())
			}
			if len(list) != len(tt.want) {
				t.Fatalf("got %d runs, want %d", len(list), len(tt.want))
			}
			for i, r := range list {
				if int64(r["id"].(float64)) != tt.want[i] {
					t.Errorf("run %d id = %v, want %d", i, r["id"], tt.want[i])
				}
				if _, hasResults := r["results"]; hasResults {
					t.Error("list should not include results")
				}
			}
		})
	}

	for _, q := range []string{"?limit=0", "?limit=101", "?limit=x", "?app_id=0", "?app_id=x"} {
		rec := env.do(t, "GET", "/api/runs"+q, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, rec.Code)
		}
	}
}

func TestStartRunReadOnly(t *testing.T) {
	env := newTestEnv(t, nil)
	a := env.createApp(t, `{"name":"ShopLite","base_url":"http://localhost:4000"}`)
	ro := newTestEnv(t, func(c *config.Config) { c.DemoReadOnly = true })
	wantError(t, ro.do(t, "POST", runsPath(a.ID), ""), http.StatusForbidden, "read-only")
}
