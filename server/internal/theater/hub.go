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

// outbound is a message waiting to be written to one browser: an update, or
// a snapshot the whole room is being sent.
type outbound struct {
	revision int
	data     []byte
	// always marks a message that is sent whatever the browser already
	// has. An update is dropped if the browser's snapshot covers it.
	always bool
}

// conn is one browser's connection as the hub sees it.
type conn struct {
	// id names this connection among all servers' connections, so that a
	// notice can say which one not to tell.
	id string
	// send holds updates waiting to be written. Only the connection's own
	// writer loop receives from it.
	send chan outbound
	// replies holds answers to this browser's own messages, such as
	// vote_received. Nil for a connection that cannot be answered.
	replies chan []byte
	// resync holds at most one request from this browser for a fresh
	// snapshot. The writer loop takes the snapshot, so that nothing is
	// written between taking it and sending it.
	resync chan struct{}
	// director is true for a connection opened by the room's director.
	// Some things are for the director alone: who is in the audience.
	director bool
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
	h.queue(roomID, nil, outbound{revision: revision, data: msg})
}

// announce queues a snapshot for every connection in the room but one: the
// connection whose request brought the news, which is answered directly. It
// is named by its ID, and may be on another server. No browser drops the
// snapshot as something it already has.
func (h *Hub) announce(roomID, exceptID string, msg []byte) {
	h.queue(roomID, func(c *conn) bool { return c.id != exceptID }, outbound{data: msg, always: true})
}

// hasDirector reports whether the room's director has a connection here.
func (h *Hub) hasDirector(roomID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.rooms[roomID] {
		if c.director {
			return true
		}
	}
	return false
}

// tellDirectors queues a message for the room's director, on however many
// connections they have open, and for nobody else.
func (h *Hub) tellDirectors(roomID string, msg []byte) {
	h.queue(roomID, func(c *conn) bool { return c.director }, outbound{data: msg, always: true})
}

// tell queues a message for one connection, if it is still in the room.
func (h *Hub) tell(roomID string, only *conn, msg []byte) {
	h.queue(roomID, func(c *conn) bool { return c == only }, outbound{data: msg, always: true})
}

// queue hands a message to every connection in the room that wanted picks,
// or to all of them if wanted is nil.
func (h *Hub) queue(roomID string, wanted func(*conn) bool, out outbound) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.rooms[roomID] {
		if wanted != nil && !wanted(c) {
			continue
		}
		select {
		case c.send <- out:
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
