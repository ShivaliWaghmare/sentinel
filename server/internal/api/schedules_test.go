package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShivaliWaghmare/sentinel/server/internal/config"
)

func schedulePath(id int64) string {
	return "/api/schedules/" + strconv.FormatInt(id, 10)
}

func (e testEnv) createSchedule(t *testing.T, body string) scheduleJSON {
	t.Helper()
	rec := e.do(t, "POST", "/api/schedules", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create schedule: status %d, body %s", rec.Code, rec.Body.String())
	}
	var sch scheduleJSON
	decode(t, rec, &sch)
	return sch
}

func TestCreateAndListSchedules(t *testing.T) {
	env := newTestEnv(t, nil)
	shop := env.createApp(t, `{"name":"ShopLite","base_url":"http://localhost:4000"}`)
	other := env.createApp(t, `{"name":"Other","base_url":"http://localhost:5000"}`)

	on := env.createSchedule(t, `{"app_id":`+strconv.FormatInt(shop.ID, 10)+`,"cron":"  0 9 * * 1,4 "}`)
	if on.AppID != shop.ID || on.Cron != "0 9 * * 1,4" || !on.Enabled || on.NextRun == nil || on.CreatedAt == "" {
		t.Errorf("created = %+v, want enabled, trimmed cron and a next_run", on)
	}
	// 09:00 IST is 03:30 UTC on a Monday or Thursday.
	next, err := time.Parse(time.RFC3339, *on.NextRun)
	if err != nil || next.UTC().Hour() != 3 || next.UTC().Minute() != 30 ||
		(next.Weekday() != time.Monday && next.Weekday() != time.Thursday) {
		t.Errorf("next_run = %s (%v), want Mon/Thu 03:30Z", *on.NextRun, err)
	}

	off := env.createSchedule(t, `{"app_id":`+strconv.FormatInt(other.ID, 10)+`,"cron":"@daily","enabled":false}`)
	if off.Enabled || off.NextRun != nil {
		t.Errorf("disabled schedule = %+v, want enabled=false and next_run null", off)
	}

	tests := []struct {
		query string
		want  []int64
	}{
		{"", []int64{on.ID, off.ID}},
		{"?app_id=" + strconv.FormatInt(other.ID, 10), []int64{off.ID}},
		{"?app_id=999", []int64{}},
	}
	for _, tt := range tests {
		t.Run("list"+tt.query, func(t *testing.T) {
			rec := env.do(t, "GET", "/api/schedules"+tt.query, "")
			var body struct {
				Schedules []scheduleJSON `json:"schedules"`
			}
			decode(t, rec, &body)
			if rec.Code != http.StatusOK || body.Schedules == nil || len(body.Schedules) != len(tt.want) {
				t.Fatalf("status %d, body %s; want %d schedules", rec.Code, rec.Body.String(), len(tt.want))
			}
			for i, id := range tt.want {
				if body.Schedules[i].ID != id {
					t.Errorf("schedule %d id = %d, want %d", i, body.Schedules[i].ID, id)
				}
			}
		})
	}
	wantError(t, env.do(t, "GET", "/api/schedules?app_id=abc", ""), http.StatusBadRequest, "app_id")
}

func TestCreateScheduleErrors(t *testing.T) {
	env := newTestEnv(t, nil)
	a := env.createApp(t, `{"name":"ShopLite","base_url":"http://localhost:4000"}`)
	app := strconv.FormatInt(a.ID, 10)

	tests := []struct {
		name, body string
		status     int
		msg        string
	}{
		{"missing app_id", `{"cron":"@daily"}`, http.StatusBadRequest, "app_id is required"},
		{"unknown app", `{"app_id":999,"cron":"@daily"}`, http.StatusNotFound, "app not found"},
		{"missing cron", `{"app_id":` + app + `}`, http.StatusBadRequest, "invalid cron: cron expression is required"},
		{"too few fields", `{"app_id":` + app + `,"cron":"0 9 * *"}`, http.StatusBadRequest, "expected exactly 5 fields"},
		{"out of range", `{"app_id":` + app + `,"cron":"0 25 * * *"}`, http.StatusBadRequest, "above maximum"},
		{"@every", `{"app_id":` + app + `,"cron":"@every 5m"}`, http.StatusBadRequest, "@every is not supported"},
		{"tz prefix", `{"app_id":` + app + `,"cron":"CRON_TZ=UTC 0 9 * * *"}`, http.StatusBadRequest, "time zone"},
		{"unknown field", `{"app_id":` + app + `,"cron":"@daily","tz":"UTC"}`, http.StatusBadRequest, "unknown field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantError(t, env.do(t, "POST", "/api/schedules", tt.body), tt.status, tt.msg)
		})
	}
}

