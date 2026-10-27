package agents

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShivaliWaghmare/sentinel/server/internal/sse"
	"github.com/ShivaliWaghmare/sentinel/server/internal/store"
)

type testEnv struct {
	sup    *Supervisor
	store  *store.Store
	app    store.App
	opts   Options
	doneCh chan store.AgentRun
}

// newTestEnv builds a supervisor whose "CLI" is a shell script with body.
// The script gets the usual arguments: <kind> --input <file>.
func newTestEnv(t *testing.T, body string, mutate func(*Options)) testEnv {
	t.Helper()
	dataDir := t.TempDir()
	st, err := store.Open(context.Background(), dataDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	app, err := st.CreateApp(context.Background(), store.NewApp{
		Name: "ShopLite", Slug: "shoplite", BaseURL: "http://localhost:4000", Notes: "Login: demo / demo123",
	})
	if err != nil {
		t.Fatalf("CreateApp: %v", err)
	}

	script := filepath.Join(t.TempDir(), "fake-cli.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	opts := Options{
		AgentsDir:   t.TempDir(),
		DataDir:     dataDir,
		Concurrency: 1,
		Timeout:     time.Minute,
		KillGrace:   time.Second,
		CancelGrace: 5 * time.Second,
		ExitGrace:   5 * time.Second,
		Command:     []string{"/bin/sh", script},
	}
	if mutate != nil {
		mutate(&opts)
	}

	ctx, cancel := context.WithCancel(context.Background())
	sup := New(ctx, st, sse.NewBroker(), opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	doneCh := make(chan store.AgentRun, 10)
	sup.OnDone(store.AgentProbe, func(_ context.Context, run store.AgentRun) { doneCh <- run })
	t.Cleanup(func() {
		cancel()
		sup.Wait()
		st.Close()
	})
	return testEnv{sup: sup, store: st, app: app, opts: opts, doneCh: doneCh}
}

// waitDone returns the next run reported to the OnDone callback.
func (e testEnv) waitDone(t *testing.T) store.AgentRun {
	t.Helper()
	select {
	case run := <-e.doneCh:
		return run
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for an agent run to finish")
		return store.AgentRun{}
	}
}

func (e testEnv) start(t *testing.T) store.AgentRun {
	t.Helper()
	run, err := e.sup.Start(context.Background(), store.AgentProbe, e.app.ID, nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return run
}

func (e testEnv) events(t *testing.T, id int64) []store.AgentEvent {
	t.Helper()
	evs, err := e.store.ListAgentEvents(context.Background(), id, 0)
	if err != nil {
		t.Fatalf("ListAgentEvents: %v", err)
	}
	return evs
}

func eventTypesOf(evs []store.AgentEvent) []string {
	out := make([]string, len(evs))
	for i, ev := range evs {
		out[i] = ev.Type
	}
	return out
}

// checkSeq fails unless seq is 1, 2, 3, ... and the last event is done.
func checkSeq(t *testing.T, evs []store.AgentEvent) {
	t.Helper()
	for i, ev := range evs {
		if ev.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d; all: %v", i, ev.Seq, eventTypesOf(evs))
		}
	}
	if len(evs) == 0 || evs[len(evs)-1].Type != "done" {
		t.Fatalf("last event is not done: %v", eventTypesOf(evs))
	}
}

const runStarted = `{"seq":1,"ts":"2026-10-27T10:00:00Z","type":"run_started","data":{"agent":"probe","input_summary":{},"prompts":{}}}`

func TestSupervisorRecordsEvents(t *testing.T) {
	env := newTestEnv(t, `
cp "$3" "$(dirname "$3")/seen-input.json"
echo "args: $*" >&2
echo '`+runStarted+`'
echo 'this is not json'
echo '{"seq":2,"ts":"2026-10-27T10:00:01Z","type":"llm_call","data":{"model":"m","purpose":"probe","tokens_in":120,"tokens_out":30,"cost_usd":0.002,"latency_ms":900}}'
echo '{"seq":3,"ts":"2026-10-27T10:00:02Z","type":"mystery","data":{}}'
echo '{"seq":4,"ts":"2026-10-27T10:00:03Z","type":"llm_call","data":{"model":"m","purpose":"probe","tokens_in":80,"tokens_out":20,"cost_usd":0.001,"latency_ms":700}}'
echo '{"seq":5,"ts":"2026-10-27T10:00:04Z","type":"done","data":{"status":"succeeded","summary":{"app_summary":"A shop"}}}'
echo '{"seq":6,"ts":"2026-10-27T10:00:05Z","type":"phase","data":{"name":"late"}}'
`, nil)

	run := env.start(t)
	if run.Status != store.AgentRunning {
		t.Fatalf("status = %s, want running", run.Status)
	}
	got := env.waitDone(t)

	if got.Status != store.AgentSucceeded || got.Error != "" {
		t.Errorf("status = %s, error %q; want succeeded", got.Status, got.Error)
	}
	if got.SummaryJSON != `{"app_summary":"A shop"}` {
		t.Errorf("summary = %s", got.SummaryJSON)
	}
	if got.Steps != 2 || got.TokensIn != 200 || got.TokensOut != 50 || got.CostUSD < 0.00299 || got.CostUSD > 0.00301 {
		t.Errorf("usage = steps %d, in %d, out %d, cost %v", got.Steps, got.TokensIn, got.TokensOut, got.CostUSD)
	}

	evs := env.events(t, run.ID)
	want := []string{"run_started", "warning", "llm_call", "warning", "llm_call", "done", "warning"}
	if strings.Join(eventTypesOf(evs), ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", eventTypesOf(evs), want)
	}
	for i, ev := range evs {
		if ev.Seq != int64(i+1) {
			t.Errorf("event %d seq = %d", i, ev.Seq)
		}
	}
	if !strings.Contains(evs[1].PayloadJSON, "not JSON") || !strings.Contains(evs[3].PayloadJSON, "mystery") {
		t.Errorf("warnings = %s / %s", evs[1].PayloadJSON, evs[3].PayloadJSON)
	}
	if !strings.Contains(evs[6].PayloadJSON, "after done") {
		t.Errorf("late event warning = %s", evs[6].PayloadJSON)
	}

	// input.json has the run and app fields the CLI expects.
	data, err := os.ReadFile(filepath.Join(RunDir(env.opts.DataDir, run.ID), "seen-input.json"))
	if err != nil {
		t.Fatalf("read input copy: %v", err)
	}
	var input struct {
		AgentRunID int64  `json:"agent_run_id"`
		RunDir     string `json:"run_dir"`
		App        struct {
			Slug, BaseURL, Notes string
		} `json:"app"`
	}
	if err := json.Unmarshal(data, &input); err != nil {
		t.Fatalf("decode input: %v", err)
	}
	if input.AgentRunID != run.ID || input.RunDir != RunDir(env.opts.DataDir, run.ID) || input.App.Slug != "shoplite" {
		t.Errorf("input = %s", data)
	}
	log, _ := os.ReadFile(filepath.Join(RunDir(env.opts.DataDir, run.ID), LogFile))
	if !strings.Contains(string(log), "args: probe --input ") {
		t.Errorf("agent.log = %q, want the CLI arguments", log)
	}
}

func TestSupervisorRunIsFinishedWhenDoneIsPublished(t *testing.T) {
	// A client that reacts to done (like curl -N, then GET the run) must see
	// the final status, even while the CLI is still exiting.
	env := newTestEnv(t, `
echo '`+runStarted+`'
echo '{"seq":2,"ts":"2026-10-27T10:00:01Z","type":"done","data":{"status":"failed","summary":{},"error":"quota exceeded"}}'
sleep 1
`, nil)

	sub := env.sup.broker.Subscribe(1) // the first run gets ID 1
	defer env.sup.broker.Unsubscribe(sub)
	run := env.start(t)
	for ev := range sub.C {
		if ev.Type != "done" {
			continue
		}
		got, err := env.store.GetAgentRun(context.Background(), run.ID)
		if err != nil {
			t.Fatalf("GetAgentRun: %v", err)
		}
		if got.Status != store.AgentFailed || got.Error != "quota exceeded" {
			t.Errorf("run when done was published = %s %q; want failed with the done's error", got.Status, got.Error)
		}
		break
	}
	if got := env.waitDone(t); got.Status != store.AgentFailed {
		t.Errorf("callback status = %s", got.Status)
	}
}

func TestSupervisorExitWithoutDone(t *testing.T) {
	env := newTestEnv(t, `
echo '`+runStarted+`'
echo 'TypeError: cannot read properties of undefined' >&2
exit 3
`, nil)

	run := env.start(t)
	got := env.waitDone(t)

	if got.Status != store.AgentFailed {
		t.Errorf("status = %s, want failed", got.Status)
	}
	for _, want := range []string{"without a done event", "exit status 3", "TypeError"} {
		if !strings.Contains(got.Error, want) {
			t.Errorf("error = %q, want it to contain %q", got.Error, want)
		}
	}
	evs := env.events(t, run.ID)
	checkSeq(t, evs)
	if !strings.Contains(evs[len(evs)-1].PayloadJSON, `"status":"failed"`) {
		t.Errorf("synthesized done = %s", evs[len(evs)-1].PayloadJSON)
	}
}

func TestSupervisorCancel(t *testing.T) {
	env := newTestEnv(t, `
trap 'echo "{\"seq\":2,\"ts\":\"2026-10-27T10:00:01Z\",\"type\":\"done\",\"data\":{\"status\":\"cancelled\",\"summary\":{}}}"; exit 0' TERM
echo '`+runStarted+`'
while true; do sleep 0.05; done
`, nil)

	run := env.start(t)
	waitForEvents(t, env, run.ID, 1)
	if _, err := env.sup.Cancel(context.Background(), run.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	got := env.waitDone(t)

	if got.Status != store.AgentCancelled {
		t.Errorf("status = %s, want cancelled", got.Status)
	}
	evs := env.events(t, run.ID)
	checkSeq(t, evs)
	if len(evs) != 2 {
		t.Errorf("events = %v, want run_started and the CLI's done", eventTypesOf(evs))
	}
	if _, err := env.sup.Cancel(context.Background(), run.ID); err != ErrFinished {
		t.Errorf("second Cancel err = %v, want ErrFinished", err)
	}
}

func TestSupervisorCancelKillsAfterGrace(t *testing.T) {
	env := newTestEnv(t, `
trap '' TERM
echo '`+runStarted+`'
while true; do sleep 0.05; done
`, func(o *Options) { o.CancelGrace = 200 * time.Millisecond })

	run := env.start(t)
	waitForEvents(t, env, run.ID, 1)
	start := time.Now()
	if _, err := env.sup.Cancel(context.Background(), run.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	got := env.waitDone(t)

	if got.Status != store.AgentCancelled {
		t.Errorf("status = %s, want cancelled", got.Status)
	}
	if d := time.Since(start); d < 200*time.Millisecond {
		t.Errorf("finished after %s, before the cancel grace", d)
	}
	checkSeq(t, env.events(t, run.ID))
}

func TestSupervisorTimeoutKillsGroup(t *testing.T) {
	env := newTestEnv(t, `
echo '`+runStarted+`'
while true; do sleep 0.05; done
`, func(o *Options) {
		o.Timeout = 100 * time.Millisecond
		o.KillGrace = 100 * time.Millisecond
	})

	run := env.start(t)
	got := env.waitDone(t)

	if got.Status != store.AgentFailed || !strings.Contains(got.Error, "agent killed") {
		t.Errorf("status = %s, error %q; want failed with agent killed", got.Status, got.Error)
	}
	checkSeq(t, env.events(t, run.ID))
}

func TestSupervisorDoesNotWaitForStrayChildren(t *testing.T) {
	// A child that keeps stdout open (like a browser) must not keep the run
	// open after the CLI exits: it is killed after ExitGrace.
	env := newTestEnv(t, `
sleep 30 &
echo '`+runStarted+`'
echo '{"seq":2,"ts":"2026-10-27T10:00:01Z","type":"done","data":{"status":"succeeded","summary":{}}}'
`, func(o *Options) { o.ExitGrace = 200 * time.Millisecond })

	start := time.Now()
	env.start(t)
	got := env.waitDone(t)

	if got.Status != store.AgentSucceeded {
		t.Errorf("status = %s, want succeeded", got.Status)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("run took %s; the stray child kept it open", d)
	}
}

func TestSupervisorQueuesBeyondConcurrency(t *testing.T) {
	// Each run waits for a file named after its input before finishing.
	gate := t.TempDir()
	env := newTestEnv(t, `
echo '`+runStarted+`'
id=$(basename "$(dirname "$3")")
while [ ! -f "`+gate+`/$id" ]; do sleep 0.02; done
echo '{"seq":2,"ts":"2026-10-27T10:00:01Z","type":"done","data":{"status":"succeeded","summary":{}}}'
`, nil)
	release := func(id int64) {
		if err := os.WriteFile(filepath.Join(gate, itoa(id)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	first := env.start(t)
	second := env.start(t)
	third := env.start(t)
	if first.Status != store.AgentRunning || second.Status != store.AgentQueued || third.Status != store.AgentQueued {
		t.Fatalf("statuses = %s, %s, %s; want running, queued, queued", first.Status, second.Status, third.Status)
	}

	// A queued run is cancelled at once, with a done event for SSE clients.
	cancelled, err := env.sup.Cancel(context.Background(), third.ID)
	if err != nil || cancelled.Status != store.AgentCancelled {
		t.Fatalf("Cancel queued = %s, %v", cancelled.Status, err)
	}
	if got := env.waitDone(t); got.ID != third.ID {
		t.Fatalf("first finished run = %d, want the cancelled %d", got.ID, third.ID)
	}
	checkSeq(t, env.events(t, third.ID))

	release(first.ID)
	if got := env.waitDone(t); got.ID != first.ID || got.Status != store.AgentSucceeded {
		t.Fatalf("finished = %d %s, want %d succeeded", got.ID, got.Status, first.ID)
	}
	// The second run starts once the slot is free.
	waitForEvents(t, env, second.ID, 1)
	release(second.ID)
	if got := env.waitDone(t); got.ID != second.ID || got.Status != store.AgentSucceeded || got.StartedAt == nil {
		t.Fatalf("finished = %+v, want %d succeeded", got, second.ID)
	}
}

func TestSupervisorStartUnknownApp(t *testing.T) {
	env := newTestEnv(t, "exit 0\n", nil)
	if _, err := env.sup.Start(context.Background(), store.AgentProbe, 999, nil); err == nil {
		t.Error("Start for an unknown app succeeded")
	}
	if _, err := env.sup.Cancel(context.Background(), 999); err == nil {
		t.Error("Cancel for an unknown run succeeded")
	}
}

func TestSupervisorRecover(t *testing.T) {
	env := newTestEnv(t, "exit 0\n", nil)
	ctx := context.Background()
	stuck, err := env.store.CreateAgentRun(ctx, env.app.ID, store.AgentProbe, store.AgentRunning, "{}")
	if err != nil {
		t.Fatalf("CreateAgentRun: %v", err)
	}
	if err := env.sup.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	got, _ := env.store.GetAgentRun(ctx, stuck.ID)
	if got.Status != store.AgentFailed || got.Error != "server restarted" {
		t.Errorf("after Recover = %s %q", got.Status, got.Error)
	}
}

// waitForEvents waits until the run has at least n stored events.
func waitForEvents(t *testing.T, env testEnv, id int64, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(env.events(t, id)) >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run %d: fewer than %d events after 10 s", id, n)
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
