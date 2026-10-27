package spatialindex

import (
	"bytes"
	"math/rand"
	"reflect"
	"testing"
)

func TestIndexMatchesBruteForceAndRoundtrip(t *testing.T) {
	rng := rand.New(rand.NewSource(27))
	entries := make([]Entry, 10000)
	for i := range entries {
		x := float32(-120 + rng.Float64()*6)
		y := float32(35 + rng.Float64()*7)
		entries[i] = Entry{x, x + float32(rng.Float64()*.2), y, y + float32(rng.Float64()*.2), uint32(i + 1), uint8(i%3 + 1)}
	}
	entries = append(entries, Entry{179, 180, 50, 51, 10001, 3}, Entry{-180, -179, 50, 51, 10001, 3}, Entry{-120, -119, 35, 36, 10002, 2}, Entry{-120, -119, 35, 36, 10002, 2})
	x, err := Build(entries, 16)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	n, err := x.WriteTo(&buf)
	if err != nil || n != x.ByteSize() {
		t.Fatalf("write %d,%v", n, err)
	}
	framed := append([]byte("header"), buf.Bytes()...)
	framed = append(framed, []byte("footer")...)
	y, err := Open(bytes.NewReader(framed), 6, n)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 500; i++ {
		lon, lat := -120+rng.Float64()*6, 35+rng.Float64()*7
		if i < 100 {
			e := entries[i]
			lon, lat = float64(e.MinLon), float64(e.MaxLat)
		}
		if i == 498 {
			lon, lat = 180, 50.5
		}
		if i == 499 {
			lon, lat = -180, 50.5
		}
		for layer := uint8(0); layer <= 3; layer++ {
			var want []Match
			for _, e := range entries {
				if (layer == 0 || e.Layer == layer) && lon >= float64(e.MinLon) && lon <= float64(e.MaxLon) && lat >= float64(e.MinLat) && lat <= float64(e.MaxLat) {
					want = append(want, Match{e.ID, e.Layer})
				}
			}
			want = deduplicate(want, 0)
			for _, idx := range []*Index{x, y} {
				got := idx.SearchLayer(lon, lat, layer)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("point %.9f %.9f layer%d got %v want %v", lon, lat, layer, got, want)
				}
			}
		}
	}
}

func TestEmptyAndMalformedIndex(t *testing.T) {
	x, err := Build(nil, 16)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	x.WriteTo(&b)
	y, err := Open(bytes.NewReader(b.Bytes()), 0, int64(b.Len()))
	if err != nil || len(y.Search(0, 0)) != 0 {
		t.Fatalf("empty: %v", err)
	}
	if _, err := Build([]Entry{{1, 0, 0, 1, 1, 1}}, 16); err == nil {
		t.Fatal("accepted inverted bbox")
	}
	if _, err := Build(nil, 1); err == nil {
		t.Fatal("accepted bad fanout")
	}
	if _, err := Open(bytes.NewReader([]byte("bad")), 0, 3); err == nil {
		t.Fatal("accepted truncated index")
	}
}
