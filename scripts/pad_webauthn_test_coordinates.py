"""Correct only v0.18.2's generated P-256 test coordinates in a disposable cache."""

import json
from pathlib import Path
import stat
import subprocess
import sys


def pad_coordinates(source):
    marker = "func getTPMAttestionKeys()"
    prefix, found, remainder = source.partition(marker)
    body, ending, suffix = remainder.partition("\n}")
    if not found or not ending or marker in remainder:
        raise ValueError("Unexpected WebAuthn TPM test helper; review the workaround")
    if "ecdsa.GenerateKey(elliptic.P256(), rand.Reader)" not in body:
        raise ValueError("TPM test helper no longer generates a P-256 key")
    for axis in ("X", "Y"):
        original = f"\t\t{axis}Coord:    eccKey.{axis}.Bytes(),"
        if body.count(original) != 1:
            raise ValueError(f"Unexpected TPM test {axis} coordinate; review the workaround")
        # RFC 9053 section 7.1.1 requires preserving leading-zero octets.
        # FillBytes preserves the generated integer and fills only its missing
        # leading bytes. Oversized values fail rather than being truncated.
        body = body.replace(original, f"\t\t{axis}Coord:    eccKey.{axis}.FillBytes(make([]byte, 32)),")
    return prefix + found + body + ending + suffix


def patch_test_fixture(module, cache):
    if module.get("Version") != "v0.18.2":
        return False
    source = Path(module["Dir"]).resolve() / "protocol" / "attestation_tpm_test.go"
    if not source.resolve().is_relative_to(Path(cache).resolve()):
        raise ValueError("Refusing to modify a module outside the disposable test cache")
    original = source.read_text(encoding="utf-8")
    corrected = pad_coordinates(original)
    source.chmod(source.stat().st_mode | stat.S_IWUSR)
    source.write_text(corrected, encoding="utf-8")
    return True


if __name__ == "__main__":
    module = json.loads(subprocess.check_output(
        ["go", "list", "-mod=readonly", "-m", "-json", "github.com/go-webauthn/webauthn"], text=True))
    changed = patch_test_fixture(module, sys.argv[1])
    print(f"WebAuthn {module.get('Version')}: TPM test coordinate padding applied={changed}")
