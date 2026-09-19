# API Conventions

Applies to all endpoints. Draft.

## Basics

- Base path: `/v1`
- Content type: `application/json; charset=utf-8` (request and response)
- Auth: `Authorization: Bearer <token>` on all endpoints except register/login
- Timestamps: RFC 3339, UTC, e.g. `2026-09-19T08:30:00Z`
- IDs: UUID string (lowercase). Progress IDs are **client-generated**; user/plan/exercise IDs may be server- or client-generated (see each endpoint).
- Field naming: `snake_case`
- Units are fixed, no unit fields: weight = kg (decimal, max 2 places), distance = meters, duration = seconds.
- Unknown JSON fields in request: rejected with 400 (strict decoding) — decision to confirm.
- Max request body: 1 MiB (progress payloads are small; tune later). Exception: exercise image upload, 2 MiB.

## Error format

All non-2xx responses:

```json
{
  "error": {
    "code": "validation_failed",
    "message": "Human readable summary",
    "details": [
      { "field": "username", "issue": "invalid_format" }
    ]
  }
}
```

`details` optional, always an array of `{field?, issue, ...}` objects. A conflict may add extra keys to its first element: `current` (server copy, `stale`) or `existing_id` (`already_exists`). `code` is stable machine string; `message` may change.

| HTTP | code | When |
|------|------|------|
| 400 | `bad_request` | Malformed JSON, unknown field |
| 401 | `unauthorized` | Missing/invalid/expired token, bad credentials |
| 403 | `forbidden` | Authenticated but not allowed |
| 404 | `not_found` | Missing resource **or** resource owned by another user (no leaking existence) |
| 409 | `conflict` | Duplicate (username, exercise name), stale write (`issue: stale`), or write to a deleted row (`issue: deleted`) |
| 413 | `payload_too_large` | Body over the limit (image upload) |
| 415 | `unsupported_media_type` | Wrong `Content-Type` / file type (image upload) |
| 422 | `validation_failed` | Well-formed JSON, invalid values |
| 429 | `rate_limited` | Too many requests (auth endpoints) |
| 500 | `internal` | Unexpected; no internals leaked |

## Pagination (list endpoints)

Cursor-based (stable under concurrent writes, good for sync).

Request: `?limit=50&cursor=<opaque>` — `limit` default 50, max 200.

Response envelope:

```json
{
  "items": [ ... ],
  "next_cursor": "opaque-string-or-null"
}
```

`next_cursor = null` → last page. Cursor is opaque to clients.

## Sync semantics

For data that syncs between devices (progress, later plans):

- Every row has `created_at`, `deleted_at` (nullable, soft delete) and timestamps for change tracking. For data the phone writes (**progress, workout plans**): `updated_at` = **client's** last-modified time (used for the conflict check), `server_updated_at` = server time of the last accepted write or delete (used for sync). For server-only data (exercises): `updated_at` is server-set.
- List endpoints support `updated_since=<RFC3339>` → returns rows whose `server_updated_at` (or `updated_at` for exercises) is after that time, **including soft-deleted rows** (`deleted_at` set) so other devices can remove them locally. Optional `include_deleted=true` otherwise; default hides deleted.
- Client stores the max `server_updated_at` seen and uses it as next `updated_since`.
- Writes are idempotent upserts keyed by client-generated ID.
- Conflict: see [save-progress](endpoints/03-save-progress.md).

## Auth details

- Token TTL: 365 days from login. Response gives `expires_at`.
- Expired/invalid token → 401 `unauthorized` with header `WWW-Authenticate: Bearer`.
- Passwords: argon2id (or bcrypt cost ≥ 12). Never logged, never returned.
- Login is brute-force protected (per username and per IP), see [02-login](endpoints/02-login.md#brute-force-protection). Other auth/admin endpoints get a generic per-IP limit.

## Roles & access

Two roles: `user` and `admin`. Admin accounts are **management-only**: they cannot use workout data endpoints. Enforced by role middleware; wrong role → 403 `forbidden`.

| # | Endpoint | user | admin |
|---|----------|:----:|:-----:|
| 1 | register | 403 | ✅ |
| 2 | login | ✅ (no auth) | ✅ (no auth) |
| 3–10, 16–21 | progress, workout plans, exercises (incl. edit/delete/image) | ✅ | 403 |
| 11 | logout | ✅ | ✅ (own session) |
| 12 | revoke (own other devices) | ✅ | 403 (use 13 on own username) |
| 15 | list-tokens | ✅ | 403 |
| 13 | admin-revoke-login | 403 | ✅ |
| 14 | admin-change-password | 403 | ✅ |

Admin can call login and logout because they are needed to hold a session at all. The three real admin powers are 1, 13, 14.

## Media files (exercise images)

Static files under `/media/` are **public, unauthenticated**, served by the reverse proxy from disk (`MEDIA_DIR`) with long immutable cache headers. Not part of the JSON API. Details: [20-set-exercise-image](endpoints/20-set-exercise-image.md).

## Health / ops (not counted in the 21 endpoints, proposed)

- `GET /healthz` — liveness, no auth. Useful for deploy. Confirm if wanted.
