#!/usr/bin/env bash
set -euo pipefail
[[ "${GITHUB_ACTIONS:-}" == true && "$(uname -s)" == Linux ]] || exit 1
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IFS=$'\t' read -r version url checksum < <(python3 - "$root/build/locks/trivy.json" "$(uname -m)" <<'PY'
import json,re,sys
lock=json.load(open(sys.argv[1])); value=lock['platforms'][sys.argv[2]]
assert lock['schema']==1 and re.fullmatch(r'\d+\.\d+\.\d+',lock['version'])
assert re.fullmatch(r'[a-f0-9]{64}',value['sha256'])
assert value['url'].startswith('https://github.com/aquasecurity/trivy/releases/download/v'+lock['version']+'/')
assert not re.search(r'\s',value['url'])
print(lock['version'],value['url'],value['sha256'],sep='\t')
PY
)
dest="$RUNNER_TEMP/omni-trivy"
mkdir "$dest"
mkdir "$dest/bin" "$dest/cache" "$dest/identity"
curl --fail --location --retry 3 --proto '=https' --tlsv1.2 "$url" -o "$dest/trivy.tar.gz"
printf '%s  %s\n' "$checksum" "$dest/trivy.tar.gz" | sha256sum --check --strict
tar -xzf "$dest/trivy.tar.gz" -C "$dest/bin" trivy
test "$("$dest/bin/trivy" --version | head -1)" = "Version: $version"
sha256sum "$dest/bin/trivy" > "$dest/identity/binary.sha256"
cp "$root/build/locks/trivy.json" "$dest/identity/tool-lock.json"
rm "$dest/trivy.tar.gz"
printf '%s\n' "$dest/bin" >> "$GITHUB_PATH"
printf 'cache-path=%s\nidentity-path=%s\n' "$dest/cache" "$dest/identity" >> "$GITHUB_OUTPUT"
