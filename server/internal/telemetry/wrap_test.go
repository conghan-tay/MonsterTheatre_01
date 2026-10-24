package telemetry

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/objectstore"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/providers"
)

type stubDialogue struct{ err error }

func (s stubDialogue) GenerateChoices(context.Context, providers.ChoicesRequest) (providers.Choices, error) {
	return providers.Choices{Lines: []string{"a", "b", "c"}}, s.err
}
func (s stubDialogue) EnhanceTags(context.Context, providers.TagsRequest) (providers.Tagged, error) {
	return providers.Tagged{Text: "x"}, s.err
}

type stubSpeech struct{}

func (stubSpeech) Synthesize(context.Context, providers.SpeechRequest) ([]byte, error) {
	return []byte("mp3"), nil
}

type stubLipSync struct{}

func (stubLipSync) Upload(context.Context, providers.UploadRequest) (string, error) { return "u", nil }
func (stubLipSync) Submit(context.Context, providers.LipSyncRequest) (string, error) {
	return "p", nil
}
func (stubLipSync) Result(context.Context, string) (providers.LipSyncResult, error) {
	return providers.LipSyncResult{}, nil
}
func (stubLipSync) Download(context.Context, string) ([]byte, error) { return nil, nil }

type stubStore struct{ err error }

func (s stubStore) Put(_ context.Context, _ string, body io.Reader, _ string) error {
	_, _ = io.Copy(io.Discard, body)
	return s.err
}
func (s stubStore) Get(context.Context, string) (*objectstore.Object, error) { return nil, nil }

func TestEveryProviderCallIsLoggedWithItsProviderAndOperation(t *testing.T) {
	logger, lines := captured()
	ctx := WithFields(context.Background(), []any{"room_id", "room-1"})
	d := Dialogue{Next: stubDialogue{}, Provider: "anthropic", Logger: logger}
	s := Speech{Next: stubSpeech{}, Provider: "elevenlabs", Logger: logger}
	l := LipSync{Next: stubLipSync{}, Provider: "wavespeed", Logger: logger}

	got, err := d.GenerateChoices(ctx, providers.ChoicesRequest{})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b", "c"}, got.Lines, "the answer is passed on")
	_, _ = d.EnhanceTags(ctx, providers.TagsRequest{})
	audio, _ := s.Synthesize(ctx, providers.SpeechRequest{})
	require.Equal(t, []byte("mp3"), audio)
	_, _ = l.Upload(ctx, providers.UploadRequest{})
	_, _ = l.Submit(ctx, providers.LipSyncRequest{})
	_, _ = l.Result(ctx, "p")
	_, _ = l.Download(ctx, "u")

	var ops []string
	for _, line := range only(lines(), "provider call") {
		require.Equal(t, "room-1", line["room_id"])
		require.Equal(t, "ok", line["outcome"])
		ops = append(ops, line["provider"].(string)+"/"+line["op"].(string))
	}
	require.Equal(t, []string{
		"anthropic/choices", "anthropic/tags", "elevenlabs/speech",
		"wavespeed/lipsync_upload", "wavespeed/lipsync_submit", "wavespeed/lipsync_result", "wavespeed/lipsync_download",
	}, ops)
}

func TestAFailedProviderCallIsLoggedAndItsErrorPassedOn(t *testing.T) {
	logger, lines := captured()
	boom := errors.New("overloaded")
	d := Dialogue{Next: stubDialogue{err: boom}, Provider: "anthropic", Logger: logger}
	_, err := d.GenerateChoices(context.Background(), providers.ChoicesRequest{})
	require.ErrorIs(t, err, boom)
	got := only(lines(), "provider call")
	require.Len(t, got, 1)
	require.Equal(t, "failed", got[0]["outcome"])
	require.Equal(t, "overloaded", got[0]["error"])
}

func TestEveryUploadIsLoggedWithItsKeyAndSize(t *testing.T) {
	logger, lines := captured()
	ctx := WithFields(context.Background(), []any{"performance_id", "perf-1"})
	ok := Store{Next: stubStore{}, Logger: logger}
	require.NoError(t, ok.Put(ctx, "performances/perf-1/turn-2.mp3", strings.NewReader("12345"), "audio/mpeg"))
	bad := Store{Next: stubStore{err: errors.New("rename: is a directory")}, Logger: logger}
	require.Error(t, bad.Put(ctx, "performances/perf-1/turn-3.mp3", strings.NewReader("1"), "audio/mpeg"))

	got := only(lines(), "object put")
	require.Len(t, got, 2)
	require.Equal(t, "performances/perf-1/turn-2.mp3", got[0]["key"])
	require.EqualValues(t, 5, got[0]["bytes"])
	require.Equal(t, "perf-1", got[0]["performance_id"])
	require.Equal(t, "ok", got[0]["outcome"])
	require.Equal(t, "failed", got[1]["outcome"])
	require.Equal(t, "rename: is a directory", got[1]["error"])
}
