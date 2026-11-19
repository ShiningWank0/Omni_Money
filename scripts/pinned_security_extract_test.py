import importlib.util
import io
from pathlib import Path
import tarfile
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("extract", Path(__file__).with_name("pinned-security-extract.py"))
extract = importlib.util.module_from_spec(spec)
spec.loader.exec_module(extract)


class ExtractionTests(unittest.TestCase):
    def archive(self, folder, name, kind=tarfile.REGTYPE):
        path = Path(folder) / "payload.tar.gz"
        with tarfile.open(path, "w:gz") as archive:
            member = tarfile.TarInfo(name)
            member.type = kind
            member.linkname = "/tmp/escape"
            content = b"\x7fELFfixture"
            if kind == tarfile.REGTYPE:
                member.size = len(content)
            archive.addfile(member, io.BytesIO(content) if member.size else None)
        return path

    def test_regular_payload_is_readable_for_static_analysis(self):
        with tempfile.TemporaryDirectory() as folder:
            path = self.archive(folder, "bin/tool")
            target = Path(folder) / "unpacked"
            extract.unpack(path, target)
            self.assertEqual((target / "bin/tool").read_bytes(), b"\x7fELFfixture")
            self.assertEqual((target / "bin/tool").stat().st_mode & 0o777, 0o700)

    def test_traversal_and_special_files_are_rejected(self):
        for name, kind in [("../outside", tarfile.REGTYPE), ("/absolute", tarfile.REGTYPE),
                           ("link", tarfile.SYMTYPE), ("hardlink", tarfile.LNKTYPE), ("fifo", tarfile.FIFOTYPE)]:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as folder:
                with self.assertRaises(ValueError):
                    extract.unpack(self.archive(folder, name, kind), Path(folder) / "unpacked")


if __name__ == "__main__":
    unittest.main()
