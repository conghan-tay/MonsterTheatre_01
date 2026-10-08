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
	// CookieSecure marks session cookies Secure (HTTPS only). It is on unless
	// COOKIE_SECURE is explicitly "false" or "0", which local HTTP needs.
	CookieSecure bool
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
		CookieSecure:      envBool("COOKIE_SECURE", true),
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envBool reads a boolean. Only "false" and "0" turn a setting off, so a
// typo leaves a security setting on.
func envBool(key string, fallback bool) bool {
	switch os.Getenv(key) {
	case "":
		return fallback
	case "false", "0":
		return false
	default:
		return true
	}
}
