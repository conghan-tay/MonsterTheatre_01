#!/usr/bin/env bash
# One entry point for the AWS deployment: deploy, check, clear, tear down.
#
#   ./scripts/aws.sh status
#   ./scripts/aws.sh deploy-data
#   ./scripts/aws.sh deploy-app test|live
#   ./scripts/aws.sh verify
#   ./scripts/aws.sh e2e
#   ./scripts/aws.sh cycle [--teardown-between]
#   ./scripts/aws.sh clear-data test|rooms|all [--yes | --yes-delete-everything]
#   ./scripts/aws.sh destroy-app
#   ./scripts/aws.sh destroy-data --yes-delete-everything
#   ./scripts/aws.sh smoke | counts | query "SELECT ..." | logs server|worker|web [SINCE]
#   ./scripts/aws.sh url | diff | wait | leftovers
#
# It reads what it needs (endpoint, bucket, role ARNs, address, mode) from
# the stacks' own outputs. It signs in as AWS_PROFILE, "mt-admin" unless set,
# and refuses to run as the account's root user. It never reads a secret's
# value.
#
# What each command costs, when to ask first, and what has gone wrong before
# are in .agents/skills/monster-theatre-aws/SKILL.md. tests: aws_test.sh.
set -euo pipefail
cd "$(dirname "$0")/.."

export AWS_PROFILE="${AWS_PROFILE:-mt-admin}"
export AWS_REGION="${AWS_REGION:-ap-southeast-1}"
DATA=MonsterTheatreData
APP=MonsterTheatreApp
SECRETS="anthropic-api-key elevenlabs-api-key temporal-api-key share-link-secret"
# How often a wait looks again. The tests set it to 0.
POLL="${AWS_SH_POLL_SECONDS:-20}"
# Roughly what the app stack costs an hour while it exists (README, "Cost").
APP_USD_PER_HOUR="0.10"

say() { printf '\n== %s\n' "$*"; }
die() { printf 'aws.sh: %s\n' "$*" >&2; exit 1; }

usage() {
  sed -n '2,17p' "$0" | sed 's/^# \{0,1\}//'
}

# ------------------------------------------------------------------ helpers

# require_identity stops unless signed in as someone who is not root.
require_identity() {
  local arn
  arn="$(aws sts get-caller-identity --query Arn --output text 2>/dev/null)" ||
    die "not signed in to AWS as '$AWS_PROFILE' (or the sign-in has expired). Run: aws login --profile $AWS_PROFILE"
  case "$arn" in
  *:root) die "signed in as the account's root user ($arn). Deploy as an IAM identity instead, for example the mt-admin profile." ;;
  esac
  IDENTITY="$arn"
}

# stack_status NAME prints the stack's status, or NONE if it does not exist.
stack_status() {
  aws cloudformation describe-stacks --stack-name "$1" --query 'Stacks[0].StackStatus' --output text 2>/dev/null || echo NONE
}

# output STACK KEY prints one of the stack's outputs.
output() {
  aws cloudformation describe-stacks --stack-name "$1" \
    --query "Stacks[0].Outputs[?OutputKey=='$2'].OutputValue" --output text
}

# wait_stable NAME waits until the stack is not in the middle of anything and
# prints the status it settled in (NONE once a delete has finished).
wait_stable() {
  local status last=""
  while :; do
    status="$(stack_status "$1")"
    case "$status" in
    *_IN_PROGRESS)
      [ "$status" = "$last" ] || printf '   %s: %s...\n' "$1" "$status" >&2
      last="$status"
      sleep "$POLL"
      ;;
    *) echo "$status"; return ;;
    esac
  done
}

require_not_busy() {
  local stack status
  for stack in "$DATA" "$APP"; do
    status="$(stack_status "$stack")"
    case "$status" in
    *_IN_PROGRESS) die "$stack is $status. Wait for it (./scripts/aws.sh wait) before starting anything else." ;;
    esac
  done
}

