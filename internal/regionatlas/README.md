# Region atlas

`regionatlas.Build(path, []ShardInput{{Path: "az.atlas"}, {Path: "ca.atlas"}})` creates one file containing the original state atlas bytes. `BuildContext(ctx, path, inputs)` supports cancellation during verification/copying and before publication. The optional `State` field asserts the input's source state. The builder stages in the output directory, syncs the completed file, then uses an atomic no-overwrite hard link to publish it. Existing output files are refused. Failed or canceled builds remove their staging file; an abruptly killed process can leave a `.partial` staging file, but no incomplete final destination.

The builder verifies each state before copying it, derives conservative extents from its spatial index, and records the source metadata, source SHA256, and copied-state SHA256. The bounded 64 KiB region header has its own SHA256. It contains provenance and routes; absent-geometry audit records stay in their state headers. Polygons and source records are never merged or deduplicated.

`OpenMapped` maps the region once. State readers initialize only when needed. By default all requested states stay available, with separately bounded geometry caches. `OpenWithOptions(path, Options{MaxOpenShards: 3})` caps initialized state readers using a pinned LRU cache; a busy cache waits without exceeding that cap. This trades repeated state-open work for lower retained memory. `Open` uses buffered reads. All openers also accept a standalone state atlas.

Opening independently validates every state's provenance, conservative index extent, and ID routes before using those routes to skip states. Version3 keys keep the routing dictionary compact; older key versions with large dictionary headers can take longer to inspect. Complete component and copied-shard checksums are verified explicitly with `Verify`.

Coordinate lookup tests the original full-precision polygons in every candidate state and then keeps all matches at the globally finest available layer: intersected subdivision, section, township. State boundaries and overlapping records stay distinct. A result's identity is `(State, FeatureID)`; local feature numbers can repeat in different states. Published IDs can also repeat. Reverse ID routing accounts for independent survey IDs and foreign-state prefixes rather than assuming every identifier starts with its source state.

Readers support concurrent calls. `Close` waits for active calls, closes borrowed state readers, then releases the single mapping and file. Returned hits, source metadata, audit attributes, and reconstructed geometry own their data and remain valid after closing. Files must remain unchanged while open.

Application code outside this module should import the public `offline` package instead of this internal implementation. The compact format remains experimental.
