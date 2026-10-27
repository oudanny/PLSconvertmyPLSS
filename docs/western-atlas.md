# One-file western-US PLSS atlas

The compact reader supports Arizona, California, Colorado, Idaho, Montana,
New Mexico, Nevada, Oregon, Utah, Washington and Wyoming in one file. This is
the contiguous 11-state West; Alaska and Hawaii are not part of this build.
Coverage means the records published by BLM under those state prefixes, not
an assertion that every geographic location has a surveyed PLSS polygon.

## Completed dataset

The complete file is **741,964,969 bytes (741.96 MB / 707.59 MiB)**. It contains
**18,026,615 polygon records** and **24 unlocated records** from the October 27,
2026 captures. The source SQLite packs total 16,056,418,304 bytes; the conversion
file is 95.38% smaller. Source packs also retain ancillary attributes, while the
conversion file retains geometry, identifiers and the unlocated audit.

| State | Polygon records | Unlocated | State atlas MB |
| --- | ---: | ---: | ---: |
| Arizona | 1,542,351 | 0 | 62.05 |
| California | 2,281,307 | 18 | 104.36 |
| Colorado | 2,067,634 | 0 | 88.93 |
| Idaho | 1,249,811 | 1 | 47.07 |
| Montana | 2,425,196 | 2 | 99.06 |
| New Mexico | 1,860,015 | 1 | 70.73 |
| Nevada | 1,510,482 | 1 | 58.68 |
| Oregon | 1,579,039 | 1 | 65.02 |
| Utah | 1,315,533 | 0 | 51.23 |
| Washington | 631,123 | 0 | 32.50 |
| Wyoming | 1,564,124 | 0 | 62.26 |

The region adds a 65,536-byte directory to the state files. Its component totals
are 446,726,480 bytes of geometry, 191,083,727 bytes of spatial indexes,
103,368,330 bytes of identifiers/reverse indexes, and 786,432 bytes of headers.
These are actual completed-file measurements. Build reports, counts, per-state
source hashes and geometry verification hashes are in
[`western-builds.json`](../internal/westbench/western-builds.json).

The stripped Windows executable is 9,075,200 bytes. Together, the executable
and dataset total **751,040,169 bytes (751.04 MB)**. The local portable directory
is `dist/western-us/`, containing only `plssatlas.exe` and `western-us.atlas`.

The complete region SHA-256 is
`564d057cfd079d86d0f58d7daeb70e38b145388cc333284980f42408d52f57d9`.

## Measured lookup performance

Final region run: Windows amd64, Intel Core i7-1195G7, Go 1.27.3. These timings
reuse an open reader and warm OS file pages. The first-pass test creates a fresh
reader for each timing/allocation pass and initializes its state indexes with
`Verify`; prepared polygons and decoded geometry caches start empty. The
repeated tests can reuse prepared polygons from earlier queries.

| Workload | Queries | Median | p95 |
| --- | ---: | ---: | ---: |
| First pass, points scattered across all states | 11,000 | 46.8 µs | 102.7 µs |
| Repeat the scattered workload | 11,000 | 6.0 µs | 51.4 µs |
| Repeated representative points, rotating through 11 states | 10,000 | 2.3 µs | 17.9 µs |

The first-pass scattered workload allocates about 20.5 KB/58 allocations per
query; repeated representatives allocate about 580 bytes/9 allocations.
Coordinates are tested against exact polygons, while archival attributes remain
outside the conversion path. These workload definitions matter: a cache-warmed
median should not be presented as the cost of every new coordinate.

Before prepared-polygon caching, the repeated-representative p95 was **2,617 µs**;
the California 65-candidate case caused the long tail. Its removal is a change
in decoded geometry reuse and exact containment preparation, not a data-file
change. Historical and final raw results are retained in
[`western-before-cache.json`](../internal/westbench/western-before-cache.json)
and [`western-results.json`](../internal/westbench/western-results.json).

Opening the region measured 21.6 ms; a first query initializing state readers
took 5.0–18.6 ms in that run, excluding opening. A separate 33-process CLI test
measured **63.6 ms median / 87.1 ms p95**, including process startup, output and
exit. Earlier CLI runs measured 49–52 ms medians; host activity and OS caching
were uncontrolled. These are not cold-disk or other-hardware guarantees. See
[`western-cli-results.json`](../internal/westbench/western-cli-results.json).

Reproduce the complete correctness and timing pass with:

```sh
go run ./internal/westbench --data data/western-us.atlas --source-dir data/west --nv-db data/nevada.sqlite --points-per-state 1000
```

## Build and use

```sh
go build -trimpath -ldflags '-s -w' -o bin/plssbuild.exe ./cmd/plssbuild
go build -trimpath -ldflags '-s -w' -o bin/plssatlas.exe ./cmd/plssatlas
bin/plssbuild.exe west --work-dir data/west --out data/western-us.atlas --source NV=data/nevada.sqlite
bin/plssatlas.exe latlon --value '40.015,-105.2705' --data data/western-us.atlas
bin/plssatlas.exe plss --layer division --id NV210190N0190E0SN110ASWNE --data data/western-us.atlas --json
bin/plssbuild.exe verify --data data/western-us.atlas
```

Omit the optional `--source NV=...` when no Nevada source pack already exists.
Omit `.exe` for a non-Windows build. The builder needs network access for missing
source packs and temporary disk space. The finished lookup app needs only the
converter executable and the atlas; no Go runtime or network is needed.
Downloaded data and binaries are local artifacts, not files committed to Git.

