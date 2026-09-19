# 6. get-list-workout-plan

`GET /v1/workout-plans` — auth: bearer, role `user` only (admin gets 403)

List the current user's saved workout plans. Summaries by default.

A plan is **one flat list of exercises** (agreed): a user makes as many plans as they like, e.g. "Push", "Pull", "Legs" or "Chest", "Legs", "Back". No days/sections inside a plan. Plans are created/updated via [save-workout-plan](10-save-workout-plan.md).

## Request

Query params (all optional):

| Param | Type | Notes |
|-------|------|-------|
| limit | int | default 50, max 200 |
| cursor | string | from previous `next_cursor` |
| updated_since | timestamp | rows whose `server_updated_at` > value, **including soft-deleted**. Sync mode: ordered by `server_updated_at` ASC, tie-break `id` |
| include_deleted | bool | default false (implied true with `updated_since`) |
| expand | enum | `exercises`: return full plans (with exercises) instead of summaries, so a sync pull is one call |

Default ordering (no `updated_since`): `name` ASC (case-insensitive), tie-break `id`.

## Response

**200 OK**

```json
{
  "items": [
    {
      "id": "0195f3a2-aaaa-7000-8000-000000000001",
      "name": "Push",
      "description": "Chest / shoulders / triceps",
      "exercise_count": 6,
      "created_at": "2026-08-01T10:00:00Z",
      "updated_at": "2026-09-10T07:12:00Z",
      "server_updated_at": "2026-09-10T07:12:03Z",
      "deleted_at": null
    }
  ],
  "next_cursor": null
}
```

With `expand=exercises` each item is the full object from [get-workout-plan](07-get-workout-plan.md) (without `exercise_count`).

| Status | code | Case |
|--------|------|------|
| 400 | `bad_request` | bad cursor / bad timestamp |
| 401 | `unauthorized` | |
| 403 | `forbidden` | caller is admin |
| 422 | `validation_failed` | limit out of range, bad `expand` |

## Data to persist

Nothing new. Reads `workout_plans` (columns in [10-save-workout-plan](10-save-workout-plan.md)).

## Decisions

- Plans are **per user only**. No shared/template plans.
- Sync uses the same recipe as progress ([05](05-get-list-progress.md#sync-recipe-for-the-app)); plans are small, so `expand=exercises` on a first sync is cheap.
- No archive flag; soft delete ([17](17-delete-workout-plan.md)) is the only way to hide a plan.
