# Lossless vertex dictionary experiment

`compactgeom.NewBuilder` continues writing the original v1 format. Explicit
`NewBuilderWithOptions(path, BuilderOptions{VertexCodec: VertexXORBitpack,
VertexBlockSize: 256})` selects the smaller v2 dictionary. Both v1 and v2 readers
remain supported. The optional `VertexXORShuffleZlib` codec is retained for
comparison; its random cache misses cost substantially more CPU.

On the complete Nevada component, direct bit packing reduced geometry from
41,321,472 to **34,826,095 bytes** (15.7%) with comparable measured geometry
lookup throughput. Record blocks, vertex IDs, ring order, closure, overlaps,
duplicate source records, and extra ordinates remain unchanged. Every output
float64 bit was compared with the previously source-verified component for
all **1,510,482 records**, separately for both block sizes.

| Dictionary | Whole component bytes | Random geometry read | Repeated16 geometry read |
|---|---:|---:|---:|
| Raw v1 | 41,321,472 | 36.84 µs | 1.87 µs |
| Bitpack128 | 34,873,443 | 34.71 µs | 1.42 µs |
| Bitpack256 | 34,826,095 | 35.36 µs | 1.47 µs |

These are warm OS-cache, memory-mapped component reads with a bounded64-block
vertex cache and16-block geometry cache, median of three runs on an
i7-1195G7/Windows/Go1.27.3 host. Random reads use10,000 deterministically sampled
feature IDs. Repeated reads use the first16 after warmup. The timing includes
full geometry reconstruction, excluding opening/mapping, and does not include
the atlas spatial search, exact point-in-polygon test, or reverse-ID lookup.
Concurrent host activity and frequency changes affect these measurements;
small differences should not be interpreted as a proven speedup. Structured
raw measurements include preload comparisons and allocation counts.

XOR/shuffle+zlib256 produced34,169,711 bytes (656,384 fewer than bitpack256),
but random geometry reads measured147.5 µs in a separate run. Direct bitpack
provides the better measured size/lookup tradeoff here.

## Layout and caches

The128-byte component header retains the existing magic, with version2 at
offset8. Decimals remains0. The formerly reserved final16 bytes store vertex
block size (u32), codec (u32:1=zlib,2=bitpack), and vertex-directory offset
(u64). Compressed record blocks precede the vertex payload, then the vertex
directory, then the original record directory. Both directories use16-byte
entries: offset:u64, stored-size:u32, raw-size:u32. Sections must be contiguous.

A bitpack block contains baseX:u64, baseY:u64, widthX:u8, widthY:u8, two separate
fixed-width XOR bit streams in original vertex order, and a CRC32C:u32. The
directory supplies the count. XOR bit width is the highest differing bit from
the base, including all lower bits; widths0–64 are valid. Individual vertices
are decoded directly by arithmetic offsets and bit extraction. Floating-point
rounding is never involved. The checksum is checked once per block cache miss.

`ReaderOptions.VertexCacheBlocks` independently bounds these blocks; the
default `OpenReaderAt` uses64. Explicit reader options must opt into this cache.
Zero disables it. `VertexCachePages` still controls raw v1 pages.
`PreloadVertices` reconstructs the full direct dictionary once and uses
`Metadata.RawVertexBytes` additional payload memory (25,288,896 bytes forNV).
`Metadata.VertexBytes` describes stored vertex payload; `DirectoryBytes`
includes both directories, with `VertexDirectoryBytes` exposing the extra part.
Returned geometry owns its coordinates; cached block bytes remain immutable.

## Reproduce

```
go run ./internal/geomlab -source data/topolab/lossless.cgeom -out data/geomlab -codec bitpack
go run ./internal/geomlab -source data/topolab/lossless.cgeom -out data/geomlab -codec xor-shuffle
```

Outputs are new exclusive-create component files under the ignored `data/`
directory. `RepackVertices` copies record blocks verbatim and streams the exact
dictionary into the chosen encoding. Each tool run verifies all features and
prints JSON measurements. Source and output components must have Decimals=0;
the repacker refuses to relabel a rounded source as exact.

Tests cover all fixed widths and unaligned reads, signed zero and subnormals,
arbitrary extra-ordinate bits, partial final blocks, section embedding,
malformed directory sizes, checksums, padding, bounded caches, concurrent
reads, and recovery after failed decompression. This remains an experimental
component format, separate from the public SQLite pack.
