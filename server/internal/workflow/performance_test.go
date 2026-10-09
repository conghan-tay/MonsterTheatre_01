package workflow

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/activities"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/performance"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/providers"
)

const (
	testWindow    = 10 * time.Second
	testCountdown = 5 * time.Second
)

var testInput = performance.WorkflowInput{PerformanceID: "perf-1", RoomID: "room-1"}

// published is one call to the Publish activity and the workflow's clock
// when it was made.
type published struct {
	update performance.Update
	at     time.Time
}

// recorder stands in for the outside world: it answers every activity and
// keeps what the workflow asked for.
type recorder struct {
	env        *testsuite.TestWorkflowEnvironment
	publishes  []published
	publishErr error
	choices    []activities.ChoicesInput
	speech     []activities.SpeechInput
	completed  []activities.CompleteInput
}

// newRun returns a test environment with every activity mocked.
func newRun(t *testing.T) (*testsuite.TestWorkflowEnvironment, *recorder) {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rec := &recorder{env: env}
	var a *activities.Activities

	env.OnActivity(a.FetchContext, mock.Anything, "room-1").Return(activities.RoomContext{
		Scene:             providers.Scene{Topic: "A biscuit"},
		CharacterA:        activities.Character{Name: "Grumble", VoiceID: "voice-a"},
		CharacterB:        activities.Character{Name: "Fizz", VoiceID: "voice-b"},
		Model:             "test-model",
		VoteWindow:        testWindow,
		PremiereCountdown: testCountdown,
	}, nil)
	env.OnActivity(a.GenerateChoices, mock.Anything, mock.Anything).Return(
		func(_ context.Context, in activities.ChoicesInput) ([]string, error) {
			rec.choices = append(rec.choices, in)
			return []string{
				fmt.Sprintf("turn %d one", in.Turn), fmt.Sprintf("turn %d two", in.Turn), fmt.Sprintf("turn %d three", in.Turn),
			}, nil
		})
	env.OnActivity(a.EnhanceTags, mock.Anything, mock.Anything).Return(
		func(_ context.Context, in activities.TagsInput) (string, error) { return "[tag] " + in.Text, nil })
	env.OnActivity(a.Synthesize, mock.Anything, mock.Anything).Return(
		func(_ context.Context, in activities.SpeechInput) (string, error) {
			rec.speech = append(rec.speech, in)
			return activities.AudioKey(in.PerformanceID, in.Turn), nil
		})
	env.OnActivity(a.Publish, mock.Anything, mock.Anything).Return(
		func(_ context.Context, update performance.Update) error {
			rec.publishes = append(rec.publishes, published{update: update, at: env.Now()})
			return rec.publishErr
		})
	env.OnActivity(a.Complete, mock.Anything, mock.Anything).Return(
		func(_ context.Context, in activities.CompleteInput) error {
			rec.completed = append(rec.completed, in)
			return nil
		})
	return env, rec
}

func TestPerformancePublishesTwentyFiveUpdatesInOrder(t *testing.T) {
	env, rec := newRun(t)
	env.ExecuteWorkflow(Performance, testInput)
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	require.Len(t, rec.publishes, 3*performance.Turns+1)
	phases := []string{performance.PhaseGenerating, performance.PhaseVoting, performance.PhasePreparingLine}
	for i, p := range rec.publishes {
		revision := i + 1
		require.Equal(t, revision, p.update.Revision, "revisions go up by one")
		require.Equal(t, "room-1", p.update.RoomID)
		require.Equal(t, "perf-1", p.update.State.PerformanceID)
		if revision == len(rec.publishes) {
			require.Equal(t, performance.PhasePremiere, p.update.State.Phase)
			continue
		}
		turn := i/3 + 1
		require.Equal(t, phases[i%3], p.update.State.Phase, "revision %d", revision)
		require.Equal(t, turn, p.update.State.Turn, "revision %d", revision)
		require.Equal(t, Schedule[turn-1].Speaker, p.update.State.Speaker)
		require.Equal(t, Schedule[turn-1].Beat, p.update.State.Beat)
	}
}

