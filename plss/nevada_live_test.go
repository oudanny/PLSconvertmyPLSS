package plss_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

// Integration checks and the real-pack benchmark are opt-in. Ordinary tests
// never download Nevada or require a network connection.
var nevadaPoints = []struct {
	name     string
	lat, lon float64
}{
	{"Reno", 39.5296, -119.8138},
	{"Las Vegas", 36.1699, -115.1398},
	{"Carson City", 39.1638, -119.7674},
	{"Elko", 40.8324, -115.7631},
	{"Ely", 39.2474, -114.8880},
	{"Winnemucca", 40.9730, -117.7357},
	{"Tonopah", 38.0672, -117.2301},
	{"Pahrump", 36.2083, -115.9839},
	{"Fallon", 39.4735, -118.7774},
	{"Boulder City", 35.9786, -114.8325},
}

func matchKeys(matches []plss.Match) []string {
	keys := make([]string, len(matches))
	for i, m := range matches {
		objectID, _ := json.Marshal(m.Attributes["OBJECTID"])
		keys[i] = m.Layer + ":" + m.ID() + ":" + string(objectID)
	}
	sort.Strings(keys)
	return keys
}

func TestNevadaLiveParity(t *testing.T) {
	path := os.Getenv("PLSS_NEVADA_PACK")
	if path == "" || os.Getenv("PLSS_LIVE_VERIFY") != "1" {
		t.Skip("set PLSS_NEVADA_PACK and PLSS_LIVE_VERIFY=1 for live BLM comparison")
	}
	pack, err := plss.OpenPack(path)
	if err != nil {
		t.Fatal(err)
	}
	defer pack.Close()
	client := plss.NewClient()
	for _, point := range nevadaPoints {
		t.Run(point.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			local, err := pack.Lookup(ctx, point.lat, point.lon)
			if err != nil {
				t.Fatal(err)
			}
			remote, err := client.Lookup(ctx, point.lat, point.lon)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(matchKeys(local), matchKeys(remote)) {
				t.Fatalf("offline %v != online %v", matchKeys(local), matchKeys(remote))
			}
			for _, m := range local {
				inside, err := m.Geometry.ContainsPoint(plss.Point{Latitude: point.lat, Longitude: point.lon})
				if err != nil || !inside {
					t.Fatalf("point not in returned boundary: %v", err)
				}
				kind, id := "intersected", m.Attribute("SECDIVID")
				if id == "" {
					kind, id = "section", m.Attribute("FRSTDIVID")
				}
				if id == "" {
					kind, id = "township", m.Attribute("PLSSID")
				}
				localByID, err := pack.FindID(ctx, kind, id)
				if err != nil {
					t.Fatal(err)
				}
				remoteByID, err := client.FindID(ctx, kind, id)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(matchKeys(localByID), matchKeys(remoteByID)) {
					t.Fatalf("ID %s differs offline/online", id)
				}
			}
			t.Logf("matched %v", matchKeys(local))
		})
	}
}

