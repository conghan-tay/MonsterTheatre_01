package e2e_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/tests/e2e/client"
	"github.com/conghan-tay/MonsterTheatre_01/tests/e2e/fixtures"
)

// resyncPause is a little over the server's limit of one resync a second.
const resyncPause = 1100 * time.Millisecond

// restartWorkerAfter makes sure a test that stops the shared worker leaves
// it running, whatever happens.
func restartWorkerAfter(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if !stack.WorkerRunning() {
			require.NoError(t, stack.StartWorker())
		}
	})
}

func requireRecovering(t *testing.T, msg client.Message, performanceID string) {
	t.Helper()
	require.Equal(t, client.TypeSnapshot, msg.Type, "message: %s", msg.Raw)
	require.Equal(t, 0, msg.Revision, "message: %s", msg.Raw)
	require.Equal(t, client.PhaseRecovering, msg.State.Phase, "message: %s", msg.Raw)
	require.Equal(t, 0, msg.State.Turn, "message: %s", msg.Raw)
	require.Equal(t, performanceID, msg.State.PerformanceID, "message: %s", msg.Raw)
	require.Empty(t, msg.State.Choices, "message: %s", msg.Raw)
	require.Empty(t, msg.State.Lines, "message: %s", msg.Raw)
}

// H8: the worker is stopped mid-performance and restarted; connected
// browsers stay connected and the run completes.
//
// This test stops the shared worker, so it does not run in parallel.
func TestARunSurvivesAWorkerRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	restartWorkerAfter(t)

	th := openTheatre(t, ctx, 1)
	opened := th.awaitAll(t, ctx, client.PhaseVoting, 2)
	require.Equal(t, 5, opened[0].Revision)

	// The worker dies with a round open, and stays down past its deadline:
	// nobody is there to close the round.
	require.NoError(t, stack.StopWorker())
	time.Sleep(time.Until(opened[0].State.Deadline) + time.Second)
	restarted := time.Now()
	require.NoError(t, stack.StartWorker())

	// The same two connections, never reopened, read on from where they
	// were: every later revision, in order, none missing.
	for i, conn := range th.conns {
		updates := readThrough(t, ctx, conn, opened[i].Revision)
		closed := updates[0].State
		require.Equal(t, client.PhasePreparingLine, closed.Phase)
		require.NotNil(t, closed.Winner)
		require.Equal(t, "no_votes", closed.Winner.DecidedBy)
	}
	require.GreaterOrEqual(t, time.Since(restarted), 5*voteWindow, "the remaining rounds ran after the restart")

	row, found, err := stack.PerformanceRow(ctx, th.roomID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "completed", row.Status)
}

// S11: a browser connecting while the worker is down is told the performance
// is recovering, then succeeds once the worker returns.
//
// This test stops the shared worker, so it does not run in parallel.
func TestConnectingWhileTheWorkerIsDownSaysRecovering(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	restartWorkerAfter(t)

	director, room, _ := newRoom(t, ctx)
	// Voicing turn 1 takes two retries, a few seconds, so that when the
	// worker returns the performance is still in turn 1 for a moment.
	fixtures.Script{
		Failures: []fixtures.Failure{{Op: fixtures.OpSpeech, Turn: 1, Attempts: 2}},
	}.Write(t, stack.ScriptDir, room.RoomID)
	member := enteredMember(t, ctx, room.ShareLink)
	first, err := member.ConnectRoom(ctx, room.RoomID)
	require.NoError(t, err)
	defer first.Close()
	requireWaitingSnapshot(t, ctx, first)

	status, started := start(t, ctx, director.Client, room.RoomID)
	require.Equal(t, http.StatusCreated, status)
	_, err = first.WaitForPhase(ctx, client.PhaseVoting, 1)
	require.NoError(t, err)
	vote(t, ctx, first, 1, "c2")

	require.NoError(t, stack.StopWorker())

	// The same member opens the room again, in another tab say. Nobody can
	// answer for the workflow, and the browser is told so.
	late, err := member.ConnectRoom(ctx, room.RoomID)
	require.NoError(t, err)
	defer late.Close()
	snapshot, err := late.Snapshot(ctx)
	require.NoError(t, err)
	requireRecovering(t, snapshot, started.PerformanceID)

	// Asking again changes nothing while the worker is down.
	again, err := late.Resync(ctx)
	require.NoError(t, err)
	requireRecovering(t, again, started.PerformanceID)

	require.NoError(t, stack.StartWorker())

	// Once it is back, asking again gets the real state, with the vote this
	// member cast before the worker went down.
	var real client.Message
	require.Eventually(t, func() bool {
		real, err = late.Resync(ctx)
		return err == nil && real.State.Phase != client.PhaseRecovering
	}, 30*time.Second, resyncPause, "last snapshot: %s", real.Raw)
	require.Equal(t, 1, real.State.Turn, "snapshot: %s", real.Raw)
	require.Contains(t, []string{client.PhaseVoting, client.PhasePreparingLine}, real.State.Phase, "snapshot: %s", real.Raw)
	require.Equal(t, "c2", real.State.YourVote, "snapshot: %s", real.Raw)
	require.Equal(t, started.PerformanceID, real.State.PerformanceID)

	// And from there the run's updates, each one revision on.
	readThrough(t, ctx, late, real.Revision)
}

