# Full Nevada geometry experiment

This experiment streams every spatial record in the existing Nevada SQLite
pack. It does not modify that pack or the production PLSS provider. Generated
components are geometry-only research artifacts; attributes, IDs, spatial
indexes, and the source record without geometry belong in separate components.

```sh
go test ./internal/compactgeom
go run ./internal/topolab --db data/nevada.sqlite --out data/topolab > internal/topolab/nevada-results.json
go run ./internal/topolab/vertexprobe --input data/topolab/lossless.cgeom --out data/topolab > internal/topolab/vertex-results.json
```

Output files must not already exist. Use a different output directory for a
repeat run. Every emitted geometry is verified against a SHA-256 digest of its
source-derived canonical coordinate bits, dimensions, ring counts and ordering.
The quantized experiments are checked against their explicitly rounded source
coordinates. No source coordinates are changed.

## Complete-corpus counts

The measured source is the Nevada snapshot documented in `docs/offline-nevada.md`.
These are full-corpus results, not sample estimates.

| Measure | Township | First division | Intersected | Global |
| --- | ---: | ---: | ---: | ---: |
| Features | 3,380 | 112,133 | 1,394,969 | 1,510,482 |
| Vertex occurrences | 367,730 | 1,701,815 | 7,405,634 | 9,475,179 |
| Unique bit-exact XY pairs | 190,435 | 695,961 | 1,575,789 | 1,580,556 |
| Unique undirected XY edges | 194,108 | 808,960 | 2,949,331 | 2,960,042 |
| Unique ordered bit-exact polygons | 3,378 | 112,121 | 1,364,267 | 1,471,479 |

All 1,512,994 rings had bit-identical closing vertices. Every vertex had exactly
two ordinates. The codec also supports additional ordinates, varying dimensions,
signed zero, and unclosed rings. Polygon identity includes original ring/vertex
order; rotated or reversed representations are not counted as identical.
The 39,003 duplicate polygon occurrences remain distinct feature records.

## Actual component files

| Geometry component | Actual bytes | Vertex table | Compressed record blocks | Block directory |
| --- | ---: | ---: | ---: | ---: |
| Exact float64 | 41,321,472 | 25,288,896 | 15,843,632 | 188,816 |
| Rounded to 1e-7° | 28,493,366 | 12,486,568 | 15,817,854 | 188,816 |
| Rounded to 1e-6° | 28,299,555 | 12,331,032 | 15,779,579 | 188,816 |

Each file also includes a 128-byte header. The exact file preserves every
coordinate bit, every ring and vertex, and every feature. Shared XY pairs live
once in a direct-access dictionary. Rings store signed deltas between dictionary
IDs; a closing vertex is omitted only when it can be reconstructed exactly.
Groups of 128 records are compressed with zlib. There is no coordinate rounding
in the exact file.

Building all three candidates plus the full redundancy audit took 44.85 seconds
on Windows amd64, Go 1.27.3, Intel Core i7-1195G7. The combined audit/build
reported approximately 1.37 GB of Go heap in use at its largest progress sample;
that includes audit dictionaries, three builders, and verification hashes.
Reading does not require these build structures.

The exact component SHA-256 is
`faf35d55b7dd7e98f0a4ab4fa37f0bf5190e4784c8154adaf01f9bb78b66b870`.
All 1,510,482 exact records passed source-hash verification; sequential
verification took 4.51 seconds after the build.

## Quantization changes topology

The rounded candidates are explicitly lossy experiments, not the default or a
recommended replacement for the exact component. A 1e-7° grid is approximately
1.1 cm in latitude, with a maximum latitude rounding displacement of about
0.56 cm; 1e-6° is approximately 11 cm with about 5.6 cm maximum displacement.
The exact maximum errors in degrees are in the JSON report.

| Grid | Nonzero XY edges collapsed | Newly zero-area rings | Ring orientation changes |
| --- | ---: | ---: | ---: |
| 1e-7° | 9,286 | 211 | 67 |
| 1e-6° | 48,680 | 873 | 98 |

