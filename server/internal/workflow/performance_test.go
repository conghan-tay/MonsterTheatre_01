package workflow

import (
	"context"
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
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/providers"
)

const (
	testWindow      = 10 * time.Second
	testCountdown   = 5 * time.Second
	testRetryWindow = 2 * time.Minute
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
	tags       []activities.TagsInput
	speech     []activities.SpeechInput
	completed  []activities.CompleteInput
	failed     []activities.FailInput
	// failedAfterPublishes is how many publishes had been made when the
	// performance was marked failed.
	failedAfterPublishes int

	// failTurn is the turn on which choicesErr, tagsErr, or speechErr is
	// returned, on every attempt. failErr is returned by Fail itself.
	failTurn   int
	choicesErr error
	tagsErr    error
	speechErr  error
	failErr    error
}

// newRun returns a test environment with every activity mocked.
func newRun(t *testing.T) (*testsuite.TestWorkflowEnvironment, *recorder) {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rec := &recorder{env: env}
	var a *activities.Activities

	env.OnActivity(a.FetchContext, mock.Anything, "room-1").Return(activities.RoomContext{
		Scene:              providers.Scene{Topic: "A biscuit"},
		CharacterA:         activities.Character{Name: "Grumble", VoiceID: "voice-a"},
		CharacterB:         activities.Character{Name: "Fizz", VoiceID: "voice-b"},
		VoteWindow:         testWindow,
		PremiereCountdown:  testCountdown,
		ChoicesRetryWindow: testRetryWindow,
	}, nil)
	env.OnActivity(a.GenerateChoices, mock.Anything, mock.Anything).Return(
		func(_ context.Context, in activities.ChoicesInput) (activities.ChoicesResult, error) {
			rec.choices = append(rec.choices, in)
			if rec.choicesErr != nil && in.Turn == rec.failTurn {
				return activities.ChoicesResult{}, rec.choicesErr
			}
			return activities.ChoicesResult{Model: "test-model", Lines: []string{
				fmt.Sprintf("turn %d one", in.Turn), fmt.Sprintf("turn %d two", in.Turn), fmt.Sprintf("turn %d three", in.Turn),
			}}, nil
		})
	env.OnActivity(a.EnhanceTags, mock.Anything, mock.Anything).Return(
		func(_ context.Context, in activities.TagsInput) (string, error) {
			rec.tags = append(rec.tags, in)
			if rec.tagsErr != nil && in.Turn == rec.failTurn {
				return "", rec.tagsErr
			}
			return "[tag] " + in.Text, nil
		})
	env.OnActivity(a.Synthesize, mock.Anything, mock.Anything).Return(
		func(_ context.Context, in activities.SpeechInput) (string, error) {
			rec.speech = append(rec.speech, in)
			if rec.speechErr != nil && in.Turn == rec.failTurn {
				return "", rec.speechErr
			}
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
	env.OnActivity(a.Fail, mock.Anything, mock.Anything).Return(
		func(_ context.Context, in activities.FailInput) error {
			rec.failed = append(rec.failed, in)
			rec.failedAfterPublishes = len(rec.publishes)
			return rec.failErr
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
		voice, name := "voice-a", "Grumble"
		if Schedule[turn-1].Speaker == "B" {
			voice, name = "voice-b", "Fizz"
		}
		// Tagging is told who is speaking and to what beat.
		require.Equal(t, name, rec.tags[turn-1].Character.Name)
		require.Equal(t, Schedule[turn-1].Beat, rec.tags[turn-1].Beat)
		require.Equal(t, winner.Text, rec.tags[turn-1].Text)
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
		require.True(t, turn.TagsApplied)
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
		value, err := env.QueryWorkflow(performance.QueryState, "nobody")
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
	env.OnActivity(a.Fail, mock.Anything, mock.Anything).Return(nil)
	env.OnActivity(a.GenerateChoices, mock.Anything, mock.Anything).Return(activities.ChoicesResult{Lines: []string{"only", "two"}}, nil)

	env.ExecuteWorkflow(Performance, testInput)
	require.True(t, env.IsWorkflowCompleted())
	require.ErrorContains(t, env.GetWorkflowError(), "2 choices, not 3")
}

// castAt sends a vote signal at a moment of workflow time, stamped as
// received then unless the test says otherwise.
func castAt(env *testsuite.TestWorkflowEnvironment, at time.Duration, voter string, turn int, choice string, stampOffset time.Duration) {
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(performance.SignalVote, performance.Vote{
			VoterID: voter, Turn: turn, ChoiceID: choice, ReceivedAt: env.Now().Add(stampOffset),
		})
	}, at)
}

// winnerOf returns the winner published for a turn.
func winnerOf(t *testing.T, rec *recorder, turn int) *performance.Winner {
	t.Helper()
	preparing := rec.publishes[3*turn-1].update.State
	require.Equal(t, performance.PhasePreparingLine, preparing.Phase)
	require.Equal(t, turn, preparing.Turn)
	require.NotNil(t, preparing.Winner)
	return preparing.Winner
}

// Turn n's round is open from (n-1) windows to n windows of workflow time,
// because the mocked activities take no time.
func during(turn int) time.Duration { return time.Duration(turn-1)*testWindow + testWindow/2 }

func TestTheMostVotedChoiceWins(t *testing.T) {
	env, rec := newRun(t)
	castAt(env, during(1), "director-1", 1, "c3", 0)
	castAt(env, during(1), "session-1", 1, "c3", 0)
	castAt(env, during(1), "session-2", 1, "c1", 0)
	// Turn 2: one vote is enough.
	castAt(env, during(2), "session-2", 2, "c2", 0)

	env.ExecuteWorkflow(Performance, testInput)
	require.NoError(t, env.GetWorkflowError())

	first := winnerOf(t, rec, 1)
	require.Equal(t, "c3", first.ChoiceID)
	require.Equal(t, "turn 1 three", first.Text)
	require.Equal(t, map[string]int{"c1": 1, "c2": 0, "c3": 2}, first.Counts)
	require.Equal(t, performance.DecidedByMajority, first.DecidedBy)

	second := winnerOf(t, rec, 2)
	require.Equal(t, "c2", second.ChoiceID)
	require.Equal(t, map[string]int{"c1": 0, "c2": 1, "c3": 0}, second.Counts, "counts start again each turn")

	require.Equal(t, performance.DecidedByNoVotes, winnerOf(t, rec, 3).DecidedBy)

	// The script keeps the counts and how each turn was decided.
	script := rec.completed[0].Script
	require.Equal(t, map[string]int{"c1": 1, "c2": 0, "c3": 2}, script.Turns[0].Counts)
	require.Equal(t, performance.DecidedByMajority, script.Turns[0].DecidedBy)
	require.Equal(t, "c3", script.Turns[0].Winner.ID)
	require.Equal(t, "[tag] turn 1 three", script.Turns[0].TaggedText)
}

func TestATieIsSettledBetweenTheTiedChoicesOnly(t *testing.T) {
	env, rec := newRun(t)
	for turn := 1; turn <= performance.Turns; turn++ {
		castAt(env, during(turn), "session-1", turn, "c1", 0)
		castAt(env, during(turn), "session-2", turn, "c3", 0)
	}

	env.ExecuteWorkflow(Performance, testInput)
	require.NoError(t, env.GetWorkflowError())

	for turn := 1; turn <= performance.Turns; turn++ {
		winner := winnerOf(t, rec, turn)
		require.Equal(t, performance.DecidedByTie, winner.DecidedBy)
		require.Equal(t, map[string]int{"c1": 1, "c2": 0, "c3": 1}, winner.Counts)
		require.Contains(t, []string{"c1", "c3"}, winner.ChoiceID, "turn %d: never the choice nobody voted for", turn)
	}
}

func TestVotesThatDoNotCount(t *testing.T) {
	env, rec := newRun(t)

	// Turn 1. The first vote from each voter stands.
	castAt(env, during(1), "session-1", 1, "c1", 0)
	castAt(env, during(1)+time.Second, "session-1", 1, "c2", 0)
	// A choice that was not offered; another turn's round; no voter.
	castAt(env, during(1), "session-2", 1, "c9", 0)
	castAt(env, during(1), "session-3", 5, "c2", 0)
	castAt(env, during(1), "", 1, "c2", 0)
	// Delivered while the round is open, but received by the server after
	// its deadline, or before it opened.
	castAt(env, during(1), "session-4", 1, "c2", testWindow)
	castAt(env, during(1), "session-5", 1, "c2", -testWindow)
	// A voter whose first vote did not count may still vote.
	castAt(env, during(1)+2*time.Second, "session-2", 1, "c1", 0)

	// Turn 2 is open now. A vote for turn 1 is too late, even though its
	// stamp claims it was received in time.
	castAt(env, during(2), "session-6", 1, "c2", -testWindow)

	env.ExecuteWorkflow(Performance, testInput)
	require.NoError(t, env.GetWorkflowError())

	first := winnerOf(t, rec, 1)
	require.Equal(t, map[string]int{"c1": 2, "c2": 0, "c3": 0}, first.Counts)
	require.Equal(t, "c1", first.ChoiceID)

	second := winnerOf(t, rec, 2)
	require.Equal(t, map[string]int{"c1": 0, "c2": 0, "c3": 0}, second.Counts, "nothing leaks into the next round")
	require.Equal(t, performance.DecidedByNoVotes, second.DecidedBy)
}

func TestAVoteAtTheDeadlineCountsAndOneJustAfterDoesNot(t *testing.T) {
	env, rec := newRun(t)
	// Both are delivered mid-round; only their stamps differ.
	castAt(env, during(1), "session-1", 1, "c1", testWindow/2)
	castAt(env, during(1), "session-2", 1, "c2", testWindow/2+time.Nanosecond)
	// And at the other edge: the moment the round opened, and just before.
	castAt(env, during(1), "session-3", 1, "c1", -testWindow/2)
	castAt(env, during(1), "session-4", 1, "c3", -testWindow/2-time.Nanosecond)

	env.ExecuteWorkflow(Performance, testInput)
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, map[string]int{"c1": 2, "c2": 0, "c3": 0}, winnerOf(t, rec, 1).Counts)
}

func TestTheQueryShowsAVoterOnlyTheirOwnVote(t *testing.T) {
	env, _ := newRun(t)
	query := func(voter string) performance.Snapshot {
		value, err := env.QueryWorkflow(performance.QueryState, voter)
		require.NoError(t, err)
		var snapshot performance.Snapshot
		require.NoError(t, value.Get(&snapshot))
		return snapshot
	}

	castAt(env, during(1), "session-1", 1, "c2", 0)
	castAt(env, during(1), "session-2", 1, "c3", 0)

	var mine, theirs, nobodys, afterwards performance.Snapshot
	env.RegisterDelayedCallback(func() {
		mine, theirs, nobodys = query("session-1"), query("session-2"), query("session-9")
	}, during(1)+time.Second)
	env.RegisterDelayedCallback(func() { afterwards = query("session-1") }, during(2))

	env.ExecuteWorkflow(Performance, testInput)
	require.NoError(t, env.GetWorkflowError())

	require.Equal(t, performance.PhaseVoting, mine.State.Phase)
	require.Equal(t, "c2", mine.State.YourVote)
	require.Equal(t, "c3", theirs.State.YourVote)
	require.Empty(t, nobodys.State.YourVote)
	require.Nil(t, mine.State.Winner, "no count is shown while the round is open")

	// Apart from that one field, everyone is shown the same thing.
	mine.State.YourVote, theirs.State.YourVote = "", ""
	require.Equal(t, nobodys, mine)
	require.Equal(t, nobodys, theirs)

	require.Equal(t, 2, afterwards.State.Turn)
	require.Empty(t, afterwards.State.YourVote, "a new turn starts with no vote")
}

func TestUpdatesNeverCarryAnyonesVote(t *testing.T) {
	env, rec := newRun(t)
	castAt(env, during(1), "session-1", 1, "c2", 0)
	env.ExecuteWorkflow(Performance, testInput)
	require.NoError(t, env.GetWorkflowError())
	for _, p := range rec.publishes {
		require.Empty(t, p.update.State.YourVote, "revision %d", p.update.Revision)
	}
}

// requireFailedRun checks a run that stopped: the workflow failed, the row
// was marked first with the right reason, then browsers were told, in an
// update one revision after the last, on the turn it stopped at.
func requireFailedRun(t *testing.T, env *testsuite.TestWorkflowEnvironment, rec *recorder, code string, turn, lastGoodRevision int) performance.State {
	t.Helper()
	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError(), "the workflow ends as failed, so the operator sees it in Temporal")

	require.Equal(t, []activities.FailInput{{PerformanceID: "perf-1", Reason: code}}, rec.failed)
	require.Empty(t, rec.completed, "a failed run is not saved as completed")

	last := rec.publishes[len(rec.publishes)-1].update
	require.Equal(t, lastGoodRevision+1, last.Revision)
	require.Equal(t, lastGoodRevision, rec.failedAfterPublishes, "the row is marked before browsers are told")
	require.Equal(t, performance.PhaseFailed, last.State.Phase)
	require.Equal(t, turn, last.State.Turn)
	require.Equal(t, "perf-1", last.State.PerformanceID)
	require.Equal(t, code, last.State.Failure.Code)
	require.NotEmpty(t, last.State.Failure.Message)
	require.Empty(t, last.State.Choices)
	require.Nil(t, last.State.Winner)
	return last.State
}

