# Offline size and lookup performance

The deployment target is a desktop or laptop with one executable and one local
data file. The longer-term coverage target is the western United States. Nevada
is the measured pilot; western-US size and latency have not yet been measured.

## What the current Nevada file contains

The October 27, 2026 pack is 1,341,652,992 bytes (1.25 GiB), with 1,510,482 usable
polygons and one source record lacking geometry. The file retains every source
attribute and coordinate, including overlapping layers and distinct records with
the same cadastral identifier.

A SQLite `dbstat` scan separates the actual disk usage:

| Component | Bytes | Share of file |
| --- | ---: | ---: |
| Feature table, including compressed payloads | 1,152,958,464 | 85.9% |
| RTree spatial index and its auxiliary tables | 88,637,440 | 6.6% |
| Description and identifier indexes | 99,893,248 | 7.4% |
| Metadata, audit records and remaining pages | 163,840 | <0.1% |

The compressed feature blobs occupy 926,503,638 bytes *within* the feature table;
do not add that figure to the table above. Indexed SQL string columns also repeat
121,794,471 bytes of attribute values before SQLite record/page overhead.

In a deterministic 500-record sample of the finest layer, raw attributes averaged
937 bytes and geometry 223 bytes. Attribute key names alone averaged 546 bytes.
The median polygon had five vertices; the mean was 5.258. These are sample
measurements, not statewide guarantees. The biggest opportunity is eliminating
repeated metadata and serialization overhead before changing boundary precision.

## First shipped optimization

The original reader allocated a new zlib/DEFLATE decoder for each candidate.
An allocation profile of the real-pack lookup benchmark attributed about 69% of
allocated bytes to creating those decoders. The reader now pools independent
decompressors and bounded scratch buffers. It does not cache returned polygons or
attributes; callers still own independent results. Unusually large scratch
buffers are discarded, and the runtime may reclaim idle pool entries.

Three unprofiled runs on Windows amd64, Go 1.27.3, Intel i7-1195G7, with one open
pack and ten warmed locations, measured:

| Measurement | Original reader | Pooled reader |
| --- | ---: | ---: |
| Allocated bytes per lookup | about 82,000 | about 21,100 |
| Allocations per lookup | 289 | 273 |
| Median of benchmark-run mean latency | 219 microseconds | 208 microseconds |

This removes about 74% of allocated bytes in that workload. Latency differences
are small relative to machine/load variation; these results are not a throughput
guarantee. Allocated bytes per operation are not retained process RAM. This
change preserves format version 1 and reads the existing pack without rebuilding
or downloading it.

Additional exploratory runs measured about 0.313 ms per lookup across 512
deterministic points in Nevada's bounding rectangle (70.3% found coverage), and
1.879 ms for opening, looking up and closing a pack with warmed OS pages. Twenty
fresh CLI invocations had a median of about 15.6 ms. These are different workloads;
none is a cold-disk measurement or a p95/p99 guarantee.

The Windows executable built with `CGO_ENABLED=0`, `-trimpath` and
`-ldflags '-s -w'` is 11,642,880 bytes (11.1 MiB). It runs without an installed Go
or SQLite runtime. The database dominates the portable bundle's size.

## Measured compact-payload experiments

The isolated [packlab prototype](../internal/packlab/README.md) sampled 1,024 real
records from each of the three layers, in eight stratified contiguous groups of
128. Each layer's mean encoded size was weighted by its full record count. Every
sample was decoded again and checked against the original attributes and every
float64 coordinate bit. No coordinates were rounded or simplified.

| Representation | Estimated payload bytes, all layers | Finest-layer random-record decode |
| --- | ---: | ---: |
| Existing zlib JSON | 920 MB | 36 microseconds |
| Binary geometry, original attribute objects | 893 MB | See raw results |
| Shared field-name schema + binary geometry, per-record compression | 499 MB | 17 microseconds |
| Same, compressed in blocks of 32 records | 196 MB | 94 microseconds |
| Same, compressed in blocks of 128 records | 171 MB | 215 microseconds |

