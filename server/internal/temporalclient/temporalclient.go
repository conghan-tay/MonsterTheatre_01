// Package temporalclient builds the Temporal connection settings the server
// and the worker share.
package temporalclient

import (
	"crypto/tls"
	"log/slog"

	"go.temporal.io/sdk/client"
	temporallog "go.temporal.io/sdk/log"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/config"
)

// Options returns how to reach Temporal. With an API key that is Temporal
// Cloud, over TLS and authenticated by the key; without one it is the local
// dev server, which uses neither.
func Options(cfg config.Config, logger *slog.Logger) client.Options {
	opts := client.Options{
		HostPort:  cfg.TemporalAddress,
		Namespace: cfg.TemporalNamespace,
		Logger:    temporallog.NewStructuredLogger(logger),
	}
	if cfg.TemporalAPIKey != "" {
		opts.ConnectionOptions = client.ConnectionOptions{TLS: &tls.Config{MinVersion: tls.VersionTLS12}}
		opts.Credentials = client.NewAPIKeyStaticCredentials(cfg.TemporalAPIKey)
	}
	return opts
}
