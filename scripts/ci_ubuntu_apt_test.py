import importlib.util
from pathlib import Path
import tempfile
import unittest


spec = importlib.util.spec_from_file_location("ci_ubuntu_apt", Path(__file__).with_name("ci_ubuntu_apt.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class UbuntuAPTTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix="omni-apt-test-")
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        (self.root / "apt.conf.d").mkdir()
        (self.root / "sources.list.d").mkdir()

    def test_removes_failed_mirror_and_preserves_official_fallbacks_and_sources(self):
        fallbacks = "https://archive.ubuntu.com/ubuntu/\tpriority:2\nhttps://security.ubuntu.com/ubuntu/\tpriority:3\n"
        mirrors = self.root / "apt-mirrors.txt"
        mirrors.write_text("http://azure.archive.ubuntu.com/ubuntu/\tpriority:1\n" + fallbacks)
        source = self.root / "sources.list.d" / "ubuntu.sources"
        source_text = "URIs: mirror+file:/etc/apt/apt-mirrors.txt\nSuites: noble noble-security\nSigned-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg\n"
        source.write_text(source_text)
        self.assertEqual(module.configure_apt(self.root), 1)
        self.assertEqual(mirrors.read_text(), fallbacks)
        self.assertEqual(source.read_text(), source_text)

    def test_refuses_to_remove_only_mirror_before_writing_any_configuration(self):
        mirrors = self.root / "apt-mirrors.txt"
        original = "http://azure.archive.ubuntu.com/ubuntu/\tpriority:1\n"
        mirrors.write_text(original)
        with self.assertRaisesRegex(ValueError, "official HTTPS Ubuntu fallback"):
            module.configure_apt(self.root)
        self.assertEqual(mirrors.read_text(), original)
        self.assertFalse((self.root / "apt.conf.d" / "80omni-network").exists())

    def test_does_not_accept_http_fallback_or_similarly_named_host(self):
        for fallback in ["http://archive.ubuntu.com/ubuntu/", "https://archive.ubuntu.com.example/ubuntu/"]:
            with self.subTest(fallback=fallback):
                mirrors = self.root / "apt-mirrors.txt"
                original = "https://azure.archive.ubuntu.com/ubuntu/\n" + fallback + "\n"
                mirrors.write_text(original)
                with self.assertRaises(ValueError):
                    module.configure_apt(self.root)
                self.assertEqual(mirrors.read_text(), original)

    def test_arm_ports_sources_work_without_an_amd64_mirror_file(self):
        source = self.root / "sources.list.d" / "ubuntu.sources"
        original = "URIs: http://ports.ubuntu.com/ubuntu-ports\nSuites: noble\n"
        source.write_text(original)
        self.assertEqual(module.configure_apt(self.root), 0)
        self.assertEqual(source.read_text(), original)
        self.assertFalse((self.root / "apt-mirrors.txt").exists())
        self.assertTrue((self.root / "apt.conf.d" / "80omni-network").exists())

    def test_repeated_configuration_is_idempotent(self):
        mirrors = self.root / "apt-mirrors.txt"
        mirrors.write_text("http://azure.archive.ubuntu.com/ubuntu/\nhttps://archive.ubuntu.com/ubuntu/\n")
        self.assertEqual(module.configure_apt(self.root), 1)
        first = {path.relative_to(self.root): path.read_text() for path in self.root.rglob("*") if path.is_file()}
        self.assertEqual(module.configure_apt(self.root), 0)
        second = {path.relative_to(self.root): path.read_text() for path in self.root.rglob("*") if path.is_file()}
        self.assertEqual(second, first)


if __name__ == "__main__":
    unittest.main()
