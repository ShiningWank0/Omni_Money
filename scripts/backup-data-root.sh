#!/usr/bin/env bash
#
# Operator whole-data-root backup for the multi-user server (Issue #152).
#
# Takes a cold, consistency-safe archive of the attested data root while the
# service is stopped, verifies it, and brings the service back up:
#
#   1. resolve the pinned Compose service container and its /app/data source
#   2. validate the data tree contract (no symlinks, no writable group/other,
#      10001:10001 ownership, regular files/directories only, one filesystem)
#   3. stop the container (docker stop --time 30), re-validate the tree
#   4. tar --one-file-system --numeric-owner into a staged exclusive file,
#      validate every member name/type, fsync, atomic rename, sha256 sidecar
#   5. reject any archived database carrying a plaintext SQLite header
#   6. write a manifest (archive hash, source identity, vault inventory)
#   7. restart the container and wait for health (--no-start to leave stopped)
#
# The control DB key file, the data-at-rest attestation, and every user's
# recovery code live OUTSIDE the data root by contract and are never captured
# here; docs/disaster-recovery.md explains how they must be stored separately.
#
# Usage:
#   bash scripts/backup-data-root.sh [--dest DIR] [--keep N] [--no-start]
#        [--project-dir DIR] [--project-name NAME]
#   bash scripts/backup-data-root.sh --verify <backup-generation-dir>
#
# Exit codes: 0 success, 1 failure, 2 usage error.

set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

readonly DATA_UID=10001
readonly DATA_GID=10001
readonly SERVICE_NAME="omni-money"
readonly APP_DATA_MOUNT="/app/data"
readonly CONTROL_DB_REL="control/omni_control.db"
readonly VAULTS_REL="vaults"
readonly STOP_TIMEOUT=30
readonly HEALTH_WAIT_SECONDS=90
readonly MANIFEST_VERSION=1

MODE="backup"
DEST_ROOT=""
KEEP_GENERATIONS=0
NO_START=0
PROJECT_DIR="$REPO_ROOT"
PROJECT_NAME=""
VERIFY_DIR=""

usage() {
  cat >&2 <<'USAGE'
usage:
  scripts/backup-data-root.sh [--dest DIR] [--keep N] [--no-start]
                              [--project-dir DIR] [--project-name NAME]
  scripts/backup-data-root.sh --verify BACKUP_GENERATION_DIR

options:
  --dest DIR        destination root for backup generations
                    (default: <data-dir parent>/omni-money-backups)
  --keep N          after a successful backup, prune oldest generations so
                    only the newest N remain (default: keep all)
  --no-start        leave the service stopped after the archive is durable
  --project-dir DIR Compose project directory (default: repository root)
  --project-name N  Compose project name override (-p)
  --verify DIR      verify an existing backup generation; touches nothing
USAGE
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dest) [ "$#" -ge 2 ] || { usage; exit 2; }; DEST_ROOT="$2"; shift 2 ;;
    --keep)
      [ "$#" -ge 2 ] || { usage; exit 2; }
      case "$2" in ''|*[!0-9]*) usage; echo "backup-data-root: --keep requires a non-negative integer" >&2; exit 2 ;; esac
      KEEP_GENERATIONS="$2"; shift 2
      ;;
    --no-start) NO_START=1; shift ;;
    --project-dir) [ "$#" -ge 2 ] || { usage; exit 2; }; PROJECT_DIR="$2"; shift 2 ;;
    --project-name) [ "$#" -ge 2 ] || { usage; exit 2; }; PROJECT_NAME="$2"; shift 2 ;;
    --verify) [ "$#" -ge 2 ] || { usage; exit 2; }; MODE="verify"; VERIFY_DIR="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) usage; echo "backup-data-root: unknown argument: $1" >&2; exit 2 ;;
  esac
done

fail() {
  echo "backup-data-root: $*" >&2
  exit 1
}

command -v tar >/dev/null 2>&1 || fail "tar is required"
command -v sha256sum >/dev/null 2>&1 || fail "sha256sum is required"
command -v find >/dev/null 2>&1 || fail "find is required"

