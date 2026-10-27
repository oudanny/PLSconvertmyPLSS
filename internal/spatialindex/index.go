// Package spatialindex is an experimental immutable packed spatial index.
// It selects bounding-box candidates; callers must still test real polygons.
package spatialindex

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
)

const headerSize = 64
const leafFlag uint32 = 1 << 31
const maxEntries = 100_000_000

type Entry struct {
	MinLon, MaxLon, MinLat, MaxLat float32
	ID                             uint32
	Layer                          uint8
}
type Match struct {
	ID    uint32
	Layer uint8
}
type box struct{ minX, maxX, minY, maxY float32 }
type record struct {
	box
	id uint32
}
type node struct {
	box
	first, flags uint32
}

// Index owns its arrays and supports concurrent read-only searches.
type Index struct {
	records   []record
	layers    []uint8
	nodes     []node
	root      uint32
	fanout    uint32
	quantized []byte
	quantBits uint8
}

func validBox(b box) bool {
	for _, x := range []float32{b.minX, b.maxX, b.minY, b.maxY} {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return false
		}
	}
	return b.minX <= b.maxX && b.minY <= b.maxY && b.minX >= -180 && b.maxX <= 180 && b.minY >= -90 && b.maxY <= 90
}
func (b box) contains(x, y float64) bool {
	return float64(b.minX) <= x && float64(b.maxX) >= x && float64(b.minY) <= y && float64(b.maxY) >= y
}
func union(a, b box) box {
	return box{min(a.minX, b.minX), max(a.maxX, b.maxX), min(a.minY, b.minY), max(a.maxY, b.maxY)}
}
func emptyBox() box {
	return box{float32(math.Inf(1)), float32(math.Inf(-1)), float32(math.Inf(1)), float32(math.Inf(-1))}
}
func mask(layer uint8) uint32 { return 1 << uint32(layer-1) }

func spread16(x uint32) uint32 {
	x &= 0xffff
	x = (x | x<<8) & 0x00ff00ff
	x = (x | x<<4) & 0x0f0f0f0f
	x = (x | x<<2) & 0x33333333
	x = (x | x<<1) & 0x55555555
	return x
}

// Build retains original boxes exactly. Center quantization is used only to
// order records, never for candidate decisions or output coordinates.
func Build(entries []Entry, fanout int) (*Index, error) {
	if fanout < 2 || fanout > 256 {
		return nil, errors.New("fanout must be 2–256")
	}
	if len(entries) > maxEntries {
		return nil, errors.New("too many index entries")
	}
	x := &Index{fanout: uint32(fanout), root: math.MaxUint32}
	if len(entries) == 0 {
		return x, nil
	}
	whole := emptyBox()
	for _, e := range entries {
		b := box{e.MinLon, e.MaxLon, e.MinLat, e.MaxLat}
		if !validBox(b) || e.Layer < 1 || e.Layer > 3 {
			return nil, errors.New("invalid bounds or layer")
		}
		whole = union(whole, b)
	}
	keys := make([]uint32, len(entries))
	order := make([]uint32, len(entries))
	width := max(float64(whole.maxX)-float64(whole.minX), 1e-20)
	height := max(float64(whole.maxY)-float64(whole.minY), 1e-20)
	for i, e := range entries {
		cx := (float64(e.MinLon) + float64(e.MaxLon)) / 2
		cy := (float64(e.MinLat) + float64(e.MaxLat)) / 2
		mx := uint32(min(65535., max(0., (cx-float64(whole.minX))/width*65535)))
		my := uint32(min(65535., max(0., (cy-float64(whole.minY))/height*65535)))
		keys[i] = spread16(mx) | (spread16(my) << 1)
		order[i] = uint32(i)
	}
	sort.Slice(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if keys[a] != keys[b] {
			return keys[a] < keys[b]
		}
		if entries[a].ID != entries[b].ID {
			return entries[a].ID < entries[b].ID
		}
		if entries[a].Layer != entries[b].Layer {
			return entries[a].Layer < entries[b].Layer
		}
		return a < b
	})
	x.records = make([]record, len(entries))
	x.layers = make([]uint8, len(entries))
	for i, id := range order {
		e := entries[id]
		x.records[i] = record{box{e.MinLon, e.MaxLon, e.MinLat, e.MaxLat}, e.ID}
		x.layers[i] = e.Layer
	}
	for first := 0; first < len(x.records); first += fanout {
		last := min(first+fanout, len(x.records))
		b := emptyBox()
		var layers uint32
		for i := first; i < last; i++ {
			b = union(b, x.records[i].box)
			layers |= mask(x.layers[i])
		}
		x.nodes = append(x.nodes, node{b, uint32(first), leafFlag | layers<<16 | uint32(last-first)})
	}
	start, count := 0, len(x.nodes)
	for count > 1 {
		next := len(x.nodes)
		for first := start; first < start+count; first += fanout {
			last := min(first+fanout, start+count)
			b := emptyBox()
			var layers uint32
			for i := first; i < last; i++ {
				b = union(b, x.nodes[i].box)
				layers |= (x.nodes[i].flags >> 16) & 7
			}
			x.nodes = append(x.nodes, node{b, uint32(first), layers<<16 | uint32(last-first)})
		}
		start, count = next, len(x.nodes)-next
	}
	x.root = uint32(len(x.nodes) - 1)
	return x, nil
}

