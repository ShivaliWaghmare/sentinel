package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite golden files in testdata/")

func TestSlackPayloadGolden(t *testing.T) {
	app, run, results := failedRun()
	got, err := json.MarshalIndent(buildSlackPayload(NewReport(app, run, results, "http://localhost:3000")), "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got = append(got, '\n')

	golden := filepath.Join("testdata", "slack_failed_run.golden.json")
	if *update {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (run with -update to create it): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("payload differs from %s (run go test ./internal/notify -update to accept):\n%s", golden, got)
	}
}

func TestSlackPayloadVariants(t *testing.T) {
	r := sampleReport("http://d")
	p := buildSlackPayload(r)
	// header, context, 10 failures, "and 2 more", actions
	if len(p.Blocks) != 14 {
		t.Fatalf("blocks = %d, want 14", len(p.Blocks))
	}
	more := p.Blocks[12].Elements[0].(slackText)
	if more.Text != "and 2 more failures" {
		t.Errorf("more line = %q", more.Text)
	}

	r = Report{AppName: strings.Repeat("A", 200), Status: "error", RunError: "no `report` <here>", RunURL: "http://d/runs/1"}
	p = buildSlackPayload(r)
	if n := len([]rune(p.Blocks[0].Text.Text)); n > slackHeaderMax {
		t.Errorf("header is %d chars, over Slack's %d", n, slackHeaderMax)
	}
	if got := p.Blocks[2].Text.Text; got != "*Run error*\n`no 'report' &lt;here&gt;`" {
		t.Errorf("run error section = %q", got)
	}
}

func TestSlackSend(t *testing.T) {
	var gotBody []byte
	var gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		io.WriteString(w, "ok")
	}))
	defer srv.Close()

	s := slackSender{webhookURL: srv.URL + "/services/T000/B000/secret", client: srv.Client()}
	if err := s.send(context.Background(), sampleReport("http://d")); err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotType != "application/json" {
		t.Errorf("Content-Type = %q", gotType)
	}
	var p slackPayload
	if err := json.Unmarshal(gotBody, &p); err != nil || len(p.Blocks) == 0 || !strings.Contains(p.Text, "ShopLite") {
		t.Errorf("body = %s (%v), want a Block Kit payload", gotBody, err)
	}
}

func TestSlackSendErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, "invalid_payload")
	}))
	defer srv.Close()

	s := slackSender{webhookURL: srv.URL, client: srv.Client()}
	err := s.send(context.Background(), sampleReport("http://d"))
	if err == nil || !strings.Contains(err.Error(), "400: invalid_payload") {
		t.Errorf("err = %v, want status and Slack's reason", err)
	}

	// Connection errors must not leak the webhook URL, which is a secret.
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	secretURL := closed.URL + "/services/T000/B000/secret"
	s = slackSender{webhookURL: secretURL, client: &http.Client{Timeout: time.Second}}
	err = s.send(context.Background(), sampleReport("http://d"))
	if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), closed.URL) {
		t.Errorf("err = %v, want an error without the webhook URL", err)
	}
}
