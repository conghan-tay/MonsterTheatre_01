package performance

import (
	"context"
	"errors"
	"fmt"
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
	// lipSyncRunTimeout is for a run with lip sync: turn 8's line may be
	// waited for for up to 25 minutes after the turns end (D055).
	lipSyncRunTimeout = 45 * time.Minute
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
	// LipSync asks for a lip-sync video of each line, as the director
	// chose when pressing Start.
	LipSync bool
}

// Workflows reaches performance workflows through Temporal. It is the
// server's only route to the worker.
type Workflows struct {
	Client    client.Client
	TaskQueue string
}

// RunTimeout is how long a run may take before it is ended as hung.
func RunTimeout(lipSync bool) time.Duration {
	if lipSync {
		return lipSyncRunTimeout
	}
	return runTimeout
}

// Start starts the workflow for a performance. It is safe to repeat: if the
// workflow is already running, or has already run, nothing happens.
func (w *Workflows) Start(ctx context.Context, performanceID, roomID string, lipSync bool) error {
	_, err := w.Client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                 WorkflowID(performanceID),
		TaskQueue:          w.TaskQueue,
		WorkflowRunTimeout: RunTimeout(lipSync),
		// A running one is left running...
		WorkflowIDConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
		// ...and a finished one is never run again: one performance per room.
		WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
	}, WorkflowName, WorkflowInput{PerformanceID: performanceID, RoomID: roomID, LipSync: lipSync})
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

// ErrWorkflowClosed means the performance's workflow has ended: completed,
// failed, timed out, or terminated.
var ErrWorkflowClosed = errors.New("performance workflow is closed")

// State asks a running performance's workflow for its current state, as
// seen by one voter. It needs a worker to answer, so it fails while none is
// running.
//
// A workflow that has ended is not asked. Left to itself, Temporal would
// have a worker rebuild the ended run and answer with the state it stopped
// in, and a run that died mid-round would look like a round still open. The
// query is sent on condition that the workflow is open, and ErrWorkflowClosed
// is returned if it is not.
func (w *Workflows) State(ctx context.Context, performanceID, voterID string) (Snapshot, error) {
	var snapshot Snapshot
	resp, err := w.Client.QueryWorkflowWithOptions(ctx, &client.QueryWorkflowWithOptionsRequest{
		WorkflowID:           WorkflowID(performanceID),
		QueryType:            QueryState,
		Args:                 []any{voterID},
		QueryRejectCondition: enumspb.QUERY_REJECT_CONDITION_NOT_OPEN,
	})
	if err != nil {
		return snapshot, err
	}
	if resp.QueryRejected != nil {
		return snapshot, fmt.Errorf("%w (%s)", ErrWorkflowClosed, resp.QueryRejected.GetStatus())
	}
	return snapshot, resp.QueryResult.Get(&snapshot)
}
