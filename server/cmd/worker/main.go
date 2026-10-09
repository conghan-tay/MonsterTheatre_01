// Command worker is the Temporal worker process. It runs the performance
// workflow and its activities, and can be stopped and restarted without
// affecting browsers connected to the server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"go.temporal.io/sdk/client"
	temporallog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"
	sdkworkflow "go.temporal.io/sdk/workflow"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/activities"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/config"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/db"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/logging"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/objectstore"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/performance"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/providers/fake"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/rooms"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/workflow"
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

	acts, closeDeps, err := buildActivities(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeDeps()

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
	w.RegisterWorkflowWithOptions(workflow.Performance, sdkworkflow.RegisterOptions{Name: performance.WorkflowName})
	w.RegisterActivity(acts)

	if err := w.Start(); err != nil {
		return err
	}
	logger.Info("worker started",
		"temporal_address", cfg.TemporalAddress,
		"namespace", cfg.TemporalNamespace,
		"task_queue", cfg.TemporalTaskQueue,
		"llm_provider", cfg.LLMProvider,
		"tts_provider", cfg.TTSProvider,
		"vote_window", cfg.VoteWindow.String(),
	)

	<-ctx.Done()
	w.Stop()
	logger.Info("worker stopped")
	return nil
}

// buildActivities connects the activities to the database, Redis, the object
// store, and the providers. The returned function releases them.
//
// The settings are checked here, before the worker takes any work, so a
// mistake stops the process at once and not in the middle of a performance.
func buildActivities(ctx context.Context, cfg config.Config) (*activities.Activities, func(), error) {
	if cfg.VoteWindow <= 0 {
		return nil, nil, errors.New(`VOTE_WINDOW must be a positive duration such as "10s"`)
	}
	if cfg.PremiereCountdown <= 0 {
		return nil, nil, errors.New(`PREMIERE_COUNTDOWN must be a positive duration such as "5s"`)
	}
	// Real providers arrive in step 11.
	if cfg.LLMProvider != "fake" {
		return nil, nil, fmt.Errorf("LLM_PROVIDER %q is not supported; only \"fake\" exists so far", cfg.LLMProvider)
	}
	if cfg.TTSProvider != "fake" {
		return nil, nil, fmt.Errorf("TTS_PROVIDER %q is not supported; only \"fake\" exists so far", cfg.TTSProvider)
	}
	scripts := fake.Scripts{Dir: cfg.FakeScriptDir}

	objects, err := objectstore.NewFS(cfg.ObjectStoreDir)
	if err != nil {
		return nil, nil, err
	}
	redisOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return nil, nil, err
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}
	redisClient := redis.NewClient(redisOpts)

	acts := &activities.Activities{
		Rooms:             &rooms.PGStore{Pool: pool},
		Performances:      &performance.PGStore{Pool: pool},
		Stream:            redisClient,
		Objects:           objects,
		Dialogue:          fake.Dialogue{Scripts: scripts},
		Speech:            fake.Speech{Scripts: scripts},
		Model:             cfg.LLMProvider,
		VoteWindow:        cfg.VoteWindow,
		PremiereCountdown: cfg.PremiereCountdown,
		Now:               time.Now,
	}
	return acts, func() {
		redisClient.Close()
		pool.Close()
	}, nil
}
