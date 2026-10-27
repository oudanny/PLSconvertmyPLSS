package plss

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func packRectangle(x1, y1, x2, y2 float64) Geometry {
	return Geometry{Rings: [][][]float64{{{x1, y1}, {x2, y1}, {x2, y2}, {x1, y2}, {x1, y1}}}}
}

func packTownshipAttrs() map[string]any {
	return map[string]any{"PLSSID": "NV210010N0010W0", "STATEABBR": "NV", "PRINMERCD": "21", "PRINMER": "Mount Diablo", "TWNSHPNO": "001", "TWNSHPDIR": "N", "TWNSHPFRAC": "0", "RANGENO": "001", "RANGEDIR": "W", "RANGEFRAC": "0", "TWNSHPLAB": "1N 1W"}
}

func packDivisionAttrs(id, aliquot string) map[string]any {
	return map[string]any{"PLSSID": "NV210010N0010W0", "FRSTDIVID": "section-id", "FRSTDIVTYP": "SN", "FRSTDIVNO": "001", "FRSTDIVLAB": "1", "FRSTDIVTXT": "Section", "SECDIVID": id, "SECDIVTYP": "A", "SECDIVNO": aliquot, "QSEC": "SE", "QQSEC": aliquot, "OBJECTID": json.Number("9007199254740993"), "nullable": nil}
}