func TestEachRoundClosesAtTheDeadlineItPublished(t *testing.T) {
	env, rec := newRun(t)
	env.ExecuteWorkflow(Performance, testInput)
	require.NoError(t, env.GetWorkflowError())

	for turn := 1; turn <= performance.Turns; turn++ {
		voting, preparing := rec.publishes[3*turn-2], rec.publishes[3*turn-1]
		state := voting.update.State
		require.Len(t, state.Choices, 3)
		require.Equal(t, []performance.Choice{
			{ID: "c1", Text: fmt.Sprintf("turn %d one", turn)},
			{ID: "c2", Text: fmt.Sprintf("turn %d two", turn)},
			{ID: "c3", Text: fmt.Sprintf("turn %d three", turn)},
		}, state.Choices)
		require.Nil(t, state.Winner)
		require.NotNil(t, state.Deadline)
		require.WithinDuration(t, voting.at.Add(testWindow), *state.Deadline, 0,
			"the deadline is one window after the round opened")
		require.WithinDuration(t, *state.Deadline, preparing.at, 0,
			"and the winner is published at that deadline, not before or after")

		winner := preparing.update.State.Winner
		require.NotNil(t, winner)
		require.Nil(t, preparing.update.State.Deadline)
		require.Equal(t, performance.DecidedByNoVotes, winner.DecidedBy)
		require.Equal(t, map[string]int{"c1": 0, "c2": 0, "c3": 0}, winner.Counts)
		var offered []string
		for _, c := range state.Choices {
			if c.ID == winner.ChoiceID {
				require.Equal(t, c.Text, winner.Text, "the winner's text is that choice's text")
			}
			offered = append(offered, c.Text)
		}
		require.Contains(t, offered, winner.Text)
	}
}

func TestWinningLinesFeedTheNextTurnAndTheScript(t *testing.T) {
	env, rec := newRun(t)
	env.ExecuteWorkflow(Performance, testInput)
	require.NoError(t, env.GetWorkflowError())

	var lines []performance.Line
	for turn := 1; turn <= performance.Turns; turn++ {
		generating, preparing := rec.publishes[3*turn-3], rec.publishes[3*turn-1]
		require.Equal(t, lines, generating.update.State.Lines, "turn %d opens with the lines so far", turn)

		// The language model is given the dialogue chosen so far, and only that.
		asked := rec.choices[turn-1]
		require.Len(t, asked.History, turn-1)
		for i, line := range lines {
			require.Equal(t, providers.Line{Speaker: line.Speaker, Text: line.Text}, asked.History[i])
		}
		require.Equal(t, performance.Turns-turn, asked.TurnsRemaining)
		require.Equal(t, Schedule[turn-1].Beat, asked.Beat)

		winner := preparing.update.State.Winner
		lines = append(lines, performance.Line{Turn: turn, Speaker: Schedule[turn-1].Speaker, Text: winner.Text})
		require.Equal(t, lines, preparing.update.State.Lines)

		// The tagged line is spoken in the speaker's own voice.
		voice := "voice-a"
		if Schedule[turn-1].Speaker == "B" {
			voice = "voice-b"
		}
		require.Equal(t, activities.SpeechInput{
			PerformanceID: "perf-1", RoomID: "room-1", Turn: turn, VoiceID: voice, Text: "[tag] " + winner.Text,
		}, rec.speech[turn-1])
	}

	require.Len(t, rec.completed, 1, "saved once")
	saved := rec.completed[0]
	require.Equal(t, "perf-1", saved.PerformanceID)
	require.Len(t, saved.Script.Turns, performance.Turns)
	for i, turn := range saved.Script.Turns {
		require.Equal(t, i+1, turn.Turn)
		require.Equal(t, Schedule[i].Speaker, turn.Speaker)
		require.Equal(t, Schedule[i].Beat, turn.Beat)
		require.Len(t, turn.Choices, 3)
		require.Equal(t, lines[i].Text, turn.Winner.Text)
		require.Equal(t, "[tag] "+lines[i].Text, turn.TaggedText)
		require.Equal(t, performance.DecidedByNoVotes, turn.DecidedBy)
		require.Equal(t, "test-model", turn.Model)
		require.Equal(t, activities.AudioKey("perf-1", i+1), saved.AudioKeys[i])
	}

	// The premiere comes after the save, and names every clip.
	premiere := rec.publishes[len(rec.publishes)-1]
	state := premiere.update.State
	require.Equal(t, performance.Turns, state.Turn)
	require.Equal(t, lines, state.Lines)
	require.Empty(t, state.Choices)
	require.Nil(t, state.Winner)
	require.WithinDuration(t, premiere.at.Add(testCountdown), state.Premiere.StartsAt, 0)
	require.Len(t, state.Premiere.Clips, performance.Turns)
	for i, clip := range state.Premiere.Clips {
		require.Equal(t, performance.Clip{
			Turn:    i + 1,
			Speaker: Schedule[i].Speaker,
			URL:     fmt.Sprintf("/api/v1/performances/perf-1/turns/%d/audio", i+1),
		}, clip)
	}
}

