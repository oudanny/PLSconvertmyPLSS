package plss

import (
	"errors"
	"math"
	"sort"
)

// InteriorPoint returns a planar point inside the supplied polygon, including
// polygons with holes and disconnected parts. It is not a surveyed corner or
// an asserted geodesic centroid. Longitude is unwrapped around the first vertex
// so small cadastral polygons crossing the antimeridian behave correctly.
func (g Geometry) InteriorPoint() (Point, error) {
	rings, err := g.unwrapped()
	if err != nil {
		return Point{}, err
	}
	minY, maxY := math.Inf(1), math.Inf(-1)
	var ys []float64
	for _, r := range rings {
		for _, v := range r {
			minY = math.Min(minY, v[1])
			maxY = math.Max(maxY, v[1])
			ys = append(ys, v[1])
		}
	}
	if minY == maxY {
		return Point{}, errors.New("degenerate polygon")
	}
	// A horizontal scan intersects alternating exterior/interior intervals;
	// choosing an interval midpoint avoids centroids that fall inside holes.
	find := func(y float64) (Point, bool) {
		var xs []float64
		for _, r := range rings {
			for i := range r {
				a, b := r[i], r[(i+1)%len(r)]
				if (a[1] > y) != (b[1] > y) {
					xs = append(xs, a[0]+(y-a[1])*(b[0]-a[0])/(b[1]-a[1]))
				}
			}
		}
		sort.Float64s(xs)
		best := 0.0
		var x float64
		for i := 0; i+1 < len(xs); i += 2 {
			w := xs[i+1] - xs[i]
			if w > best {
				best = w
				x = (xs[i] + xs[i+1]) / 2
			}
		}
		if best <= 0 {
			return Point{}, false
		}
		for x > 180 {
			x -= 360
		}
		for x < -180 {
			x += 360
		}
		return Point{Latitude: y, Longitude: x}, true
	}
	// A scan exactly through a vertex can run along a hole boundary. Select
	// only heights strictly between vertex heights to ensure a true interior.
	mid := (minY + maxY) / 2
	atVertex := false
	for _, y := range ys {
		if y == mid {
			atVertex = true
			break
		}
	}
	if !atVertex {
		if p, ok := find(mid); ok {
			return p, nil
		}
	}
	// A multipart polygon's bounding-box midpoint may fall between parts.
	sort.Float64s(ys)
	for i := 0; i+1 < len(ys); i++ {
		if ys[i] < ys[i+1] {
			if p, ok := find((ys[i] + ys[i+1]) / 2); ok {
				return p, nil
			}
		}
	}
	return Point{}, errors.New("polygon has no nonzero interior")
}

func (g Geometry) unwrapped() ([][][]float64, error) {
	if len(g.Rings) == 0 || len(g.Rings[0]) < 3 || len(g.Rings[0][0]) < 2 {
		return nil, errors.New("polygon is empty or malformed")
	}
	ref := g.Rings[0][0][0]
	rings := make([][][]float64, len(g.Rings))
	for i, r := range g.Rings {
		if len(r) < 3 {
			return nil, errors.New("polygon ring has fewer than three vertices")
		}
		rings[i] = make([][]float64, len(r))
		for j, v := range r {
			if len(v) < 2 || !finite(v[0]) || !finite(v[1]) || v[0] < -180 || v[0] > 180 || v[1] < -90 || v[1] > 90 {
				return nil, errors.New("invalid WGS84 polygon vertex")
			}
			x := v[0]
			for x-ref > 180 {
				x -= 360
			}
			for x-ref < -180 {
				x += 360
			}
			rings[i][j] = []float64{x, v[1]}
		}
	}
	return rings, nil
}
