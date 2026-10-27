package spatialindex

import (
	"bytes"
	"math"
	"math/rand"
	"reflect"
	"testing"
)

func TestQuantizedIndexNeverMissesOriginalCandidates(t *testing.T) {
	rng := rand.New(rand.NewSource(2701))
	entries := make([]Entry, 5000)
	for i := range entries {
		x := float32(-120 + rng.Float64()*6)
		y := float32(35 + rng.Float64()*7)
		entries[i] = Entry{x, x + float32(rng.Float64()*.1), y, y + float32(rng.Float64()*.1), uint32(i + 1), uint8(i%3 + 1)}
	}
	entries = append(entries, Entry{179, 180, 50, 51, 5001, 3}, Entry{-180, -179, 50, 51, 5001, 3}, Entry{1, 1, 1, 1, 5002, 2})
	full, err := Build(entries, 16)
	if err != nil {
		t.Fatal(err)
	}
	for _, bits := range []int{8, 16} {
		q, err := full.Quantize(bits)
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		n, err := q.WriteTo(&b)
		if err != nil || n != q.ByteSize() {
			t.Fatalf("write %d,%v", n, err)
		}
		loaded, err := Open(bytes.NewReader(b.Bytes()), 0, n)
		if err != nil {
			t.Fatal(err)
		}
		if q.ByteSize() >= full.ByteSize() {
			t.Fatal("quantized index did not shrink")
		}
		for i, e := range entries {
			for _, p := range [][2]float64{{float64(e.MinLon), float64(e.MinLat)}, {float64(e.MaxLon), float64(e.MaxLat)}, {float64(e.MinLon), float64(e.MaxLat)}, {float64(e.MaxLon), float64(e.MinLat)}, {(float64(e.MinLon) + float64(e.MaxLon)) / 2, (float64(e.MinLat) + float64(e.MaxLat)) / 2}} {
				want := full.Search(p[0], p[1])
				got := q.Search(p[0], p[1])
				if !reflect.DeepEqual(got, loaded.Search(p[0], p[1])) {
					t.Fatal("serialization changed candidates")
				}
				for _, w := range want {
					found := false
					for _, g := range got {
						if g == w {
							found = true
							break
						}
					}
					if !found {
						t.Fatalf("bits%d entry%d point%v missed %+v", bits, i, p, w)
					}
				}
			}
		}
	}
}

func TestQuantizedSinglePointAndInvalidBits(t *testing.T) {
	x, err := Build([]Entry{{MinLon: math.SmallestNonzeroFloat32, MaxLon: math.SmallestNonzeroFloat32, MinLat: 0, MaxLat: 0, ID: 1, Layer: 1}}, 16)
	if err != nil {
		t.Fatal(err)
	}
	q, err := x.Quantize(8)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Search(float64(math.SmallestNonzeroFloat32), 0)) != 1 {
		t.Fatal("lost zero-extent box")
	}
	if _, err := x.Quantize(7); err == nil {
		t.Fatal("accepted unsupported bits")
	}
}
