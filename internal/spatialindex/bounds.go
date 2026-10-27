package spatialindex

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
)

// Bounds is the conservative extent of every indexed candidate. It may span
// the world when an index has parts on both sides of the antimeridian.
type Bounds struct {
	MinLon float64 `json:"min_lon"`
	MaxLon float64 `json:"max_lon"`
	MinLat float64 `json:"min_lat"`
	MaxLat float64 `json:"max_lat"`
}

func publicBounds(b box) Bounds {
	return Bounds{float64(b.minX), float64(b.maxX), float64(b.minY), float64(b.maxY)}
}

func (x *Index) Bounds() (Bounds, bool) {
	if x == nil || len(x.nodes) == 0 {
		return Bounds{}, false
	}
	return publicBounds(x.nodes[x.root].box), true
}

func (v *View) Bounds() (Bounds, bool) {
	if v == nil || len(v.nodes) == 0 {
		return Bounds{}, false
	}
	return publicBounds(v.nodes[v.root].box), true
}

// ReadBounds reads only the header and root node. It validates their layout
// and finite bounds, without claiming to verify the entire spatial tree.
func ReadBounds(r io.ReaderAt, size int64) (Bounds, bool, error) {
	var h [headerSize]byte
	if size < headerSize {
		return Bounds{}, false, errors.New("truncated spatial index")
	}
	if _, err := r.ReadAt(h[:], 0); err != nil {
		return Bounds{}, false, err
	}
	version, bits := binary.LittleEndian.Uint32(h[8:]), binary.LittleEndian.Uint32(h[36:])
	if string(h[:8]) != "PLSSIDX1" || (version != 1 && version != 2) || (version == 1 && bits != 0) || (version == 2 && bits != 8 && bits != 16) {
		return Bounds{}, false, errors.New("unsupported spatial index")
	}
	stride := uint64(20)
	if bits == 8 {
		stride = 8
	} else if bits == 16 {
		stride = 12
	}
	nr, nn := binary.LittleEndian.Uint64(h[16:]), binary.LittleEndian.Uint64(h[24:])
	root, fanout := binary.LittleEndian.Uint32(h[32:]), binary.LittleEndian.Uint32(h[12:])
	if nr > maxEntries || nn > 2*nr || uint64(size) != headerSize+nr*(stride+1)+nn*24 || fanout < 2 || fanout > 256 || (nn == 0 && (nr != 0 || root != math.MaxUint32)) || (nn > 0 && root != uint32(nn-1)) {
		return Bounds{}, false, errors.New("invalid spatial index layout")
	}
	if nn == 0 {
		return Bounds{}, false, nil
	}
	var raw [24]byte
	if _, err := r.ReadAt(raw[:], size-24); err != nil {
		return Bounds{}, false, err
	}
	b := readBox(raw[:])
	flags := binary.LittleEndian.Uint32(raw[20:])
	if !validBox(b) || flags&0xffff == 0 || flags&0xffff > fanout || flags&^(leafFlag|7<<16|0xffff) != 0 {
		return Bounds{}, false, errors.New("invalid spatial root")
	}
	return publicBounds(b), true, nil
}
