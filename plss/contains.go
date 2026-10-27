package plss

import (
	"errors"
	"math"
)

// boundaryTolerance absorbs float64 arithmetic noise without assigning a
// meaningful strip of land to both sides of a boundary (about 0.1 micrometres
// of latitude). Index bounds use the same padding to retain these candidates.
const boundaryTolerance = 1e-12

// Bounds is an axis-aligned WGS84 rectangle, in longitude/latitude degrees.
// Each rectangle returned by IndexBounds stays within [-180,180] longitude
// and [-90,90] latitude and can be inserted directly into a spatial index.
type Bounds struct {
	MinLon float64
	MaxLon float64
	MinLat float64
	MaxLat float64
}

// ContainsPoint reports whether p intersects the polygon, including its outer
// boundary and the boundaries of holes. A point strictly inside a hole is
// excluded. Rings use even-odd filling, supporting holes and disconnected parts
// independently of ring orientation. This is a planar test in longitude and
// latitude, with longitude unwrapped for cadastral polygons at the antimeridian.
func (g Geometry) ContainsPoint(p Point) (bool, error) {
	if !finite(p.Latitude) || !finite(p.Longitude) || p.Latitude < -90 || p.Latitude > 90 || p.Longitude < -180 || p.Longitude > 180 {
		return false, errors.New("point must contain finite WGS84 latitude and longitude")
	}
	rings, err := g.validatedRings()
	if err != nil {
		return false, err
	}
	x, y := p.Longitude, p.Latitude
	ref := rings[0][0][0]
	for x-ref > 180 {
		x -= 360
	}
	for x-ref < -180 {
		x += 360
	}
	inside := false
	for _, ring := range rings {
		for i, a := range ring {
			b := ring[(i+1)%len(ring)]
			if onSegment(x, y, a, b) {
				return true, nil
			}
			if (a[1] > y) != (b[1] > y) && x < a[0]+(y-a[1])*(b[0]-a[0])/(b[1]-a[1]) {
				inside = !inside
			}
		}
	}
	return inside, nil
}

func onSegment(x, y float64, a, b []float64) bool {
	dx, dy := b[0]-a[0], b[1]-a[1]
	qx, qy := x-a[0], y-a[1]
	lengthSquared := dx*dx + dy*dy
	if lengthSquared == 0 {
		return math.Hypot(qx, qy) <= boundaryTolerance
	}
	t := (qx*dx + qy*dy) / lengthSquared
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(qx-t*dx, qy-t*dy) <= boundaryTolerance
}

// IndexBounds returns conservative bounding rectangles for a spatial index.
// An antimeridian-crossing polygon produces two rectangles; a point at either
// representation of the seam (-180 or +180) will remain an index candidate.
// These rectangles only shortlist candidates: ContainsPoint must make the
// final decision. The full polygon bounds may include holes and multipart gaps.
func (g Geometry) IndexBounds() ([]Bounds, error) {
	rings, err := g.validatedRings()
	if err != nil {
		return nil, err
	}
	minX, maxX, minY, maxY := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	for _, ring := range rings {
		for _, v := range ring {
			minX, maxX = math.Min(minX, v[0]), math.Max(maxX, v[0])
			minY, maxY = math.Min(minY, v[1]), math.Max(maxY, v[1])
		}
	}
	minX, maxX = minX-boundaryTolerance, maxX+boundaryTolerance
	minY, maxY = math.Max(-90, minY-boundaryTolerance), math.Min(90, maxY+boundaryTolerance)
	if maxX-minX >= 360 {
		return []Bounds{{MinLon: -180, MaxLon: 180, MinLat: minY, MaxLat: maxY}}, nil
	}
	if minX < -180 {
		return []Bounds{
			{MinLon: -180, MaxLon: maxX, MinLat: minY, MaxLat: maxY},
			{MinLon: minX + 360, MaxLon: 180, MinLat: minY, MaxLat: maxY},
		}, nil
	}
	if maxX > 180 {
		return []Bounds{
			{MinLon: minX, MaxLon: 180, MinLat: minY, MaxLat: maxY},
			{MinLon: -180, MaxLon: maxX - 360, MinLat: minY, MaxLat: maxY},
		}, nil
	}
	return []Bounds{{MinLon: minX, MaxLon: maxX, MinLat: minY, MaxLat: maxY}}, nil
}

// In addition to the vertex validation in unwrapped, reject zero-area rings.
// Subtracting a nearby origin avoids cancellation for small cadastral polygons
// represented by coordinates far from zero. Full topology repair is out of scope.
func (g Geometry) validatedRings() ([][][]float64, error) {
	rings, err := g.unwrapped()
	if err != nil {
		return nil, err
	}
	for _, ring := range rings {
		origin := ring[0]
		area := 0.0
		for i, a := range ring {
			b := ring[(i+1)%len(ring)]
			area += (a[0]-origin[0])*(b[1]-origin[1]) - (b[0]-origin[0])*(a[1]-origin[1])
		}
		if area == 0 {
			return nil, errors.New("polygon ring has zero area")
		}
	}
	return rings, nil
}
