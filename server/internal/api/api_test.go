package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ShivaliWaghmare/sentinel/server/internal/config"
	"github.com/ShivaliWaghmare/sentinel/server/internal/runner"
	"github.com/ShivaliWaghmare/sentinel/server/internal/runs"
	"github.com/ShivaliWaghmare/sentinel/server/internal/store"
)

const testOrigin = "http://localhost:3000"

type testEnv struct {
	h      http.Handler
	store  *store.Store
	cfg    config.Config
	runs   *runs.Service
	runner *fakeRunner
}

// fakeRunner stands in for Playwright. Run blocks until release is closed,
// so tests control how long a run stays active, then returns report.
type fakeRunner struct {
	release chan struct{}
	report  runner.Report
}

func (f *fakeRunner) Run(ctx context.Context, _ runner.Spec) (runner.Report, error) {
	select {
	case <-f.release:
		return f.report, nil
	case <-ctx.Done():
		return runner.Report{}, ctx.Err()
	}
}

// newTestEnv builds the full handler on a temporary DATA_DIR, database and
// AGENTS_DIR, with a runs service backed by a fakeRunner. mutate, if given,
// adjusts the config first.
func newTestEnv(t *testing.T, mutate func(*config.Config)) testEnv {
	t.Helper()
	cfg := config.Config{
		DataDir:     t.TempDir(),
		AgentsDir:   t.TempDir(),
		CORSOrigin:  testOrigin,
		LLMProvider: "gemini",
		SMTPHost:    "smtp.gmail.com",
	}
	if mutate != nil {
		mutate(&cfg)
	}
	st, err := store.Open(context.Background(), cfg.DataDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}

	fr := &fakeRunner{release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	svc := runs.New(ctx, st, fr, runs.NopNotifier{}, cfg.DataDir, discardLogger())
	// Stop runs the test left active before closing the database.
	t.Cleanup(func() {
		cancel()
		svc.Wait()
		st.Close()
	})
	return testEnv{h: New(cfg, st, svc, discardLogger()), store: st, cfg: cfg, runs: svc, runner: fr}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// do sends a request with an optional JSON body and returns the recorder.
func (e testEnv) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

// decode parses the response body into dst, failing the test on bad JSON.
func decode(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
}

// wantError checks the status and that {"error"} contains msg.
func wantError(t *testing.T, rec *httptest.ResponseRecorder, status int, msg string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, status, rec.Body.String())
	}
	var body errorResponse
	decode(t, rec, &body)
	if !strings.Contains(body.Error, msg) {
		t.Errorf("error = %q, want it to contain %q", body.Error, msg)
	}
}

