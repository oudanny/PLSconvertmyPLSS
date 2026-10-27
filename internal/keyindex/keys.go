// Package keyindex is an experimental lossless cadastral-ID index. It factors
// township and division prefixes instead of repeating full IDs for every row.
package keyindex

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/bits"
	"sort"
	"strings"
	"unsafe"
)

type Part struct {
	Text   string `json:"text"`
	Append bool   `json:"append,omitempty"`
}
type Source struct {
	ID                              uint32
	Layer                           uint8
	Township, First, Second, Survey string
}
type codes struct{ values [3]uint32 }
type Builder struct {
	dicts      [3][]Part
	maps       [3]map[Part]uint32
	rows       []codes
	layers     [3]uint32
	lastLayer  uint8
	surveys    map[uint32]string
	exceptions map[string][]uint32
	postings   [][]uint32
}

func NewBuilder() *Builder {
	b := &Builder{surveys: map[uint32]string{}, exceptions: map[string][]uint32{}}
	for i := range b.maps {
		b.maps[i] = map[Part]uint32{{}: 0}
		b.dicts[i] = []Part{{}}
	}
	b.postings = [][]uint32{nil}
	return b
}
func (b *Builder) intern(field int, p Part) uint32 {
	if p == (Part{}) {
		return 0
	}
	if id, ok := b.maps[field][p]; ok {
		return id
	}
	id := uint32(len(b.dicts[field]))
	b.maps[field][p] = id
	b.dicts[field] = append(b.dicts[field], p)
	if field == 0 {
		b.postings = append(b.postings, nil)
	}
	return id
}
func part(value, parent string) Part {
	if value == "" {
		return Part{}
	}
	if parent != "" && strings.HasPrefix(value, parent) {
		return Part{Text: value[len(parent):], Append: true}
	}
	return Part{Text: value}
}
func (b *Builder) Add(s Source) error {
	if s.ID != uint32(len(b.rows)+1) || s.Layer < 1 || s.Layer > 3 || s.Layer < b.lastLayer {
		return errors.New("key rows must be consecutive IDs grouped by ascending layer")
	}
	t := b.intern(0, Part{Text: s.Township})
	row := codes{values: [3]uint32{t, b.intern(1, part(s.First, s.Township)), b.intern(2, part(s.Second, s.First))}}
	b.rows = append(b.rows, row)
	b.layers[s.Layer-1]++
	b.lastLayer = s.Layer
	b.postings[t] = append(b.postings[t], s.ID)
	if s.Survey != "" {
		b.surveys[s.ID] = s.Survey
		b.exceptions["4:"+s.Survey] = append(b.exceptions["4:"+s.Survey], s.ID)
	}
	for i, v := range []string{s.First, s.Second} {
		if v != "" && (s.Township == "" || !strings.HasPrefix(v, s.Township)) {
			key := fmt.Sprintf("%d:%s", i+2, v)
			b.exceptions[key] = append(b.exceptions[key], s.ID)
		}
	}
	return nil
}

type manifest struct {
	Count      uint32              `json:"count"`
	Layers     [3]uint32           `json:"layers"`
	Bits       [3]uint8            `json:"bits"`
	Width      uint8               `json:"width"`
	Dicts      [3][]Part           `json:"dictionaries"`
	Surveys    map[uint32]string   `json:"surveys,omitempty"`
	Exceptions map[string][]uint32 `json:"exceptions,omitempty"`
}

func (b *Builder) WriteTo(w io.Writer) (int64, error) {
	return b.writeVersion(w, 2)
}

