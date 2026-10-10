# Monster Theatre — Implementation Plan

Status: proposed on 2026-10-08, for review. It implements [the system design](system-design.md) under the choices in [the decisions log](decisions.md).

How to use it: work through the build plan in order. Each step starts by writing its end-to-end tests, watching them fail for the expected reason, then building until they pass with every earlier test still green.

## 1. E2E testing strategy

### What counts as an E2E test here

A Go test that drives the real server and the real worker over HTTP and WebSocket, exactly as a browser would, with real PostgreSQL, Redis, and a Temporal dev server behind them. Only the three paid or remote services are replaced by fakes: the language model, ElevenLabs, and S3.

This API-level suite is the progress dashboard. A small Playwright suite covers what only a browser can prove (the forms, the voting screen, and audio playback).

```text
tests/e2e (Go)  ──HTTP + WebSocket──▶  server ──▶ PostgreSQL, Redis, Temporal dev server
                                       worker ──▶ fake language model, fake speech, local object store
web/e2e (Playwright)  ──browser──▶  Next.js ──▶ the same stack
```

### How the stack is made testable

| Need | Mechanism |
| --- | --- |
| Fast runs | `VOTE_WINDOW` and `PREMIERE_COUNTDOWN` are configuration. Tests use 2 seconds and 1 second; production uses 10 and 5 |
| No paid calls | `LLM_PROVIDER=fake` and `TTS_PROVIDER=fake` select in-process fakes on the worker. The fake model returns scripted choices; the fake speech returns a tiny valid MP3 generated in code |
| Forced failures | The fakes read a per-room script (for example "speech fails for the first 3 attempts on turn 2") from `<roomId>.json` in a directory named by `FAKE_SCRIPT_DIR`. Scripts are per room so one worker can serve many tests at once; failures are decided from the attempt number, so they behave the same after a worker restart |
| No S3 emulator | Storage sits behind an `ObjectStore` interface with two implementations: local filesystem (tests and local development) and S3 (AWS). One separate smoke test exercises the S3 implementation against a real development bucket |
| Killing the worker | The harness starts the server and worker as child processes, so a test can stop and restart the worker mid-run |
| Clean state | Each test creates its own director and room; nothing is shared between tests, so no table truncation is needed |
| Isolation from `make dev` | The harness uses its own database (`monstertheatre_e2e`), Redis database 1, its own in-memory Temporal dev server, and free ports, so it can run while the dev stack is up |

### Fixtures

- `NewDirector(t)`: returns credentials for a director unique to the test. From step 4 it also creates the account, through the add-director command.
- `SampleRoom()`: a valid scene, two characters, and two tiny PNG portraits generated in code.
- `ScriptedChoices(...)` and `FailureScript(...)`: write the room's script for the fakes. Added at step 9, when rooms and the workflow exist.
- `StartStack(t)`: starts or reuses the containers, runs migrations, and launches the server and worker processes.

Note: `.gitignore` excludes `*.mp3` and `*.wav`, so audio fixtures are generated in code and never committed.

### Client

`tests/e2e/client` wraps everything a browser does, so tests read as user actions. Each method is added in the step whose test first needs it:

- `DirectorClient`: `Login`, `CreateRoom`, `Start`, `GetRoom`, `Connect`.
- `AudienceClient`: `Enter(shareLink)`, `Connect`, `Vote(turn, choice)`, `FetchPortrait`, `FetchAudio`.
- `Conn` (a WebSocket): `Snapshot()`, `NextUpdate()`, `WaitForPhase(phase, turn)`, `Close()`.
- Each client keeps its own cookie jar, so ten audience members are ten clients.

### Happy paths

| ID | Proves | Built in step |
| --- | --- | --- |
| H1 | A director logs in, creates a room with two portraits, and gets a share link; the portraits can be fetched back | 4, 5 |
| H2 | An audience member enters through the link and gets a session; re-entering with the same cookie restores it without using another slot | 6 |
| H3 | A connected browser's first message is a snapshot saying the room is waiting for the director | 8 |
| H4 | After Start, with no votes, connected browsers receive revisions 1 to 25 in order and the performance row ends `completed` with a full script | 9 |
| H5 | Votes decide the winner: majority wins, the director's vote counts, and a tie is settled once | 10 |
| H6 | A browser that reconnects mid-round gets a snapshot containing its own vote and then continues from the right revision | 10 |
| H7 | After completion, all eight clips can be fetched, including by range request, and a browser connecting later gets the completed snapshot | 12 |
| H8 | The worker is stopped mid-performance and restarted; connected browsers stay connected and the run completes | 13 |

