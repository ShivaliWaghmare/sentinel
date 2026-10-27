package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/ShivaliWaghmare/sentinel/server/internal/agents"
	"github.com/ShivaliWaghmare/sentinel/server/internal/sse"
	"github.com/ShivaliWaghmare/sentinel/server/internal/store"
)

const (
	defaultAgentRunsLimit = 20
	maxAgentRunsLimit     = 100
	// sseHeartbeat keeps idle connections (and proxies) from timing out.
	sseHeartbeat = 15 * time.Second
)

type agentRunJSON struct {
	ID         int64           `json:"id"`
	AppID      int64           `json:"app_id"`
	Kind       string          `json:"kind"`
	Status     string          `json:"status"`
	Input      json.RawMessage `json:"input"`
	Summary    json.RawMessage `json:"summary"` // null until finished
	Steps      int             `json:"steps"`
	TokensIn   int64           `json:"tokens_in"`
	TokensOut  int64           `json:"tokens_out"`
	CostUSD    float64         `json:"cost_usd"`
	Error      *string         `json:"error"`
	StartedAt  *string         `json:"started_at"`
	FinishedAt *string         `json:"finished_at"`
}

func toAgentRunJSON(r store.AgentRun) agentRunJSON {
	out := agentRunJSON{
		ID: r.ID, AppID: r.AppID, Kind: r.Kind, Status: r.Status,
		Input:   json.RawMessage(r.InputJSON),
		Summary: json.RawMessage("null"),
		Steps:   r.Steps, TokensIn: r.TokensIn, TokensOut: r.TokensOut, CostUSD: r.CostUSD,
		Error: optionalString(r.Error),
	}
	if r.SummaryJSON != "" {
		out.Summary = json.RawMessage(r.SummaryJSON)
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

// handleStartProbe starts a probe agent run for the app: 202 with the run,
// which is running or queued.
func (s *Server) handleStartProbe(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "app not found")
		return
	}
	// The probe takes no options; an empty body or {} is accepted.
	if r.ContentLength != 0 {
		var body struct{}
		if err := decodeJSON(w, r, &body); err != nil {
			writeRequestError(w, err)
			return
		}
	}
	run, err := s.agents.Start(r.Context(), store.AgentProbe, id, nil)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "app not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAgentRunJSON(run))
}

// handleListAgentRuns lists agent runs newest first, optionally for one app
// (?app_id=) and kind (?kind=), at most ?limit= (default 20, max 100).
func (s *Server) handleListAgentRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := store.AgentRunFilter{Limit: defaultAgentRunsLimit}
	if v := q.Get("app_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "app_id must be a positive integer")
			return
		}
		filter.AppID = id
	}
	if v := q.Get("kind"); v != "" {
		if !store.ValidAgentKind(v) {
			writeError(w, http.StatusBadRequest, "kind must be probe, explore, triage or heal")
			return
		}
		filter.Kind = v
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxAgentRunsLimit {
			writeError(w, http.StatusBadRequest, "limit must be an integer from 1 to 100")
			return
		}
		filter.Limit = n
	}

	list, err := s.store.ListAgentRuns(r.Context(), filter)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]agentRunJSON, 0, len(list))
	for _, run := range list {
		out = append(out, toAgentRunJSON(run))
	}
	writeJSON(w, http.StatusOK, map[string][]agentRunJSON{"agent_runs": out})
}

func (s *Server) handleGetAgentRun(w http.ResponseWriter, r *http.Request) {
	run, ok := s.agentRunFromPath(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toAgentRunJSON(run))
}

// handleCancelAgentRun asks a run to stop: 202 with the run. A queued run
// is cancelled at once; a running one ends when its process does. 409 if
// the run already finished.
func (s *Server) handleCancelAgentRun(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "agent run not found")
		return
	}
	run, err := s.agents.Cancel(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "agent run not found")
		return
	case errors.Is(err, agents.ErrFinished):
		writeError(w, http.StatusConflict, fmt.Sprintf("agent run %d already finished (%s)", id, run.Status))
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAgentRunJSON(run))
}

// handleAgentRunEvents streams a run's events as server-sent events: first
// the stored events after Last-Event-ID (the header, or ?last_event_id= for
// clients like curl), then live ones, until the done event. Each message's
// id is the event's seq and its data is the event as JSON.
func (s *Server) handleAgentRunEvents(w http.ResponseWriter, r *http.Request) {
	run, ok := s.agentRunFromPath(w, r)
	if !ok {
		return
	}
	lastSeq, err := lastEventID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rc := http.NewResponseController(w)

	// Subscribe before reading the store: an event stored after the read is
	// then still delivered live. Events seen in both are skipped by seq.
	sub := s.broker.Subscribe(run.ID)
	defer s.broker.Unsubscribe(sub)
	stored, err := s.store.ListAgentEvents(r.Context(), run.ID, lastSeq)
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no") // don't let a proxy buffer the stream
	w.WriteHeader(http.StatusOK)

	send := func(ev sse.Event) bool {
		data, err := json.Marshal(ev)
		if err != nil {
			s.logger.Error("encode sse event", "agent_run_id", run.ID, "err", err)
			return false
		}
		if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", ev.Seq, data); err != nil {
			return false
		}
		lastSeq = ev.Seq
		return rc.Flush() == nil
	}

	sendStored := func(list []store.AgentEvent) (more bool) {
		for _, ev := range list {
			if !send(sse.Event{Seq: ev.Seq, TS: ev.CreatedAt, Type: ev.Type, Data: json.RawMessage(ev.PayloadJSON)}) {
				return false
			}
			if ev.Type == "done" {
				return false
			}
		}
		return true
	}
	if !sendStored(stored) {
		return
	}
	if err := rc.Flush(); err != nil {
		return
	}
	// A run that already finished has all its events stored: send any that
	// arrived since the first read and stop. (A run interrupted by a restart
	// has no done event at all.)
	if current, err := s.store.GetAgentRun(r.Context(), run.ID); err != nil || current.Finished() {
		if err == nil {
			if rest, err := s.store.ListAgentEvents(r.Context(), run.ID, lastSeq); err == nil {
				sendStored(rest)
			}
		}
		return
	}

	heartbeat := time.NewTicker(sseHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case ev, ok := <-sub.C:
			if !ok {
				return // dropped as too slow; the client reconnects with Last-Event-ID
			}
			if ev.Seq <= lastSeq {
				continue
			}
			if !send(ev) || ev.Type == "done" {
				return
			}
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

func lastEventID(r *http.Request) (int64, error) {
	v := r.Header.Get("Last-Event-ID")
	if v == "" {
		v = r.URL.Query().Get("last_event_id")
	}
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, errors.New("Last-Event-ID must be a non-negative integer")
	}
	return n, nil
}

func (s *Server) agentRunFromPath(w http.ResponseWriter, r *http.Request) (store.AgentRun, bool) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "agent run not found")
		return store.AgentRun{}, false
	}
	run, err := s.store.GetAgentRun(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "agent run not found")
		return store.AgentRun{}, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return store.AgentRun{}, false
	}
	return run, true
}