// S12: a browser that misses an update sends resync and receives a fresh
// snapshot.
func TestResyncSendsAFreshSnapshot(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Before Start there is nothing to miss, and the answer says so.
	director, room, _ := newRoom(t, ctx)
	waiting, err := director.ConnectRoom(ctx, room.RoomID)
	require.NoError(t, err)
	defer waiting.Close()
	requireWaitingSnapshot(t, ctx, waiting)
	answer, err := waiting.Resync(ctx)
	require.NoError(t, err)
	require.Equal(t, client.PhaseWaiting, answer.State.Phase, "snapshot: %s", answer.Raw)
	require.Equal(t, 0, answer.Revision)

	th := openTheatre(t, ctx, 1)
	opened := th.awaitAll(t, ctx, client.PhaseVoting, 1)
	directorConn, memberConn := th.conns[0], th.conns[1]
	vote(t, ctx, memberConn, 1, "c3")

	// Mid-round: the current state, with this browser's own vote.
	snapshot, err := memberConn.Resync(ctx)
	require.NoError(t, err)
	require.Equal(t, opened[1].Revision, snapshot.Revision, "snapshot: %s", snapshot.Raw)
	require.Equal(t, client.PhaseVoting, snapshot.State.Phase, "snapshot: %s", snapshot.Raw)
	require.Equal(t, 1, snapshot.State.Turn)
	require.Equal(t, opened[1].State.Choices, snapshot.State.Choices)
	require.Equal(t, "c3", snapshot.State.YourVote, "snapshot: %s", snapshot.Raw)

	// Someone who has not voted is shown nobody's vote.
	theirs, err := directorConn.Resync(ctx)
	require.NoError(t, err)
	require.Equal(t, opened[0].Revision, theirs.Revision, "snapshot: %s", theirs.Raw)
	require.Empty(t, theirs.State.YourVote, "snapshot: %s", theirs.Raw)

	// The updates carry on from the snapshot: the next revision, and on.
	readThrough(t, ctx, memberConn, snapshot.Revision)
	readThrough(t, ctx, directorConn, theirs.Revision)

	// After the run, the answer is the premiere, from the row.
	finished, err := memberConn.Resync(ctx)
	require.NoError(t, err)
	require.Equal(t, lastRevision, finished.Revision, "snapshot: %s", finished.Raw)
	require.Equal(t, client.PhasePremiere, finished.State.Phase, "snapshot: %s", finished.Raw)
}

// A Start that wrote the row but never started the workflow leaves a
// performance that says it is running with nothing running it. A browser
// that connects is told it is recovering, and connecting does not start
// anything: only the director pressing Start again does (D043).
func TestAMissingWorkflowIsRepairedByStartNotByConnecting(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	director, room, _ := newRoom(t, ctx)
	member := enteredMember(t, ctx, room.ShareLink)
	performanceID, err := stack.InsertPerformance(ctx, room.RoomID)
	require.NoError(t, err)

	conn, err := member.ConnectRoom(ctx, room.RoomID)
	require.NoError(t, err)
	defer conn.Close()
	snapshot, err := conn.Snapshot(ctx)
	require.NoError(t, err)
	requireRecovering(t, snapshot, performanceID)

	time.Sleep(resyncPause)
	again, err := conn.Resync(ctx)
	require.NoError(t, err)
	requireRecovering(t, again, performanceID)

	status, started := start(t, ctx, director.Client, room.RoomID)
	require.Equal(t, http.StatusOK, status, "the performance already exists; Start only starts its workflow")
	require.Equal(t, performanceID, started.PerformanceID)
	require.Equal(t, "running", started.Status)

	// The browser that was waiting is in the room, so it gets the run from
	// its first update.
	readThrough(t, ctx, conn, 0)
}

// A run whose workflow ended without marking its row, as one ended by its
// timeout does: the first browser to connect finds out, the row is marked
// failed, and everyone in the room is told.
func TestConnectingToADeadRunMarksItFailed(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	th := openTheatre(t, ctx, 1)
	th.awaitAll(t, ctx, client.PhaseVoting, 1)
	require.NoError(t, stack.TerminateWorkflow(ctx, th.performanceID))

	requireDead := func(msg client.Message) {
		t.Helper()
		require.Equal(t, client.TypeSnapshot, msg.Type, "message: %s", msg.Raw)
		require.Equal(t, client.PhaseFailed, msg.State.Phase, "message: %s", msg.Raw)
		require.Equal(t, th.performanceID, msg.State.PerformanceID)
		require.NotNil(t, msg.State.Failure, "message: %s", msg.Raw)
		require.Equal(t, "internal_error", msg.State.Failure.Code, "message: %s", msg.Raw)
	}

	late, err := th.members[0].ConnectRoom(ctx, th.roomID)
	require.NoError(t, err)
	defer late.Close()
	snapshot, err := late.Snapshot(ctx)
	require.NoError(t, err)
	requireDead(snapshot)

	// The browsers that were already there, waiting for a round that will
	// never close, are sent the same.
	for _, conn := range th.conns {
		msg, err := conn.Next(ctx)
		require.NoError(t, err)
		requireDead(msg)
	}

	row, found, err := stack.PerformanceRow(ctx, th.roomID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "failed", row.Status)
	require.Equal(t, "internal_error", row.FailureReason)
}
