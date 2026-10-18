package theater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/performance"
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
		send: make(chan outbound, sendBuffer),
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

	hub.Broadcast("room-a", 7, []byte("hello"))
	require.Equal(t, outbound{revision: 7, data: []byte("hello")}, <-a1.send)
	require.Equal(t, "hello", string((<-a2.send).data))
	require.Empty(t, b.send, "another room hears nothing")

	hub.remove("room-a", a1.conn)
	hub.remove("room-a", a1.conn) // twice is harmless
	hub.Broadcast("room-a", 8, []byte("again"))
	require.Empty(t, a1.send, "a removed connection hears nothing")
	require.Equal(t, "again", string((<-a2.send).data))

	hub.remove("room-a", a2.conn)
	require.Zero(t, hub.Count("room-a"))
	hub.Broadcast("room-a", 9, []byte("nobody")) // an empty room is fine
}

func TestHubDropsAConnectionThatFallsBehind(t *testing.T) {
	hub := NewHub()
	stalled, healthy := newFakeConn(), newFakeConn()
	hub.add("room", stalled.conn)
	hub.add("room", healthy.conn)

	// Nobody reads the stalled connection; the healthy one keeps up.
	for i := 0; i < sendBuffer+1; i++ {
		hub.Broadcast("room", i+1, []byte("update"))
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

// fakePerformances is the room's performance row, or none.
type fakePerformances struct {
	mu  sync.Mutex
	row *performance.Performance
	err error
	// calls counts lookups.
	calls int
}

func (f *fakePerformances) ByRoom(context.Context, string) (performance.Performance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return performance.Performance{}, f.err
	}
	if f.row == nil {
		return performance.Performance{}, performance.ErrNotFound
	}
	return *f.row, nil
}

// fakeWorkflow answers the state query. If asked is set, it is called while
// the query is in flight, which is when a real update could be published.
type fakeWorkflow struct {
	snapshot performance.Snapshot
	err      error
	asked    func()
	// askedBy records the voter each query was made for.
	askedBy []string
}

func (f *fakeWorkflow) State(_ context.Context, _, voterID string) (performance.Snapshot, error) {
	f.askedBy = append(f.askedBy, voterID)
	if f.asked != nil {
		f.asked()
	}
	return f.snapshot, f.err
}

// served runs a Service on a test server and returns it with its WebSocket
// URL. The room has no performance unless the test gives it one.
func served(t *testing.T, ping, pong time.Duration) (*Service, *seenRecorder, string) {
	t.Helper()
	seen := &seenRecorder{}
	svc := &Service{
		Rooms:        everyone{},
		Audience:     seen,
		Performances: &fakePerformances{},
		Workflows:    &fakeWorkflow{err: errors.New("no performance is running")},
		Votes:        &fakeVotes{},
		Now:          func() time.Time { return testNow },
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

func readMessage(t *testing.T, ctx context.Context, ws *websocket.Conn) performance.Message {
	t.Helper()
	_, data, err := ws.Read(ctx)
	require.NoError(t, err)
	var msg performance.Message
	require.NoError(t, json.Unmarshal(data, &msg))
	return msg
}

func TestServeWSSendsTheSnapshotFirstThenBroadcasts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc, seen, url := served(t, 0, 0)

	ws := dial(t, ctx, url)
	require.Equal(t, performance.Message{Type: "snapshot", Revision: 0, State: performance.State{Phase: "waiting", Turn: 0}}, readMessage(t, ctx, ws))
	require.Equal(t, 1, svc.Hub.Count("room"))
	require.Equal(t, []string{"session-1"}, seen.ids, "an audience session is marked as seen")

	svc.Hub.Broadcast("room", 1, []byte(`{"type":"update","revision":1,"state":{"phase":"generating","turn":1}}`))
	require.Equal(t, performance.Message{Type: "update", Revision: 1, State: performance.State{Phase: "generating", Turn: 1}}, readMessage(t, ctx, ws))

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

func TestWaitingSnapshotIsExactlyPhaseAndTurn(t *testing.T) {
	// The browser tests compare this message whole, so nothing may be added.
	require.JSONEq(t, `{"type":"snapshot","revision":0,"state":{"phase":"waiting","turn":0}}`,
		string(snapshotMessage(0, performance.State{Phase: performance.PhaseWaiting})))
}

func updateJSON(revision int) []byte {
	data, _ := json.Marshal(performance.Message{Type: performance.TypeUpdate, Revision: revision, State: performance.State{Phase: "voting", Turn: 1}})
	return data
}

func TestServeWSSnapshotComesFromTheRunningWorkflowAndOlderUpdatesAreDropped(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc, _, url := served(t, 0, 0)
	deadline := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	state := performance.State{
		Phase: performance.PhaseVoting, Turn: 2, PerformanceID: "perf-1", Speaker: "B", Beat: "QUESTION",
		Choices:  []performance.Choice{{ID: "c1", Text: "one"}, {ID: "c2", Text: "two"}, {ID: "c3", Text: "three"}},
		Deadline: &deadline,
		Lines:    []performance.Line{{Turn: 1, Speaker: "A", Text: "hello"}},
	}
	svc.Performances = &fakePerformances{row: &performance.Performance{ID: "perf-1", RoomID: "room", Status: performance.StatusRunning}}
	svc.Workflows = &fakeWorkflow{
		snapshot: performance.Snapshot{Revision: 5, State: state},
		// While the workflow is being asked, updates 4, 5, and 6 arrive. The
		// connection is already registered, so they are queued for it.
		asked: func() {
			for revision := 4; revision <= 6; revision++ {
				svc.Hub.Broadcast("room", revision, updateJSON(revision))
			}
		},
	}

	ws := dial(t, ctx, url)
	snapshot := readMessage(t, ctx, ws)
	require.Equal(t, performance.Message{Type: "snapshot", Revision: 5, State: state}, snapshot)

	// 4 and 5 are covered by the snapshot; only 6 follows.
	require.Equal(t, 6, readMessage(t, ctx, ws).Revision)
	svc.Hub.Broadcast("room", 7, updateJSON(7))
	require.Equal(t, 7, readMessage(t, ctx, ws).Revision)
}

func TestServeWSSendsACompletedPerformanceAsItsPremiere(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	startsAt := time.Date(2026, 10, 11, 12, 0, 5, 0, time.UTC)
	svc, _, url := served(t, 0, 0)
	svc.Performances = &fakePerformances{row: &performance.Performance{
		ID: "perf-1", RoomID: "room", Status: performance.StatusCompleted, PremiereStartsAt: startsAt,
		Script: performance.Script{Turns: []performance.ScriptTurn{
			{Turn: 1, Speaker: "A", Winner: performance.Choice{ID: "c2", Text: "hello"}, TaggedText: "[tag] hello"},
			{Turn: 2, Speaker: "B", Winner: performance.Choice{ID: "c1", Text: "goodbye"}, TaggedText: "[tag] goodbye"},
		}},
	}}

	ws := dial(t, ctx, url)
	require.Equal(t, performance.Message{
		Type: "snapshot", Revision: performance.LastRevision, State: performance.State{
			Phase: performance.PhasePremiere, Turn: performance.Turns, PerformanceID: "perf-1",
			Lines: []performance.Line{{Turn: 1, Speaker: "A", Text: "hello"}, {Turn: 2, Speaker: "B", Text: "goodbye"}},
			Premiere: &performance.Premiere{StartsAt: startsAt, Clips: []performance.Clip{
				{Turn: 1, Speaker: "A", URL: "/api/v1/performances/perf-1/turns/1/audio"},
				{Turn: 2, Speaker: "B", URL: "/api/v1/performances/perf-1/turns/2/audio"},
			}},
		},
	}, readMessage(t, ctx, ws), "read from the row: the workflow is not asked, so no worker is needed")

	// The premiere update itself, arriving late, is nothing new.
	svc.Hub.Broadcast("room", performance.LastRevision, updateJSON(performance.LastRevision))
	svc.Hub.Broadcast("room", performance.LastRevision+1, updateJSON(performance.LastRevision+1))
	require.Equal(t, performance.LastRevision+1, readMessage(t, ctx, ws).Revision)
}

func TestACompletedLipSyncRunIsSentAtItsOwnPremiereRevision(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc, _, url := served(t, 0, 0)
	svc.Performances = &fakePerformances{row: &performance.Performance{
		ID: "perf-1", RoomID: "room", Status: performance.StatusCompleted,
		Script: performance.Script{PremiereRevision: 29, Turns: []performance.ScriptTurn{
			{Turn: 1, Speaker: "A", Winner: performance.Choice{ID: "c2", Text: "hello"}, VideoKey: "performances/perf-1/turn-1.mp4"},
		}},
	}}

	ws := dial(t, ctx, url)
	snapshot := readMessage(t, ctx, ws)
	require.Equal(t, 29, snapshot.Revision, "the revision the premiere was published at, past 25 for its progress updates")
	require.Equal(t, "/api/v1/performances/perf-1/turns/1/video", snapshot.State.Premiere.Clips[0].VideoURL)

	// The last progress update, still on its way when the row was read, is
	// older than the premiere and is dropped, not shown after it.
	svc.Hub.Broadcast("room", 28, updateJSON(28))
	svc.Hub.Broadcast("room", 30, updateJSON(30))
	require.Equal(t, 30, readMessage(t, ctx, ws).Revision)
}

func TestServeWSSendsAFailedPerformanceWithItsReason(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc, _, url := served(t, 0, 0)
	svc.Performances = &fakePerformances{row: &performance.Performance{
		ID: "perf-1", RoomID: "room", Status: performance.StatusFailed, FailureReason: performance.FailureSpeech,
	}}

	ws := dial(t, ctx, url)
	require.Equal(t, performance.Message{
		Type: "snapshot", Revision: 0, State: performance.State{
			Phase: performance.PhaseFailed, PerformanceID: "perf-1",
			Failure: &performance.Failure{Code: "speech_failed", Message: "The line could not be turned into speech."},
		},
	}, readMessage(t, ctx, ws), "read from the row: the workflow is not asked, so no worker is needed")

	// The row does not say which revision the run failed at, so the failed
	// update, if it is still on its way, is let through.
	svc.Hub.Broadcast("room", 7, updateJSON(7))
	require.Equal(t, 7, readMessage(t, ctx, ws).Revision)
}

// fakeStream is a Redis stream held in memory. Entry IDs are "1-0", "2-0", ...
type fakeStream struct {
	mu      sync.Mutex
	entries []redis.XMessage
	// failures is how many of the next calls fail.
	failures int
	// readFrom records the ID each XRead asked to read after.
	readFrom []string
}

func (f *fakeStream) add(roomID string, revision int, message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, redis.XMessage{
		ID: strconv.Itoa(len(f.entries)+1) + "-0",
		Values: map[string]any{
			performance.StreamFieldRoomID:   roomID,
			performance.StreamFieldRevision: strconv.Itoa(revision),
			performance.StreamFieldMessage:  message,
		},
	})
}

func (f *fakeStream) fail() bool {
	if f.failures > 0 {
		f.failures--
		return true
	}
	return false
}

func (f *fakeStream) XRevRangeN(context.Context, string, string, string, int64) *redis.XMessageSliceCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail() {
		return redis.NewXMessageSliceCmdResult(nil, errors.New("connection refused"))
	}
	if len(f.entries) == 0 {
		return redis.NewXMessageSliceCmdResult(nil, nil)
	}
	return redis.NewXMessageSliceCmdResult(f.entries[len(f.entries)-1:], nil)
}

func (f *fakeStream) XRead(ctx context.Context, a *redis.XReadArgs) *redis.XStreamSliceCmd {
	f.mu.Lock()
	if f.fail() {
		f.mu.Unlock()
		return redis.NewXStreamSliceCmdResult(nil, errors.New("connection reset"))
	}
	after, _ := strconv.Atoi(strings.TrimSuffix(a.Streams[1], "-0"))
	f.readFrom = append(f.readFrom, a.Streams[1])
	newer := append([]redis.XMessage(nil), f.entries[min(after, len(f.entries)):]...)
	f.mu.Unlock()
	if len(newer) == 0 {
		// A short stand-in for the blocking wait.
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Millisecond):
		}
		return redis.NewXStreamSliceCmdResult(nil, redis.Nil)
	}
	return redis.NewXStreamSliceCmdResult([]redis.XStream{{Stream: a.Streams[0], Messages: newer}}, nil)
}

