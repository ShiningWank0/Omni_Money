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
runtime_contract_file=""
runtime_contract_hash=""
secret_contract_file=""
secret_contract_hash=""
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
archive_tmp=""
checksum_tmp=""
archive_device=""
archive_inode=""
archive_nlink=""
archive_hash=""
checksum_device=""
checksum_inode=""
checksum_nlink=""
checksum_hash=""
capacity_reservation=""
capacity_reservation_kb=""
capacity_reservation_device=""
capacity_reservation_inode=""
capacity_reservation_nlink=""
capacity_reservation_hash=""
rollback_image=""
data_dir=""
data_uid="10001"
data_gid="10001"
network_name=""
network_ip=""
checkpoint_root=""
checkpoint_root_created=0
checkpoint_root_journal=""
checkpoint_journal_phase=""
recovery_bundle_dir=""
recovery_env_file=""
recovery_attestation_file=""
recovery_compose_file=""
recovery_snapshot_file=""
recovery_runtime_file=""
recovery_secret_file=""
recovery_rollback_file=""
recovery_manifest_file=""
recovery_bundle_device=""
recovery_bundle_inode=""
recovery_bundle_nlink=""
recovery_manifest_hash=""
recovery_manifest_device=""
recovery_manifest_inode=""
recovery_manifest_nlink=""
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
env_owner=""
env_group=""
env_mode=""
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
updated_env_tmp=""
restore_env_tmp=""
state="init"
compose_ps_sequence=0
compose_source_changed=0
current_runtime_contract_file=""
current_runtime_contract_hash=""
current_removed_expected=0
tar_path=""
tar_device=""
tar_inode=""
tar_nlink=""
tar_hash=""
secret_sources=()
secret_targets=()
secret_paths=()
secret_devices=()
secret_inodes=()
secret_nlinks=()
secret_hashes=()
secret_owners=()
secret_groups=()
secret_modes=()

fail() {
  printf 'safe-update: %s\n' "$*" >&2
  return 1
}

validate_image_reference() {
  local image="$1" last_component tag
  case "$image" in
    ''|*[!A-Za-z0-9._/@:+-]*) fail "image reference contains unsupported characters"; return 1 ;;
  esac
  case "$image" in
    *@*)
      [[ "$image" =~ ^[^@]+@sha256:[0-9a-fA-F]{64}$ ]] || {
        fail "image digest must be a complete sha256 digest"
        return 1
      }
      ;;
    *)
      last_component="${image##*/}"
      case "$last_component" in
        *:*) tag="${last_component##*:}" ;;
        *) fail "image reference must include an immutable version tag or sha256 digest"; return 1 ;;
      esac
      [ -n "$tag" ] || { fail "image tag must not be empty"; return 1; }
      [ "$tag" != latest ] || { fail "the mutable latest tag is not allowed"; return 1; }
      ;;
  esac
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

fsync_path() {
  sync -f -- "$1"
}

fsync_directory() {
  sync -f -- "$1"
}

directory_size_kb() {
  local path="$1" apparent="${2:-0}" output value
  if [ "$apparent" -eq 1 ]; then
    output="$(du -sk --apparent-size -- "$path")" || return 1
  else
    output="$(du -sk -- "$path")" || return 1
  fi
  value="${output%%[[:space:]]*}"
  case "$value" in ''|*[!0-9]*) return 1 ;; esac
  printf '%s' "$value"
}

free_space_kb() {
  local path="$1" output value
  output="$(df -Pk -- "$path")" || return 1
  value="$(printf '%s\n' "$output" | awk 'NR == 2 {print $4}')"
  case "$value" in ''|*[!0-9]*) return 1 ;; esac
  printf '%s' "$value"
}

reserve_capacity() {
  local reserve_kb="$1" available_after
  capacity_reservation="$checkpoint_dir/.capacity.reserve"
  capacity_reservation_kb="$reserve_kb"
  create_exclusive_file "$capacity_reservation" || return 1
  fallocate -l "${reserve_kb}K" "$capacity_reservation" || {
    rm -f -- "$capacity_reservation"
    return 1
  }
  validate_root_owned_file "$capacity_reservation" "rollback capacity reservation" || return 1
  [ "$(stat_device "$capacity_reservation")" = "$checkpoint_root_device" ] || return 1
  [ "$(stat_nlink "$capacity_reservation")" = 1 ] || return 1
  fsync_path "$capacity_reservation" || return 1
  capacity_reservation_device="$(stat_device "$capacity_reservation")"
  capacity_reservation_inode="$(stat_inode "$capacity_reservation")"
  capacity_reservation_nlink="$(stat_nlink "$capacity_reservation")"
  capacity_reservation_hash="$(sha256_file "$capacity_reservation")"
  available_after="$(free_space_kb "$checkpoint_root")" || return 1
  [ "$available_after" -ge "$reserve_kb" ] || return 1
  fsync_directory "$checkpoint_root" || return 1
}

write_journal() {
  local phase="$1" temporary
  [ -n "$checkpoint_root_journal" ] || { fail "durable update journal path is not initialized"; return 1; }
  if [ -n "$recovery_bundle_dir" ]; then
    validate_pinned_directory "$recovery_bundle_dir" "durable recovery bundle" \
      "$recovery_bundle_device" "$recovery_bundle_inode" "$recovery_bundle_nlink" root || return 1
    validate_pinned_private_file "$recovery_manifest_file" "durable recovery manifest" \
      "$recovery_manifest_device" "$recovery_manifest_inode" "$recovery_manifest_nlink" "$recovery_manifest_hash" root || return 1
  fi
  temporary="$checkpoint_root/.safe-update-journal.tmp.$$"
  create_exclusive_file "$temporary" || { fail "could not create exclusive journal staging file"; return 1; }
  if ! jq -n \
    --arg phase "$phase" --arg state "$state" --arg pid "$$" --arg target "$target_image" \
    --arg current "$current_id" --arg current_image "$current_image_id" --arg rollback "$rollback_image" \
    --arg candidate "$candidate_id" --arg data "$data_dir" --arg checkpoint "$checkpoint_dir" \
    --arg archive "$archive_path" --arg env "${recovery_env_file:-$env_file_pin}" --arg env_hash "$env_hash" \
    --arg compose "${recovery_compose_file:-$compose_file_pin}" --arg compose_hash "$compose_hash" \
    --arg rollback_definition "${recovery_rollback_file:-$rollback_definition}" \
    --arg snapshot "${recovery_snapshot_file:-$compose_snapshot}" --arg snapshot_hash "$compose_snapshot_hash" \
    --arg runtime "${recovery_runtime_file:-$current_runtime_contract_file}" --arg runtime_hash "$current_runtime_contract_hash" \
    --arg secret_contract "${recovery_secret_file:-$secret_contract_file}" --arg secret_hash "$secret_contract_hash" \
    --arg data_device "$data_dir_device" --arg data_inode "$data_dir_inode" --arg data_nlink "$data_dir_nlink" \
    --arg checkpoint_device "$checkpoint_root_device" --arg checkpoint_inode "$checkpoint_root_inode" --arg checkpoint_nlink "$checkpoint_root_nlink" \
    --arg env_device "$env_device" --arg env_inode "$env_inode" --arg env_nlink "$env_nlink" \
    --arg attestation "$attestation_file" --arg attestation_hash "$attestation_hash" \
    --arg network "$network_name" --arg ip "$network_ip" \
    --arg recovery_bundle "$recovery_bundle_dir" --arg recovery_device "$recovery_bundle_device" \
    --arg recovery_inode "$recovery_bundle_inode" --arg recovery_nlink "$recovery_bundle_nlink" \
    --arg recovery_manifest "$recovery_manifest_file" --arg recovery_manifest_hash "$recovery_manifest_hash" \
    --arg recovery_manifest_device "$recovery_manifest_device" --arg recovery_manifest_inode "$recovery_manifest_inode" --arg recovery_manifest_nlink "$recovery_manifest_nlink" \
    --arg archive_device "$archive_device" --arg archive_inode "$archive_inode" --arg archive_nlink "$archive_nlink" --arg archive_hash "$archive_hash" \
    --arg checksum_device "$checksum_device" --arg checksum_inode "$checksum_inode" --arg checksum_nlink "$checksum_nlink" --arg checksum_hash "$checksum_hash" \
    --arg capacity_reservation "$capacity_reservation" --arg capacity_reservation_kb "$capacity_reservation_kb" \
    --arg capacity_device "$capacity_reservation_device" --arg capacity_inode "$capacity_reservation_inode" --arg capacity_nlink "$capacity_reservation_nlink" --arg capacity_hash "$capacity_reservation_hash" \
    '{version:1,phase:$phase,state:$state,pid:($pid|tonumber),target_image:$target,current_id:$current,current_image_id:$current_image,rollback_image:$rollback,candidate_id:$candidate,data_dir:$data,checkpoint_dir:$checkpoint,archive_path:$archive,archive_identity:{device:$archive_device,inode:$archive_inode,nlink:$archive_nlink,sha256:$archive_hash},checksum_identity:{device:$checksum_device,inode:$checksum_inode,nlink:$checksum_nlink,sha256:$checksum_hash},capacity_reservation:$capacity_reservation,capacity_reservation_kb:($capacity_reservation_kb|tonumber?),capacity_reservation_identity:{device:$capacity_device,inode:$capacity_inode,nlink:$capacity_nlink,sha256:$capacity_hash},env_file:$env,env_hash:$env_hash,compose_file:$compose,compose_hash:$compose_hash,rollback_definition:$rollback_definition,compose_snapshot:$snapshot,compose_snapshot_hash:$snapshot_hash,runtime_contract_file:$runtime,runtime_contract_hash:$runtime_hash,secret_contract_file:$secret_contract,secret_contract_hash:$secret_hash,recovery_bundle:$recovery_bundle,recovery_bundle_identity:{device:$recovery_device,inode:$recovery_inode,nlink:$recovery_nlink},recovery_manifest:$recovery_manifest,recovery_manifest_hash:$recovery_manifest_hash,recovery_manifest_identity:{device:$recovery_manifest_device,inode:$recovery_manifest_inode,nlink:$recovery_manifest_nlink},data_identity:{device:$data_device,inode:$data_inode,nlink:$data_nlink},checkpoint_identity:{device:$checkpoint_device,inode:$checkpoint_inode,nlink:$checkpoint_nlink},env_identity:{device:$env_device,inode:$env_inode,nlink:$env_nlink},attestation_file:$attestation,attestation_hash:$attestation_hash,network:$network,network_ip:$ip}' \
    > "$temporary"; then
    rm -f -- "$temporary"
    return 1
  fi
  chmod 0600 "$temporary" || { rm -f -- "$temporary"; return 1; }
  chown 0:0 "$temporary" || { rm -f -- "$temporary"; fail "durable update journal must be root-owned"; return 1; }
  validate_root_owned_file "$temporary" "journal staging file" || { rm -f -- "$temporary"; return 1; }
  fsync_path "$temporary" || { rm -f -- "$temporary"; return 1; }
  mv -f -- "$temporary" "$checkpoint_root_journal" || return 1
  validate_root_owned_file "$checkpoint_root_journal" "durable update journal" || return 1
  fsync_path "$checkpoint_root_journal" || return 1
  fsync_directory "$checkpoint_root" || return 1
  checkpoint_journal_phase="$phase"
}

