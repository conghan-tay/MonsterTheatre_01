package workflow

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/workflow"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/activities"
)

// PrepareLineName is the name the line workflow is registered under.
const PrepareLineName = "PrepareLine"

// lineTimeout bounds one line's workflow. Its activities bound it first: at
// worst two 20 s tagging attempts and three 60 s speech attempts, with the
// pauses between them, about four minutes.
const lineTimeout = 5 * time.Minute

// LineInput is one winning line to tag and voice.
type LineInput struct {
	PerformanceID string
	RoomID        string
	Turn          int
	Speaker       string
	Beat          string
	Character     activities.Character
	Text          string
}

// PreparedLine is a line ready for the premiere: what was spoken, whether
// the tags were kept, and where the recording is.
type PreparedLine struct {
	TaggedText  string
	TagsApplied bool
	AudioKey    string
}

// LineWorkflowID is the ID of a turn's line workflow, next to its
// performance's own.
func LineWorkflowID(performanceID string, turn int) string {
	return fmt.Sprintf("performance/%s/line-%d", performanceID, turn)
}

// PrepareLine tags a winning line and voices it. The performance runs one
// per turn as a child, without waiting for it, and the next turn goes ahead
// meanwhile (docs/decisions.md, D053).
//
// If tagging fails, the line is spoken as chosen. That is still the real
// line, only delivered plainly. If speech fails, so does this workflow, and
// the performance stops.
func PrepareLine(ctx workflow.Context, in LineInput) (PreparedLine, error) {
	var a *activities.Activities

	var tagged string
	err := workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, tagsOptions), a.EnhanceTags, activities.TagsInput{
		RoomID: in.RoomID, Turn: in.Turn, Speaker: in.Speaker, Beat: in.Beat, Character: in.Character, Text: in.Text,
	}).Get(ctx, &tagged)
	tagsApplied := err == nil
	if err != nil {
		workflow.GetLogger(ctx).Warn("tagging failed; speaking the line without tags", "turn", in.Turn, "error", err)
		tagged = in.Text
	}

	var audioKey string
	err = workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, speechOptions), a.Synthesize, activities.SpeechInput{
		PerformanceID: in.PerformanceID, RoomID: in.RoomID, Turn: in.Turn, VoiceID: in.Character.VoiceID, Text: tagged,
	}).Get(ctx, &audioKey)
	if err != nil {
		return PreparedLine{}, err
	}
	return PreparedLine{TaggedText: tagged, TagsApplied: tagsApplied, AudioKey: audioKey}, nil
}
