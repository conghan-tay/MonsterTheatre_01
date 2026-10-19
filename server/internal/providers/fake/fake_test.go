package fake

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/providers"
)

func scriptsWith(t *testing.T, roomID, script string) Scripts {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, roomID+".json"), []byte(script), 0o644))
	return Scripts{Dir: dir}
}

func TestDialogueDefaultsWhenRoomHasNoScript(t *testing.T) {
	ctx := context.Background()
	for name, scripts := range map[string]Scripts{
		"no directory":    {},
		"empty directory": {Dir: t.TempDir()},
	} {
		lines, err := Dialogue{Scripts: scripts}.GenerateChoices(ctx, providers.ChoicesRequest{RoomID: "room1", Turn: 3, Attempt: 1})
		require.NoError(t, err, name)
		require.Equal(t, providers.Choices{
			Lines: []string{"Turn 3, first option.", "Turn 3, second option.", "Turn 3, third option."},
			Model: "fake",
		}, lines, name)
	}
}

func TestDialogueReturnsScriptedChoicesForThatRoomAndTurnOnly(t *testing.T) {
	ctx := context.Background()
	d := Dialogue{Scripts: scriptsWith(t, "room1", `{"choices": {"2": ["a", "b", "c"]}}`)}

	scripted, err := d.GenerateChoices(ctx, providers.ChoicesRequest{RoomID: "room1", Turn: 2, Attempt: 1})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b", "c"}, scripted.Lines)

	otherTurn, err := d.GenerateChoices(ctx, providers.ChoicesRequest{RoomID: "room1", Turn: 3, Attempt: 1})
	require.NoError(t, err)
	require.Equal(t, "Turn 3, first option.", otherTurn.Lines[0])

	otherRoom, err := d.GenerateChoices(ctx, providers.ChoicesRequest{RoomID: "room2", Turn: 2, Attempt: 1})
	require.NoError(t, err)
	require.Equal(t, "Turn 2, first option.", otherRoom.Lines[0])
}

func TestEnhanceTagsKeepsTheWords(t *testing.T) {
	tagged, err := Dialogue{}.EnhanceTags(context.Background(), providers.TagsRequest{RoomID: "room1", Turn: 1, Attempt: 1, Text: "Hello there."})
	require.NoError(t, err)
	require.Equal(t, providers.Tagged{Text: "[excited] Hello there.", Model: "fake"}, tagged)
}

func TestScriptedFailureLastsForTheGivenAttemptsOnThatOperationAndTurn(t *testing.T) {
	ctx := context.Background()
	scripts := scriptsWith(t, "room1", `{"failures": [{"op": "speech", "turn": 2, "attempts": 3}]}`)
	speech := Speech{Scripts: scripts}

	for attempt := 1; attempt <= 3; attempt++ {
		_, err := speech.Synthesize(ctx, providers.SpeechRequest{RoomID: "room1", Turn: 2, Attempt: attempt})
		require.ErrorIs(t, err, ErrInjected, "attempt %d", attempt)
	}
	_, err := speech.Synthesize(ctx, providers.SpeechRequest{RoomID: "room1", Turn: 2, Attempt: 4})
	require.NoError(t, err, "attempt 4 succeeds")

	_, err = speech.Synthesize(ctx, providers.SpeechRequest{RoomID: "room1", Turn: 1, Attempt: 1})
	require.NoError(t, err, "other turns are unaffected")

	_, err = Dialogue{Scripts: scripts}.EnhanceTags(ctx, providers.TagsRequest{RoomID: "room1", Turn: 2, Attempt: 1, Text: "x"})
	require.NoError(t, err, "other operations are unaffected")
}

func TestChoicesAndTagsFailuresAreInjected(t *testing.T) {
	ctx := context.Background()
	d := Dialogue{Scripts: scriptsWith(t, "room1", `{"failures": [
		{"op": "choices", "turn": 1, "attempts": 1},
		{"op": "tags", "turn": 1, "attempts": 1}
	]}`)}

	_, err := d.GenerateChoices(ctx, providers.ChoicesRequest{RoomID: "room1", Turn: 1, Attempt: 1})
	require.ErrorIs(t, err, ErrInjected)
	_, err = d.EnhanceTags(ctx, providers.TagsRequest{RoomID: "room1", Turn: 1, Attempt: 1, Text: "x"})
	require.ErrorIs(t, err, ErrInjected)

	_, err = d.GenerateChoices(ctx, providers.ChoicesRequest{RoomID: "room1", Turn: 1, Attempt: 2})
	require.NoError(t, err)
}

