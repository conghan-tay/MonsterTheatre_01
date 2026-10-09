package e2e_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/tests/e2e/client"
)

// theatre is a room with its director and some audience members connected,
// each having read the waiting snapshot.
type theatre struct {
	roomID        string
	performanceID string
	director      *client.DirectorClient
	members       []*client.AudienceClient
	// conns[0] is the director's; conns[i] is members[i-1]'s.
	conns []*client.Conn
}

// openTheatre creates a room, connects the director and n audience members,
// and starts the performance.
func openTheatre(t *testing.T, ctx context.Context, n int) *theatre {
	t.Helper()
	director, room, _ := newRoom(t, ctx)
	th := &theatre{roomID: room.RoomID, director: director}

	browsers := []*client.Client{director.Client}
	for range n {
		member := enteredMember(t, ctx, room.ShareLink)
		th.members = append(th.members, member)
		browsers = append(browsers, member.Client)
	}
	for _, browser := range browsers {
		conn, err := browser.ConnectRoom(ctx, room.RoomID)
		require.NoError(t, err)
		t.Cleanup(func() { conn.Close() })
		requireWaitingSnapshot(t, ctx, conn)
		th.conns = append(th.conns, conn)
	}

	status, started := start(t, ctx, director.Client, room.RoomID)
	require.Equal(t, http.StatusCreated, status)
	th.performanceID = started.PerformanceID
	return th
}

// awaitAll waits for every connection to show the phase and turn, and
// returns each one's message.
func (th *theatre) awaitAll(t *testing.T, ctx context.Context, phase string, turn int) []client.Message {
	t.Helper()
	var msgs []client.Message
	for _, conn := range th.conns {
		msg, err := conn.WaitForPhase(ctx, phase, turn)
		require.NoError(t, err)
		msgs = append(msgs, msg)
	}
	return msgs
}

// vote casts a vote and requires the server to say it passed it on.
func vote(t *testing.T, ctx context.Context, conn *client.Conn, turn int, choiceID string) {
	t.Helper()
	reply, err := conn.Vote(ctx, turn, choiceID)
	require.NoError(t, err)
	require.Equal(t, client.TypeVoteReceived, reply.Type, "reply: %s", reply.Raw)
	require.Equal(t, turn, reply.Turn, "reply: %s", reply.Raw)
	require.Equal(t, choiceID, reply.ChoiceID, "reply: %s", reply.Raw)
}

func choiceText(t *testing.T, state client.State, choiceID string) string {
	t.Helper()
	for _, choice := range state.Choices {
		if choice.ID == choiceID {
			return choice.Text
		}
	}
	t.Fatalf("no choice %q in %+v", choiceID, state.Choices)
	return ""
}

// H5 (first half): majority wins, and the director's vote counts.
func TestMajorityWinsAndTheDirectorsVoteCounts(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	th := openTheatre(t, ctx, 2)
	th.awaitAll(t, ctx, client.PhaseVoting, 1)

	// The director and one audience member agree; the other does not.
	vote(t, ctx, th.conns[0], 1, "c1")
	vote(t, ctx, th.conns[1], 1, "c1")
	vote(t, ctx, th.conns[2], 1, "c3")

	results := th.awaitAll(t, ctx, client.PhasePreparingLine, 1)
	winner := results[0].State.Winner
	require.NotNil(t, winner, "message: %s", results[0].Raw)
	require.Equal(t, "c1", winner.ChoiceID)
	require.Equal(t, choiceText(t, results[0].State, "c1"), winner.Text)
	require.Equal(t, map[string]int{"c1": 2, "c2": 0, "c3": 1}, winner.Counts,
		"two votes for c1, one of them the director's")
	require.Equal(t, "majority", winner.DecidedBy)
	for i := range results {
		require.JSONEq(t, string(results[0].Raw), string(results[i].Raw), "everyone is told the same result")
		require.Empty(t, results[i].State.YourVote, "an update never says who voted for what")
	}
}

