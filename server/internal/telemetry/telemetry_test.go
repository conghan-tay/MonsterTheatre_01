package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
)

// captured is a logger writing JSON lines into a buffer, and a way to read
// them back.
func captured() (*slog.Logger, func() []map[string]any) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return logger, func() []map[string]any {
		var lines []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			if line == "" {
				continue
			}
			var entry map[string]any
			if err := json.Unmarshal([]byte(line), &entry); err == nil {
				lines = append(lines, entry)
			}
		}
		return lines
	}
}

// only is the lines with the given message.
func only(lines []map[string]any, msg string) []map[string]any {
	var out []map[string]any
	for _, l := range lines {
		if l["msg"] == msg {
			out = append(out, l)
		}
	}
	return out
}

type roomInput struct{ Room, Perf string }

func (in roomInput) LogFields() []any {
	return []any{"room_id", in.Room, "performance_id", in.Perf, "turn", 2}
}

func TestTheInterceptorLogsEachAttemptWithItsIDsAndDuration(t *testing.T) {
	logger, lines := captured()
	var s testsuite.WorkflowTestSuite
	env := s.NewTestActivityEnvironment()
	env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{NewInterceptor(logger)}})

	var inside []any
	work := func(ctx context.Context, in roomInput) (string, error) {
		inside = Fields(ctx)
		return "done", nil
	}
	env.RegisterActivity(work)
	_, err := env.ExecuteActivity(work, roomInput{Room: "room-1", Perf: "perf-1"})
	require.NoError(t, err)

	started := only(lines(), "activity started")
	require.Len(t, started, 1)
	require.Equal(t, "room-1", started[0]["room_id"])
	require.Equal(t, "perf-1", started[0]["performance_id"])
	require.EqualValues(t, 2, started[0]["turn"])
	require.Contains(t, started[0], "workflow_id")
	require.Contains(t, started[0], "activity_type")
	require.Contains(t, started[0], "activity_id")
	require.EqualValues(t, 1, started[0]["attempt"])
	require.Contains(t, started[0], "schedule_to_start_ms", "how long it waited in the queue")

	finished := only(lines(), "activity finished")
	require.Len(t, finished, 1)
	require.Equal(t, "ok", finished[0]["outcome"])
	require.Contains(t, finished[0], "duration_ms")
	require.Equal(t, false, finished[0]["will_retry"])
	require.Contains(t, inside, "room-1", "the activity's context carries the same fields, for the lines logged inside it")
}

func TestTheInterceptorLogsAFailedAttempt(t *testing.T) {
	logger, lines := captured()
	var s testsuite.WorkflowTestSuite
	env := s.NewTestActivityEnvironment()
	env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{NewInterceptor(logger)}})
	work := func(ctx context.Context, in roomInput) error { return errors.New("disk full") }
	env.RegisterActivity(work)
	_, err := env.ExecuteActivity(work, roomInput{Room: "room-1", Perf: "perf-1"})
	require.Error(t, err)

	finished := only(lines(), "activity finished")
	require.Len(t, finished, 1)
	require.Equal(t, "failed", finished[0]["outcome"])
	require.Contains(t, finished[0]["error"], "disk full")
}

func TestThePerformanceIDComesFromTheWorkflowIDWhenTheInputHasNone(t *testing.T) {
	require.Equal(t, "abc", performanceFromWorkflow("performance/abc"))
	require.Equal(t, "abc", performanceFromWorkflow("performance/abc/line-3"))
	require.Equal(t, "", performanceFromWorkflow("something-else"))
}

func TestWillRetryFollowsTheRetryPolicy(t *testing.T) {
	three := &temporal.RetryPolicy{MaximumAttempts: 3}
	require.False(t, willRetry(nil, 1, three, nil), "nothing failed")
	require.True(t, willRetry(errors.New("x"), 1, three, nil))
	require.True(t, willRetry(errors.New("x"), 2, three, nil))
	require.False(t, willRetry(errors.New("x"), 3, three, nil), "the last attempt")
	require.True(t, willRetry(errors.New("x"), 9, nil, nil), "no policy: Temporal retries without limit")
	require.True(t, willRetry(errors.New("x"), 9, &temporal.RetryPolicy{}, nil), "0 attempts means no limit")
	require.False(t, willRetry(temporal.NewNonRetryableApplicationError("no", "Bad", nil), 1, three, nil))
	require.False(t, willRetry(temporal.NewApplicationError("no", "Bad"), 1, &temporal.RetryPolicy{NonRetryableErrorTypes: []string{"Bad"}}, nil))
	require.False(t, willRetry(context.Canceled, 1, three, context.Canceled), "a cancelled attempt is not retried")
}

