#!/usr/bin/env bash
set -Eeuo pipefail

# Docker-free safe-update regression suite. Linux runs the real updater through
# a mock Docker/Compose state machine; other hosts run portable preflight tests.

script_dir="$(cd -- "$(dirname -- "$0")" && pwd -P)"
project_dir="$(cd -- "$script_dir/.." && pwd -P)"
test_root="$(mktemp -d "$project_dir/.omni-safe-update-test.XXXXXX")"
trap 'rm -rf -- "$test_root"' EXIT

assert_rejected() {
  local label="$1"; shift
  if "$@" >/dev/null 2>&1; then printf 'FAIL: %s was accepted\n' "$label" >&2; exit 1; fi
}

# shellcheck disable=SC1091
source "$script_dir/safe-update.sh"
pin_dir="$test_root/pin"; mkdir -m 0700 -- "$pin_dir"

if grep -Eq '^[[:space:]]*source[[:space:]]+' "$script_dir/safe-update.sh"; then
  echo "FAIL: safe-update contains a shell env-file source" >&2; exit 1
fi
assert_rejected "root checkpoint" reject_dangerous_target "/"
assert_rejected "tmp checkpoint" reject_dangerous_target "/tmp"
assert_rejected "parent traversal" reject_path_syntax "$test_root/../escape"
path_contains "$test_root" "$test_root/child"
assert_rejected "ambiguous sibling prefix" path_contains "$test_root/data" "$test_root/database"

mkdir -m 0700 -- "$pin_dir/source"
printf 'ok\n' > "$pin_dir/source/regular"
tar -C "$pin_dir/source" -cf "$pin_dir/regular.tar" .
validate_tar_members "$pin_dir/regular.tar"
printf 'newline\n' > "$pin_dir/source/$'bad\nname'"
tar -C "$pin_dir/source" -cf "$pin_dir/newline.tar" .
assert_rejected "newline archive member" validate_tar_members "$pin_dir/newline.tar"
ln -s regular "$pin_dir/source/link"
tar -C "$pin_dir/source" -cf "$pin_dir/symlink.tar" .
assert_rejected "symlink archive member" validate_tar_members "$pin_dir/symlink.tar"
rm -- "$pin_dir/source/link"
ln "$pin_dir/source/regular" "$pin_dir/source/hardlink"
tar -C "$pin_dir/source" -cf "$pin_dir/hardlink.tar" .
assert_rejected "hardlink archive member" validate_tar_members "$pin_dir/hardlink.tar"
if command -v mkfifo >/dev/null 2>&1; then
  mkfifo "$pin_dir/source/fifo"
  tar -C "$pin_dir/source" -cf "$pin_dir/fifo.tar" .
  assert_rejected "FIFO archive member" validate_tar_members "$pin_dir/fifo.tar"
  rm -f -- "$pin_dir/source/fifo"
fi
if [ "$(id -u)" = 0 ] && command -v mknod >/dev/null 2>&1; then
  mknod "$pin_dir/source/device" c 1 7
  tar -C "$pin_dir/source" -cf "$pin_dir/device.tar" .
  assert_rejected "device archive member" validate_tar_members "$pin_dir/device.tar"
  rm -f -- "$pin_dir/source/device"
fi

# A normal directory has link count >=2 (and changes as children are added),
# while a regular file in the data tree must not be hard-linked. Exercise both
# sides of that contract explicitly.
test_data_uid="$(id -u)"; test_data_gid="$(stat_group "$pin_dir")"; data_uid="$test_data_uid"; data_gid="$test_data_gid"
mkdir -m 0700 -- "$pin_dir/source-tree" "$pin_dir/source-tree/nested"
printf 'nested\n' > "$pin_dir/source-tree/nested/file"
validate_source_tree "$pin_dir/source-tree" "nested source tree"
ln "$pin_dir/source-tree/nested/file" "$pin_dir/source-tree/hardlink"
assert_rejected "hard-linked source-tree file" validate_source_tree "$pin_dir/source-tree" "hardlink source tree"
data_uid="10001"; data_gid="10001"

printf 'not a tar archive\n' > "$pin_dir/corrupt.tar"
assert_rejected "corrupt archive" validate_tar_members "$pin_dir/corrupt.tar"

