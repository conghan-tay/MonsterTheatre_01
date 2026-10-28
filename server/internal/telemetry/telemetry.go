// Package telemetry writes the worker's named log lines: one when each
// activity attempt starts and ends, and one for each provider call and
// upload, all carrying the same IDs (docs/decisions.md, D060). Every line is
// a JSON line from the process's logger; on AWS, metric filters count some
// of them.
package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/temporal"
)

// LogFielder is an activity input that knows its room, performance, and
// turn, as key and value pairs for a log line.
type LogFielder interface {
	LogFields() []any
}

type fieldsKey struct{}

// WithFields returns a context carrying fields for the lines logged inside
// an activity.
func WithFields(ctx context.Context, fields []any) context.Context {
	return context.WithValue(ctx, fieldsKey{}, fields)
}

// Fields are the IDs of the activity attempt ctx belongs to, or nothing
// outside an activity.
func Fields(ctx context.Context) []any {
	fields, _ := ctx.Value(fieldsKey{}).([]any)
	return fields
}

// ActivityIDs are only the workflow and activity IDs of the attempt ctx
// belongs to, for a line that already carries the room, turn, and attempt.
func ActivityIDs(ctx context.Context) []any {
	fields := Fields(ctx)
	var out []any
	for i := 0; i+1 < len(fields); i += 2 {
		switch fields[i] {
		case "workflow_id", "run_id", "activity_id", "activity_type", "performance_id":
			out = append(out, fields[i], fields[i+1])
		}
	}
	return out
}

// NewInterceptor logs "activity started" and "activity finished" for every
// activity attempt the worker runs.
func NewInterceptor(logger *slog.Logger) interceptor.WorkerInterceptor {
	return &workerInterceptor{logger: logger, now: time.Now}
}

type workerInterceptor struct {
	interceptor.WorkerInterceptorBase
	logger *slog.Logger
	now    func() time.Time
}

func (w *workerInterceptor) InterceptActivity(ctx context.Context, next interceptor.ActivityInboundInterceptor) interceptor.ActivityInboundInterceptor {
	i := &activityInbound{w: w}
	i.Next = next
	return i
}

type activityInbound struct {
	interceptor.ActivityInboundInterceptorBase
	w *workerInterceptor
}

func (a *activityInbound) ExecuteActivity(ctx context.Context, in *interceptor.ExecuteActivityInput) (any, error) {
	return a.observe(ctx, activity.GetInfo(ctx), in.Args, func(ctx context.Context) (any, error) {
		return a.Next.ExecuteActivity(ctx, in)
	})
}

// observe logs one attempt's start and end around call.
func (a *activityInbound) observe(ctx context.Context, info activity.Info, args []any, call func(context.Context) (any, error)) (any, error) {
	fields := []any{
		"workflow_id", info.WorkflowExecution.ID,
		"run_id", info.WorkflowExecution.RunID,
		"activity_type", info.ActivityType.Name,
		"activity_id", info.ActivityID,
		"attempt", info.Attempt,
	}
	fromInput := false
	for _, arg := range args {
		if f, ok := arg.(LogFielder); ok {
			fields = append(fields, f.LogFields()...)
			fromInput = slices.Contains(f.LogFields(), "performance_id")
			break
		}
	}
	if !fromInput {
		if id := performanceFromWorkflow(info.WorkflowExecution.ID); id != "" {
			fields = append(fields, "performance_id", id)
		}
	}
	ctx = WithFields(ctx, fields)

	start := a.w.now()
	a.w.logger.Info("activity started", append(slices.Clone(fields), queueFields(info)...)...)

	// A panic is left to Temporal, which recovers it outside this
	// interceptor, logs "Activity panic." with its value and stack, and
	// fails the attempt. It is not recovered here, so that line and the
	// retry are exactly as they would be without the interceptor; the
	// attempt's own line is still written.
	returned := false
	defer func() {
		if returned {
			return
		}
		a.w.logger.Warn("activity finished", append(slices.Clone(fields),
			"duration_ms", a.w.now().Sub(start).Milliseconds(),
			"outcome", "panic",
			"will_retry", willRetry(errPanicked, info.Attempt, info.RetryPolicy, nil),
			"error", errPanicked.Error())...)
	}()

	result, err := call(ctx)
	returned = true

	ctxErr := ctx.Err()
	done := append(slices.Clone(fields),
		"duration_ms", a.w.now().Sub(start).Milliseconds(),
		"outcome", outcome(err, ctxErr),
		"will_retry", willRetry(err, info.Attempt, info.RetryPolicy, ctxErr),
	)
	switch {
	case err != nil:
		a.w.logger.Warn("activity finished", append(done, "error", err.Error())...)
	case errors.Is(ctxErr, context.DeadlineExceeded):
		// Temporal discards a result returned after the deadline and counts
		// the attempt as timed out.
		a.w.logger.Warn("activity finished", append(done, "error", "returned after its deadline")...)
	default:
		a.w.logger.Info("activity finished", done...)
	}
	return result, err
}

