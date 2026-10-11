// Command stack runs the end-to-end test stack as a long-lived process, for
// tests that are not written in Go: the Playwright browser tests start it,
// point the web app at it, and stop it when they finish.
//
// It is the same isolated stack the Go tests use (its own database, Redis
// database, and Temporal dev server), started by the same harness.
//
// Usage:
//
//	stack -port 18080 -public-base-url http://127.0.0.1:13000 -director-username NAME -script-dir DIR
//
// With -director-username, a director account is created; its password is
// read from STACK_DIRECTOR_PASSWORD.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/conghan-tay/MonsterTheatre_01/tests/e2e/harness"
)

func main() {
	port := flag.Int("port", 0, "port for the Go server (0 picks a free one)")
	publicBaseURL := flag.String("public-base-url", "", "where the web pages are served; share links start with it")
	director := flag.String("director-username", "", "create a director with this username")
	scriptDir := flag.String("script-dir", "", "where the fake providers read per-room scripts (default: a directory of the stack's own)")
	flag.Parse()

	if err := run(*port, *publicBaseURL, *director, *scriptDir); err != nil {
		fmt.Fprintln(os.Stderr, "stack:", err)
		os.Exit(1)
	}
}

func run(port int, publicBaseURL, director, scriptDir string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	stack, err := harness.StartWith(ctx, harness.Options{ServerPort: port, PublicBaseURL: publicBaseURL, ScriptDir: scriptDir})
	if err != nil {
		return err
	}
	defer stack.Stop()

	if director != "" {
		password := os.Getenv("STACK_DIRECTOR_PASSWORD")
		if password == "" {
			return fmt.Errorf("-director-username needs STACK_DIRECTOR_PASSWORD")
		}
		if err := stack.AddDirector(ctx, director, password); err != nil {
			return err
		}
	}

	fmt.Printf("stack ready: %s (logs: %s)\n", stack.BaseURL, stack.LogDir)
	<-ctx.Done()
	fmt.Println("stack stopping")
	return nil
}
