GO ?= go

# go.mod pins a newer Go than many machines have installed. Let the go command
# fetch the pinned toolchain even if the user's go env defaults to "local".
export GOTOOLCHAIN ?= auto

# Development defaults for `make run`, `make migrate` and `make dev-db`. They
# apply only when the variable is not already set in the environment or on the
# make command line. The development database lives in the same throwaway
# PostgreSQL as the tests (docker-compose.test.yml, tmpfs, gone on `make
# db-down`): `make dev-db` recreates it. DEV_DB_NAME must match DATABASE_URL.
DEV_DB_NAME  ?= workout_dev
DATABASE_URL ?= postgres://postgres:postgres@localhost:55432/$(DEV_DB_NAME)?sslmode=disable
MEDIA_DIR    ?= ./var/media

# Environment for the server in development. APP_ENV is always development
# here: production settings (absolute MEDIA_DIR, https MEDIA_BASE_URL) are for
# the systemd unit, not for `make`.
DEV_ENV = APP_ENV=development DATABASE_URL='$(DATABASE_URL)' MEDIA_DIR='$(MEDIA_DIR)'

COMPOSE = docker compose -f docker-compose.test.yml

# Stamped into `server version`; falls back to "dev" outside a git checkout.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test lint fmt migrate run db-up db-down dev-db

build:
	$(GO) build -ldflags "-X main.version=$(VERSION)" -o bin/server ./cmd/server

test:
	$(GO) test -race ./...

lint:
	$(GO) vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run; else echo "golangci-lint not installed, skipped"; fi

fmt:
	$(GO) fmt ./...

# Apply pending migrations to the development database.
migrate:
	$(DEV_ENV) $(GO) run ./cmd/server migrate up

# Run the server against the development database (see dev-db).
run:
	$(DEV_ENV) $(GO) run ./cmd/server serve

# Start the PostgreSQL container (if needed), create the development database
# if it is missing and migrate it.
dev-db: db-up
	@$(COMPOSE) exec -T postgres psql -U postgres -d postgres -tAc \
		"SELECT 1 FROM pg_database WHERE datname = '$(DEV_DB_NAME)'" | grep -q 1 \
		|| $(COMPOSE) exec -T postgres createdb -U postgres $(DEV_DB_NAME)
	@$(MAKE) --no-print-directory migrate

db-up:
	$(COMPOSE) up -d --wait

db-down:
	$(COMPOSE) down
