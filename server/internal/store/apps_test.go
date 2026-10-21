package store

import (
	"context"
	"errors"
	"testing"
)

func createTestApp(t *testing.T, s *Store, slug string) App {
	t.Helper()
	a, err := s.CreateApp(context.Background(), NewApp{
		Name: "App " + slug, Slug: slug, BaseURL: "http://localhost:4000",
	})
	if err != nil {
		t.Fatalf("CreateApp(%q): %v", slug, err)
	}
	return a
}

func TestCreateAndGetApp(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	in := NewApp{Name: "ShopLite", Slug: "shoplite", BaseURL: "http://localhost:4000", Notes: "login demo/demo", AllowDestructive: true}
	created, err := s.CreateApp(ctx, in)
	if err != nil {
		t.Fatalf("CreateApp: %v", err)
	}
	if created.ID == 0 || created.CreatedAt.IsZero() {
		t.Errorf("CreateApp did not set ID or CreatedAt: %+v", created)
	}
	if created.Name != in.Name || created.Slug != in.Slug || created.BaseURL != in.BaseURL ||
		created.Notes != in.Notes || created.AllowDestructive != in.AllowDestructive {
		t.Errorf("CreateApp = %+v, want fields from %+v", created, in)
	}

	byID, err := s.GetApp(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	bySlug, err := s.GetAppBySlug(ctx, "shoplite")
	if err != nil {
		t.Fatalf("GetAppBySlug: %v", err)
	}
	if byID != created || bySlug != created {
		t.Errorf("GetApp = %+v, GetAppBySlug = %+v, want %+v", byID, bySlug, created)
	}
}

func TestCreateAppDuplicateSlug(t *testing.T) {
	s := newTestStore(t)
	createTestApp(t, s, "shoplite")
	_, err := s.CreateApp(context.Background(), NewApp{Name: "Other", Slug: "shoplite", BaseURL: "http://x"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestAppNotFound(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	name := "x"

	tests := []struct {
		name string
		call func() error
	}{
		{"GetApp", func() error { _, err := s.GetApp(ctx, 99); return err }},
		{"GetAppBySlug", func() error { _, err := s.GetAppBySlug(ctx, "nope"); return err }},
		{"UpdateApp", func() error { _, err := s.UpdateApp(ctx, 99, AppUpdate{Name: &name}); return err }},
		{"DeleteApp", func() error { return s.DeleteApp(ctx, 99) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); !errors.Is(err, ErrNotFound) {
				t.Errorf("err = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestListAppsOrderedByName(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	empty, err := s.ListApps(ctx)
	if err != nil {
		t.Fatalf("ListApps: %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Errorf("ListApps on empty DB = %#v, want empty non-nil slice", empty)
	}

	for _, slug := range []string{"zeta", "alpha", "mid"} {
		createTestApp(t, s, slug)
	}
	apps, err := s.ListApps(ctx)
	if err != nil {
		t.Fatalf("ListApps: %v", err)
	}
	want := []string{"alpha", "mid", "zeta"}
	if len(apps) != len(want) {
		t.Fatalf("got %d apps, want %d", len(apps), len(want))
	}
	for i, a := range apps {
		if a.Slug != want[i] {
			t.Errorf("apps[%d].Slug = %q, want %q", i, a.Slug, want[i])
		}
	}
}

func TestUpdateApp(t *testing.T) {
	ctx := context.Background()
	str := func(s string) *string { return &s }
	yes := true

	tests := []struct {
		name   string
		update AppUpdate
		check  func(t *testing.T, before, after App)
	}{
		{
			name:   "notes only",
			update: AppUpdate{Notes: str("new notes")},
			check: func(t *testing.T, before, after App) {
				if after.Notes != "new notes" || after.Name != before.Name || after.BaseURL != before.BaseURL {
					t.Errorf("after = %+v", after)
				}
			},
		},
		{
			name:   "all fields",
			update: AppUpdate{Name: str("Renamed"), BaseURL: str("https://example.com"), Notes: str(""), AllowDestructive: &yes},
			check: func(t *testing.T, before, after App) {
				if after.Name != "Renamed" || after.BaseURL != "https://example.com" || after.Notes != "" || !after.AllowDestructive {
					t.Errorf("after = %+v", after)
				}
				if after.Slug != before.Slug {
					t.Errorf("slug changed from %q to %q", before.Slug, after.Slug)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			before, err := s.CreateApp(ctx, NewApp{Name: "ShopLite", Slug: "shoplite", BaseURL: "http://localhost:4000", Notes: "old"})
			if err != nil {
				t.Fatalf("CreateApp: %v", err)
			}
			after, err := s.UpdateApp(ctx, before.ID, tt.update)
			if err != nil {
				t.Fatalf("UpdateApp: %v", err)
			}
			tt.check(t, before, after)
		})
	}
}

func TestDeleteAppCascades(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	a := createTestApp(t, s, "shoplite")
	keep := createTestApp(t, s, "other")

	for _, appID := range []int64{a.ID, keep.ID} {
		if _, err := s.UpsertTestCase(ctx, TestCaseInput{AppID: appID, FilePath: "manual/a.spec.ts", Title: "t", Origin: OriginManual}); err != nil {
			t.Fatalf("UpsertTestCase: %v", err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO schedules (app_id, cron, created_at) VALUES (?, '@daily', 0)`, a.ID); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}

	if err := s.DeleteApp(ctx, a.ID); err != nil {
		t.Fatalf("DeleteApp: %v", err)
	}
	for _, table := range []string{"test_cases", "schedules"} {
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE app_id = ?`, a.ID).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%s still has %d rows for the deleted app", table, n)
		}
	}
	remaining, err := s.ListTestCases(ctx, keep.ID, TestCaseFilter{})
	if err != nil || len(remaining) != 1 {
		t.Errorf("other app's test cases = %v, %v; want 1 row", remaining, err)
	}
}

func TestDeletedAppIDIsNotReused(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	first := createTestApp(t, s, "first")
	if err := s.DeleteApp(ctx, first.ID); err != nil {
		t.Fatalf("DeleteApp: %v", err)
	}
	second := createTestApp(t, s, "second")
	if second.ID == first.ID {
		t.Errorf("new app reused deleted ID %d", first.ID)
	}
}

func TestSlugExists(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	createTestApp(t, s, "shoplite")

	tests := []struct {
		slug string
		want bool
	}{
		{"shoplite", true},
		{"shoplite-2", false},
	}
	for _, tt := range tests {
		got, err := s.SlugExists(ctx, tt.slug)
		if err != nil {
			t.Fatalf("SlugExists(%q): %v", tt.slug, err)
		}
		if got != tt.want {
			t.Errorf("SlugExists(%q) = %v, want %v", tt.slug, got, tt.want)
		}
	}
}
