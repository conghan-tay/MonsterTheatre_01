// Package config loads process configuration from environment variables.
package config

import (
	"os"
	"time"
)

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
	// LLMProvider and TTSProvider choose the worker's language model and
	// speech service. Only "fake" exists until step 11.
	LLMProvider string
	TTSProvider string
	// FakeScriptDir holds per-room scripts for the fake providers. Empty
	// means every room gets the fakes' defaults.
	FakeScriptDir string
	// VoteWindow is how long each round's voting stays open, and
	// PremiereCountdown how far ahead the premiere's start time is set.
	// Zero means the variable was set but is not a duration; the worker
	// refuses to start with that.
	VoteWindow        time.Duration
	PremiereCountdown time.Duration
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
		LLMProvider:       env("LLM_PROVIDER", "fake"),
		TTSProvider:       env("TTS_PROVIDER", "fake"),
		FakeScriptDir:     os.Getenv("FAKE_SCRIPT_DIR"),
		VoteWindow:        envDuration("VOTE_WINDOW", 10*time.Second),
		PremiereCountdown: envDuration("PREMIERE_COUNTDOWN", 5*time.Second),
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envDuration reads a duration such as "10s". A value that is not a positive
// duration gives zero, which the caller must refuse.
func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0
	}
	return d
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
