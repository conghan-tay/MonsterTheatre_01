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
