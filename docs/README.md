# Workout Tracker BE — Plan

Status: **planning complete, no code yet.** API spec, data model and implementation plan are written; all design questions are decided (see the decisions log below).

## Goal

Backend service so the mobile workout tracker app can sync data between devices of the same user.

## Constraints (v1)

- Language: Go, latest stable release at project start (verify on go.dev/dl, pin in `go.mod` + CI).
- Auth: bearer token, TTL 1 year.
- Simple scope: 21 endpoints below (9 original + save-workout-plan, logout, revoke, list-tokens, admin-revoke-login, admin-change-password, delete-progress, delete-workout-plan, update-exercise, delete-exercise, set-exercise-image, delete-exercise-image). Nothing else.

## Endpoints

| # | Name | Method + Path | Auth | Spec |
|---|------|---------------|------|------|
| 1 | register | `POST /v1/auth/register` | admin | [01-register](api/endpoints/01-register.md) |
| 2 | login | `POST /v1/auth/login` | none | [02-login](api/endpoints/02-login.md) |
| 3 | save-progress | `PUT /v1/progress/{id}` | user | [03-save-progress](api/endpoints/03-save-progress.md) |
| 4 | get-progress | `GET /v1/progress/{id}` | user | [04-get-progress](api/endpoints/04-get-progress.md) |
| 5 | get-list-progress | `GET /v1/progress` | user | [05-get-list-progress](api/endpoints/05-get-list-progress.md) |
| 6 | get-list-workout-plan | `GET /v1/workout-plans` | user | [06-get-list-workout-plan](api/endpoints/06-get-list-workout-plan.md) |
| 7 | get-workout-plan | `GET /v1/workout-plans/{id}` | user | [07-get-workout-plan](api/endpoints/07-get-workout-plan.md) |
| 8 | get-list-exercise | `GET /v1/exercises` | user | [08-get-list-exercise](api/endpoints/08-get-list-exercise.md) |
| 9 | create-exercise | `POST /v1/exercises` | user | [09-create-exercise](api/endpoints/09-create-exercise.md) |
| 10 | save-workout-plan (added) | `PUT /v1/workout-plans/{id}` | user | [10-save-workout-plan](api/endpoints/10-save-workout-plan.md) |
| 11 | logout (added) | `POST /v1/auth/logout` | any | [11-logout](api/endpoints/11-logout.md) |
| 12 | revoke (added) | `POST /v1/auth/revoke` | user | [12-revoke](api/endpoints/12-revoke.md) |
| 13 | admin-revoke-login (added) | `POST /v1/admin/users/{username}/revoke-login` | admin | [13-admin-revoke-login](api/endpoints/13-admin-revoke-login.md) |
| 14 | admin-change-password (added) | `PUT /v1/admin/users/{username}/password` | admin | [14-admin-change-password](api/endpoints/14-admin-change-password.md) |
| 15 | list-tokens (added) | `GET /v1/auth/tokens` | user | [15-list-tokens](api/endpoints/15-list-tokens.md) |
| 16 | delete-progress (added) | `DELETE /v1/progress/{id}` | user | [16-delete-progress](api/endpoints/16-delete-progress.md) |
| 17 | delete-workout-plan (added) | `DELETE /v1/workout-plans/{id}` | user | [17-delete-workout-plan](api/endpoints/17-delete-workout-plan.md) |
| 18 | update-exercise (added) | `PUT /v1/exercises/{id}` | user | [18-update-exercise](api/endpoints/18-update-exercise.md) |
| 19 | delete-exercise (added) | `DELETE /v1/exercises/{id}` | user | [19-delete-exercise](api/endpoints/19-delete-exercise.md) |
| 20 | set-exercise-image (added) | `PUT /v1/exercises/{id}/image` | user | [20-set-exercise-image](api/endpoints/20-set-exercise-image.md) |
| 21 | delete-exercise-image (added) | `DELETE /v1/exercises/{id}/image` | user | [21-delete-exercise-image](api/endpoints/21-delete-exercise-image.md) |

Shared rules (errors, pagination, ids, timestamps, sync): [api/conventions.md](api/conventions.md). Fixed enum lists: [api/enums.md](api/enums.md).

## Key design decisions (agreed)

