package mapengine

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/oudanny/PLSconvertmyPLSS/offline"
	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

type fakeReader struct{ err error }

func (f fakeReader) Lookup(context.Context, float64, float64) ([]offline.Hit, error) {
	return f.FindID("division", "shared")
}
func (f fakeReader) FindID(string, string) ([]offline.Hit, error) {
	return []offline.Hit{{State: "NV", FeatureID: 1, ID: "shared", Layer: "intersected"}, {State: "CA", FeatureID: 2, ID: "shared", Layer: "intersected"}}, f.err
}
func (f fakeReader) Full(h offline.Hit) (plss.Match, error) {
	return plss.Match{Label: h.ID, Geometry: plss.Geometry{Rings: [][][]float64{{{0, 0}, {1, 0}, {1, 1}, {0, 0}}, {{.1, .1}, {.2, .1}, {.1, .2}, {.1, .1}}}}, Representative: plss.Point{Latitude: .5, Longitude: .5}}, nil
}

func TestConversionsWithoutFileAndInvalidPoints(t *testing.T) {
	for _, value := range []string{"39.5296,-119.8138", "39:31:46.56N,119:48:49.68W", "+39.5296-119.8138/"} {
		r, err := Execute(context.Background(), nil, "convert", Request{Value: value})
		if err != nil || math.Abs(r.Coordinates.Latitude-39.5296) > 1e-10 || r.Coordinates.UTM == nil || r.Coordinates.DMS == "" {
			t.Fatalf("conversion: %+v %v", r, err)
		}
	}
	if _, err := Execute(context.Background(), nil, "lookup", Request{Latitude: math.NaN()}); err == nil {
		t.Fatal("NaN accepted")
	}
	if _, err := Execute(context.Background(), nil, "lookup", Request{Longitude: 181}); err == nil {
		t.Fatal("invalid longitude")
	}
	if _, err := Execute(context.Background(), nil, "find", Request{ID: "x"}); err == nil {
		t.Fatal("find without atlas")
	}
}
func TestCoverageGapsKeepCoordinatesAndErrorsPropagate(t *testing.T) {
	r, err := Execute(context.Background(), fakeReader{plss.ErrNotFound}, "lookup", Request{Latitude: 40, Longitude: -75})
	if err != nil || !r.NoMatch || r.Coordinates == nil || len(r.Matches) != 0 {
		t.Fatalf("coverage gap: %+v %v", r, err)
	}
	if _, err := Execute(context.Background(), fakeReader{plss.ErrNoGeometry}, "find", Request{ID: "missinggeometry"}); !errors.Is(err, plss.ErrNoGeometry) {
		t.Fatal("unlocated record must remain distinguishable")
	}
}
func TestDuplicatesRingsAndRepresentativePreserved(t *testing.T) {
	r, err := Execute(context.Background(), fakeReader{}, "find", Request{ID: "shared"})
	if err != nil || len(r.Matches) != 2 || !r.Representative || len(r.Matches[0].Geometry.Rings) != 2 || r.Matches[0].State == r.Matches[1].State {
		t.Fatalf("lost source data: %+v %v", r, err)
	}
	if r.Coordinates.Latitude != .5 || r.Matches[1].Representative.Longitude != .5 {
		t.Fatal("representative coordinates")
	}
}

type describedReader struct {
	fakeReader
	attributes map[string]any
}

func (r describedReader) Full(hit offline.Hit) (plss.Match, error) {
	m, err := r.fakeReader.Full(hit)
	m.Attributes = r.attributes
	return m, err
}

func TestDescriptionsKeepOriginalIdentifiersAndQualifyParents(t *testing.T) {
	const township = "NV210190N0190E0"
	const section = township + "SN110"
	const division = section + "ASWNE"
	r, err := Execute(context.Background(), describedReader{attributes: map[string]any{"PLSSID": township, "FRSTDIVID": section, "SECDIVID": division}}, "lookup", Request{Latitude: 39.5296, Longitude: -119.8138})
	if err != nil {
		t.Fatal(err)
	}
	m := r.Matches[0]
	if m.Description != "SW¼ of NE¼ · Sec. 11 · T. 19 N · R. 19 E" || m.DescriptionDetail != "Mount Diablo Meridian · Nevada" || m.Identifiers.Division != division || m.ID != "shared" {
		t.Fatalf("description changed raw identity or lost readable fields: %#v", m)
	}
	encoded, err := json.Marshal(m)
	if err != nil || !strings.Contains(string(encoded), `"description":`) || !strings.Contains(string(encoded), `"description_detail":`) {
		t.Fatalf("description missing from export: %s %v", encoded, err)
	}
	r, err = Execute(context.Background(), describedReader{attributes: map[string]any{"PLSSID": township, "FRSTDIVID": section}}, "lookup", Request{})
	if err != nil || !strings.Contains(r.Matches[0].DescriptionDetail, "Subdivision identifier unavailable; showing parent description.") {
		t.Fatalf("parent falsely presented as exact subdivision: %#v %v", r, err)
	}
	r, err = Execute(context.Background(), describedReader{}, "lookup", Request{})
	if err != nil || r.Matches[0].Description != "Published polygon (no cadastral identifier)" {
		t.Fatalf("missing source identity invented: %#v %v", r, err)
	}
}
