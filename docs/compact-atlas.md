# Compact Nevada atlas: complete files and measured queries

The redesign builds a **65,199,261-byte (62.18 MiB) Nevada file** for exact
point-to-PLSS and published-ID-to-coordinate lookup. It retains all 1,510,482
original polygon records, their full-precision coordinates and cadastral IDs,
including duplicate IDs and overlapping layers. Its size is **95.14% below** the
original 1,341,652,992-byte SQLite pack.

An alternative **128,789,740-byte (122.82 MiB)** file also retains every original
BLM attribute. These are actual completed files, not estimates from a sample.
Both retain the one source record lacking geometry as an audit record; looking
up its identifier returns `ErrNoGeometry`.

This is a research format and a separate executable. The stable `plssconvert`
CLI and `plss.Provider` API remain unchanged. `plssatlas` supports latitude/longitude,
UTM and exact published IDs; structured township/range/section/aliquot input is
not yet implemented in this new reader. Nevada is the only complete dataset
tested. No western-US file size, throughput, or coverage guarantee is implied.

## What changed

| Part of the compact file | Actual bytes | Representation |
| --- | ---: | --- |
| Geometry | 41,321,472 | Shared exact vertices; compressed polygon references |
| Spatial index | 16,011,274 | Static tree with conservative relative leaf bounds |
| Cadastral identifiers and reverse index | 7,800,979 | Factored prefixes, bit-packed codes, township postings |
| Manifest and source audit | 65,536 | Source date, counts, checksums, unlocated records |
| **Complete conversion file** | **65,199,261** | One portable file |
| Optional full attribute columns | 63,590,479 | Prefix dictionaries and compressed columns |
| **Complete file with all attributes** | **128,789,740** | One portable file |

**Shared vertices.** The source has 9,475,179 vertex occurrences but only
1,580,556 distinct XY bit pairs. A common corner is stored once. Each polygon
retains its ordered rings and references those corners through small delta-coded
integers. Closing vertices are reconstructed only when bit-identical. The format
also preserves extra ordinates when present, although this Nevada file has none.
This is exact deduplication: there is no snapping, rounding or simplification.

**Compact spatial tree.** The static tree orders nearby boxes together. A leaf
stores four small bounds relative to its parent's box instead of four full
coordinates. Minimums round outward down; maximums round outward up. This widens
candidate selection, never the original geometry. The full polygon decides the
answer. The 8-bit variant introduced about 3.02% extra bounding-box candidates in
the measured workload and missed none of the original candidates.

**Identifiers as structure.** Township prefixes, section suffixes and subdivision
suffixes are stored once. A record contains bit-packed dictionary codes, with
explicit exceptions for non-hierarchical identifiers and special surveys.
Reverse lookup uses delta-coded township postings instead of a second complete
copy of every identifier. A posting directory avoids scanning all records at
startup. Separate source records are never merged merely because their IDs match.

**Metadata outside the lookup path.** Conversion needs boundaries and identities;
source dates, acreage text and other descriptive attributes can remain in an
optional compressed component. Full archives preserve exact JSON numbers,
missing versus null values, strings, arrays and nested values. The core file
does not claim to preserve all attributes of located records. The full file does.

