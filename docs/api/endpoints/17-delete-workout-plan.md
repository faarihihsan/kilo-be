# 17. delete-workout-plan

`DELETE /v1/workout-plans/{id}` — auth: bearer, role `user` only (admin gets 403)

Soft-delete a workout plan. Same mechanics as [16-delete-progress](16-delete-progress.md).

## Request

Path: `id` — UUID. No body.

## Response

**204 No Content** (idempotent: already-deleted also 204)

| Status | code | Case |
|--------|------|------|
| 401 | `unauthorized` | |
| 403 | `forbidden` | caller is admin |
| 404 | `not_found` | id never existed, or owned by another user |

## Behavior

- Sets `deleted_at = now()` and `server_updated_at = now()`.
- **Progress logs that reference the plan are untouched.** Their `workout_plan_id` stays valid (the plan row still exists, just soft-deleted), so history keeps working.
- After delete, `GET /v1/workout-plans/{id}` → 404; list hides it unless `updated_since` / `include_deleted`.
- A later `PUT` to the same id → 409 `deleted` (no resurrection).

## Data to persist

Updates `workout_plans` (`deleted_at`, `server_updated_at`). No new tables.