# stat helpers with GNU/BSD fallbacks (same contract as scripts/safe-update.sh).
stat_field() {
  local path="$1" format_gnu="$2" format_bsd="$3" value
  if value="$(stat -c "$format_gnu" -- "$path" 2>/dev/null)"; then printf '%s' "$value"; return 0; fi
  if value="$(stat -f "$format_bsd" -- "$path" 2>/dev/null)"; then printf '%s' "$value"; return 0; fi
  return 1
}
stat_mode() { stat_field "$1" '%a' '%Lp'; }
stat_owner() { stat_field "$1" '%u' '%u'; }
stat_group() { stat_field "$1" '%g' '%g'; }
stat_device() { stat_field "$1" '%d' '%d'; }
stat_inode() { stat_field "$1" '%i' '%i'; }
stat_nlink() { stat_field "$1" '%h' '%l'; }

sha256_file() {
  sha256sum -- "$1" | sed -E 's/[[:space:]].*$//'
}

directory_size_kb() {
  local output value
  output="$(du -sk -- "$1")" || return 1
  value="${output%%[[:space:]]*}"
  case "$value" in ''|*[!0-9]*) return 1 ;; esac
  printf '%s' "$value"
}

free_space_kb() {
  local output value
  output="$(df -Pk -- "$1")" || return 1
  value="$(printf '%s\n' "$output" | awk 'NR == 2 {print $4}')"
  case "$value" in ''|*[!0-9]*) return 1 ;; esac
  printf '%s' "$value"
}

fsync_path() {
  sync -f -- "$1" 2>/dev/null || sync 2>/dev/null || return 1
}

reject_dangerous_target() {
  local path="$1"
  [ -n "$path" ] || return 1
  case "$path" in /|/tmp|/var|/usr|/etc|/boot|/dev|/proc|/sys|/run) return 1 ;; esac
  case "$path" in *..*) return 1 ;; esac
  return 0
}

validate_json_scalar() {
  local label="$1" value="$2"
  case "$value" in
    *'"'*|*'\'*|*$'\n'*|*$'\r'*|*$'\t'*)
      fail "$label contains a character that cannot be recorded safely in the manifest"
      ;;
  esac
}

# GNU tar is required for --one-file-system --numeric-owner listing semantics.
validate_tar_binary() {
  tar --version 2>/dev/null | grep -q GNU || fail "GNU tar is required"
}

# Reject unsafe member names and unsupported member types; fail closed on any
# symlink, hardlink, device, FIFO, or special name. Same rules as safe-update.
validate_tar_members() {
  local archive="$1" members="$2" member line type
  validate_tar_binary
  if ! tar -tf "$archive" > "$members"; then fail "archive table of contents could not be read"; fi
  grep -q . "$members" || fail "archive is empty"
  while IFS= read -r member; do
    case "$member" in
      ''|/*|../*|*/../*|*/..|*\\*|*$'\n'*|*$'\r'*|*$'\t'*)
        fail "archive contains an unsafe member name"
        ;;
    esac
  done < "$members"
  local details
  details="$(mktemp "${TMPDIR:-/tmp}/backup-members-details.XXXXXX")"
  if ! tar -tvf "$archive" > "$details"; then
    rm -f -- "$details"
    fail "archive details could not be read"
  fi
  while IFS= read -r line; do
    case "$line" in *\\*|*$'\n'*|*$'\r'*|*$'\t'*)
      rm -f -- "$details"
      fail "archive contains an unsafe detailed member name"
      ;;
    esac
    type="${line:0:1}"
    case "$type" in
      -|d) ;;
      *) rm -f -- "$details"; fail "archive contains unsupported member type: $type" ;;
    esac
  done < "$details"
  rm -f -- "$details"
}

