#!/usr/bin/env bash
# shellcheck disable=SC2034,SC2218
# The sourced updater intentionally exports state variables and the Linux-only
# fixture later exports a tar shim; portable preflight calls occur before that
# fixture definition.
set -Eeuo pipefail

# Docker-free safe-update regression suite. Linux runs the real updater through
# a mock Docker/Compose state machine; other hosts run portable preflight tests.

script_dir="$(cd -- "$(dirname -- "$0")" && pwd -P)"
project_dir="$(cd -- "$script_dir/.." && pwd -P)"
test_root="$(mktemp -d "${TMPDIR:-/tmp}/.omni-safe-update-test.XXXXXX")"
trap 'rm -rf -- "$test_root"' EXIT

assert_rejected() {
  local label="$1"; shift
  if "$@" >/dev/null 2>&1; then printf 'FAIL: %s was accepted\n' "$label" >&2; exit 1; fi
}

assert_rejected_with() {
  local label="$1" pattern="$2" output
  shift 2
  if output="$("$@" 2>&1)"; then printf 'FAIL: %s was accepted\n' "$label" >&2; exit 1; fi
  grep -Eq "$pattern" <<< "$output" || { printf 'FAIL: %s failed for the wrong reason: %s\n' "$label" "$output" >&2; exit 1; }
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
assert_rejected "registry port without immutable tag" validate_image_reference "registry:5000/image"
assert_rejected "mutable latest tag" validate_image_reference "registry:5000/image:latest"
validate_image_reference "registry:5000/image:1.2.3"
validate_image_reference "registry:5000/image@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

printf '# safe dotenv\nOMNI_IMAGE=registry.example/omni:1.2.3\nEMPTY=\nQUOTED='"'"'single line'"'"'\n' > "$pin_dir-safe-env"
validate_compose_env_syntax "$pin_dir-safe-env"
printf 'MALICIOUS : value\n' > "$pin_dir-colon-env"
assert_rejected "colon dotenv syntax" validate_compose_env_syntax "$pin_dir-colon-env"
printf ' MALICIOUS=value\n' > "$pin_dir-space-env"
assert_rejected "whitespace dotenv syntax" validate_compose_env_syntax "$pin_dir-space-env"
printf "MALICIOUS='first\nsecond'\n" > "$pin_dir-multiline-env"
assert_rejected "multiline single-quoted dotenv" validate_compose_env_syntax "$pin_dir-multiline-env"

mkdir -m 0700 -- "$pin_dir/source"
printf 'ok\n' > "$pin_dir/source/regular"
tar -C "$pin_dir/source" -cf "$pin_dir/regular.tar" .
validate_tar_members "$pin_dir/regular.tar"
tar -cf "$pin_dir/empty.tar" -T /dev/null
assert_rejected "empty archive" validate_tar_members "$pin_dir/empty.tar"
mkdir -m 0700 -- "$pin_dir/archive-newline"
newline_name="$(printf 'bad\nname')"
printf 'newline\n' > "$pin_dir/archive-newline/$newline_name"
tar -C "$pin_dir/archive-newline" -cf "$pin_dir/newline.tar" .
assert_rejected_with "newline archive member" 'unsafe .*member name' validate_tar_members "$pin_dir/newline.tar"
mkdir -m 0700 -- "$pin_dir/archive-symlink"
printf 'target\n' > "$pin_dir/archive-symlink/target"
ln -s target "$pin_dir/archive-symlink/link"
tar -C "$pin_dir/archive-symlink" -cf "$pin_dir/symlink.tar" .
assert_rejected_with "symlink archive member" 'unsupported member type: l' validate_tar_members "$pin_dir/symlink.tar"
mkdir -m 0700 -- "$pin_dir/archive-hardlink"
printf 'hardlink\n' > "$pin_dir/archive-hardlink/one"
ln "$pin_dir/archive-hardlink/one" "$pin_dir/archive-hardlink/two"
tar -C "$pin_dir/archive-hardlink" -cf "$pin_dir/hardlink.tar" .
assert_rejected_with "hardlink archive member" 'unsupported member type: h' validate_tar_members "$pin_dir/hardlink.tar"
if command -v mkfifo >/dev/null 2>&1; then
  mkdir -m 0700 -- "$pin_dir/archive-fifo"
  mkfifo "$pin_dir/archive-fifo/fifo"
  tar -C "$pin_dir/archive-fifo" -cf "$pin_dir/fifo.tar" .
  assert_rejected_with "FIFO archive member" 'unsupported member type: p' validate_tar_members "$pin_dir/fifo.tar"
fi
if [ "$(id -u)" = 0 ] && command -v mknod >/dev/null 2>&1; then
  mkdir -m 0700 -- "$pin_dir/archive-device"
  mknod "$pin_dir/archive-device/device" c 1 7
  tar -C "$pin_dir/archive-device" -cf "$pin_dir/device.tar" .
  assert_rejected_with "device archive member" 'unsupported member type: [bc]' validate_tar_members "$pin_dir/device.tar"
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

# Realistic util-linux JSON may retain children even when callers request a
# list. Both a foreign filesystem and a same-device bind can therefore hide
# below the top-level entry unless the tree is traversed recursively.
cat > "$pin_dir/findmnt.json" <<'EOF'
{"filesystems":[{"target":"/srv","children":[{"target":"/srv/omni/data","children":[{"target":"/srv/omni/data/foreign-fs"},{"target":"/srv/omni/data/same-device-bind"}]}]}]}
EOF
create_exclusive_file "$pin_dir/findmnt-targets.nul"
write_findmnt_targets "$pin_dir/findmnt.json" "$pin_dir/findmnt-targets.nul"
foreign_seen=0; bind_seen=0
while IFS= read -r -d '' mount_target; do
  [ "$mount_target" = /srv/omni/data/foreign-fs ] && foreign_seen=1
  [ "$mount_target" = /srv/omni/data/same-device-bind ] && bind_seen=1
done < "$pin_dir/findmnt-targets.nul"
[ "$foreign_seen" -eq 1 ] && [ "$bind_seen" -eq 1 ] || { echo "FAIL: recursive findmnt targets were omitted" >&2; exit 1; }

# Identity validation must detect same-content inode swaps and type swaps; a
# content-only mock cannot exercise this security boundary.
printf 'same-content\n' > "$pin_dir/identity-file"
identity_device="$(stat_device "$pin_dir/identity-file")"; identity_inode="$(stat_inode "$pin_dir/identity-file")"
identity_nlink="$(stat_nlink "$pin_dir/identity-file")"; identity_hash="$(sha256_file "$pin_dir/identity-file")"
mv -- "$pin_dir/identity-file" "$pin_dir/identity-file.old"
cp -- "$pin_dir/identity-file.old" "$pin_dir/identity-file"
assert_rejected "same-content inode swap" validate_pinned_file "$pin_dir/identity-file" identity-file "$identity_device" "$identity_inode" "$identity_nlink" "$identity_hash"
mkdir -m 0700 -- "$pin_dir/identity-directory"
directory_device="$(stat_device "$pin_dir/identity-directory")"; directory_inode="$(stat_inode "$pin_dir/identity-directory")"; directory_nlink="$(stat_nlink "$pin_dir/identity-directory")"
mv -- "$pin_dir/identity-directory" "$pin_dir/identity-directory.old"
mkdir -m 0700 -- "$pin_dir/identity-directory"
assert_rejected "directory replacement" validate_pinned_directory "$pin_dir/identity-directory" identity-directory "$directory_device" "$directory_inode" "$directory_nlink"
rm -rf -- "$pin_dir/identity-directory"
printf 'file-instead\n' > "$pin_dir/identity-directory"
assert_rejected "directory-to-file replacement" validate_pinned_directory "$pin_dir/identity-directory" identity-directory "$directory_device" "$directory_inode" "$directory_nlink"

xtrace_status=0
xtrace_output="$(bash -x "$script_dir/safe-update.sh" registry.example/do-not-log-this:1 2>&1)" || xtrace_status=$?
[ "$xtrace_status" -eq 77 ] || { echo "FAIL: inherited xtrace returned $xtrace_status instead of 77" >&2; exit 1; }
grep -q 'inherited xtrace is not allowed' <<< "$xtrace_output" || { echo "FAIL: inherited xtrace was not rejected" >&2; exit 1; }
grep -q 'do-not-log-this' <<< "$xtrace_output" && { echo "FAIL: inherited xtrace logged the target image" >&2; exit 1; }
grep -Fq 'sha256_file "$capacity_reservation"' "$script_dir/safe-update.sh" && { echo "FAIL: capacity reserve is still fully hashed" >&2; exit 1; }

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

# Keep the Linux state-machine independent of the host's /usr/bin/tar symlink
# (macOS commonly points it at BSD tar). The updater still sees an absolute,
# regular executable and the wrapper removes the one GNU-only extraction flag
# that the portable fixture does not implement.
cat > "$mock_bin/tar" <<'MOCK_TAR'
#!/usr/bin/env bash
set -Eeuo pipefail
if [ "${1:-}" = --version ]; then
  printf '%s\n' 'tar (GNU tar) 1.35'
  exit 0
fi
args=()
for arg in "$@"; do
  [ "$arg" = --no-overwrite-dir ] || args+=("$arg")
done
exec /usr/bin/tar "${args[@]}"
MOCK_TAR
chmod 0700 "$mock_bin/tar"

cat > "$mock_bin/stat" <<'MOCK_STAT'
#!/usr/bin/env bash
set -Eeuo pipefail
fmt="$2"; if [ "$#" -eq 3 ]; then path="$3"; else path="$4"; fi
if [ "${MOCK_SCENARIO:-}" = secret_permissions_bad ] && [ "$path" = "${MOCK_CONTROL_KEY:-}" ] && [ "$fmt" = %a ]; then echo 444; exit 0; fi
if [ "${MOCK_SCENARIO:-}" = full_toolchain_drift ] && [ -e "$MOCK_STATE_DIR/toolchain_trigger" ] && [[ "$path" == */mock-bin/tar ]] && [ "$fmt" = %i ]; then echo 999999; exit 0; fi
if [ "${MOCK_SCENARIO:-}" = isolation_toolchain_drift ] && [ -e "$MOCK_STATE_DIR/toolchain_trigger" ] && [[ "$path" == */mock-bin/docker ]] && [ "$fmt" = %i ]; then echo 999999; exit 0; fi
if [ "${MOCK_SCENARIO:-}" = jq_only_drift ] && [ -e "$MOCK_STATE_DIR/toolchain_trigger" ] && [[ "$path" == */mock-bin/jq ]] && [ "$fmt" = %i ]; then echo 999999; exit 0; fi
if { [ "${MOCK_SCENARIO:-}" = cross_fs_stage ] || [ "${MOCK_SCENARIO:-}" = legacy_archive_failure ]; } && [[ "$path" == */.data.tar.tmp.* ]] && [ "$fmt" = %d ]; then echo 999; exit 0; fi
if [[ "$path" == */.capacity.reserve ]] && [ "$fmt" = %s ]; then cat "$MOCK_STATE_DIR/capacity_size"; exit 0; fi
case "$fmt:$path" in
  "%u:$MOCK_DATA"|"%u:$MOCK_DATA"/*) echo 10001; exit 0 ;;
  "%g:$MOCK_DATA"|"%g:$MOCK_DATA"/*) echo 10001; exit 0 ;;
  "%g:$MOCK_CONTROL_KEY") echo 10001; exit 0 ;;
  "%u:$MOCK_FIXTURE"|"%u:$MOCK_FIXTURE"/*) echo 0; exit 0 ;;
  "%g:$MOCK_FIXTURE"|"%g:$MOCK_FIXTURE"/*) echo 0; exit 0 ;;
  "%d:$MOCK_FIXTURE"|"%d:$MOCK_FIXTURE"/*) echo 1; exit 0 ;;
  "%d:"*"/omni-money-update-checkpoints"|"%d:"*"/omni-money-update-checkpoints/"*) echo 1; exit 0 ;;
  "%a:$MOCK_DATA") echo 700; exit 0 ;;
  "%a:/private/tmp"|"%Lp:/private/tmp") echo 755; exit 0 ;;
  "%a:/tmp"|"%Lp:/tmp") echo 755; exit 0 ;;
  "%a:/var/tmp"|"%Lp:/var/tmp") echo 755; exit 0 ;;
  "%u:$MOCK_AT_REST"|"%u:$MOCK_CONTROL_KEY"|"%u:"*"/.safe-update-journal"*) echo 0; exit 0 ;;
  "%g:$MOCK_AT_REST"|"%g:"*"/.safe-update-journal"*) echo 0; exit 0 ;;
  "%a:$MOCK_AT_REST") echo 444; exit 0 ;;
  "%a:$MOCK_CONTROL_KEY") echo 440; exit 0 ;;
  "%a:"*"/.safe-update-journal"*) echo 600; exit 0 ;;
  "%u:"*"/.capacity.reserve") echo 0; exit 0 ;;
  "%g:"*"/.capacity.reserve") echo 0; exit 0 ;;
  "%a:"*"/.capacity.reserve") echo 600; exit 0 ;;
  "%l:"*"/.capacity.reserve") echo 1; exit 0 ;;
  "%u:"*"/recovery"|"%u:"*"/recovery/"*) echo 0; exit 0 ;;
  "%g:"*"/recovery"|"%g:"*"/recovery/"*) echo 0; exit 0 ;;
  "%a:"*"/recovery") echo 700; exit 0 ;;
  "%a:"*"/recovery/"*) echo 400; exit 0 ;;
  "%u:$MOCK_ATTESTATION") echo 0; exit 0 ;;
  "%g:$MOCK_ATTESTATION") echo 0; exit 0 ;;
  "%a:$MOCK_ATTESTATION") echo 444; exit 0 ;;
  "%u:"*"/mock-bin/tar") echo 0; exit 0 ;;
  "%g:"*"/mock-bin/tar") echo 0; exit 0 ;;
  "%a:"*"/mock-bin/tar") echo 755; exit 0 ;;
  "%l:"*"/.omni-money-safe-update-pin."*) echo 2; exit 0 ;;
  "%l:"*"/recovery") echo 2; exit 0 ;;
  "%l:"*"/omni-money-update-checkpoints") echo 3; exit 0 ;;
  "%l:"*"/omni-money-update-checkpoints/"[0-9]*) echo 3; exit 0 ;;
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
    current:connected|candidate:connected|rollback:connected) echo '{"omni-money-pangolin":{"NetworkID":"network-123","IPAddress":"172.30.240.2"}}' ;;
    candidate:extra) echo '{"omni-money-pangolin":{"NetworkID":"network-123","IPAddress":"172.30.240.2"},"unexpected":{}}' ;;
    *) echo '{}' ;;
  esac
}
container_networks_runtime() {
  case "$1:$(get net_$1)" in
    current:connected|candidate:connected|rollback:connected) echo '{"omni-money-pangolin":{"NetworkID":"network-123","Aliases":["omni-money"],"IPAddress":"172.30.240.2","GlobalIPv6Address":"","IPAMConfig":{"IPv4Address":"172.30.240.2"}}}' ;;
    candidate:extra) echo '{"omni-money-pangolin":{"NetworkID":"network-123","Aliases":["omni-money"],"IPAddress":"172.30.240.2","GlobalIPv6Address":"","IPAMConfig":{"IPv4Address":"172.30.240.2"}},"unexpected":{"Aliases":[],"IPAddress":""}}' ;;
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
    config)
      [ "$scenario" != compose_config_failure ] || exit 18
      [ -z "${OMNI_DATA_DIR+x}" ] && [ -z "${OMNI_CONTROL_DB_ENCRYPTION_KEY_FILE+x}" ] && [ -z "${ALLOWED_HOSTS+x}" ] && [ -z "${TRUSTED_PROXIES+x}" ] && [ -z "${PASSKEY_RP_ID+x}" ] && [ -z "${SESSION_MAX_AGE_HOURS+x}" ] && [ -z "${AUTH_KDF_CONCURRENCY+x}" ] || exit 28
      case "$scenario" in
        compose_gpu_request) jq '.services["omni-money"].gpus = [{"driver":"nvidia","count":1}]' "$MOCK_CONFIG" ;;
        compose_device_cgroup_rule) jq '.services["omni-money"].device_cgroup_rules = ["c 1:3 rwm"]' "$MOCK_CONFIG" ;;
        compose_unapproved_runtime) jq '.services["omni-money"].runtime = "kata"' "$MOCK_CONFIG" ;;
        *) cat "$MOCK_CONFIG" ;;
      esac
      ;;
    ps)
      [ "$all" -eq 1 ] || exit 17
      case "$(get phase)" in
        current) [ "$scenario" != legacy_project ] && [ "$scenario" != legacy_archive_failure ] && echo current;;
        candidate)
          if [ "$scenario" = unknown_removal ] && [ -e "$state_dir/candidate_ps_seen" ]; then echo unexpected; else echo candidate; : > "$state_dir/candidate_ps_seen"; fi
          ;;
        rollback) echo rollback;;
      esac;;
    up)
      if [[ "$OMNI_IMAGE" == *rollback-* ]]; then put phase rollback; put rollback_state created; put net_rollback connected
      else put phase candidate; put candidate_state created; put net_candidate connected; put current_state absent; [ "$scenario" = partial_create ] && { put phase partial; exit 27; }; [ "$scenario" = extra_network ] && put net_candidate extra; case "$scenario" in env_swap) printf '# swapped during compose up\n' >> "$MOCK_ENV";; config_swap) printf '# swapped during compose up\n' >> "$MOCK_COMPOSE";; secret_inode_replacement) mv -- "$MOCK_AT_REST" "$MOCK_AT_REST.replaced"; printf 'tampered secret\n' > "$MOCK_AT_REST";; secret_inode_only_swap) mv -- "$MOCK_AT_REST" "$MOCK_AT_REST.replaced"; cp -- "$MOCK_AT_REST.replaced" "$MOCK_AT_REST";; esac; fi;;
  esac; exit 0
fi
case "$1" in
  ps)
    if [[ " $* " == *" label=com.docker.compose.project="* ]]; then
      case "$(get phase)" in
        candidate) [ "$scenario" = unknown_removal ] && echo unexpected || echo candidate ;;
        rollback) echo rollback ;;
      esac
    else
      { [ "$scenario" = legacy_project ] || [ "$scenario" = legacy_archive_failure ]; } && echo current
    fi
    ;;
  context)
    case "$2" in
      show) echo default ;;
      inspect) echo unix:///var/run/docker.sock ;;
      *) exit 26 ;;
    esac ;;
  pull) [ "$scenario" != pull_failure ];;
  tag) exit 0;;
  image) [ "$2" = inspect ]; echo sha256:target;;
  inspect)
    fmt="$3"; id="$4"
    case "$fmt" in
      *'index .Config.Labels "com.docker.compose.project"'*) { [ "$scenario" = legacy_project ] || [ "$scenario" = legacy_archive_failure ]; } && echo "${MOCK_FIXTURE##*/}" || exit 19;;
      *'index .Config.Labels "com.docker.compose.service"'*) { [ "$scenario" = legacy_project ] || [ "$scenario" = legacy_archive_failure ]; } && echo omni-money || exit 19;;
      *'{{.Name}}'*) { [ "$scenario" = legacy_project ] || [ "$scenario" = legacy_archive_failure ]; } && echo /omni-money || exit 19;;
      *'{{json .}}'*)
        mounts_json="$(container_mounts "$id")"; networks_json="$(container_networks_runtime "$id")"
        version=old; [ "$id" = candidate ] && version=target
        cap_add='[]'; [ "$id" = candidate ] && [ "$scenario" = unsafe_cap ] && cap_add='["SYS_ADMIN"]'
        service_env_json="$(jq -c '.services["omni-money"].environment | to_entries | map(.key + "=" + (.value|tostring))' "$MOCK_CONFIG")"
        config_user="10001:10001"; [ "$id" = candidate ] && [ "$scenario" = named_uid0_user ] && config_user=omni
        group_add='[]'; [ "$id" = candidate ] && [ "$scenario" = supplementary_group ] && group_add='["0"]'
        device_requests='[]'; [ "$id" = candidate ] && [ "$scenario" = runtime_gpu_request ] && device_requests='[{"Driver":"nvidia","Count":1,"Capabilities":[["gpu"]]}]'
        device_rules='[]'; [ "$id" = candidate ] && [ "$scenario" = runtime_device_cgroup_rule ] && device_rules='["c 1:3 rwm"]'
        runtime=runc; [ "$id" = candidate ] && [ "$scenario" = runtime_unapproved_runtime ] && runtime=kata
        jq -cn --arg version "$version" --arg user "$config_user" --arg runtime "$runtime" --argjson service_env "$service_env_json" --argjson cap_add "$cap_add" --argjson group_add "$group_add" --argjson device_requests "$device_requests" --argjson device_rules "$device_rules" --argjson mounts "$mounts_json" --argjson networks "$networks_json" '{Config:{User:$user,Entrypoint:["/entrypoint"],Cmd:["server"],WorkingDir:"/app",StopSignal:"SIGTERM",Healthcheck:{Test:["CMD","health"],Interval:1000000000,Timeout:1000000000,Retries:3},Env:($service_env + ["APP_MODE=server","VERSION="+$version,"SECRET_VALUE=private"]),Labels:{"com.example.role":"omni-money"}},HostConfig:{RestartPolicy:{Name:"unless-stopped",MaximumRetryCount:0},NetworkMode:"omni-money-pangolin",ReadonlyRootfs:true,Privileged:false,Init:false,CapDrop:["ALL"],CapAdd:$cap_add,GroupAdd:$group_add,DeviceRequests:$device_requests,DeviceCgroupRules:$device_rules,Runtime:$runtime,Devices:[],PidMode:"",IpcMode:"",NanoCpus:2000000000,Memory:1073741824,PidsLimit:256,LogConfig:{Type:"json-file",Config:{"max-size":"10m","max-file":"3"}},SecurityOpt:["no-new-privileges:true"]},Mounts:$mounts,NetworkSettings:{Networks:$networks}}';;
      *State.Status*) container_state "$id";;
      *State.Health*)
        if [ "$id" = candidate ] && [ "$scenario" = rollback_journal_failure ] && [ "$(get net_candidate)" = connected ]; then
          : > "$state_dir/rollback_trigger"; echo missing
        elif [ "$id" = candidate ] && { [ "$scenario" = full_toolchain_drift ] || [ "$scenario" = isolation_toolchain_drift ] || [ "$scenario" = jq_only_drift ]; } && [ "$(get net_candidate)" = connected ]; then
          : > "$state_dir/toolchain_trigger"; echo missing
        elif [ "$id" = candidate ] && { [ "$scenario" = candidate_failure ] || [ "$scenario" = candidate_data_mutation ] || [ "$scenario" = rollback_failure ] || [ "$scenario" = rollback_tag_mutation ] || [ "$scenario" = recovery_bundle_corrupt ] || [ "$scenario" = unknown_removal ] || { [ "$scenario" = candidate_ingress_health_failure ] && [ "$(get net_candidate)" = connected ]; }; }; then
          echo missing
        else
          echo healthy
        fi
        ;;
      *'.Image}'*) case "$id" in current) echo sha256:old;; rollback) [ "$scenario" = rollback_tag_mutation ] && echo sha256:tampered || echo sha256:old;; candidate) echo sha256:target;; esac;;
      *'.Config.Image}'*) case "$id" in current) echo omni-money:old;; *) echo omni-money:target;; esac;;
      *'.Config.User}'*) [ "$id" = candidate ] && [ "$scenario" = named_uid0_user ] && echo omni || echo 10001:10001;;
      *'{{json .HostConfig.GroupAdd}}'*) [ "$id" = candidate ] && [ "$scenario" = supplementary_group ] && echo '["0"]' || echo '[]';;
      *'{{json .Config.Env}}'*)
        if [ "$id" = current ] && [ "$scenario" = runtime_env_drift ]; then
          jq -c '.services["omni-money"].environment.ALLOWED_HOSTS = "drifted.example" | .services["omni-money"].environment | to_entries | map(.key + "=" + (.value|tostring)) + ["APP_MODE=server","VERSION=old","SECRET_VALUE=private"]' "$MOCK_CONFIG"
        else
          jq -c '.services["omni-money"].environment | to_entries | map(.key + "=" + (.value|tostring)) + ["APP_MODE=server","VERSION=old","SECRET_VALUE=private"]' "$MOCK_CONFIG"
        fi;;
      *'{{json .HostConfig}}'*)
        nano_cpus=2000000000
        [ "$id" = current ] && [ "$scenario" = runtime_resource_drift ] && nano_cpus=1000000000
        group_add='[]'; [ "$id" = candidate ] && [ "$scenario" = supplementary_group ] && group_add='["0"]'
        device_requests='[]'; [ "$id" = candidate ] && [ "$scenario" = runtime_gpu_request ] && device_requests='[{"Driver":"nvidia","Count":1,"Capabilities":[["gpu"]]}]'
        device_rules='[]'; [ "$id" = candidate ] && [ "$scenario" = runtime_device_cgroup_rule ] && device_rules='["c 1:3 rwm"]'
        runtime=runc; [ "$id" = candidate ] && [ "$scenario" = runtime_unapproved_runtime ] && runtime=kata
        jq -cn --arg runtime "$runtime" --argjson nano_cpus "$nano_cpus" --argjson group_add "$group_add" --argjson device_requests "$device_requests" --argjson device_rules "$device_rules" '{RestartPolicy:{Name:"unless-stopped",MaximumRetryCount:0},NetworkMode:"omni-money-pangolin",ReadonlyRootfs:true,Privileged:false,Init:false,CapDrop:["ALL"],CapAdd:[],GroupAdd:$group_add,DeviceRequests:$device_requests,DeviceCgroupRules:$device_rules,Runtime:$runtime,Devices:[],PidMode:"",IpcMode:"",NanoCpus:$nano_cpus,Memory:1073741824,PidsLimit:256,LogConfig:{Type:"json-file",Config:{"max-size":"10m","max-file":"3"}},SecurityOpt:["no-new-privileges:true"]}';;
      *PortBindings*) [ "$id" = candidate ] && [ "$scenario" = extra_port ] && echo '{"4000/tcp":[{"HostPort":"4000"}]}' || echo '{}';;
      *'.Mounts}'*) container_mounts "$id";;
      *'.NetworkSettings.Networks}'*) container_networks "$id";;
      *ReadonlyRootfs*) echo true;;
      *CapDrop*) echo '["ALL"]';;
      *SecurityOpt*) echo '["no-new-privileges:true"]';;
      *'IPAddress'*|*'index .NetworkSettings.Networks'*) [ "$(get net_$id)" = connected ] && echo 172.30.240.2 || true;;
      *) exit 19;;
    esac;;
  rm)
    id="$2"; [ "$id" = current ] && { put current_state absent; put phase none; } || exit 1
    ;;
  stop)
    id="$4"
    state_file="${id}_state"
    [ "$(get "$state_file")" != absent ] || exit 1
    if [ "$scenario" = sigkill ]; then put "$state_file" exited; kill -KILL "$PPID"; fi
    if [ "$scenario" = signal_int ] || [ "$scenario" = signal_term ]; then
      if [ ! -e "$state_dir/signal_sent" ]; then : > "$state_dir/signal_sent"; state_file="$id""_state"; put "$state_file" exited; [ "$scenario" = signal_int ] && kill -INT "$PPID" || kill -TERM "$PPID"; fi
    fi
    state_file="$id""_state"; put "$state_file" exited
    [ "$id" = current ] && : > "$state_dir/current_stopped"
    case "$scenario" in
      paused_after_stop) put "$state_file" paused ;;
      restarting_after_stop) put "$state_file" restarting ;;
      removing_after_stop) put "$state_file" removing ;;
    esac
    if [ "$id" = current ] && [ "$scenario" = partial_stop ] && [ ! -e "$state_dir/partial_sent" ]; then : > "$state_dir/partial_sent"; exit 23; fi;;
  start)
    id="$2"
    if [ "$id" = candidate ] && [ "$scenario" = candidate_data_mutation ]; then printf 'tampered ledger\n' > "$MOCK_DATA/ledger.txt"; put candidate_state exited
    elif [ "$id" = candidate ] && [ "$scenario" = recovery_bundle_corrupt ]; then
      shopt -s nullglob; recovery_members=("$MOCK_FIXTURE/omni-money-update-checkpoints"/*/recovery/network-contract.json); shopt -u nullglob
      [ "${#recovery_members[@]}" -eq 1 ] || exit 30
      printf 'corrupt recovery member\n' > "${recovery_members[0]}"
      put candidate_state exited
    elif [ "$id" = candidate ] && { [ "$scenario" = candidate_failure ] || [ "$scenario" = rollback_failure ] || [ "$scenario" = rollback_tag_mutation ] || [ "$scenario" = unknown_removal ]; }; then put candidate_state exited
    else state_file="$id""_state"; put "$state_file" running; fi;;
  network)
    action="$2"
    case "$action" in
      inspect)
        network_id=network-123
        [ "$scenario" = network_contract_drift ] && [ -e "$state_dir/current_stopped" ] && network_id=network-tampered
        printf '[{"Name":"omni-money-pangolin","Id":"%s","Driver":"bridge","Internal":true,"IPAM":{"Driver":"default","Config":[{"Subnet":"172.30.240.0/28"}]}}]\n' "$network_id"
        ;;
      disconnect) id="$4"; [ "$scenario" != network_disconnect_failure ] || exit 24; put "net_$id" none;;
      connect) id="$6"; if [ "$scenario" = network_reconnect_failure ] || { [ "$scenario" = rollback_failure ] && [ "$id" = rollback ]; }; then exit 25; fi; put "net_$id" connected;;
    esac;;
  *) exit 26;;
esac
MOCK_DOCKER
chmod 0700 "$mock_bin/docker"
cat > "$mock_bin/jq" <<'MOCK_JQ'
#!/usr/bin/env bash
set -Eeuo pipefail
exec /usr/bin/jq "$@"
MOCK_JQ
chmod 0700 "$mock_bin/jq"
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
  # BSD df does not accept GNU's `--` end-of-options marker; retain the
  # Linux-shaped output contract needed by the mock updater.
  exec /bin/df -Pk "${@: -1}"
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
[ "${MOCK_SCENARIO:-}" = rollback_journal_failure ] && [ -e "$MOCK_STATE_DIR/rollback_trigger" ] && [[ "$*" == *safe-update-journal* ]] && exit 29
exit 0
MOCK_SYNC
chmod 0700 "$mock_bin/sync"
cat > "$mock_bin/fallocate" <<'MOCK_FALLOCATE'
#!/usr/bin/env bash
set -Eeuo pipefail
[ "${MOCK_SCENARIO:-}" = reserve_failure ] && exit 1
path="${@: -1}"
size_arg="$2"; size_kb="${size_arg%K}"
printf '%s' "$((size_kb * 1024))" > "$MOCK_STATE_DIR/capacity_size"
printf 'reserved\n' > "$path"
MOCK_FALLOCATE
chmod 0700 "$mock_bin/fallocate"
cat > "$mock_bin/du" <<'MOCK_DU'
#!/usr/bin/env bash
set -Eeuo pipefail
path="${@: -1}"
if [[ "$path" == */.capacity.reserve ]]; then
  size_bytes="$(cat "$MOCK_STATE_DIR/capacity_size")"
  printf '%s\t%s\n' "$((size_bytes / 1024))" "$path"