// writeVersion retains the v1 encoder only for compatibility tests. New files
// use v2's group directory so opening never has to decode all feature postings.
func (b *Builder) writeVersion(w io.Writer, version int) (int64, error) {
	m := manifest{Count: uint32(len(b.rows)), Layers: b.layers, Dicts: b.dicts, Surveys: b.surveys, Exceptions: b.exceptions}
	total := 0
	for i, d := range b.dicts {
		m.Bits[i] = uint8(bits.Len32(uint32(len(d) - 1)))
		total += int(m.Bits[i])
	}
	if total > 64 {
		return 0, errors.New("key dictionaries exceed 64-bit record capacity")
	}
	m.Width = uint8((total + 7) / 8)
	if m.Width == 0 {
		m.Width = 1
	}
	doc, err := json.Marshal(m)
	if err != nil {
		return 0, err
	}
	var out bytes.Buffer
	if version == 1 {
		out.WriteString("PLSSKEY1")
	} else {
		out.WriteString("PLSSKEY2")
	}
	_ = binary.Write(&out, binary.LittleEndian, uint32(len(doc)))
	out.Write(doc)
	for _, r := range b.rows {
		var code uint64
		shift := uint(0)
		for i, v := range r.values {
			code |= uint64(v) << shift
			shift += uint(m.Bits[i])
		}
		for j := uint8(0); j < m.Width; j++ {
			out.WriteByte(byte(code >> (8 * j)))
		}
	}
	var postingData bytes.Buffer
	offsets := make([]uint32, 0, len(b.postings)+1)
	var tmp [10]byte
	for _, ids := range b.postings {
		if uint64(postingData.Len()) > math.MaxUint32 {
			return 0, errors.New("posting payload exceeds uint32 directory capacity")
		}
		offsets = append(offsets, uint32(postingData.Len()))
		n := binary.PutUvarint(tmp[:], uint64(len(ids)))
		postingData.Write(tmp[:n])
		previous := uint32(0)
		for _, id := range ids {
			n = binary.PutUvarint(tmp[:], uint64(id-previous))
			postingData.Write(tmp[:n])
			previous = id
		}
	}
	if uint64(postingData.Len()) > math.MaxUint32 {
		return 0, errors.New("posting payload exceeds uint32 directory capacity")
	}
	offsets = append(offsets, uint32(postingData.Len()))
	if version == 2 {
		for _, offset := range offsets {
			_ = binary.Write(&out, binary.LittleEndian, offset)
		}
	}
	out.Write(postingData.Bytes())
	n, err := w.Write(out.Bytes())
	if err == nil && n != out.Len() {
		err = io.ErrShortWrite
	}
	return int64(n), err
}

// Index reads bit-packed ID records and delta-coded township postings. OpenView
// borrows immutable backing bytes; Open makes an owned copy. Searches may run
// concurrently while the backing bytes remain unchanged and accessible.
type Index struct {
	m             manifest
	data          []byte
	postingData   []byte
	offsets       []byte
	legacyOffsets []uint32
	townships     map[string]uint32
	prefixLengths []int
	exceptions    map[string][]uint32
	ownedData     int
	sourceBytes   int
}

func Open(r io.ReaderAt, size int64) (*Index, error) {
	if size < 12 || size > 1<<30 {
		return nil, errors.New("invalid key-index size")
	}
	data := make([]byte, int(size))
	if _, err := r.ReadAt(data, 0); err != nil {
		return nil, err
	}
	x, err := OpenView(data)
	if err != nil {
		return nil, err
	}
	x.ownedData = len(data)
	return x, nil
}

