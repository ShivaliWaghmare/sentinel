package runner

import (
	"os"
	"strings"
	"testing"
)

// The fixtures are real Playwright 1.64 reports with machine paths replaced:
// AGENTS_DIR is /AGENTS and the run folder is /DATA/runs/1.
//   - all-passing.json: the ShopLite manual suite, default mode.
//   - failure-and-flaky.json: cart.spec.ts with BUGS=cart_badge, plus a
//     temporary generated spec that failed only on its first attempt.
const (
	fixtureAgentsDir = "/AGENTS"
	fixtureDataDir   = "/DATA"
)

func parseFixture(t *testing.T, name string) Report {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := ParseReport(data, fixtureAgentsDir, fixtureDataDir)
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	return rep
}

func TestParseReportAllPassing(t *testing.T) {
	rep := parseFixture(t, "all-passing.json")
	if len(rep.Errors) != 0 {
		t.Errorf("Errors = %v, want none", rep.Errors)
	}
	if len(rep.Results) != 8 {
		t.Fatalf("got %d results, want 8", len(rep.Results))
	}
	for _, r := range rep.Results {
		if r.Status != "passed" || r.Error != "" || r.Retries != 0 || len(r.Artifacts) != 0 || len(r.inline) != 0 {
			t.Errorf("result %+v, want a clean pass", r)
		}
		if !strings.HasPrefix(r.File, "tests/shoplite/manual/") || r.Line == 0 || r.DurationMS <= 0 {
			t.Errorf("result %q: file %q line %d duration %d", r.Title, r.File, r.Line, r.DurationMS)
		}
	}
	first := rep.Results[0]
	if first.Title != "demo user can log in" || first.File != "tests/shoplite/manual/auth.spec.ts" || first.Line != 4 {
		t.Errorf("first result = %q %s:%d", first.Title, first.File, first.Line)
	}
}

func TestParseReportFailureAndFlaky(t *testing.T) {
	rep := parseFixture(t, "failure-and-flaky.json")
	byTitle := map[string]Result{}
	for _, r := range rep.Results {
		byTitle[r.Title] = r
	}
	if len(byTitle) != 3 {
		t.Fatalf("titles = %v, want 3 results", byTitle)
	}

	tests := []struct {
		title          string
		file           string
		status         string
		retries        int
		errorContains  string
		screenshotPath string
	}{
		{
			title:          "adding products updates the cart badge",
			file:           "tests/shoplite/manual/cart.spec.ts",
			status:         "failed",
			retries:        1,
			errorContains:  `expect(locator).toHaveText(expected) failed`,
			screenshotPath: "runs/1/artifacts/shoplite-manual-cart-adding-products-updates-the-cart-badge-chromium-retry1/test-failed-1.png",
		},
		{
			title:   "removing a product empties the cart",
			file:    "tests/shoplite/manual/cart.spec.ts",
			status:  "passed",
			retries: 0,
		},
		{
			// Nested in test.describe("Fixture"), so the title path has two parts.
			title:          "Fixture › fails on first attempt only",
			file:           "tests/fixturegen/generated/flaky.spec.ts",
			status:         "flaky",
			retries:        1,
			errorContains:  "first attempt fails on purpose",
			screenshotPath: "runs/1/artifacts/fixturegen-generated-flaky-99fc6-fails-on-first-attempt-only-chromium/test-failed-1.png",
		},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			r, ok := byTitle[tt.title]
			if !ok {
				t.Fatalf("no result titled %q", tt.title)
			}
			if r.File != tt.file || r.Status != tt.status || r.Retries != tt.retries {
				t.Errorf("got file %q status %q retries %d", r.File, r.Status, r.Retries)
			}
			if tt.errorContains == "" {
				if r.Error != "" || len(r.Artifacts) != 0 {
					t.Errorf("passing test has error %q artifacts %v", r.Error, r.Artifacts)
				}
				return
			}
			if !strings.Contains(r.Error, tt.errorContains) {
				t.Errorf("Error = %q, want it to contain %q", r.Error, tt.errorContains)
			}
			if strings.Contains(r.Error, "\x1b[") {
				t.Errorf("Error still has ANSI codes: %q", r.Error)
			}
			if r.Artifacts["screenshot"] != tt.screenshotPath {
				t.Errorf("screenshot = %q, want %q", r.Artifacts["screenshot"], tt.screenshotPath)
			}
			if !strings.HasSuffix(r.Artifacts["trace"], "/trace.zip") {
				t.Errorf("trace = %q", r.Artifacts["trace"])
			}
			if _, ok := r.Artifacts["error-context"]; ok {
				t.Error("error-context should be ignored")
			}
			// url.txt and aria-snapshot.txt are inlined in the report.
			inline := map[string]string{}
			for _, a := range r.inline {
				inline[a.kind] = string(a.body)
			}
			if !strings.HasPrefix(inline["url"], "http://localhost:4100/") {
				t.Errorf("inline url = %q", inline["url"])
			}
			if !strings.Contains(inline["aria_snapshot"], "- ") {
				t.Errorf("inline aria snapshot = %q", inline["aria_snapshot"])
			}
		})
	}
}

func TestParseReportFileErrors(t *testing.T) {
	data := `{"config":{"rootDir":"/AGENTS/tests"},"suites":[],
		"errors":[{"message":"\u001b[31mError:\u001b[39m Cannot find module './missing'"}]}`
	rep, err := ParseReport([]byte(data), fixtureAgentsDir, fixtureDataDir)
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	if len(rep.Results) != 0 || len(rep.Errors) != 1 || rep.Errors[0] != "Error: Cannot find module './missing'" {
		t.Errorf("report = %+v", rep)
	}
}

func TestParseReportInvalid(t *testing.T) {
	if _, err := ParseReport([]byte("not json"), fixtureAgentsDir, fixtureDataDir); err == nil {
		t.Error("want an error for invalid JSON")
	}
}

func TestDataRelative(t *testing.T) {
	p := parser{dataDir: "/DATA"}
	tests := []struct {
		path string
		want string
		ok   bool
	}{
		{"/DATA/runs/1/a.png", "runs/1/a.png", true},
		{"/DATA/../etc/passwd", "", false},
		{"/elsewhere/a.png", "", false},
		{"/DATA", ".", true},
	}
	for _, tt := range tests {
		got, ok := p.dataRelative(tt.path)
		if got != tt.want || ok != tt.ok {
			t.Errorf("dataRelative(%q) = %q, %v; want %q, %v", tt.path, got, ok, tt.want, tt.ok)
		}
	}
}

func TestTestStatus(t *testing.T) {
	tests := map[string]string{
		"expected": "passed", "unexpected": "failed", "flaky": "flaky", "skipped": "skipped",
	}
	for in, want := range tests {
		if got := testStatus(in); got != want {
			t.Errorf("testStatus(%q) = %q, want %q", in, got, want)
		}
	}
}
