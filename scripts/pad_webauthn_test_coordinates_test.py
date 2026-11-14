import tempfile
from pathlib import Path
import unittest

from pad_webauthn_test_coordinates import pad_coordinates, patch_test_fixture


HELPER = """func getTPMAttestionKeys() {
\teccKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
\tc := CredentialPublicKey{
\t\tXCoord:    eccKey.X.Bytes(),
\t\tYCoord:    eccKey.Y.Bytes(),
\t}
}
"""


class PaddingTests(unittest.TestCase):
    def test_only_the_generated_p256_fixture_is_changed(self):
        outside = "func otherTest() {\n\t\tXCoord:    eccKey.X.Bytes(),\n}\n"
        corrected = pad_coordinates(outside + HELPER + outside)
        self.assertTrue(corrected.startswith(outside))
        self.assertTrue(corrected.endswith(outside))
        self.assertEqual(corrected.replace(".FillBytes(make([]byte, 32))", ".Bytes()"), outside + HELPER + outside)
        self.assertEqual(corrected.count(".FillBytes(make([]byte, 32))"), 2)

    def test_unexpected_helper_fails_before_writing(self):
        for source in ("", HELPER + HELPER, HELPER.replace("P256()", "P384()"),
                       HELPER.replace("eccKey.Y.Bytes()", "changed()")):
            with self.subTest(source=source), self.assertRaises(ValueError):
                pad_coordinates(source)

    def test_only_a_test_file_in_the_disposable_cache_can_be_written(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            module_dir = root / "cache" / "module"
            protocol = module_dir / "protocol"
            protocol.mkdir(parents=True)
            source = protocol / "attestation_tpm_test.go"
            production = protocol / "attestation_tpm.go"
            source.write_text(HELPER, encoding="utf-8")
            production.write_text("production sentinel", encoding="utf-8")
            module = {"Version": "v0.18.2", "Dir": str(module_dir)}
            with self.assertRaises(ValueError):
                patch_test_fixture(module, root / "other-cache")
            self.assertEqual(source.read_text(encoding="utf-8"), HELPER)
            source.chmod(0o444)
            self.assertTrue(patch_test_fixture(module, root / "cache"))
            self.assertEqual(production.read_text(encoding="utf-8"), "production sentinel")
            source.write_text(HELPER, encoding="utf-8")
            self.assertFalse(patch_test_fixture({**module, "Version": "v0.18.3"}, root / "cache"))
            self.assertEqual(source.read_text(encoding="utf-8"), HELPER)


if __name__ == "__main__":
    unittest.main()
