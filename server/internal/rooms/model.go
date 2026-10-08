// Package rooms handles room creation and reads. A room is the director's
// immutable setup: a scene, two characters, and their portraits.
package rooms

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits on submitted text, in characters (docs/system-design.md, section 5).
const (
	maxNameLength        = 80
	maxBriefLength       = 400
	maxTopicLength       = 200
	maxDescriptionLength = 1000
	maxGenreLength       = 80
)

// Scene is the room's scene context.
type Scene struct {
	Topic       string `json:"topic"`
	Description string `json:"description"`
	Genre       string `json:"genre"`
}

// CharacterInput is a character as submitted.
type CharacterInput struct {
	Name  string `json:"name"`
	Brief string `json:"brief"`
}

// CreateInput is the JSON part of a room submission.
type CreateInput struct {
	Scene      Scene          `json:"scene"`
	CharacterA CharacterInput `json:"characterA"`
	CharacterB CharacterInput `json:"characterB"`
}

// Normalize trims surrounding whitespace from every field.
func (in *CreateInput) Normalize() {
	for _, field := range []*string{
		&in.Scene.Topic, &in.Scene.Description, &in.Scene.Genre,
		&in.CharacterA.Name, &in.CharacterA.Brief,
		&in.CharacterB.Name, &in.CharacterB.Brief,
	} {
		*field = strings.TrimSpace(*field)
	}
}

// Validate returns what is wrong with each field, keyed by field name, or an
// empty map. Call Normalize first.
func (in *CreateInput) Validate() map[string]string {
	problems := map[string]string{}
	required := func(field, value string, max int) {
		switch {
		case value == "":
			problems[field] = "Required."
		case utf8.RuneCountInString(value) > max:
			problems[field] = tooLong(max)
		}
	}
	optional := func(field, value string, max int) {
		if utf8.RuneCountInString(value) > max {
			problems[field] = tooLong(max)
		}
	}

	required("scene.topic", in.Scene.Topic, maxTopicLength)
	optional("scene.description", in.Scene.Description, maxDescriptionLength)
	optional("scene.genre", in.Scene.Genre, maxGenreLength)
	required("characterA.name", in.CharacterA.Name, maxNameLength)
	optional("characterA.brief", in.CharacterA.Brief, maxBriefLength)
	required("characterB.name", in.CharacterB.Name, maxNameLength)
	optional("characterB.brief", in.CharacterB.Brief, maxBriefLength)
	return problems
}

func tooLong(max int) string {
	return "Must be at most " + strconv.Itoa(max) + " characters."
}

// Character is one of a room's two characters as stored.
type Character struct {
	Name        string
	Brief       string
	VoiceID     string
	PortraitKey string
}

// Room is a stored room.
type Room struct {
	ID         string
	DirectorID string
	Scene      Scene
	CharacterA Character
	CharacterB Character
	CreatedAt  time.Time
}

// Summary is one entry in a director's room list.
type Summary struct {
	ID        string
	Topic     string
	CreatedAt time.Time
}
