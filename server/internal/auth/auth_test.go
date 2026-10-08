package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// --- password and token ---

func TestPasswordHashVerifiesOnlyTheSamePassword(t *testing.T) {
	hash, err := HashPassword("correct horse")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$"), hash)

	ok, err := VerifyPassword("correct horse", hash)
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = VerifyPassword("wrong horse", hash)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestSamePasswordHashesDifferentlyEachTime(t *testing.T) {
	a, _ := HashPassword("same")
	b, _ := HashPassword("same")
	require.NotEqual(t, a, b, "each hash has its own salt")
}

func TestVerifyPasswordRejectsMalformedHashes(t *testing.T) {
	for _, bad := range []string{"", "plaintext", "$bcrypt$x$y$z$w", "$argon2id$v=19$m=1$salt$hash", "$argon2id$v=19$m=19456,t=2,p=1$!!!$!!!"} {
		ok, err := VerifyPassword("x", bad)
		require.Error(t, err, bad)
		require.False(t, ok, bad)
	}
}

func TestTokenHashIsStableAndTokensAreUnique(t *testing.T) {
	token1, hash1, err := NewToken()
	require.NoError(t, err)
	token2, _, _ := NewToken()

	require.NotEqual(t, token1, token2)
	require.Len(t, hash1, 32)
	require.Equal(t, hash1, HashToken(token1))
	require.GreaterOrEqual(t, len(token1), 43, "32 random bytes, base64url")
}

// --- throttle ---

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

func newTestThrottle(clock *fakeClock) *Throttle {
	return &Throttle{MaxFailures: 5, Window: 15 * time.Minute, Now: clock.Now}
}

func TestThrottleLocksAfterMaxFailuresUntilTheWindowPasses(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)}
	th := newTestThrottle(clock)

	for range 4 {
		th.Fail("alice")
	}
	require.Zero(t, th.RetryAfter("alice"), "four failures do not lock")

	clock.advance(time.Minute)
	th.Fail("alice")
	require.Equal(t, 14*time.Minute, th.RetryAfter("alice"), "locked until 15 minutes after the first failure")
	require.Zero(t, th.RetryAfter("bob"), "other usernames are unaffected")

	clock.advance(14 * time.Minute)
	require.Zero(t, th.RetryAfter("alice"), "the lock ends when the window passes")

	th.Fail("alice")
	require.Zero(t, th.RetryAfter("alice"), "and the count starts again from one")
}

func TestThrottleResetClearsTheCount(t *testing.T) {
	clock := &fakeClock{now: time.Now()}
	th := newTestThrottle(clock)
	for range 4 {
		th.Fail("alice")
	}
	th.Reset("alice")
	for range 4 {
		th.Fail("alice")
	}
	require.Zero(t, th.RetryAfter("alice"))
}

// --- handlers and middleware, against an in-memory store ---

type memStore struct {
	directors map[string]Director // by username
	sessions  map[string]Session  // by string(hash)
}

func (m *memStore) DirectorByUsername(_ context.Context, username string) (Director, error) {
	d, ok := m.directors[NormalizeUsername(username)]
	if !ok {
		return d, ErrNotFound
	}
	return d, nil
}

func (m *memStore) CreateSession(_ context.Context, directorID string, hash []byte, _, expiresAt time.Time) error {
	for _, d := range m.directors {
		if d.ID == directorID {
			m.sessions[string(hash)] = Session{DirectorID: d.ID, Username: d.Username, ExpiresAt: expiresAt}
		}
	}
	return nil
}

func (m *memStore) SessionByHash(_ context.Context, hash []byte) (Session, error) {
	s, ok := m.sessions[string(hash)]
	if !ok {
		return s, ErrNotFound
	}
	return s, nil
}

func (m *memStore) DeleteSession(_ context.Context, hash []byte) error {
	delete(m.sessions, string(hash))
	return nil
}

type fixture struct {
	svc   *Service
	store *memStore
	clock *fakeClock
	mux   *http.ServeMux
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	hash, err := HashPassword("s3cret-pass")
	require.NoError(t, err)
	store := &memStore{
		directors: map[string]Director{"alice": {ID: "d-1", Username: "alice", PasswordHash: hash}},
		sessions:  map[string]Session{},
	}
	clock := &fakeClock{now: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)}
	svc := &Service{
		Store:        store,
		Throttle:     newTestThrottle(clock),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:          clock.Now,
		CookieSecure: true,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login", svc.Login)
	mux.HandleFunc("POST /logout", svc.Logout)
	mux.Handle("GET /me", svc.RequireDirector(http.HandlerFunc(svc.Me)))
	return &fixture{svc: svc, store: store, clock: clock, mux: mux}
}

func (f *fixture) login(username, password string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(loginRequest{Username: username, Password: password})
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec
}

func (f *fixture) do(method, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie {
			return c
		}
	}
	t.Fatalf("no %s cookie in response", SessionCookie)
	return nil
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	return body.Error.Code
}

