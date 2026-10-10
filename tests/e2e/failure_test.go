package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/tests/e2e/client"
	"github.com/conghan-tay/MonsterTheatre_01/tests/e2e/fixtures"
)

// choicesRetryWindow is the test stack's setting (tests/e2e/harness/stack.go).
const choicesRetryWindow = 4 * time.Second

// scriptedRoom creates a room whose fake providers follow the script, with
// its director connected and the performance started.
func scriptedRoom(t *testing.T, ctx context.Context, script fixtures.Script) (*client.DirectorClient, client.RoomView, *client.Conn, client.StartResponse) {
	t.Helper()
	director, room, _ := newRoom(t, ctx)
	script.Write(t, stack.ScriptDir, room.RoomID)

	conn, err := director.ConnectRoom(ctx, room.RoomID)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	requireWaitingSnapshot(t, ctx, conn)

	status, started := start(t, ctx, director.Client, room.RoomID)
	require.Equal(t, http.StatusCreated, status)
	return director, room, conn, started
}

// readUpdates reads the next n updates, requiring them to be revisions
// first, first+1, ... with the phases of a normal run.
func readUpdates(t *testing.T, ctx context.Context, conn *client.Conn, first, n int) []client.Message {
	t.Helper()
	var updates []client.Message
	for revision := first; revision < first+n; revision++ {
		msg, err := conn.NextUpdate(ctx)
		require.NoError(t, err, "waiting for revision %d", revision)
		require.Equal(t, revision, msg.Revision, "message: %s", msg.Raw)
		phase, turn := expectedStep(revision)
		require.Equal(t, phase, msg.State.Phase, "revision %d; message: %s", revision, msg.Raw)
		require.Equal(t, turn, msg.State.Turn, "revision %d; message: %s", revision, msg.Raw)
		updates = append(updates, msg)
	}
	return updates
}

// requireFailed checks everything a failed run should leave behind: the
// final update, the row, what Start and a new connection say, and that
// voting is over.
func requireFailed(t *testing.T, ctx context.Context, director *client.DirectorClient, room client.RoomView, conn *client.Conn, started client.StartResponse, revision, turn, lines int, code string) client.Message {
	t.Helper()
	failed, err := conn.NextUpdate(ctx)
	require.NoError(t, err, "waiting for the failed update")
	require.Equal(t, client.PhaseFailed, failed.State.Phase, "message: %s", failed.Raw)
	require.Equal(t, revision, failed.Revision, "the failure is one revision after the last update; message: %s", failed.Raw)
	require.Equal(t, turn, failed.State.Turn, "message: %s", failed.Raw)
	require.Equal(t, started.PerformanceID, failed.State.PerformanceID)
	require.NotNil(t, failed.State.Failure, "message: %s", failed.Raw)
	require.Equal(t, code, failed.State.Failure.Code, "message: %s", failed.Raw)
	require.NotEmpty(t, failed.State.Failure.Message, "a sentence for the audience")
	require.Len(t, failed.State.Lines, lines, "the lines chosen before the failure are kept")
	require.Nil(t, failed.State.Winner)
	require.Nil(t, failed.State.Premiere)

	// The row was marked before the update was published.
	row, found, err := stack.PerformanceRow(ctx, room.RoomID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "failed", row.Status)
	require.Equal(t, code, row.FailureReason)
	require.Empty(t, row.Script, "a failed run saves no script")

	status, again := start(t, ctx, director.Client, room.RoomID)
	require.Equal(t, http.StatusOK, status, "Start does not run a failed performance again")
	require.Equal(t, started.PerformanceID, again.PerformanceID)
	require.Equal(t, "failed", again.Status)

	later, err := director.ConnectRoom(ctx, room.RoomID)
	require.NoError(t, err)
	defer later.Close()
	snapshot, err := later.Snapshot(ctx)
	require.NoError(t, err)
	require.Equal(t, client.PhaseFailed, snapshot.State.Phase, "snapshot: %s", snapshot.Raw)

	// Nothing more is published, and once the run has closed a vote is refused.
	var reply client.Message
	require.Eventually(t, func() bool {
		voter, err := director.ConnectRoom(ctx, room.RoomID)
		if err != nil {
			return false
		}
		defer voter.Close()
		if _, err := voter.Snapshot(ctx); err != nil {
			return false
		}
		reply, err = voter.Vote(ctx, turn, "c1")
		return err == nil && reply.Type == client.TypeError
	}, 5*time.Second, 100*time.Millisecond, "last reply: %s", reply.Raw)
	require.Equal(t, "vote_not_delivered", reply.Code)
	return failed
}

func audioKey(performanceID string, turn int) string {
	return fmt.Sprintf("performances/%s/turn-%d.mp3", performanceID, turn)
}

