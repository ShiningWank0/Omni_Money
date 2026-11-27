#!/usr/bin/env bash
# Rebuild the affected Linux components and preserve the rest of the Docker release.
set -euo pipefail
recipe="$(cd "$(dirname "$0")" && pwd)"
source_lock="$recipe/sources.json"
output="$(realpath "$1")"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
cd "$work"
arch="$(uname -m)"
case "$arch" in x86_64|aarch64) ;; *) exit 1 ;; esac
mkdir -p payload "$output" "$work/prefix"

unpack() {
  local selector="$1" directory="$2" url checksum
  url="$(jq -er "$selector.url" "$source_lock")"
  checksum="$(jq -er "$selector.sha256" "$source_lock")"
  [[ "$checksum" =~ ^[a-f0-9]{64}$ ]]
  curl --connect-timeout 20 --fail --silent --show-error --location --retry 3 --proto '=https' --proto-redir '=https' "$url" -o source.tgz
  printf '%s  source.tgz\n' "$checksum" | sha256sum --check --strict
  mkdir -p "$directory"
  tar -xzf source.tgz --strip-components=1 -C "$directory"
}
unpack ".upstreamArchives.$arch" payload/docker
for package in containerd runc libseccomp gperf; do unpack ".$package" "$package"; done
(cd containerd && patch --fuzz=0 -p1 < "$recipe/containerd.patch")

# Same static seccomp version as the Docker release. Keep its generator fixed too.
export PATH="$work/prefix/bin:$PATH" PKG_CONFIG_PATH="$work/prefix/lib/pkgconfig"
for package in gperf libseccomp; do
  (cd "$package" && ./configure --prefix="$work/prefix" --disable-shared && make -j2 && make install)
done

bin="$work/payload/docker"
export CGO_ENABLED=0 GOTOOLCHAIN=local
go_version="$(go env GOVERSION)"
test "$go_version" = "go$(cat "$recipe/../../../.go-version")"
(
  cd containerd
  pkg=github.com/containerd/containerd/v2
  version="v$(jq -er .containerd.version "$source_lock")"
  revision="$(jq -er .containerd.revision "$source_lock")"
  for command in containerd ctr containerd-shim-runc-v2; do
    tags='urfave_cli_no_docs osusergo netgo static_build'
    if [[ "$command" == containerd-shim-runc-v2 ]]; then tags+=' no_grpc'; fi
    go build -p 2 -buildvcs=false -mod=readonly -trimpath -tags "$tags" \
      -ldflags "-s -w -X $pkg/version.Version=$version -X $pkg/version.Revision=$revision -X $pkg/version.Package=$pkg -extldflags '-static'" \
      -o "$bin/$command" "./cmd/$command"
  done
  go mod verify
  "$bin/containerd" --version | grep -F "$revision"
  "$bin/containerd" config default > /dev/null
  "$bin/ctr" --version | grep -F "$version"
  "$bin/containerd-shim-runc-v2" -v | grep -F "$revision"
)
(
  cd runc
  version="$(jq -er .runc.version "$source_lock")"
  revision="$(jq -er .runc.revision "$source_lock")"
  CGO_ENABLED=1 go build -p 2 -buildvcs=false -mod=readonly -trimpath -buildmode=pie \
    -tags 'seccomp urfave_cli_no_template godbus_no_tcp netgo osusergo' \
    -ldflags "-s -w -X main.gitCommit=v$version-0-g${revision:0:7} -linkmode external -extldflags '-static-pie'" \
    -o "$bin/runc" .
  go mod verify
  "$bin/runc" --version | grep -Fx "runc version $version"
  "$bin/runc" --version | grep -F "libseccomp: $(jq -er .libseccomp.version "$source_lock")"
  "$bin/runc" features | jq -e '.linux.seccomp.enabled == true and .linux.apparmor.enabled == true' >/dev/null
)
for command in containerd ctr containerd-shim-runc-v2 runc; do
  readelf -l "$bin/$command" > "$work/elf.txt"
  if grep -q 'INTERP' "$work/elf.txt"; then exit 1; fi
  go version -m "$bin/$command" > "$output/$command.txt"
  grep -Fq "$go_version" "$output/$command.txt"
done
for command in containerd ctr containerd-shim-runc-v2; do
  awk '$1 == "dep" && $2 == "github.com/cilium/ebpf" && $3 == "v0.22.0" {found=1} END {exit !found}' "$output/$command.txt"
done
awk '$1 == "dep" && $2 == "github.com/moby/sys/user" && $3 == "v0.5.1" {found=1} END {exit !found}' "$output/runc.txt"
if grep -Fq 'github.com/cilium/ebpf' "$output/runc.txt"; then exit 1; fi
for command in containerd ctr; do
  awk '$1 == "dep" && $2 == "go.opentelemetry.io/otel/trace" && $3 == "v1.47.0" {found=1} END {exit !found}' "$output/$command.txt"
done
awk '$1 == "dep" && $2 == "github.com/go-jose/go-jose/v4" && $3 == "v4.1.6" {found=1} END {exit !found}' "$output/containerd.txt"
mkdir -p "$bin/licenses"
cp containerd/LICENSE "$bin/licenses/containerd"
cp runc/LICENSE "$bin/licenses/runc"
cp libseccomp/LICENSE "$bin/licenses/libseccomp"
cp "$source_lock" "$output/source.json"
tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner -czf "$output/engine.tar.gz" -C payload docker
(cd "$output" && sha256sum engine.tar.gz > SHA256SUMS)