func TestOutcomeNamesWhatHappened(t *testing.T) {
	require.Equal(t, "ok", outcome(nil, nil))
	require.Equal(t, "failed", outcome(errors.New("x"), nil))
	require.Equal(t, "timeout", outcome(errors.New("x"), context.DeadlineExceeded))
	require.Equal(t, "canceled", outcome(errors.New("x"), context.Canceled))
}

func TestTimedLogsOneLineWithTheContextFields(t *testing.T) {
	logger, lines := captured()
	ctx := WithFields(context.Background(), []any{"room_id", "room-1", "attempt", 2})
	clock := []time.Time{time.Unix(100, 0), time.Unix(100, 0).Add(1500 * time.Millisecond)}
	now := func() time.Time { t := clock[0]; clock = clock[1:]; return t }

	err := timed(ctx, logger, now, "provider call", []any{"provider", "fake", "op", "speech"}, func() error { return errors.New("boom") })
	require.EqualError(t, err, "boom", "the error is passed on unchanged")
	got := only(lines(), "provider call")
	require.Len(t, got, 1)
	require.Equal(t, "room-1", got[0]["room_id"])
	require.Equal(t, "speech", got[0]["op"])
	require.EqualValues(t, 1500, got[0]["duration_ms"])
	require.Equal(t, "failed", got[0]["outcome"])
	require.Equal(t, "boom", got[0]["error"])
	require.Equal(t, "WARN", got[0]["level"], "a failure is a warning")
}

func TestQueueTimeIsReportedOnlyForAFirstAttempt(t *testing.T) {
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	first := queueFields(activity.Info{Attempt: 1, ScheduledTime: at, StartedTime: at.Add(200 * time.Millisecond)})
	require.Equal(t, []any{"schedule_to_start_ms", int64(200)}, first)

	// Temporal's ScheduledTime is when the activity was first scheduled, so
	// on a retry the gap holds the earlier attempts and the backoff.
	third := queueFields(activity.Info{Attempt: 3, ScheduledTime: at, StartedTime: at.Add(3 * time.Second)})
	require.NotContains(t, third, "schedule_to_start_ms", "a retry's wait is not queue time")
	require.Equal(t, []any{"since_first_scheduled_ms", int64(3000)}, third)
}

func TestALateReturnIsATimeout(t *testing.T) {
	three := &temporal.RetryPolicy{MaximumAttempts: 3}
	require.Equal(t, "timeout", outcome(nil, context.DeadlineExceeded), "Temporal discards a result returned after the deadline")
	require.True(t, willRetry(nil, 1, three, context.DeadlineExceeded))
	require.False(t, willRetry(nil, 3, three, context.DeadlineExceeded))

	logger, lines := captured()
	w := &workerInterceptor{logger: logger, now: time.Now}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := (&activityInbound{w: w}).observe(ctx, activity.Info{Attempt: 1, RetryPolicy: three}, nil,
		func(ctx context.Context) (any, error) {
			<-ctx.Done()
			return "too late", nil
		})
	require.NoError(t, err, "what the activity returned is passed on unchanged")

	finished := only(lines(), "activity finished")
	require.Len(t, finished, 1)
	require.Equal(t, "timeout", finished[0]["outcome"])
	require.Equal(t, true, finished[0]["will_retry"])
	require.Equal(t, "WARN", finished[0]["level"])
	require.Equal(t, "returned after its deadline", finished[0]["error"])
}

func TestAPanickedAttemptStillFinishesItsLine(t *testing.T) {
	logger, lines := captured()
	var s testsuite.WorkflowTestSuite
	env := s.NewTestActivityEnvironment()
	env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{NewInterceptor(logger)}})
	work := func(ctx context.Context, in roomInput) error { panic("nil map") }
	env.RegisterActivity(work)

	_, err := env.ExecuteActivity(work, roomInput{Room: "room-1", Perf: "perf-1"})
	var panicked *temporal.PanicError
	require.ErrorAs(t, err, &panicked, "Temporal still handles the panic itself")
	require.Contains(t, panicked.Error(), "nil map")

	finished := only(lines(), "activity finished")
	require.Len(t, finished, 1, "one start, one finish")
	require.Equal(t, "panic", finished[0]["outcome"])
	require.Equal(t, "room-1", finished[0]["room_id"])
	require.Equal(t, "WARN", finished[0]["level"])
	require.Contains(t, finished[0]["error"], "Activity panic.")
}
