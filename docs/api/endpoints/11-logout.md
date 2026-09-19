# 11. logout

`POST /v1/auth/logout` — auth: bearer

Revoke the **token used for this request** (current device). Other devices stay logged in. Closes the "lost token stays valid for a year" gap.

## Request

No body.

## Response

**204 No Content** — token revoked.

| Status | code | Case |
|--------|------|------|
| 401 | `unauthorized` | missing/invalid/expired/already-revoked token |

Logout is effectively idempotent from the client's view: a second call with the same token gets 401, and the client should treat that as "already logged out" and clear local state anyway.

## Data to persist

Sets `auth_tokens.revoked_at = now()` for the current token row. No new tables. See [02-login](02-login.md).

## Defaults (assumed, change if you disagree)

- The app deletes its local token regardless of the response (network failure or 401 included).
- No push/device registration exists in v1, so nothing else to clean up on logout.
- Expired/revoked token rows are purged by a small daily in-process job (rows expired or revoked more than 30 days ago).
