// Command sentinel runs the Sentinel API server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/ShivaliWaghmare/sentinel/server/internal/agents"
	"github.com/ShivaliWaghmare/sentinel/server/internal/api"
	"github.com/ShivaliWaghmare/sentinel/server/internal/config"
	"github.com/ShivaliWaghmare/sentinel/server/internal/notify"
	"github.com/ShivaliWaghmare/sentinel/server/internal/runner"
	"github.com/ShivaliWaghmare/sentinel/server/internal/runs"
	"github.com/ShivaliWaghmare/sentinel/server/internal/scheduler"
	"github.com/ShivaliWaghmare/sentinel/server/internal/sse"
	"github.com/ShivaliWaghmare/sentinel/server/internal/store"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	if err := run(); err != nil {
		slog.Error("sentinel stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	// The server is started from server/, so the repo root is one level up.
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working dir: %w", err)
	}
	cfg, err := config.Load("../.env", wd)
	if err != nil {
		return err
	}
	slog.Info("config loaded", "config", cfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	slog.Info("database ready", "path", filepath.Join(cfg.DataDir, store.DBFile))

	// Runs use ctx, so a shutdown signal kills in-flight Playwright processes.
	pw := runner.New(cfg.AgentsDir, cfg.DataDir, cfg.RunTimeout)
	notifier := notify.New(cfg, slog.Default())
	slog.Info("notifications", "slack", cfg.SlackConfigured(), "email", cfg.EmailConfigured(), "notify_on", cfg.NotifyOn)
	runSvc := runs.New(ctx, st, pw, notifier, cfg.DataDir, slog.Default())
	if err := runSvc.Recover(ctx); err != nil {
		return err
	}
	// Registered after st.Close above, so it runs first: runs finish
	// writing before the database closes.
	defer runSvc.Wait()

	// Agent runs, like test runs, are killed on shutdown and recorded as
	// failed before the database closes.
	broker := sse.NewBroker()
	agentSup := agents.New(ctx, st, broker,
		agents.DefaultOptions(cfg.AgentsDir, cfg.DataDir, cfg.AgentConcurrency, cfg.AgentTimeout), slog.Default())
	if err := agentSup.Recover(ctx); err != nil {
		return err
	}
	defer agentSup.Wait()

	// config.Load already checked that TIMEZONE loads.
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return fmt.Errorf("load timezone: %w", err)
	}
	sched := scheduler.New(ctx, st, runSvc, loc, slog.Default())
	if err := sched.Load(ctx); err != nil {
		return err
	}
	sched.Start()
	// Runs before runSvc.Wait: no new scheduled run can start while
	// in-flight runs are being waited for.
	defer sched.Stop()

	srv := &http.Server{
		Addr: ":" + strconv.Itoa(cfg.Port),
		Handler: api.New(cfg, st, api.Services{
			Runs: runSvc, Scheduler: sched, Notifier: notifier, Agents: agentSup, Broker: broker,
		}, slog.Default()),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
	}

	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
