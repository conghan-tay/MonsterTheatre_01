package fixtures

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// ScriptedChoices tells the fake language model which three lines to offer
// on the given turns of one room. Turns not listed get the fake's defaults.
// scriptDir is the stack's ScriptDir.
func ScriptedChoices(t testing.TB, scriptDir, roomID string, choices map[int][]string) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"choices": choices})
	if err != nil {
		t.Fatalf("fixture: encode script: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scriptDir, roomID+".json"), data, 0o644); err != nil {
		t.Fatalf("fixture: write script: %v", err)
	}
}
