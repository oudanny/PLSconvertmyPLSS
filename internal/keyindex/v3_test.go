package keyindex

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func v3Fixture(t *testing.T, sparse bool) ([]byte, []Source) {
	t.Helper()
	rows := []Source{
		{ID: 1, Layer: 1, Township: "WA_A", Survey: "WA_A"},
		{ID: 2, Layer: 2, Township: "WA_A", First: "WA_A/S", Survey: "WA_A/S/X"},
		{ID: 3, Layer: 3, Township: "WA_A", First: "WA_A/S", Second: "WA_A/S/X", Survey: "WA_A/S/X"},
		{ID: 4, Layer: 3, Township: "WA_A", First: "WA_A/S", Second: "WA_A/S/X", Survey: "WA_A/S/X"},
		{ID: 5, Layer: 3, Township: "WA_A", First: "WA_A/S", Second: "WA_A/S/Y", Survey: "WA_A/S"},
		{ID: 6, Layer: 3, Township: "WA_A", First: "WA_A/S", Second: "WA_A/S/Z", Survey: "WA_A"},
		{ID: 7, Layer: 3, Township: "WA_A", First: "WA_A/S", Second: "WA_A/S/X", Survey: "WA_A/S/Xextra"},
		{ID: 8, Layer: 3, Township: "WA_A", First: "WA_A/S", Survey: "WA_A/Sextra"},
		{ID: 9, Layer: 3, Township: "WA_A", First: "WA_A/S", Survey: "WA_Aextra"},
		{ID: 10, Layer: 3, Township: "WA_A", Second: "external", Survey: "foreign-survey"},
		{ID: 11, Layer: 3, Township: "", Survey: "OR-survey"},
		{ID: 12, Layer: 3, Township: "C", Survey: "CA-survey"},
		{ID: 13, Layer: 3, Township: "WA_A", Survey: ""},
	}
	if sparse {
		for len(rows) < 1000 {
			rows = append(rows, Source{ID: uint32(len(rows) + 1), Layer: 3, Township: "WA_A", Second: "WA_A/other"})
		}
	}
	b := NewBuilder()
	for _, row := range rows {
		if err := b.Add(row); err != nil {
			t.Fatal(err)
		}
	}
	var data bytes.Buffer
	if _, err := b.WriteTo(&data); err != nil {
		t.Fatal(err)
	}
	return data.Bytes(), rows
}

func assertAllFieldsAndReverse(t *testing.T, x *Index, rows []Source) {
	t.Helper()
	if err := x.Validate(); err != nil {
		t.Fatal(err)
	}
	expected := map[string]map[string][]uint32{"township": {}, "section": {}, "division": {}, "survey": {}}
	for _, row := range rows {
		got, err := x.Fields(row.ID)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"PLSSID": row.Township, "FRSTDIVID": row.First, "SECDIVID": row.Second, "SURVID": row.Survey}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("fields row%d: %v != %v", row.ID, got, want)
		}
		for _, pair := range []struct {
			kind, id string
			layer    uint8
		}{{"township", row.Township, 1}, {"section", row.First, 2}, {"division", row.Second, 3}, {"survey", row.Survey, 3}} {
			if pair.layer == row.Layer && pair.id != "" {
				expected[pair.kind][pair.id] = append(expected[pair.kind][pair.id], row.ID)
			}
		}
	}
	for kind, values := range expected {
		for id, want := range values {
			got, err := x.Find(kind, id)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("Find(%s,%s): %v,%v != %v", kind, id, got, err, want)
			}
		}
	}
}