func TestUpdateAndDeleteSchedule(t *testing.T) {
	env := newTestEnv(t, nil)
	a := env.createApp(t, `{"name":"ShopLite","base_url":"http://localhost:4000"}`)
	sch := env.createSchedule(t, `{"app_id":`+strconv.FormatInt(a.ID, 10)+`,"cron":"*/2 * * * *"}`)

	rec := env.do(t, "PATCH", schedulePath(sch.ID), `{"enabled":false}`)
	var got scheduleJSON
	decode(t, rec, &got)
	if rec.Code != http.StatusOK || got.Enabled || got.NextRun != nil {
		t.Fatalf("disable: status %d, %+v; want enabled=false, next_run null", rec.Code, got)
	}
	if env.sched.NextRuns(sch.ID, 1) != nil {
		t.Error("scheduler still has the disabled schedule")
	}

	rec = env.do(t, "PATCH", schedulePath(sch.ID), `{"enabled":true}`)
	decode(t, rec, &got)
	if rec.Code != http.StatusOK || !got.Enabled || got.NextRun == nil {
		t.Fatalf("enable: status %d, %+v; want enabled with next_run", rec.Code, got)
	}

	wantError(t, env.do(t, "PATCH", schedulePath(sch.ID), `{}`), http.StatusBadRequest, "no fields to update")
	wantError(t, env.do(t, "PATCH", schedulePath(sch.ID), `{"cron":"@daily"}`), http.StatusBadRequest, "unknown field")
	wantError(t, env.do(t, "PATCH", schedulePath(999), `{"enabled":true}`), http.StatusNotFound, "schedule not found")
	wantError(t, env.do(t, "PATCH", "/api/schedules/abc", `{"enabled":true}`), http.StatusNotFound, "schedule not found")

	rec = env.do(t, "DELETE", schedulePath(sch.ID), "")
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("delete: status %d, body %q; want 204 with no body", rec.Code, rec.Body.String())
	}
	if env.sched.NextRuns(sch.ID, 1) != nil {
		t.Error("scheduler still has the deleted schedule")
	}
	wantError(t, env.do(t, "DELETE", schedulePath(sch.ID), ""), http.StatusNotFound, "schedule not found")
}

func TestDeleteAppRemovesScheduleEntries(t *testing.T) {
	env := newTestEnv(t, nil)
	a := env.createApp(t, `{"name":"ShopLite","base_url":"http://localhost:4000"}`)
	sch := env.createSchedule(t, `{"app_id":`+strconv.FormatInt(a.ID, 10)+`,"cron":"@daily"}`)

	if rec := env.do(t, "DELETE", appPath(a.ID), ""); rec.Code != http.StatusOK {
		t.Fatalf("delete app: status %d", rec.Code)
	}
	if env.sched.NextRuns(sch.ID, 1) != nil {
		t.Error("scheduler kept the deleted app's schedule")
	}
}

func TestPreviewSchedule(t *testing.T) {
	env := newTestEnv(t, nil)

	rec := env.do(t, "GET", "/api/schedules/preview?cron="+urlQuery("0 9 * * 1,4"), "")
	var got previewJSON
	decode(t, rec, &got)
	if rec.Code != http.StatusOK || !got.Valid || got.Error != nil || got.Timezone != "Asia/Kolkata" || len(got.NextRuns) != 5 {
		t.Fatalf("status %d, %+v; want valid with 5 times in Asia/Kolkata", rec.Code, got)
	}
	ist, _ := time.LoadLocation("Asia/Kolkata")
	var prev time.Time
	for _, s := range got.NextRuns {
		ts, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		local := ts.In(ist)
		if local.Hour() != 9 || local.Minute() != 0 || (local.Weekday() != time.Monday && local.Weekday() != time.Thursday) || !ts.After(prev) {
			t.Errorf("fire time %s = %s IST, want ascending Mon/Thu 09:00", s, local.Format("Mon 15:04"))
		}
		prev = ts
	}

	for _, spec := range []string{"", "0 9 * *", "@every 1h"} {
		t.Run("invalid "+spec, func(t *testing.T) {
			rec := env.do(t, "GET", "/api/schedules/preview?cron="+urlQuery(spec), "")
			var got previewJSON
			decode(t, rec, &got)
			if rec.Code != http.StatusBadRequest || got.Valid || got.Error == nil || *got.Error == "" || got.NextRuns == nil {
				t.Errorf("status %d, %s; want 400 with valid=false, an error and next_runs []", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestNotifyTest(t *testing.T) {
	var hits atomic.Int32
	slack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer slack.Close()

	tests := []struct {
		name   string
		mutate func(*config.Config)
		want   notifyTestResponse
		hits   int32
	}{
		{"nothing configured", nil, notifyTestResponse{}, 0},
		{"slack only", func(c *config.Config) { c.SlackWebhookURL = slack.URL }, notifyTestResponse{
			Slack: channelResultJSON{Configured: true, OK: true},
		}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hits.Store(0)
			env := newTestEnv(t, tt.mutate)
			rec := env.do(t, "POST", "/api/notify/test", "")
			var got notifyTestResponse
			decode(t, rec, &got)
			if rec.Code != http.StatusOK || got.Slack != tt.want.Slack || got.Email.Configured || got.Email.OK {
				t.Errorf("status %d, %s; want %+v", rec.Code, rec.Body.String(), tt.want)
			}
			if hits.Load() != tt.hits {
				t.Errorf("webhook hits = %d, want %d", hits.Load(), tt.hits)
			}
		})
	}

	// An email server that refuses connections reports an error, not a 500.
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	host, port := hostPort(t, closed.URL)
	env := newTestEnv(t, func(c *config.Config) {
		c.SMTPHost, c.SMTPPort, c.SMTPUser, c.SMTPPass, c.EmailTo = host, port, "u", "secret-pw", "a@b.c"
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req := httptest.NewRequest("POST", "/api/notify/test", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	env.h.ServeHTTP(rec, req)
	var got notifyTestResponse
	decode(t, rec, &got)
	if rec.Code != http.StatusOK || !got.Email.Configured || got.Email.OK || got.Email.Error == nil {
		t.Errorf("status %d, %s; want email configured with an error", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secret-pw") {
		t.Error("response leaks SMTP_PASS")
	}
}

func urlQuery(s string) string {
	return url.QueryEscape(s)
}

// hostPort splits an httptest URL like http://127.0.0.1:1234.
func hostPort(t *testing.T, rawURL string) (string, int) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("port of %q: %v", rawURL, err)
	}
	return u.Hostname(), port
}
