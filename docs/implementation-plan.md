# Implementation Plan

Status: **plan only, no code yet.** Everything here follows the agreed API spec ([README](README.md), 21 endpoints) and [data model](data-model.md). Versions of tools/libraries are picked when the project starts (check for the latest stable then).

Target: one Go binary + PostgreSQL + a reverse proxy on a single **2 core / 2 GB RAM VPS**, for a personal project with a handful of users.

## 1. Stack

| Concern | Choice | Why |
|---------|--------|-----|
| Language | Go, latest stable at project start; pin in `go.mod` (`go` + `toolchain` lines) and CI | requirement |
| HTTP | standard library `net/http` with its built-in method + path routing (`PUT /v1/progress/{id}`) | no framework needed for 21 routes; fewer dependencies |
| Database | PostgreSQL, latest stable major | requirement |
| DB driver | `pgx` v5 with `pgxpool`, hand-written SQL in a store layer | fast, explicit SQL, transactions are simple; `sqlc` can be added later if SQL grows |
| Migrations | plain numbered SQL files, applied by the binary (`migrate` subcommand) using a small library such as `goose` or `golang-migrate` | reproducible schema, runs in deploy |
| Passwords | argon2id (`golang.org/x/crypto/argon2`) | agreed |
| IDs | UUID v7 (library such as `github.com/google/uuid`) | time-ordered, index friendly |
| Config | environment variables only, validated at startup | 12-factor, simple with systemd/Docker |
| Logging | standard `log/slog`, JSON to stdout | journald/Docker collects it |
| Tests | standard `testing` + a real Postgres (docker, or `testcontainers-go`) | integration tests matter more than mocks here |
| Lint/CI | `go vet`, `staticcheck` or `golangci-lint`, `go test -race` | |

Dependency budget is deliberately small: `pgx`, a migration lib, `x/crypto`, a UUID lib. Everything else is stdlib.

## 2. Project layout

```
workout-tracker-be/
├── cmd/
│   └── server/main.go          # single binary; subcommands: serve, migrate, admin ..., media gc
├── internal/
│   ├── config/                 # env parsing + validation
│   ├── httpapi/                # router, middleware, handlers, request/response DTOs, error mapping
│   │   ├── middleware/         # recover, request id, logging, body limit, rate limit, auth, role
│   │   └── handlers/           # one file per resource: auth, admin, exercises, plans, progress
│   ├── service/                # business rules: auth, exercises, plans, progress (conflict rule lives here)
│   ├── store/                  # pgx queries, transactions; one file per table group
│   ├── auth/                   # password hashing, token generation/hashing, login rate limiter
│   ├── media/                  # disk storage for exercise images (atomic write, gc), image sniffing
│   ├── domain/                 # core types, enums, validation helpers, error kinds
│   └── clock/                  # time source interface (fake clock in tests)
├── migrations/                 # 0001_users.sql, 0002_auth_tokens.sql, ...
├── docs/                       # this plan + API spec
├── deploy/                     # Caddyfile, systemd unit, backup script, env example
├── Makefile                    # build, test, lint, migrate, run
└── go.mod
```

Layering rule: `httpapi` → `service` → `store`. Handlers only decode/encode and map errors; services hold rules (validation, conflict rule, limits); store holds SQL. `domain` is imported by everything and imports nothing internal.

## 3. Configuration (environment)

| Variable | Example | Notes |
|----------|---------|-------|
| `APP_ENV` | `production` | `development` allows relaxed defaults |
| `HTTP_ADDR` | `127.0.0.1:8080` | listens only on localhost behind the proxy |
| `DATABASE_URL` | `postgres://...` | |
| `DB_MAX_CONNS` | `10` | keeps Postgres memory small |
| `MEDIA_DIR` | `/var/lib/workout-tracker/media` | persistent, backed up |
| `MEDIA_BASE_URL` | `https://api.example.com` | prefix for `image_url`; change when a CDN is added |
| `TRUSTED_PROXY_CIDRS` | `127.0.0.1/32` | only these may set `X-Forwarded-For` (client IP for rate limits) |
| `TOKEN_TTL_DAYS` | `365` | |
| `LOGIN_MAX_FAILS_USER` / `_IP` / `LOGIN_LOCK_MINUTES` | `5` / `20` / `15` | brute-force limits |
| `ARGON2_MEMORY_KIB` / `ARGON2_TIME` / `ARGON2_PARALLELISM` | `65536` / `2` / `1` | tuned to ~100 ms on the VPS |
| `ARGON2_MAX_CONCURRENT` | `2` | caps parallel hashing, see below |
| `LOG_LEVEL` | `info` | |

Startup fails with a clear message if a required variable is missing or invalid.

## 4. Request pipeline

Order of middleware, outermost first:

