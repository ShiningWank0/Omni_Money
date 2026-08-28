#!/bin/sh
set -eu

# Build the pinned SQLCipher amalgamation as a static MinGW/UCRT64 archive.
# This script intentionally does not install a DLL, import library, pkg-config
# file, or executable. The release build must link libsqlite3.a and the static
# OpenSSL libcrypto.a from OPENSSL_PREFIX explicitly.

SQLCIPHER_VERSION="4.18.0"
SQLCIPHER_SHA256="1df02d1b346fa27feaf2da2cb2c0d8209e788248e461ec288718aa5d3e9643e5"
OPENSSL_PREFIX="${OPENSSL_PREFIX:-/ucrt64}"
SQLCIPHER_PREFIX="${SQLCIPHER_PREFIX:-/ucrt64/omni-sqlcipher-${SQLCIPHER_VERSION}}"

CC="${CC:-gcc}"
AR="${AR:-ar}"
RANLIB="${RANLIB:-ranlib}"
NM="${NM:-nm}"
OBJDUMP="${OBJDUMP:-objdump}"
MAKE="${MAKE:-make}"

fail() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

if [ "${MSYSTEM:-}" != "UCRT64" ]; then
  fail "this script must run in an MSYS2 UCRT64 shell (MSYSTEM=UCRT64)"
fi
if [ "$(uname -m)" != "x86_64" ]; then
  fail "only native Windows amd64 builds are supported"
fi

for tool in curl sha256sum tar mktemp "$CC" "$AR" "$RANLIB" "$NM" "$OBJDUMP" "$MAKE"; do
  command -v "$tool" >/dev/null 2>&1 || fail "required tool is unavailable: $tool"
done

target_machine="$($CC -dumpmachine)"
case "$target_machine" in
  x86_64-w64-mingw32*) ;;
  *) fail "CC must target x86_64-w64-mingw32, got: $target_machine" ;;
esac

case "$OPENSSL_PREFIX$SQLCIPHER_PREFIX" in
  *[[:space:]]*) fail "OPENSSL_PREFIX and SQLCIPHER_PREFIX must not contain whitespace" ;;
esac
case "$SQLCIPHER_PREFIX" in
  ""|/|/ucrt64) fail "refusing unsafe SQLCIPHER_PREFIX: $SQLCIPHER_PREFIX" ;;
esac

openssl_header="$OPENSSL_PREFIX/include/openssl/evp.h"
openssl_archive="$OPENSSL_PREFIX/lib/libcrypto.a"
[ -f "$openssl_header" ] || fail "OpenSSL headers not found under OPENSSL_PREFIX: $openssl_header"
[ -f "$openssl_archive" ] || fail "static OpenSSL archive not found: $openssl_archive"
[ ! -L "$openssl_archive" ] || fail "static OpenSSL archive must not be a symlink: $openssl_archive"
"$AR" t "$openssl_archive" >/dev/null 2>&1 || fail "libcrypto.a is not a readable static archive"
if ! "$OBJDUMP" -f "$openssl_archive" | grep -q 'file format pe-x86-64'; then
  fail "libcrypto.a is not a Windows amd64 archive"
fi

if [ -e "$SQLCIPHER_PREFIX" ] || [ -L "$SQLCIPHER_PREFIX" ]; then
  fail "SQLCIPHER_PREFIX must be a fresh, nonexistent path: $SQLCIPHER_PREFIX"
fi

prefix_parent="$(dirname "$SQLCIPHER_PREFIX")"
mkdir -p "$prefix_parent"
stage_prefix="$(mktemp -d "${SQLCIPHER_PREFIX}.tmp.XXXXXX")"
work_dir="$(mktemp -d)"

cleanup() {
  rm -rf "$work_dir"
  if [ -n "${stage_prefix:-}" ] && [ -d "$stage_prefix" ]; then
    rm -rf "$stage_prefix"
  fi
}
trap cleanup EXIT INT TERM

archive="$work_dir/sqlcipher.tar.gz"
curl --fail --location --silent --show-error --retry 5 \
  "https://github.com/sqlcipher/sqlcipher/archive/refs/tags/v${SQLCIPHER_VERSION}.tar.gz" \
  --output "$archive"
printf '%s  %s\n' "$SQLCIPHER_SHA256" "$archive" | sha256sum -c -
tar -xzf "$archive" -C "$work_dir"

source_dir="$work_dir/sqlcipher-${SQLCIPHER_VERSION}"
build_dir="$work_dir/build"
mkdir -p "$build_dir"
cd "$build_dir"

# Configure is used only to generate SQLCipher's pinned amalgamation. The
# release archive below is compiled explicitly so every security-relevant
# preprocessor definition is visible and independent of host defaults.
CPPFLAGS="-I$OPENSSL_PREFIX/include" \
LDFLAGS="-L$OPENSSL_PREFIX/lib" \
  "$source_dir/configure" \
    --host=x86_64-w64-mingw32 \
    --with-tempstore=yes \
    --disable-tcl \
    --disable-shared \
    --enable-static
"$MAKE" sqlite3.c

"$CC" \
  -O2 \
  -fstack-protector-strong \
  -D_FORTIFY_SOURCE=2 \
  -fno-common \
  -ffunction-sections \
  -fdata-sections \
  -DSQLITE_HAS_CODEC \
  -DSQLITE_TEMP_STORE=2 \
  -DSQLITE_THREADSAFE=1 \
  -DSQLITE_EXTRA_INIT=sqlcipher_extra_init \
  -DSQLITE_EXTRA_SHUTDOWN=sqlcipher_extra_shutdown \
  -DSQLCIPHER_CRYPTO_OPENSSL \
  -DSQLITE_OMIT_LOAD_EXTENSION \
  -DNDEBUG \
  -I"$OPENSSL_PREFIX/include" \
  -c sqlite3.c \
  -o sqlite3.o

mkdir -p "$stage_prefix/include" "$stage_prefix/lib"
"$AR" rcs "$stage_prefix/lib/libsqlite3.a" sqlite3.o
"$RANLIB" "$stage_prefix/lib/libsqlite3.a"
install -m 0644 sqlite3.h "$stage_prefix/include/sqlite3.h"
install -m 0644 "$source_dir/src/sqlite3ext.h" "$stage_prefix/include/sqlite3ext.h"

archive_members="$($AR t "$stage_prefix/lib/libsqlite3.a")"
[ "$archive_members" = "sqlite3.o" ] || fail "unexpected members in libsqlite3.a: $archive_members"
if ! "$NM" -g --defined-only "$stage_prefix/lib/libsqlite3.a" | grep -q '[[:space:]]sqlite3_key$'; then
  fail "libsqlite3.a does not export sqlite3_key; SQLCipher codec support is missing"
fi
if ! "$NM" -u "$stage_prefix/lib/libsqlite3.a" | grep -q 'EVP_'; then
  fail "libsqlite3.a has no OpenSSL EVP references; the OpenSSL provider is missing"
fi

file_count="$(find "$stage_prefix" -type f | wc -l | tr -d '[:space:]')"
[ "$file_count" = "3" ] || fail "staged prefix contains unexpected files"
if find "$stage_prefix" -type f \( -name '*.dll' -o -name '*.dll.a' -o -name '*.so' -o -name '*.dylib' \) | grep -q .; then
  fail "staged prefix contains a dynamic library or import library"
fi

mv "$stage_prefix" "$SQLCIPHER_PREFIX"
stage_prefix=""
printf 'Built SQLCipher %s static archive at %s\n' "$SQLCIPHER_VERSION" "$SQLCIPHER_PREFIX"
