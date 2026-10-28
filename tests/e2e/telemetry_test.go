package e2e_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/tests/e2e/client"
	"github.com/conghan-tay/MonsterTheatre_01/tests/e2e/fixtures"
	"github.com/conghan-tay/MonsterTheatre_01/tests/e2e/harness"
)

// These tests break one part of the system at a time and check that the
// logs say where, and whether the run carried on while browsers stopped
// hearing about it (docs/decisions.md, D060 and D061). Each reads the JSON
// lines the stack's processes wrote.

// logged waits until a process has logged at least n lines with the
// message for which keep is true, and returns them.
func logged(t *testing.T, process, msg string, n int, keep func(harness.LogLine) bool) []harness.LogLine {
	t.Helper()
	var lines []harness.LogLine
	require.Eventually(t, func() bool {
		var err error
		lines, err = stack.LogLines(process, msg, keep)
		require.NoError(t, err)
		return len(lines) >= n
	}, 30*time.Second, 100*time.Millisecond, "%s: %d %q line(s)", process, n, msg)
	return lines
}

func inRoom(roomID string, also ...func(harness.LogLine) bool) func(harness.LogLine) bool {
	return func(l harness.LogLine) bool {
		if l.String("room_id") != roomID {
			return false
		}
		for _, f := range also {
			if !f(l) {
				return false
			}
		}
		return true
	}
}

func field(key string, want any) func(harness.LogLine) bool {
	return func(l harness.LogLine) bool {
		switch w := want.(type) {
		case string:
			return l.String(key) == w
		case int:
			return l.Number(key) == float64(w)
		case bool:
			v, ok := l[key].(bool)
			return ok && v == w
		}
		return false
	}
}

// A slow model: the run is late but carries on. The worker's lines show the
// slow call and how long it took; the server's say the performance stalled
// while its reader was working, then resumed.
func TestTelemetryShowsASlowProviderAndThatTheRunCarriedOn(t *testing.T) {
	requireLocal(t, "the test reads the stack's own log files")
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	_, room, conn, _ := scriptedRoom(t, ctx, fixtures.Script{
		Delays: []fixtures.Delay{{Op: fixtures.OpChoices, Turn: 2, Ms: 3000}},
	})
	readUpdates(t, ctx, conn, 1, lastRevision) // the whole run, as usual

	call := logged(t, "worker", "provider call", 1, inRoom(room.RoomID, field("op", "choices"), field("turn", 2)))[0]
	require.Equal(t, "fake", call.String("provider"))
	require.GreaterOrEqual(t, call.Number("duration_ms"), 3000.0, "the slow call, and how slow")
	require.Equal(t, "ok", call.String("outcome"))
	require.NotEmpty(t, call.String("workflow_id"))
	require.NotEmpty(t, call.String("activity_id"))

	attempt := logged(t, "worker", "activity finished", 1,
		inRoom(room.RoomID, field("activity_type", "GenerateChoices"), field("turn", 2)))[0]
	require.GreaterOrEqual(t, attempt.Number("duration_ms"), 3000.0)
	require.EqualValues(t, 1, attempt.Number("attempt"), "slow, not retried")

	stalled := logged(t, "server", "performance stalled", 1, inRoom(room.RoomID))[0]
	require.Equal(t, client.PhaseGenerating, stalled.String("phase"))
	require.EqualValues(t, 2, stalled.Number("turn"))
	require.Equal(t, true, stalled["reader_ok"], "this server was reading: the delay was upstream")
	logged(t, "server", "performance resumed", 1, inRoom(room.RoomID))
}

// A failed upload: the recording of turn 2 cannot be stored. The worker's
// lines name the key, each attempt, and that the retries ran out; the run
// stops there, and browsers are told.
func TestTelemetryShowsAFailedUploadAndWhereTheRunStopped(t *testing.T) {
	requireLocal(t, "the test blocks a file in the stack's own object store")
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	_, room, conn, started := scriptedRoom(t, ctx, fixtures.Script{})
	key := audioKey(started.PerformanceID, 2)
	require.NoError(t, stack.BlockObject(key))

	var failed client.Message
	for {
		msg, err := conn.NextUpdate(ctx)
		require.NoError(t, err)
		if msg.State.Phase == client.PhaseFailed {
			failed = msg
			break
		}
	}
	require.Equal(t, "speech_failed", failed.State.Failure.Code)

	puts := logged(t, "worker", "object put", 3, inRoom(room.RoomID, field("key", key)))
	for _, put := range puts {
		require.Equal(t, "failed", put.String("outcome"))
		require.NotEmpty(t, put.String("error"))
		require.Equal(t, started.PerformanceID, put.String("performance_id"))
	}
	attempts := logged(t, "worker", "activity finished", 3,
		inRoom(room.RoomID, field("activity_type", "Synthesize"), field("turn", 2)))
	require.Len(t, attempts, 3, "the retry policy's three attempts")
	for i, a := range attempts {
		require.Equal(t, "failed", a.String("outcome"))
		require.EqualValues(t, i+1, a.Number("attempt"))
		require.Equal(t, i < 2, a["will_retry"], "attempt %d", i+1)
	}
	// Only a first attempt's wait is queue time; a retry's also holds the
	// earlier attempts and the backoff, and is named for that.
	starts := logged(t, "worker", "activity started", 3,
		inRoom(room.RoomID, field("activity_type", "Synthesize"), field("turn", 2)))
	require.Contains(t, starts[0], "schedule_to_start_ms")
	for _, retry := range starts[1:] {
		require.NotContains(t, retry, "schedule_to_start_ms", "attempt %v", retry["attempt"])
		require.GreaterOrEqual(t, retry.Number("since_first_scheduled_ms"), 1000.0, "it includes the backoff")
	}
	stopped := logged(t, "worker", "a line could not be voiced; stopping the run", 1, inRoom(room.RoomID))[0]
	require.Equal(t, started.PerformanceID, stopped.String("performance_id"))

	// The failure itself was published and delivered.
	logged(t, "worker", "update published", 1, inRoom(room.RoomID, field("phase", client.PhaseFailed)))
	logged(t, "server", "update broadcast", 1, inRoom(room.RoomID, field("phase", client.PhaseFailed), field("revision", failed.Revision)))
}

