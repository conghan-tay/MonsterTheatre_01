// Package performance holds what the server and the worker share about a
// performance: the state browsers are shown, the names the two processes use
// to reach the workflow, the performance row, and the Start endpoint.
//
// While a performance runs, its state lives only in the workflow
// (docs/decisions.md, D024). The types here are how that state crosses
// process boundaries: as a workflow query result, a Redis stream entry, and
// a WebSocket message.
package performance

import (
	"fmt"
	"time"
)

// Turns is how many voted turns a performance has.
const Turns = 8

// Phases. Waiting, Completed, and Failed appear only in snapshots the server
// builds itself; the others are published by the workflow.
const (
	PhaseWaiting       = "waiting"
	PhaseGenerating    = "generating"
	PhaseVoting        = "voting"
	PhasePreparingLine = "preparing_line"
	PhasePremiere      = "premiere"
	PhaseCompleted     = "completed"
	PhaseFailed        = "failed"
)

// How a round's winner was decided.
const (
	DecidedByMajority = "majority"
	DecidedByTie      = "tie"
	DecidedByNoVotes  = "no_votes"
)

// Message types on the WebSocket (docs/system-design.md, section 5). The
// first four are sent by the server; a browser sends vote.
const (
	TypeSnapshot     = "snapshot"
	TypeUpdate       = "update"
	TypeVoteReceived = "vote_received"
	TypeError        = "error"
	TypeVote         = "vote"
)

// State is everything a browser is shown about a performance. Snapshots and
// updates both carry all of it (docs/decisions.md, D036); a field is absent
// in phases that do not have it.
type State struct {
	Phase         string     `json:"phase"`
	Turn          int        `json:"turn"`
	PerformanceID string     `json:"performanceId,omitempty"`
	Speaker       string     `json:"speaker,omitempty"`
	Beat          string     `json:"beat,omitempty"`
	Choices       []Choice   `json:"choices,omitempty"`
	Deadline      *time.Time `json:"deadline,omitempty"`
	Winner        *Winner    `json:"winner,omitempty"`
	// YourVote is the choice the asking voter has made in the current turn.
	// Only a snapshot carries it: an update goes to every browser in the
	// room, so it can say nothing about one voter.
	YourVote string `json:"yourVote,omitempty"`
	// Lines are the winning lines so far, in turn order.
	Lines    []Line    `json:"lines,omitempty"`
	Premiere *Premiere `json:"premiere,omitempty"`
	// Failure is set in the failed phase.
	Failure *Failure `json:"failure,omitempty"`
}

// Failure is why a performance ended early. Code is stable and is what the
// performance row records; Message is a sentence for the audience.
type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Failure codes.
const (
	// FailureChoices: the next lines could not be written, after retrying
	// for the whole retry window. There is no stand-in for lines the model
	// did not write (docs/decisions.md, D038).
	FailureChoices = "choices_failed"
	// FailureSpeech: a line could not be voiced.
	FailureSpeech = "speech_failed"
	// FailureInternal: anything else that stopped the run.
	FailureInternal = "internal_error"
)

// NewFailure returns the failure for a code, with its message.
func NewFailure(code string) *Failure {
	messages := map[string]string{
		FailureChoices: "The next lines could not be written.",
		FailureSpeech:  "The line could not be turned into speech.",
	}
	message, ok := messages[code]
	if !ok {
		code, message = FailureInternal, "Something went wrong and the performance could not continue."
	}
	return &Failure{Code: code, Message: message}
}

// Choice is one of a turn's three candidate lines.
type Choice struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// Winner is the line a round chose and how.
type Winner struct {
	ChoiceID  string         `json:"choiceId"`
	Text      string         `json:"text"`
	Counts    map[string]int `json:"counts"`
	DecidedBy string         `json:"decidedBy"`
}

// Line is a winning line. Speaker is "A" or "B".
type Line struct {
	Turn    int    `json:"turn"`
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
}

// Premiere says when playback starts and which clips to play, in order.
type Premiere struct {
	StartsAt time.Time `json:"startsAt"`
	Clips    []Clip    `json:"clips"`
}

// Clip is one turn's recording: who speaks in it and where to fetch it.
type Clip struct {
	Turn    int    `json:"turn"`
	Speaker string `json:"speaker"`
	// URL is a path on the server, as a room's portrait URLs are. The
	// browser plays it as given and never builds one itself
	// (docs/decisions.md, D037).
	URL string `json:"url"`
}

// AudioRoute is the route that serves a turn's recording, and AudioURL the
// path of one recording on it. They live together, in the package the
// server and the worker share, because the workflow publishes these URLs
// and the server answers them.
const AudioRoute = "GET /api/v1/performances/{performanceId}/turns/{turn}/audio"

// AudioURL is where a browser fetches a turn's recording.
func AudioURL(performanceID string, turn int) string {
	return fmt.Sprintf("/api/v1/performances/%s/turns/%d/audio", performanceID, turn)
}

// Vote is one vote as the server hands it to the workflow.
type Vote struct {
	// VoterID is an audience session ID or a director ID. The server takes
	// it from the connection, never from what the browser sent.
	VoterID  string
	Turn     int
	ChoiceID string
	// ReceivedAt is the server's clock when the vote arrived. Whether the
	// vote was in time is decided from this stamp (docs/decisions.md, D033).
	ReceivedAt time.Time
}

// Message is a snapshot or update as sent to a browser.
type Message struct {
	Type     string `json:"type"`
	Revision int    `json:"revision"`
	State    State  `json:"state"`
}

// Snapshot is the workflow's answer to the state query: its current state
// and the revision of the last update it published, or is about to.
type Snapshot struct {
	Revision int   `json:"revision"`
	State    State `json:"state"`
}

// Update is one published change, as the workflow hands it to the publish
// activity.
type Update struct {
	RoomID   string `json:"roomId"`
	Revision int    `json:"revision"`
	State    State  `json:"state"`
}

// Script is the final record of a performance, saved on its row.
type Script struct {
	Turns []ScriptTurn `json:"turns"`
}

// ScriptTurn is one turn of the final script.
type ScriptTurn struct {
	Turn      int            `json:"turn"`
	Speaker   string         `json:"speaker"`
	Beat      string         `json:"beat"`
	Choices   []Choice       `json:"choices"`
	Counts    map[string]int `json:"counts"`
	Winner    Choice         `json:"winner"`
	DecidedBy string         `json:"decidedBy"`
	// TaggedText is what was spoken: the winning line with audio tags
	// added, or the winning line itself if tagging failed.
	TaggedText string `json:"taggedText"`
	// TagsApplied is false when tagging failed and the plain line was spoken.
	TagsApplied bool `json:"tagsApplied"`
	// Model is the language model that wrote the choices and the tags.
	Model string `json:"model"`
}