type broadcast struct {
	roomID   string
	revision int
	msg      string
}

type recordingHub struct {
	mu   sync.Mutex
	sent []broadcast
}

func (r *recordingHub) Broadcast(roomID string, revision int, msg []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, broadcast{roomID, revision, string(msg)})
}

func (r *recordingHub) all() []broadcast {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]broadcast(nil), r.sent...)
}

func runReader(t *testing.T, stream *fakeStream) *recordingHub {
	t.Helper()
	hub := &recordingHub{}
	reader := &StreamReader{
		Redis:      stream,
		Hub:        hub,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		RetryDelay: time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { reader.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("the reader did not stop when its context ended")
		}
	})
	return hub
}

// waitUntilReading returns once the reader has found the end of the stream
// and asked for what follows.
func waitUntilReading(t *testing.T, stream *fakeStream) {
	t.Helper()
	require.Eventually(t, func() bool {
		stream.mu.Lock()
		defer stream.mu.Unlock()
		return len(stream.readFrom) > 0
	}, time.Second, time.Millisecond)
}

func TestStreamReaderForwardsOnlyWhatArrivesAfterItStarts(t *testing.T) {
	stream := &fakeStream{}
	stream.add("room-a", 1, "old-1")
	stream.add("room-a", 2, "old-2")
	hub := runReader(t, stream)

	// Once it has begun reading, it is past the two existing entries.
	waitUntilReading(t, stream)

	stream.add("room-a", 3, "new-3")
	stream.add("room-b", 1, "other-room")
	stream.add("room-a", 4, "new-4")

	require.Eventually(t, func() bool { return len(hub.all()) == 3 }, time.Second, time.Millisecond)
	require.Equal(t, []broadcast{
		{"room-a", 3, "new-3"}, {"room-b", 1, "other-room"}, {"room-a", 4, "new-4"},
	}, hub.all(), "each entry goes to its own room, once, in order")
}

