package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/tests/e2e/client"
	"github.com/conghan-tay/MonsterTheatre_01/tests/e2e/fixtures"
)

// The test stack's settings (tests/e2e/harness/stack.go).
const (
	voteWindow        = 2 * time.Second
	premiereCountdown = time.Second
)

const (
	turns = 8
	// lastRevision is the premiere: three updates a turn, then one.
	lastRevision = 3*turns + 1
)

// schedule is who speaks on each turn and to what story beat.
var schedule = []struct{ speaker, beat string }{
	{"A", "INTRO"}, {"B", "QUESTION"}, {"A", "REACT"}, {"B", "QUESTION"},
	{"A", "QUESTION"}, {"B", "REACT"}, {"A", "REACT"}, {"B", "EXPLAIN + PUNCHLINE"},
}

// expectedStep is the phase and turn of a revision in a run with no failures.
func expectedStep(revision int) (phase string, turn int) {
	if revision == lastRevision {
		return client.PhasePremiere, turns
	}
	phases := []string{client.PhaseGenerating, client.PhaseVoting, client.PhasePreparingLine}
	return phases[(revision-1)%3], (revision-1)/3 + 1
}

// defaultChoices is what the fake language model offers on an unscripted turn.
func defaultChoices(turn int) []string {
	return []string{
		fmt.Sprintf("Turn %d, first option.", turn),
		fmt.Sprintf("Turn %d, second option.", turn),
		fmt.Sprintf("Turn %d, third option.", turn),
	}
}

func start(t *testing.T, ctx context.Context, browser *client.Client, roomID string) (int, client.StartResponse) {
	t.Helper()
	resp, err := browser.Start(ctx, roomID)
	require.NoError(t, err)
	var body client.StartResponse
	if resp.Status == http.StatusOK || resp.Status == http.StatusCreated {
		require.NoError(t, resp.Decode(&body))
	}
	return resp.Status, body
}

// readThrough reads updates until the premiere and returns them in order,
// requiring each to be exactly one revision after the last.
func readThrough(t *testing.T, ctx context.Context, conn *client.Conn, after int) []client.Message {
	t.Helper()
	var updates []client.Message
	for revision := after + 1; revision <= lastRevision; revision++ {
		msg, err := conn.Next(ctx)
		require.NoError(t, err, "waiting for revision %d", revision)
		require.Equal(t, client.TypeUpdate, msg.Type, "message: %s", msg.Raw)
		require.Equal(t, revision, msg.Revision, "updates arrive in order with none missing; message: %s", msg.Raw)
		phase, turn := expectedStep(revision)
		require.Equal(t, phase, msg.State.Phase, "revision %d; message: %s", revision, msg.Raw)
		require.Equal(t, turn, msg.State.Turn, "revision %d; message: %s", revision, msg.Raw)
		updates = append(updates, msg)
	}
	return updates
}

