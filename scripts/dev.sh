#!/usr/bin/env bash
# Starts the whole local stack: PostgreSQL and Redis (containers), the Temporal
# dev server, the server, the worker, and the web app. Ctrl-C stops all of it.
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

# Local data lives under .data/ (git-ignored): uploaded portraits, and the
# secret share links are derived from. The secret is created once and kept,
# so links survive restarts; deleting the file invalidates every local link.
mkdir -p .data
export OBJECT_STORE_DIR="${OBJECT_STORE_DIR:-$PWD/.data/objects}"
if [ -z "${SHARE_LINK_SECRET:-}" ]; then
  if [ ! -f .data/share-link-secret ]; then
    (umask 077 && openssl rand -hex 32 > .data/share-link-secret)
  fi
  SHARE_LINK_SECRET="$(cat .data/share-link-secret)"
  export SHARE_LINK_SECRET
fi

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

# The web app proxies /api/* to the server, so the browser sees one origin.
if [ ! -d web/node_modules ]; then
  echo "installing web packages (first run only)..."
  (cd web && npm install)
fi
(cd web && API_ORIGIN="http://localhost:8080" exec ./node_modules/.bin/next dev --port 3000) &
pids+=($!)

echo "ready: http://localhost:3000 (API health: http://localhost:8080/healthz)"
wait
