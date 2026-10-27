// Package runner runs a Playwright suite as a child process and parses its
// JSON report (lifecycle 1, step 2 in ARCHITECTURE.md).
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Files and folders inside a run folder (DATA_DIR/runs/<id>/).
const (
	ReportFile   = "report.json"
	LogFile      = "playwright.log"
	ArtifactsDir = "artifacts"
)

// tailBytes is how much of the log goes into an error message.
const tailBytes = 4 << 10

// Spec describes one run.
type Spec struct {
	Slug    string // runs tests/<slug> when File is empty
	File    string // optional single spec file, relative to AGENTS_DIR
	BaseURL string
	RunDir  string // absolute; created if missing
}

// Runner spawns Playwright in AGENTS_DIR.
type Runner struct {
	AgentsDir string // absolute
	DataDir   string // absolute; artifact paths are made relative to it
	Timeout   time.Duration
	// Command is the program and leading arguments; the target and
	// --reporter=json are appended. Tests replace it with a shell script.
	Command []string
}

// New returns a Runner that uses `npx playwright test`.
func New(agentsDir, dataDir string, timeout time.Duration) *Runner {
	return &Runner{
		AgentsDir: agentsDir,
		DataDir:   dataDir,
		Timeout:   timeout,
		Command:   []string{"npx", "playwright", "test"},
	}
}

// Run executes the suite and returns its parsed report. A non-zero exit with
// a report is a normal failing run and returns no error. It returns an error
// when the run timed out, ctx was cancelled, there is no report, or the
// report has no tests. Combined stdout and stderr go to RunDir/playwright.log.
func (r *Runner) Run(ctx context.Context, spec Spec) (Report, error) {
	if err := os.MkdirAll(spec.RunDir, 0o755); err != nil {
		return Report{}, fmt.Errorf("create run dir: %w", err)
	}
	logPath := filepath.Join(spec.RunDir, LogFile)
	logFile, err := os.Create(logPath)
	if err != nil {
		return Report{}, fmt.Errorf("create log file: %w", err)
	}
	defer logFile.Close()

	target := spec.File
	if target == "" {
		target = "tests/" + spec.Slug
	}
	reportPath := filepath.Join(spec.RunDir, ReportFile)

	runCtx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	args := append(append([]string{}, r.Command[1:]...), target, "--reporter=json")
	cmd := exec.CommandContext(runCtx, r.Command[0], args...)
	cmd.Dir = r.AgentsDir
	cmd.Env = append(os.Environ(),
		"BASE_URL="+spec.BaseURL,
		"PLAYWRIGHT_JSON_OUTPUT_NAME="+reportPath,
		"PW_OUTPUT_DIR="+filepath.Join(spec.RunDir, ArtifactsDir),
	)
	// Writing straight to a file (not a pipe) means Wait never blocks on
	// browser processes that inherited stdout.
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Own process group, so a timeout kills npx, node and every browser.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	runErr := cmd.Run()
	switch {
	case ctx.Err() != nil:
		return Report{}, fmt.Errorf("run cancelled: %w", ctx.Err())
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return Report{}, fmt.Errorf("run timed out after %s", r.Timeout)
	}
	var exitErr *exec.ExitError
	if runErr != nil && !errors.As(runErr, &exitErr) {
		// The command could not start at all (for example, npx not on PATH).
		return Report{}, fmt.Errorf("start playwright in %s: %w", r.AgentsDir, runErr)
	}

	data, err := os.ReadFile(reportPath)
	if errors.Is(err, fs.ErrNotExist) {
		return Report{}, fmt.Errorf("playwright wrote no report (%s); output tail:\n%s",
			exitDescription(runErr), tail(logPath))
	}
	if err != nil {
		return Report{}, fmt.Errorf("read report: %w", err)
	}

	rep, err := ParseReport(data, r.AgentsDir, r.DataDir)
	if err != nil {
		return Report{}, err
	}
	if len(rep.Results) == 0 {
		if len(rep.Errors) > 0 {
			return Report{}, fmt.Errorf("no tests ran: %s", rep.Errors[0])
		}
		return Report{}, fmt.Errorf("no tests found in %s", target)
	}
	if err := r.saveInline(spec.RunDir, rep.Results); err != nil {
		return Report{}, err
	}
	return rep, nil
}

// saveInline writes attachments that were embedded in the report to files,
// so every artifact is a path the API can serve. They go next to the
// screenshot when there is one, else to artifacts/inline-<n>/.
func (r *Runner) saveInline(runDir string, results []Result) error {
	for i := range results {
		res := &results[i]
		if len(res.inline) == 0 {
			continue
		}
		dir := filepath.Join(runDir, ArtifactsDir, fmt.Sprintf("inline-%d", i))
		if shot, ok := res.Artifacts["screenshot"]; ok {
			dir = filepath.Dir(filepath.Join(r.DataDir, filepath.FromSlash(shot)))
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("save attachments: %w", err)
		}
		for _, a := range res.inline {
			path := filepath.Join(dir, inlineFileName(a.kind))
			if err := os.WriteFile(path, a.body, 0o644); err != nil {
				return fmt.Errorf("save attachment: %w", err)
			}
			rel, err := filepath.Rel(r.DataDir, path)
			if err != nil {
				return fmt.Errorf("save attachment: %w", err)
			}
			res.Artifacts[a.kind] = filepath.ToSlash(rel)
		}
		res.inline = nil
	}
	return nil
}

// inlineFileName gives the file name Playwright attached the body under.
func inlineFileName(kind string) string {
	for name, k := range attachmentKinds {
		if k == kind {
			return name
		}
	}
	return kind + ".txt"
}

func exitDescription(err error) string {
	if err == nil {
		return "exit code 0"
	}
	return err.Error()
}

// tail returns the last tailBytes of the file at path, or a note if it
// can't be read.
func tail(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return "(no output)"
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "(no output)"
	}
	if info.Size() > tailBytes {
		if _, err := f.Seek(-tailBytes, io.SeekEnd); err != nil {
			return "(no output)"
		}
	}
	b, err := io.ReadAll(f)
	if err != nil || len(b) == 0 {
		return "(no output)"
	}
	return stripANSI(strings.TrimSpace(string(b)))
}
