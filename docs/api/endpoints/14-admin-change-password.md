# 14. admin-change-password

`PUT /v1/admin/users/{username}/password` — auth: **bearer, admin role required**

Admin sets a new password for a user (the recovery path when a user forgets theirs). Used from the mobile app's admin screen.

## Request

Path: `username` — target user.

```json
{ "password": "newpass" }
```

| Field | Type | Required | Rules |
|-------|------|----------|-------|
| password | string | yes | same as register: non-empty, max 128 bytes, no other policy |

Admin does not need to know the old password.

## Response

**204 No Content**

| Status | code | Case |
|--------|------|------|
| 400 | `bad_request` | malformed JSON, unknown field |
| 401 | `unauthorized` | |
| 403 | `forbidden` | caller is not admin |
| 404 | `not_found` | no such username |
| 422 | `validation_failed` | empty / too long password |

## Side effects

- Replaces `users.password_hash`, bumps `updated_at`.
- **Revokes all the target's active tokens** (all devices must log in again with the new password). Exception: if the admin changes **their own** password, the token used for this request is kept and all their other tokens are revoked.

## Data to persist

Updates `users`; sets `revoked_at` on `auth_tokens`. No new tables.

## Defaults (assumed, change if you disagree)

- Revoke-on-change is always on (safer, and it is one call from the app).
- Only the admin can change passwords; users cannot change their own.
- Rate limit on admin endpoints: same generic per-IP limit as other auth endpoints.
