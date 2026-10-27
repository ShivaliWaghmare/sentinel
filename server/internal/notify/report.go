// Package notify sends run reports to Slack and email (lifecycle 1, step 6).
// A Report holds what both formats show; slack.go and email.go render it.
package notify

import (
	"fmt"
	"strings"
	"time"

	"github.com/ShivaliWaghmare/sentinel/server/internal/store"
)

const (
	// maxFailures is how many failures a report lists; the rest are counted.
	maxFailures = 10
	// maxErrorLen caps each failure's error line, in characters.
	maxErrorLen = 200
)

// Report is a finished run, ready to format.
type Report struct {
	AppName  string
	RunID    int64
	Status   string // store.RunPassed, RunFailed or RunError
	Trigger  string
	Total    int
	Passed   int
	Flaky    int
	Skipped  int
	Duration time.Duration
	RunError string    // first line of the run's error; set for error runs
	Failures []Failure // at most maxFailures
	// MoreFailures counts failed tests beyond Failures.
	MoreFailures int
	RunURL       string
	// Sample marks the report sent by POST /api/notify/test.
	Sample bool
}

// Failure is one failed test in a report.
type Failure struct {
	Title     string
	ErrorLine string // first line of the error, truncated
	// STEP 9 (triage): add Category and Summary here, filled from
	// triage_results, and render them in slack.go and email.go.
}

// NewReport builds a report for a finished run. dashboardURL is DASHBOARD_URL.
func NewReport(app store.App, run store.Run, results []store.TestResult, dashboardURL string) Report {
	r := Report{
		AppName:  app.Name,
		RunID:    run.ID,
		Status:   run.Status,
		Trigger:  run.Trigger,
		Total:    run.Total,
		Passed:   run.Passed,
		Flaky:    run.Flaky,
		Skipped:  run.Skipped,
		Duration: time.Duration(run.DurationMS) * time.Millisecond,
		RunError: truncate(firstLine(run.Error), maxErrorLen),
		RunURL:   fmt.Sprintf("%s/runs/%d", strings.TrimRight(dashboardURL, "/"), run.ID),
	}
	for _, res := range results {
		if res.Status != store.ResultFailed {
			continue
		}
		if len(r.Failures) == maxFailures {
			r.MoreFailures++
			continue
		}
		r.Failures = append(r.Failures, Failure{
			Title:     res.Title,
			ErrorLine: truncate(firstLine(res.Error), maxErrorLen),
		})
	}
	return r
}

// Emoji shows the run status at a glance.
func (r Report) Emoji() string {
	switch r.Status {
	case store.RunPassed:
		return "✅"
	case store.RunFailed:
		return "❌"
	default:
		return "⚠️"
	}
}

// Headline is "ShopLite: 6/8 passed", or "ShopLite: run error".
func (r Report) Headline() string {
	if r.Status == store.RunError {
		return r.AppName + ": run error"
	}
	return fmt.Sprintf("%s: %d/%d passed", r.AppName, r.Passed, r.Total)
}

// Subject is the email subject and Slack fallback text:
// "❌ ShopLite: 6/8 passed (run #12)".
func (r Report) Subject() string {
	s := fmt.Sprintf("%s %s (run #%d)", r.Emoji(), r.Headline(), r.RunID)
	if r.Sample {
		s = "[Test] " + s
	}
	return s
}

// Details is the context line: trigger, duration, flaky and skipped counts.
func (r Report) Details() string {
	s := fmt.Sprintf("Run #%d · Trigger: %s · Duration: %s · Flaky: %d · Skipped: %d",
		r.RunID, r.Trigger, formatDuration(r.Duration), r.Flaky, r.Skipped)
	if r.Sample {
		s = "Test notification with sample data · " + s
	}
	return s
}

// MoreText is "and 3 more failures", or "" when every failure is listed.
func (r Report) MoreText() string {
	switch r.MoreFailures {
	case 0:
		return ""
	case 1:
		return "and 1 more failure"
	default:
		return fmt.Sprintf("and %d more failures", r.MoreFailures)
	}
}

// formatDuration renders 83.4s as "1m 23s" and 4.2s as "4.2s".
func formatDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Hour:
		d = d.Round(time.Second)
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		d = d.Round(time.Minute)
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// firstLine returns the first non-blank line of s, trimmed.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// truncate cuts s to at most n characters, ending in "…" if it was cut.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}

// sampleReport is the report POST /api/notify/test sends: a failing run with
// enough failures to show the "and N more" line.
func sampleReport(dashboardURL string) Report {
	app := store.App{Name: "ShopLite"}
	run := store.Run{
		ID: 12, Status: store.RunFailed, Trigger: store.TriggerSchedule,
		Total: 20, Passed: 7, Failed: 12, Flaky: 1, DurationMS: 83_400,
	}
	results := make([]store.TestResult, 0, run.Failed)
	for i := 1; i <= run.Failed; i++ {
		results = append(results, store.TestResult{
			Title:  fmt.Sprintf("checkout › sample failure %d", i),
			Status: store.ResultFailed,
			Error:  "Error: expect(locator).toHaveText(expected) failed\n\nLocator: getByTestId('cart-count')",
		})
	}
	r := NewReport(app, run, results, dashboardURL)
	r.Sample = true
	return r
}