1. **Recover**: panic → 500 `internal`, logged with stack, no internals leaked.
2. **Request ID**: generate/propagate `X-Request-Id`, add to logs.
3. **Access log**: method, path, status, duration, user id (if known). Never log bodies, passwords or tokens.
4. **Body limit**: 1 MiB, 2 MiB on the image route.
5. **Rate limit** (per IP, generic) for auth/admin routes.
6. **Auth** (except login): parse bearer → SHA-256 → look up token → check `expires_at`, `revoked_at` → attach user id + role; lazily update `last_used_at` (at most hourly).
7. **Role check** per route from the access matrix in [conventions](api/conventions.md#roles--access). Wrong role → 403.
8. **Handler**.

Error handling: services return typed errors (`NotFound`, `Conflict{issue, details}`, `Validation{field issues}`, `Forbidden`, `Unauthorized`, `RateLimited`); one place in `httpapi` maps them to the JSON error format and status codes from [conventions](api/conventions.md#error-format). Unknown errors → 500.

JSON decoding is strict (unknown fields rejected). Ownership rule: a resource owned by another user is reported as 404.

## 5. Key implementation notes (the non-obvious parts)

**Idempotent upsert with conflict rule** (progress, plans). One transaction:
1. Lock/select the row by `id` (`FOR UPDATE`).
2. Apply the [conflict rule](api/endpoints/03-save-progress.md#conflict-rule): missing → insert; deleted → 409; request older → 409 with current copy; equal → no-op; newer → update.
3. Replace child rows (delete + insert), set `server_updated_at = now()`.
4. Return the stored object.
Also check the row is not owned by another user (→ 404) before anything else. Concurrent inserts of the same id: rely on the PK; retry once on unique violation.

**Sync feed** (`updated_since`). Order by `(server_updated_at, id)`; cursor encodes that pair (opaque, base64). Timestamps compared at microsecond precision; never round trip through floats.

**Login and password hashing on a 2 GB box.** argon2id at 64 MiB uses that much RAM per concurrent hash. Cap concurrency with a semaphore (`ARGON2_MAX_CONCURRENT`, default 2 → at most ~128 MiB), and queue or return 429 beyond it. Tune time/memory so one hash takes ~100 ms on the VPS. Unknown username → hash against a dummy hash (timing-safe), see [02-login](api/endpoints/02-login.md#timing-safe-verification).

**Login rate limiter**: in-memory map keyed by username and by IP, sliding 15-minute window, periodic cleanup of old keys. Single instance only (documented limitation).

**Tokens**: 32 random bytes, `wt_` prefix, base64url; store SHA-256; `crypto/rand` only.

**Exercise images**: read the body with a size cap, sniff type from bytes, decode config for dimensions (no full decode), hash, write temp file → fsync → rename into `MEDIA_DIR/exercises/{id}/{hash}.{ext}`, update DB row in a transaction, then delete the old file. `media gc` subcommand removes files not referenced by the DB.

**Name uniqueness for exercises**: rely on the partial unique index; translate the unique violation into 409 `already_exists` and look up `existing_id`.

**Enums and limits** live once in `domain` (single source used for validation and for the DB CHECK constraints in migrations).

## 6. Migrations

- One SQL file per step in `migrations/`, forward-only in production (`down` files optional for local use).
- Order as in [data-model](data-model.md#migration-order).
- `server migrate` runs pending migrations; the systemd unit runs it before `serve` on deploy (or `serve` refuses to start if the schema is behind).
- Never edit an applied migration; add a new one. Test each migration against an empty database and against the previous version in CI.

## 7. Admin CLI (same binary)

| Command | Purpose |
|---------|---------|
| `server serve` | run the HTTP server |
| `server migrate up` | apply migrations |
| `server admin create-user --username U --password P [--role admin]` | bootstrap the first admin; may bypass the reserved-name list |
| `server admin reset-password --username U --password P` | emergency reset; also revokes all that user's tokens |
| `server media gc` | delete unreferenced image files |

The password is read from a prompt or stdin when not given as a flag, so it does not end up in shell history.

## 8. Deployment on the VPS (2 core / 2 GB)

- **Processes:** Caddy (reverse proxy, automatic HTTPS) → Go binary on `127.0.0.1:8080`; PostgreSQL on the same host, listening on localhost only.
- **Run the Go binary** under systemd (restart on failure, env file with restricted permissions) or Docker Compose; pick one at setup. Single instance (rate limiter is in-memory).
- **Caddy config:**
  - `/media/*` served straight from `MEDIA_DIR`, no directory listing, headers `Cache-Control: public, max-age=31536000, immutable` and `X-Content-Type-Options: nosniff`.
  - Everything else proxied to the Go app; sets `X-Forwarded-For`.
- **Postgres tuning for 2 GB** (starting points, adjust after measuring): `shared_buffers` ≈ 256 MB, `effective_cache_size` ≈ 1 GB, `work_mem` 4 MB, `max_connections` 20 (app uses ≤ 10). Leaves room for Go (~100–200 MB), Caddy, and the OS.
- **Firewall:** allow 22 (key-only SSH), 80, 443; block everything else, including 5432.
- **Backups (do before real use):**
  - nightly `pg_dump` (compressed) + copy of `MEDIA_DIR`, sent **off the VPS** (another machine or object storage), keep ~14 days.
  - Do a test restore once before relying on it.
- **Health:** `GET /healthz` (process up + DB ping), used by an external uptime check. Logs go to journald or Docker; rotate.
- **Swap:** a small swap file (1 GB) as a safety net against out-of-memory kills.
- **HTTPS is mandatory**: tokens travel in headers and passwords in login bodies.

## 9. Testing strategy

| Level | What |
|-------|------|
| Unit | validation rules and enums; conflict-rule decision function (table-driven: missing/deleted/older/equal/newer); cursor encode/decode; login limiter; token generation/hashing; image sniffing |
| Integration (real Postgres) | every endpoint: happy path + each error code in its spec; role matrix (user vs admin vs anonymous on every route); ownership (user B cannot see user A's data, gets 404) |
| Sync scenarios | two "phones" for one user: offline upload, stale write → 409, retry after lost response → 200 no-op, delete on one phone then pull on the other, exercise deleted then referenced by an offline save |
| Auth | revoke by token id, revoke others/all, admin revoke-login, admin change-password revokes tokens, expired token, brute-force lock (with a fake clock) |
| Media | upload/replace/delete, wrong type, oversize, corrupt image, crash-safety (DB never points at a missing file) |
| Race | `go test -race`; concurrent PUT of the same id |

A fake clock (`internal/clock`) makes expiry and rate-limit tests deterministic.

## 10. Build order and milestones

Each milestone ends with tests passing and the listed endpoints matching their spec.

| # | Milestone | Endpoints | Done when |
|---|-----------|-----------|-----------|
| 0 | Skeleton | `/healthz` | repo layout, config, logging, DB pool, migration runner, CI (vet, lint, test), Makefile; deploys to the VPS behind Caddy |
| 1 | Users + auth core | 2 login, 11 logout, 15 list-tokens, 12 revoke | users + auth_tokens migrations; argon2id, opaque tokens, auth + role middleware, brute-force limiter, timing-safe login; CLI `create-user` and `reset-password` |
| 2 | Admin endpoints | 1 register, 13 admin-revoke-login, 14 admin-change-password | admin can manage users; role matrix tests for all routes so far |
| 3 | Exercise master | 9 create, 8 list, 18 update, 19 delete | enums in `domain` + CHECKs, name uniqueness, sync via `updated_since`, trigram search |
| 4 | Exercise images | 20 set image, 21 delete image | disk storage, atomic writes, `image_url`, Caddy `/media/`, `media gc`, backups include `MEDIA_DIR` |
| 5 | Workout plans | 10 save, 7 get, 6 list, 17 delete | conflict rule, replace-children, 100-plan cap, `expand=exercises` |
| 6 | Progress | 3 save, 4 get, 5 list, 16 delete | same engine as plans (share the conflict-rule code), summaries and `expand`, sync scenario tests |
| 7 | Hardening | | generic rate limits, request-size limits verified, log review, backup + restore drill, load smoke test on the VPS, token purge job |
| 8 | Handover (optional) | | machine-readable API description (OpenAPI) generated from the specs for the mobile app, example requests |

Why this order: auth first (everything depends on it), then admin (so real users can be created), then exercises (plans and progress reference them), plans before progress (progress may reference a plan). Milestones 5 and 6 share most logic, so 6 is quick once 5 is done.

## 11. Risks and mitigations

| Risk | Mitigation |
|------|-----------|
| RAM pressure from argon2 or image handling | concurrency cap on hashing; no server-side image processing; 2 MiB cap; small DB pool; swap safety net |
| Lost data (VPS failure) | off-box nightly backups of DB + `MEDIA_DIR`, tested restore |
| Token or password leak in logs | never log bodies or `Authorization`; tests assert error responses and logs contain no secrets |
| Weak passwords allowed (`123`) | strong brute-force limits, admin-only registration, HTTPS only |
| Clock drift between phones affects conflict rule | 5-minute future-time rejection; equal/older handling documented; app should use network time when possible |
| Enum lists change | app tolerates unknown values; new values added via migration + deploy |
| Any user can delete any exercise | accepted for trusted users; revisit if more people join |
| In-memory rate limiter resets on restart / single instance | acceptable for one VPS; move to Postgres if scaling out |

## 12. Out of scope for v1

Email, self-service password change, public sign-up, refresh tokens, GIF/video, CDN, shared or template plans, plan days/sections, body measurements, personal records, push notifications, admin API beyond the three actions, audit history, undo for deletes, multi-instance deployment.

## 13. Decisions log for the build phase (defaults; change before starting)

- Router: standard library, not a third-party framework.
- Data access: `pgx` + plain SQL, not an ORM.
- Config: environment variables only.
- Process manager: systemd or Docker Compose (decide when provisioning the VPS).
- Migration tool: `goose` or `golang-migrate` (either is fine; pick at M0).
