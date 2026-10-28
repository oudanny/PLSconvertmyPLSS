# Browser lookup engine

Build `./cmd/plsswasm` with `GOOS=js GOARCH=wasm` and use the matching Go
installation's `lib/wasm/wasm_exec.js`. The map launcher embeds both artifacts.
Run this engine in a **dedicated Web Worker**, never the browser's UI thread.

The bridge installs `globalThis.plssEngine(command, payloadJSON, optionalFile)`
and sets `plssEngineReady = true`. Calls return a JSON string with
`{ok, result, error, stats}`. A legitimate PLSS coverage gap returns a successful
coordinate conversion with `result.no_match = true` and an empty match array.

| Command | Payload |
| --- | --- |
| `open` | `{}` and a browser `File` as the third argument |
| `openURL` | `{url, size, name}` for the same-origin local atlas endpoint |
| `info`, `close` | `{}` |
| `convert` | `{kind: "latlon" or "utm", value: "..."}` |
| `lookup` | `{latitude, longitude}` in WGS84 decimal degrees |
| `find` | `{kind: "division", id: "published identifier"}`; also section, township, intersected, survey |

Conversions work before opening data. When data is loaded, conversion includes
PLSS lookup. Query results contain `coordinates`, `matches`, and optional notes.
Coordinates include DD, DDM, DMS, ISO6709, and UTM where supported. Matches keep
state, feature ID, layer, published identifier, representative coordinate, and
the original Esri `geometry.rings`; disconnected outer rings are not mislabeled
as GeoJSON holes. Display geometry may be unwrapped at the antimeridian, but the
lookup itself always uses the original source geometry.

Each match also includes `identifiers.{township,section,division,survey}`. These
are the actual four published fields. Some subdivision records lack both their
own division and survey IDs; their display `id` falls back to a parent identifier.
Looking up that parent identifier is not guaranteed to return the same record.
The `(state, feature_id)` pair still identifies the original matched source row.

## Memory and I/O

The **whole atlas is never loaded into WASM memory**, and selected files are
never uploaded. Local selection uses
`File.slice` and worker-only `FileReaderSync`; automatic opening from the portable
launcher uses synchronous worker XHR with HTTP byte ranges. Local file reads
are at most 1 MiB; HTTP bulk reads are at most 8 MiB to reduce round trips.
An LRU retains at most 8 MiB of file pages: 128 x 64 KiB
for local FileReaderSync, or 16 x 512 KiB for HTTP ranges to amortize request
latency. Up to six state readers are retained, each with its packed identifier index,
spatial tree/layers, and bounded geometry/prepared-polygon caches. Spatial leaf
records stay on file and only candidate leaves are read. Memory scales with
retained state indexes and small region metadata, not the whole national file. This is
not an 8 MiB total-memory limit: decoded indexes, Go runtime, and map rendering
consume additional memory.

The national snapshot has up to five overlapping shard rectangles, including a
global shard for records without a supported state prefix. Six slots avoid an
LRU cycle on repeated points near those overlaps. A smaller two-state cache can
reopen and reread states on every query, even for the same point.

Offsets remain int64 throughout the reader and JavaScript number conversion is
accepted only for safe integer file sizes. The page cache is tested with reads
above 4 GiB. Region opening reads headers and routing metadata; it does not hash
the whole dataset. Use `plssbuild verify --data FILE.atlas` for complete integrity
verification before distributing a file.

Region opening retains conservative identifier filters derived from the small
published-ID dictionaries. Reverse lookups rule out impossible states before
opening their full indexes, including when unrelated survey IDs contribute a
foreign prefix or wildcard route. Actual matches still come from the original
row-level index, preserving duplicate and foreign-prefix records. Unlocated
records are checked separately so filtering cannot hide a known missing polygon.

`stats` reports cumulative underlying bytes/read calls and page hits, current
page-cache bytes, current Go heap allocation (not browser RSS), and elapsed time
for the latest operation. Invalid replacement files preserve the previous
working atlas. Same-origin range responses must be HTTP 206 with the expected
Content-Range and byte length; file changes or range failures surface as errors.

The portable application's atlas must remain unchanged while open. File access
and lookups are local; an optional online basemap is a separate UI feature.

## Verification

Native tests cover request semantics, source duplicates, multipart/hole rings,
coverage gaps, source ownership, cancellation, and bounded random reads. A Node
smoke test runs the actual WASM binary against the full atlas, replacing only
the browser file API with `fs.readSync` and optionally comparing each result to
the native CLI:

```sh
node cmd/plsswasm/smoke.mjs bin/plss.wasm data/western-us.atlas /path/to/go/lib/wasm/wasm_exec.js bin/plssatlas.exe
```

The map was also exercised in an actual browser using local file selection and
the launcher's HTTP ranges, including conversion, map interaction, and reverse
lookup. See [the GUI guide and browser measurements](../../docs/map-gui.md).
Node does not implement native FileReaderSync, XMLHttpRequest, or map rendering,
so its reports below remain separate from that browser verification.

`PLSS_SMOKE_RANGE=1` exercises the actual range adapter with a simulated XHR
transport backed by the same local file; its timings do not include HTTP request
latency. `PLSS_SMOKE_POINTS=path.json` supplies an array of `[state,lat,lon]`
probes, allowing every national shard to be checked against the native CLI.

The recorded national run used one original-polygon interior point from each
of the 30 state shards and the unassigned-record shard. All 31 query result sets
and DD/DMS output matched the native CLI; 25 available record-level identifiers
also matched through reverse lookup. Eight matched rows lacked a layer-specific
identifier, so their parent display ID was not treated as an identifier for that
same source record. Another 200 nearby Reno queries and 200 queries in an area
with five overlapping shard rectangles required **zero additional file reads**
after warming those states. Raw results and reproducible probe coordinates:

- [`wasm-national-results.json`](../../internal/mapengine/wasm-national-results.json)
- [`wasm-national-points.json`](../../internal/mapengine/wasm-national-points.json)
- [`wasm-range-simulation.json`](../../internal/mapengine/wasm-range-simulation.json)

These are Node WASM measurements with a file-backed browser API adapter. They
do not measure browser UI rendering, HTTP latency, or physical cold-disk access.

Runtime references: [Go WebAssembly](https://go.dev/wiki/WebAssembly),
[worker FileReaderSync](https://developer.mozilla.org/en-US/docs/Web/API/FileReaderSync),
and [synchronous XHR in workers](https://developer.mozilla.org/en-US/docs/Web/API/XMLHttpRequest_API/Synchronous_and_Asynchronous_Requests).