func TestStreamReaderResumesFromWhereItWasAfterRedisFails(t *testing.T) {
	stream := &fakeStream{failures: 2} // Redis is down when the server starts
	hub := runReader(t, stream)

	waitUntilReading(t, stream)
	stream.add("room", 1, "first")
	require.Eventually(t, func() bool { return len(hub.all()) == 1 }, time.Second, time.Millisecond)

	// Redis fails again; two updates are published while reads are failing.
	stream.mu.Lock()
	stream.failures = 3
	stream.mu.Unlock()
	stream.add("room", 2, "second")
	stream.add("room", 3, "third")

	require.Eventually(t, func() bool { return len(hub.all()) == 3 }, time.Second, time.Millisecond)
	require.Equal(t, []broadcast{{"room", 1, "first"}, {"room", 2, "second"}, {"room", 3, "third"}}, hub.all(),
		"nothing published during the outage is lost or repeated")
}

func TestStreamReaderSkipsAMalformedEntry(t *testing.T) {
	stream := &fakeStream{}
	hub := runReader(t, stream)
	waitUntilReading(t, stream)

	stream.mu.Lock()
	stream.entries = append(stream.entries, redis.XMessage{ID: "1-0", Values: map[string]any{"unexpected": "shape"}})
	stream.mu.Unlock()
	stream.add("room", 2, "good")

	require.Eventually(t, func() bool { return len(hub.all()) == 1 }, time.Second, time.Millisecond)
	require.Equal(t, []broadcast{{"room", 2, "good"}}, hub.all())
}

