package httpapi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// lockedBuffer is a log destination the server's goroutines and the test
// can share.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func accessLines(buf *lockedBuffer) []map[string]any {
	var lines []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil && m["msg"] == "http request" {
			lines = append(lines, m)
		}
	}
	return lines
}

func TestEachRequestIsLoggedWithItsRouteStatusAndID(t *testing.T) {
	var buf lockedBuffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	mux := http.NewServeMux()
	var seen string
	mux.HandleFunc("GET /api/v1/rooms/{roomId}", func(w http.ResponseWriter, r *http.Request) {
		seen = RequestID(r.Context())
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(Logged(logger, mux))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/rooms/room-9")
	require.NoError(t, err)
	resp.Body.Close()

	lines := accessLines(&buf)
	require.Len(t, lines, 1)
	line := lines[0]
	require.Equal(t, "GET", line["method"])
	require.Equal(t, "GET /api/v1/rooms/{roomId}", line["route"], "the pattern, not the path, so lines group by route")
	require.Equal(t, "room-9", line["room_id"])
	require.EqualValues(t, 404, line["status"])
	require.Contains(t, line, "duration_ms")
	require.NotEmpty(t, line["request_id"])
	require.Equal(t, line["request_id"], resp.Header.Get("X-Request-Id"), "the caller is told the ID")
	require.Equal(t, line["request_id"], seen, "and the handler can log it")
}

func TestARequestIDFromTheCallerIsKeptOnlyIfItIsSafe(t *testing.T) {
	var buf lockedBuffer
	srv := httptest.NewServer(Logged(slog.New(slog.NewJSONHandler(&buf, nil)), http.NotFoundHandler()))
	defer srv.Close()
	ask := func(id string) string {
		req, _ := http.NewRequest("GET", srv.URL+"/x", nil)
		req.Header.Set("X-Request-Id", id)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.Header.Get("X-Request-Id")
	}
	require.Equal(t, "abc-123", ask("abc-123"))
	require.NotEqual(t, `"}{evil`, ask(`"}{evil`))
	require.NotEqual(t, strings.Repeat("a", 200), ask(strings.Repeat("a", 200)))
}

func TestTheLivenessCheckIsLoggedOnlyAtDebug(t *testing.T) {
	var buf lockedBuffer
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, r *http.Request) {})
	srv := httptest.NewServer(Logged(slog.New(slog.NewJSONHandler(&buf, nil)), mux))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/livez")
	require.NoError(t, err)
	resp.Body.Close()
	require.Empty(t, accessLines(&buf), "the load balancer asks every 30 s")
}

func TestAWebSocketUpgradeCanStillTakeOverTheConnection(t *testing.T) {
	var buf lockedBuffer
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := http.NewResponseController(w).Hijack()
		require.NoError(t, err)
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: test\r\n\r\n")
		_ = rw.Flush()
		conn.Close()
	})
	srv := httptest.NewServer(Logged(slog.New(slog.NewJSONHandler(&buf, nil)), handler))
	defer srv.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	require.NoError(t, err)
	defer conn.Close()
	_, _ = conn.Write([]byte("GET /ws HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: test\r\n\r\n"))
	status, err := bufio.NewReader(conn).ReadString('\n')
	require.NoError(t, err)
	require.Contains(t, status, "101")
	require.Eventually(t, func() bool { return len(accessLines(&buf)) == 1 }, time.Second, 10*time.Millisecond)
	require.EqualValues(t, 101, accessLines(&buf)[0]["status"])
}