func TestChoiceGenerationIsRetriedForTheWholeWindowThenTheRunFails(t *testing.T) {
	env, rec := newRun(t)
	rec.failTurn, rec.choicesErr = 3, errors.New("the model is overloaded")
	began := env.Now()

	env.ExecuteWorkflow(Performance, testInput)

	// Turns 1 and 2 published six updates; turn 3 published "generating".
	state := requireFailedRun(t, env, rec, performance.FailureChoices, 3, 7)
	require.Len(t, state.Lines, 2)

	attempts := 0
	for _, asked := range rec.choices {
		if asked.Turn == 3 {
			attempts++
		}
	}
	require.Greater(t, attempts, 5, "many attempts, not three: it retries on time, not on a count")

	// It gave up within the window, and not at the first failure. The two
	// earlier rounds account for two vote windows.
	spent := env.Now().Sub(began) - 2*testWindow
	require.LessOrEqual(t, spent, testRetryWindow)
	require.Greater(t, spent, testRetryWindow/2)
}

func TestChoiceGenerationThatRecoversCarriesOn(t *testing.T) {
	env, rec := newRun(t)
	var a *activities.Activities
	calls := 0
	env.OnActivity(a.GenerateChoices, mock.Anything, mock.Anything).Return(
		func(_ context.Context, in activities.ChoicesInput) (activities.ChoicesResult, error) {
			if in.Turn == 2 {
				if calls++; calls <= 4 {
					return activities.ChoicesResult{}, errors.New("a brief outage")
				}
			}
			return activities.ChoicesResult{Model: "test-model", Lines: []string{"one", "two", "three"}}, nil
		})

	env.ExecuteWorkflow(Performance, testInput)
	require.NoError(t, env.GetWorkflowError())
	require.Empty(t, rec.failed)
	require.Len(t, rec.completed, 1)
	require.Len(t, rec.publishes, 3*performance.Turns+1, "no extra updates were published while it retried")
}

