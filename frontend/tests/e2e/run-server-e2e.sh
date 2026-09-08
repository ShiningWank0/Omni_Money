#!/usr/bin/env bash
#
# Minimal server/browser E2E harness.
#
# Starts a disposable, hardened omni-money server container (same flags as the
# CI smoke test), runs the Playwright suite in frontend/tests/e2e against it,
# then verifies the control database stayed encrypted. Everything is isolated:
# a fresh data root under mktemp, per-run generated credentials, and a dynamic
# loopback host port. Host port 4000 and any pre-existing data are never used.
#
# Usage: bash tests/e2e/run-server-e2e.sh [--with-deps]
#   --with-deps  install Chromium OS dependencies (GitHub Actions runners).
#
# Required tooling: docker, python3, node/npm with frontend dependencies
# installed (npm ci --ignore-scripts).

set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FRONTEND_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
IMAGE="${OMNI_E2E_IMAGE:-omni-money:ci}"
INSTALL_WITH_DEPS=0
for arg in "$@"; do
  case "$arg" in
    --with-deps) INSTALL_WITH_DEPS=1 ;;
    *) echo "server-e2e: unknown argument: $arg" >&2; exit 2 ;;
  esac
done

fail() {
  echo "server-e2e: $*" >&2
  exit 1
}

command -v docker >/dev/null 2>&1 || fail "docker is required"
command -v python3 >/dev/null 2>&1 || fail "python3 is required for dynamic port allocation"
docker info >/dev/null 2>&1 || fail "docker daemon is not reachable"
docker image inspect "$IMAGE" >/dev/null 2>&1 \
  || fail "image $IMAGE not found; build it with: docker build --build-arg VERSION=ci --tag $IMAGE ."