1. **Offline-first sync model.** Mobile creates data offline, so **client generates IDs** (UUID, v7 preferred). Server never invents IDs for progress. Makes `save-progress` an idempotent upsert — safe to retry on flaky network.
2. **Soft delete + `server_updated_at`.** Other devices pull changes via `updated_since` and learn about deletions. Delete endpoints exist for progress and plans (16, 17).
3. **Conflict strategy (decided):** client sends `updated_at`; older than stored → 409 with server copy, equal → no-op (safe retry), newer → saved; deleted rows → 409 `deleted`. Separate server-set `server_updated_at` drives sync. Details in [save-progress](api/endpoints/03-save-progress.md#conflict-rule).
3b. **Progress = completed sessions only.** In-progress workouts live only on the phone; the app uploads when finished. No `status` field.
4. **Token: opaque random token, stored hashed in DB** (revocable, 1-year `expires_at`). Alternative: JWT (stateless, not revocable). See Q1 below.
5. **Data ownership.** Progress + workout plans are per-user. Exercise master is shared/global.
6. **Admin role is narrow and management-only.** `users.role` = `user` | `admin`. Admin can only: register a user, revoke a user's logins, change a user's password (endpoints 1, 13, 14), plus login/logout for its own session. Admin cannot log workouts or touch any workout data (403). No public sign-up. The mobile app's admin screens offer exactly those three. First admin is created via CLI (bootstrap); CLI also serves as emergency password reset.
7. **Workout plan = one flat exercise list** (decided). No days/sections; a user makes several plans ("Push", "Pull", "Legs"). Simple targets per exercise (sets × reps × weight, optional duration/distance), per-user only, max 100 active plans.

## Decisions log (former open questions, all resolved)

- **Q1 Token type — opaque** (random string, DB lookup per request; JWT rejected because it can't be cancelled before expiry). Details in [02-login](api/endpoints/02-login.md) — revocation (logout/revoke, endpoints 11–12) needs it. With JWT, logout/revoke would require a denylist, defeating the stateless point.
- ~~**Q1b Device list**~~ **Resolved:** added `GET /v1/auth/tokens` (endpoint 15) so the app can show devices and revoke one by `token_id`.
- ~~**Q2 Workout plan creation**~~ **Resolved:** added `PUT /v1/workout-plans/{id}` (endpoint 10), same idempotent upsert pattern as save-progress.
- ~~**Q3 Exercise master ownership**~~ **Resolved:** any regular user can create, edit and delete exercises in the shared master (endpoints 9, 18, 19), fixed enum lists, starts empty (no seed). Original question: any authenticated user can create into the shared master? Risk: junk/duplicates. Options: open-to-all + unique name, admin-only, or per-user custom exercises + curated global set. Since registration is admin-only, all users are trusted, so open-to-all authenticated users + unique name is now the proposal.
- ~~**Q4 Units**~~ **Resolved:** weight always kg. No `weight_unit` field anywhere; app converts for display if ever needed.
- ~~**Q5 Login identifier**~~ **Resolved:** username only. No email, no extra profile fields. 409 on duplicate username. Password recovery: admin changes it via endpoint 14 (CLI only for emergencies). No password policy (any non-empty password).
- ~~**Q7 Admin also a normal user?**~~ **Resolved:** no. Admin is management-only (403 on progress/plan/exercise endpoints), so a separate regular account is needed for workouts. Matrix in [conventions](api/conventions.md#roles--access).
- ~~**Q8 Address target user by username or id?**~~ **Resolved:** `username`. No list-users endpoint.
- ~~**Q6 Datastore**~~ **Resolved:** PostgreSQL.
- ~~**Q9 Exercise images**~~ **Resolved:** one static image per exercise (JPEG/PNG/WebP, ≤ 2 MiB, ≤ 2000 px), stored on the VPS disk, served publicly by the reverse proxy, phone does the resizing. Endpoints 20, 21. No CDN for now; `MEDIA_BASE_URL` config allows adding one later.

## Documents

| Doc | What |
|-----|------|
| [api/conventions.md](api/conventions.md) | errors, pagination, sync rules, roles and access matrix, media files |
| [api/enums.md](api/enums.md) | fixed enum lists |
| `api/endpoints/01…21-*.md` | one spec per endpoint (table above) |
| [data-model.md](data-model.md) | all tables, columns, constraints, indexes, migration order |
| [implementation-plan.md](implementation-plan.md) | stack, project layout, config, deployment on the VPS, testing, milestones |

## Next steps

1. Review the docs; change any default you disagree with (search for "Defaults (assumed" in the endpoint specs).
2. Start implementation at milestone 0 of the [implementation plan](implementation-plan.md#10-build-order-and-milestones) (in a new session; this one is planning only).
