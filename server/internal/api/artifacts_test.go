package api

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// writeFile creates dataDir/rel with body, making parent folders.
func writeFile(t *testing.T, dataDir, rel, body string) {
	t.Helper()
	path := filepath.Join(dataDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactServesFiles(t *testing.T) {
	env := newTestEnv(t, nil)
	dir := env.cfg.DataDir
	writeFile(t, dir, "runs/1/artifacts/t/test-failed-1.png", "\x89PNG fake")
	writeFile(t, dir, "runs/1/artifacts/t/trace.zip", "PK fake")
	writeFile(t, dir, "runs/1/artifacts/t/url.txt", "http://localhost:4000/cart")
	writeFile(t, dir, "runs/1/artifacts/t/data.bin", "raw")

	tests := []struct {
		path        string
		contentType string
		body        string
	}{
		{"runs/1/artifacts/t/test-failed-1.png", "image/png", "\x89PNG fake"},
		{"runs/1/artifacts/t/trace.zip", "application/zip", "PK fake"},
		{"runs/1/artifacts/t/url.txt", "text/plain; charset=utf-8", "http://localhost:4000/cart"},
		{"runs/1/artifacts/t/data.bin", "application/octet-stream", "raw"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := env.do(t, "GET", "/api/artifacts/"+tt.path, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d (body %s)", rec.Code, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); ct != tt.contentType {
				t.Errorf("Content-Type = %q, want %q", ct, tt.contentType)
			}
			if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Error("missing nosniff")
			}
			if rec.Body.String() != tt.body {
				t.Errorf("body = %q, want %q", rec.Body.String(), tt.body)
			}
		})
	}
}

func TestArtifactRejectsEscapes(t *testing.T) {
	env := newTestEnv(t, nil)
	dir := env.cfg.DataDir
	writeFile(t, dir, "runs/1/ok.txt", "ok")
	// A secret next to DATA_DIR, and sentinel.db inside it (created by the store).
	outside := filepath.Join(filepath.Dir(dir), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "runs", "1", "escape.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "sentinel.db"), filepath.Join(dir, "runs", "1", "db.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(dir), filepath.Join(dir, "runs", "1", "up")); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		url    string
		status int
	}{
		{"dot-dot segment", "/api/artifacts/runs/1/..%2F..%2Fsentinel.db", http.StatusBadRequest},
		{"encoded dot-dot", "/api/artifacts/runs/%2e%2e/%2e%2e/sentinel.db", http.StatusBadRequest},
		{"leading dot-dot", "/api/artifacts/..%2Fsecret.txt", http.StatusBadRequest},
		{"absolute path", "/api/artifacts/%2Fetc%2Fpasswd", http.StatusBadRequest},
		// ServeMux itself redirects to the cleaned path before our handler runs.
		{"double slash", "/api/artifacts/runs//1/ok.txt", http.StatusTemporaryRedirect},
		{"backslash", "/api/artifacts/runs%5C1%5Cok.txt", http.StatusBadRequest},
		{"empty", "/api/artifacts/", http.StatusBadRequest},
		{"database outside allowed folders", "/api/artifacts/sentinel.db", http.StatusNotFound},
		{"symlink to outside DATA_DIR", "/api/artifacts/runs/1/escape.txt", http.StatusNotFound},
		{"symlink to the database", "/api/artifacts/runs/1/db.txt", http.StatusNotFound},
		{"through a symlinked folder", "/api/artifacts/runs/1/up/secret.txt", http.StatusNotFound},
		{"directory", "/api/artifacts/runs/1", http.StatusNotFound},
		{"missing file", "/api/artifacts/runs/1/nope.png", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := env.do(t, "GET", tt.url, "")
			if rec.Code != tt.status {
				t.Errorf("status = %d, want %d (body %s)", rec.Code, tt.status, rec.Body.String())
			}
			if body := rec.Body.String(); body == "secret" || len(body) > 100 {
				t.Errorf("leaked content: %q", body)
			}
		})
	}

	// The legitimate file next to them is still served.
	if rec := env.do(t, "GET", "/api/artifacts/runs/1/ok.txt", ""); rec.Code != http.StatusOK {
		t.Errorf("ok.txt status = %d", rec.Code)
	}
}
