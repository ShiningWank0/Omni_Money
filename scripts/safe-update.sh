#!/bin/bash -p

# This must be the first executable statement.  An inherited xtrace shell can
# otherwise print the target image or raw runtime environment before the
# updater has a chance to establish its logging boundary.
case "$-" in
  *x*) builtin set +x; builtin printf 'safe-update: inherited xtrace is not allowed\n' >&2; builtin exit 77 ;;
esac

# This root entry point is supported only through its privileged Bash shebang.
# Privileged mode is established by the kernel before Bash startup, so BASH_ENV
# and exported shell functions cannot execute before this file's first line.
# A non-privileged `bash safe-update.sh` has already crossed that unsafe startup
# boundary, while sourcing would inherit the caller's shell state. Fail closed
# in both cases without invoking any external command.
# Direct-execution boundary begins.
if [[ "${BASH_SOURCE[0]:-$0}" != "$0" ]]; then
  builtin printf 'safe-update: sourcing is not supported; execute the script directly\n' >&2
  return 77 2>/dev/null || builtin exit 77
fi
case "$-" in
  *p*) ;;
  *) builtin printf 'safe-update: execute the script directly; privileged Bash startup is required\n' >&2; builtin exit 77 ;;
esac
# Direct-execution boundary ends.
builtin set -Eeuo pipefail

# Deploy one pinned Omni Money image with an offline checkpoint. This entry
# point treats Compose files and paths as data, never as shell code. It is
# written for Bash and the GNU userland used by the production Linux host; the
# stat compatibility layer also keeps preflight tests useful on macOS.

script_source="${BASH_SOURCE[0]:-$0}"
trusted_path="/usr/sbin:/usr/bin:/sbin:/bin"
PATH="$trusted_path"
builtin export PATH
# Exported Bash functions otherwise take precedence over the fixed PATH.
# Remove every external command name before dirname/id are first used; the
# later toolchain attestation then pins their canonical root-owned files.
builtin unset -f bash docker jq sha256sum tar stat awk sed grep mktemp du df chown find findmnt \
  uname id dirname pwd printf type cp chmod mkdir rmdir rm mv date sleep sync fallocate readlink tr 2>/dev/null || true
builtin hash -r

