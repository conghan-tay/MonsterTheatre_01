package theater

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/performance"
)

func TestWhenAPerformanceCountsAsStalled(t *testing.T) {
	at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	after, premiere := 90*time.Second, 16*time.Minute
	deadline := at.Add(20 * time.Second)

	cases := []struct {
		name     string
		phase    string
		deadline *time.Time
		elapsed  time.Duration
		want     bool
	}{
		{"generating, within the limit", performance.PhaseGenerating, nil, 89 * time.Second, false},
		{"generating, past it", performance.PhaseGenerating, nil, 91 * time.Second, true},
		{"preparing a line, past it", performance.PhasePreparingLine, nil, 91 * time.Second, true},
		{"voting, before the deadline and the limit", performance.PhaseVoting, &deadline, 100 * time.Second, false},
		{"voting, the limit past the deadline", performance.PhaseVoting, &deadline, 111 * time.Second, true},
		{"voting with no deadline counts from the update", performance.PhaseVoting, nil, 91 * time.Second, true},
		{"preparing the premiere, a slow video", performance.PhasePreparingPremiere, nil, 15 * time.Minute, false},
		{"preparing the premiere, past the lip-sync limit", performance.PhasePreparingPremiere, nil, 17 * time.Minute, true},
		{"the premiere never stalls", performance.PhasePremiere, nil, time.Hour, false},
		{"a failed run never stalls", performance.PhaseFailed, nil, time.Hour, false},
	}
	for _, c := range cases {
		got := overdue(c.phase, c.deadline, at, at.Add(c.elapsed), after, premiere)
		require.Equal(t, c.want, got, c.name)
	}
}

type fakeReaderHealth struct{ err error }

func (f *fakeReaderHealth) Healthy(time.Time) error { return f.err }

func stallLines(buf *bytes.Buffer, msg string) []map[string]any {
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil && m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

func TestAStallIsLoggedOnceThenItsEnd(t *testing.T) {
	var buf bytes.Buffer
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	reader := &fakeReaderHealth{}
	w := &StallWatcher{
		After: 2 * time.Second, Logger: slog.New(slog.NewJSONHandler(&buf, nil)),
		Now: func() time.Time { return now }, Reader: reader,
		Connections: func(string) int { return 3 },
	}
	w.Seen("room-1", 4, performance.State{Phase: performance.PhaseGenerating, Turn: 2, PerformanceID: "perf-1"})

	now = now.Add(time.Second)
	w.Check()
	require.Empty(t, stallLines(&buf, "performance stalled"), "not yet")

	now = now.Add(2 * time.Second)
	reader.err = errors.New("reads failing for 12s")
	w.Check()
	w.Check()
	stalled := stallLines(&buf, "performance stalled")
	require.Len(t, stalled, 1, "once per stall")
	require.Equal(t, "room-1", stalled[0]["room_id"])
	require.Equal(t, "perf-1", stalled[0]["performance_id"])
	require.Equal(t, performance.PhaseGenerating, stalled[0]["phase"])
	require.EqualValues(t, 4, stalled[0]["revision"])
	require.EqualValues(t, 2, stalled[0]["turn"])
	require.EqualValues(t, 3, stalled[0]["since_update_s"])
	require.Equal(t, false, stalled[0]["reader_ok"], "says whether this server could have seen an update")
	require.Equal(t, "reads failing for 12s", stalled[0]["reader_error"])
	require.EqualValues(t, 3, stalled[0]["connections"])

	w.Seen("room-1", 4, performance.State{Phase: performance.PhaseGenerating})
	require.Empty(t, stallLines(&buf, "performance resumed"), "a repeated revision is not progress")

	now = now.Add(4 * time.Second)
	w.Seen("room-1", 5, performance.State{Phase: performance.PhaseVoting})
	resumed := stallLines(&buf, "performance resumed")
	require.Len(t, resumed, 1)
	require.EqualValues(t, 4, resumed[0]["stalled_for_s"])
	require.EqualValues(t, 5, resumed[0]["revision"])
}

func TestAFinishedPerformanceIsForgotten(t *testing.T) {
	var buf bytes.Buffer
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	w := &StallWatcher{After: time.Second, Logger: slog.New(slog.NewJSONHandler(&buf, nil)), Now: func() time.Time { return now }}
	w.Seen("room-1", 24, performance.State{Phase: performance.PhasePreparingLine})
	w.Seen("room-1", 25, performance.State{Phase: performance.PhasePremiere})
	w.Seen("room-2", 3, performance.State{Phase: performance.PhaseGenerating})
	w.Seen("room-2", 4, performance.State{Phase: performance.PhaseFailed})
	now = now.Add(time.Hour)
	w.Check()
	require.Empty(t, stallLines(&buf, "performance stalled"))
	require.Zero(t, w.watching())
}

func TestARoomStalledForAnHourIsForgottenAfterItsLine(t *testing.T) {
	// A run that ended without its last update reaching the stream would
	// otherwise be watched forever.
	var buf bytes.Buffer
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	w := &StallWatcher{After: time.Second, Logger: slog.New(slog.NewJSONHandler(&buf, nil)), Now: func() time.Time { return now }}
	w.Seen("room-1", 3, performance.State{Phase: performance.PhaseGenerating})
	now = now.Add(2 * time.Second)
	w.Check()
	require.Len(t, stallLines(&buf, "performance stalled"), 1)
	now = now.Add(61 * time.Minute)
	w.Check()
	require.Zero(t, w.watching())
}