else
  exec /usr/bin/du "$@"
fi
MOCK_DU
chmod 0700 "$mock_bin/du"

# The updater pins its production PATH to canonical system directories. Linux
# state-machine tests use exported Bash shims instead of placing attacker-like
# directories on that PATH; the shims are accepted only for this fixture path.
MOCK_BIN="$mock_bin"
export MOCK_BIN
docker() { "$MOCK_BIN/docker" "$@"; }
jq() { "$MOCK_BIN/jq" "$@"; }
stat() { "$MOCK_BIN/stat" "$@"; }
tar() { "$MOCK_BIN/tar" "$@"; }
chown() { "$MOCK_BIN/chown" "$@"; }
df() { "$MOCK_BIN/df" "$@"; }
du() { "$MOCK_BIN/du" "$@"; }
sleep() { "$MOCK_BIN/sleep" "$@"; }
sync() { "$MOCK_BIN/sync" "$@"; }
fallocate() { "$MOCK_BIN/fallocate" "$@"; }
findmnt() { return 0; }
export -f docker jq stat tar chown df du sleep sync fallocate findmnt

run_update() {
  local seconds="${SAFE_UPDATE_CASE_TIMEOUT_SECONDS:-90}"
  if command -v timeout >/dev/null 2>&1; then
    timeout --signal=KILL "$seconds" "$@"
  elif command -v perl >/dev/null 2>&1; then
    SAFE_UPDATE_CASE_TIMEOUT_SECONDS="$seconds" perl -e '$s=$ENV{SAFE_UPDATE_CASE_TIMEOUT_SECONDS} || 90; alarm $s; exec @ARGV' "$@"
  else
    "$@"
  fi
}