service_name="omni-money"
project_name="omni-money"
target_image="${1:-}"
case "$script_source" in */*) script_parent="${script_source%/*}" ;; *) script_parent=. ;; esac
script_dir="$(builtin cd -- "$script_parent" && builtin pwd -P)"
project_dir="$(builtin cd -- "$script_dir/.." && builtin pwd -P)"
compose_file="$project_dir/compose.yaml"
compose_definition="$compose_file"
compose_file_pin=""
current_uid="$EUID"
env_file_input="${OMNI_UPDATE_ENV_FILE:-.env}"
env_file=""
env_file_pin=""
attestation_file=""
attestation_pin=""
compose_snapshot=""
compose_snapshot_hash=""
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
current_project="$project_name"
legacy_project_name=""
legacy_current=0
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
capacity_reservation_size=""
capacity_reservation_released=0
rollback_image=""
data_dir=""
data_uid="10001"
data_gid="10001"
network_name=""
network_ip=""
network_id=""
network_driver=""
network_internal=""
network_ipam_driver=""
network_subnet=""
network_contract_file=""
network_contract_hash=""
network_contract_device=""
network_contract_inode=""
network_contract_nlink=""
candidate_ingress_state="never"
checkpoint_root=""
checkpoint_root_created=0
checkpoint_root_journal=""
recovery_bundle_dir=""
recovery_env_file=""
recovery_attestation_file=""
recovery_compose_file=""
recovery_snapshot_file=""
recovery_runtime_file=""
recovery_secret_file=""
recovery_rollback_file=""
recovery_network_file=""
recovery_network_device=""
recovery_network_inode=""
recovery_network_nlink=""
recovery_network_hash=""
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
active_data_device=""
active_data_inode=""
active_data_nlink=""
active_data_location=""
failed_candidate_data=""
failed_candidate_data_device=""
failed_candidate_data_inode=""
failed_candidate_data_nlink=""
incomplete_restore_data=""
incomplete_restore_data_device=""
incomplete_restore_data_inode=""
incomplete_restore_data_nlink=""
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
recovery_env_device=""
recovery_env_inode=""
recovery_env_nlink=""
recovery_env_hash=""
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
active_env_device=""
active_env_inode=""
active_env_nlink=""
active_env_hash=""
updated_env_tmp=""
restore_env_tmp=""
state="init"
compose_source_changed=0
current_runtime_contract_file=""
current_runtime_contract_hash=""
current_removed_expected=0
tar_path=""
tar_device=""
tar_inode=""
tar_nlink=""
tar_hash=""
docker_path=""
docker_device=""
docker_inode=""
docker_nlink=""
docker_hash=""
docker_socket_path=""
docker_socket_device=""
docker_socket_inode=""
docker_socket_nlink=""
docker_socket_owner=""
docker_socket_group=""
docker_socket_mode=""
docker_socket_ready=0
docker_config_dir=""
docker_config_device=""
docker_config_inode=""
docker_config_nlink=""
compose_plugin_path=""
compose_plugin_device=""
compose_plugin_inode=""
compose_plugin_nlink=""
compose_plugin_hash=""
tool_names=()
tool_paths=()
tool_devices=()
tool_inodes=()
tool_nlinks=()
tool_hashes=()
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

stat_size() {
  local path="$1" value
  if value="$(stat -c '%s' -- "$path" 2>/dev/null)"; then printf '%s' "$value"; else stat -f '%z' "$path"; fi
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
  local reserve_kb="$1" available_after allocated_kb expected_bytes
  capacity_reservation="$checkpoint_dir/.capacity.reserve"
  capacity_reservation_kb="$reserve_kb"
  create_exclusive_file "$capacity_reservation" || return 1
  fallocate -l "${reserve_kb}K" "$capacity_reservation" || {
    rm -f -- "$capacity_reservation"
    return 1
  }
  validate_root_owned_file "$capacity_reservation" "rollback capacity reservation" || { fail "capacity reservation owner/mode validation failed"; return 1; }
  [ "$(stat_device "$capacity_reservation")" = "$checkpoint_root_device" ] || { fail "capacity reservation is not on checkpoint filesystem"; return 1; }
  [ "$(stat_nlink "$capacity_reservation")" = 1 ] || { fail "capacity reservation link count is not one"; return 1; }
  fsync_path "$capacity_reservation" || { fail "capacity reservation fsync failed"; return 1; }
  capacity_reservation_device="$(stat_device "$capacity_reservation")"
  capacity_reservation_inode="$(stat_inode "$capacity_reservation")"
  capacity_reservation_nlink="$(stat_nlink "$capacity_reservation")"
  # Do not hash the reserve: it can be hundreds of GiB and its bytes have no
  # recovery value.  Bind its identity and exact length, and prove that the
  # filesystem actually allocated the requested blocks instead.
  expected_bytes=$((reserve_kb * 1024))
  capacity_reservation_size="$(stat_size "$capacity_reservation")" || return 1
  [ "$capacity_reservation_size" -eq "$expected_bytes" ] || { fail "capacity reservation size is not exact"; return 1; }
  allocated_kb="$(directory_size_kb "$capacity_reservation")" || return 1
  [ "$allocated_kb" -ge "$reserve_kb" ] || { fail "capacity reservation is sparse or incompletely allocated"; return 1; }
  available_after="$(free_space_kb "$checkpoint_root")" || { fail "free space could not be rechecked after reservation"; return 1; }
  [ "$available_after" -ge "$reserve_kb" ] || { fail "capacity reservation exceeds remaining free space"; return 1; }
  fsync_directory "$checkpoint_root" || { fail "checkpoint root fsync failed after capacity reservation"; return 1; }
}

release_capacity_reservation() {
  [ "$capacity_reservation_released" -eq 1 ] && return 0
  validate_capacity_reservation || return 1
  state="reservation-release-prepared"
  write_journal reservation-release-prepared || return 1
  rm -f -- "$capacity_reservation" || return 1
  # Mark the reservation consumed before extraction. If the following fsync or
  # journal write fails, the updater remains stopped with a durable phase that
  # tells recovery the reservation is no longer available.
  capacity_reservation_released=1
  fsync_directory "$checkpoint_dir" || return 1
  state="reservation-released"
  write_journal reservation-released || return 1
}

validate_capacity_reservation() {
  validate_existing_file "$capacity_reservation" "rollback capacity reservation" root || return 1
  [ "$(stat_device "$capacity_reservation")" = "$capacity_reservation_device" ] || return 1
  [ "$(stat_inode "$capacity_reservation")" = "$capacity_reservation_inode" ] || return 1
  [ "$(stat_nlink "$capacity_reservation")" = "$capacity_reservation_nlink" ] || return 1
  [ "$(stat_size "$capacity_reservation")" = "$capacity_reservation_size" ] || return 1
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
    --arg current "$current_id" --arg current_project "$current_project" --arg current_image "$current_image_id" --arg rollback "$rollback_image" \
    --arg candidate "$candidate_id" --arg data "$data_dir" --arg checkpoint "$checkpoint_dir" \
    --arg archive "$archive_path" --arg env "$env_file" --arg env_hash "$env_hash" \
    --arg recovery_env_file "$recovery_env_file" \
    --arg compose "$compose_file" --arg compose_hash "$compose_hash" \
    --arg rollback_definition "${recovery_rollback_file:-$rollback_definition}" \
    --arg snapshot "${recovery_snapshot_file:-$compose_snapshot}" --arg snapshot_hash "$compose_snapshot_hash" \
    --arg runtime "${recovery_runtime_file:-$current_runtime_contract_file}" --arg runtime_hash "$current_runtime_contract_hash" \
    --arg secret_contract "${recovery_secret_file:-$secret_contract_file}" --arg secret_hash "$secret_contract_hash" \
    --arg data_device "$data_dir_device" --arg data_inode "$data_dir_inode" --arg data_nlink "$data_dir_nlink" \
    --arg active_data_location "$active_data_location" --arg active_data_device "$active_data_device" --arg active_data_inode "$active_data_inode" --arg active_data_nlink "$active_data_nlink" \
    --arg failed_candidate_data "$failed_candidate_data" --arg failed_candidate_data_device "$failed_candidate_data_device" --arg failed_candidate_data_inode "$failed_candidate_data_inode" --arg failed_candidate_data_nlink "$failed_candidate_data_nlink" \
    --arg incomplete_restore_data "$incomplete_restore_data" --arg incomplete_restore_data_device "$incomplete_restore_data_device" --arg incomplete_restore_data_inode "$incomplete_restore_data_inode" --arg incomplete_restore_data_nlink "$incomplete_restore_data_nlink" \
    --arg checkpoint_device "$checkpoint_root_device" --arg checkpoint_inode "$checkpoint_root_inode" --arg checkpoint_nlink "$checkpoint_root_nlink" \
    --arg checkpoint_dir_device "$checkpoint_dir_device" --arg checkpoint_dir_inode "$checkpoint_dir_inode" --arg checkpoint_dir_nlink "$checkpoint_dir_nlink" \
    --arg env_device "$env_device" --arg env_inode "$env_inode" --arg env_nlink "$env_nlink" \
    --arg active_env_device "$active_env_device" --arg active_env_inode "$active_env_inode" --arg active_env_nlink "$active_env_nlink" --arg active_env_hash "$active_env_hash" \
    --arg env_pin "$env_file_pin" --arg env_pin_device "$env_pin_device" --arg env_pin_inode "$env_pin_inode" --arg env_pin_nlink "$env_pin_nlink" --arg env_pin_hash "$env_pin_hash" \
    --arg recovery_env_device "$recovery_env_device" --arg recovery_env_inode "$recovery_env_inode" --arg recovery_env_nlink "$recovery_env_nlink" --arg recovery_env_hash "$recovery_env_hash" \
    --arg attestation "$attestation_file" --arg attestation_hash "$attestation_hash" \
    --arg network "$network_name" --arg ip "$network_ip" --arg network_id "$network_id" --arg network_driver "$network_driver" \
    --arg network_internal "$network_internal" --arg network_ipam_driver "$network_ipam_driver" --arg network_subnet "$network_subnet" \
    --arg network_contract "$recovery_network_file" --arg network_contract_hash "$recovery_network_hash" \
    --arg network_contract_device "$recovery_network_device" --arg network_contract_inode "$recovery_network_inode" --arg network_contract_nlink "$recovery_network_nlink" \
    --arg candidate_ingress_state "$candidate_ingress_state" \
    --arg recovery_bundle "$recovery_bundle_dir" --arg recovery_device "$recovery_bundle_device" \
    --arg recovery_inode "$recovery_bundle_inode" --arg recovery_nlink "$recovery_bundle_nlink" \
    --arg recovery_manifest "$recovery_manifest_file" --arg recovery_manifest_hash "$recovery_manifest_hash" \
    --arg recovery_manifest_device "$recovery_manifest_device" --arg recovery_manifest_inode "$recovery_manifest_inode" --arg recovery_manifest_nlink "$recovery_manifest_nlink" \
    --arg docker_socket "$docker_socket_path" --arg docker_socket_owner "$docker_socket_owner" --arg docker_socket_group "$docker_socket_group" --arg docker_socket_mode "$docker_socket_mode" \
    --arg docker_socket_device "$docker_socket_device" --arg docker_socket_inode "$docker_socket_inode" --arg docker_socket_nlink "$docker_socket_nlink" \
    --arg archive_device "$archive_device" --arg archive_inode "$archive_inode" --arg archive_nlink "$archive_nlink" --arg archive_hash "$archive_hash" \
    --arg checksum_device "$checksum_device" --arg checksum_inode "$checksum_inode" --arg checksum_nlink "$checksum_nlink" --arg checksum_hash "$checksum_hash" \
    --arg checkpoint_root "$checkpoint_root" \
    --arg capacity_reservation "$capacity_reservation" --arg capacity_reservation_kb "$capacity_reservation_kb" --arg capacity_state "$capacity_reservation_released" \
    --arg capacity_device "$capacity_reservation_device" --arg capacity_inode "$capacity_reservation_inode" --arg capacity_nlink "$capacity_reservation_nlink" --arg capacity_size "$capacity_reservation_size" \
    '{version:1,phase:$phase,state:$state,pid:($pid|tonumber),target_image:$target,current_id:$current,current_project:$current_project,current_project_migration:($current_project != "omni-money"),current_image_id:$current_image,rollback_image:$rollback,candidate_id:$candidate,candidate_ingress_state:$candidate_ingress_state,data_dir:$data,original_data_identity:{device:$data_device,inode:$data_inode,nlink:$data_nlink},active_data_location:$active_data_location,active_data_identity:{device:$active_data_device,inode:$active_data_inode,nlink:$active_data_nlink},failed_candidate_data:$failed_candidate_data,failed_candidate_data_identity:{device:$failed_candidate_data_device,inode:$failed_candidate_data_inode,nlink:$failed_candidate_data_nlink},incomplete_restore_data:$incomplete_restore_data,incomplete_restore_data_identity:{device:$incomplete_restore_data_device,inode:$incomplete_restore_data_inode,nlink:$incomplete_restore_data_nlink},checkpoint_root:$checkpoint_root,checkpoint_root_identity:{device:$checkpoint_device,inode:$checkpoint_inode,nlink:$checkpoint_nlink},checkpoint_dir:$checkpoint,checkpoint_dir_identity:{device:$checkpoint_dir_device,inode:$checkpoint_dir_inode,nlink:$checkpoint_dir_nlink},archive_path:$archive,archive_identity:{device:$archive_device,inode:$archive_inode,nlink:$archive_nlink,sha256:$archive_hash},checksum_identity:{device:$checksum_device,inode:$checksum_inode,nlink:$checksum_nlink,sha256:$checksum_hash},capacity_reservation:$capacity_reservation,capacity_reservation_kb:($capacity_reservation_kb|tonumber?),capacity_reservation_state:$capacity_state,capacity_reservation_identity:{device:$capacity_device,inode:$capacity_inode,nlink:$capacity_nlink,size:($capacity_size|tonumber?)},env_file:$env,original_env_identity:{device:$env_device,inode:$env_inode,nlink:$env_nlink,sha256:$env_hash},active_env_identity:{device:$active_env_device,inode:$active_env_inode,nlink:$active_env_nlink,sha256:$active_env_hash},env_pin_file:$env_pin,env_pin_identity:{device:$env_pin_device,inode:$env_pin_inode,nlink:$env_pin_nlink,sha256:$env_pin_hash},recovery_env_file:$recovery_env_file,recovery_env_identity:{device:$recovery_env_device,inode:$recovery_env_inode,nlink:$recovery_env_nlink,sha256:$recovery_env_hash},compose_file:$compose,compose_hash:$compose_hash,rollback_definition:$rollback_definition,compose_snapshot:$snapshot,compose_snapshot_hash:$snapshot_hash,runtime_contract_file:$runtime,runtime_contract_hash:$runtime_hash,secret_contract_file:$secret_contract,secret_contract_hash:$secret_hash,recovery_bundle:$recovery_bundle,recovery_bundle_identity:{device:$recovery_device,inode:$recovery_inode,nlink:$recovery_nlink},recovery_manifest:$recovery_manifest,recovery_manifest_hash:$recovery_manifest_hash,recovery_manifest_identity:{device:$recovery_manifest_device,inode:$recovery_manifest_inode,nlink:$recovery_manifest_nlink},docker_socket:$docker_socket,docker_socket_identity:{owner:$docker_socket_owner,group:$docker_socket_group,mode:$docker_socket_mode,device:$docker_socket_device,inode:$docker_socket_inode,nlink:$docker_socket_nlink,type:"socket"},attestation_file:$attestation,attestation_hash:$attestation_hash,network:$network,network_ip:$ip,network_identity:{id:$network_id,driver:$network_driver,internal:$network_internal,ipam_driver:$network_ipam_driver,subnet:$network_subnet},network_contract_file:$network_contract,network_contract_hash:$network_contract_hash,network_contract_identity:{device:$network_contract_device,inode:$network_contract_inode,nlink:$network_contract_nlink}}' \
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
  recovery_network_file="$recovery_bundle_dir/network-contract.json"
  for source in "$env_file_pin" "$attestation_pin" "$compose_file_pin" "$compose_snapshot" "$current_runtime_contract_file" "$secret_contract_file" "$network_contract_file"; do
    case "$source" in
      "$env_file_pin") name="$recovery_env_file" ;;
      "$attestation_pin") name="$recovery_attestation_file" ;;
      "$compose_file_pin") name="$recovery_compose_file" ;;
      "$compose_snapshot") name="$recovery_snapshot_file" ;;
      "$current_runtime_contract_file") name="$recovery_runtime_file" ;;
      "$secret_contract_file") name="$recovery_secret_file" ;;
      "$network_contract_file") name="$recovery_network_file" ;;
      *) return 1 ;;
    esac
    create_exclusive_file "$name" || return 1
    cp -p -- "$source" "$name" || return 1
    chmod 0400 "$name" || return 1
    chown 0:0 "$name" || return 1
    validate_root_owned_file "$name" "recovery bundle file" || return 1
    fsync_path "$name" || return 1
    if [ "$name" = "$recovery_env_file" ]; then
      recovery_env_device="$(stat_device "$name")"
      recovery_env_inode="$(stat_inode "$name")"
      recovery_env_nlink="$(stat_nlink "$name")"
      recovery_env_hash="$(sha256_file "$name")"
    elif [ "$name" = "$recovery_network_file" ]; then
      recovery_network_device="$(stat_device "$name")"
      recovery_network_inode="$(stat_inode "$name")"
      recovery_network_nlink="$(stat_nlink "$name")"
      recovery_network_hash="$(sha256_file "$name")"
    fi
  done
  recovery_manifest_file="$recovery_bundle_dir/manifest"
  create_exclusive_file "$recovery_manifest_file" || return 1
  # Keep recovery copies self-describing without copying any secret bytes into
  # the journal. Each fixed destination is bound to its device/inode/link-count
  # and digest, so a power-loss replay can verify the bundle before use.
  for destination in "$recovery_env_file" "$recovery_attestation_file" "$recovery_compose_file" "$recovery_snapshot_file" "$recovery_runtime_file" "$recovery_secret_file" "$recovery_network_file"; do
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

validate_compose_env_syntax() {
  local path="$1" line key value inner seen line_number=0
  local -a keys=()
  while IFS= read -r line || [ -n "$line" ]; do
    line_number=$((line_number + 1))
    case "$line" in *$'\r'*) fail "update env file contains a carriage return at line $line_number"; return 1 ;; esac
    [[ "$line" =~ ^[[:space:]]*(#.*)?$ ]] && continue
    if [[ ! "$line" =~ ^([A-Za-z_][A-Za-z0-9_]*)=(.*)$ ]]; then
      fail "update env file uses unsupported Compose dotenv syntax at line $line_number"
      return 1
    fi
    key="${BASH_REMATCH[1]}"; value="${BASH_REMATCH[2]}"
    if ((${#keys[@]})); then
      for seen in "${keys[@]}"; do
        [ "$seen" != "$key" ] || { fail "update env file contains duplicate key $key"; return 1; }
      done
    fi
    keys+=("$key")
    case "$value" in
      \'*)
        [ "${#value}" -ge 2 ] && [ "${value: -1}" = "'" ] || {
          fail "update env file contains an unterminated single-quoted value at line $line_number"; return 1;
        }
        inner="${value:1:${#value}-2}"
        case "$inner" in *\'*) fail "update env file contains unsupported embedded single quote at line $line_number"; return 1 ;; esac
        ;;
      \"*)
        [ "${#value}" -ge 2 ] && [ "${value: -1}" = '"' ] || {
          fail "update env file contains an unterminated double-quoted value at line $line_number"; return 1;
        }
        inner="${value:1:${#value}-2}"
        case "$inner" in *\"*|*\\*|*\$*|*\`*) fail "update env file contains unsupported expansion or escape syntax at line $line_number"; return 1 ;; esac
        ;;
      *)
        [[ "$value" =~ ^[A-Za-z0-9_./:@,+%=?-]*$ ]] || {
          fail "update env file contains unsupported unquoted characters at line $line_number"; return 1;
        }
        ;;
    esac
  done < "$path"
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
  local env_seen=0 attestation_seen=0 compose_seen=0 snapshot_seen=0 runtime_seen=0 secret_seen=0 network_seen=0 rollback_seen=0
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
      "$recovery_network_file") [ "$network_seen" -eq 0 ] || return 1; network_seen=1 ;;
      "$recovery_rollback_file") [ -n "$recovery_rollback_file" ] && [ "$rollback_seen" -eq 0 ] || return 1; rollback_seen=1 ;;
      *) return 1 ;;
    esac
    validate_pinned_private_file "$path" "durable recovery copy" "$device" "$inode" "$nlink" "$hash" root || return 1
    count=$((count + 1))
  done < "$recovery_manifest_file"
  [ "$env_seen" -eq 1 ] && [ "$attestation_seen" -eq 1 ] && [ "$compose_seen" -eq 1 ] &&
    [ "$snapshot_seen" -eq 1 ] && [ "$runtime_seen" -eq 1 ] && [ "$secret_seen" -eq 1 ] && [ "$network_seen" -eq 1 ] || return 1
  if [ "$count" -eq 7 ] && [ "$rollback_seen" -eq 0 ]; then
    return 0
  fi
  [ "$count" -eq 8 ] && [ "$rollback_seen" -eq 1 ]
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
  # These values are all Compose interpolations or application startup
  # inputs. They must come exclusively from the pinned env file, never from
  # the updater's ambient shell (including proxy/passkey/session policy).
  unset AUTH_KDF_CONCURRENCY SESSION_MAX_AGE_HOURS SESSION_IDLE_TIMEOUT_MINUTES
  unset SESSION_REAUTH_MAX_AGE_MINUTES SESSION_MAX_CONCURRENT TRUSTED_PROXIES
  unset HTTPS_REDIRECT_HOST PASSKEY_RP_ID PASSKEY_ORIGINS ALLOWED_HOSTS
  unset CORS_ALLOWED_ORIGINS CONTROL_DB_PATH CONTROL_DB_ENCRYPTION_KEY_FILE
  unset VAULT_ROOT TMPDIR SQLITE_TMPDIR DATA_AT_REST_MODE
  unset DATA_AT_REST_ATTESTATION_FILE HOST_IP PORT FORCE_HTTPS ALLOW_INSECURE_HTTP
  unset DOCKER_CONFIG DOCKER_CLI_PLUGIN_EXTRA_DIRS DOCKER_CLI_EXPERIMENTAL
}

validate_local_docker() {
  local context endpoint canonical_socket
  [ -z "${DOCKER_HOST+x}" ] || { fail "DOCKER_HOST must be unset; remote Docker endpoints are not allowed"; return 1; }
  [ -z "${DOCKER_TLS_VERIFY+x}" ] || { fail "DOCKER_TLS_VERIFY must be unset for the local daemon contract"; return 1; }
  [ -z "${DOCKER_CERT_PATH+x}" ] || { fail "DOCKER_CERT_PATH must be unset for the local daemon contract"; return 1; }
  [ -z "${DOCKER_CONFIG+x}" ] || { fail "DOCKER_CONFIG must be unset; the updater uses the trusted local Docker configuration"; return 1; }
  [ -z "${DOCKER_CLI_PLUGIN_EXTRA_DIRS+x}" ] || { fail "Docker CLI plugin search paths must be unset"; return 1; }
  [ -z "${DOCKER_CLI_EXPERIMENTAL+x}" ] || { fail "Docker CLI experimental/plugin configuration must be unset"; return 1; }
  if [ -n "${DOCKER_CONTEXT:-}" ] && [ "${DOCKER_CONTEXT}" != default ]; then
    fail "DOCKER_CONTEXT must select the local default context"
    return 1
  fi
  context="$("$docker_path" context show 2>/dev/null)" || { fail "Docker context could not be inspected"; return 1; }
  [ "$context" = default ] || { fail "Docker context is not the local default context"; return 1; }
  endpoint="$("$docker_path" context inspect default --format '{{(index .Endpoints "docker").Host}}' 2>/dev/null)" || {
    fail "Docker default context endpoint could not be inspected"
    return 1
  }
  [ "$endpoint" = "unix:///var/run/docker.sock" ] || {
    fail "Docker default context must use the canonical local Unix socket"
    return 1
  }
  # The socket is the authority boundary for Docker and must be on this host.
  # Non-Linux hosts receive placeholders only until the mandatory Linux
  # production-host check rejects them later in preflight.
  if [ "$(uname -s)" = Linux ]; then
    canonical_socket="$(readlink -f -- /var/run/docker.sock)" || {
      fail "canonical Docker socket could not be resolved"
      return 1
    }
    [ "$canonical_socket" = /run/docker.sock ] || { fail "Docker socket canonical path must be /run/docker.sock"; return 1; }
    [ -S "$canonical_socket" ] && [ ! -L "$canonical_socket" ] || { fail "canonical Docker socket is missing or is a symbolic link"; return 1; }
    docker_socket_path="$canonical_socket"
    docker_socket_owner="$(stat_owner "$docker_socket_path")" || return 1
    docker_socket_group="$(stat_group "$docker_socket_path")" || return 1
    docker_socket_mode="$(stat_mode "$docker_socket_path")" || return 1
    [ "$docker_socket_owner" = 0 ] || { fail "Docker socket must be root-owned"; return 1; }
    case "$docker_socket_mode" in 600|660) ;; *) fail "Docker socket mode must be 0600 or 0660"; return 1 ;; esac
    docker_socket_device="$(stat_device "$docker_socket_path")" || return 1
    docker_socket_inode="$(stat_inode "$docker_socket_path")" || return 1
    docker_socket_nlink="$(stat_nlink "$docker_socket_path")" || return 1
    [ "$docker_socket_nlink" = 1 ] || { fail "Docker socket must have exactly one link"; return 1; }
    [ -n "$docker_socket_device" ] && [ -n "$docker_socket_inode" ] || return 1
  else
    docker_socket_path=/run/docker.sock
    docker_socket_owner=0; docker_socket_group=0; docker_socket_mode=660
    docker_socket_device=mock; docker_socket_inode=mock; docker_socket_nlink=1
  fi
  docker_socket_ready=1
  unset DOCKER_CONTEXT DOCKER_TLS_VERIFY DOCKER_CERT_PATH
}

validate_pinned_docker_socket() {
  [ "$docker_socket_ready" -eq 1 ] || return 0
  [ "$docker_socket_path" = /run/docker.sock ] && [ -S "$docker_socket_path" ] && [ ! -L "$docker_socket_path" ] || return 1
  [ "$(stat_owner "$docker_socket_path")" = "$docker_socket_owner" ] || return 1
  [ "$(stat_group "$docker_socket_path")" = "$docker_socket_group" ] || return 1
  [ "$(stat_mode "$docker_socket_path")" = "$docker_socket_mode" ] || return 1
  [ "$(stat_device "$docker_socket_path")" = "$docker_socket_device" ] || return 1
  [ "$(stat_inode "$docker_socket_path")" = "$docker_socket_inode" ] || return 1
  [ "$(stat_nlink "$docker_socket_path")" = "$docker_socket_nlink" ] || return 1
}

docker_cli() {
  validate_pinned_docker_socket || { fail "pinned Docker socket identity changed"; return 1; }
  validate_pinned_file "$docker_path" "Docker CLI executable" "$docker_device" "$docker_inode" "$docker_nlink" "$docker_hash" root || return 1
  "$docker_path" "$@"
}

validate_trusted_toolchain() {
  local command_name path canonical i
  tool_names=(); tool_paths=(); tool_devices=(); tool_inodes=(); tool_nlinks=(); tool_hashes=()
  for command_name in bash docker jq sha256sum tar stat awk sed grep mktemp du df chown find findmnt uname id dirname pwd cp chmod mkdir rmdir rm mv date sleep sync fallocate readlink tr; do
    if declare -F "$command_name" >/dev/null 2>&1; then
      fail "shell function shadows trusted command: $command_name"
      return 1
    fi
    path="$(type -P "$command_name" 2>/dev/null)" || {
      fail "required command is missing: $command_name"
      return 1
    }
    canonical="$(readlink -f -- "$path" 2>/dev/null || printf '%s' "$path")"
    path="$canonical"
    [ -n "$path" ] || {
      fail "required command is missing: $command_name"
      return 1
    }
    case "$path" in
      /bin/*|/sbin/*|/usr/bin/*|/usr/sbin/*) ;;
      *) fail "command is outside the trusted system path: $command_name ($path)"; return 1 ;;
    esac
    validate_existing_file "$path" "trusted $command_name executable" root || return 1
    tool_names+=("$command_name"); tool_paths+=("$path")
    tool_devices+=("$(stat_device "$path")"); tool_inodes+=("$(stat_inode "$path")")
    tool_nlinks+=("$(stat_nlink "$path")"); tool_hashes+=("$(sha256_file "$path")")
    if [ "$command_name" = docker ]; then
      docker_path="$path"; i=$((${#tool_names[@]} - 1))
      docker_device="${tool_devices[$i]}"; docker_inode="${tool_inodes[$i]}"
      docker_nlink="${tool_nlinks[$i]}"; docker_hash="${tool_hashes[$i]}"
    fi
  done
}

validate_pinned_toolchain() {
  local i
  for i in "${!tool_names[@]}"; do
    validate_pinned_file "${tool_paths[$i]}" "trusted ${tool_names[$i]} executable" \
      "${tool_devices[$i]}" "${tool_inodes[$i]}" "${tool_nlinks[$i]}" "${tool_hashes[$i]}" root || return 1
  done
}

validate_pinned_tool_by_name() {
  local expected="$1" i
  for i in "${!tool_names[@]}"; do
    [ "${tool_names[$i]}" = "$expected" ] || continue
    if validate_pinned_file "${tool_paths[$i]}" "trusted ${tool_names[$i]} executable" \
      "${tool_devices[$i]}" "${tool_inodes[$i]}" "${tool_nlinks[$i]}" "${tool_hashes[$i]}" root; then
      return 0
    fi
    return 1
  done
  fail "pinned isolation tool is missing: $expected"
  return 1
}

validate_docker_isolation_authority() {
  local command_name
  # Authenticate only the local Docker authority and the tools needed to
  # verify its pinned executable/socket identity. Container stop/status uses
  # Docker Go templates and must not be blocked by a changed JSON parser.
  for command_name in docker stat sha256sum sed dirname; do
    if ! validate_pinned_tool_by_name "$command_name"; then
      fail "pinned Docker isolation authority dependency changed: $command_name"
      return 1
    fi
  done
  validate_pinned_docker_socket || { fail "pinned Docker isolation socket changed"; return 1; }
}

validate_network_isolation_parsers() {
  local command_name
  # Network attachment inspection is JSON and therefore requires jq. Keep it
  # behind the stop boundary so parser drift cannot leave a running container
  # merely because automatic network isolation is no longer trustworthy.
  for command_name in stat sha256sum sed dirname jq; do
    if ! validate_pinned_tool_by_name "$command_name"; then
      fail "pinned network isolation parser dependency changed: $command_name"
      return 1
    fi
  done
}

configure_compose_plugin_boundary() {
  local candidate canonical
  docker_config_dir="$pin_dir/docker-config"
  mkdir -m 0700 -- "$docker_config_dir" || return 1
  chown 0:0 "$docker_config_dir" || return 1
  validate_existing_directory "$docker_config_dir" "private Docker configuration directory" root || return 1
  docker_config_device="$(stat_device "$docker_config_dir")"
  docker_config_inode="$(stat_inode "$docker_config_dir")"
  docker_config_nlink="$(stat_nlink "$docker_config_dir")"
  for candidate in \
    /usr/local/lib/docker/cli-plugins/docker-compose \
    /usr/local/libexec/docker/cli-plugins/docker-compose \
    /usr/lib/docker/cli-plugins/docker-compose \
    /usr/libexec/docker/cli-plugins/docker-compose; do
    [ -x "$candidate" ] || continue
    canonical="$(readlink -f -- "$candidate")" || return 1
    case "$canonical" in /usr/lib/*|/usr/libexec/*|/usr/local/lib/*|/usr/local/libexec/*) ;; *) continue ;; esac
    compose_plugin_path="$canonical"
    break
  done
  [ -n "$compose_plugin_path" ] || { fail "Docker Compose plugin is not installed in a trusted system directory"; return 1; }
  validate_existing_file "$compose_plugin_path" "Docker Compose plugin" root || return 1
  compose_plugin_device="$(stat_device "$compose_plugin_path")"; compose_plugin_inode="$(stat_inode "$compose_plugin_path")"
  compose_plugin_nlink="$(stat_nlink "$compose_plugin_path")"; compose_plugin_hash="$(sha256_file "$compose_plugin_path")"
  DOCKER_CONFIG="$docker_config_dir"
  DOCKER_CLI_PLUGIN_EXTRA_DIRS="$(dirname -- "$compose_plugin_path")"
  export DOCKER_CONFIG DOCKER_CLI_PLUGIN_EXTRA_DIRS
}

validate_compose_plugin_boundary() {
  validate_pinned_directory "$docker_config_dir" "private Docker configuration directory" \
    "$docker_config_device" "$docker_config_inode" "$docker_config_nlink" root || return 1
  validate_pinned_file "$compose_plugin_path" "Docker Compose plugin" "$compose_plugin_device" \
    "$compose_plugin_inode" "$compose_plugin_nlink" "$compose_plugin_hash" root
}

compose() {
  # The selected env file is always the immutable private pin. Explicit
  # project/file arguments make COMPOSE_* ambient configuration irrelevant.
  validate_compose_plugin_boundary || return 1
  docker_cli compose --env-file "$env_file_pin" \
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

discover_legacy_current() {
  local output_file="$pin_dir/legacy-ps.$$.txt" id count=0 only="" legacy_project legacy_service legacy_name
  legacy_project_name="$(printf '%s' "${project_dir##*/}" | tr '[:upper:]' '[:lower:]' | sed -E 's/[^a-z0-9_-]//g; s/^[^a-z0-9]+//')"
  [ -n "$legacy_project_name" ] && [ "$legacy_project_name" != "$project_name" ] || {
    fail "directory-derived legacy Compose project name is not distinct and valid"
    return 1
  }
  create_exclusive_file "$output_file" || return 1
  if ! docker_cli ps -aq --filter "name=^/${service_name}$" > "$output_file"; then
    rm -f -- "$output_file"
    return 1
  fi
  while IFS= read -r id; do
    [ -n "$id" ] || continue
    count=$((count + 1)); only="$id"
  done < "$output_file"
  rm -f -- "$output_file"
  [ "$count" -eq 1 ] || {
    fail "fixed Compose project has no current container and legacy discovery was not unique"
    return 1
  }
  legacy_project="$(docker_cli inspect --format '{{index .Config.Labels "com.docker.compose.project"}}' "$only")" || return 1
  legacy_service="$(docker_cli inspect --format '{{index .Config.Labels "com.docker.compose.service"}}' "$only")" || return 1
  legacy_name="$(docker_cli inspect --format '{{.Name}}' "$only")" || return 1
  [ "$legacy_project" = "$legacy_project_name" ] && [ "$legacy_service" = "$service_name" ] && [ "$legacy_name" = "/$service_name" ] || {
    fail "legacy container identity is not the verified directory-derived Compose project"
    return 1
  }
  current_project="$legacy_project"
  legacy_current=1
  # Do not return the ID through command substitution: that would execute this
  # function in a subshell and silently discard the migration state.  Keep the
  # verified ID and project binding in the parent updater state machine.
  current_id="$only"
}

