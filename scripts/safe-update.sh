#!/usr/bin/env bash
set -Eeuo pipefail

# Deploy one pinned Omni Money image with an offline checkpoint. This entry
# point treats Compose files and paths as data, never as shell code. It is
# written for Bash and the GNU userland used by the production Linux host; the
# stat compatibility layer also keeps preflight tests useful on macOS.

service_name="omni-money"
project_name="omni-money"
target_image="${1:-}"
script_source="${BASH_SOURCE[0]:-$0}"
script_dir="$(cd -- "$(dirname -- "$script_source")" && pwd -P)"
project_dir="$(cd -- "$script_dir/.." && pwd -P)"
compose_file="$project_dir/compose.yaml"
compose_definition="$compose_file"
compose_file_pin=""
current_uid="$(id -u)"
env_file_input="${OMNI_UPDATE_ENV_FILE:-.env}"
env_file=""
env_file_pin=""
attestation_file=""
attestation_pin=""
compose_snapshot=""
compose_snapshot_hash=""
rollback_definition=""
project_dir_device=""
project_dir_inode=""
project_dir_nlink=""
pin_dir=""
lock_dir="$project_dir/.omni-money-safe-update.lock"
lock_owned=0
health_timeout="${OMNI_UPDATE_HEALTH_TIMEOUT_SECONDS:-180}"
rollback_armed=0
rollback_running=0
update_verified=0
env_updated=0
checkpoint_ready=0
candidate_id=""
current_id=""
checkpoint_dir=""
archive_path=""
rollback_image=""
data_dir=""
data_uid="10001"
data_gid="10001"
network_name=""
network_ip=""
checkpoint_root=""
checkpoint_root_created=0
checkpoint_root_device=""
checkpoint_root_inode=""
checkpoint_root_nlink=""
checkpoint_dir_device=""
checkpoint_dir_inode=""
checkpoint_dir_nlink=""
data_dir_device=""
data_dir_inode=""
data_dir_nlink=""
env_device=""
env_inode=""
env_nlink=""
env_hash=""
env_pin_device=""
env_pin_inode=""
env_pin_nlink=""
env_pin_hash=""
attestation_device=""
attestation_inode=""
attestation_nlink=""
attestation_hash=""
attestation_pin_device=""
attestation_pin_inode=""
attestation_pin_nlink=""
attestation_pin_hash=""
compose_device=""
compose_inode=""
compose_nlink=""
compose_hash=""
compose_pin_device=""
compose_pin_inode=""
compose_pin_nlink=""
compose_pin_hash=""
compose_snapshot_device=""
compose_snapshot_inode=""
compose_snapshot_nlink=""
pin_device=""
pin_inode=""
pin_nlink=""
updated_env_device=""
updated_env_inode=""
updated_env_nlink=""
updated_env_hash=""
state="init"
compose_ps_sequence=0
compose_source_changed=0

fail() {
  printf 'safe-update: %s\n' "$*" >&2
  return 1
}

stat_owner() {
  local path="$1" value
  if value="$(stat -c '%u' -- "$path" 2>/dev/null)"; then printf '%s' "$value"; else stat -f '%u' "$path"; fi
}

stat_group() {
  local path="$1" value
  if value="$(stat -c '%g' -- "$path" 2>/dev/null)"; then printf '%s' "$value"; else stat -f '%g' "$path"; fi
}

stat_mode() {
  local path="$1" value
  if value="$(stat -c '%a' -- "$path" 2>/dev/null)"; then printf '%s' "$value"; else stat -f '%Lp' "$path"; fi
}

stat_device() {
  local path="$1" value
  if value="$(stat -c '%d' -- "$path" 2>/dev/null)"; then printf '%s' "$value"; else stat -f '%d' "$path"; fi
}

stat_inode() {
  local path="$1" value
  if value="$(stat -c '%i' -- "$path" 2>/dev/null)"; then printf '%s' "$value"; else stat -f '%i' "$path"; fi
}

stat_nlink() {
  local path="$1" value
  if value="$(stat -c '%h' -- "$path" 2>/dev/null)"; then printf '%s' "$value"; else stat -f '%l' "$path"; fi
}

sha256_file() {
  sha256sum -- "$1" | sed -E 's/[[:space:]].*$//'
}

reject_path_syntax() {
  local path="$1"
  case "$path" in
    ''|*/../*|*/..|*/./*|*/.) return 1 ;;
    *$'\n'*|*$'\r'*) return 1 ;;
  esac
}

