package main

import (
	"math/rand"
	"reflect"
	"testing"

	"github.com/oudanny/PLSconvertmyPLSS/internal/spatialindex"
)

func TestGridMatchesBruteForceIncludingCellBoundaries(t *testing.T) {
	rng := rand.New(rand.NewSource(52))
	entries := make([]spatialindex.Entry, 200)
	for i := range entries {
		x := float32(-120 + rng.Float64())
		y := float32(35 + rng.Float64())
		entries[i] = spatialindex.Entry{MinLon: x, MaxLon: x + .02, MinLat: y, MaxLat: y + .02, ID: uint32(i + 1), Layer: uint8(i%3 + 1)}
	}
	entries = append(entries, spatialindex.Entry{MinLon: -120, MaxLon: -119.5, MinLat: 35, MaxLat: 35.5, ID: 201, Layer: 3})
	for _, cell := range []float64{.01, .02, .05} {
		g, err := newGrid(entries, cell)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 1000; i++ {
			x, y := -120+rng.Float64(), 35+rng.Float64()
			if i < 200 {
				e := entries[i]
				x, y = float64(e.MinLon), float64(e.MaxLat)
			}
			if i == 999 {
				x, y = -119.5, 35.5
			}
			var want []spatialindex.Match
			for _, e := range entries {
				if x >= float64(e.MinLon) && x <= float64(e.MaxLon) && y >= float64(e.MinLat) && y <= float64(e.MaxLat) {
					want = append(want, spatialindex.Match{ID: e.ID, Layer: e.Layer})
				}
			}
			want = sortedUnique(want)
			got := g.search(x, y)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("cell%g point%g,%g got%v want%v", cell, x, y, got, want)
			}
		}
	}
}
