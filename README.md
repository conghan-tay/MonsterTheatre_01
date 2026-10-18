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

This starts PostgreSQL and Valkey (which speaks the Redis protocol, and is what runs on AWS) in containers, the Temporal dev server, applies migrations, and runs the server, the worker, and the web app. Ctrl-C stops all of it. The first run installs the web app's packages.

| What | Where |
| --- | --- |
| Web app | http://localhost:3000 |
| Health check | http://localhost:8080/healthz |
| Temporal UI | http://localhost:8233 |
| PostgreSQL | `localhost:5432`, database `monstertheatre` |
| Valkey (Redis protocol) | `localhost:6379` |

Other targets:

| Command | Does |
| --- | --- |
| `make test` | Runs the Go unit tests |
| `make e2e` | Runs the end-to-end tests against their own isolated stack |
| `make test-verbose` / `make e2e-verbose` | The same, listing every test with its result and ending with a count of tests run, passed, failed, and skipped |
| `make infra` / `make infra-down` | Starts or removes only PostgreSQL and Valkey |
| `make migrate` | Applies migrations |
| `make director USERNAME=alice` | Creates a director account (prompts for the password) |
| `make web-e2e` | Runs the browser tests against their own isolated stack and web app |
| `make web-e2e-prod` | The same browser tests against the production build (`next build`, then `next start`) |
| `make web-check` | Lints and type-checks the web app |
| `make infra-check` | Type-checks the CDK app and tests the templates it produces; needs no AWS account |
| `make aws-test` | Tests `scripts/aws.sh` with stand-ins for the AWS and CDK commands; needs no AWS account |
| `make aws-status` and other `make aws-<command>` | The AWS deployment; see "Deploying to AWS" |
| `make server` / `make worker` / `make web` | Runs one process on its own |

Configuration comes from environment variables, all with local defaults. See [.env.example](.env.example).

## The web app

`make dev` serves it at http://localhost:3000. A director logs in, creates a room from one form, and gets the room's page with its share link and a Start button. Anyone who opens the share link gets the audience page. The director's page lists who has joined, by nickname, as they join. Both show the performance live: the three lines to vote on with a countdown, the winning line, and at the end the premiere, which plays the eight recordings in order. While each one plays, its speaker takes the stage: their portrait fills the window, with the line beneath it. Create an account first (next section).

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

Which server a browser is connected to does not matter. Everything it is sent reaches the servers through two Redis streams that each of them reads whole: `performance-updates`, written by the worker, and `room-notices`, written by a server with news for the others (someone joined; a run was found dead). One server is what `make dev` and the deployment run; the end-to-end tests run two.

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

The worker then runs eight turns. Each has a ten-second voting window, so a run takes about 80 seconds. The choice with the most votes wins; a tie, or a round nobody voted in, is settled at random. An open room WebSocket (previous section) receives 25 updates: `generating`, `voting`, and `preparing_line` for each turn, then `premiere`. The run is visible in the Temporal UI at http://localhost:8233 as `performance/<performanceId>`, and the eight recordings land in `.data/objects/performances/<performanceId>/`. Each winning line is tagged and voiced in the background by a child workflow of its own, `performance/<performanceId>/line-<turn>`, while the next turn goes ahead; the premiere waits for all eight.

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

## Deploying to AWS

**Status, 2026-10-13: the deployment has been made, tested, and run live (below); not every command of the script has been run against the real account, and the ones that have not say so. The application stack passed the end-to-end suite in test mode on 2026-10-12 (32 run, 14 skipped), was destroyed, and was deployed again in live mode on 2026-10-13, where one performance with the real providers completed from three devices. Whether it is up right now depends on the last teardown; `./scripts/aws.sh status` says.** Design: [system design](docs/system-design.md), sections 2 and 7, and decisions D047 to D052.

