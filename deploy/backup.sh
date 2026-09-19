#!/usr/bin/env bash
#
# Nightly backup for workout-tracker: a compressed PostgreSQL dump plus the media
# directory, both copied OFF the VPS. Started by workout-tracker-backup.timer,
# whose service loads /etc/workout-tracker/env (see deploy/README.md).
#
# What it does, in order:
#   1. pg_dump in custom format (-Fc, compressed) into $BACKUP_DIR. The dump is
#      written to a .partial file, checked with pg_restore --list, and only then
#      renamed, so a half-written dump never looks like a good backup.
#   2. Deletes local dumps older than $BACKUP_RETENTION_DAYS days. This runs only
#      after step 1 succeeded, so a failing dump never eats the last good ones.
#   3. Mirrors the dump directory to the off-box target. Dumps pruned in step 2
#      disappear there too, so the target keeps the same retention.
#   4. Copies $MEDIA_DIR to the target. This never deletes anything at the
#      target: image files are named by content hash and never change, and an
#      empty or unmounted MEDIA_DIR must not be able to wipe the remote copy.
#      Files superseded by an image replace, or removed by `media gc`, simply
#      stay in the backup (a few hundred KB); `media gc` after a restore
#      cleans them up.
#
# The script exits non-zero as soon as any step fails; systemd then marks the
# run failed. Every step is logged to stdout/stderr (journald).
#
# Configuration comes from the environment (deploy/env.example):
#   DATABASE_URL            required. Passed to pg_dump, so it must use plain
#                           libpq URL parameters. NOTE: pg_dump receives it on
#                           its command line, so the password is visible in the
#                           process list to other local users while it runs;
#                           acceptable on a single-tenant VPS.
#   MEDIA_DIR               required. Directory of exercise images.
#   BACKUP_RSYNC_TARGET     off-box target for rsync over SSH, user@host:/dir
#                           (the directory must exist), or
#   BACKUP_RCLONE_REMOTE    off-box target for rclone, remote:path.
#                           Set exactly one; there is no local-only mode.
#   BACKUP_SSH_KEY          optional. SSH key for the rsync target.
#   BACKUP_DIR              local staging directory,
#                           default /var/backups/workout-tracker
#   BACKUP_RETENTION_DAYS   default 14
#   BACKUP_HEALTHCHECK_URL  optional. Fetched after a fully successful run.
#
# Restore: see "Restore drill" in deploy/README.md.

set -euo pipefail
set -o errtrace

# Dumps hold every user's data and password hashes: owner-only files.
umask 077

log() { printf '%s backup: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }
die() { log "ERROR: $*" >&2; exit 1; }
# $BASH_COMMAND is the command text before expansion, so it cannot leak secrets.
trap 'log "ERROR: command failed (line $LINENO): $BASH_COMMAND" >&2' ERR
# Never leave a half-written dump behind (partial is set in step 1).
partial=""
trap '[ -z "$partial" ] || rm -f "$partial"' EXIT

require_var() { [ -n "${!1:-}" ] || die "$1 is required"; }
require_cmd() { command -v "$1" >/dev/null 2>&1 || die "$1 is not installed or not in PATH"; }

# ---- configuration ----------------------------------------------------------

require_var DATABASE_URL
require_var MEDIA_DIR
BACKUP_DIR="${BACKUP_DIR:-/var/backups/workout-tracker}"
BACKUP_RETENTION_DAYS="${BACKUP_RETENTION_DAYS:-14}"
BACKUP_RSYNC_TARGET="${BACKUP_RSYNC_TARGET:-}"
BACKUP_RCLONE_REMOTE="${BACKUP_RCLONE_REMOTE:-}"

case "$BACKUP_RETENTION_DAYS" in
    '' | *[!0-9]* | 0) die "BACKUP_RETENTION_DAYS must be a positive integer, got '$BACKUP_RETENTION_DAYS'" ;;
esac

[ -d "$MEDIA_DIR" ] || die "MEDIA_DIR '$MEDIA_DIR' is not a directory"

if [ -n "$BACKUP_RSYNC_TARGET" ] && [ -n "$BACKUP_RCLONE_REMOTE" ]; then
    die "set only one of BACKUP_RSYNC_TARGET and BACKUP_RCLONE_REMOTE"
