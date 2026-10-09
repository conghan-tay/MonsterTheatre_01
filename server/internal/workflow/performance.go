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
	choicesOptions = workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	}
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

// run is one performance's state. It exists only in the workflow.
type run struct {
	in performance.WorkflowInput
	// revision counts published updates. It goes up before each publish.
	revision int
	// state is what browsers are shown: published with each update, and
	// returned by the state query.
	state performance.State
}

// Performance runs one performance from Start to the premiere.
func Performance(ctx workflow.Context, in performance.WorkflowInput) error {
	r := &run{in: in, state: performance.State{Phase: performance.PhaseWaiting, PerformanceID: in.PerformanceID}}
	// Registered before anything else, so a browser connecting at any moment
	// of the run can be answered.
	err := workflow.SetQueryHandler(ctx, performance.QueryState, func() (performance.Snapshot, error) {
		return performance.Snapshot{Revision: r.revision, State: r.state}, nil
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
	var audioKeys []string
	var history []providers.Line

	for turn := 1; turn <= performance.Turns; turn++ {
		plan := Schedule[turn-1]
		speaker := room.CharacterA
		if plan.Speaker == "B" {
			speaker = room.CharacterB
		}

		// A turn begins.
		r.publish(ctx, performance.State{
			Phase: performance.PhaseGenerating, Turn: turn, Speaker: plan.Speaker, Beat: plan.Beat,
		})

		var texts []string
		err := workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, choicesOptions), a.GenerateChoices, activities.ChoicesInput{
			RoomID:         in.RoomID,
			Turn:           turn,
			Speaker:        plan.Speaker,
			Beat:           plan.Beat,
			Scene:          room.Scene,
			CharacterA:     room.CharacterA,
			CharacterB:     room.CharacterB,
			History:        history,
			TurnsRemaining: performance.Turns - turn,
		}).Get(ctx, &texts)
		if err != nil {
			return err
		}
		if len(texts) != choicesPerTurn {
			return temporal.NewApplicationError(
				fmt.Sprintf("turn %d: the language model returned %d choices, not %d", turn, len(texts), choicesPerTurn),
				"BadChoices")
		}
		choices := make([]performance.Choice, choicesPerTurn)
		for i, text := range texts {
			choices[i] = performance.Choice{ID: fmt.Sprintf("c%d", i+1), Text: text}
		}

		// The timer is started before the deadline is published, so browsers
		// are shown the deadline the workflow enforces, however long the
		// publish takes.
		deadline := workflow.Now(ctx).Add(room.VoteWindow)
		timer := workflow.NewTimer(ctx, room.VoteWindow)
		r.publish(ctx, performance.State{
			Phase: performance.PhaseVoting, Turn: turn, Speaker: plan.Speaker, Beat: plan.Beat,
			Choices: choices, Deadline: &deadline,
		})

		counts, err := collectVotes(ctx, timer, choices)
		if err != nil {
			return err
		}
		winner, decidedBy := pickWinner(ctx, choices, counts)

		history = append(history, providers.Line{Speaker: plan.Speaker, Text: winner.Text})
		r.state.Lines = append(r.state.Lines, performance.Line{Turn: turn, Speaker: plan.Speaker, Text: winner.Text})
		r.publish(ctx, performance.State{
			Phase: performance.PhasePreparingLine, Turn: turn, Speaker: plan.Speaker, Beat: plan.Beat,
			Choices: choices,
			Winner:  &performance.Winner{ChoiceID: winner.ID, Text: winner.Text, Counts: counts, DecidedBy: decidedBy},
		})

		// Tagging and speech publish nothing: browsers stay on the winning
		// line until the next turn begins.
		var tagged string
		err = workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, tagsOptions), a.EnhanceTags, activities.TagsInput{
			RoomID: in.RoomID, Turn: turn, Speaker: plan.Speaker, Text: winner.Text,
		}).Get(ctx, &tagged)
		if err != nil {
			return err
		}
		var audioKey string
		err = workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, speechOptions), a.Synthesize, activities.SpeechInput{
			PerformanceID: in.PerformanceID, RoomID: in.RoomID, Turn: turn, VoiceID: speaker.VoiceID, Text: tagged,
		}).Get(ctx, &audioKey)
		if err != nil {
			return err
		}

		audioKeys = append(audioKeys, audioKey)
		script.Turns = append(script.Turns, performance.ScriptTurn{
			Turn: turn, Speaker: plan.Speaker, Beat: plan.Beat,
			Choices: choices, Counts: counts, Winner: winner, DecidedBy: decidedBy,
			TaggedText: tagged, Model: room.Model,
		})
	}

	// Saved before the premiere is announced, so the recordings and the
	// script are there for any browser that hears about it.
	err = workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, databaseOptions), a.Complete, activities.CompleteInput{
		PerformanceID: in.PerformanceID, Script: script, AudioKeys: audioKeys,
	}).Get(ctx, nil)
	if err != nil {
		return err
	}

	clips := make([]performance.Clip, performance.Turns)
	for i, plan := range Schedule {
		clips[i] = performance.Clip{
			Turn: i + 1, Speaker: plan.Speaker, URL: performance.AudioURL(in.PerformanceID, i+1),
		}
	}
	r.publish(ctx, performance.State{
		Phase: performance.PhasePremiere, Turn: performance.Turns,
		Premiere: &performance.Premiere{StartsAt: workflow.Now(ctx).Add(room.PremiereCountdown), Clips: clips},
	})
	return nil
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
		workflow.GetLogger(ctx).Warn("publish failed; continuing", "revision", r.revision, "phase", next.Phase, "error", err)
	}
}

// collectVotes waits for the round to close and returns each choice's count.
//
// Nobody can vote yet, so it only waits for the timer and every count is
// zero. Step 10 receives vote signals here, under the rules in D033.
func collectVotes(ctx workflow.Context, timer workflow.Future, choices []performance.Choice) (map[string]int, error) {
	if err := timer.Get(ctx, nil); err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(choices))
	for _, choice := range choices {
		counts[choice.ID] = 0
	}
	return counts, nil
}

// pickWinner chooses the round's winner. With no votes it is picked at
// random; the pick is recorded in history by SideEffect, so a replay after a
// worker restart reaches the same answer. Step 10 adds majority and ties.
func pickWinner(ctx workflow.Context, choices []performance.Choice, _ map[string]int) (performance.Choice, string) {
	var index int
	pick := workflow.SideEffect(ctx, func(workflow.Context) any { return rand.IntN(len(choices)) })
	if err := pick.Get(&index); err != nil || index < 0 || index >= len(choices) {
		// Cannot happen: an int always decodes. The first choice is as good
		// as any, and the same on every replay.
		index = 0
	}
	return choices[index], performance.DecidedByNoVotes
}
