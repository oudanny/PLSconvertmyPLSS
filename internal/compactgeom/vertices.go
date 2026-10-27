package compactgeom

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"math/bits"
)

// VertexCodec identifies the representation of the XY dictionary, independently
// of Decimals. All supported codecs are bit-exact when Decimals is zero.
type VertexCodec uint32

const (
	VertexRaw VertexCodec = iota
	VertexXORShuffleZlib
	VertexXORBitpack
)

type BuilderOptions struct {
	Decimals    int
	VertexCodec VertexCodec
	// VertexBlockSize is 128 or 256 for compressed dictionaries; zero selects
	// 256. Raw dictionaries require zero and use the compatible v1 format.
	VertexBlockSize int
}

func (o BuilderOptions) validate() error {
	if o.VertexCodec == VertexRaw {
		if o.VertexBlockSize != 0 {
			return errors.New("raw vertices do not have compressed blocks")
		}
		return nil
	}
	if (o.VertexCodec != VertexXORShuffleZlib && o.VertexCodec != VertexXORBitpack) || o.Decimals != 0 || (o.VertexBlockSize != 0 && o.VertexBlockSize != 128 && o.VertexBlockSize != 256) {
		return errors.New("compressed vertices require lossless XY and block size128or256")
	}
	return nil
}

func writeDirectory(w io.Writer, entries []blockEntry) error {
	for _, e := range entries {
		var encoded [16]byte
		binary.LittleEndian.PutUint64(encoded[:8], e.offset)
		binary.LittleEndian.PutUint32(encoded[8:12], e.compressed)
		binary.LittleEndian.PutUint32(encoded[12:], e.raw)
		if _, err := w.Write(encoded[:]); err != nil {
			return err
		}
	}
	return nil
}

// Shuffling groups equal byte positions together after XOR prediction along
// each axis. No float arithmetic occurs, including for signed zero/subnormals.
func shuffleVertices(raw []byte) []byte {
	n := len(raw) / 16
	out := make([]byte, len(raw))
	var previous [2]uint64
	for i := 0; i < n; i++ {
		for axis := 0; axis < 2; axis++ {
			bits := binary.LittleEndian.Uint64(raw[i*16+axis*8:])
			delta := bits ^ previous[axis]
			previous[axis] = bits
			for j := 0; j < 8; j++ {
				out[(axis*8+j)*n+i] = byte(delta >> uint(j*8))
			}
		}
	}
	return out
}

func unshuffleVertices(encoded []byte) []byte {
	n := len(encoded) / 16
	out := make([]byte, len(encoded))
	var previous [2]uint64
	for i := 0; i < n; i++ {
		for axis := 0; axis < 2; axis++ {
			var delta uint64
			for j := 0; j < 8; j++ {
				delta |= uint64(encoded[(axis*8+j)*n+i]) << uint(j*8)
			}
			bits := delta ^ previous[axis]
			previous[axis] = bits
			binary.LittleEndian.PutUint64(out[i*16+axis*8:], bits)
		}
	}
	return out
}

