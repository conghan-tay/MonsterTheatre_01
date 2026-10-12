package e2e_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/tests/e2e/client"
)

// These tests use two server processes that share a database, Redis, and
// Temporal, as two instances behind a load balancer would. One process is
// deployed today; these prove nothing on the live path depends on that.

// enteredOn is an audience member who entered through the given server.
func enteredOn(t *testing.T, ctx context.Context, server, shareLink, nickname string) *client.AudienceClient {
	t.Helper()
	member := client.NewAudience(server)
	resp, err := member.EnterAs(ctx, shareLink, nickname)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.Status, "enter the theatre; body: %s", resp.Body)
	return member
}

// A join handled by one server reaches the director's page on another.
func TestAJoinOnOneServerReachesTheDirectorOnAnother(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	first, second := stack.BaseURL, stack.SecondBaseURL

	director, room, _ := newRoom(t, ctx) // logged in through the first server
	onSecond, err := director.On(second).ConnectRoom(ctx, room.RoomID)
	require.NoError(t, err)
	defer onSecond.Close()
	requireWaitingSnapshot(t, ctx, onSecond)

	told := func(conn *client.Conn, nicknames ...string) {
		t.Helper()
		msg, err := conn.Audience(ctx)
		require.NoError(t, err)
		require.Equal(t, nicknames, msg.Nicknames, "message: %s", msg.Raw)
		require.Equal(t, len(nicknames), msg.Count, "message: %s", msg.Raw)
	}
	told(onSecond, []string{}...)

	// Ana enters through the first server. The director is on the second.
	ana := enteredOn(t, ctx, first, room.ShareLink, "Ana")
	told(onSecond, "Ana")

	// Ben enters through the second: the same route, though nothing has to
	// leave the server this time.
	enteredOn(t, ctx, second, room.ShareLink, "Ben")
	told(onSecond, "Ana", "Ben")

	// The director opens the room on the first server as well. Ana renames
	// through the second. Both of the director's pages are told.
	onFirst, err := director.ConnectRoom(ctx, room.RoomID)
	require.NoError(t, err)
	defer onFirst.Close()
	requireWaitingSnapshot(t, ctx, onFirst)
	told(onFirst, "Ana", "Ben")

	resp, err := (&client.AudienceClient{Client: ana.On(second)}).EnterAs(ctx, room.ShareLink, "Anabel")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.Status, "the same seat, by its cookie, on another server; body: %s", resp.Body)
	told(onFirst, "Anabel", "Ben")
	told(onSecond, "Anabel", "Ben")
}

// A run found dead by a browser on one server is announced to browsers
// waiting on another.
func TestADeadRunFoundOnOneServerIsAnnouncedOnAnother(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	th := openTheatre(t, ctx, 1) // everyone connected to the first server
	th.awaitAll(t, ctx, client.PhaseVoting, 1)
	require.NoError(t, stack.TerminateWorkflow(ctx, th.performanceID))

	requireDead := func(msg client.Message) {
		t.Helper()
		require.Equal(t, client.TypeSnapshot, msg.Type, "message: %s", msg.Raw)
		require.Equal(t, client.PhaseFailed, msg.State.Phase, "message: %s", msg.Raw)
		require.NotNil(t, msg.State.Failure, "message: %s", msg.Raw)
		require.Equal(t, "internal_error", msg.State.Failure.Code, "message: %s", msg.Raw)
	}

	// The same audience member opens the room through the second server,
	// which is the one that finds the run dead.
	late, err := th.members[0].On(stack.SecondBaseURL).ConnectRoom(ctx, th.roomID)
	require.NoError(t, err)
	defer late.Close()
	snapshot, err := late.Snapshot(ctx)
	require.NoError(t, err)
	requireDead(snapshot)

	for _, conn := range th.conns {
		msg, err := conn.Next(ctx)
		require.NoError(t, err)
		requireDead(msg)
	}
}

// A whole performance with the director on one server and an audience
// member on another: the same updates reach both, a vote cast through the
// second server is counted, and a recording can be fetched from it.
func TestAPerformanceAcrossTwoServers(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	first, second := stack.BaseURL, stack.SecondBaseURL

	director, room, _ := newRoom(t, ctx)
	member := enteredOn(t, ctx, second, room.ShareLink, "Ana")

	directorConn, err := director.ConnectRoom(ctx, room.RoomID)
	require.NoError(t, err)
	defer directorConn.Close()
	requireWaitingSnapshot(t, ctx, directorConn)
	memberConn, err := member.ConnectRoom(ctx, room.RoomID)
	require.NoError(t, err)
	defer memberConn.Close()
	requireWaitingSnapshot(t, ctx, memberConn)
	require.NotEqual(t, first, member.BaseURL, "the member is on the other server")

	status, started := start(t, ctx, director.Client, room.RoomID)
	require.Equal(t, http.StatusCreated, status)

	// Turn 1: the member's vote goes up through the second server.
	_, err = memberConn.WaitForPhase(ctx, client.PhaseVoting, 1)
	require.NoError(t, err)
	vote(t, ctx, memberConn, 1, "c2")
	closed, err := directorConn.WaitForPhase(ctx, client.PhasePreparingLine, 1)
	require.NoError(t, err)
	require.Equal(t, map[string]int{"c1": 0, "c2": 1, "c3": 0}, closed.State.Winner.Counts, "message: %s", closed.Raw)
	require.Equal(t, "majority", closed.State.Winner.DecidedBy)

	// The rest of the run, on both: every revision, and the same bytes.
	fromFirst := readThrough(t, ctx, directorConn, closed.Revision)
	_, err = memberConn.WaitForPhase(ctx, client.PhasePreparingLine, 1)
	require.NoError(t, err)
	fromSecond := readThrough(t, ctx, memberConn, closed.Revision)
	require.Len(t, fromSecond, len(fromFirst))
	for i := range fromFirst {
		require.JSONEq(t, string(fromFirst[i].Raw), string(fromSecond[i].Raw), "revision %d", fromFirst[i].Revision)
	}

	premiere := fromSecond[len(fromSecond)-1]
	require.Equal(t, client.PhasePremiere, premiere.State.Phase)
	resp, err := member.FetchAudio(ctx, premiere.State.Premiere.Clips[0].URL)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.Status, "a recording, from the second server; body: %s", resp.Body)
	stored, err := stack.StoredObject(audioKey(started.PerformanceID, 1))
	require.NoError(t, err)
	require.Equal(t, stored, resp.Body)
}
