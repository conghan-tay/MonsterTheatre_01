package anthropic

import (
	"sync"
	"time"
)

// rates are one model's prices, in US dollars per million tokens.
type rates struct {
	input, cacheWrite, cacheRead, output float64
}

// prices are Anthropic's published rates for the models this adapter is
// expected to run, as listed in the Claude API reference on 2026-09-25. A
// cache write is the 5-minute kind, 1.25 times the input price; a cache
// read is $0.20 on all three newer models listed, 0.1 times input on Sonnet
// 5.5. A model not listed is logged as unpriced, never guessed
// (docs/decisions.md, D057).
var prices = map[string]rates{
	"claude-sonnet-5-5": {input: 2, cacheWrite: 2.5, cacheRead: 0.2, output: 10},
	"claude-opus-5-5":   {input: 4, cacheWrite: 5, cacheRead: 0.2, output: 20},
	"claude-haiku-4-5":  {input: 1, cacheWrite: 1.25, cacheRead: 0.1, output: 5},
}

// price works out one call's cost in US dollars, and what the same tokens
// would have cost with no caching: every input token at the input price.
// ok is false for a model with no known price.
func price(model string, input, cacheWrite, cacheRead, output int64) (cost, withoutCache float64, ok bool) {
	r, ok := prices[model]
	if !ok {
		return 0, 0, false
	}
	const million = 1_000_000
	out := float64(output) * r.output / million
	cost = (float64(input)*r.input+float64(cacheWrite)*r.cacheWrite+float64(cacheRead)*r.cacheRead)/million + out
	withoutCache = float64(input+cacheWrite+cacheRead)*r.input/million + out
	return cost, withoutCache, true
}

// roomQuiet is how long a room's running total is kept after its last call.
const roomQuiet = time.Hour

// roomTotals keeps a running total of each room's Claude calls, so the log
// line of a performance's last call shows the whole performance. It is held
// in the worker's memory: a convenience for comparing runs by eye, not
// accounting. A worker restart starts it again, and a room quiet for an hour
// is forgotten.
type roomTotals struct {
	mu    sync.Mutex
	now   func() time.Time
	rooms map[string]*roomTotal
}

type roomTotal struct {
	calls        int
	cost         float64
	withoutCache float64
	last         time.Time
}

// add counts one call for a room and returns the room's totals so far. An
// unpriced call is counted but adds no cost.
func (t *roomTotals) add(room string, cost, withoutCache float64) roomTotal {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	if t.now != nil {
		now = t.now()
	}
	if t.rooms == nil {
		t.rooms = map[string]*roomTotal{}
	}
	for id, total := range t.rooms {
		if now.Sub(total.last) > roomQuiet {
			delete(t.rooms, id)
		}
	}
	total := t.rooms[room]
	if total == nil {
		total = &roomTotal{}
		t.rooms[room] = total
	}
	total.calls++
	total.cost += cost
	total.withoutCache += withoutCache
	total.last = now
	return *total
}