validate_data_entry() {
  local path="$1" label="$2" owner group mode links
  owner="$(stat_owner "$path")" || fail "$label ownership could not be read: $path"
  group="$(stat_group "$path")" || fail "$label group could not be read: $path"
  mode="$(stat_mode "$path")" || fail "$label mode could not be read: $path"
  [ "$owner" = "$DATA_UID" ] && [ "$group" = "$DATA_GID" ] || fail "$label must be owned by ${DATA_UID}:${DATA_GID}: $path"
  case "$mode" in *[!0-7]*|'') fail "$label has an unreadable mode: $path" ;; esac
  (( (8#$mode & 18) == 0 )) || fail "$label is group/other writable: $path"
  if [ -f "$path" ]; then
    links="$(stat_nlink "$path")" || fail "$label link count could not be read: $path"
    [ "$links" = "1" ] || fail "$label contains a hard-linked regular file: $path"
  fi
}

validate_nested_mounts() {
  local root="$1" label="$2" target
  # find -xdev alone misses same-device bind mounts; on Linux cross-check the
  # kernel mount table for anything mounted inside the data root.
  if [ "$(uname -s)" != Linux ]; then return 0; fi
  command -v findmnt >/dev/null 2>&1 || fail "findmnt is required on Linux"
  while IFS= read -r target; do
    [ -n "$target" ] || continue
    [ "$target" = "$root" ] && continue
    case "$target" in
      "$root"|"$root"/*) fail "$label contains a nested mount: $target" ;;
    esac
  done < <(findmnt -rn --output TARGET 2>/dev/null || true)
}

validate_source_tree() {
  local root="$1" label="$2" entry
  local tree
  tree="$(mktemp "${TMPDIR:-/tmp}/backup-tree.XXXXXX")"
  if ! find -P "$root" -xdev -mindepth 1 -print0 > "$tree"; then
    rm -f -- "$tree"
    fail "$label tree could not be enumerated"
  fi
  while IFS= read -r -d '' entry; do
    [ -L "$entry" ] && { rm -f -- "$tree"; fail "$label contains a symbolic link: $entry"; }
    [ -d "$entry" ] || [ -f "$entry" ] || { rm -f -- "$tree"; fail "$label contains a non-directory/non-regular entry: $entry"; }
    validate_data_entry "$entry" "$label entry" || { rm -f -- "$tree"; exit 1; }
  done < "$tree"
  rm -f -- "$tree"
  validate_nested_mounts "$root" "$label"
}

create_exclusive_file() {
  local path="$1"
  [ ! -e "$path" ] && [ ! -L "$path" ] || return 1
  (set -o noclobber; : > "$path") 2>/dev/null
}

move_exclusive_file() {
  local source="$1" destination="$2"
  [ -e "$source" ] && [ ! -L "$source" ] || return 1
  [ ! -e "$destination" ] && [ ! -L "$destination" ] || return 1
  mv -n -- "$source" "$destination" || return 1
  [ ! -e "$source" ] && [ -e "$destination" ] && [ ! -L "$destination" ]
}

container_state() {
  docker inspect --format '{{.State.Status}}' "$1" 2>/dev/null
}

container_health() {
  docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$1" 2>/dev/null
}

wait_until_healthy() {
  local cid="$1" health
  for _ in $(seq 1 "$HEALTH_WAIT_SECONDS"); do
    health="$(container_health "$cid")" || return 1
    [ "$health" = "healthy" ] && return 0
    [ "$health" = "unhealthy" ] && return 1
    sleep 1
  done
  return 1
}

# A database member must not carry the plaintext SQLite header; the control
# DB, every ledger and every snapshot are SQLCipher ciphertext by contract.
assert_member_not_plaintext() {
  local archive="$1" member="$2" label="$3" first16
  # Disable pipefail inside the substitution: head exits after 16 bytes, so
  # tar would die on EPIPE for any member larger than the pipe buffer and a
  # member that cannot be read must still fail closed via the empty check.
  first16="$(set +o pipefail; tar -xOf "$archive" "$member" 2>/dev/null | head -c 16)" \
    || fail "archived $label could not be read for the encryption check"
  [ -n "$first16" ] || fail "archived $label could not be read for the encryption check"
  [ "$first16" = "SQLite format 3" ] \
    && fail "archived $label carries a plaintext SQLite header; the encryption contract is violated"
  return 0
}

prune_generations() {
  local dest="$1" keep="$2" current="$3" generation count=0
  [ -d "$dest" ] || return 0
  local -a generations=()
  while IFS= read -r generation; do
    [ -n "$generation" ] || continue
    generations+=("$generation")
  done < <(ls -1 "$dest" 2>/dev/null | grep -E '^[0-9]{8}T[0-9]{6}Z$' | sort -r || true)
  # --keep N counts every generation including the one just created; the
  # current generation itself is never a deletion candidate even if a
  # future-dated directory sorts ahead of it.
  for generation in "${generations[@]:-$}"; do
    [ "$generation" = "$" ] && continue
    count=$((count + 1))
    [ "$count" -le "$keep" ] && continue
    [ "$generation" = "$current" ] && continue
    rm -rf -- "$dest/$generation"
  done
}

# Global so both the verify-mode trap and the backup-mode cleanup can remove
# the temporary members listing even when a fail() aborts mid-verification.
VERIFY_TMP=""

verify_generation() {
  local dir="$1" archive checksum manifest recorded_hash computed_hash member line vault_id
  local members
  reject_dangerous_target "$dir" || fail "backup directory is too broad or dangerous: $dir"
  [ -d "$dir" ] || fail "backup generation directory does not exist: $dir"
  archive="$dir/data.tar"
  checksum="$dir/data.tar.sha256"
  manifest="$dir/manifest.json"
  for f in "$archive" "$checksum" "$manifest"; do
    [ -f "$f" ] && [ ! -L "$f" ] || fail "backup generation is incomplete: $f"
  done
  (cd "$dir" && sha256sum --check --strict data.tar.sha256 >/dev/null) \
    || fail "archive checksum verification failed"
  VERIFY_TMP="$(mktemp "${TMPDIR:-/tmp}/backup-verify-members.XXXXXX")"
  members="$VERIFY_TMP"
  validate_tar_members "$archive" "$members"
  grep -qx "./$CONTROL_DB_REL" "$members" || fail "archive is missing the control database member"
  # tar lists directory members with a trailing slash
  grep -qx -e "./$VAULTS_REL/" -e "./$VAULTS_REL" "$members" \
    || fail "archive is missing the vaults directory member"
  computed_hash="$(sha256_file "$archive")"
  recorded_hash="$(sed -n 's/.*"archive_sha256"[[:space:]]*:[[:space:]]*"\([0-9a-f]\{64\}\)".*/\1/p' "$manifest")"
  [ -n "$recorded_hash" ] || fail "manifest does not contain a parsable archive_sha256"
  [ "$recorded_hash" = "$computed_hash" ] || fail "manifest archive_sha256 does not match the archive"
  assert_member_not_plaintext "$archive" "./$CONTROL_DB_REL" "control database"
  while IFS= read -r member; do
    [ -n "$member" ] || continue
    assert_member_not_plaintext "$archive" "$member" "vault ledger ($member)"
  done < <(grep -E '^\./vaults/[^/]+/ledger\.db$' "$members" || true)
  while IFS= read -r member; do
    [ -n "$member" ] || continue
    assert_member_not_plaintext "$archive" "$member" "vault snapshot ($member)"
  done < <(grep -E '^\./vaults/[^/]+/snapshots/[^/]+\.db$' "$members" || true)
  while IFS= read -r vault_id; do
    [ -n "$vault_id" ] || continue
    grep -qx "./$VAULTS_REL/$vault_id/ledger.db" "$members" \
      || fail "manifest lists vault without an archived ledger: $vault_id"
  done < <(grep -oE '"vault_ids"[[:space:]]*:\[[^]]*\]' "$manifest" \
    | sed -E 's/^"vault_ids"[[:space:]]*:\[//; s/\]$//' | tr ',' '\n' \
    | sed -E 's/^[[:space:]]*//; s/[[:space:]]*$//; s/^"//; s/"$//' || true)
  rm -f -- "$members"
  VERIFY_TMP=""
  echo "backup-data-root: verification OK: $dir"
}

# ---------------------------------------------------------------- verify mode
if [ "$MODE" = "verify" ]; then
  [ -n "$VERIFY_DIR" ] || { usage; exit 2; }
  # capture the triggering status first: rm must not mask a verification failure
  trap 'status=$?; rm -f -- "$VERIFY_TMP" 2>/dev/null; exit "$status"' EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  verify_generation "$VERIFY_DIR"
  exit 0
fi

# --------------------------------------------------------------- backup mode
command -v docker >/dev/null 2>&1 || fail "docker is required"
[ -d "$PROJECT_DIR" ] || fail "Compose project directory does not exist: $PROJECT_DIR"
[ -f "$PROJECT_DIR/compose.yaml" ] || fail "compose.yaml not found in: $PROJECT_DIR"

compose_ps() {
  if [ -n "$PROJECT_NAME" ]; then
    docker compose --project-directory "$PROJECT_DIR" -p "$PROJECT_NAME" -f "$PROJECT_DIR/compose.yaml" ps --all -q "$SERVICE_NAME"
  else
    docker compose --project-directory "$PROJECT_DIR" -f "$PROJECT_DIR/compose.yaml" ps --all -q "$SERVICE_NAME"
  fi
}

container_id="$(compose_ps | while IFS= read -r id; do
  [ -n "$id" ] || continue
  printf '%s\n' "$id"
done)"
id_count="$(printf '%s\n' "$container_id" | awk 'NF {count++} END {print count+0}')"
case "$id_count" in
  1) container_id="$(printf '%s\n' "$container_id" | head -n 1)" ;;
  0) fail "service container could not be resolved (exactly one $SERVICE_NAME container is required)" ;;
  *) fail "multiple $SERVICE_NAME containers resolved; refusing to guess" ;;
