# Full Nevada spatial-index experiment

This is an isolated candidate-index experiment. It reads the complete
`data/nevada.sqlite` spatial table without changing it. The reusable experimental
reader/builder is in `internal/spatialindex`; production PLSS providers are unchanged.

```sh
go test ./internal/spatialindex ./internal/indexlab
go run ./internal/indexlab > internal/indexlab/nevada-results.json
```

Generated indexes are written to ignored `internal/indexlab/out/`. They contain
feature row IDs and layers, not cadastral geometry, descriptions, or attributes.
Their size must not be presented as the size of a complete offline PLSS pack.

## Complete-data measurements

The checked-in report uses all **1,510,482** Nevada spatial-index rows. Original
SQLite RTree coordinates are already outward-rounded float32 values; the full
packed-tree and grid variants retain those values exactly. Candidate sets were
compared against SQLite for **10,000 deterministic spread points** and **1,000
sampled box corners/edge points**, across all three layers. The SQL oracle
returned 26,006 feature/layer candidates over that workload.

| Index | Actual file MB | Median µs | p95 µs | p99 µs | Candidate count / original |
| --- | ---: | ---: | ---: | ---: | ---: |
| SQLite RTree query | Part of the source database | 153.9 | 229.8 | 296.2 | 1.000000 |
| Packed tree, fanout 8 | 36.90 | 1.6 | 2.5 | 3.1 | 1.000000 |
| Packed tree, fanout 16 | 34.14 | 1.8 | 3.0 | 3.8 | 1.000000 |
| Packed tree, fanout 32 | 32.89 | 2.2 | 3.8 | 5.6 | 1.000000 |
| Packed tree, fanout 16, 8-bit relative leaf boxes | **16.01** | **2.7** | **4.8** | **6.5** | **1.030185** |
| Packed tree, fanout 16, 16-bit relative leaf boxes | 22.05 | 3.6 | 6.2 | 8.1 | 1.000077 |
| Uniform grid, 0.01° cells | 49.63 | 0.8 | 1.5 | 2.3 | 1.000000 |
| Uniform grid, 0.02° cells | 42.36 | 1.1 | 2.2 | 3.2 | 1.000000 |
| Uniform grid, 0.05° cells | 39.28 | 2.4 | 5.4 | 7.9 | 1.000000 |

MB means 1,000,000 bytes. These are actual serialized index files, not sample
size projections. Each custom index was checked for candidate correctness before
timing. The 8-bit variant missed **zero** original candidates and returned about
3.02% additional candidates overall. Those must be rejected by exact geometry
tests in an integrated converter; this experiment does not time that work.

The 8-bit index loads into about 16.01 MB of index arrays. Opening and validating
its just-written file took 23 ms in this run, with OS pages already warm. Full
fanout-16 index loading took 52 ms. Grid startup was not measured. Resident-array
figures exclude benchmark fixtures and temporary builder memory.

## Packed tree and relative bounds

Records are ordered by the Morton code of their box centers and grouped into
fixed-fanout leaves. Bottom-up parent nodes contain exact float32 union bounds.
Sorting quantizes centers only to select an ordering; it does not affect queries.

The ordinary format stores 20 bytes per bbox/feature ID plus one byte per layer.
Every packed node takes 24 bytes. The relative-box variants keep parent bounds
and store each leaf box as four unsigned 8- or 16-bit values, plus its uint32
feature ID. Minima are rounded down, maxima up. The original geometry is never
quantized or simplified. Query scaling is monotone within a parent rectangle,
and conservative rounding preserves original-box containment while allowing
extra candidates. Parent rectangles themselves still clip the search.

The library supports holes, arbitrary overlap and duplicate cadastral records by
returning all feature-row candidates for subsequent geometry checks. Multiple
bbox entries for the same feature row/layer are deduplicated; distinct source
rows remain distinct. Antimeridian-split entries and both ±180 representations
are covered by unit tests. Layer-specific search supports the existing finest-
available-layer fallback.

```go
idx, err := spatialindex.Build(entries, 16)
// Handle err.
idx, err = idx.Quantize(8)
// Handle err, then idx.WriteTo(writer).

// Reads a section inside a larger single-file pack and loads compact arrays.
loaded, err := spatialindex.Open(fileReaderAt, sectionOffset, sectionLength)
// Handle err.
candidates := loaded.SearchLayer(longitude, latitude, 3)
```

## Timing limits

The report was produced on Windows amd64, Go 1.27.3, Intel Core i7-1195G7.
Custom indexes are memory resident; the source SQLite index was read before its
query pass, warming storage caches. Custom latency distributions cover three
passes; SQLite's reference distribution covers one. Runs on this shared machine
show normal scheduling/frequency variation, so small differences between tree
fanouts should not be treated as statistically conclusive.

Per-query timing uses Windows
[QueryPerformanceCounter](https://learn.microsoft.com/en-us/windows/win32/api/profileapi/nf-profileapi-queryperformancecounter)
because ordinary `time.Now` calls on this host resolved too coarsely for
microsecond percentiles. Other platforms use Go monotonic elapsed time. Timestamp
overhead is included. Index allocations are measured in a separate untimed pass;
SQLite allocation measurements are omitted. Timings exclude geometry decoding,
attribute loading, point-in-polygon tests, process startup, and rendering.

The grid is faster for this workload but stores duplicated feature references
across cells and is larger. The 8-bit packed tree is the recommended small-index
candidate for an integrated, single-file experiment. Complete-pack lookup and
reverse-ID tests are still required before adopting it in production.
