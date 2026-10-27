// Command atlaslab builds and measures the integrated experimental offline file.
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/oudanny/PLSconvertmyPLSS/coord"
	"github.com/oudanny/PLSconvertmyPLSS/internal/atlas"
	"github.com/oudanny/PLSconvertmyPLSS/internal/keyindex"
	"github.com/oudanny/PLSconvertmyPLSS/plss"
	_ "modernc.org/sqlite"
)

func database(path string) (*sql.DB, error) {
	p, e := filepath.Abs(path)
	if e != nil {
		return nil, e
	}
	p = filepath.ToSlash(p)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p, RawQuery: "mode=ro"}
	return sql.Open("sqlite", u.String())
}
func emit(v any) error { e := json.NewEncoder(os.Stdout); e.SetIndent("", "  "); return e.Encode(v) }

func buildKeys(dbPath, out string) error {
	db, err := database(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	read := func(fn func(keyindex.Source) error) error {
		rows, err := db.Query("SELECT id,layer,plssid,frstdivid,secdivid,survid FROM features ORDER BY id")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r keyindex.Source
			if err = rows.Scan(&r.ID, &r.Layer, &r.Township, &r.First, &r.Second, &r.Survey); err != nil {
				return err
			}
			if err = fn(r); err != nil {
				return err
			}
		}
		return rows.Err()
	}
	start := time.Now()
	b := keyindex.NewBuilder()
	if err = read(b.Add); err != nil {
		return err
	}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	size, err := b.WriteTo(f)
	if err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	x, err := keyindex.Open(f, size)
	if err != nil {
		return err
	}
	verified := 0
	err = read(func(r keyindex.Source) error {
		want := r.Second
		if want == "" {
			want = r.Survey
		}
		if want == "" {
			want = r.First
		}
		if want == "" {
			want = r.Township
		}
		got, err := x.ID(r.ID)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("key row%d changed: %q != %q", r.ID, got, want)
		}
		if r.ID%997 == 1 {
			kind := map[uint8]string{1: "township", 2: "section", 3: "division"}[r.Layer]
			value := map[uint8]string{1: r.Township, 2: r.First, 3: r.Second}[r.Layer]
			if value != "" {
				ids, err := x.Find(kind, value)
				if err != nil {
					return err
				}
				found := false
				for _, id := range ids {
					if id == r.ID {
						found = true
					}
				}
				if !found {
					return fmt.Errorf("reverse index lost row%d", r.ID)
				}
			}
		}
		verified++
		return nil
	})
	if err != nil {
		return err
	}
	return emit(map[string]any{"file_bytes": size, "records_verified": verified, "build_and_verify_seconds": time.Since(start).Seconds()})
}

type stats struct {
	Count          int     `json:"queries"`
	MeanUS         float64 `json:"mean_us"`
	MedianUS       float64 `json:"median_us"`
	P95US          float64 `json:"p95_us"`
	P99US          float64 `json:"p99_us"`
	AllocatedBytes float64 `json:"allocated_bytes_per_query"`
	Allocs         float64 `json:"allocations_per_query"`
}

func measure(n int, fn func(int) error) (stats, error) {
	samples := make([]int64, n)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := preciseNow()
	for i := 0; i < n; i++ {
		t := preciseNow()
		if err := fn(i); err != nil {
			return stats{}, err
		}
		samples[i] = preciseNow() - t
	}
	elapsed := preciseNow() - start
	runtime.ReadMemStats(&after)
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	return stats{n, float64(elapsed) / float64(n) / 1e3, float64(samples[n/2]) / 1e3, float64(samples[int(float64(n)*.95)]) / 1e3, float64(samples[int(float64(n)*.99)]) / 1e3, float64(after.TotalAlloc-before.TotalAlloc) / float64(n), float64(after.Mallocs-before.Mallocs) / float64(n)}, nil
}

var cities = []plss.Point{{Latitude: 39.5296, Longitude: -119.8138}, {Latitude: 36.1699, Longitude: -115.1398}, {Latitude: 39.1638, Longitude: -119.7674}, {Latitude: 40.8324, Longitude: -115.7631}, {Latitude: 39.2474, Longitude: -114.8880}, {Latitude: 40.9730, Longitude: -117.7357}, {Latitude: 38.0672, Longitude: -117.2301}, {Latitude: 36.2083, Longitude: -115.9839}, {Latitude: 39.4735, Longitude: -118.7774}, {Latitude: 35.9786, Longitude: -114.8325}}

func matchKeys(ms []plss.Match) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Layer + ":" + m.ID()
	}
	sort.Strings(out)
	return out
}
func okay(err error) error {
	if errors.Is(err, plss.ErrNotFound) {
		return nil
	}
	return err
}