`west` reuses completed source packs and verifies existing state atlases against
their source hashes before reuse. It resumes at completed-state boundaries;
an interrupted download restarts the affected state. A completed region output
is never overwritten. For a refresh, use a new work directory and output file.
Inputs must be closed, immutable, self-contained SQLite source packs; WAL or
journal sidecars are rejected rather than excluded from the provenance hash.
Build publication uses hard links, so build on a filesystem that supports them.
The finished data file can then be copied to removable storage.

For selected states, use `plssconvert data download --state AZ --out ...`, then
`plssbuild state --db ... --out ...`. Combine any completed state atlases with:

```sh
plssbuild bundle --out selected-states.atlas --atlas arizona.atlas --atlas utah.atlas
```

## Further lossless reductions

The original Nevada atlas was 65,199,261 bytes. The new one is **58,684,492 bytes**,
with the same 1,510,482 polygon records and one unlocated audit record.
The difference comes mainly from the shared coordinate dictionary.

Each block stores base longitude/latitude bit patterns, the widths of the
differing bits, and fixed-width residual bit streams. A vertex is recovered by
direct bit extraction and XOR. No floating-point rounding or geometric
simplification occurs. This preserves even small source slivers and rings.
Unlike DEFLATE-compressed coordinate blocks, a vertex does not require inflating
the block. Full Nevada geometry reads measured comparable throughput to the
raw dictionary; see [component measurements](../internal/geomlab/README.md).

Washington exposed a separate issue: most survey IDs repeated a division ID
already stored on the same record. Factoring that relationship reduced its key
component from **44,496,850 to 4,869,229 bytes**. Independent, non-hierarchical
IDs remain explicit exceptions. Every PLSSID, FRSTDIVID, SECDIVID and SURVID is
checked against its source value. The finished Washington atlas is
**32,503,203 bytes**, compared with 77,706,214 using the previous representation.

The current writer uses exact bit-packed vertices by default. `--vertices raw`
retains the direct float64 dictionary; `--vertices compressed` selects the
smaller DEFLATE experiment, which had slower scattered dictionary misses.
Old geometry v1 and key v1/v2 files remain readable. Current key output is v3.

## How a large region stays quick

The region file contains complete state sections and a small directory of
conservative extents, ID routes, source metadata and checksums. It is mapped
once. State readers initialize on demand and remain open for subsequent queries.
Only extents that could contain the point are searched. Actual source polygons
decide containment after the conservative spatial index selects candidates.

Across overlapping states, the reader returns all matches at the finest
available layer: intersected subdivisions, then sections, then townships.
It does not mix a neighboring state's coarse fallback into a finer result.
Exact-ID routing includes exceptions whose prefix differs from their state;
duplicate published IDs and distinct source records are preserved.

The multi-state workload exposed a California query with 65 candidates and
5,957 candidate vertices, although only one polygon contains the point.
Repeatedly decoding and validating those boundaries dominated its query cost.
The reader now caches immutable prepared polygons with conservative ring and
edge bounds, then applies the same final boundary and even-odd containment
tests. This works for nearby new coordinates as well as repeated points; it is
not a coordinate-result cache. Each state has a 4 MiB budget for accounted
prepared storage and an entry allowance. Map/allocator overhead is additional,
so this is not an exact process-RAM cap. Large uncached polygons remain usable.

The public `offline` package supplies `Open`, `Lookup`, `FindID`,
`Representative`, `Full`, `Sources`, `Unlocated`, `Verify` and `Close`. Keep a
reader open for repeated queries. `OpenWithOptions` supports buffered I/O or
an explicit cap on cached state readers. A low cap trades memory for repeated
initialization when queries jump across states. Returned values remain owned
Go values after the reader closes; the mapped file must remain immutable while
open. See the [library example](../README.md#go-libraries).

## Verification and limits

State builds compare a framed hash of every decoded geometry coordinate bit
against the source and compare all four identifiers on every record. Index
coverage, layer counts and component hashes are verified before publication.
The region assembler verifies each state, copies its bytes unchanged, and
publishes atomically only after completion. Cancellation leaves no final file.

The completed region matched its source packs on **14,984 point queries** and
**3,521 reverse-ID queries**, comparing source-record identities as well as
published IDs. Spatial samples include random spread points, original vertices,
edge midpoints and nearby points on both sides. Reverse samples explicitly
include surveys and records without geometry. The same checks passed before
and after prepared-polygon caching. All 33 fresh CLI runs over 11 city locations
returned the expected state; a reverse representative and UTM roundtrip also
preserved their source rows.

The region tests cover shared boundaries, holes, duplicate IDs, foreign-prefix
IDs, global fallback, absent geometry, corrupt routing and truncated files,
bounded concurrent caches, mapped/buffered parity and close during queries.
The full-data comparison tool is `internal/westbench`; its measurements are
persistent-open lookups after warming file pages, not cold-disk guarantees.
One-shot process launch has a separate cost. Mapped bytes are not all resident
RAM, and Go heap figures do not include resident mapped pages.

The compact conversion file retains exact geometry, published identifiers and
the full audit of records without geometry. Other attributes of located records
remain in the source SQLite packs. This is still an experimental file contract.
Structured township/range/section/aliquot input remains in `plssconvert` with
SQLite or live BLM data; the compact reverse reader currently accepts published
IDs. No missing boundaries or idealized subdivisions are synthesized.

Data comes from the public [BLM national service](https://gis.blm.gov/natlplss/rest/services/Cadastral/BLM_Natl_PLSS_CadNSDI/MapServer).
Each state captures layers 1, 2 and 3 using its `PLSSID` prefix and preserves
the returned WGS84 geometry. State timestamps differ, and the mutable service
does not provide a transactional snapshot across all states. BLM's GIS coverage
and accuracy limitations still apply; see [source provenance](data-sources.md).
