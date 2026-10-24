package fake

import (
	"bytes"
	"context"
	"fmt"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/providers"
)

// ModelName is what the fake language model calls itself in a saved script.
const ModelName = "fake"

// Dialogue is a scripted providers.DialogueModel.
type Dialogue struct {
	Scripts Scripts
}

var _ providers.DialogueModel = Dialogue{}

// GenerateChoices returns the scripted lines for the turn, or three
// predictable defaults.
func (d Dialogue) GenerateChoices(ctx context.Context, req providers.ChoicesRequest) (providers.Choices, error) {
	script, err := d.Scripts.For(req.RoomID)
	if err != nil {
		return providers.Choices{}, err
	}
	if script.fails(OpChoices, req.Turn, req.Attempt) {
		return providers.Choices{}, ErrInjected
	}
	if err := script.wait(ctx, OpChoices, req.Turn); err != nil {
		return providers.Choices{}, err
	}
	if lines, ok := script.Choices[req.Turn]; ok {
		return providers.Choices{Lines: lines, Model: ModelName}, nil
	}
	return providers.Choices{Model: ModelName, Lines: []string{
		fmt.Sprintf("Turn %d, first option.", req.Turn),
		fmt.Sprintf("Turn %d, second option.", req.Turn),
		fmt.Sprintf("Turn %d, third option.", req.Turn),
	}}, nil
}

// EnhanceTags prefixes one fixed tag and leaves the words unchanged.
func (d Dialogue) EnhanceTags(ctx context.Context, req providers.TagsRequest) (providers.Tagged, error) {
	script, err := d.Scripts.For(req.RoomID)
	if err != nil {
		return providers.Tagged{}, err
	}
	if script.fails(OpTags, req.Turn, req.Attempt) {
		return providers.Tagged{}, ErrInjected
	}
	if err := script.wait(ctx, OpTags, req.Turn); err != nil {
		return providers.Tagged{}, err
	}
	return providers.Tagged{Text: "[excited] " + req.Text, Model: ModelName}, nil
}

// Speech is a scripted providers.Speech that returns silence.
type Speech struct {
	Scripts Scripts
}

var _ providers.Speech = Speech{}

// Synthesize returns a short silent MP3.
func (s Speech) Synthesize(ctx context.Context, req providers.SpeechRequest) ([]byte, error) {
	script, err := s.Scripts.For(req.RoomID)
	if err != nil {
		return nil, err
	}
	if script.fails(OpSpeech, req.Turn, req.Attempt) {
		return nil, ErrInjected
	}
	if err := script.wait(ctx, OpSpeech, req.Turn); err != nil {
		return nil, err
	}
	return SilentMP3(), nil
}

const (
	mp3FrameSize   = 417 // 144 * 128000 / 44100, no padding
	silentMP3Count = 10  // about a quarter of a second
)

// SilentMP3 builds a short silent MP3 in code: MPEG-1 Layer III frames at
// 128 kbit/s and 44.1 kHz whose payload is all zeros. Audio fixtures are
// generated this way because *.mp3 files are ignored by git.
func SilentMP3() []byte {
	frame := make([]byte, mp3FrameSize)
	copy(frame, []byte{0xFF, 0xFB, 0x90, 0x64})
	return bytes.Repeat(frame, silentMP3Count)
}