// H5 (second half): a tie is settled once.
func TestATieIsSettledOnce(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	th := openTheatre(t, ctx, 2)
	th.awaitAll(t, ctx, client.PhaseVoting, 1)

	vote(t, ctx, th.conns[0], 1, "c1")
	vote(t, ctx, th.conns[1], 1, "c2")

	results := th.awaitAll(t, ctx, client.PhasePreparingLine, 1)
	winner := results[0].State.Winner
	require.NotNil(t, winner, "message: %s", results[0].Raw)
	require.Equal(t, "tie", winner.DecidedBy)
	require.Equal(t, map[string]int{"c1": 1, "c2": 1, "c3": 0}, winner.Counts)
	require.Contains(t, []string{"c1", "c2"}, winner.ChoiceID, "the winner is one of the tied lines, never the third")
	for i := range results {
		require.JSONEq(t, string(results[0].Raw), string(results[i].Raw), "every browser is told the same winner")
	}

	// A browser arriving later is shown the same line: the tie is not
	// settled again for it.
	late := enteredMember(t, ctx, roomShareLink(t, ctx, th))
	conn, err := late.ConnectRoom(ctx, th.roomID)
	require.NoError(t, err)
	defer conn.Close()
	snapshot, err := conn.Snapshot(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, snapshot.State.Lines, "snapshot: %s", snapshot.Raw)
	require.Equal(t, client.Line{Turn: 1, Speaker: "A", Text: winner.Text}, snapshot.State.Lines[0])
}

// roomShareLink reads the room's share link as its director.
func roomShareLink(t *testing.T, ctx context.Context, th *theatre) string {
	t.Helper()
	resp, err := th.director.GetRoom(ctx, th.roomID)
	require.NoError(t, err)
	var room client.RoomView
	require.NoError(t, resp.Decode(&room))
	return room.ShareLink
}

// H6: a browser that reconnects mid-round gets a snapshot containing its own
// vote and then continues from the right revision.
func TestReconnectingMidRoundRestoresYourVote(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	th := openTheatre(t, ctx, 2)
	th.awaitAll(t, ctx, client.PhaseVoting, 1)
	voter, bystander := th.members[0], th.members[1]

	vote(t, ctx, th.conns[1], 1, "c2")
	require.NoError(t, th.conns[1].Close(), "the voter's connection drops")

	again, err := voter.ConnectRoom(ctx, th.roomID)
	require.NoError(t, err)
	defer again.Close()
	snapshot, err := again.Snapshot(ctx)
	require.NoError(t, err)
	require.Equal(t, client.PhaseVoting, snapshot.State.Phase, "still the same round; snapshot: %s", snapshot.Raw)
	require.Equal(t, 1, snapshot.State.Turn)
	require.Equal(t, "c2", snapshot.State.YourVote, "the vote is remembered; snapshot: %s", snapshot.Raw)
	require.Nil(t, snapshot.State.Winner, "and nothing about the count is shown early")

	// Someone who has not voted is not shown anyone else's vote.
	other, err := bystander.ConnectRoom(ctx, th.roomID)
	require.NoError(t, err)
	defer other.Close()
	otherSnapshot, err := other.Snapshot(ctx)
	require.NoError(t, err)
	require.Empty(t, otherSnapshot.State.YourVote, "snapshot: %s", otherSnapshot.Raw)

	// The reconnected browser carries on from its snapshot: each later
	// update once, in order.
	revision := snapshot.Revision
	for range 5 {
		msg, err := again.Next(ctx)
		require.NoError(t, err)
		require.Equal(t, client.TypeUpdate, msg.Type, "message: %s", msg.Raw)
		revision++
		require.Equal(t, revision, msg.Revision, "message: %s", msg.Raw)
		if msg.State.Phase == client.PhasePreparingLine && msg.State.Turn == 1 {
			require.Equal(t, map[string]int{"c1": 0, "c2": 1, "c3": 0}, msg.State.Winner.Counts,
				"the vote cast before the drop was counted")
		}
	}
}

