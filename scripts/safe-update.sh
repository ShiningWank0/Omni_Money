#!/usr/bin/env bash
set -Eeuo pipefail

# Deploy one pinned Omni Money image with an offline checkpoint. The previous
# image and data are restored only if the candidate fails before it becomes
# healthy and is reconnected to the ingress network.

service_name="omni-money"
target_image="${1:-}"
env_file="${OMNI_UPDATE_ENV_FILE:-.env}"
health_timeout="${OMNI_UPDATE_HEALTH_TIMEOUT_SECONDS:-180}"
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

fail() {
  printf 'safe-update: %s\n' "$*" >&2
  return 1
}

compose() {
  docker compose "$@"
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
  [ -n "$candidate_id" ] || fail "candidate container was not created"
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
  sha256sum --check "$archive_path.sha256" >/dev/null || return 1
  [ ! -e "$failed_data" ] || return 1
  mv -- "$data_dir" "$failed_data" || return 1
  mkdir -- "$data_dir" || {
    mv -- "$failed_data" "$data_dir" || true
    return 1
  }
  if ! tar --numeric-owner -xpf "$archive_path" -C "$data_dir"; then
    mv -- "$data_dir" "$incomplete_restore" || true
    mv -- "$failed_data" "$data_dir" || true
    return 1
  fi
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
  if [ -f "$checkpoint_dir/environment.before" ]; then
    cp -p -- "$checkpoint_dir/environment.before" "$env_file" || {
      printf 'safe-update: could not restore the pre-update env file; service remains stopped\n' >&2
      exit "$original_status"
    }
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

trap on_exit EXIT
trap 'exit 130' INT TERM

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
[ -f "$env_file" ] && [ ! -L "$env_file" ] || fail "update env file must be an existing regular non-symlink file: $env_file"

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

data_dir="$(docker inspect --format '{{range .Mounts}}{{if eq .Destination "/app/data"}}{{.Source}}{{end}}{{end}}' "$current_id")"
[ -n "$data_dir" ] && [ -d "$data_dir" ] && [ ! -L "$data_dir" ] || fail "the /app/data bind mount could not be resolved safely"
data_dir="$(cd -- "$data_dir" && pwd -P)"
case "$data_dir" in
  /|/home|/Users|/root|.) fail "refusing unsafe data directory: $data_dir" ;;
esac
data_relative="${data_dir#/}"
case "$data_relative" in
  */*/*) ;;
  *) fail "data directory is too broad for automatic rollback: $data_dir" ;;
esac
project_dir="$(pwd -P)"
[ "$data_dir" != "$project_dir" ] || fail "the Compose project root cannot be used as the data directory"

checkpoint_root="${OMNI_UPDATE_CHECKPOINT_DIR:-$(dirname -- "$data_dir")/omni-money-update-checkpoints}"
mkdir -p -- "$checkpoint_root"
checkpoint_root="$(cd -- "$checkpoint_root" && pwd -P)"
case "$checkpoint_root/" in
  "$data_dir"/*) fail "checkpoint directory must not be inside the live data directory" ;;
esac

timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
checkpoint_dir="$checkpoint_root/$timestamp"
mkdir -- "$checkpoint_dir"
chmod 0700 "$checkpoint_dir"
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
{
  printf 'created_at=%s\n' "$timestamp"
  printf 'data_directory=%s\n' "$data_dir"
  printf 'previous_image=%s\n' "$current_image"
  printf 'previous_image_id=%s\n' "$current_image_id"
  printf 'rollback_image=%s\n' "$rollback_image"
  printf 'candidate_image=%s\n' "$target_image"
} > "$checkpoint_dir/manifest"

compose stop --timeout 30 "$service_name" >/dev/null
rollback_armed=1
tar --numeric-owner -cpf "$archive_path" -C "$data_dir" .
sha256sum "$archive_path" > "$archive_path.sha256"
sha256sum --check "$archive_path.sha256" >/dev/null
sync "$archive_path" "$archive_path.sha256"
checkpoint_ready=1

create_disconnected_container "$target_image"
start_and_validate

# Persist the image only after the isolated candidate is healthy. The env
# file backup remains inside the checkpoint for manual disaster recovery.
env_dir="$(dirname -- "$env_file")"
env_tmp="$(mktemp "$env_dir/.omni-money-env.XXXXXX")"
awk -v image="$target_image" '
  BEGIN { replaced = 0 }
  /^OMNI_IMAGE=/ { if (!replaced) { print "OMNI_IMAGE=" image; replaced = 1 }; next }
  { print }
  END { if (!replaced) print "OMNI_IMAGE=" image }
' "$env_file" > "$env_tmp"
chmod --reference="$env_file" "$env_tmp"
mv -- "$env_tmp" "$env_file"

docker network connect --ip "$network_ip" "$network_name" "$candidate_id" >/dev/null
[ "$(docker inspect --format '{{.State.Health.Status}}' "$candidate_id")" = "healthy" ] || fail "candidate lost health while reconnecting ingress"

update_verified=1
rollback_armed=0
printf 'safe-update: update succeeded with %s\n' "$target_image"
printf 'safe-update: retained checkpoint: %s\n' "$checkpoint_dir"
printf 'safe-update: retained rollback image: %s\n' "$rollback_image"
