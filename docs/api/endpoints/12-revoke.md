# 12. revoke

`POST /v1/auth/revoke` — auth: bearer, role `user` only (admin gets 403)

Revoke **other** tokens of the current user: one specific token (e.g. lost phone) or all of them. Difference from [logout](11-logout.md): logout kills the token in use; revoke targets tokens of other devices. For an admin force-logging-out *another user*, see [13-admin-revoke-login](13-admin-revoke-login.md).

## Request

Exactly one of `token_id` or `scope`:

```json
{ "token_id": "0195f3a2-dddd-7000-8000-000000000200" }
```

```json
{ "scope": "others" }
```

| Field | Type | Rules |
|-------|------|-------|
| token_id | uuid | id of a token owned by caller (not the secret token string). May be the current token's id |
| scope | enum | `others` = every active token except the current one. `all` = every active token including current (full sign-out everywhere) |

Sending both or neither → 422.

## Response

**200 OK**

```json
{ "revoked_count": 2 }
```

| Status | code | Case |
|--------|------|------|
| 400 | `bad_request` | malformed JSON |
| 401 | `unauthorized` | |
| 404 | `not_found` | `token_id` unknown or owned by another user |
| 422 | `validation_failed` | both/neither field, bad scope value |

Revoking an already-revoked or expired token by id → 200 with `revoked_count: 0` (idempotent).

## Data to persist

Sets `revoked_at = now()` on matching `auth_tokens` rows (`WHERE user_id = caller AND revoked_at IS NULL`). No new tables.

## Where does the client get `token_id`?

- [login](02-login.md) response now includes `token_id` (id of the token just issued).
- That only covers the device's *own* token. To revoke a **lost phone** from a new device, the user needs to see their devices. Options:
  - **Chosen:** [15-list-tokens](15-list-tokens.md), `GET /v1/auth/tokens`, lists active tokens `{id, device_name, created_at, last_used_at, current}`. The app shows a device list; tapping one calls revoke with its `token_id`. `scope` stays as a shortcut for "sign out everywhere else".

## Defaults (assumed, change if you disagree)

- `scope: "all"` includes the current token: the call succeeds, the next request gets 401.
- No password re-entry for `scope: all` in v1 (the bearer token already proves ownership).
- There is no endpoint for a user to change their own password; only the admin can ([14](14-admin-change-password.md), which revokes tokens itself).