if [ "$(uname -s)" != "Linux" ]; then
  echo "safe-update portable preflight tests passed (Linux state-machine tests skipped)"; exit 0
fi

fixture_root="$test_root/fixture"
mkdir -m 0700 -- "$fixture_root" "$fixture_root/scripts"
cp -- "$script_dir/safe-update.sh" "$fixture_root/scripts/safe-update.sh"
chmod 0700 "$fixture_root/scripts/safe-update.sh"
mkdir -m 0700 -- "$fixture_root/data"
printf 'fixture ledger\n' > "$fixture_root/data/ledger.txt"
touch "$fixture_root/compose.yaml"
printf 'OMNI_DATA_DIR=%s\nOMNI_IMAGE=omni-money:old\nOMNI_UPDATE_ATTESTATION_FILE=%s\n' "$fixture_root/data" "$fixture_root/attestation.json" > "$fixture_root/.env"
chmod 0600 "$fixture_root/.env" "$fixture_root/compose.yaml"
cat > "$fixture_root/attestation.json" <<EOF
{"version":1,"protection":"external-encrypted-volume","encrypted_volume_root":"$fixture_root","data_root":"$fixture_root/data","checkpoint_root":"$fixture_root/omni-money-update-checkpoints"}
EOF
chmod 0444 "$fixture_root/attestation.json"
printf 'at-rest-secret-value\n' > "$fixture_root/at-rest.json"
printf 'control-key\n' > "$fixture_root/control.key"
chmod 0444 "$fixture_root/at-rest.json"; chmod 0440 "$fixture_root/control.key"

mock_bin="$test_root/mock-bin"; mock_state="$test_root/mock-state"
mkdir -m 0700 -- "$mock_bin" "$mock_state"

