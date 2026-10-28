package keyindex

import (
	"bytes"
	"fmt"
	"testing"
)

func TestIDFilterNeverExcludesPublishedFields(t *testing.T) {
	b := NewBuilder()
	var rows []Source
	for layer := uint8(1); layer <= 3; layer++ {
		for i := 0; i < 400; i++ {
			town := fmt.Sprintf("%sT%03d", []string{"NV", "CA", "AK", "foreign", ""}[i%5], i%31)
			if i%13 == 0 {
				town = ""
			}
			first := town + fmt.Sprintf("S%02d", i%36)
			if i%7 == 0 {
				first = fmt.Sprintf("other-first-%d", i%19)
			}
			if i%17 == 0 {
				first = ""
			}
			second := first + fmt.Sprintf("D%02d", i%29)
			if i%11 == 0 {
				second = fmt.Sprintf("NVforeign-division-%d", i%23)
			}
			if i%19 == 0 {
				second = ""
			}
			survey := []string{town, first, second, town + "survey", first + "suffix", second + "tail", fmt.Sprintf("independent-survey-%d", i%41), ""}[i%8]
			s := Source{ID: uint32(len(rows) + 1), Layer: layer, Township: town, First: first, Second: second, Survey: survey}
			rows = append(rows, s)
			if err := b.Add(s); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, version := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			var buf bytes.Buffer
			if _, err := b.writeVersion(&buf, version); err != nil {
				t.Fatal(err)
			}
			filter, err := ReadIDFilter(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
			if err != nil {
				t.Fatal(err)
			}
			x, err := OpenView(buf.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range rows {
				for kind, id := range map[string]string{"township": s.Township, "section": s.First, "division": s.Second, "intersected": s.Second, "survey": s.Survey} {
					if id == "" {
						continue
					}
					found, err := x.Find(kind, id)
					if err != nil {
						t.Fatal(err)
					}
					if len(found) > 0 && !filter.MayContain(kind, id) {
						t.Fatalf("false negative %s %s", kind, id)
					}
				}
			}
			for _, kind := range []string{"township", "section", "division", "survey"} {
				if filter.MayContain(kind, "impossible-unpublished-id") {
					t.Fatalf("failed obvious absent %s ID", kind)
				}
			}
		})
	}
}

func TestIDFilterSeparatesKindsAndKeepsForeignIDs(t *testing.T) {
	b := NewBuilder()
	for _, s := range []Source{{ID: 1, Layer: 1, Township: "CAT"}, {ID: 2, Layer: 2, Township: "CAT", First: "CATS"}, {ID: 3, Layer: 3, Township: "CAT", First: "CATS", Second: "CATSD", Survey: "NVsurvey"}, {ID: 4, Layer: 3, Second: "NVindependent", Survey: "lowercase"}} {
		if err := b.Add(s); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	b.WriteTo(&buf)
	f, err := ReadIDFilter(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		kind, id string
		want     bool
	}{{"division", "NVsurvey", false}, {"survey", "NVsurvey", true}, {"division", "NVindependent", true}, {"survey", "lowercase", true}, {"division", "lowercase", false}, {"section", "CATS", true}, {"division", "CATSD", true}} {
		if got := f.MayContain(tc.kind, tc.id); got != tc.want {
			t.Fatalf("%s %s: %t", tc.kind, tc.id, got)
		}
	}
}
