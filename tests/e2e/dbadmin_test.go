package e2e_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// These tests run the dbadmin command against a database of their own,
// because clearing the shared one would take the other tests' rows with it.
// The rows are written straight in: no server is involved.

type seeded struct {
	conn *pgx.Conn
	dir  string
	url  string
}

const (
	testDirector  = "11111111-0000-4000-8000-000000000001" // director-a530cd3441f8
	webDirector   = "11111111-0000-4000-8000-000000000002" // web-e2e-1760257000
	realDirector  = "11111111-0000-4000-8000-000000000003" // conghan
	lookalike     = "11111111-0000-4000-8000-000000000004" // director-bob
	testRoom      = "22222222-0000-4000-8000-000000000001"
	webRoom       = "22222222-0000-4000-8000-000000000002"
	realRoom      = "22222222-0000-4000-8000-000000000003"
	lookalikeRoom = "22222222-0000-4000-8000-000000000004"
	testPerf      = "33333333-0000-4000-8000-000000000001"
	realPerf      = "33333333-0000-4000-8000-000000000003"
)

// seed fills a scratch database and object directory with two test
// accounts, a person's account, and an account whose name only resembles a
// test's, each with a room; two of the rooms have a performance.
func seed(t *testing.T, ctx context.Context) *seeded {
	t.Helper()
	requireLocal(t, "it needs a database it can empty")

	url, drop, err := stack.ScratchDatabase(ctx)
	require.NoError(t, err)
	t.Cleanup(drop)
	conn, err := pgx.Connect(ctx, url)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close(context.Background()) })
	s := &seeded{conn: conn, dir: t.TempDir(), url: url}

	exec := func(sql string, args ...any) {
		t.Helper()
		_, err := conn.Exec(ctx, sql, args...)
		require.NoError(t, err, sql)
	}
	for _, d := range []struct{ id, username string }{
		{testDirector, "director-a530cd3441f8"},
		{webDirector, "web-e2e-1760257000"},
		{realDirector, "conghan"},
		{lookalike, "director-bob"},
	} {
		exec(`INSERT INTO directors (id, username, password_hash, created_at) VALUES ($1, $2, 'x', now())`, d.id, d.username)
		// Two logins each.
		for i := range 2 {
			exec(`INSERT INTO director_sessions (id, director_id, credential_hash, created_at, expires_at)
			      VALUES (gen_random_uuid(), $1, $2, now(), now())`, d.id, d.username+string(rune('a'+i)))
		}
	}
	for _, r := range []struct{ id, director string }{
		{testRoom, testDirector}, {webRoom, webDirector}, {realRoom, realDirector}, {lookalikeRoom, lookalike},
	} {
		exec(`INSERT INTO rooms (id, director_id, scene_topic, scene_description, scene_genre,
		        character_a_name, character_a_brief, character_a_voice_id, character_a_portrait_key,
		        character_b_name, character_b_brief, character_b_voice_id, character_b_portrait_key,
		        audience_count, created_at)
		      VALUES ($1, $2, 't', 'd', 'g', 'a', 'a', 'a', 'a', 'b', 'b', 'b', 'b', 3, now())`, r.id, r.director)
		for i := range 3 {
			exec(`INSERT INTO audience_sessions (id, room_id, credential_hash, nickname, created_at, last_seen_at, expires_at)
			      VALUES (gen_random_uuid(), $1, $2, 'n', now(), now(), now())`, r.id, r.id+string(rune('a'+i)))
		}
		s.put(t, "rooms/"+r.id+"/portrait-a")
		s.put(t, "rooms/"+r.id+"/portrait-b")
	}
	for _, p := range []struct{ id, room string }{{testPerf, testRoom}, {realPerf, realRoom}} {
		exec(`INSERT INTO performances (id, room_id, status, created_at) VALUES ($1, $2, 'completed', now())`, p.id, p.room)
		s.put(t, "performances/"+p.id+"/turn-1.mp3")
		s.put(t, "performances/"+p.id+"/turn-2.mp3")
	}
	return s
}

// put writes an object the way the filesystem store keeps one: the file, and
// a second file with its content type.
func (s *seeded) put(t *testing.T, key string) {
	t.Helper()
	path := filepath.Join(s.dir, filepath.FromSlash(key))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(path+".contenttype", []byte("text/plain"), 0o644))
}

func (s *seeded) has(key string) bool {
	_, err := os.Stat(filepath.Join(s.dir, filepath.FromSlash(key)))
	return err == nil
}

// counts returns the rows in each table, in a fixed order: directors,
// director sessions, rooms, audience sessions, performances.
func (s *seeded) counts(t *testing.T, ctx context.Context) [5]int {
	t.Helper()
	var c [5]int
	for i, table := range []string{"directors", "director_sessions", "rooms", "audience_sessions", "performances"} {
		require.NoError(t, s.conn.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&c[i]))
	}
	return c
}

func (s *seeded) usernames(t *testing.T, ctx context.Context) []string {
	t.Helper()
	rows, err := s.conn.Query(ctx, "SELECT username FROM directors ORDER BY username")
	require.NoError(t, err)
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	return names
}

var seededCounts = [5]int{4, 8, 4, 12, 2}

