// Package columns is an experimental lossless block-column attribute archive.
// Geometry is deliberately excluded so callers can keep coordinate lookup hot.
package columns

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

const maxBlockBytes = 128 << 20

type Record struct {
	ID         uint32
	Layer      uint8
	Attributes map[string]json.RawMessage
}

type AuditRecord struct {
	ID         uint32                     `json:"id"`
	Layer      uint8                      `json:"layer"`
	Attributes map[string]json.RawMessage `json:"attributes"`
}

// Essential fields preserve current conversion/filter/label behavior. This mode
// intentionally discards other attributes and is never presented as lossless.
var essential = map[string]bool{}

func init() {
	for _, k := range []string{"OBJECTID", "STATEABBR", "PRINMERCD", "PRINMER", "TWNSHPNO", "TWNSHPDIR", "TWNSHPFRAC", "RANGENO", "RANGEDIR", "RANGEFRAC", "PLSSID", "TWNSHPLAB", "FRSTDIVID", "FRSTDIVTYP", "FRSTDIVTXT", "FRSTDIVNO", "FRSTDIVLAB", "SECDIVID", "SECDIVTYP", "SECDIVNO", "SECDIVLAB", "SECDIVNOTE", "QSEC", "QQSEC", "GOVLOT", "SURVID"} {
		essential[k] = true
	}
}

