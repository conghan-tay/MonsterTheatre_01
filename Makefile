.PHONY: dev infra infra-down migrate server worker director test test-verbose e2e e2e-verbose

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