teardown_fixture_case_artifacts() {
  local path
  if [ -d "$fixture_root/.omni-money-safe-update.lock" ] && [ ! -L "$fixture_root/.omni-money-safe-update.lock" ]; then
    rmdir -- "$fixture_root/.omni-money-safe-update.lock" || return 1
  fi
  shopt -s nullglob
  for path in "$fixture_root"/.omni-money-safe-update-pin.*; do
    [ -d "$path" ] && [ ! -L "$path" ] || { shopt -u nullglob; return 1; }
    rm -rf -- "$path" || { shopt -u nullglob; return 1; }
  done
  shopt -u nullglob
  if [ -d "$fixture_root/omni-money-update-checkpoints" ] && [ ! -L "$fixture_root/omni-money-update-checkpoints" ]; then
    rm -rf -- "$fixture_root/omni-money-update-checkpoints" || return 1
  fi
}

assert_fixture_recovery_artifacts_retained() {
  local -a retained_pins=()
  [ -d "$fixture_root/.omni-money-safe-update.lock" ] &&
    [ ! -L "$fixture_root/.omni-money-safe-update.lock" ] &&
    [ -f "$fixture_root/omni-money-update-checkpoints/.safe-update-journal" ] || return 1
  shopt -s nullglob
  retained_pins=("$fixture_root"/.omni-money-safe-update-pin.*)
  shopt -u nullglob
  [ "${#retained_pins[@]}" -eq 1 ] && [ -d "${retained_pins[0]}" ] && [ ! -L "${retained_pins[0]}" ]
}

