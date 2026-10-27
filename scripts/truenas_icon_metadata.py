#!/usr/bin/env python3
"""Print an offline TrueNAS metadata.icon entry for the existing web favicon."""

import argparse
import base64
import json
from pathlib import Path
import re
import xml.etree.ElementTree as ET


ICON_PATH = Path(__file__).resolve().parents[1] / "frontend/src/assets/favicon.svg"
SVG_NS = "http://www.w3.org/2000/svg"
ALLOWED_ELEMENTS = {
    "svg", "defs", "g", "clipPath", "path", "circle", "ellipse", "rect",
    "line", "polygon", "polyline", "linearGradient", "radialGradient", "stop",
}


def icon_data_uri(data: bytes) -> str:
    # This generator accepts only the repository's static drawing vocabulary.
    # Reject active/external content rather than sanitize and change the icon.
    if not data or len(data) > 64 * 1024:
        raise ValueError("SVG must be nonempty and at most 64 KiB")
    text = data.decode("utf-8-sig")
    if "\x00" in text or re.search(r"<!\s*(DOCTYPE|ENTITY)\b", text, re.IGNORECASE):
        raise ValueError("XML declarations with entities are not allowed")
    if re.search(r"<\?(?!xml(?:\s|\?>))", text, re.IGNORECASE):
        raise ValueError("SVG processing instructions are not allowed")
    root = ET.fromstring(text)
    if root.tag != f"{{{SVG_NS}}}svg":
        raise ValueError("Expected an SVG drawing")
    for element in root.iter():
        if element.tag not in {f"{{{SVG_NS}}}{name}" for name in ALLOWED_ELEMENTS}:
            raise ValueError("Unsupported or active SVG element")
        for attribute, value in element.attrib.items():
            name = attribute.rsplit("}", 1)[-1].lower()
            if name.startswith("on") or name == "style" or "\\" in value or attribute == "{http://www.w3.org/XML/1998/namespace}base":
                raise ValueError("SVG event handlers and inline styles are not allowed")
            if name == "href" and not re.fullmatch(r"#[A-Za-z_][\w:.-]*", value):
                raise ValueError("External SVG references are not allowed")
            if re.search(r"url\s*\(", value, re.IGNORECASE) and not re.fullmatch(
                r"url\(\s*#[A-Za-z_][\w:.-]*\s*\)", value, re.IGNORECASE
            ):
                raise ValueError("Only fragment-local SVG references are allowed")
    return "data:image/svg+xml;base64," + base64.b64encode(data).decode("ascii")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--data-uri", action="store_true", help="Print only the image data URI")
    args = parser.parse_args(argv)
    try:
        uri = icon_data_uri(ICON_PATH.read_bytes())
    except (OSError, ValueError, ET.ParseError) as error:
        parser.exit(1, f"Icon generation failed: {error}\n")
    # JSON strings are valid YAML scalar values. The two-space indentation
    # places this entry inside an existing top-level metadata mapping.
    print(uri if args.data_uri else "  icon: " + json.dumps(uri))


if __name__ == "__main__":
    main()
