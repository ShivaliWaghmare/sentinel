package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShivaliWaghmare/sentinel/server/internal/agents"
	"github.com/ShivaliWaghmare/sentinel/server/internal/config"
	"github.com/ShivaliWaghmare/sentinel/server/internal/store"
)

// fakeAgentScript stands in for the agent CLI. It writes run_started, waits
// until AGENTS_DIR/release exists, then writes an llm_call and done. On
// SIGTERM it writes done with status cancelled.
const fakeAgentScript = `#!/bin/sh
trap 'echo "{\"seq\":9,\"ts\":\"2026-10-27T10:00:09Z\",\"type\":\"done\",\"data\":{\"status\":\"cancelled\",\"summary\":{}}}"; exit 0' TERM
echo '{"seq":1,"ts":"2026-10-27T10:00:00Z","type":"run_started","data":{"agent":"probe","input_summary":{},"prompts":{}}}'
while [ ! -f "$(dirname "$0")/release" ]; do sleep 0.02; done
echo '{"seq":2,"ts":"2026-10-27T10:00:01Z","type":"llm_call","data":{"model":"m","purpose":"probe","tokens_in":10,"tokens_out":5,"cost_usd":0.01,"latency_ms":1}}'
echo '{"seq":3,"ts":"2026-10-27T10:00:02Z","type":"done","data":{"status":"succeeded","summary":{"app_summary":"A small shop"}}}'
`

func fakeAgentOptions(t *testing.T, cfg config.Config) agents.Options {
	t.Helper()
	script := filepath.Join(cfg.AgentsDir, "fake-cli.sh")
	if err := os.WriteFile(script, []byte(fakeAgentScript), 0o755); err != nil {
		t.Fatalf("write fake agent: %v", err)
	}
	opts := agents.DefaultOptions(cfg.AgentsDir, cfg.DataDir, 1, time.Minute)
	opts.Command = []string{"/bin/sh", script}
	opts.ExitGrace = time.Second
	return opts
}

