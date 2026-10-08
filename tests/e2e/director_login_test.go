package e2e_test

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/tests/e2e/client"
	"github.com/conghan-tay/MonsterTheatre_01/tests/e2e/fixtures"
)

type meResponse struct {
	DirectorID string `json:"directorId"`
	Username   string `json:"username"`
}

type errorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// H1 (login part): a director logs in and holds a session.
func TestDirectorLogsIn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	account := fixtures.NewDirector(t, stack)
	director := client.NewDirector(stack.BaseURL)

	resp, err := director.Login(ctx, account.Username, account.Password)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.Status,
		"POST /api/v1/director/login with valid credentials; body: %s", resp.Body)

	cookie, ok := director.Cookie(client.DirectorSessionCookie)
	require.True(t, ok, "login sets the %s cookie", client.DirectorSessionCookie)
	require.NotEmpty(t, cookie.Value)

	me, err := director.Me(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, me.Status, "GET /api/v1/director/me after login; body: %s", me.Body)
	var who meResponse
	require.NoError(t, me.Decode(&who))
	require.Equal(t, account.Username, who.Username)
	require.NotEmpty(t, who.DirectorID)
}

// S1: wrong password and unknown user are refused identically.
func TestLoginWithBadCredentialsIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	account := fixtures.NewDirector(t, stack)

	wrongPassword := client.NewDirector(stack.BaseURL)
	resp, err := wrongPassword.Login(ctx, account.Username, "not-the-password")
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, resp.Status, "body: %s", resp.Body)
	_, hasCookie := wrongPassword.Cookie(client.DirectorSessionCookie)
	require.False(t, hasCookie, "a failed login sets no session cookie")

	unknownUser := client.NewDirector(stack.BaseURL)
	resp2, err := unknownUser.Login(ctx, "nobody-"+account.Username, account.Password)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, resp2.Status, "body: %s", resp2.Body)

	require.JSONEq(t, string(resp.Body), string(resp2.Body),
		"the two failures are indistinguishable, so the endpoint does not reveal which usernames exist")
	var body errorResponse
	require.NoError(t, resp.Decode(&body))
	require.Equal(t, "invalid_credentials", body.Error.Code)
}

// S1: repeated failures lock the username for a while, even for the right password.
func TestRepeatedLoginFailuresAreThrottled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	account := fixtures.NewDirector(t, stack)
	director := client.NewDirector(stack.BaseURL)

	for attempt := 1; attempt <= 5; attempt++ {
		resp, err := director.Login(ctx, account.Username, "wrong-"+strconv.Itoa(attempt))
		require.NoError(t, err)
		require.Equal(t, http.StatusUnauthorized, resp.Status, "failed attempt %d; body: %s", attempt, resp.Body)
	}

	resp, err := director.Login(ctx, account.Username, account.Password)
	require.NoError(t, err)
	require.Equal(t, http.StatusTooManyRequests, resp.Status,
		"the sixth attempt is throttled even though the password is right; body: %s", resp.Body)
	retryAfter, err := strconv.Atoi(resp.Header.Get("Retry-After"))
	require.NoError(t, err, "Retry-After is a number of seconds")
	require.Positive(t, retryAfter)
	_, hasCookie := director.Cookie(client.DirectorSessionCookie)
	require.False(t, hasCookie)

	other := fixtures.NewDirector(t, stack)
	resp, err = client.NewDirector(stack.BaseURL).Login(ctx, other.Username, other.Password)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.Status, "another director is unaffected; body: %s", resp.Body)
}

// Definition of done: a director route without a valid cookie returns 401.
func TestDirectorRoutesNeedAValidSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	anonymous := client.NewDirector(stack.BaseURL)
	resp, err := anonymous.Me(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, resp.Status, "no cookie; body: %s", resp.Body)

	forged := client.NewDirector(stack.BaseURL)
	forged.SetCookie(client.DirectorSessionCookie, "made-up-value")
	resp, err = forged.Me(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, resp.Status, "made-up cookie; body: %s", resp.Body)

	account := fixtures.NewDirector(t, stack)
	director := client.NewDirector(stack.BaseURL)
	resp, err = director.Login(ctx, account.Username, account.Password)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.Status, "body: %s", resp.Body)
	session, _ := director.Cookie(client.DirectorSessionCookie)

	resp, err = director.Logout(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, resp.Status, "body: %s", resp.Body)
	resp, err = director.Me(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, resp.Status, "after logout; body: %s", resp.Body)

	// The old secret must be dead on the server, not just forgotten by the browser.
	replay := client.NewDirector(stack.BaseURL)
	replay.SetCookie(client.DirectorSessionCookie, session.Value)
	resp, err = replay.Me(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, resp.Status, "replaying the logged-out cookie; body: %s", resp.Body)
}
