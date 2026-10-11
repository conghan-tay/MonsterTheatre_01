package client

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// AudienceClient is an audience member's browser.
type AudienceClient struct {
	*Client
	// Nickname is what this person types before entering. It has a default
	// because most tests do not care what anyone is called.
	Nickname string
}

// NewAudience returns an audience member's browser with no session yet.
func NewAudience(baseURL string) *AudienceClient {
	return &AudienceClient{Client: New(baseURL), Nickname: "Guest"}
}

// AudienceSessionCookie is the name of the session cookie for a room. It is
// per room, so one browser can attend several.
func AudienceSessionCookie(roomID string) string {
	return "mt_session_" + roomID
}

// EnterResponse is the body of a successful Enter.
type EnterResponse struct {
	SessionID string   `json:"sessionId"`
	RoomID    string   `json:"roomId"`
	Restored  bool     `json:"restored"`
	Room      RoomView `json:"room"`
}

// ParseShareLink splits a share link, ".../join/{roomId}/{token}", into its
// room ID and token, as the join page will.
func ParseShareLink(link string) (roomID, token string, err error) {
	u, err := url.Parse(link)
	if err != nil {
		return "", "", err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "join" {
		return "", "", fmt.Errorf("not a share link: %q", link)
	}
	return parts[1], parts[2], nil
}

// Enter is the "Enter theatre" click: it presents the share link and, on
// success, holds a session cookie for the room.
func (a *AudienceClient) Enter(ctx context.Context, shareLink string) (*Response, error) {
	roomID, token, err := ParseShareLink(shareLink)
	if err != nil {
		return nil, err
	}
	return a.EnterWith(ctx, roomID, token)
}

// EnterAs is Enter under a given nickname. Entering again as someone who is
// already in the room keeps their seat and changes their name.
func (a *AudienceClient) EnterAs(ctx context.Context, shareLink, nickname string) (*Response, error) {
	a.Nickname = nickname
	return a.Enter(ctx, shareLink)
}

// EnterWith presents a room ID and token directly, for testing bad links.
func (a *AudienceClient) EnterWith(ctx context.Context, roomID, token string) (*Response, error) {
	return a.PostJSON(ctx, "/api/v1/audience-sessions", map[string]string{
		"roomId":   roomID,
		"token":    token,
		"nickname": a.Nickname,
	})
}

// EnterRaw posts exactly the given body, for testing what the server makes
// of a nickname that is missing or not allowed.
func (a *AudienceClient) EnterRaw(ctx context.Context, body map[string]any) (*Response, error) {
	return a.PostJSON(ctx, "/api/v1/audience-sessions", body)
}
