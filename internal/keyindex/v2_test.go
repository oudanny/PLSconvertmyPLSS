package keyindex

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"strings"
	"testing"
)

func directoryFixture(t *testing.T, version int) ([]byte, *Builder) {
	t.Helper()
	b := NewBuilder()
	for _, source := range []Source{
		{ID: 1, Layer: 1, Township: "A"},
		{ID: 2, Layer: 2, Township: "A", First: "A/S"},
		{ID: 3, Layer: 3, Township: "A", First: "A/S", Second: "A/S/X"},
		{ID: 4, Layer: 3, Township: "B", First: "external", Second: "orphan", Survey: "survey"},
	} {
		if err := b.Add(source); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if _, err := b.writeVersion(&out, version); err != nil {
		t.Fatal(err)
	}
	return out.Bytes(), b
}
func directoryPositions(t *testing.T, data []byte) (int, int) {
	t.Helper()
	x, err := OpenView(data)
	if err != nil {
		t.Fatal(err)
	}
	start := 12 + int(binary.LittleEndian.Uint32(data[8:12])) + int(x.m.Count)*int(x.m.Width)
	return start, start + (len(x.m.Dicts[0])+1)*4
}

func TestV1V2CompatibilityAndValidation(t *testing.T) {
	for _, version := range []int{1, 2} {
		data, _ := directoryFixture(t, version)
		for _, view := range []bool{false, true} {
			var x *Index
			var err error
			if view {
				x, err = OpenView(data)
			} else {
				x, err = Open(bytes.NewReader(data), int64(len(data)))
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := x.Validate(); err != nil {
				t.Fatalf("version%d/view%t Validate: %v", version, view, err)
			}
			if got, err := x.Find("division", "A/S/X"); err != nil || !reflect.DeepEqual(got, []uint32{3}) {
				t.Fatalf("normalprefix: %v,%v", got, err)
			}
			if got, err := x.Find("division", "orphan"); err != nil || !reflect.DeepEqual(got, []uint32{4}) {
				t.Fatalf("exception: %v,%v", got, err)
			}
			if got, err := x.Find("survey", "survey"); err != nil || !reflect.DeepEqual(got, []uint32{4}) {
				t.Fatalf("survey: %v,%v", got, err)
			}
			stats := x.MemoryStats()
			if stats.OwnedArrayBytes <= 0 || stats.OwnedMetadataEstimateBytes <= 0 || x.MemoryBytes() != int(stats.OwnedArrayBytes+stats.OwnedMetadataEstimateBytes) {
				t.Fatalf("bad memory stats: %+v", stats)
			}
			if view && x.BorrowedBytes() != len(data) || !view && x.BorrowedBytes() != 0 {
				t.Fatalf("wrong ownership accounting: %+v", stats)
			}
		}
	}
}

func TestViewBorrowsRecordsButReturnedStringsAreOwned(t *testing.T) {
	data, _ := directoryFixture(t, 2)
	view, err := OpenView(data)
	if err != nil {
		t.Fatal(err)
	}
	owned, err := Open(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	id, err := view.ID(3)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := view.Fields(4)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := view.Find("division", "A/S/X")
	if err != nil {
		t.Fatal(err)
	}
	start := 12 + int(binary.LittleEndian.Uint32(data[8:12]))
	data[start] = 0
	if got, err := view.ID(1); err != nil || got != "" {
		t.Fatalf("mapped record was copied: %q,%v", got, err)
	}
	if got, err := owned.ID(1); err != nil || got != "A" {
		t.Fatalf("owned open shares input: %q,%v", got, err)
	}
	// Simulate destroying the borrowed buffer after the query. Results must not
	// reference it, although further Index queries now violate its lifetime.
	clear(data)
	if id != "A/S/X" || fields["SECDIVID"] != "orphan" || fields["SURVID"] != "survey" || !reflect.DeepEqual(rows, []uint32{3}) {
		t.Fatal("query results referenced borrowed bytes")
	}
}

func TestV2DirectoryAndLazyPostingValidation(t *testing.T) {
	original, _ := directoryFixture(t, 2)
	directory, payload := directoryPositions(t, original)
	copyData := func() []byte { return append([]byte(nil), original...) }
	for name, mutate := range map[string]func([]byte){
		"nonzero first offset": func(d []byte) { binary.LittleEndian.PutUint32(d[directory:], 1) },
		"descending offset":    func(d []byte) { binary.LittleEndian.PutUint32(d[directory+8:], 0) },
		"equal offsets": func(d []byte) {
			binary.LittleEndian.PutUint32(d[directory+8:], binary.LittleEndian.Uint32(d[directory+4:]))
		},
		"out of bounds": func(d []byte) { binary.LittleEndian.PutUint32(d[directory+12:], uint32(len(d)-payload+1)) },
	} {
		t.Run(name, func(t *testing.T) {
			data := copyData()
			mutate(data)
			if _, err := OpenView(data); err == nil {
				t.Fatal("accepted invalid directory")
			}
		})
	}
	for name, mutate := range map[string]func([]byte){
		"shifted group boundary": func(d []byte) {
			binary.LittleEndian.PutUint32(d[directory+8:], binary.LittleEndian.Uint32(d[directory+8:])+1)
		},
		"zero delta":                func(d []byte) { start := int(binary.LittleEndian.Uint32(d[directory+4:])); d[payload+start+1] = 0 },
		"invalid count":             func(d []byte) { start := int(binary.LittleEndian.Uint32(d[directory+4:])); d[payload+start] = 127 },
		"truncated delta varint":    func(d []byte) { end := int(binary.LittleEndian.Uint32(d[directory+8:])); d[payload+end-1] = 0x80 },
		"wrong township membership": func(d []byte) { start := int(binary.LittleEndian.Uint32(d[directory+4:])); d[payload+start+1] = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			data := copyData()
			mutate(data)
			x, err := OpenView(data)
			if err != nil {
				t.Fatalf("posting bytes were scanned at open: %v", err)
			}
			if _, err := x.Find("township", "A"); err == nil {
				t.Fatal("Find accepted invalid posting")
			}
			if err := x.Validate(); err == nil {
				t.Fatal("Validate accepted invalid posting")
			}
		})
	}
	for n := 0; n < len(original); n++ {
		if _, err := OpenView(original[:n]); err == nil {
			t.Fatalf("accepted truncation at%d", n)
		}
	}
}

func TestValidateFindsOmissionsAndBadReferences(t *testing.T) {
	data, _ := directoryFixture(t, 2)
	directory, payload := directoryPositions(t, data)
	// Remove the final A delta and adjust offsets, retaining valid v2 framing.
	endA := int(binary.LittleEndian.Uint32(data[directory+8:]))
	startA := int(binary.LittleEndian.Uint32(data[directory+4:]))
	data[payload+startA]--
	data = append(data[:payload+endA-1], data[payload+endA:]...)
	binary.LittleEndian.PutUint32(data[directory+8:], uint32(endA-1))
	binary.LittleEndian.PutUint32(data[directory+12:], binary.LittleEndian.Uint32(data[directory+12:])-1)
	x, err := OpenView(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.Validate(); err == nil || !strings.Contains(err.Error(), "omit") {
		t.Fatalf("omission not found: %v", err)
	}
	for _, alter := range []func(*Index){
		func(x *Index) { delete(x.exceptions, "3:orphan") },
		func(x *Index) { x.exceptions["3:orphan"] = []uint32{999} },
		func(x *Index) { x.m.Surveys[999] = "bad" },
	} {
		data, _ := directoryFixture(t, 2)
		x, err := OpenView(data)
		if err != nil {
			t.Fatal(err)
		}
		alter(x)
		if err := x.Validate(); err == nil {
			t.Fatal("accepted corrupt exception metadata")
		}
	}
}

func FuzzOpenViewAndQueries(f *testing.F) {
	b := NewBuilder()
	_ = b.Add(Source{ID: 1, Layer: 1, Township: "A"})
	_ = b.Add(Source{ID: 2, Layer: 3, Township: "A", Second: "orphan", Survey: "survey"})
	var data bytes.Buffer
	_, _ = b.WriteTo(&data)
	f.Add(data.Bytes())
	f.Add([]byte("PLSSKEY2"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		x, err := OpenView(data)
		if err != nil {
			return
		}
		_, _ = x.ID(1)
		_, _ = x.Fields(1)
		_, _ = x.Find("township", "A")
		_, _ = x.Find("division", "orphan")
		_, _ = x.Find("survey", "survey")
		_ = x.Validate()
	})
}
