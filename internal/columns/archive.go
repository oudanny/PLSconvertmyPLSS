package columns

import (
	"bytes"
	"compress/zlib"
	"container/list"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
)

type Metadata struct {
	Format    string          `json:"format"`
	Codec     string          `json:"codec"`
	BlockSize int             `json:"block_size"`
	Essential bool            `json:"essential"`
	Records   uint64          `json:"records"`
	Source    json.RawMessage `json:"source"`
	Audit     []AuditRecord   `json:"unlocated,omitempty"`
}

type Options struct {
	BlockSize  int
	Dictionary bool
	Essential  bool
	Source     json.RawMessage
	Verify     bool
}

type directoryEntry struct {
	first, last uint32
	offset      uint64
	length      uint32
}
type Writer struct {
	out      io.WriteSeeker
	opts     Options
	meta     Metadata
	pending  []Record
	index    []directoryEntry
	zip      *zlib.Writer
	zipped   bytes.Buffer
	lastID   uint32
	finished bool
}

func NewWriter(out io.WriteSeeker, opts Options) (*Writer, error) {
	if opts.BlockSize == 0 {
		opts.BlockSize = 1024
	}
	if opts.BlockSize < 1 || opts.BlockSize > 8192 {
		return nil, errors.New("block size must be 1–8192")
	}
	if _, e := out.Seek(0, io.SeekStart); e != nil {
		return nil, e
	}
	if _, e := out.Write(make([]byte, 32)); e != nil {
		return nil, e
	}
	codec := "plain-columns"
	if opts.Dictionary {
		codec = "dictionary-prefix"
	}
	w := &Writer{out: out, opts: opts, meta: Metadata{Format: "PLSCOL01", Codec: codec, BlockSize: opts.BlockSize, Essential: opts.Essential, Source: opts.Source}}
	var e error
	w.zip, e = zlib.NewWriterLevel(&w.zipped, zlib.BestSpeed)
	return w, e
}

func (w *Writer) Add(r Record) error {
	if w.finished {
		return errors.New("column writer is finished")
	}
	if r.ID == 0 || r.ID <= w.lastID {
		return errors.New("feature IDs must increase")
	}
	if r.Layer < 1 || r.Layer > 3 || r.Attributes == nil {
		return errors.New("invalid attribute record")
	}
	w.lastID = r.ID
	w.pending = append(w.pending, r)
	w.meta.Records++
	if len(w.pending) >= w.opts.BlockSize {
		return w.flush()
	}
	return nil
}

func (w *Writer) AddUnlocated(r AuditRecord) { w.meta.Audit = append(w.meta.Audit, r) }

func (w *Writer) flush() error {
	if len(w.pending) == 0 {
		return nil
	}
	raw, e := encode(w.pending, w.opts.Dictionary, w.opts.Essential)
	if e != nil {
		return e
	}
	if w.opts.Verify {
		b, e := decode(raw, len(w.pending), w.opts.Dictionary)
		if e != nil {
			return e
		}
		if e = verify(b, w.pending, w.opts.Essential); e != nil {
			return e
		}
	}
	w.zipped.Reset()
	w.zip.Reset(&w.zipped)
	if _, e = w.zip.Write(raw); e != nil {
		return e
	}
	if e = w.zip.Close(); e != nil {
		return e
	}
	position, e := w.out.Seek(0, io.SeekCurrent)
	if e != nil {
		return e
	}
	var header [16]byte
	binary.LittleEndian.PutUint32(header[0:4], uint32(len(raw)))
	binary.LittleEndian.PutUint32(header[4:8], uint32(w.zipped.Len()))
	binary.LittleEndian.PutUint32(header[8:12], uint32(len(w.pending)))
	binary.LittleEndian.PutUint32(header[12:16], w.pending[0].ID)
	if _, e = w.out.Write(header[:]); e != nil {
		return e
	}
	if _, e = w.out.Write(w.zipped.Bytes()); e != nil {
		return e
	}
	w.index = append(w.index, directoryEntry{first: w.pending[0].ID, last: w.pending[len(w.pending)-1].ID, offset: uint64(position), length: uint32(16 + w.zipped.Len())})
	for i := range w.pending {
		w.pending[i] = Record{}
	}
	w.pending = w.pending[:0]
	return nil
}

