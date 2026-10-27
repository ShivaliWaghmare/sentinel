package notify

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ShivaliWaghmare/sentinel/server/internal/store"
)

// failedRun is the sample run behind the golden files: 6 of 8 passed.
func failedRun() (store.App, store.Run, []store.TestResult) {
	app := store.App{Name: "ShopLite"}
	run := store.Run{
		ID: 12, Status: store.RunFailed, Trigger: store.TriggerSchedule,
		Total: 8, Passed: 6, Failed: 1, Flaky: 1, DurationMS: 83_400,
	}
	results := []store.TestResult{
		{Title: "auth › logs in", Status: store.ResultPassed},
		{Title: "cart › badge <count> & total", Status: store.ResultFailed,
			Error: "\nError: expect(locator).toHaveText(expected) failed\n\nLocator: getByTestId('cart-count')\nExpected: \"1\""},
		{Title: "search › finds products", Status: store.ResultFlaky, Error: "Timeout 5000ms exceeded"},
	}
	return app, run, results
}

func TestNewReport(t *testing.T) {
	app, run, results := failedRun()
	r := NewReport(app, run, results, "http://localhost:3000/")

	if r.RunURL != "http://localhost:3000/runs/12" {
		t.Errorf("RunURL = %q", r.RunURL)
	}
	if len(r.Failures) != 1 || r.Failures[0].ErrorLine != "Error: expect(locator).toHaveText(expected) failed" {
		t.Errorf("Failures = %+v, want one with the first non-blank error line", r.Failures)
	}
	if got := r.Subject(); got != "❌ ShopLite: 6/8 passed (run #12)" {
		t.Errorf("Subject = %q", got)
	}
	if got := r.Details(); got != "Run #12 · Trigger: schedule · Duration: 1m 23s · Flaky: 1 · Skipped: 0" {
		t.Errorf("Details = %q", got)
	}
}

func TestNewReportCapsFailures(t *testing.T) {
	var results []store.TestResult
	for i := range 13 {
		results = append(results, store.TestResult{
			Title: fmt.Sprintf("t%d", i), Status: store.ResultFailed, Error: strings.Repeat("x", 500),
		})
	}
	r := NewReport(store.App{Name: "A"}, store.Run{ID: 1, Status: store.RunFailed}, results, "http://d")
	if len(r.Failures) != maxFailures || r.MoreFailures != 3 || r.MoreText() != "and 3 more failures" {
		t.Errorf("failures = %d, more = %d (%q); want 10 and 3", len(r.Failures), r.MoreFailures, r.MoreText())
	}
	if n := len([]rune(r.Failures[0].ErrorLine)); n != maxErrorLen || !strings.HasSuffix(r.Failures[0].ErrorLine, "…") {
		t.Errorf("error line has %d chars, want %d ending in …", n, maxErrorLen)
	}
}

func TestReportStatusLines(t *testing.T) {
	tests := []struct {
		name        string
		run         store.Run
		wantSubject string
	}{
		{"passed", store.Run{ID: 3, Status: store.RunPassed, Total: 8, Passed: 8}, "✅ ShopLite: 8/8 passed (run #3)"},
		{"failed", store.Run{ID: 4, Status: store.RunFailed, Total: 8, Passed: 2}, "❌ ShopLite: 2/8 passed (run #4)"},
		{"error", store.Run{ID: 5, Status: store.RunError, Error: "no report\nlog tail"}, "⚠️ ShopLite: run error (run #5)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewReport(store.App{Name: "ShopLite"}, tt.run, nil, "http://d")
			if got := r.Subject(); got != tt.wantSubject {
				t.Errorf("Subject = %q, want %q", got, tt.wantSubject)
			}
		})
	}
	r := NewReport(store.App{Name: "ShopLite"}, store.Run{Status: store.RunError, Error: "no report\nlog tail"}, nil, "http://d")
	if r.RunError != "no report" {
		t.Errorf("RunError = %q, want the first line", r.RunError)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0.0s"},
		{4200 * time.Millisecond, "4.2s"},
		{83400 * time.Millisecond, "1m 23s"},
		{59*time.Minute + 59600*time.Millisecond, "60m 0s"},
		{2*time.Hour + 5*time.Minute, "2h 5m"},
	}
	for _, tt := range tests {
		if got := formatDuration(tt.d); got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestSampleReport(t *testing.T) {
	r := sampleReport("http://localhost:3000")
	if !r.Sample || len(r.Failures) != maxFailures || r.MoreFailures != 2 {
		t.Errorf("sample = %+v, want 10 failures listed and 2 more", r)
	}
	if !strings.HasPrefix(r.Subject(), "[Test] ") {
		t.Errorf("Subject = %q, want [Test] prefix", r.Subject())
	}
}