reject_dangerous_target() {
  local path="$1"
  case "$path" in /|/home|/Users|/root|/tmp|/private) return 1 ;; esac
  case "${path#/}" in */*) return 0 ;; *) return 1 ;; esac
}

owner_mode_ok() {
  local path="$1" policy="${2:-host}" owner group mode
  owner="$(stat_owner "$path")" || return 1
  group="$(stat_group "$path")" || return 1
  mode="$(stat_mode "$path")" || return 1
  case "$mode" in *[!0-7]*|'') return 1 ;; esac
  case "$policy" in
    host) [ "$owner" = "0" ] || [ "$owner" = "$current_uid" ] || return 1 ;;
    data) [ "$owner" = "0" ] || [ "$owner" = "$current_uid" ] || [ "$owner" = "$data_uid" ] || return 1 ;;
    root) [ "$owner" = "0" ] || return 1 ;;
    *) return 1 ;;
  esac
  (( (8#$mode & 18) == 0 )) || return 1
  printf '%s:%s:%s' "$owner" "$group" "$mode" >/dev/null
}

validate_directory_chain() {
  local path="$1" label="$2" policy="${3:-host}" current="/" component
  local -a components=()
  if [ "${path#/}" = "$path" ]; then fail "$label must be absolute: $path"; return 1; fi
  if ! reject_path_syntax "$path"; then fail "$label contains an unsafe path component: $path"; return 1; fi
  IFS='/' read -r -a components <<< "${path#/}"
  for component in "${components[@]}"; do
    if [ -z "$component" ]; then fail "$label contains an empty path component: $path"; return 1; fi
    current="${current%/}/$component"
    if [ -L "$current" ]; then fail "$label must not contain symbolic links: $path"; return 1; fi
    if [ ! -e "$current" ]; then fail "$label component does not exist: $current"; return 1; fi
    if [ ! -d "$current" ]; then fail "$label component must be a normal directory: $current"; return 1; fi
    if ! owner_mode_ok "$current" "$policy"; then fail "$label component has an untrusted owner or mode: $current"; return 1; fi
  done
}

validate_existing_file() {
  local path="$1" label="$2" policy="${3:-host}" parent
  if ! reject_dangerous_target "$path"; then fail "$label is too broad or dangerous: $path"; return 1; fi
  parent="$(dirname -- "$path")"
  # A root-owned document may live in an operator-owned, non-writable config
  # directory. The file owner contract is strict; the parent directory uses
  # the separate trusted-host contract.
  if [ "$policy" = root ]; then
    validate_directory_chain "$parent" "$label parent" host || return 1
  else
    validate_directory_chain "$parent" "$label parent" "$policy" || return 1
  fi
  if [ -L "$path" ]; then fail "$label must not be a symbolic link: $path"; return 1; fi
  if [ ! -f "$path" ]; then fail "$label must be a regular file: $path"; return 1; fi
  if ! owner_mode_ok "$path" "$policy"; then fail "$label has an untrusted owner or mode: $path"; return 1; fi
}

validate_existing_directory() {
  local path="$1" label="$2" policy="${3:-host}"
  if ! reject_dangerous_target "$path"; then fail "$label is too broad or dangerous: $path"; return 1; fi
  validate_directory_chain "$path" "$label" "$policy"
}

validate_data_directory() {
  local path="$1" label="$2" owner group mode
  validate_existing_directory "$path" "$label" data || return 1
  owner="$(stat_owner "$path")"; group="$(stat_group "$path")"; mode="$(stat_mode "$path")"
  [ "$owner" = "$data_uid" ] && [ "$group" = "$data_gid" ] && [ "$mode" = "700" ] || {
    fail "$label must be owned by ${data_uid}:${data_gid} with mode 0700: $path"
    return 1
  }
}

validate_root_owned_file() {
  local path="$1" label="$2" owner group mode parent
  # The attestation document is root-owned, while its trusted project/secret
  # directory may be owned by the operator running the updater. Keep those
  # host-directory and document-owner contracts separate.
  reject_dangerous_target "$path" || { fail "$label is too broad or dangerous: $path"; return 1; }
  parent="$(dirname -- "$path")"
  validate_directory_chain "$parent" "$label parent" host || return 1
  if [ -L "$path" ]; then fail "$label must not be a symbolic link: $path"; return 1; fi
  if [ ! -f "$path" ]; then fail "$label must be a regular file: $path"; return 1; fi
  owner_mode_ok "$path" root || { fail "$label has an untrusted owner or mode: $path"; return 1; }
  owner="$(stat_owner "$path")"; group="$(stat_group "$path")"; mode="$(stat_mode "$path")"
  [ "$owner" = "0" ] || { fail "$label must be root-owned: $path"; return 1; }
  [ "$group" = "0" ] || { fail "$label must have root group ownership: $path"; return 1; }
  case "$mode" in 400|440|444) ;; *) fail "$label must be private/read-only: $path"; return 1 ;; esac
}

resolve_config_path() {
  local value="$1"
  case "$value" in ./*) value="${value#./}" ;; esac
  if ! reject_path_syntax "$value"; then fail "configured path contains an unsafe component: $value"; return 1; fi
  case "$value" in /*) printf '%s' "$value" ;; *) printf '%s/%s' "$project_dir" "$value" ;; esac
}

path_contains() {
  local parent="${1%/}" child="${2%/}"
  [ "$parent" = "/" ] && return 0
  case "$child/" in "$parent/"*) return 0 ;; esac
  return 1
}

validate_pinned_directory() {
  local path="$1" label="$2" expected_device="$3" expected_inode="$4" expected_nlink="$5" policy="${6:-host}"
  validate_existing_directory "$path" "$label" "$policy" || return 1
  [ "$(stat_device "$path")" = "$expected_device" ] || { fail "$label changed filesystem identity"; return 1; }
  [ "$(stat_inode "$path")" = "$expected_inode" ] || { fail "$label was replaced unexpectedly"; return 1; }
  [ "$(stat_nlink "$path")" = "$expected_nlink" ] || { fail "$label link count changed unexpectedly"; return 1; }
}

validate_pinned_file() {
  local path="$1" label="$2" expected_device="$3" expected_inode="$4" expected_nlink="$5" expected_hash="$6" policy="${7:-host}"
  validate_existing_file "$path" "$label" "$policy" || return 1
  [ "$(stat_device "$path")" = "$expected_device" ] || { fail "$label changed filesystem identity"; return 1; }
  [ "$(stat_inode "$path")" = "$expected_inode" ] || { fail "$label was replaced unexpectedly"; return 1; }
  [ "$(stat_nlink "$path")" = "$expected_nlink" ] || { fail "$label link count changed unexpectedly"; return 1; }
  [ "$(sha256_file "$path")" = "$expected_hash" ] || { fail "$label content changed unexpectedly"; return 1; }
}

clear_compose_environment() {
  unset COMPOSE_FILE COMPOSE_PROJECT_NAME COMPOSE_PROFILES COMPOSE_PARALLEL_LIMIT
  unset COMPOSE_ENV_FILES COMPOSE_PATH_SEPARATOR COMPOSE_IGNORE_ORPHANS COMPOSE_REMOVE_ORPHANS
  unset COMPOSE_SSH_AUTH_SOCK COMPOSE_MENU COMPOSE_EXPERIMENTAL COMPOSE_ANSI
  unset COMPOSE_STATUS_STDOUT COMPOSE_PROGRESS COMPOSE_DRY_RUN COMPOSE_SERVICE COMPOSE_DISABLE_ENV_FILE
}

compose() {
  # The selected env file is always the immutable private pin. Explicit
  # project/file arguments make COMPOSE_* ambient configuration irrelevant.
  env -u COMPOSE_FILE -u COMPOSE_PROJECT_NAME -u COMPOSE_PROFILES \
    -u COMPOSE_PARALLEL_LIMIT -u COMPOSE_ENV_FILES -u COMPOSE_PATH_SEPARATOR \
    -u COMPOSE_IGNORE_ORPHANS -u COMPOSE_REMOVE_ORPHANS -u COMPOSE_SSH_AUTH_SOCK \
    -u COMPOSE_MENU -u COMPOSE_EXPERIMENTAL -u COMPOSE_ANSI \
    -u COMPOSE_STATUS_STDOUT -u COMPOSE_PROGRESS -u COMPOSE_DRY_RUN \
    -u COMPOSE_SERVICE -u COMPOSE_DISABLE_ENV_FILE docker compose --env-file "$env_file_pin" \
    --project-directory "$project_dir" --project-name "$project_name" -f "$compose_definition" "$@"
}

read_single_compose_id() {
  local output_file="$1" label="$2" id count=0
  local -a ids=()
  while IFS= read -r id; do
    [ -n "$id" ] || continue
    ids+=("$id")
  done < "$output_file"
  count="${#ids[@]}"
  [ "$count" -eq 1 ] || { fail "$label must resolve to exactly one container (got $count)"; return 1; }
  printf '%s' "${ids[0]}"
}

compose_single_id() {
  local image="$1" label="$2" output_file safe_label
  safe_label="${label// /_}"
  output_file="$pin_dir/compose-ps.$$.${safe_label}.txt"
  create_exclusive_file "$output_file" || { fail "could not create exclusive Compose ps output"; return 1; }
  if ! OMNI_IMAGE="$image" compose ps --all -q "$service_name" > "$output_file"; then
    fail "$label Compose ps failed"
    return 1
  fi
  validate_existing_file "$output_file" "Compose ps output" || return 1
  read_single_compose_id "$output_file" "$label"
}

container_state() { docker inspect --format '{{.State.Status}}' "$1"; }
container_health() { docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}missing{{end}}' "$1" 2>/dev/null || true; }
container_image_id() { docker inspect --format '{{.Image}}' "$1"; }
container_config_user() { docker inspect --format '{{.Config.User}}' "$1"; }
container_ports() { docker inspect --format '{{json .HostConfig.PortBindings}}' "$1"; }
container_mounts() { docker inspect --format '{{json .Mounts}}' "$1"; }
container_networks() { docker inspect --format '{{json .NetworkSettings.Networks}}' "$1"; }

validate_container_config() {
  local id="$1" expected_image="$2" expected_state="$3" label="$4" ports mounts networks user readonly capdrop security
  [ "$(container_image_id "$id")" = "$expected_image" ] || { fail "$label image ID does not match the resolved image"; return 1; }
  [ "$(container_state "$id")" = "$expected_state" ] || { fail "$label must be in $expected_state state"; return 1; }
  ports="$(container_ports "$id")"
  jq -e '(. == null) or (. == {})' <<< "$ports" >/dev/null || { fail "$label has published host ports"; return 1; }
  mounts="$(container_mounts "$id")"
  jq -e --arg source "$data_dir" --argjson inspected "$mounts" '
    . as $root |
    .services["omni-money"].secrets as $service_secrets |
    (($service_secrets | map({Type: "bind", Source: $root.secrets[.source].file, Destination: ("/run/secrets/" + .target), RW: false})) +
      [{Type: "bind", Source: $source, Destination: "/app/data", RW: true}, {Type: "tmpfs", Source: "", Destination: "/tmp", RW: true}]) as $expected |
    (($inspected | map({Type, Source: (if .Type == "tmpfs" then "" else (.Source // "") end), Destination, RW: (.RW // false)}) | sort_by(.Destination)) ==
      ($expected | sort_by(.Destination)))
  ' "$compose_snapshot" >/dev/null || { fail "$label has an unexpected mount set"; return 1; }
  networks="$(container_networks "$id")"
  jq -e --arg network "$network_name" '((keys | length) == 1) and (has($network))' <<< "$networks" >/dev/null || { fail "$label has an unexpected network set"; return 1; }
  user="$(container_config_user "$id")"
  case "$user" in ''|0|root|0:0|root:root) fail "$label must not be configured as root"; return 1 ;; esac
  readonly="$(docker inspect --format '{{.HostConfig.ReadonlyRootfs}}' "$id")"
  [ "$readonly" = "true" ] || { fail "$label must use a read-only root filesystem"; return 1; }
  capdrop="$(docker inspect --format '{{json .HostConfig.CapDrop}}' "$id")"
  jq -e 'index("ALL") != null' <<< "$capdrop" >/dev/null || { fail "$label must drop all capabilities"; return 1; }
  security="$(docker inspect --format '{{json .HostConfig.SecurityOpt}}' "$id")"
  jq -e 'index("no-new-privileges:true") != null' <<< "$security" >/dev/null || { fail "$label must set no-new-privileges"; return 1; }
}

validate_container_user() {
  local id="$1" label="$2" uid gid
  uid="$(docker exec "$id" id -u)" || { fail "$label UID could not be inspected"; return 1; }
  gid="$(docker exec "$id" id -g)" || { fail "$label GID could not be inspected"; return 1; }
  [ "$uid" = "$data_uid" ] && [ "$gid" = "$data_gid" ] || { fail "$label must run as ${data_uid}:${data_gid}, got ${uid}:${gid}"; return 1; }
}

validate_single_network_ip() {
  local id="$1" networks
  networks="$(container_networks "$id")"
  jq -e --arg network "$network_name" '((keys | length) == 1) and (has($network))' <<< "$networks" >/dev/null || return 1
  docker inspect --format "{{with index .NetworkSettings.Networks \"$network_name\"}}{{.IPAddress}}{{end}}" "$id"
}

disconnect_all_networks() {
  local id="$1" networks network
  networks="$(container_networks "$id")"
  while IFS= read -r network; do
    [ -n "$network" ] || continue
    if ! docker network disconnect "$network" "$id" >/dev/null; then
      fail "could not disconnect $id from network $network"
      return 1
    fi
  done < <(jq -r 'keys[]' <<< "$networks")
  networks="$(container_networks "$id")"
  jq -e 'length == 0' <<< "$networks" >/dev/null || { fail "container retained a network after isolation"; return 1; }
}

create_disconnected_container() {
  local image="$1" expected_image="$2" label="$3"
  if ! OMNI_IMAGE="$image" compose up --no-start --no-build --force-recreate "$service_name" >/dev/null; then
    fail "$label Compose up --no-start failed"
    return 1
  fi
  candidate_id="$(compose_single_id "$image" "$label Compose ps")"
  validate_container_config "$candidate_id" "$expected_image" created "$label container"
  disconnect_all_networks "$candidate_id" || return 1
  [ "$(container_state "$candidate_id")" = "created" ] || { fail "$label was not left stopped after network isolation"; return 1; }
}

stop_container_safely() {
  local id="$1" label="$2" state_value
  [ -n "$id" ] || return 0
  # A stop can return non-zero after delivering the signal (for example, a
  # timeout or interrupted client). Retry once, then inspect the pinned ID;
  # rollback must not proceed while either the old or candidate container is
  # still running.
  docker stop --time 30 "$id" >/dev/null 2>&1 || true
  state_value="$(container_state "$id" 2>/dev/null || true)"
  if [ "$state_value" = running ]; then
    docker stop --time 30 "$id" >/dev/null 2>&1 || true
    state_value="$(container_state "$id" 2>/dev/null || true)"
  fi
  case "$state_value" in
    created|exited|dead) return 0 ;;
    running) fail "$label container could not be stopped"; return 1 ;;
    *) fail "$label container state could not be verified after stop"; return 1 ;;
  esac
}

prepare_rollback_definition() {
  rollback_definition="$pin_dir/compose-rollback.json"
  create_exclusive_file "$rollback_definition" || return 1
  # Compose cannot override an already-resolved `image` field with an env
  # variable. Derive this private, immutable rollback view from the one
  # resolved snapshot; all mounts, networks, ports, and security settings are
  # therefore identical and only the pre-tagged image reference changes.
  if ! jq --arg image "$rollback_image" '.services["omni-money"].image = $image' "$compose_snapshot" > "$rollback_definition"; then
    return 1
  fi
  validate_existing_file "$rollback_definition" "rollback Compose snapshot" || return 1
  jq -e --arg image "$rollback_image" '.services["omni-money"].image == $image' "$rollback_definition" >/dev/null || return 1
}

wait_for_health() {
  local id="$1" deadline=$((SECONDS + health_timeout)) status
  while (( SECONDS < deadline )); do
    status="$(container_health "$id")"
    [ "$status" = "healthy" ] && return 0
    [ "$status" = "unhealthy" ] || [ "$(container_state "$id")" = "running" ] || return 1
    sleep 2
  done
  return 1
}

validate_tar_members() {
  local archive="$1" member line type members details
  members="$pin_dir/tar-members.$$.txt"
  details="$pin_dir/tar-details.$$.txt"
  create_exclusive_file "$members" || return 1
  create_exclusive_file "$details" || return 1
  # GNU tar quotes control characters in names in its normal listing. Reject
  # any escaped/control name instead of trying to reconstruct it from a line
  # listing; this deliberately excludes unusual names (newline, tab,
  # backslash, etc.) from a backup rather than risking an ambiguous restore.
  if ! tar -tf "$archive" > "$members"; then
    rm -f -- "$members" "$details"
    return 1
  fi
  while IFS= read -r member; do
    case "$member" in
      ''|/*|../*|*/../*|*/..|*\\*|*$'\n'*|*$'\r'*|*$'\t'*) rm -f -- "$members" "$details"; return 1 ;;
    esac
  done < "$members"
  if ! tar -tvf "$archive" > "$details"; then
    rm -f -- "$members" "$details"
    return 1
  fi
  while IFS= read -r line; do
    case "$line" in *\\*|*$'\n'*|*$'\r'*|*$'\t'*) rm -f -- "$members" "$details"; return 1 ;; esac
    type="${line:0:1}"
    case "$type" in l|h|b|c|p|s) rm -f -- "$members" "$details"; return 1 ;; esac
  done < "$details"
  rm -f -- "$members" "$details"
}

