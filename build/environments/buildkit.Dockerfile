ARG TARGETARCH
FROM scratch AS reviewed-modules
COPY <<'SUM' /go.sum
github.com/moby/buildkit v0.35.1 h1:5eMfbpfMFsznXL18kdc8JgxbnHtZsXptuRjzgTvyk3A=
github.com/moby/buildkit v0.35.1/go.mod h1:i872EpyI/+sYyZywXAp+fWr1DK6qFDdWVxhoAMG1B6Q=
github.com/moby/go-archive v0.3.3 h1:OxxR9paxsluYi+zDUEXTTaIxtkK3viymW+Ka7vRhhME=
github.com/moby/go-archive v0.3.3/go.mod h1:Npdv43fFqlhZW7Xo8fbm3ZMYFvAGNviUPqX21VERbcE=
github.com/moby/sys/user v0.5.1 h1:IMM/21OvpU2icUZczWBwLMf8iBEx1bOoiCQqcy1eKu0=
github.com/moby/sys/user v0.5.1/go.mod h1:4mQzP5aTmaLjDLp/dc870oNkik6E9vjRtWYwI05gvow=
github.com/opencontainers/runc v1.5.2 h1:eUUowc2WJCnFYzI87C1WqRcu356Dk/wsoi90y+2wEfE=
github.com/opencontainers/runc v1.5.2/go.mod h1:xGf9+KlNJkiI1y/C4rLIyLFckqs8WOMo4FFplswY9sw=
SUM

FROM golang:1.27.4-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS compiler
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
RUN mkdir -p /out/bin /out/inventory

FROM compiler AS daemon
WORKDIR /src
ADD --checksum=sha256:dce31a3f405045af3406a626b6888dd347103222764c2bf11bbf4286aafed9b8 https://codeload.github.com/moby/buildkit/tar.gz/412cc86ad3c4252b82e929f9b1b1ad167376ded8 /tmp/buildkit.tar.gz
RUN tar -xzf /tmp/buildkit.tar.gz --strip-components=1
RUN --mount=type=bind,from=reviewed-modules,source=/go.sum,target=/tmp/reviewed.sum <<'SH'
set -eu
pkg=github.com/moby/buildkit
revision=412cc86ad3c4252b82e929f9b1b1ad167376ded8
# Build versioned commands so future SBOMs retain the main module version.
go mod edit -module=omni.local/build-input -require="${pkg}@v0.35.1" \
  -require=github.com/moby/go-archive@v0.3.3 -require=github.com/moby/sys/user@v0.5.1
cat /tmp/reviewed.sum >> go.sum
for cmd in buildkitd buildctl; do
  tags=grpcnotrace
  if [ "$cmd" = buildkitd ]; then tags='osusergo netgo static_build seccomp grpcnotrace'; fi
  go build -mod=readonly -trimpath -tags "$tags" \
    -ldflags "-X ${pkg}/version.Version=v0.35.1 -X ${pkg}/version.Revision=${revision} -X ${pkg}/version.Package=${pkg} -extldflags '-static'" \
    -o "/out/bin/$cmd" "${pkg}/cmd/$cmd"
  "/out/bin/$cmd" --version | grep -F "$revision"
  go version -m "/out/bin/$cmd" > "/out/inventory/$cmd.txt"
  awk -v pkg="$pkg" '$1 == "mod" && $2 == pkg && $3 == "v0.35.1" {found=1} END {exit !found}' "/out/inventory/$cmd.txt"
done
go mod verify
awk '$1 == "dep" && $2 == "github.com/moby/go-archive" && $3 == "v0.3.3" {found=1} END {exit !found}' /out/inventory/buildkitd.txt
cp LICENSE /out/inventory/buildkit.LICENSE
SH