[ -d "$FRONTEND_DIR/node_modules/@playwright/test" ] \
  || fail "frontend dependencies missing; run: (cd frontend && npm ci --ignore-scripts)"

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/omni-server-e2e.XXXXXX")"
container_id=""
cleanup() {
  local status=$?
  set +e
  if [ -n "$container_id" ]; then
    docker rm -f "$container_id" >/dev/null 2>&1 || true
  fi
  # On Linux the data dir is owned by UID 10001 (mode 0700), so a plain rm
  # fails for the runner user. Never block the exit on a dev machine without
  # passwordless sudo; warn about the leftover path instead.
  rm -rf -- "$tmp_dir" 2>/dev/null \
    || sudo -n rm -rf -- "$tmp_dir" 2>/dev/null \
    || echo "server-e2e: warning: could not remove $tmp_dir" >&2
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

data_dir="$tmp_dir/data"
attestation_file="$tmp_dir/attestation.json"
control_key_file="$tmp_dir/control_key"
setup_token_file="$tmp_dir/setup_token"
admin_password_file="$tmp_dir/admin_password"
mkdir -m 700 -p "$data_dir"

# --- per-run credentials (never echoed, never passed as arguments) ----------
# Cross-platform UTC date arithmetic: BSD date uses -v, GNU date uses -d.
iso_now() { date -u +%Y-%m-%dT%H:%M:%SZ; }
iso_future() {
  if date -u -v+1d +%F >/dev/null 2>&1; then
    date -u -v+"$1"d +%Y-%m-%dT%H:%M:%SZ
  else
    date -u -d "+$1 days" +%Y-%m-%dT%H:%M:%SZ
  fi
}
base64url_32_bytes() {
  head -c 32 /dev/urandom | base64 | tr '+/' '-_' | tr -d '=\n'
}

cat > "$attestation_file" <<EOF
{
  "version": 1,
  "protection": "external-encrypted-volume",
  "provider": "e2e-ephemeral-volume",
  "data_root": "/app/data",
  "key_id": "e2e-ephemeral-volume-2026",
  "verified_at": "$(iso_now)",
  "recovery_tested_at": "$(iso_now)",
  "next_rotation_at": "$(iso_future 180)"
}
EOF
base64_32_bytes="$(head -c 32 /dev/urandom | base64)"
printf '%s' "$base64_32_bytes" > "$control_key_file"
base64url_32_bytes > "$setup_token_file"
base64url_32_bytes > "$admin_password_file"
chmod 444 "$attestation_file" "$control_key_file" "$setup_token_file" "$admin_password_file"
# CI (Linux) mirrors the compose/CI ownership model; Docker Desktop (macOS)
# maps bind-mount ownership inside the VM, so a missing sudo is non-fatal.
if [ "$(uname -s)" = "Linux" ]; then
  sudo chown 0:0 "$attestation_file" "$control_key_file" "$setup_token_file" "$admin_password_file"
  sudo chmod 0444 "$attestation_file" "$control_key_file" "$setup_token_file" "$admin_password_file"
  sudo chown 10001:10001 "$data_dir"
fi

# --- dynamic loopback host port ---------------------------------------------
pick_free_port() {
  python3 - <<'PY'
import socket

sock = socket.socket()
sock.bind(("127.0.0.1", 0))
print(sock.getsockname()[1])
sock.close()
PY
}

start_container() {
  local host_port="$1"
  local error_file="$2"
  docker run -d \
    --read-only \
    --tmpfs /tmp:rw,noexec,nosuid,nodev,size=16m \
    --cap-drop ALL \
    --security-opt no-new-privileges \
    --health-interval 1s \
    --health-start-period 20s \
    --health-timeout 2s \
    --health-retries 10 \
    --mount "type=bind,src=$data_dir,dst=/app/data" \
    --mount "type=bind,src=$attestation_file,dst=/run/secrets/omni_data_at_rest_attestation.json,readonly" \
    --mount "type=bind,src=$control_key_file,dst=/run/secrets/omni_control_database_key,readonly" \
    --mount "type=bind,src=$setup_token_file,dst=/run/secrets/omni_initial_admin_setup_token,readonly" \
    --env CONTROL_DB_PATH=/app/data/control/omni_control.db \
    --env CONTROL_DB_ENCRYPTION_KEY_FILE=/run/secrets/omni_control_database_key \
    --env VAULT_ROOT=/app/data/vaults \
    --env INITIAL_ADMIN_SETUP_TOKEN_FILE=/run/secrets/omni_initial_admin_setup_token \
    --env DATA_AT_REST_MODE=external-encrypted-volume \
    --env DATA_AT_REST_ATTESTATION_FILE=/run/secrets/omni_data_at_rest_attestation.json \
    --env HOST_IP=0.0.0.0 \
    --env PORT=4000 \
    --env WEB_EXTERNAL_HOST=localhost \
    --env FORCE_HTTPS=false \
    --env ALLOW_INSECURE_HTTP=true \
    --env ALLOWED_HOSTS="localhost:$host_port,127.0.0.1:$host_port" \
    --publish "127.0.0.1:$host_port:4000" \
    "$IMAGE" 2>"$error_file"
}

wait_until_healthy() {
  local cid="$1"
  local health
  for _ in $(seq 1 90); do
    if ! health="$(docker inspect --format '{{.State.Health.Status}}' "$cid" 2>/dev/null)"; then
      return 1
    fi
    if [ "$health" = "healthy" ]; then
      return 0
    fi
    if [ "$health" = "unhealthy" ]; then
      return 1
    fi
    sleep 1
  done
  return 1
}

base_url=""
docker_run_error="$tmp_dir/docker_run_error"
for attempt in 1 2 3 4 5; do
  host_port="$(pick_free_port)"
  echo "server-e2e: starting hardened container (attempt $attempt, dynamic loopback port)"
  if ! container_id="$(start_container "$host_port" "$docker_run_error")"; then
    container_id=""
    echo "server-e2e: docker run failed (attempt $attempt)" >&2
    cat "$docker_run_error" >&2 || true
    continue
  fi
  if wait_until_healthy "$container_id"; then
    base_url="http://localhost:$host_port"
    break
  fi
  echo "server-e2e: container did not become healthy, retrying" >&2
  docker logs "$container_id" 2>&1 | tail -n 50 >&2 || true
  docker rm -f "$container_id" >/dev/null 2>&1 || true
  container_id=""
done
[ -n "$base_url" ] || fail "no healthy container after 5 attempts"

# --- run Playwright against the isolated server ------------------------------
export E2E_BASE_URL="$base_url"
export E2E_SETUP_TOKEN
E2E_SETUP_TOKEN="$(cat "$setup_token_file")"
export E2E_ADMIN_EMAIL="e2e-admin@example.com"
export E2E_ADMIN_PASSWORD
E2E_ADMIN_PASSWORD="$(cat "$admin_password_file")"

cd "$FRONTEND_DIR"
if [ "$INSTALL_WITH_DEPS" -eq 1 ]; then
  npx playwright install --with-deps chromium
else
  npx playwright install chromium
fi

playwright_status=0
npx playwright test || playwright_status=$?
# Playwright may leave an empty artifacts dir behind; keep the worktree clean.
rm -rf "$FRONTEND_DIR/test-results" "$FRONTEND_DIR/playwright-report"
if [ "$playwright_status" -ne 0 ]; then
  echo "server-e2e: playwright failed; last container log lines:" >&2
  docker logs "$container_id" 2>&1 | tail -n 200 >&2 || true
  fail "playwright suite failed (exit $playwright_status)"
fi

# --- encryption-at-rest inspection (same as the CI smoke test) ----------------
control_db="$data_dir/control/omni_control.db"
db_header_read() {
  if [ -r "$control_db" ]; then
    head -c 16 "$control_db"
  elif sudo -n test -f "$control_db" 2>/dev/null; then
    sudo -n head -c 16 "$control_db"
  else
    fail "control database not found at the expected data root path"
  fi
}
# Capture outside the pipeline so a missing control DB aborts the script
# (a fail() inside a pipeline subshell would only kill the subshell).
db_header="$(db_header_read)"
if printf '%s' "$db_header" | LC_ALL=C grep -q "SQLite format 3"; then
  fail "control database retained a plaintext SQLite header"
fi
if [ -d "$data_dir/vaults" ]; then
  :
elif sudo -n test -d "$data_dir/vaults" 2>/dev/null; then
  :
else
  fail "per-user vault directory was not created under the data root"
fi

echo "server-e2e: OK — UI flow passed against $base_url with an encrypted control database"