fi
if [ -z "$BACKUP_RSYNC_TARGET" ] && [ -z "$BACKUP_RCLONE_REMOTE" ]; then
    die "no off-box target: set BACKUP_RSYNC_TARGET or BACKUP_RCLONE_REMOTE (a backup on the same VPS is not a backup)"
fi

require_cmd pg_dump
require_cmd pg_restore
if [ -n "$BACKUP_RSYNC_TARGET" ]; then require_cmd rsync; else require_cmd rclone; fi

mkdir -p "$BACKUP_DIR"

# ---- 1. dump ----------------------------------------------------------------

stamp="$(date -u +%Y%m%dT%H%M%SZ)"
dump="$BACKUP_DIR/workout-$stamp.dump"
partial="$dump.partial"

# Leftovers of a run that was killed half way.
rm -f "$BACKUP_DIR"/workout-*.dump.partial

log "dumping the database to $dump"
# --no-password: fail instead of waiting for a prompt nobody will answer.
pg_dump --format=custom --compress=6 --no-password --dbname="$DATABASE_URL" --file="$partial"
# Reading the table of contents back proves the archive is complete and intact.
pg_restore --list "$partial" >/dev/null
mv "$partial" "$dump"
log "dump complete ($(du -h "$dump" | cut -f1))"

# ---- 2. local retention -----------------------------------------------------

log "removing local dumps older than $BACKUP_RETENTION_DAYS days"
# -mmin, not -mtime: -mtime +14 would keep files up to 15 days old.
find "$BACKUP_DIR" -maxdepth 1 -type f -name 'workout-*.dump' \
    -mmin +"$((BACKUP_RETENTION_DAYS * 24 * 60))" -print -delete

# ---- 3 and 4. copy off-box --------------------------------------------------

# rsync exit 24 means "some source files vanished during the transfer", which
# happens when an image upload replaces a temp file mid-copy. Not a failure.
rsync_ok() {
    local rc=0
    rsync "$@" || rc=$?
    case "$rc" in
        0) ;;
        24) log "warning: some files vanished during the transfer; continuing" ;;
        *) die "rsync failed (exit code $rc)" ;;
    esac
}

if [ -n "$BACKUP_RSYNC_TARGET" ]; then
    # BatchMode: never prompt for a passphrase or an unknown host key.
    ssh_cmd="ssh -o BatchMode=yes"
    if [ -n "${BACKUP_SSH_KEY:-}" ]; then
        ssh_cmd="$ssh_cmd -i $BACKUP_SSH_KEY"
    fi

    log "mirroring dumps to $BACKUP_RSYNC_TARGET/db/"
    rsync_ok -a --delete --include='workout-*.dump' --exclude='*' \
        -e "$ssh_cmd" "$BACKUP_DIR/" "$BACKUP_RSYNC_TARGET/db/"

    log "copying media to $BACKUP_RSYNC_TARGET/media/"
    rsync_ok -a -e "$ssh_cmd" "$MEDIA_DIR/" "$BACKUP_RSYNC_TARGET/media/"
else
    log "mirroring dumps to $BACKUP_RCLONE_REMOTE/db"
    rclone sync --include 'workout-*.dump' "$BACKUP_DIR" "$BACKUP_RCLONE_REMOTE/db"

    log "copying media to $BACKUP_RCLONE_REMOTE/media"
    rclone copy "$MEDIA_DIR" "$BACKUP_RCLONE_REMOTE/media"
fi

log "backup complete"

# ---- optional dead-man's switch ---------------------------------------------

# Only reached when everything above succeeded. A monitor that stops receiving
# this ping alerts you even if the timer itself silently stopped running.
if [ -n "${BACKUP_HEALTHCHECK_URL:-}" ]; then
    if command -v curl >/dev/null 2>&1; then
        curl -fsS -m 10 --retry 3 -o /dev/null "$BACKUP_HEALTHCHECK_URL" ||
            log "warning: could not reach BACKUP_HEALTHCHECK_URL"
    else
        log "warning: curl is not installed, healthcheck not pinged"
    fi
fi
