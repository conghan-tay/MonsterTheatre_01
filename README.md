# Monster Theatre

An audience votes on the dialogue for two characters, line by line; after eight turns everyone hears the performance they wrote.

- [Product brief](docs/monster-theatre-product-brief.md)
- [System design](docs/system-design.md)
- [Decisions](docs/decisions.md)
- [Implementation plan](docs/implementation-plan.md)

## Local development

Prerequisites: Go 1.26+, Node.js 22+, Docker, and the [Temporal CLI](https://docs.temporal.io/cli).

```sh
make dev
```

This starts PostgreSQL and Redis in containers, the Temporal dev server, applies migrations, and runs the server, the worker, and the web app. Ctrl-C stops all of it. The first run installs the web app's packages.

| What | Where |
| --- | --- |
| Web app | http://localhost:3000 |
| Health check | http://localhost:8080/healthz |
| Temporal UI | http://localhost:8233 |
| PostgreSQL | `localhost:5432`, database `monstertheatre` |
| Redis | `localhost:6379` |

Other targets:

| Command | Does |
| --- | --- |
| `make test` | Runs the Go unit tests |
| `make e2e` | Runs the end-to-end tests against their own isolated stack |
| `make test-verbose` / `make e2e-verbose` | The same, listing every test with its result and ending with a count of tests run, passed, failed, and skipped |
| `make infra` / `make infra-down` | Starts or removes only PostgreSQL and Redis |
| `make migrate` | Applies migrations |
| `make director USERNAME=alice` | Creates a director account (prompts for the password) |
| `make web-e2e` | Runs the browser tests against their own isolated stack and web app |
| `make web-check` | Lints and type-checks the web app |
| `make server` / `make worker` / `make web` | Runs one process on its own |

Configuration comes from environment variables, all with local defaults. See [.env.example](.env.example).

## The web app

`make dev` serves it at http://localhost:3000. A director logs in, creates a room from one form, and gets the room's page with its share link and a Start button. Anyone who opens the share link gets the audience page. The director's page lists who has joined, by nickname, as they join. Both show the performance live: the three lines to vote on with a countdown, the winning line, and at the end the premiere, which plays the eight recordings in order. Create an account first (next section).

The browser only ever talks to port 3000: the web app proxies `/api/*` to the Go server, which is the routing a load balancer does in production.

## Director accounts

There is no signup. With the stack running, create an account from another terminal:

```sh
make director USERNAME=alice
```

It prompts for a password of at least 8 characters (or reads `DIRECTOR_PASSWORD`). Then log in:

```sh
curl -i -c cookies.txt -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"..."}' localhost:8080/api/v1/director/login
curl -b cookies.txt localhost:8080/api/v1/director/me
```

Five failed logins for a username within 15 minutes lock that username until the 15 minutes have passed. The count is held in the server's memory, so restarting the server clears it.

## Creating a room

Logged in as a director, submit the scene, both characters, and two portraits (JPEG, PNG, or WebP, up to 5 MiB each) in one request:

```sh
curl -b cookies.txt \
  -F 'room={"scene":{"topic":"Two monsters argue about a biscuit","genre":"comedy"},
            "characterA":{"name":"Grumble","brief":"A tired swamp monster."},
            "characterB":{"name":"Fizz","brief":"An excitable imp."}};type=application/json' \
  -F portraitA=@grumble.png -F portraitB=@fizz.png \
  localhost:8080/api/v1/rooms
```

The response carries the room ID and the share link for the audience. `GET /api/v1/rooms` lists your rooms, and `GET /api/v1/rooms/{roomId}` shows one, including its share link.

## Joining as audience

The share link is `{PUBLIC_BASE_URL}/join/{roomId}/{token}`. Opening it asks for a nickname, which the director will see, and shows an **Enter theatre** button. The click joins the room, and is also what lets that page play sound later, so the button is there after a reload too; entering again as the same browser uses no extra seat. To try a second audience member on one machine, open the link in a private window or another browser.

The same thing with curl:

```sh
curl -i -c audience.txt -H 'Content-Type: application/json' \
  -d '{"roomId":"ROOM_ID","token":"TOKEN","nickname":"Ana"}' localhost:8080/api/v1/audience-sessions
curl -b audience.txt localhost:8080/api/v1/rooms/ROOM_ID
```

A room admits ten audience sessions and never frees a slot; the eleventh gets `409`. Posting again with the same cookie returns the same session with `"restored": true`.

Share links are derived from `SHARE_LINK_SECRET`, not stored. `make dev` keeps a local secret in `.data/share-link-secret`; deleting that file invalidates every link made with it. `make server` on its own needs the variable set.

## The room WebSocket

`GET /api/v1/rooms/{roomId}/ws` upgrades to a WebSocket for the room's director or a member of its audience. Its first message is always a snapshot. Before the director starts the performance that is:

```json
{"type":"snapshot","revision":0,"state":{"phase":"waiting","turn":0}}
```

The theatre pages open it themselves. To watch it by hand, log in at http://localhost:3000, open a room, and run this in the browser console on that page:

```js
new WebSocket(`ws://${location.host}/api/v1/rooms/${location.pathname.split("/").pop()}/ws`).onmessage = (e) => console.log(e.data)
```

The handshake is accepted only from a page on `PUBLIC_BASE_URL` or on the server's own address; anything else, including a request with no `Origin` header, gets `403`. The server pings every 25 seconds and drops a connection that does not answer within 10.

## Running a performance

Press **Start performance** on the room page. Everything after that is automatic, and the page becomes the live view: the director votes like everyone else. The rest of this section is the same thing by hand, which is useful for seeing what the pages do underneath. To start one with curl, logged in as the room's director (see "Director accounts" for `cookies.txt`):

```sh
curl -b cookies.txt -X POST -H 'Content-Type: application/json' -d '{}' \
  localhost:8080/api/v1/rooms/ROOM_ID/performance
```

The response is `{"performanceId": "...", "roomId": "...", "status": "running"}`, with `201` the first time and `200` after that: a room has one performance, and pressing Start again returns it.

The worker then runs eight turns. Each has a ten-second voting window, so a run takes about 80 seconds. The choice with the most votes wins; a tie, or a round nobody voted in, is settled at random. An open room WebSocket (previous section) receives 25 updates: `generating`, `voting`, and `preparing_line` for each turn, then `premiere`. The run is visible in the Temporal UI at http://localhost:8233 as `performance/<performanceId>`, and the eight recordings land in `.data/objects/performances/<performanceId>/`.

To vote, send a message on the room WebSocket while a round is open. From the browser console, with `ws` connected as in the previous section:

```js
ws.send(JSON.stringify({type: "vote", turn: 1, choiceId: "c2"}))
```

The reply is `{"type":"vote_received","turn":1,"choiceId":"c2"}`. The director and each audience member get one vote per turn, and the first one stands. The result arrives in that turn's `preparing_line` update as `winner`, with each choice's count and `decidedBy` (`majority`, `tie`, or `no_votes`).

If a message was missed, or just to check, ask for a fresh snapshot: `ws.send(JSON.stringify({type: "resync"}))`. The reply is a `snapshot` with the current state and your own vote. One a second is answered.

The run survives the worker. Stop the worker mid-run and the browsers already connected see updates pause; a browser that connects meanwhile gets a snapshot with phase `recovering`. Start the worker again and the run carries on from where it was, with votes cast in the meantime judged by when they were received. With `make dev` running, `pkill -f bin/worker` stops it, and `./bin/worker` from the repository root starts it again with the default settings; if your `.env` changes any the worker reads, export the same ones first.

Once the run has completed, each recording can be fetched by the room's director or an audience member, at the URL its clip carries in the `premiere` update:

```sh
curl -b cookies.txt -o turn-1.mp3 localhost:8080/api/v1/performances/PERFORMANCE_ID/turns/1/audio
curl -b cookies.txt -H 'Range: bytes=0-99' -o first-100-bytes localhost:8080/api/v1/performances/PERFORMANCE_ID/turns/1/audio
```

Before that the answer is `409`: nothing is heard before the premiere. A room WebSocket opened after the run gets the same `premiere` state as its snapshot, at revision 25, read from the database; its `startsAt` is then in the past, which is how a page knows to offer the recording from the beginning. The pages then offer **Play performance** instead of a countdown.

By default the language model and speech service are stand-ins: lines read "Turn 1, first option." and the recordings are silent. Nothing is spent unless you ask for the real ones.

### With real providers

Claude writes the lines and the audio tags, and ElevenLabs voices them. Put these in `.env` (never committed):

```sh
ANTHROPIC_API_KEY=...
ELEVENLABS_API_KEY=...
VOICE_ID_A=...   # ElevenLabs voice for character A
VOICE_ID_B=...   # and for character B; use two different voices
```

Then start the stack with the real providers selected:

```sh
LLM_PROVIDER=anthropic TTS_PROVIDER=elevenlabs make dev
```

The worker's `worker started` log line shows which providers it is using, and it refuses to start if a key is missing. A room records its two voice IDs when it is created, so set them before creating the room. Each performance makes 16 calls to Claude and 8 to ElevenLabs.

If a provider keeps failing, the performance fails and browsers are told why: the lines are retried for two minutes, speech three times. Only the audio tags have a fallback, which is to speak the line without them.

### Settings

All optional, read by the worker:

| Variable | Default | Meaning |
| --- | --- | --- |
| `VOTE_WINDOW` | `10s` | How long each round stays open |
| `PREMIERE_COUNTDOWN` | `5s` | How far ahead the premiere's start time is set |
| `LLM_PROVIDER` | `fake` | `fake` or `anthropic` |
| `LLM_MODEL` | `claude-sonnet-5-5` | The Claude model, when `LLM_PROVIDER` is `anthropic` |
| `TTS_PROVIDER` | `fake` | `fake` or `elevenlabs` |
| `TTS_MODEL` | `eleven_v4_turbo` | The ElevenLabs model, when `TTS_PROVIDER` is `elevenlabs` |
| `CHOICES_RETRY_WINDOW` | `2m` | How long writing a turn's lines is retried before the run fails |
| `MAX_LINE_LENGTH` | `160` | The longest a generated line may be, in characters. May be set from 60 to 300. About 13 characters are spoken per second, so 160 is roughly 12 seconds a line |
| `FAKE_SCRIPT_DIR` | unset | A directory of `<roomId>.json` scripts for the stand-ins |

## End-to-end tests

```sh
make e2e
```

This needs Docker running and the Temporal CLI. It starts the real server and worker against real PostgreSQL, Redis, and Temporal, with fake stand-ins for the language model, speech, and S3, then tears everything down.

`make web-e2e` runs the browser tests the same way: Playwright starts that stack, starts the web app pointed at it, and stops both. The first run downloads Chromium (about 100 MB).

Both are isolated from `make dev` (their own database, their own Temporal server, their own ports). `make e2e` can run while it is up. `make web-e2e` cannot: Next.js refuses to start a second development server from the same directory, so stop `make dev` first. If the stack cannot start you get a `HARNESS FAILURE` banner with log tails, which is different from a failing test.

## Layout

```text
server/cmd/server    HTTP and WebSocket process
server/cmd/worker    Temporal worker process
server/cmd/migrate   Applies SQL migrations
server/cmd/adddirector  Creates a director account
server/internal      Application packages
server/migrations    SQL migrations
tests/e2e            End-to-end tests: harness, client, fixtures
web                  Next.js web app and its Playwright tests (web/e2e)
```

## Migration rules

The deployed database is Aurora DSQL, which restricts schema changes. Every migration follows these rules so the same files run locally and on AWS:

- one schema statement per file;
- never mix schema and data changes in one file;
- no column drops or type changes without recreating the table.

One exception was made while every database was still a local test one: step 12 added `premiere_starts_at` to the `performances` create-table migration (`00006`) instead of adding a migration. Goose does not re-run a migration it has recorded, so a database migrated before that needs the table recreated, which deletes its performance rows:

```sh
cd server && go run ./cmd/migrate down && go run ./cmd/migrate down && go run ./cmd/migrate up
```

The same was done once more after step 14, for `nickname` on `audience_sessions` (`00005`); a database migrated before that needs three `down`s, not two, and loses its audience sessions as well.

For the end-to-end tests' database, drop it and let the next run recreate it: `docker compose exec postgres dropdb -U monster monstertheatre_e2e`.
