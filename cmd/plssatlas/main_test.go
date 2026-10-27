package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oudanny/PLSconvertmyPLSS/coord"
	"github.com/oudanny/PLSconvertmyPLSS/internal/atlas"
	"github.com/oudanny/PLSconvertmyPLSS/internal/columns"
	"github.com/oudanny/PLSconvertmyPLSS/internal/compactgeom"
	"github.com/oudanny/PLSconvertmyPLSS/internal/keyindex"
	"github.com/oudanny/PLSconvertmyPLSS/internal/spatialindex"
	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

func smallAtlas(t *testing.T, mode string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	paths := map[string]string{"keys": filepath.Join(dir, "keys"), "index": filepath.Join(dir, "index"), "geometry": filepath.Join(dir, "geometry")}
	key := keyindex.NewBuilder()
	geom, e := compactgeom.NewBuilder(paths["geometry"], 0)
	if e != nil {
		t.Fatal(e)
	}
	defer geom.Close()
	var entries []spatialindex.Entry
	var attrFile *os.File
	var attrs *columns.Writer
	if mode != "core" {
		paths["attributes"] = filepath.Join(dir, "attributes")
		attrFile, e = os.Create(paths["attributes"])
		if e != nil {
			t.Fatal(e)
		}
		defer attrFile.Close()
		attrs, e = columns.NewWriter(attrFile, columns.Options{Dictionary: true, Essential: mode == "essential", BlockSize: 2, Verify: true})
		if e != nil {
			t.Fatal(e)
		}
	}
	id := "NV210010N0010E0SN010ASWSE"
	for i := uint32(1); i <= 2; i++ {
		x1 := -119.82 + float64(i-1)*.01
		x2 := x1 + .01
		g := plss.Geometry{Rings: [][][]float64{{{x1, 39.52}, {x2, 39.52}, {x2, 39.54}, {x1, 39.54}, {x1, 39.52}}}}
		if e := geom.Add(i, g); e != nil {
			t.Fatal(e)
		}
		if e := key.Add(keyindex.Source{ID: i, Layer: 3, Township: "NV210010N0010E0", First: "NV210010N0010E0SN010", Second: id}); e != nil {
			t.Fatal(e)
		}
		bounds, e := g.IndexBounds()
		if e != nil {
			t.Fatal(e)
		}
		for _, b := range bounds {
			entries = append(entries, spatialindex.Entry{MinLon: math.Nextafter32(float32(b.MinLon), float32(math.Inf(-1))), MaxLon: math.Nextafter32(float32(b.MaxLon), float32(math.Inf(1))), MinLat: math.Nextafter32(float32(b.MinLat), float32(math.Inf(-1))), MaxLat: math.Nextafter32(float32(b.MaxLat), float32(math.Inf(1))), ID: i, Layer: 3})
		}
		if attrs != nil {
			a := map[string]json.RawMessage{"SECDIVID": json.RawMessage(`"` + id + `"`), "OBJECTID": json.RawMessage(`9007199254740993`), "source_note": json.RawMessage(`"original source"`), "nullable": json.RawMessage(`null`)}
			if e := attrs.Add(columns.Record{ID: i, Layer: 3, Attributes: a}); e != nil {
				t.Fatal(e)
			}
		}
	}
	if _, e := geom.Finish(); e != nil {
		t.Fatal(e)
	}
	if attrs != nil {
		if _, e := attrs.Finish(); e != nil {
			t.Fatal(e)
		}
		if e := attrFile.Close(); e != nil {
			t.Fatal(e)
		}
	}
	kf, e := os.Create(paths["keys"])
	if e != nil {
		t.Fatal(e)
	}
	if _, e := key.WriteTo(kf); e != nil {
		t.Fatal(e)
	}
	kf.Close()
	index, e := spatialindex.Build(entries, 4)
	if e != nil {
		t.Fatal(e)
	}
	index, e = index.Quantize(8)
	if e != nil {
		t.Fatal(e)
	}
	ix, e := os.Create(paths["index"])
	if e != nil {
		t.Fatal(e)
	}
	if _, e := index.WriteTo(ix); e != nil {
		t.Fatal(e)
	}
	ix.Close()
	if mode == "broken-metadata" {
		f, e := os.OpenFile(paths["attributes"], os.O_RDWR, 0600)
		if e != nil {
			t.Fatal(e)
		}
		if _, e := f.WriteAt([]byte{0, 0}, 48); e != nil {
			t.Fatal(e)
		}
		f.Close()
	}
	path := filepath.Join(dir, "example.atlas")
	source := plss.PackMetadata{Version: 1, State: "NV", Source: "https://example.invalid/offline", CreatedAt: "2026-10-27T00:00:00Z", Counts: map[string]int{"township": 0, "section": 0, "intersected": 2}, UnlocatedCounts: map[string]int{"intersected": 1}}
	audit := []plss.UnlocatedRecord{{Layer: "intersected", Attributes: map[string]any{"SECDIVID": "requires-survey"}, Reason: "source has no geometry"}}
	if _, e := atlas.Build(path, source, "fixture", paths, audit); e != nil {
		t.Fatal(e)
	}
	return path, id
}

func invoke(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, errout bytes.Buffer
	e := run(args, &out, &errout)
	return out.String(), e
}
func decodeOutput(t *testing.T, text string) output {
	t.Helper()
	var out output
	d := json.NewDecoder(strings.NewReader(text))
	d.UseNumber()
	if e := d.Decode(&out); e != nil {
		t.Fatal(e)
	}
	return out
}

