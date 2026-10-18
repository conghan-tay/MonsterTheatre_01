package harness

import (
	"strings"
	"testing"
)

// The test stack must never reach a paid provider, whatever the shell it
// was started from holds.
func TestChildEnvDropsEveryProviderKey(t *testing.T) {
	for _, key := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "ELEVENLABS_API_KEY", "WAVESPEED_API_KEY"} {
		t.Setenv(key, "a-real-key")
	}
	t.Setenv("SOMETHING_ELSE", "kept")

	env := childEnv(map[string]string{"LIPSYNC_PROVIDER": "fake"})
	joined := "\n" + strings.Join(env, "\n") + "\n"
	for _, key := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "ELEVENLABS_API_KEY", "WAVESPEED_API_KEY"} {
		if strings.Contains(joined, "\n"+key+"=") {
			t.Errorf("%s reached the test stack", key)
		}
	}
	if !strings.Contains(joined, "\nSOMETHING_ELSE=kept\n") || !strings.Contains(joined, "\nLIPSYNC_PROVIDER=fake\n") {
		t.Errorf("the rest of the environment, and the overrides, should be passed on: %s", joined)
	}
}
