package compactgeom

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

func compressedFixture(t testing.TB, blockSize int, codecs ...VertexCodec) ([]byte, []plss.Geometry) {
	t.Helper()
	codec := VertexXORShuffleZlib
	if len(codecs) > 0 {
		codec = codecs[0]
	}
	path := filepath.Join(t.TempDir(), "vertices.cgeom")
	b, err := NewBuilderWithOptions(path, BuilderOptions{VertexCodec: codec, VertexBlockSize: blockSize})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var geometries []plss.Geometry
	for id := 0; id < 320; id++ {
		x := -120 + float64(id)*.001
		g := plss.Geometry{Rings: [][][]float64{{{x, 40}, {math.Nextafter(x, 0), 40, math.Float64frombits(0x7ff8000000000123)}, {x + .0005, 40.0005, math.Inf(1), math.Copysign(0, -1)}, {x, 40}}}}
		if id == 0 {
			g.Rings = append(g.Rings, [][]float64{{math.Copysign(0, -1), 0}, {math.SmallestNonzeroFloat64, 0}, {0, math.SmallestNonzeroFloat64}, {math.Copysign(0, -1), 0}})
		}
		if err := b.Add(uint32(id+1), g); err != nil {
			t.Fatal(err)
		}
		geometries = append(geometries, g)
	}
	m, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != 2 || m.Decimals != 0 || m.VertexCodec != codec || m.VertexBlockSize != uint32(blockSize) || m.RawVertexBytes != m.Vertices*16 || m.Bytes != headerSize+m.BlockBytes+m.VertexBytes+m.DirectoryBytes {
		t.Fatalf("metadata %+v", m)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data, geometries
}

func TestCompressedVerticesExactAndConcurrent(t *testing.T) {
	for _, spec := range []struct {
		size  int
		codec VertexCodec
	}{{128, VertexXORShuffleZlib}, {256, VertexXORShuffleZlib}, {128, VertexXORBitpack}, {256, VertexXORBitpack}} {
		t.Run(fmt.Sprintf("%d/%d", spec.codec, spec.size), func(t *testing.T) {
			data, geometries := compressedFixture(t, spec.size, spec.codec)
			framed := append(make([]byte, 37), data...)
			for _, options := range []ReaderOptions{{}, {VertexCacheBlocks: 1, GeometryCacheBlocks: 1}, {VertexCacheBlocks: 64, GeometryCacheBlocks: 4}, {PreloadVertices: true, GeometryCacheBlocks: 2}} {
				r, err := OpenReaderAtWithOptions(io.NewSectionReader(bytes.NewReader(framed), 37, int64(len(data))), int64(len(data)), options)
				if err != nil {
					t.Fatal(err)
				}
				for i, want := range geometries {
					got, err := r.Record(uint32(i + 1))
					if err != nil || !equalGeometry(got, want) {
						t.Fatalf("id%d changed bits: %v", i+1, err)
					}
				}
				var wg sync.WaitGroup
				for worker := range 8 {
					wg.Go(func() {
						for n := range 80 {
							id := (n*137 + worker*79) % len(geometries)
							g, err := r.Record(uint32(id + 1))
							if err != nil || !equalGeometry(g, geometries[id]) {
								t.Errorf("parallel id%d: %v", id+1, err)
								return
							}
							g.Rings[0][0][0] = 999
						}
					})
				}
				wg.Wait()
				if len(r.vertexBlocks.entries) > options.VertexCacheBlocks {
					t.Fatal("vertex cache exceeded capacity")
				}
				if options.PreloadVertices && len(r.vertexData) != int(r.meta.RawVertexBytes) {
					t.Fatal("incorrect preload size")
				}
			}
		})
	}
}

func TestCompressedVertexRepackPreservesRecords(t *testing.T) {
	data, want := compressedFixture(t, 128)
	for _, codec := range []VertexCodec{VertexRaw, VertexXORShuffleZlib, VertexXORBitpack} {
		path := filepath.Join(t.TempDir(), "repacked.cgeom")
		m, err := RepackVertices(path, bytes.NewReader(data), int64(len(data)), BuilderOptions{VertexCodec: codec})
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		oldEnd := binary.LittleEndian.Uint64(data[32:40])
		newEnd := binary.LittleEndian.Uint64(got[32:40])
		if oldEnd != newEnd || !bytes.Equal(data[headerSize:oldEnd], got[headerSize:newEnd]) {
			t.Fatal("repack changed record blocks")
		}
		r, err := OpenReaderAt(bytes.NewReader(got), int64(m.Bytes))
		if err != nil {
			t.Fatal(err)
		}
		for i, g := range want {
			actual, err := r.Record(uint32(i + 1))
			if err != nil || !equalGeometry(actual, g) {
				t.Fatalf("repacked id%d: %v", i+1, err)
			}
		}
		if _, err := RepackVertices(path, bytes.NewReader(data), int64(len(data)), BuilderOptions{}); err == nil {
			t.Fatal("overwrote existing file")
		}
	}
}

func TestCompressedVertexMalformedAndPoolRecovery(t *testing.T) {
	data, _ := compressedFixture(t, 128)
	directory := int(binary.LittleEndian.Uint64(data[120:128]))
	mutations := map[string]func([]byte){
		"codec":                  func(p []byte) { binary.LittleEndian.PutUint32(p[116:], 99) },
		"block size":             func(p []byte) { binary.LittleEndian.PutUint32(p[112:], 0) },
		"huge block":             func(p []byte) { binary.LittleEndian.PutUint32(p[112:], math.MaxUint32) },
		"directory overflow":     func(p []byte) { binary.LittleEndian.PutUint64(p[120:], math.MaxUint64) },
		"directory overlaps":     func(p []byte) { binary.LittleEndian.PutUint64(p[120:], headerSize) },
		"nonconsecutive block":   func(p []byte) { binary.LittleEndian.PutUint64(p[directory:], 0) },
		"raw length":             func(p []byte) { binary.LittleEndian.PutUint32(p[directory+12:], math.MaxUint32) },
		"compressed length":      func(p []byte) { binary.LittleEndian.PutUint32(p[directory+8:], math.MaxUint32) },
		"lossy mode":             func(p []byte) { binary.LittleEndian.PutUint32(p[12:], 7) },
		"reported error":         func(p []byte) { binary.LittleEndian.PutUint64(p[64:], math.Float64bits(1e-7)) },
		"invalid reported error": func(p []byte) { binary.LittleEndian.PutUint64(p[72:], math.Float64bits(math.NaN())) },
		"reported collapse":      func(p []byte) { binary.LittleEndian.PutUint64(p[80:], 1) },
		"vertex emptiness":       func(p []byte) { binary.LittleEndian.PutUint64(p[24:], 0) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			bad := bytes.Clone(data)
			mutate(bad)
			if _, err := OpenReaderAt(bytes.NewReader(bad), int64(len(bad))); err == nil {
				t.Fatal("accepted malformed vertex directory")
			}
		})
	}
	for _, cut := range []int{0, 127, len(data) - 1} {
		if _, err := OpenReaderAt(bytes.NewReader(data[:cut]), int64(cut)); err == nil {
			t.Fatal("accepted truncated component")
		}
	}
	for _, mode := range []string{"checksum", "header", "expansion", "short"} {
		t.Run(mode, func(t *testing.T) {
			r, err := OpenReaderAt(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			e := r.vertexEntries[0]
			bad := bytes.Clone(data)
			switch mode {
			case "checksum":
				bad[int(e.offset)+int(e.compressed)-1] ^= 1
			case "header":
				bad[e.offset] = 0
			case "expansion", "short":
				var compressed bytes.Buffer
				zw := zlib.NewWriter(&compressed)
				n := int(e.raw) + 1
				if mode == "short" {
					n = int(e.raw) - 1
				}
				zw.Write(make([]byte, n))
				zw.Close()
				copy(bad[e.offset:], compressed.Bytes())
				r.vertexEntries[0].compressed = uint32(compressed.Len())
			}
			r.r = bytes.NewReader(bad)
			if _, err := r.readVertexBlock(0); err == nil {
				t.Fatal("accepted corrupt compressed vertex block")
			}
			// A failed inflater must not poison another valid block on the same reader.
			r.r = bytes.NewReader(data)
			if _, err := r.readVertexBlock(1); err != nil {
				t.Fatalf("pool poisoned: %v", err)
			}
		})
	}
	for _, options := range []BuilderOptions{{VertexCodec: 99}, {VertexCodec: VertexXORShuffleZlib, Decimals: 7}, {VertexCodec: VertexXORShuffleZlib, VertexBlockSize: 127}, {VertexBlockSize: 256}} {
		if _, err := NewBuilderWithOptions(filepath.Join(t.TempDir(), "bad"), options); err == nil {
			t.Fatal("accepted invalid builder options")
		}
	}
	if _, err := OpenReaderAtWithOptions(bytes.NewReader(data), int64(len(data)), ReaderOptions{VertexCacheBlocks: -1}); err == nil {
		t.Fatal("accepted negative vertex cache")
	}
}

func TestVertexTransformAllBits(t *testing.T) {
	// Exercise arbitrary bit patterns without depending on float formatting.
	var state uint64 = 0x123456789abcdef
	for _, count := range []int{1, 127, 128, 255, 256} {
		raw := make([]byte, count*16)
		for i := range raw {
			state = state*6364136223846793005 + 1
			raw[i] = byte(state >> 32)
		}
		if !bytes.Equal(raw, unshuffleVertices(shuffleVertices(raw))) {
			t.Fatal("transform changed bits")
		}
	}
}

func TestPackedVertexWidthsAndMalformedData(t *testing.T) {
	var state uint64 = 0x123456789abcdef
	for width := 0; width <= 64; width++ {
		for _, count := range []int{1, 3, 127, 128, 255, 256} {
			raw := make([]byte, count*16)
			for i := 0; i < count*2; i++ {
				state = state*6364136223846793005 + 1
				value := state
				if width < 64 {
					value &= (uint64(1) << width) - 1
				}
				value ^= 0xbff0ff0011aabbcc
				binary.LittleEndian.PutUint64(raw[i*8:], value)
			}
			packed := packVertices(raw)
			if err := validatePackedVertices(packed, count); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < count; i++ {
				v := packedVertex(packed, count, i)
				if v.x != binary.LittleEndian.Uint64(raw[i*16:]) || v.y != binary.LittleEndian.Uint64(raw[i*16+8:]) {
					t.Fatalf("width%d count%d index%d changed bits", width, count, i)
				}
			}
			if err := validatePackedVertices(packed[:len(packed)-1], count); err == nil {
				t.Fatal("accepted truncated packed block")
			}
			bad := bytes.Clone(packed)
			bad[0] ^= 1
			if err := validatePackedVertices(bad, count); err == nil {
				t.Fatal("accepted bad checksum")
			}
		}
	}
	raw := make([]byte, 3*16)
	raw[16] = 1
	packed := packVertices(raw)
	bad := bytes.Clone(packed)
	bad[16] = 65
	if err := validatePackedVertices(bad, 3); err == nil {
		t.Fatal("accepted width65")
	}
	bad = bytes.Clone(packed)
	bad[18] |= 0x80
	binary.LittleEndian.PutUint32(bad[len(bad)-4:], crc32.Checksum(bad[:len(bad)-4], vertexCRC))
	if err := validatePackedVertices(bad, 3); err == nil {
		t.Fatal("accepted nonzero padding")
	}
	data, _ := compressedFixture(t, 128, VertexXORBitpack)
	r, err := OpenReaderAt(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	corrupt := bytes.Clone(data)
	corrupt[r.vertexEntries[0].offset] ^= 1
	r.r = bytes.NewReader(corrupt)
	if _, err := r.vertexBlock(0); err == nil {
		t.Fatal("accepted corrupt packed payload")
	}
	r.r = bytes.NewReader(data)
	if _, err := r.vertexBlock(0); err != nil {
		t.Fatalf("cached a failed decode: %v", err)
	}
}
