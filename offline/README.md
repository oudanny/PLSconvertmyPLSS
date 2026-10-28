# Offline library

Read one state atlas or a multi-state region file without network access:

```go
import (
    "context"
    "github.com/oudanny/PLSconvertmyPLSS/offline"
)

data, err := offline.Open("western-us.atlas")
if err != nil { return err }
defer data.Close()

hits, err := data.Lookup(context.Background(), 39.5296, -119.8138)
if err != nil { return err }
for _, hit := range hits {
    // hit.State + hit.FeatureID identifies the original source record.
    // hit.ID is the published BLM identifier; hit.Layer names its PLSS layer.
    point, err := data.Representative(hit)
    if err != nil { return err }
    _ = point
}
```

`FindID(kind, id)` supports exact township, section, division/intersected, and survey IDs. `Representative` computes an approximate interior point. `Full` returns original rings and available attributes; `HasFullAttributes` distinguishes a full source-attributes file from a core conversion file. `Sources`, `Unlocated`, and `Verify` expose provenance, records without geometry, and a complete integrity check.

The reader preserves overlapping records and uses a global subdivision → section → township fallback across states. It never snaps, rounds, or merges source polygons. Lookups can return multiple boundary matches. Missing coverage returns `plss.ErrNotFound`; a known ID without published geometry returns `plss.ErrNoGeometry`.

The default maps the file once and lazily retains requested states. `OpenWithOptions(path, offline.Options{Buffered: true, LazyStateCache: 3})` uses ordinary reads and caps initialized states, at the cost of reopening evicted states. Concurrent calls are supported; `Close` waits for them. Returned data remains usable after closing. Keep the source file unchanged while open.

`OpenReaderAt(source, size, offline.Options{LazyStateCache: 2})` accepts an
immutable `io.ReaderAt`, including a bounded browser file adapter. It borrows the
source without closing it or copying the entire atlas. State indexes initialize
on demand; only the configured number remain cached. File sizes and offsets are
`int64`, so the container does not have to fit in a Go byte slice. The browser
worker adapter is documented in [`cmd/plsswasm`](../cmd/plsswasm/README.md).

The file format is experimental. Structured PLSS-description search is not yet exposed through this reader; it does not implement `plss.Provider`.
