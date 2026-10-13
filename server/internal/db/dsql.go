package db

import (
	"context"
	"fmt"

	"github.com/awslabs/aurora-dsql-connectors/go/pgx/dsql"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/config"
)

// OpenDSQL builds a pool for an Aurora DSQL cluster. There is no password:
// the connector asks AWS for a short-lived token for each new connection,
// using the process's own AWS identity, and replaces connections before the
// cluster's one-hour limit. Like Open, it does not connect until first use.
func OpenDSQL(ctx context.Context, endpoint, user string) (*pgxpool.Pool, error) {
	pool, err := dsql.NewPool(ctx, dsql.Config{Host: endpoint, User: user})
	if err != nil {
		return nil, fmt.Errorf("open Aurora DSQL pool: %w", err)
	}
	return pool, nil
}

// OpenConfigured builds the pool the configuration asks for: Aurora DSQL
// when an endpoint is set, and otherwise PostgreSQL from the connection
// string. It also says which, for the startup log.
func OpenConfigured(ctx context.Context, cfg config.Config) (*pgxpool.Pool, string, error) {
	if cfg.DSQLEndpoint != "" {
		pool, err := OpenDSQL(ctx, cfg.DSQLEndpoint, cfg.DSQLUser)
		return pool, "aurora-dsql as " + cfg.DSQLUser, err
	}
	pool, err := Open(ctx, cfg.DatabaseURL)
	return pool, "postgresql", err
}