func TestStateQueryAnswersWithTheCurrentRevision(t *testing.T) {
	env, _ := newRun(t)

	query := func() performance.Snapshot {
		value, err := env.QueryWorkflow(performance.QueryState)
		require.NoError(t, err)
		var snapshot performance.Snapshot
		require.NoError(t, value.Get(&snapshot))
		return snapshot
	}

	// Halfway through the third round: turns 1 and 2 took six publishes,
	// and turn 3 has published generating and voting.
	var midRun performance.Snapshot
	env.RegisterDelayedCallback(func() { midRun = query() }, 2*testWindow+testWindow/2)

	env.ExecuteWorkflow(Performance, testInput)
	require.NoError(t, env.GetWorkflowError())

	require.Equal(t, 8, midRun.Revision)
	require.Equal(t, performance.PhaseVoting, midRun.State.Phase)
	require.Equal(t, 3, midRun.State.Turn)
	require.Equal(t, "perf-1", midRun.State.PerformanceID)
	require.Len(t, midRun.State.Choices, 3)
	require.NotNil(t, midRun.State.Deadline)
	require.Len(t, midRun.State.Lines, 2, "the two lines already chosen")

	// A finished workflow still answers, with where it ended.
	final := query()
	require.Equal(t, 3*performance.Turns+1, final.Revision)
	require.Equal(t, performance.PhasePremiere, final.State.Phase)
}

func TestAFailedPublishDoesNotStopTheRun(t *testing.T) {
	env, rec := newRun(t)
	rec.publishErr = errors.New("redis is down")

	env.ExecuteWorkflow(Performance, testInput)
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError(), "the run finishes without its updates")

	require.Len(t, rec.completed, 1, "and is still saved")
	require.Len(t, rec.completed[0].Script.Turns, performance.Turns)

	// Each publish was tried three times, and revisions still went up by one
	// per publish, so a browser can tell what it missed.
	require.Len(t, rec.publishes, 3*(3*performance.Turns+1))
	require.Equal(t, 1, rec.publishes[0].update.Revision)
	require.Equal(t, 1, rec.publishes[2].update.Revision)
	require.Equal(t, 2, rec.publishes[3].update.Revision)
	require.Equal(t, 3*performance.Turns+1, rec.publishes[len(rec.publishes)-1].update.Revision)
}

func TestTheRunFailsIfTheModelReturnsTheWrongNumberOfChoices(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	var a *activities.Activities
	env.OnActivity(a.FetchContext, mock.Anything, mock.Anything).Return(activities.RoomContext{VoteWindow: testWindow}, nil)
	env.OnActivity(a.Publish, mock.Anything, mock.Anything).Return(nil)
	env.OnActivity(a.GenerateChoices, mock.Anything, mock.Anything).Return([]string{"only", "two"}, nil)

	env.ExecuteWorkflow(Performance, testInput)
	require.True(t, env.IsWorkflowCompleted())
	require.ErrorContains(t, env.GetWorkflowError(), "returned 2 choices")
}