func TestAnErrorNotWorthRetryingFailsTheRunAtOnce(t *testing.T) {
	env, rec := newRun(t)
	rec.failTurn = 1
	rec.choicesErr = temporal.NewNonRetryableApplicationError("invalid API key", "ProviderRejected", nil)
	began := env.Now()

	env.ExecuteWorkflow(Performance, testInput)

	requireFailedRun(t, env, rec, performance.FailureChoices, 1, 1)
	require.Len(t, rec.choices, 1, "one attempt")
	require.Less(t, env.Now().Sub(began), time.Second, "and no waiting out the retry window")
}

func TestSpeechFailingThreeTimesFailsTheRun(t *testing.T) {
	env, rec := newRun(t)
	rec.failTurn, rec.speechErr = 2, errors.New("the speech service is down")

	env.ExecuteWorkflow(Performance, testInput)

	// Turn 2's winner was announced (revision 6) before its speech failed.
	state := requireFailedRun(t, env, rec, performance.FailureSpeech, 2, 6)
	require.Len(t, state.Lines, 2, "turn 2's line was chosen, though never voiced")

	attempts := 0
	for _, asked := range rec.speech {
		if asked.Turn == 2 {
			attempts++
		}
	}
	require.Equal(t, 3, attempts)
}

func TestTaggingFailureSpeaksTheLineAsChosen(t *testing.T) {
	env, rec := newRun(t)
	rec.failTurn, rec.tagsErr = 4, errors.New("the tagged line does not keep the original words")

	env.ExecuteWorkflow(Performance, testInput)
	require.NoError(t, env.GetWorkflowError(), "the run completes")
	require.Empty(t, rec.failed)

	attempts := 0
	for _, asked := range rec.tags {
		if asked.Turn == 4 {
			attempts++
		}
	}
	require.Equal(t, 2, attempts, "tagging is tried twice")

	turn4 := rec.completed[0].Script.Turns[3]
	require.Equal(t, turn4.Winner.Text, turn4.TaggedText)
	require.False(t, turn4.TagsApplied)
	require.True(t, rec.completed[0].Script.Turns[2].TagsApplied, "other turns are unaffected")

	// The plain line is what was sent to speech.
	for _, spoken := range rec.speech {
		if spoken.Turn == 4 {
			require.Equal(t, turn4.Winner.Text, spoken.Text)
		}
	}
}

func TestBrowsersAreStillToldWhenTheRowCannotBeMarked(t *testing.T) {
	env, rec := newRun(t)
	rec.failTurn, rec.speechErr = 1, errors.New("the speech service is down")
	rec.failErr = temporal.NewNonRetryableApplicationError("database unreachable", "Database", nil)

	env.ExecuteWorkflow(Performance, testInput)

	require.ErrorContains(t, env.GetWorkflowError(), "the speech service is down",
		"the workflow reports why the run stopped, not the trouble recording it")
	last := rec.publishes[len(rec.publishes)-1].update.State
	require.Equal(t, performance.PhaseFailed, last.Phase)
	require.Equal(t, performance.FailureSpeech, last.Failure.Code)
}

func TestTheScriptRecordsTheModelThatWroteEachTurn(t *testing.T) {
	env, rec := newRun(t)
	env.ExecuteWorkflow(Performance, testInput)
	require.NoError(t, env.GetWorkflowError())
	for _, turn := range rec.completed[0].Script.Turns {
		require.Equal(t, "test-model", turn.Model)
	}
}
