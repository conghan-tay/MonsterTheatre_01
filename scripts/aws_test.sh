#!/usr/bin/env bash
# Tests for scripts/aws.sh. They touch no AWS account: `aws`, `npx`, `go`,
# `docker`, and `curl` are replaced by stand-ins that record how they were
# called and answer from a small pretend state.
#
#   ./scripts/aws_test.sh            run every test
#   ./scripts/aws_test.sh cycle      run the tests whose name contains "cycle"
set -uo pipefail
cd "$(dirname "$0")/.."
SCRIPT="$PWD/scripts/aws.sh"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
STUBS="$WORK/stubs"
mkdir -p "$STUBS"

# ---------------------------------------------------------------- stand-ins
#
# The pretend state is a directory of small files ($STATE):
#   arn            who is signed in; absent means the sign-in has expired
#   data, app      each stack's status; absent means the stack does not exist
#   mode           the app stack's Mode output
#   docker-down    present means Docker is not running
#   unset-<name>   present means that secret still holds its placeholder
#   worker-llm     the provider in the worker's startup line
#   e2e-fail       present means the end-to-end suite fails
#   leftovers      a number of resources still tagged with the app stack
# Every call is appended to $STATE/calls as one line.

cat >"$STUBS/aws" <<'STUB'
#!/usr/bin/env bash
echo "aws $*" >>"$STATE/calls"
args=" $* "
stack=""
case "$args" in *" MonsterTheatreData "*) stack=data ;; *" MonsterTheatreApp "*) stack=app ;; esac
case "$args" in
*" sts get-caller-identity "*)
  [ -f "$STATE/arn" ] || { echo "Your session has expired. Please reauthenticate using 'aws login'." >&2; exit 255; }
  cat "$STATE/arn" ;;
*" cloudformation describe-stacks "*)
  [ -f "$STATE/$stack" ] || { echo "Stack with id does not exist" >&2; exit 254; }
  case "$args" in
  *StackStatus*) cat "$STATE/$stack" ;;
  *CreationTime*) echo "2026-10-13T00:00:00+00:00" ;;
  *"'Mode'"*) cat "$STATE/mode" ;;
  *"'Url'"*) echo "https://dexample.cloudfront.net" ;;
  *"'ClusterName'"*) echo "MonsterTheatreApp-Cluster" ;;
  *"'DsqlEndpoint'"*) echo "abcdefghijklmnopqrstuvwxyz.dsql.ap-southeast-1.on.aws" ;;
  *"'BucketName'"*) echo "the-bucket" ;;
  *"'ServerRoleArn'"*) echo "arn:aws:iam::123456789012:role/Server" ;;
  *"'WorkerRoleArn'"*) echo "arn:aws:iam::123456789012:role/Worker" ;;
  esac ;;
*" secretsmanager describe-secret "*)
  name="$(echo "$args" | sed -E 's/.*monster-theatre\/([a-z-]+).*/\1/')"
  if [ -f "$STATE/unset-$name" ]; then echo "1"; else echo "2"; fi ;;
*" ecs list-services "*) echo "arn:a arn:b arn:c" ;;
*" ecs describe-services "*) printf 'Server\t1\t1\nWorker\t1\t1\nWeb\t1\t1\n' ;;

*" logs filter-log-events "* | *" logs tail "*)
  llm="$(cat "$STATE/worker-llm" 2>/dev/null || echo fake)"
  # An earlier worker's line too, when the state has one. It comes last,
  # as the real command returned it: one result for each page of events,
  # in no useful order.
  if [ -f "$STATE/worker-llm-earlier" ]; then
    old="$(cat "$STATE/worker-llm-earlier")"
    later="{\"time\":\"2026-10-13T05:00:00Z\",\"level\":\"INFO\",\"msg\":\"worker started\",\"llm_provider\":\"$llm\",\"tts_provider\":\"$llm\",\"llm_model\":\"m\",\"tts_model\":\"t\",\"vote_window\":\"2s\"}"
    earlier="{\"time\":\"2026-10-12T23:00:00Z\",\"level\":\"INFO\",\"msg\":\"worker started\",\"llm_provider\":\"$old\",\"tts_provider\":\"$old\",\"llm_model\":\"m\",\"tts_model\":\"t\",\"vote_window\":\"10s\"}"
    printf '%s\n%s\n' "$later" "$earlier"
    exit 0
  fi
  echo "2026-10-13T00:00:00 {\"level\":\"INFO\",\"msg\":\"worker started\",\"llm_provider\":\"$llm\",\"tts_provider\":\"$llm\",\"llm_model\":\"m\",\"tts_model\":\"t\",\"vote_window\":\"2s\"}" ;;
