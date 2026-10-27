// Command vertexprobe measures experimental lossless dictionary compression.
// It never changes compactgeom's format or the source component.
package main

import (
	"bytes"
	"compress/zlib"
	"container/list"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"
)

type entry struct {
	offset          int64
	compressed, raw int
}
type result struct {
	Mode           string  `json:"mode"`
	BlockVertices  int     `json:"vertices_per_block"`
	FileBytes      int64   `json:"actual_file_bytes"`
	DirectoryBytes int64   `json:"directory_bytes"`
	RandomNS       float64 `json:"random_vertex_ns"`
	HotNS          float64 `json:"hot16_vertex_ns"`
	CacheBytes     int     `json:"cache_payload_limit_bytes"`
	BuildSeconds   float64 `json:"build_seconds"`
}

func transform(raw []byte, mode string) []byte {
	if mode == "raw" {
		return append([]byte(nil), raw...)
	}
	work := append([]byte(nil), raw...)
	if mode == "xor-shuffle" {
		var prev [2]uint64
		for i := 0; i < len(work); i += 8 {
			axis := (i / 8) % 2
			bits := binary.LittleEndian.Uint64(work[i : i+8])
			binary.LittleEndian.PutUint64(work[i:i+8], bits^prev[axis])
			prev[axis] = bits
		}
	}
	n := len(raw) / 16
	out := make([]byte, len(raw))
	for col := 0; col < 16; col++ {
		for i := 0; i < n; i++ {
			out[col*n+i] = work[i*16+col]
		}
	}
	return out
}
func inverse(encoded []byte, mode string) []byte {
	if mode == "raw" {
		return encoded
	}
	n := len(encoded) / 16
	out := make([]byte, len(encoded))
	for col := 0; col < 16; col++ {
		for i := 0; i < n; i++ {
			out[i*16+col] = encoded[col*n+i]
		}
	}
	if mode == "xor-shuffle" {
		var prev [2]uint64
		for i := 0; i < len(out); i += 8 {
			axis := (i / 8) % 2
			bits := binary.LittleEndian.Uint64(out[i:i+8]) ^ prev[axis]
			binary.LittleEndian.PutUint64(out[i:i+8], bits)
			prev[axis] = bits
		}
	}
	return out
}

type item struct {
	key  int
	data []byte
}
type reader struct {
	f       *os.File
	entries []entry
	mode    string
	block   int
	order   list.List
	cache   map[int]*list.Element
	inflate io.ReadCloser
	buffer  bytes.Buffer
}

