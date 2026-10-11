package performance

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/auth"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/httpapi"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/objectstore"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/rooms"
)

const performanceID = "22222222-2222-4222-8222-222222222222"

// recording is turn 3's audio in these tests: ten bytes, to take ranges from.
const recording = "0123456789"

// doorman is the room's authorisation. It admits everyone unless told to
// refuse, and records which room it was asked about.
type doorman struct {
	refuse bool
	asked  []string
}

func (d *doorman) AuthorizeRoom(w http.ResponseWriter, _ *http.Request, roomID string) (rooms.Viewer, bool) {
	d.asked = append(d.asked, roomID)
	if d.refuse {
		httpapi.WriteError(w, http.StatusForbidden, "not_your_room", "This room belongs to another director.")
		return rooms.Viewer{}, false
	}
	return rooms.Viewer{Room: rooms.Room{ID: roomID}, ID: "session-1"}, true
}

type fakeDirectors struct{ loggedIn bool }

func (f fakeDirectors) Director(*http.Request) (auth.Identity, bool, error) {
	return auth.Identity{DirectorID: directorID}, f.loggedIn, nil
}

// fakeObjects is an object store holding recordings by key.
type fakeObjects struct {
	objects map[string]string
	opened  []string
}

type seekingBody struct{ *bytes.Reader }

func (seekingBody) Close() error { return nil }

func (f *fakeObjects) Get(_ context.Context, key string) (*objectstore.Object, error) {
	f.opened = append(f.opened, key)
	data, ok := f.objects[key]
	if !ok {
		return nil, objectstore.ErrNotFound
	}
	return &objectstore.Object{Body: seekingBody{bytes.NewReader([]byte(data))}, Size: int64(len(data)), ContentType: "audio/mpeg"}, nil
}

type audioFixture struct {
	svc     *Service
	store   *memStore
	door    *doorman
	objects *fakeObjects
}

// newAudio is a completed performance whose turn 3 has a recording.
func newAudio() *audioFixture {
	keys := make([]string, Turns)
	for i := range keys {
		keys[i] = "performances/" + performanceID + "/turn-" + string(rune('1'+i)) + ".mp3"
	}
	f := &audioFixture{
		store:   &memStore{row: &Performance{ID: performanceID, RoomID: roomID, Status: StatusCompleted, AudioKeys: keys}},
		door:    &doorman{},
		objects: &fakeObjects{objects: map[string]string{keys[2]: recording}},
	}
	f.svc = &Service{
		Store:      f.store,
		Authorizer: f.door,
		Directors:  fakeDirectors{loggedIn: true},
		Objects:    f.objects,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:        time.Now,
	}
	return f
}

func (f *audioFixture) request(method, url, byteRange string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	mux.HandleFunc(AudioRoute, f.svc.Audio)
	r := httptest.NewRequest(method, url, nil)
	if byteRange != "" {
		r.Header.Set("Range", byteRange)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func (f *audioFixture) get(url string) *httptest.ResponseRecorder {
	return f.request(http.MethodGet, url, "")
}

func responseCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), "body: %s", w.Body)
	return errorCode(body)
}

func TestAudioServesACompletedPerformancesRecording(t *testing.T) {
	f := newAudio()
	w := f.get(AudioURL(performanceID, 3))

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body)
	require.Equal(t, recording, w.Body.String())
	require.Equal(t, "audio/mpeg", w.Header().Get("Content-Type"))
	require.Equal(t, "10", w.Header().Get("Content-Length"))
	require.Equal(t, "bytes", w.Header().Get("Accept-Ranges"))
	require.Equal(t, "private, max-age=604800, immutable", w.Header().Get("Cache-Control"))
	require.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	require.Empty(t, w.Header().Get("Last-Modified"), "a recording is written once; there is nothing to revalidate")

	require.Equal(t, []string{roomID}, f.door.asked, "the caller is checked against the performance's own room")
	require.Equal(t, []string{f.store.row.AudioKeys[2]}, f.objects.opened, "the key comes from the row")
}

