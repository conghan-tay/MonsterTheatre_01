package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// Message types sent by the server (docs/system-design.md, section 5).
const (
	TypeSnapshot     = "snapshot"
	TypeUpdate       = "update"
	TypeVoteReceived = "vote_received"
	TypeError        = "error"
)

// TypeVote is the message a browser sends to vote.
const TypeVote = "vote"

// Message is one WebSocket message from the server. Snapshots and updates
// carry a revision and the state they describe.
type Message struct {
	Type     string `json:"type"`
	Revision int    `json:"revision"`
	State    State  `json:"state"`
	// Turn and ChoiceID are set on vote_received: the vote the server passed on.
	Turn     int    `json:"turn"`
	ChoiceID string `json:"choiceId"`
	// Code and Text are set on error.
	Code string `json:"code"`
	Text string `json:"message"`
	// Raw is the whole message, for tests that need fields not modelled here.
	Raw json.RawMessage `json:"-"`
}

// Phases a snapshot or update can show (docs/system-design.md, section 4).
const (
	PhaseWaiting       = "waiting"
	PhaseGenerating    = "generating"
	PhaseVoting        = "voting"
	PhasePreparingLine = "preparing_line"
	PhasePremiere      = "premiere"
	PhaseCompleted     = "completed"
	PhaseFailed        = "failed"
)

// State is the performance state a snapshot or update carries. Every update
// carries all of it; fields are absent in phases that do not have them.
type State struct {
	Phase         string    `json:"phase"`
	Turn          int       `json:"turn"`
	PerformanceID string    `json:"performanceId"`
	Speaker       string    `json:"speaker"`
	Beat          string    `json:"beat"`
	Choices       []Choice  `json:"choices"`
	Deadline      time.Time `json:"deadline"`
	// YourVote is the choice this browser's voter has made in the current
	// turn. Only snapshots carry it.
	YourVote string    `json:"yourVote"`
	Winner   *Winner   `json:"winner"`
	Lines    []Line    `json:"lines"`
	Premiere *Premiere `json:"premiere"`
	// Failure is set in the failed phase: why the run could not continue.
	Failure *Failure `json:"failure"`
}

// Failure is why a performance ended early.
type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Choice is one of a turn's three candidate lines.
type Choice struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// Winner is the line a round chose.
type Winner struct {
	ChoiceID  string         `json:"choiceId"`
	Text      string         `json:"text"`
	Counts    map[string]int `json:"counts"`
	DecidedBy string         `json:"decidedBy"`
}

// Line is a winning line from an earlier turn.
type Line struct {
	Turn    int    `json:"turn"`
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
}

// Premiere says when playback starts and which clips to play.
type Premiere struct {
	StartsAt time.Time `json:"startsAt"`
	Clips    []Clip    `json:"clips"`
}

// Clip is one turn's recording: who speaks in it and where to fetch it.
type Clip struct {
	Turn    int    `json:"turn"`
	Speaker string `json:"speaker"`
	URL     string `json:"url"`
}

// Conn is one browser's WebSocket to the server.
type Conn struct {
	ws *websocket.Conn
	// pending holds messages read while waiting for something else, in the
	// order they arrived. Next returns these before reading more.
	pending []Message
}

// HandshakeError is a WebSocket handshake the server answered with an
// ordinary HTTP response instead of upgrading.
type HandshakeError struct {
	Status int
	// Code is the error code from the response body, if it had one.
	Code string
	err  error
}

func (e *HandshakeError) Error() string {
	return fmt.Sprintf("websocket handshake refused with status %d (code %q): %v", e.Status, e.Code, e.err)
}

func (e *HandshakeError) Unwrap() error { return e.err }

// Connect opens a WebSocket at path, sending this client's cookies and its
// own origin, as a browser on the same site would.
func (c *Client) Connect(ctx context.Context, path string) (*Conn, error) {
	return c.ConnectFrom(ctx, path, c.BaseURL)
}

// ConnectRoom opens the room's WebSocket.
func (c *Client) ConnectRoom(ctx context.Context, roomID string) (*Conn, error) {
	return c.Connect(ctx, RoomSocketPath(roomID))
}

// RoomSocketPath is the path of a room's WebSocket.
func RoomSocketPath(roomID string) string {
	return "/api/v1/rooms/" + roomID + "/ws"
}