// OpenView opens an immutable key-index component without copying its packed
// records, posting directory, or postings. The caller keeps data alive and
// unchanged (including keeping a memory mapping open). Version2 opening is
// proportional to the small manifest and group directory, not feature count.
// Legacy v1 data has no directory and necessarily scans postings at opening.
// Validate performs the full semantic integrity audit for either version.
func OpenView(data []byte) (*Index, error) {
	if len(data) < 12 || len(data) > 1<<30 {
		return nil, errors.New("invalid key-index size")
	}
	magic := string(data[:8])
	if magic != "PLSSKEY1" && magic != "PLSSKEY2" {
		return nil, errors.New("invalid key-index magic")
	}
	manifestLength := uint64(binary.LittleEndian.Uint32(data[8:12]))
	if manifestLength > uint64(len(data)-12) {
		return nil, errors.New("truncated key manifest")
	}
	n := int(manifestLength)
	x := &Index{sourceBytes: len(data), townships: map[string]uint32{}}
	if err := json.Unmarshal(data[12:12+n], &x.m); err != nil {
		return nil, err
	}
	m := &x.m
	total := 0
	if m.Width < 1 || m.Width > 8 || uint64(m.Layers[0])+uint64(m.Layers[1])+uint64(m.Layers[2]) != uint64(m.Count) {
		return nil, errors.New("invalid key counts/width")
	}
	for i, d := range m.Dicts {
		if len(d) == 0 || m.Bits[i] > 32 || uint64(len(d)) > uint64(1)<<m.Bits[i] || d[0] != (Part{}) {
			return nil, errors.New("invalid dictionary")
		}
		total += int(m.Bits[i])
	}
	if total > int(m.Width)*8 {
		return nil, errors.New("invalid key bit widths")
	}
	pos := 12 + n
	end := uint64(pos) + uint64(m.Count)*uint64(m.Width)
	if end > uint64(len(data)) {
		return nil, errors.New("truncated key records")
	}
	x.data = data[pos:int(end):int(end)]
	pos = int(end)
	if magic == "PLSSKEY2" {
		directoryBytes := (uint64(len(m.Dicts[0])) + 1) * 4
		if directoryBytes > uint64(len(data)-pos) {
			return nil, errors.New("truncated posting directory")
		}
		directoryEnd := pos + int(directoryBytes)
		x.offsets = data[pos:directoryEnd:directoryEnd]
		x.postingData = data[directoryEnd:len(data):len(data)]
		if binary.LittleEndian.Uint32(x.offsets[:4]) != 0 {
			return nil, errors.New("posting directory must begin at zero")
		}
		previous := uint32(0)
		for i := 1; i <= len(m.Dicts[0]); i++ {
			current := binary.LittleEndian.Uint32(x.offsets[i*4 : i*4+4])
			if current <= previous || uint64(current) > uint64(len(x.postingData)) {
				return nil, errors.New("descending or out-of-range posting offset")
			}
			previous = current
		}
		if uint64(previous) != uint64(len(x.postingData)) {
			return nil, errors.New("trailing or truncated posting data")
		}
	} else {
		x.postingData = data[pos:len(data):len(data)]
		x.legacyOffsets = make([]uint32, len(m.Dicts[0])+1)
		cursor := 0
		for group := range m.Dicts[0] {
			x.legacyOffsets[group] = uint32(cursor)
			consumed, _, err := walkPosting(x.postingData[cursor:], m.Count, false, nil)
			if err != nil {
				return nil, err
			}
			cursor += consumed
		}
		if cursor != len(x.postingData) {
			return nil, errors.New("trailing key-index data")
		}
		x.legacyOffsets[len(m.Dicts[0])] = uint32(cursor)
	}
	lengths := map[int]bool{}
	for i, d := range m.Dicts[0] {
		if d.Append || (i > 0 && d.Text == "") {
			return nil, errors.New("invalid township dictionary entry")
		}
		if i > 0 {
			if _, exists := x.townships[d.Text]; exists {
				return nil, errors.New("duplicate township dictionary entry")
			}
			x.townships[d.Text] = uint32(i)
			lengths[len(d.Text)] = true
		}
	}
	for l := range lengths {
		x.prefixLengths = append(x.prefixLengths, l)
	}
	sort.Ints(x.prefixLengths)
	// Persist exceptions so opening a pack does not scan all feature records.
	x.exceptions = m.Exceptions
	return x, nil
}

// walkPosting validates before slicing or converting deltas to uint32.
func walkPosting(data []byte, max uint32, exact bool, visit func(uint32) error) (int, uint64, error) {
	count, n := binary.Uvarint(data)
	if n <= 0 || count > uint64(max) {
		return 0, 0, errors.New("invalid posting count")
	}
	pos := n
	previous := uint64(0)
	for j := uint64(0); j < count; j++ {
		delta, k := binary.Uvarint(data[pos:])
		if k <= 0 || delta == 0 || delta > uint64(max)-previous {
			return 0, 0, errors.New("invalid posting delta")
		}
		pos += k
		previous += delta
		if visit != nil {
			if err := visit(uint32(previous)); err != nil {
				return 0, 0, err
			}
		}
	}
	if exact && pos != len(data) {
		return 0, 0, errors.New("posting offset does not align with its encoded group")
	}
	return pos, count, nil
}

func (x *Index) posting(group uint32) ([]byte, error) {
	if uint64(group) >= uint64(len(x.m.Dicts[0])) {
		return nil, errors.New("posting group outside dictionary")
	}
	var start, end uint32
	if x.offsets != nil {
		start = binary.LittleEndian.Uint32(x.offsets[int(group)*4:])
		end = binary.LittleEndian.Uint32(x.offsets[(int(group)+1)*4:])
	} else {
		start = x.legacyOffsets[group]
		end = x.legacyOffsets[group+1]
	}
	if start >= end || uint64(end) > uint64(len(x.postingData)) {
		return nil, errors.New("invalid posting offsets")
	}
	return x.postingData[start:end:end], nil
}

func (x *Index) codes(id uint32) ([3]uint32, error) {
	var values [3]uint32
	if id == 0 || id > x.m.Count {
		return values, errors.New("feature ID outside key index")
	}
	p := (int(id) - 1) * int(x.m.Width)
	var packed uint64
	for j := 0; j < int(x.m.Width); j++ {
		packed |= uint64(x.data[p+j]) << uint(8*j)
	}
	for i, b := range x.m.Bits {
		values[i] = uint32(packed & ((uint64(1) << b) - 1))
		packed >>= b
		if values[i] >= uint32(len(x.m.Dicts[i])) {
			return values, errors.New("key dictionary reference out of range")
		}
	}
	if packed != 0 {
		return values, errors.New("nonzero key record padding bits")
	}
	return values, nil
}
func (x *Index) Count() uint32 { return x.m.Count }

