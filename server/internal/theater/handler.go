package theater

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/httpapi"
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

// Service serves the room WebSocket.
type Service struct {
	Rooms    RoomAuthorizer
	Audience SessionToucher
	Hub      *Hub
	Origins  OriginPolicy
	Logger   *slog.Logger
	// PingInterval and PongTimeout default to DefaultPingInterval and
	// DefaultPongTimeout when zero.
	PingInterval time.Duration
	PongTimeout  time.Duration
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
		send: make(chan []byte, sendBuffer),
		close: func(code websocket.StatusCode, reason string) {
			closeOnce.Do(func() { _ = ws.Close(code, reason) })
		},
	}

	// Registered before the snapshot is sent, so no update can fall between
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

	if err := write(ctx, ws, waitingSnapshot()); err != nil {
		return
	}

	// Reading is what notices the browser closing, and what lets pings be
	// answered. Messages are ignored for now: votes arrive in step 10 and
	// resync in step 13.
	go func() {
		defer cancel()
		for {
			if _, _, err := ws.Read(ctx); err != nil {
				return
			}
		}
	}()

	s.writeLoop(ctx, ws, c)
}

// writeLoop sends queued messages and pings until the connection ends. It is
// the only writer of data messages, so they go out in the order queued.
func (s *Service) writeLoop(ctx context.Context, ws *websocket.Conn, c *conn) {
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
		case msg := <-c.send:
			if err := write(ctx, ws, msg); err != nil {
				return
			}
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
