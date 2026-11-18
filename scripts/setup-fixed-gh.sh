#!/usr/bin/env bash
# Install only the CLI used by existing release jobs. No GitHub mutations here.
set -euo pipefail
[[ "${GITHUB_ACTIONS:-}" == true && "$(uname -s)" == Linux ]] || exit 1
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IFS=$'\t' read -r version url checksum archive_root < <(python3 - "$root/build/locks/gh.json" "$(uname -m)" <<'PY'
import json,re,sys
lock=json.load(open(sys.argv[1])); value=lock['platforms'][sys.argv[2]]
assert lock['schema']==1 and re.fullmatch(r'\d+\.\d+\.\d+',lock['version'])
assert re.fullmatch(r'[a-f0-9]{64}',value['sha256']) and re.fullmatch(r'[a-z0-9_.]+',value['root'])
assert value['url'].startswith('https://github.com/cli/cli/releases/download/v'+lock['version']+'/')
assert not re.search(r'\s',value['url'])
print(lock['version'],value['url'],value['sha256'],value['root'],sep='\t')
PY
)
dest="$RUNNER_TEMP/omni-gh"
mkdir "$dest"
curl --fail --location --retry 3 --proto '=https' --tlsv1.2 "$url" -o "$dest/gh.tar.gz"
printf '%s  %s\n' "$checksum" "$dest/gh.tar.gz" | sha256sum --check --strict
tar -xzf "$dest/gh.tar.gz" -C "$dest" "$archive_root/bin/gh"
test "$("$dest/$archive_root/bin/gh" --version | head -1 | awk '{print $3}')" = "$version"
printf '%s\n' "$dest/$archive_root/bin" >> "$GITHUB_PATH"