var testNow = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

// fakeVotes records the votes passed to the workflow.
type fakeVotes struct {
	mu    sync.Mutex
	err   error
	votes []performance.Vote
	// performances is the performance each vote was sent to.
	performances []string
}

func (f *fakeVotes) Vote(_ context.Context, performanceID string, vote performance.Vote) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.votes = append(f.votes, vote)
	f.performances = append(f.performances, performanceID)
	return nil
}

func (f *fakeVotes) all() []performance.Vote {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]performance.Vote(nil), f.votes...)
}

// votingRoom is a served room whose performance is running, with one
// browser connected and its snapshot read.
func votingRoom(t *testing.T, ctx context.Context) (*Service, *fakeVotes, *websocket.Conn) {
	t.Helper()
	svc, _, url := served(t, 0, 0)
	votes := &fakeVotes{}
	svc.Votes = votes
	svc.Performances = &fakePerformances{row: &performance.Performance{ID: "perf-1", RoomID: "room", Status: performance.StatusRunning}}
	svc.Workflows = &fakeWorkflow{snapshot: performance.Snapshot{Revision: 2, State: performance.State{Phase: performance.PhaseVoting, Turn: 1}}}
	ws := dial(t, ctx, url)
	readMessage(t, ctx, ws)
	return svc, votes, ws
}

