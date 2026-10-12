package temporalclient

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/config"
)

func TestOptionsForTheLocalDevServerHaveNoTLSAndNoCredentials(t *testing.T) {
	opts := Options(config.Config{TemporalAddress: "localhost:7233", TemporalNamespace: "default"}, slog.Default())

	require.Equal(t, "localhost:7233", opts.HostPort)
	require.Equal(t, "default", opts.Namespace)
	require.NotNil(t, opts.Logger)
	require.Nil(t, opts.ConnectionOptions.TLS)
	require.Nil(t, opts.Credentials)
}

// Temporal Cloud is reached over TLS and authenticates the API key.
func TestOptionsWithAnAPIKeyUseTLSAndTheKey(t *testing.T) {
	opts := Options(config.Config{
		TemporalAddress:   "theatre.a1b2c.tmprl.cloud:7233",
		TemporalNamespace: "theatre.a1b2c",
		TemporalAPIKey:    "not-a-real-key",
	}, slog.Default())

	require.Equal(t, "theatre.a1b2c.tmprl.cloud:7233", opts.HostPort)
	require.Equal(t, "theatre.a1b2c", opts.Namespace)
	require.NotNil(t, opts.ConnectionOptions.TLS)
	require.NotNil(t, opts.Credentials)
}
