# 21. delete-exercise-image

`DELETE /v1/exercises/{id}/image` — auth: bearer, role `user` only (admin gets 403)

Remove the image of an exercise. Any regular user can do this.

## Request

Path: `id` — UUID. No body.

## Response

**204 No Content** — idempotent: no image, or exercise already soft-deleted, also returns 204.

| Status | code | Case |
|--------|------|------|
| 401 | `unauthorized` | |
| 403 | `forbidden` | caller is admin |
| 404 | `not_found` | exercise never existed |

## Behavior

- Clears `image_hash`, `image_ext`, `image_size_bytes`, bumps `updated_at` (so other phones learn `image_url` is now null), then deletes the file from disk.
- Deleting an **exercise** ([19](19-delete-exercise.md)) does **not** delete its image: old workouts still show it. Only this endpoint removes the file.

## Data to persist

Updates `exercises` (image columns, `updated_at`). No new tables.
