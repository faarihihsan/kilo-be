# 7. get-workout-plan

`GET /v1/workout-plans/{id}` — auth: bearer, role `user` only (admin gets 403)

Get one saved workout plan with **all exercises** and their targets. A plan is one flat, ordered list of exercises.

Plans are created/updated via [save-workout-plan](10-save-workout-plan.md).

## Request

Path: `id` — UUID. No body.

## Response

**200 OK**

```json
{
  "id": "0195f3a2-aaaa-7000-8000-000000000001",
  "name": "Push",
  "description": "Chest / shoulders / triceps",
  "updated_at": "2026-09-10T07:12:00Z",
  "created_at": "2026-08-01T10:00:00Z",
  "server_updated_at": "2026-09-10T07:12:03Z",
  "deleted_at": null,
  "exercises": [
    {
      "exercise_id": "0195f3a2-bbbb-7000-8000-000000000010",
      "position": 0,
      "target_sets": 4,
      "target_reps": 8,
      "target_reps_max": 10,
      "target_weight": 80.0,
      "target_duration_seconds": null,
      "target_distance_meters": null,
      "rest_seconds": 120,
      "notes": "Pause on chest"
    }
  ]
}
```

| Field | Notes |
|-------|-------|
| position | order in plan, ascending |
| target_sets | int ≥ 1 |
| target_reps / target_reps_max | rep range; max nullable (null = fixed reps) |
| target_weight | kg, nullable (user may leave weight open) |
| target_duration_seconds | nullable, for timed exercises |
| target_distance_meters | nullable, for cardio |
| rest_seconds | nullable |

- Targets are **simple**: one target per exercise (sets × reps × weight), not per-set targets.
- **No exercise name** in the response; the app resolves `exercise_id` via the exercise master list (endpoint 8).
- Same exercise may appear more than once in a plan (different positions).

| Status | code | Case |
|--------|------|------|
| 401 | `unauthorized` | |
| 403 | `forbidden` | caller is admin |
| 404 | `not_found` | missing, soft-deleted, or owned by another user |

## Data to persist

Nothing new, read only. Tables in [10-save-workout-plan](10-save-workout-plan.md).
