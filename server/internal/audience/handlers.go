package audience

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/auth"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/httpapi"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/rooms"
)

const (
	// SessionTTL is how long an audience session lasts, matching how long a
	// room's media is kept (docs/decisions.md, D030).
	SessionTTL = 7 * 24 * time.Hour

	maxJoinBodyBytes = 4 << 10
)

// CookieName is the session cookie for a room. It is per room, so one
// browser can attend several rooms at once.
func CookieName(roomID string) string {
	return "mt_session_" + roomID
}

// RoomReader loads a room. *rooms.PGStore implements it.
type RoomReader interface {
	Get(ctx context.Context, id string) (rooms.Room, error)
}

// Service serves the join endpoint and recognises audience sessions.
type Service struct {
	Store        Store
	Rooms        RoomReader
	Links        rooms.ShareLinks
	Logger       *slog.Logger
	Now          func() time.Time
	CookieSecure bool
}

type joinRequest struct {
	RoomID string `json:"roomId"`
	Token  string `json:"token"`
}

type joinResponse struct {
	SessionID string                 `json:"sessionId"`
	RoomID    string                 `json:"roomId"`
	Restored  bool                   `json:"restored"`
	Room      rooms.AudienceRoomView `json:"room"`
}

// Join handles POST /api/v1/audience-sessions: the "Enter theatre" click.
//
// It is safe to repeat. A browser that already holds a live session for the
// room gets that session back and no new slot is used.
func (s *Service) Join(w http.ResponseWriter, r *http.Request) {
	var req joinRequest
	if err := httpapi.DecodeJSON(w, r, &req, maxJoinBodyBytes); err != nil {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "invalid_request", "Send a JSON body with roomId and token.")
		return
	}

	// One answer for everything wrong with a link, so it reveals nothing
	// about which rooms exist.
	if uuid.Validate(req.RoomID) != nil || !s.Links.Verify(req.RoomID, req.Token) {
		httpapi.WriteError(w, http.StatusForbidden, "invalid_share_link", "This link is not valid.")
		return
	}

	room, err := s.Rooms.Get(r.Context(), req.RoomID)
	if errors.Is(err, rooms.ErrNotFound) {
		httpapi.WriteError(w, http.StatusNotFound, "not_found", "This room no longer exists.")
		return
	}
	if err != nil {
		s.internalError(w, "load room", err)
		return
	}

	if session, ok, err := s.SessionFor(r, room.ID); err != nil {
		s.internalError(w, "look up session", err)
		return
	} else if ok {
		if err := s.Store.Touch(r.Context(), session.ID, s.Now()); err != nil {
			s.internalError(w, "touch session", err)
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, joinResponse{
			SessionID: session.ID, RoomID: room.ID, Restored: true, Room: rooms.AudienceView(room),
		})
		return
	}

	token, hash, err := auth.NewToken()
	if err != nil {
		s.internalError(w, "create session token", err)
		return
	}
	now := s.Now()
	sessionID, err := s.Store.Admit(r.Context(), room.ID, hash, now, now.Add(SessionTTL))
	switch {
	case errors.Is(err, ErrRoomFull):
		httpapi.WriteError(w, http.StatusConflict, "room_full", "This room is full.")
		return
	case errors.Is(err, ErrRoomNotFound):
		httpapi.WriteError(w, http.StatusNotFound, "not_found", "This room no longer exists.")
		return
	case err != nil:
		s.internalError(w, "admit session", err)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     CookieName(room.ID),
		Value:    token,
		Path:     "/",
		MaxAge:   int(SessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   s.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	httpapi.WriteJSON(w, http.StatusCreated, joinResponse{
		SessionID: sessionID, RoomID: room.ID, Restored: false, Room: rooms.AudienceView(room),
	})
}

// SessionFor returns the live audience session this request holds for the
// room, if any. The cookie's name ties it to the room, and the stored
// session's room is checked as well, so a secret for one room presented
// under another room's cookie name opens nothing.
func (s *Service) SessionFor(r *http.Request, roomID string) (Session, bool, error) {
	cookie, err := r.Cookie(CookieName(roomID))
	if err != nil || cookie.Value == "" {
		return Session{}, false, nil
	}
	session, err := s.Store.SessionByHash(r.Context(), auth.HashToken(cookie.Value))
	if errors.Is(err, ErrNotFound) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}
	if session.RoomID != roomID || !s.Now().Before(session.ExpiresAt) {
		return Session{}, false, nil
	}
	return session, true, nil
}

// IsMember reports whether the request holds a live session for the room.
func (s *Service) IsMember(r *http.Request, roomID string) (bool, error) {
	_, ok, err := s.SessionFor(r, roomID)
	return ok, err
}

func (s *Service) internalError(w http.ResponseWriter, what string, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	s.Logger.Error("audience: "+what, "error", err)
	httpapi.WriteError(w, http.StatusInternalServerError, "internal_error", "Something went wrong. Try again.")
}
