// Package compactgeom is an experimental geometry-only component format.
// It is separate from the production PLSS pack format.
package compactgeom

import (
	"bytes"
	"compress/zlib"
	"container/list"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sync"

	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

const headerSize = 128
const defaultBlockSize = 128
const maxRawBlock = 128 << 20

var magic = [8]byte{'C', 'G', 'E', 'O', 'M', '0', '1', 0}

// Metadata describes a geometry component. Decimals=0 is bit-exact. Values6
// and7 are explicitly lossy experiments, rounding only XY to that many places.
type Metadata struct {
	Version               uint32      `json:"version"`
	VertexCodec           VertexCodec `json:"vertex_codec"`
	VertexBlockSize       uint32      `json:"vertices_per_block,omitempty"`
	RawVertexBytes        uint64      `json:"raw_vertex_bytes"`
	VertexDirectoryBytes  uint64      `json:"vertex_directory_bytes,omitempty"`
	Features              uint32      `json:"features"`
	Vertices              uint64      `json:"unique_xy_vertices"`
	Decimals              uint32      `json:"quantization_decimals"`
	BlockSize             uint32      `json:"records_per_block"`
	Bytes                 uint64      `json:"file_bytes"`
	VertexBytes           uint64      `json:"vertex_table_bytes"`
	BlockBytes            uint64      `json:"compressed_record_block_bytes"`
	DirectoryBytes        uint64      `json:"directory_bytes"`
	MaxLatitudeError      float64     `json:"maximum_latitude_error_degrees"`
	MaxLongitudeError     float64     `json:"maximum_longitude_error_degrees"`
	CollapsedEdges        uint64      `json:"collapsed_nonzero_xy_edges"`
	CollapsedRings        uint64      `json:"new_zero_area_rings"`
	OrientationChanges    uint64      `json:"ring_orientation_changes"`
	ExtraOrdinateVertices uint64      `json:"vertices_with_extra_ordinates"`
}

type xy struct{ x, y uint64 }
type blockEntry struct {
	offset          uint64
	compressed, raw uint32
}

// Builder interns exact XY bit pairs and writes compressed record blocks.
// Build memory is proportional to the vertex dictionary. Reading requires
// only the small block directory plus one requested block and its vertices.
type Builder struct {
	f               *os.File
	dictionary      map[xy]uint32
	vertices        []xy
	entries         []blockEntry
	records         [][]byte
	buffer          bytes.Buffer
	zip             *zlib.Writer
	meta            Metadata
	closed          bool
	failed          error
	vertexCodec     VertexCodec
	vertexBlockSize uint32
}

func NewBuilder(path string, decimals int) (*Builder, error) {
	return NewBuilderWithOptions(path, BuilderOptions{Decimals: decimals})
}

// NewBuilderWithOptions enables explicitly selected dictionary compression.
// Its zero options preserve the original lossless version1 format.
func NewBuilderWithOptions(path string, options BuilderOptions) (*Builder, error) {
	decimals := options.Decimals
	if decimals != 0 && decimals != 6 && decimals != 7 {
		return nil, errors.New("decimals must be0(lossless),6,or7")
	}
	if err := options.validate(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	b := &Builder{f: f, dictionary: make(map[xy]uint32), meta: Metadata{Decimals: uint32(decimals), BlockSize: defaultBlockSize}, vertexCodec: options.VertexCodec, vertexBlockSize: uint32(options.VertexBlockSize)}
	if b.vertexCodec != VertexRaw && b.vertexBlockSize == 0 {
		b.vertexBlockSize = 256
	}
	b.meta.Version = 1
	if b.vertexCodec != VertexRaw {
		b.meta.Version = 2
	}
	b.meta.VertexCodec, b.meta.VertexBlockSize = b.vertexCodec, b.vertexBlockSize
	b.zip, _ = zlib.NewWriterLevel(&b.buffer, zlib.DefaultCompression)
	if _, err = f.Write(make([]byte, headerSize)); err != nil {
		f.Close()
		return nil, err
	}
	return b, nil
}

func (b *Builder) key(v []float64) xy {
	if b.meta.Decimals == 0 {
		return xy{math.Float64bits(v[0]), math.Float64bits(v[1])}
	}
	scale := math.Pow10(int(b.meta.Decimals))
	x := int32(math.Round(v[0] * scale))
	y := int32(math.Round(v[1] * scale))
	b.meta.MaxLongitudeError = math.Max(b.meta.MaxLongitudeError, math.Abs(v[0]-float64(x)/scale))
	b.meta.MaxLatitudeError = math.Max(b.meta.MaxLatitudeError, math.Abs(v[1]-float64(y)/scale))
	return xy{uint64(uint32(x)), uint64(uint32(y))}
}

func sameVertex(a, c []float64) bool {
	if len(a) != len(c) {
		return false
	}
	for i := range a {
		if math.Float64bits(a[i]) != math.Float64bits(c[i]) {
			return false
		}
	}
	return true
}
func sameXY(a, c []float64) bool {
	return math.Float64bits(a[0]) == math.Float64bits(c[0]) && math.Float64bits(a[1]) == math.Float64bits(c[1])
}
func putU(out []byte, v uint64) []byte { return binary.AppendUvarint(out, v) }
func getU(in []byte, p *int) (uint64, error) {
	if *p >= len(in) {
		return 0, io.ErrUnexpectedEOF
	}
	v, n := binary.Uvarint(in[*p:])
	if n <= 0 {
		return 0, errors.New("invalid varint")
	}
	*p += n
	return v, nil
}
func zigzag(v int64) uint64   { return uint64(v<<1) ^ uint64(v>>63) }
func unzigzag(v uint64) int64 { return int64(v>>1) ^ -int64(v&1) }

// Add requires consecutive source feature IDs beginning at1. Every feature
// remains a separate record, even if its geometry matches another feature.
func (b *Builder) Add(id uint32, g plss.Geometry) error {
	if b.closed {
		return errors.New("builder closed")
	}
	if b.failed != nil {
		return b.failed
	}
	err := b.add(id, g)
	if err != nil {
		b.failed = err
	}
	return err
}
func (b *Builder) add(id uint32, g plss.Geometry) error {
	if id != b.meta.Features+1 || id == 0 {
		return fmt.Errorf("expected consecutive feature%d, got%d", b.meta.Features+1, id)
	}
	if len(g.Rings) == 0 {
		return errors.New("empty geometry")
	}
	variable := false
	for _, ring := range g.Rings {
		if len(ring) < 3 {
			return errors.New("short ring")
		}
		for _, v := range ring {
			if len(v) < 2 || math.IsNaN(v[0]) || math.IsNaN(v[1]) || math.IsInf(v[0], 0) || math.IsInf(v[1], 0) || math.Abs(v[0]) > 180 || math.Abs(v[1]) > 90 {
				return errors.New("invalid XY")
			}
			if len(v) != 2 {
				variable = true
				b.meta.ExtraOrdinateVertices++
			}
		}
	}
	flag := byte(0)
	if variable {
		flag = 1
	}
	out := []byte{flag}
	out = putU(out, uint64(len(g.Rings)))
	for _, ring := range g.Rings {
		closed := sameVertex(ring[0], ring[len(ring)-1])
		header := uint64(len(ring)) << 1
		if closed {
			header |= 1
		}
		out = putU(out, header)
		limit := len(ring)
		if closed {
			limit--
		}
		var previous int64
		keys := make([]xy, len(ring))
		for j, v := range ring {
			keys[j] = b.key(v)
		}
		if b.meta.Decimals != 0 {
			b.auditQuantization(ring, keys)
		}
		for j := 0; j < limit; j++ {
			v := ring[j]
			key := keys[j]
			vertexID, ok := b.dictionary[key]
			if !ok {
				if uint64(len(b.vertices)) >= math.MaxUint32 {
					return errors.New("vertex dictionary exceeds uint32")
				}
				vertexID = uint32(len(b.vertices))
				b.dictionary[key] = vertexID
				b.vertices = append(b.vertices, key)
			}
			if variable {
				out = putU(out, uint64(len(v)))
			}
			out = putU(out, zigzag(int64(vertexID)-previous))
			previous = int64(vertexID)
			for _, extra := range v[2:] {
				out = binary.LittleEndian.AppendUint64(out, math.Float64bits(extra))
			}
		}
	}
	b.records = append(b.records, out)
	b.meta.Features++
	if len(b.records) == int(b.meta.BlockSize) {
		return b.flush()
	}
	return nil
}

func (b *Builder) auditQuantization(ring [][]float64, keys []xy) {
	origin := ring[0]
	qx0, qy0 := float64(int32(uint32(keys[0].x))), float64(int32(uint32(keys[0].y)))
	var oldArea, newArea float64
	for i, a := range ring {
		j := (i + 1) % len(ring)
		c := ring[j]
		if keys[i] == keys[j] && !sameXY(a, c) {
			b.meta.CollapsedEdges++
		}
		oldArea += (a[0]-origin[0])*(c[1]-origin[1]) - (c[0]-origin[0])*(a[1]-origin[1])
		ax, ay := float64(int32(uint32(keys[i].x)))-qx0, float64(int32(uint32(keys[i].y)))-qy0
		cx, cy := float64(int32(uint32(keys[j].x)))-qx0, float64(int32(uint32(keys[j].y)))-qy0
		newArea += ax*cy - cx*ay
	}
	if oldArea != 0 && newArea == 0 {
		b.meta.CollapsedRings++
	}
	if oldArea*newArea < 0 {
		b.meta.OrientationChanges++
	}
}

func (b *Builder) flush() error {
	if len(b.records) == 0 {
		return nil
	}
	raw := putU(nil, uint64(len(b.records)))
	for _, record := range b.records {
		raw = putU(raw, uint64(len(record)))
	}
	for _, record := range b.records {
		raw = append(raw, record...)
	}
	if len(raw) > maxRawBlock {
		return errors.New("geometry block exceeds128MiB")
	}
	b.buffer.Reset()
	b.zip.Reset(&b.buffer)
	if _, err := b.zip.Write(raw); err != nil {
		return err
	}
	if err := b.zip.Close(); err != nil {
		return err
	}
	offset, err := b.f.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if _, err := b.f.Write(b.buffer.Bytes()); err != nil {
		return err
	}
	b.entries = append(b.entries, blockEntry{uint64(offset), uint32(b.buffer.Len()), uint32(len(raw))})
	b.meta.BlockBytes += uint64(b.buffer.Len())
	b.records = b.records[:0]
	return nil
}

func (b *Builder) Snapshot() Metadata {
	m := b.meta
	if !b.closed {
		m.Vertices = uint64(len(b.vertices))
	}
	return m
}

// Finish writes the direct vertex table and block directory, syncs, and closes.
func (b *Builder) Finish() (Metadata, error) {
	return b.finish(uint64(len(b.vertices)), func(id uint32) (xy, error) { return b.vertices[id], nil })
}

func (b *Builder) finish(vertexCount uint64, at func(uint32) (xy, error)) (Metadata, error) {
	if b.closed {
		return Metadata{}, errors.New("builder closed")
	}
	if b.failed != nil {
		return Metadata{}, b.failed
	}
	if err := b.flush(); err != nil {
		return Metadata{}, err
	}
	vertexOffset, err := b.f.Seek(0, io.SeekCurrent)
	if err != nil {
		return Metadata{}, err
	}
	width := 16
	if b.meta.Decimals != 0 {
		width = 8
	}
	vertexEntries, err := b.writeVertices(vertexCount, width, at)
	if err != nil {
		return Metadata{}, err
	}
	vertexEnd, err := b.f.Seek(0, io.SeekCurrent)
	if err != nil {
		return Metadata{}, err
	}
	if err := writeDirectory(b.f, vertexEntries); err != nil {
		return Metadata{}, err
	}
	directoryOffset, err := b.f.Seek(0, io.SeekCurrent)
	if err != nil {
		return Metadata{}, err
	}
	if err := writeDirectory(b.f, b.entries); err != nil {
		return Metadata{}, err
	}
	end, err := b.f.Seek(0, io.SeekCurrent)
	if err != nil {
		return Metadata{}, err
	}
	b.meta.Version = 1
	if b.vertexCodec != VertexRaw {
		b.meta.Version = 2
	}
	b.meta.VertexCodec = b.vertexCodec
	b.meta.VertexBlockSize = b.vertexBlockSize
	b.meta.Vertices = vertexCount
	b.meta.RawVertexBytes = vertexCount * uint64(width)
	b.meta.VertexBytes = uint64(vertexEnd - vertexOffset)
	b.meta.VertexDirectoryBytes = uint64(len(vertexEntries)) * 16
	b.meta.DirectoryBytes = uint64(len(b.entries))*16 + b.meta.VertexDirectoryBytes
	b.meta.Bytes = uint64(end)
	header := make([]byte, headerSize)
	copy(header, magic[:])
	binary.LittleEndian.PutUint32(header[8:12], b.meta.Version)
	binary.LittleEndian.PutUint32(header[12:16], b.meta.Decimals)
	binary.LittleEndian.PutUint32(header[16:20], b.meta.Features)
	binary.LittleEndian.PutUint32(header[20:24], b.meta.BlockSize)
	binary.LittleEndian.PutUint64(header[24:32], b.meta.Vertices)
	binary.LittleEndian.PutUint64(header[32:40], uint64(vertexOffset))
	binary.LittleEndian.PutUint64(header[40:48], uint64(directoryOffset))
	binary.LittleEndian.PutUint32(header[48:52], uint32(len(b.entries)))
	binary.LittleEndian.PutUint32(header[52:56], uint32(width))
	binary.LittleEndian.PutUint64(header[56:64], b.meta.Bytes)
	binary.LittleEndian.PutUint64(header[64:72], math.Float64bits(b.meta.MaxLatitudeError))
	binary.LittleEndian.PutUint64(header[72:80], math.Float64bits(b.meta.MaxLongitudeError))
	binary.LittleEndian.PutUint64(header[80:88], b.meta.CollapsedEdges)
	binary.LittleEndian.PutUint64(header[88:96], b.meta.CollapsedRings)
	binary.LittleEndian.PutUint64(header[96:104], b.meta.ExtraOrdinateVertices)
	binary.LittleEndian.PutUint64(header[104:112], b.meta.OrientationChanges)
	if b.meta.Version == 2 {
		binary.LittleEndian.PutUint32(header[112:116], b.vertexBlockSize)
		binary.LittleEndian.PutUint32(header[116:120], uint32(b.vertexCodec))
		binary.LittleEndian.PutUint64(header[120:128], uint64(vertexEnd))
	}
	if _, err := b.f.WriteAt(header, 0); err != nil {
		return Metadata{}, err
	}
	if err := b.f.Sync(); err != nil {
		return Metadata{}, err
	}
	if err := b.Close(); err != nil {
		return Metadata{}, err
	}
	b.dictionary = nil
	b.vertices = nil
	b.entries = nil
	b.records = nil
	return b.meta, nil
}
func (b *Builder) Close() error {
	if b.closed {
		return nil
	}
	b.closed = true
	_ = b.zip.Close()
	return b.f.Close()
}

// Reader uses a caller-owned ReaderAt (including io.SectionReader for an
// embedded component). Only the block directory is resident. Record is safe
// for concurrent calls; the caller keeps the backing file alive.
type Reader struct {
	r             io.ReaderAt
	meta          Metadata
	vertexOffset  uint64
	vertexWidth   uint32
	entries       []blockEntry
	pool          sync.Pool
	vertexPages   *byteCache
	blocks        *byteCache
	vertexData    []byte
	vertexEntries []blockEntry
	vertexBlocks  *byteCache
	vertexPool    sync.Pool
}

// ReaderOptions bounds independent component caches. Raw vertex pages are
// 4096bytes. Zlib vertex blocks decode to at most VertexBlockSize*16 bytes;
// bit-packed blocks retain their encoded bytes (at most that size plus22).
// Geometry blocks vary with source complexity. Zero disables a cache.
// PreloadVertices uses Metadata.RawVertexBytes additional payload RAM.
type ReaderOptions struct {
	VertexCachePages    int
	GeometryCacheBlocks int
	PreloadVertices     bool
	VertexCacheBlocks   int
}
type cacheItem struct {
	key  uint64
	data []byte
}
type byteCache struct {
	mu       sync.Mutex
	capacity int
	entries  map[uint64]*list.Element
	order    list.List
}

func newByteCache(capacity int) *byteCache {
	return &byteCache{capacity: capacity, entries: map[uint64]*list.Element{}}
}
func (c *byteCache) get(key uint64) ([]byte, bool) {
	if c.capacity == 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(e)
	return e.Value.(cacheItem).data, true
}
func (c *byteCache) put(key uint64, data []byte) {
	if c.capacity == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		c.order.MoveToFront(e)
		return
	}
	c.entries[key] = c.order.PushFront(cacheItem{key, data})
	if len(c.entries) > c.capacity {
		e := c.order.Back()
		delete(c.entries, e.Value.(cacheItem).key)
		c.order.Remove(e)
	}
}

type inflateState struct {
	reader     io.ReadCloser
	buffer     bytes.Buffer
	compressed []byte
}

func OpenReaderAt(r io.ReaderAt, size int64) (*Reader, error) {
	return OpenReaderAtWithOptions(r, size, ReaderOptions{VertexCachePages: 64, VertexCacheBlocks: 64, GeometryCacheBlocks: 16})
}

func OpenReaderAtWithOptions(r io.ReaderAt, size int64, options ReaderOptions) (*Reader, error) {
	if options.VertexCachePages < 0 || options.GeometryCacheBlocks < 0 || options.VertexCacheBlocks < 0 {
		return nil, errors.New("negative cache capacity")
	}
	if size < headerSize {
		return nil, errors.New("truncated geometry component")
	}
	header := make([]byte, headerSize)
	if _, err := r.ReadAt(header, 0); err != nil {
		return nil, err
	}
	version := binary.LittleEndian.Uint32(header[8:12])
	if !bytes.Equal(header[:8], magic[:]) || (version != 1 && version != 2) {
		return nil, errors.New("unsupported geometry component")
	}
	u32 := func(off int) uint32 { return binary.LittleEndian.Uint32(header[off : off+4]) }
	u64 := func(off int) uint64 { return binary.LittleEndian.Uint64(header[off : off+8]) }
	m := Metadata{Version: version, Decimals: u32(12), Features: u32(16), BlockSize: u32(20), Vertices: u64(24), Bytes: u64(56), MaxLatitudeError: math.Float64frombits(u64(64)), MaxLongitudeError: math.Float64frombits(u64(72)), CollapsedEdges: u64(80), CollapsedRings: u64(88), ExtraOrdinateVertices: u64(96), OrientationChanges: u64(104)}
	vertexOffset, directoryOffset, nblocks, width := u64(32), u64(40), u32(48), u32(52)
	if m.Decimals != 0 && m.Decimals != 6 && m.Decimals != 7 {
		return nil, errors.New("invalid quantization mode")
	}
	expectedWidth := uint32(16)
	if m.Decimals != 0 {
		expectedWidth = 8
	}
	// Check ranges before arithmetic/allocation so malicious component offsets
	// cannot wrap into a small file and request a huge directory allocation.
	if vertexOffset > uint64(size) || directoryOffset > uint64(size) || width == 0 || uint64(nblocks) > (uint64(size)-directoryOffset)/16 {
		return nil, errors.New("geometry sections exceed component bounds")
	}
	if width != expectedWidth || m.BlockSize == 0 || m.BlockSize > 65536 || uint64(nblocks) != (uint64(m.Features)+uint64(m.BlockSize)-1)/uint64(m.BlockSize) || m.Bytes != uint64(size) || vertexOffset < headerSize || m.Vertices > math.MaxUint32 || directoryOffset < vertexOffset || directoryOffset+uint64(nblocks)*16 != uint64(size) {
		return nil, errors.New("invalid geometry offsets/counts")
	}
	m.RawVertexBytes = m.Vertices * uint64(width)
	m.VertexBytes = directoryOffset - vertexOffset
	if version == 1 && directoryOffset-vertexOffset != m.RawVertexBytes {
		return nil, errors.New("invalid raw vertex table size")
	}
	m.DirectoryBytes = uint64(nblocks) * 16
	m.BlockBytes = vertexOffset - headerSize
	encoded := make([]byte, int(nblocks)*16)
	if len(encoded) > 0 {
		if _, err := r.ReadAt(encoded, int64(directoryOffset)); err != nil {
			return nil, err
		}
	}
	reader := &Reader{r: r, meta: m, vertexOffset: vertexOffset, vertexWidth: width, entries: make([]blockEntry, nblocks), vertexPages: newByteCache(options.VertexCachePages), blocks: newByteCache(options.GeometryCacheBlocks), vertexBlocks: newByteCache(options.VertexCacheBlocks)}
	if version == 2 {
		if err := reader.openVertexDirectory(u32(112), VertexCodec(u32(116)), u64(120), directoryOffset); err != nil {
			return nil, err
		}
		m = reader.meta
	}
	var previousEnd uint64 = headerSize
	for i := range reader.entries {
		data := encoded[i*16 : i*16+16]
		entry := blockEntry{binary.LittleEndian.Uint64(data[:8]), binary.LittleEndian.Uint32(data[8:12]), binary.LittleEndian.Uint32(data[12:])}
		if entry.offset != previousEnd || entry.offset+uint64(entry.compressed) > vertexOffset || entry.raw > maxRawBlock || entry.compressed == 0 {
			return nil, errors.New("invalid block directory")
		}
		reader.entries[i] = entry
		previousEnd = entry.offset + uint64(entry.compressed)
	}
	if previousEnd != vertexOffset {
		return nil, errors.New("incomplete block directory")
	}
	if options.PreloadVertices {
		if m.RawVertexBytes > 2<<30 {
			return nil, errors.New("vertex preload exceeds2GiB; use bounded page cache")
		}
		reader.vertexData = make([]byte, int(m.RawVertexBytes))
		if m.Version == 2 {
			for i := range reader.vertexEntries {
				data, err := reader.readVertexBlock(uint64(i))
				if err != nil {
					return nil, err
				}
				if m.VertexCodec == VertexXORBitpack {
					count := int(reader.vertexEntries[i].raw) / 16
					for j := 0; j < count; j++ {
						v := packedVertex(data, count, j)
						p := reader.vertexData[(uint64(i)*uint64(m.VertexBlockSize)+uint64(j))*16:]
						binary.LittleEndian.PutUint64(p, v.x)
						binary.LittleEndian.PutUint64(p[8:], v.y)
					}
				} else {
					copy(reader.vertexData[uint64(i)*uint64(m.VertexBlockSize)*16:], data)
				}
			}
		} else if len(reader.vertexData) > 0 {
			if _, err := r.ReadAt(reader.vertexData, int64(vertexOffset)); err != nil {
				return nil, err
			}
		}
	}
	return reader, nil
}
func (r *Reader) Metadata() Metadata { return r.meta }

// Record reconstructs the original rings for Decimals=0. Quantized components
// return explicitly rounded XY and preserve all extra ordinates bit-for-bit.
func (r *Reader) Record(featureID uint32) (plss.Geometry, error) {
	if featureID == 0 || featureID > r.meta.Features {
		return plss.Geometry{}, errors.New("featureID outside geometry component")
	}
	index := (featureID - 1) / r.meta.BlockSize
	within := (featureID - 1) % r.meta.BlockSize
	entry := r.entries[index]
	data, hit := r.blocks.get(uint64(index))
	if !hit {
		compressed := make([]byte, entry.compressed)
		if _, err := r.r.ReadAt(compressed, int64(entry.offset)); err != nil {
			return plss.Geometry{}, err
		}
		state, ok := r.pool.Get().(*inflateState)
		if !ok {
			state = &inflateState{}
		}
		defer r.pool.Put(state)
		var err error
		if state.reader == nil {
			state.reader, err = zlib.NewReader(bytes.NewReader(compressed))
		} else {
			err = state.reader.(zlib.Resetter).Reset(bytes.NewReader(compressed), nil)
		}
		if err != nil {
			return plss.Geometry{}, err
		}
		state.buffer.Reset()
		_, err = io.Copy(&state.buffer, io.LimitReader(state.reader, int64(entry.raw)+1))
		closeErr := state.reader.Close()
		if err != nil {
			return plss.Geometry{}, err
		}
		if closeErr != nil {
			return plss.Geometry{}, closeErr
		}
		if state.buffer.Len() != int(entry.raw) {
			return plss.Geometry{}, errors.New("incorrect decompressed block length")
		}
		data = state.buffer.Bytes()
		if r.blocks.capacity > 0 {
			data = append([]byte(nil), data...)
			r.blocks.put(uint64(index), data)
		}
	}
	pos := 0
	count, err := getU(data, &pos)
	if err != nil || count > uint64(r.meta.BlockSize) || uint64(within) >= count {
		return plss.Geometry{}, errors.New("invalid record block")
	}
	var start, length uint64
	for i := uint64(0); i < count; i++ {
		n, err := getU(data, &pos)
		if err != nil {
			return plss.Geometry{}, err
		}
		if i < uint64(within) {
			start += n
		}
		if i == uint64(within) {
			length = n
		}
	}
	if start > uint64(len(data)-pos) || length > uint64(len(data)-pos)-start {
		return plss.Geometry{}, errors.New("record outside block")
	}
	return r.decodeRecord(data[pos+int(start) : pos+int(start+length)])
}

func (r *Reader) decodeRecord(data []byte) (plss.Geometry, error) {
	if len(data) == 0 || data[0] > 1 {
		return plss.Geometry{}, errors.New("invalid record flags")
	}
	variable := data[0] == 1
	pos := 1
	nrings, err := getU(data, &pos)
	if err != nil || nrings > uint64(len(data)) {
		return plss.Geometry{}, errors.New("invalid ring count")
	}
	g := plss.Geometry{Rings: make([][][]float64, int(nrings))}
	vertexCache := make(map[uint32][2]float64)
	for i := range g.Rings {
		header, err := getU(data, &pos)
		if err != nil {
			return plss.Geometry{}, err
		}
		n := int(header >> 1)
		closed := header&1 != 0
		if n < 3 || n > len(data)+1 {
			return plss.Geometry{}, errors.New("invalid vertex count")
		}
		g.Rings[i] = make([][]float64, n)
		coords := make([]float64, n*2)
		limit := n
		if closed {
			limit--
		}
		var previous int64
		for j := 0; j < limit; j++ {
			dims := uint64(2)
			if variable {
				dims, err = getU(data, &pos)
				if err != nil {
					return plss.Geometry{}, err
				}
			}
			if dims < 2 || dims > uint64(len(data))+2 {
				return plss.Geometry{}, errors.New("invalid dimensions")
			}
			v := coords[j*2 : j*2+2 : j*2+2]
			if dims != 2 {
				v = make([]float64, int(dims))
			}
			g.Rings[i][j] = v
			encoded, err := getU(data, &pos)
			if err != nil {
				return plss.Geometry{}, err
			}
			id := previous + unzigzag(encoded)
			if id < 0 || uint64(id) >= r.meta.Vertices {
				return plss.Geometry{}, errors.New("invalid vertex reference")
			}
			previous = id
			xy, ok := vertexCache[uint32(id)]
			if !ok {
				raw, err := r.vertexXY(uint32(id))
				if err != nil {
					return plss.Geometry{}, err
				}
				if r.meta.Decimals == 0 {
					xy = [2]float64{math.Float64frombits(raw.x), math.Float64frombits(raw.y)}
				} else {
					scale := math.Pow10(int(r.meta.Decimals))
					xy = [2]float64{float64(int32(uint32(raw.x))) / scale, float64(int32(uint32(raw.y))) / scale}
				}
				vertexCache[uint32(id)] = xy
			}
			v[0], v[1] = xy[0], xy[1]
			for k := 2; k < int(dims); k++ {
				if len(data)-pos < 8 {
					return plss.Geometry{}, io.ErrUnexpectedEOF
				}
				v[k] = math.Float64frombits(binary.LittleEndian.Uint64(data[pos : pos+8]))
				pos += 8
			}
		}
		if closed {
			first := g.Rings[i][0]
			last := coords[(n-1)*2 : n*2 : n*2]
			if len(first) != 2 {
				last = make([]float64, len(first))
			}
			copy(last, first)
			g.Rings[i][n-1] = last
		}
	}
	if pos != len(data) {
		return plss.Geometry{}, errors.New("trailing geometry record bytes")
	}
	return g, nil
}

func (r *Reader) vertex(id uint32) ([]byte, error) {
	offset := uint64(id) * uint64(r.vertexWidth)
	if len(r.vertexData) > 0 {
		return r.vertexData[offset : offset+uint64(r.vertexWidth)], nil
	}
	if r.meta.Version == 2 {
		block := uint64(id) / uint64(r.meta.VertexBlockSize)
		within := uint64(id) % uint64(r.meta.VertexBlockSize) * 16
		data, err := r.vertexBlock(block)
		if err != nil {
			return nil, err
		}
		if r.meta.VertexCodec == VertexXORBitpack {
			v := packedVertex(data, int(r.vertexEntries[block].raw)/16, int(id%r.meta.VertexBlockSize))
			out := make([]byte, 16)
			binary.LittleEndian.PutUint64(out, v.x)
			binary.LittleEndian.PutUint64(out[8:], v.y)
			return out, nil
		}
		return data[within : within+16 : within+16], nil
	}
	if r.vertexPages.capacity == 0 {
		raw := make([]byte, r.vertexWidth)
		_, err := r.r.ReadAt(raw, int64(r.vertexOffset+offset))
		return raw, err
	}
	page := offset / 4096
	within := offset % 4096
	data, hit := r.vertexPages.get(page)
	if !hit {
		length := min(uint64(4096), r.meta.VertexBytes-page*4096)
		data = make([]byte, int(length))
		if _, err := r.r.ReadAt(data, int64(r.vertexOffset+page*4096)); err != nil {
			return nil, err
		}
		r.vertexPages.put(page, data)
	}
	return data[within : within+uint64(r.vertexWidth)], nil
}
