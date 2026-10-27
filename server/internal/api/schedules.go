package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/ShivaliWaghmare/sentinel/server/internal/scheduler"
	"github.com/ShivaliWaghmare/sentinel/server/internal/store"
)

// previewCount is how many fire times the preview endpoint returns.
const previewCount = 5

type scheduleJSON struct {
	ID        int64  `json:"id"`
	AppID     int64  `json:"app_id"`
	Cron      string `json:"cron"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at"`
	// NextRun is null while the schedule is disabled.
	NextRun *string `json:"next_run"`
}

type createScheduleRequest struct {
	AppID   int64  `json:"app_id"`
	Cron    string `json:"cron"`
	Enabled *bool  `json:"enabled"` // default true
}

type updateScheduleRequest struct {
	Enabled *bool `json:"enabled"`
}

// previewJSON answers GET /api/schedules/preview. Times are UTC like every
// timestamp in the API; Timezone says where schedules fire so the dashboard
// can show them in that zone.
type previewJSON struct {
	Valid    bool     `json:"valid"`
	Error    *string  `json:"error"`
	Timezone string   `json:"timezone"`
	NextRuns []string `json:"next_runs"`
}

func (s *Server) toScheduleJSON(sch store.Schedule) scheduleJSON {
	out := scheduleJSON{
		ID:        sch.ID,
		AppID:     sch.AppID,
		Cron:      sch.Cron,
		Enabled:   sch.Enabled,
		CreatedAt: formatTime(sch.CreatedAt),
	}
	if next := s.sched.NextRuns(sch.ID, 1); len(next) == 1 {
		t := formatTime(next[0])
		out.NextRun = &t
	}
	return out
}

// handleListSchedules lists schedules, optionally for one app (?app_id=).
func (s *Server) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	var appID int64
	if v := r.URL.Query().Get("app_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "app_id must be a positive integer")
			return
		}
		appID = id
	}
	list, err := s.store.ListSchedules(r.Context(), appID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]scheduleJSON, 0, len(list))
	for _, sch := range list {
		out = append(out, s.toScheduleJSON(sch))
	}
	writeJSON(w, http.StatusOK, map[string][]scheduleJSON{"schedules": out})
}

// handleCreateSchedule validates the cron expression, stores the schedule
// and, if enabled, adds it to the scheduler.
func (s *Server) handleCreateSchedule(w http.ResponseWriter, r *http.Request) {
	var req createScheduleRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeRequestError(w, err)
		return
	}
	if req.AppID <= 0 {
		writeError(w, http.StatusBadRequest, "app_id is required and must be a positive integer")
		return
	}
	spec := strings.TrimSpace(req.Cron)
	if _, err := scheduler.Parse(spec); err != nil {
		writeError(w, http.StatusBadRequest, "invalid cron: "+err.Error())
		return
	}
	enabled := req.Enabled == nil || *req.Enabled

	sch, err := s.store.CreateSchedule(r.Context(), req.AppID, spec, enabled)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "app not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.sched.Add(sch); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.toScheduleJSON(sch))
}

// handleUpdateSchedule turns a schedule on or off.
func (s *Server) handleUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	}
	var req updateScheduleRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeRequestError(w, err)
		return
	}
	if req.Enabled == nil {
		writeError(w, http.StatusBadRequest, "no fields to update; send enabled")
		return
	}

	sch, err := s.store.SetScheduleEnabled(r.Context(), id, *req.Enabled)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	// Add removes the entry when the schedule is disabled.
	if err := s.sched.Add(sch); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.toScheduleJSON(sch))
}

// handleDeleteSchedule deletes a schedule and its cron entry.
func (s *Server) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	}
	err := s.store.DeleteSchedule(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.sched.Remove(id)
	w.WriteHeader(http.StatusNoContent)
}

// handlePreviewSchedule validates ?cron= and returns its next fire times.
// An invalid expression is a 400 with the same shape, so its "error" field
// follows the API's error format.
func (s *Server) handlePreviewSchedule(w http.ResponseWriter, r *http.Request) {
	out := previewJSON{Timezone: s.sched.Location().String(), NextRuns: []string{}}
	times, err := s.sched.Preview(r.URL.Query().Get("cron"), previewCount)
	if err != nil {
		msg := err.Error()
		out.Error = &msg
		writeJSON(w, http.StatusBadRequest, out)
		return
	}
	out.Valid = true
	for _, t := range times {
		out.NextRuns = append(out.NextRuns, formatTime(t))
	}
	writeJSON(w, http.StatusOK, out)
}
