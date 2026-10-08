# Monster Theatre

An audience votes on the dialogue for two characters, line by line; after eight turns everyone hears the performance they wrote.

- [Product brief](docs/monster-theatre-product-brief.md)
- [System design](docs/system-design.md)
- [Decisions](docs/decisions.md)
- [Implementation plan](docs/implementation-plan.md)

## Local development

Prerequisites: Go 1.26+, Docker, and the [Temporal CLI](https://docs.temporal.io/cli).

```sh
make dev
```

This starts PostgreSQL and Redis in containers, the Temporal dev server, applies migrations, and runs the server and the worker. Ctrl-C stops all of it.

| What | Where |
| --- | --- |
| Health check | http://localhost:8080/healthz |
| Temporal UI | http://localhost:8233 |
| PostgreSQL | `localhost:5432`, database `monstertheatre` |
| Redis | `localhost:6379` |

Other targets:

| Command | Does |
| --- | --- |
| `make test` | Runs the Go unit tests |
| `make e2e` | Runs the end-to-end tests against their own isolated stack |
| `make infra` / `make infra-down` | Starts or removes only PostgreSQL and Redis |
| `make migrate` | Applies migrations |
| `make director USERNAME=alice` | Creates a director account (prompts for the password) |
| `make server` / `make worker` | Runs one process on its own |

Configuration comes from environment variables, all with local defaults. See [.env.example](.env.example).

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

## End-to-end tests

```sh
make e2e
```

This needs Docker running and the Temporal CLI. It starts the real server and worker against real PostgreSQL, Redis, and Temporal, with fake stand-ins for the language model, speech, and S3, then tears everything down.

It is isolated from `make dev` (its own database, its own Temporal server, free ports), so both can run at once. If the stack cannot start you get a `HARNESS FAILURE` banner with log tails, which is different from a failing test.

## Layout

```text
server/cmd/server    HTTP and WebSocket process
server/cmd/worker    Temporal worker process
server/cmd/migrate   Applies SQL migrations
server/cmd/adddirector  Creates a director account
server/internal      Application packages
server/migrations    SQL migrations
tests/e2e            End-to-end tests: harness, client, fixtures
```

## Migration rules

The deployed database is Aurora DSQL, which restricts schema changes. Every migration follows these rules so the same files run locally and on AWS:

- one schema statement per file;
- never mix schema and data changes in one file;
- no column drops or type changes without recreating the table.
