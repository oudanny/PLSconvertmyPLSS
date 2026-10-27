package plss

import (
	"math"
	"testing"
)

func inside(g Geometry, p Point) bool {
	rings, e := g.unwrapped()
	if e != nil {
		return false
	}
	x := p.Longitude
	ref := rings[0][0][0]
	for x-ref > 180 {
		x -= 360
	}
	for x-ref < -180 {
		x += 360
	}
	y := p.Latitude
	in := false
	for _, r := range rings {
		for i := range r {
			a, b := r[i], r[(i+1)%len(r)]
			cross := (x-a[0])*(b[1]-a[1]) - (y-a[1])*(b[0]-a[0])
			if math.Abs(cross) < 1e-12 && x >= math.Min(a[0], b[0]) && x <= math.Max(a[0], b[0]) && y >= math.Min(a[1], b[1]) && y <= math.Max(a[1], b[1]) {
				return false
			}
			if (a[1] > y) != (b[1] > y) && x < (b[0]-a[0])*(y-a[1])/(b[1]-a[1])+a[0] {
				in = !in
			}
		}
	}
	return in
}

func TestInteriorPoint(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rings [][][]float64
	}{
		{"rectangle", [][][]float64{{{0, 0}, {10, 0}, {10, 10}, {0, 10}, {0, 0}}}},
		{"concave", [][][]float64{{{0, 0}, {10, 0}, {10, 2}, {2, 2}, {2, 8}, {10, 8}, {10, 10}, {0, 10}, {0, 0}}}},
		{"hole", [][][]float64{{{0, 0}, {10, 0}, {10, 10}, {0, 10}, {0, 0}}, {{1, 1}, {1, 9}, {9, 9}, {9, 1}, {1, 1}}}},
		{"hole edge at middle", [][][]float64{{{0, 0}, {10, 0}, {10, 10}, {0, 10}, {0, 0}}, {{1, 1}, {1, 5}, {9, 5}, {9, 1}, {1, 1}}}},
		{"multipart", [][][]float64{{{0, 0}, {1, 0}, {1, 1}, {0, 1}, {0, 0}}, {{2, 9}, {3, 9}, {3, 10}, {2, 10}, {2, 9}}}},
		{"dateline", [][][]float64{{{179, 0}, {-179, 0}, {-179, 1}, {179, 1}, {179, 0}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := Geometry{Rings: tc.rings}
			p, e := g.InteriorPoint()
			if e != nil {
				t.Fatal(e)
			}
			if !inside(g, p) {
				t.Fatalf("point %+v outside polygon", p)
			}
		})
	}
}

func TestInvalidPolygon(t *testing.T) {
	for _, g := range []Geometry{{}, {Rings: [][][]float64{{{1, 1}, {2, 2}}}}, {Rings: [][][]float64{{{0, 0}, {1, 0}, {2, 0}}}}, {Rings: [][][]float64{{{0, 0}, {1, math.NaN()}, {1, 1}}}}} {
		if _, e := g.InteriorPoint(); e == nil {
			t.Errorf("accepted bad polygon: %v", g)
		}
	}
}
