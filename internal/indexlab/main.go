// Command indexlab compares complete Nevada bounding-box indexes. It never
// modifies the source database and does not claim polygon-containment timings.
package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/oudanny/PLSconvertmyPLSS/internal/spatialindex"
	_ "modernc.org/sqlite"
)

type point struct{ x, y float64 }
type distribution struct {
	MedianUS, P95US, P99US, MaxUS, MeanUS float64
	AllocatedBytesPerQuery                float64 `json:",omitempty"`
	AllocationsPerQuery                   float64 `json:",omitempty"`
	CandidateAmplification                float64
}
type result struct {
	Name                          string
	FileBytes, ResidentArrayBytes int64
	BuildSeconds                  float64
	OpenSeconds                   float64 `json:",omitempty"`
	Query                         distribution
}
type report struct {
	Source, GoVersion, Notes                                 string
	Entries, RandomQueries, BoundaryQueries, TotalCandidates int
	SQL                                                      distribution
	Variants                                                 []result
}

func sortedUnique(m []spatialindex.Match) []spatialindex.Match {
	sort.Slice(m, func(i, j int) bool {
		if m[i].ID != m[j].ID {
			return m[i].ID < m[j].ID
		}
		return m[i].Layer < m[j].Layer
	})
	if len(m) == 0 {
		return m
	}
	n := 1
	for i := 1; i < len(m); i++ {
		if m[i] != m[n-1] {
			m[n] = m[i]
			n++
		}
	}
	return m[:n]
}
func summarize(ns []int64, bytes, allocs uint64) distribution {
	sort.Slice(ns, func(i, j int) bool { return ns[i] < ns[j] })
	var sum int64
	for _, n := range ns {
		sum += n
	}
	p := func(q float64) float64 { return float64(ns[int(float64(len(ns)-1)*q)]) / 1000 }
	return distribution{MedianUS: p(.5), P95US: p(.95), P99US: p(.99), MaxUS: p(1), MeanUS: float64(sum) / float64(len(ns)) / 1000, AllocatedBytesPerQuery: float64(bytes) / float64(len(ns)), AllocationsPerQuery: float64(allocs) / float64(len(ns))}
}

func load(path string) (*sql.DB, []spatialindex.Entry, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, nil, err
	}
	db.SetMaxOpenConns(1)
	rows, err := db.Query("SELECT min_lon,max_lon,min_lat,max_lat,feature_id,layer FROM spatial ORDER BY id")
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	defer rows.Close()
	entries := make([]spatialindex.Entry, 0, 1600000)
	for rows.Next() {
		var a, b, c, d float64
		var id uint64
		var layer uint8
		if err := rows.Scan(&a, &b, &c, &d, &id, &layer); err != nil {
			db.Close()
			return nil, nil, err
		}
		if id > math.MaxUint32 {
			return nil, nil, errors.New("feature ID exceeds uint32")
		}
		e := spatialindex.Entry{MinLon: float32(a), MaxLon: float32(b), MinLat: float32(c), MaxLat: float32(d), ID: uint32(id), Layer: layer}
		if float64(e.MinLon) != a || float64(e.MaxLon) != b || float64(e.MinLat) != c || float64(e.MaxLat) != d {
			return nil, nil, errors.New("source bbox not exactly representable as float32")
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		db.Close()
		return nil, nil, err
	}
	return db, entries, nil
}

type grid struct {
	entries       []spatialindex.Entry
	offsets, refs []uint32
	cell, x0, y0  float64
	nx, ny        int
}

