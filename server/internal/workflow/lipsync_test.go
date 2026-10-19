package workflow

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/activities"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/performance"
)

var lipSyncInput = performance.WorkflowInput{PerformanceID: "perf-1", RoomID: "room-1", LipSync: true}

func TestWithoutLipSyncNoVideoIsMadeAndTheRunIsAsBefore(t *testing.T) {
	env, rec := newRun(t)
	env.ExecuteWorkflow(Performance, testInput)
	require.NoError(t, env.GetWorkflowError())

	require.Empty(t, rec.uploads)
	require.Empty(t, rec.submissions)
	require.Empty(t, rec.waits)
	require.Len(t, rec.publishes, performance.LastRevision, "no progress updates")
	for _, p := range rec.publishes {
		require.NotEqual(t, performance.PhasePreparingPremiere, p.update.State.Phase)
	}
	saved := rec.completed[0]
	require.Zero(t, saved.Script.PremiereRevision, "nothing new is recorded")
	for _, turn := range saved.Script.Turns {
		require.Empty(t, turn.VideoKey)
	}
	for _, clip := range rec.publishes[len(rec.publishes)-1].update.State.Premiere.Clips {
		require.Empty(t, clip.VideoURL)
	}
}

func TestWithLipSyncEachLineIsUploadedSubmittedAwaitedAndSaved(t *testing.T) {
	env, rec := newRun(t)
	env.ExecuteWorkflow(Performance, lipSyncInput)
	require.NoError(t, env.GetWorkflowError())

	require.Len(t, rec.uploads, performance.Turns)
	require.Len(t, rec.submissions, performance.Turns)
	require.Len(t, rec.waits, performance.Turns)
	for turn := 1; turn <= performance.Turns; turn++ {
		portrait := "rooms/room-1/portrait-a"
		if Schedule[turn-1].Speaker == "B" {
			portrait = "rooms/room-1/portrait-b"
		}
		require.Equal(t, activities.LipSyncInputs{PerformanceID: "perf-1", RoomID: "room-1", Turn: turn, PortraitKey: portrait}, rec.uploads[turn-1],
			"the speaker's own portrait, with the turn's recording")
		require.Equal(t, activities.LipSyncSubmission{
			RoomID: "room-1", Turn: turn, Resolution: "480p",
			ImageURL: "https://files.example/" + portrait, AudioURL: fmt.Sprintf("https://files.example/turn-%d.mp3", turn),
		}, rec.submissions[turn-1], "what was uploaded, at the configured resolution")
		require.Equal(t, activities.LipSyncWait{PerformanceID: "perf-1", RoomID: "room-1", Turn: turn, PredictionID: fmt.Sprintf("pred-%d", turn)}, rec.waits[turn-1])
	}

	saved := rec.completed[0]
	premiere := rec.publishes[len(rec.publishes)-1].update
	require.Equal(t, performance.PhasePremiere, premiere.State.Phase)
	require.Equal(t, premiere.Revision, saved.Script.PremiereRevision, "the premiere's revision is saved with the script")
	for i, turn := range saved.Script.Turns {
		require.Equal(t, activities.VideoKey("perf-1", i+1), turn.VideoKey)
		require.Equal(t, performance.VideoURL("perf-1", i+1), premiere.State.Premiere.Clips[i].VideoURL)
		require.Equal(t, performance.AudioURL("perf-1", i+1), premiere.State.Premiere.Clips[i].URL, "the recording is still played")
	}
}