# What destroy-app counts afterwards. "leftovers" stands for load balancers.
*" resourcegroupstaggingapi "*) echo 2 ;; # lists deleted clusters for a day; must not be relied on
*" elbv2 describe-load-balancers "*) cat "$STATE/leftovers" 2>/dev/null || echo 0 ;;
*" elasticache describe-replication-groups "* | *" ecs list-clusters "* | *" ec2 describe-vpcs "* | *" cloudfront list-distributions "*) echo 0 ;;
*" logs describe-log-groups "*"length("*) echo 0 ;;
*" logs describe-log-groups "*) echo "MonsterTheatreApp-Logs" ;;
*" s3 ls "*) echo "" ;;
esac
exit 0
STUB

cat >"$STUBS/npx" <<'STUB'
#!/usr/bin/env bash
echo "npx $*" >>"$STATE/calls"
args=" $* "
case "$args" in
*" cdk deploy MonsterTheatreData "*) echo CREATE_COMPLETE >"$STATE/data" ;;
*" cdk deploy MonsterTheatreApp "*)
  echo CREATE_COMPLETE >"$STATE/app"
  case "$args" in *"mode=live"*) echo live >"$STATE/mode"; echo anthropic >"$STATE/worker-llm" ;; *) echo test >"$STATE/mode"; echo fake >"$STATE/worker-llm" ;; esac ;;
*" cdk destroy MonsterTheatreApp "*) rm -f "$STATE/app" "$STATE/mode" ;;
*" cdk destroy MonsterTheatreData "*) rm -f "$STATE/data" ;;
*" cdk diff "*) echo "Number of stacks with differences: 0" ;;
esac
exit 0
STUB

cat >"$STUBS/go" <<'STUB'
#!/usr/bin/env bash
# The settings that matter to each command are recorded with the call.
echo "go $* [DSQL_ENDPOINT=${DSQL_ENDPOINT:-} S3_BUCKET=${S3_BUCKET:-} E2E_BASE_URL=${E2E_BASE_URL:-} E2E_DSQL_ENDPOINT=${E2E_DSQL_ENDPOINT:-} E2E_S3_BUCKET=${E2E_S3_BUCKET:-}]" >>"$STATE/calls"
case " $* " in
*" test "*)
  if [ -f "$STATE/e2e-fail" ]; then echo "--- FAIL: TestSomething (0.10s)"; echo FAIL; exit 1; fi
  echo "--- PASS: TestSomething (0.10s)"; echo "--- SKIP: TestOther (0.00s)"; echo "ok" ;;
esac
exit 0
STUB

cat >"$STUBS/docker" <<'STUB'
#!/usr/bin/env bash
echo "docker $*" >>"$STATE/calls"
[ ! -f "$STATE/docker-down" ]
STUB

