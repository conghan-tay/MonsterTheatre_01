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
| H7 | After completion, all eight clips can be fetched, including by range request, and a browser connecting later gets the premiere as its snapshot, exactly as it was published | 12 |
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
| `github.com/aws/aws-sdk-go-v2/feature/dsql/auth` | v1.2.1 | Brought in by the DSQL connector: signs the IAM database tokens |
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
| `tsx` | ^4 | Runs the CDK app and its template tests without a build step |

### Containers and services

| Name | Version | Purpose |
| --- | --- | --- |
| `postgres` image | 17-alpine | Local database only |
| Aurora DSQL | — | Database on AWS (D032). PostgreSQL-compatible, no version to choose |
| `valkey/valkey` image | 8-alpine (8.1.10) | Local stand-in for ElastiCache. Replaced `redis:8-alpine` at step 15: ElastiCache has Redis OSS only up to 7.1 (D051) |
| ElastiCache | Valkey 8.1, `cache.t4g.micro` | The two Redis streams on AWS. Checked with `DescribeCacheEngineVersions` in `ap-southeast-1` on 2026-10-12: Valkey 7.2 to 9.1 |
| CloudFront | — | The public HTTPS address, in front of an internal load balancer (D049) |
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

**Done 2026-10-10.** The manual run with real providers completed on the third attempt, after two rejected API keys: eight turns, every line tagged with its words unchanged, eight MP3 recordings of 11 to 17 seconds; listening to them is the user's check. One rule changed: choice generation has no canned fallback; it is retried for a retry window and then the run fails (D038). So "the two fallbacks" below became one, for tagging. The default model is `claude-sonnet-5-5`. Added beyond the list: a `failure_reason` column, `tagsApplied` in the script, and `MAX_LINE_LENGTH`, which makes the longest allowed line a setting (default 160, down from a fixed 300) after the first real run produced lines too long to read in a ten-second vote.

**What to build**
- The language-model adapter interface with the Anthropic implementation: prompts for choice generation and tag enhancement, validation of three distinct bounded lines, and the check that tagging preserved the words.
- The ElevenLabs implementation of the speech provider.
- The retry and timeout values from the system design, the two fallbacks, and the failure path that marks the row `failed` and publishes `failed`.

**Definition of done**
- S8 and S9 pass using the fakes' failure scripts.
- One manual run with real providers produces eight audible, tagged recordings. This is a checked-by-ear step and is not automated.

### Step 12 — Premiere and replay

**Done 2026-10-11.** Two things differ from the list below. A completed room's snapshot is not a separate "completed" state: it is the `premiere` state exactly as published, at revision 25, rebuilt from the row, and the `completed` phase has left the WebSocket contract (D039). For that the premiere's start time is now saved on the row: the workflow fixes it before the save. The column was added to the `performances` create-table migration (`00006`) instead of a new migration, since only local test data existed; a database migrated before this step must drop and recreate that table (README, "Migration rules"). Range requests are answered by `http.ServeContent` over an object body that can seek (D040). Review items 14 and 16 are decided. A failed room's snapshot carries the reason but no turn or lines, and is at revision 0. Not checked: playback in a real audio element, in any browser.

**What to build**
- The audio endpoint with authorisation, the completed-only rule, and range requests.
- The completed and failed snapshots read from the performance row.

**Definition of done**
- H7 and S10 pass.

### Step 13 — Recovery

**Done 2026-10-11.** One rule below was reversed: a missing workflow is **not** started when a browser connects. It is reported as `recovering` and only Start repairs it (D043). "Recovering" is a snapshot phase on a connection that is kept, and the browser asks again with `resync` (D041). A closed workflow is found by sending the query on condition that the workflow is open, in one call, because a closed workflow otherwise answers with its last state; the row is then marked failed and the whole room is told (D042). `resync` is limited to one a second per connection. The harness could already stop and start the worker (step 10); it gained helpers to write a row with no workflow and to terminate a workflow. H8 passed before any change, as it should: Temporal does that work. The rehearsal by hand in the Temporal UI is the user's, with steps in the pull request's review guide.

**What to build**
- The connect-time rule when a query fails: start a missing workflow, mark a closed one failed, otherwise report recovering.
- The `resync` message.
- Harness support for stopping and restarting the worker.