func TestCoordinateCommandsAndBufferedParity(t *testing.T) {
	path, _ := smallAtlas(t, "core")
	p := coord.Point{Latitude: 39.5296, Longitude: -119.8138}
	u, e := coord.ToUTM(p)
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		command, value string
		lat, lon       float64
	}{{"latlon", "39.5296,-119.8138", p.Latitude, p.Longitude}, {"latlon", `39°31'46.56"N, 119°48'49.68"W`, p.Latitude, p.Longitude}, {"utm", u.String(), p.Latitude, p.Longitude}} {
		for _, buffered := range []bool{false, true} {
			args := []string{tc.command, "--value", tc.value, "--data", path, "--json"}
			if buffered {
				args = append(args, "--buffered")
			}
			text, e := invoke(t, args...)
			if e != nil {
				t.Fatal(e)
			}
			out := decodeOutput(t, text)
			if !out.Experimental || len(out.Results) != 1 || len(out.Results[0].Matches) != 1 {
				t.Fatalf("unexpected output: %+v", out)
			}
			r := out.Results[0]
			if math.Abs(r.Coordinates.Latitude-tc.lat) > 1e-7 || math.Abs(r.Coordinates.Longitude-tc.lon) > 1e-7 {
				t.Fatalf("coordinate changed: %+v", r.Coordinates)
			}
			if r.Coordinates.DD == "" || r.Coordinates.DDM == "" || r.Coordinates.DMS == "" || r.Coordinates.ISO6709 == "" || r.Coordinates.UTM == nil {
				t.Fatal("missing coordinate notation")
			}
			if r.Matches[0].Attributes != nil {
				t.Fatal("core lookup should not include invented source attributes")
			}
		}
	}
	text, e := invoke(t, "latlon", "--value", "39.5296,-119.8138", "--data", path)
	if e != nil || !strings.Contains(text, "Experimental PLSS atlas") || !strings.Contains(text, "PLSS [intersected]") {
		t.Fatalf("text output: %s %v", text, e)
	}
}

func TestReverseExactIDsPreservesDuplicatesAndAuditErrors(t *testing.T) {
	path, id := smallAtlas(t, "core")
	text, e := invoke(t, "plss", "--id", id, "--data", path, "--json")
	if e != nil {
		t.Fatal(e)
	}
	out := decodeOutput(t, text)
	if len(out.Results) != 2 || len(out.Notes) == 0 {
		t.Fatalf("duplicate area results missing: %+v", out)
	}
	for _, r := range out.Results {
		if !r.Representative || r.Matches[0].ID != id || r.Coordinates.UTM == nil {
			t.Fatalf("bad reverse output: %+v", r)
		}
	}
	if out.Results[0].Coordinates.Longitude == out.Results[1].Coordinates.Longitude {
		t.Fatal("different source polygons collapsed to one coordinate")
	}
	if _, e := invoke(t, "plss", "--id", "requires-survey", "--data", path); !errors.Is(e, plss.ErrNoGeometry) {
		t.Fatalf("unlocated error: %v", e)
	}
	if _, e := invoke(t, "latlon", "--value", "0,0", "--data", path); !errors.Is(e, plss.ErrNotFound) {
		t.Fatalf("out-of-coverage error: %v", e)
	}
}

func TestMetadataRequiresFullComponentAndStaysOffHotPath(t *testing.T) {
	for _, mode := range []string{"core", "essential"} {
		path, _ := smallAtlas(t, mode)
		text, e := invoke(t, "latlon", "--value", "39.5296,-119.8138", "--data", path, "--metadata", "--json")
		if e == nil || !strings.Contains(e.Error(), "--metadata requires") || text != "" {
			t.Fatalf("metadata mode %s: %s %v", mode, text, e)
		}
	}
	path, id := smallAtlas(t, "full")
	for _, args := range [][]string{{"latlon", "--value", "39.5296,-119.8138"}, {"plss", "--id", id}} {
		text, e := invoke(t, append(args, "--data", path, "--metadata", "--json")...)
		if e != nil {
			t.Fatal(e)
		}
		out := decodeOutput(t, text)
		a := out.Results[0].Matches[0].Attributes
		if a["OBJECTID"] != json.Number("9007199254740993") || a["source_note"] != "original source" {
			t.Fatalf("source attributes lost: %#v", a)
		}
	}
	broken, id := smallAtlas(t, "broken-metadata")
	for _, args := range [][]string{{"latlon", "--value", "39.5296,-119.8138"}, {"plss", "--id", id}} {
		if _, e := invoke(t, append(args, "--data", broken)...); e != nil {
			t.Fatalf("unrequested attribute block was read: %v", e)
		}
		if _, e := invoke(t, append(args, "--data", broken, "--metadata")...); e == nil {
			t.Fatal("requested corrupt metadata was silently ignored")
		}
	}
}

func TestCLIValidationAndHelp(t *testing.T) {
	for _, args := range [][]string{{}, {"--help"}, {"latlon", "--help"}, {"plss", "--help"}} {
		if _, e := invoke(t, args...); e != nil {
			t.Fatalf("help: %v", e)
		}
	}
	for _, args := range [][]string{{"unknown"}, {"latlon", "--value", "0,0"}, {"latlon", "--data", "missing"}, {"utm", "--value", "bad", "--data", "missing"}, {"plss", "--data", "missing"}, {"plss", "--id", "a", "--layer", "bad", "--data", "missing"}, {"latlon", "--value", "0,0", "--data", "missing", "extra"}, {"latlon", "--value", "0,0", "--data", "missing", "--service-url", "https://invalid"}} {
		if _, e := invoke(t, args...); e == nil {
			t.Fatalf("invalid flags accepted: %v", args)
		}
	}
	c, note := formatCoordinates(coord.Point{Latitude: 89, Longitude: 0})
	if c.UTM != nil || note == "" || c.DMS == "" {
		t.Fatal("polar coordinate formatting incorrect")
	}
}
