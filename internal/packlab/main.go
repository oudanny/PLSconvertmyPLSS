// Command packlab is an isolated research prototype. It reads a v1 pack but
// never modifies it. Its encodings are experimental, not a published format.
package main

import (
	"bytes"
	"compress/zlib"
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
	"sort"
	"time"

	"github.com/oudanny/PLSconvertmyPLSS/plss"
	_ "modernc.org/sqlite"
)

type feature struct {
	Attributes map[string]any `json:"attributes"`
	Geometry   plss.Geometry  `json:"geometry"`
}

type sample struct {
	id      int64
	blob    []byte
	feature feature
}
type layerSample struct {
	layer int
	count int
	keys  []string
	rows  []sample
}
type measurement struct {
	Layer        int     `json:"layer"`
	Sample       int     `json:"sample"`
	MeanBytes    float64 `json:"mean_bytes_per_record"`
	EncodeNS     float64 `json:"encode_ns_per_record"`
	DecodeNS     float64 `json:"decode_ns_per_record"`
	DecodeBytes  float64 `json:"decode_alloc_bytes_per_record"`
	DecodeAllocs float64 `json:"decode_allocs_per_record"`
}
type variantReport struct {
	Name               string        `json:"name"`
	BlockSize          int           `json:"block_size"`
	EstimatedBlobBytes int64         `json:"estimated_all_layer_blob_bytes"`
	Layers             []measurement `json:"layers"`
}
type report struct {
	Source     string          `json:"source"`
	GoVersion  string          `json:"go_version"`
	Sampling   string          `json:"sampling"`
	SampleKeys map[int]int     `json:"schema_keys"`
	Variants   []variantReport `json:"variants"`
}

func putU(dst []byte, v uint64) []byte { return binary.AppendUvarint(dst, v) }
func getU(data []byte, pos *int) (uint64, error) {
	if *pos >= len(data) {
		return 0, io.ErrUnexpectedEOF
	}
	v, n := binary.Uvarint(data[*pos:])
	if n <= 0 {
		return 0, fmt.Errorf("invalid varint")
	}
	*pos += n
	return v, nil
}

// Geometry encodes every ordinate bit, dimension, vertex, and ring, including
// repeated closing vertices. mode0 is little-endian64, mode1 is per-axis XOR
// varints. Predictors reset per ring; no arithmetic delta/quantization is used.
func encodeGeometry(g plss.Geometry, mode byte) []byte {
	out := []byte{mode}
	out = putU(out, uint64(len(g.Rings)))
	for _, ring := range g.Rings {
		out = putU(out, uint64(len(ring)))
		prev := []uint64{}
		for _, v := range ring {
			out = putU(out, uint64(len(v)))
			for axis, x := range v {
				bits := math.Float64bits(x)
				if mode == 0 {
					out = binary.LittleEndian.AppendUint64(out, bits)
					continue
				}
				for len(prev) <= axis {
					prev = append(prev, 0)
				}
				out = putU(out, bits^prev[axis])
				prev[axis] = bits
			}
		}
	}
	return out
}

func decodeGeometry(data []byte) (plss.Geometry, error) {
	if len(data) == 0 || data[0] > 1 {
		return plss.Geometry{}, fmt.Errorf("bad geometry mode")
	}
	mode := data[0]
	pos := 1
	nrings, err := getU(data, &pos)
	if err != nil || nrings > uint64(len(data)) {
		return plss.Geometry{}, fmt.Errorf("bad rings")
	}
	g := plss.Geometry{Rings: make([][][]float64, int(nrings))}
	for i := range g.Rings {
		nverts, err := getU(data, &pos)
		if err != nil || nverts > uint64(len(data)) {
			return plss.Geometry{}, fmt.Errorf("bad vertices")
		}
		g.Rings[i] = make([][]float64, int(nverts))
		prev := []uint64{}
		// Retain one contiguous two-ordinate allocation for common GIS vertices.
		xy := make([]float64, int(nverts)*2)
		for j := range g.Rings[i] {
			dims, err := getU(data, &pos)
			if err != nil || dims > uint64(len(data)) {
				return plss.Geometry{}, fmt.Errorf("bad dimensions")
			}
			v := xy[j*2 : j*2+2]
			if dims != 2 {
				v = make([]float64, int(dims))
			}
			g.Rings[i][j] = v
			for axis := range v {
				var bits uint64
				if mode == 0 {
					if len(data)-pos < 8 {
						return plss.Geometry{}, io.ErrUnexpectedEOF
					}
					bits = binary.LittleEndian.Uint64(data[pos : pos+8])
					pos += 8
				} else {
					value, err := getU(data, &pos)
					if err != nil {
						return plss.Geometry{}, err
					}
					for len(prev) <= axis {
						prev = append(prev, 0)
					}
					bits = value ^ prev[axis]
					prev[axis] = bits
				}
				v[axis] = math.Float64frombits(bits)
			}
		}
	}
	if pos != len(data) {
		return plss.Geometry{}, fmt.Errorf("trailing geometry bytes")
	}
	return g, nil
}

