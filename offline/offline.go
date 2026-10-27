// Package offline reads compact PLSS state atlases and multi-state region files.
// Lookups use the original, full-precision source polygons and never access a
// network. The compact on-disk format is experimental; use matching builders.
package offline

import (
	"context"

	"github.com/oudanny/PLSconvertmyPLSS/internal/atlas"
	"github.com/oudanny/PLSconvertmyPLSS/internal/regionatlas"
	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

// Options controls file access and the number of initialized state readers.
type Options struct {
	// Buffered uses ordinary file reads instead of a read-only native mapping.
	Buffered bool
	// LazyStateCache caps initialized states. Zero keeps every requested state
	// lazily (at most64); a positive cap uses an LRU cache. Smaller caps reduce
	// retained memory but require reopening states after eviction.
	LazyStateCache int
}

// Reader supports concurrent queries. Close waits for in-flight methods before
// releasing the mapping. Returned hits, metadata and geometry remain usable
// after Close. Keep the backing file unchanged while the reader is open.
type Reader struct{ inner *regionatlas.Reader }

// Open reads either a state atlas or region using one read-only memory mapping.
func Open(path string) (*Reader, error) { return OpenWithOptions(path, Options{}) }
func OpenWithOptions(path string, options Options) (*Reader, error) {
	r, err := regionatlas.OpenWithOptions(path, regionatlas.Options{Buffered: options.Buffered, MaxOpenShards: options.LazyStateCache})
	if err != nil {
		return nil, err
	}
	return &Reader{inner: r}, nil
}

// Hit identifies one original source record. FeatureID is local to State.
// Different source records can share a published ID and are never collapsed.
type Hit struct {
	State     string `json:"state"`
	FeatureID uint32 `json:"feature_id"`
	Layer     string `json:"layer"`
	ID        string `json:"id"`
}

func publicHits(in []regionatlas.Hit) []Hit {
	if in == nil {
		return nil
	}
	out := make([]Hit, len(in))
	for i, h := range in {
		out[i] = Hit{State: h.State, FeatureID: h.FeatureID, Layer: h.Layer, ID: h.ID}
	}
	return out
}
func internalHit(h Hit) regionatlas.Hit {
	return regionatlas.Hit{State: h.State, Hit: atlas.Hit{FeatureID: h.FeatureID, Layer: h.Layer, ID: h.ID}}
}

// Lookup returns all containing records at the finest available PLSS layer
// across every overlapping state: intersected subdivisions, then sections,
// then townships. Coordinates are WGS84 decimal degrees. Boundary matches can
// return multiple records. Missing coverage returns plss.ErrNotFound.
func (r *Reader) Lookup(ctx context.Context, latitude, longitude float64) ([]Hit, error) {
	hits, err := r.inner.Lookup(ctx, latitude, longitude)
	return publicHits(hits), err
}

// FindID accepts exact published IDs with kind township, section,
// division/intersected, or survey. It preserves all records sharing that ID,
// including foreign-prefix source exceptions. A known ID with no published
// polygon returns plss.ErrNoGeometry; absent IDs return plss.ErrNotFound.
func (r *Reader) FindID(kind, id string) ([]Hit, error) {
	hits, err := r.inner.FindID(kind, id)
	return publicHits(hits), err
}

// Representative computes an interior point without loading source metadata.
// PLSS describes an area, so this point is approximate, not a surveyed corner.
func (r *Reader) Representative(hit Hit) (plss.Point, error) {
	return r.inner.Representative(internalHit(hit))
}

// Full returns original rings and an interior representative. A full atlas
// includes its original source attributes; a core atlas returns identifiers.
// HasFullAttributes distinguishes those cases without loading attribute rows.
func (r *Reader) Full(hit Hit) (plss.Match, error) { return r.inner.Full(internalHit(hit)) }
func (r *Reader) HasFullAttributes(hit Hit) (bool, error) {
	return r.inner.HasFullAttributes(internalHit(hit))
}
func (r *Reader) Sources() []plss.PackMetadata { return r.inner.Sources() }

// UnlocatedRecord retains a known source record with no published polygon.
type UnlocatedRecord struct {
	State string `json:"state"`
	plss.UnlocatedRecord
}

func (r *Reader) Unlocated(ctx context.Context) ([]UnlocatedRecord, error) {
	items, err := r.inner.Unlocated(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]UnlocatedRecord, len(items))
	for i, item := range items {
		out[i] = UnlocatedRecord{State: item.State, UnlocatedRecord: item.UnlocatedRecord}
	}
	return out, nil
}

// Verify reads every component and validates routing, records and SHA256 hashes.
// Opening and individual lookups intentionally do not scan the entire file.
func (r *Reader) Verify() error { return r.inner.Verify() }
func (r *Reader) Close() error  { return r.inner.Close() }
