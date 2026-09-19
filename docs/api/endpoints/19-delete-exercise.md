# 19. delete-exercise

`DELETE /v1/exercises/{id}` — auth: bearer, role `user` only (admin gets 403)

Soft-delete an exercise in the shared master. **Any regular user can delete any exercise** (agreed). It disappears for everyone.

## Request

Path: `id` — UUID. No body.

## Response

**204 No Content** (idempotent: already-deleted also 204)

| Status | code | Case |
|--------|------|------|
| 401 | `unauthorized` | |
| 403 | `forbidden` | caller is admin |
| 404 | `not_found` | id never existed |

## Behavior

- Sets `deleted_at = now()` and `updated_at = now()`.
- **Not blocked by usage.** Progress logs and plans that reference the exercise keep working; the row stays with all its fields. Apps keep the deleted exercise's data locally (from the sync feed) so old workouts still show its name.
- The exercise's image file is **kept** so old workouts still show it ([21](21-delete-exercise-image.md) is the only way to remove it).
- Deleted exercises are hidden from the default list; visible via `updated_since` / `include_deleted`.
- New saves may still **reference** a deleted exercise (progress and plans accept it), so an offline phone never loses a workout. The app should just stop offering deleted exercises in pickers.
- A later `PUT` on the same id → 409 `deleted`. No undo endpoint: to bring it back, create a new exercise (the name is free again, new id).

## Data to persist

Updates `exercises` (`deleted_at`, `updated_at`). No new tables.

## Risk to be aware of

Any user can delete any exercise and it vanishes from everyone's pickers. Fine for a trusted personal setup; if you add more people later, consider limiting delete to the creator.
