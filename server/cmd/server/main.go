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
	temporallog "go.temporal.io/sdk/log"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/auth"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/config"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/db"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/health"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/logging"
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

	pool, err := db.Open(ctx, cfg.DatabaseURL)
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
	temporalClient, err := client.NewLazyClient(client.Options{
		HostPort:  cfg.TemporalAddress,
		Namespace: cfg.TemporalNamespace,
		Logger:    temporallog.NewStructuredLogger(logger),
	})
	if err != nil {
		return err
	}
	defer temporalClient.Close()

	mux := http.NewServeMux()
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

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	logger.Info("server started", "addr", cfg.HTTPAddr)

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
	logger.Info("server stopped")
	return nil
}
