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
| `make server` / `make worker` | Runs one process on its own |

Configuration comes from environment variables, all with local defaults. See [.env.example](.env.example).

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
server/internal      Application packages
server/migrations    SQL migrations
tests/e2e            End-to-end tests: harness, client, fixtures
```

## Migration rules

The deployed database is Aurora DSQL, which restricts schema changes. Every migration follows these rules so the same files run locally and on AWS:

- one schema statement per file;
- never mix schema and data changes in one file;
- no column drops or type changes without recreating the table.
