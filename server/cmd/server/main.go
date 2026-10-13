// Command server is the HTTP and WebSocket process.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"go.temporal.io/sdk/client"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/audience"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/auth"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/config"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/db"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/health"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/logging"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/objectstore"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/performance"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/rooms"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/temporalclient"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/theater"
)

func main() {
	cfg := config.Load()
	logger := logging.New("server", cfg.LogLevel)

	if err := run(cfg, logger); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(cfg config.Config, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Checked first, so a missing secret stops the server before it listens.
	shareLinks, err := rooms.NewShareLinks(cfg.ShareLinkSecret, cfg.PublicBaseURL)
	if err != nil {
		return err
	}
	origins, err := theater.NewOriginPolicy(cfg.PublicBaseURL)
	if err != nil {
		return err
	}
	objects, objectStore, err := objectstore.Configured(ctx, cfg)
	if err != nil {
		return err
	}

	pool, database, err := db.OpenConfigured(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	redisOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return err
	}
	redisClient := redis.NewClient(redisOpts)
	defer redisClient.Close()

	// A lazy client connects on first use, so Temporal being down at startup
	// is reported by /healthz and does not stop the server from starting.
	temporalClient, err := client.NewLazyClient(temporalclient.Options(cfg, logger))
	if err != nil {
		return err
	}
	defer temporalClient.Close()

	mux := http.NewServeMux()
	// What the load balancer asks. It checks nothing, on purpose.
	mux.Handle("GET /livez", health.Live())
	mux.Handle("GET /healthz", health.Handler(logger,
		health.Check{Name: "database", Run: pool.Ping},
		health.Check{Name: "redis", Run: func(ctx context.Context) error {
			return redisClient.Ping(ctx).Err()
		}},
		health.Check{Name: "temporal", Run: func(ctx context.Context) error {
			_, err := temporalClient.CheckHealth(ctx, &client.CheckHealthRequest{})
			return err
		}},
	))

	directors := &auth.Service{
		Store:        &auth.PGStore{Pool: pool},
		Throttle:     auth.NewThrottle(),
		Logger:       logger,
		Now:          time.Now,
		CookieSecure: cfg.CookieSecure,
	}
	mux.HandleFunc("POST /api/v1/director/login", directors.Login)
	mux.HandleFunc("POST /api/v1/director/logout", directors.Logout)
	// Every director route from here on is wrapped in RequireDirector.
	mux.Handle("GET /api/v1/director/me", directors.RequireDirector(http.HandlerFunc(directors.Me)))

	roomStore := &rooms.PGStore{Pool: pool}
	audienceService := &audience.Service{
		Store:        &audience.PGStore{Pool: pool},
		Rooms:        roomStore,
		Links:        shareLinks,
		Logger:       logger,
		Now:          time.Now,
		CookieSecure: cfg.CookieSecure,
	}
	roomService := &rooms.Service{
		Store:     roomStore,
		Directors: directors,
		Audience:  audienceService,
		Objects:   objects,
		Links:     shareLinks,
		VoiceA:    cfg.VoiceIDA,
		VoiceB:    cfg.VoiceIDB,
		Logger:    logger,
		Now:       time.Now,
	}
	director := func(h http.HandlerFunc) http.Handler { return directors.RequireDirector(h) }
	mux.Handle("POST /api/v1/rooms", director(roomService.Create))
	mux.Handle("GET /api/v1/rooms", director(roomService.List))
	// These two admit the room's director or a member of its audience, and
	// decide that themselves.
	mux.HandleFunc("GET /api/v1/rooms/{roomId}", roomService.Get)
	mux.HandleFunc("GET /api/v1/rooms/{roomId}/portraits/{slot}", roomService.Portrait)
	// Start writes the performance row and starts its workflow.
	performances := &performance.PGStore{Pool: pool}
	workflows := &performance.Workflows{Client: temporalClient, TaskQueue: cfg.TemporalTaskQueue}
	performanceService := &performance.Service{
		Rooms:      roomStore,
		Store:      performances,
		Workflows:  workflows,
		Authorizer: roomService,
		Directors:  directors,
		Objects:    objects,
		Logger:     logger,
		Now:        time.Now,
	}
	mux.Handle("POST /api/v1/rooms/{roomId}/performance", director(performanceService.Start))
	// A turn's recording, for the same people who may read the room, once
	// the performance has completed.
	mux.HandleFunc(performance.AudioRoute, performanceService.Audio)

	// The room's live connection, for the same people who may read the room.
	hub := theater.NewHub()
	theaterService := &theater.Service{
		Rooms:        roomService,
		Audience:     audienceService,
		Members:      audienceService,
		Performances: performances,
		Workflows:    workflows,
		Failures:     performances,
		Votes:        workflows,
		Hub:          hub,
		Notices:      redisClient,
		Origins:      origins,
		Logger:       logger,
		Now:          time.Now,
	}
	// The director's page is told as people join.
	audienceService.Watcher = theaterService
	// Updates the worker publishes are read from Redis here and forwarded to
	// each room's connections, from startup until the process stops.
	// Notices, the servers' own news for one another, arrive the same way.
	go (&theater.StreamReader{Redis: redisClient, Hub: hub, Notices: theaterService, Logger: logger}).Run(ctx)
	mux.HandleFunc("GET /api/v1/rooms/{roomId}/ws", theaterService.ServeWS)
	// Open to anyone holding a valid share link.
	mux.HandleFunc("POST /api/v1/audience-sessions", audienceService.Join)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	logger.Info("server started",
		"addr", cfg.HTTPAddr,
		"database", database,
		"object_store", objectStore,
		"temporal_address", cfg.TemporalAddress,
		"namespace", cfg.TemporalNamespace,
		"public_base_url", cfg.PublicBaseURL,
	)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	// Shutdown leaves upgraded connections alone, so they are closed here.
	hub.CloseAll()
	logger.Info("server stopped")
	return nil
}