// say sends a raw message and returns the reply, as generic JSON.
func say(t *testing.T, ctx context.Context, ws *websocket.Conn, message string) map[string]any {
	t.Helper()
	require.NoError(t, ws.Write(ctx, websocket.MessageText, []byte(message)))
	_, data, err := ws.Read(ctx)
	require.NoError(t, err)
	var reply map[string]any
	require.NoError(t, json.Unmarshal(data, &reply), "reply: %s", data)
	return reply
}

func TestAVoteIsStampedAndPassedOnAsTheConnectionsVoter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, votes, ws := votingRoom(t, ctx)

	// The message tries to vote as someone else, at a time of its choosing.
	reply := say(t, ctx, ws, `{"type":"vote","turn":1,"choiceId":"c2","voterId":"director-1","receivedAt":"2020-01-01T00:00:00Z"}`)
	require.Equal(t, map[string]any{"type": "vote_received", "turn": float64(1), "choiceId": "c2"}, reply)

	require.Equal(t, []performance.Vote{
		{VoterID: "session-1", Turn: 1, ChoiceID: "c2", ReceivedAt: testNow},
	}, votes.all(), "the voter is the connection's and the stamp is the server's clock")
	require.Equal(t, []string{"perf-1"}, votes.performances)
}

func TestTheSnapshotIsAskedForAsTheConnectionsVoter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc, _, _ := votingRoom(t, ctx)
	require.Equal(t, []string{"session-1"}, svc.Workflows.(*fakeWorkflow).askedBy)
}

func TestAConnectionPassesOnOneVotePerTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, votes, ws := votingRoom(t, ctx)

	say(t, ctx, ws, `{"type":"vote","turn":1,"choiceId":"c2"}`)
	again := say(t, ctx, ws, `{"type":"vote","turn":1,"choiceId":"c3"}`)
	require.Equal(t, map[string]any{"type": "vote_received", "turn": float64(1), "choiceId": "c2"}, again,
		"the reply names the vote that was passed on, not the repeat")
	next := say(t, ctx, ws, `{"type":"vote","turn":2,"choiceId":"c1"}`)
	require.Equal(t, "c1", next["choiceId"])

	passed := votes.all()
	require.Len(t, passed, 2, "one signal per turn")
	require.Equal(t, "c2", passed[0].ChoiceID)
	require.Equal(t, 2, passed[1].Turn)
}

