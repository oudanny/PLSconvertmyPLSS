package spatialindex

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// View searches an immutable serialized index without copying its record or
// layer arrays. The caller must keep the backing bytes alive and unchanged,
// including keeping any memory mapping open until all searches have finished.
// Searches may run concurrently. Only the much smaller node array is copied.
//
// OpenView validates the tree and layers without reading record pages. Bounds
// in visited leaves are validated during searches; Validate explicitly checks
// every leaf. A successful open is therefore not a full file integrity audit.
type View struct {
	records   []byte
	layers    []byte
	nodes     []node
	root      uint32
	stride    int
	quantBits uint8
}

// OpenView opens exactly one serialized index component. Slice a larger mapped
// container to its index section before calling this function. The on-disk
// format is the same as Index.WriteTo and Open.
func OpenView(data []byte) (*View, error) {
	if len(data) < headerSize {
		return nil, errors.New("invalid index section")
	}
	version := binary.LittleEndian.Uint32(data[8:])
	bits := binary.LittleEndian.Uint32(data[36:])
	if string(data[:8]) != "PLSSIDX1" || (version != 1 && version != 2) || (version == 1 && bits != 0) || (version == 2 && bits != 8 && bits != 16) {
		return nil, errors.New("unsupported spatial index")
	}
	stride := 20
	if bits == 8 {
		stride = 8
	} else if bits == 16 {
		stride = 12
	}
	nr, nn := binary.LittleEndian.Uint64(data[16:]), binary.LittleEndian.Uint64(data[24:])
	if nr > maxEntries || nn > 2*nr || uint64(len(data)) != headerSize+nr*uint64(stride+1)+nn*24 {
		return nil, errors.New("invalid spatial index size")
	}
	fanout, root := binary.LittleEndian.Uint32(data[12:]), binary.LittleEndian.Uint32(data[32:])
	if fanout < 2 || fanout > 256 || (nn == 0 && (nr != 0 || root != math.MaxUint32)) || (nn > 0 && root != uint32(nn-1)) {
		return nil, errors.New("invalid spatial index root or fanout")
	}
	leafEnd := headerSize + int(nr)*stride
	layerEnd := leafEnd + int(nr)
	v := &View{
		records: data[headerSize:leafEnd:leafEnd], layers: data[leafEnd:layerEnd:layerEnd],
		nodes: make([]node, int(nn)), root: root, stride: stride, quantBits: uint8(bits),
	}
	// Packed leaves partition records, and subsequent internal nodes partition
	// all earlier nodes except the root. Checking these consecutive ranges
	// rejects cycles, orphan nodes, duplicate children, and omitted records
	// without allocating a second per-record or per-node visitation array.
	var nextRecord, nextChild uint64
	seenBranch := false
	for i := range v.nodes {
		p := data[layerEnd+i*24:]
		n := node{readBox(p), binary.LittleEndian.Uint32(p[16:]), binary.LittleEndian.Uint32(p[20:])}
		count := n.flags & 0xffff
		if !validBox(n.box) || count == 0 || count > fanout || n.flags&^(leafFlag|7<<16|0xffff) != 0 {
			return nil, errors.New("invalid spatial node")
		}
		end := uint64(n.first) + uint64(count)
		var layerMask uint32
		if n.flags&leafFlag != 0 {
			if seenBranch || uint64(n.first) != nextRecord || end > nr {
				return nil, errors.New("invalid spatial leaf coverage")
			}
			for _, layer := range v.layers[int(n.first):int(end)] {
				if layer < 1 || layer > 3 {
					return nil, errors.New("invalid indexed layer")
				}
				layerMask |= mask(layer)
			}
			nextRecord = end
		} else {
			seenBranch = true
			if uint64(n.first) != nextChild || end > uint64(i) {
				return nil, errors.New("invalid spatial child coverage")
			}
			bounds := emptyBox()
			for _, child := range v.nodes[int(n.first):int(end)] {
				bounds = union(bounds, child.box)
				layerMask |= (child.flags >> 16) & 7
			}
			if bounds != n.box {
				return nil, fmt.Errorf("node %d bounds inconsistent", i)
			}
			nextChild = end
		}
		if layerMask != (n.flags>>16)&7 {
			return nil, fmt.Errorf("node %d layer mask inconsistent", i)
		}
		v.nodes[i] = n
	}
	if nextRecord != nr || (nn != 0 && nextChild != nn-1) {
		return nil, errors.New("incomplete spatial tree coverage")
	}
	return v, nil
}

func (v *View) Len() int { return len(v.layers) }

func (v *View) ByteSize() int64 {
	return headerSize + int64(len(v.records)) + int64(len(v.layers)) + int64(len(v.nodes))*24
}

// MemoryBytes reports owned array bytes, excluding the borrowed backing slice.
// A memory mapping also consumes resident pages as they are accessed.
func (v *View) MemoryBytes() int64 { return int64(cap(v.nodes)) * 24 }