// MemoryUsage separates owned backing arrays from an estimate of parsed
// strings/map storage. BorrowedBytes is excluded from MemoryBytes; a mapping
// still consumes resident OS pages when accessed. Go allocator rounding, map
// implementation details and temporary query allocations are not exact here.
type MemoryUsage struct {
	OwnedArrayBytes            int64 `json:"owned_array_bytes"`
	OwnedMetadataEstimateBytes int64 `json:"owned_metadata_estimate_bytes"`
	BorrowedBytes              int64 `json:"borrowed_bytes"`
}

func (x *Index) MemoryStats() MemoryUsage {
	if x == nil {
		return MemoryUsage{}
	}
	s := MemoryUsage{OwnedArrayBytes: int64(x.ownedData) + int64(cap(x.legacyOffsets))*4 + int64(cap(x.prefixLengths))*int64(unsafe.Sizeof(int(0))), OwnedMetadataEstimateBytes: int64(unsafe.Sizeof(*x))}
	if x.ownedData == 0 {
		s.BorrowedBytes = int64(x.sourceBytes)
	}
	for _, dict := range x.m.Dicts {
		s.OwnedArrayBytes += int64(cap(dict)) * int64(unsafe.Sizeof(Part{}))
		for _, p := range dict {
			s.OwnedMetadataEstimateBytes += int64(len(p.Text))
		}
	}
	s.OwnedMetadataEstimateBytes += int64(len(x.townships))*40 + int64(len(x.m.Surveys))*40 + int64(len(x.exceptions))*56
	for _, text := range x.m.Surveys {
		s.OwnedMetadataEstimateBytes += int64(len(text))
	}
	for key, ids := range x.exceptions {
		s.OwnedMetadataEstimateBytes += int64(len(key))
		s.OwnedArrayBytes += int64(cap(ids)) * 4
	}
	return s
}
func (x *Index) MemoryBytes() int {
	s := x.MemoryStats()
	return int(s.OwnedArrayBytes + s.OwnedMetadataEstimateBytes)
}
func (x *Index) BorrowedBytes() int { return int(x.MemoryStats().BorrowedBytes) }
func (x *Index) Layer(id uint32) uint8 {
	if id == 0 || id > x.m.Count {
		return 0
	}
	if id <= x.m.Layers[0] {
		return 1
	}
	if id <= x.m.Layers[0]+x.m.Layers[1] {
		return 2
	}
	return 3
}
func (x *Index) value(c [3]uint32, field int) string {
	p := x.m.Dicts[field][c[field]]
	if field == 0 || !p.Append {
		return p.Text
	}
	return x.value(c, field-1) + p.Text
}
func (x *Index) ID(id uint32) (string, error) {
	c, err := x.codes(id)
	if err != nil {
		return "", err
	}
	if v := x.value(c, 2); v != "" {
		return v, nil
	}
	if v := x.m.Surveys[id]; v != "" {
		return v, nil
	}
	if v := x.value(c, 1); v != "" {
		return v, nil
	}
	return x.value(c, 0), nil
}

// Fields reconstructs only the published ID strings, without ancillary BLM
// attributes. Survey IDs remain independent of the subdivision hierarchy.
func (x *Index) Fields(id uint32) (map[string]any, error) {
	c, err := x.codes(id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"PLSSID": x.value(c, 0), "FRSTDIVID": x.value(c, 1), "SECDIVID": x.value(c, 2), "SURVID": x.m.Surveys[id]}, nil
}
func (x *Index) match(c [3]uint32, field int, value string) bool {
	p := x.m.Dicts[field][c[field]]
	if field == 0 || !p.Append {
		return p.Text == value
	}
	if !strings.HasSuffix(value, p.Text) {
		return false
	}
	return x.match(c, field-1, value[:len(value)-len(p.Text)])
}

