# Enums (fixed lists)

Agreed: enum fields are **fixed lists validated by the server**. Anything else → 422 `validation_failed` (`issue: invalid_value`).

Adding a value later = a server change + deploy. So the **mobile app must tolerate unknown values** it receives (show a generic label instead of crashing), in case the server is newer than the app.

Values are lowercase `snake_case` strings. Proposed lists, edit freely before implementation.

## Exercise

**category**

`strength`, `cardio`, `mobility`

**muscle group** (used by `primary_muscle_group` and `secondary_muscle_groups[]`)

`chest`, `upper_back`, `lats`, `lower_back`, `traps`, `shoulders`, `biceps`, `triceps`, `forearms`, `abs`, `obliques`, `glutes`, `quads`, `hamstrings`, `calves`, `adductors`, `abductors`, `full_body`

**equipment**

`barbell`, `dumbbell`, `kettlebell`, `machine`, `cable`, `smith_machine`, `bodyweight`, `resistance_band`, `cardio_machine`, `other`

**measurement_type** — tells the app which set fields to show and log

| Value | Set fields used |
|-------|-----------------|
| `reps_weight` | reps + weight (e.g. bench press) |
| `reps` | reps only (e.g. pull-ups, push-ups) |
| `duration` | duration_seconds (e.g. plank) |
| `distance_duration` | distance_meters + duration_seconds (e.g. running) |

## Progress set

**type**: `warmup`, `normal`, `drop`, `failure`

## Not enums

`role` (`user` | `admin`) is fixed in code, not user-facing data.
