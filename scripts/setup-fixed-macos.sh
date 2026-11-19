#!/usr/bin/env bash
set -euo pipefail
[[ "${GITHUB_ACTIONS:-}" == true && "$(uname -s)" == Darwin ]] || {
  echo 'This setup requires a GitHub Actions macOS runner' >&2; exit 1;
}
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
eval "$(node --input-type=module - "$root/build/locks/macos.json" <<'JS'
import { readFileSync } from 'node:fs';
const lock=JSON.parse(readFileSync(process.argv[2], 'utf8'));
if(lock.schema!==1) throw Error('Unsupported lock schema');
const values={DEVELOPER_DIR:lock.xcode.developerDirectory, XCODE_VERSION:lock.xcode.version,
  XCODE_BUILD:lock.xcode.build, SDK_VERSION:lock.xcode.macosSDK,
  TCL_VERSION:lock.tcl.version, TCL_URL:lock.tcl.url, TCL_SHA256:lock.tcl.sha256,
  TCL_ARCHIVE_ROOT:lock.tcl.archiveRoot, TCL_SHELL:lock.tcl.shell};
for(const [key,value] of Object.entries(values)) {
  if(typeof value!=='string' || !/^[a-zA-Z0-9_./:-]+$/.test(value)) throw Error('Invalid '+key);
  console.log(`${key}='${value}'`);
}
JS
)"
export DEVELOPER_DIR
test -d "$DEVELOPER_DIR"
test "$(xcodebuild -version)" = "$(printf 'Xcode %s\nBuild version %s' "$XCODE_VERSION" "$XCODE_BUILD")"
test "$(xcrun --sdk macosx --show-sdk-version)" = "$SDK_VERSION"
export SDKROOT="$(xcrun --sdk macosx --show-sdk-path)"
export CC="$(xcrun --find clang)" CXX="$(xcrun --find clang++)"
printf 'DEVELOPER_DIR=%s\nSDKROOT=%s\nCC=%s\nCXX=%s\n' "$DEVELOPER_DIR" "$SDKROOT" "$CC" "$CXX" >> "$GITHUB_ENV"

prefix="$RUNNER_TEMP/omni-tcl-$TCL_VERSION"
test ! -e "$prefix" && test ! -L "$prefix"
work="$(mktemp -d "$RUNNER_TEMP/omni-tcl-build.XXXXXX")"
success=false
cleanup() {
  rm -rf "$work"
  if [[ "$success" != true ]]; then rm -rf "$prefix"; fi
}
trap cleanup EXIT
curl --fail --location --retry 5 --retry-all-errors --retry-delay 2 --retry-max-time 300 \
  --connect-timeout 20 --max-time 180 --proto '=https' --tlsv1.2 "$TCL_URL" -o "$work/tcl.tar.gz"
test "$(shasum -a 256 "$work/tcl.tar.gz" | awk '{print $1}')" = "$TCL_SHA256"
while IFS= read -r path; do
  case "$path" in "$TCL_ARCHIVE_ROOT"|"$TCL_ARCHIVE_ROOT/"*) ;; *) exit 1 ;; esac
  case "/$path/" in *'/../'*) exit 1 ;; esac
done < <(tar -tzf "$work/tcl.tar.gz")
tar -xzf "$work/tcl.tar.gz" -C "$work"
(
  cd "$work/$TCL_ARCHIVE_ROOT/unix"
  CC="$CC" CXX="$CXX" ./configure --prefix="$prefix" --disable-shared --disable-zipfs
  # SQLCipher needs the Tcl interpreter, not Tk or Tcl extension packages.
  make -j2 binaries
  make install-binaries install-libraries
)
ln -s "$TCL_SHELL" "$prefix/bin/tclsh"
test "$(printf 'puts [info patchlevel]\n' | "$prefix/bin/tclsh")" = "$TCL_VERSION"
if otool -L "$prefix/bin/$TCL_SHELL" | grep -Eiq 'homebrew|cellar|libtcl.*dylib'; then
  echo 'Tcl unexpectedly links an external installation' >&2; exit 1
fi
printf '%s\n' "$prefix/bin" >> "$GITHUB_PATH"
printf 'TCLSH_CMD=%s\n' "$prefix/bin/tclsh" >> "$GITHUB_ENV"
success=true
xcodebuild -version
printf 'SDK: %s\nTcl: %s\n' "$SDK_VERSION" "$TCL_VERSION"
