package fixtures

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Operations a script can make fail (server/internal/providers/fake).
const (
	OpChoices = "choices"
	OpTags    = "tags"
	OpSpeech  = "speech"
	// OpLipSync fails a turn's lip-sync submission, which is tried once.
	OpLipSync = "lipsync"
)

// Always is an attempt count no retry policy reaches: the operation fails
// however often it is tried.
const Always = 1_000_000

// Failure makes one operation on one turn fail for its first Attempts
// attempts. The fakes decide from the attempt number alone, so the answer is
// the same after a worker restart.
type Failure struct {
	Op       string `json:"op"`
	Turn     int    `json:"turn"`
	Attempts int    `json:"attempts"`
}

// Script tells the fake providers how to behave for one room.
type Script struct {
	// Choices maps a turn to the three lines to offer. Turns not listed get
	// the fake's defaults.
	Choices map[int][]string `json:"choices,omitempty"`
	// Failures are the operations to fail.
	Failures []Failure `json:"failures,omitempty"`
	// LipSyncDelays maps a turn to how long its lip-sync video takes, in
	// milliseconds after it is submitted.
	LipSyncDelays map[int]int `json:"lipSyncDelays,omitempty"`
}

// Write stores the script where the fakes read it. scriptDir is the stack's
// ScriptDir. It must be written before the performance starts.
func (s Script) Write(t testing.TB, scriptDir, roomID string) {
	t.Helper()
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("fixture: encode script: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scriptDir, roomID+".json"), data, 0o644); err != nil {
		t.Fatalf("fixture: write script: %v", err)
	}
}

// ScriptedChoices tells the fake language model which three lines to offer
// on the given turns of one room.
func ScriptedChoices(t testing.TB, scriptDir, roomID string, choices map[int][]string) {
	t.Helper()
	Script{Choices: choices}.Write(t, scriptDir, roomID)
}
