package fake

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
		require.Equal(t, []string{"Turn 3, first option.", "Turn 3, second option.", "Turn 3, third option."}, lines, name)
	}
}

func TestDialogueReturnsScriptedChoicesForThatRoomAndTurnOnly(t *testing.T) {
	ctx := context.Background()
	d := Dialogue{Scripts: scriptsWith(t, "room1", `{"choices": {"2": ["a", "b", "c"]}}`)}

	scripted, err := d.GenerateChoices(ctx, providers.ChoicesRequest{RoomID: "room1", Turn: 2, Attempt: 1})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b", "c"}, scripted)

	otherTurn, err := d.GenerateChoices(ctx, providers.ChoicesRequest{RoomID: "room1", Turn: 3, Attempt: 1})
	require.NoError(t, err)
	require.Equal(t, "Turn 3, first option.", otherTurn[0])

	otherRoom, err := d.GenerateChoices(ctx, providers.ChoicesRequest{RoomID: "room2", Turn: 2, Attempt: 1})
	require.NoError(t, err)
	require.Equal(t, "Turn 2, first option.", otherRoom[0])
}

func TestEnhanceTagsKeepsTheWords(t *testing.T) {
	tagged, err := Dialogue{}.EnhanceTags(context.Background(), providers.TagsRequest{RoomID: "room1", Turn: 1, Attempt: 1, Text: "Hello there."})
	require.NoError(t, err)
	require.Equal(t, "[excited] Hello there.", tagged)
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
