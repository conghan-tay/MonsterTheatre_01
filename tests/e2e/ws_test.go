package e2e_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/tests/e2e/client"
)

// noSuchRoomID is a well-formed room ID that no room has.
const noSuchRoomID = "00000000-0000-4000-8000-000000000000"

// enteredMember returns an audience member's browser holding a session for
// the room.
func enteredMember(t *testing.T, ctx context.Context, shareLink string) *client.AudienceClient {
	t.Helper()
	member := client.NewAudience(stack.BaseURL)
	resp, err := member.Enter(ctx, shareLink)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.Status, "enter the theatre; body: %s", resp.Body)
	return member
}

// requireWaitingSnapshot reads the connection's first message and requires
// it to say the room is waiting for its director.
func requireWaitingSnapshot(t *testing.T, ctx context.Context, conn *client.Conn) {
	t.Helper()
	msg, err := conn.Snapshot(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, msg.Revision, "nothing has been published yet; message: %s", msg.Raw)
	require.Equal(t, client.PhaseWaiting, msg.State.Phase, "message: %s", msg.Raw)
	require.Equal(t, 0, msg.State.Turn, "message: %s", msg.Raw)
}

// H3: a connected browser's first message is a snapshot saying the room is
// waiting for the director.
func TestFirstMessageIsAWaitingSnapshot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	director, room, _ := newRoom(t, ctx)
	member := enteredMember(t, ctx, room.ShareLink)

	for name, browser := range map[string]*client.Client{
		"director":        director.Client,
		"audience member": member.Client,
	} {
		conn, err := browser.ConnectRoom(ctx, room.RoomID)
		require.NoError(t, err, "%s connects", name)
		defer conn.Close()
		requireWaitingSnapshot(t, ctx, conn)

		// A second tab in the same browser gets its own connection.
		second, err := browser.ConnectRoom(ctx, room.RoomID)
		require.NoError(t, err, "%s connects again", name)
		defer second.Close()
		requireWaitingSnapshot(t, ctx, second)
	}
}

// S5: a WebSocket from another origin, or with no session, is refused.
func TestWebSocketIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	director, room, _ := newRoom(t, ctx)
	member := enteredMember(t, ctx, room.ShareLink)

	_, otherRoom, _ := newRoom(t, ctx)
	memberElsewhere := enteredMember(t, ctx, otherRoom.ShareLink)
	otherDirector := loggedInDirector(t, ctx)

	cases := []struct {
		name    string
		browser *client.Client
		roomID  string
		// origin is what the handshake claims; "same" means the server's own.
		origin string
		status int
		code   string
	}{
		{"director, from another site", director.Client, room.RoomID, "http://evil.test", http.StatusForbidden, "origin_not_allowed"},
		{"audience member, from another site", member.Client, room.RoomID, "http://evil.test", http.StatusForbidden, "origin_not_allowed"},
		{"another site on the server's hostname but a different port", director.Client, room.RoomID, "http://127.0.0.1:1", http.StatusForbidden, "origin_not_allowed"},
		{"no Origin header", director.Client, room.RoomID, "", http.StatusForbidden, "origin_not_allowed"},
		{"no session", client.New(stack.BaseURL), room.RoomID, "same", http.StatusUnauthorized, "not_logged_in"},
		{"audience member of another room", memberElsewhere.Client, room.RoomID, "same", http.StatusUnauthorized, "not_logged_in"},
		{"another director", otherDirector.Client, room.RoomID, "same", http.StatusForbidden, "not_your_room"},
		{"a room that does not exist", director.Client, noSuchRoomID, "same", http.StatusNotFound, "not_found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origin := tc.origin
			if origin == "same" {
				origin = stack.BaseURL
			}
			conn, err := tc.browser.ConnectFrom(ctx, client.RoomSocketPath(tc.roomID), origin)
			if err == nil {
				conn.Close()
				t.Fatal("the WebSocket was opened; it should have been refused")
			}
			var refused *client.HandshakeError
			require.ErrorAs(t, err, &refused)
			require.Equal(t, tc.status, refused.Status, "error: %v", err)
			require.Equal(t, tc.code, refused.Code, "error: %v", err)
		})
	}
}

// Step 8: a browser on the web app's origin is accepted, although that
// origin differs from the server's own address.
func TestWebSocketAcceptsTheWebAppOrigin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	director, room, _ := newRoom(t, ctx)
	conn, err := director.ConnectFrom(ctx, client.RoomSocketPath(room.RoomID), stack.PublicBaseURL)
	require.NoError(t, err)
	defer conn.Close()
	requireWaitingSnapshot(t, ctx, conn)
}
