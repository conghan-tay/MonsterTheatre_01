package theater

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/rooms"
)

// doorByCookie admits a request as the room's director if it carries a
// cookie named director, and as an audience member otherwise.
type doorByCookie struct{}

func (doorByCookie) Authorize(_ http.ResponseWriter, r *http.Request) (rooms.Viewer, bool) {
	if _, err := r.Cookie("director"); err == nil {
		return rooms.Viewer{Room: rooms.Room{ID: "room"}, ID: "director-1", AsDirector: true}, true
	}
	return rooms.Viewer{Room: rooms.Room{ID: "room"}, ID: "session-1"}, true
}

// fakeMembers is a room's audience, by nickname.
type fakeMembers struct {
	mu        sync.Mutex
	nicknames []string
	calls     int
}

func (f *fakeMembers) Nicknames(context.Context, string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return append([]string(nil), f.nicknames...), nil
}

// asked is how many times the audience has been read.
func (f *fakeMembers) asked() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeMembers) set(nicknames ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nicknames = nicknames
}

func dialAs(t *testing.T, ctx context.Context, url string, director bool) *websocket.Conn {
	t.Helper()
	header := http.Header{"Origin": []string{url}}
	if director {
		header.Set("Cookie", "director=yes")
	}
	ws, _, err := websocket.Dial(ctx, "ws"+url[len("http"):], &websocket.DialOptions{HTTPHeader: header})
	require.NoError(t, err)
	t.Cleanup(func() { ws.CloseNow() })
	return ws
}

func readRaw(t *testing.T, ctx context.Context, ws *websocket.Conn) map[string]any {
	t.Helper()
	_, data, err := ws.Read(ctx)
	require.NoError(t, err)
	var msg map[string]any
	require.NoError(t, json.Unmarshal(data, &msg), "message: %s", data)
	return msg
}

func TestTheDirectorIsToldTheAudienceOnConnectingAndWhenItChanges(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc, _, url := served(t, 0, 0)
	members := &fakeMembers{}
	svc.Rooms, svc.Members = doorByCookie{}, members

	director := dialAs(t, ctx, url, true)
	require.Equal(t, "snapshot", readRaw(t, ctx, director)["type"], "the snapshot is still first")
	require.Equal(t, map[string]any{"type": "audience", "count": 0.0, "nicknames": []any{}}, readRaw(t, ctx, director),
		"an empty list, not null")

	member := dialAs(t, ctx, url, false)
	require.Equal(t, "snapshot", readRaw(t, ctx, member)["type"])

	members.set("Ana")
	svc.AudienceChanged(ctx, "room")
	require.Equal(t, map[string]any{"type": "audience", "count": 1.0, "nicknames": []any{"Ana"}}, readRaw(t, ctx, director))

	members.set("Ana", "Ben")
	svc.AudienceChanged(ctx, "room")
	require.Equal(t, map[string]any{"type": "audience", "count": 2.0, "nicknames": []any{"Ana", "Ben"}}, readRaw(t, ctx, director))

	// The audience member was sent none of that: the next thing on their
	// connection is the update broadcast now.
	svc.Hub.Broadcast("room", 1, updateJSON(1))
	next := readRaw(t, ctx, member)
	require.Equal(t, "update", next["type"], "message: %v", next)
	// The director gets the update too, after the audience messages.
	require.Equal(t, "update", readRaw(t, ctx, director)["type"])
}

func TestAudienceChangedInARoomNobodyIsWatching(t *testing.T) {
	svc, _, _ := served(t, 0, 0)
	svc.Members = &fakeMembers{nicknames: []string{"Ana"}}
	svc.AudienceChanged(context.Background(), "room") // nothing to send to, and nothing breaks
	require.Zero(t, svc.Hub.Count("room"))
}
