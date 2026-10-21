package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ShivaliWaghmare/sentinel/server/internal/store"
)

// createApp posts an app and returns the decoded response.
func (e testEnv) createApp(t *testing.T, body string) appJSON {
	t.Helper()
	rec := e.do(t, "POST", "/api/apps", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create app: status %d, body %s", rec.Code, rec.Body.String())
	}
	var a appJSON
	decode(t, rec, &a)
	return a
}

func appPath(id int64) string {
	return "/api/apps/" + strconv.FormatInt(id, 10)
}

func TestCreateApp(t *testing.T) {
	env := newTestEnv(t, nil)
	a := env.createApp(t, `{"name":"  ShopLite Demo ","base_url":"http://localhost:4000","notes":"login demo/demo","allow_destructive":true}`)

	if a.ID == 0 || a.Name != "ShopLite Demo" || a.Slug != "shoplite-demo" || a.BaseURL != "http://localhost:4000" ||
		a.Notes != "login demo/demo" || !a.AllowDestructive {
		t.Errorf("created app = %+v", a)
	}
	if _, err := time.Parse(time.RFC3339, a.CreatedAt); err != nil {
		t.Errorf("created_at %q is not RFC 3339: %v", a.CreatedAt, err)
	}
	for _, sub := range []string{"manual", "generated"} {
		dir := filepath.Join(env.cfg.AgentsDir, "tests", "shoplite-demo", sub)
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Errorf("folder %s not created: %v", dir, err)
		}
	}
}

func TestCreateAppDefaults(t *testing.T) {
	env := newTestEnv(t, nil)
	a := env.createApp(t, `{"name":"Minimal","base_url":"https://example.com/app"}`)
	if a.Notes != "" || a.AllowDestructive {
		t.Errorf("defaults = %+v, want empty notes and allow_destructive=false", a)
	}
}

func TestCreateAppValidation(t *testing.T) {
	env := newTestEnv(t, nil)
	tests := []struct {
		name    string
		body    string
		wantMsg string
	}{
		{"missing name", `{"base_url":"http://localhost:4000"}`, "name is required"},
		{"blank name", `{"name":"   ","base_url":"http://localhost:4000"}`, "name is required"},
		{"name without letters", `{"name":"!!!","base_url":"http://localhost:4000"}`, "at least one letter or digit"},
		{"name too long", `{"name":"` + strings.Repeat("a", maxNameLen+1) + `","base_url":"http://x"}`, "at most 100 characters"},
		{"missing base_url", `{"name":"X"}`, "base_url is required"},
		{"relative base_url", `{"name":"X","base_url":"/shop"}`, "absolute http(s) URL"},
		{"no scheme", `{"name":"X","base_url":"localhost:4000"}`, "absolute http(s) URL"},
		{"ftp scheme", `{"name":"X","base_url":"ftp://example.com"}`, "absolute http(s) URL"},
		{"no host", `{"name":"X","base_url":"http://"}`, "absolute http(s) URL"},
		{"unknown field", `{"name":"X","base_url":"http://x","slug":"mine"}`, `unknown field "slug"`},
		{"wrong type", `{"name":"X","base_url":"http://x","allow_destructive":"yes"}`, `field "allow_destructive" must be of type bool`},
		{"malformed JSON", `{"name":`, "malformed JSON"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantError(t, env.do(t, "POST", "/api/apps", tt.body), http.StatusBadRequest, tt.wantMsg)
		})
	}
	if apps, _ := env.store.ListApps(context.Background()); len(apps) != 0 {
		t.Errorf("invalid requests created %d apps", len(apps))
	}
}

func TestCreateAppSlugCollisions(t *testing.T) {
	env := newTestEnv(t, nil)

	first := env.createApp(t, `{"name":"ShopLite","base_url":"http://localhost:4000"}`)
	second := env.createApp(t, `{"name":"shoplite!","base_url":"http://localhost:4000"}`)
	if first.Slug != "shoplite" || second.Slug != "shoplite-2" {
		t.Errorf("slugs = %q, %q; want shoplite, shoplite-2", first.Slug, second.Slug)
	}

	// Files left behind by a deleted app keep the slug taken.
	if err := os.MkdirAll(filepath.Join(env.cfg.AgentsDir, "tests", "leftover", "manual"), 0o755); err != nil {
		t.Fatal(err)
	}
	third := env.createApp(t, `{"name":"Leftover","base_url":"http://localhost:4000"}`)
	if third.Slug != "leftover-2" {
		t.Errorf("slug = %q, want leftover-2 (leftover/ exists on disk)", third.Slug)
	}
}

