package theater

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/conghan-tay/MonsterTheatre_01/server/internal/performance"
)

const (
	// DefaultStallAfter is how long a running performance may go without
	// an update, past any vote deadline, before it is reported stalled.
	DefaultStallAfter = 90 * time.Second
	// premiereStallAfter is the same for the wait before a lip-sync
	// premiere: one video may take its full 15 minutes, and progress is
	// only published as each one is ready.
	premiereStallAfter = 16 * time.Minute
	// stallCheckEvery is the longest the watcher waits between looks.
	stallCheckEvery = 5 * time.Second
	// stallForgetAfter is how long a stalled room is kept: a run whose last
	// update never reached the stream would otherwise be watched forever.
	stallForgetAfter = time.Hour
)

// ReaderHealth reports whether the stream reader is getting updates
// through right now. *StreamReader implements it.
type ReaderHealth interface {
	Reading(now time.Time) error
}

// StallWatcher notices a running performance whose updates have stopped
// arriving at this server, and logs "performance stalled" once, then
// "performance resumed" when the next one arrives (docs/decisions.md, D061).
// Each line says whether this server's reader was working, which tells a
// slow or stuck run (reader_ok true) from updates not reaching this server
// (reader_ok false).
//
// It knows only the rooms whose updates this server has read since it
// started.
type StallWatcher struct {
	// After is how long a phase may go without an update; zero means
	// DefaultStallAfter.
	After  time.Duration
	Logger *slog.Logger
	Now    func() time.Time
	// Reader, if set, is asked whether it is working when a stall is seen.
	Reader ReaderHealth
	// Connections, if set, counts a room's connections on this server.
	Connections func(roomID string) int

	mu    sync.Mutex
	rooms map[string]*watched
}

type watched struct {
	revision      int
	phase         string
	turn          int
	performanceID string
	deadline      *time.Time
	at            time.Time
	stalledAt     time.Time
}

// Seen records an update this server has read.
func (w *StallWatcher) Seen(roomID string, revision int, state performance.State) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.Now()
	last := w.rooms[roomID]
	if last != nil && revision <= last.revision {
		return // a repeat, from a retried publish
	}
	if last != nil && !last.stalledAt.IsZero() {
		w.Logger.Info("performance resumed",
			"room_id", roomID, "performance_id", state.PerformanceID, "revision", revision, "phase", state.Phase,
			"stalled_for_s", int(now.Sub(last.stalledAt).Seconds()))
	}
	if state.Phase == performance.PhasePremiere || state.Phase == performance.PhaseFailed {
		delete(w.rooms, roomID)
		return
	}
	if w.rooms == nil {
		w.rooms = make(map[string]*watched)
	}
	w.rooms[roomID] = &watched{
		revision: revision, phase: state.Phase, turn: state.Turn, performanceID: state.PerformanceID,
		deadline: state.Deadline, at: now,
	}
}

// Check logs every performance that has newly stalled.
func (w *StallWatcher) Check() {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.Now()
	after := w.After
	if after <= 0 {
		after = DefaultStallAfter
	}
	for roomID, r := range w.rooms {
		if !r.stalledAt.IsZero() {
			if now.Sub(r.stalledAt) > stallForgetAfter {
				delete(w.rooms, roomID)
			}
			continue
		}
		if !overdue(r.phase, r.deadline, r.at, now, after, premiereStallAfter) {
			continue
		}
		r.stalledAt = now
		fields := []any{
			"room_id", roomID, "performance_id", r.performanceID, "phase", r.phase, "turn", r.turn,
			"revision", r.revision, "since_update_s", int(now.Sub(r.at).Seconds()),
		}
		if w.Reader != nil {
			err := w.Reader.Reading(now)
			fields = append(fields, "reader_ok", err == nil)
			if err != nil {
				fields = append(fields, "reader_error", err.Error())
			}
		}
		if w.Connections != nil {
			fields = append(fields, "connections", w.Connections(roomID))
		}
		w.Logger.Warn("performance stalled", fields...)
	}
}

// every is how often the watcher looks: twice per limit, so a stall is
// reported within half a limit of its starting, but at most every
// stallCheckEvery and at least every tenth of a second.
func (w *StallWatcher) every() time.Duration {
	after := w.After
	if after <= 0 {
		after = DefaultStallAfter
	}
	return min(stallCheckEvery, max(after/2, 100*time.Millisecond))
}

// Run checks until ctx ends.
func (w *StallWatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(w.every())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.Check()
		}
	}
}

// watching is how many rooms are being watched.
func (w *StallWatcher) watching() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.rooms)
}

// overdue reports whether a performance last updated at lastUpdate, in the
// given phase, should have been updated again by now. A vote round is
// expected to last until its deadline; the wait for a lip-sync premiere,
// until the slowest video could be done; anything else, about as long as
// a model's answer.
func overdue(phase string, deadline *time.Time, lastUpdate, now time.Time, after, premiere time.Duration) bool {
	switch phase {
	case performance.PhasePremiere, performance.PhaseFailed:
		return false
	case performance.PhasePreparingPremiere:
		return now.Sub(lastUpdate) > premiere
	case performance.PhaseVoting:
		if deadline != nil && deadline.After(lastUpdate) {
			return now.Sub(*deadline) > after
		}
	}
	return now.Sub(lastUpdate) > after
}
