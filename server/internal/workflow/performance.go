// Package workflow holds the performance workflow: the one piece of code
// that advances a performance from turn to turn.
//
// Everything here must be deterministic, because Temporal rebuilds a
// workflow's state after a worker restart by running this code again against
// the recorded history. So: time comes from workflow.Now, waiting from
// workflow timers, randomness from workflow.SideEffect, and anything that
// touches the outside world is an activity.
package workflow

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/activities"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/performance"
	"github.com/conghan-tay/MonsterTheatre_01/server/internal/providers"
)

// choicesPerTurn is how many lines the audience chooses between.
const choicesPerTurn = 3

// turnPlan is who speaks on a turn and to what story beat.
type turnPlan struct {
	Speaker string
	Beat    string
}

// Schedule is the fixed plan of a performance: eight alternating turns,
// A first, B closing (docs/monster-theatre-product-brief.md).
var Schedule = [performance.Turns]turnPlan{
	{"A", "INTRO"}, {"B", "QUESTION"}, {"A", "REACT"}, {"B", "QUESTION"},
	{"A", "QUESTION"}, {"B", "REACT"}, {"A", "REACT"}, {"B", "EXPLAIN + PUNCHLINE"},
}

// Activity options, from the table in docs/system-design.md, section 4.
var (
	tagsOptions = workflow.ActivityOptions{
		StartToCloseTimeout: 20 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 2},
	}
	speechOptions = workflow.ActivityOptions{
		StartToCloseTimeout: 60 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	}
	publishOptions = workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	}
	// Database activities retry until the run's own timeout.
	databaseOptions = workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Second,
	}
)

// choicesOptions bounds choice generation by time, not by a count of
// attempts: it is retried with growing pauses for the whole retry window,
// which rides out a brief outage of the language model. If the window runs
// out the run fails. There is no stand-in for lines the model did not write:
// the audience would vote on filler, the model would build on it, and the
// premiere would play it (docs/decisions.md, D038).
func choicesOptions(retryWindow time.Duration) workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout:    30 * time.Second,
		ScheduleToCloseTimeout: retryWindow,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2,
			MaximumInterval:    15 * time.Second,
		},
	}
}

// stopped is an error that ends the run, with the reason browsers and the
// performance row are given.
type stopped struct {
	code string
	err  error
}

func (s *stopped) Error() string { return s.code + ": " + s.err.Error() }
func (s *stopped) Unwrap() error { return s.err }

// run is one performance's state. It exists only in the workflow.
type run struct {
	in performance.WorkflowInput
	// revision counts published updates. It goes up before each publish.
	revision int
	// state is what browsers are shown: published with each update, and
	// returned by the state query.
	state performance.State
	// votes is who has voted for what in the current turn, by voter ID. It
	// is never published: a snapshot shows a voter only their own entry.
	votes map[string]string
	// counts is how many votes each of the current turn's choices has.
	counts map[string]int

	// prepared holds each turn's line once its child workflow has tagged and
	// voiced it; ready counts them.
	prepared [performance.Turns]PreparedLine
	ready    int
	// lineFailure is the first child workflow that failed. Recording it
	// cancels whatever the run was waiting on (stop), and the run ends.
	lineFailure error
	stop        workflow.CancelFunc
}

// Performance runs one performance from Start to the premiere. If it cannot
// continue, it marks the performance failed and tells browsers before
// ending as a failed workflow.
func Performance(ctx workflow.Context, in performance.WorkflowInput) error {
	r := &run{in: in, state: performance.State{Phase: performance.PhaseWaiting, PerformanceID: in.PerformanceID}}
	playing, stop := workflow.WithCancel(ctx)
	r.stop = stop
	err := r.play(ctx, playing)
	// A line that could not be voiced is why the run stopped, whatever its
	// cancellation then did to the step that was under way.
	if r.lineFailure != nil {
		err = &stopped{code: performance.FailureSpeech, err: r.lineFailure}
	}
	if err != nil {
		r.fail(ctx, err)
	}
	return err
}