### Sad paths

| ID | Proves | Built in step |
| --- | --- | --- |
| S1 | Wrong password is refused; repeated failures are throttled | 4 |
| S2 | Invalid room input (missing portrait, oversize or non-image file, over-long text) is refused with a clear error and creates nothing | 5 |
| S3 | A director cannot read or start another director's room | 5, 9 |
| S4 | An eleventh audience member is refused; a bad share link is refused | 6 |
| S5 | A WebSocket from another origin, or with no session, is refused | 8 |
| S6 | A second Start returns the same performance and does not create another | 9 |
| S7 | A second vote from the same voter, a vote for an unknown choice, and a vote after the window closes are all ignored | 10 |
| S8 | Speech failing past its retry limit ends the run as `failed`, and browsers are told | 11 |
| S9 | Choice generation that fails briefly is retried and the run completes; failing past its retry window it ends the run as `failed` (D038); tagging failing falls back to the untagged line and the run still completes | 11 |
| S10 | Audio is not served before the performance completes, or to someone outside the room | 12 |
| S11 | A browser connecting while the worker is down is told the performance is recovering, then succeeds once the worker returns | 13 |
| S12 | A browser that misses an update sends `resync` and receives a fresh snapshot | 13 |

### Unit and workflow tests

Kept small, for logic that is awkward to reach end to end: winner selection (majority, tie, empty), vote bookkeeping, the conflict-retry helper, input validation, the tag-preservation check, and the workflow itself under Temporal's test environment with mocked activities.

## 2. Dependencies

Versions were checked against the Go module proxy, the npm registry, and Docker Hub on 2026-10-08. Go modules are pinned exactly by `go.sum`; npm packages by the lockfile.

### Toolchain (already installed locally)

| Name | Version | Purpose |
| --- | --- | --- |
| Go | 1.26.5 | Server and worker. The Temporal SDK requires Go 1.26 |
| Node.js | 22.23.2 | Next.js frontend, Playwright, CDK |
| Docker | 29.4.2 | Local PostgreSQL and Redis |
| Temporal CLI | 1.8.2 (server 1.31.2) | Local Temporal dev server and UI |

### Go modules

| Name | Version | Purpose |
| --- | --- | --- |
| `go.temporal.io/sdk` | v1.49.0 | Workflow, activities, client, and the workflow test environment |
| `github.com/jackc/pgx/v5` | v5.11.0 | PostgreSQL driver and connection pool |
| `github.com/awslabs/aurora-dsql-connectors/go/pgx` | v0.5.0 | Builds the pgx pool for Aurora DSQL with automatic IAM tokens. Used only on AWS |
| `github.com/pressly/goose/v3` | v3.28.0 | SQL migrations |
| `github.com/redis/go-redis/v9` | v9.23.0 | Redis Streams publish and read |
| `github.com/coder/websocket` | v1.8.15 | WebSocket server, and client in E2E tests |
| `github.com/aws/aws-sdk-go-v2` | v1.47.1 | AWS SDK core |
| `github.com/aws/aws-sdk-go-v2/config` | v1.33.7 | AWS credential and region loading |
| `github.com/aws/aws-sdk-go-v2/service/s3` | v1.114.1 | S3 implementation of the object store |
| `golang.org/x/crypto` | v0.57.0 | Password hashing (argon2id) |
| `github.com/anthropics/anthropic-sdk-go` | v1.80.0 | First real implementation of the language-model adapter |
| `github.com/stretchr/testify` | v1.12.1 | Test assertions; also used by the Temporal test suite |

Deliberately not added: a web framework (the standard library router is enough), an ORM (plain SQL through pgx), and an ElevenLabs SDK (one HTTP request through the standard library).

### Frontend and infrastructure (npm)