**Demand-loaded pages.** The mapped reader borrows read-only file pages and copies
only the small upper tree and parsed dictionaries. Decompressed polygon blocks
have a bounded cache. Mapping virtual address space does not mean the whole file
is resident in RAM; the OS manages file-backed pages. See Microsoft's
[file-mapping description](https://learn.microsoft.com/en-us/windows/win32/memory/file-mapping).
An ordinary-file-reader mode is also available.

## End-to-end measurements

Final mapped-core run: Windows amd64, Go 1.27.3, Intel Core i7-1195G7. The database
stays open; OS pages have been warmed by verification. Timings include candidate
search, exact polygon containment and identifier reconstruction.

| Workload | SQLite median | Mapped atlas median | Atlas p95 |
| --- | ---: | ---: | ---: |
| Repeated ten-city lookup | 179.1 µs | 2.4 µs | 7.0 µs |
| 10,000 points across Nevada's bounding rectangle | 278.9 µs | 22.0 µs | 40.1 µs |
| Exact ID → representative coordinate | Not measured in this run | 14.1 µs | 54.5 µs |

The important workload distinction: the stable SQLite reader constructs complete
attribute maps and geometries on every lookup; the lightweight atlas path returns
IDs/layers and does not construct archival metadata. This is deliberately less
work for the conversion use case, not a claim that arbitrary full-record queries
became equally fast. Exact containment still uses the original polygon in both.

The latest opening measured 6.51 ms and about 3.19 MB of additional Go heap.
Mapped file pages and OS cache residency are **not** counted as Go heap. Explicit
copied index nodes occupy 2,416,872 bytes; key metadata/owned-array estimates are
about 360 KB. The mapping borrows approximately 7.8 MB of key-file bytes. This is
not a measured process peak-RSS claim.

Twenty alternating fresh CLI invocations measured **21.9 ms median for the atlas
and 17.7 ms for SQLite**, including process launch and output. The compact reader
currently pays more initialization work for one isolated query; its large speed
gain appears when a library, batch process or future GUI reuses an open reader.
The stripped Windows executable is **8,963,584 bytes**, so the executable plus
compact Nevada file total **74,162,845 bytes (70.73 MiB)**.

Earlier integrated trials measured 2.5–4.1 µs hot medians and 35–57 µs spread
medians; machine load and evolving reader implementations matter. The checked-in
reports preserve those trials rather than treating the fastest number as a
guarantee. Cold storage, removable media, other hardware, and complete western-US
datasets have not been benchmarked. CLI launch and formatting are excluded from
lookup timings. Windows timings use
[QueryPerformanceCounter](https://learn.microsoft.com/en-us/windows/win32/sysinfo/acquiring-high-resolution-time-stamps).

Full archival attributes have a different cost. The 4,096-record-column variant
measured a 3.44 ms median for scattered lookup plus all attributes in an earlier
full-file trial. A 256-record attribute component uses 72.15 MB instead of
63.59 MB and measured roughly 248 µs per uncached attribute-block read in the
isolated codec experiment. These are distinct workloads; keeping metadata cold
is essential to the fast conversion path.

## Other approaches actually tested

| Approach | Observation | Current choice |
| --- | --- | --- |
| Uniform spatial grid | 0.01° grid gives very fast candidates but costs 49.63 MB | Static 16.01 MB index saves about 34 MB |
| Full float32 leaf bounds | 34.14 MB index | Conservative relative bounds roughly halve it |
| 16-bit relative leaf bounds | 22.05 MB; fewer false positives | 8-bit bounds plus exact containment |
| Per-record compact JSON/binary | Good initial improvement, retained considerable repeated text | Column dictionaries and factored IDs |
| Stronger lossless vertex compression | Saves another 7.15–7.67 MB, but increases random dictionary-miss cost | Direct exact vertex table for now |
| Coordinate rounding to 10⁻⁷ degrees | Geometry shrinks to 28.49 MB, but 211 rings collapse and 67 change orientation | Rejected for exact lookup |
| Coordinate rounding to 10⁻⁶ degrees | 873 collapsed rings and 98 orientation changes | Rejected for exact lookup |

Shared edges/topology, block caches, binary encodings and bounded dictionary
storage were investigated using the full Nevada source. A regular-grid formula
alone is not a substitute for irregular published boundaries. A predictor could
help select candidates in a later version, but would still need exact correction
and exceptions. No machine-learning predictor is needed for these measured gains.

## Verification

- Every geometry record was decoded and checked bit-for-bit against the source;
  all 1,510,482 records were included.
- Every full attribute record in each tested encoding was decoded and checked
  against the source, including exact number spelling and null/missing semantics.
- Every factored primary ID was checked; sampled reverse queries retained their
  original feature rows. Duplicate IDs remain separate records.
- The integrated files matched SQLite at 10,000 deterministic spread points plus
  1,000 original vertices/edge midpoints. Returned polygon coordinates were also
  compared. This is 11,000 points, not proof of every possible query.
- Synthetic tests cover holes, boundary overlaps, fallback, dateline splits,
  duplicate/special IDs, unlocated records, corrupt/truncated data, checksums,
  no-overwrite behavior and concurrent close of mapped readers.
- `Verify` checks component hashes and key/spatial coverage. Normal opening is
  intentionally lighter. Completed mapped files must remain immutable while open.

The source SQLite SHA-256 is
`241646c9eebbfcc52d46b33c4c8ad7882cc5a5ab45fed58f8b832ac6246ffff3`.
Its capture time is `2026-10-27T18:41:01Z`; this is not a survey date.

## Run the experimental CLI

Build once; the executable then needs only the atlas file, with no Go runtime,
SQLite installation or network connection:

```sh
go build -trimpath -ldflags '-s -w' -o bin/plssatlas.exe ./cmd/plssatlas
bin/plssatlas.exe latlon --value "39.5296,-119.8138" --data data/atlaslab/nevada-compact.atlas
bin/plssatlas.exe utm --value "11N 258156.249 4379330.926" --data data/atlaslab/nevada-compact.atlas
bin/plssatlas.exe plss --id NV210190N0190E0SN110ASWNE --layer division --data data/atlaslab/nevada-compact.atlas --json
bin/plssatlas.exe latlon --value "39.5296,-119.8138" --data data/atlaslab/nevada-complete.atlas --metadata --json
```

Use `--buffered` to avoid memory mapping. Core files reject `--metadata` clearly;
they do not silently return an incomplete archival record. For a non-Windows
build, omit `.exe` from the binary path. Existing source packs remain available
to the stable CLI for structured descriptions.

## Reproduce the research builds

See [the atlas experiment](../internal/atlaslab/README.md) for the full sequence
and raw integrated measurements. Component details are in
[topology/geometry](../internal/topolab/README.md),
[attribute columns](../internal/columnlab/README.md), and
[spatial indexes](../internal/indexlab/README.md).

The next production steps are structured-description parity in the compact
reader, a single conversion/download command, hardening/versioning the file
contract, and measuring other western states. The present files and CLI make
the size/speed result concrete without changing the stable format prematurely.
