package audience

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/auth"
)

const (
	roomA = "11111111-1111-4111-8111-111111111111"
	roomB = "22222222-2222-4222-8222-222222222222"
)

type memStore struct {
	sessions map[string]Session // by string(hash)
}

func (m *memStore) Admit(context.Context, string, []byte, time.Time, time.Time) (string, error) {
	panic("not used by these tests")
}
func (m *memStore) Touch(context.Context, string, time.Time) error { return nil }
func (m *memStore) SessionByHash(_ context.Context, hash []byte) (Session, error) {
	s, ok := m.sessions[string(hash)]
	if !ok {
		return s, ErrNotFound
	}
	return s, nil
}

func requestWith(cookieName, value string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if cookieName != "" {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: value})
	}
	return r
}

func newService(now time.Time, secret string, session Session) *Service {
	return &Service{
		Store: &memStore{sessions: map[string]Session{string(auth.HashToken(secret)): session}},
		Now:   func() time.Time { return now },
	}
}

func TestSessionForRecognisesALiveSessionForThatRoom(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	svc := newService(now, "secret-a", Session{ID: "s-1", RoomID: roomA, ExpiresAt: now.Add(time.Hour)})

	session, ok, err := svc.SessionFor(requestWith(CookieName(roomA), "secret-a"), roomA)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "s-1", session.ID)

	id, member, err := svc.MemberID(requestWith(CookieName(roomA), "secret-a"), roomA)
	require.NoError(t, err)
	require.True(t, member)
	require.Equal(t, "s-1", id)
}

func TestSessionForRefusesEverythingElse(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	svc := newService(now, "secret-a", Session{ID: "s-1", RoomID: roomA, ExpiresAt: now.Add(time.Hour)})

	for name, r := range map[string]*http.Request{
		"no cookie":                  requestWith("", ""),
		"empty cookie":               requestWith(CookieName(roomA), ""),
		"unknown secret":             requestWith(CookieName(roomA), "made-up"),
		"right secret, other cookie": requestWith(CookieName(roomB), "secret-a"),
		"director cookie name":       requestWith(auth.SessionCookie, "secret-a"),
	} {
		_, ok, err := svc.SessionFor(r, roomA)
		require.NoError(t, err, name)
		require.False(t, ok, name)
	}

	// Room A's secret presented under room B's cookie name, asking for room B:
	// the cookie is found, but the stored session belongs to room A.
	_, ok, err := svc.SessionFor(requestWith(CookieName(roomB), "secret-a"), roomB)
	require.NoError(t, err)
	require.False(t, ok, "a session never opens a room other than its own")
}

func TestSessionForRefusesAnExpiredSession(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	r := requestWith(CookieName(roomA), "secret-a")

	justValid := newService(now, "secret-a", Session{ID: "s-1", RoomID: roomA, ExpiresAt: now.Add(time.Second)})
	_, ok, _ := justValid.SessionFor(r, roomA)
	require.True(t, ok)

	expired := newService(now, "secret-a", Session{ID: "s-1", RoomID: roomA, ExpiresAt: now})
	_, ok, err := expired.SessionFor(r, roomA)
	require.NoError(t, err)
	require.False(t, ok, "expired exactly at the expiry time, so it is not restored")
}

func TestCookieNameIsPerRoom(t *testing.T) {
	require.Equal(t, "mt_session_"+roomA, CookieName(roomA))
	require.NotEqual(t, CookieName(roomA), CookieName(roomB))
}
