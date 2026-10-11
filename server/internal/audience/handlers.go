package audience

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/auth"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/httpapi"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/rooms"
)

const (
	// SessionTTL is how long an audience session lasts, matching how long a
	// room's media is kept (docs/decisions.md, D030).
	SessionTTL = 7 * 24 * time.Hour

	// MaxNicknameLength is the longest nickname, in characters.
	MaxNicknameLength = 30

	maxJoinBodyBytes = 4 << 10
	// watcherTimeout bounds telling the theatre that a room's audience
	// changed. The join has already succeeded by then.
	watcherTimeout = 5 * time.Second
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

// Watcher is told when a room's audience has changed: someone was admitted,
// or someone already in changed their name. *theater.Service implements it,
// and passes the news to the room's director.
type Watcher interface {
	AudienceChanged(ctx context.Context, roomID string)
}

// Service serves the join endpoint and recognises audience sessions.
type Service struct {
	Store Store
	Rooms RoomReader
	// Watcher may be nil, when nobody needs telling.
	Watcher      Watcher
	Links        rooms.ShareLinks
	Logger       *slog.Logger
	Now          func() time.Time
	CookieSecure bool
}

type joinRequest struct {
	RoomID   string `json:"roomId"`
	Token    string `json:"token"`
	Nickname string `json:"nickname"`
}

// cleanNickname trims a nickname and says what is wrong with it, if
// anything. Length is counted in characters, as the page counts it.
func cleanNickname(raw string) (nickname, problem string) {
	nickname = strings.TrimSpace(raw)
	switch {
	case nickname == "":
		return "", "Required."
	case utf8.RuneCountInString(nickname) > MaxNicknameLength:
		return "", "Must be at most " + strconv.Itoa(MaxNicknameLength) + " characters."
	}
	return nickname, ""
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
// room gets that session back and no new slot is used; the nickname it gives
// this time replaces the one it gave before.
//
// A nickname is required. It is shown to the room's director and to nobody
// else, and has no part in voting: votes are counted by session.
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
	nickname, problem := cleanNickname(req.Nickname)
	if problem != "" {
		httpapi.WriteValidationError(w, "invalid_nickname", "Enter a nickname to join.", map[string]string{"nickname": problem})
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
		if err := s.Store.Return(r.Context(), session.ID, nickname, s.Now()); err != nil {
			s.internalError(w, "record returning session", err)
			return
		}
		s.audienceChanged(r.Context(), room.ID)
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
	sessionID, err := s.Store.Admit(r.Context(), room.ID, hash, nickname, now, now.Add(SessionTTL))
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

	s.audienceChanged(r.Context(), room.ID)

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

// MemberID returns the ID of the live session the request holds for the
// room, if any.
func (s *Service) MemberID(r *http.Request, roomID string) (string, bool, error) {
	session, ok, err := s.SessionFor(r, roomID)
	return session.ID, ok, err
}

// Nicknames lists a room's audience in the order they joined.
func (s *Service) Nicknames(ctx context.Context, roomID string) ([]string, error) {
	return s.Store.Nicknames(ctx, roomID)
}

// audienceChanged tells the watcher, if there is one. The change is already
// saved, so it is told even if the browser that made it has gone away.
func (s *Service) audienceChanged(ctx context.Context, roomID string) {
	if s.Watcher == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), watcherTimeout)
	defer cancel()
	s.Watcher.AudienceChanged(ctx, roomID)
}

// Seen records that a session's browser has just connected.
func (s *Service) Seen(ctx context.Context, sessionID string) error {
	return s.Store.Touch(ctx, sessionID, s.Now())
}

func (s *Service) internalError(w http.ResponseWriter, what string, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	s.Logger.Error("audience: "+what, "error", err)
	httpapi.WriteError(w, http.StatusInternalServerError, "internal_error", "Something went wrong. Try again.")
}
