"""Rebuild the small offline context map from pinned upstream public data.

Run with Python 3.11+ from any directory. The application does not need Python.
Only attributes and features are filtered; retained coordinates are unchanged.
"""

from __future__ import annotations

import hashlib
import json
from pathlib import Path
import urllib.request


DEST = Path(__file__).resolve().parent
BASE = "https://raw.githubusercontent.com/nvkelso/natural-earth-vector/v5.1.2/geojson/"


def read_source(name: str) -> tuple[dict, dict]:
    url = BASE + name + ".geojson"
    with urllib.request.urlopen(url, timeout=60) as response:
        raw = response.read()
    return json.loads(raw), {
        "url": url,
        "sha256": hashlib.sha256(raw).hexdigest(),
        "bytes": len(raw),
    }


def points(value):
    if isinstance(value[0], (int, float)):
        yield value
    else:
        for child in value:
            yield from points(child)


def feature(original, properties):
    return {"type": "Feature", "properties": properties, "geometry": original["geometry"]}


def publish(name: str, features: list[dict], source: dict, description: str):
    raw = (json.dumps({"type": "FeatureCollection", "features": features},
                      ensure_ascii=False, separators=(",", ":")) + "\n").encode("utf-8")
    path = DEST / name
    path.write_bytes(raw)
    return {
        "file": name,
        "description": description,
        "features": len(features),
        "bytes": len(raw),
        "sha256": hashlib.sha256(raw).hexdigest(),
        "source": source,
    }


def main():
    manifest = {"upstream_tag": "v5.1.2", "license": "public domain", "datasets": []}
    source, info = read_source("ne_50m_land")
    land = [feature(f, {}) for f in source["features"]]
    manifest["datasets"].append(publish("land.geojson", land, info, "Global land, 1:50 million."))

    source, info = read_source("ne_50m_admin_1_states_provinces")
    states = []
    for f in source["features"]:
        p = f["properties"]
        if p["adm0_a3"] != "USA":
            continue
        states.append(feature(f, {
            "name": p["name"], "code": p["postal"],
            "label": [p["longitude"], p["latitude"]],
        }))
    assert len(states) == 51, "Expected 50 states plus the District of Columbia"
    assert {"AK", "HI"} <= {f["properties"]["code"] for f in states}
    manifest["datasets"].append(publish("states.geojson", states, info,
        "50 U.S. states and District of Columbia; display boundaries, not PLSS coverage."))

    source, info = read_source("ne_50m_lakes")
    lakes = []
    for f in source["features"]:
        p = f["properties"]
        if not any(-180 <= x <= -45 and 5 <= y <= 85 for x, y, *_ in points(f["geometry"]["coordinates"])):
            continue
        lakes.append(feature(f, {"name": p["name"]}))
    manifest["datasets"].append(publish("lakes.geojson", lakes, info,
        "Lakes with at least one vertex in North America (-180..-45 longitude, 5..85 latitude)."))

    source, info = read_source("ne_50m_populated_places_simple")
    cities = []
    for f in source["features"]:
        p = f["properties"]
        if p["adm0_a3"] not in {"USA", "CAN", "MEX"}:
            continue
        cities.append(feature(f, {
            "name": p["name"], "country": p["adm0_a3"],
            "rank": p["scalerank"], "minZoom": p["min_zoom"],
        }))
    manifest["datasets"].append(publish("cities.geojson", cities, info,
        "Published Natural Earth place labels in the U.S., Canada and Mexico."))
    (DEST / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    for item in manifest["datasets"]:
        print(f"{item['file']}: {item['features']} features, {item['bytes']:,} bytes")


if __name__ == "__main__":
    main()
