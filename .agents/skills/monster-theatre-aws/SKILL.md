---
name: monster-theatre-aws
description: Operate Monster Theatre's AWS deployment. Use when asked to deploy (test or live), redeploy or migrate the data stack, run the end-to-end tests against what is deployed, clear or delete deployed data, tear down the app or data stack, check what is deployed or what it is costing, read the deployed logs, or run the whole test-then-live flow. Everything goes through scripts/aws.sh.
---

# Monster Theatre on AWS

One script does the work: `./scripts/aws.sh <command>`, run from the repository root. It reads the endpoint, bucket, role ARNs, address, and mode from the stacks' own outputs, so nothing is copied by hand. Do not rebuild its steps from separate `aws`, `cdk`, or `go` commands; if it cannot do something, say so and extend it (its tests are `./scripts/aws_test.sh`).

The design is in `docs/system-design.md` and decisions D047 to D052. The README's "Deploying to AWS" has the same commands for a person.

## What exists

| Stack | Holds | Cost |
| --- | --- | --- |
| `MonsterTheatreData` | Aurora DSQL cluster, S3 bucket, four secrets, two task roles | About 2 USD a month. Kept between demos |
| `MonsterTheatreApp` | VPC, Valkey node, three Fargate tasks, internal load balancer, CloudFront | About 0.10 USD an hour (about 75 USD a month). Destroyed between demos |

Region `ap-southeast-1`. Two modes for the app stack: **test** (stand-in providers, 2-second votes, the worker is not given the provider keys, spends nothing) and **live** (Claude and ElevenLabs; every Start is a paid run of 16 Claude calls and 8 ElevenLabs calls).

## Start every session with `status`

```sh
./scripts/aws.sh status
```

It is read-only and says who is signed in, whether Docker is up, which stacks exist, the mode and address, how long the app stack has been up and roughly what that has cost, whether each key has been set, and the row and object counts. Tell the user what it shows before doing anything else, especially an app stack that has been left running.

## What the user says, and what to run

| The user asks for | Run | Takes |
| --- | --- | --- |
| What is deployed / what is it costing | `status` | Seconds |
| Deploy or redeploy Data | `deploy-data` | About 1 minute when nothing changed; a few minutes from nothing |
| Deploy test | `deploy-app test` | 15 to 20 minutes from nothing; about 5 when switching a running stack (measured: 5 min 34 s and 4 min 54 s); seconds when nothing changed |
| Deploy live | `deploy-app live` | The same |
| Run the e2e tests against what is deployed | `e2e` | About 1 minute |
| Deploy test, test, then go live | `cycle` | About 7 minutes from a stack already in test mode (measured: 6 min 38 s); about 25 from nothing. See "The cycle" |
| Clear the data | `clear-data test`, `rooms`, or `all` | Seconds to a minute |
| Tear down App | `destroy-app` | 16 to 20 minutes |
| Is anything of the app stack left? | `leftovers` | Seconds |
| Tear down Data | `destroy-data --yes-delete-everything` | A few minutes |
| Check a deployment | `verify` | 1 to 2 minutes |
| Logs | `logs server`, `logs worker`, `logs web` (add `2h` for a longer window) | Seconds |
| A look at the data | `counts`, or `query "SELECT ..."` (one read-only statement) | Seconds |
| The address | `url` | Seconds |

Every deploy ends with `verify`, which fails the command unless the routes answer as designed (`/login` 200, `/api/v1/director/me` 401, `/healthz` 404), the three services run 1 of 1, the worker's provider matches the mode, and `cdk diff` is empty. It prints the worker's startup line. **Relay that line to the user**: it is the evidence of which providers a deployment will call.

## When to act and when to ask

The user chose this on 2026-10-13: **naming an operation is the approval for it.** State in one or two lines what will change and what it costs, then run it. A flow the user named (`cycle`) runs through without asking at each step.

Three things are never covered by that:

1. **`destroy-data`**: ask, every time, immediately before. It deletes the database, every recording, and the four secrets for good; the user must then put the three keys in again and every share link dies.
2. **`clear-data all`**: ask, every time. It removes every director, including the user's own.
3. **Starting a performance: never.** Not through the pages, the API, or a test against a live deployment. `e2e` refuses in live mode for this reason; do not work around it. The user starts performances.

Also ask before `aws login` (see below), and before anything this skill does not name.

For `clear-data`, run it without `--yes` first and show the user the counts it prints, unless they asked for the `test` scope by name, which is safe to apply directly: it removes only accounts whose names are exactly what the test suites generate.

## The cycle

`./scripts/aws.sh cycle` does: `deploy-app test`, `e2e`, stop if anything failed, `clear-data test --yes`, `deploy-app live`. It ends with the worker's provider line on screen and nothing started.

- By default the switch from test to live is **in place**: only the server and worker tasks are replaced, in a few minutes, and the address stays the same.
- `cycle --teardown-between` destroys the app stack after the tests and creates it again in live mode. About 35 minutes longer, and the address changes. Use it only when the user asks for a teardown in between.
- If the suite fails, the cycle stops with the deployment still up **in test mode** and nothing cleared, so it can be looked at. Report the failures (the command prints them and the path of the full log) and remember the stack is still costing money.

