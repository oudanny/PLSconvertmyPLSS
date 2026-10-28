# Offline map GUI

`plssmap` opens a local map application in your existing browser. Its lookup
engine is the same Go library compiled to WebAssembly. Latitude/longitude and
UTM conversion, point-in-polygon tests, and reverse identifier lookup execute
inside a dedicated worker, so the map can continue to pan and zoom during I/O.

## Run the portable app

Place the launcher and an atlas together:

```text
plssmap.exe
us.atlas
```

Double-click `plssmap.exe`, or run it from a terminal. The launcher finds
`us.atlas` or `western-us.atlas` beside itself, chooses an available loopback
port, and opens your default browser. Keep its console window open while using
the map; press Ctrl+C to stop. On macOS/Linux the executable is `plssmap`.
An explicit path is also supported:

```sh
plssmap --data /path/to/us.atlas
```

You can start without a data file and choose one through **Choose atlas file**.
Coordinate notation and UTM conversion work without an atlas. A local atlas is
required for PLSS queries. No file is uploaded, and the application makes no
requests to a remote API, CDN, or map tile provider.

## Use the map

- The compact Windows XP-style interface keeps a readable **PLSS** row together
  with decimal degrees, DMS, DDM, UTM, and ISO 6709 in **Converted coordinates**.
  For example, the Reno result reads `SW¼ of NE¼ · Sec. 11 · T. 19 N · R. 19 E`,
  with `Mount Diablo Meridian · Nevada` underneath. The PLSS copy button includes
  both lines.
- Expand **Published survey records** for the exact original BLM identifiers,
  all matching polygons, and selection controls. Changing the selected polygon
  updates the PLSS description; reverse lookups also update its representative
  coordinates. Human-readable descriptions are display output; the **PLSS ID**
  input still accepts exact published IDs.
- Enter decimal degrees, DDM, DMS, or ISO 6709 on the **Lat / Long** tab.
- Enter a zone, hemisphere, easting and northing on the **UTM** tab. `N` and `S`
  mean hemispheres; latitude bands must be explicit in the input syntax.
- Click any map point to convert it and find containing published polygons.
- Use **PLSS ID** for exact `PLSSID`, `FRSTDIVID`, `SECDIVID`, or `SURVID`
  identifiers. The identifier type matters; records can lack an identifier at
  their own layer and display a parent ID instead. The result card identifies
  the displayed field when available.
- Select a result or **Show polygon** to fit its boundary. All matching records
  remain available, including duplicate IDs and shared-boundary matches.
- Copy a coordinate notation or export the result, source provenance and
  original rings as JSON. Reverse results use an interior representative point.
- The region buttons move the map to the lower 48, Alaska, or Hawaii. The map
  background is geographic context; it does not indicate PLSS coverage.

The background geography is generalized Natural Earth land, lakes, states and
city labels. It remains available offline, including across the dateline.
It does not include streets or satellite imagery. The highlighted BLM polygons
retain their original atlas coordinates; rendering shifts into another world
copy do not alter lookup or exported geometry. See [map sources](map-sources.md).

## Build from source

From the repository root, with the project's supported Go toolchain:

```sh
go run ./internal/guibuild
bin/plssmap --data data/us.atlas
```

On Windows use `bin/plssmap.exe`. `--out` changes the output executable path.
The builder compiles `cmd/plsswasm` with `GOOS=js GOARCH=wasm`, copies the
**matching toolchain's** `wasm_exec.js` and Go license, and embeds those files
along with the UI, Leaflet, and map data in the native launcher. The generated
runtime files are ignored by Git. Building the launcher directly without
generating these assets produces a clear startup error.

Ordinary `go test ./...` and CLI/library builds work from a fresh checkout
without precompiled WebAssembly. CI additionally builds the GUI and runs the
real WASM engine and local HTTP smoke tests on Windows, macOS, and Linux.

## Large-file access

The national data file is not copied wholesale into JavaScript or WASM memory.
The worker borrows an `io.ReaderAt` through either of these interfaces:

1. A user-selected browser `File`: bounded slices read through
   `FileReaderSync` inside the dedicated worker.
