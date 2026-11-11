"""Configure GitHub Ubuntu runners before apt or Playwright installs packages."""

import os
from pathlib import Path
import sys
from urllib.parse import urlsplit


def configure_apt(apt_root: Path) -> int:
    mirrors = apt_root / "apt-mirrors.txt"
    removed = 0
    if mirrors.exists():
        original = mirrors.read_text()
        kept = []
        official_fallback = False
        for line in original.splitlines(keepends=True):
            value = line.strip()
            uri = urlsplit(value.split()[0]) if value and not value.startswith("#") else None
            if uri and uri.hostname == "azure.archive.ubuntu.com":
                removed += 1
                continue
            if uri and uri.scheme == "https" and uri.hostname in {"archive.ubuntu.com", "security.ubuntu.com"}:
                official_fallback = True
            kept.append(line)
        if removed and not official_fallback:
            raise ValueError("Azure mirror has no official HTTPS Ubuntu fallback")
        if removed:
            mirrors.write_text("".join(kept))

    # ARM runners use ports.ubuntu.com rather than this mirror list. Leave
    # their sources, suites, components and signature verification unchanged.
    config = apt_root / "apt.conf.d" / "80omni-network"
    config.write_text('Acquire::Retries "2";\nAcquire::http::Timeout "30";\nAcquire::https::Timeout "30";\n')
    return removed


if __name__ == "__main__":
    if sys.platform != "linux" or os.environ.get("GITHUB_ACTIONS") != "true":
        raise SystemExit("APT configuration is restricted to Linux GitHub Actions runners")
    if os.geteuid() != 0:
        raise SystemExit("Run the CI APT configuration with sudo")
    count = configure_apt(Path("/etc/apt"))
    print(f"Configured bounded APT network retries; removed {count} Azure mirror entries")