cat > "$mock_bin/stat" <<'MOCK_STAT'
#!/usr/bin/env bash
set -Eeuo pipefail
fmt="$2"; if [ "$#" -eq 3 ]; then path="$3"; else path="$4"; fi
case "$fmt:$path" in
  "%u:$MOCK_DATA"|"%u:$MOCK_DATA"/*) echo 10001; exit 0 ;;
  "%g:$MOCK_DATA"|"%g:$MOCK_DATA"/*) echo 10001; exit 0 ;;
  "%u:$MOCK_FIXTURE"|"%u:$MOCK_FIXTURE"/*) echo 0; exit 0 ;;
  "%g:$MOCK_FIXTURE"|"%g:$MOCK_FIXTURE"/*) echo 0; exit 0 ;;
  "%d:$MOCK_FIXTURE"|"%d:$MOCK_FIXTURE"/*) echo 1; exit 0 ;;
  "%i:$MOCK_FIXTURE"|"%i:$MOCK_FIXTURE"/*) echo 42; exit 0 ;;
  "%a:$MOCK_DATA") echo 700; exit 0 ;;
  "%a:/private/tmp"|"%Lp:/private/tmp") echo 755; exit 0 ;;
  "%a:/tmp"|"%Lp:/tmp") echo 755; exit 0 ;;
  "%a:/var/tmp"|"%Lp:/var/tmp") echo 755; exit 0 ;;
  "%l:"*"/omni-money-update-checkpoints"|"%l:"*"/omni-money-update-checkpoints/"*) echo 2; exit 0 ;;
  "%u:$MOCK_AT_REST"|"%u:$MOCK_CONTROL_KEY"|"%u:"*"/.safe-update-journal"*) echo 0; exit 0 ;;
  "%g:$MOCK_AT_REST"|"%g:"*"/.safe-update-journal"*) echo 0; exit 0 ;;
  "%g:$MOCK_CONTROL_KEY") echo 10001; exit 0 ;;
  "%a:$MOCK_AT_REST") echo 444; exit 0 ;;
  "%a:$MOCK_CONTROL_KEY") echo 440; exit 0 ;;
  "%a:"*"/.safe-update-journal"*) echo 600; exit 0 ;;
  "%u:"*"/recovery"|"%u:"*"/recovery/"*) echo 0; exit 0 ;;
  "%g:"*"/recovery"|"%g:"*"/recovery/"*) echo 0; exit 0 ;;
  "%a:"*"/recovery") echo 700; exit 0 ;;
  "%a:"*"/recovery/"*) echo 400; exit 0 ;;
  "%u:$MOCK_ATTESTATION") echo 0; exit 0 ;;
  "%g:$MOCK_ATTESTATION") echo 0; exit 0 ;;
  "%a:$MOCK_ATTESTATION") echo 444; exit 0 ;;
  "%l:"*"/.omni-money-safe-update-pin."*) echo 2; exit 0 ;;
esac
exec /usr/bin/stat "$@"
MOCK_STAT
chmod 0700 "$mock_bin/stat"

cat > "$mock_bin/docker" <<'MOCK_DOCKER'
#!/usr/bin/env bash
set -Eeuo pipefail
state_dir="$MOCK_STATE_DIR"; scenario="$MOCK_SCENARIO"; log="$MOCK_LOG"
printf '%s\n' "$*" >> "$log"
get() { [ -f "$state_dir/$1" ] && cat "$state_dir/$1" || true; }
put() { printf '%s' "$2" > "$state_dir/$1"; }
container_state() { local value; case "$1" in current) value="$(get current_state)";; candidate) value="$(get candidate_state)";; rollback) value="$(get rollback_state)";; *) value=missing;; esac; [ "$value" = absent ] && return 1; printf '%s' "$value"; }
container_networks() {
  case "$1:$(get net_$1)" in
    current:connected|candidate:connected|rollback:connected) echo '{"omni-money-pangolin":{"IPAddress":"172.30.240.2"}}' ;;
    candidate:extra) echo '{"omni-money-pangolin":{"IPAddress":"172.30.240.2"},"unexpected":{}}' ;;
    *) echo '{}' ;;
  esac
}
container_networks_runtime() {
  case "$1:$(get net_$1)" in
    current:connected|candidate:connected|rollback:connected) echo '{"omni-money-pangolin":{"Aliases":["omni-money"],"IPAddress":"172.30.240.2","GlobalIPv6Address":"","IPAMConfig":{"IPv4Address":"172.30.240.2"}}}' ;;
    candidate:extra) echo '{"omni-money-pangolin":{"Aliases":["omni-money"],"IPAddress":"172.30.240.2","GlobalIPv6Address":"","IPAMConfig":{"IPv4Address":"172.30.240.2"}},"unexpected":{"Aliases":[],"IPAddress":""}}' ;;
    *) echo '{}' ;;
  esac
}
container_mounts() {
  if [ "$1" = candidate ] && [ "$scenario" = extra_mount ]; then
    printf '[{"Type":"bind","Source":"%s","Destination":"/app/data","RW":true},{"Type":"tmpfs","Source":"","Destination":"/tmp","RW":true},{"Type":"bind","Source":"%s","Destination":"/run/secrets/omni_data_at_rest_attestation.json","RW":false},{"Type":"bind","Source":"%s","Destination":"/run/secrets/omni_control_database_key","RW":false},{"Type":"bind","Source":"%s","Destination":"/run/extra","RW":true}]\n' "$MOCK_DATA" "$MOCK_AT_REST" "$MOCK_CONTROL_KEY" "$MOCK_DATA"
  elif [ "$1" = candidate ] && [ "$scenario" = extra_secret ]; then
    printf '[{"Type":"bind","Source":"%s","Destination":"/app/data","RW":true},{"Type":"tmpfs","Source":"","Destination":"/tmp","RW":true},{"Type":"bind","Source":"%s","Destination":"/run/secrets/omni_data_at_rest_attestation.json","RW":false},{"Type":"bind","Source":"%s","Destination":"/run/secrets/omni_control_database_key","RW":false},{"Type":"bind","Source":"%s","Destination":"/run/secrets/unexpected","RW":false}]\n' "$MOCK_DATA" "$MOCK_AT_REST" "$MOCK_CONTROL_KEY" "$MOCK_ATTESTATION"
  else
    printf '[{"Type":"bind","Source":"%s","Destination":"/app/data","RW":true},{"Type":"tmpfs","Source":"","Destination":"/tmp","RW":true},{"Type":"bind","Source":"%s","Destination":"/run/secrets/omni_data_at_rest_attestation.json","RW":false},{"Type":"bind","Source":"%s","Destination":"/run/secrets/omni_control_database_key","RW":false}]\n' "$MOCK_DATA" "$MOCK_AT_REST" "$MOCK_CONTROL_KEY"
  fi
}
if [ "$1" = compose ]; then
  shift; command_name=""; all=0
  while (($#)); do
    case "$1" in
      --env-file|--project-directory|--project-name|-f) shift 2;;
      --all) all=1; shift;;
      config|ps|up) command_name="$1"; shift;;
      *) shift;;
    esac
  done
  case "$command_name" in
    config) [ "$scenario" != compose_config_failure ] || exit 18; cat "$MOCK_CONFIG";;
    ps)
      [ "$all" -eq 1 ] || exit 17
      case "$(get phase)" in
        current) echo current;;
        candidate)
          if [ "$scenario" = unknown_removal ] && [ -e "$state_dir/candidate_ps_seen" ]; then echo unexpected; else echo candidate; : > "$state_dir/candidate_ps_seen"; fi
          ;;
        rollback) echo rollback;;
      esac;;
    up)
      if [[ "$OMNI_IMAGE" == *rollback-* ]]; then put phase rollback; put rollback_state created; put net_rollback connected
      else put phase candidate; put candidate_state created; put net_candidate connected; put current_state absent; [ "$scenario" = extra_network ] && put net_candidate extra; case "$scenario" in env_swap) printf '# swapped during compose up\n' >> "$MOCK_ENV";; config_swap) printf '# swapped during compose up\n' >> "$MOCK_COMPOSE";; secret_inode_replacement) mv -- "$MOCK_AT_REST" "$MOCK_AT_REST.replaced"; printf 'tampered secret\n' > "$MOCK_AT_REST";; esac; fi;;
  esac; exit 0
fi
case "$1" in
  pull) [ "$scenario" != pull_failure ];;
  tag) exit 0;;
  image) [ "$2" = inspect ]; echo sha256:target;;
  inspect)
    fmt="$3"; id="$4"
    case "$fmt" in
      *'{{json .}}'*)
        mounts_json="$(container_mounts "$id")"; networks_json="$(container_networks_runtime "$id")"
        jq -cn --argjson mounts "$mounts_json" --argjson networks "$networks_json" '{Config:{User:"10001:10001",Entrypoint:["/entrypoint"],Cmd:["server"],WorkingDir:"/app",StopSignal:"SIGTERM",Healthcheck:{Test:["CMD","health"],Interval:1000000000,Timeout:1000000000,Retries:3},Env:["APP_MODE=server","SECRET_VALUE=private"],Labels:{"com.example.role":"omni-money"}},HostConfig:{RestartPolicy:{Name:"unless-stopped",MaximumRetryCount:0},NetworkMode:"omni-money-pangolin",ReadonlyRootfs:true,Privileged:false,Init:false,CapDrop:["ALL"],SecurityOpt:["no-new-privileges:true"]},Mounts:$mounts,NetworkSettings:{Networks:$networks}}';;
      *State.Status*) container_state "$id";;
      *State.Health*) if [ "$id" = candidate ] && { [ "$scenario" = candidate_failure ] || [ "$scenario" = candidate_data_mutation ] || [ "$scenario" = rollback_failure ] || [ "$scenario" = rollback_tag_mutation ] || [ "$scenario" = unknown_removal ]; }; then echo missing; else echo healthy; fi;;
      *'.Image}'*) case "$id" in current) echo sha256:old;; rollback) [ "$scenario" = rollback_tag_mutation ] && echo sha256:tampered || echo sha256:old;; candidate) echo sha256:target;; esac;;
      *'.Config.Image}'*) case "$id" in current) echo omni-money:old;; *) echo omni-money:target;; esac;;
      *'.Config.User}'*) echo 10001:10001;;
      *PortBindings*) [ "$id" = candidate ] && [ "$scenario" = extra_port ] && echo '{"4000/tcp":[{"HostPort":"4000"}]}' || echo '{}';;
      *'.Mounts}'*) container_mounts "$id";;
      *'.NetworkSettings.Networks}'*) container_networks "$id";;
      *ReadonlyRootfs*) echo true;;
      *CapDrop*) echo '["ALL"]';;
      *SecurityOpt*) echo '["no-new-privileges:true"]';;
      *'IPAddress'*|*'index .NetworkSettings.Networks'*) [ "$(get net_$id)" = connected ] && echo 172.30.240.2 || true;;
      *) exit 19;;
    esac;;
  exec) [ "$scenario" = bad_user ] && echo 10000 || echo 10001;;
  stop)
    id="$4"
    state_file="${id}_state"
    [ "$(get "$state_file")" != absent ] || exit 1
    if [ "$scenario" = sigkill ]; then put "$state_file" exited; kill -KILL "$PPID"; fi
    if [ "$scenario" = signal_int ] || [ "$scenario" = signal_term ]; then
      if [ ! -e "$state_dir/signal_sent" ]; then : > "$state_dir/signal_sent"; state_file="$id""_state"; put "$state_file" exited; [ "$scenario" = signal_int ] && kill -INT "$PPID" || kill -TERM "$PPID"; fi
    fi
    state_file="$id""_state"; put "$state_file" exited
    if [ "$id" = current ] && [ "$scenario" = partial_stop ] && [ ! -e "$state_dir/partial_sent" ]; then : > "$state_dir/partial_sent"; exit 23; fi;;
  start)
    id="$2"
    if [ "$id" = candidate ] && [ "$scenario" = candidate_data_mutation ]; then printf 'tampered ledger\n' > "$MOCK_DATA/ledger.txt"; put candidate_state exited
    elif [ "$id" = candidate ] && { [ "$scenario" = candidate_failure ] || [ "$scenario" = rollback_failure ] || [ "$scenario" = rollback_tag_mutation ] || [ "$scenario" = unknown_removal ]; }; then put candidate_state exited
    else state_file="$id""_state"; put "$state_file" running; fi;;
  network)
    action="$2"
    case "$action" in
      disconnect) id="$4"; [ "$scenario" != network_disconnect_failure ] || exit 24; put "net_$id" none;;
      connect) id="$6"; if [ "$scenario" = network_reconnect_failure ] || { [ "$scenario" = rollback_failure ] && [ "$id" = rollback ]; }; then exit 25; fi; put "net_$id" connected;;
    esac;;
  *) exit 26;;
esac
MOCK_DOCKER
chmod 0700 "$mock_bin/docker"
cat > "$mock_bin/chown" <<'MOCK_CHOWN'
#!/usr/bin/env bash
exit 0
MOCK_CHOWN
chmod 0700 "$mock_bin/chown"
cat > "$mock_bin/df" <<'MOCK_DF'
#!/usr/bin/env bash
if [ "${MOCK_SCENARIO:-}" = low_space ]; then
  printf 'Filesystem 1024-blocks Used Available Capacity Mounted on\nmock 100 99 1 99%% /\n'
else
  exec /bin/df "$@"
fi
MOCK_DF
chmod 0700 "$mock_bin/df"
cat > "$mock_bin/sleep" <<'MOCK_SLEEP'
#!/usr/bin/env bash
exit 0
MOCK_SLEEP
chmod 0700 "$mock_bin/sleep"
cat > "$mock_bin/sync" <<'MOCK_SYNC'
#!/usr/bin/env bash
printf 'sync %s\n' "$*" >> "$MOCK_LOG"
exit 0
MOCK_SYNC
chmod 0700 "$mock_bin/sync"

run_case() {
  local scenario="$1" expected="$2" output status=0 original_env_hash original_data_hash journal_path stale_status pin_path
  printf 'safe-update state-machine scenario: %s\n' "$scenario" >&2
  local -a scenario_env=(SAFE_UPDATE_TEST_SCENARIO="$scenario")
  [ "$scenario" = checkpoint_env ] && scenario_env=(OMNI_UPDATE_CHECKPOINT_DIR=/tmp/attacker-controlled)
  find "$mock_state" -mindepth 1 ! -name config.json -exec rm -f -- {} +
  if [ -d "$fixture_root/omni-money-update-checkpoints" ] && [ ! -L "$fixture_root/omni-money-update-checkpoints" ]; then rm -rf -- "$fixture_root/omni-money-update-checkpoints"; fi
  printf current > "$mock_state/phase"; printf running > "$mock_state/current_state"; printf connected > "$mock_state/net_current"; : > "$mock_state/log"
  chmod 0600 "$fixture_root/.env" "$fixture_root/compose.yaml"
  chmod 0644 "$fixture_root/at-rest.json"; chmod 0600 "$fixture_root/control.key"
  printf 'OMNI_DATA_DIR=%s\nOMNI_IMAGE=omni-money:old\nOMNI_UPDATE_ATTESTATION_FILE=%s\n' "$fixture_root/data" "$fixture_root/attestation.json" > "$fixture_root/.env"
  printf 'fixture compose\n' > "$fixture_root/compose.yaml"
  printf 'at-rest-secret-value\n' > "$fixture_root/at-rest.json"; printf 'control-key\n' > "$fixture_root/control.key"
  chmod 0444 "$fixture_root/at-rest.json"; chmod 0440 "$fixture_root/control.key"
  rm -f -- "$fixture_root/at-rest.json.replaced"
  original_env_hash="$(sha256_file "$fixture_root/.env")"
  original_data_hash="$(sha256sum -- "$fixture_root/data/ledger.txt" | sed -E 's/[[:space:]].*$//')"
  output="$(env PATH="$mock_bin:$PATH" MOCK_STATE_DIR="$mock_state" MOCK_SCENARIO="$scenario" MOCK_LOG="$mock_state/log" MOCK_CONFIG="$mock_state/config.json" MOCK_FIXTURE="$fixture_root" MOCK_DATA="$fixture_root/data" MOCK_ATTESTATION="$fixture_root/attestation.json" MOCK_AT_REST="$fixture_root/at-rest.json" MOCK_CONTROL_KEY="$fixture_root/control.key" MOCK_ENV="$fixture_root/.env" MOCK_COMPOSE="$fixture_root/compose.yaml" OMNI_UPDATE_HEALTH_TIMEOUT_SECONDS=30 "${scenario_env[@]}" "$fixture_root/scripts/safe-update.sh" registry.example/omni-money:1.2.3 2>&1)" || status=$?
  if [ "$status" -ne "$expected" ]; then printf 'FAIL: scenario %s returned %s (expected %s)\n%s\nmock log:\n' "$scenario" "$status" "$expected" "$output" >&2; sed -n '1,120p' "$mock_state/log" >&2; exit 1; fi
  if grep -Eq 'control-key|at-rest-secret-value' <<< "$output" || grep -Eq 'control-key|at-rest-secret-value' "$mock_state/log"; then
    echo "FAIL: $scenario exposed secret content in output or mock log" >&2
    exit 1
  fi
  if [ "$scenario" = checkpoint_env ] || [ "$scenario" = pull_failure ] || [ "$scenario" = compose_config_failure ] || [ "$scenario" = low_space ]; then
    [ "$(cat "$mock_state/current_state")" = running ] && [ "$(cat "$mock_state/phase")" = current ] || { echo "FAIL: $scenario stopped current before a pre-stop failure" >&2; exit 1; }
    [ "$(sha256_file "$fixture_root/.env")" = "$original_env_hash" ] || { echo "FAIL: $scenario changed the env before stop" >&2; exit 1; }
    return 0
  fi
  if [ "$scenario" = sigkill ]; then
    [ "$(cat "$mock_state/current_state")" = exited ] || { echo "FAIL: SIGKILL did not leave the pinned current container stopped" >&2; exit 1; }
    journal_path="$fixture_root/omni-money-update-checkpoints/.safe-update-journal"
    [ -f "$journal_path" ] && [ -d "$fixture_root/.omni-money-safe-update.lock" ] || { echo "FAIL: SIGKILL did not leave durable recovery state" >&2; exit 1; }
    stale_status=0
env PATH="$mock_bin:$PATH" MOCK_STATE_DIR="$mock_state" MOCK_SCENARIO=stale_lock MOCK_LOG="$mock_state/log" MOCK_CONFIG="$mock_state/config.json" MOCK_FIXTURE="$fixture_root" MOCK_DATA="$fixture_root/data" MOCK_ATTESTATION="$fixture_root/attestation.json" MOCK_AT_REST="$fixture_root/at-rest.json" MOCK_CONTROL_KEY="$fixture_root/control.key" MOCK_ENV="$fixture_root/.env" MOCK_COMPOSE="$fixture_root/compose.yaml" "$fixture_root/scripts/safe-update.sh" registry.example/omni-money:1.2.3 >/dev/null 2>&1 || stale_status=$?
    [ "$stale_status" -ne 0 ] && [ -d "$fixture_root/.omni-money-safe-update.lock" ] || { echo "FAIL: stale SIGKILL lock was not fail-closed" >&2; exit 1; }
    rmdir -- "$fixture_root/.omni-money-safe-update.lock"
    for pin_path in "$fixture_root"/.omni-money-safe-update-pin.*; do [ -d "$pin_path" ] && [ ! -L "$pin_path" ] && rm -rf -- "$pin_path"; done
    rm -rf -- "$fixture_root/omni-money-update-checkpoints"
    return 0
  fi
  if [ "$scenario" = unknown_removal ]; then
    [ "$(cat "$mock_state/current_state")" = absent ] && [ "$(cat "$mock_state/phase")" = candidate ] && [ ! -e "$mock_state/rollback_state" ] || { echo "FAIL: unknown current disappearance was not kept stopped" >&2; exit 1; }
    return 0
  fi
  if [ "$scenario" != partial_stop ] && [ "$scenario" != signal_int ] && [ "$scenario" != signal_term ]; then
    [ "$(cat "$mock_state/current_state")" = absent ] || { echo "FAIL: $scenario did not model Compose removal of the pinned current container (state=$(cat "$mock_state/current_state"))" >&2; printf '%s\n' "$output" >&2; sed -n '1,160p' "$mock_state/log" >&2; exit 1; }
  else
    [ "$(cat "$mock_state/current_state")" = exited ] || { echo "FAIL: partial_stop did not leave the interrupted current container stopped" >&2; exit 1; }
  fi
  if [ "$scenario" = success ]; then
    grep -q 'update succeeded' <<< "$output" || { echo "FAIL: success scenario did not verify update" >&2; exit 1; }
    [ "$(cat "$mock_state/phase")" = candidate ] && [ "$(cat "$mock_state/candidate_state")" = running ] && [ "$(cat "$mock_state/net_candidate")" = connected ] || { echo "FAIL: success candidate was not healthy and connected" >&2; exit 1; }
    grep -q 'OMNI_IMAGE=registry.example/omni-money:1.2.3' "$fixture_root/.env" || { echo "FAIL: success did not persist the new image" >&2; exit 1; }
    grep -Fq 'inspect --format {{json .}} candidate' "$mock_state/log" || { echo "FAIL: success did not compare the candidate runtime contract" >&2; exit 1; }
  elif [ "$scenario" = network_reconnect_failure ] || [ "$scenario" = rollback_failure ] || [ "$scenario" = network_disconnect_failure ] || [ "$scenario" = rollback_tag_mutation ]; then
    [ "$(cat "$mock_state/phase")" = rollback ] && [ "$(cat "$mock_state/rollback_state")" != running ] || { echo "FAIL: $scenario left rollback running (phase=$(cat "$mock_state/phase") state=$(cat "$mock_state/rollback_state"))" >&2; sed -n '1,120p' "$mock_state/log" >&2; exit 1; }
    [ "$scenario" = rollback_tag_mutation ] || grep -Fq 'inspect --format {{json .}} rollback' "$mock_state/log" || { echo "FAIL: $scenario did not compare the rollback runtime contract" >&2; exit 1; }
  elif [ "$scenario" = secret_inode_replacement ]; then
    [ "$(cat "$mock_state/phase")" = candidate ] && [ ! -e "$mock_state/rollback_state" ] || { echo "FAIL: secret inode replacement was not fail-closed before rollback recreation" >&2; exit 1; }
  else
    [ "$(cat "$mock_state/phase")" = rollback ] && [ "$(cat "$mock_state/rollback_state")" = running ] && [ "$(cat "$mock_state/net_rollback")" = connected ] || { echo "FAIL: $scenario did not complete an isolated rollback" >&2; exit 1; }
    grep -Fq 'inspect --format {{json .}} rollback' "$mock_state/log" || { echo "FAIL: $scenario did not compare the rollback runtime contract" >&2; exit 1; }
    [ "$scenario" != config_swap ] || grep -q 'pinned resolved snapshot' <<< "$output" || { echo "FAIL: config swap was not explicitly reported" >&2; exit 1; }
  fi
  if [ "$scenario" != success ]; then
    [ "$(sha256_file "$fixture_root/.env")" = "$original_env_hash" ] || { echo "FAIL: $scenario did not retain/restore the exact original env" >&2; exit 1; }
  fi
  if [ "$scenario" = candidate_data_mutation ]; then
    [ "$(sha256sum -- "$fixture_root/data/ledger.txt" | sed -E 's/[[:space:]].*$//')" = "$original_data_hash" ] || { echo "FAIL: candidate data mutation was not restored byte-for-byte" >&2; exit 1; }
  fi
  [ "$scenario" != config_swap ] || grep -q 'swapped during compose up' "$fixture_root/compose.yaml" || { echo "FAIL: config swap marker was unexpectedly lost" >&2; exit 1; }
}

cat > "$mock_state/config.json" <<EOF
{"name":"omni-money","x-omni-update-attestation-file":"$fixture_root/attestation.json","services":{"omni-money":{"image":"registry.example/omni-money:1.2.3","container_name":"omni-money","read_only":true,"cap_drop":["ALL"],"security_opt":["no-new-privileges:true"],"ports":[],"volumes":[{"type":"bind","source":"$fixture_root/data","target":"/app/data","read_only":false},{"type":"tmpfs","target":"/tmp"}],"secrets":[{"source":"omni_data_at_rest_attestation","target":"omni_data_at_rest_attestation.json"},{"source":"omni_control_database_key","target":"omni_control_database_key"}],"networks":{"pangolin_target":{"ipv4_address":"172.30.240.2"}}}},"secrets":{"omni_data_at_rest_attestation":{"file":"$fixture_root/at-rest.json"},"omni_control_database_key":{"file":"$fixture_root/control.key"}},"networks":{"pangolin_target":{"name":"omni-money-pangolin","internal":true}}}
EOF

run_case success 0
run_case checkpoint_env 1
run_case pull_failure 1
run_case compose_config_failure 18
run_case low_space 1
run_case candidate_failure 1
run_case candidate_data_mutation 1
run_case unknown_removal 1
run_case partial_stop 23
run_case signal_int 130
run_case signal_term 130
run_case network_reconnect_failure 25
run_case network_disconnect_failure 1
run_case rollback_failure 1
run_case rollback_tag_mutation 1
run_case env_swap 1
run_case secret_inode_replacement 1
run_case config_swap 1
run_case extra_port 1
run_case extra_network 1
run_case extra_mount 1
run_case extra_secret 1
run_case sigkill 137
find "$mock_state" -mindepth 1 ! -name config.json -exec rm -f -- {} +
printf current > "$mock_state/phase"; printf running > "$mock_state/current_state"; printf connected > "$mock_state/net_current"; : > "$mock_state/log"
mkdir -m 0700 -- "$fixture_root/omni-money-update-checkpoints"
printf '%s\n' '{"version":1,"phase":"stopping"}' > "$fixture_root/omni-money-update-checkpoints/.safe-update-journal"
chmod 0600 "$fixture_root/omni-money-update-checkpoints/.safe-update-journal"
stale_status=0
env PATH="$mock_bin:$PATH" MOCK_STATE_DIR="$mock_state" MOCK_SCENARIO=stale_journal MOCK_LOG="$mock_state/log" MOCK_CONFIG="$mock_state/config.json" MOCK_FIXTURE="$fixture_root" MOCK_DATA="$fixture_root/data" MOCK_ATTESTATION="$fixture_root/attestation.json" MOCK_AT_REST="$fixture_root/at-rest.json" MOCK_CONTROL_KEY="$fixture_root/control.key" MOCK_ENV="$fixture_root/.env" MOCK_COMPOSE="$fixture_root/compose.yaml" OMNI_UPDATE_HEALTH_TIMEOUT_SECONDS=30 "$fixture_root/scripts/safe-update.sh" registry.example/omni-money:1.2.3 >/dev/null 2>&1 || stale_status=$?
[ "$stale_status" -ne 0 ] && [ -f "$fixture_root/omni-money-update-checkpoints/.safe-update-journal" ] || { echo "FAIL: stale durable journal was not fail-closed" >&2; exit 1; }
rm -rf -- "$fixture_root/omni-money-update-checkpoints"
echo "safe-update state-machine tests passed"
