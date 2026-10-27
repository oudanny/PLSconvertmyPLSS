# Integrated compact-atlas experiment

This command builds the factored cadastral-ID index, assembles complete compact
files and compares real lookups with the original Nevada SQLite pack. User-facing
conversion is in `cmd/plssatlas`; this command is the reproducible research tool.

The preferred completed artifacts are `data/atlaslab/nevada-compact.atlas`
(65,199,261 bytes) and `data/atlaslab/nevada-complete.atlas` (128,789,740 bytes).
The latter adds all original attributes. Both have every exact source polygon,
published ID and the unlocated-source audit. The intermediate files and original
SQLite pack are not required by the executable when using an assembled atlas.

## Build from the existing Nevada source

Run from the repository root. Component builders and atlas outputs refuse existing
destinations; choose new output paths for another build. Topolab also emits two
explicitly lossy comparison files; neither is accepted by the atlas reader.

```sh
go run ./internal/topolab --db data/nevada.sqlite --out data/topolab
go run ./internal/columnlab --db data/nevada.sqlite --out data/columnlab
go run ./internal/indexlab --db data/nevada.sqlite --out internal/indexlab/out
go run ./internal/atlaslab keys --db data/nevada.sqlite --keys data/atlaslab/nevada-v2.keys
go run ./internal/atlaslab assemble --keys data/atlaslab/nevada-v2.keys --out data/atlaslab/nevada-compact.atlas
go run ./internal/atlaslab assemble --keys data/atlaslab/nevada-v2.keys --out data/atlaslab/nevada-complete.atlas --attributes data/columnlab/dict-full-4096.cols
```

Assembly includes the source pack's SHA-256, metadata and source audit; each
component receives its own SHA-256. It checks all hashes and spatial/key coverage
after reopening. Geometry and attribute builders separately verify every source
record. The caller must supply components from the same source build; hashes
identify components but are not signatures authenticating an external publisher.

## Integrated verification and measurement

```sh
go run ./internal/atlaslab bench --mmap --file data/atlaslab/nevada-compact.atlas --points 10000
go run ./internal/atlaslab bench --file data/atlaslab/nevada-compact.atlas --points 10000
go run ./internal/atlaslab lookup --mmap --file data/atlaslab/nevada-compact.atlas --value "39.5296,-119.8138"
go run ./internal/atlaslab id --mmap --file data/atlaslab/nevada-compact.atlas --id NV210190N0190E0SN110ASWNE --layer division
```

`bench` performs integrity checks, compares 10,000 seeded spread points and 1,000
original vertices/edge midpoints with SQLite, checks returned geometries, and
checks exact-ID queries. It then times persistent-open hot and spread queries,
reverse IDs, and reverse IDs plus representative-point calculation. Full files
also measure a separate workload that retrieves all original attributes.

Times use Windows QueryPerformanceCounter or Go monotonic time elsewhere. They
include timing overhead and real machine scheduling. The spread workload includes
points outside source coverage; misses and layer fallback are part of the
measurement. These are OS-cache-warm runs. They do not include CLI startup, source
download, cold physical storage, or final output formatting. Opening is recorded
separately with unspecified initial OS-cache state.

## Reports

- `nevada-compact-results.json`: final mapped reader with key-index v2; preferred
  report for the completed compact file.
- `nevada-core-results.json`: earlier ordinary-file reader and key-index v1.
- `nevada-full-results.json`: earlier complete file including cold attributes.
- `nevada-mapped-results.json`: intermediate mapped reader before the v2 key
  directory removed the linear startup scan.
- `nevada-cli-results.json`: twenty alternating fresh-process invocations on the
  final mapped compact file and stable SQLite CLI; includes startup/output.

Different trials ran at different times and host loads. Earlier reports remain
as evidence of variation, not as simultaneous controlled measurements. The
latest complete-file analysis and tradeoffs are in
[`docs/compact-atlas.md`](../../docs/compact-atlas.md).

## Lifetime and format limits

`atlas.OpenMapped` borrows read-only OS pages; `atlas.Open` uses ordinary file
reads. Both expose the same lookup/ID/full-record methods. Mapped files must not
be edited or truncated while open. Reader methods coordinate with `Close`; direct
component references must not outlive their reader. Returned result values are
Go-owned and can outlive it. Explicit `Verify` performs full hashes and index
audits; opening alone is lighter and not a full corruption audit.

The atlas container (`PLSSATL1`) embeds independently versioned components;
the final build uses `PLSSKEY2` for its ID directory. The format is experimental
and has no public stability commitment yet. It does not implement structured
PLSS descriptions or replace the stable provider/download interfaces.
