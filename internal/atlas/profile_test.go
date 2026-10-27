package atlas

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Optional real-data diagnostic; it uses the same per-state representative
// selection as westbench. Set PLSS_PROFILE_WEST to the captured state directory.
func TestWesternRepresentativeProfile(t *testing.T) {
	dir := os.Getenv("PLSS_PROFILE_WEST")
	if dir == "" {
		t.Skip("set PLSS_PROFILE_WEST to captured state atlas directory")
	}
	files, err := filepath.Glob(filepath.Join(dir, "*-packed.atlas"))
	if err != nil || len(files) == 0 {
		t.Fatalf("state files: %v", err)
	}
	for _, path := range files {
		a, err := OpenMapped(path)
		if err != nil {
			t.Fatal(err)
		}
		id := a.Keys.Count()/2 + 1
		g, err := a.Geometry.Record(id)
		if err != nil {
			t.Fatal(err)
		}
		p, err := g.InteriorPoint()
		if err != nil {
			t.Fatal(err)
		}
		var candidates, vertices, rings int
		for layer := uint8(3); layer > 0; layer-- {
			rows, err := a.view.SearchLayer(p.Longitude, p.Latitude, layer)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				g, err := a.Geometry.Record(row.ID)
				if err != nil {
					t.Fatal(err)
				}
				candidates++
				rings += len(g.Rings)
				for _, ring := range g.Rings {
					vertices += len(ring)
				}
			}
			if len(rows) > 0 {
				break
			}
		}
		hits, err := a.Lookup(context.Background(), p.Latitude, p.Longitude)
		if err != nil {
			t.Fatal(err)
		}
		// Calibrate above the host clock's resolution; tiny fixed loops can
		// otherwise report misleading zero durations on some Windows hosts.
		n := 200
		var elapsed time.Duration
		for {
			start := time.Now()
			for i := 0; i < n; i++ {
				if _, err := a.Lookup(context.Background(), p.Latitude, p.Longitude); err != nil {
					t.Fatal(err)
				}
			}
			elapsed = time.Since(start)
			if elapsed >= 50*time.Millisecond {
				break
			}
			n *= 2
		}
		us := float64(elapsed.Nanoseconds()) / float64(n) / 1000
		allocs := testing.AllocsPerRun(20, func() {
			if _, err := a.Lookup(context.Background(), p.Latitude, p.Longitude); err != nil {
				t.Fatal(err)
			}
		})
		t.Logf("%s selected=%d point=%.12f,%.12f candidates=%d vertices=%d rings=%d hits=%d mean_us=%.3f allocs=%.0f prepared_accounted_bytes=%d", a.Manifest.Source.State, id, p.Latitude, p.Longitude, candidates, vertices, rings, len(hits), us, allocs, a.prepared.used)
		a.Close()
	}
}
