# 5. get-list-progress

`GET /v1/progress` — auth: bearer, role `user` only (admin gets 403)

List the user's completed workout session logs. Doubles as the **sync pull** endpoint via `updated_since`.

## Request

Query params (all optional):

| Param | Type | Notes |
|-------|------|-------|
| limit | int | default 50, max 200 |
| cursor | string | from previous `next_cursor` |
| updated_since | timestamp | rows whose `server_updated_at` > value, **including soft-deleted**. Sync mode: ordered by `server_updated_at` ASC, tie-break `id` |
| workout_plan_id | uuid | filter by plan |
| from, to | timestamp | filter on `started_at` range |
| include_deleted | bool | default false (implied true with `updated_since`) |
| expand | enum | `exercises`: return full sessions (with exercises and sets) instead of summaries. Lets a sync pull get everything in one call |

Default ordering (no `updated_since`): `started_at` DESC, tie-break `id`.

## Response

**200 OK** — summaries by default (no sets); use [get-progress](04-get-progress.md) for detail, or `expand=exercises`.

```json
{
  "items": [
    {
      "id": "0195f3a2-cccc-7000-8000-000000000100",
      "workout_plan_id": "0195f3a2-aaaa-7000-8000-000000000001",
      "name": "Push Day A",
      "started_at": "2026-09-19T08:00:00Z",
      "ended_at": "2026-09-19T08:55:00Z",
      "duration_seconds": 3120,
      "exercise_count": 5,
      "set_count": 18,
      "updated_at": "2026-09-19T08:55:02Z",
      "server_updated_at": "2026-09-19T08:55:04Z",
      "deleted_at": null
    }
  ],
  "next_cursor": null
}
```

With `expand=exercises` each item is the full object from get-progress (no `exercise_count` / `set_count`).

### Sync recipe for the app

1. First sync: `GET /v1/progress?updated_since=1970-01-01T00:00:00Z&expand=exercises&limit=200`, follow `next_cursor` until null.
2. Save the largest `server_updated_at` seen.
3. Later syncs: use that value as `updated_since`. Items with `deleted_at` set → delete locally. Others → upsert locally.
4. Uploads: for every locally changed **completed** session, `PUT /v1/progress/{id}` (see conflict rule in [03](03-save-progress.md#conflict-rule)); for locally deleted, `DELETE` ([16](16-delete-progress.md)).

| Status | code | Case |
|--------|------|------|
| 400 | `bad_request` | bad cursor / bad timestamp |
| 401 | `unauthorized` | |
| 403 | `forbidden` | caller is admin |
| 422 | `validation_failed` | limit out of range, bad `expand` |

## Data to persist

Nothing new. Uses indexes on `progress` (see [03](03-save-progress.md)).

## Notes

- Cursor is tied to the sort order and filters; changing filters invalidates it.
- `expand=exercises` with `limit=200` can be a large response (up to 200 × 50 exercises × 100 sets worst case); real sessions are far smaller. Fine for v1.
