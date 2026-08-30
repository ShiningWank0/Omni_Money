#!/usr/bin/env bash
set -Eeuo pipefail

# Deploy one pinned Omni Money image with an offline checkpoint. The previous
# image and data are restored only if the candidate fails before it becomes
# healthy and is reconnected to the ingress network.

service_name="omni-money"
target_image="${1:-}"
project_dir="$(pwd -P)"
current_uid="$(id -u)"
env_file_input="${OMNI_UPDATE_ENV_FILE:-.env}"
env_file=""
health_timeout=""
rollback_armed=0
rollback_running=0
update_verified=0
checkpoint_ready=0
candidate_id=""
checkpoint_dir=""
archive_path=""
rollback_image=""
data_dir=""
network_name=""
network_ip=""
checkpoint_root_device=""
checkpoint_root_inode=""
checkpoint_dir_device=""
checkpoint_dir_inode=""
data_dir_device=""
data_dir_inode=""

# This script intentionally does not source a Compose env file.  Compose env
# files are data, not shell programs: sourcing one would let a modified file
# execute commands with the privileges used for the update.  The small parser
# below is only used for the few update settings that must agree with Compose.
env_file_lookup() {
  local key="$1"
  awk -v want="$key" '
    BEGIN { single_quote = sprintf("%c", 39) }
    function trim(value) {
      sub(/^[[:space:]]+/, "", value)
      sub(/[[:space:]]+$/, "", value)
      return value
    }
    {
      line = $0
      sub(/^[[:space:]]+/, "", line)
      if (line == "" || substr(line, 1, 1) == "#") next
      if (line ~ /^export[[:space:]]+/) sub(/^export[[:space:]]+/, "", line)
      separator = index(line, "=")
      if (separator < 2 || substr(line, 1, separator - 1) != want) next
      value = substr(line, separator + 1)
      value = trim(value)
      if (length(value) >= 2 && substr(value, 1, 1) == "\"" && substr(value, length(value), 1) == "\"") {
        value = substr(value, 2, length(value) - 2)
      } else if (length(value) >= 2 && substr(value, 1, 1) == single_quote && substr(value, length(value), 1) == single_quote) {
        value = substr(value, 2, length(value) - 2)
      }
      print value
      found = 1
    }
    END { if (!found) exit 1 }
  ' "$env_file"
}

compose_env_value() {
  local key="$1"
  local fallback="$2"
  local value=""
  case "$key" in
    OMNI_DATA_DIR|OMNI_AT_REST_ATTESTATION_FILE|OMNI_UPDATE_CHECKPOINT_DIR|OMNI_UPDATE_HEALTH_TIMEOUT_SECONDS) ;;
    *) fail "internal error: unsupported Compose env key: $key"; return 1 ;;
  esac
  if [[ "${!key+x}" == x ]]; then
    value="${!key}"
  elif value="$(env_file_lookup "$key" 2>/dev/null)"; then
    :
  else
    value="$fallback"
  fi
  case "$value" in
    *$'\n'*|*$'\r'*) fail "$key must not contain a newline"; return 1 ;;
  esac
  printf '%s' "$value"
}

reject_path_syntax() {
  local path="$1"
  case "$path" in
    ''|*/../*|*/..|*/./*|*/.) return 1 ;;
  esac
  case "$path" in
    *$'\n'*|*$'\r'*) return 1 ;;
  esac
  return 0
}

