#!/usr/bin/env bash
#
# Docker-free regression suite for scripts/backup-data-root.sh.
#
# The real backup script runs against a mock Docker CLI, a mock stat (so the
# 10001:10001 ownership contract can be asserted without root), and no-op
# sync/findmnt shims. tar, sha256sum, du, df and find are the real host
# binaries, so archive creation, member validation, checksum sidecars and the
# plaintext-header rejection are exercised for real. The tar wrapper reports a
# GNU version because the script requires GNU tar (CI runs real GNU tar; on
# macOS the wrapper delegates to bsdtar, which understands every flag used).
#
# Usage:
#   scripts/backup-data-root_test.sh
#   BACKUP_ONLY=<scenario> BACKUP_EXPECTED=<exit> scripts/backup-data-root_test.sh
#
# Scenarios:
#   success                 full cycle: stop, archive, manifest, restart, verify
#   verify_detects_tamper   --verify fails after the archive is modified
#   backup_rejects_plaintext plaintext SQLite header in the control DB fails closed
#   symlink_source_rejected symlink in the data tree fails before the service stops
#   stop_failure            docker stop failure leaves the service untouched
#   restart_unhealthy       unhealthy restart keeps the archive and fails loudly
#   concurrent_lock         an existing lock blocks a second backup
#   keep_prunes_oldest      --keep N removes the oldest generations only

set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BACKUP_SCRIPT="$SCRIPT_DIR/backup-data-root.sh"
[ -x "$BACKUP_SCRIPT" ] || { echo "backup-data-root_test: $BACKUP_SCRIPT is not executable" >&2; exit 1; }

test_root="$(mktemp -d "${TMPDIR:-/tmp}/.omni-backup-test.XXXXXX")"
trap 'status=$?; if [ "$FAILURES" -eq 0 ]; then rm -rf -- "$test_root"; else echo "backup-data-root_test: fixtures kept at $test_root" >&2; fi; exit $status' EXIT

# resolve the real host binaries before the mock bin shadows PATH
REAL_STAT="$(command -v stat)"
REAL_TAR="$(command -v tar)"
REAL_DOCKER="$(command -v docker || true)"

FAILURES=0

timeout_wrap() {
  local seconds="$1"
  shift
  perl -e 'alarm shift; exec @ARGV' "$seconds" "$@"
}

assert_eq() {
  local label="$1" expected="$2" actual="$3"
  if [ "$expected" != "$actual" ]; then
    echo "FAIL: $label: expected '$expected', got '$actual'" >&2
    FAILURES=$((FAILURES + 1))
    return 1
  fi
}

assert_file() {
  if [ ! -f "$1" ]; then
    echo "FAIL: expected file to exist: $1" >&2
    FAILURES=$((FAILURES + 1))
    return 1
  fi
}

assert_no_file() {
  if [ -e "$1" ]; then
    echo "FAIL: expected file to be absent: $1" >&2
    FAILURES=$((FAILURES + 1))
    return 1
  fi
}

