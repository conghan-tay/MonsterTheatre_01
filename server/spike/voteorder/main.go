// Command voteorder is a throwaway spike (implementation plan, step 2).
//
// Question: when a worker returns from an outage and receives a vote sent
// before the deadline, the timer firing, and a vote sent after the deadline in
// one batch, can Go workflow code still tell which vote was in time?
//
// Usage:
//
//	voteorder          run every scenario and print a report
//	voteorder worker   run only the worker (the runner starts this itself)
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const (
	taskQueue  = "spike-vote-order"
	signalName = "vote"
	window     = 4 * time.Second
	repeats    = 3
)

// Vote is the signal payload. SentAt is the sender's clock when it sent the
// signal, standing in for "the server's receipt time" (variant D).
type Vote struct {
	Label  string
	SentAt time.Time
}

// Observation is one thing the workflow saw, in the order it saw it.
type Observation struct {
	Kind  string // "signal" or "timer"
	Label string
	// Variants A/B: had the selector already delivered the timer?
	TimerDelivered bool
	// Variant C: was the timer future ready when this signal was handled?
	TimerReady bool
	// Variant D: was the vote stamped at or before the deadline?
	StampInTime bool
}

type Result struct {
	Deadline time.Time
	Log      []Observation
}

// VoteWindow starts a timer and collects signals until the timer has fired
// and expectSignals signals have been seen. order is "signal-first" (variant
// A) or "timer-first" (variant B): the order the cases are registered on the
// selector.
func VoteWindow(ctx workflow.Context, order string, expectSignals int) (Result, error) {
	res := Result{Deadline: workflow.Now(ctx).Add(window)}
	timer := workflow.NewTimer(ctx, window)
	votes := workflow.GetSignalChannel(ctx, signalName)

	timerDelivered := false
	seen := 0
	selector := workflow.NewSelector(ctx)

	addSignal := func() {
		selector.AddReceive(votes, func(c workflow.ReceiveChannel, _ bool) {
			var v Vote
			c.Receive(ctx, &v)
			seen++
			res.Log = append(res.Log, Observation{
				Kind:           "signal",
				Label:          v.Label,
				TimerDelivered: timerDelivered,
				TimerReady:     timer.IsReady(),
				StampInTime:    !v.SentAt.After(res.Deadline),
			})
		})
	}
	addTimer := func() {
		selector.AddFuture(timer, func(f workflow.Future) {
			_ = f.Get(ctx, nil)
			timerDelivered = true
			res.Log = append(res.Log, Observation{Kind: "timer"})
		})
	}
	if order == "signal-first" {
		addSignal()
		addTimer()
	} else {
		addTimer()
		addSignal()
	}

	for !timerDelivered || seen < expectSignals {
		selector.Select(ctx)
	}
	return res, nil
}

// RoundResult is what the candidate algorithm for step 10 decides.
type RoundResult struct {
	Accepted []string
	Rejected []string
}

// VoteRound is the candidate for the real workflow: classify every vote by
// its receipt-time stamp, and when the timer is delivered, drain whatever is
// still buffered before closing the round. Votes arriving after that are not
// seen at all.
func VoteRound(ctx workflow.Context) (RoundResult, error) {
	var res RoundResult
	deadline := workflow.Now(ctx).Add(window)
	timer := workflow.NewTimer(ctx, window)
	votes := workflow.GetSignalChannel(ctx, signalName)

	classify := func(v Vote) {
		if v.SentAt.After(deadline) {
			res.Rejected = append(res.Rejected, v.Label)
			return
		}
		res.Accepted = append(res.Accepted, v.Label)
	}

	closed := false
	selector := workflow.NewSelector(ctx)
	selector.AddFuture(timer, func(f workflow.Future) {
		_ = f.Get(ctx, nil)
		closed = true
	})
	selector.AddReceive(votes, func(c workflow.ReceiveChannel, _ bool) {
		var v Vote
		c.Receive(ctx, &v)
		classify(v)
	})
	for !closed {
		selector.Select(ctx)
	}
	for {
		var v Vote
		if !votes.ReceiveAsync(&v) {
			break
		}
		classify(v)
	}
	return res, nil
}