**Definition of done**
- H8, S11, and S12 pass.
- The worker-restart run is repeated by hand while watching the Temporal UI, as the demonstration rehearsal.

### Step 14 — Theatre web pages

**Done 2026-10-11**, except the run by hand in Chrome and Safari, which is the user's; they made it on 2026-10-13 on the AWS deployment, also on an Android phone, and the premiere played with sound in all three (step 15). Built as listed, web-only, with one harness addition: the test stack takes a script directory so a browser test can slow a turn. The audience page lives at the share link and always opens on Enter theatre; sound goes through one audio element started inside that click; clips are fetched whole during the countdown (D044). Three Playwright tests where two were listed: a full performance, replay (by a newcomer, after a reload, and on the director's page), and an invalid link. One real-provider performance was driven through the pages in Playwright's Chromium and completed: lines of 101 to 152 characters, about 3 s to write each turn's lines and 3 to 4 s to tag and voice the winner, eight clips of 9 to 11 s, a premiere of about 85 s. Not checked: what it sounds like, Safari, any browser driven by a person, the Tap to hear audio path in a browser that really blocks sound, and `next build`.

**What to build**
- On a `recovering` snapshot, show that the performance is recovering and send `resync` every few seconds (no more than one a second is answered) until the phase changes.
- The audience page: Enter theatre (which unlocks audio), waiting state, the three choices with a countdown, the winning line and "preparing" state, reconnection with backoff, and the failure message.
- The premiere player: countdown, preloading, eight clips in order with the speaker's portrait highlighted, the **Tap to hear audio** fallback, and replay for a finished room. A finished room arrives as the `premiere` state with a `startsAt` that has passed (D039); there is no separate phase for it.
- The director's room page wired to Start, with the same live view and voting.
- Playwright tests: a full performance with one director and two audience browsers using fake providers, and a replay of a finished room.

**Definition of done**
- Both Playwright tests pass.
- A complete performance is run by hand in Chrome and Safari with real providers.

### After step 14 — page changes asked for by the user

**Done 2026-10-11.** Not one of the fifteen steps; four changes in one pull request.

- **Nicknames.** The join page requires one (1 to 30 characters). It is stored on the audience session, in a column added to that table's original migration, and shown to the director only. Entering again as the same browser replaces it (D045).
- **A live audience list.** The director's page shows the count and the names as people join, from a new WebSocket message sent to the director's connections only (D045).
- **Script order.** Newest line first while the turns run; only the line being spoken while a recording plays; the whole script in speaking order otherwise.
- **Character counts.** Every text field with a limit shows how much of it is used.

Tests added: three Go end-to-end tests and one browser test, a pure test of the script rule, and more assertions in the full-performance browser test.

### After step 14 — room messages through Redis

**Done 2026-10-11.** Not one of the fifteen steps. The two messages a server originates, the `audience` message and the failed snapshot for a dead run, now reach browsers through a second Redis stream, `room-notices`, read by every server, so a room's director and audience can be connected to different server instances (D046). One server is still deployed. No page, message, schema, or workflow changed. The test stack runs two servers, and three end-to-end tests put a room's people on both.

### Step 15 — AWS deployment

**Done 2026-10-13.** Built and deployed in test mode on 2026-10-12, and in live mode on 2026-10-13. Every item of the definition of done below is met, with the changes from its text listed here. What was decided on the way is D047 to D052.