validate_source_tree() {
  local root="$1" label="$2" entry tree owner links
  tree="$pin_dir/source-tree.$$.nul"
  create_exclusive_file "$tree" || return 1
  if ! find -P "$root" -mindepth 1 -print0 > "$tree"; then
    rm -f -- "$tree"
    return 1
  fi
  while IFS= read -r -d '' entry; do
    [ -L "$entry" ] && { fail "$label contains a symbolic link: $entry"; rm -f -- "$tree"; return 1; }
    [ -d "$entry" ] || [ -f "$entry" ] || { fail "$label contains a non-directory/non-regular entry: $entry"; rm -f -- "$tree"; return 1; }
    owner_mode_ok "$entry" data || { fail "$label contains an untrusted owner or mode: $entry"; rm -f -- "$tree"; return 1; }
    if [ -f "$entry" ]; then
      links="$(stat_nlink "$entry")" || { rm -f -- "$tree"; return 1; }
      [ "$links" = "1" ] || { fail "$label contains a hard-linked regular file: $entry"; rm -f -- "$tree"; return 1; }
    fi
  done < "$tree"
  rm -f -- "$tree"
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
  # GNU mv -n does not replace a destination that appears after the first
  # check. Verify that the source disappeared so a no-op is never accepted.
  mv -n -- "$source" "$destination" || return 1
  [ ! -e "$source" ] && [ -e "$destination" ] && [ ! -L "$destination" ]
}

