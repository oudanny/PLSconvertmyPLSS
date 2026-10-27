// Command geomlab builds and verifies optional lossless vertex compression.
// Inputs and outputs are standalone geometry components, never an atlas.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/oudanny/PLSconvertmyPLSS/internal/compactgeom"
	"github.com/oudanny/PLSconvertmyPLSS/internal/mappedfile"
	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

type timing struct {
	Mode           string  `json:"mode"`
	NS             float64 `json:"ns_per_record"`
	AllocatedBytes float64 `json:"allocated_bytes_per_record"`
	Allocations    float64 `json:"allocations_per_record"`
}
type result struct {
	Path          string               `json:"path"`
	Metadata      compactgeom.Metadata `json:"metadata"`
	BuildSeconds  float64              `json:"repack_seconds"`
	Verified      uint32               `json:"verified_records"`
	VerifySeconds float64              `json:"verify_seconds"`
	Timings       []timing             `json:"warm_os_cache_median_of_three_timings"`
}

func equal(a, b plss.Geometry) bool {
	if len(a.Rings) != len(b.Rings) {
		return false
	}
	for i, ring := range a.Rings {
		if len(ring) != len(b.Rings[i]) {
			return false
		}
		for j, v := range ring {
			if len(v) != len(b.Rings[i][j]) {
				return false
			}
			for axis, n := range v {
				if math.Float64bits(n) != math.Float64bits(b.Rings[i][j][axis]) {
					return false
				}
			}
		}
	}
	return true
}

func measure(data []byte, mode string, options compactgeom.ReaderOptions, hot bool) (timing, error) {
	r, err := compactgeom.OpenReaderAtWithOptions(bytes.NewReader(data), int64(len(data)), options)
	if err != nil {
		return timing{}, err
	}
	ids := make([]uint32, 10000)
	var state uint64 = 20261027
	for i := range ids {
		state = state*6364136223846793005 + 1
		ids[i] = uint32(state>>32)%r.Metadata().Features + 1
	}
	rounds := 1
	if hot {
		ids = ids[:16]
		rounds = 1000
	}
	for _, id := range ids {
		if _, err := r.Record(id); err != nil {
			return timing{}, err
		}
	}
	var samples []timing
	for range 3 {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		begin := time.Now()
		for range rounds {
			for _, id := range ids {
				if _, err := r.Record(id); err != nil {
					return timing{}, err
				}
			}
		}
		elapsed := time.Since(begin)
		runtime.ReadMemStats(&after)
		n := float64(rounds * len(ids))
		samples = append(samples, timing{mode, float64(elapsed.Nanoseconds()) / n, float64(after.TotalAlloc-before.TotalAlloc) / n, float64(after.Mallocs-before.Mallocs) / n})
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].NS < samples[j].NS })
	return samples[1], nil
}

func run(source, out string, verify bool, codecName string) ([]result, error) {
	codec := compactgeom.VertexXORShuffleZlib
	if codecName == "bitpack" {
		codec = compactgeom.VertexXORBitpack
	} else if codecName != "xor-shuffle" {
		return nil, fmt.Errorf("unknown codec %q", codecName)
	}
	sourceMap, err := mappedfile.Open(source)
	if err != nil {
		return nil, err
	}
	defer sourceMap.Close()
	sourceReader, err := compactgeom.OpenReaderAtWithOptions(bytes.NewReader(sourceMap.Bytes), int64(len(sourceMap.Bytes)), compactgeom.ReaderOptions{PreloadVertices: true, GeometryCacheBlocks: 16})
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(out, 0755); err != nil {
		return nil, err
	}
	results := []result{{Path: source, Metadata: sourceReader.Metadata()}}
	for _, size := range []int{128, 256} {
		path := filepath.Join(out, fmt.Sprintf("lossless-%s-%d.cgeom", codecName, size))
		begin := time.Now()
		meta, err := compactgeom.RepackVertices(path, bytes.NewReader(sourceMap.Bytes), int64(len(sourceMap.Bytes)), compactgeom.BuilderOptions{VertexCodec: codec, VertexBlockSize: size})
		if err != nil {
			return nil, err
		}
		results = append(results, result{Path: path, Metadata: meta, BuildSeconds: time.Since(begin).Seconds()})
		fmt.Fprintf(os.Stderr, "built %s: %d bytes\n", path, meta.Bytes)
	}
	for i := range results {
		res := &results[i]
		mapped, err := mappedfile.Open(res.Path)
		if err != nil {
			return nil, err
		}
		if verify && i != 0 {
			r, err := compactgeom.OpenReaderAt(bytes.NewReader(mapped.Bytes), int64(len(mapped.Bytes)))
			if err != nil {
				mapped.Close()
				return nil, err
			}
			begin := time.Now()
			for id := uint32(1); id <= res.Metadata.Features; id++ {
				want, err := sourceReader.Record(id)
				if err != nil {
					mapped.Close()
					return nil, err
				}
				got, err := r.Record(id)
				if err != nil {
					mapped.Close()
					return nil, err
				}
				if !equal(got, want) {
					mapped.Close()
					return nil, fmt.Errorf("feature%d bits differ", id)
				}
				res.Verified++
				if id%250000 == 0 {
					fmt.Fprintf(os.Stderr, "verified %s: %d/%d\n", res.Path, id, res.Metadata.Features)
				}
			}
			res.VerifySeconds = time.Since(begin).Seconds()
		}
		for _, config := range []struct {
			name    string
			options compactgeom.ReaderOptions
			hot     bool
		}{
			{"bounded-random", compactgeom.ReaderOptions{VertexCachePages: 64, VertexCacheBlocks: 64, GeometryCacheBlocks: 16}, false},
			{"bounded-hot16", compactgeom.ReaderOptions{VertexCachePages: 64, VertexCacheBlocks: 64, GeometryCacheBlocks: 16}, true},
			{"preloaded-random", compactgeom.ReaderOptions{PreloadVertices: true, GeometryCacheBlocks: 16}, false},
			{"preloaded-hot16", compactgeom.ReaderOptions{PreloadVertices: true, GeometryCacheBlocks: 16}, true},
		} {
			timed, err := measure(mapped.Bytes, config.name, config.options, config.hot)
			if err != nil {
				mapped.Close()
				return nil, err
			}
			res.Timings = append(res.Timings, timed)
			fmt.Fprintf(os.Stderr, "%s %s %.2fus/record\n", res.Path, config.name, timed.NS/1000)
		}
		mapped.Close()
	}
	return results, nil
}

func main() {
	source := flag.String("source", "data/topolab/lossless.cgeom", "exact source geometry component")
	out := flag.String("out", "data/geomlab", "new output directory")
	verify := flag.Bool("verify", true, "compare every output feature's float64 bits with source")
	codec := flag.String("codec", "bitpack", "bitpack or xor-shuffle")
	flag.Parse()
	results, err := run(*source, *out, *verify, *codec)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	if err := e.Encode(results); err != nil {
		panic(err)
	}
}
