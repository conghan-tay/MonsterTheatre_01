package workflow

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/activities"
)

// PrepareLineName is the name the line workflow is registered under.
const PrepareLineName = "PrepareLine"

// lineTimeout bounds one line's workflow. Its activities bound it first: at
// worst two 20 s tagging attempts and three 60 s speech attempts, with the
// pauses between them, about four minutes. A line with lip sync is given
// lipSyncTimeout more, which its own activities bound at about twenty
// minutes: three one-minute uploads, one submission, and fifteen minutes of
// waiting.
const (
	lineTimeout    = 5 * time.Minute
	lipSyncTimeout = 20 * time.Minute
)

// Lip-sync activity options (docs/decisions.md, D055).
var (
	uploadOptions = workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	}
	// A submission is tried once: one lost on its way back may still have
	// started a video, and been billed, and WaveSpeed has no way to cancel
	// it or to ask whether it was made.
	submitOptions = workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 1},
	}
	// Waiting polls every 20 seconds and says it is alive after each poll,
	// so a worker that dies is noticed within a minute and the wait carries
	// on elsewhere. Fifteen minutes is more than twice the six minutes a
	// fifteen-second clip has been seen to take.
	awaitOptions = workflow.ActivityOptions{
		ScheduleToCloseTimeout: 15 * time.Minute,
		StartToCloseTimeout:    15 * time.Minute,
		HeartbeatTimeout:       time.Minute,
	}
)

// LineInput is one winning line to tag and voice.
type LineInput struct {
	PerformanceID string
	RoomID        string
	Turn          int
	Speaker       string
	Beat          string
	Character     activities.Character
	Text          string
	// LipSync asks for a lip-sync video of the line, at Resolution.
	LipSync    bool
	Resolution string
}

// PreparedLine is a line ready for the premiere: what was spoken, whether
// the tags were kept, and where the recording is.
type PreparedLine struct {
	TaggedText  string
	TagsApplied bool
	AudioKey    string
	// VideoKey is the line's lip-sync video, if one was asked for and made.
	VideoKey string
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
// the performance stops. If lip sync was asked for, the voiced line is then
// made into a video; if that fails, the line has none and is played with
// the portrait, as without lip sync.
func PrepareLine(ctx workflow.Context, in LineInput) (PreparedLine, error) {
	var a *activities.Activities

	var tagged string
	err := workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, tagsOptions), a.EnhanceTags, activities.TagsInput{
		RoomID: in.RoomID, Turn: in.Turn, Speaker: in.Speaker, Beat: in.Beat, Character: in.Character, Text: in.Text,
	}).Get(ctx, &tagged)
	tagsApplied := err == nil
	if err != nil {
		logger(ctx, in.RoomID, in.PerformanceID).Warn("tagging failed; speaking the line without tags", "turn", in.Turn, "error", err)
		tagged = in.Text
	}

	var audioKey string
	err = workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, speechOptions), a.Synthesize, activities.SpeechInput{
		PerformanceID: in.PerformanceID, RoomID: in.RoomID, Turn: in.Turn, VoiceID: in.Character.VoiceID, Text: tagged,
	}).Get(ctx, &audioKey)
	if err != nil {
		return PreparedLine{}, err
	}
	line := PreparedLine{TaggedText: tagged, TagsApplied: tagsApplied, AudioKey: audioKey}
	if in.LipSync {
		line.VideoKey = lipSync(ctx, in)
	}
	return line, nil
}

// lipSync makes the line's video and returns its key, or nothing if it
// could not be made.
func lipSync(ctx workflow.Context, in LineInput) string {
	var a *activities.Activities
	log := logger(ctx, in.RoomID, in.PerformanceID)

	var uploads activities.LipSyncUploads
	err := workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, uploadOptions), a.UploadLipSyncInputs, activities.LipSyncInputs{
		PerformanceID: in.PerformanceID, RoomID: in.RoomID, Turn: in.Turn, PortraitKey: in.Character.PortraitKey,
	}).Get(ctx, &uploads)
	if err != nil {
		log.Warn("lip sync: could not upload; the line has no video", "turn", in.Turn, "error", err)
		return ""
	}
	var predictionID string
	err = workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, submitOptions), a.SubmitLipSync, activities.LipSyncSubmission{
		RoomID: in.RoomID, Turn: in.Turn, ImageURL: uploads.ImageURL, AudioURL: uploads.AudioURL, Resolution: in.Resolution,
	}).Get(ctx, &predictionID)
	if err != nil {
		log.Warn("lip sync: could not submit; the line has no video", "turn", in.Turn, "error", err)
		return ""
	}
	var videoKey string
	err = workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, awaitOptions), a.AwaitLipSync, activities.LipSyncWait{
		PerformanceID: in.PerformanceID, RoomID: in.RoomID, Turn: in.Turn, PredictionID: predictionID,
	}).Get(ctx, &videoKey)
	if err != nil {
		log.Warn("lip sync: no video; the line has none", "turn", in.Turn, "prediction", predictionID, "error", err)
		return ""
	}
	return videoKey
}

// logger is the workflow's logger with the room and the performance named
// on every line (docs/decisions.md, D060). Temporal adds the workflow and
// run IDs, and leaves out lines written while history is replayed.
func logger(ctx workflow.Context, roomID, performanceID string) log.Logger {
	return log.With(workflow.GetLogger(ctx), "room_id", roomID, "performance_id", performanceID)
}