restore_checkpoint() {
  local failed_data="$checkpoint_dir/failed-candidate-data" incomplete_restore="$checkpoint_dir/incomplete-restore"
  [ "$checkpoint_ready" -eq 1 ] || return 0
  validate_pinned_directory "$checkpoint_root" "checkpoint root" "$checkpoint_root_device" "$checkpoint_root_inode" "$checkpoint_root_nlink" || return 1
  validate_pinned_directory "$checkpoint_dir" "checkpoint directory" "$checkpoint_dir_device" "$checkpoint_dir_inode" "$checkpoint_dir_nlink" || return 1
  validate_pinned_file "$archive_path" "checkpoint archive" "$archive_device" "$archive_inode" "$archive_nlink" "$archive_hash" || return 1
  validate_pinned_file "$archive_path.sha256" "checkpoint checksum" "$checksum_device" "$checksum_inode" "$checksum_nlink" "$checksum_hash" || return 1
  validate_data_directory "$data_dir" "live data directory" || return 1
  [ ! -e "$failed_data" ] && [ ! -L "$failed_data" ] || return 1
  [ ! -e "$incomplete_restore" ] && [ ! -L "$incomplete_restore" ] || return 1
  sha256sum --check "$archive_path.sha256" >/dev/null || return 1
  validate_tar_members "$archive_path" || { fail "checkpoint archive contains unsafe members"; return 1; }
  mv -- "$data_dir" "$failed_data" || return 1
  mkdir -m 0700 -- "$data_dir" || return 1
  chown "$data_uid:$data_gid" "$data_dir" || return 1
  validate_data_directory "$data_dir" "restore data directory" || return 1
  if ! tar --numeric-owner --no-overwrite-dir -xpf "$archive_path" -C "$data_dir"; then
    mv -- "$data_dir" "$incomplete_restore" || true
    mv -- "$failed_data" "$data_dir" || true
    return 1
  fi
  chown -R "$data_uid:$data_gid" "$data_dir" || return 1
  validate_data_directory "$data_dir" "restored data directory" || return 1
  validate_source_tree "$data_dir" "restored data tree" || return 1
}

restore_original_env() {
  local should_restore=0
  validate_pinned_file "$env_file_pin" "pinned update env file" "$env_pin_device" "$env_pin_inode" "$env_pin_nlink" "$env_pin_hash" || return 1
  if [ "$env_updated" -eq 1 ]; then
    validate_pinned_file "$env_file" "updated update env file" "$updated_env_device" "$updated_env_inode" "$updated_env_nlink" "$updated_env_hash" || return 1
    should_restore=1
  elif ! validate_pinned_file "$env_file" "pre-update env file" "$env_device" "$env_inode" "$env_nlink" "$env_hash"; then
    # A source/env swap can happen before the normal OMNI_IMAGE write. It is
    # still safe to restore only after confirming the replacement is a normal
    # file in the same trusted parent; symlink/untrusted replacements remain
    # stopped for operator recovery.
    validate_existing_file "$env_file" "swapped update env file" || return 1
    should_restore=1
  fi
  [ "$should_restore" -eq 1 ] || return 0
  local restore_tmp="$pin_dir/env.restore"
  create_exclusive_file "$restore_tmp" || return 1
  cp -p -- "$env_file_pin" "$restore_tmp" || return 1
  chmod 0400 "$restore_tmp" || return 1
  mv -- "$restore_tmp" "$env_file" || return 1
  validate_existing_file "$env_file" "restored update env file" || return 1
  env_updated=0
}

cleanup_private_state() {
  trap - EXIT INT TERM
  if [ "$lock_owned" -eq 1 ] && [ -d "$lock_dir" ] && [ ! -L "$lock_dir" ]; then rmdir -- "$lock_dir" 2>/dev/null || true; fi
  if [ -n "$pin_dir" ] && [ -d "$pin_dir" ] && [ ! -L "$pin_dir" ] && [ -n "$pin_device" ] &&
     [ "$(stat_device "$pin_dir" 2>/dev/null || true)" = "$pin_device" ] &&
     [ "$(stat_inode "$pin_dir" 2>/dev/null || true)" = "$pin_inode" ] &&
     [ "$(stat_nlink "$pin_dir" 2>/dev/null || true)" = "$pin_nlink" ]; then
    rm -rf -- "$pin_dir" || true
  fi
}

