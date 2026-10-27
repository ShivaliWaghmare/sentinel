package notify

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ShivaliWaghmare/sentinel/server/internal/config"
	"github.com/ShivaliWaghmare/sentinel/server/internal/store"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// countingWebhook stands in for Slack and counts the messages it receives.
func countingWebhook(t *testing.T, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func TestNewEnablesConfiguredChannels(t *testing.T) {
	n := New(config.Config{}, discardLogger())
	if n.slack != nil || n.email != nil {
		t.Error("channels enabled without configuration")
	}

	n = New(config.Config{
		SlackWebhookURL: "https://hooks.example/x",
		SMTPHost:        "smtp.gmail.com", SMTPPort: 587, SMTPUser: "me@gmail.com", SMTPPass: "pw",
		EmailTo: "a@x.com, b@y.com",
	}, discardLogger())
	if n.slack == nil || n.email == nil {
		t.Fatal("configured channels not enabled")
	}
	if n.email.from != "me@gmail.com" || len(n.email.to) != 2 {
		t.Errorf("email from = %q, to = %v; want SMTP_USER as sender and two recipients", n.email.from, n.email.to)
	}
}

func TestRunFinishedRespectsNotifyOn(t *testing.T) {
	app, failed, results := failedRun()
	passed := failed
	passed.Status = store.RunPassed
	errored := failed
	errored.Status = store.RunError

	tests := []struct {
		notifyOn string
		run      store.Run
		want     int32
	}{
		{config.NotifyAll, passed, 1},
		{config.NotifyAll, failed, 1},
		{config.NotifyAll, errored, 1},
		{config.NotifyFailures, passed, 0},
		{config.NotifyFailures, failed, 1},
		{config.NotifyFailures, errored, 1},
	}
	for _, tt := range tests {
		t.Run(tt.notifyOn+"/"+tt.run.Status, func(t *testing.T) {
			srv, count := countingWebhook(t, http.StatusOK)
			n := New(config.Config{SlackWebhookURL: srv.URL, NotifyOn: tt.notifyOn}, discardLogger())
			if err := n.RunFinished(context.Background(), app, tt.run, results); err != nil {
				t.Fatalf("RunFinished: %v", err)
			}
			if got := count.Load(); got != tt.want {
				t.Errorf("messages = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestRunFinishedReturnsChannelErrors(t *testing.T) {
	srv, _ := countingWebhook(t, http.StatusInternalServerError)
	n := New(config.Config{SlackWebhookURL: srv.URL, NotifyOn: config.NotifyAll}, discardLogger())
	app, run, results := failedRun()
	err := n.RunFinished(context.Background(), app, run, results)
	if err == nil || !strings.HasPrefix(err.Error(), "slack:") {
		t.Errorf("err = %v, want a slack error", err)
	}
}

func TestSendTest(t *testing.T) {
	srv, count := countingWebhook(t, http.StatusOK)
	// NOTIFY_ON=failures doesn't apply to test notifications.
	n := New(config.Config{SlackWebhookURL: srv.URL, NotifyOn: config.NotifyFailures}, discardLogger())

	slack, email := n.SendTest(context.Background())
	if slack != (ChannelResult{Configured: true, OK: true}) {
		t.Errorf("slack = %+v, want configured and ok", slack)
	}
	if email != (ChannelResult{}) {
		t.Errorf("email = %+v, want not configured", email)
	}
	if count.Load() != 1 {
		t.Errorf("messages = %d, want 1", count.Load())
	}
}