func newGrid(entries []spatialindex.Entry, cell float64) (*grid, error) {
	if len(entries) == 0 || cell <= 0 {
		return nil, errors.New("invalid grid inputs")
	}
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, e := range entries {
		minX = min(minX, float64(e.MinLon))
		maxX = max(maxX, float64(e.MaxLon))
		minY = min(minY, float64(e.MinLat))
		maxY = max(maxY, float64(e.MaxLat))
	}
	g := &grid{entries: entries, cell: cell, x0: math.Floor(minX/cell) * cell, y0: math.Floor(minY/cell) * cell}
	g.nx = int(math.Floor((maxX-g.x0)/cell)) + 1
	g.ny = int(math.Floor((maxY-g.y0)/cell)) + 1
	if int64(g.nx)*int64(g.ny) > 10000000 {
		return nil, errors.New("grid exceeds10million cells")
	}
	g.offsets = make([]uint32, g.nx*g.ny+1)
	visit := func(e spatialindex.Entry, fn func(int)) {
		x1 := int(math.Floor((float64(e.MinLon) - g.x0) / cell))
		x2 := int(math.Floor((float64(e.MaxLon) - g.x0) / cell))
		y1 := int(math.Floor((float64(e.MinLat) - g.y0) / cell))
		y2 := int(math.Floor((float64(e.MaxLat) - g.y0) / cell))
		for y := y1; y <= y2; y++ {
			for x := x1; x <= x2; x++ {
				fn(y*g.nx + x)
			}
		}
	}
	var total uint64
	for _, e := range entries {
		visit(e, func(c int) { g.offsets[c+1]++; total++ })
	}
	if total > math.MaxUint32 {
		return nil, errors.New("grid reference list exceedsuint32")
	}
	for i := 1; i < len(g.offsets); i++ {
		g.offsets[i] += g.offsets[i-1]
	}
	g.refs = make([]uint32, int(total))
	cursor := append([]uint32(nil), g.offsets[:len(g.offsets)-1]...)
	for i, e := range entries {
		visit(e, func(c int) { g.refs[cursor[c]] = uint32(i); cursor[c]++ })
	}
	return g, nil
}
func (g *grid) search(x, y float64) []spatialindex.Match {
	ix := int(math.Floor((x - g.x0) / g.cell))
	iy := int(math.Floor((y - g.y0) / g.cell))
	if ix < 0 || iy < 0 || ix >= g.nx || iy >= g.ny {
		return nil
	}
	c := iy*g.nx + ix
	var out []spatialindex.Match
	for _, id := range g.refs[g.offsets[c]:g.offsets[c+1]] {
		e := g.entries[id]
		if float64(e.MinLon) <= x && float64(e.MaxLon) >= x && float64(e.MinLat) <= y && float64(e.MaxLat) >= y {
			out = append(out, spatialindex.Match{ID: e.ID, Layer: e.Layer})
		}
	}
	return sortedUnique(out)
}
func (g *grid) bytes() int64 {
	return 64 + int64(len(g.entries))*21 + int64(len(g.offsets)+len(g.refs))*4
}
func (g *grid) memory() int64 { return int64(cap(g.entries))*24 + int64(cap(g.offsets)+cap(g.refs))*4 }
func (g *grid) write(w io.Writer) error {
	bw := bufio.NewWriterSize(w, 64<<10)
	var h [64]byte
	copy(h[:8], "PLSSGRD1")
	binary.LittleEndian.PutUint32(h[8:], 1)
	binary.LittleEndian.PutUint64(h[16:], uint64(len(g.entries)))
	binary.LittleEndian.PutUint64(h[24:], math.Float64bits(g.cell))
	binary.LittleEndian.PutUint64(h[32:], math.Float64bits(g.x0))
	binary.LittleEndian.PutUint64(h[40:], math.Float64bits(g.y0))
	binary.LittleEndian.PutUint32(h[48:], uint32(g.nx))
	binary.LittleEndian.PutUint32(h[52:], uint32(g.ny))
	binary.LittleEndian.PutUint64(h[56:], uint64(len(g.refs)))
	if _, err := bw.Write(h[:]); err != nil {
		return err
	}
	var b [20]byte
	for _, e := range g.entries {
		for i, f := range []float32{e.MinLon, e.MaxLon, e.MinLat, e.MaxLat} {
			binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
		}
		binary.LittleEndian.PutUint32(b[16:], e.ID)
		if _, err := bw.Write(b[:]); err != nil {
			return err
		}
	}
	for _, e := range g.entries {
		if err := bw.WriteByte(e.Layer); err != nil {
			return err
		}
	}
	for _, list := range [][]uint32{g.offsets, g.refs} {
		for _, v := range list {
			binary.LittleEndian.PutUint32(b[:4], v)
			if _, err := bw.Write(b[:4]); err != nil {
				return err
			}
		}
	}
	return bw.Flush()
}

