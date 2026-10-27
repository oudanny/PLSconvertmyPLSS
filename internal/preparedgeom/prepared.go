// Package preparedgeom prepares immutable exact polygon containment tests.
// Its validation, unwrapping, even-odd fill, and final boundary arithmetic match
// plss.Geometry.ContainsPoint. Bounds only reject impossible edge/ring matches.
package preparedgeom

import (
	"errors"
	"math"
	"unsafe"

	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

// Keep the final distance threshold identical to plss.boundaryTolerance.
const boundaryTolerance = 1e-12

type bounds struct{ minX, maxX, minY, maxY float64 }
type edge struct {
	ax, ay, by            float64
	dx, dy, lengthSquared float64
	bounds
}
type ring struct {
	first, end int
	bounds
}

// Geometry owns its arrays and supports concurrent, allocation-free queries.
// It contains only planar testing data; it does not replace the original rings
// used for exported geometry or representative-point calculation.
type Geometry struct {
	reference float64
	rings     []ring
	edges     []edge
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// padded rounds away from the interval after padding. The doubled pruning
// margin conservatively covers float arithmetic noise; final inclusion still
// uses exactly1e-12 degrees, so this does not widen polygon boundaries.
func padded(minX, maxX, minY, maxY float64) bounds {
	const padding = 2 * boundaryTolerance
	return bounds{math.Nextafter(minX-padding, math.Inf(-1)), math.Nextafter(maxX+padding, math.Inf(1)), math.Nextafter(minY-padding, math.Inf(-1)), math.Nextafter(maxY+padding, math.Inf(1))}
}

// New validates every ring and copies all needed coordinates. Extra ordinates
// are ignored for planar containment exactly as in the source implementation.
func New(g plss.Geometry) (*Geometry, error) {
	if len(g.Rings) == 0 || len(g.Rings[0]) < 3 || len(g.Rings[0][0]) < 2 {
		return nil, errors.New("polygon is empty or malformed")
	}
	total := 0
	for _, r := range g.Rings {
		if len(r) < 3 {
			return nil, errors.New("polygon ring has fewer than three vertices")
		}
		if len(r) > int(^uint(0)>>1)-total {
			return nil, errors.New("polygon is too large")
		}
		total += len(r)
	}
	p := &Geometry{reference: g.Rings[0][0][0], rings: make([]ring, len(g.Rings)), edges: make([]edge, total)}
	first := 0
	for i, r := range g.Rings {
		last := first + len(r)
		minX, maxX, minY, maxY := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
		for j, v := range r {
			if len(v) < 2 || !finite(v[0]) || !finite(v[1]) || v[0] < -180 || v[0] > 180 || v[1] < -90 || v[1] > 90 {
				return nil, errors.New("invalid WGS84 polygon vertex")
			}
			x := v[0]
			for x-p.reference > 180 {
				x -= 360
			}
			for x-p.reference < -180 {
				x += 360
			}
			p.edges[first+j].ax, p.edges[first+j].ay = x, v[1]
			minX, maxX = math.Min(minX, x), math.Max(maxX, x)
			minY, maxY = math.Min(minY, v[1]), math.Max(maxY, v[1])
		}
		origin := p.edges[first]
		area := 0.0
		for j := first; j < last; j++ {
			a := &p.edges[j]
			next := j + 1
			if next == last {
				next = first
			}
			b := p.edges[next]
			// Keep the original arithmetic and operand order; reciprocal slopes
			// would change rounding at points extremely close to a boundary.
			area += (a.ax-origin.ax)*(b.ay-origin.ay) - (b.ax-origin.ax)*(a.ay-origin.ay)
			a.by = b.ay
			a.dx = b.ax - a.ax
			a.dy = b.ay - a.ay
			a.lengthSquared = a.dx*a.dx + a.dy*a.dy
			a.bounds = padded(math.Min(a.ax, b.ax), math.Max(a.ax, b.ax), math.Min(a.ay, b.ay), math.Max(a.ay, b.ay))
		}
		if area == 0 {
			return nil, errors.New("polygon ring has zero area")
		}
		p.rings[i] = ring{first: first, end: last, bounds: padded(minX, maxX, minY, maxY)}
		first = last
	}
	return p, nil
}

func (b bounds) contains(x, y float64) bool {
	return x >= b.minX && x <= b.maxX && y >= b.minY && y <= b.maxY
}

func (e edge) onSegment(x, y float64) bool {
	qx, qy := x-e.ax, y-e.ay
	if e.lengthSquared == 0 {
		return math.Hypot(qx, qy) <= boundaryTolerance
	}
	t := (qx*e.dx + qy*e.dy) / e.lengthSquared
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(qx-t*e.dx, qy-t*e.dy) <= boundaryTolerance
}

func (p *Geometry) ContainsPoint(point plss.Point) (bool, error) {
	x, y := point.Longitude, point.Latitude
	if !finite(x) || !finite(y) || x < -180 || x > 180 || y < -90 || y > 90 {
		return false, errors.New("point must contain finite WGS84 latitude and longitude")
	}
	for x-p.reference > 180 {
		x -= 360
	}
	for x-p.reference < -180 {
		x += 360
	}
	inside := false
	for _, r := range p.rings {
		if !r.bounds.contains(x, y) {
			continue
		}
		for i := r.first; i < r.end; i++ {
			e := &p.edges[i]
			if y < e.minY || y > e.maxY {
				continue
			}
			if x >= e.minX && x <= e.maxX && e.onSegment(x, y) {
				return true, nil
			}
			if (e.ay > y) != (e.by > y) && x < e.ax+(y-e.ay)*e.dx/e.dy {
				inside = !inside
			}
		}
	}
	return inside, nil
}

// MemoryBytes counts owned array storage and the Geometry value. Cache
// bookkeeping is accounted separately by the owning reader.
func (p *Geometry) MemoryBytes() int {
	return int(unsafe.Sizeof(*p)) + cap(p.rings)*int(unsafe.Sizeof(ring{})) + cap(p.edges)*int(unsafe.Sizeof(edge{}))
}