rollback_update() {
  local original_status="$1" old_id="" old_image_id="${current_image_id:-}"
  trap - EXIT INT TERM
  rollback_running=1
  state="rolling-back"
  set +e
  printf 'safe-update: candidate failed; restoring the pre-update checkpoint and previous image\n' >&2
  if [ -n "$candidate_id" ] && ! stop_container_safely "$candidate_id" candidate; then
    printf 'safe-update: candidate could not be safely stopped; service remains stopped\n' >&2
    cleanup_private_state; exit "$original_status"
  fi
  if [ -n "$current_id" ] && ! stop_container_safely "$current_id" current; then
    printf 'safe-update: current container could not be safely stopped; service remains stopped\n' >&2
    cleanup_private_state; exit "$original_status"
  fi
  state="rollback-stopped"
  if ! validate_pinned_directory "$project_dir" "Compose project directory" "$project_dir_device" "$project_dir_inode" "$project_dir_nlink" ||
     ! validate_pinned_file "$attestation_file" "update attestation" "$attestation_device" "$attestation_inode" "$attestation_nlink" "$attestation_hash" root ||
     ! validate_pinned_file "$attestation_pin" "pinned update attestation" "$attestation_pin_device" "$attestation_pin_inode" "$attestation_pin_nlink" "$attestation_pin_hash" ||
     ! validate_pinned_file "$env_file_pin" "pinned update env file" "$env_pin_device" "$env_pin_inode" "$env_pin_nlink" "$env_pin_hash" ||
     ! validate_pinned_file "$compose_snapshot" "Compose config snapshot" "$compose_snapshot_device" "$compose_snapshot_inode" "$compose_snapshot_nlink" "$compose_snapshot_hash"; then
    printf 'safe-update: trusted Compose/attestation inputs changed; service remains stopped\n' >&2
    cleanup_private_state; exit "$original_status"
  fi
  if ! validate_pinned_file "$compose_file" "Compose file" "$compose_device" "$compose_inode" "$compose_nlink" "$compose_hash"; then
    compose_source_changed=1
    printf 'safe-update: Compose source changed; rollback will use the pinned resolved snapshot\n' >&2
  fi
  if ! restore_checkpoint; then
    printf 'safe-update: automatic data restore failed; service remains stopped. Recovery artifacts: %s\n' "$checkpoint_dir" >&2
    cleanup_private_state; exit "$original_status"
  fi
  state="restored"
  if ! restore_original_env; then
    printf 'safe-update: pre-update env file could not be restored; service remains stopped\n' >&2
    cleanup_private_state; exit "$original_status"
  fi
  candidate_id=""
  if ! prepare_rollback_definition; then
    printf 'safe-update: rollback Compose snapshot could not be prepared. Service remains stopped\n' >&2
    cleanup_private_state; exit "$original_status"
  fi
  compose_definition="$rollback_definition"
  if ! create_disconnected_container "$rollback_image" "$old_image_id" "rollback"; then
    if [ -n "$candidate_id" ]; then stop_container_safely "$candidate_id" rollback >/dev/null 2>&1 || true; fi
    printf 'safe-update: previous container could not be recreated. Checkpoint: %s\n' "$checkpoint_dir" >&2
    cleanup_private_state; exit "$original_status"
  fi
  state="rollback-isolated"
  old_id="$candidate_id"
  if ! docker start "$old_id" >/dev/null || ! validate_container_user "$old_id" "rollback container" || ! wait_for_health "$old_id"; then
    stop_container_safely "$old_id" rollback >/dev/null 2>&1 || true
    printf 'safe-update: previous image did not become healthy after data restore. Service remains isolated. Checkpoint: %s\n' "$checkpoint_dir" >&2
    cleanup_private_state; exit "$original_status"
  fi
  state="rollback-healthy"
  if ! docker network connect --ip "$network_ip" "$network_name" "$old_id" >/dev/null; then
    stop_container_safely "$old_id" rollback >/dev/null 2>&1 || true
    printf 'safe-update: previous image is healthy but could not be reconnected to ingress\n' >&2
    cleanup_private_state; exit "$original_status"
  fi
  if [ "$(validate_single_network_ip "$old_id")" != "$network_ip" ]; then
    stop_container_safely "$old_id" rollback >/dev/null 2>&1 || true
    printf 'safe-update: rollback container was reconnected with an unexpected network identity\n' >&2
    cleanup_private_state; exit "$original_status"
  fi
  if [ "$compose_source_changed" -eq 1 ]; then
    printf 'safe-update: rollback completed from the pinned snapshot; inspect the changed Compose source before retrying. Checkpoint: %s\n' "$checkpoint_dir" >&2
  else
    printf 'safe-update: rollback completed because the candidate update failed. Checkpoint: %s\n' "$checkpoint_dir" >&2
  fi
  cleanup_private_state; exit "$original_status"
}

on_signal() {
  local signal="$1"
  printf 'safe-update: received %s; entering rollback\n' "$signal" >&2
  exit 130
}

on_exit() {
  local status="$?"
  if [ "$status" -ne 0 ] && [ "$rollback_armed" -eq 1 ] && [ "$update_verified" -eq 0 ] && [ "$rollback_running" -eq 0 ]; then rollback_update "$status"; fi
  cleanup_private_state
  exit "$status"
}