func TestMessagesThatAreNotVotes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, votes, ws := votingRoom(t, ctx)

	for name, message := range map[string]string{
		"not JSON":           `vote for c1 please`,
		"turn is not number": `{"type":"vote","turn":"one","choiceId":"c1"}`,
		"turn zero":          `{"type":"vote","turn":0,"choiceId":"c1"}`,
		"turn nine":          `{"type":"vote","turn":9,"choiceId":"c1"}`,
		"no choice":          `{"type":"vote","turn":1}`,
		"an enormous choice": `{"type":"vote","turn":1,"choiceId":"` + strings.Repeat("c", 100) + `"}`,
	} {
		reply := say(t, ctx, ws, message)
		require.Equal(t, "error", reply["type"], name)
		require.Equal(t, "invalid_message", reply["code"], name)
		require.NotEmpty(t, reply["message"], name)
	}
	require.Empty(t, votes.all(), "nothing reached the workflow")

	// A type this server does not know is ignored without a reply: the next
	// reply to arrive is the vote's.
	require.NoError(t, ws.Write(ctx, websocket.MessageText, []byte(`{"type":"applause"}`)))
	reply := say(t, ctx, ws, `{"type":"vote","turn":1,"choiceId":"c1"}`)
	require.Equal(t, "vote_received", reply["type"])
}

func TestAVoteThatCannotBeDeliveredIsReportedAndCanBeRetried(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, votes, ws := votingRoom(t, ctx)

	votes.mu.Lock()
	votes.err = errors.New("workflow execution already completed")
	votes.mu.Unlock()
	reply := say(t, ctx, ws, `{"type":"vote","turn":1,"choiceId":"c1"}`)
	require.Equal(t, "error", reply["type"])
	require.Equal(t, "vote_not_delivered", reply["code"])

	// It was not recorded as passed on, so the same vote can be sent again.
	votes.mu.Lock()
	votes.err = nil
	votes.mu.Unlock()
	reply = say(t, ctx, ws, `{"type":"vote","turn":1,"choiceId":"c1"}`)
	require.Equal(t, "vote_received", reply["type"])
	require.Len(t, votes.all(), 1)
}

func TestAVoteBeforeStartIsNotDeliveredThenWorksOnceStarted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc, _, url := served(t, 0, 0)
	votes := svc.Votes.(*fakeVotes)
	rows := svc.Performances.(*fakePerformances)

	// Connected while the room is waiting, as most browsers are.
	ws := dial(t, ctx, url)
	readMessage(t, ctx, ws)
	reply := say(t, ctx, ws, `{"type":"vote","turn":1,"choiceId":"c1"}`)
	require.Equal(t, "vote_not_delivered", reply["code"])
	require.Empty(t, votes.all())

	// The director presses Start. The same connection can now vote.
	rows.mu.Lock()
	rows.row = &performance.Performance{ID: "perf-1", RoomID: "room", Status: performance.StatusRunning}
	rows.mu.Unlock()
	reply = say(t, ctx, ws, `{"type":"vote","turn":1,"choiceId":"c1"}`)
	require.Equal(t, "vote_received", reply["type"])
	require.Equal(t, []string{"perf-1"}, votes.performances)
}

func TestAConnectionLooksItsPerformanceUpOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc, votes, ws := votingRoom(t, ctx)
	rows := svc.Performances.(*fakePerformances)
	rows.mu.Lock()
	rows.calls = 0 // connecting read the row once, for the snapshot
	rows.mu.Unlock()

	for turn := 1; turn <= performance.Turns; turn++ {
		reply := say(t, ctx, ws, fmt.Sprintf(`{"type":"vote","turn":%d,"choiceId":"c1"}`, turn))
		require.Equal(t, "vote_received", reply["type"], "turn %d", turn)
	}
	require.Len(t, votes.all(), performance.Turns)

	rows.mu.Lock()
	defer rows.mu.Unlock()
	require.Equal(t, 1, rows.calls,
		"a vote is one call to Temporal and nothing else: the database is read for the first vote only")
}
