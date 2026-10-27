// Package agents supervises agent runs (lifecycle 2 in ARCHITECTURE.md): it
// spawns the TypeScript agent CLI, reads its JSONL events from stdout,
// stores them, publishes them to SSE and records the outcome.
//
// Timeouts are layered (ARCHITECTURE.md, "Agent timeouts"): AGENT_TIMEOUT is
// the agent's own wall-clock budget, after which it ends itself with
// budget_exceeded. The supervisor kills the process group only if the agent
// is still running KillGrace (30 s) later.
package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/ShivaliWaghmare/sentinel/server/internal/sse"
	"github.com/ShivaliWaghmare/sentinel/server/internal/store"
)

// Files in an agent run folder (DATA_DIR/agent-runs/<id>/).
const (
	InputFile = "input.json"
	LogFile   = "agent.log" // the CLI's stderr
)

// ErrFinished is returned by Cancel for a run that already ended.
var ErrFinished = errors.New("agent run already finished")

// Options configures a Supervisor.
type Options struct {
	AgentsDir   string // absolute; the CLI runs here
	DataDir     string // absolute
	Concurrency int    // runs at the same time; more are queued
	Timeout     time.Duration
	// KillGrace is how long after Timeout the process group is killed.
	KillGrace time.Duration
	// CancelGrace is how long Cancel waits after SIGTERM before SIGKILL.
	CancelGrace time.Duration
	// ExitGrace is how long other members of the process group (the CLI
	// under npx, browsers) may keep stdout open after the leader exits.
	ExitGrace time.Duration
	// Command is the program and leading arguments; "<kind> --input <file>"
	// is appended. Tests replace it with a shell script.
	Command []string
}

// DefaultOptions returns the production settings around the given values.
func DefaultOptions(agentsDir, dataDir string, concurrency int, timeout time.Duration) Options {
	return Options{
		AgentsDir:   agentsDir,
		DataDir:     dataDir,
		Concurrency: concurrency,
		Timeout:     timeout,
		KillGrace:   30 * time.Second,
		CancelGrace: 10 * time.Second,
		ExitGrace:   10 * time.Second,
		Command:     []string{"npx", "tsx", "src/cli.ts"},
	}
}

// DoneFunc is called after a run of its kind finished and was stored.
type DoneFunc func(ctx context.Context, run store.AgentRun)

// Supervisor starts, queues, cancels and records agent runs.
type Supervisor struct {
	ctx    context.Context // cancelled when the server stops
	opts   Options
	store  *store.Store
	broker *sse.Broker
	logger *slog.Logger

	mu     sync.Mutex
	active map[int64]*process // running runs by ID
	onDone map[string][]DoneFunc
	wg     sync.WaitGroup
}

// process is the supervisor's handle on one running CLI.
type process struct {
	cancel     chan struct{} // closed by Cancel
	cancelOnce sync.Once
}

func (p *process) requestCancel() {
	p.cancelOnce.Do(func() { close(p.cancel) })
}

// New returns a Supervisor. Cancelling ctx kills running agents; their runs
// end as failed.
func New(ctx context.Context, st *store.Store, broker *sse.Broker, opts Options, logger *slog.Logger) *Supervisor {
	return &Supervisor{
		ctx: ctx, opts: opts, store: st, broker: broker, logger: logger,
		active: map[int64]*process{}, onDone: map[string][]DoneFunc{},
	}
}

// OnDone registers fn for runs of kind. Register callbacks before Start.
func (s *Supervisor) OnDone(kind string, fn DoneFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onDone[kind] = append(s.onDone[kind], fn)
}

// Recover marks agent runs left queued or running by a previous process as
// failed. Call it once at boot, before Start.
func (s *Supervisor) Recover(ctx context.Context) error {
	n, err := s.store.FailInterruptedAgentRuns(ctx, "server restarted")
	if err != nil {
		return err
	}
	if n > 0 {
		s.logger.Warn("marked interrupted agent runs as failed", "count", n)
	}
	return nil
}

// Wait blocks until every running agent has finished.
func (s *Supervisor) Wait() {
	s.wg.Wait()
}

// RunDir is where an agent run's input, log and artifacts live.
func RunDir(dataDir string, id int64) string {
	return filepath.Join(dataDir, "agent-runs", strconv.FormatInt(id, 10))
}

// Start creates an agent run for the app and starts it, or queues it when
// Concurrency runs are already active. input holds the kind-specific fields
// of input.json. It returns store.ErrNotFound for an unknown app.
func (s *Supervisor) Start(ctx context.Context, kind string, appID int64, input map[string]any) (store.AgentRun, error) {
	if !store.ValidAgentKind(kind) {
		return store.AgentRun{}, fmt.Errorf("unknown agent kind %q", kind)
	}
	app, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return store.AgentRun{}, err
	}
	if input == nil {
		input = map[string]any{}
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return store.AgentRun{}, fmt.Errorf("encode agent input: %w", err)
	}

	// The lock covers the slot check, the insert and the launch, so two
	// requests can't both take the last slot.
	s.mu.Lock()
	defer s.mu.Unlock()
	status := store.AgentQueued
	if len(s.active) < s.opts.Concurrency {
		status = store.AgentRunning
	}
	run, err := s.store.CreateAgentRun(ctx, appID, kind, status, string(inputJSON))
	if err != nil {
		return store.AgentRun{}, err
	}
	s.logger.Info("agent run created", "agent_run_id", run.ID, "kind", kind, "app", app.Slug, "status", status)
	if status == store.AgentRunning {
		s.launch(app, run)
	}
	return run, nil
}

