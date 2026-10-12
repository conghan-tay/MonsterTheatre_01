package e2e_test

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"testing"

	"github.com/conghan-tay/MonsterTheatre_01/tests/e2e/harness"
)

// stack is the system under test, shared by every test in this package.
var stack *harness.Stack

func TestMain(m *testing.M) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	s, err := harness.Start(ctx)
	if err != nil {
		cancel()
		// Exit code 2 and this banner separate "the harness could not start"
		// from "a test failed".
		fmt.Fprintf(os.Stderr, "\nHARNESS FAILURE (the stack did not start; no tests ran)\n%v\n", err)
		os.Exit(2)
	}
	stack = s
	if s.Remote {
		fmt.Printf("e2e against the deployment at %s\n", s.BaseURL)
	} else {
		fmt.Printf("e2e stack up: %s (logs: %s)\n", s.BaseURL, s.LogDir)
	}

	// Tear down on Ctrl-C too, so no child processes are left behind.
	go func() {
		<-ctx.Done()
		s.Stop()
	}()

	code := m.Run()
	cancel()
	s.Stop()
	os.Exit(code)
}

// requireLocal skips a test that needs something only the stack the harness
// starts itself can give: a worker to kill, a second server, scripts on the
// worker's disk. Against a deployment (E2E_BASE_URL) such a test cannot run.
func requireLocal(t *testing.T, why string) {
	t.Helper()
	if stack.Remote {
		t.Skipf("not run against a deployment: %s", why)
	}
}