func benchmark(points []point, want [][]spatialindex.Match, fn func(float64, float64) []spatialindex.Match, allowExtra bool) (distribution, error) {
	var originals, actual int
	for i, p := range points {
		got := fn(p.x, p.y)
		originals += len(want[i])
		actual += len(got)
		matches := reflect.DeepEqual(got, want[i])
		if allowExtra {
			matches = true
			for _, expected := range want[i] {
				found := false
				for _, candidate := range got {
					if candidate == expected {
						found = true
						break
					}
				}
				if !found {
					matches = false
					break
				}
			}
		}
		if !matches {
			return distribution{}, fmt.Errorf("candidate mismatch at point%d (%g,%g): got%v want%v", i, p.x, p.y, got, want[i])
		}
	}
	durations := make([]int64, 0, len(points)*3)
	for repeat := 0; repeat < 3; repeat++ {
		for _, p := range points {
			start := preciseNow()
			_ = fn(p.x, p.y)
			durations = append(durations, preciseNow()-start)
		}
	}
	// Measure allocations separately so Windows timing syscalls and samples
	// are excluded from the index's own allocation totals.
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for repeat := 0; repeat < 3; repeat++ {
		for _, p := range points {
			_ = fn(p.x, p.y)
		}
	}
	runtime.ReadMemStats(&after)
	stats := summarize(durations, after.TotalAlloc-before.TotalAlloc, after.Mallocs-before.Mallocs)
	if originals > 0 {
		stats.CandidateAmplification = float64(actual) / float64(originals)
	}
	return stats, nil
}

