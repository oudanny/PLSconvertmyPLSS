package plss

import (
	"math"
	"testing"
)

func TestContainsPoint(t *testing.T) {
	outer := [][]float64{{0, 0}, {10, 0}, {10, 10}, {0, 10}, {0, 0}}
	hole := [][]float64{{2, 2}, {2, 8}, {8, 8}, {8, 2}, {2, 2}}
	for _, tc := range []struct {
		name     string
		geometry Geometry
		point    Point
		want     bool
	}{
		{"interior", Geometry{Rings: [][][]float64{outer}}, Point{5, 5}, true},
		{"exterior", Geometry{Rings: [][][]float64{outer}}, Point{11, 5}, false},
		{"edge", Geometry{Rings: [][][]float64{outer}}, Point{0, 5}, true},
		{"vertex", Geometry{Rings: [][][]float64{outer}}, Point{0, 0}, true},
		{"roundoff near boundary", Geometry{Rings: [][][]float64{outer}}, Point{-5e-13, 5}, true},
		{"outside tolerance", Geometry{Rings: [][][]float64{outer}}, Point{-2e-12, 5}, false},
		{"inside hole", Geometry{Rings: [][][]float64{outer, hole}}, Point{5, 5}, false},
		{"hole boundary", Geometry{Rings: [][][]float64{outer, hole}}, Point{2, 5}, true},
		{"hole corner", Geometry{Rings: [][][]float64{outer, hole}}, Point{2, 2}, true},
		{"outside hole", Geometry{Rings: [][][]float64{outer, hole}}, Point{1, 5}, true},
		{"open ring", Geometry{Rings: [][][]float64{outer[:4]}}, Point{5, 5}, true},
		{"concave notch", Geometry{Rings: [][][]float64{{{0, 0}, {10, 0}, {10, 2}, {2, 2}, {2, 8}, {10, 8}, {10, 10}, {0, 10}}}}, Point{5, 5}, false},
		{"sloped boundary", Geometry{Rings: [][][]float64{{{0, 0}, {10, 0}, {0, 10}}}}, Point{5, 5}, true},
		{"tiny polygon", Geometry{Rings: [][][]float64{{{-105, 40}, {-105 + 1e-8, 40}, {-105 + 1e-8, 40 + 1e-8}, {-105, 40 + 1e-8}}}}, Point{40 + 5e-9, -105 + 5e-9}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.geometry.ContainsPoint(tc.point)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("ContainsPoint(%+v)=%v, want %v", tc.point, got, tc.want)
			}
			if got {
				assertIndexed(t, tc.geometry, tc.point)
			}
		})
	}
}

func TestContainsMultipart(t *testing.T) {
	g := Geometry{Rings: [][][]float64{
		{{0, 0}, {1, 0}, {1, 1}, {0, 1}},
		{{2, 2}, {3, 2}, {3, 3}, {2, 3}},
	}}
	for _, tc := range []struct {
		p    Point
		want bool
	}{
		{Point{0.5, 0.5}, true}, {Point{2.5, 2.5}, true}, {Point{1.5, 1.5}, false},
	} {
		got, err := g.ContainsPoint(tc.p)
		if err != nil || got != tc.want {
			t.Fatalf("ContainsPoint(%+v)=%v,%v", tc.p, got, err)
		}
		if got {
			assertIndexed(t, g, tc.p)
		}
	}
}

func TestContainsAntimeridian(t *testing.T) {
	for _, ring := range [][][]float64{
		{{179, 0}, {-179, 0}, {-179, 1}, {179, 1}},
		{{-179, 0}, {179, 0}, {179, 1}, {-179, 1}},
	} {
		g := Geometry{Rings: [][][]float64{ring}}
		boxes, err := g.IndexBounds()
		if err != nil || len(boxes) != 2 {
			t.Fatalf("expected two seam boxes, got %v, %v", boxes, err)
		}
		for _, tc := range []struct {
			lon  float64
			want bool
		}{
			{179.5, true}, {-179.5, true}, {180, true}, {-180, true}, {179, true}, {-179, true}, {178, false}, {-178, false}, {0, false},
		} {
			p := Point{0.5, tc.lon}
			got, err := g.ContainsPoint(p)
			if err != nil || got != tc.want {
				t.Fatalf("longitude %v got %v,%v", tc.lon, got, err)
			}
			if got {
				assertIndexed(t, g, p)
			}
		}
	}
}

func TestIndexBoundsSeamAndPoles(t *testing.T) {
	for _, g := range []Geometry{
		{Rings: [][][]float64{{{-180, 0}, {-179, 0}, {-179, 1}, {-180, 1}}}},
		{Rings: [][][]float64{{{179, 0}, {180, 0}, {180, 1}, {179, 1}}}},
	} {
		for _, lon := range []float64{-180, 180} {
			p := Point{0.5, lon}
			got, err := g.ContainsPoint(p)
			if err != nil || !got {
				t.Fatalf("seam point missing: %v %v", got, err)
			}
			assertIndexed(t, g, p)
		}
	}
	g := Geometry{Rings: [][][]float64{{{0, 89}, {1, 89}, {1, 90}, {0, 90}}}}
	assertIndexed(t, g, Point{90, 0.5})
}

func TestContainsValidation(t *testing.T) {
	valid := Geometry{Rings: [][][]float64{{{0, 0}, {1, 0}, {1, 1}, {0, 1}}}}
	for _, p := range []Point{{math.NaN(), 0}, {0, math.Inf(1)}, {91, 0}, {0, 181}} {
		if _, err := valid.ContainsPoint(p); err == nil {
			t.Errorf("accepted invalid point %+v", p)
		}
	}
	for _, g := range []Geometry{
		{}, {Rings: [][][]float64{{{0, 0}, {1, 1}}}},
		{Rings: [][][]float64{{{0, 0}, {1, 1}, {2, 2}}}},
		{Rings: [][][]float64{{{0, 0}, {1, math.NaN()}, {1, 1}}}},
		{Rings: [][][]float64{{{0, 0}, {181, 0}, {1, 1}}}},
		{Rings: [][][]float64{{{0, 0}, {1, 0}, {1, 1}}, {{2, 2}}}},
	} {
		if _, err := g.ContainsPoint(Point{0, 0}); err == nil {
			t.Errorf("accepted invalid polygon %v", g)
		}
		if _, err := g.IndexBounds(); err == nil {
			t.Errorf("indexed invalid polygon %v", g)
		}
	}
}

func assertIndexed(t *testing.T, g Geometry, p Point) {
	t.Helper()
	boxes, err := g.IndexBounds()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, b := range boxes {
		if b.MinLon < -180 || b.MaxLon > 180 || b.MinLat < -90 || b.MaxLat > 90 || b.MinLon > b.MaxLon || b.MinLat > b.MaxLat {
			t.Fatalf("invalid index rectangle %+v", b)
		}
		if b.MinLon <= p.Longitude && b.MaxLon >= p.Longitude && b.MinLat <= p.Latitude && b.MaxLat >= p.Latitude {
			found = true
		}
	}
	if !found {
		t.Fatalf("polygon contains %+v but boxes exclude it: %+v", p, boxes)
	}
}
