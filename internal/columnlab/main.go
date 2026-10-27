// Command columnlab builds complete experimental column-attribute archives.
package main

import (
	"bytes"
	"compress/zlib"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/oudanny/PLSconvertmyPLSS/internal/columns"
	"io"
	_ "modernc.org/sqlite"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

type variant struct {
	name    string
	options columns.Options
	file    *os.File
	writer  *columns.Writer
}

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	dbpath := flag.String("db", "data/nevada.sqlite", "source SQLite pack")
	outdir := flag.String("out", "data/columnlab", "new output directory")
	bench := flag.String("bench", "", "benchmark an existing .cols archive only")
	blockSizes := flag.String("block-sizes", "1024,4096", "comma-separated column block sizes")
	modes := flag.String("modes", "plain-full,dict-full,dict-essential", "comma-separated codec variants")
	flag.Parse()
	if *bench != "" {
		return benchmark(*bench)
	}
	abs, e := filepath.Abs(*dbpath)
	if e != nil {
		return e
	}
	p := filepath.ToSlash(abs)
	if p[0] != '/' {
		p = "/" + p
	}
	uri := url.URL{Scheme: "file", Path: p, RawQuery: "mode=ro"}
	db, e := sql.Open("sqlite", uri.String())
	if e != nil {
		return e
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var source string
	if e := db.QueryRow("SELECT document FROM pack_metadata WHERE complete=1").Scan(&source); e != nil {
		return e
	}
	if e := os.MkdirAll(*outdir, 0755); e != nil {
		return e
	}
	var variants []*variant
	for _, blockText := range strings.Split(*blockSizes, ",") {
		blocksize, err := strconv.Atoi(strings.TrimSpace(blockText))
		if err != nil || blocksize < 1 || blocksize > 8192 {
			return fmt.Errorf("invalid block size %q", blockText)
		}
		for _, mode := range strings.Split(*modes, ",") {
			if mode != "plain-full" && mode != "dict-full" && mode != "dict-essential" {
				return fmt.Errorf("invalid mode %q", mode)
			}
			v := &variant{name: fmt.Sprintf("%s-%d", mode, blocksize), options: columns.Options{BlockSize: blocksize, Dictionary: mode != "plain-full", Essential: mode == "dict-essential", Source: json.RawMessage(source), Verify: true}}
			v.file, e = os.OpenFile(filepath.Join(*outdir, v.name+".cols"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
			if e != nil {
				return e
			}
			defer v.file.Close()
			v.writer, e = columns.NewWriter(v.file, v.options)
			if e != nil {
				return e
			}
			variants = append(variants, v)
		}
	}
	started := time.Now()
	rows, e := db.Query("SELECT id,layer,data FROM features ORDER BY id")
	if e != nil {
		return e
	}
	var inflater io.ReadCloser
	var raw bytes.Buffer
	count := 0
	peakHeap, peakSys := uint64(0), uint64(0)
	for rows.Next() {
		var id uint32
		var layer uint8
		var blob []byte
		if e := rows.Scan(&id, &layer, &blob); e != nil {
			rows.Close()
			return e
		}
		if inflater == nil {
			inflater, e = zlib.NewReader(bytes.NewReader(blob))
		} else {
			e = inflater.(zlib.Resetter).Reset(bytes.NewReader(blob), nil)
		}
		if e != nil {
			rows.Close()
			return e
		}
		raw.Reset()
		if _, e := raw.ReadFrom(inflater); e != nil {
			rows.Close()
			return e
		}
		inflater.Close()
		var feature struct {
			Attributes map[string]json.RawMessage `json:"attributes"`
		}
		if e := json.Unmarshal(raw.Bytes(), &feature); e != nil {
			rows.Close()
			return e
		}
		r := columns.Record{ID: id, Layer: layer, Attributes: feature.Attributes}
		for _, v := range variants {
			if e := v.writer.Add(r); e != nil {
				rows.Close()
				return fmt.Errorf("%s: %w", v.name, e)
			}
		}
		count++
		if count%100000 == 0 {
			var mem runtime.MemStats
			runtime.ReadMemStats(&mem)
			if mem.HeapAlloc > peakHeap {
				peakHeap = mem.HeapAlloc
			}
			if mem.Sys > peakSys {
				peakSys = mem.Sys
			}
			sizes := map[string]int64{}
			for _, v := range variants {
				stat, _ := v.file.Stat()
				sizes[v.name] = stat.Size()
			}
			report(map[string]any{"records": count, "elapsed_s": time.Since(started).Seconds(), "heap_bytes": mem.HeapAlloc, "go_sys_bytes": mem.Sys, "bytes": sizes})
		}
	}
	if e := rows.Err(); e != nil {
		rows.Close()
		return e
	}
	rows.Close()
	audit, e := db.Query("SELECT id,layer,attributes FROM unlocated ORDER BY id")
	if e != nil {
		return e
	}
	for audit.Next() {
		var a columns.AuditRecord
		var raw string
		if e := audit.Scan(&a.ID, &a.Layer, &raw); e != nil {
			audit.Close()
			return e
		}
		if e := json.Unmarshal([]byte(raw), &a.Attributes); e != nil {
			audit.Close()
			return e
		}
		for _, v := range variants {
			v.writer.AddUnlocated(a)
		}
	}
	if e := audit.Err(); e != nil {
		audit.Close()
		return e
	}
	audit.Close()
	sizes := map[string]int64{}
	for _, v := range variants {
		if _, e := v.writer.Finish(); e != nil {
			return e
		}
		if e := v.file.Sync(); e != nil {
			return e
		}
		stat, _ := v.file.Stat()
		sizes[v.name] = stat.Size()
	}
	report(map[string]any{"complete": true, "verified_every_record_every_variant": true, "records": count, "elapsed_s": time.Since(started).Seconds(), "sampled_peak_heap_bytes": peakHeap, "sampled_peak_go_sys_bytes": peakSys, "bytes": sizes})
	for _, v := range variants {
		if e := benchmark(filepath.Join(*outdir, v.name+".cols")); e != nil {
			return e
		}
	}
	return nil
}

func report(v any) { b, _ := json.Marshal(v); fmt.Println(string(b)) }
func benchmark(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	stat, e := f.Stat()
	if e != nil {
		return e
	}
	start := time.Now()
	r, e := columns.OpenReader(f, stat.Size())
	if e != nil {
		return e
	}
	openTime := time.Since(start)
	meta := r.Metadata()
	// Batched opens avoid zero readings on coarse Windows clock ticks.
	start = time.Now()
	for i := 0; i < 100; i++ {
		if _, e := columns.OpenReader(f, stat.Size()); e != nil {
			return e
		}
	}
	openTime = time.Since(start) / 100
	if meta.Records == 0 {
		return fmt.Errorf("empty archive")
	}
	r.SetCacheBlocks(0)
	cold := make([]float64, 100)
	coldStart := time.Now()
	for i := range cold {
		id := uint32(1 + (uint64(i)*104729+500000)%meta.Records)
		t := time.Now()
		if _, e := r.Attributes(id); e != nil {
			return e
		}
		cold[i] = float64(time.Since(t).Nanoseconds()) / 1000
	}
	coldMean := float64(time.Since(coldStart).Nanoseconds()) / float64(len(cold)) / 1000
	sort.Float64s(cold)
	r.SetCacheBlocks(4)
	id := uint32(500000)
	if uint64(id) > meta.Records {
		id = 1
	}
	if _, e := r.Attributes(id); e != nil {
		return e
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start = time.Now()
	for i := 0; i < 5000; i++ {
		if _, e := r.Attributes(id); e != nil {
			return e
		}
	}
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	report(map[string]any{"benchmark": filepath.Base(path), "file_bytes": stat.Size(), "open_us": float64(openTime.Nanoseconds()) / 1000, "random_record_cache_disabled_mean_us": coldMean, "same_block_hot_us": float64(elapsed.Nanoseconds()) / 5000000, "hot_alloc_bytes": float64(after.TotalAlloc-before.TotalAlloc) / 5000, "hot_allocs": float64(after.Mallocs-before.Mallocs) / 5000, "heap_bytes": after.HeapAlloc, "go_sys_bytes": after.Sys})
	return nil
}