func TestHealth(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config.Config)
		want   healthResponse
	}{
		{
			name: "nothing configured",
			want: healthResponse{OK: true, DB: "ok", LLM: llmHealth{Provider: "gemini"}},
		},
		{
			name: "everything configured",
			mutate: func(c *config.Config) {
				c.LLMProvider, c.LLMModel, c.AnthropicAPIKey = "anthropic", "claude-sonnet-5-5", "sk-secret"
				c.SlackWebhookURL = "https://hooks.slack.com/services/secret"
				c.SMTPUser, c.SMTPPass, c.EmailTo = "me@example.com", "app-password", "team@example.com"
				c.DemoReadOnly = true
			},
			want: healthResponse{
				OK: true, DB: "ok", ReadOnly: true,
				LLM:           llmHealth{Provider: "anthropic", Model: "claude-sonnet-5-5", APIKeyConfigured: true},
				Notifications: notificationsHealth{Slack: true, Email: true},
			},
		},
		{
			name: "key for the other provider does not count",
			mutate: func(c *config.Config) {
				c.AnthropicAPIKey = "sk-secret"
			},
			want: healthResponse{OK: true, DB: "ok", LLM: llmHealth{Provider: "gemini"}},
		},
		{
			name: "email needs credentials, not just EMAIL_TO",
			mutate: func(c *config.Config) {
				c.EmailTo = "team@example.com"
			},
			want: healthResponse{OK: true, DB: "ok", LLM: llmHealth{Provider: "gemini"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestEnv(t, tt.mutate)
			rec := env.do(t, "GET", "/api/health", "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			for _, secret := range []string{"sk-secret", "app-password", "hooks.slack.com"} {
				if strings.Contains(rec.Body.String(), secret) {
					t.Errorf("health response leaks %q: %s", secret, rec.Body.String())
				}
			}
			var got healthResponse
			decode(t, rec, &got)
			if got != tt.want {
				t.Errorf("health = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestHealthDatabaseDown(t *testing.T) {
	env := newTestEnv(t, nil)
	env.store.Close()

	rec := env.do(t, "GET", "/api/health", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var got healthResponse
	decode(t, rec, &got)
	if got.OK || got.DB != "unavailable" {
		t.Errorf("health = %+v, want ok=false db=unavailable", got)
	}
}

func TestReadOnlyMode(t *testing.T) {
	env := newTestEnv(t, func(c *config.Config) { c.DemoReadOnly = true })

	tests := []struct {
		method, path, body string
		wantStatus         int
	}{
		{"GET", "/api/health", "", http.StatusOK},
		{"GET", "/api/apps", "", http.StatusOK},
		{"POST", "/api/apps", `{"name":"X","base_url":"http://x"}`, http.StatusForbidden},
		{"PATCH", "/api/apps/1", `{"notes":"x"}`, http.StatusForbidden},
		{"DELETE", "/api/apps/1", "", http.StatusForbidden},
		{"PATCH", "/api/tests/1", `{"status":"retired"}`, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			rec := env.do(t, tt.method, tt.path, tt.body)
			if tt.wantStatus == http.StatusForbidden {
				wantError(t, rec, http.StatusForbidden, "read-only")
				return
			}
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}

	apps, err := env.store.ListApps(context.Background())
	if err != nil || len(apps) != 0 {
		t.Errorf("read-only mode wrote data: %v, %v", apps, err)
	}
}

func TestCORS(t *testing.T) {
	env := newTestEnv(t, nil)

	tests := []struct {
		name        string
		method      string
		origin      string
		preflight   bool
		wantStatus  int
		wantAllowed bool
	}{
		{"allowed origin", "GET", testOrigin, false, http.StatusOK, true},
		{"other origin", "GET", "http://evil.example", false, http.StatusOK, false},
		{"preflight allowed", "OPTIONS", testOrigin, true, http.StatusNoContent, true},
		{"preflight other origin", "OPTIONS", "http://evil.example", true, http.StatusNoContent, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/api/apps", nil)
			req.Header.Set("Origin", tt.origin)
			if tt.preflight {
				req.Header.Set("Access-Control-Request-Method", "PATCH")
			}
			rec := httptest.NewRecorder()
			env.h.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			got := rec.Header().Get("Access-Control-Allow-Origin")
			if tt.wantAllowed && got != testOrigin {
				t.Errorf("Allow-Origin = %q, want %q", got, testOrigin)
			}
			if !tt.wantAllowed && got != "" {
				t.Errorf("Allow-Origin = %q, want none", got)
			}
			if tt.preflight && tt.wantAllowed && !strings.Contains(rec.Header().Get("Access-Control-Allow-Methods"), "PATCH") {
				t.Errorf("Allow-Methods = %q, want PATCH included", rec.Header().Get("Access-Control-Allow-Methods"))
			}
		})
	}
}

func TestCORSPreflightInReadOnlyMode(t *testing.T) {
	env := newTestEnv(t, func(c *config.Config) { c.DemoReadOnly = true })
	req := httptest.NewRequest("OPTIONS", "/api/apps", nil)
	req.Header.Set("Origin", testOrigin)
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	env.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
}

func TestRecoverPanic(t *testing.T) {
	h := recoverPanic(discardLogger(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	wantError(t, rec, http.StatusInternalServerError, "internal server error")
}

func TestLogRequests(t *testing.T) {
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	h := logRequests(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("PATCH", "/api/x", nil))

	for _, want := range []string{"method=PATCH", "path=/api/x", "status=418", "duration="} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("log %q missing %q", buf.String(), want)
		}
	}
}

func TestDecodeJSONErrors(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantMsg    string
	}{
		{"empty", "", http.StatusBadRequest, "must not be empty"},
		{"malformed", `{"name":`, http.StatusBadRequest, "malformed JSON"},
		{"syntax", `{"name" "x"}`, http.StatusBadRequest, "malformed JSON"},
		{"wrong type", `{"name": 5}`, http.StatusBadRequest, `field "name" must be of type string`},
		{"not an object", `[1,2]`, http.StatusBadRequest, "must be a JSON object"},
		{"unknown field", `{"nme":"x"}`, http.StatusBadRequest, `unknown field "nme"`},
		{"trailing data", `{"name":"x"} {}`, http.StatusBadRequest, "single JSON object"},
		{"too large", `{"name":"` + strings.Repeat("a", maxBodyBytes) + `"}`, http.StatusRequestEntityTooLarge, "must not be larger"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var dst struct {
				Name string `json:"name"`
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/", strings.NewReader(tt.body))
			err := decodeJSON(rec, req, &dst)
			if err == nil {
				t.Fatal("want error, got nil")
			}
			writeRequestError(rec, err)
			wantError(t, rec, tt.wantStatus, tt.wantMsg)
		})
	}
}
