// Package api holds Sentinel's REST handlers and HTTP middleware.
package api

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/ShivaliWaghmare/sentinel/server/internal/config"
	"github.com/ShivaliWaghmare/sentinel/server/internal/store"
)

// Server holds what the handlers need.
type Server struct {
	cfg    config.Config
	store  *store.Store
	runs   RunStarter
	logger *slog.Logger
}

// New returns the API handler with all routes and middleware.
func New(cfg config.Config, st *store.Store, rs RunStarter, logger *slog.Logger) http.Handler {
	s := &Server{cfg: cfg, store: st, runs: rs, logger: logger}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.handleHealth)

	mux.HandleFunc("GET /api/apps", s.handleListApps)
	mux.HandleFunc("POST /api/apps", s.handleCreateApp)
	mux.HandleFunc("GET /api/apps/{id}", s.handleGetApp)
	mux.HandleFunc("PATCH /api/apps/{id}", s.handleUpdateApp)
	mux.HandleFunc("DELETE /api/apps/{id}", s.handleDeleteApp)

	mux.HandleFunc("GET /api/apps/{id}/tests", s.handleListTests)
	mux.HandleFunc("PATCH /api/tests/{id}", s.handleUpdateTest)

	mux.HandleFunc("POST /api/apps/{id}/runs", s.handleStartRun)
	mux.HandleFunc("GET /api/runs", s.handleListRuns)
	mux.HandleFunc("GET /api/runs/{id}", s.handleGetRun)

	mux.HandleFunc("GET /api/artifacts/{path...}", s.handleArtifact)

	// Outermost first: log → recover → CORS → read-only → routes.
	// Logging wraps recovery so the 500 from a panic is logged too.
	var h http.Handler = mux
	h = readOnly(cfg.DemoReadOnly, h)
	h = cors(cfg.CORSOrigin, h)
	h = recoverPanic(logger, h)
	h = logRequests(logger, h)
	return h
}

// pathID parses the {id} path value. ok is false for anything that is not a
// positive integer; callers answer 404 because no such resource can exist.
func pathID(r *http.Request) (id int64, ok bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// internalError logs err and sends a generic 500; details stay in the log.
func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	s.logger.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal server error")
}
