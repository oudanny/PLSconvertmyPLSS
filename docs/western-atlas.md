# One-file western-US PLSS atlas

The compact reader supports Arizona, California, Colorado, Idaho, Montana,
New Mexico, Nevada, Oregon, Utah, Washington and Wyoming in one file. This is
the contiguous 11-state West; Alaska and Hawaii are not part of this build.
Coverage means the records published by BLM under those state prefixes, not
an assertion that every geographic location has a surveyed PLSS polygon.

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