func TestListApps(t *testing.T) {
	env := newTestEnv(t, nil)

	var empty struct {
		Apps []appJSON `json:"apps"`
	}
	rec := env.do(t, "GET", "/api/apps", "")
	decode(t, rec, &empty)
	if rec.Code != http.StatusOK || empty.Apps == nil || len(empty.Apps) != 0 {
		t.Fatalf("empty list: status %d, body %s; want 200 with \"apps\": []", rec.Code, rec.Body.String())
	}

	env.createApp(t, `{"name":"Beta","base_url":"http://b"}`)
	env.createApp(t, `{"name":"Alpha","base_url":"http://a"}`)

	var got struct {
		Apps []appJSON `json:"apps"`
	}
	decode(t, env.do(t, "GET", "/api/apps", ""), &got)
	if len(got.Apps) != 2 || got.Apps[0].Name != "Alpha" || got.Apps[1].Name != "Beta" {
		t.Errorf("apps = %+v, want Alpha then Beta", got.Apps)
	}
}

func TestGetApp(t *testing.T) {
	env := newTestEnv(t, nil)
	a := env.createApp(t, `{"name":"ShopLite","base_url":"http://localhost:4000"}`)

	rec := env.do(t, "GET", appPath(a.ID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got appJSON
	decode(t, rec, &got)
	if got != a {
		t.Errorf("GET = %+v, want %+v", got, a)
	}

	for _, path := range []string{"/api/apps/999", "/api/apps/abc", "/api/apps/0", "/api/apps/-1"} {
		wantError(t, env.do(t, "GET", path, ""), http.StatusNotFound, "app not found")
	}
}

func TestUpdateApp(t *testing.T) {
	env := newTestEnv(t, nil)
	a := env.createApp(t, `{"name":"ShopLite","base_url":"http://localhost:4000","notes":"old"}`)

	rec := env.do(t, "PATCH", appPath(a.ID), `{"notes":"login with demo/demo"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var got appJSON
	decode(t, rec, &got)
	want := a
	want.Notes = "login with demo/demo"
	if got != want {
		t.Errorf("PATCH notes = %+v, want %+v", got, want)
	}

	decode(t, env.do(t, "PATCH", appPath(a.ID), `{"name":" Renamed ","base_url":"https://shop.example","allow_destructive":true}`), &got)
	if got.Name != "Renamed" || got.BaseURL != "https://shop.example" || !got.AllowDestructive || got.Notes != want.Notes {
		t.Errorf("PATCH several = %+v", got)
	}
	if got.Slug != "shoplite" {
		t.Errorf("slug changed to %q on rename", got.Slug)
	}
}

func TestUpdateAppErrors(t *testing.T) {
	env := newTestEnv(t, nil)
	a := env.createApp(t, `{"name":"ShopLite","base_url":"http://localhost:4000"}`)

	tests := []struct {
		name       string
		path       string
		body       string
		wantStatus int
		wantMsg    string
	}{
		{"empty object", appPath(a.ID), `{}`, http.StatusBadRequest, "no fields to update"},
		{"blank name", appPath(a.ID), `{"name":""}`, http.StatusBadRequest, "name is required"},
		{"bad base_url", appPath(a.ID), `{"base_url":"not a url"}`, http.StatusBadRequest, "absolute http(s) URL"},
		{"slug not updatable", appPath(a.ID), `{"slug":"new"}`, http.StatusBadRequest, `unknown field "slug"`},
		{"unknown app", "/api/apps/999", `{"notes":"x"}`, http.StatusNotFound, "app not found"},
		{"non-numeric id", "/api/apps/abc", `{"notes":"x"}`, http.StatusNotFound, "app not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantError(t, env.do(t, "PATCH", tt.path, tt.body), tt.wantStatus, tt.wantMsg)
		})
	}
}

func TestDeleteApp(t *testing.T) {
	env := newTestEnv(t, nil)
	a := env.createApp(t, `{"name":"ShopLite","base_url":"http://localhost:4000"}`)
	if _, err := env.store.UpsertTestCase(context.Background(), store.TestCaseInput{
		AppID: a.ID, FilePath: "manual/a.spec.ts", Title: "t", Origin: store.OriginManual,
	}); err != nil {
		t.Fatal(err)
	}

	rec := env.do(t, "DELETE", appPath(a.ID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var got deleteAppResponse
	decode(t, rec, &got)
	if !got.Deleted || got.ID != a.ID || !strings.Contains(got.Message, "agents/tests/shoplite/ were left on disk") {
		t.Errorf("delete response = %+v", got)
	}

	wantError(t, env.do(t, "GET", appPath(a.ID), ""), http.StatusNotFound, "app not found")
	wantError(t, env.do(t, "DELETE", appPath(a.ID), ""), http.StatusNotFound, "app not found")

	if _, err := os.Stat(filepath.Join(env.cfg.AgentsDir, "tests", "shoplite", "manual")); err != nil {
		t.Errorf("test folder was removed: %v", err)
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"ShopLite", "shoplite"},
		{"ShopLite Demo", "shoplite-demo"},
		{"  ShopLite (staging)! ", "shoplite-staging"},
		{"my_app--v2", "my-app-v2"},
		{"Café Ünïcode", "caf-n-code"},
		{"123", "123"},
		{"!!!", ""},
		{"", ""},
		{strings.Repeat("ab-", 30), strings.TrimRight(strings.Repeat("ab-", 30)[:maxSlugLen], "-")},
	}
	for _, tt := range tests {
		if got := slugify(tt.in); got != tt.want {
			t.Errorf("slugify(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
