package rooms

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/auth"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/httpapi"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/objectstore"
)

const (
	maxRoomJSONBytes = 64 << 10
	// maxCreateBodyBytes bounds the whole request: two portraits, the JSON
	// part, and multipart overhead.
	maxCreateBodyBytes = 2*MaxPortraitBytes + 1<<20
)

// Service serves the room endpoints. Every route is behind
// auth.RequireDirector.
type Service struct {
	Store   Store
	Objects objectstore.Store
	Links   ShareLinks
	// VoiceA and VoiceB are recorded on each new room.
	VoiceA string
	VoiceB string
	Logger *slog.Logger
	Now    func() time.Time
}

type characterView struct {
	Name        string `json:"name"`
	Brief       string `json:"brief"`
	PortraitURL string `json:"portraitUrl"`
}

type roomView struct {
	RoomID     string        `json:"roomId"`
	Scene      Scene         `json:"scene"`
	CharacterA characterView `json:"characterA"`
	CharacterB characterView `json:"characterB"`
	ShareLink  string        `json:"shareLink"`
	CreatedAt  time.Time     `json:"createdAt"`
}

type summaryView struct {
	RoomID    string    `json:"roomId"`
	Topic     string    `json:"topic"`
	CreatedAt time.Time `json:"createdAt"`
}

func (s *Service) view(r Room) roomView {
	portrait := func(slot string) string { return "/api/v1/rooms/" + r.ID + "/portraits/" + slot }
	return roomView{
		RoomID:     r.ID,
		Scene:      r.Scene,
		CharacterA: characterView{Name: r.CharacterA.Name, Brief: r.CharacterA.Brief, PortraitURL: portrait("a")},
		CharacterB: characterView{Name: r.CharacterB.Name, Brief: r.CharacterB.Brief, PortraitURL: portrait("b")},
		ShareLink:  s.Links.URL(r.ID),
		CreatedAt:  r.CreatedAt,
	}
}

// submission is a parsed create request.
type submission struct {
	input     CreateInput
	hasInput  bool
	portraits map[string][]byte // "portraitA", "portraitB"
}

var errPortraitTooLarge = errors.New("portrait too large")

// Create handles POST /api/v1/rooms.
//
// Order matters: everything is validated before anything is stored, and the
// room row is written last, so a room never exists without its portraits.
// A failure after the uploads can leave orphaned portrait files, which the
// seven-day expiry removes (docs/decisions.md, D025).
func (s *Service) Create(w http.ResponseWriter, r *http.Request) {
	director, _ := auth.DirectorFrom(r.Context())

	sub, err := readSubmission(w, r)
	var tooLarge *http.MaxBytesError
	switch {
	case errors.Is(err, errPortraitTooLarge) || errors.As(err, &tooLarge):
		httpapi.WriteError(w, http.StatusRequestEntityTooLarge, "upload_too_large", "Each portrait must be at most 5 MiB.")
		return
	case err != nil:
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "invalid_request",
			"Send a multipart form with a JSON part named room and files named portraitA and portraitB.")
		return
	}

	sub.input.Normalize()
	problems := sub.input.Validate()
	if !sub.hasInput {
		problems["room"] = "Required."
	}
	contentTypes := map[string]string{}
	for _, field := range []string{"portraitA", "portraitB"} {
		data, ok := sub.portraits[field]
		if !ok {
			problems[field] = "Required."
			continue
		}
		contentType, err := DetectImage(data)
		if err != nil {
			problems[field] = err.Error()
			continue
		}
		contentTypes[field] = contentType
	}
	if len(problems) > 0 {
		httpapi.WriteValidationError(w, "invalid_room", "Some fields need attention.", problems)
		return
	}

	id := uuid.NewString()
	room := Room{
		ID:         id,
		DirectorID: director.DirectorID,
		Scene:      sub.input.Scene,
		CharacterA: Character{Name: sub.input.CharacterA.Name, Brief: sub.input.CharacterA.Brief, VoiceID: s.VoiceA, PortraitKey: portraitKey(id, "a")},
		CharacterB: Character{Name: sub.input.CharacterB.Name, Brief: sub.input.CharacterB.Brief, VoiceID: s.VoiceB, PortraitKey: portraitKey(id, "b")},
		CreatedAt:  s.Now().UTC().Truncate(time.Microsecond),
	}

	for field, key := range map[string]string{"portraitA": room.CharacterA.PortraitKey, "portraitB": room.CharacterB.PortraitKey} {
		if err := s.Objects.Put(r.Context(), key, bytes.NewReader(sub.portraits[field]), contentTypes[field]); err != nil {
			s.internalError(w, "store portrait", err)
			return
		}
	}
	if err := s.Store.Create(r.Context(), room); err != nil {
		s.internalError(w, "insert room", err)
		return
	}
	httpapi.WriteJSON(w, http.StatusCreated, s.view(room))
}

