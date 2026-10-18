// Package config loads process configuration from environment variables.
package config

import (
	"os"
	"strconv"
	"time"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/providers"
)

// Config holds the settings shared by the server, worker, and migrate commands.
// Every field has a default that works against the local development stack.
type Config struct {
	HTTPAddr    string
	DatabaseURL string
	// DSQLEndpoint is the Aurora DSQL cluster's endpoint. Set, it replaces
	// DatabaseURL: the pool is built by the DSQL connector, which signs in
	// with the process's AWS identity as DSQLUser (docs/decisions.md, D032).
	DSQLEndpoint      string
	DSQLUser          string
	RedisURL          string
	TemporalAddress   string
	TemporalNamespace string
	TemporalTaskQueue string
	// TemporalAPIKey is a Temporal Cloud API key. Set, the connection uses
	// TLS and the key; empty is the local dev server, which needs neither.
	TemporalAPIKey string
	LogLevel       string
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
	// S3Bucket is the bucket portraits and audio are kept in. Set, it
	// replaces ObjectStoreDir.
	S3Bucket string
	// VoiceIDA and VoiceIDB are the two fixed voices, recorded on each room
	// when it is created.
	VoiceIDA string
	VoiceIDB string
	// LLMProvider chooses the worker's language model: "fake" or
	// "anthropic". TTSProvider chooses its speech service: "fake" or
	// "elevenlabs". Both default to "fake", so nothing is spent unless a
	// real provider is asked for.
	LLMProvider string
	TTSProvider string
	// LLMModel and TTSModel name the model each real provider uses. Empty
	// means that provider's own default.
	LLMModel string
	TTSModel string
	// LipSyncProvider chooses the worker's lip-sync service: "fake" or
	// "wavespeed", "fake" unless asked. LipSyncResolution is its videos'
	// resolution, "480p" or "720p"; the worker refuses anything else.
	LipSyncProvider   string
	LipSyncResolution string
	// LipSyncPollInterval is how often the worker asks whether a lip-sync
	// video is done. The tests shorten it; zero is refused.
	LipSyncPollInterval time.Duration
	// FakeScriptDir holds per-room scripts for the fake providers. Empty
	// means every room gets the fakes' defaults.
	FakeScriptDir string
	// VoteWindow is how long each round's voting stays open, and
	// PremiereCountdown how far ahead the premiere's start time is set.
	// Zero means the variable was set but is not a duration; the worker
	// refuses to start with that.
	VoteWindow        time.Duration
	PremiereCountdown time.Duration
	// ChoicesRetryWindow is how long choice generation is retried, in
	// total, before the run fails. Zero is refused, like the two above.
	ChoicesRetryWindow time.Duration
	// MaxLineLength is the longest a generated line may be, in characters.
	// Zero means the variable was set but is not a whole number; the worker
	// refuses that, and any value outside the allowed range.
	MaxLineLength int
}

// Load reads the configuration from the environment.
func Load() Config {
	return Config{
		HTTPAddr:          env("HTTP_ADDR", ":8080"),
		DatabaseURL:       env("DATABASE_URL", "postgres://monster:monster@localhost:5432/monstertheatre?sslmode=disable"),
		DSQLEndpoint:      os.Getenv("DSQL_ENDPOINT"),
		DSQLUser:          env("DSQL_USER", "admin"),
		RedisURL:          env("REDIS_URL", "redis://localhost:6379/0"),
		TemporalAddress:   env("TEMPORAL_ADDRESS", "localhost:7233"),
		TemporalNamespace: env("TEMPORAL_NAMESPACE", "default"),
		TemporalTaskQueue: env("TEMPORAL_TASK_QUEUE", "performance"),
		TemporalAPIKey:    os.Getenv("TEMPORAL_API_KEY"),
		LogLevel:          env("LOG_LEVEL", "info"),
		CookieSecure:      envBool("COOKIE_SECURE", true),
		ShareLinkSecret:   os.Getenv("SHARE_LINK_SECRET"),
		PublicBaseURL:     env("PUBLIC_BASE_URL", "http://localhost:3000"),
		ObjectStoreDir:    env("OBJECT_STORE_DIR", ".data/objects"),
		S3Bucket:          os.Getenv("S3_BUCKET"),
		VoiceIDA:          env("VOICE_ID_A", "voice-a-placeholder"),
		VoiceIDB:          env("VOICE_ID_B", "voice-b-placeholder"),
		LLMProvider:       env("LLM_PROVIDER", "fake"),
		TTSProvider:       env("TTS_PROVIDER", "fake"),
		LipSyncProvider:   env("LIPSYNC_PROVIDER", "fake"),
		LipSyncResolution: env("LIPSYNC_RESOLUTION", "480p"),
		// WaveSpeed asks for at least two seconds between polls, and a
		// video takes minutes; a video is seen at most this late (D055).
		LipSyncPollInterval: envDuration("LIPSYNC_POLL_INTERVAL", 20*time.Second),
		FakeScriptDir:       os.Getenv("FAKE_SCRIPT_DIR"),
		VoteWindow:          envDuration("VOTE_WINDOW", 10*time.Second),
		PremiereCountdown:   envDuration("PREMIERE_COUNTDOWN", 5*time.Second),
		ChoicesRetryWindow:  envDuration("CHOICES_RETRY_WINDOW", 2*time.Minute),
		MaxLineLength:       envInt("MAX_LINE_LENGTH", providers.DefaultLineLimit),
		LLMModel:            os.Getenv("LLM_MODEL"),
		TTSModel:            os.Getenv("TTS_MODEL"),
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

// envInt reads a whole number. A value that is not one gives zero, which the
// caller must refuse.
func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
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
