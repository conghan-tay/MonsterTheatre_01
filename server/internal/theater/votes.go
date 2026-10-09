package theater

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/performance"
)

const (
	// signalTimeout is how long handing a vote to Temporal may take.
	signalTimeout = 5 * time.Second
	// maxChoiceIDLength bounds what a browser can make the workflow store.
	// Real choice IDs are "c1" to "c3".
	maxChoiceIDLength = 16
)

// Error codes sent to a browser in an error message.
const (
	codeInvalidMessage   = "invalid_message"
	codeVoteNotDelivered = "vote_not_delivered"
)

// inbox handles the messages one browser sends. It belongs to the
// connection's read loop and is used by that goroutine only.
type inbox struct {
	service *Service
	roomID  string
	// voterID is who this connection is: a director ID or an audience
	// session ID, established at the handshake.
	voterID string
	log     *slog.Logger

	// passed is what this connection has already handed to the workflow,
	// by turn.
	passed map[int]string
}

type incoming struct {
	Type     string `json:"type"`
	Turn     int    `json:"turn"`
	ChoiceID string `json:"choiceId"`
}

// handle deals with one message and returns the reply to send, if any.
// Message types this server does not know are ignored, so a newer page does
// not break against an older server.
func (in *inbox) handle(ctx context.Context, data []byte) []byte {
	// Stamped before anything else: this is the time the vote is judged by.
	receivedAt := in.service.Now()

	var msg incoming
	if err := json.Unmarshal(data, &msg); err != nil {
		return errorMessage(codeInvalidMessage, "That message could not be read.")
	}
	if msg.Type != performance.TypeVote {
		return nil
	}
	return in.vote(ctx, msg, receivedAt)
}

// vote hands a vote to the workflow and confirms that it did.
//
// The confirmation means "passed on", not "counted": only the workflow knows
// whether a vote was in time, for a real choice, and the voter's first, and
// it says so in the next snapshot. The voter is this connection's, whatever
// the message claims.
//
// A connection passes on one vote per turn. A repeat is confirmed with the
// vote already passed on and goes no further, so a stuck or hostile browser
// cannot fill the workflow's history with signals.
func (in *inbox) vote(ctx context.Context, msg incoming, receivedAt time.Time) []byte {
	if msg.Turn < 1 || msg.Turn > performance.Turns || msg.ChoiceID == "" || len(msg.ChoiceID) > maxChoiceIDLength {
		return errorMessage(codeInvalidMessage, "A vote needs a turn number and a choice.")
	}
	if already, ok := in.passed[msg.Turn]; ok {
		return voteReceived(msg.Turn, already)
	}

	// Read on every vote, not remembered: most browsers connect before
	// Start, and the row is what says for certain that the voting is over.
	// (The workflow lingers for a moment after its last update, and would
	// accept a signal it will never read.) One indexed read per vote, for at
	// most eleven voters, is cheap.
	p, err := in.service.Performances.ByRoom(ctx, in.roomID)
	switch {
	case errors.Is(err, performance.ErrNotFound):
		return errorMessage(codeVoteNotDelivered, "The performance has not started.")
	case err != nil:
		in.failed("find the performance", err)
		return errorMessage(codeVoteNotDelivered, "Your vote could not be delivered. Try again.")
	case p.Status != performance.StatusRunning:
		return errorMessage(codeVoteNotDelivered, "The voting is over.")
	}

	ctx, cancel := context.WithTimeout(ctx, signalTimeout)
	defer cancel()
	err = in.service.Votes.Vote(ctx, p.ID, performance.Vote{
		VoterID:    in.voterID,
		Turn:       msg.Turn,
		ChoiceID:   msg.ChoiceID,
		ReceivedAt: receivedAt,
	})
	if err != nil {
		in.failed("signal the vote", err)
		return errorMessage(codeVoteNotDelivered, "Your vote could not be delivered. Try again.")
	}

	if in.passed == nil {
		in.passed = make(map[int]string)
	}
	in.passed[msg.Turn] = msg.ChoiceID
	return voteReceived(msg.Turn, msg.ChoiceID)
}

func (in *inbox) failed(what string, err error) {
	if errors.Is(err, context.Canceled) {
		return // the browser went away
	}
	in.log.Warn("theater: could not "+what, "error", err)
}

func voteReceived(turn int, choiceID string) []byte {
	return encode(struct {
		Type     string `json:"type"`
		Turn     int    `json:"turn"`
		ChoiceID string `json:"choiceId"`
	}{performance.TypeVoteReceived, turn, choiceID})
}

func errorMessage(code, message string) []byte {
	return encode(struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}{performance.TypeError, code, message})
}

func encode(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err) // cannot happen: the values are plain strings and numbers
	}
	return data
}