// H4: after Start, with no votes, connected browsers receive revisions 1 to
// 25 in order and the performance row ends completed with a full script.
func TestPerformanceRunsEightTurnsWithoutVotes(t *testing.T) {
	requireLocal(t, "the fake providers read their scripts from the worker's disk")
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	director, room, _ := newRoom(t, ctx)
	member := enteredMember(t, ctx, room.ShareLink)
	opening := []string{"Who ate the last biscuit?", "I did not eat it.", "What biscuit?"}
	closing := []string{"It was the cat.", "It was me all along.", "There never was a biscuit."}
	fixtures.ScriptedChoices(t, stack.ScriptDir, room.RoomID, map[int][]string{1: opening, 8: closing})
	offered := func(turn int) []string {
		switch turn {
		case 1:
			return opening
		case 8:
			return closing
		}
		return defaultChoices(turn)
	}

	directorConn, err := director.ConnectRoom(ctx, room.RoomID)
	require.NoError(t, err)
	defer directorConn.Close()
	requireWaitingSnapshot(t, ctx, directorConn)
	memberConn, err := member.ConnectRoom(ctx, room.RoomID)
	require.NoError(t, err)
	defer memberConn.Close()
	requireWaitingSnapshot(t, ctx, memberConn)

	status, started := start(t, ctx, director.Client, room.RoomID)
	require.Equal(t, http.StatusCreated, status)
	require.NotEmpty(t, started.PerformanceID)
	require.Equal(t, room.RoomID, started.RoomID)
	require.Equal(t, "running", started.Status)

	// The audience member's updates are collected in the background while
	// the director's are checked one by one as they arrive.
	memberUpdates := make(chan []client.Message, 1)
	go func() {
		var got []client.Message
		for len(got) < lastRevision {
			msg, err := memberConn.Next(ctx)
			if err != nil {
				break
			}
			got = append(got, msg)
		}
		memberUpdates <- got
	}()

	var updates []client.Message
	var lines []client.Line
	for revision := 1; revision <= lastRevision; revision++ {
		msg, err := directorConn.Next(ctx)
		require.NoError(t, err, "waiting for revision %d", revision)
		received := time.Now()
		updates = append(updates, msg)
		state := msg.State
		require.Equal(t, client.TypeUpdate, msg.Type, "message: %s", msg.Raw)
		require.Equal(t, revision, msg.Revision, "message: %s", msg.Raw)
		phase, turn := expectedStep(revision)
		require.Equal(t, phase, state.Phase, "revision %d; message: %s", revision, msg.Raw)
		require.Equal(t, turn, state.Turn, "revision %d; message: %s", revision, msg.Raw)
		require.Equal(t, started.PerformanceID, state.PerformanceID)

		if phase == client.PhasePremiere {
			require.NotNil(t, state.Premiere, "message: %s", msg.Raw)
			require.WithinDuration(t, received.Add(premiereCountdown), state.Premiere.StartsAt, 900*time.Millisecond,
				"playback starts one countdown ahead")
			require.Len(t, state.Premiere.Clips, turns)
			for i, clip := range state.Premiere.Clips {
				require.Equal(t, client.Clip{
					Turn:    i + 1,
					Speaker: schedule[i].speaker,
					URL:     fmt.Sprintf("/api/v1/performances/%s/turns/%d/audio", started.PerformanceID, i+1),
				}, clip, "each clip says where its recording is fetched from")
			}
			require.Equal(t, lines, state.Lines, "the premiere carries all eight winning lines")
			continue
		}

		require.Equal(t, schedule[turn-1].speaker, state.Speaker, "turn %d", turn)
		require.Equal(t, schedule[turn-1].beat, state.Beat, "turn %d", turn)
		var texts []string
		for i, choice := range state.Choices {
			require.Equal(t, fmt.Sprintf("c%d", i+1), choice.ID)
			texts = append(texts, choice.Text)
		}
		switch phase {
		case client.PhaseGenerating:
			require.Empty(t, state.Choices, "message: %s", msg.Raw)
			require.Nil(t, state.Winner, "message: %s", msg.Raw)
			require.Equal(t, lines, state.Lines, "the lines chosen so far")
		case client.PhaseVoting:
			require.Equal(t, offered(turn), texts, "turn %d", turn)
			require.Nil(t, state.Winner, "message: %s", msg.Raw)
			require.WithinDuration(t, received.Add(voteWindow), state.Deadline, 900*time.Millisecond,
				"the deadline is one vote window ahead")
		case client.PhasePreparingLine:
			require.Equal(t, offered(turn), texts, "turn %d", turn)
			require.NotNil(t, state.Winner, "message: %s", msg.Raw)
			require.Contains(t, []string{"c1", "c2", "c3"}, state.Winner.ChoiceID)
			require.Contains(t, offered(turn), state.Winner.Text)
			require.Equal(t, map[string]int{"c1": 0, "c2": 0, "c3": 0}, state.Winner.Counts)
			require.Equal(t, "no_votes", state.Winner.DecidedBy)
			voting := updates[revision-2].State
			require.False(t, received.Before(voting.Deadline.Add(-200*time.Millisecond)),
				"the winner is announced only once the window has closed")
			lines = append(lines, client.Line{Turn: turn, Speaker: state.Speaker, Text: state.Winner.Text})
			require.Equal(t, lines, state.Lines, "the winning line joins the lines so far")
		}
	}

	// Every browser in the room got exactly the same messages.
	fromMember := <-memberUpdates
	require.Len(t, fromMember, lastRevision)
	for i := range updates {
		require.JSONEq(t, string(updates[i].Raw), string(fromMember[i].Raw), "revision %d", i+1)
	}

	// The row is completed. The premiere is published just after the row is
	// saved, so by now it must be.
	status, again := start(t, ctx, director.Client, room.RoomID)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, started.PerformanceID, again.PerformanceID)
	require.Equal(t, "completed", again.Status)

	row, found, err := stack.PerformanceRow(ctx, room.RoomID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, started.PerformanceID, row.ID)
	require.Equal(t, "completed", row.Status)

	var script struct {
		Turns []struct {
			Turn       int             `json:"turn"`
			Speaker    string          `json:"speaker"`
			Beat       string          `json:"beat"`
			Choices    []client.Choice `json:"choices"`
			Counts     map[string]int  `json:"counts"`
			Winner     client.Choice   `json:"winner"`
			DecidedBy  string          `json:"decidedBy"`
			TaggedText string          `json:"taggedText"`
			Model      string          `json:"model"`
		} `json:"turns"`
	}
	require.NoError(t, json.Unmarshal(row.Script, &script), "script: %s", row.Script)
	require.Len(t, script.Turns, turns)
	for i, turn := range script.Turns {
		require.Equal(t, i+1, turn.Turn)
		require.Equal(t, schedule[i].speaker, turn.Speaker)
		require.Equal(t, schedule[i].beat, turn.Beat)
		require.Len(t, turn.Choices, 3)
		require.Equal(t, lines[i].Text, turn.Winner.Text, "the saved winner is the one browsers were shown")
		require.Equal(t, map[string]int{"c1": 0, "c2": 0, "c3": 0}, turn.Counts)
		require.Equal(t, "no_votes", turn.DecidedBy)
		require.Equal(t, "[excited] "+lines[i].Text, turn.TaggedText, "the fake tagger's output")
		require.Equal(t, "fake", turn.Model)
	}

	// Step 10: once the run has closed, a vote is refused. The workflow
	// stays open for a moment after its last update, and a vote sent in that
	// moment is accepted and ignored, so this waits for the refusal. Each
	// try uses a new connection, because a connection passes on only one
	// vote per turn and would answer a second try itself.
	var reply client.Message
	require.Eventually(t, func() bool {
		conn, err := director.ConnectRoom(ctx, room.RoomID)
		if err != nil {
			return false
		}
		defer conn.Close()
		if _, err := conn.Snapshot(ctx); err != nil {
			return false
		}
		reply, err = conn.Vote(ctx, turns, "c1")
		return err == nil && reply.Type == client.TypeError
	}, 5*time.Second, 100*time.Millisecond, "a vote after the run has closed is refused; last reply: %s", reply.Raw)
	require.Equal(t, "vote_not_delivered", reply.Code, "reply: %s", reply.Raw)

	var audioKeys []string
	require.NoError(t, json.Unmarshal(row.AudioKeys, &audioKeys), "audio keys: %s", row.AudioKeys)
	require.Len(t, audioKeys, turns)
	for i, key := range audioKeys {
		require.Equal(t, fmt.Sprintf("performances/%s/turn-%d.mp3", started.PerformanceID, i+1), key)
		audio, err := stack.StoredObject(key)
		require.NoError(t, err, "turn %d's recording is in the object store", i+1)
		require.Equal(t, []byte{0xFF, 0xFB}, audio[:2], "and is an MP3")
	}
}

