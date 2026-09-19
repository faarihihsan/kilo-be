# 4. get-progress

`GET /v1/progress/{id}` — auth: bearer, role `user` only (admin gets 403)

Get one completed workout session log by id, including all exercises and sets.

## Request

Path: `id` — UUID. No body.

## Response

**200 OK**

```json
{
  "id": "0195f3a2-cccc-7000-8000-000000000100",
  "workout_plan_id": "0195f3a2-aaaa-7000-8000-000000000001",
  "name": "Push Day A",
  "notes": "Felt strong",
  "started_at": "2026-09-19T08:00:00Z",
  "ended_at": "2026-09-19T08:55:00Z",
  "duration_seconds": 3120,
  "updated_at": "2026-09-19T08:55:02Z",
  "created_at": "2026-09-19T08:55:04Z",
  "server_updated_at": "2026-09-19T08:55:04Z",
  "deleted_at": null,
  "exercises": [
    {
      "exercise_id": "0195f3a2-bbbb-7000-8000-000000000010",
      "position": 0,
      "notes": null,
      "sets": [
        {
          "position": 0,
          "type": "normal",
          "reps": 8,
          "weight": 80.0,
          "duration_seconds": null,
          "distance_meters": null,
          "rpe": 8.5,
          "completed": true
        }
      ]
    }
  ]
}
```

- `updated_at`: the client's last-modified time (echo of what the phone sent). `server_updated_at`: server time of the last accepted write, used for sync.
- Exercises ordered by `position`, sets by `position`.
- **No exercise name** in the log; the app resolves `exercise_id` via the exercise master (endpoint 8).

| Status | code | Case |
|--------|------|------|
| 401 | `unauthorized` | |
| 403 | `forbidden` | caller is admin |
| 404 | `not_found` | not found, soft-deleted, or owned by another user |

## Data to persist

Nothing new, read only. Tables in [save-progress](03-save-progress.md).

## Notes

- Soft-deleted → 404 (no `include_deleted` on get-by-id). Deletions reach other phones through the list endpoint with `updated_since` (see [05](05-get-list-progress.md)).
- No `ETag` / `If-None-Match` in v1.
