# Deferred implicit spatial-ID experiment

No production format changes or measured performance claims came from this
experiment. It was paused when Washington's repeated survey identifiers proved
to be a much larger problem, subsequently addressed by key-index v3.

The existing spatial q8 index stores four quantized bound bytes, four feature-ID
bytes, and one layer byte per record. A forest of three layer-specific Morton
trees could omit IDs and layers if geometry, keys, and optional attributes all
use exactly the same new feature order. Internal IDs would then be
`layer base + leaf ordinal + 1`; published cadastral IDs and source OBJECTIDs
would remain unchanged. Layer-major ordering preserves the key-index assumption
that each layer occupies one contiguous ID range.

For Nevada's 1,510,482 records, removing the five explicit ID/layer bytes would
save 7,552,410 bytes before any tree-layout changes. This is a structural
calculation, not a completed full-file measurement. Keeping original geometry
order would require a 6,041,928-byte permutation table and erase most savings.

Split antimeridian bounds must not allocate multiple implicit feature IDs:
retain one primary box per feature, with a separate sparse explicit-ID tree for
additional boxes. Duplicate published cadastral identifiers remain distinct
source features throughout. Quantization must stay conservative, followed by
full-precision original polygon containment.

Before adoption, measure actual full-state component sizes, key/attribute
compression changes after reordering, mapped open cost, random and repeated
query latency, and exact agreement at real polygon vertices and edge midpoints.
No implementation is retained here because it had not reached that validation.
