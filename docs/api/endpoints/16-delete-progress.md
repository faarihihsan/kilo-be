# 16. delete-progress

`DELETE /v1/progress/{id}` — auth: bearer, role `user` only (admin gets 403)

Soft-delete a workout session log. The row stays in the DB with `deleted_at` set, so other phones learn about the deletion on their next sync ([05](05-get-list-progress.md) with `updated_since`).

## Request

Path: `id` — UUID. No body.

## Response

**204 No Content**

Idempotent: deleting an already-deleted session also returns 204.

| Status | code | Case |
|--------|------|------|
| 401 | `unauthorized` | |
| 403 | `forbidden` | caller is admin |
| 404 | `not_found` | id never existed, or owned by another user |

## Behavior

- Sets `deleted_at = now()` and `server_updated_at = now()`.
- Child exercises/sets are kept (needed if a hard purge or undo is added later).
- After delete, `GET /v1/progress/{id}` → 404; the list hides it unless `updated_since` / `include_deleted`.
- A later `PUT` to the same id → 409 `deleted` (deleted wins, no resurrection). To "undo", the app creates a new session with a new id.
- No undo endpoint in v1.

## Data to persist

Updates `progress` (`deleted_at`, `server_updated_at`). No new tables.

## Notes

- Old soft-deleted rows are never physically purged in v1 (the data is tiny).
