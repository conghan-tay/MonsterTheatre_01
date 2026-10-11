package theater

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/serviceerror"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/performance"
)

// scriptedWorkflow answers each state query by its number, starting at 1,
// so a test can give the connect-time query and a later resync different
// answers. It is safe to ask from the connection's goroutines.
type scriptedWorkflow struct {
	mu     sync.Mutex
	calls  int
	answer func(call int) (performance.Snapshot, error)
}

func (f *scriptedWorkflow) State(context.Context, string, string) (performance.Snapshot, error) {
	f.mu.Lock()
	f.calls++
	call := f.calls
	f.mu.Unlock()
	return f.answer(call)
}

func (f *scriptedWorkflow) asked() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeFailer marks the fake row as PGStore.Fail marks a real one: only if
// it is still running. If finishedFirst is set, the run is found to have
// completed by the time it is marked, as when it finishes at that moment.
type fakeFailer struct {
	rows          *fakePerformances
	finishedFirst bool
	err           error

	mu      sync.Mutex
	reasons []string
}

func (f *fakeFailer) Fail(_ context.Context, _, reason string, _ time.Time) error {
	if f.err != nil {
		return f.err
	}
	f.mu.Lock()
	f.reasons = append(f.reasons, reason)
	f.mu.Unlock()

	f.rows.mu.Lock()
	defer f.rows.mu.Unlock()
	if f.finishedFirst {
		f.rows.row.Status = performance.StatusCompleted
	}
	if f.rows.row.Status == performance.StatusRunning {
		f.rows.row.Status = performance.StatusFailed
		f.rows.row.FailureReason = reason
	}
	return nil
}

func (f *fakeFailer) marked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reasons...)
}

func runningRow() *fakePerformances {
	return &fakePerformances{row: &performance.Performance{ID: "perf-1", RoomID: "room", Status: performance.StatusRunning}}
}

var votingAtTwo = performance.Snapshot{Revision: 2, State: performance.State{Phase: performance.PhaseVoting, Turn: 1, PerformanceID: "perf-1"}}

func resync(t *testing.T, ctx context.Context, ws *websocket.Conn) {
	t.Helper()
	require.NoError(t, ws.Write(ctx, websocket.MessageText, []byte(`{"type":"resync"}`)))
}

func TestServeWSSaysRecoveringWhenTheWorkflowCannotBeAsked(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for name, err := range map[string]error{
		"no worker is up to answer":         context.DeadlineExceeded,
		"the workflow was never started":    serviceerror.NewNotFound("workflow not found"),
		"Temporal itself cannot be reached": errors.New("connection refused"),
	} {
		svc, _, url := served(t, 0, 0)
		rows := runningRow()
		failer := &fakeFailer{rows: rows}
		svc.Performances, svc.Failures = rows, failer
		svc.Workflows = &fakeWorkflow{err: err}

		ws := dial(t, ctx, url)
		require.Equal(t, performance.Message{
			Type: "snapshot", Revision: 0,
			State: performance.State{Phase: performance.PhaseRecovering, PerformanceID: "perf-1"},
		}, readMessage(t, ctx, ws), name)
		require.Empty(t, failer.marked(), "%s: a run that may yet continue is not marked failed", name)

		// The browser is kept, and in the room: when updates resume it is
		// sent them like everyone else.
		require.Equal(t, 1, svc.Hub.Count("room"), name)
		svc.Hub.Broadcast("room", 3, updateJSON(3))
		require.Equal(t, 3, readMessage(t, ctx, ws).Revision, name)
	}
}

func TestServeWSMarksADeadRunFailedAndTellsTheRoom(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc, _, url := served(t, 0, 0)
	rows := runningRow()
	failer := &fakeFailer{rows: rows}
	svc.Performances, svc.Failures = rows, failer
	// The first browser connects while the run is alive. By the time the
	// second connects, the workflow has ended without marking its row.
	svc.Workflows = &scriptedWorkflow{answer: func(call int) (performance.Snapshot, error) {
		if call == 1 {
			return votingAtTwo, nil
		}
		return performance.Snapshot{}, fmt.Errorf("%w (Terminated)", performance.ErrWorkflowClosed)
	}}

	waiting := dial(t, ctx, url)
	require.Equal(t, 2, readMessage(t, ctx, waiting).Revision)

	failed := performance.Message{
		Type: "snapshot", Revision: 0, State: performance.State{
			Phase: performance.PhaseFailed, PerformanceID: "perf-1",
			Failure: &performance.Failure{Code: "internal_error", Message: "Something went wrong and the performance could not continue."},
		},
	}
	late := dial(t, ctx, url)
	require.Equal(t, failed, readMessage(t, ctx, late), "the browser that found out")
	require.Equal(t, []string{performance.FailureInternal}, failer.marked())

	// The one already there is told, although revision 0 is below what it
	// holds. The one that found out is not told twice.
	require.Equal(t, failed, readMessage(t, ctx, waiting), "the browser that was waiting")
	svc.Hub.Broadcast("room", 9, updateJSON(9))
	next := readMessage(t, ctx, late)
	require.Equal(t, "update", next.Type)
	require.Equal(t, 9, next.Revision)
}

