# 13. admin-revoke-login

`POST /v1/admin/users/{username}/revoke-login` — auth: **bearer, admin role required**

Admin force-logs-out a user: revokes **all active tokens** of the target user on every device. Used from the mobile app's admin screen. Differs from [12-revoke](12-revoke.md), which is a user revoking their own other devices.

## Request

Path: `username` — target user (lowercase, as in register). No body.

## Response

**200 OK**

```json
{ "revoked_count": 3 }
```

| Status | code | Case |
|--------|------|------|
| 401 | `unauthorized` | missing/invalid/expired/revoked token |
| 403 | `forbidden` | caller is not admin |
| 404 | `not_found` | no such username |

Idempotent: user with no active tokens → 200 with `revoked_count: 0`.

## Data to persist

Sets `revoked_at = now()` on `auth_tokens WHERE user_id = target AND revoked_at IS NULL`. No new tables.

## Defaults (assumed, change if you disagree)

- Admin revoking their **own** username is allowed: it kills all their tokens including the current one, so the next request gets 401.
- Target user is addressed by `username` (agreed). No list-users endpoint; the admin types the name.
- No message shown to the revoked user; their app gets 401 and shows the login screen.