// releaseAgents lets every fake agent run finish.
func (e testEnv) releaseAgents(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.cfg.AgentsDir, "release"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// waitAgentRun polls until the run has finished.
func (e testEnv) waitAgentRun(t *testing.T, id int64) store.AgentRun {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		run, err := e.store.GetAgentRun(context.Background(), id)
		if err != nil {
			t.Fatalf("GetAgentRun: %v", err)
		}
		if run.Finished() {
			return run
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("agent run %d did not finish", id)
	return store.AgentRun{}
}

func (e testEnv) startProbe(t *testing.T, appID int64) agentRunJSON {
	t.Helper()
	rec := e.do(t, http.MethodPost, "/api/apps/"+itoa(appID)+"/probe", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST probe: status %d, body %s", rec.Code, rec.Body.String())
	}
	var run agentRunJSON
	decode(t, rec, &run)
	return run
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestStartProbeAndGetAgentRun(t *testing.T) {
	env := newTestEnv(t, nil)
	app := env.createApp(t, `{"name":"ShopLite","base_url":"http://localhost:4000"}`)

	run := env.startProbe(t, app.ID)
	if run.Kind != "probe" || run.Status != "running" || run.AppID != app.ID || string(run.Summary) != "null" {
		t.Errorf("started run = %+v", run)
	}
	queued := env.startProbe(t, app.ID)
	if queued.Status != "queued" {
		t.Errorf("second run status = %s, want queued (AGENT_CONCURRENCY 1)", queued.Status)
	}

	env.releaseAgents(t)
	env.waitAgentRun(t, run.ID)
	env.waitAgentRun(t, queued.ID)

	rec := env.do(t, http.MethodGet, "/api/agent-runs/"+itoa(run.ID), "")
	var got agentRunJSON
	decode(t, rec, &got)
	if got.Status != "succeeded" || got.Steps != 1 || got.TokensIn != 10 || got.TokensOut != 5 || got.CostUSD != 0.01 {
		t.Errorf("finished run = %+v", got)
	}
	if string(got.Summary) != `{"app_summary":"A small shop"}` || got.FinishedAt == nil || got.Error != nil {
		t.Errorf("summary %s, finished_at %v, error %v", got.Summary, got.FinishedAt, got.Error)
	}
	if string(got.Input) != "{}" {
		t.Errorf("input = %s, want {}", got.Input)
	}
}

func TestStartProbeErrors(t *testing.T) {
	env := newTestEnv(t, nil)
	app := env.createApp(t, `{"name":"ShopLite","base_url":"http://localhost:4000"}`)

	wantError(t, env.do(t, http.MethodPost, "/api/apps/999/probe", ""), http.StatusNotFound, "app not found")
	wantError(t, env.do(t, http.MethodPost, "/api/apps/x/probe", ""), http.StatusNotFound, "app not found")
	wantError(t, env.do(t, http.MethodPost, "/api/apps/"+itoa(app.ID)+"/probe", `{"max_pages":3}`),
		http.StatusBadRequest, "max_pages")
	wantError(t, env.do(t, http.MethodGet, "/api/agent-runs/999", ""), http.StatusNotFound, "agent run not found")
	wantError(t, env.do(t, http.MethodGet, "/api/agent-runs/999/events", ""), http.StatusNotFound, "agent run not found")
	wantError(t, env.do(t, http.MethodPost, "/api/agent-runs/999/cancel", ""), http.StatusNotFound, "agent run not found")

	if rec := env.do(t, http.MethodPost, "/api/apps/"+itoa(app.ID)+"/probe", "{}"); rec.Code != http.StatusAccepted {
		t.Errorf("POST probe with {}: status %d", rec.Code)
	}
}

func TestListAgentRuns(t *testing.T) {
	env := newTestEnv(t, nil)
	a := env.createApp(t, `{"name":"ShopLite","base_url":"http://localhost:4000"}`)
	b := env.createApp(t, `{"name":"Other","base_url":"http://localhost:4000"}`)
	ctx := context.Background()
	mk := func(appID int64, kind string) int64 {
		run, err := env.store.CreateAgentRun(ctx, appID, kind, store.AgentQueued, "{}")
		if err != nil {
			t.Fatal(err)
		}
		return run.ID
	}
	r1, r2, r3 := mk(a.ID, "probe"), mk(a.ID, "explore"), mk(b.ID, "probe")

	tests := []struct {
		query string
		want  []int64
	}{
		{"", []int64{r3, r2, r1}},
		{"?app_id=" + itoa(a.ID), []int64{r2, r1}},
		{"?kind=probe", []int64{r3, r1}},
		{"?app_id=" + itoa(a.ID) + "&kind=probe", []int64{r1}},
		{"?limit=1", []int64{r3}},
		{"?app_id=999", []int64{}},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			rec := env.do(t, http.MethodGet, "/api/agent-runs"+tt.query, "")
			var body map[string][]agentRunJSON
			decode(t, rec, &body)
			list, ok := body["agent_runs"]
			if !ok || list == nil {
				t.Fatalf("body = %s, want an agent_runs list", rec.Body.String())
			}
			var ids []int64
			for _, r := range list {
				ids = append(ids, r.ID)
			}
			if len(ids) != len(tt.want) {
				t.Fatalf("ids = %v, want %v", ids, tt.want)
			}
			for i := range ids {
				if ids[i] != tt.want[i] {
					t.Fatalf("ids = %v, want %v", ids, tt.want)
				}
			}
		})
	}

	for _, q := range []string{"?kind=dance", "?app_id=0", "?limit=101"} {
		if rec := env.do(t, http.MethodGet, "/api/agent-runs"+q, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", q, rec.Code)
		}
	}
}

func TestCancelAgentRun(t *testing.T) {
	env := newTestEnv(t, nil)
	app := env.createApp(t, `{"name":"ShopLite","base_url":"http://localhost:4000"}`)
	run := env.startProbe(t, app.ID)
	queued := env.startProbe(t, app.ID)

	// The queued run is cancelled at once.
	rec := env.do(t, http.MethodPost, "/api/agent-runs/"+itoa(queued.ID)+"/cancel", "")
	var got agentRunJSON
	if rec.Code != http.StatusAccepted {
		t.Fatalf("cancel queued: status %d", rec.Code)
	}
	decode(t, rec, &got)
	if got.Status != "cancelled" {
		t.Errorf("queued run after cancel = %s", got.Status)
	}

	// The running one gets SIGTERM and writes its own done.
	waitStoredEvents(t, env, run.ID, 1)
	if rec := env.do(t, http.MethodPost, "/api/agent-runs/"+itoa(run.ID)+"/cancel", ""); rec.Code != http.StatusAccepted {
		t.Fatalf("cancel running: status %d", rec.Code)
	}
	if finished := env.waitAgentRun(t, run.ID); finished.Status != store.AgentCancelled {
		t.Errorf("running run after cancel = %s", finished.Status)
	}
	wantError(t, env.do(t, http.MethodPost, "/api/agent-runs/"+itoa(run.ID)+"/cancel", ""),
		http.StatusConflict, "already finished")
}

func TestAgentRunEventsReplayAfterLastEventID(t *testing.T) {
	env := newTestEnv(t, nil)
	app := env.createApp(t, `{"name":"ShopLite","base_url":"http://localhost:4000"}`)
	run := env.startProbe(t, app.ID)
	env.releaseAgents(t)
	env.waitAgentRun(t, run.ID)

	tests := []struct {
		name    string
		header  string
		query   string
		wantIDs []int64
	}{
		{"everything", "", "", []int64{1, 2, 3}},
		{"after header id", "1", "", []int64{2, 3}},
		{"after query id", "", "?last_event_id=2", []int64{3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/agent-runs/"+itoa(run.ID)+"/events"+tt.query, nil)
			if tt.header != "" {
				req.Header.Set("Last-Event-ID", tt.header)
			}
			rec := httptest.NewRecorder()
			env.h.ServeHTTP(rec, req) // returns: the stream closes after done

			if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
				t.Errorf("Content-Type = %q", ct)
			}
			msgs := parseSSE(t, rec.Body.String())
			if len(msgs) != len(tt.wantIDs) {
				t.Fatalf("got %d messages, want %d:\n%s", len(msgs), len(tt.wantIDs), rec.Body.String())
			}
			for i, m := range msgs {
				if m.id != tt.wantIDs[i] || m.event.Seq != tt.wantIDs[i] {
					t.Errorf("message %d: id %d, seq %d; want %d", i, m.id, m.event.Seq, tt.wantIDs[i])
				}
			}
			last := msgs[len(msgs)-1].event
			if last.Type != "done" || !strings.Contains(string(last.Data), `"succeeded"`) {
				t.Errorf("last message = %+v", last)
			}
		})
	}

	wantError(t, env.do(t, http.MethodGet, "/api/agent-runs/"+itoa(run.ID)+"/events?last_event_id=x", ""),
		http.StatusBadRequest, "Last-Event-ID")
}

