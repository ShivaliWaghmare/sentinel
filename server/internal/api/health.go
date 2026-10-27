package api

import (
	"context"
	"net/http"
	"time"
)

type healthResponse struct {
	OK            bool                `json:"ok"`
	DB            string              `json:"db"`
	ReadOnly      bool                `json:"read_only"`
	LLM           llmHealth           `json:"llm"`
	Notifications notificationsHealth `json:"notifications"`
}

// llmHealth says which provider is configured; keys are never included.
type llmHealth struct {
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	APIKeyConfigured bool   `json:"api_key_configured"`
}

type notificationsHealth struct {
	Slack bool `json:"slack"`
	Email bool `json:"email"`
}

// handleHealth reports whether the server and database work and which
// integrations are configured. It returns 503 if the database is down.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	resp := healthResponse{
		OK:       true,
		DB:       "ok",
		ReadOnly: s.cfg.DemoReadOnly,
		LLM: llmHealth{
			Provider:         s.cfg.LLMProvider,
			Model:            s.cfg.LLMModel,
			APIKeyConfigured: s.llmKeyConfigured(),
		},
		Notifications: notificationsHealth{
			Slack: s.cfg.SlackConfigured(),
			Email: s.cfg.EmailConfigured(),
		},
	}
	status := http.StatusOK
	if err := s.store.Ping(ctx); err != nil {
		s.logger.Error("health: database ping failed", "err", err)
		resp.OK = false
		resp.DB = "unavailable"
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, resp)
}

// llmKeyConfigured reports whether the selected provider has a key.
func (s *Server) llmKeyConfigured() bool {
	switch s.cfg.LLMProvider {
	case "gemini":
		return s.cfg.GeminiAPIKey != ""
	case "anthropic":
		return s.cfg.AnthropicAPIKey != ""
	default:
		return false
	}
}
