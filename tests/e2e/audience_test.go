package e2e_test

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/tests/e2e/client"
	"github.com/conghan-tay/MonsterTheatre_01/tests/e2e/fixtures"
)

// newRoom creates a room and returns its director's browser and the room.
func newRoom(t *testing.T, ctx context.Context) (*client.DirectorClient, client.RoomView, client.RoomInput) {
	t.Helper()
	director := loggedInDirector(t, ctx)
	input := fixtures.SampleRoom(t)
	resp, err := director.CreateRoom(ctx, input)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.Status, "create room; body: %s", resp.Body)
	var room client.RoomView
	require.NoError(t, resp.Decode(&room))
	return director, room, input
}

// audienceCount reads the room's count from its director's view.
func audienceCount(t *testing.T, ctx context.Context, director *client.DirectorClient, roomID string) int {
	t.Helper()
	resp, err := director.GetRoom(ctx, roomID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.Status, "body: %s", resp.Body)
	var room client.RoomView
	require.NoError(t, resp.Decode(&room))
	return room.AudienceCount
}

func errorCode(t *testing.T, resp *client.Response) string {
	t.Helper()
	var body errorResponse
	require.NoError(t, resp.Decode(&body))
	return body.Error.Code
}

// H2: an audience member enters through the link and gets a session;
// re-entering with the same cookie restores it without using another slot.
func TestAudienceMemberEntersAndIsRestored(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	director, room, input := newRoom(t, ctx)
	require.Zero(t, audienceCount(t, ctx, director, room.RoomID))

	member := client.NewAudience(stack.BaseURL)
	resp, err := member.Enter(ctx, room.ShareLink)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.Status, "POST /api/v1/audience-sessions; body: %s", resp.Body)
	var entered client.EnterResponse
	require.NoError(t, resp.Decode(&entered))
	require.NotEmpty(t, entered.SessionID)
	require.Equal(t, room.RoomID, entered.RoomID)
	require.False(t, entered.Restored)
	require.Equal(t, input.Fields.Scene, entered.Room.Scene, "the response carries the room, so the page can render at once")
	require.Equal(t, "Grumble", entered.Room.CharacterA.Name)

	cookie, ok := member.Cookie(client.AudienceSessionCookie(room.RoomID))
	require.True(t, ok, "entering sets a session cookie named for the room")
	require.NotEmpty(t, cookie.Value)
	require.Equal(t, 1, audienceCount(t, ctx, director, room.RoomID))

	again, err := member.Enter(ctx, room.ShareLink)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, again.Status, "re-entering; body: %s", again.Body)
	var restored client.EnterResponse
	require.NoError(t, again.Decode(&restored))
	require.True(t, restored.Restored)
	require.Equal(t, entered.SessionID, restored.SessionID, "the same identity comes back")
	require.Equal(t, 1, audienceCount(t, ctx, director, room.RoomID), "and no second slot is used")
}

// Step 6: audience members can read their room and its portraits.
func TestAudienceMemberReadsTheRoomAndPortraits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, room, input := newRoom(t, ctx)
	member := client.NewAudience(stack.BaseURL)
	resp, err := member.Enter(ctx, room.ShareLink)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.Status, "body: %s", resp.Body)

	got, err := member.GetRoom(ctx, room.RoomID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, got.Status, "GET room as audience; body: %s", got.Body)
	var view client.RoomView
	require.NoError(t, got.Decode(&view))
	require.Equal(t, input.Fields.Scene, view.Scene)
	require.Equal(t, "Fizz", view.CharacterB.Name)
	require.NotContains(t, string(got.Body), "shareLink", "the audience view does not hand the link back out")
	require.NotContains(t, string(got.Body), "audienceCount")

	portrait, err := member.FetchPortrait(ctx, room.RoomID, "b")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, portrait.Status, "body: %s", portrait.Body)
	require.True(t, bytes.Equal(input.PortraitB.Content, portrait.Body))
}

