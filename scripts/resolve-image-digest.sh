#!/usr/bin/env bash
# Resolve a mutable image tag to its immutable multi-architecture digest.
#
# safe-update requires a digest-pinned target so a registry tag cannot be
# swapped between release and deployment. This helper resolves the digest with
# the local Docker CLI and prints the exact reference to pass to safe-update.
# Cross-check the output against the digest reported in the Docker Release job
# summary; that digest comes from the workflow that pushed the image.
#
# Usage:
#   scripts/resolve-image-digest.sh <repository:tag>
#   scripts/resolve-image-digest.sh <repository@sha256:digest>
set -Eeuo pipefail

usage() {
  printf '%s\n' \
    'usage: scripts/resolve-image-digest.sh <repository:tag|repository@sha256:digest>' >&2
  exit 2
}

[ "$#" -eq 1 ] || usage
image="$1"
case "$image" in
  ''|*[!A-Za-z0-9._/@:+-]*)
    printf 'error: image reference contains unsupported characters\n' >&2
    exit 2
    ;;
esac

reference_re='^[^@]+@sha256:[0-9a-f]{64}$'
if [[ "$image" == *"@"* ]]; then
  [[ "$image" =~ $reference_re ]] || {
    printf 'error: digest reference must be repository@sha256:<64 lowercase hex>\n' >&2
    exit 2
  }
  printf '%s\n' "$image"
  exit 0
fi

last_component="${image##*/}"
case "$last_component" in
  *:*) tag="${last_component##*:}" ;;
  *)
    printf 'error: image reference must include a version tag or sha256 digest\n' >&2
    exit 2
    ;;
esac
[ -n "$tag" ] || {
  printf 'error: image tag must not be empty\n' >&2
  exit 2
}
[ "$tag" != latest ] || {
  printf 'error: refusing to resolve the mutable latest tag; use a release version tag\n' >&2
  exit 2
}
repository="${image%:*}"
command -v docker >/dev/null 2>&1 || {
  printf 'error: docker CLI is required to resolve a tag to a digest\n' >&2
  exit 1
}

digest=""
if docker buildx version >/dev/null 2>&1; then
  digest="$(docker buildx imagetools inspect --format '{{.Manifest.Digest}}' "$image" 2>/dev/null || true)"
fi
if [[ ! "$digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
  # Fallback for hosts without the buildx plugin: pull the reference and keep
  # only the RepoDigest that belongs to this exact repository. A failed pull or
  # a stale/foreign RepoDigest is rejected instead of being reported as the
  # resolution result.
  docker pull --quiet "$image" >/dev/null 2>&1 || {
    printf 'error: could not pull %s to resolve its digest\n' "$image" >&2
    exit 1
  }
  digest="$(docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$image" 2>/dev/null |
    awk -v prefix="${repository}@" 'index($0, prefix) == 1 { print substr($0, length(prefix) + 1); exit }' || true)"
fi
[[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || {
  printf 'error: could not resolve an immutable digest for %s\n' "$image" >&2
  exit 1
}
printf '%s@%s\n' "$repository" "$digest"
