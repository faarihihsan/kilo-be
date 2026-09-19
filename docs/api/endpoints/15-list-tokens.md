# 15. list-tokens

`GET /v1/auth/tokens` — auth: bearer, role `user` only (admin gets 403)

List the caller's **active** logins (one per phone). Gives the app the `token_id`s it needs for [revoke](12-revoke.md), e.g. "log out my old phone" from a new phone.

## Request

No params, no body.

## Response

**200 OK**

```json
{
  "items": [
    {
      "id": "0195f3a2-dddd-7000-8000-000000000200",
      "device_name": "Ihsan's iPhone",
      "created_at": "2026-09-19T08:30:00Z",
      "last_used_at": "2026-09-19T09:10:00Z",
      "expires_at": "2027-09-19T08:30:00Z",
      "current": true
    }
  ]
}
```

- Only tokens with `revoked_at IS NULL` and `expires_at > now`.
- `current: true` marks the token used for this request (so the app can label "this phone" and avoid revoking itself by accident).
- `last_used_at` is updated lazily (at most once per hour), so it is approximate.
- Ordered by `created_at` DESC. No pagination (a user has a handful of tokens).
- Never returns token secrets or hashes.

| Status | code | Case |
|--------|------|------|
| 401 | `unauthorized` | |
| 403 | `forbidden` | caller is admin |

## Data to persist

Nothing new. Reads `auth_tokens` (see [02-login](02-login.md)).