func encodeFeature(f feature, keys []string, compact bool, mode byte) ([]byte, error) {
	var attrData []byte
	var err error
	present := []byte{}
	if compact {
		present = make([]byte, (len(keys)+7)/8)
		values := []any{}
		for i, key := range keys {
			if value, ok := f.Attributes[key]; ok {
				present[i/8] |= 1 << uint(i%8)
				values = append(values, value)
			}
		}
		if len(values) != len(f.Attributes) {
			return nil, fmt.Errorf("schema omits an attribute")
		}
		attrData, err = json.Marshal(values)
	} else {
		attrData, err = json.Marshal(f.Attributes)
	}
	if err != nil {
		return nil, err
	}
	geometry := encodeGeometry(f.Geometry, mode)
	out := putU(nil, uint64(len(attrData)))
	out = append(out, present...)
	out = append(out, attrData...)
	out = append(out, geometry...)
	return out, nil
}

func decodeFeature(data []byte, keys []string, compact bool) (feature, error) {
	pos := 0
	n, err := getU(data, &pos)
	if err != nil {
		return feature{}, err
	}
	present := []byte{}
	if compact {
		count := (len(keys) + 7) / 8
		if len(data)-pos < count {
			return feature{}, io.ErrUnexpectedEOF
		}
		present = data[pos : pos+count]
		pos += count
	}
	if n > uint64(len(data)-pos) {
		return feature{}, io.ErrUnexpectedEOF
	}
	f := feature{}
	d := json.NewDecoder(bytes.NewReader(data[pos : pos+int(n)]))
	d.UseNumber()
	if compact {
		var values []any
		if err := d.Decode(&values); err != nil {
			return f, err
		}
		f.Attributes = make(map[string]any, len(values))
		j := 0
		for i, key := range keys {
			if present[i/8]&(1<<uint(i%8)) != 0 {
				if j >= len(values) {
					return f, fmt.Errorf("short schema values")
				}
				f.Attributes[key] = values[j]
				j++
			}
		}
		if j != len(values) {
			return f, fmt.Errorf("extra schema values")
		}
	} else if err := d.Decode(&f.Attributes); err != nil {
		return f, err
	}
	pos += int(n)
	f.Geometry, err = decodeGeometry(data[pos:])
	return f, err
}

type compressor struct {
	buffer bytes.Buffer
	writer *zlib.Writer
}

func newCompressor(level int) *compressor {
	c := &compressor{}
	c.writer, _ = zlib.NewWriterLevel(&c.buffer, level)
	return c
}
func (c *compressor) zip(raw []byte) []byte {
	c.buffer.Reset()
	c.writer.Reset(&c.buffer)
	_, _ = c.writer.Write(raw)
	_ = c.writer.Close()
	return append([]byte(nil), c.buffer.Bytes()...)
}

type inflater struct{ reader io.ReadCloser }

func (d *inflater) unzip(data []byte) ([]byte, error) {
	var err error
	if d.reader == nil {
		d.reader, err = zlib.NewReader(bytes.NewReader(data))
	} else {
		err = d.reader.(zlib.Resetter).Reset(bytes.NewReader(data), nil)
	}
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(d.reader)
	_ = d.reader.Close()
	return raw, err
}
func decodeJSON(raw []byte) (feature, error) {
	var f feature
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	err := d.Decode(&f)
	return f, err
}

func same(a, b feature) bool {
	aa, _ := json.Marshal(a.Attributes)
	bb, _ := json.Marshal(b.Attributes)
	if !bytes.Equal(aa, bb) {
		return false
	}
	if len(a.Geometry.Rings) != len(b.Geometry.Rings) {
		return false
	}
	for i, ring := range a.Geometry.Rings {
		if len(ring) != len(b.Geometry.Rings[i]) {
			return false
		}
		for j, v := range ring {
			if len(v) != len(b.Geometry.Rings[i][j]) {
				return false
			}
			for k, x := range v {
				if math.Float64bits(x) != math.Float64bits(b.Geometry.Rings[i][j][k]) {
					return false
				}
			}
		}
	}
	return true
}