remove_journal() {
  [ -n "$checkpoint_root_journal" ] || return 0
  [ -e "$checkpoint_root_journal" ] || return 0
  validate_pinned_directory "$checkpoint_root" "checkpoint root" "$checkpoint_root_device" "$checkpoint_root_inode" "$checkpoint_root_nlink" || return 1
  validate_root_owned_file "$checkpoint_root_journal" "durable update journal" || return 1
  rm -f -- "$checkpoint_root_journal" || return 1
  fsync_directory "$checkpoint_root" || return 1
}

prepare_recovery_bundle() {
  local source name destination i
  recovery_bundle_dir="$checkpoint_dir/recovery"
  mkdir -m 0700 -- "$recovery_bundle_dir" || return 1
  chown 0:0 "$recovery_bundle_dir" || return 1
  [ "$(stat_owner "$recovery_bundle_dir")" = 0 ] && [ "$(stat_group "$recovery_bundle_dir")" = 0 ] && [ "$(stat_mode "$recovery_bundle_dir")" = 700 ] || return 1
  recovery_env_file="$recovery_bundle_dir/environment"
  recovery_attestation_file="$recovery_bundle_dir/update-attestation.json"
  recovery_compose_file="$recovery_bundle_dir/compose.yaml"
  recovery_snapshot_file="$recovery_bundle_dir/compose-config.json"
  recovery_runtime_file="$recovery_bundle_dir/runtime-current.json"
  recovery_secret_file="$recovery_bundle_dir/secret-contract"
  for source in "$env_file_pin" "$attestation_pin" "$compose_file_pin" "$compose_snapshot" "$current_runtime_contract_file" "$secret_contract_file"; do
    case "$source" in
      "$env_file_pin") name="$recovery_env_file" ;;
      "$attestation_pin") name="$recovery_attestation_file" ;;
      "$compose_file_pin") name="$recovery_compose_file" ;;
      "$compose_snapshot") name="$recovery_snapshot_file" ;;
      "$current_runtime_contract_file") name="$recovery_runtime_file" ;;
      "$secret_contract_file") name="$recovery_secret_file" ;;
      *) return 1 ;;
    esac
    create_exclusive_file "$name" || return 1
    cp -p -- "$source" "$name" || return 1
    chmod 0400 "$name" || return 1
    chown 0:0 "$name" || return 1
    validate_root_owned_file "$name" "recovery bundle file" || return 1
    fsync_path "$name" || return 1
  done
  recovery_manifest_file="$recovery_bundle_dir/manifest"
  create_exclusive_file "$recovery_manifest_file" || return 1
  # Keep recovery copies self-describing without copying any secret bytes into
  # the journal. Each fixed destination is bound to its device/inode/link-count
  # and digest, so a power-loss replay can verify the bundle before use.
  for destination in "$recovery_env_file" "$recovery_attestation_file" "$recovery_compose_file" "$recovery_snapshot_file" "$recovery_runtime_file" "$recovery_secret_file"; do
    printf '%s\t%s\t%s\t%s\t%s\n' "$destination" "$(stat_device "$destination")" "$(stat_inode "$destination")" "$(stat_nlink "$destination")" "$(sha256_file "$destination")" >> "$recovery_manifest_file" || return 1
  done
  chmod 0400 "$recovery_manifest_file" || return 1
  chown 0:0 "$recovery_manifest_file" || return 1
  validate_root_owned_file "$recovery_manifest_file" "recovery bundle manifest" || return 1
  fsync_path "$recovery_manifest_file" || return 1
  recovery_manifest_device="$(stat_device "$recovery_manifest_file")"
  recovery_manifest_inode="$(stat_inode "$recovery_manifest_file")"
  recovery_manifest_nlink="$(stat_nlink "$recovery_manifest_file")"
  recovery_manifest_hash="$(sha256_file "$recovery_manifest_file")"
  fsync_directory "$recovery_bundle_dir" || return 1
  # Creating recovery/ changes the checkpoint directory link count. Pin the
  # post-creation identity only after this durable parent-directory update;
  # later checks therefore detect replacement without rejecting the expected
  # recovery subdirectory itself.
  fsync_directory "$checkpoint_dir" || return 1
  recovery_bundle_device="$(stat_device "$recovery_bundle_dir")"
  recovery_bundle_inode="$(stat_inode "$recovery_bundle_dir")"
  recovery_bundle_nlink="$(stat_nlink "$recovery_bundle_dir")"
  checkpoint_dir_nlink="$(stat_nlink "$checkpoint_dir")"
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
  owner="$(stat_owner "$path")" || return 1; group="$(stat_group "$path")" || return 1; mode="$(stat_mode "$path")" || return 1
  [ "$owner" = "$data_uid" ] && [ "$group" = "$data_gid" ] && [ "$mode" = "700" ] || {
    fail "$label must be owned by ${data_uid}:${data_gid} with mode 0700: $path"
    return 1
  }
}

validate_data_entry() {
  local path="$1" label="$2" owner group mode links
  owner="$(stat_owner "$path")" || return 1; group="$(stat_group "$path")" || return 1; mode="$(stat_mode "$path")" || return 1
  [ "$owner" = "$data_uid" ] && [ "$group" = "$data_gid" ] || {
    fail "$label must be owned by ${data_uid}:${data_gid}: $path"
    return 1
  }
  case "$mode" in *[!0-7]*|'') return 1 ;; esac
  (( (8#$mode & 18) == 0 )) || { fail "$label is group/other writable: $path"; return 1; }
  if [ -f "$path" ]; then
    links="$(stat_nlink "$path")" || return 1
    [ "$links" = "1" ] || { fail "$label contains a hard-linked regular file: $path"; return 1; }
  fi
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
  owner="$(stat_owner "$path")" || return 1; group="$(stat_group "$path")" || return 1; mode="$(stat_mode "$path")" || return 1
  [ "$owner" = "0" ] || { fail "$label must be root-owned: $path"; return 1; }
  [ "$group" = "0" ] || { fail "$label must have root group ownership: $path"; return 1; }
  case "$mode" in 400|440|444|600) ;; *) fail "$label must be private/read-only: $path"; return 1 ;; esac
}