dump_legacy_archive_failure_diagnostics() {
  local key value
  printf '%s\n' '--- legacy_archive_failure updater output ---' >&2
  printf '%s\n' "$output" >&2
  printf '%s\n' '--- legacy_archive_failure fixture state ---' >&2
  for key in phase current_state candidate_state rollback_state net_current net_candidate net_rollback; do
    if [ -f "$mock_state/$key" ]; then value="$(cat "$mock_state/$key")"; else value='<missing>'; fi
    printf '%s=%s\n' "$key" "$value" >&2
  done
  printf '%s\n' '--- legacy_archive_failure ordered mock log ---' >&2
  nl -ba "$mock_state/log" >&2
}

fail_legacy_archive_case() {
  printf 'FAIL: %s\n' "$1" >&2
  dump_legacy_archive_failure_diagnostics
  exit 1
}

run_case() {
  local scenario="$1" expected="$2" output status=0 original_env_hash original_data_hash journal_path stale_status pin_path reserve_path
  printf 'safe-update state-machine scenario: %s\n' "$scenario" >&2
  local -a scenario_env=(SAFE_UPDATE_TEST_SCENARIO="$scenario")
  [ "$scenario" = checkpoint_env ] && scenario_env=(OMNI_UPDATE_CHECKPOINT_DIR=/tmp/attacker-controlled)
  [ "$scenario" = remote_context ] && scenario_env=(DOCKER_CONTEXT=remote)
  [ "$scenario" = remote_host ] && scenario_env=(DOCKER_HOST=tcp://127.0.0.1:2375)
  [ "$scenario" = docker_config_override ] && scenario_env=(DOCKER_CONFIG=/tmp/attacker-docker-config)
  [ "$scenario" = plugin_path_override ] && scenario_env=(DOCKER_CLI_PLUGIN_EXTRA_DIRS=/tmp/attacker-plugin)
  [ "$scenario" = tar_options ] && scenario_env=(TAR_OPTIONS=--checkpoint=1)
  [ "$scenario" = ambient_override ] && scenario_env=(OMNI_DATA_DIR=/attacker OMNI_CONTROL_DB_ENCRYPTION_KEY_FILE=/attacker ALLOWED_HOSTS=attacker.example TRUSTED_PROXIES=10.0.0.1 PASSKEY_RP_ID=attacker.example SESSION_MAX_AGE_HOURS=1 AUTH_KDF_CONCURRENCY=16)
  find "$mock_state" -mindepth 1 ! -name config.json -exec rm -f -- {} +
  if [ -d "$fixture_root/omni-money-update-checkpoints" ] && [ ! -L "$fixture_root/omni-money-update-checkpoints" ]; then rm -rf -- "$fixture_root/omni-money-update-checkpoints"; fi
  mkdir -m 0700 -p -- "$fixture_root/data"
  printf 'fixture ledger\n' > "$fixture_root/data/ledger.txt"
  printf current > "$mock_state/phase"; printf running > "$mock_state/current_state"; printf connected > "$mock_state/net_current"; : > "$mock_state/log"
  chmod 0600 "$fixture_root/.env" "$fixture_root/compose.yaml"
  chmod 0644 "$fixture_root/at-rest.json"; chmod 0600 "$fixture_root/control.key"
  printf 'OMNI_DATA_DIR=%s\nOMNI_IMAGE=omni-money:old\nOMNI_UPDATE_ATTESTATION_FILE=%s\n' "$fixture_root/data" "$fixture_root/attestation.json" > "$fixture_root/.env"
  case "$scenario" in
    env_colon_syntax) printf 'MALICIOUS : value\n' >> "$fixture_root/.env" ;;
    env_whitespace_syntax) printf ' MALICIOUS=value\n' >> "$fixture_root/.env" ;;
    env_multiline_quote) printf "MALICIOUS='first\nsecond'\n" >> "$fixture_root/.env" ;;
  esac
  printf 'fixture compose\n' > "$fixture_root/compose.yaml"
  printf 'at-rest-secret-value\n' > "$fixture_root/at-rest.json"; printf 'control-key\n' > "$fixture_root/control.key"
  chmod 0444 "$fixture_root/at-rest.json"; chmod 0440 "$fixture_root/control.key"
  rm -f -- "$fixture_root/at-rest.json.replaced"
  original_env_hash="$(sha256_file "$fixture_root/.env")"
  original_data_hash="$(sha256sum -- "$fixture_root/data/ledger.txt" | sed -E 's/[[:space:]].*$//')"
  output="$(run_update env -u DOCKER_HOST -u DOCKER_TLS_VERIFY -u DOCKER_CERT_PATH -u DOCKER_CONTEXT -u DOCKER_CONFIG -u DOCKER_CLI_PLUGIN_EXTRA_DIRS -u DOCKER_CLI_EXPERIMENTAL MOCK_STATE_DIR="$mock_state" MOCK_SCENARIO="$scenario" MOCK_LOG="$mock_state/log" MOCK_CONFIG="$mock_state/config.json" MOCK_FIXTURE="$fixture_root" MOCK_DATA="$fixture_root/data" MOCK_ATTESTATION="$fixture_root/attestation.json" MOCK_AT_REST="$fixture_root/at-rest.json" MOCK_CONTROL_KEY="$fixture_root/control.key" MOCK_ENV="$fixture_root/.env" MOCK_COMPOSE="$fixture_root/compose.yaml" OMNI_UPDATE_HEALTH_TIMEOUT_SECONDS=30 "${scenario_env[@]}" "$fixture_root/scripts/safe-update.sh" registry.example/omni-money:1.2.3 2>&1)" || status=$?
  if [ "$status" -ne "$expected" ]; then printf 'FAIL: scenario %s returned %s (expected %s)\n%s\nmock log:\n' "$scenario" "$status" "$expected" "$output" >&2; sed -n '1,120p' "$mock_state/log" >&2; exit 1; fi
  if grep -Eq 'control-key|at-rest-secret-value' <<< "$output" || grep -Eq 'control-key|at-rest-secret-value' "$mock_state/log"; then
    echo "FAIL: $scenario exposed secret content in output or mock log" >&2
    exit 1
  fi
  if [ "$scenario" = checkpoint_env ] || [ "$scenario" = remote_context ] || [ "$scenario" = remote_host ] || [ "$scenario" = docker_config_override ] || [ "$scenario" = plugin_path_override ] || [ "$scenario" = tar_options ] || [ "$scenario" = env_colon_syntax ] || [ "$scenario" = env_whitespace_syntax ] || [ "$scenario" = env_multiline_quote ] || [ "$scenario" = compose_gpu_request ] || [ "$scenario" = compose_device_cgroup_rule ] || [ "$scenario" = compose_unapproved_runtime ] || [ "$scenario" = runtime_env_drift ] || [ "$scenario" = runtime_resource_drift ] || [ "$scenario" = secret_permissions_bad ] || [ "$scenario" = pull_failure ] || [ "$scenario" = compose_config_failure ] || [ "$scenario" = low_space ] || [ "$scenario" = reserve_failure ]; then
    [ "$(cat "$mock_state/current_state")" = running ] && [ "$(cat "$mock_state/phase")" = current ] || { echo "FAIL: $scenario stopped current before a pre-stop failure" >&2; exit 1; }
    [ "$(sha256_file "$fixture_root/.env")" = "$original_env_hash" ] || { echo "FAIL: $scenario changed the env before stop" >&2; exit 1; }
    return 0
  fi
  if [ "$scenario" = sigkill ]; then
    [ "$(cat "$mock_state/current_state")" = exited ] || { echo "FAIL: SIGKILL did not leave the pinned current container stopped" >&2; exit 1; }
    journal_path="$fixture_root/omni-money-update-checkpoints/.safe-update-journal"
    [ -f "$journal_path" ] && [ -d "$fixture_root/.omni-money-safe-update.lock" ] || { echo "FAIL: SIGKILL did not leave durable recovery state" >&2; exit 1; }
    stale_status=0