func TestTheAudienceIsToldHowManyVideosAreReadyBeforeThePremiere(t *testing.T) {
	// Turn n's line is decided at n windows, 10 s each. Turn 1's video takes
	// 100 s, so is ready at 110 s; turn 3's takes 70 s, so is ready at
	// 100 s. Both are still being made when turn 8's round closes, at 80 s.
	var a *activities.Activities
	slow := func(turn int, d time.Duration) func(*testsuite.TestWorkflowEnvironment) {
		return func(env *testsuite.TestWorkflowEnvironment) {
			env.OnActivity(a.AwaitLipSync, mock.Anything, mock.MatchedBy(func(in activities.LipSyncWait) bool { return in.Turn == turn })).
				Return(activities.VideoKey("perf-1", turn), nil).After(d)
		}
	}
	env, rec := newRun(t, slow(1, 100*time.Second), slow(3, 70*time.Second))
	began := env.Now()
	env.ExecuteWorkflow(Performance, lipSyncInput)
	require.NoError(t, env.GetWorkflowError())

	// The 24 updates of the turns, then progress, then the premiere.
	require.Equal(t, performance.PhasePreparingLine, rec.publishes[23].update.State.Phase)
	progress := rec.publishes[24 : len(rec.publishes)-1]
	require.NotEmpty(t, progress)
	for i, p := range progress {
		state := p.update.State
		require.Equal(t, performance.PhasePreparingPremiere, state.Phase)
		require.Equal(t, 3*performance.Turns+1+i, p.update.Revision, "one revision after another")
		require.Equal(t, performance.Turns, state.Turn)
		require.Len(t, state.Lines, performance.Turns, "the script so far stays")
		require.Equal(t, performance.Turns, state.Progress.Total)
		require.Less(t, state.Progress.Ready, performance.Turns, "never 8 of 8: that is the premiere")
		if i > 0 {
			require.Greater(t, state.Progress.Ready, progress[i-1].update.State.Progress.Ready, "a new count each time, going up")
		}
	}
	// The first is told when the turns end, before turn 8's own line is
	// ready; the last once turn 3's video is ready, leaving turn 1's.
	require.Equal(t, 8*testWindow, progress[0].at.Sub(began))
	require.LessOrEqual(t, progress[0].update.State.Progress.Ready, 6, "turns 1 and 3 are not ready when the turns end")
	last := progress[len(progress)-1]
	require.Equal(t, 7, last.update.State.Progress.Ready)
	require.Equal(t, 3*testWindow+70*time.Second, last.at.Sub(began))

	premiere := rec.publishes[len(rec.publishes)-1]
	require.Equal(t, performance.PhasePremiere, premiere.update.State.Phase)
	require.Equal(t, testWindow+100*time.Second, premiere.at.Sub(began), "once turn 1's is ready too")
	require.Nil(t, premiere.update.State.Progress)
	require.Equal(t, premiere.update.Revision, rec.completed[0].Script.PremiereRevision)
}

func TestALineWhoseSubmissionFailsIsPlayedWithoutAVideo(t *testing.T) {
	env, rec := newRun(t)
	rec.failTurn, rec.submitErr = 4, temporal.NewApplicationError("status 500", "LipSync")
	env.ExecuteWorkflow(Performance, lipSyncInput)
	require.NoError(t, env.GetWorkflowError(), "the run still reaches its premiere")
	require.Empty(t, rec.failed)

	submitted := 0
	for _, s := range rec.submissions {
		if s.Turn == 4 {
			submitted++
		}
	}
	require.Equal(t, 1, submitted, "a submission is never repeated: it may have started a video, and been billed")
	for _, w := range rec.waits {
		require.NotEqual(t, 4, w.Turn, "there is nothing to wait for")
	}
	requireOnlyTurnWithoutVideo(t, rec, 4)
}

func TestALineWhoseVideoFailsIsPlayedWithoutAVideo(t *testing.T) {
	env, rec := newRun(t)
	rec.failTurn, rec.awaitErr = 6, temporal.NewNonRetryableApplicationError("lip sync failed: no face", "LipSyncFailed", nil)
	env.ExecuteWorkflow(Performance, lipSyncInput)
	require.NoError(t, env.GetWorkflowError())
	require.Empty(t, rec.failed)
	requireOnlyTurnWithoutVideo(t, rec, 6)
}

func requireOnlyTurnWithoutVideo(t *testing.T, rec *recorder, without int) {
	t.Helper()
	saved := rec.completed[0]
	clips := rec.publishes[len(rec.publishes)-1].update.State.Premiere.Clips
	for i, turn := range saved.Script.Turns {
		if i+1 == without {
			require.Empty(t, turn.VideoKey, "turn %d", i+1)
			require.Empty(t, clips[i].VideoURL, "turn %d is played as before: the portrait and the recording", i+1)
			continue
		}
		require.NotEmpty(t, turn.VideoKey, "turn %d", i+1)
		require.NotEmpty(t, clips[i].VideoURL, "turn %d", i+1)
	}
}

func TestSpeechFailingStillFailsARunWithLipSync(t *testing.T) {
	env, rec := newRun(t)
	rec.failTurn, rec.speechErr = 2, errors.New("the speech service is down")
	env.ExecuteWorkflow(Performance, lipSyncInput)
	require.Error(t, env.GetWorkflowError())
	require.Equal(t, []activities.FailInput{{PerformanceID: "perf-1", Reason: performance.FailureSpeech}}, rec.failed)
	for _, u := range rec.uploads {
		require.NotEqual(t, 2, u.Turn, "a line that was never voiced has nothing to lip-sync")
	}
}