require_data() {
  case "$(stack_status "$DATA")" in
  CREATE_COMPLETE | UPDATE_COMPLETE) ;;
  *) die "the data stack is not deployed. Run: ./scripts/aws.sh deploy-data" ;;
  esac
}

# cdk runs the CDK command line in infra/, with the config file the tests ask for.
cdk() {
  local config=()
  [ -z "${AWS_SH_CONFIG:-}" ] || config=(-c "config=$AWS_SH_CONFIG")
  (cd infra && npx cdk "$@" ${config[@]+"${config[@]}"})
}

# secret_is_set NAME: a secret the stack created holds one version, its
# placeholder; putting a value in gives it a second. Only counts are read.
secret_is_set() {
  local versions
  versions="$(aws secretsmanager describe-secret --secret-id "monster-theatre/$1" \
    --query 'length(keys(VersionIdsToStages))' --output text 2>/dev/null)" || return 1
  [ "${versions:-0}" -ge 2 ]
}

# deployed_mode prints "test" or "live", or nothing if the app stack is down.
deployed_mode() {
  case "$(stack_status "$APP")" in
  NONE) ;;
  *) output "$APP" Mode ;;
  esac
}

# with_database runs a command in server/ with the cluster's and bucket's
# addresses set, as admin.
with_database() {
  local endpoint bucket
  endpoint="$(output "$DATA" DsqlEndpoint)"
  bucket="$(output "$DATA" BucketName)"
  (cd server && DSQL_ENDPOINT="$endpoint" S3_BUCKET="$bucket" "$@")
}

# worker_line prints the worker's most recent startup line.
#
# The log holds one such line for every worker there has been, and the
# command returns them a page at a time in no useful order. Each line begins
# with its own time, {"time":"2026-...", so sorting them puts the newest last.
worker_line() {
  local group
  group="$(aws logs describe-log-groups --log-group-name-prefix "$APP-WorkerLogs" \
    --query 'logGroups[0].logGroupName' --output text)"
  aws logs filter-log-events --log-group-name "$group" --filter-pattern '"worker started"' \
    --query 'events[].message' --output text | tr '\t' '\n' | grep '"worker started"' | sort | tail -1
}

# field LINE NAME prints the value of one field of a JSON log line.
field() {
  printf '%s' "$1" | sed -n -E "s/.*\"$2\":\"?([^\",}]*)\"?.*/\1/p"
}

# ----------------------------------------------------------------- commands

cmd_status() {
  require_identity
  say "Sign-in"
  echo "identity: $IDENTITY   region: $AWS_REGION"
  if docker info >/dev/null 2>&1; then echo "docker:   running"; else echo "docker:   NOT running (needed to deploy the app stack)"; fi

  say "Stacks"
  local data app
  data="$(stack_status "$DATA")"
  app="$(stack_status "$APP")"
  echo "$DATA  $data"
  echo "$APP   $app"
  if [ "$app" != NONE ]; then
    local created since hours
    echo "mode:     $(output "$APP" Mode)"
    echo "address:  $(output "$APP" Url)"
    created="$(aws cloudformation describe-stacks --stack-name "$APP" --query 'Stacks[0].CreationTime' --output text)"
    created="${created%%.*}"
    created="${created%%+*}"
    since="$(date -j -u -f '%Y-%m-%dT%H:%M:%S' "$created" +%s 2>/dev/null || date -u -d "$created" +%s 2>/dev/null || echo "")"
    if [ -n "$since" ]; then
      hours=$((($(date -u +%s) - since) / 3600))
      echo "up since: ${created}Z, about $hours hours, at about $APP_USD_PER_HOUR USD an hour"
    fi
  else
    echo "nothing is billed by the hour."
  fi

  if [ "$data" = NONE ]; then
    echo "no data stack: nothing else to report."
    return
  fi

  say "Secrets (whether a value has been put in; values are never read)"
  local name
  for name in $SECRETS; do
    if [ "$name" = share-link-secret ]; then
      echo "$name: generated by Secrets Manager"
    elif secret_is_set "$name"; then
      echo "$name: set"
    else
      echo "$name: still the placeholder. Set it yourself: README, \"Put the three keys into Secrets Manager\""
    fi
  done

  say "Data"
  with_database go run ./cmd/dbadmin counts || echo "(could not count rows)"
  echo "objects in the bucket: $(aws s3 ls "s3://$(output "$DATA" BucketName)" --recursive 2>/dev/null | wc -l | tr -d ' ')"

  say "To create a director (in your own terminal; it asks for a password)"
  echo "AWS_PROFILE=$AWS_PROFILE DSQL_ENDPOINT=$(output "$DATA" DsqlEndpoint) make director USERNAME=<name>"
}

