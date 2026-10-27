package main

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

func TestLosslessCodecs(t *testing.T) {
	f := feature{Attributes: map[string]any{"number": json.Number("9007199254740993"), "nil": nil, "empty": "", "nested": map[string]any{"a": json.Number("1.23456789012345")}}, Geometry: plss.Geometry{Rings: [][][]float64{{{-120, 40, 17}, {math.Nextafter(-120, 0), 40, 18}, {math.Copysign(0, -1), 41}, {-120, 40, 17}}, {{1, 2}, {3, 4}, {1, 2}}}}}
	keys := []string{"absent", "empty", "nested", "nil", "number"}
	for _, mode := range []byte{0, 1} {
		for _, compact := range []bool{false, true} {
			data, err := encodeFeature(f, keys, compact, mode)
			if err != nil {
				t.Fatal(err)
			}
			got, err := decodeFeature(data, keys, compact)
			if err != nil || !same(f, got) {
				t.Fatalf("mode%d compact%t changed data: %+v, %v", mode, compact, got, err)
			}
			if _, ok := got.Attributes["absent"]; ok {
				t.Error("missing attribute became null")
			}
		}
	}
}

func TestBlockRecord(t *testing.T) {
	rows := [][]byte{{1, 2}, {3}, {4, 5, 6}}
	data := packBlock(rows)
	for i, want := range rows {
		got, err := blockRecord(data, i)
		if err != nil || string(got) != string(want) {
			t.Fatalf("block%d %v,%v", i, got, err)
		}
	}
}

func TestTruncatedGeometry(t *testing.T) {
	g := plss.Geometry{Rings: [][][]float64{{{1, 2}, {3, 4}, {1, 2}}}}
	for _, mode := range []byte{0, 1} {
		data := encodeGeometry(g, mode)
		for n := 0; n < len(data); n++ {
			if _, err := decodeGeometry(data[:n]); err == nil {
				t.Fatalf("accepted truncated mode%d length%d", mode, n)
			}
		}
	}
}
