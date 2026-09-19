# 20. set-exercise-image

`PUT /v1/exercises/{id}/image` — auth: bearer, role `user` only (admin gets 403)

Upload (or replace) the **single static image** of an exercise, so users can see what the exercise is instead of relying on the name. Any regular user can set the image of any exercise (same trust model as [18-update-exercise](18-update-exercise.md)).

Decisions (agreed): static image only (no GIF/video), stored on the **VPS disk**, no CDN for now.

## Request

Path: `id` — UUID of an existing exercise.

Headers: `Authorization: Bearer <token>`, `Content-Type: image/jpeg` | `image/png` | `image/webp`

Body: the **raw image bytes** (not multipart, not base64).

| Rule | Value |
|------|-------|
| Formats | JPEG, PNG, WebP |
| Max size | 2 MiB (an exception to the 1 MiB general body limit) |
| Max dimensions | 2000 × 2000 px |
| Type check | server sniffs the real file type from the bytes; the `Content-Type` header must agree, but is not trusted alone |

The **phone resizes and compresses before upload** (for example to ≤ 1024 px, WebP or JPEG). The server does **no image processing**: no resize, no re-encode. That keeps CPU and RAM low on the 2 core / 2 GB VPS. Side effect: the server does not strip metadata, so the app should upload a re-encoded image (which drops EXIF/GPS in practice).

## Response

**200 OK** — the exercise, same shape as [get-list-exercise](08-get-list-exercise.md) items, now with `image_url`:

```json
{
  "id": "0195f3a2-bbbb-7000-8000-000000000010",
  "name": "Bench Press",
  "image_url": "https://api.example.com/media/exercises/0195f3a2-bbbb-7000-8000-000000000010/9f2c4e1ab37d05c6.webp",
  "updated_at": "2026-09-19T10:00:00Z",
  "...": "other exercise fields"
}
```

| Status | code | Case |
|--------|------|------|
| 400 | `bad_request` | empty body, bad path id |
| 401 | `unauthorized` | |
| 403 | `forbidden` | caller is admin |
| 404 | `not_found` | no such exercise |
| 409 | `conflict` | `deleted`: exercise was soft-deleted |
| 413 | `payload_too_large` | body over 2 MiB |
| 415 | `unsupported_media_type` | not JPEG/PNG/WebP (by sniffed content or header) |
| 422 | `validation_failed` | corrupt/undecodable image (`invalid_image`), over 2000 px (`too_large_dimensions`) |

Idempotent: uploading identical bytes gives the same hash, so the result is the same and nothing changes (no `updated_at` bump).

## How the image is stored and served

- File path on disk: `<MEDIA_DIR>/exercises/{exercise_id}/{hash}.{ext}`, where `hash` = first 16 hex chars of SHA-256 of the bytes and `ext` comes from the sniffed type (`jpg`, `png`, `webp`).
- Write safely: write to a temp file, `fsync`, rename into place, then update the DB row, then delete the previous file. A crash never leaves a DB pointer to a missing file. A leftover orphan file is harmless; an optional cleanup command can remove files not referenced by the DB.
- **Public URL**, no auth: `{MEDIA_BASE_URL}/media/exercises/{exercise_id}/{hash}.{ext}`.
  - Public so the mobile image loader needs no token and a CDN can cache it later. Exercise images are not sensitive; the id + hash in the path is not guessable.
  - `MEDIA_BASE_URL` is a config value. Today it is the API domain; if a CDN or object storage is added later, only this value changes.
- The file name changes whenever the image changes, so it can be cached forever: `Cache-Control: public, max-age=31536000, immutable`, plus `X-Content-Type-Options: nosniff`.
- Served by the reverse proxy (Caddy/nginx) straight from disk, no directory listing. Go can serve `/media/` as a fallback in dev.
- Changing the image bumps the exercise's `updated_at`, so other phones pick up the new `image_url` through the normal sync ([08](08-get-list-exercise.md) with `updated_since`).

## Data to persist

`exercises` gets three nullable columns (see [09](09-create-exercise.md)):

| Column | Notes |
|--------|-------|
| image_hash | 16 hex chars, null = no image |
| image_ext | `jpg` \| `png` \| `webp` |
| image_size_bytes | informational |

`image_url` in responses is built from `MEDIA_BASE_URL` + exercise id + hash + ext. Null when there is no image.

## Ops notes

- Disk use is tiny: a few hundred images of ~100–200 KB is about 100 MB. **Include `MEDIA_DIR` in backups** together with the Postgres dump.
- The media directory must be a persistent volume (not lost on redeploy).
