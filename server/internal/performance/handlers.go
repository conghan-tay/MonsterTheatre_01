package performance

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

const maxStartBodyBytes = 1 << 10

// RoomReader loads a room. *rooms.PGStore implements it.
type RoomReader interface {
	Get(ctx context.Context, id string) (rooms.Room, error)
}

// Store is the persistence the handlers need. *PGStore implements it.
type Store interface {
	CreateOrGet(ctx context.Context, roomID string, now time.Time) (Performance, bool, error)
	ByID(ctx context.Context, id string) (Performance, error)
}

// Starter starts a performance's workflow. *Workflows implements it.
type Starter interface {
	Start(ctx context.Context, performanceID, roomID string, lipSync bool) error
}

// Service serves the performance endpoints. Start is behind
// auth.RequireDirector. Audio admits the room's director or a member of its
// audience, and checks that itself.
type Service struct {
	Rooms     RoomReader
	Store     Store
	Workflows Starter
	// Authorizer, Directors, and Objects are what Audio needs.
	Authorizer RoomAuthorizer
	Directors  DirectorResolver
	Objects    ObjectReader
	Logger     *slog.Logger
	Now        func() time.Time
}

type startResponse struct {
	PerformanceID string `json:"performanceId"`
	RoomID        string `json:"roomId"`
	Status        string `json:"status"`
}

// Start handles POST /api/v1/rooms/{roomId}/performance: the Start button.
//
// It writes the row first and starts the workflow second, and it is safe to
// repeat. A room has one performance, so a second press returns the first
// one (200 where the first press got 201). While the row says running, every
// press also starts the workflow, which does nothing if it already exists;
// so if the first press died between the two steps, pressing again repairs
// it.
func (s *Service) Start(w http.ResponseWriter, r *http.Request) {
	director, _ := auth.DirectorFrom(r.Context())

	// The body says whether to make lip-sync videos, and may be empty
	// ({}), which means not. It must be declared as JSON: a form on another
	// site cannot send that type, so it cannot press Start for a director.
	var body struct {
		LipSync bool `json:"lipSync"`
	}
	if err := httpapi.DecodeJSON(w, r, &body, maxStartBodyBytes); err != nil {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "invalid_request", `Send a JSON object, with "lipSync" true or false if wanted.`)
		return
	}

	roomID := r.PathValue("roomId")
	if uuid.Validate(roomID) != nil {
		httpapi.WriteError(w, http.StatusNotFound, "not_found", "No such room.")
		return
	}
	room, err := s.Rooms.Get(r.Context(), roomID)
	if errors.Is(err, rooms.ErrNotFound) {
		httpapi.WriteError(w, http.StatusNotFound, "not_found", "No such room.")
		return
	}
	if err != nil {
		s.internalError(w, "load room", err)
		return
	}
	if room.DirectorID != director.DirectorID {
		httpapi.WriteError(w, http.StatusForbidden, "not_your_room", "This room belongs to another director.")
		return
	}

	p, created, err := s.Store.CreateOrGet(r.Context(), roomID, s.Now())
	if err != nil {
		s.internalError(w, "create performance", err)
		return
	}
	log := s.Logger.With("room_id", roomID, "performance_id", p.ID)

	if p.Status == StatusRunning {
		if err := s.Workflows.Start(r.Context(), p.ID, roomID, body.LipSync); err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			log.Error("performance: start workflow", "error", err)
			httpapi.WriteError(w, http.StatusServiceUnavailable, "start_unavailable",
				"The performance could not be started. Press Start again.")
			return
		}
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
		log.Info("performance started")
	}
	httpapi.WriteJSON(w, status, startResponse{PerformanceID: p.ID, RoomID: roomID, Status: p.Status})
}

func (s *Service) internalError(w http.ResponseWriter, what string, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	s.Logger.Error("performance: "+what, "error", err)
	httpapi.WriteError(w, http.StatusInternalServerError, "internal_error", "Something went wrong. Try again.")
}
