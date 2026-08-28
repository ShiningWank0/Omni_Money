#!/bin/sh
set -eu

SQLCIPHER_VERSION="4.18.0"
SQLCIPHER_SHA256="1df02d1b346fa27feaf2da2cb2c0d8209e788248e461ec288718aa5d3e9643e5"
SQLCIPHER_PREFIX="${SQLCIPHER_PREFIX:-/usr/local}"
SQLCIPHER_LIBRARY_KIND="${SQLCIPHER_LIBRARY_KIND:-shared}"

case "$SQLCIPHER_LIBRARY_KIND" in
  shared | static) ;;
  *)
    echo "SQLCIPHER_LIBRARY_KIND must be shared or static" >&2
    exit 1
    ;;
esac

# A static release build must not accidentally prefer a pre-existing shared
# sqlite3 library. Require a dedicated empty prefix instead of deleting or
# overwriting anything supplied by the runner.
if [ "$SQLCIPHER_LIBRARY_KIND" = "static" ] && { [ -e "$SQLCIPHER_PREFIX" ] || [ -L "$SQLCIPHER_PREFIX" ]; }; then
  if [ ! -d "$SQLCIPHER_PREFIX" ] || [ -L "$SQLCIPHER_PREFIX" ]; then
    echo "static SQLCipher prefix must be an empty, non-symlink directory: $SQLCIPHER_PREFIX" >&2
    exit 1
  fi
  prefix_entry="$(find "$SQLCIPHER_PREFIX" -mindepth 1 -print -quit)"
  if [ -n "$prefix_entry" ]; then
    echo "static SQLCipher prefix must be empty: $SQLCIPHER_PREFIX" >&2
    exit 1
  fi
fi

work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT INT TERM

archive="$work_dir/sqlcipher.tar.gz"
curl --fail --location --silent --show-error --retry 5 \
  "https://github.com/sqlcipher/sqlcipher/archive/refs/tags/v${SQLCIPHER_VERSION}.tar.gz" \
  --output "$archive"
printf '%s  %s\n' "$SQLCIPHER_SHA256" "$archive" | sha256sum -c -
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

compile_sqlcipher() {
  cc -O2 -fPIC -fstack-protector-strong -D_FORTIFY_SOURCE=2 \
    -DSQLITE_HAS_CODEC \
    -DSQLITE_TEMP_STORE=2 \
    -DSQLITE_THREADSAFE=1 \
    -DSQLITE_EXTRA_INIT=sqlcipher_extra_init \
    -DSQLITE_EXTRA_SHUTDOWN=sqlcipher_extra_shutdown \
    -DSQLCIPHER_CRYPTO_OPENSSL \
    -DSQLITE_OMIT_LOAD_EXTENSION \
    -DNDEBUG \
    "$@"
}

if [ "$SQLCIPHER_LIBRARY_KIND" = "static" ]; then
  static_object="$build_dir/sqlite3.o"
  static_archive="$build_dir/libsqlite3.a"
  compile_sqlcipher -c sqlite3.c -o "$static_object"

  # GNU ar supports the explicit deterministic-mode D modifier. Some ar
  # implementations are deterministic by default but reject D, so retry with
  # ZERO_AR_DATE rather than making the static build platform-dependent.
  if ! ar rcsD "$static_archive" "$static_object" 2>/dev/null; then
    rm -f "$static_archive"
    ZERO_AR_DATE=1 ar rcs "$static_archive" "$static_object"
  fi
  install -m 0644 "$static_archive" "$SQLCIPHER_PREFIX/lib/libsqlite3.a"

  unexpected_library="$(find "$SQLCIPHER_PREFIX/lib" -mindepth 1 ! -name libsqlite3.a -print -quit)"
  if [ -n "$unexpected_library" ]; then
    echo "static SQLCipher prefix contains an unexpected library: $unexpected_library" >&2
    exit 1
  fi
else
  compile_sqlcipher \
    -shared \
    -Wl,-z,relro,-z,now,-soname,libsqlite3.so \
    sqlite3.c -o "$SQLCIPHER_PREFIX/lib/libsqlite3.so" \
    -lcrypto -lpthread -ldl -lm
fi

install -m 0644 sqlite3.h "$SQLCIPHER_PREFIX/include/sqlite3.h"
install -m 0644 "$source_dir/src/sqlite3ext.h" "$SQLCIPHER_PREFIX/include/sqlite3ext.h"