env -u DOCKER_HOST -u DOCKER_TLS_VERIFY -u DOCKER_CERT_PATH -u DOCKER_CONTEXT MOCK_STATE_DIR="$mock_state" MOCK_SCENARIO=stale_lock MOCK_LOG="$mock_state/log" MOCK_CONFIG="$mock_state/config.json" MOCK_FIXTURE="$fixture_root" MOCK_DATA="$fixture_root/data" MOCK_ATTESTATION="$fixture_root/attestation.json" MOCK_AT_REST="$fixture_root/at-rest.json" MOCK_CONTROL_KEY="$fixture_root/control.key" MOCK_ENV="$fixture_root/.env" MOCK_COMPOSE="$fixture_root/compose.yaml" "$fixture_root/scripts/safe-update.sh" registry.example/omni-money:1.2.3 >/dev/null 2>&1 || stale_status=$?
    [ "$stale_status" -ne 0 ] && [ -d "$fixture_root/.omni-money-safe-update.lock" ] || { echo "FAIL: stale SIGKILL lock was not fail-closed" >&2; exit 1; }
    rmdir -- "$fixture_root/.omni-money-safe-update.lock"
    for pin_path in "$fixture_root"/.omni-money-safe-update-pin.*; do [ -d "$pin_path" ] && [ ! -L "$pin_path" ] && rm -rf -- "$pin_path"; done
    rm -rf -- "$fixture_root/omni-money-update-checkpoints"
    return 0
  fi
  if [ "$scenario" = rollback_journal_failure ]; then
    journal_path="$fixture_root/omni-money-update-checkpoints/.safe-update-journal"
    [ -d "$fixture_root/.omni-money-safe-update.lock" ] && [ -f "$journal_path" ] || { echo "FAIL: rollback journal failure did not preserve lock/journal" >&2; exit 1; }
    shopt -s nullglob
    recovery_paths=("$fixture_root/omni-money-update-checkpoints"/*/recovery)
    pin_paths=("$fixture_root"/.omni-money-safe-update-pin.*)
    shopt -u nullglob
    [ "${#recovery_paths[@]}" -eq 1 ] && [ -d "${recovery_paths[0]}" ] && [ "${#pin_paths[@]}" -eq 1 ] || { echo "FAIL: rollback journal failure removed recovery/pin artifacts" >&2; exit 1; }
    [ "$(cat "$mock_state/candidate_state")" = exited ] && [ "$(cat "$mock_state/current_state")" = absent ] && [ "$(cat "$mock_state/net_candidate")" = none ] && [ ! -e "$mock_state/rollback_state" ] || { echo "FAIL: rollback journal failure did not leave containers stopped and ingress-disconnected" >&2; exit 1; }
    candidate_stop_line="$(grep -n -F 'stop --time 30 candidate' "$mock_state/log" | tail -1 | cut -d: -f1)"
    current_stop_line="$(grep -n -F 'stop --time 30 current' "$mock_state/log" | tail -1 | cut -d: -f1)"
    disconnect_line="$(grep -n -F 'network disconnect network-123 candidate' "$mock_state/log" | tail -1 | cut -d: -f1)"
    journal_sync_line="$(grep -n -F '.safe-update-journal.tmp.' "$mock_state/log" | tail -1 | cut -d: -f1)"
    [ -n "$candidate_stop_line" ] && [ -n "$current_stop_line" ] && [ -n "$disconnect_line" ] && [ -n "$journal_sync_line" ] && [ "$candidate_stop_line" -lt "$disconnect_line" ] && [ "$current_stop_line" -lt "$disconnect_line" ] && [ "$disconnect_line" -lt "$journal_sync_line" ] || { echo "FAIL: rollback journal preceded stop/disconnect ordering" >&2; exit 1; }
    [ "$(sha256sum -- "$fixture_root/data/ledger.txt" | sed -E 's/[[:space:]].*$//')" = "$original_data_hash" ] || { echo "FAIL: rollback journal failure touched live data" >&2; exit 1; }
    rmdir -- "$fixture_root/.omni-money-safe-update.lock"
    rm -rf -- "${pin_paths[0]}" "$fixture_root/omni-money-update-checkpoints"
    return 0
  fi
  if [ "$scenario" = recovery_bundle_corrupt ]; then
    journal_path="$fixture_root/omni-money-update-checkpoints/.safe-update-journal"
    [ -d "$fixture_root/.omni-money-safe-update.lock" ] && [ -f "$journal_path" ] || { echo "FAIL: corrupt recovery member did not preserve lock/journal" >&2; exit 1; }
    shopt -s nullglob
    recovery_paths=("$fixture_root/omni-money-update-checkpoints"/*/recovery)
    pin_paths=("$fixture_root"/.omni-money-safe-update-pin.*)
    shopt -u nullglob
    [ "${#recovery_paths[@]}" -eq 1 ] && [ -f "${recovery_paths[0]}/network-contract.json" ] && [ "${#pin_paths[@]}" -eq 1 ] || { echo "FAIL: corrupt recovery member removed recovery/pin evidence" >&2; exit 1; }
    [ "$(cat "$mock_state/candidate_state")" = exited ] && [ "$(cat "$mock_state/current_state")" = absent ] && [ ! -e "$mock_state/rollback_state" ] || { echo "FAIL: corrupt recovery member did not leave containers stopped" >&2; exit 1; }
    jq -e '.phase == "rollback-stopped"' "$journal_path" >/dev/null || { echo "FAIL: corrupt recovery member did not retain the durable isolated phase" >&2; exit 1; }
    [ "$(sha256sum -- "$fixture_root/data/ledger.txt" | sed -E 's/[[:space:]].*$//')" = "$original_data_hash" ] || { echo "FAIL: corrupt recovery member touched live data" >&2; exit 1; }
    teardown_fixture_case_artifacts || { echo "FAIL: recovery_bundle_corrupt fixture teardown failed" >&2; exit 1; }
    return 0
  fi
  if [ "$scenario" = full_toolchain_drift ]; then
    journal_path="$fixture_root/omni-money-update-checkpoints/.safe-update-journal"
    [ -d "$fixture_root/.omni-money-safe-update.lock" ] && [ -f "$journal_path" ] || { echo "FAIL: full toolchain drift did not preserve lock/journal" >&2; exit 1; }
    [ "$(cat "$mock_state/candidate_state")" = exited ] && [ "$(cat "$mock_state/current_state")" = absent ] && [ "$(cat "$mock_state/net_candidate")" = none ] && [ ! -e "$mock_state/rollback_state" ] || { echo "FAIL: full toolchain drift blocked Docker isolation" >&2; exit 1; }
    jq -e '.phase == "network-connect"' "$journal_path" >/dev/null || { echo "FAIL: full toolchain drift advanced the journal before recovery tools were trusted" >&2; exit 1; }
    [ "$(sha256sum -- "$fixture_root/data/ledger.txt" | sed -E 's/[[:space:]].*$//')" = "$original_data_hash" ] || { echo "FAIL: full toolchain drift touched live data" >&2; exit 1; }
    teardown_fixture_case_artifacts || { echo "FAIL: full_toolchain_drift fixture teardown failed" >&2; exit 1; }
    return 0
  fi
  if [ "$scenario" = jq_only_drift ]; then
    journal_path="$fixture_root/omni-money-update-checkpoints/.safe-update-journal"
    assert_fixture_recovery_artifacts_retained || { echo "FAIL: jq-only drift did not retain lock/journal/pins" >&2; exit 1; }
    [ "$(cat "$mock_state/candidate_state")" = exited ] && [ "$(cat "$mock_state/current_state")" = absent ] && [ "$(cat "$mock_state/net_candidate")" = connected ] && [ ! -e "$mock_state/rollback_state" ] || { echo "FAIL: jq-only drift did not stop containers while retaining the unverified network state" >&2; exit 1; }
    grep -q 'containers are stopped.*network disconnect state is unverified' <<< "$output" || { echo "FAIL: jq-only drift was not reported as stopped with unverified network state" >&2; exit 1; }
    jq -e '.phase == "network-connect"' "$journal_path" >/dev/null || { echo "FAIL: jq-only drift advanced the durable journal" >&2; exit 1; }
    [ "$(sha256sum -- "$fixture_root/data/ledger.txt" | sed -E 's/[[:space:]].*$//')" = "$original_data_hash" ] || { echo "FAIL: jq-only drift touched live data" >&2; exit 1; }
    teardown_fixture_case_artifacts || { echo "FAIL: jq_only_drift fixture teardown failed" >&2; exit 1; }
    return 0
  fi
  if [ "$scenario" = isolation_toolchain_drift ]; then
    journal_path="$fixture_root/omni-money-update-checkpoints/.safe-update-journal"
    assert_fixture_recovery_artifacts_retained || { echo "FAIL: isolation toolchain drift did not retain lock/journal/pins" >&2; exit 1; }
    [ "$(cat "$mock_state/candidate_state")" = running ] && [ "$(cat "$mock_state/current_state")" = absent ] && [ "$(cat "$mock_state/net_candidate")" = connected ] && [ ! -e "$mock_state/rollback_state" ] || { echo "FAIL: isolation toolchain drift did not preserve the honestly unverified runtime state" >&2; exit 1; }
    grep -q 'container stop and network disconnect states are unverified' <<< "$output" || { echo "FAIL: isolation toolchain drift was not reported as unverified" >&2; exit 1; }
    jq -e '.phase == "network-connect"' "$journal_path" >/dev/null || { echo "FAIL: isolation toolchain drift advanced the durable journal" >&2; exit 1; }
    [ "$(sha256sum -- "$fixture_root/data/ledger.txt" | sed -E 's/[[:space:]].*$//')" = "$original_data_hash" ] || { echo "FAIL: isolation toolchain drift touched live data" >&2; exit 1; }
    teardown_fixture_case_artifacts || { echo "FAIL: isolation_toolchain_drift fixture teardown failed" >&2; exit 1; }
    return 0
  fi
  if [ "$scenario" = legacy_archive_failure ]; then
    [ "$(cat "$mock_state/current_state")" = running ] && [ "$(cat "$mock_state/net_current")" = connected ] || fail_legacy_archive_case "legacy rollback did not recover healthy ingress"
    disconnect_line="$(grep -n -F 'network disconnect omni-money-pangolin current' "$mock_state/log" | tail -1 | cut -d: -f1)"
    start_line="$(grep -n -F 'start current' "$mock_state/log" | tail -1 | cut -d: -f1)"
    connect_line="$(grep -n -F 'network connect --ip 172.30.240.2 omni-money-pangolin current' "$mock_state/log" | tail -1 | cut -d: -f1)"
    [ -n "$disconnect_line" ] && [ -n "$start_line" ] && [ -n "$connect_line" ] && [ "$disconnect_line" -lt "$start_line" ] && [ "$start_line" -lt "$connect_line" ] || fail_legacy_archive_case "legacy rollback was not disconnect -> start/health -> reconnect"
  elif [ "$scenario" = network_reconnect_failure ] || [ "$scenario" = candidate_ingress_health_failure ]; then
    [ "$(cat "$mock_state/phase")" = candidate ] && [ "$(cat "$mock_state/candidate_state")" = exited ] && [ "$(cat "$mock_state/current_state")" = absent ] && [ "$(cat "$mock_state/net_candidate")" = none ] && [ ! -e "$mock_state/rollback_state" ] || { echo "FAIL: $scenario did not stop and ingress-isolate the uncertain candidate" >&2; exit 1; }
    shopt -s nullglob
    failed_paths=("$fixture_root/omni-money-update-checkpoints"/*/failed-candidate-data)
    shopt -u nullglob
    [ "${#failed_paths[@]}" -eq 1 ] && [ -d "${failed_paths[0]}" ] && [ -f "${failed_paths[0]}/ledger.txt" ] && [ ! -e "$fixture_root/data" ] || { echo "FAIL: $scenario did not quarantine candidate data for manual reconciliation" >&2; exit 1; }
    journal_path="$fixture_root/omni-money-update-checkpoints/.safe-update-journal"
    jq -e '
      .phase == "manual-reconciliation-required" and
      (.candidate_ingress_state == "uncertain" or .candidate_ingress_state == "connected") and
      (.failed_candidate_data_identity.inode != "") and (.active_data_location == "") and
      (.checkpoint_root != .checkpoint_dir) and
      (.checkpoint_root_identity.inode != "") and (.checkpoint_dir_identity.inode != "") and
      (.env_file != .env_pin_file) and (.env_pin_file != .recovery_env_file) and
      (.original_env_identity.inode != "") and (.active_env_identity.inode != "") and
      (.env_pin_identity.inode != "") and (.recovery_env_identity.inode != "") and
      (.original_data_identity.inode != "") and
      (.network_identity.id != "") and (.network_contract_identity.inode != "") and
      (.docker_socket_identity.type == "socket")
    ' "$journal_path" >/dev/null || { echo "FAIL: $scenario did not durably record manual reconciliation identities" >&2; exit 1; }
    [ "$(sha256_file "$fixture_root/.env")" = "$original_env_hash" ] || { echo "FAIL: $scenario did not restore the exact original env" >&2; exit 1; }
    return 0
  elif [ "$scenario" = unknown_removal ]; then
    [ "$(cat "$mock_state/current_state")" = absent ] && [ "$(cat "$mock_state/phase")" = candidate ] && [ ! -e "$mock_state/rollback_state" ] || { echo "FAIL: unknown current disappearance was not kept stopped" >&2; exit 1; }
    assert_fixture_recovery_artifacts_retained || { echo "FAIL: unknown_removal did not retain lock/journal/pins" >&2; exit 1; }
    teardown_fixture_case_artifacts || { echo "FAIL: unknown_removal fixture teardown failed" >&2; exit 1; }
    return 0
  fi
  if [ "$scenario" = paused_after_stop ] || [ "$scenario" = restarting_after_stop ] || [ "$scenario" = removing_after_stop ]; then
    [ "$(cat "$mock_state/current_state")" = "${scenario%_after_stop}" ] && [ ! -e "$mock_state/rollback_state" ] || { echo "FAIL: $scenario was not rejected while fail-closed" >&2; exit 1; }
    assert_fixture_recovery_artifacts_retained || { echo "FAIL: $scenario did not retain lock/journal/pins" >&2; exit 1; }
    teardown_fixture_case_artifacts || { echo "FAIL: $scenario fixture teardown failed" >&2; exit 1; }
    return 0
  fi
  if [ "$scenario" = legacy_archive_failure ]; then
    [ "$(cat "$mock_state/current_state")" = running ] || fail_legacy_archive_case "legacy rollback did not restart current"
  elif [ "$scenario" != partial_stop ] && [ "$scenario" != signal_int ] && [ "$scenario" != signal_term ] && [ "$scenario" != cross_fs_stage ]; then
    [ "$(cat "$mock_state/current_state")" = absent ] || { echo "FAIL: $scenario did not model Compose removal of the pinned current container (state=$(cat "$mock_state/current_state"))" >&2; printf '%s\n' "$output" >&2; sed -n '1,160p' "$mock_state/log" >&2; exit 1; }
  else
    [ "$(cat "$mock_state/current_state")" = exited ] || { echo "FAIL: $scenario did not leave the interrupted current container stopped" >&2; exit 1; }
  fi
  if [ "$scenario" = success ] || [ "$scenario" = ambient_override ] || [ "$scenario" = legacy_project ]; then
    grep -q 'update succeeded' <<< "$output" || { echo "FAIL: success scenario did not verify update" >&2; exit 1; }
    [ "$(cat "$mock_state/phase")" = candidate ] && [ "$(cat "$mock_state/candidate_state")" = running ] && [ "$(cat "$mock_state/net_candidate")" = connected ] || { echo "FAIL: success candidate was not healthy and connected" >&2; exit 1; }
    grep -q 'OMNI_IMAGE=registry.example/omni-money:1.2.3' "$fixture_root/.env" || { echo "FAIL: success did not persist the new image" >&2; exit 1; }
    grep -Fq 'inspect --format {{json .}} candidate' "$mock_state/log" || { echo "FAIL: success did not compare the candidate runtime contract" >&2; exit 1; }
    for reserve_path in "$fixture_root/omni-money-update-checkpoints"/*/.capacity.reserve; do
      [ ! -e "$reserve_path" ] || { echo "FAIL: success retained the capacity reservation" >&2; exit 1; }
    done
    if [ "$scenario" = legacy_project ]; then
      grep -Fq 'rm current' "$mock_state/log" || { echo "FAIL: legacy project was not explicitly migrated" >&2; exit 1; }
    fi
  elif [ "$scenario" = legacy_archive_failure ]; then
    [ "$(cat "$mock_state/current_state")" = running ] && [ "$(cat "$mock_state/net_current")" = connected ] && [ ! -e "$mock_state/rollback_state" ] || fail_legacy_archive_case "legacy rollback final outcome is not the retained current container"
    grep -Fq 'inspect --format {{json .}} current' "$mock_state/log" || fail_legacy_archive_case "legacy rollback did not compare the retained runtime contract"
    jq -e '.phase == "rolled-back" and .capacity_reservation_state == "1"' "$fixture_root/omni-money-update-checkpoints/.safe-update-journal" >/dev/null || fail_legacy_archive_case "legacy rollback did not retain the final durable identity outcome"
  elif [ "$scenario" = rollback_failure ] || [ "$scenario" = network_disconnect_failure ] || [ "$scenario" = rollback_tag_mutation ] || [ "$scenario" = network_contract_drift ]; then
    [ "$(cat "$mock_state/phase")" = rollback ] && [ "$(cat "$mock_state/rollback_state")" != running ] || { echo "FAIL: $scenario left rollback running (phase=$(cat "$mock_state/phase") state=$(cat "$mock_state/rollback_state"))" >&2; sed -n '1,120p' "$mock_state/log" >&2; exit 1; }
    [ "$scenario" = rollback_tag_mutation ] || grep -Fq 'inspect --format {{json .}} rollback' "$mock_state/log" || { echo "FAIL: $scenario did not compare the rollback runtime contract" >&2; exit 1; }
  elif [ "$scenario" = secret_inode_replacement ] || [ "$scenario" = secret_inode_only_swap ]; then
    [ "$(cat "$mock_state/phase")" = candidate ] && [ ! -e "$mock_state/rollback_state" ] || { echo "FAIL: secret inode replacement was not fail-closed before rollback recreation" >&2; exit 1; }
  else
    [ "$(cat "$mock_state/phase")" = rollback ] && [ "$(cat "$mock_state/rollback_state")" = running ] && [ "$(cat "$mock_state/net_rollback")" = connected ] || { echo "FAIL: $scenario did not complete an isolated rollback" >&2; printf '%s\n' "$output" >&2; sed -n '1,180p' "$mock_state/log" >&2; exit 1; }
    grep -Fq 'inspect --format {{json .}} rollback' "$mock_state/log" || { echo "FAIL: $scenario did not compare the rollback runtime contract" >&2; exit 1; }
    [ "$scenario" != config_swap ] || grep -q 'pinned resolved snapshot' <<< "$output" || { echo "FAIL: config swap was not explicitly reported" >&2; exit 1; }
  fi
  if [ "$scenario" != success ] && [ "$scenario" != ambient_override ] && [ "$scenario" != legacy_project ]; then
    [ "$(sha256_file "$fixture_root/.env")" = "$original_env_hash" ] || { echo "FAIL: $scenario did not retain/restore the exact original env" >&2; exit 1; }
  fi
  if [ "$scenario" = candidate_data_mutation ]; then
    [ "$(sha256sum -- "$fixture_root/data/ledger.txt" | sed -E 's/[[:space:]].*$//')" = "$original_data_hash" ] || { echo "FAIL: candidate data mutation was not restored byte-for-byte" >&2; exit 1; }
  fi
  journal_path="$fixture_root/omni-money-update-checkpoints/.safe-update-journal"
  if [ -f "$journal_path" ]; then
    jq -e '
      (.checkpoint_root != .checkpoint_dir) and
      (.checkpoint_root_identity.inode != "") and (.checkpoint_dir_identity.inode != "") and
      (.env_file != .env_pin_file) and (.env_pin_file != .recovery_env_file) and
      (.original_env_identity.inode != "") and (.active_env_identity.inode != "") and
      (.env_pin_identity.inode != "") and (.recovery_env_identity.inode != "") and
      (.original_data_identity.inode != "") and
      (.network_identity.id != "") and (.network_contract_identity.inode != "") and
      (.docker_socket_identity.type == "socket")
    ' "$journal_path" >/dev/null || { echo "FAIL: $scenario journal path/identity pairs are invalid" >&2; exit 1; }
    if [ -e "$mock_state/rollback_state" ] && [ "$(cat "$mock_state/rollback_state")" = running ]; then
      jq -e '.capacity_reservation_state == "1"' "$journal_path" >/dev/null || { echo "FAIL: $scenario completed rollback without releasing capacity" >&2; exit 1; }
    fi
  fi
  [ "$scenario" != config_swap ] || grep -q 'swapped during compose up' "$fixture_root/compose.yaml" || { echo "FAIL: config swap marker was unexpectedly lost" >&2; exit 1; }
}