What runs where: CloudFront (the public HTTPS address) in front of an internal load balancer; three Fargate tasks (web, server, worker); Aurora DSQL; one Valkey node on ElastiCache; a private S3 bucket; Temporal Cloud. Region `ap-southeast-1`.

There are two stacks. `MonsterTheatreData` (database, bucket, secrets, task roles) is kept. `MonsterTheatreApp` (everything billed by the hour) is destroyed between demos.

### Before the first deployment

1. Sign in to AWS as an administrator that is not the root user, and have Docker running (the images are built on this machine).
2. In Temporal Cloud, create a namespace in AWS `ap-southeast-1` with API-key authentication on, and an API key for it.
3. `cd infra && npm install && cp config.example.json config.json`, then fill `config.json` in: the namespace's address and name, and the two ElevenLabs voice IDs. It is not secret and is not committed.

### The script

Everything below goes through one script, which reads the endpoint, bucket, role ARNs, address, and mode from the stacks themselves:

```sh
./scripts/aws.sh status        # what is deployed, in which mode, at what address, and what it is costing
```

`make aws-<command>` is the same thing (`make aws-status`, `make aws-deploy-app ARGS=test`). The script signs in as the AWS profile `mt-admin` unless `AWS_PROFILE` says otherwise, refuses to run as the account's root user, and never reads a secret's value. An assistant working in this repository has the same commands as a skill, `.agents/skills/monster-theatre-aws/`, with the rules for when to ask first.

| To | Run |
| --- | --- |
| See what is deployed | `./scripts/aws.sh status` |
| Create or update the data stack, apply migrations, grant the database role | `./scripts/aws.sh deploy-data` |
| Deploy the application with stand-in providers (spends nothing) | `./scripts/aws.sh deploy-app test` |
| Deploy the application with Claude and ElevenLabs | `./scripts/aws.sh deploy-app live` |
| Check a deployment: routes, services, the worker's providers, `cdk diff` | `./scripts/aws.sh verify` |
| Run the end-to-end suite against it (test mode only) | `./scripts/aws.sh e2e` |
| Deploy test, run the suite, clear its leftovers, go live | `./scripts/aws.sh cycle` |
| Remove data | `./scripts/aws.sh clear-data test\|rooms\|all` |
| Tear down the application stack | `./scripts/aws.sh destroy-app` (and `leftovers` to check nothing remains) |
| Tear down the data stack | `./scripts/aws.sh destroy-data --yes-delete-everything` |
| Read logs, count rows, look at data | `./scripts/aws.sh logs worker`, `counts`, `query "SELECT ..."` |

### Deploy

Once per account:

```sh
cd infra && npx cdk bootstrap aws://ACCOUNT_ID/ap-southeast-1
```

Then the data stack, with its schema and the application's database role:

```sh
./scripts/aws.sh deploy-data
```

It ends by saying which of the three keys still hold a placeholder.

#### Put the three keys into Secrets Manager

The stack created three secrets that hold a meaningless placeholder. You replace each placeholder with the real key, once. The fourth secret, which share links are derived from, was generated by Secrets Manager and needs nothing from you.

| Secret | The key it needs | Where you get it |
| --- | --- | --- |
| `monster-theatre/temporal-api-key` | A Temporal Cloud API key for your namespace | Temporal Cloud, under API Keys. Pick an expiry of months, not a day |
| `monster-theatre/anthropic-api-key` | Your Anthropic API key (starts `sk-ant-`) | The Anthropic console, or copy it from your `.env` |
| `monster-theatre/elevenlabs-api-key` | Your ElevenLabs API key | The ElevenLabs site, or copy it from your `.env` |

Only the Temporal key is needed for a test-mode deployment. The other two are not used until `-c mode=live`, but there is no harm in setting all three now.

**How to do it.** Open a new Terminal window of your own (not one an assistant is running commands in), and do this once per key:

