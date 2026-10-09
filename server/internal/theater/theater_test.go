package theater

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/rooms"
)

func TestOriginPolicy(t *testing.T) {
	policy, err := NewOriginPolicy("https://theatre.example/")
	require.NoError(t, err)

	cases := []struct {
		name   string
		origin string
		host   string
		want   bool
	}{
		{"the web app, through a proxy that rewrote Host", "https://theatre.example", "10.0.0.5:8080", true},
		{"the web app, Host preserved", "https://theatre.example", "theatre.example", true},
		{"the web app, in another letter case", "HTTPS://Theatre.Example", "10.0.0.5:8080", true},
		{"the server's own address", "http://127.0.0.1:8080", "127.0.0.1:8080", true},
		{"another site", "https://evil.example", "theatre.example", false},
		{"the web app's host over plain http", "http://theatre.example", "10.0.0.5:8080", false},
		{"the web app's host on another port", "https://theatre.example:8443", "10.0.0.5:8080", false},
		{"the server's hostname on another port", "http://127.0.0.1:9999", "127.0.0.1:8080", false},
		{"a site whose name starts with the web app's", "https://theatre.example.evil.test", "10.0.0.5:8080", false},
		{"no Origin", "", "theatre.example", false},
		{"an opaque origin", "null", "theatre.example", false},
		{"not a web origin", "file://theatre.example", "theatre.example", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/v1/rooms/x/ws", nil)
			r.Host = tc.host
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			require.Equal(t, tc.want, policy.Allowed(r))
		})
	}
}

func TestOriginPolicyNeedsAWebURL(t *testing.T) {
	for _, bad := range []string{"", "theatre.example", "ftp://theatre.example", "http://"} {
		_, err := NewOriginPolicy(bad)
		require.Error(t, err, "%q", bad)
	}
}

// fakeConn is a connection with no socket behind it.
type fakeConn struct {
	*conn
	mu     sync.Mutex
	closed []websocket.StatusCode
}

func newFakeConn() *fakeConn {
	f := &fakeConn{}
	f.conn = &conn{
		send: make(chan []byte, sendBuffer),
		close: func(code websocket.StatusCode, _ string) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.closed = append(f.closed, code)
		},
	}
	return f
}

func (f *fakeConn) closeCodes() []websocket.StatusCode {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]websocket.StatusCode(nil), f.closed...)
}

func TestHubBroadcastsToOneRoomOnly(t *testing.T) {
	hub := NewHub()
	a1, a2, b := newFakeConn(), newFakeConn(), newFakeConn()
	require.True(t, hub.add("room-a", a1.conn))
	require.True(t, hub.add("room-a", a2.conn))
	require.True(t, hub.add("room-b", b.conn))
	require.Equal(t, 2, hub.Count("room-a"))

	hub.Broadcast("room-a", []byte("hello"))
	require.Equal(t, "hello", string(<-a1.send))
	require.Equal(t, "hello", string(<-a2.send))
	require.Empty(t, b.send, "another room hears nothing")

	hub.remove("room-a", a1.conn)
	hub.remove("room-a", a1.conn) // twice is harmless
	hub.Broadcast("room-a", []byte("again"))
	require.Empty(t, a1.send, "a removed connection hears nothing")
	require.Equal(t, "again", string(<-a2.send))

	hub.remove("room-a", a2.conn)
	require.Zero(t, hub.Count("room-a"))
	hub.Broadcast("room-a", []byte("nobody")) // an empty room is fine
}

func TestHubDropsAConnectionThatFallsBehind(t *testing.T) {
	hub := NewHub()
	stalled, healthy := newFakeConn(), newFakeConn()
	hub.add("room", stalled.conn)
	hub.add("room", healthy.conn)

	// Nobody reads the stalled connection; the healthy one keeps up.
	for i := 0; i < sendBuffer+1; i++ {
		hub.Broadcast("room", []byte("update"))
		<-healthy.send
	}

	require.Eventually(t, func() bool { return len(stalled.closeCodes()) == 1 }, time.Second, 5*time.Millisecond)
	require.Equal(t, websocket.StatusPolicyViolation, stalled.closeCodes()[0])
	require.Empty(t, healthy.closeCodes(), "the healthy connection is untouched")
	require.Equal(t, 1, hub.Count("room"), "and the stalled one is forgotten")
}

func TestHubCloseAllClosesEveryoneAndRefusesNewcomers(t *testing.T) {
	hub := NewHub()
	a, b := newFakeConn(), newFakeConn()
	hub.add("room-a", a.conn)
	hub.add("room-b", b.conn)

	hub.CloseAll()
	require.Equal(t, []websocket.StatusCode{websocket.StatusGoingAway}, a.closeCodes())
	require.Equal(t, []websocket.StatusCode{websocket.StatusGoingAway}, b.closeCodes())
	require.Zero(t, hub.Count("room-a"))
	require.False(t, hub.add("room-a", newFakeConn().conn))
}