cat > "$mock_state/config.json" <<EOF
{"name":"omni-money","x-omni-update-attestation-file":"./attestation.json","services":{"omni-money":{"image":"registry.example/omni-money:1.2.3","container_name":"omni-money","user":"10001:10001","group_add":[],"restart":"unless-stopped","read_only":true,"cap_drop":["ALL"],"cap_add":[],"devices":[],"security_opt":["no-new-privileges:true"],"cpus":2.0,"mem_limit":"1g","pids_limit":256,"logging":{"driver":"json-file","options":{"max-size":"10m","max-file":"3"}},"ports":[],"environment":{"CONTROL_DB_PATH":"/app/data/control/omni_control.db","CONTROL_DB_ENCRYPTION_KEY_FILE":"/run/secrets/omni_control_database_key","VAULT_ROOT":"/app/data/vaults","AUTH_KDF_CONCURRENCY":"2","TMPDIR":"/tmp","SQLITE_TMPDIR":"/tmp","DATA_AT_REST_MODE":"external-encrypted-volume","DATA_AT_REST_ATTESTATION_FILE":"/run/secrets/omni_data_at_rest_attestation.json","HOST_IP":"0.0.0.0","PORT":"4000","SESSION_MAX_AGE_HOURS":"8","SESSION_IDLE_TIMEOUT_MINUTES":"15","SESSION_REAUTH_MAX_AGE_MINUTES":"5","SESSION_MAX_CONCURRENT":"3","TRUSTED_PROXIES":"172.30.240.3/32","FORCE_HTTPS":"true","ALLOW_INSECURE_HTTP":"false","HTTPS_REDIRECT_HOST":"","PASSKEY_RP_ID":"","PASSKEY_ORIGINS":"","ALLOWED_HOSTS":"money.example.com","CORS_ALLOWED_ORIGINS":""},"volumes":[{"type":"bind","source":"$fixture_root/data","target":"/app/data","read_only":false},{"type":"tmpfs","target":"/tmp"}],"secrets":[{"source":"omni_data_at_rest_attestation","target":"omni_data_at_rest_attestation.json"},{"source":"omni_control_database_key","target":"omni_control_database_key"}],"networks":{"pangolin_target":{"ipv4_address":"172.30.240.2"}}}},"secrets":{"omni_data_at_rest_attestation":{"file":"$fixture_root/at-rest.json"},"omni_control_database_key":{"file":"$fixture_root/control.key"}},"networks":{"pangolin_target":{"name":"omni-money-pangolin","internal":true,"ipam":{"config":[{"subnet":"172.30.240.0/28"}]}}}}
EOF

