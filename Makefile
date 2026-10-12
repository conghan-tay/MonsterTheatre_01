.PHONY: dev infra infra-down migrate server worker web director test test-verbose e2e e2e-verbose web-check web-e2e web-e2e-prod

# Start everything; Ctrl-C stops everything.
dev:
	./scripts/dev.sh

# PostgreSQL and Redis only.
infra:
	docker compose up -d --wait

infra-down:
	docker compose down

migrate:
	cd server && go run ./cmd/migrate up

server:
	cd server && go run ./cmd/server

worker:
	cd server && go run ./cmd/worker

# The web app on its own, proxying /api to a server on port 8080.
web:
	cd web && API_ORIGIN=http://localhost:8080 npm run dev

# Create a director account: make director USERNAME=alice
# Prompts for the password, or reads it from DIRECTOR_PASSWORD.
director:
	cd server && go run ./cmd/adddirector -username "$(USERNAME)"

test:
	cd server && go test ./...

# The same tests, listing every test with its result and a final count.
test-verbose:
	./scripts/test-report.sh server -count=1

# End-to-end tests. Starts its own isolated stack (separate database, its own
# Temporal dev server, free ports) and tears it down afterwards. Needs Docker.
e2e:
	cd tests/e2e && go test ./... -count=1

# The same suite, listing every test with its result and a final count.
e2e-verbose:
	./scripts/test-report.sh tests/e2e -count=1

# Lint and type-check the web app.
web-check:
	cd web && npm run lint && npx tsc --noEmit

# Browser tests. Starts the same isolated stack as `make e2e`, plus the web
# app pointed at it, and tears both down. The first run downloads Chromium.
web-e2e:
	cd web && { [ -d node_modules ] || npm install; } && npx playwright install chromium \
		&& E2E_RUN_ID=$$(date +%s) npx playwright test

# The same browser tests against the production build (`next build`, then
# `next start`), which is what the web container runs.
web-e2e-prod:
	cd web && { [ -d node_modules ] || npm install; } && npx playwright install chromium \
		&& WEB_E2E_PRODUCTION=1 E2E_RUN_ID=$$(date +%s) npx playwright test