1. Copy the key to your clipboard from wherever it is kept.
2. Paste the whole block below into the terminal and press Enter. It prints a prompt and waits.
3. Paste the key with Cmd-V and press Enter. **Nothing appears as you paste; that is intended**, so the key is never shown on screen or saved in your shell history.
4. On success it prints one line, a version ID such as `a1b2c3d4-...`. An error message means it was not saved.

First, in that terminal, say which AWS sign-in to use (skip this line if you use your default profile):

```sh
export AWS_PROFILE=mt-admin
```

The Temporal key:

```sh
printf 'Paste the Temporal API key, then press Enter: '; read -s value; echo
aws secretsmanager put-secret-value --region ap-southeast-1 \
  --secret-id monster-theatre/temporal-api-key --secret-string "$value" \
  --query VersionId --output text; unset value
```

The Anthropic key:

```sh
printf 'Paste the Anthropic API key, then press Enter: '; read -s value; echo
aws secretsmanager put-secret-value --region ap-southeast-1 \
  --secret-id monster-theatre/anthropic-api-key --secret-string "$value" \
  --query VersionId --output text; unset value
```

The ElevenLabs key:

```sh
printf 'Paste the ElevenLabs API key, then press Enter: '; read -s value; echo
aws secretsmanager put-secret-value --region ap-southeast-1 \
  --secret-id monster-theatre/elevenlabs-api-key --secret-string "$value" \
  --query VersionId --output text; unset value
```

If you pasted the wrong thing, run the same block again; the newest value wins.

**To check, without showing any key:** this lists when each secret was last changed. A secret you have set shows the time you set it; one you have not shows the time the stack was deployed.

```sh
aws secretsmanager list-secrets --region ap-southeast-1 \
  --filters Key=name,Values=monster-theatre/ \
  --query 'SecretList[].[Name,LastChangedDate]' --output table
```

Do not use `get-secret-value` to check: it prints the key.

Then the application. In **test** mode it runs the stand-in providers with the test suite's short timings, and the worker is not given the provider keys, so it cannot spend anything on Claude or ElevenLabs. In **live** mode every press of Start is a paid run:

```sh
./scripts/aws.sh deploy-app test
./scripts/aws.sh deploy-app live
```

Either one takes 15 to 20 minutes from nothing. Run against a stack that is already up, it switches the mode in place in about five minutes and the address stays the same. It finishes by checking the deployment and printing the worker's startup line, which names the providers it will call. `status` prints the command for creating a director account.

A key changed later reaches a task only when it restarts, which any `deploy-app` does.

### Checking a deployment

```sh
./scripts/aws.sh verify     # routes, services, the worker's providers, cdk diff
./scripts/aws.sh smoke      # the object store against the real bucket: writes one small object and deletes it
./scripts/aws.sh e2e        # the end-to-end suite, against a deployment in test mode
```

`e2e` refuses a deployment in live mode, where each of its performances would be a paid run. It creates directors and reads rows and objects from this machine, as `admin` on the cluster. Of its 52 top-level tests, 20 skip themselves there, each saying why, and stay covered by `make e2e`: six need fake-provider scripts on the worker's disk, three stop the worker, three need a second server, one terminates a workflow, one asks `/healthz`, which is not reachable from outside, and six need a database of their own to empty.

The whole sequence in one command: `./scripts/aws.sh cycle` deploys in test mode, runs the suite, stops if anything failed, clears what the suite left behind, and switches to live. `cycle --teardown-between` destroys and recreates the application stack between the two instead of switching in place, which takes about 35 minutes longer and changes the address.

### Clearing data

```sh
./scripts/aws.sh clear-data test          # says what it would remove
./scripts/aws.sh clear-data test --yes    # removes it
```

| Scope | Removes | Keeps |
| --- | --- | --- |
| `test` | The directors the test suites create (named exactly `director-` plus 12 hex digits, or `web-e2e-` plus digits), and their sessions, rooms, audience sessions, performances, portraits, and recordings | Every other director and all they own |
| `rooms` | Every room, audience session, performance, and stored object | All directors and their logins |
| `all` | Every row in the five tables and every stored object | The schema, the grants, the migration record, the secrets |