remove_legacy_current() {
  [ "$legacy_current" -eq 1 ] || return 0
  current_removed_expected=1
  state="legacy-migration-remove"
  write_journal legacy-migration-remove || return 1
  docker_cli rm "$current_id" >/dev/null || return 1
  if container_state "$current_id" >/dev/null 2>&1; then
    fail "verified legacy container remained after migration removal"
    return 1
  fi
}

container_state() { docker_cli inspect --format '{{.State.Status}}' "$1"; }
container_health() { docker_cli inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}missing{{end}}' "$1" 2>/dev/null || true; }
container_image_id() { docker_cli inspect --format '{{.Image}}' "$1"; }
container_config_user() { docker_cli inspect --format '{{.Config.User}}' "$1"; }
container_ports() { docker_cli inspect --format '{{json .HostConfig.PortBindings}}' "$1"; }
container_mounts() { docker_cli inspect --format '{{json .Mounts}}' "$1"; }
container_networks() { docker_cli inspect --format '{{json .NetworkSettings.Networks}}' "$1"; }

write_network_contract() {
  local output="$1" raw="$pin_dir/network-inspect.$$.json"
  create_exclusive_file "$raw" || return 1
  if ! docker_cli network inspect "$network_name" > "$raw"; then
    rm -f -- "$raw"
    return 1
  fi
  if ! jq -e --arg name "$network_name" --arg subnet "$network_subnet" '
    (length == 1) and
    (.[0].Name == $name) and ((.[0].Id // "") | type == "string" and length > 0) and
    (.[0].Driver == "bridge") and (.[0].Internal == true) and
    ((.[0].IPAM.Driver // "default") == "default") and
    (((.[0].IPAM.Config // []) | map(.Subnet // "") | sort) == [$subnet])
  ' "$raw" >/dev/null; then
    rm -f -- "$raw"
    fail "Docker network does not match the internal bridge/IPAM contract"
    return 1
  fi
  if ! jq -c '.[0] | {id:.Id,name:.Name,driver:.Driver,internal:.Internal,ipam_driver:(.IPAM.Driver // "default"),subnets:((.IPAM.Config // []) | map(.Subnet // "") | sort)}' "$raw" > "$output"; then
    rm -f -- "$raw"
    return 1
  fi
  rm -f -- "$raw"
}

capture_network_contract() {
  network_contract_file="$pin_dir/network-contract.json"
  create_exclusive_file "$network_contract_file" || return 1
  write_network_contract "$network_contract_file" || return 1
  chmod 0400 "$network_contract_file" || return 1
  validate_existing_file "$network_contract_file" "Docker network contract" || return 1
  network_contract_device="$(stat_device "$network_contract_file")"
  network_contract_inode="$(stat_inode "$network_contract_file")"
  network_contract_nlink="$(stat_nlink "$network_contract_file")"
  network_contract_hash="$(sha256_file "$network_contract_file")"
  network_id="$(jq -er '.id' "$network_contract_file")"
  network_driver="$(jq -er '.driver' "$network_contract_file")"
  network_internal="$(jq -er '.internal | tostring' "$network_contract_file")"
  network_ipam_driver="$(jq -er '.ipam_driver' "$network_contract_file")"
}

validate_network_contract() {
  local candidate="$pin_dir/network-contract.$$.verify.json" candidate_hash
  validate_pinned_file "$network_contract_file" "pinned Docker network contract" \
    "$network_contract_device" "$network_contract_inode" "$network_contract_nlink" "$network_contract_hash" || return 1
  create_exclusive_file "$candidate" || return 1
  if ! write_network_contract "$candidate"; then
    rm -f -- "$candidate"
    return 1
  fi
  candidate_hash="$(sha256_file "$candidate")" || { rm -f -- "$candidate"; return 1; }
  rm -f -- "$candidate"
  [ "$candidate_hash" = "$network_contract_hash" ] || {
    fail "Docker network identity or security configuration changed"
    return 1
  }
}

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
  if ! docker_cli inspect --format '{{json .}}' "$id" > "$raw"; then
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
        group_add: (.HostConfig.GroupAdd // []),
        device_requests: (.HostConfig.DeviceRequests // []),
        device_cgroup_rules: (.HostConfig.DeviceCgroupRules // []),
        runtime: (.HostConfig.Runtime // ""),
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
  local id="$1" label="$2" raw
  raw="$pin_dir/runtime-safety.$$.${id}.json"
  create_exclusive_file "$raw" || return 1
  if ! docker_cli inspect --format '{{json .}}' "$id" > "$raw"; then
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
    (((.HostConfig.GroupAdd // []) | length) == 0) and
    (((.HostConfig.DeviceRequests // []) | length) == 0) and
    (((.HostConfig.DeviceCgroupRules // []) | length) == 0) and
    ((.HostConfig.Runtime // "") == "runc") and
    ((.HostConfig.LogConfig.Config["max-size"] // "") != "") and
    ((.HostConfig.LogConfig.Config["max-file"] // "") != "")
  ' "$raw" >/dev/null; then
    rm -f -- "$raw"
    fail "$label contains unsafe capability, namespace, device, resource, or log configuration"
    return 1
  fi
  rm -f -- "$raw" || return 1
}

validate_desired_environment() {
  local id="$1" label="$2" expected_file="$pin_dir/environment-expected.$$.json" actual_file="$pin_dir/environment-actual.$$.json"
  create_exclusive_file "$expected_file" || return 1
  create_exclusive_file "$actual_file" || { rm -f -- "$expected_file"; return 1; }
  if ! jq -c '
    .services["omni-money"].environment // {} |
    if type == "object" then to_entries | map({name:.key,value:(.value|tostring)})
    elif type == "array" then map(capture("^(?<name>[^=]*)(?:=(?<value>.*))?$") | .value = (.value // ""))
    else [] end | sort_by(.name)
  ' "$compose_snapshot" > "$expected_file"; then
    rm -f -- "$expected_file" "$actual_file"; return 1
  fi
  if ! docker_cli inspect --format '{{json .Config.Env}}' "$id" > "$actual_file"; then
    rm -f -- "$expected_file" "$actual_file"; return 1
  fi
  if ! jq -n -e --slurpfile expected "$expected_file" --slurpfile actual "$actual_file" '
    ($expected[0] // [] | sort_by(.name)) as $want |
    (($actual[0] // []) | map(select(type == "string") |
      capture("^(?<name>[^=]*)(?:=(?<value>.*))?$") | .value = (.value // ""))) as $got |
    ($want | map(.name)) as $names |
    ($got | map(select(.name as $n | ($names | index($n) != null))) | sort_by(.name)) as $selected |
    (($want | map(.name) | length) == ($want | map(.name) | unique | length)) and
    (($selected | map(.name) | length) == ($want | length)) and
    (($selected | map(.name) | length) == ($selected | map(.name) | unique | length)) and
    ($selected == $want)
  ' >/dev/null; then
    rm -f -- "$expected_file" "$actual_file"
    fail "$label runtime environment differs from the resolved Compose environment"
    return 1
  fi
  rm -f -- "$expected_file" "$actual_file"
}

validate_desired_host_config() {
  local id="$1" label="$2" actual
  actual="$pin_dir/host-config.$$.${id}.json"
  create_exclusive_file "$actual" || return 1
  if ! docker_cli inspect --format '{{json .HostConfig}}' "$id" > "$actual"; then
    rm -f -- "$actual"; return 1
  fi
  if ! jq -n -e --slurpfile actual "$actual" --slurpfile desired "$compose_snapshot" '
    def bytes:
      if type == "number" then .
      elif test("^[0-9]+$") then tonumber
      else (ascii_downcase | capture("^(?<n>[0-9]+)(?<u>[kmgt])(?:i?b)?$") |
        (.n|tonumber) * ({k:1024,m:1048576,g:1073741824,t:1099511627776}[.u])) end;
    ($actual[0]) as $a | ($desired[0].services["omni-money"]) as $s |
    ($desired[0].networks.pangolin_target.name) as $network |
    (($s.restart // "no") as $restart |
      ($a.RestartPolicy.Name // "no") == $restart and
      (($a.RestartPolicy.MaximumRetryCount // 0) == 0)) and
    (($a.NetworkMode // "") == $network) and
    (($a.ReadonlyRootfs // false) == true) and (($a.Privileged // false) == false) and
    (($a.Init // false) == false) and
    (($a.CapDrop // [] | sort) == ($s.cap_drop // [] | sort)) and
    (($a.CapAdd // [] | sort) == ($s.cap_add // [] | sort)) and
    (($a.Devices // []) | length == 0) and (($a.PidMode // "") == "") and (($a.IpcMode // "") == "") and
    (($a.NanoCpus // 0) == ((($s.cpus | tonumber) * 1000000000) | floor)) and
    (($a.Memory // 0) == ($s.mem_limit | bytes)) and
    (($a.PidsLimit // 0) == ($s.pids_limit | tonumber)) and
    (($a.LogConfig.Type // "") == $s.logging.driver) and
    (($a.LogConfig.Config["max-size"] // "" | tostring) == ($s.logging.options["max-size"] | tostring)) and
    (($a.LogConfig.Config["max-file"] // "" | tostring) == ($s.logging.options["max-file"] | tostring)) and
    (($a.SecurityOpt // [] | sort) == ($s.security_opt // [] | sort)) and
    (($a.GroupAdd // []) | length == 0) and
    (($a.DeviceRequests // []) | length == 0) and
    (($a.DeviceCgroupRules // []) | length == 0) and
    (($a.Runtime // "") == $s.runtime) and ($s.runtime == "runc")
  ' >/dev/null; then
    rm -f -- "$actual"
    fail "$label host runtime differs from the resolved Compose contract"
    return 1
  fi
  rm -f -- "$actual"
}

validate_container_config() {
  local id="$1" expected_image="$2" expected_state="$3" label="$4" ports mounts networks user readonly_root capdrop security
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
  [ "$user" = "${data_uid}:${data_gid}" ] || {
    fail "$label must use the host-pinned numeric user ${data_uid}:${data_gid}; named image users are not trusted"
    return 1
  }
  readonly_root="$(docker_cli inspect --format '{{.HostConfig.ReadonlyRootfs}}' "$id")"
  [ "$readonly_root" = "true" ] || { fail "$label must use a read-only root filesystem"; return 1; }
  capdrop="$(docker_cli inspect --format '{{json .HostConfig.CapDrop}}' "$id")"
  jq -e 'index("ALL") != null' <<< "$capdrop" >/dev/null || { fail "$label must drop all capabilities"; return 1; }
  security="$(docker_cli inspect --format '{{json .HostConfig.SecurityOpt}}' "$id")"
  jq -e 'index("no-new-privileges:true") != null' <<< "$security" >/dev/null || { fail "$label must set no-new-privileges"; return 1; }
  validate_runtime_safety "$id" "$label" || return 1
  validate_desired_environment "$id" "$label" || return 1
  validate_desired_host_config "$id" "$label" || return 1
}

validate_container_user() {
  local id="$1" label="$2" user groups
  # Never execute `id` from the candidate image: both that binary and the
  # image's passwd database are attacker-controlled. Docker's host-side
  # numeric Config.User and supplementary-group settings are the boundary.
  user="$(container_config_user "$id")" || return 1
  groups="$(docker_cli inspect --format '{{json .HostConfig.GroupAdd}}' "$id")" || return 1
  [ "$user" = "${data_uid}:${data_gid}" ] || { fail "$label must run as ${data_uid}:${data_gid}"; return 1; }
  jq -e '(. == null) or (length == 0)' <<< "$groups" >/dev/null || {
    fail "$label must not receive supplementary groups"
    return 1
  }
}

validate_single_network_ip() {
  local id="$1" networks
  networks="$(container_networks "$id")"
  jq -er --arg network "$network_name" --arg network_id "$network_id" '
    if (((keys | length) == 1) and has($network) and
        (.[$network].NetworkID == $network_id) and
        (.[$network].IPAddress | type == "string" and length > 0))
    then .[$network].IPAddress else empty end
  ' <<< "$networks"
}

disconnect_all_networks() {
  local id="$1" networks network
  networks="$(container_networks "$id")"
  while IFS= read -r network; do
    [ -n "$network" ] || continue
    if ! docker_cli network disconnect "$network" "$id" >/dev/null; then
      fail "could not disconnect $id from network $network"
      return 1
    fi
  done < <(jq -r 'keys[]' <<< "$networks")
  networks="$(container_networks "$id")"
  jq -e 'length == 0' <<< "$networks" >/dev/null || { fail "container retained a network after isolation"; return 1; }
}

disconnect_candidate_ingress() {
  local id="$1" networks
  networks="$(container_networks "$id")" || return 1
  if jq -e 'length == 0' <<< "$networks" >/dev/null; then
    return 0
  fi
  jq -e --arg network "$network_name" --arg network_id "$network_id" '
    ((keys | length) == 1) and has($network) and (.[$network].NetworkID == $network_id)
  ' <<< "$networks" >/dev/null || {
    fail "candidate ingress attachment is not the pinned Docker network"
    return 1
  }
  docker_cli network disconnect "$network_id" "$id" >/dev/null || return 1
  networks="$(container_networks "$id")" || return 1
  jq -e 'length == 0' <<< "$networks" >/dev/null || {
    fail "candidate retained a network after pinned ingress disconnect"
    return 1
  }
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

replacement_docker_state_is_safe() {
  local output id count=0 only=""
  # Do not depend on Compose or its plugin during emergency isolation. Query
  # the already-pinned local Docker authority directly, and accept only the
  # exact candidate identity previously returned and validated by Compose (or
  # no replacement when Compose failed before returning an identity).
  if ! output="$(docker_cli ps --all --quiet --no-trunc \
    --filter "label=com.docker.compose.project=$project_name" \
    --filter "label=com.docker.compose.service=$service_name")"; then
    return 1
  fi
  while IFS= read -r id; do
    [ -n "$id" ] || continue
    count=$((count + 1)); only="$id"
  done <<< "$output"
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
  docker_cli stop --time 30 "$id" >/dev/null 2>&1 || true
  if ! state_value="$(container_state "$id" 2>/dev/null)"; then
    if [ "$label" = current ] && [ "$current_removed_expected" -eq 1 ]; then
      replacement_docker_state_is_safe || {
        fail "the expected replaced current container could not be verified"
        return 1
      }
      return 0
    fi
    fail "$label container state could not be verified after stop"
    return 1
  fi
  if [ "$state_value" = running ]; then
    docker_cli stop --time 30 "$id" >/dev/null 2>&1 || true
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
      ''|/*|../*|*/../*|*/..|*\\*|*$'\n'*|*$'\r'*|*$'\t'*) rm -f -- "$members" "$details"; fail "archive contains an unsafe member name"; return 1 ;;
    esac
  done < "$members"
  if ! tar -tvf "$archive" > "$details"; then
    rm -f -- "$members" "$details"
    return 1
  fi
  while IFS= read -r line; do
    case "$line" in *\\*|*$'\n'*|*$'\r'*|*$'\t'*) rm -f -- "$members" "$details"; fail "archive contains an unsafe detailed member name"; return 1 ;; esac
    type="${line:0:1}"
    case "$type" in -|d) ;; *) rm -f -- "$members" "$details"; fail "archive contains unsupported member type: $type"; return 1 ;; esac
  done < "$details"
  rm -f -- "$members" "$details"
}

validate_source_tree() {
  local root="$1" label="$2" entry tree owner links
  tree="$pin_dir/source-tree.$$.nul"
  create_exclusive_file "$tree" || return 1
  if ! find -P "$root" -xdev -mindepth 1 -print0 > "$tree"; then
    rm -f -- "$tree"
    return 1
  fi
  while IFS= read -r -d '' entry; do
    [ -L "$entry" ] && { fail "$label contains a symbolic link: $entry"; rm -f -- "$tree"; return 1; }
    [ -d "$entry" ] || [ -f "$entry" ] || { fail "$label contains a non-directory/non-regular entry: $entry"; rm -f -- "$tree"; return 1; }
    validate_data_entry "$entry" "$label entry" || { rm -f -- "$tree"; return 1; }
  done < "$tree"
  rm -f -- "$tree"
  validate_data_tree_devices "$root" "$label" "$(stat_device "$root")" || return 1
}

write_findmnt_targets() {
  local mount_json="$1" output="$2"
  jq -j '.filesystems[]? | recurse(.children[]?) | .target? | select(type == "string") | . + "\u0000"' \
    "$mount_json" > "$output"
}

validate_data_tree_devices() {
  local root="$1" label="$2" expected_device="$3" entry tree mounts mount_list
  tree="$pin_dir/source-devices.$$.nul"
  create_exclusive_file "$tree" || return 1
  if ! find -P "$root" -xdev -print0 > "$tree"; then
    rm -f -- "$tree"; return 1
  fi
  while IFS= read -r -d '' entry; do
    [ "$(stat_device "$entry")" = "$expected_device" ] || {
      rm -f -- "$tree"
      fail "$label crosses the attested encrypted filesystem boundary: $entry"
      return 1
    }
  done < "$tree"
  rm -f -- "$tree"
  # find -xdev is not sufficient by itself (a mount can share a device number
  # with its parent), so use findmnt's mount table to reject every nested
  # mount explicitly on production Linux hosts.
  if [ "$(uname -s)" = Linux ]; then
    mounts="$pin_dir/source-mounts.$$.json"
    create_exclusive_file "$mounts" || return 1
    if ! findmnt --json --list --output TARGET > "$mounts"; then
      rm -f -- "$mounts"; return 1
    fi
    mount_list="$pin_dir/source-mounts.$$.nul"
    create_exclusive_file "$mount_list" || { rm -f -- "$mounts"; return 1; }
    # Some util-linux releases retain a children tree even with --list. Walk
    # both representations recursively; inspecting only top-level entries can
    # miss a nested foreign filesystem or a same-device bind mount.
    if ! write_findmnt_targets "$mounts" "$mount_list"; then
      rm -f -- "$mounts" "$mount_list"; return 1
    fi
    while IFS= read -r -d '' entry; do
      [ -n "$entry" ] || continue
      if [ "$entry" != "$root" ] && path_contains "$root" "$entry"; then
        rm -f -- "$mounts" "$mount_list"
        fail "$label contains a nested mount: $entry"
        return 1
      fi
    done < "$mount_list"
    rm -f -- "$mounts" "$mount_list"
  fi
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

quarantine_candidate_data() {
  failed_candidate_data="$checkpoint_dir/failed-candidate-data"
  [ "$checkpoint_ready" -eq 1 ] || return 1
  validate_pinned_directory "$checkpoint_root" "checkpoint root before candidate quarantine" \
    "$checkpoint_root_device" "$checkpoint_root_inode" "$checkpoint_root_nlink" || return 1
  validate_pinned_directory "$checkpoint_dir" "checkpoint directory before candidate quarantine" \
    "$checkpoint_dir_device" "$checkpoint_dir_inode" "$checkpoint_dir_nlink" || return 1
  validate_pinned_directory "$data_dir" "live data before candidate quarantine" \
    "$active_data_device" "$active_data_inode" "$active_data_nlink" data || return 1
  validate_source_tree "$data_dir" "live data before candidate quarantine" || return 1
  [ ! -e "$failed_candidate_data" ] && [ ! -L "$failed_candidate_data" ] || return 1
  state="manual-reconciliation-quarantine"
  write_journal manual-reconciliation-quarantine || return 1
  mv -- "$data_dir" "$failed_candidate_data" || return 1
  fsync_directory "$(dirname -- "$data_dir")" || return 1
  fsync_directory "$checkpoint_dir" || return 1
  failed_candidate_data_device="$(stat_device "$failed_candidate_data")"
  failed_candidate_data_inode="$(stat_inode "$failed_candidate_data")"
  failed_candidate_data_nlink="$(stat_nlink "$failed_candidate_data")"
  active_data_location=""; active_data_device=""; active_data_inode=""; active_data_nlink=""
  checkpoint_dir_nlink="$(stat_nlink "$checkpoint_dir")"
  state="manual-reconciliation-required"
  write_journal manual-reconciliation-required
}

restore_checkpoint() {
  failed_candidate_data="$checkpoint_dir/failed-candidate-data"
  incomplete_restore_data="$checkpoint_dir/incomplete-restore"
  [ "$checkpoint_ready" -eq 1 ] || return 0
  validate_pinned_directory "$checkpoint_root" "checkpoint root" "$checkpoint_root_device" "$checkpoint_root_inode" "$checkpoint_root_nlink" || return 1
  validate_pinned_directory "$checkpoint_dir" "checkpoint directory" "$checkpoint_dir_device" "$checkpoint_dir_inode" "$checkpoint_dir_nlink" || return 1
  validate_pinned_file "$archive_path" "checkpoint archive" "$archive_device" "$archive_inode" "$archive_nlink" "$archive_hash" || return 1
  validate_pinned_file "$archive_path.sha256" "checkpoint checksum" "$checksum_device" "$checksum_inode" "$checksum_nlink" "$checksum_hash" || return 1
  if [ "$capacity_reservation_released" -eq 0 ]; then
    validate_capacity_reservation || return 1
  fi
  validate_data_directory "$data_dir" "live data directory" || return 1
  [ ! -e "$failed_candidate_data" ] && [ ! -L "$failed_candidate_data" ] || return 1
  [ ! -e "$incomplete_restore_data" ] && [ ! -L "$incomplete_restore_data" ] || return 1
  sha256sum --check "$archive_path.sha256" >/dev/null || return 1
  validate_tar_members "$archive_path" || { fail "checkpoint archive contains unsafe members"; return 1; }
  write_journal restore-moving || return 1
  fsync_directory "$(dirname -- "$data_dir")" || return 1
  mv -- "$data_dir" "$failed_candidate_data" || return 1
  fsync_directory "$(dirname -- "$data_dir")" || return 1
  fsync_directory "$checkpoint_dir" || return 1
  failed_candidate_data_device="$(stat_device "$failed_candidate_data")"
  failed_candidate_data_inode="$(stat_inode "$failed_candidate_data")"
  failed_candidate_data_nlink="$(stat_nlink "$failed_candidate_data")"
  active_data_location=""; active_data_device=""; active_data_inode=""; active_data_nlink=""
  checkpoint_dir_nlink="$(stat_nlink "$checkpoint_dir")"
  write_journal restore-data-parked || return 1
  mkdir -m 0700 -- "$data_dir" || return 1
  chown "$data_uid:$data_gid" "$data_dir" || return 1
  validate_data_directory "$data_dir" "restore data directory" || return 1
  fsync_directory "$(dirname -- "$data_dir")" || return 1
  active_data_location="$data_dir"; active_data_device="$(stat_device "$data_dir")"
  active_data_inode="$(stat_inode "$data_dir")"; active_data_nlink="$(stat_nlink "$data_dir")"
  write_journal restore-empty || return 1
  release_capacity_reservation || return 1
  if ! tar --one-file-system --numeric-owner --no-overwrite-dir -xpf "$archive_path" -C "$data_dir"; then
    # Never silently ignore either half of the rollback move. If extraction
    # fails, park the incomplete tree only when that move succeeds, then put
    # the original tree back. A failed move leaves the original path (or the
    # named recovery artifact) for an operator instead of pretending that the
    # live data was restored.
    if [ -e "$data_dir" ] || [ -L "$data_dir" ]; then
      [ ! -e "$incomplete_restore_data" ] && [ ! -L "$incomplete_restore_data" ] || return 1
      mv -- "$data_dir" "$incomplete_restore_data" || return 1
      fsync_directory "$(dirname -- "$data_dir")" || return 1
      fsync_directory "$checkpoint_dir" || return 1
      incomplete_restore_data_device="$(stat_device "$incomplete_restore_data")"
      incomplete_restore_data_inode="$(stat_inode "$incomplete_restore_data")"
      incomplete_restore_data_nlink="$(stat_nlink "$incomplete_restore_data")"
      active_data_location=""; active_data_device=""; active_data_inode=""; active_data_nlink=""
      checkpoint_dir_nlink="$(stat_nlink "$checkpoint_dir")"
      write_journal restore-extraction-failed || return 1
    fi
    [ ! -e "$data_dir" ] && [ ! -L "$data_dir" ] || return 1
    mv -- "$failed_candidate_data" "$data_dir" || return 1
    fsync_directory "$(dirname -- "$data_dir")" || return 1
    fsync_directory "$checkpoint_dir" || return 1
    active_data_location="$data_dir"; active_data_device="$(stat_device "$data_dir")"
    active_data_inode="$(stat_inode "$data_dir")"; active_data_nlink="$(stat_nlink "$data_dir")"
    failed_candidate_data=""; failed_candidate_data_device=""; failed_candidate_data_inode=""; failed_candidate_data_nlink=""
    checkpoint_dir_nlink="$(stat_nlink "$checkpoint_dir")"
    write_journal restore-original-reinstated || return 1
    return 1
  fi
  sync || return 1
  fsync_directory "$data_dir" || return 1
  active_data_location="$data_dir"; active_data_device="$(stat_device "$data_dir")"
  active_data_inode="$(stat_inode "$data_dir")"; active_data_nlink="$(stat_nlink "$data_dir")"
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
  active_env_device="$(stat_device "$env_file")"; active_env_inode="$(stat_inode "$env_file")"
  active_env_nlink="$(stat_nlink "$env_file")"; active_env_hash="$(sha256_file "$env_file")"
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

rollback_journal_or_stop() {
  if ! write_journal "$1"; then
    printf 'safe-update: durable rollback journal failed at phase %s; phase cannot advance and lock/journal/recovery artifacts are preserved\n' "$1" >&2
    exit "${rollback_original_status:-1}"
  fi
}

rollback_update() {
  local original_status="$1" old_id="" old_image_id="${current_image_id:-}" legacy_state="" reuse_legacy=0 isolation_failed=0 stop_failed=0
  rollback_original_status="$original_status"
  trap - EXIT INT TERM
  rollback_running=1
  state="rolling-back"
  set +e
  printf 'safe-update: candidate failed; restoring the pre-update checkpoint and previous image\n' >&2
  if ! validate_docker_isolation_authority; then
    printf 'safe-update: Docker isolation authority could not be authenticated; container stop and network disconnect states are unverified and recovery artifacts are preserved\n' >&2
    exit "$original_status"
  fi
  if [ -n "$candidate_id" ] && ! stop_container_safely "$candidate_id" candidate; then
    printf 'safe-update: candidate could not be safely stopped\n' >&2
    isolation_failed=1; stop_failed=1
  fi
  if [ -n "$current_id" ] && ! stop_container_safely "$current_id" current; then
    printf 'safe-update: current container could not be safely stopped\n' >&2
    isolation_failed=1; stop_failed=1
  fi
  if ! validate_network_isolation_parsers; then
    if [ "$stop_failed" -eq 0 ]; then
      printf 'safe-update: containers are stopped, but the pinned network parser changed; network disconnect state is unverified, data is untouched, and recovery artifacts are preserved\n' >&2
    else
      printf 'safe-update: the pinned network parser changed; one or more container stop states and the network disconnect state are unverified, data is untouched, and recovery artifacts are preserved\n' >&2
    fi
    exit "$original_status"
  fi
  if [ "$candidate_ingress_state" != never ] && { [ -z "$candidate_id" ] || ! disconnect_candidate_ingress "$candidate_id"; }; then
    printf 'safe-update: candidate could not be disconnected from the pinned ingress network\n' >&2
    isolation_failed=1
  fi
  if [ "$isolation_failed" -ne 0 ]; then
    printf 'safe-update: rollback isolation could not be proven; one or more container stop/disconnect states are unverified, data is untouched, and recovery artifacts are preserved\n' >&2
    exit "$original_status"
  fi
  if ! validate_pinned_toolchain || ! validate_pinned_docker_socket || ! validate_compose_plugin_boundary; then
    printf 'safe-update: containers are stopped and ingress-isolated, but the complete recovery toolchain changed; data is untouched and recovery artifacts are preserved\n' >&2
    exit "$original_status"
  fi
  state="rollback-stopped"
  rollback_journal_or_stop rollback-stopped
  if ! validate_recovery_bundle; then
    printf 'safe-update: durable recovery bundle is incomplete or changed; lock, journal, recovery bundle, and last known-good pins are preserved\n' >&2
    exit "$original_status"
  fi
  if ! validate_pinned_directory "$project_dir" "Compose project directory" "$project_dir_device" "$project_dir_inode" "$project_dir_nlink" ||
     ! validate_pinned_file "$attestation_file" "update attestation" "$attestation_device" "$attestation_inode" "$attestation_nlink" "$attestation_hash" root ||
     ! validate_pinned_file "$attestation_pin" "pinned update attestation" "$attestation_pin_device" "$attestation_pin_inode" "$attestation_pin_nlink" "$attestation_pin_hash" ||
     ! validate_pinned_file "$env_file_pin" "pinned update env file" "$env_pin_device" "$env_pin_inode" "$env_pin_nlink" "$env_pin_hash" ||
     ! validate_pinned_file "$compose_snapshot" "Compose config snapshot" "$compose_snapshot_device" "$compose_snapshot_inode" "$compose_snapshot_nlink" "$compose_snapshot_hash" ||
     ! validate_secret_sources; then
    printf 'safe-update: trusted Compose/attestation inputs changed; service remains stopped\n' >&2
    cleanup_private_state; exit "$original_status"
  fi
  if ! validate_pinned_file "$compose_file" "Compose file" "$compose_device" "$compose_inode" "$compose_nlink" "$compose_hash"; then
    compose_source_changed=1
    printf 'safe-update: Compose source changed; rollback will use the pinned resolved snapshot\n' >&2
  fi
  if [ "$candidate_ingress_state" != never ]; then
    if ! quarantine_candidate_data; then
      printf 'safe-update: candidate may have received ingress traffic and its data could not be quarantined safely; automatic restore is prohibited\n' >&2
      cleanup_private_state; exit "$original_status"
    fi
    if ! restore_original_env; then
      printf 'safe-update: candidate data was quarantined, but the original env could not be restored\n' >&2
    fi
    state="manual-reconciliation-required"
    rollback_journal_or_stop manual-reconciliation-required
    printf 'safe-update: candidate ingress was connected or uncertain; pre-update data was not restored automatically. Reconcile %s against %s manually.\n' "$failed_candidate_data" "$archive_path" >&2
    cleanup_private_state; exit "$original_status"
  fi
  if ! restore_checkpoint; then
    printf 'safe-update: automatic data restore failed; service remains stopped. Recovery artifacts: %s\n' "$checkpoint_dir" >&2
    cleanup_private_state; exit "$original_status"
  fi
  # Even when failure happened before the archive became durable, never bind a
  # recreated service to a replaced data path.
  if { [ "$checkpoint_ready" -eq 0 ] && ! validate_pinned_directory "$data_dir" "live data directory before rollback start" \
       "$data_dir_device" "$data_dir_inode" "$data_dir_nlink" data; } ||
     ! validate_source_tree "$data_dir" "live data tree before rollback start"; then
    printf 'safe-update: live data path identity changed; service remains stopped\n' >&2
    cleanup_private_state; exit "$original_status"
  fi
  state="restored"
  rollback_journal_or_stop data-restored
  if ! restore_original_env; then
    printf 'safe-update: pre-update env file could not be restored; service remains stopped\n' >&2
    cleanup_private_state; exit "$original_status"
  fi
  candidate_id=""
  if [ "$legacy_current" -eq 1 ] && legacy_state="$(container_state "$current_id" 2>/dev/null)"; then
    case "$legacy_state" in created|exited|dead) reuse_legacy=1 ;; *) reuse_legacy=0 ;; esac
  fi
  if [ "$reuse_legacy" -eq 1 ]; then
    # A failure before the explicit migration removal can restart the exact
    # attested legacy container.  This is safer than asking the fixed project
    # to recreate a colliding container name.
    validate_runtime_contract "$current_id" "legacy rollback container" || {
      printf 'safe-update: retained legacy runtime no longer matches its captured contract; service remains stopped\n' >&2
      cleanup_private_state; exit "$original_status"
    }
    old_id="$current_id"
    if ! disconnect_all_networks "$old_id"; then
      printf 'safe-update: retained legacy runtime could not be isolated before restart; service remains stopped\n' >&2
      cleanup_private_state; exit "$original_status"
    fi
    state="rollback-isolated"
    rollback_journal_or_stop rollback-isolated
  fi
  if ! prepare_rollback_definition; then
    printf 'safe-update: rollback Compose snapshot/recovery bundle could not be prepared; recovery artifacts and last known-good pins are preserved\n' >&2
    exit "$original_status"
  fi
  compose_definition="$rollback_definition"
  if [ "$reuse_legacy" -eq 0 ]; then
    rollback_journal_or_stop rollback-create
    if ! create_disconnected_container "$rollback_image" "$old_image_id" "rollback"; then
      if [ -n "$candidate_id" ]; then stop_container_safely "$candidate_id" rollback >/dev/null 2>&1 || true; fi
      printf 'safe-update: previous container could not be recreated. Checkpoint: %s\n' "$checkpoint_dir" >&2
      cleanup_private_state; exit "$original_status"
    fi
    old_id="$candidate_id"
    state="rollback-isolated"
    rollback_journal_or_stop rollback-isolated
  fi
  if ! docker_cli start "$old_id" >/dev/null || ! validate_container_user "$old_id" "rollback container" || ! wait_for_health "$old_id"; then
    stop_container_safely "$old_id" rollback >/dev/null 2>&1 || true
    printf 'safe-update: previous image did not become healthy after data restore. Service remains isolated. Checkpoint: %s\n' "$checkpoint_dir" >&2
    cleanup_private_state; exit "$original_status"
  fi
  state="rollback-healthy"
  rollback_journal_or_stop rollback-healthy
  if ! validate_network_contract || ! docker_cli network connect --ip "$network_ip" "$network_name" "$old_id" >/dev/null; then
    stop_container_safely "$old_id" rollback >/dev/null 2>&1 || true
    printf 'safe-update: previous image is healthy but could not be reconnected to the pinned ingress network\n' >&2
    cleanup_private_state; exit "$original_status"
  fi
  if [ "$(validate_single_network_ip "$old_id")" != "$network_ip" ]; then
    stop_container_safely "$old_id" rollback >/dev/null 2>&1 || true
    printf 'safe-update: rollback container was reconnected with an unexpected network identity\n' >&2
    cleanup_private_state; exit "$original_status"
  fi
  if [ "$capacity_reservation_released" -eq 0 ] && ! release_capacity_reservation; then
    printf 'safe-update: rollback service recovered but capacity reservation could not be released durably\n' >&2
    cleanup_private_state; exit "$original_status"
  fi
  rollback_journal_or_stop rolled-back
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
  [ "$current_uid" = 0 ] || fail "safe-update must be run as root"
  [ -z "${OMNI_UPDATE_CHECKPOINT_DIR+x}" ] || fail "OMNI_UPDATE_CHECKPOINT_DIR is not supported; checkpoint root is attestation-bound"
  validate_trusted_toolchain
  validate_local_docker
  clear_compose_environment
  [ -z "${TAR_OPTIONS+x}" ] || fail "TAR_OPTIONS must be unset for the trusted archive contract"
  (( BASH_VERSINFO[0] >= 3 )) || fail "safe-update requires Bash 3.2 or newer"
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
  active_env_device="$env_device"; active_env_inode="$env_inode"; active_env_nlink="$env_nlink"; active_env_hash="$env_hash"
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
  configure_compose_plugin_boundary || fail "could not establish the trusted Docker Compose plugin boundary"
  pin_nlink="$(stat_nlink "$pin_dir")"
  # Capture project-directory link count only after creating our own lock and
  # pin subdirectories; those expected entries must not look like a swap.
  project_dir_device="$(stat_device "$project_dir")"; project_dir_inode="$(stat_inode "$project_dir")"; project_dir_nlink="$(stat_nlink "$project_dir")"
  env_file_pin="$pin_dir/environment"; create_exclusive_file "$env_file_pin" || fail "could not create exclusive update env pin"; cp -p -- "$env_file" "$env_file_pin"; chmod 0400 "$env_file_pin"
  validate_existing_file "$env_file_pin" "pinned update env file"
  env_pin_device="$(stat_device "$env_file_pin")"; env_pin_inode="$(stat_inode "$env_file_pin")"; env_pin_nlink="$(stat_nlink "$env_file_pin")"; env_pin_hash="$(sha256_file "$env_file_pin")"
  [ "$env_pin_hash" = "$env_hash" ] || fail "update env file changed while it was being pinned"
  validate_compose_env_syntax "$env_file_pin" || fail "update env file must use the safe single-line dotenv subset"
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
    (($s.user | tostring) == "10001:10001") and (($s.group_add // []) | length == 0) and
    ($s.runtime == "runc") and (($s.gpus // []) | length == 0) and
    (($s.device_cgroup_rules // []) | length == 0) and
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
    and (((.networks.pangolin_target.ipam.config // []) | length) == 1)
    and (.networks.pangolin_target.ipam.config[0].subnet | type == "string" and length > 0)
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
  if current_id="$(compose_single_id "$target_image" "current Compose ps")"; then
    :
  else
    discover_legacy_current || fail "current container could not be discovered in the fixed or verified legacy Compose project"
  fi
  current_image_id="$(container_image_id "$current_id")"
  mounts="$(container_mounts "$current_id")"
  data_mount_count="$(jq '[.[] | select(.Destination == "/app/data")] | length' <<< "$mounts")"; [ "$data_mount_count" -eq 1 ] || fail "current container must have exactly one /app/data mount"
  data_dir="$(jq -er '.[] | select(.Destination == "/app/data") | .Source' <<< "$mounts")"; [ "$data_dir" = "$configured_data_dir" ] || fail "resolved Compose data source does not match the running /app/data source"
  validate_data_directory "$data_dir" "live data directory"
  validate_source_tree "$data_dir" "live data tree"
  data_dir_device="$(stat_device "$data_dir")"; data_dir_inode="$(stat_inode "$data_dir")"; data_dir_nlink="$(stat_nlink "$data_dir")"
  active_data_location="$data_dir"; active_data_device="$data_dir_device"; active_data_inode="$data_dir_inode"; active_data_nlink="$data_dir_nlink"
  expected_network_name="$(jq -er '.networks.pangolin_target.name' "$compose_snapshot")"
  expected_ip="$(jq -er '.services["omni-money"].networks.pangolin_target.ipv4_address' "$compose_snapshot")"
  network_subnet="$(jq -er '.networks.pangolin_target.ipam.config[0].subnet' "$compose_snapshot")"
  network_name="$expected_network_name"
  capture_network_contract || fail "Docker network could not be pinned to the resolved internal bridge/IPAM contract"
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
      "reservation-release-prepared", "reservation-released",
      "legacy-migration-remove",
      "candidate-isolated", "candidate-healthy", "env-install", "env-staged",
      "env-installed", "network-connect", "network-connected", "committed",
      "rollback-start", "rollback-stopped", "restore-moving", "restore-data-parked",
      "restore-empty", "restore-extraction-failed", "restore-original-reinstated", "restore-extracted", "restore-complete", "data-restored",
      "env-restore-staged", "env-restored", "rollback-create", "rollback-restart-legacy", "rollback-isolated",
      "rollback-healthy", "rolled-back", "manual-reconciliation-quarantine", "manual-reconciliation-required"))' "$checkpoint_root_journal" >/dev/null || fail "durable update journal has an invalid recovery phase; manual recovery is required"
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
  rollback_image="omni-money:rollback-$timestamp"; docker_cli pull "$target_image" >/dev/null; target_image_id="$(docker_cli image inspect --format '{{.Id}}' "$target_image")"; docker_cli tag "$current_image_id" "$rollback_image"; prepare_rollback_definition || fail "could not persist the durable rollback Compose snapshot"; write_journal images-pinned
  # Nothing that can mutate the live deployment happens before this point.
  # Arm the EXIT state machine immediately before the direct stop of the
  # pinned current container so a signal/partial stop cannot leave it without
  # an automatic recovery attempt.
  validate_pinned_toolchain || fail "trusted toolchain changed before stop"
  validate_pinned_docker_socket || fail "Docker socket changed before stop"
  validate_network_contract || fail "Docker network changed before the current service was stopped"
  write_journal stopping
  rollback_armed=1; state="armed"; docker_cli stop --time 30 "$current_id" >/dev/null
  case "$(container_state "$current_id")" in
    created|exited|dead) ;;
    *) fail "pinned current container did not reach an accepted stopped state"; return 1 ;;
  esac
  state="stopped"; write_journal stopped
  validate_pinned_directory "$data_dir" "live data directory" "$data_dir_device" "$data_dir_inode" "$data_dir_nlink" data; validate_source_tree "$data_dir" "live data tree"; validate_pinned_directory "$checkpoint_root" "checkpoint root" "$checkpoint_root_device" "$checkpoint_root_inode" "$checkpoint_root_nlink"
  write_journal checkpoint-start
  archive_tmp="$checkpoint_dir/.data.tar.tmp.$$"; create_exclusive_file "$archive_tmp" || fail "could not create exclusive archive staging file"; [ "$(stat_device "$archive_tmp")" = "$checkpoint_root_device" ] || fail "archive staging file is not on the attested checkpoint filesystem"; validate_tar_binary; tar --one-file-system --numeric-owner -cpf "$archive_tmp" -C "$data_dir" .; fsync_path "$archive_tmp"; validate_pinned_directory "$data_dir" "live data directory after archive" "$data_dir_device" "$data_dir_inode" "$data_dir_nlink" data || fail "live data directory changed while checkpoint was created"; validate_pinned_directory "$checkpoint_root" "checkpoint root after archive" "$checkpoint_root_device" "$checkpoint_root_inode" "$checkpoint_root_nlink" || fail "checkpoint root changed while checkpoint was created"; validate_pinned_directory "$checkpoint_dir" "checkpoint directory after archive" "$checkpoint_dir_device" "$checkpoint_dir_inode" "$checkpoint_dir_nlink" || fail "checkpoint directory changed while checkpoint was created"; validate_tar_members "$archive_tmp" || fail "new checkpoint archive contains unsafe members"; move_exclusive_file "$archive_tmp" "$archive_path" || fail "could not install checkpoint archive exclusively"; archive_device="$(stat_device "$archive_path")"; archive_inode="$(stat_inode "$archive_path")"; archive_nlink="$(stat_nlink "$archive_path")"; archive_hash="$(sha256_file "$archive_path")"; fsync_path "$archive_path"; fsync_directory "$checkpoint_dir"; write_journal checkpoint-archived
  checksum_tmp="$checkpoint_dir/.data.tar.sha256.tmp.$$"; create_exclusive_file "$checksum_tmp" || fail "could not create exclusive checksum staging file"; [ "$(stat_device "$checksum_tmp")" = "$checkpoint_root_device" ] || fail "checksum staging file is not on the attested checkpoint filesystem"; sha256sum -- "$archive_path" > "$checksum_tmp"; fsync_path "$checksum_tmp"; move_exclusive_file "$checksum_tmp" "$archive_path.sha256" || fail "could not install checkpoint checksum exclusively"; fsync_path "$archive_path.sha256"; fsync_directory "$checkpoint_dir"; sha256sum --check "$archive_path.sha256" >/dev/null
  archive_device="$(stat_device "$archive_path")"; archive_inode="$(stat_inode "$archive_path")"; archive_nlink="$(stat_nlink "$archive_path")"; archive_hash="$(sha256_file "$archive_path")"; checksum_device="$(stat_device "$archive_path.sha256")"; checksum_inode="$(stat_inode "$archive_path.sha256")"; checksum_nlink="$(stat_nlink "$archive_path.sha256")"; checksum_hash="$(sha256_file "$archive_path.sha256")"; checkpoint_ready=1
  write_journal checkpoint-durable
  remove_legacy_current || fail "verified legacy Compose container could not be migrated safely"
  create_disconnected_container "$target_image" "$target_image_id" "candidate"; state="candidate-isolated"; write_journal candidate-isolated; docker_cli start "$candidate_id" >/dev/null; validate_container_user "$candidate_id" "candidate container"; wait_for_health "$candidate_id" || fail "candidate did not become healthy while isolated"; jq -e 'length == 0' <<< "$(container_networks "$candidate_id")" >/dev/null || fail "candidate was not fully isolated before reconnect"; write_journal candidate-healthy
  validate_pinned_directory "$pin_dir" "private pin directory" "$pin_device" "$pin_inode" "$pin_nlink"; validate_pinned_directory "$project_dir" "Compose project directory" "$project_dir_device" "$project_dir_inode" "$project_dir_nlink"; validate_pinned_file "$compose_file" "Compose file" "$compose_device" "$compose_inode" "$compose_nlink" "$compose_hash"; validate_pinned_file "$compose_file_pin" "pinned Compose definition" "$compose_pin_device" "$compose_pin_inode" "$compose_pin_nlink" "$compose_pin_hash"; validate_pinned_file "$env_file" "update env file" "$env_device" "$env_inode" "$env_nlink" "$env_hash"; validate_pinned_file "$env_file_pin" "pinned update env file" "$env_pin_device" "$env_pin_inode" "$env_pin_nlink" "$env_pin_hash"; validate_pinned_file "$attestation_file" "update attestation" "$attestation_device" "$attestation_inode" "$attestation_nlink" "$attestation_hash" root; validate_pinned_file "$attestation_pin" "pinned update attestation" "$attestation_pin_device" "$attestation_pin_inode" "$attestation_pin_nlink" "$attestation_pin_hash"; validate_pinned_file "$compose_snapshot" "Compose config snapshot" "$compose_snapshot_device" "$compose_snapshot_inode" "$compose_snapshot_nlink" "$compose_snapshot_hash"; validate_secret_sources
  write_journal env-install
  updated_env_tmp="$(dirname -- "$env_file")/.omni-money-env.tmp.$$"; create_exclusive_file "$updated_env_tmp" || fail "could not create exclusive updated env file"; [ "$(stat_device "$updated_env_tmp")" = "$env_device" ] || fail "updated env staging file is not on the env filesystem"; image_line_count="$(grep -Ec '^OMNI_IMAGE=' "$env_file" || true)"; (( image_line_count <= 1 )) || fail "update env file contains duplicate OMNI_IMAGE entries"; if (( image_line_count == 1 )); then sed -E "s#^OMNI_IMAGE=.*#OMNI_IMAGE=$target_image#" "$env_file" > "$updated_env_tmp"; else cp -p -- "$env_file" "$updated_env_tmp"; printf '\nOMNI_IMAGE=%s\n' "$target_image" >> "$updated_env_tmp"; fi; chmod "$env_mode" "$updated_env_tmp"; chown "$env_owner:$env_group" "$updated_env_tmp" || fail "updated env staging ownership could not be preserved"; validate_existing_file "$updated_env_tmp" "updated env staging file"; fsync_path "$updated_env_tmp"; write_journal env-staged; mv -- "$updated_env_tmp" "$env_file"; updated_env_tmp=""; fsync_path "$env_file"; fsync_directory "$(dirname -- "$env_file")"; validate_existing_file "$env_file" "updated env file"; updated_env_device="$(stat_device "$env_file")"; updated_env_inode="$(stat_inode "$env_file")"; updated_env_nlink="$(stat_nlink "$env_file")"; updated_env_hash="$(sha256_file "$env_file")"; active_env_device="$updated_env_device"; active_env_inode="$updated_env_inode"; active_env_nlink="$updated_env_nlink"; active_env_hash="$updated_env_hash"; env_updated=1; write_journal env-installed
  validate_network_contract || fail "Docker network changed before candidate ingress connection"
  candidate_ingress_state="uncertain"; write_journal network-connect
  docker_cli network connect --ip "$network_ip" "$network_name" "$candidate_id" >/dev/null
  candidate_ingress_state="connected"
  [ "$(validate_single_network_ip "$candidate_id")" = "$network_ip" ] || fail "candidate was reconnected with an unexpected network identity or IP"
  validate_network_contract || fail "Docker network changed during candidate ingress connection"
  validate_container_config "$candidate_id" "$target_image_id" running "candidate"; [ "$(container_health "$candidate_id")" = "healthy" ] || fail "candidate lost health while reconnecting ingress"; validate_secret_sources; write_journal network-connected
  release_capacity_reservation || fail "could not durably release rollback capacity after verification"; write_journal committed; remove_journal; update_verified=1; rollback_armed=0; state="verified"; printf 'safe-update: update succeeded with %s\n' "$target_image"; printf 'safe-update: retained checkpoint: %s\n' "$checkpoint_dir"; printf 'safe-update: retained rollback image: %s\n' "$rollback_image"
}

if [[ "${BASH_SOURCE[0]:-$0}" == "$0" ]]; then safe_update_main "$@"; fi
