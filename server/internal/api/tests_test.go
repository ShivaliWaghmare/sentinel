package api

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/ShivaliWaghmare/sentinel/server/internal/store"
)

// seedTests creates an app with one manual and one generated test case.
func seedTests(t *testing.T, env testEnv) (appID int64, manual, generated store.TestCase) {
	t.Helper()
	a := env.createApp(t, `{"name":"ShopLite","base_url":"http://localhost:4000"}`)
	ctx := context.Background()
	var err error
	manual, err = env.store.UpsertTestCase(ctx, store.TestCaseInput{AppID: a.ID, FilePath: "manual/login.spec.ts", Title: "logs in", Origin: store.OriginManual})
	if err != nil {
		t.Fatal(err)
	}
	generated, err = env.store.UpsertTestCase(ctx, store.TestCaseInput{AppID: a.ID, FilePath: "generated/cart.spec.ts", Title: "adds to cart", Origin: store.OriginGenerated})
	if err != nil {
		t.Fatal(err)
	}
	return a.ID, manual, generated
}

type testsResponse struct {
	Tests []testCaseJSON `json:"tests"`
}

func TestListTests(t *testing.T) {
	env := newTestEnv(t, nil)
	appID, manual, generated := seedTests(t, env)
	if _, err := env.store.UpdateTestCaseStatus(context.Background(), generated.ID, store.StatusQuarantined); err != nil {
		t.Fatal(err)
	}
	base := appPath(appID) + "/tests"

	tests := []struct {
		name    string
		query   string
		wantIDs []int64
	}{
		{"all", "", []int64{generated.ID, manual.ID}},
		{"origin manual", "?origin=manual", []int64{manual.ID}},
		{"status quarantined", "?status=quarantined", []int64{generated.ID}},
		{"both filters, no match", "?origin=manual&status=retired", []int64{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := env.do(t, "GET", base+tt.query, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
			}
			var got testsResponse
			decode(t, rec, &got)
			if got.Tests == nil {
				t.Fatalf("tests is null, want a list: %s", rec.Body.String())
			}
			if len(got.Tests) != len(tt.wantIDs) {
				t.Fatalf("got %d tests, want %d", len(got.Tests), len(tt.wantIDs))
			}
			for i, tc := range got.Tests {
				if tc.ID != tt.wantIDs[i] {
					t.Errorf("tests[%d].ID = %d, want %d", i, tc.ID, tt.wantIDs[i])
				}
			}
		})
	}
}

func TestListTestsErrors(t *testing.T) {
	env := newTestEnv(t, nil)
	appID, _, _ := seedTests(t, env)
	base := appPath(appID) + "/tests"

	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantMsg    string
	}{
		{"bad origin", base + "?origin=robot", http.StatusBadRequest, "origin must be one of"},
		{"bad status", base + "?status=broken", http.StatusBadRequest, "status must be one of"},
		{"unknown app", "/api/apps/999/tests", http.StatusNotFound, "app not found"},
		{"non-numeric app", "/api/apps/abc/tests", http.StatusNotFound, "app not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantError(t, env.do(t, "GET", tt.path, ""), tt.wantStatus, tt.wantMsg)
		})
	}
}

func TestUpdateTest(t *testing.T) {
	env := newTestEnv(t, nil)
	_, manual, _ := seedTests(t, env)
	path := "/api/tests/" + strconv.FormatInt(manual.ID, 10)

	rec := env.do(t, "PATCH", path, `{"status":"quarantined"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var got testCaseJSON
	decode(t, rec, &got)
	if got.ID != manual.ID || got.Status != store.StatusQuarantined || got.Title != manual.Title || got.AgentRunID != nil {
		t.Errorf("updated test = %+v", got)
	}

	stored, err := env.store.GetTestCase(context.Background(), manual.ID)
	if err != nil || stored.Status != store.StatusQuarantined {
		t.Errorf("stored status = %q, %v; want quarantined", stored.Status, err)
	}
}

func TestUpdateTestErrors(t *testing.T) {
	env := newTestEnv(t, nil)
	_, manual, _ := seedTests(t, env)
	path := "/api/tests/" + strconv.FormatInt(manual.ID, 10)

	tests := []struct {
		name       string
		path       string
		body       string
		wantStatus int
		wantMsg    string
	}{
		{"missing status", path, `{}`, http.StatusBadRequest, "status is required"},
		{"invalid status", path, `{"status":"deleted"}`, http.StatusBadRequest, "status must be one of"},
		{"other field", path, `{"status":"retired","title":"x"}`, http.StatusBadRequest, `unknown field "title"`},
		{"unknown test", "/api/tests/999", `{"status":"retired"}`, http.StatusNotFound, "test not found"},
		{"non-numeric id", "/api/tests/abc", `{"status":"retired"}`, http.StatusNotFound, "test not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantError(t, env.do(t, "PATCH", tt.path, tt.body), tt.wantStatus, tt.wantMsg)
		})
	}
}
