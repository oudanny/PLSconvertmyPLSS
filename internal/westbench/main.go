// Command westbench compares a region atlas with its captured BLM source packs.
// All data is read locally. This is a reproducible research benchmark, not a
// production command or a claim that sampled points prove every possible query.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"

	"github.com/oudanny/PLSconvertmyPLSS/internal/regionatlas"
	"github.com/oudanny/PLSconvertmyPLSS/plss"
	_ "modernc.org/sqlite"
)

type source struct {
	state    string
	pack     *plss.Pack
	db       *sql.DB
	bounds   [4]float64
	count    uint32
	metadata plss.PackMetadata
}
type sample struct {
	plss.Point
	State string
}
type latency struct {
	Queries int     `json:"queries"`
	Mean    float64 `json:"mean_us"`
	Median  float64 `json:"median_us"`
	P95     float64 `json:"p95_us"`
	P99     float64 `json:"p99_us"`
	Bytes   float64 `json:"allocated_bytes_per_query"`
	Allocs  float64 `json:"allocations_per_query"`
}

func measure(n int, fn func(int) error) (latency, error) {
	return measureWithSetup(n, func() (func(int) error, func() error, error) { return fn, func() error { return nil }, nil })
}

// Each pass gets its own setup/cleanup. Callers can initialize indexes without
// carrying prepared polygons from timing into the allocation measurements.
func measureWithSetup(n int, setup func() (func(int) error, func() error, error)) (latency, error) {
	if n <= 0 {
		return latency{}, errors.New("measurement requires queries")
	}
	pass := func(body func(func(int) error) error) error {
		fn, cleanup, err := setup()
		if err != nil {
			return err
		}
		runErr := body(fn)
		return errors.Join(runErr, cleanup())
	}
	times := make([]int64, n)
	var elapsed int64
	if err := pass(func(fn func(int) error) error {
		runtime.GC()
		for i := 0; i < n; i++ {
			t := preciseNow()
			if err := fn(i); err != nil {
				return err
			}
			times[i] = preciseNow() - t
			elapsed += times[i]
		}
		return nil
	}); err != nil {
		return latency{}, err
	}
	// Allocation accounting is a separate untimed pass: timers, sample storage,
	// and percentile sorting are benchmark machinery, not query allocations.
	var before, after runtime.MemStats
	if err := pass(func(fn func(int) error) error {
		runtime.GC()
		runtime.ReadMemStats(&before)
		for i := 0; i < n; i++ {
			if err := fn(i); err != nil {
				return err
			}
		}
		runtime.ReadMemStats(&after)
		return nil
	}); err != nil {
		return latency{}, err
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	return latency{n, float64(elapsed) / float64(n) / 1000, float64(times[(n*50+99)/100-1]) / 1000, float64(times[(n*95+99)/100-1]) / 1000, float64(times[(n*99+99)/100-1]) / 1000, float64(after.TotalAlloc-before.TotalAlloc) / float64(n), float64(after.Mallocs-before.Mallocs) / float64(n)}, nil
}
func harmless(err error) error {
	if errors.Is(err, plss.ErrNotFound) {
		return nil
	}
	return err
}
func rank(s string) int {
	switch s {
	case "township":
		return 1
	case "section":
		return 2
	case "intersected":
		return 3
	}
	return 0
}
func actualKeys(hits []regionatlas.Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = identity(h.State, h.Layer, h.FeatureID, h.ID)
	}
	sort.Strings(out)
	return out
}
func openSource(path string) (source, error) {
	var s source
	var err error
	s.pack, err = plss.OpenPack(path)
	if err != nil {
		return s, err
	}
	m, err := s.pack.Metadata()
	if err != nil {
		s.pack.Close()
		return s, err
	}
	s.state = m.State
	s.metadata = m
	p, err := filepath.Abs(path)
	if err != nil {
		s.pack.Close()
		return s, err
	}
	p = filepath.ToSlash(p)
	if p[0] != '/' {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p, RawQuery: "mode=ro"}
	s.db, err = sql.Open("sqlite", u.String())
	if err != nil {
		s.pack.Close()
		return s, err
	}
	s.db.SetMaxOpenConns(1)
	err = s.db.QueryRow("SELECT min(min_lon),max(max_lon),min(min_lat),max(max_lat) FROM spatial").Scan(&s.bounds[0], &s.bounds[1], &s.bounds[2], &s.bounds[3])
	if err != nil {
		s.pack.Close()
		s.db.Close()
		return s, err
	}
	for _, n := range m.Counts {
		if n < 0 || uint64(s.count)+uint64(n) > 1<<32-1 {
			s.pack.Close()
			s.db.Close()
			return s, errors.New("invalid source count")
		}
		s.count += uint32(n)
	}
	if s.count == 0 {
		s.pack.Close()
		s.db.Close()
		return s, errors.New("benchmark requires nonempty spatial sources")
	}
	return s, nil
}
func geometry(s source, id uint32) (plss.Geometry, error) {
	var blob []byte
	var g plss.Geometry
	err := s.db.QueryRow("SELECT data FROM features WHERE id=?", id).Scan(&blob)
	if err != nil {
		return g, err
	}
	return decodeGeometry(blob)
}