After a cycle the deployment is live and billing. Say so, and ask whether to tear it down when the user is done.

## Clearing data

| Scope | Removes | Keeps |
| --- | --- | --- |
| `test` | Directors named exactly `director-` plus 12 hex digits or `web-e2e-` plus digits, and their sessions, rooms, audience sessions, performances, portraits, and recordings | Every other director and all they own |
| `rooms` | Every room, audience session, performance, and stored object | All directors and their logins |
| `all` | Every row in the five tables and every stored object | The schema, the grants, the migration record, the secrets |

`--yes` applies `test` and `rooms`; `all` needs `--yes-delete-everything`. Without either it is a dry run. Do not clear while a performance is running. A dry run does not count stored objects.

## Rules

- **Identity.** The script signs in as the profile `mt-admin` (override with `AWS_PROFILE`) and refuses the account's root user. If it reports an expired sign-in, ask the user "Ready for me to run `aws login --profile mt-admin`?", run it when they agree, and they finish it in the browser. It lasts up to 12 hours.
- **Secrets.** Never read a secret's value: no `get-secret-value`, no reading `.env`. The script only checks whether a value has been put in. The user sets the three keys in their own terminal with the `read -s` commands in the README; give them those commands, never ask for a key in the conversation. A changed key reaches a task only when it restarts, which any `deploy-app` does.
- **Director accounts** take a password, so the user creates them in their own terminal. `status` prints the exact command.
- **Long commands.** `deploy-app`, `destroy-app`, and `cycle` outlast an ordinary command limit. Run them in the background with a limit of an hour. If the local process is stopped, CloudFormation carries on regardless: run `./scripts/aws.sh wait`, then `verify` or `status`. Never start a second deploy or destroy while one is in progress; the script refuses, and that refusal is right.
- **Docker** must be running for `deploy-app`: the images are built on this machine.
- **Cost.** Say what an operation costs before running it, and mention a running app stack whenever you see one.
- **Report plainly** what was run, what the checks showed, and what was not checked.
- **Code changes** to the script, the CDK app, or `dbadmin` follow the project's usual process: tests first, a branch, a pull request.

## When something goes wrong

| What you see | What it was before | What to do |
| --- | --- | --- |
| Every address answers 504 after a deploy | The load balancer's security group did not admit CloudFront. Fixed in `infra/lib/app-stack.ts` (CloudFront's managed prefix list), with a template test | If it returns, check that rule first: `make infra-check` |
| The app stack ends in `ROLLBACK` | The worker exits when it cannot reach Temporal Cloud or lacks a key, and ECS rolls the deployment back | `logs worker`. Usually a missing, wrong, or expired Temporal API key; the user replaces the secret |
| `deploy-app` refuses: a key is "still the placeholder" | The user has not put that key in since the data stack was created | Give them the README's commands |
| `migrate grant` fails on `GRANT ... ON SCHEMA public` | Aurora DSQL refuses grants on that schema. Already removed from the code | Do not add it back |
| `deploy-data` fails on a secret that already exists | A secret deleted without `destroy-data` is still in its recovery window | `destroy-data` deletes them for good; or the user purges that one secret |
| The address changed | A new CloudFront distribution gets a new address whenever the app stack is created | Share links sent before no longer work. An in-place mode switch keeps the address |
| `destroy-app` says something is "still there" | It counts load balancers, cache nodes, ECS clusters, VPCs, log groups, and CloudFront distributions straight from each service, and prints the counts | A count above zero is a real leftover: name it to the user and ask before deleting anything by hand. Do not check with the resource tagging service: it lists deleted ECS clusters for a day or more, which made the first version of this check fail wrongly |

## What has and has not been run for real

Run through this script against the real account on 2026-10-13: `status`; `deploy-app test` and `deploy-app live`, each as an in-place switch of a running stack, in both directions; `verify`; `e2e` (32 passed, 20 skipped); `clear-data` as a dry run in all three scopes; `clear-data test --yes`, which left only the user's own account and rooms; `cycle` in place; `destroy-app`; `leftovers`; `query`; `counts`. `query` ran inside a read-only transaction, so Aurora DSQL accepts those; that it refuses a write inside one was checked on local PostgreSQL only.

Two things the first real runs found, both fixed with a test: `verify` read two workers' startup lines as one after a mode switch, and `destroy-app`'s leftover check trusted the tagging service. Expect the stand-in tests to miss this kind of thing: they prove the order of commands and the refusals, not what AWS answers.

**Covered only by tests with stand-in commands, never run on AWS through this script:** `destroy-data`; `deploy-data` (its three steps were run by hand on 2026-10-12); `deploy-app` creating the stack from nothing (also done by hand); `destroy-app`'s new leftover check as part of a real teardown (it was run just after one); `clear-data rooms --yes` and `clear-data all --yes-delete-everything` on the cluster (they pass against local PostgreSQL); `cycle --teardown-between`. The first time one of these is run for real, watch it closely and update this section.