func load(path string, blocks int) ([]layerSample, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uriPath := filepath.ToSlash(abs)
	if uriPath[0] != '/' {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var raw string
	if err := db.QueryRow("SELECT document FROM pack_metadata WHERE id=1 AND complete=1").Scan(&raw); err != nil {
		return nil, err
	}
	var meta plss.PackMetadata
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return nil, err
	}
	counts := []int{meta.Counts["township"], meta.Counts["section"], meta.Counts["intersected"]}
	result := []layerSample{}
	var offset int64
	inflate := &inflater{}
	for layer, count := range counts {
		nblocks := blocks
		if nblocks*128 > count {
			nblocks = count / 128
		}
		if nblocks < 1 {
			return nil, fmt.Errorf("layer too small")
		}
		s := layerSample{layer: layer + 1, count: count}
		keys := map[string]bool{}
		for block := 0; block < nblocks; block++ {
			start := offset + int64(block*(count-128)/nblocks) + 1
			rows, err := db.Query("SELECT id,layer,data FROM features WHERE id BETWEEN ? AND ? ORDER BY id", start, start+127)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var r sample
				var actualLayer int
				if err := rows.Scan(&r.id, &actualLayer, &r.blob); err != nil {
					rows.Close()
					return nil, err
				}
				if actualLayer != s.layer {
					rows.Close()
					return nil, fmt.Errorf("unexpected layer %d in stratified range", actualLayer)
				}
				raw, err := inflate.unzip(r.blob)
				if err != nil {
					rows.Close()
					return nil, err
				}
				r.feature, err = decodeJSON(raw)
				if err != nil {
					rows.Close()
					return nil, err
				}
				for key := range r.feature.Attributes {
					keys[key] = true
				}
				s.rows = append(s.rows, r)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return nil, err
			}
			rows.Close()
		}
		for key := range keys {
			s.keys = append(s.keys, key)
		}
		sort.Strings(s.keys)
		result = append(result, s)
		offset += int64(count)
		fmt.Fprintf(os.Stderr, "sampled layer%d: %d / %d records, %d keys\n", s.layer, len(s.rows), count, len(s.keys))
	}
	return result, nil
}

// Each block stores an offset table followed by independently parseable records.
// zlib decompresses the block; only the requested record is then parsed.
func packBlock(records [][]byte) []byte {
	out := putU(nil, uint64(len(records)))
	for _, r := range records {
		out = putU(out, uint64(len(r)))
	}
	for _, r := range records {
		out = append(out, r...)
	}
	return out
}
func blockRecord(data []byte, index int) ([]byte, error) {
	pos := 0
	count, err := getU(data, &pos)
	if err != nil || index < 0 || index >= int(count) {
		return nil, fmt.Errorf("bad block")
	}
	var start, length uint64
	for i := 0; i < int(count); i++ {
		n, err := getU(data, &pos)
		if err != nil {
			return nil, err
		}
		if i < index {
			start += n
		}
		if i == index {
			length = n
		}
	}
	if start+length > uint64(len(data)-pos) {
		return nil, io.ErrUnexpectedEOF
	}
	return data[pos+int(start) : pos+int(start+length)], nil
}

var sink feature

func measureDecode(fn func(int) (feature, error), n int, rounds int) (ns, allocated, allocs float64, err error) {
	// Fixed randomized record order avoids measuring one cache-hot repeated
	// geometry. All blobs are memory resident; this is codec time, not SQLite I/O.
	order := rand.New(rand.NewSource(20261027)).Perm(n)
	for _, i := range order[:min(n, 16)] {
		if _, err = fn(i); err != nil {
			return
		}
	}
	var durations []float64
	var byteCounts, allocationCounts []float64
	for repeat := 0; repeat < 3; repeat++ {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		start := time.Now()
		for r := 0; r < rounds; r++ {
			for _, i := range order {
				sink, err = fn(i)
				if err != nil {
					return
				}
			}
		}
		duration := time.Since(start)
		runtime.ReadMemStats(&after)
		ops := float64(n * rounds)
		durations = append(durations, float64(duration.Nanoseconds())/ops)
		byteCounts = append(byteCounts, float64(after.TotalAlloc-before.TotalAlloc)/ops)
		allocationCounts = append(allocationCounts, float64(after.Mallocs-before.Mallocs)/ops)
	}
	sort.Float64s(durations)
	sort.Float64s(byteCounts)
	sort.Float64s(allocationCounts)
	return durations[1], byteCounts[1], allocationCounts[1], nil
}

