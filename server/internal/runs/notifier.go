package runs

import (
	"context"

	"github.com/ShivaliWaghmare/sentinel/server/internal/store"
)

// Notifier is told about every finished run (lifecycle 1, step 6). Step 6
// of the roadmap implements it with Slack and email; errors are logged and
// never change the run.
type Notifier interface {
	RunFinished(ctx context.Context, app store.App, run store.Run, results []store.TestResult) error
}

// NopNotifier ignores every run.
type NopNotifier struct{}

// RunFinished does nothing.
func (NopNotifier) RunFinished(context.Context, store.App, store.Run, []store.TestResult) error {
	return nil
}