func (v *View) Search(lon, lat float64) ([]Match, error) {
	return v.AppendSearch(nil, lon, lat, 0)
}

func (v *View) SearchLayer(lon, lat float64, layer uint8) ([]Match, error) {
	return v.AppendSearch(nil, lon, lat, layer)
}

// AppendSearch has the same candidate semantics as Index.AppendSearch, with an
// error for a malformed visited leaf. On error it returns the original dst
// length. Invalid geographic input or an invalid layer returns no candidates.
func (v *View) AppendSearch(dst []Match, lon, lat float64, layer uint8) ([]Match, error) {
	if v == nil || len(v.nodes) == 0 || layer > 3 || math.IsNaN(lon) || math.IsNaN(lat) || lon < -180 || lon > 180 || lat < -90 || lat > 90 {
		return dst, nil
	}
	base := len(dst)
	want := uint32(7)
	if layer != 0 {
		want = mask(layer)
	}
	var scratch [256]uint32
	stack := scratch[:1]
	stack[0] = v.root
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		n := v.nodes[id]
		if ((n.flags>>16)&want) == 0 || !n.box.contains(lon, lat) {
			continue
		}
		if n.flags&leafFlag != 0 {
			var err error
			dst, err = v.searchLeaf(dst, n, lon, lat, layer, true)
			if err != nil {
				return dst[:base], err
			}
		} else {
			for child := n.first; child < n.first+(n.flags&0xffff); child++ {
				stack = append(stack, child)
			}
		}
	}
	return deduplicate(dst, base), nil
}

func (v *View) searchLeaf(dst []Match, n node, lon, lat float64, layer uint8, search bool) ([]Match, error) {
	end := n.first + (n.flags & 0xffff)
	if v.quantBits == 0 {
		bounds := emptyBox()
		for i := n.first; i < end; i++ {
			p := v.records[int(i)*v.stride:]
			b := readBox(p)
			if !validBox(b) {
				return dst, errors.New("invalid indexed bounds")
			}
			bounds = union(bounds, b)
			if search && (layer == 0 || v.layers[i] == layer) && b.contains(lon, lat) {
				dst = append(dst, Match{binary.LittleEndian.Uint32(p[16:]), v.layers[i]})
			}
		}
		if bounds != n.box {
			return dst, errors.New("leaf bounds inconsistent")
		}
		return dst, nil
	}
	// Keep arithmetic and tolerance identical to Index.searchQuantizedLeaf.
	scale := float64(uint32(1)<<v.quantBits) - 1
	width, height := float64(n.maxX)-float64(n.minX), float64(n.maxY)-float64(n.minY)
	px, py := 0., 0.
	if width != 0 {
		px = (lon - float64(n.minX)) / width * scale
	}
	if height != 0 {
		py = (lat - float64(n.minY)) / height * scale
	}
	const ulps = 8
	tx, ty := math.Abs(px)*2.220446049250313e-16*ulps+1e-12, math.Abs(py)*2.220446049250313e-16*ulps+1e-12
	for i := n.first; i < end; i++ {
		p := v.records[int(i)*v.stride:]
		var q [4]uint16
		if v.quantBits == 8 {
			q = [4]uint16{uint16(p[0]), uint16(p[1]), uint16(p[2]), uint16(p[3])}
		} else {
			q = [4]uint16{binary.LittleEndian.Uint16(p), binary.LittleEndian.Uint16(p[2:]), binary.LittleEndian.Uint16(p[4:]), binary.LittleEndian.Uint16(p[6:])}
		}
		if q[0] > q[1] || q[2] > q[3] {
			return dst, errors.New("invalid quantized bounds")
		}
		if search && (layer == 0 || v.layers[i] == layer) && float64(q[0]) <= px+tx && float64(q[1]) >= px-tx && float64(q[2]) <= py+ty && float64(q[3]) >= py-ty {
			dst = append(dst, Match{binary.LittleEndian.Uint32(p[v.stride-4:]), v.layers[i]})
		}
	}
	return dst, nil
}

// Validate scans every leaf, including those outside any queried location.
// It faults in mapped record pages and is deliberately separate from OpenView.
func (v *View) Validate() error {
	for _, n := range v.nodes {
		if n.flags&leafFlag != 0 {
			if _, err := v.searchLeaf(nil, n, 0, 0, 0, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateCoverage checks the same feature ID/layer coverage as Index. It reads
// all record IDs, so it also belongs in an explicit audit rather than startup.
func (v *View) ValidateCoverage(count uint32, layer func(uint32) uint8) error {
	seen := make([]byte, (uint64(count)+7)/8)
	unique := uint32(0)
	for i, l := range v.layers {
		id := binary.LittleEndian.Uint32(v.records[i*v.stride+v.stride-4:])
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