// Cancel stops a run. A running agent gets SIGTERM (and SIGKILL after
// CancelGrace); its run ends when the process does. A queued run is
// cancelled at once. It returns store.ErrNotFound for an unknown run and
// ErrFinished for one that already ended.
func (s *Supervisor) Cancel(ctx context.Context, id int64) (store.AgentRun, error) {
	s.mu.Lock()
	if p, ok := s.active[id]; ok {
		p.requestCancel()
		s.mu.Unlock()
		return s.store.GetAgentRun(ctx, id)
	}
	// Holding mu keeps startQueued from starting the run meanwhile.
	finished, err := s.cancelQueued(ctx, id)
	s.mu.Unlock()
	if err != nil {
		return finished, err
	}
	s.callDone(context.WithoutCancel(s.ctx), finished)
	return finished, nil
}

// cancelQueued finishes a queued run as cancelled. Must be called with mu
// held.
func (s *Supervisor) cancelQueued(ctx context.Context, id int64) (store.AgentRun, error) {
	run, err := s.store.GetAgentRun(ctx, id)
	if err != nil {
		return store.AgentRun{}, err
	}
	if run.Status != store.AgentQueued {
		return run, ErrFinished
	}
	// It never started, so the supervisor writes its done event.
	rec := newRecorder(s, context.WithoutCancel(s.ctx), id)
	rec.synthesizeDone(store.AgentCancelled, "cancelled before it started")
	if rec.finished == nil {
		return store.AgentRun{}, fmt.Errorf("cancel agent run %d: done event not stored", id)
	}
	return *rec.finished, nil
}

// launch starts the CLI for run in the background. Must be called with mu
// held.
func (s *Supervisor) launch(app store.App, run store.AgentRun) {
	p := &process{cancel: make(chan struct{})}
	s.active[run.ID] = p
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.execute(app, run, p)
		s.release(run.ID)
	}()
}

// release frees the run's slot and starts queued runs.
func (s *Supervisor) release(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.active, id)
	s.startQueued()
}

// startQueued starts the oldest queued runs while slots are free. Must be
// called with mu held.
func (s *Supervisor) startQueued() {
	ctx := context.WithoutCancel(s.ctx)
	for len(s.active) < s.opts.Concurrency && s.ctx.Err() == nil {
		next, err := s.store.OldestQueuedAgentRun(ctx)
		if errors.Is(err, store.ErrNotFound) {
			return
		}
		if err != nil {
			s.logger.Error("find queued agent run", "err", err)
			return
		}
		run, err := s.store.StartQueuedAgentRun(ctx, next.ID)
		if errors.Is(err, store.ErrNotFound) {
			continue // cancelled meanwhile
		}
		if err != nil {
			s.logger.Error("start queued agent run", "agent_run_id", next.ID, "err", err)
			return
		}
		app, err := s.store.GetApp(ctx, run.AppID)
		if err != nil {
			s.logger.Error("start queued agent run: load app", "agent_run_id", run.ID, "err", err)
			if _, err := s.store.FinishAgentRun(ctx, run.ID, store.AgentFailed, "{}", "app not found"); err != nil {
				s.logger.Error("finish agent run", "agent_run_id", run.ID, "err", err)
			}
			continue
		}
		s.logger.Info("agent run dequeued", "agent_run_id", run.ID, "kind", run.Kind)
		s.launch(app, run)
	}
}

// execute runs the CLI to completion and stores the outcome.
func (s *Supervisor) execute(app store.App, run store.AgentRun, p *process) {
	// Writes after the run must happen even while the server is stopping.
	writeCtx := context.WithoutCancel(s.ctx)
	rec := newRecorder(s, writeCtx, run.ID)

	status, errMsg := s.runProcess(app, run, p, rec)
	// The CLI's done already finished the run; otherwise write one now.
	if rec.finished == nil {
		rec.synthesizeDone(status, errMsg)
	}
	if rec.finished == nil {
		s.logger.Error("agent run not finished: done event not stored", "agent_run_id", run.ID)
		return
	}
	finished := *rec.finished
	s.logger.Info("agent run finished", "agent_run_id", run.ID, "kind", run.Kind, "status", finished.Status,
		"steps", finished.Steps, "tokens_in", finished.TokensIn, "tokens_out", finished.TokensOut,
		"cost_usd", finished.CostUSD)
	s.callDone(writeCtx, finished)
}

func (s *Supervisor) callDone(ctx context.Context, run store.AgentRun) {
	s.mu.Lock()
	fns := append([]DoneFunc(nil), s.onDone[run.Kind]...)
	s.mu.Unlock()
	for _, fn := range fns {
		fn(ctx, run)
	}
}

