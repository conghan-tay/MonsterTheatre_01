package client

import "context"

// DirectorSessionCookie is the cookie a successful login sets.
const DirectorSessionCookie = "mt_director_session"

// DirectorClient is a director's browser.
type DirectorClient struct {
	*Client
}

// NewDirector returns a director's browser with no session yet.
func NewDirector(baseURL string) *DirectorClient {
	return &DirectorClient{Client: New(baseURL)}
}

// Login posts the credentials. On success the session cookie is kept in the
// client's jar and sent with every later request.
func (d *DirectorClient) Login(ctx context.Context, username, password string) (*Response, error) {
	return d.PostJSON(ctx, "/api/v1/director/login", map[string]string{
		"username": username,
		"password": password,
	})
}

// Me asks the server who this browser is logged in as.
func (d *DirectorClient) Me(ctx context.Context) (*Response, error) {
	return d.Get(ctx, "/api/v1/director/me")
}

// Logout ends the session.
func (d *DirectorClient) Logout(ctx context.Context) (*Response, error) {
	return d.PostJSON(ctx, "/api/v1/director/logout", struct{}{})
}

// StartResponse is the body of a Start.
type StartResponse struct {
	PerformanceID string `json:"performanceId"`
	RoomID        string `json:"roomId"`
	Status        string `json:"status"`
}

// Start is the Start button: it begins the room's one performance. Pressing
// it again returns the same performance.
func (c *Client) Start(ctx context.Context, roomID string) (*Response, error) {
	return c.PostJSON(ctx, "/api/v1/rooms/"+roomID+"/performance", struct{}{})
}
