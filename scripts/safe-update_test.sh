#!/usr/bin/env bash
set -Eeuo pipefail

# Focused, Docker-free regression tests for the safe-update preflight. The
# update itself is deliberately not run here: that operation requires a live
# Compose deployment and is covered by the deployment smoke test.
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
project_dir="$(cd -- "$script_dir/.." && pwd -P)"
test_base="${SAFE_UPDATE_TEST_ROOT:-$project_dir}"
test_root="$(mktemp -d "$test_base/.safe-update-test.XXXXXX")"
trap 'rm -rf -- "$test_root"' EXIT

# shellcheck disable=SC1091
source "$script_dir/safe-update.sh"
project_dir="$test_root"
current_uid="$(id -u)"

data_dir="$test_root/data"
mkdir -m 0700 -- "$data_dir"
checkpoint_root="$test_root/omni-money-update-checkpoints"

make_checkpoint_root "$checkpoint_root"
validate_existing_directory "$checkpoint_root" "checkpoint root"
test "$(stat_mode "$checkpoint_root")" = 700

if ! path_contains "$data_dir" "$data_dir/inside"; then
  echo "checkpoint root inside live data was accepted" >&2
  exit 1
fi

outside="$test_root/outside"
mkdir -m 0700 -- "$outside"
ln -s -- "$outside" "$test_root/checkpoint-link"
if make_checkpoint_root "$test_root/checkpoint-link"; then
  echo "symbolic-link checkpoint root was accepted" >&2
  exit 1
fi

mkdir -m 0770 -- "$test_root/writable-checkpoints"
if make_checkpoint_root "$test_root/writable-checkpoints"; then
  echo "group-writable checkpoint root was accepted" >&2
  exit 1
fi

printf 'not a directory\n' > "$test_root/not-a-directory"
if make_checkpoint_root "$test_root/not-a-directory"; then
  echo "non-directory checkpoint root was accepted" >&2
  exit 1
fi

if make_checkpoint_root "/tmp/omni-money-update-checkpoints"; then
  echo "dangerous checkpoint root was accepted" >&2
  exit 1
fi

env_file="$test_root/.env"
marker="$test_root/should-not-exist"
printf 'OMNI_UPDATE_CHECKPOINT_DIR=$(touch %s)\n' "$marker" > "$env_file"
chmod 0600 "$env_file"
parsed="$(compose_env_value OMNI_UPDATE_CHECKPOINT_DIR '')"
test "$parsed" = "\$(touch $marker)"
test ! -e "$marker"

compose_log="$test_root/compose-args"
docker() {
  printf '%s\n' "$*" > "$compose_log"
}
compose ps -q omni-money >/dev/null
grep -F -- "compose --env-file $env_file ps -q omni-money" "$compose_log" >/dev/null

mv -- "$env_file" "$test_root/replaced-env"
ln -s -- "$test_root/replaced-env" "$env_file"
if validate_existing_file "$env_file" "update env file"; then
  echo "replaced update env file was accepted" >&2
  exit 1
fi

root_device="$(stat_device "$checkpoint_root")"
root_inode="$(stat_inode "$checkpoint_root")"
validate_pinned_directory "$checkpoint_root" "checkpoint root" "$root_device" "$root_inode"
mv -- "$checkpoint_root" "$test_root/replaced-checkpoints"
mkdir -m 0700 -- "$checkpoint_root"
if validate_pinned_directory "$checkpoint_root" "checkpoint root" "$root_device" "$root_inode"; then
  echo "replaced checkpoint root identity was accepted" >&2
  exit 1
fi

echo "safe-update preflight tests passed"
