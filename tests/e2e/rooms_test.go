package e2e_test

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/tests/e2e/client"
	"github.com/conghan-tay/MonsterTheatre_01/tests/e2e/fixtures"
	"github.com/conghan-tay/MonsterTheatre_01/tests/e2e/harness"
)

type validationError struct {
	Error struct {
		Code   string            `json:"code"`
		Fields map[string]string `json:"fields"`
	} `json:"error"`
}

// loggedInDirector creates an account and returns a browser logged in as it.
func loggedInDirector(t *testing.T, ctx context.Context) *client.DirectorClient {
	t.Helper()
	account := fixtures.NewDirector(t, stack)
	director := client.NewDirector(stack.BaseURL)
	resp, err := director.Login(ctx, account.Username, account.Password)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.Status, "login; body: %s", resp.Body)
	return director
}

func listRooms(t *testing.T, ctx context.Context, director *client.DirectorClient) []client.RoomSummary {
	t.Helper()
	resp, err := director.ListRooms(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.Status, "GET /api/v1/rooms; body: %s", resp.Body)
	var body struct {
		Rooms []client.RoomSummary `json:"rooms"`
	}
	require.NoError(t, resp.Decode(&body))
	return body.Rooms
}

// H1: a director creates a room with two portraits, gets a share link, and
// can fetch the portraits back.
func TestDirectorCreatesARoom(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	director := loggedInDirector(t, ctx)
	input := fixtures.SampleRoom(t)

	resp, err := director.CreateRoom(ctx, input)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.Status, "POST /api/v1/rooms; body: %s", resp.Body)
	var created client.RoomView
	require.NoError(t, resp.Decode(&created))
	require.NotEmpty(t, created.RoomID)
	require.True(t, strings.HasPrefix(created.ShareLink, harness.PublicBaseURL+"/join/"+created.RoomID+"/"),
		"share link %q points at the join page for this room", created.ShareLink)

	got, err := director.GetRoom(ctx, created.RoomID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, got.Status, "GET room; body: %s", got.Body)
	var room client.RoomView
	require.NoError(t, got.Decode(&room))
	require.Equal(t, input.Fields.Scene, room.Scene)
	require.Equal(t, "Grumble", room.CharacterA.Name)
	require.Equal(t, input.Fields.CharacterA.Brief, room.CharacterA.Brief)
	require.Equal(t, "Fizz", room.CharacterB.Name)
	require.Equal(t, created.ShareLink, room.ShareLink, "the share link is the same every time it is read")

	for slot, sent := range map[string]*client.File{"a": input.PortraitA, "b": input.PortraitB} {
		portrait, err := director.FetchPortrait(ctx, created.RoomID, slot)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, portrait.Status, "portrait %s; body: %s", slot, portrait.Body)
		require.Equal(t, "image/png", portrait.Header.Get("Content-Type"))
		require.True(t, bytes.Equal(sent.Content, portrait.Body), "portrait %s comes back byte-identical", slot)
	}
	require.Equal(t, "/api/v1/rooms/"+created.RoomID+"/portraits/a", room.CharacterA.PortraitURL)

	rooms := listRooms(t, ctx, director)
	require.Len(t, rooms, 1)
	require.Equal(t, created.RoomID, rooms[0].RoomID)
	require.Equal(t, input.Fields.Scene.Topic, rooms[0].Topic)
}

// S2: invalid input is refused with a clear error and creates nothing.
func TestInvalidRoomInputIsRefusedAndCreatesNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	director := loggedInDirector(t, ctx)

	cases := []struct {
		name   string
		break_ func(*client.RoomInput)
		status int
		field  string
	}{
		{"missing portrait", func(in *client.RoomInput) { in.PortraitB = nil }, http.StatusUnprocessableEntity, "portraitB"},
		{"file that is not an image", func(in *client.RoomInput) {
			in.PortraitA = &client.File{Name: "notes.png", ContentType: "image/png", Content: []byte("this is plain text, whatever the name says")}
		}, http.StatusUnprocessableEntity, "portraitA"},
		{"over-long character name", func(in *client.RoomInput) {
			in.Fields.CharacterA.Name = strings.Repeat("x", 81)
		}, http.StatusUnprocessableEntity, "characterA.name"},
		{"missing scene topic", func(in *client.RoomInput) { in.Fields.Scene.Topic = "   " }, http.StatusUnprocessableEntity, "scene.topic"},
		{"oversize portrait", func(in *client.RoomInput) {
			in.PortraitA = &client.File{Name: "huge.png", ContentType: "image/png", Content: make([]byte, 5<<20+1)}
		}, http.StatusRequestEntityTooLarge, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := fixtures.SampleRoom(t)
			tc.break_(&input)

			resp, err := director.CreateRoom(ctx, input)
			require.NoError(t, err)
			require.Equal(t, tc.status, resp.Status, "body: %s", resp.Body)
			if tc.field != "" {
				var body validationError
				require.NoError(t, resp.Decode(&body))
				require.Equal(t, "invalid_room", body.Error.Code)
				require.Contains(t, body.Error.Fields, tc.field, "the error names the field; body: %s", resp.Body)
			}
		})
	}

	require.Empty(t, listRooms(t, ctx, director), "none of the refused submissions created a room")
}

// S3 (read part): a director cannot read another director's room.
func TestAnotherDirectorCannotReadARoom(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	owner := loggedInDirector(t, ctx)
	resp, err := owner.CreateRoom(ctx, fixtures.SampleRoom(t))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.Status, "body: %s", resp.Body)
	var room client.RoomView
	require.NoError(t, resp.Decode(&room))

	other := loggedInDirector(t, ctx)
	for what, get := range map[string]func() (*client.Response, error){
		"room":     func() (*client.Response, error) { return other.GetRoom(ctx, room.RoomID) },
		"portrait": func() (*client.Response, error) { return other.FetchPortrait(ctx, room.RoomID, "a") },
	} {
		resp, err := get()
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, resp.Status, "%s read by another director; body: %s", what, resp.Body)
	}
	require.Empty(t, listRooms(t, ctx, other), "and it does not appear in their list")

	unknown, err := owner.GetRoom(ctx, "00000000-0000-4000-8000-000000000000")
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, unknown.Status, "unknown room; body: %s", unknown.Body)
	malformed, err := owner.GetRoom(ctx, "not-a-room-id")
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, malformed.Status, "malformed room id; body: %s", malformed.Body)

	anonymous := client.NewDirector(stack.BaseURL)
	for what, get := range map[string]func() (*client.Response, error){
		"room":     func() (*client.Response, error) { return anonymous.GetRoom(ctx, room.RoomID) },
		"portrait": func() (*client.Response, error) { return anonymous.FetchPortrait(ctx, room.RoomID, "a") },
		"list":     func() (*client.Response, error) { return anonymous.ListRooms(ctx) },
		"create":   func() (*client.Response, error) { return anonymous.CreateRoom(ctx, fixtures.SampleRoom(t)) },
	} {
		resp, err := get()
		require.NoError(t, err)
		require.Equal(t, http.StatusUnauthorized, resp.Status, "%s without logging in; body: %s", what, resp.Body)
	}
}