// ConnectFrom opens a WebSocket at path as a page on the given origin would,
// still sending this client's cookies. An empty origin sends no Origin
// header, which no browser does. A refusal is returned as a *HandshakeError.
func (c *Client) ConnectFrom(ctx context.Context, path, origin string) (*Conn, error) {
	wsURL := "ws" + strings.TrimPrefix(c.BaseURL, "http") + path
	header := http.Header{}
	if origin != "" {
		header.Set("Origin", origin)
	}
	ws, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPClient: c.HTTP,
		HTTPHeader: header,
	})
	if err != nil {
		if resp != nil {
			refusal := &HandshakeError{Status: resp.StatusCode, err: err}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if resp.Body != nil {
				// Dial keeps the start of a refused handshake's body readable.
				if data, readErr := io.ReadAll(resp.Body); readErr == nil && json.Unmarshal(data, &body) == nil {
					refusal.Code = body.Error.Code
				}
			}
			return nil, refusal
		}
		return nil, err
	}
	return &Conn{ws: ws}, nil
}

// Next reads the next message. If ctx ends first the connection is closed,
// so a timeout here ends the test's use of this connection.
func (c *Conn) Next(ctx context.Context) (Message, error) {
	if len(c.pending) > 0 {
		msg := c.pending[0]
		c.pending = c.pending[1:]
		return msg, nil
	}
	return c.read(ctx)
}

func (c *Conn) read(ctx context.Context) (Message, error) {
	var msg Message
	_, data, err := c.ws.Read(ctx)
	if err != nil {
		return msg, err
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		return msg, fmt.Errorf("decode message %q: %w", data, err)
	}
	msg.Raw = data
	return msg, nil
}

// Snapshot reads the next message and requires it to be a snapshot, which
// the server always sends first.
func (c *Conn) Snapshot(ctx context.Context) (Message, error) {
	msg, err := c.Next(ctx)
	if err != nil {
		return msg, err
	}
	if msg.Type != TypeSnapshot {
		return msg, fmt.Errorf("expected a snapshot, got %q: %s", msg.Type, msg.Raw)
	}
	return msg, nil
}

// NextUpdate reads messages until the next update and returns it.
func (c *Conn) NextUpdate(ctx context.Context) (Message, error) {
	for {
		msg, err := c.Next(ctx)
		if err != nil {
			return msg, err
		}
		if msg.Type == TypeUpdate {
			return msg, nil
		}
	}
}

// WaitForPhase reads messages until a snapshot or update shows the given
// phase and turn, and returns that message.
func (c *Conn) WaitForPhase(ctx context.Context, phase string, turn int) (Message, error) {
	for {
		msg, err := c.Next(ctx)
		if err != nil {
			return msg, fmt.Errorf("waiting for phase %q turn %d: %w", phase, turn, err)
		}
		if (msg.Type == TypeSnapshot || msg.Type == TypeUpdate) && msg.State.Phase == phase && msg.State.Turn == turn {
			return msg, nil
		}
	}
}

// Vote is a click on one of the three choices. It returns the server's
// reply.
func (c *Conn) Vote(ctx context.Context, turn int, choiceID string) (Message, error) {
	err := c.Send(ctx, map[string]any{"type": TypeVote, "turn": turn, "choiceId": choiceID})
	if err != nil {
		return Message{}, err
	}
	return c.Reply(ctx)
}

// Reply reads the server's answer to something this browser sent:
// vote_received, or error. Snapshots and updates that arrive first are
// kept, in order, for the next call to Next.
func (c *Conn) Reply(ctx context.Context) (Message, error) {
	for {
		msg, err := c.read(ctx)
		if err != nil {
			return msg, fmt.Errorf("waiting for a reply: %w", err)
		}
		if msg.Type == TypeVoteReceived || msg.Type == TypeError {
			return msg, nil
		}
		c.pending = append(c.pending, msg)
	}
}

// Send writes v as a JSON message.
func (c *Conn) Send(ctx context.Context, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.ws.Write(ctx, websocket.MessageText, data)
}

// Close closes the connection normally.
func (c *Conn) Close() error {
	return c.ws.Close(websocket.StatusNormalClosure, "")
}
