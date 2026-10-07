#!/usr/bin/env bash
#
# Nightly database dump for workout-tracker on the home server.
#
# Runs on the HOST as `ihsan` (cron), not in a container. It dumps the database
# out of the running postgres container in custom format into $DUMP_DIR, verifies
# the archive, prunes old dumps, and writes a Prometheus textfile metric so
# Grafana can alert on a failed or stale backup.
#
# $DUMP_DIR lives under /mnt/data, which the existing nightly restic job already
# backs up (and it excludes ~/apps/*/postgres, the live DB files, on purpose).
#
# Suggested cron (as ihsan):
#   crontab -e
#   20 3 * * *  /home/ihsan/apps/workout-tracker/backup-db.sh >> /home/ihsan/apps/workout-tracker/backup.log 2>&1
#
# Restore (outline): fetch the newest dump plus the media directory from the
# restic snapshot, create an empty database, and
#   pg_restore --no-owner --role workout --dbname <db> workout-<stamp>.dump

set -euo pipefail
set -o errtrace

# Dumps hold every user's rows and password hashes: owner-only files.
umask 077

APP_DIR="${APP_DIR:-$HOME/apps/workout-tracker}"
DUMP_DIR="${DUMP_DIR:-/mnt/data/db-dumps}"
TEXTFILE_DIR="${TEXTFILE_DIR:-$HOME/apps/grafana/textfile}"
RETENTION_DAYS="${RETENTION_DAYS:-14}"
METRIC="$TEXTFILE_DIR/workout-backup.prom"
COMPOSE=(docker compose -f "$APP_DIR/compose.yml")

log() { printf '%s workout-backup: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }
die() { log "ERROR: $*" >&2; exit 1; }

write_metric() { # $1 = success (0|1); $2 = last-success epoch seconds
    mkdir -p "$TEXTFILE_DIR"
    {
        echo "# HELP workout_backup_success 1 if the last dump run succeeded"
        echo "# TYPE workout_backup_success gauge"
        echo "workout_backup_success $1"
        echo "# HELP workout_backup_last_success_timestamp_seconds Unix time of the last successful dump"
        echo "# TYPE workout_backup_last_success_timestamp_seconds gauge"
        echo "workout_backup_last_success_timestamp_seconds $2"
    } > "$METRIC.tmp"
    mv "$METRIC.tmp" "$METRIC"
}

# On failure, publish success=0 but keep the previous last-success time.
last_success=0
if [ -f "$METRIC" ]; then
    last_success="$(awk '/^workout_backup_last_success_timestamp_seconds /{print $2}' "$METRIC" 2>/dev/null || echo 0)"
    [ -n "$last_success" ] || last_success=0
fi
trap 'write_metric 0 "$last_success"' ERR

stamp="$(date -u +%Y%m%dT%H%M%SZ)"
dump="$DUMP_DIR/workout-$stamp.dump"
partial="$dump.partial"
trap 'rm -f "$partial"' EXIT

mkdir -p "$DUMP_DIR"
rm -f "$DUMP_DIR"/workout-*.dump.partial

# The postgres container's PGDATA is local-only; dumping through the container
# is the consistent copy. -U postgres uses the peer-auth superuser (the image
# always has it) so no password is needed on the command line.
log "dumping workout database to $dump"
"${COMPOSE[@]}" exec -T postgres pg_dump -U workout -Fc workout > "$partial"
"${COMPOSE[@]}" exec -T postgres pg_restore -l < "$partial" >/dev/null
mv "$partial" "$dump"
log "dump complete ($(du -h "$dump" | cut -f1))"

log "removing local dumps older than $RETENTION_DAYS days"
find "$DUMP_DIR" -maxdepth 1 -type f -name 'workout-*.dump' \
    -mmin +"$((RETENTION_DAYS * 24 * 60))" -print -delete

write_metric 1 "$(date +%s)"
log "backup complete"
