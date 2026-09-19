GO ?= go

# go.mod pins a newer Go than many machines have installed. Let the go command
# fetch the pinned toolchain even if the user's go env defaults to "local".
export GOTOOLCHAIN ?= auto

.PHONY: build test lint fmt migrate run db-up db-down

build:
	$(GO) build -o bin/server ./cmd/server

test:
	$(GO) test -race ./...

lint:
	$(GO) vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run; else echo "golangci-lint not installed, skipped"; fi

fmt:
	$(GO) fmt ./...

migrate:
	$(GO) run ./cmd/server migrate up

run:
	$(GO) run ./cmd/server serve

# docker-compose.test.yml is added by the test harness task (T0.3).
db-up:
	docker compose -f docker-compose.test.yml up -d --wait

db-down:
	docker compose -f docker-compose.test.yml down