func (w *Writer) Finish() (Metadata, error) {
	if w.finished {
		return Metadata{}, errors.New("column writer is finished")
	}
	if e := w.flush(); e != nil {
		return Metadata{}, e
	}
	footer, e := w.out.Seek(0, io.SeekCurrent)
	if e != nil {
		return Metadata{}, e
	}
	meta, e := json.Marshal(w.meta)
	if e != nil {
		return Metadata{}, e
	}
	var tail bytes.Buffer
	var n [4]byte
	binary.LittleEndian.PutUint32(n[:], uint32(len(meta)))
	tail.Write(n[:])
	tail.Write(meta)
	binary.LittleEndian.PutUint32(n[:], uint32(len(w.index)))
	tail.Write(n[:])
	for _, v := range w.index {
		var entry [20]byte
		binary.LittleEndian.PutUint32(entry[0:4], v.first)
		binary.LittleEndian.PutUint32(entry[4:8], v.last)
		binary.LittleEndian.PutUint64(entry[8:16], v.offset)
		binary.LittleEndian.PutUint32(entry[16:20], v.length)
		tail.Write(entry[:])
	}
	tail.WriteString("COLEND01")
	if _, e = w.out.Write(tail.Bytes()); e != nil {
		return Metadata{}, e
	}
	var header [32]byte
	copy(header[:8], "PLSCOL01")
	if w.opts.Dictionary {
		header[8] = 1
	}
	if w.opts.Essential {
		header[9] = 1
	}
	binary.LittleEndian.PutUint32(header[12:16], uint32(w.opts.BlockSize))
	binary.LittleEndian.PutUint64(header[16:24], uint64(footer))
	binary.LittleEndian.PutUint64(header[24:32], w.meta.Records)
	if _, e = w.out.Seek(0, io.SeekStart); e != nil {
		return Metadata{}, e
	}
	if _, e = w.out.Write(header[:]); e != nil {
		return Metadata{}, e
	}
	_, e = w.out.Seek(0, io.SeekEnd)
	w.finished = true
	return w.meta, e
}

type cacheEntry struct {
	index int
	b     *block
}
type Reader struct {
	source     io.ReaderAt
	size       int64
	dictionary bool
	meta       Metadata
	index      []directoryEntry
	mu         sync.Mutex
	cache      map[int]*list.Element
	lru        *list.List
	cacheLimit int
}

// OpenReader accepts a whole file or an io.SectionReader embedded in another
// archive. Offsets are relative to this section, and the source stays owned by
// the caller. At most four decoded blocks are cached by default.
func OpenReader(source io.ReaderAt, size int64) (*Reader, error) {
	if size < 48 {
		return nil, errors.New("attribute archive is truncated")
	}
	var header [32]byte
	if _, e := source.ReadAt(header[:], 0); e != nil {
		return nil, e
	}
	if string(header[:8]) != "PLSCOL01" {
		return nil, errors.New("not a PLSCOL01 attribute archive")
	}
	footer := binary.LittleEndian.Uint64(header[16:24])
	if footer < 32 || footer > uint64(size-16) || uint64(size)-footer > 16<<20 {
		return nil, errors.New("invalid attribute directory")
	}
	tail := make([]byte, int(uint64(size)-footer))
	if _, e := source.ReadAt(tail, int64(footer)); e != nil {
		return nil, e
	}
	if string(tail[len(tail)-8:]) != "COLEND01" {
		return nil, errors.New("incomplete attribute archive")
	}
	m := int(binary.LittleEndian.Uint32(tail[:4]))
	if m > len(tail)-16 {
		return nil, errors.New("invalid attribute metadata length")
	}
	r := &Reader{source: source, size: size, dictionary: header[8] == 1, cache: map[int]*list.Element{}, lru: list.New(), cacheLimit: 4}
	if e := json.Unmarshal(tail[4:4+m], &r.meta); e != nil {
		return nil, e
	}
	n := int(binary.LittleEndian.Uint32(tail[4+m : 8+m]))
	if n > (len(tail)-m-16)/20 || m+16+n*20 != len(tail) {
		return nil, errors.New("invalid attribute index length")
	}
	for i := 0; i < n; i++ {
		b := tail[8+m+i*20:]
		v := directoryEntry{first: binary.LittleEndian.Uint32(b[:4]), last: binary.LittleEndian.Uint32(b[4:8]), offset: binary.LittleEndian.Uint64(b[8:16]), length: binary.LittleEndian.Uint32(b[16:20])}
		if v.first == 0 || v.last < v.first || v.offset < 32 || v.offset+uint64(v.length) > footer || (i > 0 && v.first <= r.index[i-1].last) {
			return nil, errors.New("invalid attribute block entry")
		}
		r.index = append(r.index, v)
	}
	if r.meta.Records != binary.LittleEndian.Uint64(header[24:32]) {
		return nil, errors.New("attribute record count mismatch")
	}
	return r, nil
}

