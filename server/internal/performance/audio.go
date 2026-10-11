package performance

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/auth"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/httpapi"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/objectstore"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/rooms"
)

// RoomAuthorizer admits the people allowed to read a room, its director or
// a member of its audience, writing the refusal itself. *rooms.Service
// implements it.
type RoomAuthorizer interface {
	AuthorizeRoom(w http.ResponseWriter, r *http.Request, roomID string) (rooms.Viewer, bool)
}

// DirectorResolver finds the director logged in on a request, if any.
// *auth.Service implements it.
type DirectorResolver interface {
	Director(r *http.Request) (auth.Identity, bool, error)
}

// ObjectReader opens a stored object. An objectstore.Store implements it.
type ObjectReader interface {
	Get(ctx context.Context, key string) (*objectstore.Object, error)
}

// audioCacheControl lets a browser keep a recording for as long as the
// recording itself is kept. Only a completed run's recordings are served,
// and those never change; but only the room's people may hear them, so no
// shared cache may hold one.
const audioCacheControl = "private, max-age=604800, immutable"

// Audio handles GET /api/v1/performances/{performanceId}/turns/{turn}/audio:
// one turn's recording, for the room's director or a member of its audience,
// once the performance has completed.
//
// Until then it answers 409, although the recordings of earlier turns are
// already saved: nothing is heard before the premiere (docs/decisions.md,
// D022), and a failed run's recordings are never heard at all.
//
// Range requests are answered by http.ServeContent, which reads only the
// bytes asked for from the object store. Safari's audio element will not
// play without them.
func (s *Service) Audio(w http.ResponseWriter, r *http.Request) {
	p, err := s.performanceByID(r)
	if errors.Is(err, ErrNotFound) {
		s.noSuchRecording(w, r)
		return
	}
	if err != nil {
		s.internalError(w, "load performance", err)
		return
	}
	if _, ok := s.Authorizer.AuthorizeRoom(w, r, p.RoomID); !ok {
		return
	}

	turn, err := strconv.Atoi(r.PathValue("turn"))
	if err != nil || turn < 1 || turn > Turns {
		httpapi.WriteError(w, http.StatusNotFound, "not_found", "No such recording.")
		return
	}
	if p.Status != StatusCompleted {
		httpapi.WriteError(w, http.StatusConflict, "performance_not_completed",
			"The recordings can be played once the performance has completed.")
		return
	}
	if turn > len(p.AudioKeys) {
		s.internalError(w, "find recording", errors.New("completed performance "+p.ID+" has no audio key for turn "+strconv.Itoa(turn)))
		return
	}

	obj, err := s.Objects.Get(r.Context(), p.AudioKeys[turn-1])
	if errors.Is(err, objectstore.ErrNotFound) {
		// Recordings are removed after seven days.
		httpapi.WriteError(w, http.StatusNotFound, "not_found", "This recording is no longer kept.")
		return
	}
	if err != nil {
		s.internalError(w, "read recording", err)
		return
	}
	defer obj.Body.Close()

	w.Header().Set("Content-Type", obj.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", audioCacheControl)
	// No modification time: a recording is written once, so there is
	// nothing for a browser to revalidate.
	http.ServeContent(w, r, "", time.Time{}, obj.Body)
}

// performanceByID loads the performance named in the path. An ID that is
// not a UUID is nobody's, and is not put to the database.
func (s *Service) performanceByID(r *http.Request) (Performance, error) {
	id := r.PathValue("performanceId")
	if uuid.Validate(id) != nil {
		return Performance{}, ErrNotFound
	}
	return s.Store.ByID(r.Context(), id)
}

// noSuchRecording answers a request for a performance that does not exist as
// a room that does not exist is answered: someone who is not logged in as a
// director is told to log in, exactly as they would be for a performance
// that exists, so the answer does not reveal which do.
func (s *Service) noSuchRecording(w http.ResponseWriter, r *http.Request) {
	_, isDirector, err := s.Directors.Director(r)
	if err != nil {
		s.internalError(w, "look up director", err)
		return
	}
	if !isDirector {
		rooms.WriteNotLoggedIn(w)
		return
	}
	httpapi.WriteError(w, http.StatusNotFound, "not_found", "No such recording.")
}