// everyone admits every request as an audience member of room "room".
type everyone struct{}

func (everyone) Authorize(http.ResponseWriter, *http.Request) (rooms.Viewer, bool) {
	return rooms.Viewer{Room: rooms.Room{ID: "room"}, ID: "session-1"}, true
}

type seenRecorder struct {
	mu  sync.Mutex
	ids []string
}

func (s *seenRecorder) Seen(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids = append(s.ids, id)
	return nil
}

// served runs a Service on a test server and returns it with its WebSocket URL.
func served(t *testing.T, ping, pong time.Duration) (*Service, *seenRecorder, string) {
	t.Helper()
	seen := &seenRecorder{}
	svc := &Service{
		Rooms:        everyone{},
		Audience:     seen,
		Hub:          NewHub(),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		PingInterval: ping,
		PongTimeout:  pong,
	}
	srv := httptest.NewServer(http.HandlerFunc(svc.ServeWS))
	t.Cleanup(srv.Close)
	policy, err := NewOriginPolicy(srv.URL)
	require.NoError(t, err)
	svc.Origins = policy
	return svc, seen, srv.URL
}

func dial(t *testing.T, ctx context.Context, url string) *websocket.Conn {
	t.Helper()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(url, "http"), &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{url}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { ws.CloseNow() })
	return ws
}

func readMessage(t *testing.T, ctx context.Context, ws *websocket.Conn) message {
	t.Helper()
	_, data, err := ws.Read(ctx)
	require.NoError(t, err)
	var msg message
	require.NoError(t, json.Unmarshal(data, &msg))
	return msg
}

func TestServeWSSendsTheSnapshotFirstThenBroadcasts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc, seen, url := served(t, 0, 0)

	ws := dial(t, ctx, url)
	require.Equal(t, message{Type: "snapshot", Revision: 0, State: State{Phase: "waiting", Turn: 0}}, readMessage(t, ctx, ws))
	require.Equal(t, 1, svc.Hub.Count("room"))
	require.Equal(t, []string{"session-1"}, seen.ids, "an audience session is marked as seen")

	svc.Hub.Broadcast("room", []byte(`{"type":"update","revision":1,"state":{"phase":"generating","turn":1}}`))
	require.Equal(t, message{Type: "update", Revision: 1, State: State{Phase: "generating", Turn: 1}}, readMessage(t, ctx, ws))

	// The browser leaving empties the map.
	require.NoError(t, ws.Close(websocket.StatusNormalClosure, ""))
	require.Eventually(t, func() bool { return svc.Hub.Count("room") == 0 }, 2*time.Second, 10*time.Millisecond)
}

func TestServeWSRefusesAnotherOrigin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc, seen, url := served(t, 0, 0)

	_, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(url, "http"), &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"http://evil.test"}},
	})
	require.Error(t, err)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Zero(t, svc.Hub.Count("room"))
	require.Empty(t, seen.ids, "refused before anything is looked up")
}

func TestServeWSKeepsAnAnsweringBrowserAndDropsASilentOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc, _, url := served(t, 20*time.Millisecond, 100*time.Millisecond)

	// This client library answers pings only while a Read is in progress.
	// The answering browser keeps reading; the silent one stops after its
	// snapshot, with its socket still open, as a frozen tab would.
	answering, silent := dial(t, ctx, url), dial(t, ctx, url)
	readMessage(t, ctx, answering)
	readMessage(t, ctx, silent)
	require.Equal(t, 2, svc.Hub.Count("room"))

	readErr := make(chan error, 1)
	go func() {
		_, _, err := answering.Read(ctx)
		readErr <- err
	}()

	require.Eventually(t, func() bool { return svc.Hub.Count("room") == 1 }, 2*time.Second, 10*time.Millisecond,
		"the silent browser is dropped after one unanswered ping")

	// Many more heartbeats pass and the answering browser is still there.
	select {
	case err := <-readErr:
		t.Fatalf("the answering browser was disconnected: %v", err)
	case <-time.After(400 * time.Millisecond):
	}
	require.Equal(t, 1, svc.Hub.Count("room"))
}

func TestCloseAllSendsGoingAway(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc, _, url := served(t, 0, 0)

	ws := dial(t, ctx, url)
	readMessage(t, ctx, ws)

	done := make(chan struct{})
	go func() { svc.Hub.CloseAll(); close(done) }()
	_, _, err := ws.Read(ctx)
	require.Equal(t, websocket.StatusGoingAway, websocket.CloseStatus(err))
	<-done
}
