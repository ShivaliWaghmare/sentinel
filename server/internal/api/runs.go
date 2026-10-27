package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/ShivaliWaghmare/sentinel/server/internal/runs"
	"github.com/ShivaliWaghmare/sentinel/server/internal/store"
)

// RunStarter starts test runs. *runs.Service implements it.
type RunStarter interface {
	StartRun(ctx context.Context, appID int64, trigger string) (store.Run, error)
}

const (
	defaultRunsLimit = 20
	maxRunsLimit     = 100
)

type runJSON struct {
	ID         int64   `json:"id"`
	AppID      int64   `json:"app_id"`
	Trigger    string  `json:"trigger"`
	Status     string  `json:"status"`
	Total      int     `json:"total"`
	Passed     int     `json:"passed"`
	Failed     int     `json:"failed"`
	Flaky      int     `json:"flaky"`
	Skipped    int     `json:"skipped"`
	DurationMS int64   `json:"duration_ms"`
	Error      *string `json:"error"`
	StartedAt  *string `json:"started_at"`
	FinishedAt *string `json:"finished_at"`
}

// runDetailJSON is a run with its per-test results.
type runDetailJSON struct {
	runJSON
	Results []testResultJSON `json:"results"`
}

type testResultJSON struct {
	ID         int64   `json:"id"`
	TestCaseID *int64  `json:"test_case_id"`
	Title      string  `json:"title"`
	FilePath   string  `json:"file_path"`
	Line       int     `json:"line"`
	Status     string  `json:"status"`
	DurationMS int64   `json:"duration_ms"`
	Error      *string `json:"error"`
	Retries    int     `json:"retries"`
	// Paths relative to DATA_DIR, served by GET /api/artifacts/{path}.
	Artifacts map[string]string `json:"artifacts"`
}

func toRunJSON(r store.Run) runJSON {
	out := runJSON{
		ID:         r.ID,
		AppID:      r.AppID,
		Trigger:    r.Trigger,
		Status:     r.Status,
		Total:      r.Total,
		Passed:     r.Passed,
		Failed:     r.Failed,
		Flaky:      r.Flaky,
		Skipped:    r.Skipped,
		DurationMS: r.DurationMS,
		Error:      optionalString(r.Error),
	}
	if r.StartedAt != nil {
		s := formatTime(*r.StartedAt)
		out.StartedAt = &s
	}
	if r.FinishedAt != nil {
		s := formatTime(*r.FinishedAt)
		out.FinishedAt = &s
	}
	return out
}

func toTestResultJSON(r store.TestResult) testResultJSON {
	artifacts := r.Artifacts
	if artifacts == nil {
		artifacts = map[string]string{}
	}
	return testResultJSON{
		ID:         r.ID,
		TestCaseID: r.TestCaseID,
		Title:      r.Title,
		FilePath:   r.FilePath,
		Line:       r.Line,
		Status:     r.Status,
		DurationMS: r.DurationMS,
		Error:      optionalString(r.Error),
		Retries:    r.Retries,
		Artifacts:  artifacts,
	}
}

// optionalString turns "" into a JSON null.
func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// handleStartRun starts a manual run. It answers 202 with the run, which
// keeps executing in the background, or 409 if the app already has one.
func (s *Server) handleStartRun(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "app not found")
		return
	}
	run, err := s.runs.StartRun(r.Context(), id, store.TriggerManual)
	var active *runs.ActiveRunError
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "app not found")
		return
	case errors.As(err, &active):
		writeError(w, http.StatusConflict, active.Error())
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toRunJSON(run))
}

// handleListRuns lists runs newest first, optionally for one app
// (?app_id=), at most ?limit= (default 20, max 100). Results are not included.
func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := store.RunFilter{Limit: defaultRunsLimit}
	if v := q.Get("app_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "app_id must be a positive integer")
			return
		}
		filter.AppID = id
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxRunsLimit {
			writeError(w, http.StatusBadRequest, "limit must be an integer from 1 to 100")
			return
		}
		filter.Limit = n
	}

	list, err := s.store.ListRuns(r.Context(), filter)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]runJSON, 0, len(list))
	for _, run := range list {
		out = append(out, toRunJSON(run))
	}
	writeJSON(w, http.StatusOK, map[string][]runJSON{"runs": out})
}

// handleGetRun returns a run with its per-test results.
func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	run, err := s.store.GetRun(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	results, err := s.store.ListTestResults(r.Context(), id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := runDetailJSON{runJSON: toRunJSON(run), Results: make([]testResultJSON, 0, len(results))}
	for _, res := range results {
		out.Results = append(out.Results, toTestResultJSON(res))
	}
	writeJSON(w, http.StatusOK, out)
}
