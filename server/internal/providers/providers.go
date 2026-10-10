// Package providers defines the external services the performance workflow
// calls: a language model for dialogue and a speech service. The anthropic
// and elevenlabs subpackages are the real ones; fake holds scripted
// stand-ins for tests and for running without API keys.
package providers

import (
	"context"
	"errors"
)

// Scene is the room's immutable scene context.
type Scene struct {
	Topic       string
	Description string
	Genre       string
}

// Character is one of the room's two characters.
type Character struct {
	Name  string
	Brief string
}

// Line is one selected line of dialogue. Speaker is "A" or "B".
type Line struct {
	Speaker string
	Text    string
}

// ChoicesRequest asks for three candidate lines for one turn.
//
// Attempt is the activity attempt number, starting at 1. It is recorded with
// the result and lets fakes inject failures without keeping state.
type ChoicesRequest struct {
	RoomID         string
	Turn           int
	Attempt        int
	Scene          Scene
	CharacterA     Character
	CharacterB     Character
	Speaker        string
	Beat           string
	History        []Line
	TurnsRemaining int
}

// TagsRequest asks for audio tags to be added to a winning line without
// changing its words.
type TagsRequest struct {
	RoomID  string
	Turn    int
	Attempt int
	Speaker string
	// Character is who says the line, and Beat the turn's story beat, so
	// the delivery can suit them.
	Character Character
	Beat      string
	Text      string
}

// SpeechRequest asks for one line to be spoken in one voice.
type SpeechRequest struct {
	RoomID  string
	Turn    int
	Attempt int
	VoiceID string
	Text    string
}

// Choices is a turn's candidate lines and the model that wrote them.
type Choices struct {
	Lines []string
	// Model is the identifier of the model that answered, recorded in the
	// final script so a saved performance stays interpretable after the
	// configured model changes (docs/decisions.md, D017).
	Model string
}

// Tagged is a line with audio tags added, and the model that added them.
type Tagged struct {
	Text  string
	Model string
}

// DialogueModel generates dialogue choices and enhances the winner.
//
// Implementations return what the model said. Checking it (three distinct
// lines, words preserved) is done by the caller with ValidateChoices and
// ValidateTagged, so the same rules hold for every model.
type DialogueModel interface {
	GenerateChoices(ctx context.Context, req ChoicesRequest) (Choices, error)
	EnhanceTags(ctx context.Context, req TagsRequest) (Tagged, error)
}

// Speech turns a tagged line into MP3 audio.
type Speech interface {
	Synthesize(ctx context.Context, req SpeechRequest) ([]byte, error)
}

// permanentError marks a failure that trying again cannot fix.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// Permanent marks err as not worth retrying: a wrong API key, a request the
// provider rejects as malformed, an unknown voice. Other errors (timeouts,
// rate limits, server errors, an answer that fails validation) are retried.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err: err}
}

// IsPermanent reports whether err was marked with Permanent.
func IsPermanent(err error) bool {
	var p *permanentError
	return errors.As(err, &p)
}

// PermanentStatus reports whether an HTTP status from a provider means the
// request itself is wrong, so sending it again would fail the same way.
// 408 and 429 are the client-error codes that are worth retrying.
func PermanentStatus(status int) bool {
	return status >= 400 && status < 500 && status != 408 && status != 429
}