func (x *Index) Search(lon, lat float64) []Match { return x.AppendSearch(nil, lon, lat, 0) }
func (x *Index) SearchLayer(lon, lat float64, layer uint8) []Match {
	return x.AppendSearch(nil, lon, lat, layer)
}

// AppendSearch appends deduplicated, ID-sorted candidates. Layer zero searches
// all three layers. Different source records sharing cadastral IDs stay distinct.
func (x *Index) AppendSearch(dst []Match, lon, lat float64, layer uint8) []Match {
	if x == nil || len(x.nodes) == 0 || layer > 3 || math.IsNaN(lon) || math.IsNaN(lat) || lon < -180 || lon > 180 || lat < -90 || lat > 90 {
		return dst
	}
	base := len(dst)
	var want uint32 = 7
	if layer != 0 {
		want = mask(layer)
	}
	var scratch [256]uint32
	stack := scratch[:1]
	stack[0] = x.root
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		n := x.nodes[id]
		if ((n.flags>>16)&want) == 0 || !n.box.contains(lon, lat) {
			continue
		}
		end := n.first + (n.flags & 0xffff)
		if n.flags&leafFlag != 0 {
			if x.quantBits != 0 {
				dst = x.searchQuantizedLeaf(dst, n, lon, lat, layer)
				continue
			}
			for i := n.first; i < end; i++ {
				if layer != 0 && x.layers[i] != layer {
					continue
				}
				r := x.records[i]
				if r.box.contains(lon, lat) {
					dst = append(dst, Match{r.id, x.layers[i]})
				}
			}
		} else {
			for i := n.first; i < end; i++ {
				stack = append(stack, i)
			}
		}
	}
	return deduplicate(dst, base)
}

func deduplicate(matches []Match, base int) []Match {
	m := matches[base:]
	if len(m) < 2 {
		return matches
	}
	sort.Slice(m, func(i, j int) bool {
		if m[i].ID != m[j].ID {
			return m[i].ID < m[j].ID
		}
		return m[i].Layer < m[j].Layer
	})
	n := 1
	for i := 1; i < len(m); i++ {
		if m[i] != m[n-1] {
			m[n] = m[i]
			n++
		}
	}
	return matches[:base+n]
}

func (x *Index) ByteSize() int64 {
	return headerSize + int64(len(x.layers))*(int64(x.recordSize())+1) + int64(len(x.nodes))*24
}
func (x *Index) MemoryBytes() int64 {
	return int64(cap(x.records))*20 + int64(cap(x.layers)) + int64(cap(x.nodes))*24 + int64(cap(x.quantized))
}
func (x *Index) Len() int { return len(x.layers) }