func run() error {
	dbPath := flag.String("db", "data/nevada.sqlite", "read-only source pack")
	outDir := flag.String("out", "internal/indexlab/out", "generated index files")
	nq := flag.Int("queries", 10000, "deterministic spread points")
	nb := flag.Int("boundary", 1000, "existing bbox boundary samples")
	cells := flag.String("cells", "0.01,0.02,0.05", "uniform grid cell widths in degrees")
	flag.Parse()
	if *nq < 1 || *nb < 0 {
		return errors.New("invalid sample counts")
	}
	if err := os.MkdirAll(*outDir, 0755); err != nil {
		return err
	}
	db, entries, err := load(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	fmt.Fprintf(os.Stderr, "loaded %d full-precision source bbox entries\n", len(entries))
	rng := rand.New(rand.NewSource(20261027))
	points := make([]point, 0, *nq+*nb)
	for i := 0; i < *nq; i++ {
		points = append(points, point{-120.1 + rng.Float64()*6.2, 34.9 + rng.Float64()*7.2})
	}
	for i := 0; i < *nb; i++ {
		e := entries[rng.Intn(len(entries))]
		p := point{float64(e.MinLon), float64(e.MaxLat)}
		switch i % 4 {
		case 1:
			p = point{float64(e.MaxLon), float64(e.MinLat)}
		case 2:
			p = point{float64(e.MinLon), (float64(e.MinLat) + float64(e.MaxLat)) / 2}
		case 3:
			p = point{float64(e.MaxLon), (float64(e.MinLat) + float64(e.MaxLat)) / 2}
		}
		points = append(points, p)
	}
	want := make([][]spatialindex.Match, len(points))
	sqlTimes := make([]int64, len(points))
	stmt, err := db.Prepare("SELECT feature_id,layer FROM spatial WHERE min_lon<=? AND max_lon>=? AND min_lat<=? AND max_lat>=?")
	if err != nil {
		return err
	}
	defer stmt.Close()
	r := report{Source: *dbPath, GoVersion: runtime.Version(), Notes: "Complete source bbox indexes. Full/grid variants retain source float32 boxes exactly; quantized variants conservatively widen leaves and require no missed SQLite candidates. All-layer candidates only; excludes geometry/attributes/containment. 10000 seeded spread +1000 bbox boundary points by default. Custom queries use three warmed passes; Windows QueryPerformanceCounter timings include timestamp overhead. Index allocations use a separate untimed pass; SQLite allocation counts not measured. Resident array capacity excludes benchmark fixtures and transient builder memory. Open timings are warm-file; grid Open was not measured.", Entries: len(entries), RandomQueries: *nq, BoundaryQueries: *nb}
	for i, p := range points {
		start := preciseNow()
		rows, err := stmt.QueryContext(context.Background(), p.x, p.x, p.y, p.y)
		if err != nil {
			return err
		}
		var matches []spatialindex.Match
		for rows.Next() {
			var m spatialindex.Match
			if err := rows.Scan(&m.ID, &m.Layer); err != nil {
				rows.Close()
				return err
			}
			matches = append(matches, m)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		want[i] = sortedUnique(matches)
		r.TotalCandidates += len(want[i])
		sqlTimes[i] = preciseNow() - start
	}
	r.SQL = summarize(sqlTimes, 0, 0)
	r.SQL.CandidateAmplification = 1
	fmt.Fprintln(os.Stderr, "SQLite candidate oracle complete")
	for _, fanout := range []int{8, 16, 32} {
		start := time.Now()
		idx, err := spatialindex.Build(entries, fanout)
		if err != nil {
			return err
		}
		build := time.Since(start).Seconds()
		path := filepath.Join(*outDir, fmt.Sprintf("nevada-rtree%d.idx", fanout))
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		n, err := idx.WriteTo(f)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if n != idx.ByteSize() {
			return errors.New("index byte size mismatch")
		}
		// Measure opening the actual serialized file. The writer has just warmed
		// these OS pages; this is not a cold-disk startup measurement.
		start = time.Now()
		f, err = os.Open(path)
		if err != nil {
			return err
		}
		loaded, err := spatialindex.Open(f, 0, n)
		closeErr = f.Close()
		openSeconds := time.Since(start).Seconds()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		stats, err := benchmark(points, want, loaded.Search, false)
		if err != nil {
			return err
		}
		r.Variants = append(r.Variants, result{Name: fmt.Sprintf("packed-rtree%d", fanout), FileBytes: n, ResidentArrayBytes: loaded.MemoryBytes(), BuildSeconds: build, OpenSeconds: openSeconds, Query: stats})
		fmt.Fprintf(os.Stderr, "packed%d: %d bytes,p50 %.3fus,p95 %.3fus,p99 %.3fus\n", fanout, n, stats.MedianUS, stats.P95US, stats.P99US)
	}
	full, err := spatialindex.Build(entries, 16)
	if err != nil {
		return err
	}
	for _, bits := range []int{8, 16} {
		start := time.Now()
		idx, err := full.Quantize(bits)
		if err != nil {
			return err
		}
		build := time.Since(start).Seconds()
		path := filepath.Join(*outDir, fmt.Sprintf("nevada-rtree16-q%d.idx", bits))
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		n, err := idx.WriteTo(f)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		start = time.Now()
		f, err = os.Open(path)
		if err != nil {
			return err
		}
		loaded, err := spatialindex.Open(f, 0, n)
		closeErr = f.Close()
		openSeconds := time.Since(start).Seconds()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		stats, err := benchmark(points, want, loaded.Search, true)
		if err != nil {
			return err
		}
		r.Variants = append(r.Variants, result{Name: fmt.Sprintf("packed-rtree16-q%d", bits), FileBytes: n, ResidentArrayBytes: loaded.MemoryBytes(), BuildSeconds: build, OpenSeconds: openSeconds, Query: stats})
		fmt.Fprintf(os.Stderr, "packedq%d: %d bytes,p50 %.3fus,p95 %.3fus,p99 %.3fus,candidate ratio%.4f\n", bits, n, stats.MedianUS, stats.P95US, stats.P99US, stats.CandidateAmplification)
	}
	for _, word := range strings.Split(*cells, ",") {
		cell, err := strconv.ParseFloat(strings.TrimSpace(word), 64)
		if err != nil {
			return err
		}
		start := time.Now()
		g, err := newGrid(entries, cell)
		if err != nil {
			return err
		}
		build := time.Since(start).Seconds()
		path := filepath.Join(*outDir, fmt.Sprintf("nevada-grid-%g.idx", cell))
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		err = g.write(f)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if info.Size() != g.bytes() {
			return errors.New("grid byte size mismatch")
		}
		stats, err := benchmark(points, want, g.search, false)
		if err != nil {
			return err
		}
		r.Variants = append(r.Variants, result{Name: fmt.Sprintf("uniform-grid-%g", cell), FileBytes: info.Size(), ResidentArrayBytes: g.memory(), BuildSeconds: build, Query: stats})
		fmt.Fprintf(os.Stderr, "grid%g: %d bytes,p50 %.3fus,p95 %.3fus,p99 %.3fus\n", cell, info.Size(), stats.MedianUS, stats.P95US, stats.P99US)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
