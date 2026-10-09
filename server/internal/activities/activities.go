// Package activities holds everything the performance workflow does that
// touches the outside world: the database, Redis, the object store, the
// language model, and the speech service. Workflow code may do none of
// that itself.
package activities

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/objectstore"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/performance"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/providers"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/rooms"
)

// streamMaxLen is roughly how many updates the stream keeps. Nothing reads
// history (snapshots come from the workflow), so this only bounds memory.
const streamMaxLen = 1000

// RoomReader loads a room. *rooms.PGStore implements it.
type RoomReader interface {
	Get(ctx context.Context, id string) (rooms.Room, error)
}

// Completer saves a finished performance. *performance.PGStore implements it.
type Completer interface {
	Complete(ctx context.Context, id string, script performance.Script, audioKeys []string, now time.Time) error
}

// StreamAdder appends to a Redis stream. *redis.Client implements it.
type StreamAdder interface {
	XAdd(ctx context.Context, a *redis.XAddArgs) *redis.StringCmd
}

// Activities are the performance workflow's activities. The worker builds
// one and registers it; every exported method is an activity.
type Activities struct {
	Rooms        RoomReader
	Performances Completer
	Stream       StreamAdder
	Objects      objectstore.Store
	Dialogue     providers.DialogueModel
	Speech       providers.Speech
	// Model names the language model, for the final script.
	Model string
	// VoteWindow and PremiereCountdown are handed to the workflow by
	// FetchContext, so that the values a run used are in its history.
	VoteWindow        time.Duration
	PremiereCountdown time.Duration
	Now               func() time.Time
}

// Character is a character as the workflow needs it.
type Character struct {
	Name    string
	Brief   string
	VoiceID string
}

// RoomContext is everything a run needs to know before its first turn.
type RoomContext struct {
	Scene             providers.Scene
	CharacterA        Character
	CharacterB        Character
	Model             string
	VoteWindow        time.Duration
	PremiereCountdown time.Duration
}

// FetchContext loads the room's scene and characters, and the run's settings.
func (a *Activities) FetchContext(ctx context.Context, roomID string) (RoomContext, error) {
	room, err := a.Rooms.Get(ctx, roomID)
	if errors.Is(err, rooms.ErrNotFound) {
		return RoomContext{}, temporal.NewNonRetryableApplicationError("room not found", "RoomNotFound", err)
	}
	if err != nil {
		return RoomContext{}, err
	}
	character := func(c rooms.Character) Character {
		return Character{Name: c.Name, Brief: c.Brief, VoiceID: c.VoiceID}
	}
	return RoomContext{
		Scene:             providers.Scene{Topic: room.Scene.Topic, Description: room.Scene.Description, Genre: room.Scene.Genre},
		CharacterA:        character(room.CharacterA),
		CharacterB:        character(room.CharacterB),
		Model:             a.Model,
		VoteWindow:        a.VoteWindow,
		PremiereCountdown: a.PremiereCountdown,
	}, nil
}

// ChoicesInput asks for one turn's three candidate lines.
type ChoicesInput struct {
	RoomID         string
	Turn           int
	Speaker        string
	Beat           string
	Scene          providers.Scene
	CharacterA     Character
	CharacterB     Character
	History        []providers.Line
	TurnsRemaining int
}

// GenerateChoices asks the language model for a turn's candidate lines.
func (a *Activities) GenerateChoices(ctx context.Context, in ChoicesInput) ([]string, error) {
	return a.Dialogue.GenerateChoices(ctx, providers.ChoicesRequest{
		RoomID:         in.RoomID,
		Turn:           in.Turn,
		Attempt:        int(activity.GetInfo(ctx).Attempt),
		Scene:          in.Scene,
		CharacterA:     providers.Character{Name: in.CharacterA.Name, Brief: in.CharacterA.Brief},
		CharacterB:     providers.Character{Name: in.CharacterB.Name, Brief: in.CharacterB.Brief},
		Speaker:        in.Speaker,
		Beat:           in.Beat,
		History:        in.History,
		TurnsRemaining: in.TurnsRemaining,
	})
}

// TagsInput asks for audio tags on a winning line.
type TagsInput struct {
	RoomID  string
	Turn    int
	Speaker string
	Text    string
}

// EnhanceTags asks the language model to add audio tags to a winning line.
func (a *Activities) EnhanceTags(ctx context.Context, in TagsInput) (string, error) {
	return a.Dialogue.EnhanceTags(ctx, providers.TagsRequest{
		RoomID:  in.RoomID,
		Turn:    in.Turn,
		Attempt: int(activity.GetInfo(ctx).Attempt),
		Speaker: in.Speaker,
		Text:    in.Text,
	})
}

// SpeechInput asks for one tagged line to be voiced and saved.
type SpeechInput struct {
	PerformanceID string
	RoomID        string
	Turn          int
	VoiceID       string
	Text          string
}

// AudioKey is where a turn's recording is stored.
func AudioKey(performanceID string, turn int) string {
	return fmt.Sprintf("performances/%s/turn-%d.mp3", performanceID, turn)
}

// Synthesize voices a tagged line, saves the recording, and returns its key.
// The audio itself never enters workflow history. Saving replaces whatever
// is at the key, so a retry is harmless.
func (a *Activities) Synthesize(ctx context.Context, in SpeechInput) (string, error) {
	audio, err := a.Speech.Synthesize(ctx, providers.SpeechRequest{
		RoomID:  in.RoomID,
		Turn:    in.Turn,
		Attempt: int(activity.GetInfo(ctx).Attempt),
		VoiceID: in.VoiceID,
		Text:    in.Text,
	})
	if err != nil {
		return "", err
	}
	key := AudioKey(in.PerformanceID, in.Turn)
	if err := a.Objects.Put(ctx, key, bytes.NewReader(audio), "audio/mpeg"); err != nil {
		return "", err
	}
	return key, nil
}

// Publish appends one update to the Redis stream, from which the server
// forwards it to the room's browsers. A retry can append the same update
// twice; browsers ignore a revision they already have.
func (a *Activities) Publish(ctx context.Context, update performance.Update) error {
	message, err := json.Marshal(performance.Message{
		Type:     performance.TypeUpdate,
		Revision: update.Revision,
		State:    update.State,
	})
	if err != nil {
		return temporal.NewNonRetryableApplicationError("encode update", "EncodeUpdate", err)
	}
	return a.Stream.XAdd(ctx, &redis.XAddArgs{
		Stream: performance.StreamKey,
		MaxLen: streamMaxLen,
		Approx: true,
		Values: map[string]any{
			performance.StreamFieldRoomID:   update.RoomID,
			performance.StreamFieldRevision: strconv.Itoa(update.Revision),
			performance.StreamFieldMessage:  message,
		},
	}).Err()
}

// CompleteInput is a finished performance.
type CompleteInput struct {
	PerformanceID string
	Script        performance.Script
	AudioKeys     []string
}

// Complete saves the final script and audio keys and marks the performance
// completed.
func (a *Activities) Complete(ctx context.Context, in CompleteInput) error {
	return a.Performances.Complete(ctx, in.PerformanceID, in.Script, in.AudioKeys, a.Now())
}
