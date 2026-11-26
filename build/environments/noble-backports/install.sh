#!/bin/bash
# Install every binary already used from these sources, including dev and GIR packages.
set -euo pipefail
inputs=/usr/local/share/omni-backports
inventory=/opt/omni-environment
target_debs=()
arch=$(dpkg --print-architecture)
dpkg-query -W -f='${db:Status-Status}\t${source:Package}\t${Package}\t${Version}\n' > /tmp/noble-installed.tsv
while read -r source baseline version pool digest patch; do
    [[ -n "$source" && "$source" != \#* ]] || continue
    found=false
    while read -r name current; do
        [[ "$current" == "$baseline" ]] || { echo "Review updated Noble package $name $current" >&2; exit 1; }
        matches=(/backports/"${name}"_*.deb)
        [[ ${#matches[@]} == 1 && -f "${matches[0]}" ]]
        deb=${matches[0]}
        [[ "$(dpkg-deb -f "$deb" Package)" == "$name" ]]
        [[ "$(dpkg-deb -f "$deb" Version)" == "${version}+omni1" ]]
        deb_arch=$(dpkg-deb -f "$deb" Architecture)
        [[ "$deb_arch" == "$arch" || "$deb_arch" == all ]]
        rm -f "$inventory/debs/${name}_"*.deb
        cp "$deb" "$inventory/debs/"
        target_debs+=("$deb")
        found=true
    done < <(awk -v src="$source" '$1 == "installed" && $2 == src {print $3, $4}' /tmp/noble-installed.tsv)
    "$found" || { echo "No installed packages from $source" >&2; exit 1; }
done < "$inputs/sources.tsv"
dpkg -i "${target_debs[@]}"
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
