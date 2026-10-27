// Command topolab audits every geometry in a v1 pack and builds isolated
// geometry-only components. Source pack and production code remain unchanged.
package main

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/oudanny/PLSconvertmyPLSS/internal/compactgeom"
	"github.com/oudanny/PLSconvertmyPLSS/plss"
	_ "modernc.org/sqlite"
)

type counts struct {
	Features       uint64 `json:"features"`
	Rings          uint64 `json:"rings"`
	Vertices       uint64 `json:"vertex_occurrences"`
	ClosedRings    uint64 `json:"bit_exact_closed_rings"`
	ExtraVertices  uint64 `json:"vertices_not_2d"`
	UniqueVertices uint64 `json:"unique_bit_exact_xy"`
	Edges          uint64 `json:"edge_occurrences"`
	ZeroEdges      uint64 `json:"zero_xy_edge_occurrences"`
	UniqueEdges    uint64 `json:"unique_undirected_xy_edges"`
	UniquePolygons uint64 `json:"unique_bit_exact_ordered_polygons"`
}
type vertexInfo struct {
	id   uint32
	mask byte
}
type audit struct {
	total      counts
	layers     map[int]*counts
	vertices   map[[2]uint64]vertexInfo
	edges      map[uint64]byte
	polygons   map[string]byte
	dimensions map[int]uint64
}

func newAudit() *audit {
	return &audit{layers: map[int]*counts{1: {}, 2: {}, 3: {}}, vertices: map[[2]uint64]vertexInfo{}, edges: map[uint64]byte{}, polygons: map[string]byte{}, dimensions: map[int]uint64{}}
}
func (a *audit) add(layer int, g plss.Geometry, canonical []byte) {
	l := a.layers[layer]
	l.Features++
	a.total.Features++
	mask := byte(1 << uint(layer))
	key := string(canonical)
	old, ok := a.polygons[key]
	if !ok {
		a.total.UniquePolygons++
	}
	if old&mask == 0 {
		l.UniquePolygons++
		a.polygons[key] = old | mask
	}
	for _, ring := range g.Rings {
		l.Rings++
		a.total.Rings++
		ids := make([]uint32, len(ring))
		closed := same(ring[0], ring[len(ring)-1])
		if closed {
			l.ClosedRings++
			a.total.ClosedRings++
		}
		for j, v := range ring {
			l.Vertices++
			a.total.Vertices++
			a.dimensions[len(v)]++
			if len(v) != 2 {
				l.ExtraVertices++
				a.total.ExtraVertices++
			}
			key := [2]uint64{math.Float64bits(v[0]), math.Float64bits(v[1])}
			entry, ok := a.vertices[key]
			if !ok {
				entry.id = uint32(len(a.vertices))
				a.total.UniqueVertices++
			}
			if entry.mask&mask == 0 {
				l.UniqueVertices++
				entry.mask |= mask
				a.vertices[key] = entry
			}
			ids[j] = entry.id
		}
		limit := len(ids)
		if closed {
			limit--
		}
		for j := 0; j < limit; j++ {
			x, y := ids[j], ids[(j+1)%len(ids)]
			l.Edges++
			a.total.Edges++
			if x == y {
				l.ZeroEdges++
				a.total.ZeroEdges++
			}
			if x > y {
				x, y = y, x
			}
			key := uint64(x)<<32 | uint64(y)
			old, ok := a.edges[key]
			if !ok {
				a.total.UniqueEdges++
			}
			if old&mask == 0 {
				l.UniqueEdges++
				a.edges[key] = old | mask
			}
		}
	}
}
func same(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i, x := range a {
		if math.Float64bits(x) != math.Float64bits(b[i]) {
			return false
		}
	}
	return true
}
func canonical(g plss.Geometry, decimals int) []byte {
	out := binary.AppendUvarint(nil, uint64(len(g.Rings)))
	scale := math.Pow10(decimals)
	for _, ring := range g.Rings {
		out = binary.AppendUvarint(out, uint64(len(ring)))
		for _, v := range ring {
			out = binary.AppendUvarint(out, uint64(len(v)))
			for axis, x := range v {
				if decimals != 0 && axis < 2 {
					x = float64(int32(math.Round(x*scale))) / scale
				}
				out = binary.LittleEndian.AppendUint64(out, math.Float64bits(x))
			}
		}
	}
	return out
}

