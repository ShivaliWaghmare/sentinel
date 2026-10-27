package notify

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/ShivaliWaghmare/sentinel/server/internal/config"
	"github.com/ShivaliWaghmare/sentinel/server/internal/store"
)

const (
	slackTimeout = 10 * time.Second
	smtpTimeout  = 30 * time.Second
)

// Notifier sends run reports to every configured channel. It implements
// runs.Notifier. A channel is enabled only when it is configured.
type Notifier struct {
	slack        *slackSender // nil when SLACK_WEBHOOK_URL is unset
	email        *emailSender // nil unless config.EmailConfigured
	dashboardURL string
	onlyFailures bool // NOTIFY_ON=failures
	logger       *slog.Logger
}

// New builds a Notifier from the configuration.
func New(cfg config.Config, logger *slog.Logger) *Notifier {
	n := &Notifier{
		dashboardURL: cfg.DashboardURL,
		onlyFailures: cfg.NotifyOn == config.NotifyFailures,
		logger:       logger,
	}
	if cfg.SlackConfigured() {
		n.slack = &slackSender{webhookURL: cfg.SlackWebhookURL, client: &http.Client{Timeout: slackTimeout}}
	}
	if cfg.EmailConfigured() {
		from := cfg.EmailFrom
		if from == "" {
			from = cfg.SMTPUser
		}
		n.email = &emailSender{
			host: cfg.SMTPHost, port: cfg.SMTPPort, user: cfg.SMTPUser, pass: cfg.SMTPPass,
			from: from, to: splitAddresses(cfg.EmailTo), timeout: smtpTimeout,
		}
	}
	return n
}

// ChannelResult is the outcome of sending to one channel.
type ChannelResult struct {
	Configured bool
	OK         bool
	Error      string // empty when OK or not configured
}

// RunFinished sends the run's report. With NOTIFY_ON=failures, passed runs
// are skipped. Errors from all channels are joined; the caller logs them and
// the run is never changed.
func (n *Notifier) RunFinished(ctx context.Context, app store.App, run store.Run, results []store.TestResult) error {
	if n.onlyFailures && run.Status == store.RunPassed {
		n.logger.Info("notification skipped: run passed and NOTIFY_ON=failures", "run_id", run.ID)
		return nil
	}
	slackRes, emailRes := n.send(ctx, NewReport(app, run, results, n.dashboardURL))
	var errs []error
	for _, res := range []ChannelResult{slackRes, emailRes} {
		if res.Error != "" {
			errs = append(errs, errors.New(res.Error))
		}
	}
	return errors.Join(errs...)
}

// SendTest sends a sample report to every configured channel, whatever
// NOTIFY_ON says, and reports each channel's outcome.
func (n *Notifier) SendTest(ctx context.Context) (slack, email ChannelResult) {
	return n.send(ctx, sampleReport(n.dashboardURL))
}

// send delivers r to Slack and email in turn. Each error message already
// names its channel.
func (n *Notifier) send(ctx context.Context, r Report) (slack, email ChannelResult) {
	if n.slack != nil {
		slack = n.result("slack", r, n.slack.send(ctx, r))
	}
	if n.email != nil {
		email = n.result("email", r, n.email.send(ctx, r))
	}
	return slack, email
}

func (n *Notifier) result(channel string, r Report, err error) ChannelResult {
	if err != nil {
		return ChannelResult{Configured: true, Error: err.Error()}
	}
	n.logger.Info("notification sent", "channel", channel, "run_id", r.RunID, "sample", r.Sample)
	return ChannelResult{Configured: true, OK: true}
}
