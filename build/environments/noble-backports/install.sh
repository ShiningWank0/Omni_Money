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
# Noble embeds pyexpat in python3.12. Shared-extension builds expose __file__.
pyexpat=$(python3.12 -c 'import pyexpat, sys; print(getattr(pyexpat, "__file__", sys.executable))')
readelf --wide --dyn-syms "$pyexpat" > /tmp/pyexpat-symbols
grep -Eq '[[:space:]][[:digit:]]+[[:space:]]+PyInit_pyexpat$' /tmp/pyexpat-symbols
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
