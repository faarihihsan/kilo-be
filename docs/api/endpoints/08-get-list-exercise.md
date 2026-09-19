# 8. get-list-exercise

`GET /v1/exercises` — auth: bearer, role `user` only (admin gets 403)

List exercises from the master catalog. The master is **shared by all users**: any user can add ([9](09-create-exercise.md)), edit ([18](18-update-exercise.md)) and delete ([19](19-delete-exercise.md)). It starts empty (no seed data); users add what they need.

The app also uses this list to show names/details for `exercise_id`s in progress logs and plans (those responses carry no names).

## Request

Query params (all optional):

| Param | Type | Notes |
|-------|------|-------|
| q | string | case-insensitive name search (substring) |
| category | enum | see [enums](../enums.md) |
| primary_muscle_group | enum | filter by primary muscle |
| equipment | enum | |
| limit | int | default 50, max 200 |
| cursor | string | |
| updated_since | timestamp | rows whose `updated_at` > value, **including soft-deleted**. Sync mode: ordered by `updated_at` ASC, tie-break `id` |
| include_deleted | bool | default false (implied true with `updated_since`) |

Default ordering: `name` ASC (case-insensitive), tie-break `id`.

## Response

**200 OK**

```json
{
  "items": [
    {
      "id": "0195f3a2-bbbb-7000-8000-000000000010",
      "name": "Bench Press",
      "category": "strength",
      "primary_muscle_group": "chest",
      "secondary_muscle_groups": ["triceps", "shoulders"],
      "equipment": "barbell",
      "measurement_type": "reps_weight",
      "instructions": "Lie on bench...",
      "image_url": "https://api.example.com/media/exercises/0195f3a2-bbbb-7000-8000-000000000010/9f2c4e1ab37d05c6.webp",
      "created_by": "0195f3a2-7c1e-7a55-9d3b-2f6e8a1b4c90",
      "created_at": "2026-08-01T10:00:00Z",
      "updated_at": "2026-08-01T10:00:00Z",
      "deleted_at": null
    }
  ],
  "next_cursor": null
}
```

- `created_by`: id of the user who first created it (informational only; **anyone** can edit/delete).
- Soft-deleted rows (`deleted_at` set) keep **all their fields**, so the app can still show old workouts that used them.
- `updated_at` is server-set (exercises have no client timestamp).

### Sync recipe for the app

Same idea as progress: first sync with `updated_since=1970-01-01T00:00:00Z`, follow `next_cursor`, remember the max `updated_at`. Rows with `deleted_at` → mark as deleted locally **but keep the data** for history. Others → upsert.

| Status | code | Case |
|--------|------|------|
| 400 | `bad_request` | bad cursor / bad timestamp |
| 401 | `unauthorized` | |
| 403 | `forbidden` | caller is admin |
| 422 | `validation_failed` | limit out of range, invalid enum filter |

## Data to persist

See [09-create-exercise](09-create-exercise.md) (`exercises` table). Indexes: `lower(name)` (trigram for `q`), `(updated_at, id)`, `primary_muscle_group`.

## Notes

- Name search: `ILIKE '%q%'` with a trigram index; enough for a small catalog.
- `image_url`: public URL of the exercise's single static image, or `null`. Set/removed via [20](20-set-exercise-image.md) / [21](21-delete-exercise-image.md). Changing an image bumps `updated_at`, so it syncs like any edit. The image URL is stable and cacheable forever (hash in the file name).
