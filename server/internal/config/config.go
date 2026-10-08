// Package config loads process configuration from environment variables.
package config

import "os"

// Config holds the settings shared by the server, worker, and migrate commands.
// Every field has a default that works against the local development stack.
type Config struct {
	HTTPAddr          string
	DatabaseURL       string
	RedisURL          string
	TemporalAddress   string
	TemporalNamespace string
	TemporalTaskQueue string
	LogLevel          string
}

// Load reads the configuration from the environment.
func Load() Config {
	return Config{
		HTTPAddr:          env("HTTP_ADDR", ":8080"),
		DatabaseURL:       env("DATABASE_URL", "postgres://monster:monster@localhost:5432/monstertheatre?sslmode=disable"),
		RedisURL:          env("REDIS_URL", "redis://localhost:6379/0"),
		TemporalAddress:   env("TEMPORAL_ADDRESS", "localhost:7233"),
		TemporalNamespace: env("TEMPORAL_NAMESPACE", "default"),
		TemporalTaskQueue: env("TEMPORAL_TASK_QUEUE", "performance"),
		LogLevel:          env("LOG_LEVEL", "info"),
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
