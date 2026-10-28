package theater

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func TestADroppedConnectionIsLoggedWithTheLastRevisionItWasSent(t *testing.T) {
	var buf lockedBuffer
	hub := NewHub()
	hub.Logger = slog.New(slog.NewJSONHandler(&buf, nil))
	stalled := newFakeConn()
	stalled.id = "conn-1"
	stalled.lastSent.Store(3)
	hub.add("room", stalled.conn)

	var dropped int
	for i := 0; i < sendBuffer+1; i++ {
		_, d := hub.Broadcast("room", i+4, []byte("update"))
		dropped += d
	}
	require.Equal(t, 1, dropped, "the broadcast says so")
	lines := buf.lines("websocket dropped")
	require.Len(t, lines, 1)
	require.Equal(t, "room", lines[0]["room_id"])
	require.Equal(t, "conn-1", lines[0]["conn_id"])
	require.EqualValues(t, 3, lines[0]["last_revision_sent"])
	require.True(t, stalled.dropped.Load())
}

func TestADisconnectSaysWhyAndWhatTheBrowserLastHad(t *testing.T) {
	var buf lockedBuffer
	svc, _, url := served(t, time.Hour, time.Second)
	svc.Logger = slog.New(slog.NewJSONHandler(&buf, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ws := dial(t, ctx, url)
	require.Equal(t, 0, readMessage(t, ctx, ws).Revision, "the snapshot")
	require.Eventually(t, func() bool { return len(buf.lines("websocket connected")) == 1 }, time.Second, time.Millisecond)
	svc.Hub.Broadcast("room", 1, updateJSON(1))
	require.Equal(t, 1, readMessage(t, ctx, ws).Revision)
	require.NoError(t, ws.Close(websocket.StatusNormalClosure, "bye"))

	require.Eventually(t, func() bool { return len(buf.lines("websocket disconnected")) == 1 }, 2*time.Second, 5*time.Millisecond)
	connected := buf.lines("websocket connected")[0]
	line := buf.lines("websocket disconnected")[0]
	require.NotEmpty(t, line["conn_id"])
	require.Equal(t, connected["conn_id"], line["conn_id"], "the two lines can be matched")
	require.Contains(t, line["reason"], "browser closed")
	require.EqualValues(t, 1, line["last_revision_sent"])
	require.Contains(t, line, "duration_ms")
}

func TestAResyncIsLogged(t *testing.T) {
	var buf lockedBuffer
	svc, _, url := served(t, time.Hour, time.Second)
	svc.Logger = slog.New(slog.NewJSONHandler(&buf, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ws := dial(t, ctx, url)
	readMessage(t, ctx, ws)
	require.NoError(t, ws.Write(ctx, websocket.MessageText, []byte(`{"type":"resync"}`)))
	readMessage(t, ctx, ws)
	require.Eventually(t, func() bool { return len(buf.lines("resync requested")) == 1 }, time.Second, 5*time.Millisecond)
	require.Contains(t, buf.lines("resync requested")[0], "last_revision_sent")
}
