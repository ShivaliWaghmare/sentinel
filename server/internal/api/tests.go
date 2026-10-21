package api

import (
	"errors"
	"net/http"

	"github.com/ShivaliWaghmare/sentinel/server/internal/store"
)

type testCaseJSON struct {
	ID         int64  `json:"id"`
	AppID      int64  `json:"app_id"`
	FilePath   string `json:"file_path"`
	Title      string `json:"title"`
	Origin     string `json:"origin"`
	Status     string `json:"status"`
	AgentRunID *int64 `json:"agent_run_id"`
	CreatedAt  string `json:"created_at"`
}

func toTestCaseJSON(tc store.TestCase) testCaseJSON {
	return testCaseJSON{
		ID:         tc.ID,
		AppID:      tc.AppID,
		FilePath:   tc.FilePath,
		Title:      tc.Title,
		Origin:     tc.Origin,
		Status:     tc.Status,
		AgentRunID: tc.AgentRunID,
		CreatedAt:  formatTime(tc.CreatedAt),
	}
}

type updateTestRequest struct {
	Status string `json:"status"`
}

// handleListTests lists an app's test cases, optionally filtered with
// ?origin=manual|generated and ?status=active|quarantined|retired.
func (s *Server) handleListTests(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "app not found")
		return
	}
	q := r.URL.Query()
	filter := store.TestCaseFilter{Origin: q.Get("origin"), Status: q.Get("status")}
	if filter.Origin != "" && !store.ValidOrigin(filter.Origin) {
		writeError(w, http.StatusBadRequest, "origin must be one of: manual, generated")
		return
	}
	if filter.Status != "" && !store.ValidTestStatus(filter.Status) {
		writeError(w, http.StatusBadRequest, "status must be one of: active, quarantined, retired")
		return
	}

	// An unknown app is a 404, not an empty list.
	if _, err := s.store.GetApp(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "app not found")
		return
	} else if err != nil {
		s.internalError(w, r, err)
		return
	}

	tcs, err := s.store.ListTestCases(r.Context(), id, filter)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]testCaseJSON, 0, len(tcs))
	for _, tc := range tcs {
		out = append(out, toTestCaseJSON(tc))
	}
	writeJSON(w, http.StatusOK, map[string][]testCaseJSON{"tests": out})
}

// handleUpdateTest changes a test case's status (for example, quarantine).
func (s *Server) handleUpdateTest(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "test not found")
		return
	}
	var req updateTestRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeRequestError(w, err)
		return
	}
	if req.Status == "" {
		writeError(w, http.StatusBadRequest, "status is required")
		return
	}
	if !store.ValidTestStatus(req.Status) {
		writeError(w, http.StatusBadRequest, "status must be one of: active, quarantined, retired")
		return
	}

	tc, err := s.store.UpdateTestCaseStatus(r.Context(), id, req.Status)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "test not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toTestCaseJSON(tc))
}
