package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// newScriptRunner returns a Runner whose command is a shell script instead
// of Playwright. The script sees the same environment; $0 is the target.
func newScriptRunner(t *testing.T, script string, timeout time.Duration) (*Runner, Spec) {
	t.Helper()
	dataDir := t.TempDir()
	r := &Runner{
		AgentsDir: t.TempDir(),
		DataDir:   dataDir,
		Timeout:   timeout,
		Command:   []string{"sh", "-c", script},
	}
	spec := Spec{Slug: "shoplite", BaseURL: "http://localhost:4000", RunDir: filepath.Join(dataDir, "runs", "1")}
	return r, spec
}

func TestRunFailingSuiteWithReport(t *testing.T) {
	fixture, err := filepath.Abs("testdata/failure-and-flaky.json")
	if err != nil {
		t.Fatal(err)
	}
	// Exit 1 like Playwright does when a test fails.
	r, spec := newScriptRunner(t, `cp "`+fixture+`" "$PLAYWRIGHT_JSON_OUTPUT_NAME"; exit 1`, 10*time.Second)

	rep, err := r.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rep.Results) != 3 {
		t.Fatalf("got %d results, want 3", len(rep.Results))
	}
	// The fixture's file paths are outside this DATA_DIR, so only the inline
	// attachments survive; they are written to artifacts/inline-<n>/.
	for _, res := range rep.Results {
		if res.Status == "passed" {
			continue
		}
		urlPath := res.Artifacts["url"]
		if !strings.HasPrefix(urlPath, "runs/1/artifacts/inline-") || res.Artifacts["screenshot"] != "" {
			t.Errorf("%q artifacts = %v", res.Title, res.Artifacts)
		}
		body, err := os.ReadFile(filepath.Join(r.DataDir, urlPath))
		if err != nil || !strings.HasPrefix(string(body), "http://localhost:4100/") {
			t.Errorf("url.txt = %q, %v", body, err)
		}
		if _, err := os.Stat(filepath.Join(r.DataDir, res.Artifacts["aria_snapshot"])); err != nil {
			t.Errorf("aria snapshot: %v", err)
		}
	}
}

func TestRunPassesEnvironmentAndTarget(t *testing.T) {
	r, spec := newScriptRunner(t,
		`echo "target=$0 reporter=$1 base=$BASE_URL out=$PW_OUTPUT_DIR"; exit 1`, 10*time.Second)
	spec.File = "tests/shoplite/manual/auth.spec.ts"

	_, err := r.Run(context.Background(), spec)
	if err == nil {
		t.Fatal("want an error: the script wrote no report")
	}
	want := "target=tests/shoplite/manual/auth.spec.ts reporter=--reporter=json base=http://localhost:4000 out=" +
		filepath.Join(spec.RunDir, ArtifactsDir)
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not contain %q", err, want)
	}
	if !strings.Contains(err.Error(), "playwright wrote no report (exit status 1)") {
		t.Errorf("error %q does not explain the missing report", err)
	}
	log, _ := os.ReadFile(filepath.Join(spec.RunDir, LogFile))
	if !strings.Contains(string(log), want) {
		t.Errorf("log = %q", log)
	}
}

func TestRunNoTests(t *testing.T) {
	r, spec := newScriptRunner(t,
		`echo '{"config":{"rootDir":"/AGENTS/tests"},"suites":[],"errors":[]}' > "$PLAYWRIGHT_JSON_OUTPUT_NAME"; exit 1`,
		10*time.Second)
	_, err := r.Run(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "no tests found in tests/shoplite") {
		t.Errorf("err = %v, want no tests found", err)
	}
}

// TestRunTimeoutKillsProcessGroup starts a background grandchild, like a
// browser, and checks that the timeout kills it too.
func TestRunTimeoutKillsProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	r, spec := newScriptRunner(t, `sleep 30 & echo $! > "`+pidFile+`"; wait`, 300*time.Millisecond)

	start := time.Now()
	_, err := r.Run(context.Background(), spec)
	if err == nil || err.Error() != "run timed out after 300ms" {
		t.Fatalf("err = %v, want timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Run took %s after the timeout", elapsed)
	}

	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse pid: %v", err)
	}
	// The orphaned sleep is reaped by init shortly after it is killed.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("grandchild %d still alive after the timeout", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestRunCancelled(t *testing.T) {
	r, spec := newScriptRunner(t, `sleep 30`, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)

	_, err := r.Run(ctx, spec)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	long := strings.Repeat("x", tailBytes) + "\x1b[31mlast line\x1b[39m\n"
	if err := os.WriteFile(path, []byte(long), 0o644); err != nil {
		t.Fatal(err)
	}
	got := tail(path)
	if len(got) > tailBytes || !strings.HasSuffix(got, "last line") {
		t.Errorf("tail = ...%q (len %d)", got[max(len(got)-20, 0):], len(got))
	}
	if tail(filepath.Join(t.TempDir(), "missing")) != "(no output)" {
		t.Error("missing file should give (no output)")
	}
}
