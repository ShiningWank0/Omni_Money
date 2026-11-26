"""Fetch the repository's fixed build-tool archives without sharing registry credentials with redirects."""
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
from tempfile import NamedTemporaryFile
from urllib.request import urlopen

repositories = {name: "shiningwank0/omni_money-ci-" + name for name in ("trivy", "engine")}


def fetch(tool, url, checksum, destination):
    repository = repositories[tool]
    if not re.fullmatch("[0-9a-f]{64}", checksum) or url != f"https://ghcr.io/v2/{repository}/blobs/sha256:{checksum}":
        raise ValueError("Invalid fixed tool archive")
    token_url = f"https://ghcr.io/token?service=ghcr.io&scope=repository:{repository}:pull"
    # The published package is public. Request an anonymous, repository-scoped pull token.
    with urlopen(token_url, timeout=30) as response:
        pull_token = json.load(response)["token"]
    if not isinstance(pull_token, str) or not pull_token or any(c.isspace() for c in pull_token):
        raise ValueError("Invalid registry token")
    # curl strips Authorization across hosts. Feed the header via stdin so it
    # is absent from the process arguments and logs.
    with NamedTemporaryFile(dir=destination.parent) as tmp:
        subprocess.run(["curl", "--disable", "--fail", "--silent", "--show-error", "--location",
                        "--retry", "3", "--proto", "=https", "--proto-redir", "=https",
                        "--max-filesize", str(512 * 1024 * 1024), "--header", "@-",
                        "--output", tmp.name, url],
                       input=f"Authorization: Bearer {pull_token}\n", text=True, check=True)
        digest = hashlib.sha256()
        for block in iter(lambda: tmp.read(1024 * 1024), b""):
            digest.update(block)
        if digest.hexdigest() != checksum:
            raise ValueError("Tool archive checksum mismatch")
        os.link(tmp.name, destination)


if __name__ == "__main__":
    fetch(sys.argv[1], sys.argv[2], sys.argv[3], Path(sys.argv[4]))
