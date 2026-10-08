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
	// ShareLinkSecret is the key share links are derived from. It has no
	// default: the server refuses to start without one (docs/decisions.md, D034).
	ShareLinkSecret string
	// PublicBaseURL is where the web pages are served; share links start with it.
	PublicBaseURL string
	// ObjectStoreDir is where the filesystem object store keeps portraits and audio.
	ObjectStoreDir string
	// VoiceIDA and VoiceIDB are the two fixed voices, recorded on each room
	// when it is created.
	VoiceIDA string
	VoiceIDB string
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
		ShareLinkSecret:   os.Getenv("SHARE_LINK_SECRET"),
		PublicBaseURL:     env("PUBLIC_BASE_URL", "http://localhost:3000"),
		ObjectStoreDir:    env("OBJECT_STORE_DIR", ".data/objects"),
		VoiceIDA:          env("VOICE_ID_A", "voice-a-placeholder"),
		VoiceIDB:          env("VOICE_ID_B", "voice-b-placeholder"),
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