cmd_deploy_data() {
  require_identity
  require_not_busy
  # The data stack's template is the same in both modes, but the app must be
  # synthesised in the mode it is deployed in, or CDK reports a change to it.
  local mode status
  mode="$(deployed_mode)"
  say "Deploying $DATA"
  cdk deploy "$DATA" -c "mode=${mode:-test}" --require-approval never || true
  status="$(wait_stable "$DATA")"
  case "$status" in
  CREATE_COMPLETE | UPDATE_COMPLETE) ;;
  *) die "$DATA ended as $status. See the CloudFormation events for the first failure." ;;
  esac

  say "Applying migrations"
  with_database go run ./cmd/migrate up
  say "Granting the application's database role to the task roles"
  with_database go run ./cmd/migrate grant "$(output "$DATA" ServerRoleArn)" "$(output "$DATA" WorkerRoleArn)"

  say "Secrets"
  local name unset=0
  for name in anthropic-api-key elevenlabs-api-key temporal-api-key; do
    if secret_is_set "$name"; then
      echo "$name: set"
    else
      echo "$name: still the placeholder"
      unset=1
    fi
  done
  [ "$unset" -eq 0 ] || echo "Set the placeholders yourself, in your own terminal: README, \"Put the three keys into Secrets Manager\"."
  echo "$DATA is deployed, migrated, and granted."
}

cmd_deploy_app() {
  local mode="${1:-}"
  case "$mode" in
  test | live) ;;
  *) die "deploy-app needs a mode: test (stand-in providers, spends nothing) or live (Claude and ElevenLabs)" ;;
  esac
  require_identity
  require_not_busy
  require_data
  docker info >/dev/null 2>&1 || die "Docker is not running. The images are built on this machine; start Docker Desktop."
  [ -f "infra/${AWS_SH_CONFIG:-config.json}" ] || die "infra/config.json is missing. Copy infra/config.example.json and fill it in."
  secret_is_set temporal-api-key || die "the secret temporal-api-key still holds its placeholder; the worker would not start. Set it first (README)."
  if [ "$mode" = live ]; then
    local name
    for name in anthropic-api-key elevenlabs-api-key; do
      secret_is_set "$name" || die "the secret $name still holds its placeholder; live mode needs it. Set it first (README)."
    done
  fi

  say "Deploying $APP in $mode mode"
  # If this process is stopped part-way, CloudFormation carries on; the wait
  # below is what decides how it ended.
  cdk deploy "$APP" -c "mode=$mode" --require-approval never || true
  local status
  status="$(wait_stable "$APP")"
  case "$status" in
  CREATE_COMPLETE | UPDATE_COMPLETE) ;;
  *) die "$APP ended as $status. Read the worker and server logs (./scripts/aws.sh logs worker) and the CloudFormation events." ;;
  esac
  cmd_verify
}