func main() {
	c, err := client.Dial(client.Options{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot reach Temporal on localhost:7233:", err)
		os.Exit(1)
	}
	defer c.Close()

	if len(os.Args) > 1 && os.Args[1] == "worker" {
		w := worker.New(c, taskQueue, worker.Options{})
		w.RegisterWorkflow(VoteWindow)
		w.RegisterWorkflow(VoteRound)
		if err := w.Run(worker.InterruptCh()); err != nil {
			os.Exit(1)
		}
		return
	}

	if err := runAll(c); err != nil {
		fmt.Fprintln(os.Stderr, "spike failed:", err)
		os.Exit(1)
	}
}

type scenario struct {
	name   string
	outage bool
	// votes sent before the deadline, then votes sent after it
	before, after []string
}

var scenarios = []scenario{
	{name: "control (worker up)", outage: false, before: []string{"before"}, after: []string{"after"}},
	{name: "outage across deadline", outage: true, before: []string{"before"}, after: []string{"after"}},
	{name: "outage, both votes early", outage: true, before: []string{"before-1", "before-2"}, after: nil},
}

type tally struct{ right, wrong int }

func (t *tally) add(ok bool) {
	if ok {
		t.right++
	} else {
		t.wrong++
	}
}

func runAll(c client.Client) error {
	ctx := context.Background()
	summary := map[string]*tally{}
	var keys []string

	for _, sc := range scenarios {
		for _, order := range []string{"signal-first", "timer-first"} {
			for i := 1; i <= repeats; i++ {
				res, history, err := runOnce(ctx, c, sc, order, i)
				if err != nil {
					return fmt.Errorf("%s / %s / run %d: %w", sc.name, order, i, err)
				}
				fmt.Printf("\n== %s | selector %s | run %d\n", sc.name, order, i)
				fmt.Printf("history : %s\n", history)
				fmt.Printf("workflow: %s\n", describe(res))

				selectorVariant := "A selector, signal case first"
				if order == "timer-first" {
					selectorVariant = "B selector, timer case first"
				}
				for _, o := range res.Log {
					if o.Kind != "signal" {
						continue
					}
					truth := strings.HasPrefix(o.Label, "before")
					for variant, verdict := range map[string]bool{
						selectorVariant:        !o.TimerDelivered,
						"C timer.IsReady()":    !o.TimerReady,
						"D receipt-time stamp": o.StampInTime,
					} {
						key := sc.name + " | " + variant
						if summary[key] == nil {
							summary[key] = &tally{}
							keys = append(keys, key)
						}
						summary[key].add(verdict == truth)
					}
				}
			}
		}
	}

	fmt.Println("\n== CANDIDATE ALGORITHM (stamp check + drain when the timer is delivered)")
	for _, sc := range scenarios {
		for i := 1; i <= repeats; i++ {
			var res RoundResult
			history, err := execute(ctx, c, sc, fmt.Sprintf("round-%d", i), &res, VoteRound)
			if err != nil {
				return fmt.Errorf("%s / candidate / run %d: %w", sc.name, i, err)
			}
			ok := strings.Join(res.Accepted, ",") == strings.Join(sc.before, ",")
			fmt.Printf("%-28s | run %d | accepted=%v rejected=%v | correct=%t\n            history: %s\n",
				sc.name, i, res.Accepted, res.Rejected, ok, history)
		}
	}

	fmt.Println("\n== SUMMARY (votes classified correctly / total)")
	for _, sc := range scenarios {
		for _, key := range keys {
			if strings.HasPrefix(key, sc.name+" | ") {
				t := summary[key]
				fmt.Printf("%-28s | %-30s | %d/%d\n", sc.name, strings.TrimPrefix(key, sc.name+" | "), t.right, t.right+t.wrong)
			}
		}
	}
	return nil
}

func runOnce(ctx context.Context, c client.Client, sc scenario, order string, i int) (Result, string, error) {
	var res Result
	history, err := execute(ctx, c, sc, fmt.Sprintf("%s-%d", order, i), &res, VoteWindow, order, len(sc.before)+len(sc.after))
	return res, history, err
}

// execute runs one workflow through a scenario and decodes its result into out.
func execute(ctx context.Context, c client.Client, sc scenario, tag string, out any, wf any, args ...any) (string, error) {

	w, err := startWorker()
	if err != nil {
		return "", err
	}
	defer func() { stopWorker(w) }()

	id := fmt.Sprintf("spike-%d-%s", time.Now().UnixNano(), tag)
	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: id, TaskQueue: taskQueue}, wf, args...)
	if err != nil {
		return "", err
	}
	started := time.Now()

	// Let the first workflow task complete so the timer is started.
	if err := waitForEvent(ctx, c, id, enumspb.EVENT_TYPE_TIMER_STARTED); err != nil {
		return "", err
	}

	if sc.outage {
		stopWorker(w)
		w = nil
	}

	time.Sleep(500 * time.Millisecond)
	for _, label := range sc.before {
		if err := c.SignalWorkflow(ctx, id, "", signalName, Vote{Label: label, SentAt: time.Now()}); err != nil {
			return "", err
		}
	}

	// Wait until the timer has fired on the server.
	time.Sleep(time.Until(started.Add(window + 1500*time.Millisecond)))
	if err := waitForEvent(ctx, c, id, enumspb.EVENT_TYPE_TIMER_FIRED); err != nil {
		return "", err
	}

	for _, label := range sc.after {
		// With the worker up, VoteRound has already finished by now, so a
		// late vote can fail with "workflow execution already completed".
		// That is the expected production behaviour, not a spike failure.
		_ = c.SignalWorkflow(ctx, id, "", signalName, Vote{Label: label, SentAt: time.Now()})
	}

	if sc.outage {
		time.Sleep(500 * time.Millisecond)
		if w, err = startWorker(); err != nil {
			return "", err
		}
	}

	getCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := run.Get(getCtx, out); err != nil {
		return "", err
	}
	return summariseHistory(ctx, c, id)
}