validate_secret_file() {
  local path="$1" label="$2" owner group mode
  validate_existing_file "$path" "$label" host || return 1
  owner="$(stat_owner "$path")" || return 1; group="$(stat_group "$path")" || return 1; mode="$(stat_mode "$path")" || return 1
  [ "$owner" = 0 ] || { fail "$label must be root-owned: $path"; return 1; }
  # The application runs as 10001:10001. A control key may therefore use the
  # service group for read access; the attestation normally remains root:root.
  [ "$group" = 0 ] || [ "$group" = "$data_gid" ] || { fail "$label has an untrusted group: $path"; return 1; }
  case "$mode" in 400|440|444|600) ;; *) fail "$label must be private/read-only: $path"; return 1 ;; esac
}

validate_secret_contract_permissions() {
  local source="$1" path="$2" owner group mode
  owner="$(stat_owner "$path")" || return 1
  group="$(stat_group "$path")" || return 1
  mode="$(stat_mode "$path")" || return 1
  case "$source" in
    omni_control_database_key)
      [ "$owner" = 0 ] && [ "$group" = "$data_gid" ] && [ "$mode" = 440 ] || {
        fail "control database key must be root:${data_gid} mode 0440"
        return 1
      }
      ;;
    omni_data_at_rest_attestation)
      [ "$owner" = 0 ] && [ "$group" = 0 ] && [ "$mode" = 444 ] || {
        fail "data-at-rest attestation must be root:root mode 0444"
        return 1
      }
      ;;
    *) fail "unexpected Compose secret source: $source"; return 1 ;;
  esac
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
  if [ "$policy" = root ]; then
    # The durable bundle itself is root-owned, but it lives below the
    # operator-owned encrypted checkpoint root. Validate the parent with the
    # host contract and apply the strict root contract only to this directory.
    validate_existing_directory "$path" "$label" host || return 1
    [ "$(stat_owner "$path")" = 0 ] && [ "$(stat_group "$path")" = 0 ] || {
      fail "$label must be root-owned"; return 1;
    }
  else
    validate_existing_directory "$path" "$label" "$policy" || return 1
  fi
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

validate_pinned_private_file() {
  local path="$1" label="$2"
  shift 2
  validate_pinned_file "$path" "$label" "$@" || return 1
  [ "$(stat_mode "$path")" = 400 ] || { fail "$label must remain mode 0400"; return 1; }
}

validate_recovery_bundle() {
  local path device inode nlink hash count=0
  local env_seen=0 attestation_seen=0 compose_seen=0 snapshot_seen=0 runtime_seen=0 secret_seen=0 rollback_seen=0
  [ -n "$recovery_bundle_dir" ] && [ -n "$recovery_manifest_file" ] || return 1
  validate_pinned_directory "$recovery_bundle_dir" "durable recovery bundle" \
    "$recovery_bundle_device" "$recovery_bundle_inode" "$recovery_bundle_nlink" root || return 1
  validate_pinned_private_file "$recovery_manifest_file" "durable recovery manifest" \
    "$recovery_manifest_device" "$recovery_manifest_inode" "$recovery_manifest_nlink" "$recovery_manifest_hash" root || return 1
  while IFS=$'\t' read -r path device inode nlink hash; do
    [ -n "$path" ] && [ -n "$device" ] && [ -n "$inode" ] && [ -n "$nlink" ] && [ -n "$hash" ] || return 1
    case "$path" in
      "$recovery_env_file") [ "$env_seen" -eq 0 ] || return 1; env_seen=1 ;;
      "$recovery_attestation_file") [ "$attestation_seen" -eq 0 ] || return 1; attestation_seen=1 ;;
      "$recovery_compose_file") [ "$compose_seen" -eq 0 ] || return 1; compose_seen=1 ;;
      "$recovery_snapshot_file") [ "$snapshot_seen" -eq 0 ] || return 1; snapshot_seen=1 ;;
      "$recovery_runtime_file") [ "$runtime_seen" -eq 0 ] || return 1; runtime_seen=1 ;;
      "$recovery_secret_file") [ "$secret_seen" -eq 0 ] || return 1; secret_seen=1 ;;
      "$recovery_rollback_file") [ -n "$recovery_rollback_file" ] && [ "$rollback_seen" -eq 0 ] || return 1; rollback_seen=1 ;;
      *) return 1 ;;
    esac
    validate_pinned_private_file "$path" "durable recovery copy" "$device" "$inode" "$nlink" "$hash" root || return 1
    count=$((count + 1))
  done < "$recovery_manifest_file"
  [ "$env_seen" -eq 1 ] && [ "$attestation_seen" -eq 1 ] && [ "$compose_seen" -eq 1 ] &&
    [ "$snapshot_seen" -eq 1 ] && [ "$runtime_seen" -eq 1 ] && [ "$secret_seen" -eq 1 ] || return 1
  if [ "$count" -eq 6 ] && [ "$rollback_seen" -eq 0 ]; then
    return 0
  fi
  [ "$count" -eq 7 ] && [ "$rollback_seen" -eq 1 ]
}

append_recovery_manifest_entry() {
  local destination="$1" temporary
  awk -F '\t' -v path="$destination" '$1 == path {found=1} END {exit found ? 0 : 1}' \
    "$recovery_manifest_file" >/dev/null 2>&1 && return 0
  temporary="$recovery_bundle_dir/.manifest.tmp.$$"
  create_exclusive_file "$temporary" || return 1
  cp -p -- "$recovery_manifest_file" "$temporary" || { rm -f -- "$temporary"; return 1; }
  chmod 0600 "$temporary" || { rm -f -- "$temporary"; return 1; }
  printf '%s\t%s\t%s\t%s\t%s\n' "$destination" "$(stat_device "$destination")" "$(stat_inode "$destination")" "$(stat_nlink "$destination")" "$(sha256_file "$destination")" >> "$temporary" || { rm -f -- "$temporary"; return 1; }
  chmod 0400 "$temporary" && chown 0:0 "$temporary" || { rm -f -- "$temporary"; return 1; }
  validate_root_owned_file "$temporary" "recovery manifest staging file" || { rm -f -- "$temporary"; return 1; }
  [ "$(stat_mode "$temporary")" = 400 ] || { rm -f -- "$temporary"; return 1; }
  fsync_path "$temporary" || { rm -f -- "$temporary"; return 1; }
  mv -f -- "$temporary" "$recovery_manifest_file" || return 1
  fsync_directory "$recovery_bundle_dir" || return 1
  recovery_manifest_device="$(stat_device "$recovery_manifest_file")"
  recovery_manifest_inode="$(stat_inode "$recovery_manifest_file")"
  recovery_manifest_nlink="$(stat_nlink "$recovery_manifest_file")"
  recovery_manifest_hash="$(sha256_file "$recovery_manifest_file")"
}

validate_tar_binary() {
  [ -n "$tar_path" ] || return 0
  validate_pinned_file "$tar_path" "GNU tar executable" "$tar_device" "$tar_inode" "$tar_nlink" "$tar_hash" root
}

clear_compose_environment() {
  unset COMPOSE_FILE COMPOSE_PROJECT_NAME COMPOSE_PROFILES COMPOSE_PARALLEL_LIMIT
  unset COMPOSE_ENV_FILES COMPOSE_PATH_SEPARATOR COMPOSE_IGNORE_ORPHANS COMPOSE_REMOVE_ORPHANS
  unset COMPOSE_SSH_AUTH_SOCK COMPOSE_MENU COMPOSE_EXPERIMENTAL COMPOSE_ANSI
  unset COMPOSE_STATUS_STDOUT COMPOSE_PROGRESS COMPOSE_DRY_RUN COMPOSE_SERVICE COMPOSE_DISABLE_ENV_FILE
  # Compose gives shell variables precedence over --env-file. Remove every
  # repository interpolation variable inherited from the operator shell so
  # the selected, pinned env file is the only configuration source.
  unset OMNI_IMAGE OMNI_DATA_DIR OMNI_AT_REST_ATTESTATION_FILE
  unset OMNI_CONTROL_DB_ENCRYPTION_KEY_FILE OMNI_UPDATE_ATTESTATION_FILE
  unset OMNI_CPU_LIMIT OMNI_MEMORY_LIMIT OMNI_PIDS_LIMIT OMNI_LOG_MAX_SIZE OMNI_LOG_MAX_FILES
  unset OMNI_TMPFS_SIZE OMNI_MONEY_IP OMNI_PANGOLIN_NETWORK OMNI_PANGOLIN_SUBNET
  unset OMNI_UPDATE_ENV_FILE OMNI_UPDATE_CHECKPOINT_DIR
}

