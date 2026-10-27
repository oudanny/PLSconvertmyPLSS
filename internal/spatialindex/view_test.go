package spatialindex

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"os"
	"reflect"
	"sync"
	"testing"
)

func viewFixture(t testing.TB, bits int) (*Index, []byte) {
	t.Helper()
	rng := rand.New(rand.NewSource(27027))
	entries := make([]Entry, 1024)
	for i := range entries {
		x, y := float32(-120+rng.Float64()*6), float32(35+rng.Float64()*7)
		entries[i] = Entry{x, x + float32(rng.Float64()*.4), y, y + float32(rng.Float64()*.4), uint32(i + 1), uint8(i%3 + 1)}
	}
	entries = append(entries,
		Entry{179, 180, 50, 51, 1025, 3}, Entry{-180, -179, 50, 51, 1025, 3},
		Entry{0, 0, 0, 0, 1026, 2}, Entry{-120, -119, 35, 36, 1027, 1}, Entry{-120, -119, 35, 36, 1027, 1})
	x, err := Build(entries, 16)
	if err != nil {
		t.Fatal(err)
	}
	if bits != 0 {
		x, err = x.Quantize(bits)
		if err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	if _, err := x.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return x, buf.Bytes()
}

func TestViewMatchesIndex(t *testing.T) {
	for _, bits := range []int{0, 8, 16} {
		t.Run(fmt.Sprint(bits), func(t *testing.T) {
			x, data := viewFixture(t, bits)
			// Embedding requires only slicing to the exact component boundaries.
			container := append([]byte("container"), data...)
			container = append(container, []byte("footer")...)
			component := container[9 : 9+len(data)]
			v, err := OpenView(component)
			if err != nil {
				t.Fatal(err)
			}
			if &v.records[0] != &component[headerSize] || &v.layers[0] != &component[headerSize+len(x.layers)*x.recordSize()] {
				t.Fatal("record or layer bytes were copied")
			}
			if v.Len() != x.Len() || v.ByteSize() != x.ByteSize() || v.MemoryBytes() != int64(len(x.nodes))*24 {
				t.Fatal("size accounting differs")
			}
			if err := v.Validate(); err != nil {
				t.Fatal(err)
			}
			if err := v.ValidateCoverage(1027, func(id uint32) uint8 {
				if id == 1025 {
					return 3
				}
				if id == 1026 {
					return 2
				}
				return uint8((id-1)%3 + 1)
			}); err != nil {
				t.Fatal(err)
			}
			rng := rand.New(rand.NewSource(27))
			points := [][2]float64{{180, 50.5}, {-180, 50.5}, {0, 0}, {-120, 35}, {181, 0}, {0, 91}, {math.NaN(), 0}, {0, math.Inf(1)}}
			for range 1000 {
				points = append(points, [2]float64{-120 + rng.Float64()*7, 35 + rng.Float64()*8})
			}
			for _, n := range x.nodes {
				points = append(points, [2]float64{float64(n.minX), float64(n.minY)}, [2]float64{float64(n.maxX), float64(n.maxY)})
			}
			for _, p := range points {
				for layer := uint8(0); layer <= 4; layer++ {
					got, err := v.SearchLayer(p[0], p[1], layer)
					want := x.SearchLayer(p[0], p[1], layer)
					if err != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("point%v layer%d: got%v err%v want%v", p, layer, got, err, want)
					}
				}
			}
			prefix := []Match{{99999, 1}}
			got, err := v.AppendSearch(prefix, -119.5, 35.5, 0)
			want := x.AppendSearch([]Match{{99999, 1}}, -119.5, 35.5, 0)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("append: got%v err%v want%v", got, err, want)
			}
		})
	}
}

func TestViewEmptyAndConcurrentSearch(t *testing.T) {
	empty, _ := Build(nil, 16)
	var b bytes.Buffer
	empty.WriteTo(&b)
	v, err := OpenView(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := v.Search(0, 0); err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
	x, data := viewFixture(t, 8)
	v, err = OpenView(data)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Go(func() {
			rng := rand.New(rand.NewSource(int64(worker)))
			for range 500 {
				lon, lat := -120+rng.Float64()*7, 35+rng.Float64()*8
				want := x.Search(lon, lat)
				got, err := v.Search(lon, lat)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Errorf("concurrent query: got%v err%v want%v", got, err, want)
					return
				}
				if len(got) != 0 {
					got[0] = Match{99999, 99}
				}
			}
		})
	}
	wg.Wait()
}

