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

# A normal directory has link count >=2 (and changes as children are added),
# while a regular file in the data tree must not be hard-linked. Exercise both
# sides of that contract explicitly.
mkdir -m 0700 -- "$pin_dir/source-tree" "$pin_dir/source-tree/nested"
printf 'nested\n' > "$pin_dir/source-tree/nested/file"
validate_source_tree "$pin_dir/source-tree" "nested source tree"
ln "$pin_dir/source-tree/nested/file" "$pin_dir/source-tree/hardlink"
assert_rejected "hard-linked source-tree file" validate_source_tree "$pin_dir/source-tree" "hardlink source tree"

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
printf 'at-rest\n' > "$fixture_root/at-rest.json"
printf 'control-key\n' > "$fixture_root/control.key"
chmod 0444 "$fixture_root/at-rest.json"; chmod 0400 "$fixture_root/control.key"

mock_bin="$test_root/mock-bin"; mock_state="$test_root/mock-state"
mkdir -m 0700 -- "$mock_bin" "$mock_state"

cat > "$mock_bin/stat" <<'MOCK_STAT'
#!/usr/bin/env bash
set -Eeuo pipefail
fmt="$2"; if [ "$#" -eq 3 ]; then path="$3"; else path="$4"; fi
case "$fmt:$path" in
  "%u:$MOCK_DATA"|"%u:$MOCK_DATA"/*) echo 10001; exit 0 ;;
  "%g:$MOCK_DATA"|"%g:$MOCK_DATA"/*) echo 10001; exit 0 ;;
  "%a:$MOCK_DATA") echo 700; exit 0 ;;
  "%a:/private/tmp"|"%Lp:/private/tmp") echo 755; exit 0 ;;
  "%a:/tmp"|"%Lp:/tmp") echo 755; exit 0 ;;
  "%a:/var/tmp"|"%Lp:/var/tmp") echo 755; exit 0 ;;
  "%l:"*"/omni-money-update-checkpoints/"*) echo 2; exit 0 ;;
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
container_state() { case "$1" in current) get current_state;; candidate) get candidate_state;; rollback) get rollback_state;; *) echo missing;; esac; }
container_networks() {
  case "$1:$(get net_$1)" in
    current:connected|candidate:connected|rollback:connected) echo '{"omni-money-pangolin":{"IPAddress":"172.30.240.2"}}' ;;
    candidate:extra) echo '{"omni-money-pangolin":{"IPAddress":"172.30.240.2"},"unexpected":{}}' ;;
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
      case "$(get phase)" in current) echo current;; candidate) echo candidate;; rollback) echo rollback;; esac;;
    up)
      if [[ "$OMNI_IMAGE" == *rollback-* ]]; then put phase rollback; put rollback_state created; put net_rollback connected
      else put phase candidate; put candidate_state created; put net_candidate connected; [ "$scenario" = extra_network ] && put net_candidate extra; case "$scenario" in env_swap) printf '# swapped during compose up\n' >> "$MOCK_ENV";; config_swap) printf '# swapped during compose up\n' >> "$MOCK_COMPOSE";; esac; fi;;
  esac; exit 0
fi
case "$1" in
  pull) [ "$scenario" != pull_failure ];;
  tag) exit 0;;
  image) [ "$2" = inspect ]; echo sha256:target;;
  inspect)
    fmt="$3"; id="$4"
    case "$fmt" in
      *State.Status*) container_state "$id";;
      *State.Health*) if [ "$id" = candidate ] && { [ "$scenario" = candidate_failure ] || [ "$scenario" = rollback_failure ]; }; then echo missing; else echo healthy; fi;;
      *'.Image}'*) case "$id" in current|rollback) echo sha256:old;; candidate) echo sha256:target;; esac;;
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
    if [ "$scenario" = signal_int ] || [ "$scenario" = signal_term ]; then
      if [ ! -e "$state_dir/signal_sent" ]; then : > "$state_dir/signal_sent"; state_file="$id""_state"; put "$state_file" exited; [ "$scenario" = signal_int ] && kill -INT "$PPID" || kill -TERM "$PPID"; fi
    fi
    state_file="$id""_state"; put "$state_file" exited
    if [ "$id" = current ] && [ "$scenario" = partial_stop ] && [ ! -e "$state_dir/partial_sent" ]; then : > "$state_dir/partial_sent"; exit 23; fi;;
  start)
    id="$2"
    if [ "$id" = candidate ] && { [ "$scenario" = candidate_failure ] || [ "$scenario" = rollback_failure ]; }; then put candidate_state exited; else state_file="$id""_state"; put "$state_file" running; fi;;
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

run_case() {
  local scenario="$1" expected="$2" output status=0 original_env_hash
  local -a scenario_env=(SAFE_UPDATE_TEST_SCENARIO="$scenario")
  [ "$scenario" = checkpoint_env ] && scenario_env=(OMNI_UPDATE_CHECKPOINT_DIR=/tmp/attacker-controlled)
  find "$mock_state" -mindepth 1 ! -name config.json -exec rm -f -- {} +
  printf current > "$mock_state/phase"; printf running > "$mock_state/current_state"; printf connected > "$mock_state/net_current"; : > "$mock_state/log"
  chmod 0600 "$fixture_root/.env" "$fixture_root/compose.yaml"
  printf 'OMNI_DATA_DIR=%s\nOMNI_IMAGE=omni-money:old\nOMNI_UPDATE_ATTESTATION_FILE=%s\n' "$fixture_root/data" "$fixture_root/attestation.json" > "$fixture_root/.env"
  printf 'fixture compose\n' > "$fixture_root/compose.yaml"
  original_env_hash="$(sha256_file "$fixture_root/.env")"
  output="$(env PATH="$mock_bin:$PATH" MOCK_STATE_DIR="$mock_state" MOCK_SCENARIO="$scenario" MOCK_LOG="$mock_state/log" MOCK_CONFIG="$mock_state/config.json" MOCK_DATA="$fixture_root/data" MOCK_ATTESTATION="$fixture_root/attestation.json" MOCK_AT_REST="$fixture_root/at-rest.json" MOCK_CONTROL_KEY="$fixture_root/control.key" MOCK_ENV="$fixture_root/.env" MOCK_COMPOSE="$fixture_root/compose.yaml" OMNI_UPDATE_HEALTH_TIMEOUT_SECONDS=30 "${scenario_env[@]}" "$fixture_root/scripts/safe-update.sh" registry.example/omni-money:1.2.3 2>&1)" || status=$?
  if [ "$status" -ne "$expected" ]; then printf 'FAIL: scenario %s returned %s (expected %s)\n%s\nmock log:\n' "$scenario" "$status" "$expected" "$output" >&2; sed -n '1,120p' "$mock_state/log" >&2; exit 1; fi
  if [ "$scenario" = checkpoint_env ] || [ "$scenario" = pull_failure ] || [ "$scenario" = compose_config_failure ]; then
    [ "$(cat "$mock_state/current_state")" = running ] && [ "$(cat "$mock_state/phase")" = current ] || { echo "FAIL: $scenario stopped current before a pre-stop failure" >&2; exit 1; }
    [ "$(sha256_file "$fixture_root/.env")" = "$original_env_hash" ] || { echo "FAIL: $scenario changed the env before stop" >&2; exit 1; }
    return 0
  fi
  [ "$(cat "$mock_state/current_state")" = exited ] || { echo "FAIL: $scenario did not stop the pinned current container" >&2; exit 1; }
  if [ "$scenario" = success ]; then
    grep -q 'update succeeded' <<< "$output" || { echo "FAIL: success scenario did not verify update" >&2; exit 1; }
    [ "$(cat "$mock_state/phase")" = candidate ] && [ "$(cat "$mock_state/candidate_state")" = running ] && [ "$(cat "$mock_state/net_candidate")" = connected ] || { echo "FAIL: success candidate was not healthy and connected" >&2; exit 1; }
    grep -q 'OMNI_IMAGE=registry.example/omni-money:1.2.3' "$fixture_root/.env" || { echo "FAIL: success did not persist the new image" >&2; exit 1; }
  elif [ "$scenario" = network_reconnect_failure ] || [ "$scenario" = rollback_failure ] || [ "$scenario" = network_disconnect_failure ]; then
    [ "$(cat "$mock_state/phase")" = rollback ] && [ "$(cat "$mock_state/rollback_state")" != running ] || { echo "FAIL: $scenario left rollback running (phase=$(cat "$mock_state/phase") state=$(cat "$mock_state/rollback_state"))" >&2; sed -n '1,120p' "$mock_state/log" >&2; exit 1; }
  else
    [ "$(cat "$mock_state/phase")" = rollback ] && [ "$(cat "$mock_state/rollback_state")" = running ] && [ "$(cat "$mock_state/net_rollback")" = connected ] || { echo "FAIL: $scenario did not complete an isolated rollback" >&2; exit 1; }
    [ "$scenario" != config_swap ] || grep -q 'pinned resolved snapshot' <<< "$output" || { echo "FAIL: config swap was not explicitly reported" >&2; exit 1; }
  fi
  if [ "$scenario" != success ]; then
    [ "$(sha256_file "$fixture_root/.env")" = "$original_env_hash" ] || { echo "FAIL: $scenario did not retain/restore the exact original env" >&2; exit 1; }
  fi
  [ "$scenario" != config_swap ] || grep -q 'swapped during compose up' "$fixture_root/compose.yaml" || { echo "FAIL: config swap marker was unexpectedly lost" >&2; exit 1; }
}

cat > "$mock_state/config.json" <<EOF
{"x-omni-update-attestation-file":"$fixture_root/attestation.json","services":{"omni-money":{"image":"registry.example/omni-money:1.2.3","container_name":"omni-money","read_only":true,"cap_drop":["ALL"],"security_opt":["no-new-privileges:true"],"ports":[],"volumes":[{"type":"bind","source":"$fixture_root/data","target":"/app/data","read_only":false},{"type":"tmpfs","target":"/tmp"}],"secrets":[{"source":"omni_data_at_rest_attestation","target":"omni_data_at_rest_attestation.json"},{"source":"omni_control_database_key","target":"omni_control_database_key"}],"networks":{"pangolin_target":{"ipv4_address":"172.30.240.2"}}}},"secrets":{"omni_data_at_rest_attestation":{"file":"$fixture_root/at-rest.json"},"omni_control_database_key":{"file":"$fixture_root/control.key"}},"networks":{"pangolin_target":{"name":"omni-money-pangolin","internal":true}}}
EOF

run_case success 0
run_case checkpoint_env 1
run_case pull_failure 1
run_case compose_config_failure 18
run_case candidate_failure 1
run_case partial_stop 23
run_case signal_int 130
run_case signal_term 130
run_case network_reconnect_failure 25
run_case network_disconnect_failure 1
run_case rollback_failure 1
run_case env_swap 1
run_case config_swap 1
run_case extra_port 1
run_case extra_network 1
run_case extra_mount 1
echo "safe-update state-machine tests passed"
