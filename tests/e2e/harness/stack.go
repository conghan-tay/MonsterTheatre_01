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

	"github.com/aws/aws-sdk-go-v2/service/s3"
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
	// PublicBaseURL is where the stack was told the web pages live. Share
	// links start with it, and it is an origin the WebSocket accepts.
	PublicBaseURL string
	// Remote is true when the stack is a deployment the harness did not
	// start (see connectRemote). Tests that need to control the processes
	// skip themselves.
	Remote bool
	// SecondBaseURL is the address of a second server process: the same
	// program, with the same database, Redis, and Temporal, on another port.
	// Tests use it to check that people on different servers share a room.
	SecondBaseURL string
	// ScriptDir is where tests write per-room scripts for the fake providers.
	ScriptDir string
	// LogDir holds one log file per child process.
	LogDir string

	repoRoot       string
	workDir        string
	env            []string
	startedCompose bool

	// Set only for a deployment.
	dsqlEndpoint string
	bucket       string
	s3           *s3.Client

	temporal        *process
	temporalAddress string
	server          *process
	secondServer    *process
	worker          *process
	redisProxy      *proxy
}

// Start brings the whole stack up. On failure it tears down whatever it
// started and returns an error that includes the relevant log tails.
//
// With E2E_BASE_URL set it starts nothing, and returns the deployment at
// that address instead.
func Start(ctx context.Context) (*Stack, error) {
	if os.Getenv(EnvBaseURL) != "" {
		return connectRemote(ctx)
	}
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
	// ScriptDir is where the fake providers read per-room scripts. Empty
	// means a directory of the stack's own. The browser tests fix it, so
	// that they can write a script without asking the stack where.
	ScriptDir string
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
		PublicBaseURL: opts.PublicBaseURL,
		repoRoot:      repoRoot,
		workDir:       workDir,
		LogDir:        filepath.Join(workDir, "logs"),
		ScriptDir:     filepath.Join(workDir, "scripts"),
	}
	if opts.ScriptDir != "" {
		s.ScriptDir = opts.ScriptDir
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
	s.temporalAddress = temporalAddress
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
		"LIPSYNC_PROVIDER": "fake",
		// The stand-in's videos are ready at once or after a scripted
		// delay; asking every 20 s, as for the real one, would only slow
		// the tests.
		"LIPSYNC_POLL_INTERVAL": "200ms",
		"FAKE_SCRIPT_DIR":       s.ScriptDir,
		"OBJECT_STORE_DIR":      filepath.Join(workDir, "objects"),
		"VOTE_WINDOW":           "2s",
		// How long choice generation is retried before the run fails. Short,
		// so the test of that failure takes seconds and not minutes.
		"CHOICES_RETRY_WINDOW": "4s",
		"PREMIERE_COUNTDOWN":   "1s",
		// How long a running performance may go without an update before a
		// server logs it as stalled. Short, so the telemetry tests can see
		// one in seconds; every other test only gains a log line.
		"STALL_AFTER": "1s",
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
	secondPort, err := freePort()
	if err != nil {
		return nil, err
	}
	s.SecondBaseURL = fmt.Sprintf("http://127.0.0.1:%d", secondPort)
	// The second server reaches Redis through a proxy that a test can cut,
	// to break that server's stream reader alone (CutSecondServerRedis).
	if s.redisProxy, err = startProxy("localhost:6379"); err != nil {
		return nil, err
	}
	// A later entry overrides an earlier one with the same name.
	secondEnv := append(append([]string{}, s.env...),
		fmt.Sprintf("HTTP_ADDR=127.0.0.1:%d", secondPort),
		fmt.Sprintf("REDIS_URL=redis://%s/1", s.redisProxy.addr()))
	if s.secondServer, err = startProcess("server-2", s.LogDir, secondEnv, s.bin("server")); err != nil {
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
	if err := waitFor(ctx, "second server /healthz", s.secondServer, func() error {
		resp, err := http.Get(s.SecondBaseURL + "/healthz")
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
	s.secondServer.kill()
	s.redisProxy.close()
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
	conn, err := s.connect(ctx)
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
	conn, err := s.connect(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close(ctx)
	var n int
	err = conn.QueryRow(ctx, "SELECT count(*) FROM performances WHERE room_id = $1", roomID).Scan(&n)
	return n, err
}

// InsertPerformance writes a running performance row for a room without
// starting a workflow, and returns its ID. That is what a Start leaves behind
// when it dies between its two steps, which the endpoint itself cannot be
// made to do.
func (s *Stack) InsertPerformance(ctx context.Context, roomID string) (string, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close(ctx)
	var id string
	err = conn.QueryRow(ctx, `
		INSERT INTO performances (id, room_id, status, created_at)
		VALUES (gen_random_uuid(), $1, 'running', now())
		RETURNING id::text`, roomID).Scan(&id)
	return id, err
}

// TerminateWorkflow ends a performance's workflow from outside, as an
// operator could. The workflow gets no chance to mark its row, which is
// also how a run ended by its timeout leaves things.
func (s *Stack) TerminateWorkflow(ctx context.Context, performanceID string) error {
	out, err := exec.CommandContext(ctx, "temporal", "workflow", "terminate",
		"--address", s.temporalAddress, "--namespace", "default",
		"--workflow-id", "performance/"+performanceID, "--reason", "e2e test").CombinedOutput()
	if err != nil {
		return fmt.Errorf("terminate workflow of %s: %w\n%s", performanceID, err, out)
	}
	return nil
}

// IndexDefinition returns the definition of a named index in the test
// database, as PostgreSQL records it, or "" if there is no such index.
func (s *Stack) IndexDefinition(ctx context.Context, name string) (string, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close(ctx)
	var definition string
	err = conn.QueryRow(ctx, "SELECT indexdef FROM pg_indexes WHERE schemaname = 'public' AND indexname = $1", name).Scan(&definition)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return definition, err
}

// IndexIsValid reports whether a named index is ready for use. On Aurora
// DSQL an index is built in the background and is invalid until that ends,
// or for good if the build failed.
func (s *Stack) IndexIsValid(ctx context.Context, name string) (bool, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close(ctx)
	var valid bool
	err = conn.QueryRow(ctx,
		"SELECT i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid WHERE c.relname = $1", name).Scan(&valid)
	return valid, err
}

// ColumnType returns the data type of a column in the test database, as
// PostgreSQL names it, or "" if there is no such column.
func (s *Stack) ColumnType(ctx context.Context, table, column string) (string, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close(ctx)
	var dataType string
	err = conn.QueryRow(ctx,
		"SELECT data_type FROM information_schema.columns WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2",
		table, column).Scan(&dataType)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return dataType, err
}

// PlanWithoutScanning returns how PostgreSQL would run a query if it were
// not allowed to read a whole table. The test tables hold a few rows, and
// for those a full read is the sensible plan whatever indexes exist; with
// that ruled out, the plan shows whether an index can serve the query.
func (s *Stack) PlanWithoutScanning(ctx context.Context, query string, args ...any) (string, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close(ctx)
	// For this connection only, which is closed when this returns.
	if _, err := conn.Exec(ctx, "SET enable_seqscan = off"); err != nil {
		return "", err
	}
	rows, err := conn.Query(ctx, "EXPLAIN "+query, args...)
	if err != nil {
		return "", err
	}
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	return strings.Join(lines, "\n"), err
}

// StoredObject reads an object the stack has stored, such as a turn's
// recording, from its object store: the filesystem one, or a deployment's
// bucket.
func (s *Stack) StoredObject(key string) ([]byte, error) {
	if s.Remote {
		return s.storedObjectS3(key)
	}
	return os.ReadFile(filepath.Join(s.workDir, "objects", filepath.FromSlash(key)))
}

// ScratchDatabase creates an empty, migrated database of its own in the test
// PostgreSQL, for a test that must not share rows with the others (one that
// deletes everything, say). It returns the connection string; drop removes
// the database again.
func (s *Stack) ScratchDatabase(ctx context.Context) (url string, drop func(), err error) {
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return "", nil, err
	}
	name := "monstertheatre_scratch_" + hex.EncodeToString(suffix)

	admin, err := pgx.Connect(ctx, adminDatabaseURL)
	if err != nil {
		return "", nil, err
	}
	defer admin.Close(ctx)
	// The name is made here from hex digits, never from input.
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		return "", nil, err
	}
	drop = func() {
		conn, err := pgx.Connect(context.Background(), adminDatabaseURL)
		if err != nil {
			return
		}
		defer conn.Close(context.Background())
		_, _ = conn.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	}

	url = "postgres://monster:monster@localhost:5432/" + name + "?sslmode=disable"
	migrate := s.command(ctx, s.bin("migrate"), "up")
	migrate.Env = append(migrate.Env, "DATABASE_URL="+url)
	if out, err := migrate.CombinedOutput(); err != nil {
		drop()
		return "", nil, fmt.Errorf("migrate scratch database: %w\n%s", err, out)
	}
	return url, drop, nil
}

// RunAdmin runs the dbadmin command against the given database and object
// directory and returns what it printed.
func (s *Stack) RunAdmin(ctx context.Context, databaseURL, objectDir string, args ...string) (string, error) {
	cmd := s.command(ctx, s.bin("dbadmin"), args...)
	cmd.Env = append(cmd.Env, "DATABASE_URL="+databaseURL, "OBJECT_STORE_DIR="+objectDir)
	out, err := cmd.CombinedOutput()
	return string(out), err
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
		"./cmd/server", "./cmd/worker", "./cmd/migrate", "./cmd/adddirector", "./cmd/dbadmin")
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
	for _, p := range []*process{s.temporal, s.server, s.secondServer, s.worker} {
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
	blocked := map[string]bool{"ANTHROPIC_API_KEY": true, "OPENAI_API_KEY": true, "ELEVENLABS_API_KEY": true, "WAVESPEED_API_KEY": true}
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