new_fixture() {
  fixture_root="$test_root/$1"
  mock_bin="$fixture_root/mock-bin"
  mock_state="$fixture_root/state"
  mkdir -p "$mock_bin" "$mock_state"
  MOCK_DATA="$fixture_root/data"
  MOCK_CONTROL_KEY="$fixture_root/control.key"
  export MOCK_DATA MOCK_CONTROL_KEY
  mkdir -m 700 -p "$fixture_root/project"
  cat > "$fixture_root/project/compose.yaml" <<'YAML'
services:
  omni-money:
    image: omni-money:ci
    user: "10001:10001"
YAML
  mkdir -m 700 -p "$MOCK_DATA/control" "$MOCK_DATA/vaults"
  mkdir -m 700 -p "$MOCK_DATA/vaults/VaultLedger01/snapshots"
  # random ciphertext-looking bytes that never start with the SQLite header
  { printf '\001\002'; head -c 62 /dev/urandom; } > "$MOCK_DATA/control/omni_control.db"
  { printf '\003\004'; head -c 62 /dev/urandom; } > "$MOCK_DATA/vaults/VaultLedger01/ledger.db"
  head -c 32 /dev/urandom > "$MOCK_DATA/vaults/VaultLedger01/snapshots/omni_money_20260101T000000Z_abcdef01.db"
  head -c 32 /dev/urandom > "$MOCK_CONTROL_KEY"
  chmod 700 "$MOCK_DATA"
  chmod 600 "$MOCK_DATA"/control/omni_control.db "$MOCK_DATA"/vaults/VaultLedger01/ledger.db \
    "$MOCK_DATA"/vaults/VaultLedger01/snapshots/*.db "$MOCK_CONTROL_KEY"
  printf 'mock-container-0001\n' > "$mock_state/container_id"
  printf 'running\n' > "$mock_state/status"
  printf 'healthy\n' > "$mock_state/health"
  export MOCK_STATE_DIR="$mock_state"
}

build_mocks() {
  cat > "$mock_bin/docker" <<MOCK_DOCKER
#!/usr/bin/env bash
set -Eeuo pipefail
state="\$MOCK_STATE_DIR"
case "\$*" in
  *"ps --all -q"*)
    [ "\${MOCK_SCENARIO:-}" = "missing_container" ] && exit 0
    cat "\$state/container_id"
    ;;
  *"{{range .Mounts}}"*)
    printf '/app/data=%s\n' "\$MOCK_DATA"
    ;;
  *"{{.State.Status}}"*)
    cat "\$state/status"
    ;;
  *".State.Health"*)
    cat "\$state/health"
    ;;
  stop*)
    if [ "\${MOCK_SCENARIO:-}" = "stop_failure" ]; then
      echo "mock docker stop failure" >&2
      exit 1
    fi
    printf 'mock\n'
    printf 'exited\n' > "\$state/status"
    ;;
  start*)
    printf 'mock\n'
    printf 'running\n' > "\$state/status"
    ;;
  *)
    echo "backup-data-root_test: unexpected docker invocation: \$*" >&2
    exit 99
    ;;
esac
MOCK_DOCKER
  cat > "$mock_bin/stat" <<MOCK_STAT
#!/usr/bin/env bash
set -Eeuo pipefail
# the backup script always passes exactly: stat -c '<fmt>' -- <path>
fmt="\$2"
path="\$4"
case "\$fmt:\$path" in
  "%u:\$MOCK_DATA"|"%u:\$MOCK_DATA"/*) echo 10001; exit 0 ;;
  "%g:\$MOCK_DATA"|"%g:\$MOCK_DATA"/*) echo 10001; exit 0 ;;
esac
exec "$REAL_STAT" "\$@"
MOCK_STAT
  cat > "$mock_bin/sync" <<'MOCK_SYNC'
#!/usr/bin/env bash
exit 0
MOCK_SYNC
  cat > "$mock_bin/findmnt" <<'MOCK_FINDMNT'
#!/usr/bin/env bash
printf '/\n'
MOCK_FINDMNT
  cat > "$mock_bin/tar" <<MOCK_TAR
#!/usr/bin/env bash
set -Eeuo pipefail
if [ "\${1:-}" = "--version" ]; then
  printf 'tar (GNU tar) 1.35\n'
  exit 0
fi
exec "$REAL_TAR" "\$@"
MOCK_TAR
  chmod +x "$mock_bin/docker" "$mock_bin/stat" "$mock_bin/sync" "$mock_bin/findmnt" "$mock_bin/tar"
}

newest_generation() {
  ls -1 "$1" 2>/dev/null | grep -E '^[0-9]{8}T[0-9]{6}Z$' | sort -r | head -n 1
}

invoke_backup() {
  timeout_wrap 120 env PATH="$mock_bin:$PATH" MOCK_SCENARIO="$MOCK_SCENARIO" \
    MOCK_DATA="$MOCK_DATA" MOCK_STATE_DIR="$MOCK_STATE_DIR" \
    bash "$BACKUP_SCRIPT" "$@"
}

run_case() {
  local name="$1" expected="$2" status
  echo "=== scenario: $name (expected exit $expected)"
  MOCK_SCENARIO="$name"
  export MOCK_SCENARIO
  SCENARIO_ARGS=""
  "scenario_$name"
  set +e
  invoke_backup $SCENARIO_ARGS > "$fixture_root/stdout.log" 2> "$fixture_root/stderr.log"
  status=$?
  set -e
  if [ "$status" -ne "$expected" ]; then
    echo "FAIL: $name: exit $status, expected $expected" >&2
    echo "--- stdout ---" >&2; cat "$fixture_root/stdout.log" >&2
    echo "--- stderr ---" >&2; cat "$fixture_root/stderr.log" >&2
    FAILURES=$((FAILURES + 1))
    return 1
  fi
  if declare -F "post_$name" >/dev/null 2>&1; then
    "post_$name" || true
  fi
}

# ------------------------------------------------------------------ scenarios

scenario_success() {
  new_fixture success
  build_mocks
  mkdir -p "$MOCK_DATA/vaults/VaultLedger02"
  { printf '\005\006'; head -c 62 /dev/urandom; } > "$MOCK_DATA/vaults/VaultLedger02/ledger.db"
  chmod 600 "$MOCK_DATA/vaults/VaultLedger02/ledger.db"
}

post_success() {
  local generation manifest
  generation="$(newest_generation "$fixture_root/omni-money-backups")"
  [ -n "$generation" ] || { echo "FAIL: no backup generation created" >&2; FAILURES=$((FAILURES + 1)); return 1; }
  local dir="$fixture_root/omni-money-backups/$generation"
  assert_file "$dir/data.tar"
  assert_file "$dir/data.tar.sha256"
  assert_file "$dir/manifest.json"
  assert_eq "service restarted" "running" "$(cat "$mock_state/status")"
  assert_eq "health still healthy" "healthy" "$(cat "$mock_state/health")"
  ( cd "$dir" && sha256sum --check --strict data.tar.sha256 >/dev/null ) \
    || { echo "FAIL: checksum sidecar does not match" >&2; FAILURES=$((FAILURES + 1)); }
  # every archived ledger must match the source bytes byte for byte
  local extract="$fixture_root/extract-check"
  mkdir -p "$extract"
  tar -xpf "$dir/data.tar" -C "$extract"
  diff -r "$MOCK_DATA" "$extract" > /dev/null \
    || { echo "FAIL: archive contents differ from the source tree" >&2; FAILURES=$((FAILURES + 1)); }
  grep -q '"vault_count": 2' "$dir/manifest.json" \
    || { echo "FAIL: manifest does not record two vaults" >&2; FAILURES=$((FAILURES + 1)); }
  grep -q '"VaultLedger02"' "$dir/manifest.json" \
    || { echo "FAIL: manifest is missing the second vault id" >&2; FAILURES=$((FAILURES + 1)); }
  assert_no_file "$fixture_root/omni-money-backups/.backup.lock"
  # standalone verification of the published generation must pass
  set +e
  invoke_backup --verify "$dir" > /dev/null 2>&1
  local verify_status=$?
  set -e
  assert_eq "standalone --verify" "0" "$verify_status"
}

scenario_verify_detects_tamper() {
  new_fixture verify_detects_tamper
  build_mocks
}

post_verify_detects_tamper() {
  local generation dir
  generation="$(newest_generation "$fixture_root/omni-money-backups")"
  dir="$fixture_root/omni-money-backups/$generation"
  assert_file "$dir/data.tar"
  printf 'x' >> "$dir/data.tar"
  set +e
  invoke_backup --verify "$dir" > /dev/null 2>&1
  local verify_status=$?
  set -e
  assert_eq "tampered archive rejected" "1" "$verify_status"
}

scenario_backup_rejects_plaintext() {
  new_fixture backup_rejects_plaintext
  build_mocks
  printf 'SQLite format 3\000corrupted-by-design...' > "$MOCK_DATA/control/omni_control.db"
}

post_backup_rejects_plaintext() {
  local generation dir
  generation="$(newest_generation "$fixture_root/omni-money-backups")"
  [ -n "$generation" ] || { echo "FAIL: generation dir missing (fail-closed path should keep the staged archive)" >&2; FAILURES=$((FAILURES + 1)); return 1; }
  dir="$fixture_root/omni-money-backups/$generation"
  assert_file "$dir/data.tar"
  assert_no_file "$dir/manifest.json"
  # the EXIT trap must have restarted the stopped service
  assert_eq "service restarted after failure" "running" "$(cat "$mock_state/status")"
  assert_no_file "$fixture_root/omni-money-backups/.backup.lock"
}

scenario_symlink_source_rejected() {
  new_fixture symlink_source_rejected
  build_mocks
  ln -s "$MOCK_CONTROL_KEY" "$MOCK_DATA/vaults/escape-link"
}

post_symlink_source_rejected() {
  assert_eq "service never stopped" "running" "$(cat "$mock_state/status")"
  [ ! -d "$fixture_root/omni-money-backups" ] \
    || { echo "FAIL: destination root should not exist for a pre-stop failure" >&2; FAILURES=$((FAILURES + 1)); }
}

scenario_stop_failure() {
  new_fixture stop_failure
  build_mocks
}

post_stop_failure() {
  assert_eq "service untouched after stop failure" "running" "$(cat "$mock_state/status")"
  # the destination root exists (lock acquired pre-stop) but no archive may be
  # published, and the lock must have been released by the EXIT trap
  local generation
  generation="$(newest_generation "$fixture_root/omni-money-backups")"
  if [ -n "$generation" ]; then
    assert_no_file "$fixture_root/omni-money-backups/$generation/data.tar"
    assert_no_file "$fixture_root/omni-money-backups/$generation/manifest.json"
  fi
  assert_no_file "$fixture_root/omni-money-backups/.backup.lock"
}

scenario_restart_unhealthy() {
  new_fixture restart_unhealthy
  build_mocks
  printf 'unhealthy\n' > "$mock_state/health"
}

post_restart_unhealthy() {
  local generation dir
  generation="$(newest_generation "$fixture_root/omni-money-backups")"
  dir="$fixture_root/omni-money-backups/$generation"
  assert_file "$dir/data.tar"
  assert_file "$dir/manifest.json"
  assert_eq "service restarted (mock)" "running" "$(cat "$mock_state/status")"
}

scenario_concurrent_lock() {
  new_fixture concurrent_lock
  build_mocks
  mkdir -p "$fixture_root/omni-money-backups"
  printf '999999\n' > "$fixture_root/omni-money-backups/.backup.lock"
}

post_concurrent_lock() {
  assert_eq "service untouched under lock" "running" "$(cat "$mock_state/status")"
  [ -f "$fixture_root/omni-money-backups/.backup.lock" ] \
    || { echo "FAIL: pre-existing lock must not be removed by the losing run" >&2; FAILURES=$((FAILURES + 1)); }
  [ -z "$(newest_generation "$fixture_root/omni-money-backups")" ] \
    || { echo "FAIL: no generation should be created under a foreign lock" >&2; FAILURES=$((FAILURES + 1)); }
}

scenario_keep_prunes_oldest() {
  new_fixture keep_prunes_oldest
  build_mocks
  SCENARIO_ARGS="--keep 2"
  mkdir -p "$fixture_root/omni-money-backups/20240101T000000Z" \
           "$fixture_root/omni-money-backups/20240102T000000Z" \
           "$fixture_root/omni-money-backups/20240103T000000Z"
  head -c 8 /dev/urandom > "$fixture_root/omni-money-backups/20240101T000000Z/data.tar"
  head -c 8 /dev/urandom > "$fixture_root/omni-money-backups/20240102T000000Z/data.tar"
  head -c 8 /dev/urandom > "$fixture_root/omni-money-backups/20240103T000000Z/data.tar"
}

post_keep_prunes_oldest() {
  local backups="$fixture_root/omni-money-backups"
  [ -d "$backups/20240103T000000Z" ] \
    || { echo "FAIL: newest old generation should be retained" >&2; FAILURES=$((FAILURES + 1)); }
  [ -d "$backups/20240102T000000Z" ] \
    && { echo "FAIL: 20240102 should have been pruned by --keep 2" >&2; FAILURES=$((FAILURES + 1)); } || true
  [ -d "$backups/20240101T000000Z" ] \
    && { echo "FAIL: 20240101 should have been pruned by --keep 2" >&2; FAILURES=$((FAILURES + 1)); } || true
  local count
  count="$(ls -1 "$backups" | grep -E '^[0-9]{8}T[0-9]{6}Z$' | wc -l | tr -d ' ')"
  assert_eq "generation count after retention" "2" "$count"
}

# ------------------------------------------------------------------ dispatch

requested="${BACKUP_ONLY:-}"
run_all=1
[ -n "$requested" ] && run_all=0

for scenario_name in \
  success \
  verify_detects_tamper \
  backup_rejects_plaintext \
  symlink_source_rejected \
  stop_failure \
  restart_unhealthy \
  concurrent_lock \
  keep_prunes_oldest; do
  if [ "$run_all" -eq 1 ] || [ "$requested" = "$scenario_name" ]; then
    # fail-closed scenarios must exit 1; success paths must exit 0
    case "$scenario_name" in
      success|verify_detects_tamper|keep_prunes_oldest) default_expected=0 ;;
      *) default_expected=1 ;;
    esac
    expected="$default_expected"
    [ "$run_all" -eq 0 ] && expected="${BACKUP_EXPECTED:-$default_expected}"
    run_case "$scenario_name" "$expected"
  fi
done

if [ "$FAILURES" -ne 0 ]; then
  echo "backup-data-root_test: $FAILURES assertion failure(s)" >&2
  exit 1
fi
echo "backup-data-root_test: all scenarios passed"