cmd_verify() {
  require_identity
  local mode url
  mode="$(deployed_mode)"
  [ -n "$mode" ] || die "the app stack is not deployed. Run: ./scripts/aws.sh deploy-app test"
  url="$(output "$APP" Url)"
  say "Verifying $url (mode: $mode)"

  # A new CloudFront distribution can take a minute to start answering.
  local path want got try ok=1
  for path in /login:200 /api/v1/director/me:401 /healthz:404; do
    want="${path##*:}"
    path="${path%%:*}"
    got=""
    for try in 1 2 3 4 5 6; do
      got="$(curl -s -m 30 -o /dev/null -w '%{http_code}' "$url$path" || true)"
      [ "$got" = "$want" ] && break
      sleep "$POLL"
    done
    if [ "$got" = "$want" ]; then echo "ok    $path -> $got"; else echo "WRONG $path -> $got (expected $want)"; ok=0; fi
  done

  local cluster services line running desired name
  cluster="$(output "$APP" ClusterName)"
  services="$(aws ecs list-services --cluster "$cluster" --query serviceArns --output text)"
  # shellcheck disable=SC2086
  while IFS="$(printf '\t')" read -r name running desired; do
    [ -n "$name" ] || continue
    if [ "$running" = "$desired" ] && [ "$running" != 0 ]; then echo "ok    $name: $running of $desired running"; else echo "WRONG $name: $running of $desired running"; ok=0; fi
  done <<EOF
$(aws ecs describe-services --cluster "$cluster" --services $services --query 'services[].[serviceName,runningCount,desiredCount]' --output text)
EOF

  line="$(worker_line)"
  local llm tts expect
  llm="$(field "$line" llm_provider)"
  tts="$(field "$line" tts_provider)"
  say "The worker's startup line"
  echo "llm_provider: $llm   llm_model: $(field "$line" llm_model)"
  echo "tts_provider: $tts   tts_model: $(field "$line" tts_model)"
  echo "vote_window:  $(field "$line" vote_window)"
  if [ "$mode" = live ]; then expect=anthropic; else expect=fake; fi
  if [ "$llm" != "$expect" ]; then
    echo "WRONG the worker's language-model provider is '$llm' but the stack is in $mode mode (expected '$expect')"
    ok=0
  fi

  say "cdk diff"
  if cdk diff -c "mode=$mode" 2>&1 | grep -q "Number of stacks with differences: 0"; then
    echo "ok    no differences between the code and what is deployed"
  else
    echo "WRONG the deployed stacks differ from the code (./scripts/aws.sh diff shows how)"
    ok=0
  fi

  [ "$ok" -eq 1 ] || die "verification failed: the deployment is not as expected (provider, routes, services, or diff above)"
  if [ "$mode" = live ]; then
    echo
    echo "Live mode: every Start on $url is a paid run. No performance has been started."
  else
    echo
    echo "Test mode: stand-in providers; nothing is spent on Claude or ElevenLabs."
  fi
}

cmd_e2e() {
  require_identity
  local mode
  mode="$(deployed_mode)"
  [ -n "$mode" ] || die "nothing is deployed to test. Run: ./scripts/aws.sh deploy-app test"
  [ "$mode" = test ] || die "the deployment is in live mode. The suite starts performances, and each would be a paid run; redeploy in test mode first (./scripts/aws.sh deploy-app test)."

  local url endpoint bucket log code=0
  url="$(output "$APP" Url)"
  endpoint="$(output "$DATA" DsqlEndpoint)"
  bucket="$(output "$DATA" BucketName)"
  log="$(mktemp -t monstertheatre-e2e.XXXXXX)"
  say "Running the end-to-end suite against $url"
  (cd tests/e2e && E2E_BASE_URL="$url" E2E_DSQL_ENDPOINT="$endpoint" E2E_S3_BUCKET="$bucket" go test . -count=1 -v) >"$log" 2>&1 || code=$?

  grep -E '^--- FAIL|^\s+--- FAIL|^HARNESS FAILURE' "$log" || true
  echo "passed: $(grep -c '^--- PASS' "$log" || true)  failed: $(grep -c '^--- FAIL' "$log" || true)  skipped: $(grep -c '^--- SKIP' "$log" || true)  (top-level tests; full output: $log)"
  [ "$code" -eq 0 ] || die "the end-to-end suite failed"
  echo "The suite leaves throwaway directors and rooms behind: ./scripts/aws.sh clear-data test --yes removes them."
}

