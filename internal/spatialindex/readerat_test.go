package spatialindex

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"reflect"
	"testing"
)

type guardedReader struct {
	data        []byte
	leafEnd     int64
	allowLeaves bool
	readBytes   int
	failLeaves  bool
}

func (r *guardedReader) ReadAt(p []byte, off int64) (int, error) {
	if off < r.leafEnd && off+int64(len(p)) > headerSize {
		if !r.allowLeaves {
			return 0, errors.New("opening touched leaf records")
		}
		if r.failLeaves {
			return 0, io.ErrUnexpectedEOF
		}
	}
	r.readBytes += len(p)
	return bytes.NewReader(r.data).ReadAt(p, off)
}

func TestReaderAtLeavesStayOnDiskUntilNeeded(t *testing.T) {
	for _, bits := range []int{0, 8, 16} {
		t.Run(fmt.Sprint(bits), func(t *testing.T) {
			x, data := viewFixture(t, bits)
			source := &guardedReader{data: data, leafEnd: int64(headerSize + len(x.layers)*x.recordSize())}
			v, err := OpenReaderAt(source, int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			if source.readBytes != headerSize+len(x.layers)+len(x.nodes)*24 {
				t.Fatalf("unexpected opening bytes: %d", source.readBytes)
			}
			if len(v.records) != 0 || v.MemoryBytes() != int64(len(x.layers)+len(x.nodes)*24) || v.ByteSize() != int64(len(data)) {
				t.Fatal("leaf records copied or incorrect memory accounting")
			}
			source.allowLeaves = true
			rng := rand.New(rand.NewSource(28))
			points := [][2]float64{{180, 50.5}, {-180, 50.5}, {0, 0}, {-120, 35}}
			for range 2000 {
				points = append(points, [2]float64{-120 + rng.Float64()*7, 35 + rng.Float64()*8})
			}
			for _, n := range x.nodes {
				points = append(points, [2]float64{float64(n.minX), float64(n.minY)}, [2]float64{float64(n.maxX), float64(n.maxY)})
			}
			for _, p := range points {
				for layer := uint8(0); layer <= 4; layer++ {
					got, err := v.SearchLayer(p[0], p[1], layer)
					want := x.SearchLayer(p[0], p[1], layer)
					if err != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("point %v layer %d: %+v %v != %+v", p, layer, got, err, want)
					}
				}
			}
			if err := v.Validate(); err != nil {
				t.Fatal(err)
			}
			if err := v.ValidateCoverage(1027, func(id uint32) uint8 {
				if id == 1025 {
					return 3
				}
				if id == 1026 {
					return 2
				}
				return uint8((id-1)%3 + 1)
			}); err != nil {
				t.Fatal(err)
			}
			source.failLeaves = true
			prefix := []Match{{99999, 1}}
			got, err := v.AppendSearch(prefix, 180, 50.5, 0)
			if err == nil || !reflect.DeepEqual(got, prefix) {
				t.Fatalf("failed source changed result: %+v %v", got, err)
			}
			if err := v.Validate(); err == nil {
				t.Fatal("Validate ignored unreadable leaves")
			}
		})
	}
}

func TestReaderAtRejectsCorruptHeadersAndLazyLeaves(t *testing.T) {
	x, data := viewFixture(t, 8)
	for _, n := range []int{0, 63, 64, len(data) - 1} {
		if _, err := OpenReaderAt(bytes.NewReader(data[:n]), int64(n)); err == nil {
			t.Fatalf("truncation %d", n)
		}
	}
	nodeStart := headerSize + len(x.layers)*(x.recordSize()+1)
	for _, mutate := range []func([]byte){
		func(d []byte) { binary.LittleEndian.PutUint64(d[16:], ^uint64(0)) },
		func(d []byte) { d[headerSize+len(x.layers)*x.recordSize()] = 4 },
		func(d []byte) { binary.LittleEndian.PutUint32(d[nodeStart+16:], ^uint32(0)) },
		func(d []byte) { binary.LittleEndian.PutUint32(d[nodeStart+20:], leafFlag|16) },
	} {
		bad := bytes.Clone(data)
		mutate(bad)
		if _, err := OpenReaderAt(bytes.NewReader(bad), int64(len(bad))); err == nil {
			t.Fatal("accepted malformed structure")
		}
	}
	bad := bytes.Clone(data)
	bad[headerSize], bad[headerSize+1] = 255, 0
	v, err := OpenReaderAt(bytes.NewReader(bad), int64(len(bad)))
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Validate(); err == nil {
		t.Fatal("invalid lazy leaf escaped full verification")
	}
	first := x.nodes[0]
	if _, err := v.Search(float64(first.minX), float64(first.minY)); err == nil {
		t.Fatal("invalid visited leaf was accepted")
	}
	if _, err := OpenReaderAt(nil, int64(len(data))); err == nil {
		t.Fatal("nil source")
	}
}