func (r *Reader) Metadata() Metadata       { return r.meta }
func (r *Reader) Unlocated() []AuditRecord { return r.meta.Audit }
func (r *Reader) SetCacheBlocks(n int) error {
	if n < 0 || n > 64 {
		return errors.New("cache must contain 0–64 blocks")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cacheLimit = n
	for r.lru.Len() > n {
		last := r.lru.Back()
		delete(r.cache, last.Value.(cacheEntry).index)
		r.lru.Remove(last)
	}
	return nil
}

func (r *Reader) load(index int) (*block, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e := r.cache[index]; e != nil {
		r.lru.MoveToFront(e)
		return e.Value.(cacheEntry).b, nil
	}
	v := r.index[index]
	if v.length < 16 || v.length > maxBlockBytes {
		return nil, errors.New("invalid attribute block length")
	}
	data := make([]byte, v.length)
	if _, e := r.source.ReadAt(data, int64(v.offset)); e != nil {
		return nil, e
	}
	rawlen := binary.LittleEndian.Uint32(data[:4])
	ziplen := binary.LittleEndian.Uint32(data[4:8])
	rows := binary.LittleEndian.Uint32(data[8:12])
	if rawlen > maxBlockBytes || ziplen+16 != v.length {
		return nil, errors.New("invalid attribute compression header")
	}
	z, e := zlib.NewReader(bytes.NewReader(data[16:]))
	if e != nil {
		return nil, e
	}
	raw, e := io.ReadAll(io.LimitReader(z, int64(rawlen)+1))
	z.Close()
	if e != nil {
		return nil, e
	}
	if len(raw) != int(rawlen) {
		return nil, errors.New("attribute block decompressed length mismatch")
	}
	b, e := decode(raw, int(rows), r.dictionary)
	if e != nil {
		return nil, e
	}
	if b.ids[0] != v.first || b.ids[len(b.ids)-1] != v.last {
		return nil, errors.New("attribute block IDs disagree with directory")
	}
	if r.cacheLimit > 0 {
		r.cache[index] = r.lru.PushFront(cacheEntry{index, b})
		if r.lru.Len() > r.cacheLimit {
			last := r.lru.Back()
			delete(r.cache, last.Value.(cacheEntry).index)
			r.lru.Remove(last)
		}
	}
	return b, nil
}

func (r *Reader) findRow(id uint32) (*block, int, error) {
	i := sort.Search(len(r.index), func(i int) bool { return r.index[i].last >= id })
	if i == len(r.index) || id < r.index[i].first {
		return nil, 0, fmt.Errorf("feature %d is not in attribute archive", id)
	}
	b, e := r.load(i)
	if e != nil {
		return nil, 0, e
	}
	row := sort.Search(len(b.ids), func(i int) bool { return b.ids[i] >= id })
	if row == len(b.ids) || b.ids[row] != id {
		return nil, 0, fmt.Errorf("feature %d is not in attribute block", id)
	}
	return b, row, nil
}

// Attributes preserves exact strings, JSON numbers (json.Number), null, and
// missing keys. Returned maps are independent; dictionary storage is immutable.
func (r *Reader) Attributes(id uint32) (map[string]any, error) {
	b, row, e := r.findRow(id)
	if e != nil {
		return nil, e
	}
	out := make(map[string]any, len(b.cols))
	for _, col := range b.cols {
		v := col.value(row)
		if v == nil {
			continue
		}
		value, e := decodeValue(v)
		if e != nil {
			return nil, fmt.Errorf("attribute %s: %w", col.key, e)
		}
		out[col.key] = value
	}
	return out, nil
}

func decodeValue(v []byte) (any, error) {
	if len(v) == 0 {
		return nil, errors.New("empty JSON value")
	}
	switch v[0] {
	case '"':
		var s string
		if e := json.Unmarshal(v, &s); e != nil {
			return nil, e
		}
		return s, nil
	case 'n':
		if bytes.Equal(v, []byte("null")) {
			return nil, nil
		}
	case 't':
		if bytes.Equal(v, []byte("true")) {
			return true, nil
		}
	case 'f':
		if bytes.Equal(v, []byte("false")) {
			return false, nil
		}
	case '[', '{':
		var value any
		d := json.NewDecoder(bytes.NewReader(v))
		d.UseNumber()
		e := d.Decode(&value)
		return value, e
	default:
		if json.Valid(v) {
			return json.Number(string(v)), nil
		}
	}
	return nil, errors.New("invalid JSON value")
}
