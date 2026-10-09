package performance

import (
	"context"
	"errors"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
)

const (
	// WorkflowName is the name the performance workflow is registered under.
	// The server starts it by name, so it does not link the workflow's code.
	WorkflowName = "Performance"
	// QueryState is the query that returns a Snapshot. Its argument is the
	// asking voter's ID, so the answer can include that voter's own vote.
	QueryState = "state"
	// SignalVote is the signal that carries a Vote.
	SignalVote = "vote"
	// StreamKey is the Redis stream every update is published to.
	StreamKey = "performance-updates"

	// runTimeout bounds a run that hangs (docs/system-design.md, section 4).
	runTimeout = 30 * time.Minute
)

// Fields of a stream entry.
const (
	StreamFieldRoomID   = "room_id"
	StreamFieldRevision = "revision"
	StreamFieldMessage  = "message"
)

// WorkflowID is the fixed ID of a performance's workflow. Being derived from
// the performance ID is what makes Start safe to repeat.
func WorkflowID(performanceID string) string {
	return "performance/" + performanceID
}

// WorkflowInput is the performance workflow's argument.
type WorkflowInput struct {
	PerformanceID string
	RoomID        string
}

// Workflows reaches performance workflows through Temporal. It is the
// server's only route to the worker.
type Workflows struct {
	Client    client.Client
	TaskQueue string
}

// Start starts the workflow for a performance. It is safe to repeat: if the
// workflow is already running, or has already run, nothing happens.
func (w *Workflows) Start(ctx context.Context, performanceID, roomID string) error {
	_, err := w.Client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                 WorkflowID(performanceID),
		TaskQueue:          w.TaskQueue,
		WorkflowRunTimeout: runTimeout,
		// A running one is left running...
		WorkflowIDConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
		// ...and a finished one is never run again: one performance per room.
		WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
	}, WorkflowName, WorkflowInput{PerformanceID: performanceID, RoomID: roomID})
	var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
	if errors.As(err, &alreadyStarted) {
		return nil
	}
	return err
}

// Vote hands a vote to a performance's workflow. Temporal stores it even
// while no worker is running; it fails if the performance has ended.
func (w *Workflows) Vote(ctx context.Context, performanceID string, vote Vote) error {
	return w.Client.SignalWorkflow(ctx, WorkflowID(performanceID), "", SignalVote, vote)
}

// State asks a running performance's workflow for its current state, as
// seen by one voter. It needs a worker to answer, so it fails while none is
// running.
func (w *Workflows) State(ctx context.Context, performanceID, voterID string) (Snapshot, error) {
	var snapshot Snapshot
	value, err := w.Client.QueryWorkflow(ctx, WorkflowID(performanceID), "", QueryState, voterID)
	if err != nil {
		return snapshot, err
	}
	return snapshot, value.Get(&snapshot)
}
