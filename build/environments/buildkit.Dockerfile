FROM golang:1.27.4-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS compiler
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
RUN mkdir -p /out/bin /out/inventory

FROM compiler AS daemon
WORKDIR /src
ADD --checksum=sha256:dce31a3f405045af3406a626b6888dd347103222764c2bf11bbf4286aafed9b8 https://codeload.github.com/moby/buildkit/tar.gz/412cc86ad3c4252b82e929f9b1b1ad167376ded8 /tmp/buildkit.tar.gz
RUN tar -xzf /tmp/buildkit.tar.gz --strip-components=1
RUN <<'SH'
set -eu
go mod edit -require=github.com/moby/go-archive@v0.3.3
cat >>go.sum <<'SUM'
github.com/moby/go-archive v0.3.3 h1:OxxR9paxsluYi+zDUEXTTaIxtkK3viymW+Ka7vRhhME=
github.com/moby/go-archive v0.3.3/go.mod h1:Npdv43fFqlhZW7Xo8fbm3ZMYFvAGNviUPqX21VERbcE=
SUM
pkg=github.com/moby/buildkit
revision=412cc86ad3c4252b82e929f9b1b1ad167376ded8
go build -mod=readonly -trimpath -tags 'osusergo netgo static_build seccomp grpcnotrace' \
  -ldflags "-X ${pkg}/version.Version=v0.35.1 -X ${pkg}/version.Revision=${revision} -X ${pkg}/version.Package=${pkg} -extldflags '-static'" \
  -o /out/bin/buildkitd ./cmd/buildkitd
go mod verify
/out/bin/buildkitd --version | grep -F "$revision"
go version -m /out/bin/buildkitd > /out/inventory/buildkitd.txt
grep -F 'go1.27.4' /out/inventory/buildkitd.txt
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

FROM moby/buildkit:v0.35.1@sha256:baf6138eb8c37644800c7c913fba051e840ebb9ba13eaf92d92ef9a1712d0b4c
COPY --from=daemon /out/bin/ /usr/bin/
COPY --from=plugins /out/bin/ /usr/bin/
COPY --from=daemon /out/inventory/ /opt/omni-environment/
COPY --from=plugins /out/inventory/ /opt/omni-environment/
