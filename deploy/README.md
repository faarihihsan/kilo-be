# Deploying workout-tracker

One Go binary, PostgreSQL and Caddy on a single **2 vCPU / 2 GB** VPS (Debian or
Ubuntu LTS). Design background: [implementation plan, section 8](../docs/implementation-plan.md#8-deployment-on-the-vps-2-core--2-gb).

| File | Goes to | Purpose |
|------|---------|---------|
| `Caddyfile` | `/etc/caddy/Caddyfile` | HTTPS, `/media/*` from disk, everything else to the app |
| `workout-tracker.service` | `/etc/systemd/system/` | runs `server migrate up`, then `server serve` |
| `env.example` | `/etc/workout-tracker/env` (`chmod 600`) | all configuration, see comments inside |
| `postgresql-tuning.conf` | `/etc/postgresql/<ver>/main/conf.d/workout-tracker.conf` | memory settings for 2 GB |
| `backup.sh`, `workout-tracker-backup.{service,timer}` | `/opt/workout-tracker/`, `/etc/systemd/system/` | nightly dump + media, off the VPS |

## Setup checklist

Do these once, in order. Tick them off; skipping the backup or firewall items is the usual way this goes wrong.

- [ ] **DNS**: an A/AAAA record for the API host name points at the VPS. Replace `api.example.com` in `Caddyfile` and `MEDIA_BASE_URL`.
- [ ] **SSH**: key-only login (`PasswordAuthentication no`, `PermitRootLogin no`), a non-root sudo user.
- [ ] **Firewall**: `ufw default deny incoming`, `ufw allow 22,80,443/tcp`, `ufw enable`. Port **5432 stays closed**; from another machine confirm with `nmap -p 22,80,443,5432 <ip>`.
- [ ] **Swap 1 GB** as a safety net against OOM kills: `fallocate -l 1G /swapfile && chmod 600 /swapfile && mkswap /swapfile && swapon /swapfile`, then add `/swapfile none swap sw 0 0` to `/etc/fstab`.
- [ ] **PostgreSQL** (latest stable major, same as CI): install, copy `postgresql-tuning.conf` (shared_buffers 256MB, effective_cache_size 1GB, work_mem 4MB, max_connections 20, localhost only), `systemctl restart postgresql`. Create the role and database: `sudo -u postgres createuser --pwprompt workout && sudo -u postgres createdb -O workout workout`. Confirm `ss -ltn | grep 5432` shows only `127.0.0.1`.
- [ ] **App user and directories**:
  `useradd --system --create-home --home-dir /var/lib/workout-tracker --shell /usr/sbin/nologin workout`,
  `chmod 755 /var/lib/workout-tracker` (Caddy must be able to enter it),
  `install -d -o workout -g workout -m 755 /var/lib/workout-tracker/media /var/backups/workout-tracker /opt/workout-tracker`.
- [ ] **Binary**: `make build` builds `bin/server` for the local machine; for the VPS use `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bin/server ./cmd/server`, copy it to `/opt/workout-tracker/server` (owner root, mode 755).
- [ ] **Configuration**: `install -d /etc/workout-tracker && install -o root -g root -m 600 env.example /etc/workout-tracker/env`, then edit it (database password, `MEDIA_BASE_URL`, backup target). The server refuses to start and lists every problem if a value is missing or invalid.
- [ ] **Service**: install `workout-tracker.service`, `systemctl daemon-reload && systemctl enable --now workout-tracker`, check `journalctl -u workout-tracker -o cat`. **Only ever one instance**: the login rate limiter is in memory.
- [ ] **Caddy** (official package): install `Caddyfile`, `systemctl reload caddy`. The `root` in the `/media/*` block must equal `MEDIA_DIR`. **HTTPS is mandatory**: check `curl -I http://<host>/healthz` redirects to https and `curl https://<host>/healthz` returns 200.
- [ ] **Uptime check**: an external monitor (UptimeRobot, healthchecks.io, ...) polls `https://<host>/healthz` (process up and database reachable) and alerts you.
- [ ] **First admin**: `systemd-run --pty --wait --collect -p User=workout -p EnvironmentFile=/etc/workout-tracker/env /opt/workout-tracker/server admin create-user --username <name> --role admin` (same environment as the service; the password is prompted, not passed as a flag). Use the same pattern for any other `server admin ...` or `server media gc` command.
- [ ] **Logs**: JSON on stdout into journald; cap it with `SystemMaxUse=200M` in `/etc/systemd/journald.conf`.
- [ ] **Backups**, see below, and the **restore drill before real use**.

## Deploying a new version

1. Take a backup first if the release adds a migration (`systemctl start workout-tracker-backup`). Migrations are forward-only.
2. Copy the new binary to `/opt/workout-tracker/server`, then `systemctl restart workout-tracker`. The unit runs `server migrate up` before `serve`; if it fails the service stays down and `journalctl` says why.
3. `curl https://<host>/healthz`.

## Backups

`backup.sh` writes a compressed `pg_dump -Fc`, verifies it, keeps 14 days, and copies the dumps and `MEDIA_DIR` **off the VPS** (rsync over SSH or rclone). It exits non-zero on any failure. Details are in the script header.

1. Install `postgresql-client` (its major version must be at least the server's), plus `rsync` or `rclone`.
2. Set the `BACKUP_*` values in `/etc/workout-tracker/env` (exactly one target). For rsync, create a passphrase-less key for the backup host, put it at `BACKUP_SSH_KEY` (owner `workout`, mode 600), and accept the host key once: `sudo -u workout ssh -i <key> <user>@<host> true`.
3. `install -m 755 backup.sh /opt/workout-tracker/`, install the `.service` and `.timer`, `systemctl daemon-reload && systemctl enable --now workout-tracker-backup.timer`.
4. Run it once by hand and read the log: `systemctl start workout-tracker-backup && journalctl -u workout-tracker-backup -e`.

### Restore drill

Do this once before relying on the backups, and again after changing the backup setup. Never restore over the live database.

1. Fetch the **off-box** copy, not the local one: the newest `db/workout-*.dump` and the `media/` directory.
2. Restore into a scratch database:
   `sudo -u postgres createdb workout_restore && sudo -u postgres pg_restore --no-owner --exit-on-error --dbname workout_restore workout-<stamp>.dump`
3. Compare with production: `select count(*) from users;` and the same for `exercises`, `workout_plans`, `progress` in both databases; check `select max(server_updated_at) from progress;` matches last night's state.
4. Media: confirm the number of files under `media/exercises/` matches `MEDIA_DIR`, and that a file named by a row (`select id, image_hash, image_ext from exercises where image_hash is not null limit 1;` maps to `exercises/<id>/<hash>.<ext>`) exists.
5. Optional full test: start the server against `workout_restore` on another port with the restored media and call `/healthz`.
6. `dropdb workout_restore`, and note how long it took.

Real disaster: new VPS, run this checklist, restore the dump into the empty `workout` database with `pg_restore --no-owner --role workout` (so the app user owns the tables) instead of `workout_restore`, copy the media back into `MEDIA_DIR` (`chown -R workout:workout`), start the service, run `server media gc --dry-run`.

---

# Homeserver deployment (Docker + CI/CD)

The production target is the home server (`ihsan-home-server`, LAN `192.168.1.2`,
reachable remotely as `homeserver-remote` through the VPS jump host). Public
traffic reaches it as `https://kilo.faarihsan.com`:

```
client --> VPS nginx :443 (Let's Encrypt) --WireGuard--> home nginx :80 --+
              /media/*  -> ~/apps/workout-tracker/media (disk)            |
              /*        -> 127.0.0.1:8080 workout_app (Docker) <-------+ 
                                     |
                            workout_postgres 127.0.0.1:5432 (Docker)
```

Everything runs in `~/apps/workout-tracker` on the internal SSD. One folder per
app, matching Immich/Grafana. Host networking + loopback only; nginx is the sole
entry point.

## Files (in `deploy/`)

| File | Goes to |
|------|---------|
| `compose.yml` | `~/apps/workout-tracker/compose.yml` |
| `env.docker.example` | copy to `~/apps/workout-tracker/.env` (chmod 600) |
| `nginx-kilo-home.conf` | home `/etc/nginx/sites-available/kilo` |
| `nginx-kilo-vps.conf` | VPS `/etc/nginx/sites-available/kilo` |
| `backup-db.sh` | `~/apps/workout-tracker/backup-db.sh` (chmod 755) |

## One-time setup

1. Directories and config (on the server):
   ```bash
   mkdir -p ~/apps/workout-tracker/{media,postgres,backups}
   cp compose.yml ~/apps/workout-tracker/
   cp env.docker.example ~/apps/workout-tracker/.env   # then edit it
   chmod 600 ~/apps/workout-tracker/.env
   ```
   Set a strong `POSTGRES_PASSWORD` and the matching, percent-encoded password
   in `DATABASE_URL`.
2. First bring-up (use a real image tag from GHCR):
   ```bash
   cd ~/apps/workout-tracker
   TAG=<git-sha> docker compose pull
   TAG=<git-sha> docker compose up -d postgres
   TAG=<git-sha> docker compose run --rm migrate
   TAG=<git-sha> docker compose up -d app
   docker compose run --rm app admin reset-password --username admin  # retire admin/admin
   ```
3. nginx + TLS:
   ```bash
   # home
   sudo cp /path/to/nginx-kilo-home.conf /etc/nginx/sites-available/kilo
   sudo ln -s /etc/nginx/sites-available/kilo /etc/nginx/sites-enabled/kilo
   sudo nginx -t && sudo systemctl reload nginx
   # VPS
   sudo cp /path/to/nginx-kilo-vps.conf /etc/nginx/sites-available/kilo
   sudo ln -s /etc/nginx/sites-available/kilo /etc/nginx/sites-enabled/kilo
   sudo nginx -t && sudo systemctl reload nginx
   sudo certbot --nginx -d kilo.faarihsan.com
   ```
   Verify: `curl -fsS https://kilo.faarihsan.com/healthz`.

## Backups

`backup-db.sh` runs on the host as `ihsan` and dumps the database into
`/mnt/data/db-dumps/`, which the existing nightly restic job already backs up.
Add a cron entry:
```
20 3 * * *  /home/ihsan/apps/workout-tracker/backup-db.sh >> /home/ihsan/apps/workout-tracker/backup.log 2>&1
```
It also writes `~/apps/grafana/textfile/workout-backup.prom` for Grafana alerts.
The live `~/apps/*/postgres` files are intentionally excluded from restic; the
dump is the consistent copy.

## CI/CD

`.github/workflows/release.yml`, on every push to `main`:

1. `ci` — calls `ci.yml` (gofmt, vet, golangci-lint, `go test -race`).
2. `build` — builds and pushes `ghcr.io/faarihihsan/kilo-be:{sha,latest}` to GHCR.
3. `deploy` — gated by the **`production`** GitHub environment (required
   reviewer). After approval it SSHes through the VPS jump host, takes a
   `pg_dump` safety copy, pulls the new tag, migrates, restarts the app, and
   polls `/healthz`.

Required GitHub secret (in the `production` environment):
`DEPLOY_SSH_KEY` (private key authorized on the VPS *and* home server).

The servers' public host keys are pinned in `deploy/known_hosts` (names must be
`116.212.74.54` and `10.8.0.2`). If a server's host keys change, regenerate it:
`ssh-keyscan 116.212.74.54`, and `ssh-keyscan 10.8.0.2` run on the home server.

Rollback: approve a deploy of a previous `{sha}` by re-running an older
workflow run, or on the server `TAG=<old-sha> docker compose up -d app`.