cat >"$STUBS/curl" <<'STUB'
#!/usr/bin/env bash
echo "curl $*" >>"$STATE/calls"
case " $* " in *"/login"*) echo 200 ;; *"/api/v1/director/me"*) echo 401 ;; *"/healthz"*) echo 404 ;; esac
STUB
chmod +x "$STUBS"/*

# ------------------------------------------------------------------ harness

fresh() {
  STATE="$(mktemp -d "$WORK/state.XXXXXX")"
  export STATE
  echo "arn:aws:iam::123456789012:user/mt-admin" >"$STATE/arn"
  echo CREATE_COMPLETE >"$STATE/data"
  : >"$STATE/calls"
}

# app_up MODE puts a deployed app stack into the pretend state.
app_up() {
  echo CREATE_COMPLETE >"$STATE/app"
  echo "$1" >"$STATE/mode"
  if [ "$1" = live ]; then echo anthropic >"$STATE/worker-llm"; else echo fake >"$STATE/worker-llm"; fi
}

run() {
  OUT="$(PATH="$STUBS:$PATH" AWS_SH_POLL_SECONDS=0 AWS_SH_CONFIG=config.example.json "$SCRIPT" "$@" 2>&1)"
  CODE=$?
}

FAILED=0
CURRENT=""
fail() { FAILED=1; echo "  FAIL [$CURRENT] $1"; echo "$OUT" | sed 's/^/      | /' | tail -12; }
expect_ok() { [ "$CODE" -eq 0 ] || fail "expected success, got exit $CODE"; }
expect_refused() { [ "$CODE" -ne 0 ] || fail "expected a refusal, but it succeeded"; echo "$OUT" | grep -qi -- "$1" || fail "expected the message to mention: $1"; }
expect_called() { grep -q -- "$1" "$STATE/calls" || fail "expected a call matching: $1"; }
expect_not_called() { ! grep -q -- "$1" "$STATE/calls" || fail "expected no call matching: $1"; }
expect_output() { echo "$OUT" | grep -q -- "$1" || fail "expected output matching: $1"; }
# expect_order A B: a call matching A comes before one matching B.
expect_order() {
  local a b
  a="$(grep -n -- "$1" "$STATE/calls" | head -1 | cut -d: -f1)"
  b="$(grep -n -- "$2" "$STATE/calls" | tail -1 | cut -d: -f1)"
  { [ -n "$a" ] && [ -n "$b" ] && [ "$a" -lt "$b" ]; } || fail "expected '$1' before '$2'"
}

# -------------------------------------------------------------------- tests

test_root_is_refused() {
  fresh; echo "arn:aws:iam::123456789012:root" >"$STATE/arn"
  for command in status "deploy-app test" destroy-app deploy-data e2e "clear-data test"; do
    # shellcheck disable=SC2086
    run $command
    expect_refused "root"
  done
  expect_not_called "npx cdk"
  expect_not_called "^go "
}

test_an_expired_sign_in_is_refused_with_what_to_do() {
  fresh; rm "$STATE/arn"
  run deploy-app test
  expect_refused "aws login --profile"
  expect_not_called "npx cdk"
}

test_deploy_data_deploys_then_migrates_then_grants() {
  fresh
  run deploy-data
  expect_ok
  expect_order "npx cdk deploy MonsterTheatreData" "go run ./cmd/migrate up"
  expect_order "go run ./cmd/migrate up" "go run ./cmd/migrate grant arn:aws:iam::123456789012:role/Server arn:aws:iam::123456789012:role/Worker"
  expect_called "migrate up \[DSQL_ENDPOINT=abcdefghijklmnopqrstuvwxyz.dsql.ap-southeast-1.on.aws"
}

test_deploy_data_says_which_keys_are_still_placeholders() {
  fresh; touch "$STATE/unset-anthropic-api-key" "$STATE/unset-wavespeed-api-key"
  run deploy-data
  expect_ok
  expect_output "anthropic-api-key.*placeholder"
  expect_output "wavespeed-api-key.*placeholder"
}

test_deploy_app_test_deploys_in_test_mode_then_verifies() {
  fresh
  run deploy-app test
  expect_ok
  expect_called "npx cdk deploy MonsterTheatreApp -c mode=test"
  expect_order "npx cdk deploy MonsterTheatreApp" "curl .*/login"
  expect_called "curl .*/api/v1/director/me"
  expect_called "aws ecs describe-services"
  expect_called "npx cdk diff .*mode=test"
  expect_output "llm_provider.*fake"
}

test_deploy_app_live_deploys_in_live_mode_and_shows_the_providers() {
  fresh
  run deploy-app live
  expect_ok
  expect_called "npx cdk deploy MonsterTheatreApp -c mode=live"
  expect_called "npx cdk diff .*mode=live"
  expect_output "llm_provider.*anthropic"
  expect_output "No performance has been started"
}

test_deploy_app_needs_a_mode() {
  fresh
  run deploy-app
  expect_refused "test.*live"
  run deploy-app production
  expect_refused "test.*live"
  expect_not_called "npx cdk deploy"
}

test_deploy_app_is_refused_without_the_data_stack() {
  fresh; rm "$STATE/data"
  run deploy-app test
  expect_refused "deploy-data"
  expect_not_called "npx cdk deploy"
}

test_deploy_app_is_refused_when_docker_is_down() {
  fresh; touch "$STATE/docker-down"
  run deploy-app test
  expect_refused "Docker"
  expect_not_called "npx cdk deploy"
}

test_deploy_app_is_refused_while_the_temporal_key_is_a_placeholder() {
  fresh; touch "$STATE/unset-temporal-api-key"
  run deploy-app test
  expect_refused "temporal-api-key"
  expect_not_called "npx cdk deploy"
}

test_live_needs_the_provider_keys_and_test_does_not() {
  fresh; touch "$STATE/unset-elevenlabs-api-key"
  run deploy-app live
  expect_refused "elevenlabs-api-key"
  expect_not_called "npx cdk deploy"
  run deploy-app test
  expect_ok
}

