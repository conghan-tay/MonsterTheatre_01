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

// lipSyncRoom creates a room whose fake providers follow the script, with
// its director connected, and starts it with lip sync asked for.
func lipSyncRoom(t *testing.T, ctx context.Context, script fixtures.Script) (*client.DirectorClient, client.RoomView, *client.Conn, client.StartResponse) {
	t.Helper()
	director, room, _ := newRoom(t, ctx)
	script.Write(t, stack.ScriptDir, room.RoomID)

	conn, err := director.ConnectRoom(ctx, room.RoomID)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	requireWaitingSnapshot(t, ctx, conn)

	resp, err := director.StartWithLipSync(ctx, room.RoomID)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.Status, "body: %s", resp.Body)
	var started client.StartResponse
	require.NoError(t, resp.Decode(&started))
	return director, room, conn, started
}

// readProgress reads the preparing_premiere updates after the turns, and
// returns them and the premiere that follows.
func readProgress(t *testing.T, ctx context.Context, conn *client.Conn) ([]client.Message, client.Message) {
	t.Helper()
	var progress []client.Message
	for revision := lastRevision; ; revision++ {
		msg, err := conn.NextUpdate(ctx)
		require.NoError(t, err, "waiting for revision %d", revision)
		require.Equal(t, revision, msg.Revision, "one revision after another; message: %s", msg.Raw)
		if msg.State.Phase == client.PhasePremiere {
			return progress, msg
		}
		require.Equal(t, client.PhasePreparingPremiere, msg.State.Phase, "message: %s", msg.Raw)
		require.Equal(t, turns, msg.State.Turn)
		require.Len(t, msg.State.Lines, turns, "the script so far stays")
		require.NotNil(t, msg.State.Progress, "message: %s", msg.Raw)
		require.Equal(t, turns, msg.State.Progress.Total)
		require.Less(t, msg.State.Progress.Ready, turns)
		if len(progress) > 0 {
			require.Greater(t, msg.State.Progress.Ready, progress[len(progress)-1].State.Progress.Ready, "the count goes up")
		}
		progress = append(progress, msg)
	}
}

// D055: with lip sync asked for, each line becomes a video; after the turns
// the room is told how many are ready, and the premiere carries them.
func TestALipSyncRunTellsItsProgressThenPremieresWithVideos(t *testing.T) {
	requireLocal(t, "the fake providers read their scripts from the worker's disk")
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Turns 1 and 2's videos take long enough to be waited for after the
	// turns end, about 16 seconds in.
	director, room, conn, started := lipSyncRoom(t, ctx, fixtures.Script{
		LipSyncDelays: map[int]int{1: 18_000, 2: 18_000},
	})

	// The turns are as without lip sync; nothing is seen before the premiere.
	readUpdates(t, ctx, conn, 1, 4)
	resp, err := director.FetchVideo(ctx, "/api/v1/performances/"+started.PerformanceID+"/turns/1/video")
	requireRefused(t, resp, err, http.StatusConflict, "performance_not_completed", "mid-run")
	readUpdates(t, ctx, conn, 5, 3*turns-4)

	progress, premiere := readProgress(t, ctx, conn)
	require.NotEmpty(t, progress, "turns 1 and 2 were still being made when the turns ended")
	require.LessOrEqual(t, progress[0].State.Progress.Ready, 6)

	// Every clip has its video, and the recording it is played with.
	require.Len(t, premiere.State.Premiere.Clips, turns)
	for _, clip := range premiere.State.Premiere.Clips {
		require.Equal(t, audioURL(started.PerformanceID, clip.Turn), clip.URL)
		require.NotEmpty(t, clip.VideoURL, "turn %d", clip.Turn)
		video, err := director.FetchVideo(ctx, clip.VideoURL)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, video.Status, "turn %d; body: %s", clip.Turn, video.Body)
		require.Equal(t, "video/mp4", video.Header.Get("Content-Type"))
		require.Equal(t, "ftyp", string(video.Body[4:8]), "an MP4")
	}

	// Someone who comes later is given the premiere at the revision it
	// was published at, with the same videos.
	later, err := director.ConnectRoom(ctx, room.RoomID)
	require.NoError(t, err)
	defer later.Close()
	snapshot, err := later.Snapshot(ctx)
	require.NoError(t, err)
	require.Equal(t, premiere.Revision, snapshot.Revision, "snapshot: %s", snapshot.Raw)
	require.Equal(t, premiere.State.Premiere.Clips, snapshot.State.Premiere.Clips)
}

// D055: a line whose video cannot be made is played as without lip sync.
func TestALineWhoseLipSyncFailsIsPlayedWithoutAVideo(t *testing.T) {
	requireLocal(t, "the fake providers read their scripts from the worker's disk")
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	director, room, conn, started := lipSyncRoom(t, ctx, fixtures.Script{
		Failures: []fixtures.Failure{{Op: fixtures.OpLipSync, Turn: 3, Attempts: 1}},
	})
	readUpdates(t, ctx, conn, 1, 3*turns)
	_, premiere := readProgress(t, ctx, conn)

	for _, clip := range premiere.State.Premiere.Clips {
		if clip.Turn == 3 {
			require.Empty(t, clip.VideoURL, "turn 3 is played with its portrait")
			continue
		}
		require.NotEmpty(t, clip.VideoURL, "turn %d", clip.Turn)
	}
	resp, err := director.FetchVideo(ctx, "/api/v1/performances/"+started.PerformanceID+"/turns/3/video")
	requireRefused(t, resp, err, http.StatusNotFound, "not_found", "turn 3 has no video")

	row, _, err := stack.PerformanceRow(ctx, room.RoomID)
	require.NoError(t, err)
	require.Equal(t, "completed", row.Status, "the run still completed")
}
