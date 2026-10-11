package theater

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/httpapi"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/performance"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/rooms"
)

const (
	// DefaultPingInterval is how often the server pings each browser. It is
	// well under a load balancer's usual 60-second idle timeout, so a quiet
	// connection is not cut.
	DefaultPingInterval = 25 * time.Second
	// DefaultPongTimeout is how long a browser has to answer a ping before
	// its connection is dropped.
	DefaultPongTimeout = 10 * time.Second

	writeTimeout = 10 * time.Second
	// queryTimeout is how long a connecting browser waits for the workflow
	// to describe itself. With no worker running, nothing answers.
	queryTimeout = 5 * time.Second
	// maxMessageBytes bounds a message from a browser. The largest one a
	// browser has reason to send is a vote.
	maxMessageBytes = 4 << 10
)

// RoomAuthorizer admits the people allowed to watch a room, writing the
// refusal itself. *rooms.Service implements it.
type RoomAuthorizer interface {
	Authorize(w http.ResponseWriter, r *http.Request) (rooms.Viewer, bool)
}

// SessionToucher records that an audience session's browser connected.
// *audience.Service implements it.
type SessionToucher interface {
	Seen(ctx context.Context, sessionID string) error
}

// PerformanceReader finds a room's performance, or returns
// performance.ErrNotFound. *performance.PGStore implements it.
type PerformanceReader interface {
	ByRoom(ctx context.Context, roomID string) (performance.Performance, error)
}

// StateQuerier asks a running performance's workflow for its state.
// *performance.Workflows implements it.
type StateQuerier interface {
	State(ctx context.Context, performanceID, voterID string) (performance.Snapshot, error)
}

// Failer marks a performance failed, if it is still running.
// *performance.PGStore implements it.
type Failer interface {
	Fail(ctx context.Context, id, reason string, now time.Time) error
}

// VoteSignaller hands a vote to a performance's workflow.
// *performance.Workflows implements it.
type VoteSignaller interface {
	Vote(ctx context.Context, performanceID string, vote performance.Vote) error
}

// Service serves the room WebSocket.
type Service struct {
	Rooms        RoomAuthorizer
	Audience     SessionToucher
	Performances PerformanceReader
	Workflows    StateQuerier
	// Failures is where a run found dead is marked failed.
	Failures Failer
	Votes    VoteSignaller
	Hub      *Hub
	Origins  OriginPolicy
	Logger   *slog.Logger
	// PingInterval and PongTimeout default to DefaultPingInterval and
	// DefaultPongTimeout when zero.
	PingInterval time.Duration
	PongTimeout  time.Duration
	// Now is the clock votes are stamped with.
	Now func() time.Time
}

// ServeWS handles GET /api/v1/rooms/{roomId}/ws.
//
// Refusals are ordinary JSON error responses, sent before the upgrade: 403
// for a page on another origin, then the same 401, 403, and 404 as reading
// the room. Once upgraded, the first message is always a snapshot.
func (s *Service) ServeWS(w http.ResponseWriter, r *http.Request) {
	if !s.Origins.Allowed(r) {
		httpapi.WriteError(w, http.StatusForbidden, "origin_not_allowed", "This page may not connect to the theatre.")
		return
	}
	viewer, ok := s.Rooms.Authorize(w, r)
	if !ok {
		return
	}
	roomID := viewer.Room.ID
	log := s.Logger.With("room_id", roomID, "viewer_id", viewer.ID, "director", viewer.AsDirector)

	if !viewer.AsDirector {
		// Only for inspection, so a failure does not turn the browser away.
		if err := s.Audience.Seen(r.Context(), viewer.ID); err != nil {
			log.Warn("theater: record session seen", "error", err)
		}
	}

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Allowed has already decided; this repeats the rule inside the
		// library so a mistake in one is caught by the other.
		OriginPatterns: []string{s.Origins.public},
	})
	if err != nil {
		return // Accept has written the response
	}
	defer ws.CloseNow()
	ws.SetReadLimit(maxMessageBytes)

	var closeOnce sync.Once
	c := &conn{
		send:    make(chan outbound, sendBuffer),
		replies: make(chan []byte),
		resync:  make(chan struct{}, 1),
		close: func(code websocket.StatusCode, reason string) {
			closeOnce.Do(func() { _ = ws.Close(code, reason) })
		},
	}

	// Registered before the snapshot is taken, so no update can fall between
	// the two: anything broadcast meanwhile waits in the queue behind it.
	if !s.Hub.add(roomID, c) {
		c.close(websocket.StatusGoingAway, "server shutting down")
		return
	}
	defer s.Hub.remove(roomID, c)
	log.Info("websocket connected")
	defer log.Info("websocket disconnected")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	v := &viewing{conn: c, roomID: roomID, voterID: viewer.ID, log: log}
	revision, ok := s.sendSnapshot(ctx, ws, v)
	if !ok {
		return
	}

	// Reading is what notices the browser closing, and what lets pings be
	// answered. Each message is handled before the next is read, so one
	// browser's votes reach the workflow in the order it sent them.
	go func() {
		defer cancel()
		in := &inbox{service: s, roomID: roomID, voterID: viewer.ID, log: log, resync: c.resync}
		for {
			_, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			reply := in.handle(ctx, data)
			if reply == nil {
				continue
			}
			// Handed to the write loop, which does all the writing.
			select {
			case c.replies <- reply:
			case <-ctx.Done():
				return
			}
		}
	}()

	s.writeLoop(ctx, ws, v, revision)
}

// viewing is one connection and who is on it.
type viewing struct {
	conn   *conn
	roomID string
	// voterID is who is watching, so a snapshot can include their own vote.
	voterID string
	log     *slog.Logger
}