func TestViewAcceptsPackedTreeSizes(t *testing.T) {
	for _, fanout := range []int{2, 3, 16, 256} {
		for _, count := range []int{1, fanout, fanout + 1, fanout*fanout + 1} {
			entries := make([]Entry, count)
			for i := range entries {
				entries[i] = Entry{0, 1, 0, 1, uint32(i + 1), uint8(i%3 + 1)}
			}
			x, err := Build(entries, fanout)
			if err != nil {
				t.Fatal(err)
			}
			var data bytes.Buffer
			if _, err := x.WriteTo(&data); err != nil {
				t.Fatal(err)
			}
			v, err := OpenView(data.Bytes())
			if err != nil {
				t.Fatalf("fanout%d count%d: %v", fanout, count, err)
			}
			got, err := v.Search(.5, .5)
			if err != nil || len(got) != count {
				t.Fatalf("fanout%d count%d: got%d err%v", fanout, count, len(got), err)
			}
		}
	}
}

func TestViewRejectsMalformedStructure(t *testing.T) {
	x, original := viewFixture(t, 8)
	nodes := headerSize + len(x.layers)*(x.recordSize()+1)
	for _, n := range []int{0, 1, 63, 64, len(original) - 1} {
		if _, err := OpenView(original[:n]); err == nil {
			t.Fatalf("accepted truncation to%d", n)
		}
	}
	mutations := map[string]func([]byte){
		"magic":               func(p []byte) { p[0] = 'X' },
		"version":             func(p []byte) { binary.LittleEndian.PutUint32(p[8:], 99) },
		"quant bits":          func(p []byte) { binary.LittleEndian.PutUint32(p[36:], 7) },
		"count overflow":      func(p []byte) { binary.LittleEndian.PutUint64(p[16:], math.MaxUint64) },
		"node count overflow": func(p []byte) { binary.LittleEndian.PutUint64(p[24:], math.MaxUint64) },
		"root":                func(p []byte) { binary.LittleEndian.PutUint32(p[32:], 0) },
		"fanout":              func(p []byte) { binary.LittleEndian.PutUint32(p[12:], 1) },
		"layer":               func(p []byte) { p[headerSize+len(x.layers)*x.recordSize()] = 4 },
		"node nan":            func(p []byte) { binary.LittleEndian.PutUint32(p[nodes:], math.Float32bits(float32(math.NaN()))) },
		"node count zero":     func(p []byte) { binary.LittleEndian.PutUint32(p[nodes+20:], leafFlag|7<<16) },
		"node flags":          func(p []byte) { binary.LittleEndian.PutUint32(p[nodes+20:], leafFlag|1<<24|7<<16|16) },
		"node mask":           func(p []byte) { binary.LittleEndian.PutUint32(p[nodes+20:], leafFlag|16) },
		"leaf overflow":       func(p []byte) { binary.LittleEndian.PutUint32(p[nodes+16:], math.MaxUint32) },
		"leaf overlaps":       func(p []byte) { binary.LittleEndian.PutUint32(p[nodes+24+16:], 0) },
		"child self reference": func(p []byte) {
			last := nodes + (len(x.nodes)-1)*24
			binary.LittleEndian.PutUint32(p[last+16:], uint32(len(x.nodes)-1))
		},
		"child duplicate": func(p []byte) {
			last := nodes + (len(x.nodes)-1)*24
			binary.LittleEndian.PutUint32(p[last+16:], 0)
		},
		"parent bounds": func(p []byte) {
			last := nodes + (len(x.nodes)-1)*24
			binary.LittleEndian.PutUint32(p[last:], math.Float32bits(-179))
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			data := bytes.Clone(original)
			mutate(data)
			if _, err := OpenView(data); err == nil {
				t.Fatal("accepted malformed structure")
			}
		})
	}
	if _, err := OpenView(append(bytes.Clone(original), 0)); err == nil {
		t.Fatal("accepted extra data outside component")
	}
}

