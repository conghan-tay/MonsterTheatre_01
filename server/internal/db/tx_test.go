package db

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

var conflict = &pgconn.PgError{Code: "40001", Message: "change conflicts with another transaction (OC000)"}

// fakeDB hands out transactions whose commits fail with the queued errors,
// one per transaction, then succeed.
type fakeDB struct {
	commitErrs []error
	begun      int
	committed  int
	rolledBack int
}

func (f *fakeDB) Begin(context.Context) (pgx.Tx, error) {
	f.begun++
	return &fakeTx{db: f}, nil
}

type fakeTx struct {
	pgx.Tx // unused methods panic if called
	db     *fakeDB
	done   bool
}

func (t *fakeTx) Commit(context.Context) error {
	t.done = true
	if len(t.db.commitErrs) > 0 {
		err := t.db.commitErrs[0]
		t.db.commitErrs = t.db.commitErrs[1:]
		return err
	}
	t.db.committed++
	return nil
}

func (t *fakeTx) Rollback(context.Context) error {
	if !t.done {
		t.db.rolledBack++
	}
	return nil
}

func noSleep(context.Context, int) error { return nil }

func TestWithTxCommitsOnceWhenThereIsNoConflict(t *testing.T) {
	fake := &fakeDB{}
	runs := 0

	err := withTx(context.Background(), fake, func(pgx.Tx) error { runs++; return nil }, noSleep)

	require.NoError(t, err)
	require.Equal(t, 1, runs)
	require.Equal(t, 1, fake.committed)
}

func TestWithTxRetriesTheWholeTransactionWhenCommitConflicts(t *testing.T) {
	// Aurora DSQL reports conflicts at commit, so that is where they are simulated.
	fake := &fakeDB{commitErrs: []error{conflict, conflict}}
	runs := 0
	var sleeps []int

	err := withTx(context.Background(), fake, func(pgx.Tx) error { runs++; return nil },
		func(_ context.Context, attempt int) error { sleeps = append(sleeps, attempt); return nil })

	require.NoError(t, err)
	require.Equal(t, 3, runs, "the function runs again for each retry")
	require.Equal(t, 3, fake.begun)
	require.Equal(t, 1, fake.committed)
	require.Equal(t, []int{1, 2}, sleeps, "it backs off between attempts")
}

func TestWithTxRetriesWhenTheFunctionItselfConflicts(t *testing.T) {
	fake := &fakeDB{}
	runs := 0

	err := withTx(context.Background(), fake, func(pgx.Tx) error {
		runs++
		if runs == 1 {
			return conflict
		}
		return nil
	}, noSleep)

	require.NoError(t, err)
	require.Equal(t, 2, runs)
	require.Equal(t, 1, fake.rolledBack, "the failed attempt is rolled back")
	require.Equal(t, 1, fake.committed)
}

func TestWithTxGivesUpAfterTheAttemptLimit(t *testing.T) {
	fake := &fakeDB{commitErrs: []error{conflict, conflict, conflict, conflict, conflict, conflict}}
	runs := 0

	err := withTx(context.Background(), fake, func(pgx.Tx) error { runs++; return nil }, noSleep)

	require.Equal(t, maxTxAttempts, runs)
	require.ErrorContains(t, err, "still conflicting after 5 attempts")
	require.True(t, IsSerializationFailure(err), "the original conflict stays reachable")
}

func TestWithTxDoesNotRetryOtherErrors(t *testing.T) {
	fake := &fakeDB{}
	runs := 0
	boom := errors.New("unique violation or anything else")

	err := withTx(context.Background(), fake, func(pgx.Tx) error { runs++; return boom }, noSleep)

	require.ErrorIs(t, err, boom)
	require.Equal(t, 1, runs)
	require.Equal(t, 1, fake.rolledBack)
	require.Zero(t, fake.committed)
}

func TestWithTxStopsRetryingWhenTheContextEnds(t *testing.T) {
	fake := &fakeDB{commitErrs: []error{conflict, conflict}}
	runs := 0

	err := withTx(context.Background(), fake, func(pgx.Tx) error { runs++; return nil },
		func(context.Context, int) error { return context.Canceled })

	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, runs)
}
