package atlas

import (
	"sync"
	"testing"

	"github.com/oudanny/PLSconvertmyPLSS/internal/preparedgeom"
	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

func preparedSquare(t *testing.T) *preparedgeom.Geometry {
	t.Helper()
	p, err := preparedgeom.New(plss.Geometry{Rings: [][][]float64{{{0, 0}, {1, 0}, {1, 1}, {0, 1}, {0, 0}}}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPreparedCacheByteBoundEvictionAndConcurrentRetention(t *testing.T) {
	p := preparedSquare(t)
	size := p.MemoryBytes() + preparedEntryOverhead
	c := newPreparedCache(2 * size)
	c.put(1, p)
	c.put(2, p)
	retained := c.get(1)
	c.put(3, p)
	if c.get(2) != nil || c.get(1) == nil || c.get(3) == nil || c.used != 2*size {
		t.Fatal("byte budget or LRU eviction failed")
	}
	// A larger immutable candidate is usable without eviction or retention.
	tooSmall := newPreparedCache(size - 1)
	tooSmall.put(1, p)
	if tooSmall.get(1) != nil || tooSmall.used != 0 {
		t.Fatal("oversized candidate was cached")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for n := 0; n < 1000; n++ {
				id := uint32(worker*1000 + n)
				c.put(id, p)
				if got := c.get(id); got != nil {
					inside, err := got.ContainsPoint(plss.Point{Latitude: .5, Longitude: .5})
					if err != nil || !inside {
						t.Error("cached geometry mutated")
						return
					}
				}
			}
		}(i)
	}
	wg.Wait()
	if c.used > c.limit || len(c.entries) > 2 {
		t.Fatal("concurrent inserts exceeded byte budget")
	}
	if inside, err := retained.ContainsPoint(plss.Point{Latitude: .5, Longitude: .5}); err != nil || !inside {
		t.Fatal("eviction invalidated an in-flight immutable reader")
	}
}