cmd_smoke() {
  require_identity
  require_data
  say "S3 smoke test"
  (cd server && S3_SMOKE_BUCKET="$(output "$DATA" BucketName)" go test ./internal/objectstore/ -run Smoke -count=1 -v)
}

cmd_clear_data() {
  local scope="${1:-}" flag="${2:-}" yes=()
  case "$scope" in
  test | rooms | all) ;;
  *) die "clear-data needs a scope: test (what the test suites left), rooms (everything but directors), or all" ;;
  esac
  case "$flag" in
  "") ;;
  --yes)
    [ "$scope" != all ] || die "clearing all removes every director, room, and recording; it needs --yes-delete-everything, not --yes"
    yes=(-yes)
    ;;
  --yes-delete-everything) yes=(-yes) ;;
  *) die "unknown option $flag" ;;
  esac
  require_identity
  require_data
  say "Clearing data: scope $scope"
  with_database go run ./cmd/dbadmin clear -scope "$scope" ${yes[@]+"${yes[@]}"}
}

cmd_destroy_app() {
  require_identity
  if [ "$(stack_status "$APP")" = NONE ]; then
    echo "$APP does not exist; nothing to tear down."
    return
  fi
  require_not_busy
  say "Destroying $APP"
  cdk destroy "$APP" --force || true
  local status
  status="$(wait_stable "$APP")"
  [ "$status" = NONE ] || die "$APP ended as $status instead of being deleted. See the CloudFormation events."

  say "Checking that nothing is left"
  leftovers || die "something of $APP is still there after the stack was deleted (counts above)"
  echo "$APP is gone and nothing is left of it. Nothing is billed by the hour."
}

# leftovers counts what the app stack makes, straight from each service, and
# fails if any count is not zero. It does not ask the resource tagging
# service: that goes on listing a deleted ECS cluster for a day or more.
leftovers() {
  local tagged="Name=tag:aws:cloudformation:stack-name,Values=$APP" clean=0 what count
  while IFS='|' read -r what count; do
    printf '%-26s %s\n' "$what:" "$count"
    [ "$count" = 0 ] || clean=1
  done <<COUNTS
load balancers|$(aws elbv2 describe-load-balancers --query "length(LoadBalancers[?starts_with(LoadBalancerName, 'Monste')])" --output text)
cache nodes|$(aws elasticache describe-replication-groups --query "length(ReplicationGroups[?Description=='Monster Theatre streams'])" --output text)
ECS clusters|$(aws ecs list-clusters --query "length(clusterArns[?contains(@, '$APP')])" --output text)
VPCs|$(aws ec2 describe-vpcs --filters "$tagged" --query 'length(Vpcs)' --output text)
log groups|$(aws logs describe-log-groups --log-group-name-prefix "$APP" --query 'length(logGroups)' --output text)
CloudFront distributions|$(aws cloudfront list-distributions --query "length(DistributionList.Items[?Comment=='Monster Theatre'] || \`[]\`)" --output text)
COUNTS
  return "$clean"
}

