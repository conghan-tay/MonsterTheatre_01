// Package db builds the PostgreSQL connection pool.
//
// Everything else in the application takes a *pgxpool.Pool and does not care
// where it came from. Locally the pool is built from a connection string; on
// AWS it will be built by the Aurora DSQL connector (docs/decisions.md, D032).
package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Open builds a pool from a connection string. It does not connect until the
// pool is first used, so a database that is briefly unavailable at startup
// shows up in the health check and not as a crash.
func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open database pool: %w", err)
	}
	return pool, nil
}