Without `--yes` nothing is changed. `all` takes `--yes-delete-everything` instead. Do not clear while a performance is running. Locally the same thing is `cd server && go run ./cmd/dbadmin clear -scope test`, which works on the dev database and `.data/objects`.

### Cost

From the AWS pricing API for `ap-southeast-1` on 2026-10-12, at 730 hours a month. The usage-based lines are estimates for a demo's traffic.

| Item | USD a month |
| --- | --- |
| Three Fargate arm64 tasks, 0.25 vCPU and 0.5 GB each | 27.00 |
| Internal load balancer (0.0252 an hour), plus capacity units used | 18.40 + usage |
| Valkey `cache.t4g.micro` (0.0192 an hour) | 14.00 |
| Three public IPv4 addresses (0.005 an hour each) | 11.00 |
| Four secrets | 1.60 |
| Aurora DSQL (9 per million DPU, 0.40 per GB), S3, logs, CloudFront, image storage | under 3, estimated |
| **Everything left running** | **about 75** |
| **App stack destroyed** | **about 2** |

Temporal Cloud is billed separately by Temporal and is not included. A day with the app stack up is about 2.50 USD.

### Tearing down

Between demos, remove what is billed by the hour. The database, the bucket, the secrets, and the database grants are untouched. The next `deploy-app` brings it back with a new CloudFront address, so share links sent before stop working:

```sh
./scripts/aws.sh destroy-app
```

It takes 16 to 20 minutes and then counts what the stack made, straight from each service, to check that nothing is left.

To remove everything else as well:

```sh
./scripts/aws.sh destroy-data --yes-delete-everything
```

This turns off the stack's termination protection and the cluster's deletion protection, deletes every room, recording, and director, and deletes the four secrets for good, so that `deploy-data` can create them again at once. The three keys must then be put in again, and every share link ever sent stops working. It refuses while the application stack exists. **This command has not yet been run against the real account; it is covered by tests with stand-in commands only.**

### Without the script

What the script runs, for reference: `npx cdk deploy|destroy <stack> [-c mode=live]` in `infra/`; `go run ./cmd/migrate up` and `go run ./cmd/migrate grant <ServerRoleArn> <WorkerRoleArn>` in `server/` with `DSQL_ENDPOINT` set; and the end-to-end suite with `E2E_BASE_URL`, `E2E_DSQL_ENDPOINT`, and `E2E_S3_BUCKET` set. The stack outputs they need come from `aws cloudformation describe-stacks`.

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
- no column drops or type changes without recreating the table;
- an index goes in a file of its own, written as plain `CREATE INDEX`. Aurora DSQL needs `CREATE INDEX ASYNC`, which PostgreSQL does not accept, so `migrate` inserts `ASYNC` itself when it is pointed at DSQL and then waits for the builds (D048);
- a column that is unique or indexed must be a type DSQL can index, which rules out `bytea` and `jsonb` (D047).

One exception was made while every database was still a local test one: step 12 added `premiere_starts_at` to the `performances` create-table migration (`00006`) instead of adding a migration. Goose does not re-run a migration it has recorded, so a database migrated before that needs the table recreated, which deletes its performance rows:

```sh
cd server && go run ./cmd/migrate down && go run ./cmd/migrate down && go run ./cmd/migrate up
```

The same was done once more after step 14, for `nickname` on `audience_sessions` (`00005`); a database migrated before that needs three `down`s, not two, and loses its audience sessions as well.

And a third time at step 15, when `credential_hash` in `director_sessions` (`00003`) and `audience_sessions` (`00005`) changed from `bytea` to `text` (D047): a database migrated before that needs seven `down`s, which removes every room, session, and performance and keeps the directors.

For the end-to-end tests' database, drop it and let the next run recreate it: `docker compose exec postgres dropdb -U monster monstertheatre_e2e`.
