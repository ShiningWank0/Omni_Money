#!/bin/bash
# Install every binary already used from these sources, including dev and GIR packages.
set -euo pipefail
inputs=/usr/local/share/omni-backports
inventory=/opt/omni-environment
target_debs=()
arch=$(dpkg --print-architecture)
dpkg-query -W -f='${db:Status-Status}\t${source:Package}\t${Package}\t${Version}\t${source:Version}\n' > /tmp/noble-installed.tsv
while read -r source baseline version pool digest patch; do
    [[ -n "$source" && "$source" != \#* ]] || continue
    found=false
    while read -r name current source_version; do
        [[ "$source_version" == "$baseline" && "$current" == *"$baseline" ]] || { echo "Review updated Noble package $name $current" >&2; exit 1; }
        # bsdutils adds its own epoch to the util-linux source version.
        binary_prefix=${current%"$baseline"}
        [[ -z "$binary_prefix" || "$binary_prefix" =~ ^[0-9]+:$ ]]
        matches=(/backports/"${name}"_*.deb)
        [[ ${#matches[@]} == 1 && -f "${matches[0]}" ]]
        deb=${matches[0]}
        [[ "$(dpkg-deb -f "$deb" Package)" == "$name" ]]
        [[ "$(dpkg-deb -f "$deb" Version)" == "${binary_prefix}${version}+omni1" ]]
        deb_arch=$(dpkg-deb -f "$deb" Architecture)
        [[ "$deb_arch" == "$arch" || "$deb_arch" == all ]]
        rm -f "$inventory/debs/${name}_"*.deb
        cp "$deb" "$inventory/debs/"
        target_debs+=("$deb")
        found=true
    done < <(awk -v src="$source" '$1 == "installed" && $2 == src {print $3, $4, $5}' /tmp/noble-installed.tsv)
    "$found" || { echo "No installed packages from $source" >&2; exit 1; }
done < "$inputs/sources.tsv"
# Resolve versioned Breaks (ICU) and the rebuilt Python extension dependencies
# together, preserving newly required Noble archives alongside the local debs.
apt-get -y --no-remove --no-install-recommends -o Dir::Cache::archives="$inventory/debs" \
    install "${target_debs[@]}"
apt-get check
audit=$(dpkg --audit)
[[ -z "$audit" ]] || { echo "$audit" >&2; exit 1; }
for deb in "${target_debs[@]}"; do
    name=$(dpkg-deb -f "$deb" Package)
    [[ "$(dpkg-query -W -f='${Status} ${Version}' "$name")" == "install ok installed $(dpkg-deb -f "$deb" Version)" ]]
done
cp -r /backports/sources "$inventory/backport-sources"
cp "$inputs/"{*.patch,sources.tsv} "$inventory/backport-sources/"
rm /tmp/noble-installed.tsv
pkg-config --modversion xi xtst harfbuzz sqlite3 > "$inventory/backport-sources/pkg-config-versions.txt"
python3 - <<'PY'
import ctypes
import sqlite3

for library, function in (
    ("libXi.so.6", "XIQueryVersion"),
    ("libXtst.so.6", "XRecordQueryVersion"),
    ("libharfbuzz.so.0", "hb_buffer_create"),
):
    getattr(ctypes.CDLL(library), function)
with sqlite3.connect(":memory:") as db:
    db.execute("CREATE VIRTUAL TABLE items USING fts5(title)")
    db.executemany("INSERT INTO items VALUES (?)", [("rent",), ("book",)])
    assert db.execute("SELECT count(*) FROM items WHERE items MATCH 'rent'").fetchone() == (1,)
PY
dbus-run-session -- dbus-send --session --print-reply --dest=org.freedesktop.DBus \
    /org/freedesktop/DBus org.freedesktop.DBus.ListNames > "$inventory/backport-sources/dbus-session.txt"
findmnt --list --noheadings --target / > "$inventory/backport-sources/root-mount.txt"
# These Noble updates provide the companion fixes for libxslt and pyexpat.
dpkg --compare-versions "$(dpkg-query -W -f='${Version}' libxml2)" ge '2.9.14+dfsg-1.3ubuntu3.6'
dpkg --compare-versions "$(dpkg-query -W -f='${Version}' libexpat1)" ge '2.6.1-2ubuntu0.5'
pyexpat=$(python3.12 -c 'import pyexpat; print(pyexpat.__file__)')
readelf --wide --dyn-syms "$pyexpat" > /tmp/pyexpat-symbols
grep -E 'UND +XML_SetHashSalt16Bytes(@|$)' /tmp/pyexpat-symbols > "$inventory/backport-sources/pyexpat-salt-symbol.txt"
rm /tmp/pyexpat-symbols
python3.12 - <<'PY'
import ctypes
import xml.etree.ElementTree as ET
import xml.parsers.expat

parser = xml.parsers.expat.ParserCreate()
parser.Parse("<entries><entry>rent</entry></entries>", True)
assert ET.fromstring("<entries><entry>rent</entry></entries>").findtext("entry") == "rent"
for library, symbol in (("libxslt.so.1", "xsltApplyStylesheet"),
                        ("libsasl2.so.2", "sasl_client_init"),
                        ("libgstrtp-1.0.so.0", "gst_rtcp_buffer_map")):
    getattr(ctypes.CDLL(library), symbol)
PY
test ! -e /usr/bin/genrb
test -z "$(find /usr/lib -path '*/sasl2/libdigestmd5.so*' -print -quit)"
readelf --wide --dyn-syms /usr/bin/dash > /tmp/dash-symbols
grep -E 'UND +fnmatch(@|$)' /tmp/dash-symbols > "$inventory/backport-sources/dash-fnmatch.txt"
rm /tmp/dash-symbols
dash -c 'case expense.csv in *.csv) exit 0;; *) exit 1;; esac'
# The optional bytecode API is disabled while JavaScript source evaluation still works.
probe_dir=$(mktemp -d)
dpkg-deb -x /backports/duktape-dev_*.deb "$probe_dir"
cc -I"$probe_dir/usr/include" -x c - -o "$probe_dir/probe" -l:libduktape.so.207 <<'C'
#include <duktape.h>
#ifdef DUK_USE_BYTECODE_DUMP_SUPPORT
#error Precompiled bytecode must be disabled
#endif
int main(void) {
    duk_context *ctx = duk_create_heap_default();
    if (!ctx) return 1;
    int ok = duk_peval_string(ctx, "6 * 7") == 0 && duk_get_int(ctx, -1) == 42;
    duk_destroy_heap(ctx);
    return !ok;
}
C
"$probe_dir/probe"
rm -r "$probe_dir"
python3 - <<'PY'
import ctypes

slang = ctypes.CDLL("libslang.so.2")
slang.SLtt_tgoto.argtypes = [ctypes.c_char_p, ctypes.c_int, ctypes.c_int]
slang.SLtt_tgoto.restype = ctypes.c_char_p
assert slang.SLtt_tgoto(b"cursor", 4, 2) == b"cursor"
tiff = ctypes.CDLL("libtiff.so.6")
tiff.TIFFGetVersion.restype = ctypes.c_char_p
assert tiff.TIFFGetVersion().startswith(b"LIBTIFF, Version ")
PY
# Noble's official TIFF update includes both CVE-2026-12912 and CVE-2026-4775.
for package in libtiff6 libtiff-dev; do
    dpkg --compare-versions "$(dpkg-query -W -f='${Version}' "$package")" ge '4.5.1+git230720-4ubuntu2.6'
done
