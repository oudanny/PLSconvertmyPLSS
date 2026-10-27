package spatialindex

import (
	"encoding/binary"
	"errors"
	"math"
)

// Quantize creates a smaller candidate index by conservatively widening leaf
// boxes onto an8- or16-bit grid relative to each leaf's original parent box.
// Original geometry is untouched. Search can return additional candidates;
// callers must perform exact polygon containment before returning results.
func (x *Index) Quantize(bits int) (*Index, error) {
	if x == nil || x.quantBits != 0 {
		return nil, errors.New("quantization requires an original index")
	}
	if bits != 8 && bits != 16 {
		return nil, errors.New("quantization bits must be8or16")
	}
	q := &Index{layers: x.layers, nodes: x.nodes, root: x.root, fanout: x.fanout, quantBits: uint8(bits)}
	stride := q.recordSize()
	q.quantized = make([]byte, len(x.layers)*stride)
	scale := float64(uint32(1)<<bits) - 1
	encode := func(value, origin, span float64, upper bool) uint16 {
		if span == 0 {
			return 0
		}
		v := (value - origin) / span * scale
		code := math.Floor(v)
		if upper {
			code = math.Ceil(v)
		}
		code = max(0, min(scale, code))
		// Correct floating-point reconstruction outward against the original
		// exactly represented float32 bound, rather than assuming division exact.
		if upper {
			for code < scale && origin+code/scale*span < value {
				code++
			}
		} else {
			for code > 0 && origin+code/scale*span > value {
				code--
			}
		}
		return uint16(code)
	}
	for _, n := range x.nodes {
		if n.flags&leafFlag == 0 {
			continue
		}
		sx, sy := float64(n.maxX)-float64(n.minX), float64(n.maxY)-float64(n.minY)
		for i := n.first; i < n.first+(n.flags&0xffff); i++ {
			r := x.records[i]
			v := [4]uint16{encode(float64(r.minX), float64(n.minX), sx, false), encode(float64(r.maxX), float64(n.minX), sx, true), encode(float64(r.minY), float64(n.minY), sy, false), encode(float64(r.maxY), float64(n.minY), sy, true)}
			p := q.quantized[int(i)*stride:][:stride]
			if bits == 8 {
				for j, c := range v {
					p[j] = byte(c)
				}
			} else {
				for j, c := range v {
					binary.LittleEndian.PutUint16(p[j*2:], c)
				}
			}
			binary.LittleEndian.PutUint32(p[stride-4:], r.id)
		}
	}
	return q, nil
}

func (x *Index) quantBox(i uint32) [4]uint16 {
	p := x.quantized[int(i)*x.recordSize():]
	if x.quantBits == 8 {
		return [4]uint16{uint16(p[0]), uint16(p[1]), uint16(p[2]), uint16(p[3])}
	}
	return [4]uint16{binary.LittleEndian.Uint16(p), binary.LittleEndian.Uint16(p[2:]), binary.LittleEndian.Uint16(p[4:]), binary.LittleEndian.Uint16(p[6:])}
}

func (x *Index) searchQuantizedLeaf(dst []Match, n node, lon, lat float64, layer uint8) []Match {
	scale := float64(uint32(1)<<x.quantBits) - 1
	width, height := float64(n.maxX)-float64(n.minX), float64(n.maxY)-float64(n.minY)
	px, py := 0., 0.
	if width != 0 {
		px = (lon - float64(n.minX)) / width * scale
	}
	if height != 0 {
		py = (lat - float64(n.minY)) / height * scale
	}
	// Arithmetic for query scaling and bound reconstruction may differ by a
	// few float64 ULPs. This tolerance widens only the candidate test.
	const ulps = 8
	tx, ty := math.Abs(px)*2.220446049250313e-16*ulps+1e-12, math.Abs(py)*2.220446049250313e-16*ulps+1e-12
	stride := x.recordSize()
	for i := n.first; i < n.first+(n.flags&0xffff); i++ {
		if layer != 0 && x.layers[i] != layer {
			continue
		}
		v := x.quantBox(i)
		if float64(v[0]) <= px+tx && float64(v[1]) >= px-tx && float64(v[2]) <= py+ty && float64(v[3]) >= py-ty {
			id := binary.LittleEndian.Uint32(x.quantized[int(i)*stride+stride-4:])
			dst = append(dst, Match{id, x.layers[i]})
		}
	}
	return dst
}