func createTestPack(t *testing.T) (string, *Pack) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Nevada pack.sqlite")
	w, err := CreatePack(path, PackMetadata{State: "nv", Source: "https://invalid.example/no-network"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	if err := w.Add(1, packTownshipAttrs(), packRectangle(0, 0, 10, 10)); err != nil {
		t.Fatal(err)
	}
	section := map[string]any{"PLSSID": "NV210010N0010W0", "FRSTDIVID": "section-id", "FRSTDIVTYP": "SN", "FRSTDIVNO": "01", "FRSTDIVLAB": "1", "FRSTDIVTXT": "Section"}
	if err := w.Add(2, section, packRectangle(0, 0, 4, 4)); err != nil {
		t.Fatal(err)
	}
	hole := packRectangle(0, 0, 1, 1)
	hole.Rings = append(hole.Rings, packRectangle(.25, .25, .75, .75).Rings[0])
	if err := w.Add(3, packDivisionAttrs("shared-id", "SWSE"), hole); err != nil {
		t.Fatal(err)
	}
	if err := w.Add(3, packDivisionAttrs("shared-id", "NESWSE"), packRectangle(1, 0, 2, 1)); err != nil {
		t.Fatal(err)
	}
	lot := packDivisionAttrs("lot-id", "")
	lot["SECDIVTYP"] = "L"
	lot["SECDIVNO"] = "1"
	lot["GOVLOT"] = "1"
	lot["QSEC"] = ""
	lot["QQSEC"] = ""
	if err := w.Add(3, lot, packRectangle(2, 0, 3, 1)); err != nil {
		t.Fatal(err)
	}
	metadata, err := w.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Version != 1 || metadata.State != "NV" || metadata.Counts["intersected"] != 3 {
		t.Fatalf("bad metadata: %+v", metadata)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		t.Fatalf("completed database is missing from requested filesystem path: %v, %v", info, err)
	}
	header, err := os.ReadFile(path)
	if err != nil || len(header) < 16 || string(header[:16]) != "SQLite format 3\x00" {
		t.Fatalf("requested path does not contain a SQLite database: %v", err)
	}
	p, err := OpenPack(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return path, p
}

func TestPackLookupExactBoundariesHolesAndFallback(t *testing.T) {
	_, p := createTestPack(t)
	for _, tt := range []struct {
		latitude, longitude float64
		layer               string
		count               int
	}{
		{.1, .1, "intersected", 1}, {.5, .5, "section", 1}, // hole excludes finer polygon
		{.5, .25, "intersected", 1}, // hole boundary belongs to polygon
		{.5, 1, "intersected", 2},   // adjacent polygons both retained
		{3, 3, "section", 1}, {5, 5, "township", 1},
	} {
		matches, err := p.Lookup(context.Background(), tt.latitude, tt.longitude)
		if err != nil || len(matches) != tt.count || matches[0].Layer != tt.layer {
			t.Errorf("Lookup(%g,%g) = %+v,%v; want %s/%d", tt.latitude, tt.longitude, matches, err, tt.layer, tt.count)
		}
	}
	if _, err := p.Lookup(context.Background(), 11, 11); !errors.Is(err, ErrNotFound) {
		t.Errorf("outside pack: %v", err)
	}
	for _, point := range []Point{{91, 0}, {0, 181}, {math.NaN(), 0}, {0, math.Inf(1)}} {
		if _, err := p.Lookup(context.Background(), point.Latitude, point.Longitude); err == nil {
			t.Errorf("accepted invalid point %+v", point)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Lookup(ctx, .1, .1); !errors.Is(err, context.Canceled) {
		t.Errorf("cancellation: %v", err)
	}
}

func TestPackFindDescriptionsIDsAndCoverage(t *testing.T) {
	_, p := createTestPack(t)
	d := Description{State: "nv", Meridian: "21", Township: 1, TownshipDirection: "N", Range: 1, RangeDirection: "W", Section: 1}
	m, err := p.Find(context.Background(), d)
	if err != nil || len(m) != 1 || m[0].Layer != "section" || !strings.Contains(m[0].Label, "Mount Diablo") {
		t.Fatalf("section find: %+v,%v", m, err)
	}
	d.Aliquot = "SW1/4 SE1/4"
	m, err = p.Find(context.Background(), d)
	if err != nil || len(m) != 2 {
		t.Fatalf("aliquot including smaller descendants: %+v,%v", m, err)
	}
	d.Aliquot = ""
	for _, lot := range []string{"1", "01", "001"} {
		d.Lot = lot
		m, err = p.Find(context.Background(), d)
		if err != nil || len(m) != 1 || m[0].ID() != "lot-id" {
			t.Fatalf("padded lot %s: %+v,%v", lot, m, err)
		}
	}
	d.Lot = ""
	d.Section = 0
	m, err = p.Find(context.Background(), d)
	if err != nil || len(m) != 1 || m[0].Layer != "township" {
		t.Fatalf("township: %+v,%v", m, err)
	}
	d.State = "CO"
	if _, err := p.Find(context.Background(), d); !errors.Is(err, ErrPackCoverage) {
		t.Errorf("wrong-state coverage: %v", err)
	}
	m, err = p.FindID(context.Background(), "division", "shared-id")
	if err != nil || len(m) != 2 {
		t.Fatalf("duplicate identifiers: %+v,%v", m, err)
	}
	if m[0].Attributes["OBJECTID"] != json.Number("9007199254740993") {
		t.Errorf("attribute precision lost: %v", m[0].Attributes["OBJECTID"])
	}
	if _, err := p.FindID(context.Background(), "division", `shared-id' OR 1=1 --`); !errors.Is(err, ErrNotFound) {
		t.Errorf("identifier was not parameterized: %v", err)
	}
	if _, err := p.FindID(context.Background(), "bogus", "id"); err == nil {
		t.Error("accepted invalid ID kind")
	}
	if _, err := p.FindID(context.Background(), "section", "section-id"); err != nil {
		t.Errorf("section ID: %v", err)
	}
}

func TestPackFullPrecisionAndAntimeridian(t *testing.T) {
	path := filepath.Join(t.TempDir(), "precision.sqlite")
	w, err := CreatePack(path, PackMetadata{State: "AK"})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	x := math.Nextafter(179.12345678912345, 180)
	g := Geometry{Rings: [][][]float64{{{x, 60.12345678912345}, {-179.456789123456, 60.12345678912345}, {-179.456789123456, 61}, {x, 61}, {x, 60.12345678912345}}}}
	attrs := map[string]any{"SECDIVID": "dateline", "unknown": []any{"nested", json.Number("9007199254740993")}}
	if err := w.Add(3, attrs, g); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	p, err := OpenPack(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for _, lon := range []float64{179.5, -179.9, 180, -180} {
		m, err := p.Lookup(context.Background(), 60.5, lon)
		if err != nil || len(m) != 1 {
			t.Fatalf("dateline lookup %g: %+v,%v", lon, m, err)
		}
		if !reflect.DeepEqual(m[0].Geometry, g) {
			t.Errorf("geometry float64 bits changed: %+v", m[0].Geometry)
		}
		wantJSON, _ := json.Marshal(attrs)
		gotJSON, _ := json.Marshal(m[0].Attributes)
		if string(wantJSON) != string(gotJSON) {
			t.Errorf("unknown attributes changed: %s vs %s", wantJSON, gotJSON)
		}
	}
	if _, err := p.Lookup(context.Background(), 60.5, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("dateline indexed whole earth: %v", err)
	}
}

func TestPackReadOnlyMetadataAndOverwriteProtection(t *testing.T) {
	path, p := createTestPack(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec("CREATE TABLE forbidden(x)"); err == nil {
		t.Error("pack opened writable")
	}
	metadata, err := p.Metadata()
	if err != nil {
		t.Fatal(err)
	}
	metadata.Counts["intersected"] = 999
	metadata2, _ := p.Metadata()
	if metadata2.Counts["intersected"] != 3 {
		t.Error("metadata exposed internal mutable counts")
	}
	if _, err := CreatePack(path, PackMetadata{State: "NV"}); !errors.Is(err, os.ErrExist) {
		t.Errorf("overwrite not refused: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Error("read-only operations changed pack")
	}
	missing := filepath.Join(t.TempDir(), "missing.sqlite")
	if _, err := OpenPack(missing); err == nil {
		t.Error("opened missing pack")
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Error("OpenPack created a missing file")
	}
}

func TestPackRejectsPartialFailedAndCorruptFiles(t *testing.T) {
	t.Run("partial after committed batch", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "partial.sqlite")
		w, err := CreatePack(path, PackMetadata{State: "NV"})
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < packBatchSize+1; i++ {
			if err := w.Add(3, packDivisionAttrs("same", "NE"), packRectangle(0, 0, 1, 1)); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenPack(path); err == nil || !strings.Contains(err.Error(), "incomplete") {
			t.Errorf("partial accepted: %v", err)
		}
	})
	t.Run("failed add invalidates writer", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "failed.sqlite")
		w, err := CreatePack(path, PackMetadata{State: "NV"})
		if err != nil {
			t.Fatal(err)
		}
		defer w.Close()
		if err := w.Add(3, packDivisionAttrs("same", "NE"), Geometry{}); err == nil {
			t.Error("invalid geometry accepted")
		}
		if _, err := w.Finish(); err == nil {
			t.Error("finished failed import")
		}
		if _, err := OpenPack(path); err == nil {
			t.Error("opened failed import")
		}
	})
	t.Run("not sqlite", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "garbage.sqlite")
		if err := os.WriteFile(path, []byte("not a database"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenPack(path); err == nil {
			t.Error("opened non-SQLite file")
		}
	})
	t.Run("unsupported version", func(t *testing.T) {
		path, p := createTestPack(t)
		_ = p.Close()
		dsn, _ := packDSN(path, "rw")
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("PRAGMA user_version=999"); err != nil {
			t.Fatal(err)
		}
		_ = db.Close()
		if _, err := OpenPack(path); err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Errorf("unsupported accepted: %v", err)
		}
	})
	t.Run("corrupt compressed feature", func(t *testing.T) {
		path, p := createTestPack(t)
		_ = p.Close()
		dsn, _ := packDSN(path, "rw")
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("UPDATE features SET data=x'000102' WHERE layer=3"); err != nil {
			t.Fatal(err)
		}
		_ = db.Close()
		p, err = OpenPack(path)
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		if _, err := p.Lookup(context.Background(), .1, .1); err == nil || !strings.Contains(err.Error(), "corrupt") {
			t.Errorf("corruption not surfaced: %v", err)
		}
	})
}

func BenchmarkPackImportSynthetic(b *testing.B) {
	for i := 0; i < b.N; i++ {
		path := filepath.Join(b.TempDir(), "import.sqlite")
		w, err := CreatePack(path, PackMetadata{State: "NV"})
		if err != nil {
			b.Fatal(err)
		}
		for n := 0; n < 10000; n++ {
			x := -119 + float64(n%100)*.01
			y := 38 + float64(n/100)*.01
			if err := w.Add(3, packDivisionAttrs("synthetic", "SWSE"), packRectangle(x, y, x+.009, y+.009)); err != nil {
				b.Fatal(err)
			}
		}
		if _, err := w.Finish(); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(10000, "features/op")
}

func TestPackUnlocatedRecordsPreservedAndIdentified(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unlocated.sqlite")
	w, err := CreatePack(path, PackMetadata{State: "NV"})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	attrs := map[string]any{"OBJECTID": json.Number("9007199254740993"), "STATEABBR": "NV", "PLSSID": "NV210050N0280E0", "FRSTDIVID": "NV210050N0280E0SN190", "SECDIVID": "NV210050N0280E0SN190V6", "SECDIVTYP": "V", "SECDIVNOTE": "Requires Survey", "SHAPE.STArea()": nil, "GISACRE": json.Number("0"), "nested": map[string]any{"extra": "retained"}}
	if err := w.AddUnlocated(3, attrs); err != nil {
		t.Fatal(err)
	}
	spatialAttrs := packDivisionAttrs("shared-with-unlocated", "NE")
	if err := w.AddUnlocated(3, spatialAttrs); err != nil {
		t.Fatal(err)
	}
	if err := w.Add(3, spatialAttrs, packRectangle(0, 0, 1, 1)); err != nil {
		t.Fatal(err)
	}
	metadata, err := w.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Counts["intersected"] != 1 || metadata.UnlocatedCounts["intersected"] != 2 {
		t.Fatalf("wrong spatial/source accounting: %+v", metadata)
	}
	p, err := OpenPack(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err := p.FindID(context.Background(), "division", attrs["SECDIVID"].(string)); !errors.Is(err, ErrNoGeometry) || !strings.Contains(err.Error(), attrs["SECDIVID"].(string)) {
		t.Fatalf("missing-geometry ID error: %v", err)
	}
	if _, err := p.FindID(context.Background(), "division", "absent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown ID: %v", err)
	}
	if matches, err := p.FindID(context.Background(), "division", "shared-with-unlocated"); err != nil || len(matches) != 1 {
		t.Errorf("geometry preferred when present: %+v,%v", matches, err)
	}
	matches, err := p.Lookup(context.Background(), .5, .5)
	if err != nil || len(matches) != 1 || matches[0].ID() != "shared-with-unlocated" {
		t.Fatalf("spatial lookup changed: %+v,%v", matches, err)
	}
	records, err := p.Unlocated(context.Background())
	if err != nil || len(records) != 2 {
		t.Fatalf("unlocated audit: %+v,%v", records, err)
	}
	if records[0].Layer != "intersected" || !strings.Contains(records[0].Reason, "Requires Survey") {
		t.Errorf("unlocated audit context missing: %+v", records[0])
	}
	wantJSON, _ := json.Marshal(attrs)
	gotJSON, _ := json.Marshal(records[0].Attributes)
	if string(wantJSON) != string(gotJSON) {
		t.Errorf("source attributes changed: %s vs %s", wantJSON, gotJSON)
	}
	meta, _ := p.Metadata()
	meta.UnlocatedCounts["intersected"] = 99
	meta2, _ := p.Metadata()
	if meta2.UnlocatedCounts["intersected"] != 2 {
		t.Error("unlocated counts share mutable state")
	}
}

func TestPackLegacyMetadataWithoutUnlocatedCounts(t *testing.T) {
	path, p := createTestPack(t)
	metadata, _ := p.Metadata()
	metadata.UnlocatedCounts = nil
	_ = p.Close()
	dsn, _ := packDSN(path, "rw")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	document, _ := json.Marshal(metadata)
	if _, err := db.Exec("UPDATE pack_metadata SET document=?", string(document)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DROP TABLE unlocated"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	p, err = OpenPack(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	metadata, err = p.Metadata()
	if err != nil || metadata.UnlocatedCounts["intersected"] != 0 {
		t.Errorf("old pack metadata: %+v,%v", metadata, err)
	}
	records, err := p.Unlocated(context.Background())
	if err != nil || len(records) != 0 {
		t.Errorf("old pack unlocated audit: %+v,%v", records, err)
	}
	if _, err := p.Lookup(context.Background(), .1, .1); err != nil {
		t.Errorf("old pack spatial lookup: %v", err)
	}
}
