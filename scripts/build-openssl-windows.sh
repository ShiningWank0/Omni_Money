#!/usr/bin/env bash
set -euo pipefail

readonly OPENSSL_VERSION="3.5.9"
readonly OPENSSL_SHA256="603f5602e2eef00d77fbd429d34dcd5822bb301757a1bc9cdb24c670f1eb859a"

fail() {
  printf 'build-openssl-windows: %s\n' "$*" >&2
  exit 1
}

[[ "${MSYSTEM:-}" == UCRT64 && "$(uname -m)" == x86_64 ]] || \
  fail "this script requires a native Windows amd64 MSYS2 UCRT64 shell"
: "${OPENSSL_PREFIX:?OPENSSL_PREFIX must name a fresh output directory}"
case "$OPENSSL_PREFIX" in
  /|/ucrt64|*[[:space:]]*) fail "OPENSSL_PREFIX must be a non-root path without whitespace" ;;
  /*) ;;
  *) fail "OPENSSL_PREFIX must be an absolute path" ;;
esac
[[ ! -e "$OPENSSL_PREFIX" && ! -L "$OPENSSL_PREFIX" ]] || fail "OPENSSL_PREFIX already exists"

CC="${CC:-gcc}"
AR="${AR:-ar}"
RANLIB="${RANLIB:-ranlib}"
NM="${NM:-nm}"
OBJDUMP="${OBJDUMP:-objdump}"
for tool in curl sha256sum tar make perl cygpath "$CC" "$AR" "$RANLIB" "$NM" "$OBJDUMP"; do
  command -v "$tool" >/dev/null || fail "$tool is required"
done
[[ "$($CC -dumpmachine)" == x86_64-w64-mingw32* ]] || fail "CC must target native MinGW amd64"
build_jobs="${OPENSSL_BUILD_JOBS:-2}"
[[ "$build_jobs" =~ ^[1-9][0-9]*$ ]] && (( build_jobs <= 64 )) || fail "OPENSSL_BUILD_JOBS must be between 1 and 64"

destination_name="$(basename "$OPENSSL_PREFIX")"
[[ "$destination_name" != . && "$destination_name" != .. ]] || fail "invalid output directory name"
mkdir -p "$(dirname "$OPENSSL_PREFIX")"
destination_parent="$(cd "$(dirname "$OPENSSL_PREFIX")" && pwd -P)"
destination_path="$destination_parent/$destination_name"
[[ ! -e "$destination_path" && ! -L "$destination_path" ]] || fail "resolved OPENSSL_PREFIX already exists"
temporary_root="$(mktemp -d "${TMPDIR:-/tmp}/omni-openssl-windows.XXXXXX")"
staging_prefix="$(mktemp -d "$destination_parent/.omni-openssl-stage.XXXXXX")"
cleanup() {
  rm -rf "$temporary_root" "$staging_prefix"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

archive="$temporary_root/openssl.tar.gz"
curl --disable --proto '=https' --proto-redir '=https' --fail --location --silent --show-error --retry 5 \
  "https://github.com/openssl/openssl/releases/download/openssl-${OPENSSL_VERSION}/openssl-${OPENSSL_VERSION}.tar.gz" \
  --output "$archive"
printf '%s  %s\n' "$OPENSSL_SHA256" "$archive" | sha256sum -c -
archive_root="openssl-${OPENSSL_VERSION}"
tar -tzf "$archive" > "$temporary_root/archive-paths"
while IFS= read -r entry; do
  case "$entry" in
    "$archive_root"|"$archive_root/"|"$archive_root/"*) ;;
    *) fail "archive path escaped $archive_root: $entry" ;;
  esac
  case "/$entry/" in *"/../"*) fail "archive contains a parent-directory path" ;; esac
done < "$temporary_root/archive-paths"
tar -xzf "$archive" -C "$temporary_root"
source_dir="$temporary_root/$archive_root"
[[ -f "$source_dir/Configure" ]] || fail "verified archive does not contain Configure"
(
  cd "$source_dir"
  CC="$CC" AR="$AR" RANLIB="$RANLIB" \
    perl ./Configure mingw64 no-shared no-tests no-apps no-docs no-module no-legacy no-cms no-siv no-quic \
      --prefix="$(cygpath -m "$destination_path")" \
      --openssldir="$(cygpath -m "$destination_path/ssl")"
  make -s -j"$build_jobs" build_libs
)

archive_path="$source_dir/libcrypto.a"
[[ -f "$archive_path" && ! -L "$archive_path" ]] || fail "static libcrypto.a was not built"
"$AR" t "$archive_path" > "$temporary_root/archive-members"
[[ -s "$temporary_root/archive-members" ]] || fail "libcrypto.a is empty"
"$OBJDUMP" -f "$archive_path" > "$temporary_root/archive-format"
grep -q 'file format pe-x86-64' "$temporary_root/archive-format" || fail "libcrypto.a is not a Windows amd64 archive"
"$NM" -g "$archive_path" > "$temporary_root/archive-symbols"
grep -Eq '[[:space:]]T[[:space:]]+EVP_aes_256_cbc$' "$temporary_root/archive-symbols" || fail "libcrypto.a lacks the required AES provider"

if grep -Eq '[[:space:]]_?(CMS_encrypt(_ex)?|ossl_aes(128|192|256)gcm_siv_functions)$' "$temporary_root/archive-symbols"; then
  fail "libcrypto.a contains an excluded CMS or AES-GCM-SIV entry point"
fi

mkdir -p "$staging_prefix/include" "$staging_prefix/lib" "$staging_prefix/share/licenses/openssl"
cp -R "$source_dir/include/openssl" "$staging_prefix/include/openssl"
install -m 0644 "$archive_path" "$staging_prefix/lib/libcrypto.a"
install -m 0644 "$source_dir/LICENSE.txt" "$staging_prefix/share/licenses/openssl/LICENSE.txt"
for notice in "$source_dir"/NOTICE*; do
  [[ ! -f "$notice" ]] || install -m 0644 "$notice" "$staging_prefix/share/licenses/openssl/"
done
[[ -f "$staging_prefix/include/openssl/crypto.h" ]] || fail "OpenSSL public headers are incomplete"

# Execute code linked to the staged archive, so the recorded version cannot
# come from an unrelated openssl.exe on the runner's PATH.
cat > "$temporary_root/verify.c" <<'C'
#include <stdio.h>
#include <string.h>
#include <openssl/crypto.h>
#include <openssl/evp.h>
#include <openssl/rand.h>
int main(void) {
    unsigned char random[32], key[32];
    const char *version = OpenSSL_version(OPENSSL_VERSION);
    if (strncmp(version, "OpenSSL " EXPECTED_OPENSSL_VERSION " ",
                sizeof("OpenSSL " EXPECTED_OPENSSL_VERSION " ") - 1) != 0) return 1;
    if (!EVP_aes_256_cbc() || RAND_bytes(random, sizeof random) != 1) return 2;
    if (PKCS5_PBKDF2_HMAC("probe", 5, random, sizeof random, 2,
                          EVP_sha512(), sizeof key, key) != 1) return 3;
    puts(version);
    return 0;
}
C
"$CC" -O2 -I"$staging_prefix/include" "-DEXPECTED_OPENSSL_VERSION=\"$OPENSSL_VERSION\"" \
  "$temporary_root/verify.c" "$staging_prefix/lib/libcrypto.a" \
  -lbcrypt -lws2_32 -lcrypt32 -lgdi32 -o "$temporary_root/verify.exe"
"$temporary_root/verify.exe" > "$staging_prefix/BUILD_VERSION.txt"
mv "$staging_prefix" "$destination_path"
printf 'Built checksum-pinned OpenSSL %s static libcrypto for Windows amd64 at %s\n' \
  "$OPENSSL_VERSION" "$destination_path"
