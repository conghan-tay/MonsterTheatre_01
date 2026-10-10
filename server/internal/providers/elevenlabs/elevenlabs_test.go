package elevenlabs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/providers"
)

var speechRequest = providers.SpeechRequest{
	RoomID: "room-1", Turn: 2, Attempt: 1, VoiceID: "voice-b", Text: "[nervous] Ask the cat.",
}

// serve runs a stand-in for the ElevenLabs API and returns an adapter
// pointed at it.
func serve(t *testing.T, handler http.HandlerFunc) *Speech {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Speech{APIKey: "test-key", BaseURL: srv.URL}
}

func TestSynthesizeSendsOneLineInOneVoiceAndReturnsTheAudio(t *testing.T) {
	mp3 := []byte{0xFF, 0xFB, 0x90, 0x64, 1, 2, 3}
	var seen *http.Request
	var body map[string]any
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		seen = r.Clone(context.Background())
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &body))
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write(mp3)
	})

	audio, err := s.Synthesize(context.Background(), speechRequest)
	require.NoError(t, err)
	require.Equal(t, mp3, audio)

	require.Equal(t, http.MethodPost, seen.Method)
	require.Equal(t, "/v1/text-to-dialogue", seen.URL.Path)
	require.Equal(t, "mp3_44100_128", seen.URL.Query().Get("output_format"))
	require.Equal(t, "test-key", seen.Header.Get("xi-api-key"))
	require.Equal(t, "application/json", seen.Header.Get("Content-Type"))
	require.Equal(t, map[string]any{
		"inputs":   []any{map[string]any{"text": "[nervous] Ask the cat.", "voice_id": "voice-b"}},
		"model_id": DefaultModel,
	}, body, "the tagged text, the speaker's voice, and the model; nothing else")
}

func TestSynthesizeUsesTheConfiguredModel(t *testing.T) {
	var body map[string]any
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte{1})
	})
	s.Model = "eleven_v3"
	_, err := s.Synthesize(context.Background(), speechRequest)
	require.NoError(t, err)
	require.Equal(t, "eleven_v3", body["model_id"])
}

func TestHTTPErrorsAreSortedIntoRetryableAndPermanent(t *testing.T) {
	for status, permanent := range map[int]bool{
		http.StatusUnauthorized:        true,
		http.StatusUnprocessableEntity: true,
		http.StatusNotFound:            true,
		http.StatusTooManyRequests:     false,
		http.StatusInternalServerError: false,
		http.StatusBadGateway:          false,
	} {
		s := serve(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"detail":{"message":"voice not found"}}`)
		})
		_, err := s.Synthesize(context.Background(), speechRequest)
		require.Error(t, err, "status %d", status)
		require.Equal(t, permanent, providers.IsPermanent(err), "status %d: %v", status, err)
		require.ErrorContains(t, err, "voice not found", "the provider's explanation is kept for the operator")
		require.NotContains(t, err.Error(), "test-key")
	}
}

func TestSynthesizeRefusesAResponseThatIsNotAudio(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"an empty body": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "audio/mpeg")
		},
		"JSON with a 200": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"queued":true}`)
		},
	}
	for name, handler := range cases {
		_, err := serve(t, handler).Synthesize(context.Background(), speechRequest)
		require.Error(t, err, name)
	}
}

func TestSynthesizeStopsWhenItsContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := serve(t, func(http.ResponseWriter, *http.Request) {
		cancel()
		<-ctx.Done()
	})
	_, err := s.Synthesize(ctx, speechRequest)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, providers.IsPermanent(err))
}