- **Built and tested locally:** the S3 object store with its seekable body; the DSQL connection path; `migrate` for DSQL (goose's DSQL dialect, `CREATE INDEX` written as `ASYNC`, a wait for index builds) and `migrate grant`; session hashes as text, because DSQL cannot index `bytea`; Temporal Cloud sign-in by API key; `/livez`; both container images; the CDK app (two stacks, 15 template tests); a mode of the Go end-to-end suite that runs against a deployment; the browser tests against `next build` and `next start`, which had never been run.
- **Changed from the text below:** there is no domain, so HTTPS is CloudFront's, in front of an internal load balancer (D049); the tasks have public addresses and there is no NAT gateway (D050); the cache is Valkey, locally too (D051); against a deployment 32 of the suite's 46 top-level tests run and 14 skip, each saying why.
- **Done on AWS, 2026-10-12:** `cdk bootstrap`; `MonsterTheatreData` deployed; all nine migrations applied to the cluster with both indexes valid; the application's database role created and granted; the S3 smoke test passed against the bucket.
- **Also done on AWS, 2026-10-12:** `MonsterTheatreApp` deployed in test mode. The worker connected to Temporal Cloud; server and worker connect to DSQL as `monster_app`. The first deployment answered 504 because of a wrong security-group rule, since corrected (D049). `cdk diff` is empty.
- **The end-to-end suite against the deployment, 2026-10-12:** 32 top-level tests passed and 14 skipped, with no failure and no change needed to a test or to the product. It covers login, rooms and portraits through S3, joining, the WebSocket through CloudFront, voting, full performances on Temporal Cloud, and the premiere's recordings. The simultaneous-joins part of S4 admitted exactly ten of fifteen in six runs out of six; the cluster's own metric counted 108 transaction conflicts in the five minutes that included those runs, so the retries in `db.WithTx` ran for real.
- **Torn down 2026-10-12, at the user's request:** `cdk destroy MonsterTheatreApp` completed in about 20 minutes and left nothing billed by the hour: no cache, load balancer, tasks, CloudFront distribution, or VPC. The data stack, the cluster's rows and grants, the secrets, and the bucket are untouched.
- **The live run, 2026-10-13:** the application stack was deployed again with `-c mode=live` (the worker's startup line read first: `anthropic`, `claude-sonnet-5-5`; `elevenlabs`, `eleven_v4_turbo`; 10-second votes). The user then ran one performance over the CloudFront address as director with three audience members: Chrome 155 and Safari 26.5 on a MacBook Air, and Chrome 154 on a Samsung Galaxy S22+. They report that all eight turns completed and the premiere played with sound on every device, with nothing looking wrong. From the deployment's side: the performance row is `completed`, 2 minutes 16 seconds after Start; all eight turns were written by `claude-sonnet-5-5` with tags applied; eight recordings of 133 to 198 kB are in the bucket; the server and worker logs hold no warning or error. This was the first run of the pages in Safari and on a phone.
- **Not shown by any of this:** Firefox; more than one server behind the load balancer; any load; the 14 end-to-end tests that skip against a deployment; an outage of Temporal Cloud or the cache on AWS; a WebSocket held open through CloudFront for longer than one performance.

One server instance is deployed, but nothing on the live path requires that: the load balancer needs no sticky routing for WebSockets (D046). Before a second instance is added, move the failed-login lock out of server memory (system design, section 9).

**What to build**
- A CDK app: network, load balancer with HTTPS, three Fargate services (web, server, worker), an Aurora DSQL cluster, ElastiCache, the private encrypted S3 bucket with a seven-day lifecycle rule, and secrets for the provider keys and Temporal Cloud credentials.
- The S3 implementation of the object store and its smoke test against a real bucket.
- A Temporal Cloud namespace and the worker's connection to it.
- The two index migrations, `00008` and `00009`, are plain `CREATE INDEX`. DSQL is documented as needing `CREATE INDEX ASYNC`, so these two statements must be issued in that form there, by the script if goose cannot. Confirm the syntax against the DSQL documentation first; it has not been checked.
- The Aurora DSQL connection path: the connector, a scoped database role mapped to the task roles, and the migrations applied to the cluster. If goose does not run unmodified against DSQL, apply the same SQL files with a small script.

Before starting this step, load the relevant AWS skills as the project's instructions require, including `aurora-dsql`, and confirm that Aurora DSQL is available in the chosen region and which Redis engine version ElastiCache offers.

**Definition of done**
- `cdk deploy` succeeds from a clean account state and `cdk diff` is empty afterwards.
- The Go E2E suite passes when pointed at the deployed URL with fake providers enabled in a test configuration.
- The simultaneous-joins part of S4 passes against Aurora DSQL with exactly ten admitted. This is the first time conflict retries run for real.
- A full performance with real providers and at least two remote browsers completes over the public URL.
- An estimate of the monthly running cost is recorded, with instructions for tearing the stack down between demos.

### After step 15 — operating the deployment

**Done 2026-10-13.** Not one of the fifteen steps. The deployment had been made by hand; this makes its common jobs one command each, so that a person or an assistant can do them quickly and the same way every time.

- **`scripts/aws.sh`:** `status`, `deploy-data`, `deploy-app test|live`, `verify`, `e2e`, `cycle`, `clear-data`, `destroy-app`, `destroy-data`, and a few for looking (`logs`, `counts`, `query`, `leftovers`). It reads what it needs from the stacks' outputs, refuses the root user, refuses the end-to-end suite against a live deployment, and never reads a secret's value. 33 tests with stand-in commands (`make aws-test`).
- **`server/cmd/dbadmin`:** `counts`; `clear` in three scopes (what the test suites left; everything but directors; everything), a dry run unless told; and `query`, one read-only statement. The object stores gained a way to delete by prefix, used only by this command.
- **A project skill,** `.agents/skills/monster-theatre-aws/`, with the commands, their cost and duration, when to ask first, and what has gone wrong before.
- **One change to the deployment:** the data stack now exports the two provider keys' names in both modes, so a running stack can be switched from live back to test in place.
- **Run for real:** the in-place cycle (test, the suite at 32 passed and 20 skipped, clearing its leftovers, live) in under seven minutes, and the teardown. The first real runs found two faults in the script that its stand-in tests could not, both since fixed with a test. `destroy-data`, clearing `rooms` or `all` on the cluster, and a cycle with a teardown in between have not been run on AWS.

### After step 15 — tagging and speech in the background

**Done 2026-10-18.** Not one of the fifteen steps. Asked for by the user after a live run in which tagging and speech took more than 3.6 s per turn (review item 12). Decision: D053.

- **Child workflow `PrepareLine`** (`server/internal/workflow/prepare.go`), one per turn, ID `performance/{performanceId}/line-{turn}`. It tags and voices the winning line, with the same activity options and the same fallback for tagging. The parent starts it and goes on to the next turn. After turn 8 it waits for all eight, then saves the script and publishes the premiere. Registered on the worker.
- **A line that cannot be voiced stops the run at once,** in whatever turn it has reached, as `speech_failed`.
- **The page keeps a round's result on screen** while the next turn's lines are written (`nextResult` in `web/src/lib/theatre.ts`, applied in `useRoomSocket`).
- **Tests:** four new workflow tests: the next round opens while a line is still being voiced; the premiere waits, and everything is saved in turn order; a child per turn; speech failing on turn 8. One changed: speech failing on turn 2 now stops in turn 3. One new E2E test, turn 2's round opens before turn 1's line is stored, and four browser tests of `nextResult`. Four existing tests changed because they relied on speech holding the parent: speech failure, connecting while the worker is down, refusing audio mid-run, and the browser test of the winner view.
- **Not measured:** the time saved with the real providers. No paid run was made.

### After step 15 — the speaker takes the stage

**Done 2026-10-18.** Not one of the fifteen steps. Asked for by the user. Decision: D054. Web only.

- **`web/src/components/Stage.tsx`:** while a premiere recording plays, a near-black layer over the whole window shows the speaker's portrait, as large as it fits at its own shape, and the line beneath it. It is drawn into the body through a portal; `Theatre` renders it from `usePremiere`'s playing clip, and `stageLine` (`web/src/lib/script.ts`) finds the line.
- **Tests:** two rule tests for `stageLine`. One browser test holds back turns 2 and 3's recordings, so that turn 1, then turn 2, stays on stage. It checks, at 1280×720 and 390×844:
  - The layer is the whole window.
  - The one image is the right character's, at its own shape, and most of the window.
  - The line sits just beneath it, and the text is only the line.
  - It goes away when playback ends.

  The browser tests' portraits are now 20×30 and 30×20 instead of 1×1, so that the shape is tested.
- **Not checked:** Safari, Firefox, real phones, and real portraits.

### After step 15 — lip sync with WaveSpeed InfiniteTalk

**Done 2026-10-18.** Not one of the fifteen steps. Asked for by the user. Decisions: D055, D056.

- **Start:** an **Include lip-sync video** checkbox, unticked by default. It is sent as `{"lipSync": true}`, and `{}` means no lip sync, exactly as before.
- **Child workflow:** after speech, three activities run: `UploadLipSyncInputs` (retried), `SubmitLipSync` (one attempt), and `AwaitLipSync`. The last polls every 20 s with a heartbeat each time, for at most 15 minutes, then stores the video. A line whose video fails keeps its portrait.
- **Before the premiere:** a `preparing_premiere` phase with `progress`, and turn 8's result kept on the page. The premiere's revision is saved with the script.
- **Server:** clips carry `videoUrl`, and `/turns/{n}/video` serves the video. A lip-sync run's timeout is 45 minutes.
- **Providers:** `providers/wavespeed` is the client; `providers/fake` gives a one-second stand-in clip, which the fake script can slow or fail. The new settings are `LIPSYNC_PROVIDER`, `LIPSYNC_RESOLUTION`, and `LIPSYNC_POLL_INTERVAL`.
- **Page:** the stage plays a muted video alongside the recording.
- **AWS (code only, nothing deployed):** a fifth secret, `monster-theatre/wavespeed-api-key`, which live mode passes to the worker with WaveSpeed selected. `deploy-app live` refuses while it is a placeholder.
- **Checked once against WaveSpeed** (one 480p call, approved by the user): upload, submit, poll, and download all worked with the client as written. An 8.64 s line took 123 s to generate and came back 640×608 and 0.12 s shorter than its recording. Not checked: a real failure, rate limits, and the billed amount.

### After step 15 — prompt caching and prompt-injection hardening

**Done 2026-10-19.** Not one of the fifteen steps. Asked for by the user, taking two parts of an earlier plan (`docs/ai-features-plan.md`): prompt caching with a simple cost log, and injection hardening with no extra model calls. Decisions: D057, D058.

- **Prompt caching:** the choices call marks the system prompt and the room's context (scene and characters) for the cache. The dialogue and turn instruction follow, uncached. `LLM_PROMPT_CACHING` turns it off; any value other than true or false stops the worker. The tags call is too short to cache.
- **Cost log:** one worker line per Claude call (`claude call`), logged before the answer is judged, with tokens, cost, cost without caching, and the room's running totals. Prices are in `server/internal/providers/anthropic/cost.go`.
- **Injection hardening:** `<` and `>` in text from people are escaped inside the prompts' tags, and both system prompts say tagged text is story material, never instructions.
- **Tests:** 20 new: the adapter (markers, a byte-identical prefix across turns, the same text with caching off, log fields and cost, failed answers logged, room totals and pruning, unpriced models, escaping, prompt wording) and the worker (the setting and how it reaches the adapter).
- **A live injection eval,** added 2026-10-20 after a review pointed out the hardening was unmeasured: `make injection-eval`, paid and run only by hand. It has 13 attacks and a control, 3 runs each, with the choices attacks also run on the pre-hardening prompt, and fails if an attack works more than once in three. Free tests check its grader, pass rule, skip, baseline prompt, and a dry run against a stand-in.
- **Measured once** (six real Sonnet 5.5 choices calls, approved by the user): turn 1 wrote 1,232 tokens to the cache, and turns 2 and 3 read them. Three turns cost $0.0090 with caching against $0.0132 without; over eight turns that is about 1.6 US cents saved per performance. No full performance was run.

## 4. Points to settle along the way

These are items 11 to 16 in [the review](review.md), placed where they come up:

| Item | Settle in |
| --- | --- |
| Whether to show a "recovering" message | Settled: a `recovering` snapshot (D041, step 13), shown by the page as a line of text (step 14) |
| What the page shows between rounds | Settled in step 14: the winning line with its counts, then "Writing the next lines…" |
| Database cleanup | Deferred at step 15; still open |
| Whether goose runs unmodified against Aurora DSQL | Settled at step 15 (D048): it does, through its DSQL dialect, with index statements rewritten as `ASYNC`. Run on the cluster 2026-10-12 |
| Piping audio from the object store | Settled in step 12: piped (D040) |
| Who builds clip URLs | Settled in step 9: the clip list carries them (D037) |
| Caching of audio responses | Settled in step 12: private, seven days, immutable |
