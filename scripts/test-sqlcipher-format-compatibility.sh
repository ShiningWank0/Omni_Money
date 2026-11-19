#!/bin/bash
set -euo pipefail
umask 077

test "${GITHUB_ACTIONS:-}" = true
test "$(uname -s)" = Linux
test -n "${RUNNER_TEMP:-}"
test -n "${SQLCIPHER_PREFIX:-}"
recipe="$1"
test -f "$recipe" && test ! -L "$recipe"
grep -Fx 'SQLCIPHER_VERSION="4.18.0"' "$recipe"
work="$(mktemp -d "$RUNNER_TEMP/omni-sqlcipher-compat.XXXXXX")"
trap 'rm -rf "$work"' EXIT
output=.oss-cache/sqlcipher-compatibility
mkdir -p "$output"
rm -f "$output/result.json"
# This recipe comes from the immutable pre-update main commit checked out by CI.
# It verifies the original source archive's SHA256 before compiling the library.
old_prefix="$work/old"
SQLCIPHER_PREFIX="$old_prefix" SQLCIPHER_LIBRARY_KIND=shared sh "$recipe"
for variant in old current; do
  prefix="$old_prefix"
  if [ "$variant" = current ]; then prefix="$SQLCIPHER_PREFIX"; fi
  cc -Wall -Wextra -Werror -DSQLITE_HAS_CODEC -I"$prefix/include" \
    scripts/sqlcipher-format-compatibility.c -L"$prefix/lib" -lsqlite3 \
    -Wl,-rpath,"$prefix/lib" -lcrypto -lpthread -ldl -lm -o "$work/$variant-probe"
done
database="$work/old-format.db"
LD_LIBRARY_PATH="$old_prefix/lib" "$work/old-probe" create "$database" 4.18.0
python3 - "$database" <<'PY'
from pathlib import Path
import sys
data = Path(sys.argv[1]).read_bytes()
assert len(data) > 100 and data[:16] != b'SQLite format 3\x00', 'The compatibility fixture is not encrypted'
PY
LD_LIBRARY_PATH="$SQLCIPHER_PREFIX/lib" "$work/current-probe" rewrite "$database" 4.20.0
LD_LIBRARY_PATH="$old_prefix/lib" "$work/old-probe" read "$database" 4.18.0
printf '%s\n' '{"passed":true,"createdWith":"4.18.0","readAndUpdatedWith":"4.20.0","reopenedWith":"4.18.0","encrypted":true}' > "$output/result.json"
echo 'SQLCipher format compatibility passed: 4.18 create -> 4.20 read/update -> 4.18 reopen'
