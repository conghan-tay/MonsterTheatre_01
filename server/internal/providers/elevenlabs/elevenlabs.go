// Package elevenlabs is the speech adapter backed by ElevenLabs. It makes
// one plain HTTP request per line, so no SDK is used.
package elevenlabs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/providers"
)

const (
	// DefaultModel is used when TTS_MODEL is not set
	// (docs/system-design.md, section 2).
	DefaultModel = "eleven_v4_turbo"
	// DefaultBaseURL is the ElevenLabs API.
	DefaultBaseURL = "https://api.elevenlabs.io"

	// outputFormat is MP3 at 44.1 kHz and 128 kbit/s: what every browser's
	// audio element plays.
	outputFormat = "mp3_44100_128"
	// maxAudioBytes bounds one recording. A line of 300 characters is a few
	// hundred kilobytes.
	maxAudioBytes = 10 << 20
	// requestTimeout is under the activity's own 60 seconds, so a hung
	// request ends with a clear error from here.
	requestTimeout = 55 * time.Second
)

// Speech is a providers.Speech that asks ElevenLabs.
type Speech struct {
	// APIKey authenticates the request. Model is the ElevenLabs model ID.
	APIKey string
	Model  string
	// BaseURL and HTTP default to the real API and a client with a timeout.
	// Tests point them at a local server.
	BaseURL string
	HTTP    *http.Client
}

var _ providers.Speech = (*Speech)(nil)

type dialogueInput struct {
	Text    string `json:"text"`
	VoiceID string `json:"voice_id"`
}

type dialogueRequest struct {
	Inputs  []dialogueInput `json:"inputs"`
	ModelID string          `json:"model_id"`
}

// Synthesize voices one tagged line with the Text to Dialogue endpoint and
// returns the MP3. The audio tags in the text are performed, not read out.
func (s *Speech) Synthesize(ctx context.Context, req providers.SpeechRequest) ([]byte, error) {
	model, base, client := s.Model, s.BaseURL, s.HTTP
	if model == "" {
		model = DefaultModel
	}
	if base == "" {
		base = DefaultBaseURL
	}
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}

	body, err := json.Marshal(dialogueRequest{
		Inputs:  []dialogueInput{{Text: req.Text, VoiceID: req.VoiceID}},
		ModelID: model,
	})
	if err != nil {
		return nil, providers.Permanent(err)
	}
	url := strings.TrimRight(base, "/") + "/v1/text-to-dialogue?output_format=" + outputFormat
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, providers.Permanent(err)
	}
	httpReq.Header.Set("xi-api-key", s.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "audio/mpeg")

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("text to speech: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// The start of the body says what was wrong (an unknown voice, an
		// unsupported model, no credit left). It never contains the key.
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		err := fmt.Errorf("text to speech: status %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
		if providers.PermanentStatus(resp.StatusCode) {
			return nil, providers.Permanent(err)
		}
		return nil, err
	}

	audio, err := io.ReadAll(io.LimitReader(resp.Body, maxAudioBytes+1))
	if err != nil {
		return nil, fmt.Errorf("text to speech: read audio: %w", err)
	}
	switch {
	case len(audio) == 0:
		return nil, errors.New("text to speech: the response had no audio")
	case len(audio) > maxAudioBytes:
		return nil, providers.Permanent(errors.New("text to speech: the recording is larger than expected"))
	case !strings.HasPrefix(resp.Header.Get("Content-Type"), "audio/"):
		return nil, fmt.Errorf("text to speech: expected audio, got %q", resp.Header.Get("Content-Type"))
	}
	return audio, nil
}