// ValidateCoverage checks IDs/layers against the owning feature table. Split
// antimeridian boxes may repeat an ID; every feature must appear at least once.
// This is an explicit integrity check, separate from opening or querying.
func (x *Index) ValidateCoverage(count uint32, layer func(uint32) uint8) error {
	seen := make([]byte, (uint64(count)+7)/8)
	unique := uint32(0)
	for i, l := range x.layers {
		var id uint32
		if x.quantBits == 0 {
			id = x.records[i].id
		} else {
			stride := x.recordSize()
			id = binary.LittleEndian.Uint32(x.quantized[i*stride+stride-4:])
		}
		if id == 0 || id > count || l != layer(id) {
			return errors.New("spatial feature ID/layer disagrees with feature table")
		}
		p, bit := (id-1)/8, byte(1<<((id-1)%8))
		if seen[p]&bit == 0 {
			seen[p] |= bit
			unique++
		}
	}
	if unique != count {
		return errors.New("spatial index omits feature records")
	}
	return nil
}

func (x *Index) recordSize() int {
	if x.quantBits == 8 {
		return 8
	}
	if x.quantBits == 16 {
		return 12
	}
	return 20
}

func putBox(dst []byte, b box) {
	for i, v := range []float32{b.minX, b.maxX, b.minY, b.maxY} {
		binary.LittleEndian.PutUint32(dst[i*4:], math.Float32bits(v))
	}
}
func readBox(src []byte) box {
	return box{math.Float32frombits(binary.LittleEndian.Uint32(src)), math.Float32frombits(binary.LittleEndian.Uint32(src[4:])), math.Float32frombits(binary.LittleEndian.Uint32(src[8:])), math.Float32frombits(binary.LittleEndian.Uint32(src[12:]))}
}

// WriteTo writes the complete self-contained index. The file is experimental,
// little-endian and independent of native Go struct layout.
func (x *Index) WriteTo(w io.Writer) (int64, error) {
	bw := bufio.NewWriterSize(w, 64<<10)
	var count int64
	write := func(p []byte) error { n, e := bw.Write(p); count += int64(n); return e }
	var h [headerSize]byte
	copy(h[:8], "PLSSIDX1")
	version := uint32(1)
	if x.quantBits != 0 {
		version = 2
	}
	binary.LittleEndian.PutUint32(h[8:], version)
	binary.LittleEndian.PutUint32(h[12:], x.fanout)
	binary.LittleEndian.PutUint64(h[16:], uint64(len(x.layers)))
	binary.LittleEndian.PutUint64(h[24:], uint64(len(x.nodes)))
	binary.LittleEndian.PutUint32(h[32:], x.root)
	binary.LittleEndian.PutUint32(h[36:], uint32(x.quantBits))
	if e := write(h[:]); e != nil {
		return count, e
	}
	var tmp [24]byte
	for _, r := range x.records {
		putBox(tmp[:], r.box)
		binary.LittleEndian.PutUint32(tmp[16:], r.id)
		if e := write(tmp[:20]); e != nil {
			return count, e
		}
	}
	if e := write(x.quantized); e != nil {
		return count, e
	}
	if e := write(x.layers); e != nil {
		return count, e
	}
	for _, n := range x.nodes {
		putBox(tmp[:], n.box)
		binary.LittleEndian.PutUint32(tmp[16:], n.first)
		binary.LittleEndian.PutUint32(tmp[20:], n.flags)
		if e := write(tmp[:]); e != nil {
			return count, e
		}
	}
	return count, bw.Flush()
}