FROM compiler AS plugins
WORKDIR /src
ADD --checksum=sha256:3b723ab805f716f03820dfeee4289181d9ade76a34d628f9bb36120e44fe767e https://codeload.github.com/containernetworking/plugins/tar.gz/adc3e6b5b581638afbd194cf2e9319ecbb0151a1 /tmp/plugins.tar.gz
RUN tar -xzf /tmp/plugins.tar.gz --strip-components=1
RUN <<'SH'
set -eu
for plugin in main/bridge meta/firewall ipam/host-local main/loopback; do
  name="buildkit-cni-${plugin##*/}"
  go build -mod=vendor -trimpath \
    -ldflags '-extldflags -static -X github.com/containernetworking/plugins/pkg/utils/buildversion.BuildVersion=v1.9.1' \
    -o "/out/bin/$name" "./plugins/$plugin"
  CNI_COMMAND=VERSION "/out/bin/$name" | grep -F supportedVersions
  go version -m "/out/bin/$name" > "/out/inventory/$name.txt"
  grep -F go1.27.4 "/out/inventory/$name.txt"
done
cp LICENSE /out/inventory/cni.LICENSE
SH

# Pin the official security updates for each native worker.
FROM scratch AS apks-amd64
ADD --checksum=sha256:3779d8b0dfac33f82fd4ca0749318066e6beabe77cd788aa2d9f502e74a87b07 https://dl-cdn.alpinelinux.org/alpine/v3.24/main/x86_64/libexpat-2.11.0-r0.apk /libexpat-2.11.0-r0.apk
ADD --checksum=sha256:7b05bc778b8b25cd880835b40114182bd4d1581717504d0129c810c0a14291cb https://dl-cdn.alpinelinux.org/alpine/v3.24/main/x86_64/zlib-1.4-r0.apk /zlib-1.4-r0.apk
ADD --checksum=sha256:b22ccfe8cb072e4c8d3bc17222eeda132e270ce6258c27622acb193bf27b48e1 https://dl-cdn.alpinelinux.org/alpine/v3.24/main/x86_64/libseccomp-2.6.1-r0.apk /seccomp/libseccomp-2.6.1-r0.apk
ADD --checksum=sha256:487f4f1754b0921ce13ff706b4754cacafa0d1b66c0b28ef2a806dde1735c10e https://dl-cdn.alpinelinux.org/alpine/v3.24/main/x86_64/libseccomp-dev-2.6.1-r0.apk /seccomp/libseccomp-dev-2.6.1-r0.apk
ADD --checksum=sha256:0225cb9997a5c2bf8e43c0ea248cc077cc075d070f41b1593acff7ee3107515a https://dl-cdn.alpinelinux.org/alpine/v3.24/main/x86_64/libseccomp-static-2.6.1-r0.apk /seccomp/libseccomp-static-2.6.1-r0.apk

FROM scratch AS apks-arm64
ADD --checksum=sha256:cf19455af9c5c9dcd3915294d299f743553855519431bba38577b8c4a23c4373 https://dl-cdn.alpinelinux.org/alpine/v3.24/main/aarch64/libexpat-2.11.0-r0.apk /libexpat-2.11.0-r0.apk
ADD --checksum=sha256:d3e30d012a3445222b0b4a28f1d4d97ce67387f8b48561921111696da174f0de https://dl-cdn.alpinelinux.org/alpine/v3.24/main/aarch64/zlib-1.4-r0.apk /zlib-1.4-r0.apk
ADD --checksum=sha256:9a2a2a5ff3b17ca394a484a5905bceaa57c7d4d392079f07d68a03e44cdcdcd6 https://dl-cdn.alpinelinux.org/alpine/v3.24/main/aarch64/libseccomp-2.6.1-r0.apk /seccomp/libseccomp-2.6.1-r0.apk
ADD --checksum=sha256:72e899fe57fe563a5ad821b6bf5974a6a0dda8006a43f4e225a13d249d60c123 https://dl-cdn.alpinelinux.org/alpine/v3.24/main/aarch64/libseccomp-dev-2.6.1-r0.apk /seccomp/libseccomp-dev-2.6.1-r0.apk
ADD --checksum=sha256:3586d73cfb5384e20e411b5e9b4aa31fb284c505fdc406536646806f0c90796e https://dl-cdn.alpinelinux.org/alpine/v3.24/main/aarch64/libseccomp-static-2.6.1-r0.apk /seccomp/libseccomp-static-2.6.1-r0.apk

FROM apks-${TARGETARCH} AS worker-apks

