#!/usr/bin/env bash
set -euo pipefail

IFS=$'\n\t'

readonly SQLCIPHER_VERSION="4.18.0"
readonly SQLCIPHER_SHA256="1df02d1b346fa27feaf2da2cb2c0d8209e788248e461ec288718aa5d3e9643e5"

fail() {
  printf 'build-sqlcipher-darwin: %s\n' "$*" >&2
  exit 1
}

[[ "$(uname -s)" == "Darwin" ]] || fail "this script must run on macOS"

native_arch="$(uname -m)"
case "$native_arch" in
  x86_64)
    default_deployment_target="10.13"
    ;;
  arm64)
    default_deployment_target="11.0"
    ;;
  *)
    fail "unsupported native architecture: $native_arch"
    ;;
esac
readonly native_arch default_deployment_target

: "${OPENSSL_PREFIX:?OPENSSL_PREFIX must name a native static OpenSSL installation}"
: "${SQLCIPHER_PREFIX:?SQLCIPHER_PREFIX must name a fresh output directory}"

[[ "$OPENSSL_PREFIX" == /* ]] || fail "OPENSSL_PREFIX must be an absolute path"
[[ "$SQLCIPHER_PREFIX" == /* ]] || fail "SQLCIPHER_PREFIX must be an absolute path"
[[ "$SQLCIPHER_PREFIX" != "/" ]] || fail "SQLCIPHER_PREFIX must not be the filesystem root"
[[ ! -e "$SQLCIPHER_PREFIX" && ! -L "$SQLCIPHER_PREFIX" ]] || \
  fail "SQLCIPHER_PREFIX must not already exist: $SQLCIPHER_PREFIX"

[[ -d "$OPENSSL_PREFIX" ]] || fail "OPENSSL_PREFIX is not a directory: $OPENSSL_PREFIX"
openssl_prefix="$(cd "$OPENSSL_PREFIX" && pwd -P)"
readonly openssl_prefix

openssl_header="$openssl_prefix/include/openssl/crypto.h"
openssl_archive="$openssl_prefix/lib/libcrypto.a"
[[ -f "$openssl_header" && ! -L "$openssl_header" ]] || \
  fail "OPENSSL_PREFIX does not contain a regular include/openssl/crypto.h"
[[ -f "$openssl_archive" && ! -L "$openssl_archive" ]] || \
  fail "OPENSSL_PREFIX does not contain a regular static lib/libcrypto.a"

command -v curl >/dev/null || fail "curl is required"
command -v shasum >/dev/null || fail "shasum is required"
command -v tar >/dev/null || fail "tar is required"
command -v make >/dev/null || fail "make is required"
command -v xcrun >/dev/null || fail "xcrun is required"

clang="$(xcrun --find clang)"
libtool="$(xcrun --find libtool)"
lipo="$(xcrun --find lipo)"
nm_tool="$(xcrun --find nm)"
sdk_root="$(xcrun --sdk macosx --show-sdk-path)"
readonly clang libtool lipo nm_tool sdk_root

openssl_arches="$($lipo -archs "$openssl_archive" 2>/dev/null)" || \
  fail "libcrypto.a is not a Mach-O static archive"
[[ "$openssl_arches" == "$native_arch" ]] || \
  fail "libcrypto.a must contain only the native $native_arch architecture (found: $openssl_arches)"

deployment_target="${MACOSX_DEPLOYMENT_TARGET:-$default_deployment_target}"
[[ "$deployment_target" =~ ^[0-9]+([.][0-9]+){1,2}$ ]] || \
  fail "MACOSX_DEPLOYMENT_TARGET is invalid: $deployment_target"
if [[ "$native_arch" == "arm64" ]]; then
  deployment_major="${deployment_target%%.*}"
  (( deployment_major >= 11 )) || fail "arm64 requires MACOSX_DEPLOYMENT_TARGET 11.0 or newer"
fi
readonly deployment_target

build_jobs="${SQLCIPHER_BUILD_JOBS:-2}"
[[ "$build_jobs" =~ ^[1-9][0-9]*$ ]] || fail "SQLCIPHER_BUILD_JOBS must be a positive integer"
(( build_jobs <= 64 )) || fail "SQLCIPHER_BUILD_JOBS must not exceed 64"
readonly build_jobs

destination_parent_input="$(dirname "$SQLCIPHER_PREFIX")"
destination_name="$(basename "$SQLCIPHER_PREFIX")"
[[ "$destination_name" != "." && "$destination_name" != ".." && -n "$destination_name" ]] || \
  fail "SQLCIPHER_PREFIX has an invalid final path component"
mkdir -p "$destination_parent_input"
destination_parent="$(cd "$destination_parent_input" && pwd -P)"
destination_path="$destination_parent/$destination_name"
[[ ! -e "$destination_path" && ! -L "$destination_path" ]] || \
  fail "resolved SQLCIPHER_PREFIX already exists: $destination_path"
readonly destination_parent destination_name destination_path

temporary_root="$(mktemp -d "${TMPDIR:-/tmp}/omni-sqlcipher-darwin.XXXXXX")"
staging_prefix="$(mktemp -d "$destination_parent/.omni-sqlcipher-stage.XXXXXX")"
cleanup() {
  rm -rf "$temporary_root" "$staging_prefix"
}
trap cleanup EXIT INT TERM
readonly temporary_root staging_prefix

archive="$temporary_root/sqlcipher.tar.gz"
curl --fail --location --silent --show-error --retry 5 \
  "https://github.com/sqlcipher/sqlcipher/archive/refs/tags/v${SQLCIPHER_VERSION}.tar.gz" \
  --output "$archive"

actual_sha256="$(shasum -a 256 "$archive" | awk '{print $1}')"
[[ "$actual_sha256" == "$SQLCIPHER_SHA256" ]] || \
  fail "SQLCipher archive checksum mismatch: expected $SQLCIPHER_SHA256, got $actual_sha256"

archive_root="sqlcipher-${SQLCIPHER_VERSION}"
while IFS= read -r archive_entry; do
  case "$archive_entry" in
    "$archive_root" | "$archive_root/" | "$archive_root/"*)
      ;;
    *)
      fail "SQLCipher archive contains a path outside $archive_root: $archive_entry"
      ;;
  esac
  case "/$archive_entry/" in
    *"/../"*)
      fail "SQLCipher archive contains a parent-directory path: $archive_entry"
      ;;
  esac
done < <(tar -tzf "$archive")

tar -xzf "$archive" -C "$temporary_root"
source_dir="$temporary_root/$archive_root"
build_dir="$temporary_root/build"
[[ -x "$source_dir/configure" ]] || fail "verified SQLCipher archive does not contain configure"
mkdir -p "$build_dir" "$staging_prefix/include" "$staging_prefix/lib"

common_cflags=(
  -arch "$native_arch"
  -isysroot "$sdk_root"
  "-mmacosx-version-min=$deployment_target"
  -O2
  -fPIC
  -fstack-protector-strong
  -D_FORTIFY_SOURCE=2
  -DSQLITE_HAS_CODEC
  -DSQLITE_TEMP_STORE=2
  -DSQLITE_THREADSAFE=1
  -DSQLITE_EXTRA_INIT=sqlcipher_extra_init
  -DSQLITE_EXTRA_SHUTDOWN=sqlcipher_extra_shutdown
  -DSQLCIPHER_CRYPTO_OPENSSL
  -DSQLITE_OMIT_LOAD_EXTENSION
  -DNDEBUG
  "-I$openssl_prefix/include"
)
common_cflags_string="$(IFS=' '; printf '%s' "${common_cflags[*]}")"
readonly common_cflags_string

(
  cd "$build_dir"
  CC="$clang" \
  CFLAGS="$common_cflags_string" \
  CPPFLAGS="-I$openssl_prefix/include" \
  LDFLAGS="$openssl_archive" \
    "$source_dir/configure" --with-tempstore=yes --disable-tcl
  make -j"$build_jobs" sqlite3.c
)

[[ -f "$build_dir/sqlite3.c" && ! -L "$build_dir/sqlite3.c" ]] || fail "SQLCipher amalgamation was not generated"
[[ -f "$build_dir/sqlite3.h" && ! -L "$build_dir/sqlite3.h" ]] || fail "SQLCipher sqlite3.h was not generated"
[[ -f "$source_dir/src/sqlite3ext.h" && ! -L "$source_dir/src/sqlite3ext.h" ]] || \
  fail "SQLCipher sqlite3ext.h is missing"

"$clang" "${common_cflags[@]}" -c "$build_dir/sqlite3.c" -o "$build_dir/sqlite3.o"
"$libtool" -static -o "$staging_prefix/lib/libsqlite3.a" "$build_dir/sqlite3.o"
install -m 0644 "$build_dir/sqlite3.h" "$staging_prefix/include/sqlite3.h"
install -m 0644 "$source_dir/src/sqlite3ext.h" "$staging_prefix/include/sqlite3ext.h"
chmod 0755 "$staging_prefix" "$staging_prefix/include" "$staging_prefix/lib"
chmod 0644 "$staging_prefix/lib/libsqlite3.a"

sqlcipher_arches="$($lipo -archs "$staging_prefix/lib/libsqlite3.a" 2>/dev/null)" || \
  fail "generated libsqlite3.a is not a Mach-O static archive"
[[ "$sqlcipher_arches" == "$native_arch" ]] || \
  fail "generated libsqlite3.a has unexpected architectures: $sqlcipher_arches"

symbols_file="$temporary_root/libsqlite3.symbols"
"$nm_tool" -g "$staging_prefix/lib/libsqlite3.a" > "$symbols_file"
grep -Eq '[[:space:]]_sqlite3_open_v2$' "$symbols_file" || fail "generated archive lacks sqlite3_open_v2"
grep -Eq '[[:space:]]_sqlite3_key$' "$symbols_file" || fail "generated archive lacks SQLCipher sqlite3_key"

mv "$staging_prefix" "$destination_path"
trap - EXIT INT TERM
rm -rf "$temporary_root"

printf 'Built SQLCipher %s static library for macOS/%s at %s\n' \
  "$SQLCIPHER_VERSION" "$native_arch" "$destination_path"
