#!/usr/bin/env bash
#
# Remote deploy script, run ON the home server by .github/workflows/release.yml:
#   ssh home 'cd ~/apps/workout-tracker && TAG=<sha> bash deploy.sh'
#
# It is a real file (not piped over stdin) on purpose: `docker compose exec`
# and `docker compose run` attach stdin, and when the script itself arrives on
# stdin they would swallow the rest of it. For the same reason every command
# that can read stdin gets an explicit </dev/null.
set -euo pipefail
cd "$(dirname "$0")"

: "${TAG:?deploy.sh needs TAG set to the image tag}"
echo "deploy: image tag ${TAG}"

# 1. Safety copy of the database before an image that may migrate lands.
if docker compose ps --status running --services | grep -qx postgres; then
    ts="$(date -u +%Y%m%dT%H%M%SZ)"
    mkdir -p backups
    docker compose exec -T postgres pg_dump -U workout -Fc workout \
        </dev/null > "backups/pre-deploy-$ts.dump"
    echo "deploy: pre-deploy dump backups/pre-deploy-$ts.dump"
    find backups -maxdepth 1 -type f -name 'pre-deploy-*.dump' -mtime +14 -delete
else
    echo "deploy: postgres not running; skipping pre-deploy dump"
fi

# 2. Pull the new image, apply migrations, restart the app.
echo "deploy: pulling"
docker compose pull
echo "deploy: migrating"
docker compose run --rm -T migrate </dev/null
echo "deploy: starting app"
docker compose up -d app

# 3. The new container must answer /healthz (process up + database reachable).
echo "deploy: waiting for /healthz"
for i in $(seq 1 20); do
    if curl -fsS http://127.0.0.1:8080/healthz >/dev/null; then
        echo "deploy: healthz OK after ${i} attempt(s)"
        exit 0
    fi
    sleep 3
done
echo "deploy: healthz did not come up" >&2
docker compose logs --tail 100 app >&2 || true
exit 1
