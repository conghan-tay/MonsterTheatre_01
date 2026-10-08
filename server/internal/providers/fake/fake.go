package fake

import (
	"bytes"
	"context"
	"fmt"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/providers"
)

// Dialogue is a scripted providers.DialogueModel.
type Dialogue struct {
	Scripts Scripts
}

var _ providers.DialogueModel = Dialogue{}

// GenerateChoices returns the scripted lines for the turn, or three
// predictable defaults.
func (d Dialogue) GenerateChoices(_ context.Context, req providers.ChoicesRequest) ([]string, error) {
	script, err := d.Scripts.For(req.RoomID)
	if err != nil {
		return nil, err
	}
	if script.fails(OpChoices, req.Turn, req.Attempt) {
		return nil, ErrInjected
	}
	if lines, ok := script.Choices[req.Turn]; ok {
		return lines, nil
	}
	return []string{
		fmt.Sprintf("Turn %d, first option.", req.Turn),
		fmt.Sprintf("Turn %d, second option.", req.Turn),
		fmt.Sprintf("Turn %d, third option.", req.Turn),
	}, nil
}

// EnhanceTags prefixes one fixed tag and leaves the words unchanged.
func (d Dialogue) EnhanceTags(_ context.Context, req providers.TagsRequest) (string, error) {
	script, err := d.Scripts.For(req.RoomID)
	if err != nil {
		return "", err
	}
	if script.fails(OpTags, req.Turn, req.Attempt) {
		return "", ErrInjected
	}
	return "[excited] " + req.Text, nil
}

// Speech is a scripted providers.Speech that returns silence.
type Speech struct {
	Scripts Scripts
}

var _ providers.Speech = Speech{}

// Synthesize returns a short silent MP3.
func (s Speech) Synthesize(_ context.Context, req providers.SpeechRequest) ([]byte, error) {
	script, err := s.Scripts.For(req.RoomID)
	if err != nil {
		return nil, err
	}
	if script.fails(OpSpeech, req.Turn, req.Attempt) {
		return nil, ErrInjected
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