reject_dangerous_path() {
  local path="$1"
  case "$path" in
    /|/home|/Users|/root|/tmp|/private)
      return 1
      ;;
  esac
  # A root with no named parent is too broad for an automated data move.
  case "${path#/}" in
    */*) return 0 ;;
    *) return 1 ;;
  esac
}

path_owner_and_mode_ok() {
  local path="$1"
  local owner mode
  owner="$(stat_owner "$path")" || return 1
  mode="$(stat_mode "$path")" || return 1
  [ "$owner" = "0" ] || [ "$owner" = "$current_uid" ] || return 1
  case "$mode" in *[!0-7]*|'') return 1 ;; esac
  # Group/other write permission makes a path replaceable by another user.
  (( (8#$mode & 18) == 0 )) || return 1
}

stat_owner() {
  local path="$1"
  local value
  if value="$(stat -c '%u' -- "$path" 2>/dev/null)"; then
    printf '%s' "$value"
  else
    stat -f '%u' "$path"
  fi
}

stat_mode() {
  local path="$1"
  local value
  if value="$(stat -c '%a' -- "$path" 2>/dev/null)"; then
    printf '%s' "$value"
  else
    stat -f '%Lp' "$path"
  fi
}

stat_device() {
  local path="$1"
  local value
  if value="$(stat -c '%d' -- "$path" 2>/dev/null)"; then
    printf '%s' "$value"
  else
    stat -f '%d' "$path"
  fi
}

stat_inode() {
  local path="$1"
  local value
  if value="$(stat -c '%i' -- "$path" 2>/dev/null)"; then
    printf '%s' "$value"
  else
    stat -f '%i' "$path"
  fi
}

validate_directory_chain() {
  local path="$1"
  local label="$2"
  local current="/"
  local component
  local -a components=()
  if [ "${path#/}" = "$path" ]; then
    fail "$label must be an absolute path: $path"
    return 1
  fi
  if ! reject_path_syntax "$path"; then
    fail "$label contains an unsafe path component: $path"
    return 1
  fi
  IFS='/' read -r -a components <<< "${path#/}"
  for component in "${components[@]}"; do
    if [ -z "$component" ]; then
      fail "$label contains an empty path component: $path"
      return 1
    fi
    current="${current%/}/$component"
    if [ -L "$current" ]; then
      fail "$label must not contain symbolic links: $path"
      return 1
    fi
    if [ ! -e "$current" ]; then
      fail "$label component does not exist: $current"
      return 1
    fi
    if [ ! -d "$current" ]; then
      fail "$label component must be a normal directory: $current"
      return 1
    fi
    if ! path_owner_and_mode_ok "$current"; then
      fail "$label component has an untrusted owner or mode: $current"
      return 1
    fi
  done
}

validate_existing_file() {
  local path="$1"
  local label="$2"
  local parent
  if ! reject_dangerous_path "$path"; then
    fail "$label is too broad or dangerous: $path"
    return 1
  fi
  parent="$(dirname -- "$path")"
  validate_directory_chain "$parent" "$label parent" || return 1
  if [ -L "$path" ]; then
    fail "$label must not be a symbolic link: $path"
    return 1
  fi
  if [ ! -f "$path" ]; then
    fail "$label must be an existing regular file: $path"
    return 1
  fi
  if ! path_owner_and_mode_ok "$path"; then
    fail "$label has an untrusted owner or mode: $path"
    return 1
  fi
}

validate_existing_directory() {
  local path="$1"
  local label="$2"
  if ! reject_dangerous_path "$path"; then
    fail "$label is too broad or dangerous: $path"
    return 1
  fi
  validate_directory_chain "$path" "$label" || return 1
}

make_checkpoint_root() {
  local path="$1"
  local parent
  if ! reject_dangerous_path "$path"; then
    fail "checkpoint root is too broad or dangerous: $path"
    return 1
  fi
  parent="$(dirname -- "$path")"
  validate_directory_chain "$parent" "checkpoint root parent" || return 1
  if [ -e "$path" ] || [ -L "$path" ]; then
    if [ -L "$path" ]; then
      fail "checkpoint root must not be a symbolic link: $path"
      return 1
    fi
    if [ ! -d "$path" ]; then
      fail "checkpoint root must be a normal directory: $path"
      return 1
    fi
    if ! path_owner_and_mode_ok "$path"; then
      fail "checkpoint root has an untrusted owner or mode: $path"
      return 1
    fi
    chmod 0700 "$path" || { fail "could not secure checkpoint root: $path"; return 1; }
  else
    mkdir -m 0700 -- "$path" || { fail "could not create checkpoint root: $path"; return 1; }
  fi
  validate_existing_directory "$path" "checkpoint root" || return 1
  if [ "$(stat_mode "$path")" != "700" ]; then
    fail "checkpoint root must have mode 0700: $path"
    return 1
  fi
}

resolve_config_path() {
  local value="$1"
  # Compose commonly spells project-relative paths as ./name. Normalize that
  # one harmless prefix while rejecting traversal and interior dot segments.
  case "$value" in
    ./*) value="${value#./}" ;;
  esac
  if ! reject_path_syntax "$value"; then
    fail "configured path contains an unsafe component: $value"
    return 1
  fi
  case "$value" in
    /*) printf '%s' "$value" ;;
    *) printf '%s/%s' "$project_dir" "$value" ;;
  esac
}

path_contains() {
  local parent="${1%/}"
  local child="${2%/}"
  [ "$parent" != "/" ] || return 0
  case "$child/" in "$parent/"*) return 0 ;; esac
  return 1
}

validate_checkpoint_artifact() {
  local path="$1"
  local label="$2"
  validate_existing_file "$path" "$label"
}

validate_pinned_directory() {
  local path="$1"
  local label="$2"
  local expected_device="$3"
  local expected_inode="$4"
  validate_existing_directory "$path" "$label" || return 1
  if [ "$(stat_device "$path")" != "$expected_device" ]; then
    fail "$label changed filesystem identity"
    return 1
  fi
  if [ "$(stat_inode "$path")" != "$expected_inode" ]; then
    fail "$label was replaced unexpectedly"
    return 1
  fi
}

fail() {
  printf 'safe-update: %s\n' "$*" >&2
  return 1
}

compose() {
  # Always pass the exact file that was validated above.  Without this flag
  # Compose silently falls back to the project .env, which can result in the
  # candidate and rollback using a different data root or image configuration.
  docker compose --env-file "$env_file" "$@"
}

wait_for_health() {
  local container_id="$1"
  local deadline=$((SECONDS + health_timeout))
  local status
  while (( SECONDS < deadline )); do
    status="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}missing{{end}}' "$container_id" 2>/dev/null || true)"
    case "$status" in
      healthy) return 0 ;;
      unhealthy|missing) return 1 ;;
    esac
    if [ "$(docker inspect --format '{{.State.Running}}' "$container_id" 2>/dev/null || true)" != "true" ]; then
      return 1
    fi
    sleep 2
  done
  return 1
}

create_disconnected_container() {
  local image="$1"
  OMNI_IMAGE="$image" compose up --no-start --no-build --force-recreate "$service_name" >/dev/null
  candidate_id="$(compose ps -q "$service_name")"
  if [ -z "$candidate_id" ]; then
    fail "candidate container was not created"
    return 1
  fi
  docker network disconnect "$network_name" "$candidate_id" >/dev/null
}

start_and_validate() {
  docker start "$candidate_id" >/dev/null
  wait_for_health "$candidate_id"
}

restore_checkpoint() {
  local failed_data="$checkpoint_dir/failed-candidate-data"
  local incomplete_restore="$checkpoint_dir/incomplete-restore"
  [ "$checkpoint_ready" -eq 1 ] || return 0
  validate_pinned_directory "$checkpoint_root" "checkpoint root" "$checkpoint_root_device" "$checkpoint_root_inode" || return 1
  validate_pinned_directory "$checkpoint_dir" "checkpoint directory" "$checkpoint_dir_device" "$checkpoint_dir_inode" || return 1
  validate_checkpoint_artifact "$archive_path" "checkpoint archive" || return 1
  validate_checkpoint_artifact "$archive_path.sha256" "checkpoint checksum" || return 1
  validate_checkpoint_artifact "$checkpoint_dir/environment.before" "checkpoint environment" || return 1
  validate_pinned_directory "$data_dir" "live data directory" "$data_dir_device" "$data_dir_inode" || return 1
  [ ! -e "$failed_data" ] && [ ! -L "$failed_data" ] || return 1
  [ ! -e "$incomplete_restore" ] && [ ! -L "$incomplete_restore" ] || return 1
  sha256sum --check "$archive_path.sha256" >/dev/null || return 1
  mv -- "$data_dir" "$failed_data" || return 1
  mkdir -m 0700 -- "$data_dir" || {
    mv -- "$failed_data" "$data_dir" || true
    return 1
  }
  validate_existing_directory "$data_dir" "restore data directory" || {
    mv -- "$data_dir" "$incomplete_restore" || true
    mv -- "$failed_data" "$data_dir" || true
    return 1
  }
  if ! tar --numeric-owner -xpf "$archive_path" -C "$data_dir"; then
    mv -- "$data_dir" "$incomplete_restore" || true
    mv -- "$failed_data" "$data_dir" || true
    return 1
  fi
  validate_existing_directory "$data_dir" "restored data directory" || return 1
  return 0
}

rollback_update() {
  local original_status="$1"
  trap - EXIT INT TERM
  rollback_running=1
  set +e
  printf 'safe-update: candidate failed; restoring the pre-update checkpoint and previous image\n' >&2
  if [ -n "$candidate_id" ]; then
    docker stop --time 30 "$candidate_id" >/dev/null 2>&1
  fi
  if ! restore_checkpoint; then
    printf 'safe-update: automatic data restore failed; service remains stopped. Recovery artifacts: %s\n' "$checkpoint_dir" >&2
    exit "$original_status"
  fi
  if validate_checkpoint_artifact "$checkpoint_dir/environment.before" "checkpoint environment"; then
    validate_existing_file "$env_file" "update env file" || {
      printf 'safe-update: pre-update env file is no longer safe; service remains stopped\n' >&2
      exit "$original_status"
    }
    cp -p -- "$checkpoint_dir/environment.before" "$env_file" || {
      printf 'safe-update: could not restore the pre-update env file; service remains stopped\n' >&2
      exit "$original_status"
    }
    validate_existing_file "$env_file" "restored update env file" || {
      printf 'safe-update: restored env file failed safety validation; service remains stopped\n' >&2
      exit "$original_status"
    }
  else
    printf 'safe-update: checkpoint environment is missing or unsafe; service remains stopped\n' >&2
    exit "$original_status"
  fi
  candidate_id=""
  if ! create_disconnected_container "$rollback_image"; then
    printf 'safe-update: previous container could not be recreated. Checkpoint: %s\n' "$checkpoint_dir" >&2
    exit "$original_status"
  fi
  if ! start_and_validate; then
    printf 'safe-update: previous image did not become healthy after data restore. Service remains isolated. Checkpoint: %s\n' "$checkpoint_dir" >&2
    exit "$original_status"
  fi
  if ! docker network connect --ip "$network_ip" "$network_name" "$candidate_id" >/dev/null; then
    printf 'safe-update: previous image is healthy but could not be reconnected to ingress\n' >&2
    exit "$original_status"
  fi
  printf 'safe-update: rollback completed because the candidate update failed. Checkpoint: %s\n' "$checkpoint_dir" >&2
  exit "$original_status"
}

on_exit() {
  local status="$?"
  if [ "$status" -ne 0 ] && [ "$rollback_armed" -eq 1 ] && [ "$update_verified" -eq 0 ] && [ "$rollback_running" -eq 0 ]; then
    rollback_update "$status"
  fi
  exit "$status"
}

safe_update_main() {
set -Eeuo pipefail
trap on_exit EXIT
trap 'exit 130' INT TERM

env_file="$(resolve_config_path "$env_file_input")"
validate_existing_file "$env_file" "update env file"
health_timeout="$(compose_env_value OMNI_UPDATE_HEALTH_TIMEOUT_SECONDS 180)"

[ -n "$target_image" ] || fail "usage: scripts/safe-update.sh <pinned-image:version-or-digest>"
case "$target_image" in
  *[!A-Za-z0-9._/@:+-]*) fail "image reference contains unsupported characters" ;;
esac
case "$target_image" in
  *:latest|latest) fail "the mutable latest tag is not allowed" ;;
esac
case "$target_image" in
  *@sha256:*|*:* ) ;;
  *) fail "use an explicit version tag or sha256 digest" ;;
esac
case "$health_timeout" in
  ''|*[!0-9]*) fail "OMNI_UPDATE_HEALTH_TIMEOUT_SECONDS must be an integer" ;;
esac
if (( health_timeout < 30 || health_timeout > 600 )); then
  fail "health timeout must be between 30 and 600 seconds"
fi

current_id="$(compose ps -q "$service_name")"
[ -n "$current_id" ] || fail "the current Omni Money container is not running"
[ "$(docker inspect --format '{{.State.Health.Status}}' "$current_id" 2>/dev/null || true)" = "healthy" ] || fail "the current container must be healthy before an update"

published_ports="$(docker inspect --format '{{json .HostConfig.PortBindings}}' "$current_id")"
if [ "$published_ports" != "{}" ] && [ "$published_ports" != "null" ]; then
  fail "published host ports prevent isolated candidate validation; use the Pangolin base deployment"
fi

mapfile -t network_names < <(docker inspect --format '{{range $name, $settings := .NetworkSettings.Networks}}{{$name}}{{"\n"}}{{end}}' "$current_id" | sed '/^$/d')
[ "${#network_names[@]}" -eq 1 ] || fail "exactly one ingress network is required for isolated validation"
network_name="${network_names[0]}"
network_ip="$(docker inspect --format "{{with index .NetworkSettings.Networks \"$network_name\"}}{{.IPAddress}}{{end}}" "$current_id")"
[ -n "$network_ip" ] || fail "could not determine the current fixed service IP"

mapfile -t data_mounts < <(docker inspect --format '{{range .Mounts}}{{if eq .Destination "/app/data"}}{{.Source}}{{"\n"}}{{end}}{{end}}' "$current_id" | sed '/^$/d')
[ "${#data_mounts[@]}" -eq 1 ] || fail "exactly one /app/data bind mount is required"
data_dir="${data_mounts[0]}"
[ -n "$data_dir" ] && [ -d "$data_dir" ] && [ ! -L "$data_dir" ] || fail "the /app/data bind mount could not be resolved safely"
data_dir="$(cd -- "$data_dir" && pwd -P)"
validate_existing_directory "$data_dir" "live data directory"
[ "$data_dir" != "$project_dir" ] || fail "the Compose project root cannot be used as the data directory"
[ "$(resolve_config_path "$(compose_env_value OMNI_DATA_DIR ./data)")" = "$data_dir" ] || fail "Compose OMNI_DATA_DIR does not match the running /app/data mount"
data_dir_device="$(stat_device "$data_dir")"
data_dir_inode="$(stat_inode "$data_dir")"

attestation_source="$(compose_env_value OMNI_AT_REST_ATTESTATION_FILE "")"
[ -n "$attestation_source" ] || fail "OMNI_AT_REST_ATTESTATION_FILE is required for safe updates"
attestation_path="$(resolve_config_path "$attestation_source")"
validate_existing_file "$attestation_path" "data-at-rest attestation"
command -v jq >/dev/null 2>&1 || fail "jq is required to validate the data-at-rest attestation"
attested_root="$(jq -er 'if (.data_root | type) == "string" then .data_root else error end' "$attestation_path" 2>/dev/null)" || fail "could not read data root from the data-at-rest attestation"
[ "$attested_root" = "/app/data" ] || fail "attestation data_root must bind the Compose /app/data root"

default_checkpoint_root="$(dirname -- "$data_dir")/omni-money-update-checkpoints"
checkpoint_config="$(compose_env_value OMNI_UPDATE_CHECKPOINT_DIR "")"
if [ -n "$checkpoint_config" ]; then
  configured_checkpoint_root="$(resolve_config_path "$checkpoint_config")"
  configured_checkpoint_root="$(printf '%s' "$configured_checkpoint_root" | sed 's:/*$::')"
  [ "$configured_checkpoint_root" = "$default_checkpoint_root" ] || fail "OMNI_UPDATE_CHECKPOINT_DIR must match the fixed checkpoint root: $default_checkpoint_root"
fi
checkpoint_root="$default_checkpoint_root"
# The checkpoint location is deliberately an allowlist of one fixed path
# derived from the verified data mount. An env file cannot redirect backups to
# an attacker-controlled or differently protected filesystem.
checkpoint_root_allowlist="$default_checkpoint_root"
path_contains "$data_dir" "$checkpoint_root" && fail "checkpoint directory must not be inside the live data directory"
path_contains "$checkpoint_root" "$data_dir" && fail "checkpoint root must not contain the live data directory"
make_checkpoint_root "$checkpoint_root"
checkpoint_root="$(cd -- "$checkpoint_root" && pwd -P)"
[ "$checkpoint_root" = "$checkpoint_root_allowlist" ] || fail "checkpoint root changed while resolving its trusted path"
checkpoint_root_device="$(stat_device "$checkpoint_root")"
checkpoint_root_inode="$(stat_inode "$checkpoint_root")"
[ "$checkpoint_root_device" = "$data_dir_device" ] || fail "checkpoint root must stay on the attested data filesystem"
validate_pinned_directory "$checkpoint_root" "checkpoint root" "$checkpoint_root_device" "$checkpoint_root_inode"

timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
checkpoint_dir="$checkpoint_root/$timestamp"
mkdir -m 0700 -- "$checkpoint_dir"
validate_existing_directory "$checkpoint_dir" "checkpoint directory"
[ "$(stat_device "$checkpoint_dir")" = "$checkpoint_root_device" ] || fail "checkpoint directory is on a different filesystem"
[ "$(stat_mode "$checkpoint_dir")" = "700" ] || fail "checkpoint directory must have mode 0700"
checkpoint_dir_device="$(stat_device "$checkpoint_dir")"
checkpoint_dir_inode="$(stat_inode "$checkpoint_dir")"
archive_path="$checkpoint_dir/data.tar"

data_kb="$(du -sk -- "$data_dir" | awk '{print $1}')"
available_kb="$(df -Pk -- "$checkpoint_root" | awk 'NR == 2 {print $4}')"
required_kb=$((data_kb * 2 + 102400))
if (( available_kb < required_kb )); then
  fail "insufficient free space: rollback-safe update needs at least ${required_kb} KiB free"
fi

current_image="$(docker inspect --format '{{.Config.Image}}' "$current_id")"
current_image_id="$(docker inspect --format '{{.Image}}' "$current_id")"
rollback_image="omni-money:rollback-$timestamp"
docker pull "$target_image" >/dev/null
docker image inspect "$target_image" >/dev/null
docker tag "$current_image_id" "$rollback_image"

cp -p -- "$env_file" "$checkpoint_dir/environment.before"
validate_pinned_directory "$checkpoint_root" "checkpoint root" "$checkpoint_root_device" "$checkpoint_root_inode"
validate_pinned_directory "$checkpoint_dir" "checkpoint directory" "$checkpoint_dir_device" "$checkpoint_dir_inode"
validate_checkpoint_artifact "$checkpoint_dir/environment.before" "checkpoint environment"
{
  printf 'created_at=%s\n' "$timestamp"
  printf 'data_directory=%s\n' "$data_dir"
  printf 'previous_image=%s\n' "$current_image"
  printf 'previous_image_id=%s\n' "$current_image_id"
  printf 'rollback_image=%s\n' "$rollback_image"
  printf 'candidate_image=%s\n' "$target_image"
} > "$checkpoint_dir/manifest"
chmod 0600 "$checkpoint_dir/manifest"
validate_checkpoint_artifact "$checkpoint_dir/manifest" "checkpoint manifest"

compose stop --timeout 30 "$service_name" >/dev/null
rollback_armed=1
validate_pinned_directory "$data_dir" "live data directory" "$data_dir_device" "$data_dir_inode"
validate_pinned_directory "$checkpoint_root" "checkpoint root" "$checkpoint_root_device" "$checkpoint_root_inode"
tar --numeric-owner -cpf "$archive_path" -C "$data_dir" .
sha256sum "$archive_path" > "$archive_path.sha256"
sha256sum --check "$archive_path.sha256" >/dev/null
sync "$archive_path" "$archive_path.sha256" 2>/dev/null || sync
validate_checkpoint_artifact "$archive_path" "checkpoint archive"
validate_checkpoint_artifact "$archive_path.sha256" "checkpoint checksum"
checkpoint_ready=1

create_disconnected_container "$target_image"
start_and_validate

# Persist the image only after the isolated candidate is healthy. The env
# file backup remains inside the checkpoint for manual disaster recovery.
env_dir="$(dirname -- "$env_file")"
validate_pinned_directory "$checkpoint_root" "checkpoint root" "$checkpoint_root_device" "$checkpoint_root_inode"
validate_pinned_directory "$checkpoint_dir" "checkpoint directory" "$checkpoint_dir_device" "$checkpoint_dir_inode"
env_tmp="$(mktemp "$env_dir/.omni-money-env.XXXXXX")"
awk -v image="$target_image" '
  BEGIN { replaced = 0 }
  /^OMNI_IMAGE=/ { if (!replaced) { print "OMNI_IMAGE=" image; replaced = 1 }; next }
  { print }
  END { if (!replaced) print "OMNI_IMAGE=" image }
' "$env_file" > "$env_tmp"
chmod "$(stat_mode "$env_file")" "$env_tmp"
mv -- "$env_tmp" "$env_file"
validate_existing_file "$env_file" "updated update env file"

docker network connect --ip "$network_ip" "$network_name" "$candidate_id" >/dev/null
[ "$(docker inspect --format '{{.State.Health.Status}}' "$candidate_id")" = "healthy" ] || fail "candidate lost health while reconnecting ingress"

update_verified=1
rollback_armed=0
printf 'safe-update: update succeeded with %s\n' "$target_image"
printf 'safe-update: retained checkpoint: %s\n' "$checkpoint_dir"
printf 'safe-update: retained rollback image: %s\n' "$rollback_image"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  safe_update_main "$@"
fi