func TestARunThatFinishedAsItWasFoundClosedIsAnsweredAsItFinished(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc, _, url := served(t, 0, 0)
	rows := runningRow()
	svc.Performances, svc.Failures = rows, &fakeFailer{rows: rows, finishedFirst: true}
	svc.Workflows = &scriptedWorkflow{answer: func(call int) (performance.Snapshot, error) {
		if call == 1 {
			return votingAtTwo, nil
		}
		return performance.Snapshot{}, performance.ErrWorkflowClosed
	}}

	waiting := dial(t, ctx, url)
	require.Equal(t, 2, readMessage(t, ctx, waiting).Revision)

	// The row said running when it was read, and the workflow was closed
	// when it was asked: it completed in between.
	late := dial(t, ctx, url)
	snapshot := readMessage(t, ctx, late)
	require.Equal(t, performance.PhasePremiere, snapshot.State.Phase)
	require.Equal(t, performance.LastRevision, snapshot.Revision)

	// Nothing is announced: the room gets the premiere update in the
	// ordinary way.
	svc.Hub.Broadcast("room", performance.LastRevision, updateJSON(performance.LastRevision))
	next := readMessage(t, ctx, waiting)
	require.Equal(t, "update", next.Type)
	require.Equal(t, performance.LastRevision, next.Revision)
}

func TestServeWSClosesWhenThePerformanceCannotBeRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc, _, url := served(t, 0, 0)
	svc.Performances = &fakePerformances{err: errors.New("database is down")}

	ws := dial(t, ctx, url)
	_, _, err := ws.Read(ctx)
	require.Equal(t, websocket.StatusTryAgainLater, websocket.CloseStatus(err), "error: %v", err)
	require.Eventually(t, func() bool { return svc.Hub.Count("room") == 0 }, 2*time.Second, 10*time.Millisecond)
}

func TestResyncSendsAFreshSnapshotAndDropsWhatItCovers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc, _, url := served(t, 0, 0)
	svc.Performances = runningRow()
	fresh := performance.Snapshot{Revision: 4, State: performance.State{
		Phase: performance.PhaseGenerating, Turn: 2, PerformanceID: "perf-1", YourVote: "c2",
	}}
	workflow := &scriptedWorkflow{}
	workflow.answer = func(call int) (performance.Snapshot, error) {
		if call == 1 {
			return votingAtTwo, nil
		}
		// While the workflow is being asked again, updates 3, 4, and 5
		// arrive and are queued for the connection.
		for revision := 3; revision <= 5; revision++ {
			svc.Hub.Broadcast("room", revision, updateJSON(revision))
		}
		return fresh, nil
	}
	svc.Workflows = workflow

	ws := dial(t, ctx, url)
	require.Equal(t, 2, readMessage(t, ctx, ws).Revision)

	resync(t, ctx, ws)
	require.Equal(t, performance.Message{Type: "snapshot", Revision: 4, State: fresh.State}, readMessage(t, ctx, ws),
		"a snapshot, taken as the first one was, with this browser's own vote")
	// 3 and 4 are covered by it; only 5 follows.
	next := readMessage(t, ctx, ws)
	require.Equal(t, "update", next.Type)
	require.Equal(t, 5, next.Revision)
	require.Equal(t, 2, workflow.asked())
}

func TestResyncIsPassedOnOnceASecond(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc, _, url := served(t, 0, 0)
	var mu sync.Mutex
	now := testNow
	svc.Now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	svc.Performances = runningRow()
	workflow := &scriptedWorkflow{answer: func(int) (performance.Snapshot, error) { return votingAtTwo, nil }}
	svc.Workflows = workflow

	ws := dial(t, ctx, url)
	readMessage(t, ctx, ws)

	// Three at once: one is answered. The vote after them is handled after
	// them, so once its reply and one snapshot are in, all three have been.
	resync(t, ctx, ws)
	resync(t, ctx, ws)
	resync(t, ctx, ws)
	require.NoError(t, ws.Write(ctx, websocket.MessageText, []byte(`{"type":"vote","turn":1,"choiceId":"c1"}`)))
	got := map[string]int{}
	for range 2 {
		got[readMessage(t, ctx, ws).Type]++
	}
	require.Equal(t, map[string]int{"snapshot": 1, "vote_received": 1}, got)
	require.Equal(t, 2, workflow.asked(), "the first snapshot and one resync")

	// A second later the next one is answered.
	mu.Lock()
	now = now.Add(resyncInterval)
	mu.Unlock()
	resync(t, ctx, ws)
	require.Equal(t, "snapshot", readMessage(t, ctx, ws).Type)
	require.Equal(t, 3, workflow.asked())
}

func TestResyncIsAnsweredFromTheRowBeforeStartAndAfterTheEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc, _, url := served(t, 0, 0) // no performance yet

	ws := dial(t, ctx, url)
	readMessage(t, ctx, ws)
	resync(t, ctx, ws)
	require.Equal(t, performance.Message{
		Type: "snapshot", Revision: 0, State: performance.State{Phase: performance.PhaseWaiting},
	}, readMessage(t, ctx, ws))
	require.Equal(t, 1, svc.Hub.Count("room"))
}