func bench(path, source string, n int, mapped bool) error {
	var heapBefore, heapAfter runtime.MemStats
	runtime.ReadMemStats(&heapBefore)
	start := preciseNow()
	var a *atlas.Reader
	var err error
	if mapped {
		a, err = atlas.OpenMapped(path)
	} else {
		a, err = atlas.Open(path)
	}
	if err != nil {
		return err
	}
	openUS := float64(preciseNow()-start) / 1e3
	runtime.ReadMemStats(&heapAfter)
	defer a.Close()
	if err = a.Verify(); err != nil {
		return err
	}
	old, err := plss.OpenPack(source)
	if err != nil {
		return err
	}
	defer old.Close()
	ctx := context.Background()
	rng := rand.New(rand.NewSource(20261028))
	points := make([]plss.Point, n)
	for i := range points {
		points[i] = plss.Point{Latitude: 35 + 7*rng.Float64(), Longitude: -120 + 6*rng.Float64()}
	}
	// Include original vertices and edge midpoints; these are true source
	// boundaries, not rounded spatial-index boxes.
	for i := 0; i < 500; i++ {
		g, err := a.Geometry.Record(uint32(rng.Intn(int(a.Keys.Count()))) + 1)
		if err != nil {
			return err
		}
		r := g.Rings[0]
		v, w := r[0], r[1]
		points = append(points, plss.Point{Latitude: v[1], Longitude: v[0]}, plss.Point{Latitude: (v[1] + w[1]) / 2, Longitude: (v[0] + w[0]) / 2})
	}
	pointCount := len(points)
	hits := 0
	for i, p := range points {
		current, ce := a.Lookup(ctx, p.Latitude, p.Longitude)
		original, oe := old.Lookup(ctx, p.Latitude, p.Longitude)
		if okay(ce) != nil {
			return ce
		}
		if okay(oe) != nil {
			return oe
		}
		if !reflect.DeepEqual(atlas.Keys(current), matchKeys(original)) {
			return fmt.Errorf("point%d %.15g,%.15g differs: %v vs %v", i, p.Latitude, p.Longitude, atlas.Keys(current), matchKeys(original))
		}
		if len(current) > 0 {
			hits++
		}
		for _, h := range current {
			g, err := a.Geometry.Record(h.FeatureID)
			if err != nil {
				return err
			}
			found := false
			for _, m := range original {
				if m.ID() == h.ID && m.Layer == h.Layer && reflect.DeepEqual(g, m.Geometry) {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("point%d changed returned geometry", i)
			}
		}
		if (i+1)%2000 == 0 {
			fmt.Fprintf(os.Stderr, "verified %d / %d points\n", i+1, pointCount)
		}
	}
	result := map[string]any{"format": "PLSSATL1", "path": path, "mapped": mapped, "go_version": runtime.Version(), "verified_point_count": pointCount, "point_hits": hits, "open_us_os_cache_unspecified": openUS, "index_array_bytes": a.IndexMemoryBytes(), "key_encoded_bytes": a.Keys.MemoryBytes(), "geometry_metadata": a.Geometry.Metadata(), "sections": a.Manifest.Sections, "cache_note": "Index uses owned arrays or borrowed mapped bytes; bounded geometry cache; OS pages warmed by verification; full metadata omitted from lightweight timings."}
	result["open_go_heap_delta_bytes"] = int64(heapAfter.HeapAlloc) - int64(heapBefore.HeapAlloc)
	result["key_index_memory"] = a.Keys.MemoryStats()
	delete(result, "key_encoded_bytes")
	for _, p := range cities {
		if _, err = a.Lookup(ctx, p.Latitude, p.Longitude); err != nil {
			return err
		}
		if _, err = old.Lookup(ctx, p.Latitude, p.Longitude); err != nil {
			return err
		}
	}
	s, err := measure(10000, func(i int) error {
		p := cities[i%len(cities)]
		_, e := a.Lookup(ctx, p.Latitude, p.Longitude)
		return okay(e)
	})
	if err != nil {
		return err
	}
	result["atlas_hot_cities"] = s
	s, err = measure(10000, func(i int) error {
		p := cities[i%len(cities)]
		_, e := old.Lookup(ctx, p.Latitude, p.Longitude)
		return okay(e)
	})
	if err != nil {
		return err
	}
	result["sqlite_hot_cities"] = s
	s, err = measure(n, func(i int) error { p := points[i]; _, e := a.Lookup(ctx, p.Latitude, p.Longitude); return okay(e) })
	if err != nil {
		return err
	}
	result["atlas_spread"] = s
	s, err = measure(n, func(i int) error { p := points[i]; _, e := old.Lookup(ctx, p.Latitude, p.Longitude); return okay(e) })
	if err != nil {
		return err
	}
	result["sqlite_spread"] = s
	// Reverse exact IDs include holes/duplicates through the same geometry.
	var reverse []atlas.Hit
	for _, p := range cities {
		h, e := a.Lookup(ctx, p.Latitude, p.Longitude)
		if e != nil {
			return e
		}
		for i := range h {
			fields, e := a.Keys.Fields(h[i].FeatureID)
			if e != nil {
				return e
			}
			switch {
			case fields["SECDIVID"] != "":
				h[i].Layer = "division"
			case fields["SURVID"] != "":
				h[i].Layer = "survey"
			case fields["FRSTDIVID"] != "":
				h[i].Layer = "section"
			default:
				h[i].Layer = "township"
			}
		}
		reverse = append(reverse, h...)
	}
	for _, h := range reverse {
		orig, e := old.FindID(ctx, h.Layer, h.ID)
		if e != nil {
			return e
		}
		got, e := a.FindID(h.Layer, h.ID)
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(matchKeys(orig), atlas.Keys(got)) {
			return errors.New("reverse ID mismatch")
		}
	}
	s, err = measure(10000, func(i int) error { h := reverse[i%len(reverse)]; _, e := a.FindID(h.Layer, h.ID); return e })
	if err != nil {
		return err
	}
	result["reverse_id_only"] = s
	s, err = measure(10000, func(i int) error {
		h := reverse[i%len(reverse)]
		hs, e := a.FindID(h.Layer, h.ID)
		if e != nil {
			return e
		}
		for _, v := range hs {
			g, e := a.Geometry.Record(v.FeatureID)
			if e != nil {
				return e
			}
			if _, e = g.InteriorPoint(); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	result["reverse_id_and_representative"] = s
	if a.Attributes != nil {
		s, e := measure(min(n, 1000), func(i int) error {
			p := points[i]
			hs, e := a.Lookup(ctx, p.Latitude, p.Longitude)
			if okay(e) != nil {
				return e
			}
			for _, h := range hs {
				if _, e = a.Full(h); e != nil {
					return e
				}
			}
			return nil
		})
		if e != nil {
			return e
		}
		result["atlas_spread_full_attributes"] = s
	}
	return emit(result)
}

func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: atlaslab keys|assemble|bench|lookup|id [flags]")
	}
	command := os.Args[1]
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	db := fs.String("db", "data/nevada.sqlite", "source v1 pack")
	out := fs.String("out", "data/atlaslab/nevada-core.atlas", "new output file")
	file := fs.String("file", "data/atlaslab/nevada-core.atlas", "atlas to query")
	keys := fs.String("keys", "data/atlaslab/nevada.keys", "factored ID component")
	idx := fs.String("index", "internal/indexlab/out/nevada-rtree16-q8.idx", "spatial component")
	geom := fs.String("geometry", "data/topolab/lossless.cgeom", "exact geometry component")
	attrs := fs.String("attributes", "", "optional full-attribute component")
	value := fs.String("value", "39.5296,-119.8138", "lat/lon in supported notation")
	id := fs.String("id", "", "published PLSS ID")
	kind := fs.String("layer", "division", "ID kind")
	n := fs.Int("points", 10000, "random verification/benchmark points")
	mapped := fs.Bool("mmap", false, "use read-only OS mapping and lazy index leaves")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	if command == "keys" {
		if err := os.MkdirAll(filepath.Dir(*keys), 0755); err != nil {
			return err
		}
		return buildKeys(*db, *keys)
	}
	if command == "assemble" {
		p, err := plss.OpenPack(*db)
		if err != nil {
			return err
		}
		m, err := p.Metadata()
		if err != nil {
			p.Close()
			return err
		}
		audit, err := p.Unlocated(context.Background())
		p.Close()
		if err != nil {
			return err
		}
		src, err := os.Open(*db)
		if err != nil {
			return err
		}
		h := sha256.New()
		_, err = io.Copy(h, src)
		src.Close()
		if err != nil {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(*out), 0755); err != nil {
			return err
		}
		manifest, err := atlas.Build(*out, m, hex.EncodeToString(h.Sum(nil)), map[string]string{"keys": *keys, "index": *idx, "geometry": *geom, "attributes": *attrs}, audit)
		if err != nil {
			return err
		}
		a, err := atlas.Open(*out)
		if err != nil {
			return err
		}
		defer a.Close()
		if err = a.Verify(); err != nil {
			return err
		}
		return emit(manifest)
	}
	if command == "bench" {
		if *n < 100 || *n > 100000 {
			return errors.New("points must be100–100000")
		}
		return bench(*file, *db, *n, *mapped)
	}
	var a *atlas.Reader
	var err error
	if *mapped {
		a, err = atlas.OpenMapped(*file)
	} else {
		a, err = atlas.Open(*file)
	}
	if err != nil {
		return err
	}
	defer a.Close()
	switch command {
	case "lookup":
		p, err := coord.ParsePair(*value)
		if err != nil {
			return err
		}
		hits, err := a.Lookup(context.Background(), p.Latitude, p.Longitude)
		if err != nil {
			return err
		}
		return emit(hits)
	case "id":
		hits, err := a.FindID(*kind, *id)
		if err != nil {
			return err
		}
		var result []any
		for _, h := range hits {
			g, err := a.Geometry.Record(h.FeatureID)
			if err != nil {
				return err
			}
			p, err := g.InteriorPoint()
			if err != nil {
				return err
			}
			utm, err := coord.ToUTM(coord.Point{Latitude: p.Latitude, Longitude: p.Longitude})
			if err != nil {
				return err
			}
			result = append(result, map[string]any{"match": h, "representative": p, "utm": utm})
		}
		return emit(result)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
