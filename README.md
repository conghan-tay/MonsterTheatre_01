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

`make dev` serves it at http://localhost:3000. A director logs in, creates a room from one form, and gets the room's page with its share link. Create an account first (next section).

The browser only ever talks to port 3000: the web app proxies `/api/*` to the Go server, which is the routing a load balancer does in production. Opening a share link does not work yet; the audience page arrives in a later step.

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

The share link is `{PUBLIC_BASE_URL}/join/{roomId}/{token}`. Until the web pages exist, join by posting those two values:

```sh
curl -i -c audience.txt -H 'Content-Type: application/json' \
  -d '{"roomId":"ROOM_ID","token":"TOKEN"}' localhost:8080/api/v1/audience-sessions
curl -b audience.txt localhost:8080/api/v1/rooms/ROOM_ID
```

A room admits ten audience sessions and never frees a slot; the eleventh gets `409`. Posting again with the same cookie returns the same session with `"restored": true`.

Share links are derived from `SHARE_LINK_SECRET`, not stored. `make dev` keeps a local secret in `.data/share-link-secret`; deleting that file invalidates every link made with it. `make server` on its own needs the variable set.

## The room WebSocket

`GET /api/v1/rooms/{roomId}/ws` upgrades to a WebSocket for the room's director or a member of its audience. Its first message is always a snapshot. Before the director starts the performance that is:

```json
{"type":"snapshot","revision":0,"state":{"phase":"waiting","turn":0}}
```

No page opens it yet. To try it, log in at http://localhost:3000, open a room, and run this in the browser console on that page:

```js
new WebSocket(`ws://${location.host}/api/v1/rooms/${location.pathname.split("/").pop()}/ws`).onmessage = (e) => console.log(e.data)
```

The handshake is accepted only from a page on `PUBLIC_BASE_URL` or on the server's own address; anything else, including a request with no `Origin` header, gets `403`. The server pings every 25 seconds and drops a connection that does not answer within 10.

## Running a performance

The Start button on the room page is not wired up yet, so start one with curl, logged in as the room's director (see "Director accounts" for `cookies.txt`):

```sh
curl -b cookies.txt -X POST -H 'Content-Type: application/json' -d '{}' \
  localhost:8080/api/v1/rooms/ROOM_ID/performance
```

The response is `{"performanceId": "...", "roomId": "...", "status": "running"}`, with `201` the first time and `200` after that: a room has one performance, and pressing Start again returns it.

The worker then runs eight turns. Each has a ten-second voting window; nobody can vote yet, so each winner is picked at random and a run takes about 80 seconds. An open room WebSocket (previous section) receives 25 updates: `generating`, `voting`, and `preparing_line` for each turn, then `premiere`. The run is visible in the Temporal UI at http://localhost:8233 as `performance/<performanceId>`, and the eight recordings land in `.data/objects/performances/<performanceId>/`.

The language model and speech service are stand-ins until real ones are added: lines read "Turn 1, first option." and the recordings are silent. Settings, all optional:

| Variable | Default | Meaning |
| --- | --- | --- |
| `VOTE_WINDOW` | `10s` | How long each round stays open |
| `PREMIERE_COUNTDOWN` | `5s` | How far ahead the premiere's start time is set |
| `LLM_PROVIDER`, `TTS_PROVIDER` | `fake` | Only `fake` exists so far |
| `FAKE_SCRIPT_DIR` | unset | A directory of `<roomId>.json` scripts for the stand-ins |

## End-to-end tests

```sh
make e2e
```

This needs Docker running and the Temporal CLI. It starts the real server and worker against real PostgreSQL, Redis, and Temporal, with fake stand-ins for the language model, speech, and S3, then tears everything down.

`make web-e2e` runs the browser tests the same way: Playwright starts that stack, starts the web app pointed at it, and stops both. The first run downloads Chromium (about 100 MB).

Both are isolated from `make dev` (their own database, their own Temporal server, their own ports), so they can run while it is up. If the stack cannot start you get a `HARNESS FAILURE` banner with log tails, which is different from a failing test.

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