func TestViewValidatesLeafBoundsLazily(t *testing.T) {
	for _, bits := range []int{0, 8, 16} {
		t.Run(fmt.Sprint(bits), func(t *testing.T) {
			x, data := viewFixture(t, bits)
			switch bits {
			case 0:
				binary.LittleEndian.PutUint32(data[headerSize:], math.Float32bits(float32(math.NaN())))
			case 8:
				data[headerSize], data[headerSize+1] = 255, 0
			case 16:
				binary.LittleEndian.PutUint16(data[headerSize:], 65535)
				binary.LittleEndian.PutUint16(data[headerSize+2:], 0)
			}
			v, err := OpenView(data)
			if err != nil {
				t.Fatalf("open unexpectedly read corrupt record: %v", err)
			}
			if _, err := v.Search(100, -50); err != nil {
				t.Fatalf("unvisited leaf was decoded: %v", err)
			}
			n := x.nodes[0]
			lon, lat := (float64(n.minX)+float64(n.maxX))/2, (float64(n.minY)+float64(n.maxY))/2
			prefix := []Match{{99999, 1}}
			got, err := v.AppendSearch(prefix, lon, lat, 0)
			if err == nil || !reflect.DeepEqual(got, prefix) {
				t.Fatalf("visited invalid leaf: %v %v", got, err)
			}
			if err := v.Validate(); err == nil {
				t.Fatal("full validation accepted corrupt leaf")
			}
		})
	}
	_, data := viewFixture(t, 0)
	// A valid but changed leaf box must also disagree with its parent union.
	binary.LittleEndian.PutUint32(data[headerSize:], math.Float32bits(-180))
	v, err := OpenView(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Validate(); err == nil {
		t.Fatal("accepted inconsistent leaf union")
	}
	_, data = viewFixture(t, 8)
	binary.LittleEndian.PutUint32(data[headerSize+8-4:], 0)
	v, err = OpenView(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.ValidateCoverage(1027, func(uint32) uint8 { return 1 }); err == nil {
		t.Fatal("accepted bad feature coverage")
	}
}

// PLSS_NEVADA_INDEX opts in to tests and benchmarks using the actual index.
// Loading fixture bytes happens outside measured OpenView time; this is a
// warm-page constructor benchmark, not a filesystem or cold mmap benchmark.
func nevadaViewFixture(t testing.TB) ([]byte, *Index, [][2]float64) {
	t.Helper()
	path := os.Getenv("PLSS_NEVADA_INDEX")
	if path == "" {
		t.Skip("set PLSS_NEVADA_INDEX to a serialized Nevada index")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	x, err := Open(bytes.NewReader(data), 0, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(20261027))
	points := make([][2]float64, 0, 11000)
	for range 10000 {
		points = append(points, [2]float64{-120 + rng.Float64()*6, 35 + rng.Float64()*7})
	}
	for i := range 1000 {
		n := x.nodes[i*len(x.nodes)/1000]
		points = append(points, [2]float64{float64(n.minX), float64(n.maxY)})
	}
	return data, x, points
}

func TestNevadaViewParity(t *testing.T) {
	data, x, points := nevadaViewFixture(t)
	v, err := OpenView(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range points {
		for layer := uint8(0); layer <= 3; layer++ {
			got, err := v.SearchLayer(p[0], p[1], layer)
			want := x.SearchLayer(p[0], p[1], layer)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("point%v layer%d got%v err%v want%v", p, layer, got, err, want)
			}
		}
	}
	t.Logf("%d points x 4 layers agree; owned arrays %d bytes versus loaded index %d bytes", len(points), v.MemoryBytes(), x.MemoryBytes())
}

var viewOpenBenchmarkSink *View
var indexOpenBenchmarkSink *Index
var viewQueryBenchmarkSink []Match

func BenchmarkNevadaView(b *testing.B) {
	data, x, points := nevadaViewFixture(b)
	v, err := OpenView(data)
	if err != nil {
		b.Fatal(err)
	}
	b.Run("OpenViewWarmBytes", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			opened, err := OpenView(data)
			if err != nil {
				b.Fatal(err)
			}
			viewOpenBenchmarkSink = opened
		}
	})
	b.Run("OpenLoadedWarmBytes", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			opened, err := Open(bytes.NewReader(data), 0, int64(len(data)))
			if err != nil {
				b.Fatal(err)
			}
			indexOpenBenchmarkSink = opened
		}
	})
	b.Run("QueryView", func(b *testing.B) {
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			p := points[i%len(points)]
			matches, err := v.Search(p[0], p[1])
			if err != nil {
				b.Fatal(err)
			}
			viewQueryBenchmarkSink = matches
			i++
		}
	})
	b.Run("QueryLoaded", func(b *testing.B) {
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			p := points[i%len(points)]
			viewQueryBenchmarkSink = x.Search(p[0], p[1])
			i++
		}
	})
}