esac

# Mount sources are parsed from a single-line "=" template instead of JSON so
# the script has no jq dependency. sub() keeps everything after the first "="
# so a source path containing "=" is preserved rather than silently truncated.
mounts_output="$(docker inspect --format '{{range .Mounts}}{{.Destination}}={{.Source}}{{"\n"}}{{end}}' "$container_id")" \
  || fail "container mounts could not be inspected"
data_dir="$(printf '%s\n' "$mounts_output" | awk -F= -v want="$APP_DATA_MOUNT" '$1 == want {sub(/^[^=]*=/, ""); print}' | head -n 1)"
mount_count="$(printf '%s\n' "$mounts_output" | awk -F= -v want="$APP_DATA_MOUNT" '$1 == want {found++} END {print found+0}')"
[ -n "$data_dir" ] && [ "$mount_count" = "1" ] \
  || fail "container must have exactly one $APP_DATA_MOUNT mount"
reject_dangerous_target "$data_dir" || fail "resolved data root is too broad or dangerous: $data_dir"

[ -d "$data_dir" ] || fail "data root does not exist: $data_dir"
[ "$(stat_mode "$data_dir")" = "700" ] || fail "data root must have mode 0700: $data_dir"
[ "$(stat_owner "$data_dir")" = "$DATA_UID" ] && [ "$(stat_group "$data_dir")" = "$DATA_GID" ] \
  || fail "data root must be owned by ${DATA_UID}:${DATA_GID}: $data_dir"