// sendSnapshot takes a snapshot and writes it, and returns its revision. It
// is used for the first message and for the answer to a resync. If there is
// no snapshot to send, because the database could not be read, it closes
// the connection and returns false; the browser reconnects.
func (s *Service) sendSnapshot(ctx context.Context, ws *websocket.Conn, v *viewing) (int, bool) {
	revision, state, err := s.snapshot(ctx, v)
	if err != nil {
		if ctx.Err() == nil {
			v.log.Warn("theater: no snapshot available", "error", err)
		}
		v.conn.close(websocket.StatusTryAgainLater, "performance state unavailable")
		return 0, false
	}
	if err := write(ctx, ws, snapshotMessage(revision, state)); err != nil {
		return 0, false
	}
	return revision, true
}

// snapshot returns the room's current state and its revision. Where it comes
// from depends on the performance row (docs/system-design.md, section 6):
// no row, the room is waiting; running, the workflow is asked; finished, the
// row says how, and no worker is needed.
//
// If the workflow cannot be asked, the answer is still a snapshot: one that
// says the performance is recovering. The only error is a database that
// could not be read.
func (s *Service) snapshot(ctx context.Context, v *viewing) (int, performance.State, error) {
	p, err := s.Performances.ByRoom(ctx, v.roomID)
	if errors.Is(err, performance.ErrNotFound) {
		return 0, performance.State{Phase: performance.PhaseWaiting}, nil
	}
	if err != nil {
		return 0, performance.State{}, err
	}
	if p.Status != performance.StatusRunning {
		revision, state := finished(p)
		return revision, state, nil
	}

	queryCtx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	snapshot, err := s.Workflows.State(queryCtx, p.ID, v.voterID)
	switch {
	case err == nil:
		return snapshot.Revision, snapshot.State, nil
	case errors.Is(err, performance.ErrWorkflowClosed):
		return s.dead(ctx, v, p)
	default:
		// No worker is up to answer, most likely. A workflow that was never
		// started also ends here, and is not started from here: that is
		// the director's to do, with Start (docs/decisions.md, D043).
		if ctx.Err() == nil {
			v.log.Warn("theater: workflow did not answer; the performance is recovering", "performance_id", p.ID, "error", err)
		}
		return 0, performance.RecoveringState(p), nil
	}
}

// finished is the snapshot of a performance whose row says it has ended.
func finished(p performance.Performance) (int, performance.State) {
	if p.Status == performance.StatusCompleted {
		// The premiere as it was published. The row is marked a moment
		// before that update goes out; a browser that connects in between
		// is sent this, and the update is then dropped as nothing new.
		return performance.LastRevision, performance.PremiereState(p)
	}
	// The row does not record the revision a run failed at. Zero lets a
	// failed update still on its way through, which says the same.
	return 0, performance.FailedState(p)
}

// dead deals with a performance whose row says running and whose workflow
// has ended: a run stopped by its timeout, or from outside, which could not
// mark its own row. The row is marked failed and everyone in the room is
// told, since they would otherwise wait on a round that will never close
// (docs/decisions.md, D042).
//
// Marking changes only a row that is still running, and the row is read
// again afterwards. So a run that finished properly in the moment between
// the first read and the query is answered as it finished, and nothing is
// announced.
func (s *Service) dead(ctx context.Context, v *viewing, p performance.Performance) (int, performance.State, error) {
	if err := s.Failures.Fail(ctx, p.ID, performance.FailureInternal, s.Now()); err != nil {
		return 0, performance.State{}, err
	}
	p, err := s.Performances.ByRoom(ctx, v.roomID)
	if err != nil {
		return 0, performance.State{}, err
	}
	if p.Status == performance.StatusRunning {
		return 0, performance.RecoveringState(p), nil // cannot happen: Fail has just marked it
	}
	revision, state := finished(p)
	if p.Status == performance.StatusFailed {
		v.log.Warn("theater: the workflow ended without marking its performance; marked failed", "performance_id", p.ID)
		s.Hub.announce(v.roomID, v.conn, snapshotMessage(revision, state))
	}
	return revision, state, nil
}

// writeLoop sends queued updates, replies, and pings until the connection ends. It is
// the only writer of data messages, so they go out in the order queued.
//
// It drops any update at or below snapshotRevision: one that was queued
// while the snapshot was being fetched and that the snapshot already covers.
//
// A resync is answered here and not by the reader, for the same reason the
// first snapshot is taken after the connection joins the room: while this
// loop is taking the snapshot it writes nothing, so every update published
// meanwhile is queued behind it and then sent or dropped by its revision.
func (s *Service) writeLoop(ctx context.Context, ws *websocket.Conn, v *viewing, snapshotRevision int) {
	c := v.conn
	interval, timeout := s.PingInterval, s.PongTimeout
	if interval <= 0 {
		interval = DefaultPingInterval
	}
	if timeout <= 0 {
		timeout = DefaultPongTimeout
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case update := <-c.send:
			if !update.always && update.revision <= snapshotRevision {
				continue
			}
			if err := write(ctx, ws, update.data); err != nil {
				return
			}
		case reply := <-c.replies:
			if err := write(ctx, ws, reply); err != nil {
				return
			}
		case <-c.resync:
			revision, ok := s.sendSnapshot(ctx, ws, v)
			if !ok {
				return
			}
			snapshotRevision = revision
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, timeout)
			err := ws.Ping(pingCtx)
			cancel()
			if err != nil {
				return // no pong: the browser is gone
			}
		}
	}
}

func write(ctx context.Context, ws *websocket.Conn, msg []byte) error {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return ws.Write(ctx, websocket.MessageText, msg)
}
