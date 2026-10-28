package main

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/activities"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/config"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/objectstore"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/providers/anthropic"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/providers/fake"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/providers/wavespeed"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/telemetry"
)

func TestLipSyncIsTheStandInUnlessWaveSpeedIsAskedFor(t *testing.T) {
	t.Setenv("WAVESPEED_API_KEY", "")
	l, err := buildLipSync(config.Config{LipSyncProvider: "fake", LipSyncResolution: "480p"})
	require.NoError(t, err)
	require.IsType(t, &fake.LipSync{}, l)
}

func TestWaveSpeedNeedsItsKey(t *testing.T) {
	t.Setenv("WAVESPEED_API_KEY", "")
	_, err := buildLipSync(config.Config{LipSyncProvider: "wavespeed", LipSyncResolution: "480p"})
	require.ErrorContains(t, err, "WAVESPEED_API_KEY")

	t.Setenv("WAVESPEED_API_KEY", "a-key")
	l, err := buildLipSync(config.Config{LipSyncProvider: "wavespeed", LipSyncResolution: "720p"})
	require.NoError(t, err)
	require.IsType(t, &wavespeed.LipSync{}, l)
}

func TestAnUnknownLipSyncProviderOrResolutionStopsTheWorker(t *testing.T) {
	_, err := buildLipSync(config.Config{LipSyncProvider: "other", LipSyncResolution: "480p"})
	require.ErrorContains(t, err, `LIPSYNC_PROVIDER "other"`)
	for _, resolution := range []string{"1080p", "480", ""} {
		_, err = buildLipSync(config.Config{LipSyncProvider: "fake", LipSyncResolution: resolution})
		require.ErrorContains(t, err, "LIPSYNC_RESOLUTION", "resolution %q", resolution)
	}
}

func TestPromptCachingIsOnUnlessTurnedOff(t *testing.T) {
	for value, want := range map[string]bool{"": true, "true": true, "1": true, "false": false, "0": false} {
		got, err := promptCaching(config.Config{LLMPromptCaching: value})
		require.NoError(t, err, "%q", value)
		require.Equal(t, want, got, "%q", value)
	}
}

func TestAPromptCachingValueThatIsNotTrueOrFalseStopsTheWorker(t *testing.T) {
	// The usual reading of a true-or-false setting would take a typo as on;
	// this one is flipped to measure cost, so a typo must not pass.
	for _, value := range []string{"flase", "yes", "off", "TRUE "} {
		_, err := promptCaching(config.Config{LLMPromptCaching: value})
		require.ErrorContains(t, err, "LLM_PROMPT_CACHING", "%q", value)
	}
}

func TestTheClaudeAdapterIsGivenTheCachingSettingAndTheLogger(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "a-key")
	logger := slog.New(slog.DiscardHandler)
	for _, caching := range []bool{true, false} {
		dialogue, _, err := buildProviders(config.Config{LLMProvider: "anthropic", TTSProvider: "fake"}, 160, caching, logger)
		require.NoError(t, err)
		m, ok := dialogue.(*anthropic.Model)
		require.True(t, ok)
		require.Equal(t, caching, m.Caching)
		require.Same(t, logger, m.Logger)
	}
}

func TestEveryProviderAndTheStoreAreTimedAndTheActivitiesLogged(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	acts := &activities.Activities{
		Dialogue: fake.Dialogue{}, Speech: fake.Speech{}, LipSync: &fake.LipSync{},
	}
	store, err := objectstore.NewFS(t.TempDir())
	require.NoError(t, err)
	acts.Objects = store
	instrument(acts, config.Config{LLMProvider: "fake", TTSProvider: "fake", LipSyncProvider: "fake"}, logger)

	d, ok := acts.Dialogue.(telemetry.Dialogue)
	require.True(t, ok, "the language model is timed")
	require.Equal(t, "fake", d.Provider)
	_, ok = acts.Speech.(telemetry.Speech)
	require.True(t, ok, "speech is timed")
	_, ok = acts.LipSync.(telemetry.LipSync)
	require.True(t, ok, "lip sync is timed")
	_, ok = acts.Objects.(telemetry.Store)
	require.True(t, ok, "uploads are timed")
	require.Same(t, logger, acts.Logger, "published updates are logged")

	require.Len(t, workerOptions(logger).Interceptors, 1, "every activity attempt is logged")
}