[ -f "$data_dir/$CONTROL_DB_REL" ] || fail "control database is missing: $data_dir/$CONTROL_DB_REL"
[ -d "$data_dir/$VAULTS_REL" ] || fail "vaults directory is missing: $data_dir/$VAULTS_REL"
validate_source_tree "$data_dir" "data root"

data_device="$(stat_device "$data_dir")"
data_inode="$(stat_inode "$data_dir")"
data_nlink="$(stat_nlink "$data_dir")"

if [ -z "$DEST_ROOT" ]; then
  DEST_ROOT="$(dirname -- "$data_dir")/omni-money-backups"
fi
reject_dangerous_target "$DEST_ROOT" || fail "destination root is too broad or dangerous: $DEST_ROOT"
case "$DEST_ROOT" in "$data_dir"|"$data_dir"/*) fail "destination must live outside the live data root" ;; esac
mkdir -p -- "$DEST_ROOT"
[ "$(stat_mode "$DEST_ROOT")" = "700" ] || chmod 700 "$DEST_ROOT"

# Capacity preflight on the destination filesystem (archive ≈ logical size).
data_kb="$(directory_size_kb "$data_dir")" || fail "data size could not be measured"
available_kb="$(free_space_kb "$DEST_ROOT")" || fail "destination free space could not be measured"
required_kb=$((data_kb * 2 + 65536))
(( available_kb >= required_kb )) || fail "insufficient free space at $DEST_ROOT: need at least ${required_kb}KiB, have ${available_kb}KiB"

# Exclusive lock so two operators (or cron and a human) cannot interleave.
# Arm the EXIT/INT/TERM traps BEFORE the lock exists so an abort can never
# leak the lock file and block every future backup.
lock_file="$DEST_ROOT/.backup.lock"
lock_held=0
container_stopped=0
tmp_files=()
cleanup() {
  local status=$?
  set +e
  if [ "${container_stopped:-0}" -eq 1 ] && [ -n "${container_id:-}" ]; then
    echo "backup-data-root: restarting interrupted service container" >&2
    docker start "$container_id" >/dev/null 2>&1 \
      || echo "backup-data-root: WARNING: automatic restart failed; run: docker start $container_id" >&2
  fi
  for f in "${tmp_files[@]:-}"; do
    [ -n "$f" ] && rm -f -- "$f"
  done
  [ -n "$VERIFY_TMP" ] && rm -f -- "$VERIFY_TMP"
  [ "${lock_held:-0}" -eq 1 ] && rm -f -- "$lock_file"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
create_exclusive_file "$lock_file" || fail "another backup appears to be running ($lock_file exists)"
printf '%s\n' "$$" > "$lock_file"
lock_held=1

timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
backup_dir="$DEST_ROOT/$timestamp"
mkdir -m 0700 -- "$backup_dir"
[ "$(stat_mode "$backup_dir")" = "700" ] || fail "backup directory could not be created with mode 0700"
tmp_files+=("$backup_dir/.data.tar.tmp" "$backup_dir/.data.tar.sha256.tmp" "$backup_dir/.manifest.json.tmp")

echo "backup-data-root: stopping service container ($container_id)"
docker stop --time "$STOP_TIMEOUT" "$container_id" >/dev/null || fail "service container did not stop"
container_stopped=1
state_after_stop="$(container_state "$container_id")" || fail "container state could not be inspected after stop"
case "$state_after_stop" in
  created|exited|dead) ;;
  *) fail "container is not stopped (state: $state_after_stop)" ;;
esac
[ "$(stat_device "$data_dir")" = "$data_device" ] \
  && [ "$(stat_inode "$data_dir")" = "$data_inode" ] \
  && [ "$(stat_nlink "$data_dir")" = "$data_nlink" ] \
  || fail "data root identity changed while the service was being stopped"
validate_source_tree "$data_dir" "stopped data root"

archive_tmp="$backup_dir/.data.tar.tmp"
create_exclusive_file "$archive_tmp" || fail "archive staging file could not be created"
echo "backup-data-root: creating archive"
tar --one-file-system --numeric-owner -cpf "$archive_tmp" -C "$data_dir" . \
  || fail "archive creation failed"
members_file="$(mktemp "${TMPDIR:-/tmp}/backup-members.XXXXXX")"
tmp_files+=("$members_file")
validate_tar_members "$archive_tmp" "$members_file"
fsync_path "$archive_tmp" || fail "archive staging fsync failed"
archive_path="$backup_dir/data.tar"
move_exclusive_file "$archive_tmp" "$archive_path" || fail "archive could not be published atomically"
chmod 600 "$archive_path"

checksum_tmp="$backup_dir/.data.tar.sha256.tmp"
printf '%s  data.tar\n' "$(sha256_file "$archive_path")" > "$checksum_tmp"
fsync_path "$checksum_tmp" || fail "checksum sidecar fsync failed"
checksum_path="$backup_dir/data.tar.sha256"
move_exclusive_file "$checksum_tmp" "$checksum_path" || fail "checksum sidecar could not be published"
chmod 600 "$checksum_path"
( cd "$backup_dir" && sha256sum --check --strict data.tar.sha256 >/dev/null ) \
  || fail "archive checksum self-verification failed"

validate_tar_members "$archive_path" "$members_file"
assert_member_not_plaintext "$archive_path" "./$CONTROL_DB_REL" "control database"
ledger_count=0
while IFS= read -r member; do
  [ -n "$member" ] || continue
  assert_member_not_plaintext "$archive_path" "$member" "vault ledger ($member)"
  ledger_count=$((ledger_count + 1))
done < <(grep -E '^\./vaults/[^/]+/ledger\.db$' "$members_file" || true)
(( ledger_count >= 1 )) || fail "no vault ledger was captured in the archive"
while IFS= read -r member; do
  [ -n "$member" ] || continue
  assert_member_not_plaintext "$archive_path" "$member" "vault snapshot ($member)"
done < <(grep -E '^\./vaults/[^/]+/snapshots/[^/]+\.db$' "$members_file" || true)

vault_ids=""
vault_count=0
while IFS= read -r vault_dir; do
  [ -n "$vault_dir" ] || continue
  vault_id="${vault_dir##*/}"
  case "$vault_id" in
    ''|*[!A-Za-z0-9._-]*)
      fail "vault directory name is outside the supported charset; refusing to record it in the manifest: $vault_id"
      ;;
  esac
  grep -qx "./$VAULTS_REL/$vault_id/ledger.db" "$members_file" \
    || fail "vault directory has no archived ledger: $vault_id"
  if [ -n "$vault_ids" ]; then vault_ids="$vault_ids, "; fi
  vault_ids="$vault_ids\"$vault_id\""
  vault_count=$((vault_count + 1))