// received collects every update a connection is sent, in the background,
// so a test can look at what arrived without a read timing out and closing
// the connection.
type received struct {
	mu        sync.Mutex
	revisions []int
}

func collect(ctx context.Context, conn *client.Conn) *received {
	r := &received{}
	go func() {
		for {
			msg, err := conn.NextUpdate(ctx)
			if err != nil {
				return
			}
			r.mu.Lock()
			r.revisions = append(r.revisions, msg.Revision)
			r.mu.Unlock()
		}
	}()
	return r
}

func (r *received) last() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.revisions) == 0 {
		return 0
	}
	return r.revisions[len(r.revisions)-1]
}

func (r *received) all() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.revisions...)
}

func healthzStatus(t *testing.T, ctx context.Context, base string) (int, map[string]string) {
	t.Helper()
	resp, err := client.New(base).Get(ctx, "/healthz")
	require.NoError(t, err)
	var body struct {
		Checks map[string]string `json:"checks"`
	}
	_ = resp.Decode(&body)
	return resp.Status, body.Checks
}

// The second server loses Redis while a performance runs. The worker goes
// on publishing and the first server's browser goes on hearing; the second
// server's browser stops. Its lines say its reader is failing and the
// performance stalled with reader_ok false, and its /healthz says so. When
// Redis comes back it reads on from where it stopped.
//
// Not parallel: it cuts the second server, which other tests use.
func TestTelemetryShowsDeliveryStoppingOnOneServerWhileTheRunGoesOn(t *testing.T) {
	requireLocal(t, "the test cuts one server's connection to Redis")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	director, room, _ := newRoom(t, ctx)
	member := enteredMember(t, ctx, room.ShareLink)
	onFirst, err := director.ConnectRoom(ctx, room.RoomID)
	require.NoError(t, err)
	defer onFirst.Close()
	requireWaitingSnapshot(t, ctx, onFirst)
	onSecond, err := member.On(stack.SecondBaseURL).ConnectRoom(ctx, room.RoomID)
	require.NoError(t, err)
	defer onSecond.Close()
	requireWaitingSnapshot(t, ctx, onSecond)

	status, _ := start(t, ctx, director.Client, room.RoomID)
	require.Equal(t, http.StatusCreated, status)
	_, err = onFirst.WaitForPhase(ctx, client.PhaseVoting, 1)
	require.NoError(t, err)
	_, err = onSecond.WaitForPhase(ctx, client.PhaseVoting, 1)
	require.NoError(t, err)
	second := collect(ctx, onSecond)

	require.NoError(t, stack.CutSecondServerRedis())
	restored := false
	defer func() {
		if !restored {
			_ = stack.RestoreSecondServerRedis()
		}
	}()

	// The run goes on: the first server's browser reaches turn 3.
	_, err = onFirst.WaitForPhase(ctx, client.PhaseVoting, 3)
	require.NoError(t, err)
	logged(t, "worker", "update published", 1, inRoom(room.RoomID, field("revision", 8)))
	logged(t, "server", "update broadcast", 1, inRoom(room.RoomID, field("revision", 8)))
	stoppedAt := second.last()
	require.Less(t, stoppedAt, 8, "the second server's browser stopped hearing; got %v", second.all())

	// The second server says why.
	failing := logged(t, "server-2", "stream read failed", 1, nil)[0]
	require.NotEmpty(t, failing.String("error"))
	stalled := logged(t, "server-2", "performance stalled", 1, inRoom(room.RoomID))[0]
	require.Equal(t, false, stalled["reader_ok"], "this server could not have seen the next update")
	require.NotEmpty(t, stalled.String("reader_error"))
	require.EqualValues(t, 1, stalled.Number("connections"), "and one browser here was waiting")
	require.Empty(t, mustLines(t, "server", "performance stalled", inRoom(room.RoomID)),
		"the first server, still reading, saw no stall")
	require.Eventually(t, func() bool {
		code, checks := healthzStatus(t, ctx, stack.SecondBaseURL)
		return code == http.StatusServiceUnavailable && checks["stream"] != "ok"
	}, 30*time.Second, 500*time.Millisecond, "the second server's /healthz names the stream")
	code, checks := healthzStatus(t, ctx, stack.BaseURL)
	require.Equal(t, http.StatusOK, code, "the first server is healthy: %v", checks)

	// Redis comes back: the reader says so, and the browser catches up on
	// what it missed, from where the reader stopped.
	require.NoError(t, stack.RestoreSecondServerRedis())
	restored = true
	logged(t, "server-2", "stream reader recovered", 1, nil)
	require.Eventually(t, func() bool { return second.last() == lastRevision }, time.Minute, 100*time.Millisecond,
		"the second server's browser reached the premiere; got %v", second.all())
	got := second.all()
	for i := 1; i < len(got); i++ {
		require.Equal(t, got[i-1]+1, got[i], "nothing was skipped after the outage: %v", got)
	}
	_, err = onFirst.WaitForPhase(ctx, client.PhasePremiere, turns)
	require.NoError(t, err)
}

func mustLines(t *testing.T, process, msg string, keep func(harness.LogLine) bool) []harness.LogLine {
	t.Helper()
	lines, err := stack.LogLines(process, msg, keep)
	require.NoError(t, err)
	return lines
}