func TestClearWithoutYesChangesNothingAndSaysWhatWouldGo(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s := seed(t, ctx)

	for scope, want := range map[string][]string{
		"test":  {"would remove directors          2", "would remove director_sessions  4", "would remove rooms              2", "would remove audience_sessions  6", "would remove performances       1"},
		"rooms": {"would remove directors          0", "would remove director_sessions  0", "would remove rooms              4", "would remove audience_sessions  12", "would remove performances       2"},
		"all":   {"would remove directors          4", "would remove director_sessions  8", "would remove rooms              4", "would remove audience_sessions  12", "would remove performances       2"},
	} {
		out, err := stack.RunAdmin(ctx, s.url, s.dir, "clear", "-scope", scope)
		require.NoError(t, err, out)
		for _, line := range want {
			require.Contains(t, out, line, "scope %s", scope)
		}
		require.Contains(t, out, "nothing was changed")
	}
	require.Equal(t, seededCounts, s.counts(t, ctx))
	require.True(t, s.has("rooms/"+testRoom+"/portrait-a"))
	require.True(t, s.has("performances/"+testPerf+"/turn-1.mp3"))
}

func TestClearTestRemovesOnlyWhatTheTestSuitesMade(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s := seed(t, ctx)

	out, err := stack.RunAdmin(ctx, s.url, s.dir, "clear", "-scope", "test", "-yes")
	require.NoError(t, err, out)
	// Two test rooms with two portraits each, and one performance's two clips.
	require.Contains(t, out, "removed stored objects     6")

	require.Equal(t, []string{"conghan", "director-bob"}, s.usernames(t, ctx),
		"a person's account stays, and so does one whose name only resembles a test's")
	require.Equal(t, [5]int{2, 4, 2, 6, 1}, s.counts(t, ctx))

	for _, gone := range []string{"rooms/" + testRoom + "/portrait-a", "rooms/" + webRoom + "/portrait-b", "performances/" + testPerf + "/turn-1.mp3"} {
		require.False(t, s.has(gone), "%s is deleted", gone)
	}
	for _, kept := range []string{"rooms/" + realRoom + "/portrait-a", "rooms/" + lookalikeRoom + "/portrait-b", "performances/" + realPerf + "/turn-2.mp3"} {
		require.True(t, s.has(kept), "%s is kept", kept)
	}

	// Running it again finds nothing to do.
	out, err = stack.RunAdmin(ctx, s.url, s.dir, "clear", "-scope", "test", "-yes")
	require.NoError(t, err, out)
	require.Contains(t, out, "removed directors          0")
	require.Equal(t, [5]int{2, 4, 2, 6, 1}, s.counts(t, ctx))
}

func TestClearRoomsKeepsEveryDirectorAndTheirLogins(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s := seed(t, ctx)

	out, err := stack.RunAdmin(ctx, s.url, s.dir, "clear", "-scope", "rooms", "-yes")
	require.NoError(t, err, out)
	require.Contains(t, out, "removed stored objects     12")
	require.Equal(t, [5]int{4, 8, 0, 0, 0}, s.counts(t, ctx))
	require.False(t, s.has("rooms/"+realRoom+"/portrait-a"))
	require.False(t, s.has("performances/"+realPerf+"/turn-1.mp3"))
}

func TestClearAllEmptiesEveryTableAndLeavesTheSchema(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s := seed(t, ctx)

	out, err := stack.RunAdmin(ctx, s.url, s.dir, "clear", "-scope", "all", "-yes")
	require.NoError(t, err, out)
	require.Equal(t, [5]int{0, 0, 0, 0, 0}, s.counts(t, ctx))
	require.False(t, s.has("rooms/"+lookalikeRoom+"/portrait-a"))

	var version int
	require.NoError(t, s.conn.QueryRow(ctx, "SELECT max(version_id) FROM goose_db_version").Scan(&version))
	require.Equal(t, 9, version, "the migration record is untouched")

	out, err = stack.RunAdmin(ctx, s.url, s.dir, "counts")
	require.NoError(t, err, out)
	require.Contains(t, out, "directors          0")
}

func TestClearRefusesAnUnknownScope(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s := seed(t, ctx)

	for _, args := range [][]string{{"clear"}, {"clear", "-scope", "everything", "-yes"}, {"clear", "-scope", "all", "-yes", "extra"}} {
		out, err := stack.RunAdmin(ctx, s.url, s.dir, args...)
		require.Error(t, err, "%v: %s", args, out)
	}
	require.Equal(t, seededCounts, s.counts(t, ctx))
}

func TestQueryRunsASelectAndRefusesAnythingElse(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s := seed(t, ctx)

	out, err := stack.RunAdmin(ctx, s.url, s.dir, "query", "SELECT username FROM directors WHERE username = 'conghan'")
	require.NoError(t, err, out)
	require.Equal(t, "username\nconghan\n", out)

	out, err = stack.RunAdmin(ctx, s.url, s.dir, "query", "DELETE FROM audience_sessions")
	require.Error(t, err, out)
	// A function that writes gets past the statement check; the read-only
	// transaction is what stops it.
	out, err = stack.RunAdmin(ctx, s.url, s.dir, "query", "SELECT setval(pg_get_serial_sequence('goose_db_version', 'id'), 999)")
	require.Error(t, err, out)
	require.Equal(t, seededCounts, s.counts(t, ctx))
}