done < <(find "$data_dir/$VAULTS_REL" -mindepth 1 -maxdepth 1 -type d | sort)
(( vault_count == ledger_count )) || fail "vault inventory ($vault_count) does not match archived ledgers ($ledger_count)"
validate_json_scalar "data root path" "$data_dir"
validate_json_scalar "destination path" "$DEST_ROOT"
archive_hash="$(sha256_file "$archive_path")"

manifest_tmp="$backup_dir/.manifest.json.tmp"
cat > "$manifest_tmp" <<EOF
{
  "version": $MANIFEST_VERSION,
  "tool": "backup-data-root.sh",
  "created_at": "$timestamp",
  "archive": "data.tar",
  "archive_sha256": "$archive_hash",
  "source": {
    "path": "$data_dir",
    "device": "$data_device",
    "inode": "$data_inode",
    "nlink": "$data_nlink"
  },
  "control_db": "$CONTROL_DB_REL",
  "vaults_dir": "$VAULTS_REL",
  "vault_count": $vault_count,
  "vault_ids": [$vault_ids],
  "external_materials": [
    "control DB key file (never stored inside the data root)",
    "data-at-rest attestation file (never stored inside the data root)",
    "each user's recovery code (never stored server-side)"
  ]
}
EOF
chmod 600 "$manifest_tmp"
fsync_path "$manifest_tmp" || fail "manifest fsync failed"
manifest_path="$backup_dir/manifest.json"
move_exclusive_file "$manifest_tmp" "$manifest_path" || fail "manifest could not be published"
fsync_path "$backup_dir" || true
verify_generation "$backup_dir" >/dev/null || fail "published backup failed self-verification"

if [ "$KEEP_GENERATIONS" -ge 1 ]; then
  prune_generations "$DEST_ROOT" "$KEEP_GENERATIONS" "$timestamp"
fi

if [ "$NO_START" -eq 1 ]; then
  echo "backup-data-root: --no-start given; service left stopped (docker start $container_id)"
  container_stopped=0
else
  echo "backup-data-root: starting service container"
  docker start "$container_id" >/dev/null || fail "service container could not be started; run: docker start $container_id"
  container_stopped=0
  if wait_until_healthy "$container_id"; then
    :
  else
    fail "archive is durable but the service did not return healthy within ${HEALTH_WAIT_SECONDS}s; inspect: docker logs $container_id"
  fi
fi

echo "backup-data-root: OK generation=$backup_dir vaults=$vault_count sha256=$archive_hash"