func TestAudioAnswersRangeRequests(t *testing.T) {
	for _, tc := range []struct {
		byteRange, contentRange, body string
	}{
		{"bytes=0-3", "bytes 0-3/10", "0123"},
		{"bytes=6-", "bytes 6-9/10", "6789"},
		{"bytes=-2", "bytes 8-9/10", "89"},
		{"bytes=4-99", "bytes 4-9/10", "456789"},
	} {
		w := newAudio().request(http.MethodGet, AudioURL(performanceID, 3), tc.byteRange)
		require.Equal(t, http.StatusPartialContent, w.Code, "%s; body: %s", tc.byteRange, w.Body)
		require.Equal(t, tc.contentRange, w.Header().Get("Content-Range"), tc.byteRange)
		require.Equal(t, tc.body, w.Body.String(), tc.byteRange)
		require.Equal(t, "audio/mpeg", w.Header().Get("Content-Type"), tc.byteRange)
	}

	w := newAudio().request(http.MethodGet, AudioURL(performanceID, 3), "bytes=10-")
	require.Equal(t, http.StatusRequestedRangeNotSatisfiable, w.Code, "a range past the end")
	require.Equal(t, "bytes */10", w.Header().Get("Content-Range"))

	w = newAudio().request(http.MethodHead, AudioURL(performanceID, 3), "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "10", w.Header().Get("Content-Length"))
	require.Empty(t, w.Body.String(), "HEAD has no body")
}

func TestAudioIsOnlyForACompletedPerformance(t *testing.T) {
	for _, status := range []string{StatusRunning, StatusFailed} {
		f := newAudio()
		f.store.row.Status = status
		w := f.get(AudioURL(performanceID, 3))
		require.Equal(t, http.StatusConflict, w.Code, "%s; body: %s", status, w.Body)
		require.Equal(t, "performance_not_completed", responseCode(t, w), status)
		require.Empty(t, f.objects.opened, "%s: the recording is not opened", status)
	}
}

func TestAudioChecksTheCallerBeforeAnythingElse(t *testing.T) {
	// Someone outside the room learns nothing about the performance: not
	// whether it has finished, nor whether the turn exists.
	for _, url := range []string{AudioURL(performanceID, 3), AudioURL(performanceID, 99)} {
		for _, status := range []string{StatusRunning, StatusCompleted} {
			f := newAudio()
			f.store.row.Status = status
			f.door.refuse = true
			w := f.get(url)
			require.Equal(t, http.StatusForbidden, w.Code, "%s, %s; body: %s", url, status, w.Body)
			require.Equal(t, "not_your_room", responseCode(t, w))
			require.Empty(t, f.objects.opened)
		}
	}
}

func TestAudioForATurnThatDoesNotExist(t *testing.T) {
	for _, turn := range []string{"0", "9", "-1", "x", "1.5"} {
		f := newAudio()
		w := f.get("/api/v1/performances/" + performanceID + "/turns/" + turn + "/audio")
		require.Equal(t, http.StatusNotFound, w.Code, "turn %q; body: %s", turn, w.Body)
		require.Equal(t, "not_found", responseCode(t, w), "turn %q", turn)
		require.Empty(t, f.objects.opened, "turn %q", turn)
	}
}

func TestAudioForAPerformanceThatDoesNotExist(t *testing.T) {
	const unknown = "33333333-3333-4333-8333-333333333333"

	f := newAudio()
	w := f.get(AudioURL(unknown, 1))
	require.Equal(t, http.StatusNotFound, w.Code, "a director is told there is no such thing; body: %s", w.Body)
	require.Equal(t, "not_found", responseCode(t, w))

	// Anyone else is told to log in, as they would be for one that exists.
	f = newAudio()
	f.svc.Directors = fakeDirectors{loggedIn: false}
	w = f.get(AudioURL(unknown, 1))
	require.Equal(t, http.StatusUnauthorized, w.Code, "body: %s", w.Body)
	require.Equal(t, "not_logged_in", responseCode(t, w))
	require.Empty(t, f.door.asked, "there is no room to ask about")

	f = newAudio()
	w = f.get(AudioURL("not-an-id", 1))
	require.Equal(t, http.StatusNotFound, w.Code, "body: %s", w.Body)
	require.Zero(t, f.store.lookups, "an ID that is not a UUID is not put to the database")
}

func TestAudioWhoseRecordingIsGone(t *testing.T) {
	f := newAudio()
	w := f.get(AudioURL(performanceID, 4)) // completed, but nothing stored for turn 4
	require.Equal(t, http.StatusNotFound, w.Code, "body: %s", w.Body)
	require.Equal(t, "not_found", responseCode(t, w))
}

func TestPremiereStateIsThePremiereAsPublished(t *testing.T) {
	startsAt := time.Date(2026, 10, 11, 12, 0, 5, 0, time.UTC)
	p := Performance{ID: "perf-1", Status: StatusCompleted, PremiereStartsAt: startsAt}
	for turn := 1; turn <= Turns; turn++ {
		speaker := "A"
		if turn%2 == 0 {
			speaker = "B"
		}
		p.Script.Turns = append(p.Script.Turns, ScriptTurn{
			Turn: turn, Speaker: speaker, Beat: "REACT",
			Choices:    []Choice{{ID: "c1", Text: "kept"}, {ID: "c2", Text: "dropped"}, {ID: "c3", Text: "dropped too"}},
			Counts:     map[string]int{"c1": 2, "c2": 1, "c3": 0},
			Winner:     Choice{ID: "c1", Text: "kept"},
			TaggedText: "[whispers] kept",
		})
	}

	state := PremiereState(p)
	require.Equal(t, PhasePremiere, state.Phase)
	require.Equal(t, Turns, state.Turn)
	require.Equal(t, "perf-1", state.PerformanceID)
	require.Equal(t, startsAt, state.Premiere.StartsAt)
	require.Len(t, state.Lines, Turns)
	require.Len(t, state.Premiere.Clips, Turns)
	require.Equal(t, Line{Turn: 2, Speaker: "B", Text: "kept"}, state.Lines[1])
	require.Equal(t, Clip{Turn: 2, Speaker: "B", URL: AudioURL("perf-1", 2)}, state.Premiere.Clips[1])

	// Only what the premiere update carried: no choices, counts, or tags.
	sent, err := json.Marshal(state)
	require.NoError(t, err)
	require.NotContains(t, string(sent), "whispers")
	require.NotContains(t, string(sent), "dropped")
	require.NotContains(t, string(sent), "counts")
}

func TestFailedStateSaysWhy(t *testing.T) {
	require.Equal(t, State{
		Phase: PhaseFailed, PerformanceID: "perf-1",
		Failure: &Failure{Code: FailureChoices, Message: "The next lines could not be written."},
	}, FailedState(Performance{ID: "perf-1", Status: StatusFailed, FailureReason: FailureChoices}))

	// A row marked failed with no reason recorded.
	require.Equal(t, FailureInternal, FailedState(Performance{ID: "perf-1", Status: StatusFailed}).Failure.Code)
}

func TestLastRevisionIsThreeUpdatesATurnAndThePremiere(t *testing.T) {
	require.Equal(t, 25, LastRevision)
}
