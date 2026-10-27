package api

import (
	"net/http"

	"github.com/ShivaliWaghmare/sentinel/server/internal/notify"
)

type channelResultJSON struct {
	Configured bool    `json:"configured"`
	OK         bool    `json:"ok"`
	Error      *string `json:"error"`
}

type notifyTestResponse struct {
	Slack channelResultJSON `json:"slack"`
	Email channelResultJSON `json:"email"`
}

func toChannelResultJSON(r notify.ChannelResult) channelResultJSON {
	return channelResultJSON{Configured: r.Configured, OK: r.OK, Error: optionalString(r.Error)}
}

// handleNotifyTest sends a sample report to every configured channel and
// answers 200 with each channel's outcome, even when a channel failed: the
// request itself worked, and the body says what went wrong.
func (s *Server) handleNotifyTest(w http.ResponseWriter, r *http.Request) {
	slack, email := s.notifier.SendTest(r.Context())
	if slack.Error != "" || email.Error != "" {
		s.logger.Warn("test notification failed", "slack", slack.Error, "email", email.Error)
	}
	writeJSON(w, http.StatusOK, notifyTestResponse{
		Slack: toChannelResultJSON(slack),
		Email: toChannelResultJSON(email),
	})
}
