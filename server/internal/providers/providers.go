// Package providers defines the external services the performance workflow
// calls: a language model for dialogue and a speech service. Real
// implementations are added in step 11; fakes live in the fake subpackage.
package providers

import "context"

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
	Text    string
}

// SpeechRequest asks for one line to be spoken in one voice.
type SpeechRequest struct {
	RoomID  string
	Turn    int
	Attempt int
	VoiceID string
	Text    string
}

// DialogueModel generates dialogue choices and enhances the winner.
type DialogueModel interface {
	GenerateChoices(ctx context.Context, req ChoicesRequest) ([]string, error)
	EnhanceTags(ctx context.Context, req TagsRequest) (string, error)
}

// Speech turns a tagged line into MP3 audio.
type Speech interface {
	Synthesize(ctx context.Context, req SpeechRequest) ([]byte, error)
}
