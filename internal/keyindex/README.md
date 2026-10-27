# Factored cadastral ID index

New files use `PLSSKEY3`. The layout is:

1. Eight-byte magic, uint32 JSON-manifest length, and the manifest.
2. `Count × Width` bytes of bit-packed township/first/second-division codes.
3. Survey dictionary codes: either bit-packed codes for every row or sorted
   sparse row/code pairs, whichever takes fewer bytes.
4. `(township dictionary length + 1)` little-endian uint32 offsets.
5. Delta-coded posting groups, each beginning with a varint row count.

Survey dictionary entries store either an independent string or a suffix of
the same row's township, first-division, or second-division ID. Empty suffixes
preserve exact equality; code zero alone means no survey ID. Survey identifiers
that begin with their township use the existing township postings for reverse
lookup. Only identifiers with genuinely different prefixes need exception
postings. No published ID strings or duplicate source records are discarded.

Offsets are relative to the posting payload. The first is zero and the final
sentinel equals payload length. Groups include the empty-township group; even
an empty group has a one-byte zero count. The offsets must increase strictly.

`OpenView(data)` borrows immutable packed records, offsets and posting bytes.
It decodes the small JSON manifest into ordinary Go-owned dictionaries/maps.
Its caller must keep the backing bytes or read-only mapping alive and unchanged
until all index queries finish. Returned `ID` strings, `Fields` maps/strings and
`Find` slices are owned Go values and may outlive the mapping.

Version3 opening validates framing, dictionaries, sparse survey ordering and offset bounds without
decoding feature postings. `Find` validates accessed counts, positive deltas,
group boundaries, dictionary references and township membership. `Validate`
scans all groups, checks complete feature coverage and verifies exception and
survey links. Use it for explicit full-file integrity verification.

`Open(readerAt, size)` retains its owned-copy behavior and accepts v1, v2 and
v3 files. `OpenView` also accepts v1, but v1 has no directory, so its opening must
scan the posting stream to locate group boundaries. New `Builder.WriteTo`
output is always v3.

The actual Nevada v2 key component is 7,800,979 bytes, versus 7,787,439 bytes for
v1: its new offset directory adds 13,540 bytes. The feature records and posting
contents are unchanged.

Washington exposed why v3 is needed: 541,722 of 551,476 published survey IDs
equaled their second-division ID. The v2 component stored those full strings
twice, once by row and once for reverse lookup. The complete Washington key
component fell from 44,496,850 to 4,869,229 bytes. Its JSON header fell from
40,682,223 to 186,807 bytes; 11-bit survey codes occupy 867,795 bytes. Sparse
encoding avoids paying a dense-column cost in states with few survey IDs.

`RoutingPrefixes` and `ReadRoutingPrefixes` cover independent division and
survey identifiers as well as ordinary township prefixes. Short or unusual
prefixes produce a wildcard route. Routing reads dictionaries only, without
reading packed records or postings; full validation also checks the persisted
route summary against those dictionaries.

`MemoryBytes` excludes borrowed/mapped bytes. `MemoryStats` reports exact owned
slice backing-array capacities separately from an estimate for parsed strings,
maps and the index structure. The estimate excludes allocator rounding, precise
runtime map overhead and temporary query allocations. A mapped file still
consumes resident OS pages as accessed; `BorrowedBytes` reports its component
length, not a claim of zero physical memory use.

Tests cover v1/v2 compatibility and v3 packed/sparse surveys, prefix exceptions, duplicate IDs, returned-value
lifetime, zero-copy records, malformed offsets/postings, omissions and corrupt
exception references. The parser/query fuzz target completed approximately
one million executions in the initial five-second run without a panic.
