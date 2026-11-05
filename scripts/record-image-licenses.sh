#!/bin/sh
set -eu

# Read the final installed APK inventory, after upgrades. No builder packages
# or source-tree paths enter the runtime image inventory.
destination="${1:?license output directory is required}"
mkdir -p "$destination"
arch="$(apk --print-arch)"
awk -v arch="$arch" '
BEGIN { RS=""; FS="\n"; print "Alpine runtime package notices\n" }
{
  name=""; version=""; license=""; origin=""; commit=""
  for (i=1; i<=NF; i++) {
    value=substr($i,3)
    if ($i ~ /^P:/) name=value
    if ($i ~ /^V:/) version=value
    if ($i ~ /^L:/) license=value
    if ($i ~ /^o:/) origin=value
    if ($i ~ /^c:/) commit=value
  }
  if (name == "") next
  if (version == "" || license == "" || origin == "" || commit !~ /^[0-9a-f]+$/ || length(commit) != 40) {
    print "Incomplete runtime license provenance: " name > "/dev/stderr"; failed=1; next
  }
  print name " " version "\nLicense: " license
  print "Package: https://pkgs.alpinelinux.org/package/v3.24/main/" arch "/" name
  print "Source recipe commit: " commit "\nSource origin: " origin "\n"
  count++
}
END { if (failed || count == 0) exit 1 }
' /lib/apk/db/installed > "$destination/ALPINE_PACKAGES.txt"
cp /lib/apk/db/installed "$destination/alpine-installed.txt"
cat > "$destination/ALPINE_SOURCES.txt" <<'SOURCES'
The exact binary package versions, source origins, and Alpine aports recipe
commits are recorded in ALPINE_PACKAGES.txt and alpine-installed.txt.
Alpine source recipes identify upstream source archives, checksums, local
patches, and build configurations. These records cover OS packages separately
from Omni Money application dependencies.
Source recipes: https://gitlab.alpinelinux.org/alpine/aports
Source archive mirror: https://distfiles.alpinelinux.org/distfiles/v3.24/
Distribution guidance: https://wiki.alpinelinux.org/wiki/Alpine_Linux:FAQ

This inventory is provenance information, not a replacement for the applicable
licenses or a written GPL source offer. A redistributor must also satisfy any
corresponding-source delivery requirements for the packages they distribute.
SOURCES
test -s "$destination/ALPINE_PACKAGES.txt"
