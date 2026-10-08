package auth

import (
	"sync"
	"time"
)

// Throttle limits repeated failed logins per username.
//
// After MaxFailures failures inside Window, the username is locked until the
// window that began with the first failure has passed. A successful login
// clears the count.
//
// The state lives in memory, which fits the single-server design. A known
// cost: anyone who knows a director's username can lock that director out
// for the length of the window.
type Throttle struct {
	MaxFailures int
	Window      time.Duration
	Now         func() time.Time

	mu      sync.Mutex
	entries map[string]throttleEntry
}

type throttleEntry struct {
	failures int
	first    time.Time
}

// maxThrottleEntries bounds memory against a flood of made-up usernames.
const maxThrottleEntries = 10_000

// NewThrottle returns the limiter used in production: five failures per
// fifteen minutes.
func NewThrottle() *Throttle {
	return &Throttle{MaxFailures: 5, Window: 15 * time.Minute, Now: time.Now}
}

// RetryAfter returns how long the key is still locked, or zero if it is not.
func (t *Throttle) RetryAfter(key string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()

	entry, ok := t.live(key)
	if !ok || entry.failures < t.MaxFailures {
		return 0
	}
	return entry.first.Add(t.Window).Sub(t.Now())
}

// Fail records one failed attempt.
func (t *Throttle) Fail(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	entry, ok := t.live(key)
	if !ok {
		if len(t.entries) >= maxThrottleEntries {
			t.prune()
		}
		entry = throttleEntry{first: t.Now()}
	}
	entry.failures++
	if t.entries == nil {
		t.entries = make(map[string]throttleEntry)
	}
	t.entries[key] = entry
}

// Reset clears the key after a successful login.
func (t *Throttle) Reset(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, key)
}

// live returns the entry if its window has not passed, dropping it otherwise.
func (t *Throttle) live(key string) (throttleEntry, bool) {
	entry, ok := t.entries[key]
	if !ok {
		return entry, false
	}
	if !t.Now().Before(entry.first.Add(t.Window)) {
		delete(t.entries, key)
		return throttleEntry{}, false
	}
	return entry, true
}

func (t *Throttle) prune() {
	now := t.Now()
	for key, entry := range t.entries {
		if !now.Before(entry.first.Add(t.Window)) {
			delete(t.entries, key)
		}
	}
	// Still full of live entries: start over rather than grow without bound.
	if len(t.entries) >= maxThrottleEntries {
		clear(t.entries)
	}
}
