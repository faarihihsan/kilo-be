# 1. register

`POST /v1/auth/register` — auth: **bearer, admin role required**

Admin creates a user account. Does **not** log in / return token for the new user (that user calls [login](02-login.md)). No public sign-up: only an admin can create accounts.

Decisions (agreed): username only (no email), no extra profile fields, weight always kg, 409 on duplicate username, reserved usernames blocked, no password policy, registration restricted to admin role. Admin can only do three things to users: register ([this](01-register.md)), [revoke login](13-admin-revoke-login.md), [change password](14-admin-change-password.md). The mobile app's admin screens offer exactly those three.

## Request

Header: `Authorization: Bearer <admin token>`

```json
{
  "username": "ihsan",
  "password": "123"
}
```

| Field | Type | Required | Rules |
|-------|------|----------|-------|
| username | string | yes | trimmed, lowercased, 3–30 chars, `[a-z0-9_]` only (no special chars, no spaces, no unicode), not reserved, unique |
| password | string | yes | non-empty, max 128 bytes (DoS cap for hashing). **No other policy**, `123` is valid |

Created user always gets role `user`. No `role` field in the request; admins are created out-of-band (see below).

### Reserved usernames

Checked after lowercasing/trimming, exact match → 422 `reserved`:

`admin`, `administrator`, `root`, `system`, `support`, `api`, `me`, `null`, `undefined`, `anonymous`

List lives in code as a constant; extend as needed. Applies to the HTTP endpoint only; the admin CLI may bypass it (e.g. to bootstrap the first admin).

## Responses

**201 Created**

```json
{
  "id": "0195f3a2-7c1e-7a55-9d3b-2f6e8a1b4c90",
  "username": "ihsan",
  "role": "user",
  "created_at": "2026-09-19T08:30:00Z"
}
```

| Status | code | Case |
|--------|------|------|
| 400 | `bad_request` | malformed JSON, unknown field |
| 401 | `unauthorized` | missing/invalid/expired/revoked token |
| 403 | `forbidden` | valid token but caller is not admin |
| 409 | `conflict` | username already taken |
| 422 | `validation_failed` | bad username length/chars, reserved username, empty password |
| 429 | `rate_limited` | too many attempts |

409 example:

```json
{
  "error": {
    "code": "conflict",
    "message": "Username already taken",
    "details": [{ "field": "username", "issue": "already_taken" }]
  }
}
```

422 example (reserved):

```json
{
  "error": {
    "code": "validation_failed",
    "message": "Invalid input",
    "details": [{ "field": "username", "issue": "reserved" }]
  }
}
```

Other `issue` values for username: `too_short`, `too_long`, `invalid_chars`. For password: `required`, `too_long`.

## Data to persist

`users`

| Column | Notes |
|--------|-------|
| id | UUID, server-generated (v7) |
| username | text, stored lowercase; unique index |
| password_hash | argon2id hash, never plaintext |
| role | enum `user` \| `admin`, default `user` |
| created_at, updated_at | |

Nothing else. No email, no display name, no unit preference (weight is always kg).

## Admin bootstrap (out of scope for the HTTP API)

Register needs an admin token, so the **first admin must be created out-of-band**, e.g. a CLI subcommand of the same binary:

```
<binary> admin create-user --username ihsan --password <pw> --role admin
<binary> admin reset-password --username ihsan --password <new>
```

- `create-user` inserts a user directly (can set role `admin`, bypasses reserved-name check).
- `reset-password` sets a new hash and revokes all that user's tokens (`revoked_at = now()`). Emergency path, e.g. the admin forgot their **own** password.
- Access = whoever can run the binary / reach the DB. Routine admin work (register, revoke login, change password) uses the HTTP endpoints from the app; the CLI is only for bootstrap and emergencies.

Details go in the implementation plan, not the API spec.

## Notes

- **Admin scope is fixed**: register, revoke login, change password. Nothing else (no viewing other users' data, no deleting users, no editing the exercise master).
- **Mobile app**: no public sign-up screen. Logged in as admin, the app shows only the three admin screens.
- **Admin cannot log workouts** (agreed): admin accounts are management-only, so you need a separate regular account for workouts. Full access matrix in [conventions](../conventions.md#roles--access).
