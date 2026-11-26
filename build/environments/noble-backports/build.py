"""Build pinned Ubuntu source packages with the native Noble toolchain."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

from debian.deb822 import Dsc

inputs = Path(__file__).resolve().parent
output = Path("/out")
output.mkdir()
source_archive = output / "sources"
source_archive.mkdir()


def fetch(pool, name, expected):
    if Path(name).name != name:
        raise ValueError(name)
    dest = Path(name)
    url = f"https://archive.ubuntu.com/ubuntu/pool/main/{pool}/{name}"
    subprocess.run(["/usr/lib/apt/apt-helper", "-o", "Acquire::Retries=4",
                    "-o", "Acquire::https::Timeout=120", "download-file",
                    url, str(dest.resolve()), f"SHA256:{expected}"], check=True)
    return dest


def build(source, version, pool, digest, patch, revision="1"):
    filename = f"{source}_{version.split(':')[-1]}.dsc"
    dsc = fetch(pool, filename, digest)
    with dsc.open("rb") as file:
        description = Dsc(file)
    if description["Source"] != source or description["Version"] != version:
        raise ValueError(f"Unexpected source in {filename}")
    for entry in description["Checksums-Sha256"]:
        fetch(pool, entry["name"], entry["sha256"])
    # The repository pins the .dsc hash and the .dsc pins every source member.
    subprocess.run(["dpkg-source", "--no-check", "-x", filename, "src"], check=True)
    if patch != "-":
        patches = Path("src/debian/patches")
        shutil.copyfile(inputs / patch, patches / patch)
        with (patches / "series").open("a") as series:
            series.write(f"\n{patch}\n")
        subprocess.run(["quilt", "push", "--fuzz=0"], cwd="src", check=True,
                       env={**os.environ, "QUILT_PATCHES": "debian/patches"})
    changelog = Path("src/debian/changelog")
    changelog.write_text(
        f"{source} ({version}+omni{revision}) noble; urgency=medium\n\n"
        "  * Rebuild reviewed security fixes for the Omni Money CI image.\n\n"
        " -- Omni Money maintainers <noreply@users.noreply.github.com>  "
        "Thu, 26 Nov 2026 00:00:00 +0000\n\n" + changelog.read_text()
    )
    subprocess.run(["apt-get", "-y", "--no-install-recommends", "build-dep", "./src"], check=True)
    subprocess.run(["dpkg-buildpackage", "-b", "-us", "-uc", "-j2"], cwd="src", check=True)
    for package in Path.cwd().glob("*.deb"):
        shutil.move(package, output / package.name)
    # Preserve the pinned source descriptors with the installed package evidence.
    shutil.copyfile(dsc, source_archive / filename)


for line in (inputs / "sources.tsv").read_text().splitlines():
    if line and not line.startswith("#"):
        source, baseline, version, pool, digest, patch, *revision = line.split()
        with tempfile.TemporaryDirectory(dir="/tmp") as temp:
            os.chdir(temp)
            build(source, version, pool, digest, patch, *revision)
            os.chdir("/")
