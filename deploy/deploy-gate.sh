#!/usr/bin/env bash
#
# Forced command for the restricted `kilo-ci` SSH key on the HOME server.
# The key's authorized_keys line is:
#   restrict,command="/home/ihsan/apps/workout-tracker/deploy-gate.sh" ssh-ed25519 ...
# so an SSH session from CI can run ONLY the commands below; there is no shell.
# The VPS key is jump-only:
#   restrict,port-forwarding,permitopen="10.8.0.2:22",command="/usr/bin/false" ssh-ed25519 ...
# `command=` is required: `restrict` alone still lets the key run commands on the
# VPS (ihsan has NOPASSWD sudo there). ProxyJump uses a forwarded channel, not a
# session, so the forced command never interferes with the jump.
# A leaked GitHub secret therefore cannot open a shell anywhere.
#
# This script is installed ONCE BY HAND (mode 0755), never by CI:
#   ssh homeserver 'cat > ~/apps/workout-tracker/deploy-gate.sh' < deploy/deploy-gate.sh
#   ssh homeserver 'chmod 755 ~/apps/workout-tracker/deploy-gate.sh'
# It is not in the allow-list below, so CI cannot overwrite it.
#
# Allowed commands (read from $SSH_ORIGINAL_COMMAND):
#   write <name>   name from the fixed list below; body on stdin -> temp -> mv
#   deploy <sha>   TAG=<sha> bash deploy.sh
# <sha> must match ^[0-9a-f]{7,40}$. Everything else is rejected.
set -euo pipefail

# Overridable only so deploy/test-gate.sh can run the gate against a temp dir.
APP_DIR="${KILO_APP_DIR:-/home/ihsan/apps/workout-tracker}"
cd "${APP_DIR}"

reject() {
    printf 'deploy-gate: rejected: %s\n' "$1" >&2
    exit 1
}

# Fixed allow-list of files CI may write. Names are matched exactly, so path
# traversal (../, an absolute path) can never match.
allowed_files="compose.yml deploy.sh"

is_allowed_file() {
    local f
    for f in ${allowed_files}; do
        [ "$1" = "${f}" ] && return 0
    done
    return 1
}

# <sha>: a git commit id (short or full). Never a path, never shell.
is_sha() {
    case "$1" in
    *[!0-9a-f]* | "") return 1 ;;
    esac
    [ "${#1}" -ge 7 ] && [ "${#1}" -le 40 ]
}

# `write <name>`: read the whole body from stdin into a temp file on the same
# filesystem, then mv over the target (atomic replace; a partial upload never
# becomes the live file).
write_file() {
    local name="$1" tmp
    is_allowed_file "${name}" || reject "write: '${name}' is not an allowed file"
    tmp="$(mktemp "${name}.XXXXXX")"
    cat >"${tmp}"
    case "${name}" in
    *.sh) chmod 0755 "${tmp}" ;;
    *) chmod 0644 "${tmp}" ;;
    esac
    mv -f "${tmp}" "${name}"
    printf 'deploy-gate: wrote %s\n' "${name}"
}

# One optional first argument, never more: reject `deploy <sha> extra`.
cmd="${SSH_ORIGINAL_COMMAND:-}"
[ -n "${cmd}" ] || reject "no command (an interactive shell is not permitted)"

verb=""
arg=""
rest=""
read -r verb arg rest <<<"${cmd}" || true
verb="${verb:-}"
arg="${arg:-}"
if [ -n "${rest}" ]; then
    reject "too many arguments: '${cmd}'"
fi
[ -n "${verb}" ] || reject "empty command"

case "${verb}" in
write)
    [ -n "${arg}" ] || reject "write: missing file name"
    write_file "${arg}"
    ;;
deploy)
    [ -n "${arg}" ] || reject "deploy: missing sha"
    is_sha "${arg}" || reject "deploy: '${arg}' is not a commit sha"
    exec env TAG="${arg}" bash deploy.sh
    ;;
*)
    reject "unknown command '${verb}'"
    ;;
esac
