.PHONY: dev infra infra-down migrate server worker test

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

test:
	cd server && go test ./...