func TestBrokenScriptAndUnusableRoomIDAreErrorsNotSilentDefaults(t *testing.T) {
	ctx := context.Background()

	broken := Dialogue{Scripts: scriptsWith(t, "room1", `{not json`)}
	_, err := broken.GenerateChoices(ctx, providers.ChoicesRequest{RoomID: "room1", Turn: 1, Attempt: 1})
	require.Error(t, err)

	withDir := Dialogue{Scripts: Scripts{Dir: t.TempDir()}}
	for _, roomID := range []string{"", "../room1", "a/b", "room.1"} {
		_, err := withDir.GenerateChoices(ctx, providers.ChoicesRequest{RoomID: roomID, Turn: 1, Attempt: 1})
		require.Error(t, err, "room id %q", roomID)
	}
}

func TestSilentMP3IsWholeFramesWithAnMP3Header(t *testing.T) {
	audio, err := Speech{}.Synthesize(context.Background(), providers.SpeechRequest{RoomID: "room1", Turn: 1, Attempt: 1})
	require.NoError(t, err)
	require.Len(t, audio, mp3FrameSize*silentMP3Count)
	for i := range silentMP3Count {
		header := audio[i*mp3FrameSize : i*mp3FrameSize+2]
		require.Equal(t, []byte{0xFF, 0xFB}, header, "frame %d starts with an MPEG-1 Layer III sync word", i)
	}
}

func TestLipSyncMakesAVideoOfTheTestClip(t *testing.T) {
	ctx := context.Background()
	l := &LipSync{}
	image, err := l.Upload(ctx, providers.UploadRequest{RoomID: "room1", Turn: 2, Filename: "portrait.png", Data: []byte("x")})
	require.NoError(t, err)
	audio, err := l.Upload(ctx, providers.UploadRequest{RoomID: "room1", Turn: 2, Filename: "turn-2.mp3", Data: []byte("y")})
	require.NoError(t, err)

	id, err := l.Submit(ctx, providers.LipSyncRequest{RoomID: "room1", Turn: 2, ImageURL: image, AudioURL: audio, Resolution: "480p"})
	require.NoError(t, err)
	result, err := l.Result(ctx, id)
	require.NoError(t, err)
	require.Equal(t, providers.LipSyncCompleted, result.Status, "with no script, at once")

	video, err := l.Download(ctx, result.VideoURL)
	require.NoError(t, err)
	require.Equal(t, TestVideo(), video)
	require.Equal(t, "ftyp", string(video[4:8]), "an MP4")
}

func TestLipSyncIsSlowedAndFailedByScript(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1000, 0)
	l := &LipSync{
		Scripts: scriptsWith(t, "room1", `{"lipSyncDelays": {"1": 3000}, "failures": [{"op": "lipsync", "turn": 2, "attempts": 1}]}`),
		Now:     func() time.Time { return now },
	}

	id, err := l.Submit(ctx, providers.LipSyncRequest{RoomID: "room1", Turn: 1})
	require.NoError(t, err)
	result, err := l.Result(ctx, id)
	require.NoError(t, err)
	require.Equal(t, providers.LipSyncPending, result.Status, "turn 1 takes three seconds")
	now = now.Add(3 * time.Second)
	result, err = l.Result(ctx, id)
	require.NoError(t, err)
	require.Equal(t, providers.LipSyncCompleted, result.Status)

	_, err = l.Submit(ctx, providers.LipSyncRequest{RoomID: "room1", Turn: 2})
	require.ErrorIs(t, err, ErrInjected, "turn 2's submission fails")
}

func TestLipSyncResultOfAnUnknownVideoIsAnError(t *testing.T) {
	_, err := (&LipSync{}).Result(context.Background(), "not-a-fake-id")
	require.Error(t, err)
}