2. The launcher's selected atlas: same-origin HTTP byte ranges over loopback.
   Responses must be `206 Partial Content` with the exact expected range and
   file size. The launcher never performs the coordinate query itself.

The byte cache retains at most 8 MiB, with six state readers kept initialized.
Selected-file reads are at most 1 MiB each; loopback HTTP reads can batch up to
8 MiB. The six-state limit accommodates the observed maximum of five overlapping
national source bounds, including the unassigned partition. First queries in a
state can cost more than subsequent nearby
queries. The UI reports worker operation time, including file access,
conversion, representative point calculation, and returned geometry. These
numbers are not directly comparable to the native library's containment-only
microbenchmarks. The source atlas must remain unchanged while open.

The loopback server binds only a loopback IP, refuses unrelated Host/Origin
requests, accepts only GET/HEAD, exposes only its selected atlas and embedded
assets, and has no upload or filesystem mutation endpoint. Its content policy
blocks external connections and scripts. `--no-browser` prints its URL without
opening a browser; `--listen 127.0.0.1:PORT` selects a fixed local port.

## Verified desktop build

The Windows portable folder `dist/national-us/` contains only `plssmap.exe`
(17,505,280 bytes) and `us.atlas` (1,362,497,879 bytes), totaling
**1,380,003,159 bytes (1.380 GB)**. The local atlas copy is a hard link to the
verified build; copying the folder to another device produces ordinary files.

Actual browser checks on the development Windows laptop verified automatic
sibling-atlas discovery, direct browser file selection, DD/UTM conversion,
reverse identifiers, exact polygon rendering, a positive-longitude Alaska
polygon near Attu, explicit New York coverage gaps, and Anchorage's anonymous
source polygon. JSON export retained all 31 source entries and the original
anonymous record's geometry. Invalid coordinates clear stale results. Inputs
are disabled during worker requests so a pending result cannot appear under
a newly edited coordinate.

Illustrative single-run browser measurements, with warm OS file pages:

| Operation | Automatic loopback ranges | Browser-selected local file |
| --- | ---: | ---: |
| Open national metadata | 732 ms | 426 ms |
| First Reno lookup | 478 ms | 171 ms |
| Repeated Reno lookup | 0.7 ms | about 0.4 ms engine time |

A cached reverse Reno lookup took 2.2 ms through the worker. Repeating a query
at the five-partition overlap, then returning to Reno, required **zero additional
file reads**. Loopback opening plus the first Reno lookup read 96.1 MB in total;
the direct-file path read 48.6 MB. Neither read the full 1.36 GB file. These are
observed samples, not latency percentiles or cold-disk guarantees. Browser
painting, Go garbage collection, and file-access mode affect response time.
The reported Go heap is not the browser's total resident memory.
The observed counters are retained in
[`browser-national-samples.json`](../internal/mapengine/browser-national-samples.json).

The updated XP-style presentation was checked at 1280×720: readable PLSS and all
five coordinate formats fit simultaneously beside the map. Browser checks also
covered exact-ID reverse lookup, missing-ID Anchorage geometry, a New York
coverage gap, and a real four-polygon Reno boundary. Selecting a different
government lot changed the PLSS row while retaining the original queried point.
The [captioned walkthrough and screenshots](walkthrough/README.md) show this build.

The automated Node probe runs the actual Go WASM engine with file access
adapters, checks native/WASM parity for all 31 shards and 25 reverse identifiers,
and verifies 400 nearby queries without additional reads. Its timing environment
differs from the browser. Raw reports are in
[`internal/mapengine`](../internal/mapengine/wasm-national-results.json).

## Coverage and limits

The national atlas contains published BLM PLSS records, including areas in
Alaska. Some U.S. areas use other land-description systems, and published
coverage can have gaps. A no-match result is explicit; no idealized survey
polygon is invented. Unassigned source records are retained separately rather
than silently discarded. See [national coverage](national-atlas.md).

Structured township/range descriptions remain in the `plssconvert` CLI with
SQLite or the live source. This first compact GUI uses exact published IDs.
PLSS coordinates are representative points, not surveyed corners; BLM's GIS
data are not legal surveys. The compact file format is still experimental.