test_live_needs_the_wavespeed_key_too() {
  fresh; touch "$STATE/unset-wavespeed-api-key"
  run deploy-app live
  expect_refused "wavespeed-api-key"
  expect_not_called "npx cdk deploy"
  run deploy-app test
  expect_ok
}

test_deploy_app_is_refused_while_a_stack_is_busy() {
  fresh; echo UPDATE_IN_PROGRESS >"$STATE/app"; echo test >"$STATE/mode"
  run deploy-app live
  expect_refused "IN_PROGRESS"
  expect_not_called "npx cdk deploy"
}

test_verify_fails_when_the_worker_is_not_in_the_mode_deployed() {
  fresh; app_up live; echo fake >"$STATE/worker-llm"
  run verify
  expect_refused "provider"
}

# After a switch of mode the log holds the earlier worker's startup line as
# well. The newest is the one that counts. (Found on the first real run: the
# two were read as one provider, "anthropic fake".)
test_verify_reads_the_newest_worker_line() {
  fresh; app_up test; echo anthropic >"$STATE/worker-llm-earlier"
  run verify
  expect_ok
  expect_output "llm_provider: fake "
  expect_output "vote_window:  2s"
}

test_e2e_runs_against_a_test_deployment_with_its_addresses() {
  fresh; app_up test
  run e2e
  expect_ok
  expect_called "go test .* \[.*E2E_BASE_URL=https://dexample.cloudfront.net E2E_DSQL_ENDPOINT=abcdefghijklmnopqrstuvwxyz.dsql.ap-southeast-1.on.aws E2E_S3_BUCKET=the-bucket\]"
  expect_output "passed: 1"
  expect_output "skipped: 1"
}

test_e2e_is_refused_in_live_mode() {
  fresh; app_up live
  run e2e
  expect_refused "live"
  expect_not_called "go test"
}

test_e2e_is_refused_when_nothing_is_deployed() {
  fresh
  run e2e
  expect_refused "deploy-app test"
  expect_not_called "go test"
}

test_e2e_reports_failure() {
  fresh; app_up test; touch "$STATE/e2e-fail"
  run e2e
  [ "$CODE" -ne 0 ] || fail "a failing suite must fail the command"
  expect_output "failed: 1"
}

test_destroy_app_destroys_and_checks_nothing_is_left() {
  fresh; app_up test
  run destroy-app
  expect_ok
  expect_order "npx cdk destroy MonsterTheatreApp --force" "aws elbv2 describe-load-balancers"
  for service in "elasticache describe-replication-groups" "ecs list-clusters" "ec2 describe-vpcs" "logs describe-log-groups" "cloudfront list-distributions"; do
    expect_called "aws $service"
  done
  # The tagging service goes on listing deleted clusters; the first real
  # teardown failed its check because of it.
  expect_not_called "resourcegroupstaggingapi"
  expect_output "nothing is left"
}

test_destroy_app_fails_if_something_is_left() {
  fresh; app_up test; echo 2 >"$STATE/leftovers"
  run destroy-app
  expect_refused "load balancers: *2"
}

test_destroy_app_with_nothing_deployed_does_nothing() {
  fresh
  run destroy-app
  expect_ok
  expect_not_called "npx cdk destroy"
}

test_destroy_data_needs_its_flag() {
  fresh
  run destroy-data
  expect_refused "yes-delete-everything"
  expect_not_called "npx cdk destroy"
  expect_not_called "delete-secret"
}

test_destroy_data_is_refused_while_the_app_stack_exists() {
  fresh; app_up test
  run destroy-data --yes-delete-everything
  expect_refused "destroy-app"
  expect_not_called "npx cdk destroy"
}

test_destroy_data_removes_protections_then_the_stack_then_the_secrets() {
  fresh
  run destroy-data --yes-delete-everything
  expect_ok
  expect_order "aws cloudformation update-termination-protection --no-enable-termination-protection" "npx cdk destroy MonsterTheatreData --force"
  expect_order "aws dsql update-cluster --identifier abcdefghijklmnopqrstuvwxyz --no-deletion-protection-enabled" "npx cdk destroy MonsterTheatreData --force"
  for name in anthropic-api-key elevenlabs-api-key wavespeed-api-key temporal-api-key share-link-secret; do
    expect_order "npx cdk destroy MonsterTheatreData" "aws secretsmanager delete-secret .*monster-theatre/$name .*--force-delete-without-recovery"
  done
}

