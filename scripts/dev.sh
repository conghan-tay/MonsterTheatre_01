#!/usr/bin/env bash
# Starts the whole local stack: PostgreSQL and Redis (containers), the Temporal
# dev server, the server, and the worker. Ctrl-C stops all of it.
set -euo pipefail
cd "$(dirname "$0")/.."

if [ -f .env ]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

# The dev stack serves plain HTTP, and browsers do not send Secure cookies
# over HTTP, so login would not stick. Deployed environments leave this unset.
export COOKIE_SECURE="${COOKIE_SECURE:-false}"

pids=()
cleanup() {
  trap - INT TERM EXIT
  echo "stopping..."
  for pid in "${pids[@]:-}"; do
    [ -n "$pid" ] && kill "$pid" 2>/dev/null || true
  done
  wait 2>/dev/null || true
  docker compose stop >/dev/null 2>&1 || true
}
trap cleanup INT TERM EXIT

echo "starting PostgreSQL and Redis..."
docker compose up -d --wait

echo "starting Temporal dev server (UI: http://localhost:8233)..."
mkdir -p .temporal
temporal server start-dev --db-filename .temporal/dev.db --log-level warn &
pids+=($!)

for _ in $(seq 1 30); do
  if temporal operator cluster health >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
temporal operator cluster health >/dev/null

echo "applying migrations..."
(cd server && go run ./cmd/migrate up)

echo "building server and worker..."
(cd server && go build -o ../bin/ ./cmd/server ./cmd/worker)

./bin/server &
pids+=($!)
./bin/worker &
pids+=($!)

echo "ready: http://localhost:8080/healthz"
wait
