# Nevada attribute-column experiment

This experiment separates cold attributes from hot geometry. It streams all
1,510,482 located features from `data/nevada.sqlite`, preserves their original
SQLite feature IDs, and retains the unlocated source record in the archive's
audit metadata. `OBJECTID` remains an attribute and is preserved independently
of the SQLite feature ID.

```powershell
go run ./internal/columnlab --db data/nevada.sqlite --out data/columnlab
go run ./internal/columnlab --db data/nevada.sqlite --out data/columnlab256 --block-sizes 256 --modes dict-full,dict-essential
go run ./internal/columnlab --bench data/columnlab256/dict-full-256.cols
```

Existing output files are refused. Each generated block is decoded and compared
against every source attribute before writing. Full variants preserve strings,
JSON number spellings, array order, nested values, and the distinction between
missing keys and null values. No geometry is included or modified.

The essential variants intentionally omit attributes outside the conversion,
identifier, filtering, and display fields listed in `columns/codec.go`. They are
smaller conversion-only experiments, not lossless replacements for the full
attribute archive. Full attributes for unlocated records remain available in
both variants.

## Complete measured outputs

Measured on the entire Nevada source, not extrapolated from samples:

| Encoding | Records per block | Complete bytes |
|---|---:|---:|
| Plain columns, full attributes | 1,024 | 75,257,542 |
| Plain columns, full attributes | 4,096 | 70,352,541 |
| Dictionary/prefix, full attributes | 256 | 72,147,017 |
| Dictionary/prefix, full attributes | 1,024 | 67,231,136 |
| Dictionary/prefix, full attributes | 4,096 | 63,590,479 |
| Dictionary/prefix, essential attributes | 256 | 24,834,606 |
| Dictionary/prefix, essential attributes | 1,024 | 22,525,672 |
| Dictionary/prefix, essential attributes | 4,096 | 21,981,691 |

Building and verifying the six 1,024/4,096 variants together took 115.6 seconds.
Sampled peak Go heap was 43.3 MB; Go runtime system allocations peaked at 69.1 MB.
The two 256-record variants took 64.6 seconds with sampled peak heap 5.24 MB and
runtime system allocations 22.0 MB. These are Go allocation measurements, not
whole-process working-set measurements.

Current reader measurements, with a warm operating-system file cache:

| Full dictionary archive | Open, mean | Random record, decoded-block cache disabled | Cached-block record |
|---|---:|---:|---:|
| 256 records/block | 125 µs | 248 µs | 5.35 µs |
| 1,024 records/block | 59 µs | 873 µs | 5.52 µs |
| 4,096 records/block | 48 µs | 2,901 µs | 6.32 µs |

Cached full-record decoding allocates about 3.42 KB across 68 allocations.
Opens are averaged over 100 calls; uncached-block reads over 100 scattered IDs;
cached reads over 5,000 calls. Batched averages avoid misleading zero-duration
samples from coarse Windows clock ticks. These measurements are not disk-cold
benchmarks. A 256-record block favors random attribute access; 4,096 favors size.

## Embedding and format

`internal/columns` exposes:

```go
reader, err := columns.OpenReader(section, sectionSize) // io.ReaderAt, int64
attributes, err := reader.Attributes(featureID)        // uint32 -> map[string]any
metadata := reader.Metadata()
unlocated := reader.Unlocated()
err = reader.SetCacheBlocks(4) // bounded LRU; zero disables it
```

The reader does not own or close the source. An `io.SectionReader` can embed the
archive in a larger portable file without changing any offsets. Returned number
values use `json.Number`. Geometry and lookup indexes should remain independent
so ordinary coordinate lookup does not decompress cold attribute blocks.

`PLSCOL01` starts with a 32-byte header containing flags, block size, footer
offset, and record count. Each block has a 16-byte framing header followed by a
zlib stream. Its decoded payload contains feature IDs and layers, a sorted column
schema, and either raw JSON values or per-column dictionaries plus row codes.
Dictionary entries are sorted and prefix-compressed, which factors repeated
PLSSID/FRSTDIVID strings without assuming all cadastral identifiers follow one
grammar. Code zero means missing; JSON null remains a separate dictionary value.
The footer stores metadata, unlocated records, and a directory of ID ranges and
block offsets, ending in `COLEND01`. This is an experimental format.
