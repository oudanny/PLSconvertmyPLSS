package atlas

import (
	"container/list"
	"sync"

	"github.com/oudanny/PLSconvertmyPLSS/internal/preparedgeom"
)

// The cap is per initialized state. It bounds accounted prepared storage plus
// a conservative per-entry bookkeeping allowance, rather than polygon count.
const preparedCacheBytes = 4 << 20
const preparedEntryOverhead = 160

type preparedEntry struct {
	id       uint32
	geometry *preparedgeom.Geometry
	bytes    int
}
type preparedCache struct {
	mu          sync.Mutex
	limit, used int
	entries     map[uint32]*list.Element
	order       list.List
}

func newPreparedCache(limit int) *preparedCache {
	return &preparedCache{limit: limit, entries: map[uint32]*list.Element{}}
}

func (c *preparedCache) get(id uint32) *preparedgeom.Geometry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[id]; e != nil {
		c.order.MoveToFront(e)
		return e.Value.(preparedEntry).geometry
	}
	return nil
}

func (c *preparedCache) put(id uint32, p *preparedgeom.Geometry) {
	bytes := p.MemoryBytes() + preparedEntryOverhead
	if bytes > c.limit {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[id]; e != nil {
		c.order.MoveToFront(e)
		return
	}
	for c.used+bytes > c.limit {
		e := c.order.Back()
		old := e.Value.(preparedEntry)
		delete(c.entries, old.id)
		c.order.Remove(e)
		c.used -= old.bytes
	}
	c.entries[id] = c.order.PushFront(preparedEntry{id: id, geometry: p, bytes: bytes})
	c.used += bytes
}

func (a *Reader) preparedGeometry(id uint32) (*preparedgeom.Geometry, error) {
	if p := a.prepared.get(id); p != nil {
		return p, nil
	}
	g, err := a.Geometry.Record(id)
	if err != nil {
		return nil, err
	}
	p, err := preparedgeom.New(g)
	if err != nil {
		return nil, err
	}
	a.prepared.put(id, p)
	return p, nil
}