jq '.services["omni-money"].runtime = "runc"' "$mock_state/config.json" > "$mock_state/config.with-runtime.json"
mv -- "$mock_state/config.with-runtime.json" "$mock_state/config.json"

if [ -n "${SAFE_UPDATE_ONLY:-}" ]; then
  case "$SAFE_UPDATE_ONLY" in
    success) run_case success 0 ;;
    *) run_case "$SAFE_UPDATE_ONLY" "${SAFE_UPDATE_EXPECTED:-1}" ;;
  esac
  echo "safe-update focused scenario passed"
  exit 0
fi

run_case success 0
run_case legacy_project 0
run_case legacy_archive_failure 1
run_case checkpoint_env 1
run_case remote_context 1
run_case remote_host 1
run_case docker_config_override 1
run_case plugin_path_override 1
run_case tar_options 1
run_case env_colon_syntax 1
run_case env_whitespace_syntax 1
run_case env_multiline_quote 1
run_case compose_gpu_request 1
run_case compose_device_cgroup_rule 1
run_case compose_unapproved_runtime 1
run_case ambient_override 0
run_case runtime_env_drift 1
run_case runtime_resource_drift 1
run_case secret_permissions_bad 1
run_case pull_failure 1
run_case compose_config_failure 18
run_case low_space 1
run_case reserve_failure 1
run_case cross_fs_stage 1
run_case candidate_failure 1
run_case candidate_data_mutation 1
run_case partial_create 1
run_case unknown_removal 1
run_case partial_stop 23
run_case paused_after_stop 1
run_case restarting_after_stop 1
run_case removing_after_stop 1
run_case signal_int 130
run_case signal_term 130
run_case network_reconnect_failure 25
run_case candidate_ingress_health_failure 1
run_case network_disconnect_failure 1
run_case rollback_failure 1
run_case rollback_tag_mutation 1
run_case rollback_journal_failure 1
run_case recovery_bundle_corrupt 1
run_case full_toolchain_drift 1
run_case jq_only_drift 1
run_case isolation_toolchain_drift 1
run_case env_swap 1
run_case secret_inode_replacement 1
run_case secret_inode_only_swap 1
run_case config_swap 1
run_case extra_port 1
run_case extra_network 1
run_case extra_mount 1
run_case extra_secret 1
run_case unsafe_cap 1
run_case named_uid0_user 1
run_case supplementary_group 1
run_case network_contract_drift 1
run_case runtime_gpu_request 1
run_case runtime_device_cgroup_rule 1
run_case runtime_unapproved_runtime 1
run_case sigkill 137
find "$mock_state" -mindepth 1 ! -name config.json -exec rm -f -- {} +
printf current > "$mock_state/phase"; printf running > "$mock_state/current_state"; printf connected > "$mock_state/net_current"; : > "$mock_state/log"
mkdir -m 0700 -- "$fixture_root/omni-money-update-checkpoints"
printf '%s\n' '{"version":1,"phase":"stopping"}' > "$fixture_root/omni-money-update-checkpoints/.safe-update-journal"
chmod 0600 "$fixture_root/omni-money-update-checkpoints/.safe-update-journal"
stale_status=0
env MOCK_STATE_DIR="$mock_state" MOCK_SCENARIO=stale_journal MOCK_LOG="$mock_state/log" MOCK_CONFIG="$mock_state/config.json" MOCK_FIXTURE="$fixture_root" MOCK_DATA="$fixture_root/data" MOCK_ATTESTATION="$fixture_root/attestation.json" MOCK_AT_REST="$fixture_root/at-rest.json" MOCK_CONTROL_KEY="$fixture_root/control.key" MOCK_ENV="$fixture_root/.env" MOCK_COMPOSE="$fixture_root/compose.yaml" OMNI_UPDATE_HEALTH_TIMEOUT_SECONDS=30 "$fixture_root/scripts/safe-update.sh" registry.example/omni-money:1.2.3 >/dev/null 2>&1 || stale_status=$?
[ "$stale_status" -ne 0 ] && [ -f "$fixture_root/omni-money-update-checkpoints/.safe-update-journal" ] || { echo "FAIL: stale durable journal was not fail-closed" >&2; exit 1; }
rm -rf -- "$fixture_root/omni-money-update-checkpoints"
echo "safe-update state-machine tests passed"