FROM ghcr.io/shiningwank0/omni_money-build-alpine-builder@sha256:70276ebfaf61eddb9ee26087c6ac63854018b9ae81c8ce325dae9dee3672b3ea AS runc
WORKDIR /src
ENV CGO_ENABLED=1 GOTOOLCHAIN=local
ADD --checksum=sha256:5386e1968f70cc1482b4b2a93dc1a88ea7fa5517fba49b9e0fafe614d5a4c97a https://codeload.github.com/opencontainers/runc/tar.gz/29dd3dc2b13b4123162e5fe132504bb4b15569f1 /tmp/runc.tar.gz
RUN --network=none --mount=type=bind,from=worker-apks,source=/seccomp,target=/run/seccomp <<'SH'
set -eu
apk verify /run/seccomp/*.apk
apk add --no-network --no-cache --repositories-file /dev/null /run/seccomp/*.apk
SH
RUN --mount=type=bind,from=reviewed-modules,source=/go.sum,target=/tmp/reviewed.sum <<'SH'
set -eu
tar -xzf /tmp/runc.tar.gz --strip-components=1
mkdir -p /out/bin /out/inventory
pkg=github.com/opencontainers/runc
revision=29dd3dc2b13b4123162e5fe132504bb4b15569f1
go mod edit -module=omni.local/build-input -require="${pkg}@v1.5.2" -require=github.com/moby/sys/user@v0.5.1
cat /tmp/reviewed.sum >> go.sum
go build -mod=readonly -trimpath -tags 'apparmor seccomp netgo cgo static_build osusergo' \
  -ldflags "-X main.gitCommit=${revision} -extldflags '-static'" -o /out/bin/buildkit-runc "$pkg"
go mod verify
readelf -lW /out/bin/buildkit-runc > /out/inventory/buildkit-runc-elf.txt
if grep -q INTERP /out/inventory/buildkit-runc-elf.txt; then exit 1; fi
/out/bin/buildkit-runc --version | grep -Fx 'libseccomp: 2.6.1'
/out/bin/buildkit-runc features > /out/inventory/buildkit-runc-features.json
for feature in seccomp apparmor; do
  grep -F -A1 "\"$feature\": {" /out/inventory/buildkit-runc-features.json | grep -F '"enabled": true'
done
go version -m /out/bin/buildkit-runc > /out/inventory/buildkit-runc.txt
awk -v pkg="$pkg" '$1 == "mod" && $2 == pkg && $3 == "v1.5.2" {found=1} END {exit !found}' /out/inventory/buildkit-runc.txt
cp LICENSE /out/inventory/runc.LICENSE
SH

FROM moby/buildkit:v0.35.1@sha256:baf6138eb8c37644800c7c913fba051e840ebb9ba13eaf92d92ef9a1712d0b4c
COPY --from=daemon /out/bin/ /usr/bin/
COPY --from=runc /out/bin/ /usr/bin/
COPY --from=runc /out/inventory/ /opt/omni-environment/
COPY --from=plugins /out/bin/ /usr/bin/
COPY --from=daemon /out/inventory/ /opt/omni-environment/
COPY --from=plugins /out/inventory/ /opt/omni-environment/

RUN --network=none --mount=type=bind,from=worker-apks,target=/run/omni-apks,ro <<'SH'
set -eu
for cmd in buildkitd buildctl buildkit-runc; do
  info="/opt/omni-environment/$cmd.txt"
  grep -F go1.27.4 "$info"
  awk '$1 == "dep" && $2 == "github.com/moby/sys/user" && $3 == "v0.5.1" {found=1} END {exit !found}' "$info"
done
test "$(cut -d. -f1,2 /etc/alpine-release)" = 3.24
set -- /run/omni-apks/libexpat-2.11.0-r0.apk /run/omni-apks/zlib-1.4-r0.apk
apk verify "$@"
apk add --no-network --no-cache --repositories-file /dev/null "$@"
apk info --no-network --repositories-file /dev/null -v > /opt/omni-environment/apk-packages.txt
grep -Fx libexpat-2.11.0-r0 /opt/omni-environment/apk-packages.txt
grep -Fx zlib-1.4-r0 /opt/omni-environment/apk-packages.txt
sha256sum "$@" > /opt/omni-environment/security-apks.sha256
SH
