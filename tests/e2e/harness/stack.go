// Package harness starts the real server and worker, with their real
// dependencies, for the end-to-end tests.
//
// It is isolated from `make dev`: it uses its own database and Redis database
// in the shared containers, its own Temporal dev server, and free ports, so
// both can run at the same time.
package harness

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	adminDatabaseURL = "postgres://monster:monster@localhost:5432/monstertheatre?sslmode=disable"
	e2eDatabase      = "monstertheatre_e2e"
	e2eDatabaseURL   = "postgres://monster:monster@localhost:5432/" + e2eDatabase + "?sslmode=disable"
	e2eRedisURL      = "redis://localhost:6379/1"

	startTimeout = 60 * time.Second

	// PublicBaseURL is where the test stack is told the web pages live. Share
	// links start with it.
	PublicBaseURL = "http://theatre.test"
)

// Stack is the running system under test.
type Stack struct {
	// BaseURL is the server's address, for example "http://127.0.0.1:54321".
	BaseURL string
	// ScriptDir is where tests write per-room scripts for the fake providers.
	ScriptDir string
	// LogDir holds one log file per child process.
	LogDir string

	repoRoot       string
	workDir        string
	env            []string
	startedCompose bool

	temporal *process
	server   *process
	worker   *process
}

// Start brings the whole stack up. On failure it tears down whatever it
// started and returns an error that includes the relevant log tails.
func Start(ctx context.Context) (*Stack, error) {
	return StartWith(ctx, Options{})
}

// Options adjusts how the stack starts. The zero value is what the Go tests use.
type Options struct {
	// ServerPort fixes the server's port. Zero picks a free one. The browser
	// tests fix it, because the web app must know where to proxy before the
	// stack is up.
	ServerPort int
	// PublicBaseURL is where the stack is told the web pages live. Empty
	// means PublicBaseURL.
	PublicBaseURL string
}