// Step 9: a browser that connects mid-run receives a snapshot and then only
// higher revisions.
func TestConnectingMidRunGetsASnapshotThenOnlyNewerUpdates(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	director, room, _ := newRoom(t, ctx)
	member := enteredMember(t, ctx, room.ShareLink)

	first, err := director.ConnectRoom(ctx, room.RoomID)
	require.NoError(t, err)
	defer first.Close()
	requireWaitingSnapshot(t, ctx, first)

	status, started := start(t, ctx, director.Client, room.RoomID)
	require.Equal(t, http.StatusCreated, status)
	seen, err := first.WaitForPhase(ctx, client.PhaseVoting, 2)
	require.NoError(t, err)

	late, err := member.ConnectRoom(ctx, room.RoomID)
	require.NoError(t, err)
	defer late.Close()
	snapshot, err := late.Snapshot(ctx)
	require.NoError(t, err)
	require.GreaterOrEqual(t, snapshot.Revision, seen.Revision, "the snapshot is at least as new as what others have seen")
	phase, turn := expectedStep(snapshot.Revision)
	require.Equal(t, phase, snapshot.State.Phase, "snapshot: %s", snapshot.Raw)
	require.Equal(t, turn, snapshot.State.Turn, "snapshot: %s", snapshot.Raw)
	require.Equal(t, started.PerformanceID, snapshot.State.PerformanceID)
	require.Len(t, snapshot.State.Lines, 1, "it carries the line already chosen; snapshot: %s", snapshot.Raw)
	if phase == client.PhaseVoting {
		require.Len(t, snapshot.State.Choices, 3, "snapshot: %s", snapshot.Raw)
		require.False(t, snapshot.State.Deadline.IsZero())
	}

	// Then every later update, each exactly one revision on, with none
	// repeated from before the snapshot.
	readThrough(t, ctx, late, snapshot.Revision)
	// The browser that was there all along misses nothing either.
	readThrough(t, ctx, first, seen.Revision)
}