cmd_destroy_data() {
  [ "${1:-}" = --yes-delete-everything ] ||
    die "destroy-data deletes the database, every recording, and the four secrets for good. It needs --yes-delete-everything"
  require_identity
  [ "$(stack_status "$APP")" = NONE ] || die "the app stack still exists. Run ./scripts/aws.sh destroy-app first."
  if [ "$(stack_status "$DATA")" = NONE ]; then
    echo "$DATA does not exist; nothing to tear down."
  else
    require_not_busy
    local endpoint
    endpoint="$(output "$DATA" DsqlEndpoint)"
    say "Removing the protections"
    aws cloudformation update-termination-protection --no-enable-termination-protection --stack-name "$DATA" >/dev/null
    aws dsql update-cluster --identifier "${endpoint%%.*}" --no-deletion-protection-enabled >/dev/null
    say "Destroying $DATA"
    cdk destroy "$DATA" --force || true
    local status
    status="$(wait_stable "$DATA")"
    [ "$status" = NONE ] || die "$DATA ended as $status instead of being deleted. See the CloudFormation events."
  fi

  # Deleted with no recovery window, so the names are free for the next
  # deploy-data. The three keys must then be put in again.
  say "Deleting the secrets for good"
  local name
  for name in $SECRETS; do
    aws secretsmanager delete-secret --secret-id "monster-theatre/$name" --force-delete-without-recovery >/dev/null 2>&1 || true
    echo "deleted monster-theatre/$name"
  done
  echo "$DATA is gone. deploy-data creates it again, empty; the three keys then need putting in again."
}

cmd_cycle() {
  local teardown=0
  case "${1:-}" in
  "") ;;
  --teardown-between) teardown=1 ;;
  *) die "unknown option $1" ;;
  esac

  cmd_deploy_app test
  if ! (cmd_e2e); then
    die "the cycle stopped: the end-to-end suite failed. The deployment is still up in test mode, for looking at; nothing was switched to live."
  fi
  cmd_clear_data test --yes
  [ "$teardown" -eq 0 ] || cmd_destroy_app
  cmd_deploy_app live
}

cmd_logs() {
  require_identity
  local service group
  case "${1:-}" in
  server) service=Server ;;
  worker) service=Worker ;;
  web) service=Web ;;
  *) die "logs needs a service: server, worker, or web" ;;
  esac
  group="$(aws logs describe-log-groups --log-group-name-prefix "$APP-${service}Logs" \
    --query 'logGroups[0].logGroupName' --output text)"
  [ -n "$group" ] && [ "$group" != None ] || die "no log group for $1; is the app stack deployed?"
  local since="${2:-30m}" lines
  lines="$(aws logs tail "$group" --since "$since" --format short)"
  say "$1, last $since: lines by level"
  printf '%s\n' "$lines" | grep -o '"level":"[A-Z]*"' | sort | uniq -c || true
  say "$1, last $since: the last 40 lines"
  printf '%s\n' "$lines" | tail -40
}

main() {
  local command="${1:-}"
  [ $# -eq 0 ] || shift
  case "$command" in
  status) cmd_status ;;
  deploy-data) cmd_deploy_data ;;
  deploy-app) cmd_deploy_app "$@" ;;
  verify) cmd_verify ;;
  e2e) cmd_e2e ;;
  smoke) cmd_smoke ;;
  cycle) cmd_cycle "$@" ;;
  clear-data) cmd_clear_data "$@" ;;
  destroy-app) cmd_destroy_app ;;
  leftovers) require_identity; leftovers && echo "nothing of $APP exists" ;;
  destroy-data) cmd_destroy_data "$@" ;;
  logs) cmd_logs "$@" ;;
  counts) require_identity; require_data; with_database go run ./cmd/dbadmin counts ;;
  query) require_identity; require_data; with_database go run ./cmd/dbadmin query "${1:?query needs a statement}" ;;
  url) require_identity; output "$APP" Url ;;
  diff) require_identity; cdk diff -c "mode=$(deployed_mode)" ;;
  wait)
    require_identity
    echo "$DATA: $(wait_stable "$DATA")"
    echo "$APP: $(wait_stable "$APP")"
    ;;
  help | -h | --help) usage ;;
  *)
    usage >&2
    die "unknown command '${command}'"
    ;;
  esac
}

main "$@"