// StartWith is Start with options.
func StartWith(ctx context.Context, opts Options) (stack *Stack, err error) {
	if opts.PublicBaseURL == "" {
		opts.PublicBaseURL = PublicBaseURL
	}
	repoRoot, err := findRepoRoot()
	if err != nil {
		return nil, err
	}
	workDir, err := os.MkdirTemp("", "monstertheatre-e2e-")
	if err != nil {
		return nil, err
	}
	s := &Stack{
		repoRoot:  repoRoot,
		workDir:   workDir,
		LogDir:    filepath.Join(workDir, "logs"),
		ScriptDir: filepath.Join(workDir, "scripts"),
	}
	defer func() {
		if err != nil {
			err = fmt.Errorf("%w\n%s", err, s.logTails())
			s.Stop()
		}
	}()

	for _, dir := range []string{s.LogDir, s.ScriptDir, filepath.Join(workDir, "objects"), filepath.Join(workDir, "bin")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}

	if err := s.startContainers(ctx); err != nil {
		return nil, err
	}
	if err := ensureDatabase(ctx); err != nil {
		return nil, err
	}

	temporalPort, err := freePort()
	if err != nil {
		return nil, err
	}
	temporalAddress := fmt.Sprintf("127.0.0.1:%d", temporalPort)
	s.temporal, err = startProcess("temporal", s.LogDir, os.Environ(), "temporal",
		"server", "start-dev", "--headless", "--ip", "127.0.0.1",
		"--port", fmt.Sprint(temporalPort), "--log-level", "error")
	if err != nil {
		return nil, err
	}
	if err := waitFor(ctx, "Temporal dev server", s.temporal, func() error {
		return exec.CommandContext(ctx, "temporal", "operator", "cluster", "health", "--address", temporalAddress).Run()
	}); err != nil {
		return nil, err
	}

	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return nil, err
	}
	shareLinkSecret := hex.EncodeToString(secretBytes)

	serverPort := opts.ServerPort
	if serverPort == 0 {
		if serverPort, err = freePort(); err != nil {
			return nil, err
		}
	}
	s.BaseURL = fmt.Sprintf("http://127.0.0.1:%d", serverPort)
	s.env = childEnv(map[string]string{
		"HTTP_ADDR":           fmt.Sprintf("127.0.0.1:%d", serverPort),
		"DATABASE_URL":        e2eDatabaseURL,
		"REDIS_URL":           e2eRedisURL,
		"TEMPORAL_ADDRESS":    temporalAddress,
		"TEMPORAL_NAMESPACE":  "default",
		"TEMPORAL_TASK_QUEUE": "performance",
		"LOG_LEVEL":           "debug",
		// The test stack runs over plain HTTP, so cookies must not be Secure.
		"COOKIE_SECURE": "false",
		// A fresh secret per run; share links from one run are useless in the next.
		"SHARE_LINK_SECRET": shareLinkSecret,
		"PUBLIC_BASE_URL":   opts.PublicBaseURL,
		// Settings below are read by later steps; they are set from the
		// start so the test stack never reaches a paid provider.
		"LLM_PROVIDER":     "fake",
		"TTS_PROVIDER":     "fake",
		"FAKE_SCRIPT_DIR":  s.ScriptDir,
		"OBJECT_STORE_DIR": filepath.Join(workDir, "objects"),
		"VOTE_WINDOW":      "2s",
		// How long choice generation is retried before the run fails. Short,
		// so the test of that failure takes seconds and not minutes.
		"CHOICES_RETRY_WINDOW": "4s",
		"PREMIERE_COUNTDOWN":   "1s",
	})

	if err := s.build(ctx); err != nil {
		return nil, err
	}
	if out, err := s.command(ctx, s.bin("migrate"), "up").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("apply migrations: %w\n%s", err, out)
	}

	if s.server, err = startProcess("server", s.LogDir, s.env, s.bin("server")); err != nil {
		return nil, err
	}
	if err := s.StartWorker(); err != nil {
		return nil, err
	}
	if err := waitFor(ctx, "server /healthz", s.server, func() error {
		resp, err := http.Get(s.BaseURL + "/healthz")
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("status %d", resp.StatusCode)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return s, nil
}

// Stop tears the stack down. It stops the containers only if Start started
// them, so a running `make dev` is left alone.
func (s *Stack) Stop() {
	if s == nil {
		return
	}
	s.worker.kill()
	s.server.kill()
	s.temporal.kill()
	if s.startedCompose {
		cmd := exec.Command("docker", "compose", "stop")
		cmd.Dir = s.repoRoot
		_ = cmd.Run()
	}
	// Keep the logs for diagnosis; drop the binaries and stored objects.
	_ = os.RemoveAll(filepath.Join(s.workDir, "bin"))
	_ = os.RemoveAll(filepath.Join(s.workDir, "objects"))
}

// PerformanceRow is a room's row in the performances table.
type PerformanceRow struct {
	ID     string
	Status string
	// Script and AudioKeys are the stored JSON, empty until the run completes.
	Script    []byte
	AudioKeys []byte
	// FailureReason is set on a failed run.
	FailureReason string
	// PremiereStartsAt is set on a completed run.
	PremiereStartsAt *time.Time
}

// PerformanceRow reads a room's performance straight from the database, for
// what no endpoint exposes yet. found is false if the room has none.
func (s *Stack) PerformanceRow(ctx context.Context, roomID string) (row PerformanceRow, found bool, err error) {
	conn, err := pgx.Connect(ctx, e2eDatabaseURL)
	if err != nil {
		return row, false, err
	}
	defer conn.Close(ctx)
	err = conn.QueryRow(ctx,
		"SELECT id::text, status, script::text, audio_keys::text, coalesce(failure_reason, ''), premiere_starts_at FROM performances WHERE room_id = $1", roomID,
	).Scan(&row.ID, &row.Status, &row.Script, &row.AudioKeys, &row.FailureReason, &row.PremiereStartsAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return row, false, nil
	}
	return row, err == nil, err
}

// PerformanceCount returns how many performance rows a room has.
func (s *Stack) PerformanceCount(ctx context.Context, roomID string) (int, error) {
	conn, err := pgx.Connect(ctx, e2eDatabaseURL)
	if err != nil {
		return 0, err
	}
	defer conn.Close(ctx)
	var n int
	err = conn.QueryRow(ctx, "SELECT count(*) FROM performances WHERE room_id = $1", roomID).Scan(&n)
	return n, err
}

// StoredObject reads an object the stack has stored, such as a turn's
// recording, from the filesystem object store.
func (s *Stack) StoredObject(key string) ([]byte, error) {
	return os.ReadFile(filepath.Join(s.workDir, "objects", filepath.FromSlash(key)))
}

// StartWorker starts the worker process. Start calls it; tests call it again
// after StopWorker.
func (s *Stack) StartWorker() error {
	if s.worker != nil {
		return errors.New("worker is already running")
	}
	worker, err := startProcess("worker", s.LogDir, s.env, s.bin("worker"))
	if err != nil {
		return err
	}
	s.worker = worker
	return nil
}

// StopWorker kills the worker, as a crash would.
func (s *Stack) StopWorker() error {
	if s.worker == nil {
		return errors.New("worker is not running")
	}
	s.worker.kill()
	s.worker = nil
	return nil
}

// AddDirector creates a director account with the same command an
// administrator uses.
func (s *Stack) AddDirector(ctx context.Context, username, password string) error {
	cmd := s.command(ctx, s.bin("adddirector"), "-username", username)
	cmd.Env = append(cmd.Env, "DIRECTOR_PASSWORD="+password)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("add director %q: %w\n%s", username, err, out)
	}
	return nil
}