safe_update_main() {
  set -Eeuo pipefail
  umask 077
  clear_compose_environment
  for command_name in bash docker jq sha256sum tar stat awk sed grep mktemp du df chown env find uname id dirname pwd cp chmod mkdir rmdir rm mv date sleep; do command -v "$command_name" >/dev/null 2>&1 || fail "required command is missing: $command_name"; done
  [ "$(uname -s)" = "Linux" ] || fail "safe-update requires the Linux production host contract"
  tar --version 2>/dev/null | grep -q 'GNU tar' || fail "safe-update requires GNU tar"
  [ -f "$compose_file" ] && [ ! -L "$compose_file" ] || fail "Compose file must be a regular non-symlink file: $compose_file"
  validate_existing_file "$compose_file" "Compose file"
  compose_device="$(stat_device "$compose_file")"; compose_inode="$(stat_inode "$compose_file")"; compose_nlink="$(stat_nlink "$compose_file")"; compose_hash="$(sha256_file "$compose_file")"
  env_file="$(resolve_config_path "$env_file_input")"
  validate_existing_file "$env_file" "update env file"
  env_device="$(stat_device "$env_file")"; env_inode="$(stat_inode "$env_file")"; env_nlink="$(stat_nlink "$env_file")"; env_hash="$(sha256_file "$env_file")"
  case "$health_timeout" in ''|*[!0-9]*) fail "OMNI_UPDATE_HEALTH_TIMEOUT_SECONDS must be an integer" ;; esac
  (( health_timeout >= 30 && health_timeout <= 600 )) || fail "health timeout must be between 30 and 600 seconds"
  [ -z "${OMNI_UPDATE_CHECKPOINT_DIR+x}" ] || fail "OMNI_UPDATE_CHECKPOINT_DIR is not supported; checkpoint root is attestation-bound"
  [ -n "$target_image" ] || fail "usage: scripts/safe-update.sh <pinned-image:version-or-digest>"
  case "$target_image" in *[!A-Za-z0-9._/@:+-]*) fail "image reference contains unsupported characters" ;; esac
  case "$target_image" in *:latest|latest) fail "the mutable latest tag is not allowed" ;; esac
  case "$target_image" in *@sha256:*|*:* ) ;; *) fail "use an explicit version tag or sha256 digest" ;; esac
  mkdir -m 0700 "$lock_dir" || fail "another safe update is already running or lock path is unsafe"
  lock_owned=1
  trap on_exit EXIT; trap 'on_signal INT' INT; trap 'on_signal TERM' TERM
  validate_existing_directory "$lock_dir" "safe-update lock"
  pin_dir="$(mktemp -d "$project_dir/.omni-money-safe-update-pin.XXXXXX")"; chmod 0700 "$pin_dir"
  validate_existing_directory "$pin_dir" "private safe-update pin directory"
  pin_device="$(stat_device "$pin_dir")"; pin_inode="$(stat_inode "$pin_dir")"; pin_nlink="$(stat_nlink "$pin_dir")"
  # Capture project-directory link count only after creating our own lock and
  # pin subdirectories; those expected entries must not look like a swap.
  project_dir_device="$(stat_device "$project_dir")"; project_dir_inode="$(stat_inode "$project_dir")"; project_dir_nlink="$(stat_nlink "$project_dir")"
  env_file_pin="$pin_dir/environment"; create_exclusive_file "$env_file_pin" || fail "could not create exclusive update env pin"; cp -p -- "$env_file" "$env_file_pin"; chmod 0400 "$env_file_pin"
  validate_existing_file "$env_file_pin" "pinned update env file"
  env_pin_device="$(stat_device "$env_file_pin")"; env_pin_inode="$(stat_inode "$env_file_pin")"; env_pin_nlink="$(stat_nlink "$env_file_pin")"; env_pin_hash="$(sha256_file "$env_file_pin")"
  [ "$env_pin_hash" = "$env_hash" ] || fail "update env file changed while it was being pinned"
  compose_file_pin="$pin_dir/compose.yaml"; create_exclusive_file "$compose_file_pin" || fail "could not create exclusive Compose definition pin"; cp -p -- "$compose_file" "$compose_file_pin"; chmod 0400 "$compose_file_pin"; validate_pinned_file "$compose_file_pin" "pinned Compose definition" "$(stat_device "$compose_file_pin")" "$(stat_inode "$compose_file_pin")" "$(stat_nlink "$compose_file_pin")" "$compose_hash"
  compose_pin_device="$(stat_device "$compose_file_pin")"; compose_pin_inode="$(stat_inode "$compose_file_pin")"; compose_pin_nlink="$(stat_nlink "$compose_file_pin")"; compose_pin_hash="$(sha256_file "$compose_file_pin")"
  attestation_pin="$pin_dir/update-attestation.json"; compose_snapshot="$pin_dir/compose-config.json"
  create_exclusive_file "$compose_snapshot" || fail "could not create Compose config snapshot"
  state="configuring"
  compose_definition="$compose_file_pin"
  validate_pinned_file "$compose_file_pin" "pinned Compose definition" "$compose_pin_device" "$compose_pin_inode" "$compose_pin_nlink" "$compose_pin_hash"
  validate_pinned_file "$env_file_pin" "pinned update env file" "$env_pin_device" "$env_pin_inode" "$env_pin_nlink" "$env_pin_hash"
  OMNI_IMAGE="$target_image" compose config --format json > "$compose_snapshot"
  validate_existing_file "$compose_snapshot" "Compose config snapshot"
  compose_snapshot_device="$(stat_device "$compose_snapshot")"; compose_snapshot_inode="$(stat_inode "$compose_snapshot")"; compose_snapshot_nlink="$(stat_nlink "$compose_snapshot")"
  compose_snapshot_hash="$(sha256_file "$compose_snapshot")"
  jq -e --arg image "$target_image" '
    .services["omni-money"] as $s |
    ($s.image == $image) and ($s.container_name == "omni-money") and
    ($s.read_only == true) and (($s.cap_drop // []) | index("ALL") != null) and
    (($s.security_opt // []) | index("no-new-privileges:true") != null) and
    (($s.ports // []) | length == 0) and
    (($s.volumes | length) == 2) and
    (([$s.volumes[]? | select(.target == "/app/data")] | length) == 1) and
    (([$s.volumes[]? | select(.target == "/app/data")][0] | .type == "bind" and ((.read_only // false) == false))) and
    (([$s.volumes[]? | select(.target == "/tmp")] | length) == 1) and
    (([$s.volumes[]? | select(.target == "/tmp")][0] | .type == "tmpfs")) and
    (all($s.volumes[]?; .target == "/app/data" or .target == "/tmp")) and
    (($s.secrets | map(.source) | sort) == (["omni_control_database_key", "omni_data_at_rest_attestation"] | sort)) and
    (($s.networks | keys | length) == 1) and (($s.networks | keys)[0] == "pangolin_target")
    and ($s.networks.pangolin_target.ipv4_address | type == "string")
    and (.networks.pangolin_target.internal == true)
  ' "$compose_snapshot" >/dev/null || fail "resolved Compose config violates the production security contract"
  # From this point every Compose call, including ps/up during rollback, uses
  # the one resolved snapshot. The source YAML remains pinned and is checked
  # for replacement, but is never re-parsed after this binding point.
  compose_definition="$compose_snapshot"
  configured_data_dir="$(jq -er '.services["omni-money"].volumes[] | select(.target == "/app/data") | .source' "$compose_snapshot")"
  attestation_file="$(jq -er '.["x-omni-update-attestation-file"]' "$compose_snapshot")"
  validate_root_owned_file "$attestation_file" "update encrypted-volume attestation"
  attestation_device="$(stat_device "$attestation_file")"; attestation_inode="$(stat_inode "$attestation_file")"; attestation_nlink="$(stat_nlink "$attestation_file")"; attestation_hash="$(sha256_file "$attestation_file")"
  create_exclusive_file "$attestation_pin" || fail "could not create exclusive update attestation pin"; cp -p -- "$attestation_file" "$attestation_pin"; chmod 0400 "$attestation_pin"
  validate_existing_file "$attestation_pin" "pinned update attestation"
  attestation_pin_device="$(stat_device "$attestation_pin")"; attestation_pin_inode="$(stat_inode "$attestation_pin")"; attestation_pin_nlink="$(stat_nlink "$attestation_pin")"; attestation_pin_hash="$(sha256_file "$attestation_pin")"
  [ "$attestation_pin_hash" = "$attestation_hash" ] || fail "update attestation changed while it was being pinned"
  jq -e 'type == "object" and .version == 1 and .protection == "external-encrypted-volume" and (.encrypted_volume_root | type == "string") and (.data_root | type == "string") and (.checkpoint_root | type == "string")' "$attestation_pin" >/dev/null || fail "update attestation schema is invalid"
  configured_data_dir="$(resolve_config_path "$configured_data_dir")"
  current_id="$(compose_single_id "$target_image" "current Compose ps")"
  current_image_id="$(container_image_id "$current_id")"
  mounts="$(container_mounts "$current_id")"
  data_mount_count="$(jq '[.[] | select(.Destination == "/app/data")] | length' <<< "$mounts")"; [ "$data_mount_count" -eq 1 ] || fail "current container must have exactly one /app/data mount"
  data_dir="$(jq -er '.[] | select(.Destination == "/app/data") | .Source' <<< "$mounts")"; [ "$data_dir" = "$configured_data_dir" ] || fail "resolved Compose data source does not match the running /app/data source"
  validate_data_directory "$data_dir" "live data directory"
  validate_source_tree "$data_dir" "live data tree"
  data_dir_device="$(stat_device "$data_dir")"; data_dir_inode="$(stat_inode "$data_dir")"; data_dir_nlink="$(stat_nlink "$data_dir")"
  expected_network_name="$(jq -er '.networks.pangolin_target.name' "$compose_snapshot")"
  expected_ip="$(jq -er '.services["omni-money"].networks.pangolin_target.ipv4_address' "$compose_snapshot")"
  network_name="$expected_network_name"
  validate_container_config "$current_id" "$current_image_id" running "current"
  [ "$(container_health "$current_id")" = "healthy" ] || fail "the current container must be healthy before an update"
  validate_container_user "$current_id" "current container"
  current_user="$(container_config_user "$current_id")"; [ -n "$current_user" ] || fail "current container configured user is empty"
  actual_network_name="$(jq -r 'keys[]' <<< "$(container_networks "$current_id")")"; network_ip="$(validate_single_network_ip "$current_id")"
  [ "$actual_network_name" = "$expected_network_name" ] || fail "current container network does not match resolved Compose network"
  [ "$network_ip" = "$expected_ip" ] || fail "current container IP does not match resolved Compose IP"
  encrypted_volume_root="$(jq -er '.encrypted_volume_root' "$attestation_pin")"; attested_data_root="$(jq -er '.data_root' "$attestation_pin")"; attested_checkpoint_root="$(jq -er '.checkpoint_root' "$attestation_pin")"
  validate_existing_directory "$encrypted_volume_root" "attested encrypted-volume root"
  [ "$attested_data_root" = "$data_dir" ] || fail "update attestation data_root does not match the running host data root"
  checkpoint_root="$(dirname -- "$data_dir")/omni-money-update-checkpoints"
  [ "$attested_checkpoint_root" = "$checkpoint_root" ] || fail "update attestation checkpoint_root does not match the fixed checkpoint root"
  path_contains "$encrypted_volume_root" "$data_dir" || fail "data root is outside the attested encrypted volume root"
  path_contains "$encrypted_volume_root" "$checkpoint_root" || fail "checkpoint root is outside the attested encrypted volume root"
  [ "$(stat_device "$encrypted_volume_root")" = "$data_dir_device" ] || fail "data root is not on the attested encrypted filesystem"
  path_contains "$data_dir" "$checkpoint_root" && fail "checkpoint directory must be outside live data"
  path_contains "$checkpoint_root" "$data_dir" && fail "checkpoint root must not contain live data"
  if [ -e "$checkpoint_root" ] || [ -L "$checkpoint_root" ]; then
    validate_existing_directory "$checkpoint_root" "checkpoint root"
    [ "$(stat_mode "$checkpoint_root")" = "700" ] || fail "checkpoint root must already have mode 0700"
  else
    validate_directory_chain "$(dirname -- "$checkpoint_root")" "checkpoint root parent"
    mkdir -m 0700 "$checkpoint_root"
    checkpoint_root_created=1
  fi
  validate_existing_directory "$checkpoint_root" "checkpoint root"
  checkpoint_root_device="$(stat_device "$checkpoint_root")"; checkpoint_root_inode="$(stat_inode "$checkpoint_root")"; checkpoint_root_nlink="$(stat_nlink "$checkpoint_root")"
  [ "$checkpoint_root_device" = "$data_dir_device" ] || fail "checkpoint root is not on the attested encrypted filesystem"
  # If the fixed checkpoint root is a direct child of the project directory,
  # its creation is the one expected link-count change after the project pin.
  # Any other change (or an unexpected parent) is fail closed rather than
  # silently refreshing the project identity after an attacker has changed it.
  if [ "$checkpoint_root_created" -eq 1 ] && [ "$(dirname -- "$checkpoint_root")" = "$project_dir" ]; then
    project_dir_nlink=$((project_dir_nlink + 1))
  fi
  [ "$(stat_device "$project_dir")" = "$project_dir_device" ] || fail "Compose project directory changed filesystem identity"
  [ "$(stat_inode "$project_dir")" = "$project_dir_inode" ] || fail "Compose project directory was replaced unexpectedly"
  [ "$(stat_nlink "$project_dir")" = "$project_dir_nlink" ] || fail "Compose project directory link count changed unexpectedly"
  validate_pinned_directory "$pin_dir" "private pin directory" "$pin_device" "$pin_inode" "$pin_nlink"
  validate_pinned_directory "$project_dir" "Compose project directory" "$project_dir_device" "$project_dir_inode" "$project_dir_nlink"
  validate_pinned_file "$compose_file" "Compose file" "$compose_device" "$compose_inode" "$compose_nlink" "$compose_hash"
  validate_pinned_file "$compose_file_pin" "pinned Compose definition" "$compose_pin_device" "$compose_pin_inode" "$compose_pin_nlink" "$compose_pin_hash"
  validate_pinned_file "$env_file" "update env file" "$env_device" "$env_inode" "$env_nlink" "$env_hash"
  validate_pinned_file "$env_file_pin" "pinned update env file" "$env_pin_device" "$env_pin_inode" "$env_pin_nlink" "$env_pin_hash"
  validate_pinned_file "$attestation_file" "update attestation" "$attestation_device" "$attestation_inode" "$attestation_nlink" "$attestation_hash" root
  validate_pinned_file "$attestation_pin" "pinned update attestation" "$attestation_pin_device" "$attestation_pin_inode" "$attestation_pin_nlink" "$attestation_pin_hash"
  validate_pinned_file "$compose_snapshot" "Compose config snapshot" "$compose_snapshot_device" "$compose_snapshot_inode" "$compose_snapshot_nlink" "$compose_snapshot_hash"
  timestamp="$(date -u +%Y%m%dT%H%M%SZ)"; checkpoint_dir="$checkpoint_root/$timestamp"; mkdir -m 0700 "$checkpoint_dir"; validate_existing_directory "$checkpoint_dir" "checkpoint directory"
  checkpoint_dir_device="$(stat_device "$checkpoint_dir")"; checkpoint_dir_inode="$(stat_inode "$checkpoint_dir")"; checkpoint_dir_nlink="$(stat_nlink "$checkpoint_dir")"; archive_path="$checkpoint_dir/data.tar"
  # Creating the timestamped checkpoint directory increments its parent's
  # directory link count; pin the expected post-create identity before the
  # stop/checkpoint critical section.
  checkpoint_root_nlink="$(stat_nlink "$checkpoint_root")"
  data_kb="$(du -sk -- "$data_dir" | sed -E 's/[[:space:]].*$//')"; available_kb="$(df -Pk -- "$checkpoint_root" | awk 'NR == 2 {print $4}')"; required_kb=$((data_kb * 2 + 102400)); (( available_kb >= required_kb )) || fail "insufficient free space: rollback-safe update needs at least ${required_kb} KiB free"
  current_image="$(docker inspect --format '{{.Config.Image}}' "$current_id")"; rollback_image="omni-money:rollback-$timestamp"; docker pull "$target_image" >/dev/null; target_image_id="$(docker image inspect --format '{{.Id}}' "$target_image")"; docker tag "$current_image_id" "$rollback_image"
  # Nothing that can mutate the live deployment happens before this point.
  # Arm the EXIT state machine immediately before the direct stop of the
  # pinned current container so a signal/partial stop cannot leave it without
  # an automatic recovery attempt.
  rollback_armed=1; state="armed"; docker stop --time 30 "$current_id" >/dev/null
  [ "$(container_state "$current_id")" != "running" ] || fail "pinned current container remained running after stop"
  state="stopped"
  validate_pinned_directory "$data_dir" "live data directory" "$data_dir_device" "$data_dir_inode" "$data_dir_nlink" data; validate_source_tree "$data_dir" "live data tree"; validate_pinned_directory "$checkpoint_root" "checkpoint root" "$checkpoint_root_device" "$checkpoint_root_inode" "$checkpoint_root_nlink"
  archive_tmp="$pin_dir/data.tar.tmp"; create_exclusive_file "$archive_tmp" || fail "could not create exclusive archive staging file"; tar --numeric-owner -cpf "$archive_tmp" -C "$data_dir" .; validate_pinned_directory "$data_dir" "live data directory after archive" "$data_dir_device" "$data_dir_inode" "$data_dir_nlink" data || fail "live data directory changed while checkpoint was created"; validate_pinned_directory "$checkpoint_root" "checkpoint root after archive" "$checkpoint_root_device" "$checkpoint_root_inode" "$checkpoint_root_nlink" || fail "checkpoint root changed while checkpoint was created"; validate_pinned_directory "$checkpoint_dir" "checkpoint directory after archive" "$checkpoint_dir_device" "$checkpoint_dir_inode" "$checkpoint_dir_nlink" || fail "checkpoint directory changed while checkpoint was created"; validate_tar_members "$archive_tmp" || fail "new checkpoint archive contains unsafe members"; move_exclusive_file "$archive_tmp" "$archive_path" || fail "could not install checkpoint archive exclusively"
  checksum_tmp="$pin_dir/data.tar.sha256.tmp"; create_exclusive_file "$checksum_tmp" || fail "could not create exclusive checksum staging file"; sha256sum -- "$archive_path" > "$checksum_tmp"; move_exclusive_file "$checksum_tmp" "$archive_path.sha256" || fail "could not install checkpoint checksum exclusively"; sha256sum --check "$archive_path.sha256" >/dev/null
  archive_device="$(stat_device "$archive_path")"; archive_inode="$(stat_inode "$archive_path")"; archive_nlink="$(stat_nlink "$archive_path")"; archive_hash="$(sha256_file "$archive_path")"; checksum_device="$(stat_device "$archive_path.sha256")"; checksum_inode="$(stat_inode "$archive_path.sha256")"; checksum_nlink="$(stat_nlink "$archive_path.sha256")"; checksum_hash="$(sha256_file "$archive_path.sha256")"; checkpoint_ready=1
  create_disconnected_container "$target_image" "$target_image_id" "candidate"; state="candidate-isolated"; docker start "$candidate_id" >/dev/null; validate_container_user "$candidate_id" "candidate container"; wait_for_health "$candidate_id" || fail "candidate did not become healthy while isolated"; jq -e 'length == 0' <<< "$(container_networks "$candidate_id")" >/dev/null || fail "candidate was not fully isolated before reconnect"
  validate_pinned_directory "$pin_dir" "private pin directory" "$pin_device" "$pin_inode" "$pin_nlink"; validate_pinned_directory "$project_dir" "Compose project directory" "$project_dir_device" "$project_dir_inode" "$project_dir_nlink"; validate_pinned_file "$compose_file" "Compose file" "$compose_device" "$compose_inode" "$compose_nlink" "$compose_hash"; validate_pinned_file "$compose_file_pin" "pinned Compose definition" "$compose_pin_device" "$compose_pin_inode" "$compose_pin_nlink" "$compose_pin_hash"; validate_pinned_file "$env_file" "update env file" "$env_device" "$env_inode" "$env_nlink" "$env_hash"; validate_pinned_file "$env_file_pin" "pinned update env file" "$env_pin_device" "$env_pin_inode" "$env_pin_nlink" "$env_pin_hash"; validate_pinned_file "$attestation_file" "update attestation" "$attestation_device" "$attestation_inode" "$attestation_nlink" "$attestation_hash" root; validate_pinned_file "$attestation_pin" "pinned update attestation" "$attestation_pin_device" "$attestation_pin_inode" "$attestation_pin_nlink" "$attestation_pin_hash"; validate_pinned_file "$compose_snapshot" "Compose config snapshot" "$compose_snapshot_device" "$compose_snapshot_inode" "$compose_snapshot_nlink" "$compose_snapshot_hash"
  updated_env_tmp="$pin_dir/environment.updated"; create_exclusive_file "$updated_env_tmp" || fail "could not create exclusive updated env file"; image_line_count="$(grep -Ec '^[[:space:]]*(export[[:space:]]+)?OMNI_IMAGE=' "$env_file" || true)"; (( image_line_count <= 1 )) || fail "update env file contains duplicate OMNI_IMAGE entries"; if (( image_line_count == 1 )); then sed -E "s#^[[:space:]]*(export[[:space:]]+)?OMNI_IMAGE=.*#OMNI_IMAGE=$target_image#" "$env_file" > "$updated_env_tmp"; else cp -p -- "$env_file" "$updated_env_tmp"; printf '\nOMNI_IMAGE=%s\n' "$target_image" >> "$updated_env_tmp"; fi; chmod "$(stat_mode "$env_file")" "$updated_env_tmp"; validate_existing_file "$updated_env_tmp" "updated env staging file"; mv -- "$updated_env_tmp" "$env_file"; validate_existing_file "$env_file" "updated env file"; updated_env_device="$(stat_device "$env_file")"; updated_env_inode="$(stat_inode "$env_file")"; updated_env_nlink="$(stat_nlink "$env_file")"; updated_env_hash="$(sha256_file "$env_file")"; env_updated=1
  docker network connect --ip "$network_ip" "$network_name" "$candidate_id" >/dev/null; [ "$(validate_single_network_ip "$candidate_id")" = "$network_ip" ] || fail "candidate was reconnected with an unexpected IP"; validate_container_config "$candidate_id" "$target_image_id" running "candidate"; [ "$(container_health "$candidate_id")" = "healthy" ] || fail "candidate lost health while reconnecting ingress"
  update_verified=1; rollback_armed=0; state="verified"; printf 'safe-update: update succeeded with %s\n' "$target_image"; printf 'safe-update: retained checkpoint: %s\n' "$checkpoint_dir"; printf 'safe-update: retained rollback image: %s\n' "$rollback_image"
}

if [[ "${BASH_SOURCE[0]:-$0}" == "$0" ]]; then safe_update_main "$@"; fi