// S4: the eleventh audience member is refused, even when they all arrive at once.
func TestRoomAdmitsExactlyTenEvenWhenTheyArriveTogether(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	director, room, _ := newRoom(t, ctx)

	const arriving = 15
	statuses := make([]int, arriving)
	codes := make([]string, arriving)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range arriving {
		wg.Add(1)
		go func() {
			defer wg.Done()
			member := client.NewAudience(stack.BaseURL)
			<-start
			resp, err := member.Enter(ctx, room.ShareLink)
			if err != nil {
				codes[i] = err.Error()
				return
			}
			statuses[i] = resp.Status
			if resp.Status == http.StatusConflict {
				var body errorResponse
				if resp.Decode(&body) == nil {
					codes[i] = body.Error.Code
				}
			}
		}()
	}
	close(start)
	wg.Wait()

	admitted, refused := 0, 0
	for i, status := range statuses {
		switch status {
		case http.StatusCreated:
			admitted++
		case http.StatusConflict:
			refused++
			require.Equal(t, "room_full", codes[i])
		default:
			t.Fatalf("browser %d: unexpected status %d (%s)", i, status, codes[i])
		}
	}
	require.Equal(t, 10, admitted, "exactly ten are admitted")
	require.Equal(t, 5, refused, "and the rest are refused")
	require.Equal(t, 10, audienceCount(t, ctx, director, room.RoomID))

	late := client.NewAudience(stack.BaseURL)
	resp, err := late.Enter(ctx, room.ShareLink)
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, resp.Status, "a later arrival is refused too; body: %s", resp.Body)
	require.Equal(t, "room_full", errorCode(t, resp))
	_, hasCookie := late.Cookie(client.AudienceSessionCookie(room.RoomID))
	require.False(t, hasCookie)
	require.Equal(t, 10, audienceCount(t, ctx, director, room.RoomID), "a refusal does not move the count")
}

// S4: a bad share link is refused.
func TestBadShareLinkIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	director, room, _ := newRoom(t, ctx)
	_, otherRoom, _ := newRoom(t, ctx)
	roomID, token, err := client.ParseShareLink(room.ShareLink)
	require.NoError(t, err)
	_, otherToken, err := client.ParseShareLink(otherRoom.ShareLink)
	require.NoError(t, err)

	for name, link := range map[string][2]string{
		"wrong token":              {roomID, token[:len(token)-1] + "x"},
		"empty token":              {roomID, ""},
		"token for another room":   {roomID, otherToken},
		"malformed room id":        {"not-a-room-id", token},
		"unknown room, some token": {"00000000-0000-4000-8000-000000000000", token},
	} {
		member := client.NewAudience(stack.BaseURL)
		resp, err := member.EnterWith(ctx, link[0], link[1])
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, resp.Status, "%s; body: %s", name, resp.Body)
		require.Equal(t, "invalid_share_link", errorCode(t, resp), name)
		_, hasCookie := member.Cookie(client.AudienceSessionCookie(link[0]))
		require.False(t, hasCookie, "%s sets no cookie", name)
	}
	require.Zero(t, audienceCount(t, ctx, director, room.RoomID), "none of them took a slot")
}

// Sessions are scoped to their room and grant nothing a director has.
func TestAudienceSessionIsScopedToItsRoom(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, roomA, _ := newRoom(t, ctx)
	_, roomB, _ := newRoom(t, ctx)

	member := client.NewAudience(stack.BaseURL)
	resp, err := member.Enter(ctx, roomA.ShareLink)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.Status, "body: %s", resp.Body)

	for what, get := range map[string]func() (*client.Response, error){
		"room B":            func() (*client.Response, error) { return member.GetRoom(ctx, roomB.RoomID) },
		"room B's portrait": func() (*client.Response, error) { return member.FetchPortrait(ctx, roomB.RoomID, "a") },
		"the room list":     func() (*client.Response, error) { return member.Get(ctx, "/api/v1/rooms") },
		"the director's me": func() (*client.Response, error) { return member.Get(ctx, "/api/v1/director/me") },
	} {
		resp, err := get()
		require.NoError(t, err)
		require.Equal(t, http.StatusUnauthorized, resp.Status, "a room A session reading %s; body: %s", what, resp.Body)
	}

	// The same browser can attend a second room; each has its own cookie.
	resp, err = member.Enter(ctx, roomB.ShareLink)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.Status, "body: %s", resp.Body)
	for _, id := range []string{roomA.RoomID, roomB.RoomID} {
		got, err := member.GetRoom(ctx, id)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, got.Status, "room %s after joining both; body: %s", id, got.Body)
	}

	forged := client.NewAudience(stack.BaseURL)
	forged.SetCookie(client.AudienceSessionCookie(roomA.RoomID), "made-up-value")
	got, err := forged.GetRoom(ctx, roomA.RoomID)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, got.Status, "made-up cookie; body: %s", got.Body)

	// A room A cookie value presented under room B's cookie name opens nothing.
	cookieA, _ := member.Cookie(client.AudienceSessionCookie(roomA.RoomID))
	swapped := client.NewAudience(stack.BaseURL)
	swapped.SetCookie(client.AudienceSessionCookie(roomB.RoomID), cookieA.Value)
	got, err = swapped.GetRoom(ctx, roomB.RoomID)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, got.Status, "room A's secret under room B's name; body: %s", got.Body)
}