// errPanicked stands for a panic on the attempt's line.
var errPanicked = errors.New(`panicked; see the SDK's "Activity panic." line for the value and stack`)

// queueFields is how long the attempt waited to be picked up. Temporal's
// ScheduledTime is when the activity was first scheduled, not the current
// attempt, and the SDK does not give an interceptor the current attempt's.
// So only a first attempt's gap is queue time; a later attempt's also holds
// the earlier attempts and the retry backoff, and is named for what it is.
func queueFields(info activity.Info) []any {
	if info.ScheduledTime.IsZero() || info.StartedTime.IsZero() {
		return nil
	}
	gap := info.StartedTime.Sub(info.ScheduledTime).Milliseconds()
	if info.Attempt <= 1 {
		return []any{"schedule_to_start_ms", gap}
	}
	return []any{"since_first_scheduled_ms", gap}
}

// performanceFromWorkflow reads the performance ID out of a performance's
// workflow ID, "performance/<id>", or one of its lines',
// "performance/<id>/line-<n>".
func performanceFromWorkflow(workflowID string) string {
	rest, ok := strings.CutPrefix(workflowID, "performance/")
	if !ok {
		return ""
	}
	id, _, _ := strings.Cut(rest, "/")
	return id
}

// outcome names how an attempt ended. ctxErr is the attempt context's own
// error: a deadline there is the activity's timeout.
func outcome(err, ctxErr error) string {
	switch {
	case errors.Is(ctxErr, context.DeadlineExceeded):
		// Even with no error: a result returned after the deadline is
		// discarded, and the attempt is a timeout.
		return "timeout"
	case err == nil:
		return "ok"
	case errors.Is(ctxErr, context.Canceled):
		return "canceled"
	default:
		return "failed"
	}
}

// willRetry is the worker's view of whether Temporal will try a failed
// attempt again: the retry policy's attempt limit and its non-retryable
// error types. Temporal has the last word, and a ScheduleToClose limit can
// still end the retries sooner.
func willRetry(err error, attempt int32, policy *temporal.RetryPolicy, ctxErr error) bool {
	if err == nil && errors.Is(ctxErr, context.DeadlineExceeded) {
		err = ctxErr // a late result is a timeout, retried like one
	}
	if err == nil || errors.Is(ctxErr, context.Canceled) {
		return false
	}
	var app *temporal.ApplicationError
	if errors.As(err, &app) {
		if app.NonRetryable() {
			return false
		}
		if policy != nil && slices.Contains(policy.NonRetryableErrorTypes, app.Type()) {
			return false
		}
	}
	if policy == nil || policy.MaximumAttempts <= 0 {
		return true
	}
	return attempt < policy.MaximumAttempts
}

// timed runs call and logs one line named msg with the context's fields,
// the extra fields, its duration, and how it ended. A failure is logged as a
// warning; the error is returned unchanged.
func timed(ctx context.Context, logger *slog.Logger, now func() time.Time, msg string, extra []any, call func() error) error {
	start := now()
	err := call()
	fields := append(slices.Clone(Fields(ctx)), extra...)
	ended := outcome(err, ctx.Err())
	fields = append(fields, "duration_ms", now().Sub(start).Milliseconds(), "outcome", ended)
	switch {
	case err != nil:
		logger.Warn(msg, append(fields, "error", err.Error())...)
	case ended != "ok":
		logger.Warn(msg, fields...)
	default:
		logger.Info(msg, fields...)
	}
	return err
}
