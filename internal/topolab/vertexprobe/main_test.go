package main

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

func TestTransformsPreserveEveryBit(t *testing.T) {
	var raw []byte
	for _, v := range []float64{0, math.Copysign(0, -1), math.Nextafter(-120, 0), 40.12345678912345, math.SmallestNonzeroFloat64, math.Float64frombits(0x7ff8000000000012), math.Inf(1), math.Inf(-1)} {
		raw = binary.LittleEndian.AppendUint64(raw, math.Float64bits(v))
	}
	for _, mode := range []string{"raw", "shuffle", "xor-shuffle"} {
		encoded := transform(raw, mode)
		decoded := inverse(encoded, mode)
		if !bytes.Equal(raw, decoded) {
			t.Fatalf("%s changed floatingpoint bits", mode)
		}
	}
}
