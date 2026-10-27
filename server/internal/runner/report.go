package runner

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Result is one test's outcome, ready to store as a test_results row.
type Result struct {
	Title      string // describe titles and the test title joined with " › "
	File       string // relative to AGENTS_DIR, slash-separated (tests/<slug>/...)
	Line       int
	Status     string // passed, failed, flaky or skipped
	DurationMS int64  // summed over all attempts
	Error      string // first error of the last failed attempt, ANSI codes stripped
	Retries    int
	// Artifacts maps screenshot, trace, url and aria_snapshot to paths
	// relative to DATA_DIR. Only set for failed and flaky tests.
	Artifacts map[string]string

	// inline holds attachments Playwright embedded in the report as base64
	// (our fixture attaches url.txt and aria-snapshot.txt with a body).
	// saveInline writes them to files and adds them to Artifacts.
	inline []inlineAttachment
}

type inlineAttachment struct {
	kind string // artifact kind: url or aria_snapshot
	body []byte
}

// Report is the parsed outcome of one Playwright run.
type Report struct {
	Results []Result
	// Errors are errors outside any test, such as a spec file that does not
	// compile. ANSI codes are stripped.
	Errors []string
}

// The subset of Playwright's JSON reporter output we read.
type jsonReport struct {
	Config struct {
		RootDir string `json:"rootDir"`
	} `json:"config"`
	Suites []jsonSuite `json:"suites"`
	Errors []jsonError `json:"errors"`
}

type jsonSuite struct {
	Title  string      `json:"title"`
	Specs  []jsonSpec  `json:"specs"`
	Suites []jsonSuite `json:"suites"`
}

type jsonSpec struct {
	Title string     `json:"title"`
	File  string     `json:"file"` // relative to config.rootDir
	Line  int        `json:"line"`
	Tests []jsonTest `json:"tests"`
}

type jsonTest struct {
	// expected, unexpected, flaky or skipped
	Status  string       `json:"status"`
	Results []jsonResult `json:"results"`
}

// jsonResult is one attempt. Status is passed, failed, timedOut, skipped or
// interrupted.
type jsonResult struct {
	Status      string           `json:"status"`
	Duration    float64          `json:"duration"`
	Errors      []jsonError      `json:"errors"`
	Attachments []jsonAttachment `json:"attachments"`
}

type jsonError struct {
	Message string `json:"message"`
}

type jsonAttachment struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Body string `json:"body"` // base64, when there is no path
}

// attachmentKinds maps Playwright attachment names to our artifact kinds.
// Other attachments (such as error-context) are ignored.
var attachmentKinds = map[string]string{
	"screenshot":        "screenshot",
	"trace":             "trace",
	"url.txt":           "url",
	"aria-snapshot.txt": "aria_snapshot",
}

// ParseReport turns a Playwright JSON report into results. agentsDir and
// dataDir must be absolute: file paths become relative to agentsDir and
// artifact paths relative to dataDir (artifacts outside dataDir are dropped).
func ParseReport(data []byte, agentsDir, dataDir string) (Report, error) {
	var jr jsonReport
	if err := json.Unmarshal(data, &jr); err != nil {
		return Report{}, fmt.Errorf("parse playwright report: %w", err)
	}

	rep := Report{Results: []Result{}}
	for _, e := range jr.Errors {
		rep.Errors = append(rep.Errors, stripANSI(e.Message))
	}
	p := parser{rootDir: jr.Config.RootDir, agentsDir: agentsDir, dataDir: dataDir}
	// Top-level suites are files; their title is the file path, which is not
	// part of the test's title path.
	for _, file := range jr.Suites {
		if err := p.suite(file, nil, &rep); err != nil {
			return Report{}, err
		}
	}
	return rep, nil
}

type parser struct {
	rootDir, agentsDir, dataDir string
}

func (p parser) suite(s jsonSuite, titles []string, rep *Report) error {
	for _, spec := range s.Specs {
		for _, t := range spec.Tests {
			r, err := p.result(spec, t, append(titles, spec.Title))
			if err != nil {
				return err
			}
			rep.Results = append(rep.Results, r)
		}
	}
	for _, child := range s.Suites {
		// Copy so sibling describes don't share a backing array.
		childTitles := append(append([]string{}, titles...), child.Title)
		if err := p.suite(child, childTitles, rep); err != nil {
			return err
		}
	}
	return nil
}

func (p parser) result(spec jsonSpec, t jsonTest, titles []string) (Result, error) {
	file, err := filepath.Rel(p.agentsDir, filepath.Join(p.rootDir, spec.File))
	if err != nil {
		return Result{}, fmt.Errorf("test file %q: %w", spec.File, err)
	}
	r := Result{
		Title:     strings.Join(titles, " › "),
		File:      filepath.ToSlash(file),
		Line:      spec.Line,
		Status:    testStatus(t.Status),
		Retries:   max(len(t.Results)-1, 0),
		Artifacts: map[string]string{},
	}

	// The last attempt that did not pass explains the failure. For a flaky
	// test that is the attempt before the passing retry.
	var failed *jsonResult
	for i := range t.Results {
		attempt := &t.Results[i]
		r.DurationMS += int64(attempt.Duration)
		if attempt.Status != "passed" && attempt.Status != "skipped" {
			failed = attempt
		}
	}
	if failed == nil || r.Status == "passed" || r.Status == "skipped" {
		return r, nil
	}

	if len(failed.Errors) > 0 {
		r.Error = stripANSI(failed.Errors[0].Message)
	}
	for _, a := range failed.Attachments {
		kind, ok := attachmentKinds[a.Name]
		if !ok {
			continue
		}
		if a.Path == "" {
			body, err := base64.StdEncoding.DecodeString(a.Body)
			if err != nil {
				return Result{}, fmt.Errorf("attachment %q of %q: %w", a.Name, r.Title, err)
			}
			r.inline = append(r.inline, inlineAttachment{kind: kind, body: body})
			continue
		}
		if _, seen := r.Artifacts[kind]; seen {
			continue // keep the first screenshot
		}
		if rel, ok := p.dataRelative(a.Path); ok {
			r.Artifacts[kind] = rel
		}
	}
	return r, nil
}

// dataRelative returns path relative to dataDir, or false if it is outside.
func (p parser) dataRelative(path string) (string, bool) {
	rel, err := filepath.Rel(p.dataDir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// testStatus maps Playwright's test outcome to our result status.
func testStatus(s string) string {
	switch s {
	case "expected":
		return "passed"
	case "flaky", "skipped":
		return s
	default: // unexpected
		return "failed"
	}
}

var ansiCodes = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

func stripANSI(s string) string {
	return ansiCodes.ReplaceAllString(s, "")
}