// S7: a second vote from the same voter, a vote for an unknown choice, and a
// vote after the window closes are all ignored.
func TestVotesThatDoNotCount(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	th := openTheatre(t, ctx, 2)
	th.awaitAll(t, ctx, client.PhaseVoting, 1)
	director, firstTab, other := th.conns[0], th.conns[1], th.conns[2]

	// The one vote that counts.
	vote(t, ctx, firstTab, 1, "c1")

	// The same person votes again from a second tab. The server passes it
	// on; the workflow keeps the first.
	secondTab, err := th.members[0].ConnectRoom(ctx, th.roomID)
	require.NoError(t, err)
	defer secondTab.Close()
	snapshot, err := secondTab.Snapshot(ctx)
	require.NoError(t, err)
	require.Equal(t, "c1", snapshot.State.YourVote, "snapshot: %s", snapshot.Raw)
	vote(t, ctx, secondTab, 1, "c2")
	// And again from the first tab. That connection has already passed a
	// vote on for this turn, and the server says which.
	repeat, err := firstTab.Vote(ctx, 1, "c3")
	require.NoError(t, err)
	require.Equal(t, client.TypeVoteReceived, repeat.Type, "reply: %s", repeat.Raw)
	require.Equal(t, "c1", repeat.ChoiceID, "reply: %s", repeat.Raw)

	// A choice that does not exist, and a turn that is not the current one.
	vote(t, ctx, other, 1, "c9")
	vote(t, ctx, director, 5, "c3")

	// A message that is not a vote at all gets an error and changes nothing.
	require.NoError(t, other.Send(ctx, map[string]any{"type": client.TypeVote, "turn": "one", "choiceId": 7}))
	refused, err := other.Reply(ctx)
	require.NoError(t, err)
	require.Equal(t, client.TypeError, refused.Type, "reply: %s", refused.Raw)
	require.Equal(t, "invalid_message", refused.Code, "reply: %s", refused.Raw)

	results := th.awaitAll(t, ctx, client.PhasePreparingLine, 1)
	winner := results[0].State.Winner
	require.NotNil(t, winner)
	require.Equal(t, map[string]int{"c1": 1, "c2": 0, "c3": 0}, winner.Counts, "only the first vote counted")
	require.Equal(t, "c1", winner.ChoiceID)
	require.Equal(t, "majority", winner.DecidedBy)

	// The round has closed. A vote for it now is too late, and it must not
	// leak into the next round either.
	// (From the director's connection, which has not yet passed on a vote
	// for turn 1, so the server does forward this one.)
	vote(t, ctx, director, 1, "c2")
	next := th.awaitAll(t, ctx, client.PhasePreparingLine, 2)
	require.Equal(t, map[string]int{"c1": 0, "c2": 0, "c3": 0}, next[0].State.Winner.Counts)
	require.Equal(t, "no_votes", next[0].State.Winner.DecidedBy)
}

// Step 10: there is nothing to vote on before the director presses Start.
func TestVotingBeforeStartIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	director, room, _ := newRoom(t, ctx)
	conn, err := director.ConnectRoom(ctx, room.RoomID)
	require.NoError(t, err)
	defer conn.Close()
	requireWaitingSnapshot(t, ctx, conn)

	reply, err := conn.Vote(ctx, 1, "c1")
	require.NoError(t, err)
	require.Equal(t, client.TypeError, reply.Type, "reply: %s", reply.Raw)
	require.Equal(t, "vote_not_delivered", reply.Code, "reply: %s", reply.Raw)
	require.NotEmpty(t, reply.Text)
}

// Step 10 (D033): votes cast while the worker is down are judged by when the
// server received them, not by the order the returning worker sees them in.
//
// This test stops the shared worker, so it does not run in parallel.
func TestVotesCastWhileTheWorkerIsDownAreJudgedByReceiptTime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	t.Cleanup(func() {
		if !stack.WorkerRunning() {
			require.NoError(t, stack.StartWorker())
		}
	})

	th := openTheatre(t, ctx, 1)
	opened := th.awaitAll(t, ctx, client.PhaseVoting, 1)
	deadline := opened[0].State.Deadline

	require.NoError(t, stack.StopWorker())

	// In time: the worker is down, but Temporal accepts the vote.
	vote(t, ctx, th.conns[0], 1, "c1")

	// Too late: after the deadline, with the worker still down. When the
	// worker returns it is handed both votes and the fired timer together.
	time.Sleep(time.Until(deadline) + 500*time.Millisecond)
	vote(t, ctx, th.conns[1], 1, "c2")

	require.NoError(t, stack.StartWorker())

	results := th.awaitAll(t, ctx, client.PhasePreparingLine, 1)
	winner := results[0].State.Winner
	require.NotNil(t, winner)
	require.Equal(t, map[string]int{"c1": 1, "c2": 0, "c3": 0}, winner.Counts,
		"the vote received before the deadline counts; the one received after does not")
	require.Equal(t, "c1", winner.ChoiceID)
	require.Equal(t, "majority", winner.DecidedBy)
}
