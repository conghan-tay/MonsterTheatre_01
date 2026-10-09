// Package theater is the live side of the server: the WebSocket each browser
// holds open, and the map of which connections belong to which room.
package theater

import (
	"sync"

	"github.com/coder/websocket"
)

// sendBuffer is how many messages may wait for one browser. A whole
// performance is 25 updates, so a browser this far behind is not reading.
const sendBuffer = 32

// outbound is an update waiting to be written to one browser.
type outbound struct {
	revision int
	data     []byte
}

// conn is one browser's connection as the hub sees it.
type conn struct {
	// send holds updates waiting to be written. Only the connection's own
	// writer loop receives from it.
	send chan outbound
	// close ends the connection with a close frame. It is safe to call from
	// any goroutine and more than once.
	close func(code websocket.StatusCode, reason string)
}

// Hub is the in-memory map of room to open connections. It is the only
// record of who is connected; nothing about connections is stored.
type Hub struct {
	mu     sync.Mutex
	rooms  map[string]map[*conn]struct{}
	closed bool
}

// NewHub returns an empty hub.
func NewHub() *Hub {
	return &Hub{rooms: make(map[string]map[*conn]struct{})}
}

// add registers a connection for a room. It returns false once the hub has
// been closed, when the caller must close the connection itself.
func (h *Hub) add(roomID string, c *conn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return false
	}
	if h.rooms[roomID] == nil {
		h.rooms[roomID] = make(map[*conn]struct{})
	}
	h.rooms[roomID][c] = struct{}{}
	return true
}

// remove forgets a connection. Removing one twice is harmless.
func (h *Hub) remove(roomID string, c *conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.rooms[roomID], c)
	if len(h.rooms[roomID]) == 0 {
		delete(h.rooms, roomID)
	}
}

// Count returns how many connections a room has.
func (h *Hub) Count(roomID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.rooms[roomID])
}

// Broadcast queues an update for every connection in the room and returns
// without waiting for any of them. The revision travels with it so that each
// connection can drop what its snapshot already covered. A connection whose queue is full is closed rather
// than waited for, so one stalled browser cannot hold up a room; when it
// reconnects it gets a fresh snapshot.
func (h *Hub) Broadcast(roomID string, revision int, msg []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.rooms[roomID] {
		select {
		case c.send <- outbound{revision: revision, data: msg}:
		default:
			delete(h.rooms[roomID], c)
			go c.close(websocket.StatusPolicyViolation, "too far behind")
		}
	}
	if len(h.rooms[roomID]) == 0 {
		delete(h.rooms, roomID)
	}
}

// CloseAll closes every connection, telling each browser the server is going
// away, and refuses new ones. It returns when the close frames have been
// sent. http.Server.Shutdown does not do this for upgraded connections.
func (h *Hub) CloseAll() {
	h.mu.Lock()
	h.closed = true
	var all []*conn
	for _, conns := range h.rooms {
		for c := range conns {
			all = append(all, c)
		}
	}
	h.rooms = make(map[string]map[*conn]struct{})
	h.mu.Unlock()

	var wg sync.WaitGroup
	for _, c := range all {
		wg.Go(func() { c.close(websocket.StatusGoingAway, "server shutting down") })
	}
	wg.Wait()
}