func TestV3SurveyFactoringPackedSparseAndOwnedResults(t *testing.T) {
	for _, sparse := range []bool{false, true} {
		t.Run(fmt.Sprint(sparse), func(t *testing.T) {
			data, rows := v3Fixture(t, sparse)
			if string(data[:8]) != "PLSSKEY3" {
				t.Fatal("not v3")
			}
			x, err := OpenView(data)
			if err != nil {
				t.Fatal(err)
			}
			mode := "packed"
			if sparse {
				mode = "sparse"
			}
			if x.m.SurveyMode != mode {
				t.Fatalf("mode %s != %s", x.m.SurveyMode, mode)
			}
			if len(x.m.Surveys) != 0 || len(x.exceptions) > 3 {
				t.Fatalf("unfactored survey metadata: %+v", x.m)
			}
			assertAllFieldsAndReverse(t, x, rows)
			routes, err := ReadRoutingPrefixes(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(routes, []string{"*", "OR", "WA"}) || !reflect.DeepEqual(routes, x.RoutingPrefixes()) {
				t.Fatalf("routes: %v", routes)
			}
			fields, err := x.Fields(7)
			if err != nil {
				t.Fatal(err)
			}
			id, err := x.ID(11)
			if err != nil {
				t.Fatal(err)
			}
			clear(data)
			if fields["SURVID"] != "WA_A/S/Xextra" || id != "OR-survey" {
				t.Fatal("returned survey string borrowed backing bytes")
			}
		})
	}
}

func TestV3ManySurveyCodesCrossByteBoundaries(t *testing.T) {
	for _, sparse := range []bool{false, true} {
		t.Run(fmt.Sprint(sparse), func(t *testing.T) {
			b := NewBuilder()
			var rows []Source
			count := 300
			if sparse {
				count = 10000
			}
			for i := 1; i <= count; i++ {
				survey := ""
				if i <= 300 {
					survey = fmt.Sprintf("WA_T%d", i)
				}
				row := Source{ID: uint32(i), Layer: 3, Township: "WA_T", Survey: survey}
				rows = append(rows, row)
				if err := b.Add(row); err != nil {
					t.Fatal(err)
				}
			}
			var data bytes.Buffer
			if _, err := b.WriteTo(&data); err != nil {
				t.Fatal(err)
			}
			x, err := OpenView(data.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			if x.m.SurveyBits != 9 {
				t.Fatalf("survey bits %d", x.m.SurveyBits)
			}
			assertAllFieldsAndReverse(t, x, rows)
		})
	}
}

func TestV3RejectsCorruptSurveyMetadataAndCodes(t *testing.T) {
	for _, sparse := range []bool{false, true} {
		t.Run(fmt.Sprint(sparse), func(t *testing.T) {
			data, _ := v3Fixture(t, sparse)
			x, err := OpenView(data)
			if err != nil {
				t.Fatal(err)
			}
			for _, alter := range []func(*Index){
				func(x *Index) { x.m.SurveyRows-- },
				func(x *Index) { x.m.SurveyDict[1].Base = 4 },
				func(x *Index) { delete(x.exceptions, "4:foreign-survey") },
				func(x *Index) { x.m.Routes = []string{"WA"} },
			} {
				x, err := OpenView(data)
				if err != nil {
					t.Fatal(err)
				}
				alter(x)
				if err := x.Validate(); err == nil {
					t.Fatal("corrupt survey metadata passed validation")
				}
			}
			at := 12 + int(binary.LittleEndian.Uint32(data[8:])) + int(x.m.Count)*int(x.m.Width)
			bad := append([]byte(nil), data...)
			if sparse {
				binary.LittleEndian.PutUint32(bad[at:], 0)
				if _, err := OpenView(bad); err == nil {
					t.Fatal("accepted sparse row zero")
				}
				bad = append([]byte(nil), data...)
				stride := 4 + (int(x.m.SurveyBits)+7)/8
				copy(bad[at+stride:at+stride+4], bad[at:at+4])
				if _, err := OpenView(bad); err == nil {
					t.Fatal("accepted duplicate sparse rows")
				}
			} else {
				bad[at] = 0xff
				x, err := OpenView(bad)
				if err == nil {
					if _, err = x.Fields(1); err == nil {
						t.Fatal("invalid survey code accepted")
					}
					if x.Validate() == nil {
						t.Fatal("invalid survey code passed full audit")
					}
				}
			}
		})
	}
}

func TestV3RepeatedSurveyAliasesRemainCompact(t *testing.T) {
	b := NewBuilder()
	for i := uint32(1); i <= 10000; i++ {
		s := fmt.Sprintf("WA_T/S%d", i)
		if err := b.Add(Source{ID: i, Layer: 3, Township: "WA_T", Second: s, Survey: s}); err != nil {
			t.Fatal(err)
		}
	}
	var legacy, latest bytes.Buffer
	if _, err := b.writeVersion(&legacy, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := b.WriteTo(&latest); err != nil {
		t.Fatal(err)
	}
	if latest.Len()*2 >= legacy.Len() {
		t.Fatalf("survey factoring failed: v2=%d v3=%d", legacy.Len(), latest.Len())
	}
	x, err := OpenView(latest.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if err = x.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(x.m.SurveyDict) != 2 || len(x.exceptions) != 0 {
		t.Fatalf("unexpected alias dictionary: %+v", x.m.SurveyDict)
	}
	if !strings.Contains(string(latest.Bytes()[:12+binary.LittleEndian.Uint32(latest.Bytes()[8:])]), `"survey_bits":1`) {
		t.Fatal("alias column was not bit packed")
	}
}
