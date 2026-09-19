# 2. login

`POST /v1/auth/login` — auth: none

Verify credentials, return bearer token valid for 1 year. Works for both roles (`user` and `admin`); the response `role` tells the app which screens to show.

Decisions (agreed): opaque token (not JWT), many active tokens per user (one per phone), brute-force limits on, timing-safe verification.

## Request

```json
{
  "username": "ihsan",
  "password": "123",
  "device_name": "Ihsan's iPhone"
}
```

| Field | Type | Required | Rules |
|-------|------|----------|-------|
| username | string | yes | trimmed, lowercased |
| password | string | yes | non-empty, max 128 bytes |
| device_name | string | no | max 100; label for the token so devices can be told apart |

## Response

**200 OK**

```json
{
  "token_id": "0195f3a2-dddd-7000-8000-000000000200",
  "access_token": "wt_Xk3p...43-chars-base64url...",
  "token_type": "Bearer",
  "expires_at": "2027-09-19T08:30:00Z",
  "user": {
    "id": "0195f3a2-7c1e-7a55-9d3b-2f6e8a1b4c90",
    "username": "ihsan",
    "role": "user"
  }
}
```

- `access_token`: shown **once**; the app stores it (secure storage / Keychain) and sends `Authorization: Bearer <access_token>`.
- `token_id`: public id of the token row, not secret. Used by [revoke](12-revoke.md).

| Status | code | Case |
|--------|------|------|
| 400 | `bad_request` | malformed JSON, unknown field |
| 401 | `unauthorized` | wrong username or password — **identical message and timing for both** |
| 422 | `validation_failed` | missing fields |
| 429 | `rate_limited` | brute-force lockout, header `Retry-After: <seconds>` |

## Token design: opaque

- Token = `wt_` + 32 random bytes (from the OS secure random source), base64url-encoded. Carries no data, it is just a random key.
- DB stores only its **SHA-256 hash** (raw token never stored; a DB leak does not leak usable tokens). SHA-256 is fine here because the token is high-entropy random, unlike passwords.
- Every request: hash incoming token → look up row → valid if `expires_at > now` and `revoked_at IS NULL` → load user + role.
- Why not JWT: a JWT is self-contained and cannot be cancelled before it expires. We need cancel (logout, admin revoke-login, admin change-password) on a 1-year token, so we'd need a server-side denylist anyway, which is a DB lookup per request, the same cost as opaque with more complexity. Opaque gives instant revoke with plain rows.
- Cost: one indexed DB lookup per request. Fine at this scale.

## Multiple phones

- Every login creates a **new** token row; existing tokens stay valid. No cap in v1.
- Same user logging in on 3 phones = 3 active tokens, each with its own `token_id` and `device_name`.
- Logout / revoke work per token id (see [11-logout](11-logout.md), [12-revoke](12-revoke.md)).

## Brute-force protection

Password can be as weak as `123`, so guessing must be slow.

| Rule | Value (proposed) |
|------|------------------|
| Failures per **username** | 5 failed attempts in 15 min → lock that username for 15 min |
| Failures per **client IP** | 20 failed attempts in 15 min → lock that IP for 15 min |
| While locked | 429 `rate_limited` + `Retry-After`, **even if the password is right** (otherwise the attacker keeps guessing through the lock) |
| Success | resets that username's failure counter |
| Unknown usernames | counted by username too, so response behavior does not reveal which usernames exist |

- Storage: in-memory counters (single server instance, personal project). Resets on restart. If more than one instance is ever run, move to a Postgres table.
- Client IP: from the TCP peer address; only trust `X-Forwarded-For` when behind a known reverse proxy (set by config).
- Known trade-off: someone who knows your username can lock you out for 15 min by failing 5 times. Accepted; you're the only user and the admin can still do things via CLI.
- Register (endpoint 1) needs an admin token so it is not brute-forceable; admin endpoints get a generic per-IP limit.

## Timing-safe verification

Problem: if the username does not exist, a naive server answers instantly (nothing to check); if it exists, it spends ~100 ms hashing the password. An attacker can measure the response time and learn which usernames exist, even though the error text is identical.

Fix:
1. Username not found → still run the password hash against a fixed **dummy hash**, then return 401. Same work, same time.
2. Compare hashes with the library's **constant-time compare** (the argon2 libraries do this), so the comparison time does not depend on how many bytes match.
3. Same 401 body for "unknown user" and "wrong password".

## Data to persist

`auth_tokens`

| Column | Notes |
|--------|-------|
| id | UUID (returned as `token_id`, not secret) |
| user_id | FK users |
| token_hash | SHA-256 of token, unique index |
| device_name | nullable |
| created_at | |
| expires_at | created_at + 365 days |
| last_used_at | nullable, updated lazily (at most once/hour) to avoid a write per request |
| revoked_at | nullable |

Indexes: `token_hash` (unique), `user_id`.

## Defaults (assumed, change if you disagree)

- A daily in-process job deletes token rows expired or revoked more than 30 days ago.
- No refresh / sliding expiry: the token dies after 1 year and the user logs in again.
- Lock numbers (5 per username, 20 per IP, 15 min) can be tuned via config.
