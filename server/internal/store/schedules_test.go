package store

import (
	"context"
	"errors"
	"testing"
)

func TestScheduleCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	app := createTestApp(t, s, "shop")
	other := createTestApp(t, s, "other")

	a, err := s.CreateSchedule(ctx, app.ID, "0 9 * * 1,4", true)
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if a.ID == 0 || a.AppID != app.ID || a.Cron != "0 9 * * 1,4" || !a.Enabled || a.CreatedAt.IsZero() {
		t.Errorf("created = %+v", a)
	}
	b, err := s.CreateSchedule(ctx, other.ID, "@daily", false)
	if err != nil {
		t.Fatalf("CreateSchedule other: %v", err)
	}

	got, err := s.GetSchedule(ctx, a.ID)
	if err != nil || got != a {
		t.Errorf("GetSchedule = %+v, %v; want %+v", got, err, a)
	}

	tests := []struct {
		name  string
		appID int64
		want  []int64
	}{
		{"all apps", 0, []int64{a.ID, b.ID}},
		{"one app", other.ID, []int64{b.ID}},
		{"app without schedules", 999, []int64{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			list, err := s.ListSchedules(ctx, tt.appID)
			if err != nil {
				t.Fatalf("ListSchedules: %v", err)
			}
			if ids := scheduleIDs(list); !equalIDs(ids, tt.want) {
				t.Errorf("ids = %v, want %v", ids, tt.want)
			}
		})
	}

	enabled, err := s.ListEnabledSchedules(ctx)
	if err != nil || !equalIDs(scheduleIDs(enabled), []int64{a.ID}) {
		t.Errorf("ListEnabledSchedules = %v, %v; want [%d]", scheduleIDs(enabled), err, a.ID)
	}

	updated, err := s.SetScheduleEnabled(ctx, a.ID, false)
	if err != nil || updated.Enabled {
		t.Errorf("SetScheduleEnabled = %+v, %v; want disabled", updated, err)
	}

	if err := s.DeleteSchedule(ctx, a.ID); err != nil {
		t.Fatalf("DeleteSchedule: %v", err)
	}
	if _, err := s.GetSchedule(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSchedule after delete err = %v, want ErrNotFound", err)
	}
}

func TestScheduleNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.CreateSchedule(ctx, 42, "@daily", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("CreateSchedule unknown app err = %v, want ErrNotFound", err)
	}
	if _, err := s.SetScheduleEnabled(ctx, 42, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetScheduleEnabled err = %v, want ErrNotFound", err)
	}
	if err := s.DeleteSchedule(ctx, 42); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteSchedule err = %v, want ErrNotFound", err)
	}
}

func TestDeleteAppDeletesSchedules(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	app := createTestApp(t, s, "shop")
	sch, err := s.CreateSchedule(ctx, app.ID, "@daily", true)
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if err := s.DeleteApp(ctx, app.ID); err != nil {
		t.Fatalf("DeleteApp: %v", err)
	}
	if _, err := s.GetSchedule(ctx, sch.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("schedule after app delete err = %v, want ErrNotFound", err)
	}
}

func scheduleIDs(list []Schedule) []int64 {
	ids := make([]int64, 0, len(list))
	for _, s := range list {
		ids = append(ids, s.ID)
	}
	return ids
}

func equalIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
