package preparedgeom_test

import (
	"math"
	"math/rand"
	"sync"
	"testing"

	"github.com/oudanny/PLSconvertmyPLSS/internal/preparedgeom"
	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

func polygon(rings ...[][]float64) plss.Geometry { return plss.Geometry{Rings: rings} }
func square(x, y, size float64) [][]float64 {
	return [][]float64{{x, y}, {x + size, y}, {x + size, y + size}, {x, y + size}, {x, y}}
}
func normalLon(x float64) float64 {
	for x > 180 {
		x -= 360
	}
	for x < -180 {
		x += 360
	}
	return x
}

func compare(t *testing.T, g plss.Geometry, p *preparedgeom.Geometry, point plss.Point) {
	t.Helper()
	want, e1 := g.ContainsPoint(point)
	got, e2 := p.ContainsPoint(point)
	if (e1 == nil) != (e2 == nil) || want != got {
		t.Fatalf("prepared differs at%+.17g/%+.17g: got%t/%v want%t/%v rings%v", point.Latitude, point.Longitude, got, e2, want, e1, g.Rings)
	}
}

func TestContainmentMatchesIndependentOracleAtEdgesHolesAndDateline(t *testing.T) {
	geometries := []plss.Geometry{
		polygon(square(-119.8, 39.5, .01)),
		polygon(square(0, 0, 4), square(1, 1, 2)),
		polygon([][]float64{{0, 0}, {4, 0}, {4, 1}, {1, 1}, {1, 4}, {0, 4}, {0, 0}}),
		polygon(square(-10, -10, 1), square(10, 10, 1)),
		polygon([][]float64{{179.8, 10}, {-179.8, 10}, {-179.8, 12}, {179.8, 12}, {179.8, 10}}, [][]float64{{179.9, 10.5}, {-179.9, 10.5}, {-179.9, 11.5}, {179.9, 11.5}, {179.9, 10.5}}),
		polygon([][]float64{{-180, -2}, {-179, -2}, {-179, -1}, {-180, -1}, {-180, -2}}),
		polygon(square(180-1e-10, 89, 1e-10)),
		polygon(square(0, 0, 1e-13)),
		polygon([][]float64{{math.Copysign(0, -1), 0, math.NaN()}, {1, 0}, {1, 1}, {0, 1}}),
	}
	rng := rand.New(rand.NewSource(20261028))
	for _, g := range geometries {
		for orientation := 0; orientation < 2; orientation++ {
			if orientation == 1 {
				for _, ring := range g.Rings {
					for a, b := 0, len(ring)-1; a < b; a, b = a+1, b-1 {
						ring[a], ring[b] = ring[b], ring[a]
					}
				}
			}
			p, err := preparedgeom.New(g)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 1000; i++ {
				compare(t, g, p, plss.Point{Latitude: rng.Float64()*180 - 90, Longitude: rng.Float64()*360 - 180})
			}
			for _, ring := range g.Rings {
				for i, a := range ring {
					b := ring[(i+1)%len(ring)]
					dx := b[0] - a[0]
					for dx > 180 {
						dx -= 360
					}
					for dx < -180 {
						dx += 360
					}
					dy := b[1] - a[1]
					length := math.Hypot(dx, dy)
					nx, ny := 1., 0.
					if length != 0 {
						nx, ny = -dy/length, dx/length
					}
					for _, fraction := range []float64{0, .1, .5, .9, 1} {
						for _, delta := range []float64{0, -4e-12, -2e-12, -1.001e-12, -1e-12, -.999e-12, -.75e-12, -.25e-12, .25e-12, .75e-12, .999e-12, 1e-12, 1.001e-12, 2e-12, 4e-12} {
							x, y := normalLon(a[0]+fraction*dx+nx*delta), a[1]+fraction*dy+ny*delta
							for _, direction := range []float64{math.Inf(-1), x, math.Inf(1)} {
								compare(t, g, p, plss.Point{Latitude: y, Longitude: normalLon(math.Nextafter(x, direction))})
							}
						}
					}
				}
			}
		}
	}
}

func TestRandomPolygonsMatchIndependentOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for iteration := 0; iteration < 250; iteration++ {
		x, y := rng.Float64()*358-179, rng.Float64()*178-89
		size := math.Pow10(-rng.Intn(10)) * .1
		n := 3 + rng.Intn(35)
		ring := make([][]float64, n)
		for i := range ring {
			angle := float64(i) / float64(n) * 2 * math.Pi
			radius := size * (.2 + .8*rng.Float64())
			ring[i] = []float64{x + radius*math.Cos(angle), y + radius*math.Sin(angle)}
		}
		g := polygon(ring)
		p, err := preparedgeom.New(g)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 250; i++ {
			compare(t, g, p, plss.Point{Longitude: x + (rng.Float64()*2-1)*size, Latitude: y + (rng.Float64()*2-1)*size})
		}
	}
}

func TestValidationOwnershipAndConcurrency(t *testing.T) {
	for _, g := range []plss.Geometry{{}, polygon(nil), polygon([][]float64{{0, 0}, {1, 1}}), polygon([][]float64{{0, 0}, {1, 1}, {2, 2}}), polygon(square(0, 0, 1), [][]float64{{2, 2}, {3, 3}, {4, 4}}), polygon([][]float64{{0, 0}, {1, math.NaN()}, {0, 1}}), polygon([][]float64{{0, 0}, {181, 1}, {0, 1}}), polygon([][]float64{{0, 0}, {1, 91}, {0, 1}}), polygon([][]float64{{0, 0}, {1}, {0, 1}})} {
		_, want := g.ContainsPoint(plss.Point{})
		_, got := preparedgeom.New(g)
		if want == nil || got == nil {
			t.Fatalf("validation differed: %v %v", got, want)
		}
	}
	g := polygon(square(0, 0, 1))
	p, err := preparedgeom.New(g)
	if err != nil {
		t.Fatal(err)
	}
	for _, point := range []plss.Point{{Latitude: math.NaN()}, {Longitude: math.Inf(1)}, {Latitude: 91}, {Longitude: 181}} {
		compare(t, g, p, point)
	}
	g.Rings[0][0][0] = 99
	g.Rings[0] = nil
	g.Rings = nil
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 1000; n++ {
				inside, err := p.ContainsPoint(plss.Point{Latitude: .5, Longitude: .5})
				if err != nil || !inside {
					t.Error("prepared geometry aliased input or shared mutable state")
					return
				}
			}
		}()
	}
	wg.Wait()
}

// Report allocations as a benchmark, rather than a unit-test invariant:
// compiler safety instrumentation can intentionally introduce allocations.
func BenchmarkPreparedContains(b *testing.B) {
	p, err := preparedgeom.New(polygon(square(0, 0, 1)))
	if err != nil {
		b.Fatal(err)
	}
	point := plss.Point{Latitude: .5, Longitude: .5}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		inside, err := p.ContainsPoint(point)
		if err != nil || !inside {
			b.Fatal("unexpected containment result")
		}
	}
}
