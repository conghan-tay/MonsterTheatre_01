package db

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// serializationFailure is the SQLSTATE Aurora DSQL returns when a transaction
// conflicts with another one. DSQL has no row locks; it detects conflicts at
// commit and expects the application to retry (docs/decisions.md, D032).
const serializationFailure = "40001"

const maxTxAttempts = 5

// Beginner starts a transaction. *pgxpool.Pool satisfies it.
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// WithTx runs fn in a transaction and commits it. If fn or the commit fails
// with a serialization failure, the whole transaction is retried, so fn must
// be safe to run more than once. Every write goes through here.
//
// On local PostgreSQL there is normally nothing to retry; on Aurora DSQL this
// is what makes concurrent writes to the same row work.
func WithTx(ctx context.Context, db Beginner, fn func(tx pgx.Tx) error) error {
	return withTx(ctx, db, fn, backoff)
}

func withTx(ctx context.Context, db Beginner, fn func(tx pgx.Tx) error, sleep func(context.Context, int) error) error {
	var err error
	for attempt := 1; attempt <= maxTxAttempts; attempt++ {
		err = runTx(ctx, db, fn)
		if err == nil || !IsSerializationFailure(err) {
			return err
		}
		if attempt < maxTxAttempts {
			if sleepErr := sleep(ctx, attempt); sleepErr != nil {
				return sleepErr
			}
		}
	}
	return fmt.Errorf("transaction still conflicting after %d attempts: %w", maxTxAttempts, err)
}

func runTx(ctx context.Context, db Beginner, fn func(tx pgx.Tx) error) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	// Rollback after a successful commit is a no-op. It must run even if ctx
	// was cancelled, so the connection is not returned mid-transaction.
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck

	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// IsSerializationFailure reports whether err is a transaction conflict that
// is worth retrying.
func IsSerializationFailure(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == serializationFailure
}

// backoff waits 20ms, 40ms, 80ms, ... plus up to the same again at random, so
// transactions that collided do not collide again in step.
func backoff(ctx context.Context, attempt int) error {
	base := 20 * time.Millisecond << (attempt - 1)
	delay := base + time.Duration(rand.Int64N(int64(base)))
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
		return nil
	}
}
