package theater

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// startReader runs a reader with a captured logger until the test ends.
func startReader(t *testing.T, reader *StreamReader) {
	t.Helper()
	if reader.RetryDelay == 0 {
		reader.RetryDelay = time.Millisecond
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { reader.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func TestEachUpdateBroadcastIsLoggedWithItsStreamIDAndReach(t *testing.T) {
	var buf lockedBuffer
	stream := &fakeStream{}
	hub := &recordingHub{connections: 3, dropped: 1}
	watcher := &StallWatcher{Logger: slog.New(slog.DiscardHandler), Now: time.Now}
	startReader(t, &StreamReader{Redis: stream, Hub: hub, Logger: slog.New(slog.NewJSONHandler(&buf, nil)), Stalls: watcher})
	waitUntilReading(t, stream)

	stream.add("room-1", 5, `{"type":"update","revision":5,"state":{"phase":"voting","turn":2,"performanceId":"perf-1"}}`)
	require.Eventually(t, func() bool { return len(buf.lines("update broadcast")) == 1 }, time.Second, time.Millisecond)

	line := buf.lines("update broadcast")[0]
	require.Equal(t, "room-1", line["room_id"])
	require.EqualValues(t, 5, line["revision"])
	require.Equal(t, "1-0", line["stream_id"], "matches the worker's update published line")
	require.Equal(t, "voting", line["phase"])
	require.Equal(t, "perf-1", line["performance_id"])
	require.EqualValues(t, 3, line["connections"])
	require.EqualValues(t, 1, line["dropped"])
	require.Equal(t, 1, watcher.watching(), "the stall watcher is told")
}

func TestAFailingReaderLogsOnceThenItsRecovery(t *testing.T) {
	var buf lockedBuffer
	stream := &fakeStream{failures: 4}
	startReader(t, &StreamReader{Redis: stream, Hub: &recordingHub{}, Logger: slog.New(slog.NewJSONHandler(&buf, nil))})
	waitUntilReading(t, stream)

	require.Eventually(t, func() bool { return len(buf.lines("stream reader recovered")) == 1 }, time.Second, time.Millisecond)
	failed := buf.lines("stream read failed")
	require.Len(t, failed, 1, "not once per retry")
	require.EqualValues(t, 1, failed[0]["failures"])
	require.Contains(t, failed[0]["error"], "connection")
	require.Contains(t, failed[0], "failing_for_s")
	recovered := buf.lines("stream reader recovered")[0]
	require.EqualValues(t, 4, recovered["failures"])
	require.Contains(t, recovered, "downtime_s")
}

func TestTheReaderIsHealthyOnlyWhileItKeepsReading(t *testing.T) {
	start := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	r := &StreamReader{Logger: slog.New(slog.DiscardHandler)}
	require.ErrorContains(t, r.Healthy(start), "not running", "before Run")

	r.started(start)
	require.NoError(t, r.Healthy(start.Add(10*time.Second)), "the first read is still waiting")
	require.ErrorContains(t, r.Healthy(start.Add(16*time.Second)), "no read has finished", "a stuck or dead reader")

	r.readOK(start.Add(20 * time.Second))
	require.NoError(t, r.Healthy(start.Add(21*time.Second)))

	r.readFailed(start.Add(22*time.Second), "read the streams", errors.New("connection refused"))
	require.NoError(t, r.Healthy(start.Add(30*time.Second)), "a short outage is retried quietly")
	r.readFailed(start.Add(31*time.Second), "read the streams", errors.New("connection refused"))
	require.ErrorContains(t, r.Healthy(start.Add(33*time.Second)), "failing for 11s")

	r.readOK(start.Add(34 * time.Second))
	require.NoError(t, r.Healthy(start.Add(35*time.Second)))
}

func TestAnUpdateThatIsNotJSONIsStillForwarded(t *testing.T) {
	// The message is the browser's business; the reader only reads its
	// phase for the stall watcher, if it can.
	stream := &fakeStream{}
	hub := &recordingHub{}
	startReader(t, &StreamReader{Redis: stream, Hub: hub, Logger: slog.New(slog.DiscardHandler),
		Stalls: &StallWatcher{Logger: slog.New(slog.DiscardHandler), Now: time.Now}})
	waitUntilReading(t, stream)
	stream.add("room", 1, "not json")
	require.Eventually(t, func() bool { return len(hub.all()) == 1 }, time.Second, time.Millisecond)
}