Thus small coordinate displacement does not guarantee unchanged boundary
classification or polygon topology. Any future rounded provider would need an
explicit accuracy choice and treatment of boundary ambiguity/degenerate rings.
The exact file already fits in 41.3 decimal MB, so the observed 12.8 MB saving
does not require accepting these changes in the current design.

## Reader API and measured costs

`internal/compactgeom` supplies an importable reader for standalone files or
sections of a single container:

```go
section := io.NewSectionReader(container, componentOffset, componentLength)
reader, err := compactgeom.OpenReaderAt(section, componentLength)
// Handle err. Feature IDs are the original dense SQLite IDs, beginning at 1.
geometry, err := reader.Record(featureID)
```

The caller owns the backing ReaderAt/file. Returned rings can be independently
modified. Record supports concurrent calls. `Metadata().Decimals == 0` identifies
the exact component; values 6 and 7 identify the rounded experiments.

Default caches hold 64 vertex pages of 4,096 bytes and 16 decoded geometry
blocks. Only the 188,816-byte block directory must be read at opening.
`OpenReaderAtWithOptions` accepts `ReaderOptions{VertexCachePages,
GeometryCacheBlocks, PreloadVertices}`. Zero capacities disable caches;
preloading the exact vertex table uses 25,288,896 additional payload bytes.

The following Go benchmarks used the exact component, a warm OS cache and
two-second runs. Random cases cycle through 10,000 fixed-seed locations; hot
cases cycle through 16 locations that fit the caches. These are geometry-record
costs, excluding spatial search, point-in-polygon testing, metadata and startup.

| Configuration | ns/record | Allocated bytes/record | Allocations/record |
| --- | ---: | ---: | ---: |
| Bounded caches, random | 66,279 | 14,842 | 39 |
| Preloaded vertices, random | 38,933 | 4,895 | 32 |
| Bounded caches, hot 16 | 1,282 | 591 | 3 |
| Preloaded vertices, hot 16 | 855 | 591 | 3 |

The audit's separate 10,000-random-record pass measured 48.5 µs for bounded
caches. Timing variability and concurrent machine activity matter; these are
observations, not latency guarantees. Reproduce with PowerShell:

```powershell
$env:TOPO_GEOM_FILE = (Resolve-Path data/topolab/lossless.cgeom).Path
go test ./internal/compactgeom -run '^$' -bench BenchmarkNevadaGeometry -benchtime=2s
Remove-Item Env:TOPO_GEOM_FILE
```

## Further lossless vertex compression

A separate probe compresses the complete exact vertex table in independently
readable blocks. All decoded blocks were byte-compared with the original table.
It does not change the existing geometry component format.

| Vertex-table encoding | Actual bytes including directory | Random vertex read | Cache payload limit |
| --- | ---: | ---: | ---: |
| Existing direct table, 256-vertex pages | 25,288,896 | 8.36 µs | 256 KiB |
| Byte shuffle + zlib, 256 vertices/block | 18,954,986 | 36.36 µs | 256 KiB |
| XOR + byte shuffle + zlib, 256 vertices/block | 18,137,199 | 39.27 µs | 256 KiB |
| XOR + byte shuffle + zlib, 1,024 vertices/block | 17,620,528 | 150.11 µs | 1 MiB |

The probe uses a single-threaded 64-block LRU and median timing across three
fixed-seed random passes. These are vertex-access microbenchmarks, not complete
geometry reads; a geometry can require multiple dictionary blocks. Hot cache
hits are similar across encodings. Its cache lacks the production experimental
reader's concurrency locks, so its nanosecond hit timings should not be treated
as a complete reader benchmark.

The 256-vertex XOR variant saves 7.15 MB, potentially taking geometry storage
from 41.32 MB to roughly 34.17 MB, but increases dictionary cache-miss CPU cost.
Preloading and decompressing once could retain fast steady-state queries at
the existing 25.3 MB vertex RAM cost; opening latency and the integrated reader
would need separate measurement before adopting that format change.
