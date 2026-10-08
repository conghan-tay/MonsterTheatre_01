#!/usr/bin/env bash
# Runs the Go tests in a directory, printing one line per test and a final
# count of tests run, passed, failed, and skipped.
#
# Usage: scripts/test-report.sh <module-dir> [extra go test flags...]
#
# Subtests count as tests. The exit code is that of the test run.
set -uo pipefail

GOTESTSUM_VERSION="v1.13.0"

if [ $# -lt 1 ]; then
  echo "usage: $0 <module-dir> [extra go test flags...]" >&2
  exit 2
fi
dir="$1"
shift

events="$(mktemp)"
trap 'rm -f "$events"' EXIT

# gotestsum prints the per-test lines and the output of any failed test, and
# writes the raw test events to $events for the summary below.
(
  cd "$dir" &&
    go run "gotest.tools/gotestsum@${GOTESTSUM_VERSION}" \
      --format testname --jsonfile "$events" -- ./... "$@"
)
status=$?

# One event per line. Events for a test (not a whole package) carry "Test".
count() {
  grep -c "\"Action\":\"$1\".*\"Test\":" "$events" || true
}

echo
echo "Tests run: $(count run)   passed: $(count pass)   failed: $(count fail)   skipped: $(count skip)"
exit "$status"