MB means decimal megabytes. These are **payload estimates, not completed pack
sizes**. The actual current payload is 926.5 MB, so the sampled baseline is about
0.7% smaller. The current file also has approximately 415 MB outside its payload
blobs. Any new format needs its own index/offset/page accounting and a complete
Nevada build before claiming a final file size. Rare large geometries and source
ordering can change the outcome.

The decode column measures only the codec, with compressed blobs already in
memory, a reused inflater and the median of three timed runs. It does not measure
SQL, disk I/O, containment or complete lookup latency. Each block lookup inflates
the whole block and parses one requested record. There is no decompressed-block
cache, so these model cache misses. The 32-record option saves about 79% of the
sampled payload but trades extra decode time for size. The 128-record option saves
only about another 25 MB while more than doubling that miss cost; it is not the
preferred starting point for fast random lookups.

The strongest candidates are therefore the schema-based per-record encoding for
speed, and small compressed blocks with a bounded cache for size. A production
comparison still needs full-pack rebuilds, corruption checks, boundary parity,
cache limits and broad lookup benchmarks. The shipped CLI continues to use v1;
experimental encodings are not supported pack files.

The [raw measurement report](../internal/packlab/nevada-results.json) records each
variant's layer sizes, decode allocations and timings. XOR-varint encoding was
also tested: it helped larger township rings but increased size for the dominant
subdivision layer compared with plain binary float64 coordinates. It is not an
automatic improvement.

## Direction for a smaller format

Keep the following requirements while comparing alternatives:

- Retain original float64 coordinates and all source attributes, including the
  difference between missing and null values and exact integer identifiers.
- Keep separate source records even when their cadastral IDs or polygons match.
- Preserve holes, multipart polygons, shared-boundary matches and layer fallback.
- Index both spatial queries and legal descriptions/identifiers.
- Read a bounded part of the file for an individual lookup; do not inflate the
  whole state into RAM.
- Publish a completed file and use a new file for dataset updates.

The first compact-format candidate stores field names once per schema, compact
attribute values per record, and binary coordinates. Geometry and attributes can
be separated so candidate containment checks need not decode all metadata.
Small independently compressed blocks may remove repetition across neighboring
records, but must be measured against their extra random-read decode cost.

A static packed spatial index is another candidate after payload size is under
control. [FlatGeobuf](https://flatgeobuf.org/) provides a packed Hilbert RTree and
lossless binary features; adopting it would still require our description/ID
indexes and missing-geometry audit data. Replacing SQLite alone does not establish
a size or speed improvement.

Exact shared-edge storage could reduce duplicate boundary data. The
[TopoJSON specification](https://github.com/topojson/topojson-specification)
describes shared arcs. Quantizing or snapping nearly equal edges is a separate,
lossy choice and is not part of the current plan. A regular raster lookup table
also requires a cell size and cannot preserve arbitrary original boundaries by
itself.

## Reproduce the current lookup benchmark

With `PLSS_NEVADA_PACK` set to the absolute path of the downloaded Nevada file:

```sh
go test ./plss -run '^$' -bench '^BenchmarkNevadaLookup$' -benchmem -benchtime=3s -count=3
go test ./plss -run '^$' -bench '^BenchmarkNevada(LookupLocations|LookupSpread|OpenLookupWarmOSCache)$' -benchmem
```

This benchmark keeps the database open and rotates through ten locations. It
includes the SQL spatial query, decompression, JSON decoding, polygon containment
and complete result construction. It excludes downloading, opening the file,
process startup and formatting output. It is a warm-cache workload, not a
cold-storage measurement. The additional benchmarks expose per-location,
geographically spread and open-plus-lookup costs separately. Future format
decisions also need tail latency and memory measurements on the intended hardware.