type candidate struct {
	Name          string               `json:"name"`
	Path          string               `json:"path"`
	Metadata      compactgeom.Metadata `json:"metadata"`
	Verified      uint64               `json:"verified_records"`
	VerifySeconds float64              `json:"verify_seconds"`
	DecodeNS      float64              `json:"random_decode_ns"`
	DecodeBytes   float64              `json:"random_decode_alloc_bytes"`
	DecodeAllocs  float64              `json:"random_decode_allocs"`
	builder       *compactgeom.Builder
	hashes        [][32]byte
	decimals      int
}
type report struct {
	Source       string          `json:"source"`
	GoVersion    string          `json:"go_version"`
	BuildSeconds float64         `json:"build_seconds"`
	Total        counts          `json:"total"`
	Layers       map[int]*counts `json:"layers"`
	Dimensions   map[int]uint64  `json:"ordinate_count_histogram"`
	Candidates   []*candidate    `json:"candidates"`
}

func run(dbPath, outDir string, verify bool) (report, error) {
	start := time.Now()
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return report{}, err
	}
	uri := filepath.ToSlash(abs)
	if uri[0] != '/' {
		uri = "/" + uri
	}
	u := url.URL{Scheme: "file", Path: uri, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return report{}, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return report{}, err
	}
	candidates := []*candidate{}
	for _, spec := range []struct {
		name     string
		decimals int
	}{{"lossless", 0}, {"rounded-1e-7", 7}, {"rounded-1e-6", 6}} {
		path := filepath.Join(outDir, spec.name+".cgeom")
		builder, err := compactgeom.NewBuilder(path, spec.decimals)
		if err != nil {
			return report{}, err
		}
		defer builder.Close()
		candidates = append(candidates, &candidate{Name: spec.name, Path: path, builder: builder, decimals: spec.decimals})
	}
	rows, err := db.Query("SELECT id,layer,data FROM features ORDER BY id")
	if err != nil {
		return report{}, err
	}
	defer rows.Close()
	a := newAudit()
	var inflater io.ReadCloser
	var buffer bytes.Buffer
	lastLayer := 0
	for rows.Next() {
		var id uint32
		var layer int
		var compressed []byte
		if err := rows.Scan(&id, &layer, &compressed); err != nil {
			return report{}, err
		}
		if layer != lastLayer && lastLayer != 0 {
			encoded, _ := json.Marshal(a.layers[lastLayer])
			fmt.Fprintf(os.Stderr, "completed layer%d: %s\n", lastLayer, encoded)
		}
		lastLayer = layer
		if inflater == nil {
			inflater, err = zlib.NewReader(bytes.NewReader(compressed))
		} else {
			err = inflater.(zlib.Resetter).Reset(bytes.NewReader(compressed), nil)
		}
		if err != nil {
			return report{}, err
		}
		buffer.Reset()
		if _, err := io.Copy(&buffer, inflater); err != nil {
			return report{}, err
		}
		_ = inflater.Close()
		var f struct {
			Geometry plss.Geometry `json:"geometry"`
		}
		if err := json.Unmarshal(buffer.Bytes(), &f); err != nil {
			return report{}, err
		}
		raw := canonical(f.Geometry, 0)
		a.add(layer, f.Geometry, raw)
		for _, c := range candidates {
			if err := c.builder.Add(id, f.Geometry); err != nil {
				return report{}, err
			}
			if verify {
				canon := raw
				if c.decimals != 0 {
					canon = canonical(f.Geometry, c.decimals)
				}
				c.hashes = append(c.hashes, sha256.Sum256(canon))
			}
		}
		if id%100000 == 0 {
			var memory runtime.MemStats
			runtime.ReadMemStats(&memory)
			fmt.Fprintf(os.Stderr, "%d records: exactvertices=%d, uniqueedges=%d, heapseen=%.1fMB elapsed=%.1fs\n", id, a.total.UniqueVertices, a.total.UniqueEdges, float64(memory.HeapInuse)/1e6, time.Since(start).Seconds())
		}
	}
	if err := rows.Err(); err != nil {
		return report{}, err
	}
	rows.Close()
	encoded, _ := json.Marshal(a.layers[lastLayer])
	fmt.Fprintf(os.Stderr, "completed layer%d: %s\n", lastLayer, encoded)
	r := report{Source: dbPath, GoVersion: runtime.Version(), Total: a.total, Layers: a.layers, Dimensions: a.dimensions, Candidates: candidates}
	for _, c := range candidates {
		c.Metadata, err = c.builder.Finish()
		if err != nil {
			return r, err
		}
		fmt.Fprintf(os.Stderr, "built %s: %d bytes, %d vertices; collapsededges=%d zeroarearings=%d\n", c.Name, c.Metadata.Bytes, c.Metadata.Vertices, c.Metadata.CollapsedEdges, c.Metadata.CollapsedRings)
		c.builder = nil
	}
	r.BuildSeconds = time.Since(start).Seconds()
	a = nil
	runtime.GC()
	for _, c := range candidates {
		f, err := os.Open(c.Path)
		if err != nil {
			return r, err
		}
		reader, err := compactgeom.OpenReaderAt(f, int64(c.Metadata.Bytes))
		if err != nil {
			f.Close()
			return r, err
		}
		if verify {
			begin := time.Now()
			for id := uint32(1); id <= c.Metadata.Features; id++ {
				g, err := reader.Record(id)
				if err != nil {
					f.Close()
					return r, err
				}
				if sha256.Sum256(canonical(g, 0)) != c.hashes[id-1] {
					f.Close()
					return r, fmt.Errorf("%s feature%d reconstruction differs", c.Name, id)
				}
				c.Verified++
				if id%250000 == 0 {
					fmt.Fprintf(os.Stderr, "verified %s %d/%d\n", c.Name, id, c.Metadata.Features)
				}
			}
			c.VerifySeconds = time.Since(begin).Seconds()
			c.hashes = nil
			runtime.GC()
		}
		order := rand.New(rand.NewSource(20261027)).Perm(int(c.Metadata.Features))
		if len(order) > 10000 {
			order = order[:10000]
		}
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		begin := time.Now()
		for _, id := range order {
			if _, err := reader.Record(uint32(id + 1)); err != nil {
				f.Close()
				return r, err
			}
		}
		elapsed := time.Since(begin)
		runtime.ReadMemStats(&after)
		c.DecodeNS = float64(elapsed.Nanoseconds()) / float64(len(order))
		c.DecodeBytes = float64(after.TotalAlloc-before.TotalAlloc) / float64(len(order))
		c.DecodeAllocs = float64(after.Mallocs-before.Mallocs) / float64(len(order))
		f.Close()
		fmt.Fprintf(os.Stderr, "random decode %s: %.1fus %.0fB %.1fallocs\n", c.Name, c.DecodeNS/1000, c.DecodeBytes, c.DecodeAllocs)
	}
	return r, nil
}
func main() {
	db := flag.String("db", "data/nevada.sqlite", "source v1pack")
	out := flag.String("out", "data/topolab", "new experimental components")
	verify := flag.Bool("verify", true, "verify every reconstructed record against source-derived SHA256 ofcanonicalbits")
	flag.Parse()
	r, err := run(*db, *out, *verify)
	if err != nil {
		panic(err)
	}
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	if err := e.Encode(r); err != nil {
		panic(err)
	}
}
