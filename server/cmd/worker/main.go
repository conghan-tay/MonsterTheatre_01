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
	"slices"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	sdkworkflow "go.temporal.io/sdk/workflow"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/activities"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/config"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/db"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/logging"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/objectstore"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/performance"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/providers"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/providers/anthropic"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/providers/elevenlabs"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/providers/fake"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/providers/wavespeed"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/rooms"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/temporalclient"
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

	acts, chosen, closeDeps, err := buildActivities(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer closeDeps()

	// Dial connects immediately: a worker that cannot reach Temporal has
	// nothing to do, so failing here is the worker's health check.
	temporalClient, err := client.DialContext(ctx, temporalclient.Options(cfg, logger))
	if err != nil {
		return err
	}
	defer temporalClient.Close()

	w := worker.New(temporalClient, cfg.TemporalTaskQueue, worker.Options{})
	w.RegisterWorkflowWithOptions(workflow.Performance, sdkworkflow.RegisterOptions{Name: performance.WorkflowName})
	w.RegisterWorkflowWithOptions(workflow.PrepareLine, sdkworkflow.RegisterOptions{Name: workflow.PrepareLineName})
	w.RegisterActivity(acts)

	if err := w.Start(); err != nil {
		return err
	}
	logger.Info("worker started",
		"temporal_address", cfg.TemporalAddress,
		"namespace", cfg.TemporalNamespace,
		"task_queue", cfg.TemporalTaskQueue,
		"database", chosen.database,
		"object_store", chosen.objectStore,
		"llm_provider", cfg.LLMProvider,
		"tts_provider", cfg.TTSProvider,
		"llm_model", cfg.LLMModel,
		"tts_model", cfg.TTSModel,
		"llm_prompt_caching", cfg.LLMPromptCaching != "false" && cfg.LLMPromptCaching != "0",
		"lipsync_provider", cfg.LipSyncProvider,
		"lipsync_resolution", cfg.LipSyncResolution,
		"vote_window", cfg.VoteWindow.String(),
		"choices_retry_window", cfg.ChoicesRetryWindow.String(),
		"max_line_length", cfg.MaxLineLength,
	)

	<-ctx.Done()
	w.Stop()
	logger.Info("worker stopped")
	return nil
}

// backends names the database and object store the worker chose, for its
// startup log.
type backends struct {
	database    string
	objectStore string
}

// buildActivities connects the activities to the database, Redis, the object
// store, and the providers. The returned function releases them.
//
// The settings are checked here, before the worker takes any work, so a
// mistake stops the process at once and not in the middle of a performance.
func buildActivities(ctx context.Context, cfg config.Config, logger *slog.Logger) (*activities.Activities, backends, func(), error) {
	if cfg.VoteWindow <= 0 {
		return nil, backends{}, nil, errors.New(`VOTE_WINDOW must be a positive duration such as "10s"`)
	}
	if cfg.PremiereCountdown <= 0 {
		return nil, backends{}, nil, errors.New(`PREMIERE_COUNTDOWN must be a positive duration such as "5s"`)
	}
	if cfg.LipSyncPollInterval <= 0 {
		return nil, backends{}, nil, errors.New(`LIPSYNC_POLL_INTERVAL must be a positive duration such as "20s"`)
	}
	if cfg.ChoicesRetryWindow <= 0 {
		return nil, backends{}, nil, errors.New(`CHOICES_RETRY_WINDOW must be a positive duration such as "2m"`)
	}
	// Checked before any provider is built: the limit is enforced on what
	// the model returns, and a little less is what the model is asked for.
	lineLength, err := providers.LineLimit(cfg.MaxLineLength)
	if err != nil {
		return nil, backends{}, nil, err
	}
	caching, err := promptCaching(cfg)
	if err != nil {
		return nil, backends{}, nil, err
	}
	dialogue, speech, err := buildProviders(cfg, lineLength, caching, logger)
	if err != nil {
		return nil, backends{}, nil, err
	}
	lipSync, err := buildLipSync(cfg)
	if err != nil {
		return nil, backends{}, nil, err
	}

	objects, objectStore, err := objectstore.Configured(ctx, cfg)
	if err != nil {
		return nil, backends{}, nil, err
	}
	redisOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return nil, backends{}, nil, err
	}
	pool, database, err := db.OpenConfigured(ctx, cfg)
	if err != nil {
		return nil, backends{}, nil, err
	}
	redisClient := redis.NewClient(redisOpts)

	acts := &activities.Activities{
		Rooms:               &rooms.PGStore{Pool: pool},
		Performances:        &performance.PGStore{Pool: pool},
		Stream:              redisClient,
		Objects:             objects,
		Dialogue:            dialogue,
		Speech:              speech,
		LipSync:             lipSync,
		LipSyncPollInterval: cfg.LipSyncPollInterval,
		LipSyncResolution:   cfg.LipSyncResolution,
		VoteWindow:          cfg.VoteWindow,
		PremiereCountdown:   cfg.PremiereCountdown,
		ChoicesRetryWindow:  cfg.ChoicesRetryWindow,
		LineLimit:           cfg.MaxLineLength,
		Now:                 time.Now,
	}
	return acts, backends{database: database, objectStore: objectStore}, func() {
		redisClient.Close()
		pool.Close()
	}, nil
}