func BenchmarkNevadaLookup(b *testing.B) {
	path := os.Getenv("PLSS_NEVADA_PACK")
	if path == "" {
		b.Skip("set PLSS_NEVADA_PACK to benchmark a complete real pack")
	}
	pack, err := plss.OpenPack(path)
	if err != nil {
		b.Fatal(err)
	}
	defer pack.Close()
	ctx := context.Background()
	for _, p := range nevadaPoints {
		if _, err := pack.Lookup(ctx, p.lat, p.lon); err != nil {
			b.Fatal(fmt.Errorf("%s: %w", p.name, err))
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p := nevadaPoints[i%len(nevadaPoints)]
		if _, err := pack.Lookup(ctx, p.lat, p.lon); err != nil {
			b.Fatal(err)
		}
	}
}

// These sub-benchmarks keep a pack open and warm each point before timing.
// They expose variation hidden by a single average over ten cities, including
// a miss that exercises all three layer fallbacks. No network is accessed.
func BenchmarkNevadaLookupLocations(b *testing.B) {
	path := os.Getenv("PLSS_NEVADA_PACK")
	if path == "" {
		b.Skip("set PLSS_NEVADA_PACK to benchmark a complete real pack")
	}
	points := append([]struct {
		name     string
		lat, lon float64
	}{}, nevadaPoints...)
	points = append(points, struct {
		name     string
		lat, lon float64
	}{"Outside_pack", 40, -124})
	for _, point := range points {
		b.Run(point.name, func(b *testing.B) {
			pack, err := plss.OpenPack(path)
			if err != nil {
				b.Fatal(err)
			}
			defer pack.Close()
			ctx := context.Background()
			initial, initialErr := pack.Lookup(ctx, point.lat, point.lon)
			if initialErr != nil && !errors.Is(initialErr, plss.ErrNotFound) {
				b.Fatal(initialErr)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				matches, err := pack.Lookup(ctx, point.lat, point.lon)
				if (err != nil && !errors.Is(err, plss.ErrNotFound)) || len(matches) != len(initial) {
					b.Fatalf("lookup changed: %d matches, %v", len(matches), err)
				}
			}
			b.ReportMetric(float64(len(initial)), "matches/op")
		})
	}
}

// Spread measures a repeatable 512-point workload across Nevada's approximate
// bounding rectangle. Some points fall outside the state or published PLSS
// coverage, and therefore exercise the no-match path. The first pass warms the
// workload's OS/database pages; this does not measure a cold disk cache.
func BenchmarkNevadaLookupSpread(b *testing.B) {
	path := os.Getenv("PLSS_NEVADA_PACK")
	if path == "" {
		b.Skip("set PLSS_NEVADA_PACK to benchmark a complete real pack")
	}
	pack, err := plss.OpenPack(path)
	if err != nil {
		b.Fatal(err)
	}
	defer pack.Close()
	rng := rand.New(rand.NewSource(21406))
	points := make([]plss.Point, 512)
	for i := range points {
		points[i] = plss.Point{Latitude: 35 + 7*rng.Float64(), Longitude: -120 + 6*rng.Float64()}
	}
	ctx := context.Background()
	hits := 0
	for _, point := range points {
		matches, err := pack.Lookup(ctx, point.Latitude, point.Longitude)
		if err != nil && !errors.Is(err, plss.ErrNotFound) {
			b.Fatal(err)
		}
		if len(matches) > 0 {
			hits++
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		point := points[i%len(points)]
		if _, err := pack.Lookup(ctx, point.Latitude, point.Longitude); err != nil && !errors.Is(err, plss.ErrNotFound) {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(hits)/float64(len(points)), "hit_fraction")
}

// OpenLookupWarmOSCache includes opening/validating the database and closing it
// for every lookup, unlike the persistent-open benchmarks. OS caches are warmed
// first; neither process startup nor physical cold-storage latency is measured.
func BenchmarkNevadaOpenLookupWarmOSCache(b *testing.B) {
	path := os.Getenv("PLSS_NEVADA_PACK")
	if path == "" {
		b.Skip("set PLSS_NEVADA_PACK to benchmark a complete real pack")
	}
	ctx := context.Background()
	warm, err := plss.OpenPack(path)
	if err != nil {
		b.Fatal(err)
	}
	for _, point := range nevadaPoints {
		if _, err := warm.Lookup(ctx, point.lat, point.lon); err != nil {
			_ = warm.Close()
			b.Fatal(err)
		}
	}
	if err := warm.Close(); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pack, err := plss.OpenPack(path)
		if err != nil {
			b.Fatal(err)
		}
		point := nevadaPoints[i%len(nevadaPoints)]
		_, err = pack.Lookup(ctx, point.lat, point.lon)
		closeErr := pack.Close()
		if err != nil {
			b.Fatal(err)
		}
		if closeErr != nil {
			b.Fatal(closeErr)
		}
	}
}
