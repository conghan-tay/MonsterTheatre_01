package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

// scriptedServer accepts one WebSocket, records the handshake's cookie and
// origin, sends the given messages, then echoes one message back.
func scriptedServer(t *testing.T, messages ...string) (*httptest.Server, *http.Request) {
	t.Helper()
	seen := new(http.Request)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc", Path: "/"})
			return
		}
		*seen = *r
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		for _, m := range messages {
			if err := ws.Write(r.Context(), websocket.MessageText, []byte(m)); err != nil {
				return
			}
		}
		if _, data, err := ws.Read(r.Context()); err == nil {
			_ = ws.Write(r.Context(), websocket.MessageText, data)
		}
		ws.Close(websocket.StatusNormalClosure, "")
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}

func TestConnReadsSnapshotThenWaitsForAPhase(t *testing.T) {
	srv, _ := scriptedServer(t,
		`{"type":"snapshot","revision":1,"state":{"phase":"generating","turn":1}}`,
		`{"type":"vote_received"}`,
		`{"type":"update","revision":2,"state":{"phase":"voting","turn":1}}`,
		`{"type":"update","revision":3,"state":{"phase":"preparing_line","turn":1}}`,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := New(srv.URL).Connect(ctx, "/ws")
	require.NoError(t, err)
	defer conn.Close()

	snapshot, err := conn.Snapshot(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, snapshot.Revision)
	require.Equal(t, "generating", snapshot.State.Phase)

	update, err := conn.NextUpdate(ctx)
	require.NoError(t, err, "NextUpdate skips the vote_received message")
	require.Equal(t, 2, update.Revision)

	prepared, err := conn.WaitForPhase(ctx, "preparing_line", 1)
	require.NoError(t, err)
	require.Equal(t, 3, prepared.Revision)
	require.JSONEq(t, `{"type":"update","revision":3,"state":{"phase":"preparing_line","turn":1}}`, string(prepared.Raw))
}

func TestSnapshotRejectsAnyOtherFirstMessage(t *testing.T) {
	srv, _ := scriptedServer(t, `{"type":"update","revision":2,"state":{"phase":"voting","turn":1}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := New(srv.URL).Connect(ctx, "/ws")
	require.NoError(t, err)
	defer conn.Close()

	_, err = conn.Snapshot(ctx)
	require.ErrorContains(t, err, "expected a snapshot")
}

func TestConnectSendsTheClientsCookiesAndOrigin(t *testing.T) {
	srv, seen := scriptedServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c := New(srv.URL)
	_, err := c.Get(ctx, "/login")
	require.NoError(t, err)
	_, held := c.Cookie("session")
	require.True(t, held, "the jar keeps the cookie the server set")

	conn, err := c.Connect(ctx, "/ws")
	require.NoError(t, err)
	defer conn.Close()

	cookie, err := seen.Cookie("session")
	require.NoError(t, err, "the handshake carries the session cookie")
	require.Equal(t, "abc", cookie.Value)
	require.Equal(t, srv.URL, seen.Header.Get("Origin"))

	require.NoError(t, conn.Send(ctx, map[string]any{"type": "vote", "turn": 1, "choice": "c2"}))
	echo, err := conn.Next(ctx)
	require.NoError(t, err)
	require.Equal(t, "vote", echo.Type)
}

func TestWaitForPhaseTimesOutWithAUsefulError(t *testing.T) {
	srv, _ := scriptedServer(t, `{"type":"snapshot","revision":1,"state":{"phase":"generating","turn":1}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	conn, err := New(srv.URL).Connect(ctx, "/ws")
	require.NoError(t, err)
	defer conn.Close()

	_, err = conn.WaitForPhase(ctx, "premiere", 8)
	require.ErrorContains(t, err, `waiting for phase "premiere" turn 8`)
}
