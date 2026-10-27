# Offline pack codec experiment

`packlab` is an isolated, read-only prototype for comparing lossless payload
encodings against the existing Nevada v1 pack. It does not change production
pack APIs, schema, files, or decoding behavior. The experimental encoding is
not a supported interchange format.

Run from the repository root:

```sh
go test ./internal/packlab
go run ./internal/packlab --db data/nevada.sqlite --blocks 8 --rounds 1 > internal/packlab/nevada-results.json
```

The checked-in report used Go 1.27.3 on Windows amd64, an Intel Core
i7-1195G7, and the Nevada pack created at `2026-10-27T18:41:01Z`.
It sampled eight evenly spaced, contiguous 128-record ranges in each layer:
1,024 townships, 1,024 first divisions, and 1,024 intersected polygons.
These are deterministic stratified samples, not a complete repack.
The source file remains 1,341,652,992 bytes; its actual existing compressed
feature blobs total 926,503,638 bytes.

## What the candidates preserve

- Every float64 coordinate bit, every ordinate, original ring and vertex order,
  and duplicate closing vertices. There is no quantization or simplification.
- Every sampled attribute value, including exact JSON integers, nulls, empty
  strings, nested values, and the distinction between a missing key and null.
- Schema-once candidates use sorted keys shared per layer, a per-record
  presence bitmap, and JSON value arrays. Shared schema storage is excluded
  from the estimate; this sample has 29, 14, and 47 keys by layer.
- Binary geometry uses little-endian float64 values or per-axis XOR varints.
  Each decoded sample is checked against every source coordinate bit and the
  complete attribute JSON before timing.

Production schema discovery must account for every source key, including keys
absent from a small sample. The prototype rejects a record if its supplied
schema omits a key.

## Initial measurements

Decimal MB below means 1,000,000 bytes. Estimated blob totals weight each
layer's sample mean by that layer's actual polygon count. They are not measured
output SQLite file sizes. The baseline estimate is 920.0 MB, versus 926.5 MB of
actual blobs, a 0.7% difference; this is not an error bound for other variants.

| Payload encoding | Estimated blobs, MB | Layer 3 decode, µs/record | Layer 3 decode allocated bytes |
| --- | ---: | ---: | ---: |
| Existing zlib JSON | 920.0 | 35.95 | 14,133 |
| Binary64 geometry, original attribute keys, fast zlib | 893.2 | 31.69 | 10,986 |
| Shared attribute schema + binary64, fast zlib | 530.9 | 20.61 | 8,062 |
| Shared attribute schema + binary64, default zlib | 499.2 | 17.23 | 8,062 |
| Shared attribute schema + XOR geometry, default zlib | 523.3 | 17.19 | 8,058 |
| Shared schema + binary64, 32-record compressed blocks | 196.3 | 94.14 | 48,587 |
| Shared schema + binary64, 128-record compressed blocks | 170.7 | 215.08 | 151,062 |

Removing repeated attribute keys provides most of the per-record reduction.
XOR helps larger township geometries but increases the dominant intersected
layer's compressed size relative to raw binary64 in this sample.

Blocks remove more repetition across neighboring records. The reported block
decode cost is a **cache miss**: inflate the entire block, then parse only the
requested record. A bounded cache of inflated blocks could improve repeated
nearby queries; its hit rate and memory requirements have not been measured.
The per-record design has a smaller random-access decoding cost.

## Measurement limits

Decode timings are medians of three fixed-seed randomized passes over
memory-resident blobs, with a reused zlib reader. They include allocation,
decompression, full attribute reconstruction, and geometry reconstruction.
They exclude SQLite queries, disk I/O, point-in-polygon work, program startup,
network traffic, and rendering. They are not end-to-end lookup timings.
Encoding timings in the JSON report are single-pass diagnostics, not a stable
benchmark result.

Changing only feature payloads leaves roughly 415 MB of the current file's
non-blob storage, including columns, indexes, RTree pages, page headers, and
unused page space. A different schema or block table will change that amount;
only a complete candidate pack can establish its final portable file size.
The single source record without geometry is outside these spatial samples
and must remain preserved in any production format.
