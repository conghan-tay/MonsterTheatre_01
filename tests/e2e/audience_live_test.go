package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/tests/e2e/client"
)

// An audience member gives a nickname before entering. The server checks
// it, and a refusal takes no seat.
func TestANicknameIsRequiredToEnter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	director, room, _ := newRoom(t, ctx)
	roomID, token, err := client.ParseShareLink(room.ShareLink)
	require.NoError(t, err)
	member := client.NewAudience(stack.BaseURL)

	for name, body := range map[string]map[string]any{
		"no nickname":     {"roomId": roomID, "token": token},
		"an empty one":    {"roomId": roomID, "token": token, "nickname": ""},
		"only spaces":     {"roomId": roomID, "token": token, "nickname": "   "},
		"31 characters":   {"roomId": roomID, "token": token, "nickname": strings.Repeat("n", 31)},
		"31 with accents": {"roomId": roomID, "token": token, "nickname": strings.Repeat("é", 31)},
	} {
		resp, err := member.EnterRaw(ctx, body)
		require.NoError(t, err)
		require.Equal(t, http.StatusUnprocessableEntity, resp.Status, "%s; body: %s", name, resp.Body)
		var refusal struct {
			Error struct {
				Code   string            `json:"code"`
				Fields map[string]string `json:"fields"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(resp.Body, &refusal), "%s; body: %s", name, resp.Body)
		require.Equal(t, "invalid_nickname", refusal.Error.Code, name)
		require.NotEmpty(t, refusal.Error.Fields["nickname"], "%s: the field is named; body: %s", name, resp.Body)
		_, holds := member.Cookie(client.AudienceSessionCookie(roomID))
		require.False(t, holds, "%s: no session is given", name)
	}
	require.Equal(t, 0, audienceCount(t, ctx, director, roomID), "none of those took a seat")

	// Thirty characters is allowed, counted as characters and not bytes, and
	// spaces around a name are not part of it.
	resp, err := member.EnterAs(ctx, room.ShareLink, "  "+strings.Repeat("é", 30)+"  ")
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.Status, "body: %s", resp.Body)
	require.Equal(t, 1, audienceCount(t, ctx, director, roomID))
}

// The director's page shows who has joined as they join: the server sends
// the director's connection the audience when it connects and again each
// time it changes.
func TestTheDirectorIsToldAsPeopleJoin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	director, room, _ := newRoom(t, ctx)
	conn, err := director.ConnectRoom(ctx, room.RoomID)
	require.NoError(t, err)
	defer conn.Close()
	requireWaitingSnapshot(t, ctx, conn)

	audience := func(c *client.Conn, count int, nicknames ...string) {
		t.Helper()
		msg, err := c.Audience(ctx)
		require.NoError(t, err)
		require.Equal(t, count, msg.Count, "message: %s", msg.Raw)
		require.Equal(t, nicknames, msg.Nicknames, "in the order they joined; message: %s", msg.Raw)
		require.Contains(t, string(msg.Raw), `"nicknames":[`, "a list even when empty; message: %s", msg.Raw)
	}
	audience(conn, 0, []string{}...)

	ana := client.NewAudience(stack.BaseURL)
	resp, err := ana.EnterAs(ctx, room.ShareLink, "Ana")
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.Status, "body: %s", resp.Body)
	audience(conn, 1, "Ana")

	ben := client.NewAudience(stack.BaseURL)
	resp, err = ben.EnterAs(ctx, room.ShareLink, " Ben ")
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.Status, "body: %s", resp.Body)
	audience(conn, 2, "Ana", "Ben")

	// Ana reloads and types a different name: the same seat, a new name.
	resp, err = ana.EnterAs(ctx, room.ShareLink, "Anabel")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.Status, "body: %s", resp.Body)
	var entered client.EnterResponse
	require.NoError(t, resp.Decode(&entered))
	require.True(t, entered.Restored)
	audience(conn, 2, "Anabel", "Ben")
	require.Equal(t, 2, audienceCount(t, ctx, director, room.RoomID), "renaming takes no seat")

	// A director's page opened now is told the same without anyone joining.
	second, err := director.ConnectRoom(ctx, room.RoomID)
	require.NoError(t, err)
	defer second.Close()
	requireWaitingSnapshot(t, ctx, second)
	audience(second, 2, "Anabel", "Ben")
}

// Only the director is told. An audience member's page is not sent who
// else is there, or how many.
func TestTheAudienceIsNotToldWhoElseIsThere(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	director, room, _ := newRoom(t, ctx)
	first := enteredMember(t, ctx, room.ShareLink)
	conn, err := first.ConnectRoom(ctx, room.RoomID)
	require.NoError(t, err)
	defer conn.Close()
	requireWaitingSnapshot(t, ctx, conn)

	enteredMember(t, ctx, room.ShareLink) // someone else joins while they wait

	status, _ := start(t, ctx, director.Client, room.RoomID)
	require.Equal(t, http.StatusCreated, status)
	// Everything published after the join has arrived, and nothing else did.
	readThrough(t, ctx, conn, 0)
	require.Zero(t, conn.AudienceSeen(), "an audience member's connection is sent no audience message")
}