| Name | Version | Purpose |
| --- | --- | --- |
| `next` | 16.4.0 | Web frontend |
| `react`, `react-dom` | 19.3.0 | Required by Next.js |
| `typescript` | 5.9.3 | Type checking. This is what `create-next-app` 16.4.0 installed (`^5`) |
| `tailwindcss` | 4.3.3 | Styling |
| `@playwright/test` | 1.64.0 | Browser E2E tests |
| `aws-cdk-lib` | 2.272.0 | Infrastructure as code for the AWS deployment |
| `aws-cdk` | 2.1145.0 | CDK command line |

### Containers and services

| Name | Version | Purpose |
| --- | --- | --- |
| `postgres` image | 17-alpine | Local database only |
| Aurora DSQL | — | Database on AWS (D032). PostgreSQL-compatible, no version to choose |
| `redis` image | 8-alpine | Local Redis. Confirm the matching ElastiCache engine at step 15 |
| Temporal Cloud | — | Hosted workflow service for the AWS deployment |
| Anthropic API | Model set by `LLM_MODEL` | Choice generation and tag enhancement |
| ElevenLabs API | `eleven_v4_turbo`, Text to Dialogue convert endpoint | Speech |

## 3. Build plan

Proposed repository layout:

```text
server/            Go module
  cmd/server/      HTTP and WebSocket process
  cmd/worker/      Temporal worker process
  internal/        auth, rooms, sessions, theater, workflow, activities, providers, objectstore, db
  migrations/      SQL migrations
tests/e2e/         Go E2E suite as its own module: harness, client, fixtures
web/               Next.js app and its Playwright tests
infra/             CDK app
docker-compose.yml PostgreSQL and Redis
Makefile           dev, test, e2e, migrate
```

### Step 1 — Project setup and development environment

**What to build**
- The Go module with `cmd/server` and `cmd/worker`, each starting, logging one structured line, and exposing a health check (the server over HTTP; the worker by connecting to Temporal).
- `docker-compose.yml` for PostgreSQL and Redis; `make dev` also starts the Temporal dev server.
- Configuration loading from environment variables, with `.env.example` committed.
- Migration tooling with an empty first migration; `make migrate`. From the start, each migration holds one schema statement and no data changes, so the same files run on Aurora DSQL.
- A database package that accepts a pgx pool from either source: a connection string locally, or the Aurora DSQL connector on AWS.

**Definition of done**
- `make dev` brings up PostgreSQL, Redis, Temporal, the server, and the worker.
- `GET /healthz` returns `200` and reports the database, Redis, and Temporal as reachable.
- The worker appears as a poller in the Temporal UI.
- `make test` runs and passes with zero tests.

### Step 2 — Spike: vote and timer ordering after a worker outage

**Done 2026-10-08.** Result: workflow code cannot tell which vote arrived before the timer after an outage. Remedy: the server stamps each vote with its receipt time (D033). Details in [the review](review.md), item 5.

**What to build**
- A throwaway workflow that starts a short timer and collects signals, and a script that stops the worker before the deadline, sends one signal before and one after it, then restarts the worker.

**Definition of done**
- The result is written into [the review](review.md), item 5: whether workflow code can tell which signal arrived before the timer.
- If it cannot, the chosen remedy is recorded in the decisions log before step 10 (the likely one: the server stamps each vote with its receipt time and the workflow compares it with the deadline).
- The spike code is deleted.

### Step 3 — E2E infrastructure

**Done 2026-10-08.** `make e2e` is red until step 4: the login test fails with `404`, as intended. The harness's own checks pass.

**What to build**
- `StartStack`, which launches the server and worker as child processes against the local containers.
- The client package with cookie jars and the WebSocket wrapper.
- The `ObjectStore` interface with the filesystem implementation.
- The fake language-model and speech providers, with scripting and failure injection.
- The first failing test: H1's login part.

**Definition of done**
- `make e2e` starts the stack, runs the suite, and tears it down.
- The login test fails because the endpoint does not exist, not because of the harness.

### Step 4 — Director login

**Done 2026-10-08.** `make e2e` is fully green. Added beyond the list below: `GET /api/v1/director/me` and the `COOKIE_SECURE` setting.

**What to build**
- Migrations for `directors` and `director_sessions`.
- Login and logout endpoints, argon2id hashing, the session cookie, throttling of failed attempts, and the middleware that yields a verified director ID.
- A small command to add a director.
- The write-transaction helper that retries on SQLSTATE `40001`, used by every write from here on, with a unit test that simulates a conflict.

