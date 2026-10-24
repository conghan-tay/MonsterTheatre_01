// Package fake provides scripted stand-ins for the language model and speech
// service, used by tests and by local development without API keys.
package fake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Operations that a script can make fail.
const (
	OpChoices = "choices"
	OpTags    = "tags"
	OpSpeech  = "speech"
	// OpLipSync fails a lip-sync submission. It is tried once, so any
	// attempts of one or more fail it.
	OpLipSync = "lipsync"
)

// ErrInjected is the error returned for a scripted failure.
var ErrInjected = errors.New("fake provider: scripted failure")

// Script tells the fakes how to behave for one room.
//
// Example, as stored in <dir>/<roomId>.json:
//
//	{
//	  "choices":  {"1": ["Hello.", "Greetings.", "Oh, it's you."]},
//	  "failures": [{"op": "speech", "turn": 2, "attempts": 3}],
//	  "lipSyncDelays": {"1": 3000}
//	}
type Script struct {
	// Choices maps a turn number to the three lines to offer. Turns not
	// listed get predictable defaults.
	Choices map[int][]string `json:"choices"`
	// Failures lists operations that fail for their first Attempts attempts.
	Failures []Failure `json:"failures"`
	// LipSyncDelays maps a turn number to how long, in milliseconds, its
	// lip-sync video takes after it is submitted. Turns not listed are
	// ready at once.
	LipSyncDelays map[int]int `json:"lipSyncDelays"`
	// Delays slow one operation on one turn, on every attempt, the way a
	// slow provider would. The wait ends early if the call is cancelled.
	Delays []Delay `json:"delays"`
}

// Delay makes one operation on one turn take Ms milliseconds longer.
type Delay struct {
	Op   string `json:"op"`
	Turn int    `json:"turn"`
	Ms   int    `json:"ms"`
}

// wait holds a call for its scripted delay, or until ctx ends.
func (s Script) wait(ctx context.Context, op string, turn int) error {
	for _, d := range s.Delays {
		if d.Op != op || d.Turn != turn || d.Ms <= 0 {
			continue
		}
		timer := time.NewTimer(time.Duration(d.Ms) * time.Millisecond)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
	return nil
}

// Failure makes one operation on one turn fail for attempts 1..Attempts.
type Failure struct {
	Op       string `json:"op"`
	Turn     int    `json:"turn"`
	Attempts int    `json:"attempts"`
}

// fails reports whether this attempt should fail. It depends only on the
// attempt number, so it gives the same answer after a worker restart.
func (s Script) fails(op string, turn, attempt int) bool {
	for _, f := range s.Failures {
		if f.Op == op && f.Turn == turn && attempt <= f.Attempts {
			return true
		}
	}
	return false
}

// Scripts loads per-room scripts from a directory. One worker can therefore
// serve many tests at once, each with its own script. The zero value, or an
// empty directory, means no room has a script.
type Scripts struct {
	Dir string
}

// For returns the script for a room, or an empty script if it has none.
func (s Scripts) For(roomID string) (Script, error) {
	var script Script
	if s.Dir == "" {
		return script, nil
	}
	if roomID == "" || strings.ContainsAny(roomID, `/\.`) {
		return script, fmt.Errorf("fake provider: unusable room id %q", roomID)
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, roomID+".json"))
	if errors.Is(err, fs.ErrNotExist) {
		return script, nil
	}
	if err != nil {
		return script, fmt.Errorf("fake provider: read script: %w", err)
	}
	if err := json.Unmarshal(data, &script); err != nil {
		return script, fmt.Errorf("fake provider: parse script for room %s: %w", roomID, err)
	}
	return script, nil
}
