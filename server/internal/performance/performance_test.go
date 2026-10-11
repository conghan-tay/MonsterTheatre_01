package performance

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/auth"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/rooms"
)

const (
	roomID     = "11111111-1111-4111-8111-111111111111"
	directorID = "director-1"
)

type fakeRooms struct{ err error }

func (f fakeRooms) Get(_ context.Context, id string) (rooms.Room, error) {
	if f.err != nil {
		return rooms.Room{}, f.err
	}
	if id != roomID {
		return rooms.Room{}, rooms.ErrNotFound
	}
	return rooms.Room{ID: roomID, DirectorID: directorID}, nil
}

// memStore holds at most one performance, like the table does per room.
type memStore struct {
	row     *Performance
	creates int
	// lookups counts reads by ID.
	lookups int
}

func (m *memStore) CreateOrGet(_ context.Context, room string, now time.Time) (Performance, bool, error) {
	if m.row != nil {
		return *m.row, false, nil
	}
	m.creates++
	m.row = &Performance{ID: "perf-1", RoomID: room, Status: StatusRunning, CreatedAt: now}
	return *m.row, true, nil
}

func (m *memStore) ByID(_ context.Context, id string) (Performance, error) {
	m.lookups++
	if m.row == nil || m.row.ID != id {
		return Performance{}, ErrNotFound
	}
	return *m.row, nil
}

type fakeStarter struct {
	err    error
	starts []string
}

func (f *fakeStarter) Start(_ context.Context, performanceID, _ string) error {
	f.starts = append(f.starts, performanceID)
	return f.err
}

func newService(store *memStore, starter *fakeStarter) *Service {
	return &Service{
		Rooms:     fakeRooms{},
		Store:     store,
		Workflows: starter,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:       func() time.Time { return time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC) },
	}
}

// press sends a Start as the given director, through the real login
// middleware's context so the handler reads the director the usual way.
func press(t *testing.T, svc *Service, room, director string) (int, map[string]any) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/rooms/{roomId}/performance", svc.Start)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/rooms/"+room+"/performance", strings.NewReader("{}"))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(auth.WithDirector(r.Context(), auth.Identity{DirectorID: director}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), "body: %s", w.Body)
	return w.Code, body
}

func errorCode(body map[string]any) string {
	detail, _ := body["error"].(map[string]any)
	code, _ := detail["code"].(string)
	return code
}

func TestStartCreatesOnceAndStartsTheWorkflowEachTime(t *testing.T) {
	store, starter := &memStore{}, &fakeStarter{}
	svc := newService(store, starter)

	status, body := press(t, svc, roomID, directorID)
	require.Equal(t, http.StatusCreated, status)
	require.Equal(t, map[string]any{"performanceId": "perf-1", "roomId": roomID, "status": "running"}, body)

	status, body = press(t, svc, roomID, directorID)
	require.Equal(t, http.StatusOK, status, "a second press returns the same performance")
	require.Equal(t, "perf-1", body["performanceId"])

	require.Equal(t, 1, store.creates)
	require.Equal(t, []string{"perf-1", "perf-1"}, starter.starts,
		"each press starts the workflow, which Temporal ignores if it already exists")
}

func TestStartRepairsAPressThatDiedBeforeTheWorkflowStarted(t *testing.T) {
	store, starter := &memStore{}, &fakeStarter{err: errors.New("temporal unreachable")}
	svc := newService(store, starter)

	status, body := press(t, svc, roomID, directorID)
	require.Equal(t, http.StatusServiceUnavailable, status, "the row is written but the workflow could not be started")
	require.Equal(t, "start_unavailable", errorCode(body))
	require.NotNil(t, store.row)

	starter.err = nil
	status, body = press(t, svc, roomID, directorID)
	require.Equal(t, http.StatusOK, status, "pressing again starts the workflow for the same performance")
	require.Equal(t, "perf-1", body["performanceId"])
	require.Equal(t, []string{"perf-1", "perf-1"}, starter.starts)
}

func TestStartDoesNotRerunAFinishedPerformance(t *testing.T) {
	for _, finished := range []string{StatusCompleted, StatusFailed} {
		store := &memStore{row: &Performance{ID: "perf-1", RoomID: roomID, Status: finished}}
		starter := &fakeStarter{}
		status, body := press(t, newService(store, starter), roomID, directorID)
		require.Equal(t, http.StatusOK, status)
		require.Equal(t, finished, body["status"])
		require.Empty(t, starter.starts, "a %s performance's workflow is not started again", finished)
	}
}

func TestStartRefusesOtherDirectorsAndUnknownRooms(t *testing.T) {
	cases := []struct {
		name     string
		room     string
		director string
		status   int
		code     string
	}{
		{"another director", roomID, "director-2", http.StatusForbidden, "not_your_room"},
		{"a room that does not exist", "22222222-2222-4222-8222-222222222222", directorID, http.StatusNotFound, "not_found"},
		{"not a room ID", "nonsense", directorID, http.StatusNotFound, "not_found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, starter := &memStore{}, &fakeStarter{}
			status, body := press(t, newService(store, starter), tc.room, tc.director)
			require.Equal(t, tc.status, status)
			require.Equal(t, tc.code, errorCode(body))
			require.Nil(t, store.row, "nothing is created")
			require.Empty(t, starter.starts)
		})
	}
}

func TestStartMustBeSentAsJSON(t *testing.T) {
	store, starter := &memStore{}, &fakeStarter{}
	svc := newService(store, starter)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/rooms/{roomId}/performance", svc.Start)

	// What a form on another site could send.
	r := httptest.NewRequest(http.MethodPost, "/api/v1/rooms/"+roomID+"/performance", strings.NewReader(""))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r = r.WithContext(auth.WithDirector(r.Context(), auth.Identity{DirectorID: directorID}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	require.Nil(t, store.row, "nothing is started")
	require.Empty(t, starter.starts)
}

// The workflow publishes clip URLs long before any browser fetches one, so
// the URL it builds and the route the server registers must agree.
func TestAudioURLIsMatchedByAudioRoute(t *testing.T) {
	const performanceID = "33333333-3333-4333-8333-333333333333"
	var gotID, gotTurn string
	mux := http.NewServeMux()
	mux.HandleFunc(AudioRoute, func(_ http.ResponseWriter, r *http.Request) {
		gotID, gotTurn = r.PathValue("performanceId"), r.PathValue("turn")
	})

	for turn := 1; turn <= Turns; turn++ {
		gotID, gotTurn = "", ""
		url := AudioURL(performanceID, turn)
		require.True(t, strings.HasPrefix(url, "/"), "a relative path, so it works behind any origin: %s", url)
		mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, url, nil))
		require.Equal(t, performanceID, gotID, "url: %s", url)
		require.Equal(t, strconv.Itoa(turn), gotTurn, "url: %s", url)
	}
}

func TestWorkflowIDIsDerivedFromThePerformance(t *testing.T) {
	require.Equal(t, "performance/perf-1", WorkflowID("perf-1"))
}