// WorkerRunning reports whether the harness currently has a worker process.
func (s *Stack) WorkerRunning() bool {
	return s.worker != nil && !s.worker.exited()
}

func (s *Stack) startContainers(ctx context.Context) error {
	ps := exec.CommandContext(ctx, "docker", "compose", "ps", "--status", "running", "-q")
	ps.Dir = s.repoRoot
	running, err := ps.Output()
	if err != nil {
		return fmt.Errorf("docker compose ps (is Docker running?): %w", err)
	}
	s.startedCompose = strings.TrimSpace(string(running)) == ""

	up := exec.CommandContext(ctx, "docker", "compose", "up", "-d", "--wait")
	up.Dir = s.repoRoot
	if out, err := up.CombinedOutput(); err != nil {
		return fmt.Errorf("docker compose up: %w\n%s", err, out)
	}
	return nil
}

func (s *Stack) build(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(s.workDir, "bin")+string(os.PathSeparator),
		"./cmd/server", "./cmd/worker", "./cmd/migrate", "./cmd/adddirector")
	cmd.Dir = filepath.Join(s.repoRoot, "server")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("build server binaries: %w\n%s", err, out)
	}
	return nil
}

func (s *Stack) command(ctx context.Context, bin string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, args...)
	// A copy: callers append to it, and tests run commands in parallel.
	cmd.Env = slices.Clone(s.env)
	return cmd
}

func (s *Stack) bin(name string) string {
	return filepath.Join(s.workDir, "bin", name)
}

func (s *Stack) logTails() string {
	var b strings.Builder
	fmt.Fprintf(&b, "logs: %s\n", s.LogDir)
	for _, p := range []*process{s.temporal, s.server, s.worker} {
		if p != nil {
			fmt.Fprintf(&b, "--- last lines of %s\n%s\n", p.name, p.logTail(15))
		}
	}
	return b.String()
}

// ensureDatabase creates the E2E database if it does not exist yet.
func ensureDatabase(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, adminDatabaseURL)
	if err != nil {
		return fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	defer conn.Close(ctx)

	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", e2eDatabase).Scan(&exists); err != nil {
		return fmt.Errorf("check for %s: %w", e2eDatabase, err)
	}
	if exists {
		return nil
	}
	// CREATE DATABASE takes no parameters; the name is a constant.
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+e2eDatabase); err != nil {
		return fmt.Errorf("create %s: %w", e2eDatabase, err)
	}
	return nil
}

// childEnv is the parent environment with provider keys removed, so the test
// stack cannot make a paid call even by mistake, plus the given overrides.
func childEnv(overrides map[string]string) []string {
	blocked := map[string]bool{"ANTHROPIC_API_KEY": true, "OPENAI_API_KEY": true, "ELEVENLABS_API_KEY": true}
	var env []string
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if blocked[key] {
			continue
		}
		if _, overridden := overrides[key]; overridden {
			continue
		}
		env = append(env, kv)
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

// waitFor polls check until it passes. It gives up early if the process it
// depends on has exited, since waiting longer cannot help.
func waitFor(ctx context.Context, what string, p *process, check func() error) error {
	deadline := time.Now().Add(startTimeout)
	var last error
	for time.Now().Before(deadline) {
		if last = check(); last == nil {
			return nil
		}
		if p.exited() {
			return fmt.Errorf("%s exited while starting (waiting for %s): %w", p.name, what, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("timed out waiting for %s: %w", what, last)
}

// findRepoRoot walks up from the working directory to the directory that
// holds docker-compose.yml.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "docker-compose.yml")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("cannot find repository root (no docker-compose.yml above the working directory)")
		}
		dir = parent
	}
}