validate_local_docker() {
  local context endpoint
  [ -z "${DOCKER_HOST+x}" ] || { fail "DOCKER_HOST must be unset; remote Docker endpoints are not allowed"; return 1; }
  [ -z "${DOCKER_TLS_VERIFY+x}" ] || { fail "DOCKER_TLS_VERIFY must be unset for the local daemon contract"; return 1; }
  [ -z "${DOCKER_CERT_PATH+x}" ] || { fail "DOCKER_CERT_PATH must be unset for the local daemon contract"; return 1; }
  if [ -n "${DOCKER_CONTEXT:-}" ] && [ "${DOCKER_CONTEXT}" != default ]; then
    fail "DOCKER_CONTEXT must select the local default context"
    return 1
  fi
  context="$(docker context show 2>/dev/null)" || { fail "Docker context could not be inspected"; return 1; }
  [ "$context" = default ] || { fail "Docker context is not the local default context"; return 1; }
  endpoint="$(docker context inspect default --format '{{(index .Endpoints "docker").Host}}' 2>/dev/null)" || {
    fail "Docker default context endpoint could not be inspected"
    return 1
  }
  case "$endpoint" in
    unix://*) ;;
    *) fail "Docker default context must use a local Unix socket"; return 1 ;;
  esac
  unset DOCKER_CONTEXT DOCKER_TLS_VERIFY DOCKER_CERT_PATH
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

read_secret_sources() {
  local definitions source target path
  secret_sources=(); secret_targets=(); secret_paths=(); secret_devices=(); secret_inodes=(); secret_nlinks=(); secret_hashes=(); secret_owners=(); secret_groups=(); secret_modes=()
  definitions="$(jq -er '.services["omni-money"].secrets[] | [.source, .target] | @tsv' "$compose_snapshot")" || {
    fail "resolved Compose secret definitions could not be read"
    return 1
  }
  while IFS=$'\t' read -r source target; do
    [ -n "$source" ] && [ -n "$target" ] || { fail "resolved Compose secret has an empty source or target"; return 1; }
    case "$source$target" in *$'\n'*|*$'\r'*|*$'\t'*) fail "resolved Compose secret names contain control characters"; return 1 ;; esac
    case "$source:$target" in
      omni_control_database_key:omni_control_database_key|omni_data_at_rest_attestation:omni_data_at_rest_attestation.json) ;;
      *) fail "resolved Compose secret source/target is not in the production allowlist"; return 1 ;;
    esac
    path="$(jq -er --arg source "$source" '.secrets[$source].file' "$compose_snapshot")" || {
      fail "resolved Compose secret source has no file: $source"
      return 1
    }
    validate_secret_file "$path" "Compose secret $source" || return 1
    validate_secret_contract_permissions "$source" "$path" || return 1
    secret_sources+=("$source"); secret_targets+=("$target"); secret_paths+=("$path")
    secret_devices+=("$(stat_device "$path")"); secret_inodes+=("$(stat_inode "$path")")
    secret_nlinks+=("$(stat_nlink "$path")"); secret_hashes+=("$(sha256_file "$path")")
    secret_owners+=("$(stat_owner "$path")"); secret_groups+=("$(stat_group "$path")"); secret_modes+=("$(stat_mode "$path")")
  done <<< "$definitions"
  [ "${#secret_sources[@]}" -eq 2 ] || { fail "resolved Compose must define exactly two service secrets"; return 1; }
  secret_contract_file="$pin_dir/secret-contract"
  create_exclusive_file "$secret_contract_file" || return 1
  local i
  for i in "${!secret_sources[@]}"; do
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
      "${secret_sources[$i]}" "${secret_targets[$i]}" "${secret_paths[$i]}" \
      "${secret_devices[$i]}" "${secret_inodes[$i]}" "${secret_nlinks[$i]}" "${secret_hashes[$i]}" \
      "${secret_owners[$i]}" "${secret_groups[$i]}" "${secret_modes[$i]}" \
      >> "$secret_contract_file" || return 1
  done
  chmod 0400 "$secret_contract_file" || return 1
  secret_contract_hash="$(sha256_file "$secret_contract_file")" || return 1
}

validate_secret_sources() {
  local i
  [ -n "$secret_contract_file" ] || { fail "secret contract is not initialized"; return 1; }
  [ "$(sha256_file "$secret_contract_file")" = "$secret_contract_hash" ] || {
    fail "pinned secret contract changed"
    return 1
  }
  for i in "${!secret_sources[@]}"; do
    validate_secret_file "${secret_paths[$i]}" "Compose secret ${secret_sources[$i]}" || return 1
    [ "$(stat_owner "${secret_paths[$i]}")" = "${secret_owners[$i]}" ] || return 1
    [ "$(stat_group "${secret_paths[$i]}")" = "${secret_groups[$i]}" ] || return 1
    [ "$(stat_mode "${secret_paths[$i]}")" = "${secret_modes[$i]}" ] || return 1
    validate_pinned_file "${secret_paths[$i]}" "Compose secret ${secret_sources[$i]}" \
      "${secret_devices[$i]}" "${secret_inodes[$i]}" "${secret_nlinks[$i]}" "${secret_hashes[$i]}" host || return 1
  done
}