test_clear_data_is_a_dry_run_unless_told() {
  fresh
  run clear-data test
  expect_ok
  expect_called "go run ./cmd/dbadmin clear -scope test \[DSQL_ENDPOINT=abcdefghijklmnopqrstuvwxyz.dsql.ap-southeast-1.on.aws S3_BUCKET=the-bucket"
  expect_not_called "dbadmin clear .*-yes"
  run clear-data rooms --yes
  expect_ok
  expect_called "go run ./cmd/dbadmin clear -scope rooms -yes"
}

test_clear_data_all_needs_the_stronger_flag() {
  fresh
  run clear-data all --yes
  expect_refused "yes-delete-everything"
  expect_not_called "dbadmin clear -scope all -yes"
  run clear-data all
  expect_ok
  expect_called "go run ./cmd/dbadmin clear -scope all \["
  run clear-data all --yes-delete-everything
  expect_ok
  expect_called "go run ./cmd/dbadmin clear -scope all -yes"
}

test_clear_data_needs_a_known_scope() {
  fresh
  run clear-data
  expect_refused "test.*rooms.*all"
  run clear-data everything --yes
  expect_refused "test.*rooms.*all"
  expect_not_called "dbadmin"
}

test_cycle_switches_in_place_by_default() {
  fresh
  run cycle
  expect_ok
  expect_order "npx cdk deploy MonsterTheatreApp -c mode=test" "go test"
  expect_order "go test" "go run ./cmd/dbadmin clear -scope test -yes"
  expect_order "go run ./cmd/dbadmin clear -scope test -yes" "npx cdk deploy MonsterTheatreApp -c mode=live"
  expect_not_called "npx cdk destroy"
  expect_output "llm_provider.*anthropic"
  expect_output "No performance has been started"
}

test_cycle_can_tear_down_in_between() {
  fresh
  run cycle --teardown-between
  expect_ok
  expect_order "go test" "npx cdk destroy MonsterTheatreApp --force"
  expect_order "npx cdk destroy MonsterTheatreApp --force" "npx cdk deploy MonsterTheatreApp -c mode=live"
}

test_cycle_stops_when_the_tests_fail() {
  fresh; touch "$STATE/e2e-fail"
  run cycle
  [ "$CODE" -ne 0 ] || fail "a failing suite must stop the cycle"
  expect_called "npx cdk deploy MonsterTheatreApp -c mode=test"
  expect_not_called "mode=live"
  expect_not_called "dbadmin clear"
  expect_not_called "npx cdk destroy"
  expect_output "still up in test mode"
}

test_status_reports_without_changing_anything() {
  fresh; app_up live; touch "$STATE/unset-anthropic-api-key" "$STATE/unset-wavespeed-api-key"
  run status
  expect_ok
  expect_output "wavespeed-api-key.*placeholder"
  expect_output "mt-admin"
  expect_output "MonsterTheatreApp.*CREATE_COMPLETE"
  expect_output "live"
  expect_output "https://dexample.cloudfront.net"
  expect_output "anthropic-api-key.*placeholder"
  expect_output "make director"
  expect_not_called "npx cdk deploy"
  expect_not_called "npx cdk destroy"
  expect_not_called "get-secret-value"
}

test_no_command_reads_a_secret_value() {
  fresh; app_up test
  for command in status deploy-data "deploy-app test" verify e2e "clear-data test" destroy-app; do
    # shellcheck disable=SC2086
    run $command
  done
  expect_not_called "get-secret-value"
  expect_not_called "batch-get-secret-value"
}

test_an_unknown_command_lists_the_commands() {
  fresh
  run frobnicate
  expect_refused "deploy-app"
}

# --------------------------------------------------------------------- run

[ -x "$SCRIPT" ] || { echo "FAIL: $SCRIPT does not exist or is not executable"; exit 1; }
count=0
for test_name in $(declare -F | awk '{print $3}' | grep '^test_'); do
  case "$test_name" in *"${1:-}"*) ;; *) continue ;; esac
  CURRENT="$test_name"
  before=$FAILED; FAILED=0
  "$test_name"
  if [ "$FAILED" -eq 0 ]; then echo "ok   $test_name"; else echo "FAIL $test_name"; fi
  [ "$FAILED" -eq 0 ] && FAILED=$before || FAILED=1
  count=$((count + 1))
done
echo
if [ "$FAILED" -eq 0 ]; then echo "$count tests passed"; else echo "some of $count tests FAILED"; exit 1; fi