// Open reads an index section from any ReaderAt, including a larger pack file.
// Compact arrays are loaded into RAM; feature geometry and attributes are not.
func Open(r io.ReaderAt, offset, length int64) (*Index, error) {
	if offset < 0 || length < headerSize {
		return nil, errors.New("invalid index section")
	}
	br := bufio.NewReaderSize(io.NewSectionReader(r, offset, length), 64<<10)
	var h [headerSize]byte
	if _, e := io.ReadFull(br, h[:]); e != nil {
		return nil, e
	}
	version := binary.LittleEndian.Uint32(h[8:])
	bits := binary.LittleEndian.Uint32(h[36:])
	stride := 20
	if string(h[:8]) != "PLSSIDX1" || (version != 1 && version != 2) || (version == 1 && bits != 0) || (version == 2 && bits != 8 && bits != 16) {
		return nil, errors.New("unsupported spatial index")
	}
	if bits == 8 {
		stride = 8
	}
	if bits == 16 {
		stride = 12
	}
	nr, nn := binary.LittleEndian.Uint64(h[16:]), binary.LittleEndian.Uint64(h[24:])
	if nr > maxEntries || nn > 2*nr || length != headerSize+int64(nr)*int64(stride+1)+int64(nn)*24 {
		return nil, errors.New("invalid spatial index size")
	}
	x := &Index{layers: make([]uint8, int(nr)), nodes: make([]node, int(nn)), root: binary.LittleEndian.Uint32(h[32:]), fanout: binary.LittleEndian.Uint32(h[12:]), quantBits: uint8(bits)}
	if bits == 0 {
		x.records = make([]record, int(nr))
	} else {
		x.quantized = make([]byte, int(nr)*stride)
	}
	if x.fanout < 2 || x.fanout > 256 || (nn == 0 && (nr != 0 || x.root != math.MaxUint32)) || (nn > 0 && x.root != uint32(nn-1)) {
		return nil, errors.New("invalid spatial index root or fanout")
	}
	var tmp [24]byte
	for i := range x.records {
		if _, e := io.ReadFull(br, tmp[:20]); e != nil {
			return nil, e
		}
		x.records[i] = record{readBox(tmp[:]), binary.LittleEndian.Uint32(tmp[16:])}
		if !validBox(x.records[i].box) {
			return nil, errors.New("invalid indexed bounds")
		}
	}
	if _, e := io.ReadFull(br, x.quantized); e != nil {
		return nil, e
	}
	if _, e := io.ReadFull(br, x.layers); e != nil {
		return nil, e
	}
	for _, l := range x.layers {
		if l < 1 || l > 3 {
			return nil, errors.New("invalid indexed layer")
		}
	}
	for i := range x.nodes {
		if _, e := io.ReadFull(br, tmp[:]); e != nil {
			return nil, e
		}
		n := node{readBox(tmp[:]), binary.LittleEndian.Uint32(tmp[16:]), binary.LittleEndian.Uint32(tmp[20:])}
		c := n.flags & 0xffff
		if !validBox(n.box) || c == 0 || c > x.fanout || n.flags&^(leafFlag|7<<16|0xffff) != 0 {
			return nil, errors.New("invalid spatial node")
		}
		end := uint64(n.first) + uint64(c)
		b := emptyBox()
		var lm uint32
		if n.flags&leafFlag != 0 {
			if end > nr {
				return nil, errors.New("leaf outside record array")
			}
			for j := uint64(n.first); j < end; j++ {
				if x.quantBits == 0 {
					b = union(b, x.records[j].box)
				} else {
					q := x.quantBox(uint32(j))
					if q[0] > q[1] || q[2] > q[3] {
						return nil, errors.New("invalid quantized bounds")
					}
					b = n.box
				}
				lm |= mask(x.layers[j])
			}
		} else {
			if end > uint64(i) {
				return nil, errors.New("node references later node")
			}
			for j := uint64(n.first); j < end; j++ {
				b = union(b, x.nodes[j].box)
				lm |= (x.nodes[j].flags >> 16) & 7
			}
		}
		if b != n.box || lm != (n.flags>>16)&7 {
			return nil, fmt.Errorf("node %d bounds or layer mask inconsistent", i)
		}
		x.nodes[i] = n
	}
	return x, nil
}