// fail records that the run cannot continue: first on the performance row,
// then to browsers. The row comes first so that a browser told of the
// failure, or one connecting just after, finds the row agreeing.
//
// The original error is what the workflow returns, whatever happens here.
func (r *run) fail(ctx workflow.Context, cause error) {
	code := performance.FailureInternal
	var s *stopped
	if errors.As(cause, &s) {
		code = s.code
	}
	// A cancelled workflow can still do this much.
	ctx, cancel := workflow.NewDisconnectedContext(ctx)
	defer cancel()

	var a *activities.Activities
	err := workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, databaseOptions), a.Fail, activities.FailInput{
		PerformanceID: r.in.PerformanceID, Reason: code,
	}).Get(ctx, nil)
	if err != nil {
		logger(ctx, r.in.RoomID, r.in.PerformanceID).Error("could not mark the performance failed", "error", err)
	}
	r.publish(ctx, performance.State{
		Phase: performance.PhaseFailed, Turn: r.state.Turn, Failure: performance.NewFailure(code),
	})
}

// play is the performance itself. It runs in ctx, which is cancelled if a
// line cannot be voiced; root is the workflow's own context, which the line
// workflows are started from and waited on in.
func (r *run) play(root, ctx workflow.Context) error {
	in := r.in
	// Registered before anything else, so a browser connecting at any moment
	// of the run can be answered.
	err := workflow.SetQueryHandler(ctx, performance.QueryState, func(voterID string) (performance.Snapshot, error) {
		state := r.state
		state.YourVote = r.votes[voterID]
		return performance.Snapshot{Revision: r.revision, State: state}, nil
	})
	if err != nil {
		return err
	}

	// Named only to refer to its methods; activities run on the worker's own.
	var a *activities.Activities

	var room activities.RoomContext
	if err := workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, databaseOptions), a.FetchContext, in.RoomID).Get(ctx, &room); err != nil {
		return err
	}

	var script performance.Script
	var history []providers.Line

	for turn := 1; turn <= performance.Turns; turn++ {
		plan := Schedule[turn-1]
		speaker := room.CharacterA
		if plan.Speaker == "B" {
			speaker = room.CharacterB
		}

		// A turn begins, with nobody having voted in it.
		r.votes, r.counts = map[string]string{}, nil
		r.publish(ctx, performance.State{
			Phase: performance.PhaseGenerating, Turn: turn, Speaker: plan.Speaker, Beat: plan.Beat,
		})

		var generated activities.ChoicesResult
		err := workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, choicesOptions(room.ChoicesRetryWindow)), a.GenerateChoices, activities.ChoicesInput{
			RoomID:         in.RoomID,
			Turn:           turn,
			Speaker:        plan.Speaker,
			Beat:           plan.Beat,
			Scene:          room.Scene,
			CharacterA:     room.CharacterA,
			CharacterB:     room.CharacterB,
			History:        history,
			TurnsRemaining: performance.Turns - turn,
		}).Get(ctx, &generated)
		if err != nil {
			return &stopped{code: performance.FailureChoices, err: err}
		}
		// The activity has already checked the lines; this guards the
		// indexing below against a worker running different code.
		if len(generated.Lines) != choicesPerTurn {
			return &stopped{code: performance.FailureChoices, err: temporal.NewApplicationError(
				fmt.Sprintf("turn %d: %d choices, not %d", turn, len(generated.Lines), choicesPerTurn), "BadChoices")}
		}
		choices := make([]performance.Choice, choicesPerTurn)
		for i, text := range generated.Lines {
			choices[i] = performance.Choice{ID: fmt.Sprintf("c%d", i+1), Text: text}
		}

		// The timer is started before the deadline is published, so browsers
		// are shown the deadline the workflow enforces, however long the
		// publish takes.
		r.counts = make(map[string]int, len(choices))
		for _, choice := range choices {
			r.counts[choice.ID] = 0
		}
		opened := workflow.Now(ctx)
		deadline := opened.Add(room.VoteWindow)
		timer := workflow.NewTimer(ctx, room.VoteWindow)
		r.publish(ctx, performance.State{
			Phase: performance.PhaseVoting, Turn: turn, Speaker: plan.Speaker, Beat: plan.Beat,
			Choices: choices, Deadline: &deadline,
		})

		if err := r.collectVotes(ctx, timer, round{turn: turn, opened: opened, deadline: deadline}); err != nil {
			return err
		}
		counts := r.counts
		winner, decidedBy := pickWinner(ctx, choices, counts)

		history = append(history, providers.Line{Speaker: plan.Speaker, Text: winner.Text})
		r.state.Lines = append(r.state.Lines, performance.Line{Turn: turn, Speaker: plan.Speaker, Text: winner.Text})
		r.publish(ctx, performance.State{
			Phase: performance.PhasePreparingLine, Turn: turn, Speaker: plan.Speaker, Beat: plan.Beat,
			Choices: choices,
			Winner:  &performance.Winner{ChoiceID: winner.ID, Text: winner.Text, Counts: counts, DecidedBy: decidedBy},
		})

		// The line is tagged and voiced in the background, and the next turn
		// goes ahead meanwhile: nothing needs the recording before the
		// premiere, and the next lines are written from the untagged text.
		r.prepareLine(root, LineInput{
			PerformanceID: in.PerformanceID, RoomID: in.RoomID, Turn: turn,
			Speaker: plan.Speaker, Beat: plan.Beat, Character: speaker, Text: winner.Text,
			LipSync: in.LipSync, Resolution: room.LipSyncResolution,
		})

		script.Turns = append(script.Turns, performance.ScriptTurn{
			Turn: turn, Speaker: plan.Speaker, Beat: plan.Beat,
			Choices: choices, Counts: counts, Winner: winner, DecidedBy: decidedBy,
			Model: generated.Model,
		})
	}

	// Fan in: the premiere waits for every line. Without lip sync,
	// browsers stay on the last winning line until then. With it, the wait
	// can be minutes, so they are told how many lines are ready, each time
	// that changes (docs/decisions.md, D055).
	for r.ready < performance.Turns {
		if in.LipSync {
			r.publish(ctx, performance.State{
				Phase: performance.PhasePreparingPremiere, Turn: performance.Turns,
				Progress: &performance.Progress{Ready: r.ready, Total: performance.Turns},
			})
		}
		ready := r.ready
		if err := workflow.Await(ctx, func() bool { return r.ready != ready }); err != nil {
			return err
		}
	}
	audioKeys := make([]string, performance.Turns)
	for i, line := range r.prepared {
		script.Turns[i].TaggedText = line.TaggedText
		script.Turns[i].TagsApplied = line.TagsApplied
		script.Turns[i].VideoKey = line.VideoKey
		audioKeys[i] = line.AudioKey
	}
	if in.LipSync {
		// The premiere is the next publish. Its revision is saved, so that
		// a browser told of the finished room from the row is given the
		// same one, and drops any progress update still on its way.
		script.PremiereRevision = r.revision + 1
	}

	// The start time is fixed first and saved with the script, so that a
	// browser connecting later is told the same time as those watching now.
	// The save therefore comes out of the countdown: a moment, normally. It
	// is kept to what the database stores, a UTC time in microseconds, so
	// the saved time and the published one are the same to the last digit.
	startsAt := workflow.Now(ctx).Add(room.PremiereCountdown).UTC().Truncate(time.Microsecond)

	// Saved before the premiere is announced, so the recordings and the
	// script are there for any browser that hears about it.
	err = workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, databaseOptions), a.Complete, activities.CompleteInput{
		PerformanceID: in.PerformanceID, Script: script, AudioKeys: audioKeys, PremiereStartsAt: startsAt,
	}).Get(ctx, nil)
	if err != nil {
		return err
	}

	clips := make([]performance.Clip, performance.Turns)
	for i, plan := range Schedule {
		clips[i] = performance.Clip{
			Turn: i + 1, Speaker: plan.Speaker, URL: performance.AudioURL(in.PerformanceID, i+1),
		}
		if r.prepared[i].VideoKey != "" {
			clips[i].VideoURL = performance.VideoURL(in.PerformanceID, i+1)
		}
	}
	r.publish(ctx, performance.State{
		Phase: performance.PhasePremiere, Turn: performance.Turns,
		Premiere: &performance.Premiere{StartsAt: startsAt, Clips: clips},
	})
	return nil
}

