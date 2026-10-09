package auth

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/httpapi"
)

const (
	// SessionCookie is the director session cookie's name.
	SessionCookie = "mt_director_session"
	// SessionTTL is how long a login lasts (docs/decisions.md, D030).
	SessionTTL = 7 * 24 * time.Hour

	maxLoginBodyBytes = 4 << 10
)

// Service serves the director login endpoints and guards director routes.
type Service struct {
	Store    Store
	Throttle *Throttle
	Logger   *slog.Logger
	Now      func() time.Time
	// CookieSecure marks the session cookie HTTPS-only. It is off only for
	// local development and tests, which run over plain HTTP.
	CookieSecure bool
}

// Identity is the logged-in director, as established by RequireDirector.
type Identity struct {
	DirectorID string `json:"directorId"`
	Username   string `json:"username"`
}

type identityKey struct{}

// WithDirector returns ctx carrying a verified director. RequireDirector is
// the only caller outside tests.
func WithDirector(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, identity)
}

// DirectorFrom returns the director RequireDirector verified for this request.
func DirectorFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Login handles POST /api/v1/director/login.
func (s *Service) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := httpapi.DecodeJSON(w, r, &req, maxLoginBodyBytes); err != nil {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "invalid_request", "Send a JSON body with username and password.")
		return
	}
	username := NormalizeUsername(req.Username)
	if username == "" || req.Password == "" {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "invalid_request", "Username and password are required.")
		return
	}

	// Checked before the password, so a locked username is refused even with
	// the right password and costs no hashing.
	if wait := s.Throttle.RetryAfter(username); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		httpapi.WriteError(w, http.StatusTooManyRequests, "too_many_attempts", "Too many failed attempts. Try again later.")
		return
	}

	director, err := s.Store.DirectorByUsername(r.Context(), username)
	switch {
	case errors.Is(err, ErrNotFound):
		// Spend the same effort as a real check, so response time does not
		// reveal whether the username exists.
		_, _ = VerifyPassword(req.Password, dummyHash())
		s.refuse(w, username)
		return
	case err != nil:
		s.internalError(w, "look up director", err)
		return
	}

	ok, err := VerifyPassword(req.Password, director.PasswordHash)
	if err != nil {
		s.internalError(w, "verify password", err)
		return
	}
	if !ok {
		s.refuse(w, username)
		return
	}

	token, hash, err := NewToken()
	if err != nil {
		s.internalError(w, "create session token", err)
		return
	}
	now := s.Now()
	if err := s.Store.CreateSession(r.Context(), director.ID, hash, now, now.Add(SessionTTL)); err != nil {
		s.internalError(w, "store session", err)
		return
	}
	s.Throttle.Reset(username)

	http.SetCookie(w, s.cookie(token, int(SessionTTL.Seconds())))
	httpapi.WriteJSON(w, http.StatusOK, Identity{DirectorID: director.ID, Username: director.Username})
}

// Logout handles POST /api/v1/director/logout. It succeeds whether or not
// there was a session, so it is safe to repeat.
func (s *Service) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(SessionCookie); err == nil {
		if err := s.Store.DeleteSession(r.Context(), HashToken(cookie.Value)); err != nil {
			s.internalError(w, "delete session", err)
			return
		}
	}
	http.SetCookie(w, s.cookie("", -1))
	w.WriteHeader(http.StatusNoContent)
}

// Me handles GET /api/v1/director/me, behind RequireDirector.
func (s *Service) Me(w http.ResponseWriter, r *http.Request) {
	identity, _ := DirectorFrom(r.Context())
	httpapi.WriteJSON(w, http.StatusOK, identity)
}

// RequireDirector lets a request through only with a live director session,
// and makes the director available through DirectorFrom. Every director
// route is wrapped in it; handlers never look at the cookie themselves.
func (s *Service) RequireDirector(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok, err := s.Director(r)
		if err != nil {
			s.internalError(w, "look up session", err)
			return
		}
		if !ok {
			s.unauthenticated(w)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithDirector(r.Context(), identity)))
	})
}

// Director returns the director logged in on this request, if any. It is for
// routes that also admit someone else, such as a room its audience may read;
// director-only routes use RequireDirector.
func (s *Service) Director(r *http.Request) (Identity, bool, error) {
	cookie, err := r.Cookie(SessionCookie)
	if err != nil || cookie.Value == "" {
		return Identity{}, false, nil
	}
	session, err := s.Store.SessionByHash(r.Context(), HashToken(cookie.Value))
	if errors.Is(err, ErrNotFound) {
		return Identity{}, false, nil
	}
	if err != nil {
		return Identity{}, false, err
	}
	if !s.Now().Before(session.ExpiresAt) {
		return Identity{}, false, nil
	}
	return Identity{DirectorID: session.DirectorID, Username: session.Username}, true, nil
}

func (s *Service) refuse(w http.ResponseWriter, username string) {
	s.Throttle.Fail(username)
	httpapi.WriteError(w, http.StatusUnauthorized, "invalid_credentials", "Wrong username or password.")
}

func (s *Service) unauthenticated(w http.ResponseWriter) {
	httpapi.WriteError(w, http.StatusUnauthorized, "not_logged_in", "Log in to continue.")
}

func (s *Service) internalError(w http.ResponseWriter, what string, err error) {
	s.Logger.Error("auth: "+what, "error", err)
	httpapi.WriteError(w, http.StatusInternalServerError, "internal_error", "Something went wrong. Try again.")
}

func (s *Service) cookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     SessionCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	}
}

// dummyHash is a real hash of a throwaway password, computed once, for the
// unknown-username path.
var dummyHash = sync.OnceValue(func() string {
	hash, err := HashPassword("not-a-real-password")
	if err != nil {
		panic(err) // only fails if the system has no randomness
	}
	return hash
})
