package activities

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/objectstore"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/providers"
)

// lipSyncStub is a providers.LipSync that records what it is asked and
// answers Result from a list, one answer per call.
type lipSyncStub struct {
	uploads   []providers.UploadRequest
	submitted []providers.LipSyncRequest
	results   []resultAnswer
	polled    int
	downloads []string
	video     []byte
}

type resultAnswer struct {
	result providers.LipSyncResult
	err    error
}

func (s *lipSyncStub) Upload(_ context.Context, req providers.UploadRequest) (string, error) {
	s.uploads = append(s.uploads, req)
	return "https://files.example/" + req.Filename, nil
}

func (s *lipSyncStub) Submit(_ context.Context, req providers.LipSyncRequest) (string, error) {
	s.submitted = append(s.submitted, req)
	return "pred_1", nil
}

func (s *lipSyncStub) Result(_ context.Context, id string) (providers.LipSyncResult, error) {
	answer := s.results[min(s.polled, len(s.results)-1)]
	s.polled++
	return answer.result, answer.err
}

func (s *lipSyncStub) Download(_ context.Context, url string) ([]byte, error) {
	s.downloads = append(s.downloads, url)
	return s.video, nil
}

// lipSyncActivities returns activities over a fresh object store holding a
// portrait and turn 3's recording.
func lipSyncActivities(t *testing.T, stub *lipSyncStub) (*Activities, objectstore.Store) {
	t.Helper()
	store, err := objectstore.NewFS(t.TempDir())
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, store.Put(ctx, "rooms/room-1/portrait-a", bytes.NewReader([]byte("portrait")), "image/webp"))
	require.NoError(t, store.Put(ctx, AudioKey("perf-1", 3), bytes.NewReader([]byte("recording")), "audio/mpeg"))
	return &Activities{Objects: store, LipSync: stub, LipSyncPollInterval: time.Millisecond}, store
}

func activityEnv(a *Activities) *testsuite.TestActivityEnvironment {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(a)
	return env
}

func TestUploadLipSyncInputsSendsThePortraitAndTheRecording(t *testing.T) {
	stub := &lipSyncStub{}
	a, _ := lipSyncActivities(t, stub)

	value, err := activityEnv(a).ExecuteActivity(a.UploadLipSyncInputs, LipSyncInputs{
		PerformanceID: "perf-1", RoomID: "room-1", Turn: 3, PortraitKey: "rooms/room-1/portrait-a",
	})
	require.NoError(t, err)
	var uploaded LipSyncUploads
	require.NoError(t, value.Get(&uploaded))

	require.Equal(t, []providers.UploadRequest{
		{RoomID: "room-1", Turn: 3, Filename: "perf-1-turn-3-portrait.webp", ContentType: "image/webp", Data: []byte("portrait")},
		{RoomID: "room-1", Turn: 3, Filename: "perf-1-turn-3.mp3", ContentType: "audio/mpeg", Data: []byte("recording")},
	}, stub.uploads)
	require.Equal(t, LipSyncUploads{
		ImageURL: "https://files.example/perf-1-turn-3-portrait.webp",
		AudioURL: "https://files.example/perf-1-turn-3.mp3",
	}, uploaded)
}

func TestSubmitLipSyncSendsTheUploadsAndTheResolution(t *testing.T) {
	stub := &lipSyncStub{}
	a, _ := lipSyncActivities(t, stub)

	value, err := activityEnv(a).ExecuteActivity(a.SubmitLipSync, LipSyncSubmission{
		RoomID: "room-1", Turn: 3, ImageURL: "https://files.example/p.webp", AudioURL: "https://files.example/a.mp3", Resolution: "720p",
	})
	require.NoError(t, err)
	var id string
	require.NoError(t, value.Get(&id))
	require.Equal(t, "pred_1", id)
	require.Equal(t, []providers.LipSyncRequest{{
		RoomID: "room-1", Turn: 3, ImageURL: "https://files.example/p.webp", AudioURL: "https://files.example/a.mp3", Resolution: "720p",
	}}, stub.submitted)
}

func TestAwaitLipSyncPollsWithAHeartbeatThenStoresTheVideo(t *testing.T) {
	pending := resultAnswer{result: providers.LipSyncResult{Status: providers.LipSyncPending}}
	stub := &lipSyncStub{
		results: []resultAnswer{
			pending,
			{err: errors.New("status 503: busy")}, // worth waiting through
			pending,
			{result: providers.LipSyncResult{Status: providers.LipSyncCompleted, VideoURL: "https://files.example/out.mp4"}},
		},
		video: []byte("video bytes"),
	}
	a, store := lipSyncActivities(t, stub)
	env := activityEnv(a)
	heartbeats := 0
	env.SetOnActivityHeartbeatListener(func(_ *activity.Info, _ converter.EncodedValues) { heartbeats++ })

	value, err := env.ExecuteActivity(a.AwaitLipSync, LipSyncWait{PerformanceID: "perf-1", RoomID: "room-1", Turn: 3, PredictionID: "pred_1"})
	require.NoError(t, err)
	var key string
	require.NoError(t, value.Get(&key))

	require.Equal(t, VideoKey("perf-1", 3), key)
	require.Equal(t, "performances/perf-1/turn-3.mp4", key)
	require.Equal(t, 4, stub.polled, "it asked until the video was done")
	// The SDK throttles heartbeats, and its listener sees only those sent on,
	// so this says at least one, not one per answer.
	require.GreaterOrEqual(t, heartbeats, 1, "it said it was alive while waiting")
	require.Equal(t, []string{"https://files.example/out.mp4"}, stub.downloads)

	stored, err := store.Get(context.Background(), key)
	require.NoError(t, err)
	defer stored.Body.Close()
	body, _ := io.ReadAll(stored.Body)
	require.Equal(t, []byte("video bytes"), body)
	require.Equal(t, "video/mp4", stored.ContentType)
}

func TestAwaitLipSyncStopsForGoodWhenTheVideoFails(t *testing.T) {
	stub := &lipSyncStub{results: []resultAnswer{{result: providers.LipSyncResult{Status: providers.LipSyncFailed, Error: "no face found"}}}}
	a, _ := lipSyncActivities(t, stub)

	_, err := activityEnv(a).ExecuteActivity(a.AwaitLipSync, LipSyncWait{PerformanceID: "perf-1", RoomID: "room-1", Turn: 3, PredictionID: "pred_1"})
	requireNotRetryable(t, err, "no face found")
	require.Empty(t, stub.downloads)
}

func TestAwaitLipSyncStopsForGoodOnAnAnswerNotWorthRetrying(t *testing.T) {
	stub := &lipSyncStub{results: []resultAnswer{{err: providers.Permanent(errors.New("status 401: bad key"))}}}
	a, _ := lipSyncActivities(t, stub)

	_, err := activityEnv(a).ExecuteActivity(a.AwaitLipSync, LipSyncWait{PerformanceID: "perf-1", RoomID: "room-1", Turn: 3, PredictionID: "pred_1"})
	requireNotRetryable(t, err, "bad key")
	require.Equal(t, 1, stub.polled)
}

func requireNotRetryable(t *testing.T, err error, contains string) {
	t.Helper()
	require.ErrorContains(t, err, contains)
	var app *temporal.ApplicationError
	require.True(t, errors.As(err, &app), "an application error: %v", err)
	require.True(t, app.NonRetryable(), "not worth retrying: %v", err)
}