// prepareLine starts a turn's line workflow and returns at once. A
// coroutine waits for it: it keeps the result, or, if the line could not be
// voiced, records why and stops the run.
//
// Any failure of the child counts as speech failing: tagging falls back
// rather than fail, so speech, or the child's own timeout, is all that can.
func (r *run) prepareLine(root workflow.Context, in LineInput) {
	timeout := lineTimeout
	if in.LipSync {
		timeout += lipSyncTimeout
	}
	child := workflow.ExecuteChildWorkflow(workflow.WithChildOptions(root, workflow.ChildWorkflowOptions{
		WorkflowID:               LineWorkflowID(in.PerformanceID, in.Turn),
		WorkflowExecutionTimeout: timeout,
	}), PrepareLineName, in)
	workflow.Go(root, func(ctx workflow.Context) {
		var line PreparedLine
		if err := child.Get(ctx, &line); err != nil {
			if r.lineFailure == nil {
				logger(ctx, r.in.RoomID, r.in.PerformanceID).Error("a line could not be voiced; stopping the run", "turn", in.Turn, "error", err)
				r.lineFailure = err
				r.stop()
			}
			return
		}
		r.prepared[in.Turn-1] = line
		r.ready++
	})
}

// publish moves the performance into a new phase: it replaces the state
// (keeping the performance ID and the lines so far), raises the revision,
// and sends the update to browsers.
//
// A publish that fails is logged and the run carries on. A browser that then
// sees a revision skip a number knows it missed one and asks for a fresh
// snapshot (docs/system-design.md, section 4).
func (r *run) publish(ctx workflow.Context, next performance.State) {
	next.PerformanceID = r.state.PerformanceID
	next.Lines = r.state.Lines
	r.state = next
	r.revision++

	var a *activities.Activities
	err := workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, publishOptions), a.Publish, performance.Update{
		RoomID: r.in.RoomID, Revision: r.revision, State: r.state,
	}).Get(ctx, nil)
	if err != nil {
		logger(ctx, r.in.RoomID, r.in.PerformanceID).Warn("publish failed; continuing", "revision", r.revision, "phase", next.Phase, "error", err)
	}
}