// S6: a second Start returns the same performance and does not create another.
func TestSecondStartReturnsTheSamePerformance(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	director, room, _ := newRoom(t, ctx)
	conn, err := director.ConnectRoom(ctx, room.RoomID)
	require.NoError(t, err)
	defer conn.Close()
	requireWaitingSnapshot(t, ctx, conn)

	// Five presses at once, as an impatient director with a slow connection.
	const presses = 5
	statuses := make([]int, presses)
	bodies := make([]client.StartResponse, presses)
	var wg sync.WaitGroup
	for i := range presses {
		wg.Go(func() { statuses[i], bodies[i] = start(t, ctx, director.Client, room.RoomID) })
	}
	wg.Wait()

	created := 0
	for i := range presses {
		require.Contains(t, []int{http.StatusCreated, http.StatusOK}, statuses[i], "press %d", i)
		if statuses[i] == http.StatusCreated {
			created++
		}
		require.Equal(t, bodies[0].PerformanceID, bodies[i].PerformanceID, "every press names the same performance")
		require.Equal(t, "running", bodies[i].Status)
	}
	require.Equal(t, 1, created, "exactly one press created it")

	status, later := start(t, ctx, director.Client, room.RoomID)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, bodies[0].PerformanceID, later.PerformanceID)

	count, err := stack.PerformanceCount(ctx, room.RoomID)
	require.NoError(t, err)
	require.Equal(t, 1, count, "one row")

	// One workflow: revisions run 1 to 25 once, with nothing repeated.
	readThrough(t, ctx, conn, 0)
}

// S3 (start part): only the room's own director can start it.
func TestOnlyTheRoomsDirectorCanStart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	director, room, _ := newRoom(t, ctx)
	member := enteredMember(t, ctx, room.ShareLink)
	otherDirector := loggedInDirector(t, ctx)

	cases := []struct {
		name    string
		browser *client.Client
		roomID  string
		status  int
		code    string
	}{
		{"another director", otherDirector.Client, room.RoomID, http.StatusForbidden, "not_your_room"},
		{"an audience member", member.Client, room.RoomID, http.StatusUnauthorized, "not_logged_in"},
		{"nobody", client.New(stack.BaseURL), room.RoomID, http.StatusUnauthorized, "not_logged_in"},
		{"a room that does not exist", director.Client, noSuchRoomID, http.StatusNotFound, "not_found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := tc.browser.Start(ctx, tc.roomID)
			require.NoError(t, err)
			require.Equal(t, tc.status, resp.Status, "body: %s", resp.Body)
			require.Equal(t, tc.code, errorCode(t, resp))
		})
	}

	// Nothing was started.
	_, found, err := stack.PerformanceRow(ctx, room.RoomID)
	require.NoError(t, err)
	require.False(t, found, "no performance row")
	conn, err := director.ConnectRoom(ctx, room.RoomID)
	require.NoError(t, err)
	defer conn.Close()
	requireWaitingSnapshot(t, ctx, conn)
}