func TestLoginSetsAHardenedCookieAndStoresOnlyItsHash(t *testing.T) {
	f := newFixture(t)

	rec := f.login("  Alice ", "s3cret-pass") // username is normalised
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"directorId":"d-1","username":"alice"}`, rec.Body.String())

	cookie := sessionCookie(t, rec)
	require.True(t, cookie.HttpOnly)
	require.True(t, cookie.Secure)
	require.Equal(t, http.SameSiteLaxMode, cookie.SameSite)
	require.Equal(t, "/", cookie.Path)
	require.Equal(t, int(SessionTTL.Seconds()), cookie.MaxAge)

	require.Len(t, f.store.sessions, 1)
	_, storedRaw := f.store.sessions[cookie.Value]
	require.False(t, storedRaw, "the secret itself is never stored")
	stored, ok := f.store.sessions[string(HashToken(cookie.Value))]
	require.True(t, ok, "its hash is")
	require.Equal(t, f.clock.now.Add(SessionTTL), stored.ExpiresAt)
}

func TestLoginRefusesWrongPasswordAndUnknownUserIdentically(t *testing.T) {
	f := newFixture(t)

	wrong := f.login("alice", "nope")
	unknown := f.login("mallory", "s3cret-pass")

	require.Equal(t, http.StatusUnauthorized, wrong.Code)
	require.Equal(t, http.StatusUnauthorized, unknown.Code)
	require.Equal(t, wrong.Body.String(), unknown.Body.String())
	require.Equal(t, "invalid_credentials", errorCode(t, wrong))
	require.Empty(t, wrong.Result().Cookies())
	require.Empty(t, f.store.sessions)
}

func TestLoginIsThrottledAfterFiveFailuresEvenWithTheRightPassword(t *testing.T) {
	f := newFixture(t)
	for range 5 {
		require.Equal(t, http.StatusUnauthorized, f.login("alice", "nope").Code)
	}

	rec := f.login("alice", "s3cret-pass")
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Equal(t, "900", rec.Header().Get("Retry-After"))
	require.Equal(t, "too_many_attempts", errorCode(t, rec))

	f.clock.advance(15 * time.Minute)
	require.Equal(t, http.StatusOK, f.login("alice", "s3cret-pass").Code, "the lock expires")
}

func TestSuccessfulLoginClearsEarlierFailures(t *testing.T) {
	f := newFixture(t)
	for range 4 {
		f.login("alice", "nope")
	}
	require.Equal(t, http.StatusOK, f.login("alice", "s3cret-pass").Code)
	for range 4 {
		f.login("alice", "nope")
	}
	require.Equal(t, http.StatusOK, f.login("alice", "s3cret-pass").Code, "the count restarted, so this is not throttled")
}

func TestLoginRejectsBodiesThatAreNotJSONOrAreIncomplete(t *testing.T) {
	f := newFixture(t)

	form := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username=alice&password=s3cret-pass"))
	form.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, form)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "a cross-site form post is not accepted")

	require.Equal(t, http.StatusUnprocessableEntity, f.login("alice", "").Code)
	require.Equal(t, http.StatusUnprocessableEntity, f.login("", "s3cret-pass").Code)
	require.Zero(t, f.svc.Throttle.RetryAfter("alice"), "malformed requests do not count as failed logins")
}

func TestRequireDirectorAdmitsOnlyALiveSession(t *testing.T) {
	f := newFixture(t)
	cookie := sessionCookie(t, f.login("alice", "s3cret-pass"))

	ok := f.do(http.MethodGet, "/me", cookie)
	require.Equal(t, http.StatusOK, ok.Code)
	require.JSONEq(t, `{"directorId":"d-1","username":"alice"}`, ok.Body.String())

	require.Equal(t, http.StatusUnauthorized, f.do(http.MethodGet, "/me", nil).Code, "no cookie")
	require.Equal(t, http.StatusUnauthorized,
		f.do(http.MethodGet, "/me", &http.Cookie{Name: SessionCookie, Value: "made-up"}).Code, "unknown cookie")
	require.Equal(t, "not_logged_in", errorCode(t, f.do(http.MethodGet, "/me", nil)))

	f.clock.advance(SessionTTL - time.Second)
	require.Equal(t, http.StatusOK, f.do(http.MethodGet, "/me", cookie).Code, "still valid just before expiry")
	f.clock.advance(time.Second)
	require.Equal(t, http.StatusUnauthorized, f.do(http.MethodGet, "/me", cookie).Code, "expired exactly at the TTL")
}

func TestLogoutDeletesTheSessionAndClearsTheCookie(t *testing.T) {
	f := newFixture(t)
	cookie := sessionCookie(t, f.login("alice", "s3cret-pass"))

	rec := f.do(http.MethodPost, "/logout", cookie)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Negative(t, sessionCookie(t, rec).MaxAge, "the browser is told to drop the cookie")
	require.Empty(t, f.store.sessions, "the session is gone on the server")
	require.Equal(t, http.StatusUnauthorized, f.do(http.MethodGet, "/me", cookie).Code, "the old cookie no longer works")

	require.Equal(t, http.StatusNoContent, f.do(http.MethodPost, "/logout", nil).Code, "logging out twice is fine")
}