func (b *Builder) writeVertices(count uint64, width int, at func(uint32) (xy, error)) ([]blockEntry, error) {
	limit := 4096
	if b.vertexCodec != VertexRaw {
		limit = int(b.vertexBlockSize)
	}
	raw := make([]byte, 0, limit*width)
	var entries []blockEntry
	for first := uint64(0); first < count; first += uint64(limit) {
		raw = raw[:0]
		for id := first; id < min(first+uint64(limit), count); id++ {
			v, err := at(uint32(id))
			if err != nil {
				return nil, err
			}
			if width == 16 {
				raw = binary.LittleEndian.AppendUint64(raw, v.x)
				raw = binary.LittleEndian.AppendUint64(raw, v.y)
			} else {
				raw = binary.LittleEndian.AppendUint32(raw, uint32(v.x))
				raw = binary.LittleEndian.AppendUint32(raw, uint32(v.y))
			}
		}
		data := raw
		if b.vertexCodec == VertexXORShuffleZlib {
			b.buffer.Reset()
			b.zip.Reset(&b.buffer)
			if _, err := b.zip.Write(shuffleVertices(raw)); err != nil {
				return nil, err
			}
			if err := b.zip.Close(); err != nil {
				return nil, err
			}
			data = b.buffer.Bytes()
		} else if b.vertexCodec == VertexXORBitpack {
			data = packVertices(raw)
		}
		if b.vertexCodec != VertexRaw {
			offset, err := b.f.Seek(0, io.SeekCurrent)
			if err != nil {
				return nil, err
			}
			entries = append(entries, blockEntry{uint64(offset), uint32(len(data)), uint32(len(raw))})
		}
		if _, err := b.f.Write(data); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

func (r *Reader) openVertexDirectory(blockSize uint32, codec VertexCodec, offset, recordDirectory uint64) error {
	if err := validateExactMetadata(r.meta); err != nil {
		return err
	}
	if r.meta.Decimals != 0 || (codec != VertexXORShuffleZlib && codec != VertexXORBitpack) || (blockSize != 128 && blockSize != 256) || offset < r.vertexOffset || offset > recordDirectory {
		return errors.New("invalid compressed vertex mode or offsets")
	}
	count := (r.meta.Vertices + uint64(blockSize) - 1) / uint64(blockSize)
	if count > (recordDirectory-offset)/16 || offset+count*16 != recordDirectory {
		return errors.New("invalid compressed vertex directory size")
	}
	r.meta.VertexCodec = codec
	r.meta.VertexBlockSize = blockSize
	r.meta.VertexBytes = offset - r.vertexOffset
	r.meta.VertexDirectoryBytes = count * 16
	r.meta.DirectoryBytes += count * 16
	r.vertexEntries = make([]blockEntry, int(count))
	encoded := make([]byte, int(count)*16)
	if len(encoded) > 0 {
		if _, err := r.r.ReadAt(encoded, int64(offset)); err != nil {
			return err
		}
	}
	previous := r.vertexOffset
	for i := range r.vertexEntries {
		p := encoded[i*16:]
		e := blockEntry{binary.LittleEndian.Uint64(p), binary.LittleEndian.Uint32(p[8:]), binary.LittleEndian.Uint32(p[12:])}
		raw := min(uint64(blockSize), r.meta.Vertices-uint64(i)*uint64(blockSize)) * 16
		if e.offset != previous || uint64(e.raw) != raw || e.compressed == 0 || e.compressed > e.raw+128 || e.offset > offset || uint64(e.compressed) > offset-e.offset {
			return errors.New("invalid compressed vertex block")
		}
		r.vertexEntries[i] = e
		previous = e.offset + uint64(e.compressed)
	}
	if previous != offset {
		return errors.New("incomplete compressed vertex directory")
	}
	return nil
}

func validateExactMetadata(m Metadata) error {
	if m.Decimals != 0 || m.MaxLatitudeError != 0 || m.MaxLongitudeError != 0 || m.CollapsedEdges != 0 || m.CollapsedRings != 0 || m.OrientationChanges != 0 {
		return errors.New("lossless vertex metadata declares rounding or changed topology")
	}
	if (m.Features == 0) != (m.Vertices == 0) {
		return errors.New("feature and vertex emptiness disagree")
	}
	return nil
}

func (r *Reader) readVertexBlock(index uint64) ([]byte, error) {
	if index >= uint64(len(r.vertexEntries)) {
		return nil, errors.New("vertex block outside directory")
	}
	e := r.vertexEntries[index]
	if r.meta.VertexCodec == VertexXORBitpack {
		data := make([]byte, int(e.compressed))
		if _, err := r.r.ReadAt(data, int64(e.offset)); err != nil {
			return nil, err
		}
		if err := validatePackedVertices(data, int(e.raw)/16); err != nil {
			return nil, err
		}
		return data, nil
	}
	state, ok := r.vertexPool.Get().(*inflateState)
	if !ok {
		state = &inflateState{}
	}
	defer r.vertexPool.Put(state)
	if cap(state.compressed) < int(e.compressed) {
		state.compressed = make([]byte, int(e.compressed))
	}
	compressed := state.compressed[:e.compressed]
	if _, err := r.r.ReadAt(compressed, int64(e.offset)); err != nil {
		return nil, err
	}
	input := bytes.NewReader(compressed)
	var err error
	if state.reader == nil {
		state.reader, err = zlib.NewReader(input)
	} else {
		err = state.reader.(zlib.Resetter).Reset(input, nil)
	}
	if err != nil {
		return nil, err
	}
	state.buffer.Reset()
	_, err = io.Copy(&state.buffer, io.LimitReader(state.reader, int64(e.raw)+1))
	closeErr := state.reader.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if state.buffer.Len() != int(e.raw) {
		return nil, errors.New("incorrect decompressed vertex block length")
	}
	if input.Len() != 0 {
		return nil, errors.New("trailing compressed vertex block bytes")
	}
	return unshuffleVertices(state.buffer.Bytes()), nil
}

var vertexCRC = crc32.MakeTable(crc32.Castagnoli)

// Packed blocks are [baseX:u64,baseY:u64,widthX:u8,widthY:u8,Xbits,Ybits,CRC32C].
// Axis streams contain one fixed-width XOR value per vertex in source order.
// The directory supplies vertex count, so each vertex is directly addressable.
func packVertices(raw []byte) []byte {
	n := len(raw) / 16
	base := xy{binary.LittleEndian.Uint64(raw), binary.LittleEndian.Uint64(raw[8:])}
	var unionX, unionY uint64
	for i := 0; i < n; i++ {
		unionX |= binary.LittleEndian.Uint64(raw[i*16:]) ^ base.x
		unionY |= binary.LittleEndian.Uint64(raw[i*16+8:]) ^ base.y
	}
	wx, wy := bits.Len64(unionX), bits.Len64(unionY)
	xbytes, ybytes := (n*wx+7)/8, (n*wy+7)/8
	out := make([]byte, 18+xbytes+ybytes+4)
	binary.LittleEndian.PutUint64(out, base.x)
	binary.LittleEndian.PutUint64(out[8:], base.y)
	out[16], out[17] = byte(wx), byte(wy)
	for i := 0; i < n; i++ {
		putPackedBits(out[18:18+xbytes], i*wx, wx, binary.LittleEndian.Uint64(raw[i*16:])^base.x)
		putPackedBits(out[18+xbytes:len(out)-4], i*wy, wy, binary.LittleEndian.Uint64(raw[i*16+8:])^base.y)
	}
	binary.LittleEndian.PutUint32(out[len(out)-4:], crc32.Checksum(out[:len(out)-4], vertexCRC))
	return out
}

func putPackedBits(dst []byte, pos, width int, value uint64) {
	for width > 0 {
		shift := pos & 7
		n := min(width, 8-shift)
		dst[pos/8] |= byte(value&((uint64(1)<<n)-1)) << shift
		value >>= n
		pos += n
		width -= n
	}
}

func readPackedBits(data []byte, pos, width int) uint64 {
	if width == 0 {
		return 0
	}
	p := pos / 8
	shift := uint(pos & 7)
	var word uint64
	if len(data)-p >= 8 {
		word = binary.LittleEndian.Uint64(data[p:])
	} else {
		for i, b := range data[p:] {
			word |= uint64(b) << uint(i*8)
		}
	}
	value := word >> shift
	if shift != 0 && width > 64-int(shift) {
		value |= uint64(data[p+8]) << (64 - shift)
	}
	if width < 64 {
		value &= (uint64(1) << width) - 1
	}
	return value
}

func validatePackedVertices(data []byte, count int) error {
	if len(data) < 22 || count < 1 || count > 256 || data[16] > 64 || data[17] > 64 {
		return errors.New("invalid packed vertex header")
	}
	wx, wy := int(data[16]), int(data[17])
	xbytes, ybytes := (count*wx+7)/8, (count*wy+7)/8
	if len(data) != 22+xbytes+ybytes {
		return errors.New("incorrect packed vertex length")
	}
	if crc32.Checksum(data[:len(data)-4], vertexCRC) != binary.LittleEndian.Uint32(data[len(data)-4:]) {
		return errors.New("packed vertex checksum mismatch")
	}
	for _, stream := range []struct {
		p     []byte
		width int
	}{{data[18 : 18+xbytes], wx}, {data[18+xbytes : len(data)-4], wy}} {
		used := count * stream.width % 8
		if used != 0 && stream.p[len(stream.p)-1]>>used != 0 {
			return errors.New("nonzero packed vertex padding")
		}
	}
	return nil
}

func packedVertex(data []byte, count, index int) xy {
	wx, wy := int(data[16]), int(data[17])
	xbytes := (count*wx + 7) / 8
	return xy{binary.LittleEndian.Uint64(data) ^ readPackedBits(data[18:18+xbytes], index*wx, wx), binary.LittleEndian.Uint64(data[8:]) ^ readPackedBits(data[18+xbytes:len(data)-4], index*wy, wy)}
}

func (r *Reader) vertexBlock(index uint64) ([]byte, error) {
	data, hit := r.vertexBlocks.get(index)
	if !hit {
		var err error
		data, err = r.readVertexBlock(index)
		if err != nil {
			return nil, err
		}
		r.vertexBlocks.put(index, data)
	}
	return data, nil
}

func (r *Reader) vertexXY(id uint32) (xy, error) {
	if r.meta.VertexCodec == VertexXORBitpack && len(r.vertexData) == 0 {
		block := uint64(id) / uint64(r.meta.VertexBlockSize)
		data, err := r.vertexBlock(block)
		if err != nil {
			return xy{}, err
		}
		return packedVertex(data, int(r.vertexEntries[block].raw)/16, int(id%r.meta.VertexBlockSize)), nil
	}
	p, err := r.vertex(id)
	if err != nil {
		return xy{}, err
	}
	if r.meta.Decimals == 0 {
		return xy{binary.LittleEndian.Uint64(p), binary.LittleEndian.Uint64(p[8:])}, nil
	}
	return xy{uint64(binary.LittleEndian.Uint32(p)), uint64(binary.LittleEndian.Uint32(p[4:]))}, nil
}

// RepackVertices copies the original compressed record blocks unchanged and
// rewrites only the bit-exact XY dictionary. It creates a new component and
// never modifies the source. This avoids rebuilding shared topology for codec
// experiments; the source may be v1 or v2 but must have Decimals=0.
func RepackVertices(path string, source io.ReaderAt, size int64, options BuilderOptions) (Metadata, error) {
	if options.Decimals != 0 {
		return Metadata{}, errors.New("repacking requires lossless XY")
	}
	r, err := OpenReaderAt(source, size)
	if err != nil {
		return Metadata{}, err
	}
	if r.meta.Decimals != 0 {
		return Metadata{}, errors.New("cannot repack a lossy source as lossless")
	}
	if err := validateExactMetadata(r.meta); err != nil {
		return Metadata{}, err
	}
	b, err := NewBuilderWithOptions(path, options)
	if err != nil {
		return Metadata{}, err
	}
	defer b.Close()
	if _, err := io.Copy(b.f, io.NewSectionReader(source, headerSize, int64(r.vertexOffset)-headerSize)); err != nil {
		return Metadata{}, err
	}
	b.entries = r.entries
	b.meta = r.meta
	return b.finish(r.meta.Vertices, func(id uint32) (xy, error) {
		return r.vertexXY(id)
	})
}
