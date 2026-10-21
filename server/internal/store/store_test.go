package store

import (
	"context"
	"testing"
	"testing/fstest"
)

// newTestStore opens a fresh database in a temporary directory.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenAppliesMigrationsOnce(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	for i := 0; i < 2; i++ {
		s, err := Open(ctx, dir)
		if err != nil {
			t.Fatalf("Open #%d: %v", i+1, err)
		}
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
			t.Fatalf("count migrations: %v", err)
		}
		if n != 1 {
			t.Errorf("Open #%d: schema_migrations has %d rows, want 1", i+1, n)
		}
		s.Close()
	}
}

func TestOpenCreatesAllTables(t *testing.T) {
	s := newTestStore(t)
	tables := []string{
		"apps", "schedules", "test_cases", "runs", "test_results",
		"agent_runs", "agent_events", "triage_results", "heal_proposals",
	}
	for _, name := range tables {
		var n int
		err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n)
		if err != nil {
			t.Fatalf("query %s: %v", name, err)
		}
		if n != 1 {
			t.Errorf("table %s missing", name)
		}
	}
}

func TestOpenSetsPragmas(t *testing.T) {
	s := newTestStore(t)
	tests := []struct {
		pragma string
		want   string
	}{
		{"foreign_keys", "1"},
		{"journal_mode", "wal"},
		{"busy_timeout", "5000"},
	}
	for _, tt := range tests {
		var got string
		if err := s.db.QueryRow(`PRAGMA ` + tt.pragma).Scan(&got); err != nil {
			t.Fatalf("PRAGMA %s: %v", tt.pragma, err)
		}
		if got != tt.want {
			t.Errorf("PRAGMA %s = %q, want %q", tt.pragma, got, tt.want)
		}
	}
}

func TestLoadMigrations(t *testing.T) {
	sql := &fstest.MapFile{Data: []byte("SELECT 1;")}
	tests := []struct {
		name     string
		files    fstest.MapFS
		wantErr  bool
		wantVers []int
	}{
		{
			name: "sorted by version",
			files: fstest.MapFS{
				"migrations/0002_b.sql": sql,
				"migrations/0010_c.sql": sql,
				"migrations/0001_a.sql": sql,
				"migrations/README.txt": sql,
			},
			wantVers: []int{1, 2, 10},
		},
		{
			name:    "bad name",
			files:   fstest.MapFS{"migrations/init.sql": sql},
			wantErr: true,
		},
		{
			name: "duplicate version",
			files: fstest.MapFS{
				"migrations/0001_a.sql": sql,
				"migrations/1_b.sql":    sql,
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms, err := loadMigrations(tt.files)
			if tt.wantErr {
				if err == nil {
					t.Fatal("want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("loadMigrations: %v", err)
			}
			var got []int
			for _, m := range ms {
				got = append(got, m.version)
			}
			if len(got) != len(tt.wantVers) {
				t.Fatalf("versions = %v, want %v", got, tt.wantVers)
			}
			for i := range got {
				if got[i] != tt.wantVers[i] {
					t.Fatalf("versions = %v, want %v", got, tt.wantVers)
				}
			}
		})
	}
}