// S8: speech failing past its retry limit ends the run as failed, and
// browsers are told.
func TestSpeechFailingPastItsLimitFailsTheRun(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	director, room, conn, started := scriptedRoom(t, ctx, fixtures.Script{
		Failures: []fixtures.Failure{{Op: fixtures.OpSpeech, Turn: 2, Attempts: fixtures.Always}},
	})

	// Turn 1 and turn 2's round go as usual: the line is only voiced after
	// its winner is announced.
	readUpdates(t, ctx, conn, 1, 6)
	// Turn 2's line was chosen and shown, so it is among the lines kept,
	// although it was never voiced.
	requireFailed(t, ctx, director, room, conn, started, 7, 2, 2, "speech_failed")

	_, err := stack.StoredObject(audioKey(started.PerformanceID, 1))
	require.NoError(t, err, "turn 1 was voiced before the failure")
	_, err = stack.StoredObject(audioKey(started.PerformanceID, 2))
	require.Error(t, err, "turn 2 never was")
}

// S9 (first part): choice generation that fails briefly is retried, and the
// run carries on as if nothing had happened.
func TestChoiceGenerationRecoversFromABriefFailure(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	_, room, conn, _ := scriptedRoom(t, ctx, fixtures.Script{
		Choices:  map[int][]string{2: {"Recovered one.", "Recovered two.", "Recovered three."}},
		Failures: []fixtures.Failure{{Op: fixtures.OpChoices, Turn: 2, Attempts: 2}},
	})

	updates := readUpdates(t, ctx, conn, 1, lastRevision)
	voting := updates[4].State // revision 5: turn 2's round
	require.Equal(t, []client.Choice{
		{ID: "c1", Text: "Recovered one."}, {ID: "c2", Text: "Recovered two."}, {ID: "c3", Text: "Recovered three."},
	}, voting.Choices, "the lines come from the attempt that succeeded")

	row, _, err := stack.PerformanceRow(ctx, room.RoomID)
	require.NoError(t, err)
	require.Equal(t, "completed", row.Status)
	require.Empty(t, row.FailureReason)
}

// S9 (second part): choice generation failing past its limit ends the run as
// failed. There is no stand-in for lines the model did not write.
func TestChoiceGenerationFailingPastItsLimitFailsTheRun(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	director, room, conn, started := scriptedRoom(t, ctx, fixtures.Script{
		Failures: []fixtures.Failure{{Op: fixtures.OpChoices, Turn: 3, Attempts: fixtures.Always}},
	})

	// Two full turns, then turn 3 begins and its lines never arrive.
	updates := readUpdates(t, ctx, conn, 1, 7)
	began := time.Now()
	require.Equal(t, client.PhaseGenerating, updates[6].State.Phase)

	failed := requireFailed(t, ctx, director, room, conn, started, 8, 3, 2, "choices_failed")
	require.Empty(t, failed.State.Choices, "no round was opened")
	// It retries with growing pauses and gives up when the next try would
	// fall outside the window, so somewhere in the window's second half.
	require.GreaterOrEqual(t, time.Since(began), choicesRetryWindow/2, "it kept trying before giving up")
}

// S9 (third part): tagging failing past its limit falls back to the untagged
// line, and the run still completes.
func TestTaggingFailureFallsBackToTheUntaggedLine(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	_, room, conn, started := scriptedRoom(t, ctx, fixtures.Script{
		Failures: []fixtures.Failure{{Op: fixtures.OpTags, Turn: 1, Attempts: fixtures.Always}},
	})
	readUpdates(t, ctx, conn, 1, lastRevision)

	row, _, err := stack.PerformanceRow(ctx, room.RoomID)
	require.NoError(t, err)
	require.Equal(t, "completed", row.Status)
	var script struct {
		Turns []struct {
			Winner      client.Choice `json:"winner"`
			TaggedText  string        `json:"taggedText"`
			TagsApplied bool          `json:"tagsApplied"`
		} `json:"turns"`
	}
	require.NoError(t, json.Unmarshal(row.Script, &script), "script: %s", row.Script)
	require.Len(t, script.Turns, turns)

	require.Equal(t, script.Turns[0].Winner.Text, script.Turns[0].TaggedText, "turn 1 is spoken as chosen, without tags")
	require.False(t, script.Turns[0].TagsApplied)
	for i := 1; i < turns; i++ {
		require.Equal(t, "[excited] "+script.Turns[i].Winner.Text, script.Turns[i].TaggedText, "turn %d", i+1)
		require.True(t, script.Turns[i].TagsApplied, "turn %d", i+1)
	}

	// Turn 1 was still voiced.
	audio, err := stack.StoredObject(audioKey(started.PerformanceID, 1))
	require.NoError(t, err)
	require.NotEmpty(t, audio)
}