// round is one turn's voting round.
type round struct {
	turn int
	// opened and deadline bound when a vote may have been received.
	opened   time.Time
	deadline time.Time
}

// collectVotes records the round's votes in r.votes and returns when the
// round has closed.
//
// Whether a vote was in time is decided by the server's receipt stamp, not
// by whether this code saw the vote before the timer. After a worker outage
// the timer and every vote sent meanwhile are delivered together, and the
// order this code sees them in says nothing about the order they happened in
// (docs/decisions.md, D033). So when the timer is delivered, every vote
// still waiting is handled before the round closes.
//
// A vote that arrives after that is not seen here at all. It waits for the
// next round, which ignores it for naming the wrong turn.
func (r *run) collectVotes(ctx workflow.Context, timer workflow.Future, rd round) error {
	votes := workflow.GetSignalChannel(ctx, performance.SignalVote)

	var timerErr error
	closed := false
	selector := workflow.NewSelector(ctx)
	selector.AddFuture(timer, func(f workflow.Future) {
		timerErr = f.Get(ctx, nil)
		closed = true
	})
	selector.AddReceive(votes, func(c workflow.ReceiveChannel, _ bool) {
		var vote performance.Vote
		c.Receive(ctx, &vote)
		r.record(rd, vote)
	})
	for !closed {
		selector.Select(ctx)
	}
	if timerErr != nil {
		return timerErr
	}

	for {
		var vote performance.Vote
		if !votes.ReceiveAsync(&vote) {
			return nil
		}
		r.record(rd, vote)
	}
}

// record keeps a vote if it counts: it is for this turn, for one of the
// three choices, the voter's first this turn, and was received while the
// round was open. Anything else is dropped without a trace (D023).
//
// The opening time matters because choice IDs are the same every turn: a
// vote sent before the choices were shown would otherwise count.
func (r *run) record(rd round, vote performance.Vote) {
	if vote.Turn != rd.turn || vote.VoterID == "" {
		return
	}
	if vote.ReceivedAt.Before(rd.opened) || vote.ReceivedAt.After(rd.deadline) {
		return
	}
	if _, voted := r.votes[vote.VoterID]; voted {
		return
	}
	if _, offered := r.counts[vote.ChoiceID]; !offered {
		return
	}
	r.votes[vote.VoterID] = vote.ChoiceID
	r.counts[vote.ChoiceID]++
}

// pickWinner chooses the round's winner from the counts. When the counts do
// not settle it (a tie, or no votes), it picks at random among the leaders;
// the pick is recorded in history by SideEffect, so a replay after a worker
// restart reaches the same answer and the tie is settled only once.
func pickWinner(ctx workflow.Context, choices []performance.Choice, counts map[string]int) (performance.Choice, string) {
	top, decidedBy := leaders(choices, counts)
	if len(top) == 1 {
		return top[0], decidedBy
	}
	var index int
	pick := workflow.SideEffect(ctx, func(workflow.Context) any { return rand.IntN(len(top)) })
	if err := pick.Get(&index); err != nil || index < 0 || index >= len(top) {
		// Cannot happen: an int always decodes. The first leader is as good
		// as any, and the same on every replay.
		index = 0
	}
	return top[index], decidedBy
}
