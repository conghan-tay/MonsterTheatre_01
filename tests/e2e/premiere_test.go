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
)

const (
	audioCacheControl = "private, max-age=604800, immutable"
	// unknownPerformanceID is a well-formed ID that no performance has.
	unknownPerformanceID = "00000000-0000-4000-8000-000000000000"
)

func audioURL(performanceID string, turn any) string {
	return fmt.Sprintf("/api/v1/performances/%s/turns/%v/audio", performanceID, turn)
}

// stateOf returns a snapshot's or update's state exactly as it was sent.
func stateOf(t *testing.T, msg client.Message) string {
	t.Helper()
	var envelope struct {
		State json.RawMessage `json:"state"`
	}
	require.NoError(t, json.Unmarshal(msg.Raw, &envelope), "message: %s", msg.Raw)
	return string(envelope.State)
}

func requireRefused(t *testing.T, resp *client.Response, err error, status int, code, why string) {
	t.Helper()
	require.NoError(t, err)
	require.Equal(t, status, resp.Status, "%s; body: %s", why, resp.Body)
	require.Equal(t, code, errorCode(t, resp), "%s; body: %s", why, resp.Body)
}

// H7: after completion, all eight clips can be fetched, including by range
// request, and a browser connecting later gets the premiere as its snapshot.
func TestRecordingsAreServedAndALateBrowserGetsThePremiere(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	director, room, _ := newRoom(t, ctx)
	member := enteredMember(t, ctx, room.ShareLink)
	conn, err := director.ConnectRoom(ctx, room.RoomID)
	require.NoError(t, err)
	defer conn.Close()
	requireWaitingSnapshot(t, ctx, conn)

	status, started := start(t, ctx, director.Client, room.RoomID)
	require.Equal(t, http.StatusCreated, status)
	premiere := readThrough(t, ctx, conn, 0)[lastRevision-1]
	require.NotNil(t, premiere.State.Premiere, "message: %s", premiere.Raw)
	clips := premiere.State.Premiere.Clips
	require.Len(t, clips, turns)

	// Every clip, at the URL the premiere gave, for both kinds of viewer.
	browsers := []struct {
		who     string
		browser *client.Client
	}{{"the director", director.Client}, {"an audience member", member.Client}}
	var stored []byte
	for i, clip := range clips {
		stored, err = stack.StoredObject(audioKey(started.PerformanceID, i+1))
		require.NoError(t, err)
		for _, b := range browsers {
			resp, err := b.browser.FetchAudio(ctx, clip.URL)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.Status, "turn %d for %s; body: %s", i+1, b.who, resp.Body)
			require.Equal(t, "audio/mpeg", resp.Header.Get("Content-Type"))
			require.Equal(t, stored, resp.Body, "turn %d for %s is the saved recording", i+1, b.who)
			require.Equal(t, "bytes", resp.Header.Get("Accept-Ranges"), "the endpoint says it answers range requests")
			require.Equal(t, audioCacheControl, resp.Header.Get("Cache-Control"))
			require.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
		}
	}

	// Range requests, on the last clip: what an audio element sends to seek.
	url, size := clips[turns-1].URL, len(stored)
	require.Greater(t, size, 200, "the fake recording is long enough to take ranges from")
	for _, tc := range []struct {
		byteRange  string
		start, end int // the bytes expected, end inclusive
	}{
		{"bytes=0-99", 0, 99},
		{fmt.Sprintf("bytes=%d-", size-170), size - 170, size - 1},
		{"bytes=-10", size - 10, size - 1},
	} {
		resp, err := member.FetchAudioRange(ctx, url, tc.byteRange)
		require.NoError(t, err)
		require.Equal(t, http.StatusPartialContent, resp.Status, "%s; body: %s", tc.byteRange, resp.Body)
		require.Equal(t, fmt.Sprintf("bytes %d-%d/%d", tc.start, tc.end, size), resp.Header.Get("Content-Range"), tc.byteRange)
		require.Equal(t, stored[tc.start:tc.end+1], resp.Body, tc.byteRange)
		require.Equal(t, "audio/mpeg", resp.Header.Get("Content-Type"), tc.byteRange)
	}
	resp, err := member.FetchAudioRange(ctx, url, fmt.Sprintf("bytes=%d-", size+1000))
	require.NoError(t, err)
	require.Equal(t, http.StatusRequestedRangeNotSatisfiable, resp.Status, "a range past the end; body: %s", resp.Body)

	// The start time on the row is the one browsers were given.
	row, found, err := stack.PerformanceRow(ctx, room.RoomID)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, row.PremiereStartsAt, "the premiere's start time is saved with the script")
	require.True(t, premiere.State.Premiere.StartsAt.Equal(*row.PremiereStartsAt),
		"row: %s; update: %s", row.PremiereStartsAt, premiere.State.Premiere.StartsAt)

	// A browser that connects now is sent what everyone who watched holds:
	// the premiere, at its revision. That goes for someone who was in the
	// room and for someone who enters only now.
	newcomer := enteredMember(t, ctx, room.ShareLink)
	for _, b := range []struct {
		who     string
		browser *client.Client
	}{{"a member who was there", member.Client}, {"a newcomer", newcomer.Client}} {
		later, err := b.browser.ConnectRoom(ctx, room.RoomID)
		require.NoError(t, err)
		snapshot, err := later.Snapshot(ctx)
		require.NoError(t, err)
		require.Equal(t, lastRevision, snapshot.Revision, "%s; snapshot: %s", b.who, snapshot.Raw)
		require.Equal(t, client.PhasePremiere, snapshot.State.Phase, "%s; snapshot: %s", b.who, snapshot.Raw)
		require.JSONEq(t, stateOf(t, premiere), stateOf(t, snapshot), "%s gets the premiere exactly as it was published", b.who)
		require.NoError(t, later.Close())

		resp, err := b.browser.FetchAudio(ctx, snapshot.State.Premiere.Clips[0].URL)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.Status, "%s can play it; body: %s", b.who, resp.Body)
	}
}

