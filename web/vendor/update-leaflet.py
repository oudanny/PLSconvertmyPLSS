"""Fetch the pinned, unmodified Leaflet browser assets from its official source."""

import base64
import hashlib
import json
from pathlib import Path
import urllib.request

VERSION = "1.9.4"
BASE = f"https://unpkg.com/leaflet@{VERSION}/"
DEST = Path(__file__).resolve().parent / "leaflet"
FILES = ["leaflet.js", "leaflet.css", "leaflet.js.map", "images/layers.png",
         "images/layers-2x.png", "images/marker-icon.png", "images/marker-icon-2x.png",
         "images/marker-shadow.png"]
# Published by https://leafletjs.com/download.html for this exact release.
INTEGRITY = {
    "leaflet.js": "20nQCchB9co0qIjJZRGuk2/Z9VM+kNiyxNV1lvTlZBo=",
    "leaflet.css": "p4NxAoJBhIIN+hmNHrzRCf9tD/miZyoHS5obTRR9BMY=",
}

manifest = {"name": "Leaflet", "version": VERSION, "license": "BSD-2-Clause",
            "upstream": "https://leafletjs.com/download.html", "files": []}
for name in [*FILES, "LICENSE"]:
    url = BASE + (name if name == "LICENSE" else "dist/" + name)
    with urllib.request.urlopen(url, timeout=60) as response:
        raw = response.read()
    digest = hashlib.sha256(raw).digest()
    if name in INTEGRITY and base64.b64encode(digest).decode() != INTEGRITY[name]:
        raise RuntimeError(f"Published integrity check failed for {name}")
    path = DEST / name
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(raw)
    manifest["files"].append({"file": name, "bytes": len(raw), "sha256": digest.hex(), "url": url})
(DEST / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
print(f"Leaflet {VERSION}: {sum(f['bytes'] for f in manifest['files']):,} bytes, including source map and license")
