package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/config"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/providers/fake"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/providers/wavespeed"
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
