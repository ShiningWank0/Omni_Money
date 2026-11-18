#!/usr/bin/env bash
set -euo pipefail
[[ "${GITHUB_ACTIONS:-}" == true && "$(uname -s)" == Linux ]] || exit 1
test -f /opt/omni-environment/packages.tsv
test "$(id -u)" = 1001
diff -u /opt/omni-environment/packages.tsv <(dpkg-query -W -f='${binary:Package}\t${Version}\t${Architecture}\n')
printf 'Linux package inventory matches the saved image; uid=%s\n' "$(id -u)"
