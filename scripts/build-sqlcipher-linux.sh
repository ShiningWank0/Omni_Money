#!/bin/sh
set -eu

SQLCIPHER_VERSION="4.18.0"
SQLCIPHER_SHA256="1df02d1b346fa27feaf2da2cb2c0d8209e788248e461ec288718aa5d3e9643e5"
SQLCIPHER_PREFIX="${SQLCIPHER_PREFIX:-/usr/local}"

work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT INT TERM

archive="$work_dir/sqlcipher.tar.gz"
curl --fail --location --silent --show-error --retry 5 \
  "https://github.com/sqlcipher/sqlcipher/archive/refs/tags/v${SQLCIPHER_VERSION}.tar.gz" \
  --output "$archive"
printf '%s  %s\n' "$SQLCIPHER_SHA256" "$archive" | sha256sum --check --status
tar -xzf "$archive" -C "$work_dir"

source_dir="$work_dir/sqlcipher-${SQLCIPHER_VERSION}"
build_dir="$work_dir/build"
mkdir -p "$build_dir" "$SQLCIPHER_PREFIX/include" "$SQLCIPHER_PREFIX/lib"
cd "$build_dir"

"$source_dir/configure" \
  --with-tempstore=yes \
  --disable-tcl \
  CFLAGS="-O2 -fPIC -fstack-protector-strong -D_FORTIFY_SOURCE=2 -DSQLITE_HAS_CODEC -DSQLITE_EXTRA_INIT=sqlcipher_extra_init -DSQLITE_EXTRA_SHUTDOWN=sqlcipher_extra_shutdown -DSQLITE_OMIT_LOAD_EXTENSION -DNDEBUG" \
  LDFLAGS="-Wl,-z,relro,-z,now -lcrypto"
make -j"${SQLCIPHER_BUILD_JOBS:-2}" sqlite3.c

cc -O2 -fPIC -fstack-protector-strong -D_FORTIFY_SOURCE=2 \
  -DSQLITE_HAS_CODEC \
  -DSQLITE_TEMP_STORE=2 \
  -DSQLITE_THREADSAFE=1 \
  -DSQLITE_EXTRA_INIT=sqlcipher_extra_init \
  -DSQLITE_EXTRA_SHUTDOWN=sqlcipher_extra_shutdown \
  -DSQLCIPHER_CRYPTO_OPENSSL \
  -DSQLITE_OMIT_LOAD_EXTENSION \
  -DNDEBUG \
  -shared \
  -Wl,-z,relro,-z,now,-soname,libsqlite3.so \
  sqlite3.c -o "$SQLCIPHER_PREFIX/lib/libsqlite3.so" \
  -lcrypto -lpthread -ldl -lm

install -m 0644 sqlite3.h "$SQLCIPHER_PREFIX/include/sqlite3.h"
install -m 0644 "$source_dir/src/sqlite3ext.h" "$SQLCIPHER_PREFIX/include/sqlite3ext.h"

