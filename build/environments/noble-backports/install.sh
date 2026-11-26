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
dbus-run-session -- dbus-send --session --print-reply --dest=org.freedesktop.DBus \
    /org/freedesktop/DBus org.freedesktop.DBus.ListNames > "$inventory/backport-sources/dbus-session.txt"
findmnt --list --noheadings --target / > "$inventory/backport-sources/root-mount.txt"

# Noble's official TIFF update includes both CVE-2026-12912 and CVE-2026-4775.
for package in libtiff6 libtiff-dev; do
    dpkg --compare-versions "$(dpkg-query -W -f='${Version}' "$package")" ge '4.5.1+git230720-4ubuntu2.6'
done

# Check both DWARF section layouts with ordinary compiler-generated debug info.
probe_dir=$(mktemp -d)
printf '%s\n' 'struct item { long value; };' 'long value(struct item v) { return v.value; }' > "$probe_dir/item.c"
for dwarf in 4 5; do
    cc -O2 "-gdwarf-$dwarf" -fdebug-types-section -c "$probe_dir/item.c" -o "$probe_dir/item.o"
    readelf --debug-dump=info "$probe_dir/item.o" > "$inventory/backport-sources/dwarf$dwarf.txt"
    grep -q 'DW_TAG_structure_type' "$inventory/backport-sources/dwarf$dwarf.txt"
    objdump --dwarf=info "$probe_dir/item.o" > /dev/null
done
rm -rf "$probe_dir"
# A normal local push should retain the server hook's readable progress text.
git_probe=$(mktemp -d)
git init --bare -q "$git_probe/server"
git init -q -b main "$git_probe/client"
printf '%s\n' '#!/bin/sh' 'echo "reference accepted"' > "$git_probe/server/hooks/post-receive"
chmod +x "$git_probe/server/hooks/post-receive"
git -C "$git_probe/client" -c user.name=probe -c user.email=probe@example.invalid commit -qm initial --allow-empty
git -C "$git_probe/client" push "$git_probe/server" HEAD:refs/heads/main 2>"$git_probe/stderr"
grep -F 'remote: reference accepted' "$git_probe/stderr" > "$inventory/backport-sources/git-sideband.txt"
rm -rf "$git_probe"