func run() error {
	data := flag.String("data", "", "region or state atlas")
	dir := flag.String("source-dir", "data/west", "directory of state.sqlite source packs")
	nv := flag.String("nv-db", "data/nevada.sqlite", "Nevada source override")
	n := flag.Int("points-per-state", 1000, "deterministic spread queries per state")
	cap := flag.Int("cache", 0, "state reader cap, zero keeps all lazily opened states")
	flag.Parse()
	if *data == "" || *n < 10 || *n > 1000000 {
		return errors.New("provide --data and 10..1000000 points per state")
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := preciseNow()
	a, err := regionatlas.OpenWithOptions(*data, regionatlas.Options{MaxOpenShards: *cap})
	if err != nil {
		return err
	}
	openUS := float64(preciseNow()-start) / 1000
	runtime.ReadMemStats(&after)
	openHeap := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	defer a.Close()
	var sources []source
	defer func() {
		for _, s := range sources {
			s.pack.Close()
			s.db.Close()
		}
	}()
	for _, m := range a.Sources() {
		path := filepath.Join(*dir, strings.ToLower(m.State)+".sqlite")
		if m.State == "NV" && *nv != "" {
			path = *nv
		}
		s, err := openSource(path)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(s.metadata, m) {
			s.pack.Close()
			s.db.Close()
			return errors.New("source capture metadata disagrees with atlas provenance")
		}
		sources = append(sources, s)
	}
	rng := rand.New(rand.NewSource(20261028))
	var points []sample
	// Interleave states so this exercises the large region, not just repeated
	// local lookups. Bounding rectangles deliberately include uncovered points.
	for i := 0; i < *n; i++ {
		for _, s := range sources {
			b := s.bounds
			points = append(points, sample{plss.Point{Longitude: b[0] + rng.Float64()*(b[1]-b[0]), Latitude: b[2] + rng.Float64()*(b[3]-b[2])}, s.state})
		}
	}
	var boundaries []sample
	for _, s := range sources {
		for i := 0; i < 100; i++ {
			id := uint32(rng.Intn(int(s.count))) + 1
			g, err := geometry(s, id)
			if err != nil {
				return err
			}
			for _, p := range boundarySamples(g, rng) {
				boundaries = append(boundaries, sample{p, s.state})
			}
		}
	}
	ctx := context.Background()
	first := map[string]float64{}
	for _, s := range sources {
		p := points[0].Point
		for _, q := range points {
			if q.State == s.state {
				p = q.Point
				break
			}
		}
		// Each state gets a fresh reader; earlier overlapping state queries
		// must not silently pre-initialize its reader.
		fresh, e := regionatlas.OpenWithOptions(*data, regionatlas.Options{MaxOpenShards: *cap})
		if e != nil {
			return e
		}
		start = preciseNow()
		_, err := fresh.Lookup(ctx, p.Latitude, p.Longitude)
		first[s.state] = float64(preciseNow()-start) / 1000
		fresh.Close()
		if err = harmless(err); err != nil {
			return err
		}
	}
	if err = a.Verify(); err != nil {
		return err
	}
	hits := 0
	checks := append(append([]sample{}, points...), boundaries...)
	for i, p := range checks {
		actual, err := a.Lookup(ctx, p.Latitude, p.Longitude)
		if err = harmless(err); err != nil {
			return err
		}
		want, err := sourceMatches(ctx, sources, p.Point)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(actualKeys(actual), want) {
			return fmt.Errorf("query%d %.15g,%.15g: %v != %v", i, p.Latitude, p.Longitude, actualKeys(actual), want)
		}
		if len(actual) > 0 {
			hits++
		}
		if i%1000 == 0 {
			fmt.Fprintf(os.Stderr, "checked %d/%d queries\n", i, len(checks))
		}
	}
	// All selected identifiers are checked against the source's exact-ID index,
	// including duplicate records and special-survey IDs.
	reverseChecked := 0
	reverseSeen := map[string]bool{}
	check := func(kind, value string) error {
		if value == "" || reverseSeen[kind+"\x00"+value] {
			return nil
		}
		if err := checkReverse(ctx, a, sources, kind, value); err != nil {
			return err
		}
		reverseSeen[kind+"\x00"+value] = true
		reverseChecked++
		return nil
	}
	for _, s := range sources {
		for i := 0; i < 100; i++ {
			id := uint32(rng.Intn(int(s.count))) + 1
			var town, firstID, second, survey string
			if err = s.db.QueryRow("SELECT plssid,frstdivid,secdivid,survid FROM features WHERE id=?", id).Scan(&town, &firstID, &second, &survey); err != nil {
				return err
			}
			for _, q := range []reverseQuery{{"township", town}, {"section", firstID}, {"division", second}, {"survey", survey}} {
				if err := check(q.kind, q.value); err != nil {
					return err
				}
			}
		}
		queries, err := explicitReverseSamples(s)
		if err != nil {
			return err
		}
		for _, q := range queries {
			if err := check(q.kind, q.value); err != nil {
				return err
			}
		}
	}
	// These are the same independently verified points, but prepared geometry
	// from validation must not turn the first-pass metric into a repeated test.
	// Verify loads state indexes and touches component pages, never preparing
	// a polygon. A separate verified reader is used for each measured pass.
	firstSpread, err := measureWithSetup(len(points), func() (func(int) error, func() error, error) {
		fresh, err := regionatlas.OpenWithOptions(*data, regionatlas.Options{MaxOpenShards: *cap})
		if err != nil {
			return nil, nil, err
		}
		if err := fresh.Verify(); err != nil {
			fresh.Close()
			return nil, nil, err
		}
		return func(i int) error {
			p := points[i]
			_, err := fresh.Lookup(ctx, p.Latitude, p.Longitude)
			return harmless(err)
		}, fresh.Close, nil
	})
	if err != nil {
		return err
	}
	warm, err := measure(len(points), func(i int) error { p := points[i]; _, e := a.Lookup(ctx, p.Latitude, p.Longitude); return harmless(e) })
	if err != nil {
		return err
	}
	// Representative-point queries are real hits and rotate through all states.
	var representatives []sample
	for _, s := range sources {
		g, err := geometry(s, s.count/2+1)
		if err != nil {
			return err
		}
		p, err := g.InteriorPoint()
		if err != nil {
			return err
		}
		representatives = append(representatives, sample{p, s.state})
	}
	for _, p := range representatives {
		actual, err := a.Lookup(ctx, p.Latitude, p.Longitude)
		if err != nil {
			return fmt.Errorf("representative expected a real hit: %w", err)
		}
		want, err := sourceMatches(ctx, sources, p.Point)
		if err != nil {
			return err
		}
		if len(actual) == 0 || !reflect.DeepEqual(actualKeys(actual), want) {
			return errors.New("representative source rows differ")
		}
	}
	hot, err := measure(10000, func(i int) error {
		p := representatives[i%len(representatives)]
		_, e := a.Lookup(ctx, p.Latitude, p.Longitude)
		return harmless(e)
	})
	if err != nil {
		return err
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	info, err := os.Stat(*data)
	if err != nil {
		return err
	}
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	return e.Encode(map[string]any{"file_bytes": info.Size(), "go_version": runtime.Version(), "states": a.Sources(), "open_us": openUS, "open_go_heap_delta_bytes": openHeap, "first_query_us_by_state": first, "state_readers_open": a.OpenShards(), "state_cache_cap": *cap, "query_checks": len(checks), "queries_with_hits": hits, "reverse_id_checks": reverseChecked, "first_pass_spread_mixed_state": firstSpread, "spread_mixed_state": warm, "repeated_spread_mixed_state": warm, "repeated_state_representatives": hot, "note": "Mapped queries use warm OS pages. first_pass_spread_mixed_state uses a fresh reader separately for timing and allocation passes; Verify initializes indexes and warms file pages without preparing polygons. Prepared/geometry caches begin empty and may fill naturally within each pass. With a state cap below the state count, index evictions/reloads remain part of the measured workload. spread_mixed_state is the compatibility alias for repeated_spread_mixed_state: that reader has already queried the validated points and may reuse prepared geometry. Setup, verification, timers and cleanup are excluded from allocations; timing includes per-query timer overhead. Oracle compares state/layer/source-row-ID/published-ID multisets using source SQLite candidates and polygons. Reverse samples include bounded explicit survey and unlocated records. Each first-query-by-state measurement uses a fresh reader, excludes opening, and may initialize overlapping states; its OS cache state is uncontrolled. Go heap excludes mapped resident pages. These sampled checks do not prove every possible query."})
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