**Definition of done**
- H1 (login part) and S1 pass.
- A request without a valid cookie to any director route returns `401`.

### Step 5 — Create a room

**Done 2026-10-08.** Added beyond the list below: `GET /api/v1/rooms` (the director's room list). Share links are derived from a server secret, not stored (D034).

**What to build**
- Migration for `rooms`.
- `POST /api/v1/rooms` with multipart parsing, validation bounds, portrait upload to the object store, and share-link token generation.
- `GET /api/v1/rooms/{roomId}` and the portrait endpoint, with ownership checks.

**Definition of done**
- H1, S2, and S3 (read part) pass.
- A failed creation leaves no room row.

### Step 6 — Audience sessions

**Done 2026-10-08.** Added beyond the list below: `audienceCount` in the director's room view. The simultaneous-joins test sends fifteen browsers at once and expects exactly ten admitted.

**What to build**
- Migration for `audience_sessions`.
- `POST /api/v1/audience-sessions`: create or restore, the per-room cookie, and the ten-session cap enforced by a conditional update of `rooms.audience_count` in the same transaction as the insert.
- Audience access to the room and portrait endpoints.

**Definition of done**
- H2 and S4 pass, including ten simultaneous joins followed by an eleventh.

### Step 7 — Director web pages

**Done 2026-10-08.** Added beyond the list below: a room list page, and three more browser tests (wrong password, not logged in, and two near-limit portraits through the proxy). The browser tests reuse the Go test stack through `tests/e2e/cmd/stack`.

**What to build**
- The Next.js app: login page, room creation form, and a room page showing the share link and a Start button (not yet wired).
- The Playwright setup and one test: log in, create a room, see the link.

**Definition of done**
- The Playwright test passes against the local stack.
- All Go E2E tests remain green.

### Step 8 — WebSocket connection and waiting snapshot

**Done 2026-10-09.** The pre-Start phase is named `waiting`. The origin rule is D035. Added beyond the list below: `Broadcast` on the connection map (unused until step 9, but it is what makes the map testable), closing every connection on server shutdown, and three browser tests showing the Next.js development proxy carries the WebSocket upgrade.

**What to build**
- `GET /api/v1/rooms/{roomId}/ws`: cookie authentication for director or audience, the `Origin` check, the in-memory map of room to connections, heartbeats, and the `snapshot` message for a room that has not started.

**Definition of done**
- H3 and S5 pass.

### Step 9 — Start and the eight-turn workflow, without votes

**Done 2026-10-09.** Updates carry the whole state (D036), and the premiere's clips carry their URLs (D037). Added beyond the list below: unit tests for the Start handler and the stream reader, and a fix to the test harness, whose `AddDirector` was not safe to call from parallel tests. Left as stand-ins for later steps: a finished room's snapshot only names its phase (step 12), a failed query closes the socket with `1013` (step 13), and an activity that fails past its retries fails the workflow and leaves the row `running` (step 11).

**What to build**
- Migration for `performances`; the Start endpoint (insert the row, start the workflow with a fixed ID).
- The workflow loop with fake providers: fetch context, then per turn publish `generating`, generate, start the timer, publish `voting`, wait, pick a winner at random, publish `preparing_line`, tag, and synthesise to the object store; finally save the script, mark completed, and publish `premiere`.
- The publish activity writing to the Redis stream with a revision; the server's stream reader forwarding to room connections.
- The `state` query handler, and the connect path that queries the workflow for a running performance.

**Definition of done**
- H4, S6, and S3 (start part) pass.
- A workflow test under Temporal's test environment covers the loop with mocked activities.
- A browser that connects mid-run receives a snapshot and then only higher revisions.

### Step 10 — Voting

**Done 2026-10-09.** Added beyond the list below: votes received before a round opens are ignored (an amendment to D033); a connection passes on one vote per turn; and an end-to-end test that stops the worker across a deadline to check D033 against the real workflow. Snapshots carry the voter's own vote as `yourVote`.

**What to build**
- The `vote` message, the vote signal carrying the voter ID, and `vote_received`.
- Vote bookkeeping in the workflow: first vote per voter, ignore unknown choices, wrong turns, and late votes. Lateness is decided by the receipt-time stamp against the deadline, and buffered votes are handled before the round closes (D033).
- The server stamping each vote with its receipt time before signalling.
- Winner selection by majority with the replay-safe random source for ties and empty rounds.
- The voter's own vote included in snapshots.

**Definition of done**
- H5, H6, and S7 pass.
- Unit tests cover majority, tie, and empty rounds.

### Step 11 — Real providers, fallbacks, and failure

**Done 2026-10-10, except the manual run with real providers**, which is waiting on a working Anthropic API key (the first attempt got `401 invalid x-api-key`). One rule changed: choice generation has no canned fallback; it is retried for a retry window and then the run fails (D038). So "the two fallbacks" below became one, for tagging. The default model is `claude-sonnet-5-5`. Added beyond the list: a `failure_reason` column, and `tagsApplied` in the script.

**What to build**
- The language-model adapter interface with the Anthropic implementation: prompts for choice generation and tag enhancement, validation of three distinct bounded lines, and the check that tagging preserved the words.
- The ElevenLabs implementation of the speech provider.
- The retry and timeout values from the system design, the two fallbacks, and the failure path that marks the row `failed` and publishes `failed`.

**Definition of done**
- S8 and S9 pass using the fakes' failure scripts.
- One manual run with real providers produces eight audible, tagged recordings. This is a checked-by-ear step and is not automated.

### Step 12 — Premiere and replay

**What to build**
- The audio endpoint with authorisation, the completed-only rule, and range requests.
- The completed and failed snapshots read from the performance row.

**Definition of done**
- H7 and S10 pass.

### Step 13 — Recovery

**What to build**
- The connect-time rule when a query fails: start a missing workflow, mark a closed one failed, otherwise report recovering.
- The `resync` message.
- Harness support for stopping and restarting the worker.

**Definition of done**
- H8, S11, and S12 pass.
- The worker-restart run is repeated by hand while watching the Temporal UI, as the demonstration rehearsal.

### Step 14 — Theatre web pages

**What to build**
- The audience page: Enter theatre (which unlocks audio), waiting state, the three choices with a countdown, the winning line and "preparing" state, reconnection with backoff, and the failure message.
- The premiere player: countdown, preloading, eight clips in order with the speaker's portrait highlighted, the **Tap to hear audio** fallback, and replay for a finished room.
- The director's room page wired to Start, with the same live view and voting.
- Playwright tests: a full performance with one director and two audience browsers using fake providers, and a replay of a finished room.

**Definition of done**
- Both Playwright tests pass.
- A complete performance is run by hand in Chrome and Safari with real providers.

### Step 15 — AWS deployment

**What to build**
- A CDK app: network, load balancer with HTTPS, three Fargate services (web, server, worker), an Aurora DSQL cluster, ElastiCache, the private encrypted S3 bucket with a seven-day lifecycle rule, and secrets for the provider keys and Temporal Cloud credentials.
- The S3 implementation of the object store and its smoke test against a real bucket.
- A Temporal Cloud namespace and the worker's connection to it.
- The Aurora DSQL connection path: the connector, a scoped database role mapped to the task roles, and the migrations applied to the cluster. If goose does not run unmodified against DSQL, apply the same SQL files with a small script.

Before starting this step, load the relevant AWS skills as the project's instructions require, including `aurora-dsql`, and confirm that Aurora DSQL is available in the chosen region and which Redis engine version ElastiCache offers.

**Definition of done**
- `cdk deploy` succeeds from a clean account state and `cdk diff` is empty afterwards.
- The Go E2E suite passes when pointed at the deployed URL with fake providers enabled in a test configuration.
- The simultaneous-joins part of S4 passes against Aurora DSQL with exactly ten admitted. This is the first time conflict retries run for real.
- A full performance with real providers and at least two remote browsers completes over the public URL.
- An estimate of the monthly running cost is recorded, with instructions for tearing the stack down between demos.

## 4. Points to settle along the way

These are items 11 to 16 in [the review](review.md), placed where they come up:

| Item | Settle in |
| --- | --- |
| Whether to show a "recovering" message | Steps 13 and 14 |
| What the page shows between rounds | Step 14 |
| Database cleanup | Step 15, or defer |
| Whether goose runs unmodified against Aurora DSQL | Step 15 |
| Piping audio from the object store | Step 12 |
| Who builds clip URLs | Settled in step 9: the clip list carries them (D037) |
| Caching of audio responses | Step 12 |