func putVar(b *bytes.Buffer, n uint64) {
	var v [10]byte
	k := binary.PutUvarint(v[:], n)
	b.Write(v[:k])
}
func prefix(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

func encode(records []Record, dictionary, conversionOnly bool) ([]byte, error) {
	var out bytes.Buffer
	keysSet := map[string]bool{}
	for _, r := range records {
		putVar(&out, uint64(r.ID))
		out.WriteByte(r.Layer)
		for k := range r.Attributes {
			if !conversionOnly || essential[k] {
				keysSet[k] = true
			}
		}
	}
	keys := make([]string, 0, len(keysSet))
	for k := range keysSet {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	putVar(&out, uint64(len(keys)))
	for _, key := range keys {
		putVar(&out, uint64(len(key)))
		out.WriteString(key)
		if !dictionary {
			for _, r := range records {
				raw, ok := r.Attributes[key]
				if !ok {
					putVar(&out, 0)
				} else {
					if len(raw) == 0 {
						return nil, errors.New("present attributes need a JSON value")
					}
					putVar(&out, uint64(len(raw)+1))
					out.Write(raw)
				}
			}
			continue
		}
		dict := map[string]uint32{}
		for _, r := range records {
			if raw, ok := r.Attributes[key]; ok {
				if len(raw) == 0 {
					return nil, errors.New("present attributes need a JSON value")
				}
				dict[string(raw)] = 0
			}
		}
		values := make([]string, 0, len(dict))
		for v := range dict {
			values = append(values, v)
		}
		sort.Strings(values)
		putVar(&out, uint64(len(values)))
		last := ""
		for i, v := range values {
			dict[v] = uint32(i + 1)
			p := prefix(last, v)
			putVar(&out, uint64(p))
			putVar(&out, uint64(len(v)-p))
			out.WriteString(v[p:])
			last = v
		}
		for _, r := range records {
			if raw, ok := r.Attributes[key]; ok {
				putVar(&out, uint64(dict[string(raw)]))
			} else {
				putVar(&out, 0)
			}
		}
	}
	if out.Len() > maxBlockBytes {
		return nil, errors.New("attribute block exceeds size limit")
	}
	return out.Bytes(), nil
}

type cursor struct {
	b []byte
	i int
}

func (c *cursor) v() (uint64, error) {
	if c.i >= len(c.b) {
		return 0, errors.New("truncated column block")
	}
	n, k := binary.Uvarint(c.b[c.i:])
	if k <= 0 {
		return 0, errors.New("invalid column integer")
	}
	c.i += k
	return n, nil
}
func (c *cursor) take(n uint64) ([]byte, error) {
	if n > uint64(len(c.b)-c.i) {
		return nil, errors.New("truncated column value")
	}
	v := c.b[c.i : c.i+int(n)]
	c.i += int(n)
	return v, nil
}

type column struct {
	key    string
	values [][]byte
	codes  []uint32
}

func (c column) value(row int) []byte {
	if c.codes == nil {
		return c.values[row+1]
	}
	return c.values[c.codes[row]]
}

type block struct {
	ids    []uint32
	layers []byte
	cols   []column
}

func decode(raw []byte, nrows int, dictionary bool) (*block, error) {
	if nrows < 1 || nrows > 8192 || len(raw) > maxBlockBytes {
		return nil, errors.New("invalid column block dimensions")
	}
	c := cursor{b: raw}
	b := &block{ids: make([]uint32, nrows), layers: make([]byte, nrows)}
	for i := range b.ids {
		id, e := c.v()
		if e != nil {
			return nil, e
		}
		if id == 0 || id > 1<<32-1 || (i > 0 && id <= uint64(b.ids[i-1])) {
			return nil, errors.New("invalid feature ID order")
		}
		b.ids[i] = uint32(id)
		layer, e := c.take(1)
		if e != nil {
			return nil, e
		}
		b.layers[i] = layer[0]
	}
	ncols, e := c.v()
	if e != nil {
		return nil, e
	}
	if ncols > 1024 {
		return nil, errors.New("too many attribute columns")
	}
	for j := uint64(0); j < ncols; j++ {
		length, e := c.v()
		if e != nil {
			return nil, e
		}
		key, e := c.take(length)
		if e != nil {
			return nil, e
		}
		col := column{key: string(key)}
		if !dictionary {
			col.values = make([][]byte, nrows+1)
			for i := 0; i < nrows; i++ {
				n, e := c.v()
				if e != nil {
					return nil, e
				}
				if n != 0 {
					v, e := c.take(n - 1)
					if e != nil {
						return nil, e
					}
					col.values[i+1] = v
				}
			}
		} else {
			n, e := c.v()
			if e != nil {
				return nil, e
			}
			if n > uint64(nrows) {
				return nil, errors.New("invalid column dictionary size")
			}
			start := c.i
			total := uint64(0)
			previous := uint64(0)
			for k := uint64(0); k < n; k++ {
				p, e := c.v()
				if e != nil {
					return nil, e
				}
				s, e := c.v()
				if e != nil {
					return nil, e
				}
				if p > previous {
					return nil, errors.New("invalid dictionary prefix")
				}
				if _, e := c.take(s); e != nil {
					return nil, e
				}
				previous = p + s
				total += previous
				if total > maxBlockBytes {
					return nil, errors.New("dictionary expansion too large")
				}
			}
			c.i = start
			buf := make([]byte, 0, int(total))
			col.values = make([][]byte, n+1)
			var last []byte
			for k := uint64(0); k < n; k++ {
				p, _ := c.v()
				s, _ := c.v()
				suffix, _ := c.take(s)
				begin := len(buf)
				buf = append(buf, last[:int(p)]...)
				buf = append(buf, suffix...)
				last = buf[begin:]
				col.values[k+1] = last
			}
			col.codes = make([]uint32, nrows)
			for i := range col.codes {
				v, e := c.v()
				if e != nil {
					return nil, e
				}
				if v > n {
					return nil, errors.New("column code outside dictionary")
				}
				col.codes[i] = uint32(v)
			}
		}
		b.cols = append(b.cols, col)
	}
	if c.i != len(raw) {
		return nil, errors.New("trailing column block data")
	}
	return b, nil
}

func verify(b *block, records []Record, conversionOnly bool) error {
	for i, r := range records {
		if b.ids[i] != r.ID || b.layers[i] != r.Layer {
			return errors.New("column record identity changed")
		}
		seen := 0
		for _, c := range b.cols {
			want, exists := r.Attributes[c.key]
			got := c.value(i)
			if (got != nil) != exists || !bytes.Equal(got, want) {
				return fmt.Errorf("attribute %s changed for feature %d", c.key, r.ID)
			}
			if exists {
				seen++
			}
		}
		wantCount := 0
		for k := range r.Attributes {
			if !conversionOnly || essential[k] {
				wantCount++
			}
		}
		if seen != wantCount {
			return errors.New("attribute set changed")
		}
	}
	return nil
}
