#!/usr/bin/env bash
# Only for disposable GitHub-hosted Linux jobs. Never changes a developer host.
set -euo pipefail
[[ "${GITHUB_ACTIONS:-}" == true && "$(uname -s)" == Linux ]] || {
  echo 'This setup requires an ephemeral GitHub Actions Linux runner' >&2; exit 1;
}
[[ "${1:-}" == true || "${1:-}" == false ]] || exit 2
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dest="$RUNNER_TEMP/omni-docker"
mkdir "$dest"
mkdir -p "$dest/bin" "$dest/config/cli-plugins"

python3 - "$root/build/locks/docker.json" "$(uname -m)" > "$dest/inputs.tsv" <<'PY'
import json,re,sys
lock=json.load(open(sys.argv[1]))
assert lock['schema']==1
platform=lock['platforms'][sys.argv[2]]
for name in ('engine','buildx','compose'):
    value=platform[name]
    assert re.fullmatch(r'[a-f0-9]{64}',value['sha256'])
    assert re.fullmatch(r'https://[^\s]+',value['url'])
    print(name,value['url'],value['sha256'],sep='\t')
for name in ('engineVersion','buildxVersion','composeVersion'):
    assert re.fullmatch(r'\d+\.\d+\.\d+',lock[name])
    print(name,lock[name],'-',sep='\t')
for name in ('buildkitImage','sbomImage'):
    assert re.fullmatch(r'[a-z0-9/:._-]+@sha256:[a-f0-9]{64}',lock[name])
    print(name,lock[name],'-',sep='\t')
PY

while IFS=$'\t' read -r name value checksum; do
  case "$name" in
    engine|buildx|compose)
      curl --fail --location --retry 3 --proto '=https' --tlsv1.2 "$value" -o "$dest/$name.download"
      printf '%s  %s\n' "$checksum" "$dest/$name.download" | sha256sum --check --strict
      ;;
    engineVersion) engine_version="$value" ;;
    buildxVersion) buildx_version="$value" ;;
    composeVersion) compose_version="$value" ;;
    buildkitImage) buildkit_image="$value" ;;
    sbomImage) sbom_image="$value" ;;
    *) exit 1 ;;
  esac
done < "$dest/inputs.tsv"
tar -xzf "$dest/engine.download" -C "$dest/bin" --strip-components=1
install -m 755 "$dest/buildx.download" "$dest/config/cli-plugins/docker-buildx"
install -m 755 "$dest/compose.download" "$dest/config/cli-plugins/docker-compose"
rm "$dest/"*.download
export PATH="$dest/bin:$PATH" DOCKER_CONFIG="$dest/config"
unset DOCKER_CONTEXT DOCKER_API_VERSION
printf '%s\n' "$dest/bin" >> "$GITHUB_PATH"
printf 'DOCKER_CONFIG=%s\nDOCKER_CONTEXT=\nDOCKER_API_VERSION=\n' "$DOCKER_CONFIG" >> "$GITHUB_ENV"
printf 'sbom-image=%s\n' "$sbom_image" >> "$GITHUB_OUTPUT"
test "$(docker --version | awk '{print $3}' | tr -d ,)" = "$engine_version"
test "$(docker buildx version | awk '{print $2}')" = "v$buildx_version"
test "$(docker compose version --short | sed 's/^v//')" = "$compose_version"

if [[ "$1" == true ]]; then
  # Use the verified daemon/containerd/runc too, rather than the runner's daemon.
  # Host networking and host paths are retained for the existing bind-mount E2E.
  if [[ ! -e /.dockerenv ]]; then
    sudo systemctl stop docker.service docker.socket containerd.service
  else
    # The frozen Linux job image owns this namespace. Never contact or stop
    # the host daemon that GitHub uses to start the job container.
    sudo -n true
  fi
  export DOCKER_HOST="unix://$dest/docker.sock"
  printf 'DOCKER_HOST=%s\n' "$DOCKER_HOST" >> "$GITHUB_ENV"
  sudo env "PATH=$PATH" "$dest/bin/dockerd" \
    --host "$DOCKER_HOST" --group "$(id -gn)" \
    --data-root "$dest/data" --exec-root "$dest/exec" \
    --pidfile "$dest/docker.pid" > "$dest/daemon.log" 2>&1 &
  ready=false
  for _ in {1..60}; do
    if docker info > /dev/null 2>&1; then ready=true; break; fi
    sleep 1
  done
  if [[ "$ready" != true ]]; then cat "$dest/daemon.log" >&2; exit 1; fi
  test "$(docker version --format '{{.Server.Version}}')" = "$engine_version"
  docker buildx create --name omni-fixed --driver docker-container \
    --driver-opt "image=$buildkit_image" --use
  docker buildx inspect --bootstrap
  docker version
fi
cat "$dest/inputs.tsv"
