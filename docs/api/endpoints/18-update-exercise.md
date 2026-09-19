# 18. update-exercise

`PUT /v1/exercises/{id}` — auth: bearer, role `user` only (admin gets 403)

Edit an exercise in the shared master. **Any regular user can edit any exercise** (agreed). Full replace of the editable fields.

Update only: it never creates. To create, use [9](09-create-exercise.md).

## Request

Path: `id` — UUID of an existing exercise.

```json
{
  "name": "Incline Dumbbell Press",
  "category": "strength",
  "primary_muscle_group": "chest",
  "secondary_muscle_groups": ["shoulders", "triceps"],
  "equipment": "dumbbell",
  "measurement_type": "reps_weight",
  "instructions": "Set bench to 30 degrees, elbows at 45..."
}
```

Same fields and rules as [create-exercise](09-create-exercise.md) (all required except `secondary_muscle_groups` and `instructions`; no `id` in body).

## Response

**200 OK** — the updated exercise. `updated_at` = now (server). `created_by` unchanged.

| Status | code | Case |
|--------|------|------|
| 400 | `bad_request` | malformed JSON, unknown field, bad path id |
| 401 | `unauthorized` | |
| 403 | `forbidden` | caller is admin |
| 404 | `not_found` | no such exercise |
| 409 | `conflict` | `already_exists`: new name collides with another exercise (`details.existing_id`); `deleted`: exercise was soft-deleted |
| 422 | `validation_failed` | invalid enum / lengths |

## Behavior

- **Last write wins.** No client timestamp or stale check: edits to exercises are rare and small, and all users are trusted.
- Sending identical content is a 200 no-op semantically (still bumps nothing if unchanged — server may skip the write and keep `updated_at`).
- Renaming or changing `measurement_type` does not touch existing progress logs/plans; they reference the exercise by id and store their own set values. Old sets may have fields that don't match a new `measurement_type`; the app just shows whatever the set contains.
- No history/audit of who edited what in v1 (no `updated_by`).
- The image is **not** part of this request and is left untouched; use [20](20-set-exercise-image.md) / [21](21-delete-exercise-image.md).

## Data to persist

Updates `exercises` (see [09](09-create-exercise.md)). No new tables.
