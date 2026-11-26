#!/usr/bin/env bash
# CI runs in the repository's locked Noble amd64 image.
set -euo pipefail
version=5.3.3
distribution="z3-${version}-x64-glibc-2.39"
digest=d56f60b06f432f33de5810f7f1a384a339131f361e2d54d08ea83eea14326880
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
curl --disable --proto '=https' --proto-redir '=https' --fail --location --silent --show-error --retry 3 \
  "https://github.com/Z3Prover/z3/releases/download/z3-${version}/${distribution}.zip" --output "$work/z3.zip"
printf '%s  %s\n' "$digest" "$work/z3.zip" | sha256sum --check --status
unzip -p "$work/z3.zip" "$distribution/bin/z3" > "$work/z3"
chmod 0700 "$work/z3"
export OMNI_PROOF_Z3="$work/z3"
go test -count=1 ./scripts/formal-security
go run ./scripts/formal-security -z3 "$OMNI_PROOF_Z3" -report security-reports/formal-ledger.json
