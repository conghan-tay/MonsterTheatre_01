// Command worker is the Temporal worker process. It runs the performance
// workflow and its activities, and can be stopped and restarted without
// affecting browsers connected to the server.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"go.temporal.io/sdk/client"
	temporallog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/config"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/logging"
)

func main() {
	cfg := config.Load()
	logger := logging.New("worker", cfg.LogLevel)

	if err := run(cfg, logger); err != nil {
		logger.Error("worker stopped", "error", err)
		os.Exit(1)
	}
}

func run(cfg config.Config, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Dial connects immediately: a worker that cannot reach Temporal has
	// nothing to do, so failing here is the worker's health check.
	temporalClient, err := client.DialContext(ctx, client.Options{
		HostPort:  cfg.TemporalAddress,
		Namespace: cfg.TemporalNamespace,
		Logger:    temporallog.NewStructuredLogger(logger),
	})
	if err != nil {
		return err
	}
	defer temporalClient.Close()

	w := worker.New(temporalClient, cfg.TemporalTaskQueue, worker.Options{})
	// Workflows and activities are registered here from step 9 onward.

	if err := w.Start(); err != nil {
		return err
	}
	logger.Info("worker started",
		"temporal_address", cfg.TemporalAddress,
		"namespace", cfg.TemporalNamespace,
		"task_queue", cfg.TemporalTaskQueue,
	)

	<-ctx.Done()
	w.Stop()
	logger.Info("worker stopped")
	return nil
}
