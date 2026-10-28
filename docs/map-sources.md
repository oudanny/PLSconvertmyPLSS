# Offline map assets

The GUI's context map is bundled with the application. Opening and navigating
it does not contact a tile provider or CDN. The context map and the exact PLSS
lookup geometry are separate: Natural Earth's generalized geography helps with
navigation; the loaded BLM atlas determines coordinate matches and boundaries.

## Leaflet

[Leaflet 1.9.4](https://leafletjs.com/download.html) provides pan, zoom, scale,
and vector overlays. It is vendored under `web/vendor/leaflet/`, including its
[BSD 2-Clause license](../web/vendor/leaflet/LICENSE), image assets, and source
map. The JavaScript and CSS are unmodified copies of the pinned npm release,
downloaded from the CDN documented by Leaflet. The updater checks both against
the SHA-256 integrity values published on Leaflet's download page. There is no
CDN dependency at application runtime.

The library, stylesheet, images, source map, and license total 395,800 bytes.
The source map alone is 225,544 bytes and is only useful to browser developer
tools. `web/vendor/leaflet/manifest.json` records file sizes, hashes, and URLs.
Rebuild these vendor files with:

```sh
python web/vendor/update-leaflet.py
```

## Natural Earth

The geometry comes from the official
[`natural-earth-vector` v5.1.2 tag](https://github.com/nvkelso/natural-earth-vector/tree/v5.1.2/geojson).
Natural Earth identifies its map data as
[public domain](https://www.naturalearthdata.com/about/terms-of-use/), including
permission to modify and redistribute it. Attribution is included voluntarily.

| Bundled file | Source theme | Selection | Features | Bytes |
| --- | --- | --- | ---: | ---: |
| `land.geojson` | 50m land | Global | 1,420 | 1,498,886 |
| `states.geojson` | 50m admin-1 states/provinces | 50 U.S. states and DC | 51 | 268,047 |
| `lakes.geojson` | 50m lakes | At least one vertex within longitude -180 to -45 and latitude 5 to 85 | 194 | 229,831 |
| `cities.geojson` | 50m populated places, simple | U.S., Canada, Mexico | 183 | 28,591 |

The four data files total **2,025,355 bytes**. They retain the source coordinates
without additional simplification or precision reduction. Unused attributes
were removed and JSON whitespace compacted. The source geometry was already
generalized for a map scale of 1:50 million; zooming in does not reveal streets,
buildings, imagery, or survey-quality coastlines. State outlines are display
geography and do not imply that PLSS records exist throughout a state.

Sources and documentation:

- [Land](https://www.naturalearthdata.com/downloads/50m-physical-vectors/50m-land/)
- [States and provinces](https://www.naturalearthdata.com/downloads/50m-cultural-vectors/50m-admin-1-states-provinces/)
- [Lakes](https://www.naturalearthdata.com/downloads/50m-physical-vectors/50m-lakes-reservoirs/)
- [Populated places](https://www.naturalearthdata.com/downloads/50m-cultural-vectors/50m-populated-places/)

`web/data/manifest.json` records the exact upstream URLs and SHA-256 values for
both original and derived files. `web/data/NOTICE.txt` accompanies the assets.
Rebuild using Python's standard library; no GIS package is needed:

```sh
python web/data/build-basemap.py
```

The build asserts that all 50 states plus DC are present, including Alaska and
Hawaii. The checked-in polygons were additionally checked for closed rings,
finite in-range coordinates, and dateline-spanning edges. None of their
80,929 retained polygon vertices belongs to an edge spanning over 180 degrees.

## Integration and the dateline

All GeoJSON coordinates use ordinary longitude/latitude ordering. State
properties contain `name`, two-letter `code`, and a `label` point in that order.
City properties contain `name`, `country`, `rank`, and `minZoom`; use the last
two to avoid drawing all labels at low zoom.

The Natural Earth polygons are already cut at the antimeridian. Leaflet does
not automatically repeat vector layers when the view crosses the dateline.
For an Alaska view, shift each whole polygon part by a multiple of 360 degrees
toward the view's center. Keep its holes with the same part. Never independently
wrap individual vertices: that can draw a line across the whole map. A point
clicked in a shifted world copy must be normalized to -180..180 before sending
it to the lookup engine.

For arbitrary BLM polygons, unwrap consecutive longitudes first and preserve
all rings and even-odd filling. Display transformations must operate on copies;
they must not alter the exact coordinates used by the lookup engine. Avoid
fitting a dateline-crossing result using a naive global minimum/maximum
longitude, which would zoom out to nearly the entire world.

The bundled map is intentionally independent of online map services. Any
future optional online street or imagery layer should identify its provider,
retain its attribution, and make its network requirement explicit.

`web/geometry.js` supplies these display transforms without map-library or
runtime dependencies. It exports `MapGeometry` in the browser and CommonJS
exports for Node tests:

- `normalizeLongitude(longitude)` normalizes a clicked coordinate to -180..180.
- `displayRings(rings, centerLongitude)` returns new Leaflet `[lat, lon]` rings
  for a flat BLM ring list; render them with `fillRule: "evenodd"`.
- `shiftGeoJSON(value, centerLongitude)` returns a deep display copy, aligns
  each polygon's holes, and moves individual multipart polygons independently.
- `shiftPoint(coordinate, centerLongitude)` returns a copied `[lon, lat, ...]`
  point nearest the current view, preserving any extra ordinates.

The helpers unwrap ring edges before choosing the displayed world copy. Flat
BLM rings share one longitude plane so an exterior and hole cannot separate
when their means straddle the opposite side of the world. GeoJSON bounding
boxes are omitted from transformed copies because source boxes no longer
describe the displayed coordinates. Source objects are never mutated.

World-copy placement can change even when a view stays between -180 and 180.
Reposition overlays when the map finishes moving, not only when its center
crosses the antimeridian. Run the dependency-free Node tests with:

```sh
node --test web/geometry.test.cjs
```

These tests exercise dateline-crossing holes, disconnected parts, empty rings,
extra ordinates, frozen inputs, browser loading, and every bundled map feature.