// Find resolves exact township, section, division/intersected or survey IDs.
// It scans only delta-coded postings for matching township prefixes, retaining
// duplicate source records; no million-string hash table is needed.
func (x *Index) Find(kind, id string) ([]uint32, error) {
	field, layer := 0, uint8(1)
	switch kind {
	case "township":
	case "section":
		field, layer = 1, 2
	case "division", "intersected":
		field, layer = 2, 3
	case "survey":
		var found []uint32
		for _, row := range x.exceptions["4:"+id] {
			if row == 0 || row > x.m.Count || x.m.Surveys[row] != id {
				return nil, errors.New("invalid survey exception row")
			}
			if x.Layer(row) == 3 {
				found = append(found, row)
			}
		}
		return found, nil
	default:
		return nil, errors.New("unknown ID kind")
	}
	var found []uint32
	for _, n := range x.prefixLengths {
		if len(id) < n {
			continue
		}
		t, ok := x.townships[id[:n]]
		if !ok {
			continue
		}
		data, err := x.posting(t)
		if err != nil {
			return nil, err
		}
		_, _, err = walkPosting(data, x.m.Count, true, func(current uint32) error {
			c, err := x.codes(current)
			if err != nil {
				return err
			}
			if c[0] != t {
				return errors.New("posting row belongs to another township")
			}
			if x.Layer(current) == layer && x.match(c, field, id) {
				found = append(found, current)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if field > 0 {
		for _, row := range x.exceptions[fmt.Sprintf("%d:%s", field+1, id)] {
			c, err := x.codes(row)
			if err != nil {
				return nil, err
			}
			if !x.match(c, field, id) {
				return nil, errors.New("exception row has a different cadastral ID")
			}
			found = append(found, row)
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i] < found[j] })
	out := found[:0]
	for _, id := range found {
		if x.Layer(id) == layer && (len(out) == 0 || out[len(out)-1] != id) {
			out = append(out, id)
		}
	}
	return out, nil
}

// Validate reads every posting and packed record, including groups not yet
// queried. It checks exact directory alignment, positive ordered deltas, row
// coverage and township membership, dictionary references and exception links.
// OpenView deliberately leaves this full-file work to explicit integrity checks.
func (x *Index) Validate() error {
	if x == nil {
		return errors.New("nil key index")
	}
	for row, text := range x.m.Surveys {
		if row == 0 || row > x.m.Count || text == "" {
			return errors.New("invalid survey row")
		}
	}
	for key, ids := range x.exceptions {
		if len(key) < 3 || key[1] != ':' || (key[0] != '2' && key[0] != '3' && key[0] != '4') {
			return errors.New("invalid exception key")
		}
		previous := uint32(0)
		for _, row := range ids {
			if row <= previous || row > x.m.Count {
				return errors.New("invalid exception row order/range")
			}
			previous = row
			if key[0] == '4' {
				if x.m.Surveys[row] != key[2:] {
					return errors.New("survey exception differs from published ID")
				}
			} else {
				c, err := x.codes(row)
				if err != nil {
					return err
				}
				if !x.match(c, int(key[0]-'1'), key[2:]) {
					return errors.New("exception differs from published ID")
				}
			}
		}
	}
	var total uint64
	for group := range x.m.Dicts[0] {
		data, err := x.posting(uint32(group))
		if err != nil {
			return err
		}
		_, count, err := walkPosting(data, x.m.Count, true, func(row uint32) error {
			c, err := x.codes(row)
			if err != nil {
				return err
			}
			if c[0] != uint32(group) {
				return errors.New("posting row belongs to another township")
			}
			// Missing prefix-exception entries would otherwise make reverse ID
			// lookup silently omit published records. Normal appended prefixes
			// avoid allocating reconstructed strings during this audit.
			township := x.m.Dicts[0][c[0]].Text
			for field := 1; field <= 2; field++ {
				p := x.m.Dicts[field][c[field]]
				if p == (Part{}) {
					continue
				}
				if township != "" && ((field == 1 && p.Append) || (field == 2 && p.Append && x.m.Dicts[1][c[1]].Append) || (!p.Append && strings.HasPrefix(p.Text, township))) {
					continue
				}
				value := x.value(c, field)
				if value != "" && (township == "" || !strings.HasPrefix(value, township)) {
					if !containsRow(x.exceptions[fmt.Sprintf("%d:%s", field+1, value)], row) {
						return errors.New("missing cadastral ID exception")
					}
				}
			}
			if survey := x.m.Surveys[row]; survey != "" && !containsRow(x.exceptions["4:"+survey], row) {
				return errors.New("missing survey exception")
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("posting group %d: %w", group, err)
		}
		total += count
	}
	if total != uint64(x.m.Count) {
		return errors.New("postings omit feature records")
	}
	return nil
}
func containsRow(ids []uint32, row uint32) bool {
	i := sort.Search(len(ids), func(i int) bool { return ids[i] >= row })
	return i < len(ids) && ids[i] == row
}
