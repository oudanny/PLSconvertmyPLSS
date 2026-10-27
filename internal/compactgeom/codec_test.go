package compactgeom

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

func equalGeometry(a, b plss.Geometry) bool {
	if len(a.Rings) != len(b.Rings) {
		return false
	}
	for i, r := range a.Rings {
		if len(r) != len(b.Rings[i]) {
			return false
		}
		for j, v := range r {
			if len(v) != len(b.Rings[i][j]) {
				return false
			}
			for k, x := range v {
				if math.Float64bits(x) != math.Float64bits(b.Rings[i][j][k]) {
					return false
				}
			}
		}
	}
	return true
}

func TestExactGeometryAndEmbedding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exact.cgeom")
	b, err := NewBuilder(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	g := plss.Geometry{Rings: [][][]float64{{{-120, 40, 17}, {math.Nextafter(-120, 0), 40, 18}, {math.Copysign(0, -1), 41}, {-120, 40, 17}}, {{1, 2}, {3, 4}, {5, 2}, {1, 2}}}}
	for i := 1; i <= 257; i++ {
		if err := b.Add(uint32(i), g); err != nil {
			t.Fatal(err)
		}
	}
	m, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if m.Features != 257 || m.Decimals != 0 || m.ExtraOrdinateVertices != 3*257 {
		t.Fatalf("badmeta %+v", m)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	embedded := append(make([]byte, 513), data...)
	section := io.NewSectionReader(bytes.NewReader(embedded), 513, int64(len(data)))
	r, err := OpenReaderAt(section, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint32{1, 128, 129, 256, 257} {
		got, err := r.Record(id)
		if err != nil || !equalGeometry(g, got) {
			t.Fatalf("feature%d changed bits: %+v,%v", id, got, err)
		}
	}
	mutable, err := r.Record(1)
	if err != nil {
		t.Fatal(err)
	}
	neighbor := mutable.Rings[1][1][0]
	mutable.Rings[1][0] = append(mutable.Rings[1][0], 999)
	if mutable.Rings[1][1][0] != neighbor {
		t.Error("appending an ordinate overwrote a neighboring vertex")
	}
	again, err := r.Record(1)
	if err != nil || !equalGeometry(g, again) {
		t.Error("caller mutation changed cached geometry")
	}
	for _, id := range []uint32{0, 258} {
		if _, err := r.Record(id); err == nil {
			t.Errorf("invalidID%d accepted", id)
		}
	}
}

func TestExplicitQuantizationTracksCollapse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rounded.cgeom")
	b, err := NewBuilder(path, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	g := plss.Geometry{Rings: [][][]float64{{{1, 2}, {1 + 1e-8, 2}, {1 + 1e-8, 2 + 1e-8}, {1, 2}}}}
	if err := b.Add(1, g); err != nil {
		t.Fatal(err)
	}
	m, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if m.Decimals != 7 || m.CollapsedEdges == 0 || m.CollapsedRings != 1 || m.MaxLatitudeError > 5.000001e-8 {
		t.Fatalf("quantization not explicit/audited: %+v", m)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r, err := OpenReaderAt(f, int64(m.Bytes))
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Record(1)
	if err != nil || got.Rings[0][1][0] != 1 || len(got.Rings[0]) != 4 {
		t.Fatalf("roundedgeometry %+v,%v", got, err)
	}
}

func TestCorruptionAndIncompleteFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partial.cgeom")
	b, err := NewBuilder(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = b.Close()
	data, _ := os.ReadFile(path)
	if _, err := OpenReaderAt(bytes.NewReader(data), int64(len(data))); err == nil {
		t.Error("accepted incomplete component")
	}
	if _, err := NewBuilder(path, 0); err == nil {
		t.Error("overwrote existing component")
	}
	if _, err := NewBuilder(filepath.Join(t.TempDir(), "bad"), 5); err == nil {
		t.Error("accepted unspecified quantization")
	}
}

func TestCacheOptionsConcurrentAndBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.cgeom")
	b, err := NewBuilder(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	geometry := func(id int) plss.Geometry {
		x := -120 + float64(id)*.001
		return plss.Geometry{Rings: [][][]float64{{{x, 40}, {x + .0005, 40}, {x + .0005, 40.0005}, {x, 40.0005}, {x, 40}}}}
	}
	for id := 1; id <= 1024; id++ {
		if err := b.Add(uint32(id), geometry(id)); err != nil {
			t.Fatal(err)
		}
	}
	m, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, options := range []ReaderOptions{{}, {VertexCachePages: 1, GeometryCacheBlocks: 1}, {PreloadVertices: true, GeometryCacheBlocks: 2}} {
		r, err := OpenReaderAtWithOptions(bytes.NewReader(data), int64(m.Bytes), options)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for worker := 0; worker < 8; worker++ {
			wg.Add(1)
			go func(worker int) {
				defer wg.Done()
				for n := 0; n < 256; n++ {
					id := (n*137+worker*79)%1024 + 1
					got, err := r.Record(uint32(id))
					if err != nil || !equalGeometry(geometry(id), got) {
						t.Errorf("concurrent/cache decode id%d: %v", id, err)
						return
					}
				}
			}(worker)
		}
		wg.Wait()
	}
	bad := append([]byte(nil), data...)
	binary.LittleEndian.PutUint64(bad[32:40], math.MaxUint64-100)
	if _, err := OpenReaderAt(bytes.NewReader(bad), int64(len(bad))); err == nil {
		t.Error("accepted overflowed section offset")
	}
}

func BenchmarkNevadaGeometry(b *testing.B) {
	path := os.Getenv("TOPO_GEOM_FILE")
	if path == "" {
		b.Skip("set TOPO_GEOM_FILE to an experimental geometry component")
	}
	f, err := os.Open(path)
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		b.Fatal(err)
	}
	for _, config := range []struct {
		name    string
		options ReaderOptions
		hot     bool
	}{
		{"bounded-random", ReaderOptions{VertexCachePages: 64, GeometryCacheBlocks: 16}, false},
		{"preloaded-vertices-random", ReaderOptions{PreloadVertices: true, GeometryCacheBlocks: 16}, false},
		{"bounded-hot16", ReaderOptions{VertexCachePages: 64, GeometryCacheBlocks: 16}, true},
		{"preloaded-hot16", ReaderOptions{PreloadVertices: true, GeometryCacheBlocks: 16}, true},
	} {
		b.Run(config.name, func(b *testing.B) {
			r, err := OpenReaderAtWithOptions(f, stat.Size(), config.options)
			if err != nil {
				b.Fatal(err)
			}
			count := r.Metadata().Features
			ids := make([]uint32, 10000)
			var state uint64 = 20261027
			for i := range ids {
				state = state*6364136223846793005 + 1
				ids[i] = uint32(state>>32)%count + 1
			}
			if config.hot {
				ids = ids[:16]
			}
			for _, id := range ids {
				if _, err := r.Record(id); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := r.Record(ids[i%len(ids)]); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			if config.options.PreloadVertices {
				b.ReportMetric(float64(r.Metadata().VertexBytes), "vertexRAM-bytes")
			}
			b.ReportMetric(float64(len(ids)), "locations")
		})
	}
}
