# 9. create-exercise

`POST /v1/exercises` — auth: bearer, role `user` only (admin gets 403)

Add a new exercise to the shared master catalog. Any regular user can do this. The master starts empty.

## Request

```json
{
  "id": "0195f3a2-bbbb-7000-8000-000000000011",
  "name": "Incline Dumbbell Press",
  "category": "strength",
  "primary_muscle_group": "chest",
  "secondary_muscle_groups": ["shoulders", "triceps"],
  "equipment": "dumbbell",
  "measurement_type": "reps_weight",
  "instructions": "Set bench to 30 degrees..."
}
```

| Field | Type | Required | Rules |
|-------|------|----------|-------|
| id | uuid | no | client-generated: allows offline creation and idempotent retry. Server generates one if omitted |
| name | string | yes | trimmed, 1–100, unique (case-insensitive) among non-deleted exercises |
| category | enum | yes | fixed list, see [enums](../enums.md) |
| primary_muscle_group | enum | yes | fixed list |
| secondary_muscle_groups | enum[] | no | fixed list, max 5, no duplicates, must not contain the primary |
| equipment | enum | yes | fixed list |
| measurement_type | enum | yes | fixed list; which set fields the app shows |
| instructions | string | no | max 4000 |

## Responses

**201 Created** — returns the exercise (same shape as items in [get-list-exercise](08-get-list-exercise.md)), `created_by` = caller.

**200 OK** — idempotent retry: the same `id` already exists with **identical content** → returns the existing exercise, no change.

| Status | code | Case |
|--------|------|------|
| 400 | `bad_request` | malformed JSON, unknown field |
| 401 | `unauthorized` | |
| 403 | `forbidden` | caller is admin |
| 409 | `conflict` | `already_exists`: name taken by another exercise (returns `error.details.existing_id` so the app can re-map its local copy); `id_taken`: `id` exists with different content; `deleted`: `id` exists but was soft-deleted |
| 422 | `validation_failed` | invalid enum value (`invalid_value`), lengths, secondary rules |

## Data to persist

`exercises`

| Column | Notes |
|--------|-------|
| id | UUID PK |
| name | text; **unique index on `lower(name)` where `deleted_at IS NULL`** (a deleted name can be reused by a new exercise) |
| category, primary_muscle_group, equipment, measurement_type | text with CHECK constraint / Postgres enum matching [enums](../enums.md) |
| secondary_muscle_groups | text[] |
| instructions | text nullable |
| created_by | FK users (never null now: no seed data) |
| created_at, updated_at | server-set |
| deleted_at | nullable, soft delete |

## Notes

- Enum storage: `text` + CHECK is easier to extend by migration than a native Postgres enum; recommended.
- Rate limit: none beyond the generic per-user limit (registration is admin-only, so all users are trusted).
- Edit: [18-update-exercise](18-update-exercise.md). Delete: [19-delete-exercise](19-delete-exercise.md).
- Image: created without one (`image_url: null`); upload with [20-set-exercise-image](20-set-exercise-image.md). The create body has no image field.