// Get handles GET /api/v1/rooms/{roomId}.
func (s *Service) Get(w http.ResponseWriter, r *http.Request) {
	room, ok := s.ownedRoom(w, r)
	if !ok {
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, s.view(room))
}

// List handles GET /api/v1/rooms: the director's own rooms, newest first.
func (s *Service) List(w http.ResponseWriter, r *http.Request) {
	director, _ := auth.DirectorFrom(r.Context())
	summaries, err := s.Store.ListByDirector(r.Context(), director.DirectorID)
	if err != nil {
		s.internalError(w, "list rooms", err)
		return
	}
	views := make([]summaryView, 0, len(summaries))
	for _, sum := range summaries {
		views = append(views, summaryView{RoomID: sum.ID, Topic: sum.Topic, CreatedAt: sum.CreatedAt})
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"rooms": views})
}

// Portrait handles GET /api/v1/rooms/{roomId}/portraits/{slot}.
func (s *Service) Portrait(w http.ResponseWriter, r *http.Request) {
	room, ok := s.ownedRoom(w, r)
	if !ok {
		return
	}
	var key string
	switch r.PathValue("slot") {
	case "a":
		key = room.CharacterA.PortraitKey
	case "b":
		key = room.CharacterB.PortraitKey
	default:
		httpapi.WriteError(w, http.StatusNotFound, "not_found", "No such portrait.")
		return
	}

	obj, err := s.Objects.Get(r.Context(), key)
	if errors.Is(err, objectstore.ErrNotFound) {
		httpapi.WriteError(w, http.StatusNotFound, "not_found", "No such portrait.")
		return
	}
	if err != nil {
		s.internalError(w, "read portrait", err)
		return
	}
	defer obj.Body.Close()

	w.Header().Set("Content-Type", obj.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(obj.Size, 10))
	// The bytes were checked to be an image at upload; never let a browser
	// second-guess the type.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// A room's portraits never change, but only its members may see them.
	w.Header().Set("Cache-Control", "private, max-age=604800, immutable")
	_, _ = io.Copy(w, obj.Body)
}

// ownedRoom loads the room named in the path and checks the logged-in
// director owns it. It writes the error response itself when it returns false.
func (s *Service) ownedRoom(w http.ResponseWriter, r *http.Request) (Room, bool) {
	director, _ := auth.DirectorFrom(r.Context())
	id := r.PathValue("roomId")
	if _, err := uuid.Parse(id); err != nil {
		httpapi.WriteError(w, http.StatusNotFound, "not_found", "No such room.")
		return Room{}, false
	}
	room, err := s.Store.Get(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		httpapi.WriteError(w, http.StatusNotFound, "not_found", "No such room.")
		return Room{}, false
	}
	if err != nil {
		s.internalError(w, "load room", err)
		return Room{}, false
	}
	if room.DirectorID != director.DirectorID {
		httpapi.WriteError(w, http.StatusForbidden, "not_your_room", "This room belongs to another director.")
		return Room{}, false
	}
	return room, true
}

func (s *Service) internalError(w http.ResponseWriter, what string, err error) {
	if errors.Is(err, context.Canceled) {
		return // the client went away; nothing to report
	}
	s.Logger.Error("rooms: "+what, "error", err)
	httpapi.WriteError(w, http.StatusInternalServerError, "internal_error", "Something went wrong. Try again.")
}

func portraitKey(roomID, slot string) string {
	return "rooms/" + roomID + "/portrait-" + slot
}

// readSubmission reads the multipart form. Portraits are held in memory,
// which is bounded: at most two of five mebibytes each.
func readSubmission(w http.ResponseWriter, r *http.Request) (submission, error) {
	sub := submission{portraits: map[string][]byte{}}

	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		return sub, errors.New("not multipart/form-data")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxCreateBodyBytes)
	reader := multipart.NewReader(r.Body, params["boundary"])

	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return sub, nil
		}
		if err != nil {
			return sub, err
		}
		switch name := part.FormName(); name {
		case "room":
			data, err := io.ReadAll(io.LimitReader(part, maxRoomJSONBytes+1))
			if err != nil {
				return sub, err
			}
			if len(data) > maxRoomJSONBytes {
				return sub, errors.New("room part too large")
			}
			if err := json.Unmarshal(data, &sub.input); err != nil {
				return sub, err
			}
			sub.hasInput = true
		case "portraitA", "portraitB":
			data, err := io.ReadAll(io.LimitReader(part, MaxPortraitBytes+1))
			if err != nil {
				return sub, err
			}
			if len(data) > MaxPortraitBytes {
				return sub, errPortraitTooLarge
			}
			sub.portraits[name] = data
		}
	}
}