func startWorker() (*exec.Cmd, error) {
	cmd := exec.Command(os.Args[0], "worker")
	cmd.Stderr = nil
	cmd.Stdout = nil
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	time.Sleep(1500 * time.Millisecond) // let it begin polling
	return cmd, nil
}

// stopWorker kills the worker outright, as a crash would.
func stopWorker(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

func waitForEvent(ctx context.Context, c client.Client, id string, want enumspb.EventType) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		iter := c.GetWorkflowHistory(ctx, id, "", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
		for iter.HasNext() {
			ev, err := iter.Next()
			if err != nil {
				return err
			}
			if ev.GetEventType() == want {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", want)
}

// summariseHistory prints the events that matter, in the order Temporal
// recorded them. "WFT" is a workflow task: the unit of work handed to a worker.
func summariseHistory(ctx context.Context, c client.Client, id string) (string, error) {
	var parts []string
	iter := c.GetWorkflowHistory(ctx, id, "", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for iter.HasNext() {
		ev, err := iter.Next()
		if err != nil {
			return "", err
		}
		switch ev.GetEventType() {
		case enumspb.EVENT_TYPE_TIMER_STARTED:
			parts = append(parts, "TimerStarted")
		case enumspb.EVENT_TYPE_TIMER_FIRED:
			parts = append(parts, "TimerFired")
		case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_SIGNALED:
			var v Vote
			_ = converter.GetDefaultDataConverter().FromPayloads(
				ev.GetWorkflowExecutionSignaledEventAttributes().GetInput(), &v)
			parts = append(parts, "Signal("+v.Label+")")
		case enumspb.EVENT_TYPE_WORKFLOW_TASK_STARTED:
			parts = append(parts, "[WFT")
		case enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED:
			parts = append(parts, "WFT]")
		case enumspb.EVENT_TYPE_WORKFLOW_TASK_TIMED_OUT:
			parts = append(parts, "WFT-timed-out")
		}
	}
	return strings.Join(parts, " "), nil
}

func describe(res Result) string {
	var parts []string
	for _, o := range res.Log {
		if o.Kind == "timer" {
			parts = append(parts, "timer")
			continue
		}
		parts = append(parts, fmt.Sprintf("signal(%s){timerDelivered=%t timerReady=%t stampInTime=%t}",
			o.Label, o.TimerDelivered, o.TimerReady, o.StampInTime))
	}
	return strings.Join(parts, " -> ")
}