// runProcess spawns the CLI and reads its events until it exits. It returns
// the status and error to record if the CLI wrote no done event.
func (s *Supervisor) runProcess(app store.App, run store.AgentRun, p *process, rec *recorder) (status, errMsg string) {
	dir := RunDir(s.opts.DataDir, run.ID)
	inputPath, err := writeInput(dir, app, run)
	if err != nil {
		return store.AgentFailed, err.Error()
	}
	logPath := filepath.Join(dir, LogFile)
	logFile, err := os.Create(logPath)
	if err != nil {
		return store.AgentFailed, fmt.Sprintf("create agent log: %v", err)
	}
	defer logFile.Close()

	// stdout is an os.Pipe we own (not cmd.StdoutPipe), so Wait returns when
	// the CLI exits even if a browser it started still holds the write end.
	pr, pw, err := os.Pipe()
	if err != nil {
		return store.AgentFailed, fmt.Sprintf("create stdout pipe: %v", err)
	}
	defer pr.Close()

	args := append(append([]string{}, s.opts.Command[1:]...), run.Kind, "--input", inputPath)
	cmd := exec.Command(s.opts.Command[0], args...)
	cmd.Dir = s.opts.AgentsDir
	cmd.Env = os.Environ()
	cmd.Stdout = pw
	cmd.Stderr = logFile
	// Own process group: signals reach npx, tsx, node and every browser.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		pw.Close()
		return store.AgentFailed, fmt.Sprintf("start agent in %s: %v", s.opts.AgentsDir, err)
	}
	pw.Close() // the child has its copy; EOF comes when every holder exits
	pgid := cmd.Process.Pid

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		readEvents(pr, rec, s.logger)
	}()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	reason, waitErr := s.watch(pgid, p, exited)

	// The leader exited. Let the rest of the group finish writing, then kill
	// whatever is left (a stuck browser, for example).
	select {
	case <-readDone:
	case <-time.After(s.opts.ExitGrace):
		killGroup(pgid, syscall.SIGKILL)
		<-readDone
	}
	killGroup(pgid, syscall.SIGKILL)

	switch reason {
	case "timeout":
		return store.AgentFailed, fmt.Sprintf("agent killed: still running %s after AGENT_TIMEOUT (%s)", s.opts.KillGrace, s.opts.Timeout)
	case "cancel":
		return store.AgentCancelled, "cancelled"
	case "shutdown":
		return store.AgentFailed, "server stopped during the agent run"
	}
	return store.AgentFailed, fmt.Sprintf("agent exited without a done event (%s); stderr tail:\n%s",
		exitDescription(waitErr), tailFile(logPath))
}

// watch waits for the leader to exit, sending signals on timeout, cancel
// and shutdown. reason says why the agent was signalled ("" if it wasn't).
func (s *Supervisor) watch(pgid int, p *process, exited <-chan error) (reason string, waitErr error) {
	timeout := time.NewTimer(s.opts.Timeout + s.opts.KillGrace)
	defer timeout.Stop()
	cancel := p.cancel
	shutdown := s.ctx.Done()
	var forceKill <-chan time.Time
	for {
		select {
		case err := <-exited:
			return reason, err
		case <-timeout.C:
			reason = "timeout"
			killGroup(pgid, syscall.SIGKILL)
		case <-cancel:
			cancel = nil
			if reason == "" {
				reason = "cancel"
			}
			killGroup(pgid, syscall.SIGTERM)
			forceKill = time.After(s.opts.CancelGrace)
		case <-forceKill:
			forceKill = nil
			killGroup(pgid, syscall.SIGKILL)
		case <-shutdown:
			shutdown = nil
			reason = "shutdown"
			killGroup(pgid, syscall.SIGKILL)
		}
	}
}

// writeInput writes input.json: the kind-specific input plus the fields
// every agent gets (ARCHITECTURE.md, "Agent input").
func writeInput(dir string, app store.App, run store.AgentRun) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create agent run dir: %w", err)
	}
	input := map[string]any{}
	if err := json.Unmarshal([]byte(run.InputJSON), &input); err != nil {
		return "", fmt.Errorf("decode agent input: %w", err)
	}
	input["agent_run_id"] = run.ID
	input["run_dir"] = dir
	input["app"] = map[string]any{
		"id":                app.ID,
		"name":              app.Name,
		"slug":              app.Slug,
		"base_url":          app.BaseURL,
		"notes":             app.Notes,
		"allow_destructive": app.AllowDestructive,
	}
	data, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode agent input: %w", err)
	}
	path := filepath.Join(dir, InputFile)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("write agent input: %w", err)
	}
	return path, nil
}

// killGroup signals every process in the group. A group that is already
// gone is not an error.
func killGroup(pgid int, sig syscall.Signal) {
	_ = syscall.Kill(-pgid, sig)
}

func exitDescription(err error) string {
	if err == nil {
		return "exit code 0"
	}
	return err.Error()
}