// buildProviders chooses the language model and the speech service.
//
// The API keys are read here and nowhere else: only the worker holds them
// (docs/system-design.md, section 7), and they are never logged. Asking for
// a real provider without its key stops the worker now, with the name of
// what is missing, and not at the first turn of a performance.
//
// lineLength is how long a line the language model is asked to write.
//
// caching turns Claude's prompt caching on, and logger receives a line per
// Claude call with its tokens and cost (docs/decisions.md, D057).
func buildProviders(cfg config.Config, lineLength int, caching bool, logger *slog.Logger) (providers.DialogueModel, providers.Speech, error) {
	scripts := fake.Scripts{Dir: cfg.FakeScriptDir}

	var dialogue providers.DialogueModel
	switch cfg.LLMProvider {
	case "fake":
		dialogue = fake.Dialogue{Scripts: scripts}
	case "anthropic":
		key := os.Getenv("ANTHROPIC_API_KEY")
		if key == "" {
			return nil, nil, errors.New("LLM_PROVIDER is anthropic but ANTHROPIC_API_KEY is not set")
		}
		claude := anthropic.New(key, cfg.LLMModel, lineLength)
		claude.Caching = caching
		claude.Logger = logger
		dialogue = claude
	default:
		return nil, nil, fmt.Errorf(`LLM_PROVIDER %q is not supported; use "fake" or "anthropic"`, cfg.LLMProvider)
	}

	var speech providers.Speech
	switch cfg.TTSProvider {
	case "fake":
		speech = fake.Speech{Scripts: scripts}
	case "elevenlabs":
		key := os.Getenv("ELEVENLABS_API_KEY")
		if key == "" {
			return nil, nil, errors.New("TTS_PROVIDER is elevenlabs but ELEVENLABS_API_KEY is not set")
		}
		speech = &elevenlabs.Speech{APIKey: key, Model: cfg.TTSModel}
	default:
		return nil, nil, fmt.Errorf(`TTS_PROVIDER %q is not supported; use "fake" or "elevenlabs"`, cfg.TTSProvider)
	}
	return dialogue, speech, nil
}

// buildLipSync chooses the lip-sync service, after checking the resolution
// asked for. Like the other providers, the real one needs its key, which is
// read here and never logged; the stand-in is used unless WaveSpeed is
// asked for by name.
func buildLipSync(cfg config.Config) (providers.LipSync, error) {
	if !slices.Contains(providers.LipSyncResolutions, cfg.LipSyncResolution) {
		return nil, fmt.Errorf(`LIPSYNC_RESOLUTION %q is not supported; use "480p" or "720p"`, cfg.LipSyncResolution)
	}
	switch cfg.LipSyncProvider {
	case "fake":
		return &fake.LipSync{Scripts: fake.Scripts{Dir: cfg.FakeScriptDir}}, nil
	case "wavespeed":
		key := os.Getenv("WAVESPEED_API_KEY")
		if key == "" {
			return nil, errors.New("LIPSYNC_PROVIDER is wavespeed but WAVESPEED_API_KEY is not set")
		}
		return &wavespeed.LipSync{APIKey: key}, nil
	default:
		return nil, fmt.Errorf(`LIPSYNC_PROVIDER %q is not supported; use "fake" or "wavespeed"`, cfg.LipSyncProvider)
	}
}

// promptCaching reads LLM_PROMPT_CACHING. It is on unless turned off, and
// anything but true, false, 1, or 0 stops the worker: the setting is flipped
// to compare costs, so a typo must not quietly mean "on".
func promptCaching(cfg config.Config) (bool, error) {
	switch cfg.LLMPromptCaching {
	case "", "true", "1":
		return true, nil
	case "false", "0":
		return false, nil
	default:
		return false, fmt.Errorf(`LLM_PROMPT_CACHING %q is not supported; use "true" or "false"`, cfg.LLMPromptCaching)
	}
}