// S10: audio is not served before the performance completes, or to someone
// outside the room.
func TestAudioIsRefusedBeforeCompletionAndToOutsiders(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	director, room, _ := newRoom(t, ctx)
	member := enteredMember(t, ctx, room.ShareLink)
	otherDirector, otherRoom, _ := newRoom(t, ctx)
	otherMember := enteredMember(t, ctx, otherRoom.ShareLink)
	anonymous := client.New(stack.BaseURL)

	conn, err := director.ConnectRoom(ctx, room.RoomID)
	require.NoError(t, err)
	defer conn.Close()
	requireWaitingSnapshot(t, ctx, conn)
	status, started := start(t, ctx, director.Client, room.RoomID)
	require.Equal(t, http.StatusCreated, status)
	turnOne := audioURL(started.PerformanceID, 1)

	outsidersAreRefused := func(when string) {
		t.Helper()
		resp, err := anonymous.FetchAudio(ctx, turnOne)
		requireRefused(t, resp, err, http.StatusUnauthorized, "not_logged_in", when+", with no session")
		resp, err = otherDirector.FetchAudio(ctx, turnOne)
		requireRefused(t, resp, err, http.StatusForbidden, "not_your_room", when+", as another director")
		resp, err = otherMember.FetchAudio(ctx, turnOne)
		requireRefused(t, resp, err, http.StatusUnauthorized, "not_logged_in", when+", as a member of another room")
	}

	// Turn 2 has begun, so turn 1's recording is saved: what follows is
	// refused by rule, not for want of a file.
	readUpdates(t, ctx, conn, 1, 4)
	_, err = stack.StoredObject(audioKey(started.PerformanceID, 1))
	require.NoError(t, err, "turn 1's recording is in the object store")

	for who, browser := range map[string]*client.Client{"the director": director.Client, "an audience member": member.Client} {
		resp, err := browser.FetchAudio(ctx, turnOne)
		requireRefused(t, resp, err, http.StatusConflict, "performance_not_completed", "mid-run, as "+who)
		resp, err = browser.FetchAudioRange(ctx, turnOne, "bytes=0-99")
		requireRefused(t, resp, err, http.StatusConflict, "performance_not_completed", "mid-run by range, as "+who)
	}
	outsidersAreRefused("mid-run")

	readUpdates(t, ctx, conn, 5, lastRevision-4)
	resp, err := director.FetchAudio(ctx, turnOne)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.Status, "once completed, the same request is answered; body: %s", resp.Body)
	outsidersAreRefused("after completion")

	// What is not there. Someone with no login is told only to log in,
	// whether or not the performance exists.
	for _, turn := range []any{0, 9, "x"} {
		resp, err := director.FetchAudio(ctx, audioURL(started.PerformanceID, turn))
		requireRefused(t, resp, err, http.StatusNotFound, "not_found", fmt.Sprintf("turn %v", turn))
	}
	resp, err = director.FetchAudio(ctx, audioURL(unknownPerformanceID, 1))
	requireRefused(t, resp, err, http.StatusNotFound, "not_found", "an unknown performance, as a director")
	resp, err = director.FetchAudio(ctx, audioURL("not-an-id", 1))
	requireRefused(t, resp, err, http.StatusNotFound, "not_found", "a malformed performance ID, as a director")
	resp, err = anonymous.FetchAudio(ctx, audioURL(unknownPerformanceID, 1))
	requireRefused(t, resp, err, http.StatusUnauthorized, "not_logged_in", "an unknown performance, with no session")
}