func (r *reader) vertex(id int) ([2]uint64, error) {
	key := id / r.block
	within := id % r.block
	var data []byte
	if e, ok := r.cache[key]; ok {
		r.order.MoveToFront(e)
		data = e.Value.(item).data
	} else {
		e := r.entries[key]
		compressed := make([]byte, e.compressed)
		if _, err := r.f.ReadAt(compressed, e.offset); err != nil {
			return [2]uint64{}, err
		}
		var err error
		if r.mode == "uncompressed-pages" {
			data = compressed
		} else {
			if r.inflate == nil {
				r.inflate, err = zlib.NewReader(bytes.NewReader(compressed))
			} else {
				err = r.inflate.(zlib.Resetter).Reset(bytes.NewReader(compressed), nil)
			}
			if err != nil {
				return [2]uint64{}, err
			}
			r.buffer.Reset()
			if _, err := io.Copy(&r.buffer, r.inflate); err != nil {
				return [2]uint64{}, err
			}
			_ = r.inflate.Close()
			if r.buffer.Len() != e.raw {
				return [2]uint64{}, fmt.Errorf("wrongsize")
			}
			data = inverse(append([]byte(nil), r.buffer.Bytes()...), r.mode)
		}
		r.cache[key] = r.order.PushFront(item{key, data})
		if len(r.cache) > 64 {
			e := r.order.Back()
			delete(r.cache, e.Value.(item).key)
			r.order.Remove(e)
		}
	}
	part := data[within*16 : within*16+16]
	return [2]uint64{binary.LittleEndian.Uint64(part[:8]), binary.LittleEndian.Uint64(part[8:])}, nil
}
func timed(r *reader, ids []int, rounds int) (float64, error) {
	var times []float64
	for n := 0; n < 3; n++ {
		runtime.GC()
		begin := time.Now()
		for k := 0; k < rounds; k++ {
			for _, id := range ids {
				if _, err := r.vertex(id); err != nil {
					return 0, err
				}
			}
		}
		times = append(times, float64(time.Since(begin).Nanoseconds())/float64(len(ids)*rounds))
	}
	sort.Float64s(times)
	return times[1], nil
}
func build(raw []byte, outDir, mode string, block int) (result, error) {
	begin := time.Now()
	path := filepath.Join(outDir, fmt.Sprintf("vertices-%s-%d.vlab", mode, block))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return result{}, err
	}
	defer f.Close()
	_, _ = f.Write(make([]byte, 64))
	var entries []entry
	var compressed bytes.Buffer
	zip, _ := zlib.NewWriterLevel(&compressed, zlib.DefaultCompression)
	for start := 0; start < len(raw); start += block * 16 {
		part := raw[start:min(start+block*16, len(raw))]
		data := transform(part, mode)
		if !bytes.Equal(inverse(append([]byte(nil), data...), mode), part) {
			return result{}, fmt.Errorf("non-lossless transform")
		}
		compressed.Reset()
		zip.Reset(&compressed)
		_, _ = zip.Write(data)
		_ = zip.Close()
		offset, _ := f.Seek(0, io.SeekCurrent)
		if _, err := f.Write(compressed.Bytes()); err != nil {
			return result{}, err
		}
		entries = append(entries, entry{offset, compressed.Len(), len(part)})
	}
	directoryOffset, _ := f.Seek(0, io.SeekCurrent)
	for _, e := range entries {
		var encoded [16]byte
		binary.LittleEndian.PutUint64(encoded[:8], uint64(e.offset))
		binary.LittleEndian.PutUint32(encoded[8:12], uint32(e.compressed))
		binary.LittleEndian.PutUint32(encoded[12:], uint32(e.raw))
		if _, err := f.Write(encoded[:]); err != nil {
			return result{}, err
		}
	}
	size, _ := f.Seek(0, io.SeekCurrent)
	header := make([]byte, 64)
	copy(header, "VLAB01")
	binary.LittleEndian.PutUint64(header[8:16], uint64(len(raw)/16))
	binary.LittleEndian.PutUint32(header[16:20], uint32(block))
	binary.LittleEndian.PutUint64(header[24:32], uint64(directoryOffset))
	binary.LittleEndian.PutUint32(header[32:36], uint32(len(entries)))
	copy(header[40:], mode)
	_, _ = f.WriteAt(header, 0)
	if err := f.Sync(); err != nil {
		return result{}, err
	}
	r := &reader{f: f, entries: entries, mode: mode, block: block, cache: map[int]*list.Element{}}
	res := result{Mode: mode, BlockVertices: block, FileBytes: size, DirectoryBytes: int64(len(entries)) * 16, CacheBytes: 64 * block * 16, BuildSeconds: time.Since(begin).Seconds()}
	// Every decompressed block is compared with the source dictionary before timing.
	for i := range entries {
		got, err := r.vertex(i * block)
		if err != nil {
			return res, err
		}
		want := [2]uint64{binary.LittleEndian.Uint64(raw[i*block*16 : i*block*16+8]), binary.LittleEndian.Uint64(raw[i*block*16+8 : i*block*16+16])}
		if got != want {
			return res, fmt.Errorf("randomreader reconstruction mismatch")
		}
		data := r.cache[i].Value.(item).data
		if !bytes.Equal(data, raw[i*block*16:min((i+1)*block*16, len(raw))]) {
			return res, fmt.Errorf("blockbytes changed")
		}
	}
	ids := make([]int, 10000)
	random := rand.New(rand.NewSource(20261027))
	for i := range ids {
		ids[i] = random.Intn(len(raw) / 16)
	}
	res.RandomNS, err = timed(r, ids, 1)
	if err != nil {
		return res, err
	}
	hot := ids[:16]
	for _, id := range hot {
		_, _ = r.vertex(id)
	}
	res.HotNS, err = timed(r, hot, 10000)
	return res, err
}
func main() {
	input := flag.String("input", "data/topolab/lossless.cgeom", "exact component")
	out := flag.String("out", "data/topolab", "new probe files")
	flag.Parse()
	f, err := os.Open(*input)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	header := make([]byte, 128)
	if _, err = f.ReadAt(header, 0); err != nil {
		panic(err)
	}
	if binary.LittleEndian.Uint32(header[12:16]) != 0 {
		panic("requires losslesscomponent")
	}
	vertices := binary.LittleEndian.Uint64(header[24:32])
	offset := binary.LittleEndian.Uint64(header[32:40])
	raw := make([]byte, int(vertices)*16)
	if _, err = f.ReadAt(raw, int64(offset)); err != nil {
		panic(err)
	}
	var results []result
	// Baseline uses the same64-page cache over the existing raw table.
	entries := []entry{}
	for start := 0; start < len(raw); start += 4096 {
		n := min(4096, len(raw)-start)
		entries = append(entries, entry{int64(offset) + int64(start), n, n})
	}
	r := &reader{f: f, entries: entries, mode: "uncompressed-pages", block: 256, cache: map[int]*list.Element{}}
	ids := make([]int, 10000)
	random := rand.New(rand.NewSource(20261027))
	for i := range ids {
		ids[i] = random.Intn(int(vertices))
	}
	ns, err := timed(r, ids, 1)
	if err != nil {
		panic(err)
	}
	hot, err := timed(r, ids[:16], 10000)
	if err != nil {
		panic(err)
	}
	results = append(results, result{Mode: "uncompressed-pages", BlockVertices: 256, FileBytes: int64(len(raw)), RandomNS: ns, HotNS: hot, CacheBytes: 64 * 4096})
	for _, block := range []int{256, 1024} {
		for _, mode := range []string{"raw", "shuffle", "xor-shuffle"} {
			result, err := build(raw, *out, mode, block)
			if err != nil {
				panic(err)
			}
			results = append(results, result)
			fmt.Fprintf(os.Stderr, "%s/%d: %dbytes,random%.1fus,hot%.0fns\n", mode, block, result.FileBytes, result.RandomNS/1000, result.HotNS)
		}
	}
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	if err := e.Encode(results); err != nil {
		panic(err)
	}
}
