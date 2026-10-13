package e2e_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Every query the server filters by something other than a primary key or a
// unique column has an index for it. There are two, and both run often: the
// names of a room's audience, read on every join, and a director's rooms.
//
// The statements here are the ones in server/internal/audience/store.go
// (Nicknames) and server/internal/rooms/store.go (ListByDirector). If
// either changes there, change it here.
func TestQueriesThatFilterByAForeignKeyHaveAnIndex(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const anyID = "00000000-0000-4000-8000-000000000000"

	for _, tc := range []struct {
		index, on, query string
	}{
		{
			index: "audience_sessions_room_id_created_at_idx",
			on:    "(room_id, created_at)",
			query: `SELECT nickname FROM audience_sessions WHERE room_id = $1 ORDER BY created_at, id`,
		},
		{
			index: "rooms_director_id_created_at_idx",
			on:    "(director_id, created_at)",
			query: `SELECT id, scene_topic, created_at FROM rooms WHERE director_id = $1 ORDER BY created_at DESC, id`,
		},
	} {
		definition, err := stack.IndexDefinition(ctx, tc.index)
		require.NoError(t, err)
		require.NotEmpty(t, definition, "there is an index named %s", tc.index)
		require.Contains(t, definition, tc.on, "on the columns the query filters and orders by")

		valid, err := stack.IndexIsValid(ctx, tc.index)
		require.NoError(t, err)
		require.True(t, valid, "%s has finished building", tc.index)

		// Whether Aurora DSQL lets a connection rule out a full read, as
		// the next step does, is not known; a deployment is checked for the
		// index and its columns only.
		if stack.Remote {
			continue
		}
		plan, err := stack.PlanWithoutScanning(ctx, tc.query, anyID)
		require.NoError(t, err)
		require.Contains(t, plan, tc.index, "the query can be answered from the index; plan:\n%s", plan)
	}
}

// A session is looked up by the hash of its cookie, so the column has a
// unique index. Aurora DSQL cannot index bytea, so the hash is kept as text
// (its hex), which both databases index.
func TestSessionHashesAreStoredAsText(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, table := range []string{"director_sessions", "audience_sessions"} {
		dataType, err := stack.ColumnType(ctx, table, "credential_hash")
		require.NoError(t, err)
		require.Equal(t, "text", dataType, "%s.credential_hash", table)
	}
}
