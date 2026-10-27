package keyindex

import (
	"bytes"
	"reflect"
	"testing"
)

func TestFactoredIDsAndDuplicateReverseLookup(t *testing.T) {
	rows := []Source{
		{1, 1, "NV210190N0190E0", "", "", ""},
		{2, 2, "NV210190N0190E0", "NV210190N0190E0SN110", "", ""},
		{3, 2, "NV210190N0190E0", "external-section", "", ""},
		{4, 3, "NV210190N0190E0", "NV210190N0190E0SN110", "NV210190N0190E0SN110ASWNE", ""},
		{5, 3, "NV210190N0190E0", "NV210190N0190E0SN110", "NV210190N0190E0SN110ASWNE", "survey"},
		{6, 3, "NV210190N0190E0", "external-section", "external-sectionA", ""},
		{7, 3, "NV210190N0190E0", "NV210190N0190E0", "NV210190N0190E0", ""},
		{8, 3, "", "", "orphan", ""},
	}
	b := NewBuilder()
	for _, r := range rows {
		if err := b.Add(r); err != nil {
			t.Fatal(err)
		}
	}
	var data bytes.Buffer
	if _, err := b.WriteTo(&data); err != nil {
		t.Fatal(err)
	}
	x, err := Open(bytes.NewReader(data.Bytes()), int64(data.Len()))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		want := r.Second
		if want == "" {
			want = r.Survey
		}
		if want == "" {
			want = r.First
		}
		if want == "" {
			want = r.Township
		}
		got, err := x.ID(r.ID)
		if err != nil || got != want {
			t.Fatalf("row%d %q != %q: %v", r.ID, got, want, err)
		}
	}
	for _, tc := range []struct {
		kind, id string
		want     []uint32
	}{
		{"division", rows[3].Second, []uint32{4, 5}}, {"section", "external-section", []uint32{3}},
		{"division", "external-sectionA", []uint32{6}}, {"survey", "survey", []uint32{5}},
		{"township", rows[0].Township, []uint32{1}}, {"division", rows[0].Township, []uint32{7}},
		{"division", "orphan", []uint32{8}},
	} {
		got, err := x.Find(tc.kind, tc.id)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("Find(%s,%s)=%v,%v want%v", tc.kind, tc.id, got, err, tc.want)
		}
	}
	for n := 0; n < data.Len(); n++ {
		if _, err := Open(bytes.NewReader(data.Bytes()[:n]), int64(n)); err == nil {
			t.Fatalf("accepted truncation at%d", n)
		}
	}
}
