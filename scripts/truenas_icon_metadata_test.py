import base64
from contextlib import redirect_stdout
import io
import json
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import truenas_icon_metadata as icon


class TrueNASIconMetadataTest(unittest.TestCase):
    def test_uses_identical_web_favicon_bytes(self):
        original = icon.ICON_PATH.read_bytes()
        uri = icon.icon_data_uri(original)
        prefix, payload = uri.split(",", 1)
        self.assertEqual(prefix, "data:image/svg+xml;base64")
        self.assertEqual(base64.b64decode(payload, validate=True), original)

    def test_prints_only_one_yaml_entry_without_changing_files(self):
        before = icon.ICON_PATH.read_bytes()
        output = io.StringIO()
        with redirect_stdout(output):
            icon.main([])
        line = output.getvalue()
        self.assertTrue(line.startswith("  icon: "))
        self.assertEqual(len(line.splitlines()), 1)
        self.assertEqual(json.loads(line[len("  icon: "):]), icon.icon_data_uri(before))
        self.assertEqual(icon.ICON_PATH.read_bytes(), before)

    def test_can_print_uri_for_an_existing_metadata_editor(self):
        output = io.StringIO()
        with redirect_stdout(output):
            icon.main(["--data-uri"])
        self.assertEqual(output.getvalue().strip(), icon.icon_data_uri(icon.ICON_PATH.read_bytes()))

    def test_rejects_active_or_external_image_content(self):
        for content in [
            '<script>alert(1)</script>',
            '<foreignObject><div>HTML</div></foreignObject>',
            '<image href="https://attacker.example/image.svg"/>',
            '<path onload="alert(1)"/>',
            '<path href="https://attacker.example/image.svg"/>',
            '<path style="fill:url(https://attacker.example/image.svg)"/>',
            '<path fill="url(https://attacker.example/image.svg)"/>',
            '<style>@import "https://attacker.example/style.css";</style>',
            '<g xml:base="https://attacker.example/"><path fill="url(#paint)"/></g>',
        ]:
            with self.subTest(content=content):
                with self.assertRaises(ValueError):
                    icon.icon_data_uri(f'<svg xmlns="{icon.SVG_NS}">{content}</svg>'.encode())
        for data in [b"", b"x" * (64 * 1024 + 1), b'<!DOCTYPE svg [<!ENTITY payload "value">]><svg/>', b'<svg\x00/>', b'<?xml-stylesheet href="https://attacker.example/style.css"?><svg/>']:
            with self.subTest(data=data[:64]):
                with self.assertRaises(ValueError):
                    icon.icon_data_uri(data)


if __name__ == "__main__":
    unittest.main()
