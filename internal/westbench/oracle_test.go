package main

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/rand"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/oudanny/PLSconvertmyPLSS/internal/atlas"
	"github.com/oudanny/PLSconvertmyPLSS/internal/regionatlas"
	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

func sourceFixture(t *testing.T, state string, lo, hi float64, duplicate bool) source {
	t.Helper()
	path := filepath.Join(t.TempDir(), state+".sqlite")
	w, err := plss.CreatePack(path, plss.PackMetadata{State: state})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	box := func(lo, hi float64) plss.Geometry {
		return plss.Geometry{Rings: [][][]float64{{{lo, lo}, {hi, lo}, {hi, hi}, {lo, hi}, {lo, lo}}}}
	}
	for _, row := range []struct {
		layer int
		attrs map[string]any
		g     plss.Geometry
	}{
		{1, map[string]any{"PLSSID": state + "T"}, box(0, 10)},
		{2, map[string]any{"PLSSID": state + "T", "FRSTDIVID": state + "TS"}, box(0, 6)},
		{3, map[string]any{"PLSSID": state + "T", "FRSTDIVID": state + "TS", "SECDIVID": "shared", "SURVID": "survey-" + state}, box(lo, hi)},
	} {
		if err := w.Add(row.layer, row.attrs, row.g); err != nil {
			t.Fatal(err)
		}
	}
	if duplicate {
		if err := w.Add(3, map[string]any{"PLSSID": state + "T", "FRSTDIVID": state + "TS", "SECDIVID": "shared"}, box(lo, hi)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.AddUnlocated(3, map[string]any{"PLSSID": "orphan-town", "SECDIVID": "unlocated-" + state}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	s, err := openSource(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.pack.Close(); s.db.Close() })
	return s
}

func TestOracleGlobalLayerAndDuplicateSourceRows(t *testing.T) {
	sources := []source{sourceFixture(t, "NV", 0, 1, true), sourceFixture(t, "CA", .5, 2, false)}
	for _, tc := range []struct {
		point float64
		want  []string
	}{
		{.75, []string{"CA:intersected:3:shared", "NV:intersected:3:shared", "NV:intersected:4:shared"}},
		{1.5, []string{"CA:intersected:3:shared"}},
		{5, []string{"CA:section:2:CATS", "NV:section:2:NVTS"}},
		{9, []string{"CA:township:1:CAT", "NV:township:1:NVT"}},
		{20, []string{}},
	} {
		got, err := sourceMatches(context.Background(), sources, plss.Point{Latitude: tc.point, Longitude: tc.point})
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("point%g got%v err%v want%v", tc.point, got, err, tc.want)
		}
	}
	got := actualKeys([]regionatlas.Hit{{Hit: atlas.Hit{FeatureID: 4, Layer: "intersected", ID: "shared"}, State: "NV"}, {Hit: atlas.Hit{FeatureID: 3, Layer: "intersected", ID: "shared"}, State: "NV"}})
	if !reflect.DeepEqual(got, []string{"NV:intersected:3:shared", "NV:intersected:4:shared"}) {
		t.Fatal("atlas identity comparison collapsed duplicates")
	}
	for _, tc := range []struct {
		kind, id string
		want     int
		err      error
	}{{"division", "shared", 3, nil}, {"survey", "survey-CA", 1, nil}, {"division", "unlocated-NV", 0, plss.ErrNoGeometry}, {"township", "orphan-town", 0, plss.ErrNotFound}} {
		got, err := sourceReverse(context.Background(), sources, tc.kind, tc.id)
		if len(got) != tc.want || !errors.Is(err, tc.err) {
			t.Fatalf("reverse %s %s: %v %v", tc.kind, tc.id, got, err)
		}
	}
	explicit, err := explicitReverseSamples(sources[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []reverseQuery{{"survey", "survey-NV"}, {"division", "unlocated-NV"}, {"township", "orphan-town"}} {
		found := false
		for _, q := range explicit {
			found = found || q == want
		}
		if !found {
			t.Fatalf("missing explicit sample %+v", want)
		}
	}
}

func TestBoundarySamplesAndSourceChecksum(t *testing.T) {
	g := plss.Geometry{Rings: [][][]float64{{{179, 0}, {-179, 0}, {-179, 1}, {179, 1}, {179, 0}}, {{179.25, .25}, {179.75, .25}, {179.75, .75}, {179.25, .75}, {179.25, .25}}}}
	rng := rand.New(rand.NewSource(17))
	for range 100 {
		points := boundarySamples(g, rng)
		for _, p := range points[:2] {
			if math.Abs(p.Longitude) < 179 {
				t.Fatal("antimeridian midpoint crossed the world")
			}
			inside, err := g.ContainsPoint(p)
			if err != nil || !inside {
				t.Fatalf("expected polygon/hole boundary %+v: %v %v", p, inside, err)
			}
		}
	}
	doc, err := json.Marshal(struct {
		Geometry plss.Geometry `json:"geometry"`
	}{g})
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	z := zlib.NewWriter(&compressed)
	z.Write(doc)
	z.Close()
	data := compressed.Bytes()
	if got, err := decodeGeometry(data); err != nil || !reflect.DeepEqual(got, g) {
		t.Fatalf("decode %v", err)
	}
	bad := bytes.Clone(data)
	bad[len(bad)-1] ^= 1
	if _, err := decodeGeometry(bad); err == nil {
		t.Fatal("source checksum corruption was ignored")
	}
}

func TestMeasurementExcludesTimingAllocations(t *testing.T) {
	measured, err := measure(1000, func(int) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if measured.Allocs != 0 || measured.Bytes != 0 {
		t.Fatalf("counted benchmark machinery: %+v", measured)
	}
	if measured.Queries != 1000 || measured.P99 < measured.Median {
		t.Fatal("invalid percentile statistics")
	}
}