func runVariant(samples []layerSample, name string, compact bool, mode byte, level, blockSize, rounds int) (variantReport, error) {
	r := variantReport{Name: name, BlockSize: blockSize}
	compress := newCompressor(level)
	for _, s := range samples {
		start := time.Now()
		encoded := make([][]byte, len(s.rows))
		var byteTotal int64
		for i, row := range s.rows {
			var raw []byte
			var err error
			if name == "current-zlib-json" {
				encoded[i] = row.blob
				continue
			}
			if name == "json-default-zlib" {
				raw, err = json.Marshal(row.feature)
			} else {
				raw, err = encodeFeature(row.feature, s.keys, compact, mode)
			}
			if err != nil {
				return r, err
			}
			encoded[i] = raw
		}
		if name != "current-zlib-json" {
			if blockSize == 1 {
				for i, raw := range encoded {
					encoded[i] = compress.zip(raw)
				}
			} else {
				blocks := [][]byte{}
				for i := 0; i < len(encoded); i += blockSize {
					blocks = append(blocks, compress.zip(packBlock(encoded[i:min(i+blockSize, len(encoded))])))
				}
				encoded = blocks
			}
		}
		for _, v := range encoded {
			byteTotal += int64(len(v))
		}
		encodeNS := float64(time.Since(start).Nanoseconds()) / float64(len(s.rows))
		if name == "current-zlib-json" {
			encodeNS = 0
		}
		inflate := &inflater{}
		decode := func(i int) (feature, error) {
			raw, err := inflate.unzip(encoded[i/blockSize])
			if err != nil {
				return feature{}, err
			}
			if blockSize > 1 {
				raw, err = blockRecord(raw, i%blockSize)
				if err != nil {
					return feature{}, err
				}
			}
			if name == "current-zlib-json" || name == "json-default-zlib" {
				return decodeJSON(raw)
			}
			return decodeFeature(raw, s.keys, compact)
		}
		for i, row := range s.rows {
			decoded, err := decode(i)
			if err != nil || !same(row.feature, decoded) {
				return r, fmt.Errorf("%s layer%d id%d failed exact roundtrip: %v", name, s.layer, row.id, err)
			}
		}
		fmt.Fprintf(os.Stderr, "%s layer%d size %.1f B/record (exact roundtrip verified)\n", name, s.layer, float64(byteTotal)/float64(len(s.rows)))
		ns, bytes, allocs, err := measureDecode(decode, len(s.rows), rounds)
		if err != nil {
			return r, err
		}
		m := measurement{Layer: s.layer, Sample: len(s.rows), MeanBytes: float64(byteTotal) / float64(len(s.rows)), EncodeNS: encodeNS, DecodeNS: ns, DecodeBytes: bytes, DecodeAllocs: allocs}
		r.Layers = append(r.Layers, m)
		r.EstimatedBlobBytes += int64(m.MeanBytes * float64(s.count))
	}
	return r, nil
}

func main() {
	db := flag.String("db", "data/nevada.sqlite", "read-only v1 pack")
	blocks := flag.Int("blocks", 8, "stratified contiguous128-row samples per layer")
	rounds := flag.Int("rounds", 1, "randomized decode rounds per timing repetition (three repetitions)")
	flag.Parse()
	if *blocks < 1 || *rounds < 1 {
		fmt.Fprintln(os.Stderr, "blocks/rounds must be positive")
		os.Exit(1)
	}
	samples, err := load(*db, *blocks)
	if err != nil {
		panic(err)
	}
	r := report{Source: *db, GoVersion: runtime.Version(), Sampling: "Deterministic stratified contiguous128-row blocks per layer; all byte estimates weighted by actual layer counts; codec timings exclude database I/O and use a reused zlib reader; median of3 repetitions.", SampleKeys: map[int]int{}}
	for _, s := range samples {
		r.SampleKeys[s.layer] = len(s.keys)
	}
	variants := []struct {
		name             string
		compact          bool
		mode             byte
		level, blockSize int
	}{
		{"current-zlib-json", false, 0, zlib.BestSpeed, 1},
		{"json-default-zlib", false, 0, zlib.DefaultCompression, 1},
		{"binary64-zlib-fast", false, 0, zlib.BestSpeed, 1},
		{"schema-binary64-zlib-fast", true, 0, zlib.BestSpeed, 1},
		{"schema-xor-zlib-fast", true, 1, zlib.BestSpeed, 1},
		{"schema-binary64-zlib-default", true, 0, zlib.DefaultCompression, 1},
		{"schema-xor-zlib-default", true, 1, zlib.DefaultCompression, 1},
		{"schema-binary64-block32-zlib", true, 0, zlib.DefaultCompression, 32},
		{"schema-binary64-block128-zlib", true, 0, zlib.DefaultCompression, 128},
	}
	for _, v := range variants {
		m, err := runVariant(samples, v.name, v.compact, v.mode, v.level, v.blockSize, *rounds)
		if err != nil {
			panic(err)
		}
		r.Variants = append(r.Variants, m)
		fmt.Fprintf(os.Stderr, "%s estimated total blobs: %d bytes\n", v.name, m.EstimatedBlobBytes)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(r); err != nil {
		panic(err)
	}
}