write_runtime_contract() {
  local id="$1" output="$2" raw env_json operator_environment item name value digest
  raw="$pin_dir/runtime.$$.${id}.raw.json"
  create_exclusive_file "$raw" || return 1
  if ! docker inspect --format '{{json .}}' "$id" > "$raw"; then
    rm -f -- "$raw"
    return 1
  fi
  jq -e 'type == "object"' "$raw" >/dev/null || { rm -f -- "$raw"; return 1; }
  env_json="$(while IFS= read -r -d '' item; do
    name="${item%%=*}"
    value="${item#*=}"
    digest="$(printf '%s' "$value" | sha256sum | sed -E 's/[[:space:]].*$//')"
    jq -cn --arg name "$name" --arg digest "$digest" '{name:$name,sha256:$digest}'
  done < <(jq -j '.Config.Env[]? | (. + "\u0000")' "$raw") | jq -s 'sort_by(.name)')" || return 1
  operator_environment="$(jq -c '
    .services["omni-money"].environment // {} |
    if type == "object" then keys
    elif type == "array" then map(strings | split("=")[0])
    else [] end
  ' "$compose_snapshot")" || return 1
  if ! jq -c --argjson environment "$env_json" --argjson operator_environment "$operator_environment" '
    {
      config: {
        user: (.Config.User // ""),
        entrypoint: (.Config.Entrypoint // null),
        cmd: (.Config.Cmd // null),
        working_dir: (.Config.WorkingDir // ""),
        stop_signal: (.Config.StopSignal // ""),
        healthcheck: (.Config.Healthcheck // null),
        # VERSION and the other runtime defaults are owned by the image and
        # legitimately change on a release. Every other environment value is
        # operator configuration and remains digest-bound to the old runtime.
        image_environment: (["VERSION", "LD_LIBRARY_PATH", "PATH", "HOSTNAME", "HOME"] |
          map(select(. as $name | $operator_environment | index($name) == null))),
        environment: ($environment | map(. as $entry | select(
          (["VERSION", "LD_LIBRARY_PATH", "PATH", "HOSTNAME", "HOME"] | index($entry.name) == null) or
          ($operator_environment | index($entry.name) != null)
        ))),
        # Compose-generated labels include config/image hashes and container
        # numbers, so they are deliberately excluded; application/operator
        # labels remain part of the runtime identity.
        labels: ((.Config.Labels // {}) | with_entries(select(.key | startswith("com.docker.compose.") | not)))
      },
      host: {
        restart_policy: (.HostConfig.RestartPolicy // {}),
        network_mode: (.HostConfig.NetworkMode // ""),
        readonly_rootfs: (.HostConfig.ReadonlyRootfs // false),
        privileged: (.HostConfig.Privileged // false),
        init: (.HostConfig.Init // false),
        cap_drop: (.HostConfig.CapDrop // []),
        cap_add: (.HostConfig.CapAdd // []),
        devices: (.HostConfig.Devices // []),
        pid_mode: (.HostConfig.PidMode // ""),
        ipc_mode: (.HostConfig.IpcMode // ""),
        security_opt: (.HostConfig.SecurityOpt // []),
        resources: {
          nano_cpus: (.HostConfig.NanoCpus // 0),
          memory: (.HostConfig.Memory // 0),
          pids_limit: (.HostConfig.PidsLimit // 0),
          cpu_shares: (.HostConfig.CpuShares // 0),
          cpuset_cpus: (.HostConfig.CpusetCpus // "")
        },
        log_config: (.HostConfig.LogConfig // {}),
        mounts: ((.Mounts // []) | map({Type,Source,Destination,RW,Propagation,Mode,Consistency,TmpfsOptions,VolumeOptions,BindOptions}) | sort_by(.Destination)),
        tmpfs: (.HostConfig.Tmpfs // {})
      },
      networks: ((.NetworkSettings.Networks // {}) | to_entries |
        map({name:.key, aliases:(.value.Aliases // [] | sort), ip:(.value.IPAddress // ""), ipv6:(.value.GlobalIPv6Address // ""), ipam:(.value.IPAMConfig // null)}) |
        sort_by(.name))
    }
  ' "$raw" > "$output"; then
    rm -f -- "$raw"
    return 1
  fi
  rm -f -- "$raw" || return 1
  validate_existing_file "$output" "runtime contract" || return 1
  runtime_contract_file="$output"
  runtime_contract_hash="$(sha256_file "$output")" || return 1
}

validate_runtime_contract() {
  local id label candidate_file candidate_hash
  id="$1"; label="$2"; candidate_file="$pin_dir/runtime.$$.${id}.json"
  write_runtime_contract "$id" "$candidate_file" || { fail "$label runtime contract could not be captured"; return 1; }
  candidate_hash="$runtime_contract_hash"
  [ "$candidate_hash" = "$current_runtime_contract_hash" ] || {
    fail "$label runtime contract differs from the pinned current runtime"
    return 1
  }
}

validate_runtime_safety() {
  local id="$1" label="$2" raw="$pin_dir/runtime-safety.$$.${id}.json"
  create_exclusive_file "$raw" || return 1
  if ! docker inspect --format '{{json .}}' "$id" > "$raw"; then
    rm -f -- "$raw"
    return 1
  fi
  if ! jq -e '
    ((.HostConfig.Privileged // false) == false) and
    ((.HostConfig.CapAdd // []) | length == 0) and
    ((.HostConfig.Devices // []) | length == 0) and
    ((.HostConfig.PidMode // "") == "") and
    ((.HostConfig.IpcMode // "") == "") and
    ((.HostConfig.NanoCpus // 0) > 0) and
    ((.HostConfig.Memory // 0) > 0) and
    ((.HostConfig.PidsLimit // 0) > 0) and
    ((.HostConfig.LogConfig.Type // "") == "json-file") and
    ((.HostConfig.SecurityOpt // []) | sort == ["no-new-privileges:true"]) and
    ((.HostConfig.LogConfig.Config["max-size"] // "") != "") and
    ((.HostConfig.LogConfig.Config["max-file"] // "") != "")
  ' "$raw" >/dev/null; then
    rm -f -- "$raw"
    fail "$label contains unsafe capability, namespace, device, resource, or log configuration"
    return 1
  fi
  rm -f -- "$raw" || return 1
}

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
  validate_runtime_safety "$id" "$label" || return 1
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
  validate_secret_sources || return 1
  if [ "$label" = candidate ] && [ -n "$current_id" ]; then
    # Compose --force-recreate may remove the old service before reporting a
    # failed create. Arm the replacement check before invoking it so that
    # rollback can safely handle that partial lifecycle.
    current_removed_expected=1
  fi
  if ! OMNI_IMAGE="$image" compose up --no-start --no-build --force-recreate "$service_name" >/dev/null; then
    # A real force-recreate can remove the old container and fail before the
    # new one is returned to the caller. Discover only the exact, unique
    # Compose replacement here; an ambiguous or unknown result remains
    # fail-closed and is never stopped by ID guessing.
    if [ "$label" = candidate ] && [ -n "$current_id" ]; then
      candidate_id=""
      if replacement_id="$(compose_single_id "$image" "$label failed Compose ps" 2>/dev/null)"; then
        candidate_id="$replacement_id"
      fi
    fi
    fail "$label Compose up --no-start failed"
    return 1
  fi
  candidate_id="$(compose_single_id "$image" "$label Compose ps")"
  if [ "$label" = candidate ] && [ -n "$current_id" ] && [ "$candidate_id" != "$current_id" ]; then
    # The Compose ps result is the only accepted replacement identity. Record
    # that force-recreate may have removed the old ID before any later
    # candidate validation can fail; rollback can then distinguish this known
    # lifecycle event from an arbitrary inspect disappearance.
    current_removed_expected=1
  fi
  validate_container_config "$candidate_id" "$expected_image" created "$label container" || return 1
  validate_secret_sources || return 1
  if [ "$label" = candidate ] && [ -n "$current_id" ] && [ "$candidate_id" != "$current_id" ]; then
    # Compose --force-recreate is expected to remove the old service
    # container. Its disappearance is accepted only after compose ps --all
    # returned the validated replacement ID; an arbitrary inspect failure is
    # never treated as a missing old container.
    validate_runtime_contract "$candidate_id" "$label container" || return 1
  elif [ "$label" = rollback ]; then
    validate_runtime_contract "$candidate_id" "$label container" || return 1
  fi
  disconnect_all_networks "$candidate_id" || return 1
  [ "$(container_state "$candidate_id")" = "created" ] || { fail "$label was not left stopped after network isolation"; return 1; }
}

replacement_compose_state_is_safe() {
  local output_file="$pin_dir/replacement-ps.$$.txt" id count=0 only=""
  create_exclusive_file "$output_file" || return 1
  if ! OMNI_IMAGE="$target_image" compose ps --all -q "$service_name" > "$output_file"; then
    rm -f -- "$output_file"
    return 1
  fi
  while IFS= read -r id; do
    [ -n "$id" ] || continue
    count=$((count + 1)); only="$id"
  done < "$output_file"
  rm -f -- "$output_file"
  if [ "$count" -eq 0 ] && [ -z "$candidate_id" ]; then return 0; fi
  [ "$count" -eq 1 ] && [ -n "$candidate_id" ] && [ "$only" = "$candidate_id" ]
}

stop_container_safely() {
  local id="$1" label="$2" state_value
  [ -n "$id" ] || return 0
  # A stop can return non-zero after delivering the signal (for example, a
  # timeout or interrupted client). Retry once, then inspect the pinned ID;
  # rollback must not proceed while either the old or candidate container is
  # still running.
  docker stop --time 30 "$id" >/dev/null 2>&1 || true
  if ! state_value="$(container_state "$id" 2>/dev/null)"; then
    if [ "$label" = current ] && [ "$current_removed_expected" -eq 1 ]; then
      replacement_compose_state_is_safe || {
        fail "the expected replaced current container could not be verified"
        return 1
      }
      return 0
    fi
    fail "$label container state could not be verified after stop"
    return 1
  fi
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
  # Compose cannot override an already-resolved `image` field with an env
  # variable. Derive this private, immutable rollback view from the one
  # resolved snapshot; all mounts, networks, ports, and security settings are
  # therefore identical and only the pre-tagged image reference changes.
  if [ -e "$rollback_definition" ] || [ -L "$rollback_definition" ]; then
    validate_existing_file "$rollback_definition" "rollback Compose snapshot" || return 1
  else
    create_exclusive_file "$rollback_definition" || return 1
    if ! jq --arg image "$rollback_image" '.services["omni-money"].image = $image' "$compose_snapshot" > "$rollback_definition"; then
      return 1
    fi
  fi
  validate_existing_file "$rollback_definition" "rollback Compose snapshot" || return 1
  jq -e --arg image "$rollback_image" '.services["omni-money"].image == $image' "$rollback_definition" >/dev/null || return 1
  # Keep the exact rollback view beside the durable journal. The ephemeral
  # pin directory is removed on exit, so a power-loss/manual-recovery path
  # must not depend on it.
  [ -n "$recovery_bundle_dir" ] || return 1
  validate_recovery_bundle || return 1
  recovery_rollback_file="$recovery_bundle_dir/compose-rollback.json"
  if [ -e "$recovery_rollback_file" ] || [ -L "$recovery_rollback_file" ]; then
    validate_pinned_private_file "$recovery_rollback_file" "durable rollback Compose snapshot" \
      "$(stat_device "$recovery_rollback_file")" "$(stat_inode "$recovery_rollback_file")" \
      "$(stat_nlink "$recovery_rollback_file")" "$(sha256_file "$recovery_rollback_file")" root || return 1
    jq -e --arg image "$rollback_image" '.services["omni-money"].image == $image' "$recovery_rollback_file" >/dev/null || return 1
  else
    create_exclusive_file "$recovery_rollback_file" || return 1
    cp -p -- "$rollback_definition" "$recovery_rollback_file" || return 1
    chmod 0400 "$recovery_rollback_file" && chown 0:0 "$recovery_rollback_file" || return 1
  fi
  validate_root_owned_file "$recovery_rollback_file" "durable rollback Compose snapshot" || return 1
  [ "$(stat_mode "$recovery_rollback_file")" = 400 ] || return 1
  fsync_path "$recovery_rollback_file" || return 1
  append_recovery_manifest_entry "$recovery_rollback_file" || return 1
  fsync_directory "$recovery_bundle_dir" || return 1
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
  validate_tar_binary || return 1
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
  grep -q . "$members" || {
    rm -f -- "$members" "$details"
    fail "archive is empty"
    return 1
  }
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
    case "$type" in -|d) ;; *) rm -f -- "$members" "$details"; return 1 ;; esac
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
    validate_data_entry "$entry" "$label entry" || { rm -f -- "$tree"; return 1; }
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
  validate_pinned_file "$capacity_reservation" "rollback capacity reservation" "$capacity_reservation_device" "$capacity_reservation_inode" "$capacity_reservation_nlink" "$capacity_reservation_hash" || return 1
  validate_data_directory "$data_dir" "live data directory" || return 1
  [ ! -e "$failed_data" ] && [ ! -L "$failed_data" ] || return 1
  [ ! -e "$incomplete_restore" ] && [ ! -L "$incomplete_restore" ] || return 1
  sha256sum --check "$archive_path.sha256" >/dev/null || return 1
  validate_tar_members "$archive_path" || { fail "checkpoint archive contains unsafe members"; return 1; }
  write_journal restore-moving || return 1
  fsync_directory "$(dirname -- "$data_dir")" || return 1
  mv -- "$data_dir" "$failed_data" || return 1
  fsync_directory "$(dirname -- "$data_dir")" || return 1
  write_journal restore-data-parked || return 1
  mkdir -m 0700 -- "$data_dir" || return 1
  chown "$data_uid:$data_gid" "$data_dir" || return 1
  validate_data_directory "$data_dir" "restore data directory" || return 1
  fsync_directory "$(dirname -- "$data_dir")" || return 1
  write_journal restore-empty || return 1
  if ! tar --numeric-owner --no-overwrite-dir -xpf "$archive_path" -C "$data_dir"; then
    # Never silently ignore either half of the rollback move. If extraction
    # fails, park the incomplete tree only when that move succeeds, then put
    # the original tree back. A failed move leaves the original path (or the
    # named recovery artifact) for an operator instead of pretending that the
    # live data was restored.
    if [ -e "$data_dir" ] || [ -L "$data_dir" ]; then
      [ ! -e "$incomplete_restore" ] && [ ! -L "$incomplete_restore" ] || return 1
      mv -- "$data_dir" "$incomplete_restore" || return 1
      fsync_directory "$(dirname -- "$data_dir")" || return 1
    fi
    [ ! -e "$data_dir" ] && [ ! -L "$data_dir" ] || return 1
    mv -- "$failed_data" "$data_dir" || return 1
    fsync_directory "$(dirname -- "$data_dir")" || return 1
    return 1
  fi
  sync || return 1
  fsync_directory "$data_dir" || return 1
  write_journal restore-extracted || return 1
  validate_data_directory "$data_dir" "restored data directory" || return 1
  validate_source_tree "$data_dir" "restored data tree" || return 1
  fsync_directory "$(dirname -- "$data_dir")" || return 1
  write_journal restore-complete || return 1
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
  restore_env_tmp="$(dirname -- "$env_file")/.omni-money-env.restore.$$"
  create_exclusive_file "$restore_env_tmp" || return 1
  [ "$(stat_device "$restore_env_tmp")" = "$env_device" ] || return 1
  cp -p -- "$env_file_pin" "$restore_env_tmp" || return 1
  chmod "$env_mode" "$restore_env_tmp" && chown "$env_owner:$env_group" "$restore_env_tmp" || return 1
  fsync_path "$restore_env_tmp" || return 1
  write_journal env-restore-staged || return 1
  mv -- "$restore_env_tmp" "$env_file" || return 1
  restore_env_tmp=""
  fsync_path "$env_file" || return 1
  fsync_directory "$(dirname -- "$env_file")" || return 1
  validate_existing_file "$env_file" "restored update env file" || return 1
  write_journal env-restored || return 1
  env_updated=0
}

cleanup_pre_stop_checkpoint() {
  # A failure before rollback is armed has not stopped or replaced the live
  # service. Remove only the exact, pinned empty checkpoint generation and its
  # prepared journal so a healthy retry is not blocked by stale preflight
  # state. Any identity mismatch leaves artifacts for manual recovery.
  [ "$rollback_armed" -eq 0 ] || return 0
  [ "$checkpoint_ready" -eq 0 ] || return 0
  [ -n "$checkpoint_dir" ] && [ -n "$checkpoint_root_journal" ] || return 0
  validate_pinned_directory "$checkpoint_root" "checkpoint root pre-stop cleanup" \
    "$checkpoint_root_device" "$checkpoint_root_inode" "$checkpoint_root_nlink" || return 0
  validate_pinned_directory "$checkpoint_dir" "checkpoint generation pre-stop cleanup" \
    "$checkpoint_dir_device" "$checkpoint_dir_inode" "$checkpoint_dir_nlink" || return 0
  [ ! -e "$archive_path" ] && [ ! -e "$archive_path.sha256" ] || return 0
  remove_journal || return 0
  rm -rf -- "$checkpoint_dir" || return 0
  fsync_directory "$checkpoint_root" || true
}

cleanup_env_staging() {
  local path
  for path in "$updated_env_tmp" "$restore_env_tmp"; do
    [ -n "$path" ] || continue
    [ -e "$path" ] && [ ! -L "$path" ] || continue
    validate_existing_file "$path" "temporary env staging file" >/dev/null 2>&1 || continue
    rm -f -- "$path" || true
  done
  if [ -n "$env_file" ]; then
    fsync_directory "$(dirname -- "$env_file")" || true
  fi
}

cleanup_private_state() {
  trap - EXIT INT TERM
  cleanup_env_staging
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
  write_journal rollback-start || true
  if [ -n "$candidate_id" ] && ! stop_container_safely "$candidate_id" candidate; then
    printf 'safe-update: candidate could not be safely stopped; service remains stopped\n' >&2
    cleanup_private_state; exit "$original_status"
  fi
  if [ -n "$current_id" ] && ! stop_container_safely "$current_id" current; then
    printf 'safe-update: current container could not be safely stopped; service remains stopped\n' >&2
    cleanup_private_state; exit "$original_status"
  fi
  state="rollback-stopped"
  write_journal rollback-stopped || true
  if ! validate_pinned_directory "$project_dir" "Compose project directory" "$project_dir_device" "$project_dir_inode" "$project_dir_nlink" ||
     ! validate_pinned_file "$attestation_file" "update attestation" "$attestation_device" "$attestation_inode" "$attestation_nlink" "$attestation_hash" root ||
     ! validate_pinned_file "$attestation_pin" "pinned update attestation" "$attestation_pin_device" "$attestation_pin_inode" "$attestation_pin_nlink" "$attestation_pin_hash" ||
     ! validate_pinned_file "$env_file_pin" "pinned update env file" "$env_pin_device" "$env_pin_inode" "$env_pin_nlink" "$env_pin_hash" ||
     ! validate_pinned_file "$compose_snapshot" "Compose config snapshot" "$compose_snapshot_device" "$compose_snapshot_inode" "$compose_snapshot_nlink" "$compose_snapshot_hash" ||
     ! validate_recovery_bundle ||
     ! validate_secret_sources; then
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
  write_journal data-restored || true
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
  write_journal rollback-create || true
  if ! create_disconnected_container "$rollback_image" "$old_image_id" "rollback"; then
    if [ -n "$candidate_id" ]; then stop_container_safely "$candidate_id" rollback >/dev/null 2>&1 || true; fi
    printf 'safe-update: previous container could not be recreated. Checkpoint: %s\n' "$checkpoint_dir" >&2
    cleanup_private_state; exit "$original_status"
  fi
  state="rollback-isolated"
  write_journal rollback-isolated || true
  old_id="$candidate_id"
  if ! docker start "$old_id" >/dev/null || ! validate_container_user "$old_id" "rollback container" || ! wait_for_health "$old_id"; then
    stop_container_safely "$old_id" rollback >/dev/null 2>&1 || true
    printf 'safe-update: previous image did not become healthy after data restore. Service remains isolated. Checkpoint: %s\n' "$checkpoint_dir" >&2
    cleanup_private_state; exit "$original_status"
  fi
  state="rollback-healthy"
  write_journal rollback-healthy || true
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
  write_journal rolled-back || true
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
  if [ "$status" -ne 0 ]; then cleanup_pre_stop_checkpoint; fi
  cleanup_env_staging
  cleanup_private_state
  exit "$status"
}

safe_update_main() {
  set -Eeuo pipefail
  umask 077
  [ -z "${OMNI_UPDATE_CHECKPOINT_DIR+x}" ] || fail "OMNI_UPDATE_CHECKPOINT_DIR is not supported; checkpoint root is attestation-bound"
  validate_local_docker
  clear_compose_environment
  [ -z "${TAR_OPTIONS+x}" ] || fail "TAR_OPTIONS must be unset for the trusted archive contract"
  (( BASH_VERSINFO[0] >= 3 )) || fail "safe-update requires Bash 3.2 or newer"
  for command_name in bash docker jq sha256sum tar stat awk sed grep mktemp du df chown env find uname id dirname pwd cp chmod mkdir rmdir rm mv date sleep sync fallocate; do command -v "$command_name" >/dev/null 2>&1 || fail "required command is missing: $command_name"; done
  [ "$(uname -s)" = "Linux" ] || fail "safe-update requires the Linux production host contract"
  tar_path="$(type -P tar)"; case "$tar_path" in /*) ;; *) fail "GNU tar path must be absolute" ;; esac
  validate_existing_file "$tar_path" "GNU tar executable" root
  tar_device="$(stat_device "$tar_path")"; tar_inode="$(stat_inode "$tar_path")"; tar_nlink="$(stat_nlink "$tar_path")"; tar_hash="$(sha256_file "$tar_path")"
  tar --version 2>/dev/null | grep -q 'GNU tar' || fail "safe-update requires GNU tar"
  [ -f "$compose_file" ] && [ ! -L "$compose_file" ] || fail "Compose file must be a regular non-symlink file: $compose_file"
  validate_existing_file "$compose_file" "Compose file"
  compose_device="$(stat_device "$compose_file")"; compose_inode="$(stat_inode "$compose_file")"; compose_nlink="$(stat_nlink "$compose_file")"; compose_hash="$(sha256_file "$compose_file")"
  env_file="$(resolve_config_path "$env_file_input")"
  validate_existing_file "$env_file" "update env file"
  env_device="$(stat_device "$env_file")"; env_inode="$(stat_inode "$env_file")"; env_nlink="$(stat_nlink "$env_file")"; env_hash="$(sha256_file "$env_file")"; env_owner="$(stat_owner "$env_file")"; env_group="$(stat_group "$env_file")"; env_mode="$(stat_mode "$env_file")"
  case "$health_timeout" in ''|*[!0-9]*) fail "OMNI_UPDATE_HEALTH_TIMEOUT_SECONDS must be an integer" ;; esac
  (( health_timeout >= 30 && health_timeout <= 600 )) || fail "health timeout must be between 30 and 600 seconds"
  [ -n "$target_image" ] || fail "usage: scripts/safe-update.sh <pinned-image:version-or-digest>"
  validate_image_reference "$target_image"
  if [ -e "$lock_dir" ] || [ -L "$lock_dir" ]; then
    [ ! -L "$lock_dir" ] || fail "safe-update lock is a symlink; manual recovery is required"
    validate_existing_directory "$lock_dir" "existing safe-update lock"
    fail "safe-update lock already exists; inspect the durable journal and perform manual recovery before retrying"
  fi
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
    (.name == "omni-money") and
    .services["omni-money"] as $s |
    ($s.image == $image) and ($s.container_name == "omni-money") and
    ($s.read_only == true) and (($s.cap_drop // []) | index("ALL") != null) and
    (($s.cap_drop // []) | sort == ["ALL"]) and (($s.privileged // false) == false) and
    (($s.cap_add // []) | length == 0) and (($s.devices // []) | length == 0) and
    (($s.security_opt // []) | sort == ["no-new-privileges:true"]) and
    (($s.cpus // 0) | tonumber > 0) and (($s.mem_limit // "") | tostring | length > 0) and
    (($s.pids_limit // 0) | tonumber > 0) and
    ($s.logging.driver == "json-file") and
    (($s.logging.options["max-size"] // "") | tostring | length > 0) and
    (($s.logging.options["max-file"] // "") | tostring | length > 0) and
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
  read_secret_sources || fail "resolved Compose secret sources violate the host contract"
  # From this point every Compose call, including ps/up during rollback, uses
  # the one resolved snapshot. The source YAML remains pinned and is checked
  # for replacement, but is never re-parsed after this binding point.
  compose_definition="$compose_snapshot"
  configured_data_dir="$(jq -er '.services["omni-money"].volumes[] | select(.target == "/app/data") | .source' "$compose_snapshot")"
  attestation_file="$(jq -er '.["x-omni-update-attestation-file"]' "$compose_snapshot")"
  attestation_file="$(resolve_config_path "$attestation_file")"
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
  validate_secret_sources
  current_runtime_contract_file="$pin_dir/runtime-current.json"
  write_runtime_contract "$current_id" "$current_runtime_contract_file"
  current_runtime_contract_hash="$runtime_contract_hash"
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
  [ "$(stat_owner "$checkpoint_root")" = 0 ] && [ "$(stat_group "$checkpoint_root")" = 0 ] || fail "checkpoint root must be root-owned"
  [ "$(stat_mode "$checkpoint_root")" = 700 ] || fail "checkpoint root must have mode 0700"
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
  checkpoint_root_journal="$checkpoint_root/.safe-update-journal"
  if [ -e "$checkpoint_root_journal" ] || [ -L "$checkpoint_root_journal" ]; then
    [ ! -L "$checkpoint_root_journal" ] || fail "durable update journal is a symlink; manual recovery is required"
    validate_root_owned_file "$checkpoint_root_journal" "existing durable update journal"
    jq -e 'type == "object" and .version == 1 and (.phase | IN(
      "prepared", "capacity-reserved", "images-pinned", "stopping", "stopped",
      "checkpoint-start", "checkpoint-archived", "checkpoint-durable",
      "candidate-isolated", "candidate-healthy", "env-install", "env-staged",
      "env-installed", "network-connect", "network-connected", "committed",
      "rollback-start", "rollback-stopped", "restore-moving", "restore-data-parked",
      "restore-empty", "restore-extracted", "restore-complete", "data-restored",
      "env-restore-staged", "env-restored", "rollback-create", "rollback-isolated",
      "rollback-healthy", "rolled-back"))' "$checkpoint_root_journal" >/dev/null || fail "durable update journal has an invalid recovery phase; manual recovery is required"
    fail "durable update journal exists; inspect and complete its recorded recovery before retrying"
  fi
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
  prepare_recovery_bundle || fail "could not create durable root-owned recovery bundle"
  write_journal prepared
  data_allocated_kb="$(directory_size_kb "$data_dir")" || fail "allocated data size could not be measured safely"; data_logical_kb="$(directory_size_kb "$data_dir" 1)" || fail "logical data size could not be measured safely"; (( data_logical_kb > data_allocated_kb )) && data_kb="$data_logical_kb" || data_kb="$data_allocated_kb"; available_kb="$(free_space_kb "$checkpoint_root")" || fail "checkpoint free space could not be measured safely"; required_kb=$((data_kb * 4 + 262144)); (( available_kb >= required_kb * 2 )) || fail "insufficient free space: rollback-safe update needs at least $((required_kb * 2)) KiB free for operation and reservation"; reserve_capacity "$required_kb" || fail "could not reserve rollback capacity"; write_journal capacity-reserved
  current_image="$(docker inspect --format '{{.Config.Image}}' "$current_id")"; rollback_image="omni-money:rollback-$timestamp"; docker pull "$target_image" >/dev/null; target_image_id="$(docker image inspect --format '{{.Id}}' "$target_image")"; docker tag "$current_image_id" "$rollback_image"; prepare_rollback_definition || fail "could not persist the durable rollback Compose snapshot"; write_journal images-pinned
  # Nothing that can mutate the live deployment happens before this point.
  # Arm the EXIT state machine immediately before the direct stop of the
  # pinned current container so a signal/partial stop cannot leave it without
  # an automatic recovery attempt.
  write_journal stopping
  rollback_armed=1; state="armed"; docker stop --time 30 "$current_id" >/dev/null
  [ "$(container_state "$current_id")" != "running" ] || fail "pinned current container remained running after stop"
  state="stopped"; write_journal stopped
  validate_pinned_directory "$data_dir" "live data directory" "$data_dir_device" "$data_dir_inode" "$data_dir_nlink" data; validate_source_tree "$data_dir" "live data tree"; validate_pinned_directory "$checkpoint_root" "checkpoint root" "$checkpoint_root_device" "$checkpoint_root_inode" "$checkpoint_root_nlink"
  write_journal checkpoint-start
  archive_tmp="$checkpoint_dir/.data.tar.tmp.$$"; create_exclusive_file "$archive_tmp" || fail "could not create exclusive archive staging file"; [ "$(stat_device "$archive_tmp")" = "$checkpoint_root_device" ] || fail "archive staging file is not on the attested checkpoint filesystem"; validate_tar_binary; tar --numeric-owner -cpf "$archive_tmp" -C "$data_dir" .; fsync_path "$archive_tmp"; validate_pinned_directory "$data_dir" "live data directory after archive" "$data_dir_device" "$data_dir_inode" "$data_dir_nlink" data || fail "live data directory changed while checkpoint was created"; validate_pinned_directory "$checkpoint_root" "checkpoint root after archive" "$checkpoint_root_device" "$checkpoint_root_inode" "$checkpoint_root_nlink" || fail "checkpoint root changed while checkpoint was created"; validate_pinned_directory "$checkpoint_dir" "checkpoint directory after archive" "$checkpoint_dir_device" "$checkpoint_dir_inode" "$checkpoint_dir_nlink" || fail "checkpoint directory changed while checkpoint was created"; validate_tar_members "$archive_tmp" || fail "new checkpoint archive contains unsafe members"; move_exclusive_file "$archive_tmp" "$archive_path" || fail "could not install checkpoint archive exclusively"; archive_device="$(stat_device "$archive_path")"; archive_inode="$(stat_inode "$archive_path")"; archive_nlink="$(stat_nlink "$archive_path")"; archive_hash="$(sha256_file "$archive_path")"; fsync_path "$archive_path"; fsync_directory "$checkpoint_dir"; write_journal checkpoint-archived
  checksum_tmp="$checkpoint_dir/.data.tar.sha256.tmp.$$"; create_exclusive_file "$checksum_tmp" || fail "could not create exclusive checksum staging file"; [ "$(stat_device "$checksum_tmp")" = "$checkpoint_root_device" ] || fail "checksum staging file is not on the attested checkpoint filesystem"; sha256sum -- "$archive_path" > "$checksum_tmp"; fsync_path "$checksum_tmp"; move_exclusive_file "$checksum_tmp" "$archive_path.sha256" || fail "could not install checkpoint checksum exclusively"; fsync_path "$archive_path.sha256"; fsync_directory "$checkpoint_dir"; sha256sum --check "$archive_path.sha256" >/dev/null
  archive_device="$(stat_device "$archive_path")"; archive_inode="$(stat_inode "$archive_path")"; archive_nlink="$(stat_nlink "$archive_path")"; archive_hash="$(sha256_file "$archive_path")"; checksum_device="$(stat_device "$archive_path.sha256")"; checksum_inode="$(stat_inode "$archive_path.sha256")"; checksum_nlink="$(stat_nlink "$archive_path.sha256")"; checksum_hash="$(sha256_file "$archive_path.sha256")"; checkpoint_ready=1
  write_journal checkpoint-durable
  create_disconnected_container "$target_image" "$target_image_id" "candidate"; state="candidate-isolated"; write_journal candidate-isolated; docker start "$candidate_id" >/dev/null; validate_container_user "$candidate_id" "candidate container"; wait_for_health "$candidate_id" || fail "candidate did not become healthy while isolated"; jq -e 'length == 0' <<< "$(container_networks "$candidate_id")" >/dev/null || fail "candidate was not fully isolated before reconnect"; write_journal candidate-healthy
  validate_pinned_directory "$pin_dir" "private pin directory" "$pin_device" "$pin_inode" "$pin_nlink"; validate_pinned_directory "$project_dir" "Compose project directory" "$project_dir_device" "$project_dir_inode" "$project_dir_nlink"; validate_pinned_file "$compose_file" "Compose file" "$compose_device" "$compose_inode" "$compose_nlink" "$compose_hash"; validate_pinned_file "$compose_file_pin" "pinned Compose definition" "$compose_pin_device" "$compose_pin_inode" "$compose_pin_nlink" "$compose_pin_hash"; validate_pinned_file "$env_file" "update env file" "$env_device" "$env_inode" "$env_nlink" "$env_hash"; validate_pinned_file "$env_file_pin" "pinned update env file" "$env_pin_device" "$env_pin_inode" "$env_pin_nlink" "$env_pin_hash"; validate_pinned_file "$attestation_file" "update attestation" "$attestation_device" "$attestation_inode" "$attestation_nlink" "$attestation_hash" root; validate_pinned_file "$attestation_pin" "pinned update attestation" "$attestation_pin_device" "$attestation_pin_inode" "$attestation_pin_nlink" "$attestation_pin_hash"; validate_pinned_file "$compose_snapshot" "Compose config snapshot" "$compose_snapshot_device" "$compose_snapshot_inode" "$compose_snapshot_nlink" "$compose_snapshot_hash"; validate_secret_sources
  write_journal env-install
  updated_env_tmp="$(dirname -- "$env_file")/.omni-money-env.tmp.$$"; create_exclusive_file "$updated_env_tmp" || fail "could not create exclusive updated env file"; [ "$(stat_device "$updated_env_tmp")" = "$env_device" ] || fail "updated env staging file is not on the env filesystem"; image_line_count="$(grep -Ec '^[[:space:]]*(export[[:space:]]+)?OMNI_IMAGE=' "$env_file" || true)"; (( image_line_count <= 1 )) || fail "update env file contains duplicate OMNI_IMAGE entries"; if (( image_line_count == 1 )); then sed -E "s#^[[:space:]]*(export[[:space:]]+)?OMNI_IMAGE=.*#OMNI_IMAGE=$target_image#" "$env_file" > "$updated_env_tmp"; else cp -p -- "$env_file" "$updated_env_tmp"; printf '\nOMNI_IMAGE=%s\n' "$target_image" >> "$updated_env_tmp"; fi; chmod "$env_mode" "$updated_env_tmp"; chown "$env_owner:$env_group" "$updated_env_tmp" || fail "updated env staging ownership could not be preserved"; validate_existing_file "$updated_env_tmp" "updated env staging file"; fsync_path "$updated_env_tmp"; write_journal env-staged; mv -- "$updated_env_tmp" "$env_file"; updated_env_tmp=""; fsync_path "$env_file"; fsync_directory "$(dirname -- "$env_file")"; validate_existing_file "$env_file" "updated env file"; updated_env_device="$(stat_device "$env_file")"; updated_env_inode="$(stat_inode "$env_file")"; updated_env_nlink="$(stat_nlink "$env_file")"; updated_env_hash="$(sha256_file "$env_file")"; env_updated=1; write_journal env-installed
  write_journal network-connect
  docker network connect --ip "$network_ip" "$network_name" "$candidate_id" >/dev/null; [ "$(validate_single_network_ip "$candidate_id")" = "$network_ip" ] || fail "candidate was reconnected with an unexpected IP"; validate_container_config "$candidate_id" "$target_image_id" running "candidate"; [ "$(container_health "$candidate_id")" = "healthy" ] || fail "candidate lost health while reconnecting ingress"; validate_secret_sources; write_journal network-connected
  write_journal committed; remove_journal; update_verified=1; rollback_armed=0; state="verified"; printf 'safe-update: update succeeded with %s\n' "$target_image"; printf 'safe-update: retained checkpoint: %s\n' "$checkpoint_dir"; printf 'safe-update: retained rollback image: %s\n' "$rollback_image"
}

if [[ "${BASH_SOURCE[0]:-$0}" == "$0" ]]; then safe_update_main "$@"; fi
