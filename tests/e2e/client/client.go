// Package client does what a browser does: HTTP requests with a cookie jar,
// and a WebSocket. Tests use it so they read as user actions.
//
// Methods are added in the step that first needs them. Each client has its
// own cookie jar, so ten audience members are ten clients.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"
)

// Client is one browser.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// New returns a client with an empty cookie jar.
func New(baseURL string) *Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		panic(err) // cannot happen with nil options
	}
	return &Client{
		BaseURL: baseURL,
		HTTP:    &http.Client{Jar: jar, Timeout: 15 * time.Second},
	}
}

// Response is a completed HTTP response with its body read.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Decode unmarshals the JSON body.
func (r *Response) Decode(v any) error {
	if err := json.Unmarshal(r.Body, v); err != nil {
		return fmt.Errorf("decode response (status %d, body %q): %w", r.Status, r.Body, err)
	}
	return nil
}

// Cookie returns the named cookie this client holds for the server, if any.
func (c *Client) Cookie(name string) (*http.Cookie, bool) {
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return nil, false
	}
	for _, cookie := range c.HTTP.Jar.Cookies(u) {
		if cookie.Name == name {
			return cookie, true
		}
	}
	return nil, false
}

// Get sends a GET request.
func (c *Client) Get(ctx context.Context, path string) (*Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	return c.do(req)
}

// PostJSON sends body as JSON.
func (c *Client) PostJSON(ctx context.Context, path string, body any) (*Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req)
}

func (c *Client) do(req *http.Request) (*Response, error) {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return &Response{Status: resp.StatusCode, Header: resp.Header, Body: body}, nil
}