func TestAgentRunEventsStreamLive(t *testing.T) {
	env := newTestEnv(t, nil)
	app := env.createApp(t, `{"name":"ShopLite","base_url":"http://localhost:4000"}`)
	run := env.startProbe(t, app.ID)
	waitStoredEvents(t, env, run.ID, 1)

	srv := httptest.NewServer(env.h)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/agent-runs/" + itoa(run.ID) + "/events")
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer resp.Body.Close()

	// The stored run_started arrives before the agent is released...
	reader := bufio.NewReader(resp.Body)
	first := readSSEMessage(t, reader)
	if first.event.Type != "run_started" {
		t.Fatalf("first message = %+v", first.event)
	}
	// ...and the rest stream live, ending with done and a closed stream.
	env.releaseAgents(t)
	var types []string
	for {
		m := readSSEMessage(t, reader)
		types = append(types, m.event.Type)
		if m.event.Type == "done" {
			break
		}
	}
	if strings.Join(types, ",") != "llm_call,done" {
		t.Errorf("live types = %v", types)
	}
	if _, err := reader.ReadByte(); err == nil {
		t.Error("stream still open after done")
	}
}

type sseMessage struct {
	id    int64
	event struct {
		Seq  int64           `json:"seq"`
		TS   string          `json:"ts"`
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
}

func parseSSE(t *testing.T, body string) []sseMessage {
	t.Helper()
	var out []sseMessage
	r := bufio.NewReader(strings.NewReader(body))
	for {
		if _, err := r.Peek(1); err != nil {
			return out
		}
		out = append(out, readSSEMessage(t, r))
	}
}

// readSSEMessage reads one "id: / data:" message, skipping comments.
func readSSEMessage(t *testing.T, r *bufio.Reader) sseMessage {
	t.Helper()
	var m sseMessage
	gotData := false
	for {
		lineStr, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read sse: %v (partial %q)", err, lineStr)
		}
		lineStr = strings.TrimRight(lineStr, "\n")
		switch {
		case lineStr == "":
			if gotData {
				return m
			}
		case strings.HasPrefix(lineStr, ":"):
			// heartbeat comment
		case strings.HasPrefix(lineStr, "id: "):
			if err := json.Unmarshal([]byte(strings.TrimPrefix(lineStr, "id: ")), &m.id); err != nil {
				t.Fatalf("bad id line %q", lineStr)
			}
		case strings.HasPrefix(lineStr, "data: "):
			if err := json.Unmarshal([]byte(strings.TrimPrefix(lineStr, "data: ")), &m.event); err != nil {
				t.Fatalf("bad data line %q: %v", lineStr, err)
			}
			if _, err := time.Parse(time.RFC3339Nano, m.event.TS); err != nil {
				t.Errorf("ts %q is not RFC 3339", m.event.TS)
			}
			gotData = true
		default:
			t.Fatalf("unexpected sse line %q", lineStr)
		}
	}
}

func waitStoredEvents(t *testing.T, env testEnv, id int64, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		evs, err := env.store.ListAgentEvents(context.Background(), id, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(evs) >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("agent run %d: fewer than %d events", id, n)
}
