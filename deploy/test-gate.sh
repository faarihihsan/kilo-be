#!/usr/bin/env bash
#
# Rejection tests for deploy-gate.sh: the gate must reject any command outside
# its allow-list. Runs the gate locally against a throwaway directory; no server,
# no SSH, no network. From deploy/: `./test-gate.sh`.
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
gate="${here}/deploy-gate.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT

fails=0

# run <wanted-exit> <name> <SSH_ORIGINAL_COMMAND> [stdin-content]
run() {
    local want="$1" name="$2" command="$3" input="${4:-}"
    local out status=0
    out="$(KILO_APP_DIR="${tmp}" SSH_ORIGINAL_COMMAND="${command}" bash "${gate}" 2>&1 <<<"${input}")" || status=$?
    if [ "${status}" -eq "${want}" ]; then
        printf 'ok   %-28s exit %s\n' "${name}" "${status}"
    else
        printf 'FAIL %-28s exit %s (wanted %s)\n%s\n' "${name}" "${status}" "${want}" "${out}"
        fails=$((fails + 1))
    fi
}

# ---- everything not on the allow-list must be rejected (exit 1) ----
run 1 "empty command" ""
run 1 "interactive shell" "bash -i"
run 1 "arbitrary command" "id"
run 1 "read .env" "cat /home/ihsan/apps/workout-tracker/.env"
run 1 "write unknown file" "write evil.sh"
run 1 "write the gate itself" "write deploy-gate.sh"
run 1 "write .env" "write .env"
run 1 "write path traversal" "write ../.ssh/authorized_keys"
run 1 "write absolute path" "write /etc/passwd"
run 1 "write missing name" "write"
run 1 "deploy missing sha" "deploy"
run 1 "deploy uppercase sha" "deploy DEADBEEF1"
run 1 "deploy short sha" "deploy abc"
run 1 "deploy non-hex sha" "deploy zz112233"
run 1 "deploy extra argument" "deploy deadbeef1 extra"
run 1 "deploy shell metachar" "deploy deadbeef1; id"
# The literal $(...) must reach the gate unexpanded: it is attacker input.
# shellcheck disable=SC2016
run 1 "deploy command subst" 'deploy $(id)'
run 1 "rollback not offered" "rollback deadbeef1"
run 1 "unknown verb" "ls -la"
run 1 "write then command" "write deploy.sh; id"
run 1 "leading verb only" "shell"

# ---- allow-listed commands must pass (exit 0) ----
run 0 "write allowed file" "write deploy.sh" "echo stub"
run 0 "write compose.yml" "write compose.yml" "services: {}"

# A stub deploy.sh proves `deploy <sha>` runs it with TAG=<sha>. ${TAG} stays
# literal here on purpose: it is expanded by the stub, not by this script.
# shellcheck disable=SC2016
printf '%s\n' '#!/usr/bin/env bash' 'echo "stub deploy TAG=${TAG:-}"' >"${tmp}/deploy.sh"
chmod 0755 "${tmp}/deploy.sh"
out="$(KILO_APP_DIR="${tmp}" SSH_ORIGINAL_COMMAND="deploy deadbeef1" bash "${gate}" 2>&1)" || true
if [ "${out}" = "stub deploy TAG=deadbeef1" ]; then
    printf 'ok   %-28s runs deploy with TAG\n' "deploy valid sha"
else
    printf 'FAIL %-28s unexpected output: %s\n' "deploy valid sha" "${out}"
    fails=$((fails + 1))
fi

if [ "${fails}" -eq 0 ]; then
    echo "test-gate: all checks passed"
    exit 0
fi
echo "test-gate: ${fails} check(s) failed" >&2
exit 1
