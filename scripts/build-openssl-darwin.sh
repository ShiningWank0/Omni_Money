#!/usr/bin/env bash
set -euo pipefail

IFS=$'\n\t'

readonly OPENSSL_VERSION="3.5.8"
readonly OPENSSL_SHA256="a8f84a39918ec6415ce765d9b429d313ba97b8143169c172e734b9514464f5b2"

fail() {
  printf 'build-openssl-darwin: %s\n' "$*" >&2
  exit 1
}

[[ "$(uname -s)" == "Darwin" ]] || fail "this script must run on macOS"
: "${OPENSSL_PREFIX:?OPENSSL_PREFIX must name a fresh output directory}"
: "${MACOSX_DEPLOYMENT_TARGET:?MACOSX_DEPLOYMENT_TARGET is required}"

native_arch="$(uname -m)"
case "$native_arch" in
  x86_64)
    configure_target="darwin64-x86_64-cc"
    ;;
  arm64)
    configure_target="darwin64-arm64-cc"
    deployment_major="${MACOSX_DEPLOYMENT_TARGET%%.*}"
    [[ "$deployment_major" =~ ^[0-9]+$ ]] || fail "invalid deployment target"
    (( deployment_major >= 11 )) || fail "arm64 requires macOS 11.0 or newer"
    ;;
  *)
    fail "unsupported native architecture: $native_arch"
    ;;
esac
readonly native_arch configure_target

[[ "$MACOSX_DEPLOYMENT_TARGET" =~ ^[0-9]+([.][0-9]+){1,2}$ ]] || \
  fail "invalid MACOSX_DEPLOYMENT_TARGET: $MACOSX_DEPLOYMENT_TARGET"
[[ "$OPENSSL_PREFIX" == /* && "$OPENSSL_PREFIX" != "/" ]] || \
  fail "OPENSSL_PREFIX must be an absolute non-root path"
[[ ! -e "$OPENSSL_PREFIX" && ! -L "$OPENSSL_PREFIX" ]] || \
  fail "OPENSSL_PREFIX must not already exist: $OPENSSL_PREFIX"

for tool in curl shasum tar make perl xcrun; do
  command -v "$tool" >/dev/null || fail "$tool is required"
done

clang="$(xcrun --find clang)"
lipo="$(xcrun --find lipo)"
nm_tool="$(xcrun --find nm)"
sdk_root="$(xcrun --sdk macosx --show-sdk-path)"
readonly clang lipo nm_tool sdk_root

build_jobs="${OPENSSL_BUILD_JOBS:-2}"
[[ "$build_jobs" =~ ^[1-9][0-9]*$ ]] || fail "OPENSSL_BUILD_JOBS must be positive"
(( build_jobs <= 64 )) || fail "OPENSSL_BUILD_JOBS must not exceed 64"

destination_parent_input="$(dirname "$OPENSSL_PREFIX")"
destination_name="$(basename "$OPENSSL_PREFIX")"
[[ -n "$destination_name" && "$destination_name" != "." && "$destination_name" != ".." ]] || \
  fail "OPENSSL_PREFIX has an invalid final component"
mkdir -p "$destination_parent_input"
destination_parent="$(cd "$destination_parent_input" && pwd -P)"
destination_path="$destination_parent/$destination_name"
[[ ! -e "$destination_path" && ! -L "$destination_path" ]] || \
  fail "resolved OPENSSL_PREFIX already exists: $destination_path"

temporary_root="$(mktemp -d "${TMPDIR:-/tmp}/omni-openssl-darwin.XXXXXX")"
staging_prefix="$(mktemp -d "$destination_parent/.omni-openssl-stage.XXXXXX")"
cleanup() {
  rm -rf "$temporary_root" "$staging_prefix"
}
trap cleanup EXIT INT TERM

archive="$temporary_root/openssl.tar.gz"
curl --fail --location --silent --show-error --retry 5 \
  "https://github.com/openssl/openssl/releases/download/openssl-${OPENSSL_VERSION}/openssl-${OPENSSL_VERSION}.tar.gz" \
  --output "$archive"
actual_sha256="$(shasum -a 256 "$archive" | awk '{print $1}')"
[[ "$actual_sha256" == "$OPENSSL_SHA256" ]] || \
  fail "OpenSSL archive checksum mismatch: expected $OPENSSL_SHA256, got $actual_sha256"

archive_root="openssl-${OPENSSL_VERSION}"
while IFS= read -r archive_entry; do
  case "$archive_entry" in
    "$archive_root" | "$archive_root/" | "$archive_root/"*) ;;
    *) fail "OpenSSL archive contains a path outside $archive_root: $archive_entry" ;;
  esac
  case "/$archive_entry/" in
    *"/../"*) fail "OpenSSL archive contains a parent-directory path: $archive_entry" ;;
  esac
done < <(tar -tzf "$archive")

tar -xzf "$archive" -C "$temporary_root"
source_dir="$temporary_root/$archive_root"
[[ -x "$source_dir/Configure" ]] || fail "verified archive does not contain Configure"

common_flags="-arch $native_arch -isysroot $sdk_root -mmacosx-version-min=$MACOSX_DEPLOYMENT_TARGET -O2 -fstack-protector-strong"
(
  cd "$source_dir"
  CC="$clang" CFLAGS="$common_flags" LDFLAGS="$common_flags" \
    ./Configure "$configure_target" \
      no-shared no-tests no-apps no-docs no-module no-legacy \
      --prefix="$destination_path" \
      --openssldir="$destination_path/ssl"
  make -s -j"$build_jobs" build_libs
)

archive_path="$source_dir/libcrypto.a"
[[ -f "$archive_path" && ! -L "$archive_path" ]] || fail "static libcrypto.a was not built"
archive_arches="$($lipo -archs "$archive_path" 2>/dev/null)" || fail "libcrypto.a is not a Mach-O archive"
[[ "$archive_arches" == "$native_arch" ]] || fail "libcrypto.a has unexpected architectures: $archive_arches"
symbols_file="$temporary_root/libcrypto.symbols"
"$nm_tool" -g "$archive_path" > "$symbols_file"
grep -Eq '[[:space:]]_EVP_aes_256_cbc$' "$symbols_file" || \
  fail "libcrypto.a lacks the required EVP provider symbols"

mkdir -p "$staging_prefix/include" "$staging_prefix/lib"
cp -R "$source_dir/include/openssl" "$staging_prefix/include/openssl"
install -m 0644 "$archive_path" "$staging_prefix/lib/libcrypto.a"
find "$staging_prefix/include" -type f -exec chmod 0644 {} +
find "$staging_prefix/include" -type d -exec chmod 0755 {} +
chmod 0755 "$staging_prefix" "$staging_prefix/lib"

[[ -f "$staging_prefix/include/openssl/crypto.h" ]] || fail "OpenSSL public headers are incomplete"
if find "$staging_prefix" -type f \( -name '*.dylib' -o -name '*.so' -o -name 'libssl.a' \) | grep -q .; then
  fail "staged OpenSSL prefix contains an unexpected library"
fi

mv "$staging_prefix" "$destination_path"
trap - EXIT INT TERM
rm -rf "$temporary_root"
printf 'Built OpenSSL %s static libcrypto for macOS/%s at %s\n' \
  "$OPENSSL_VERSION" "$native_arch" "$destination_path"
