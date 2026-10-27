package atlas_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/oudanny/PLSconvertmyPLSS/internal/atlas"
	"github.com/oudanny/PLSconvertmyPLSS/internal/columns"
	"github.com/oudanny/PLSconvertmyPLSS/internal/compactgeom"
	"github.com/oudanny/PLSconvertmyPLSS/internal/keyindex"
	"github.com/oudanny/PLSconvertmyPLSS/internal/spatialindex"
	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

type fixture struct {
	path       string
	reader     *atlas.Reader
	geometry   []plss.Geometry
	attributes []map[string]any
	paths      map[string]string
	source     plss.PackMetadata
	audit      []plss.UnlocatedRecord
}

func rect(x1, y1, x2, y2 float64) plss.Geometry {
	return plss.Geometry{Rings: [][][]float64{{{x1, y1}, {x2, y1}, {x2, y2}, {x1, y2}, {x1, y1}}}}
}
func bound(v float64, upper bool) float32 {
	f := float32(v)
	if upper && float64(f) < v {
		return math.Nextafter32(f, float32(math.Inf(1)))
	}
	if !upper && float64(f) > v {
		return math.Nextafter32(f, float32(math.Inf(-1)))
	}
	return f
}

func buildFixture(t *testing.T, withAttributes bool, decimals, quantization int) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{path: filepath.Join(dir, "test.atlas"), paths: map[string]string{}}
	donut := rect(0, 0, 1, 1)
	donut.Rings = append(donut.Rings, rect(.25, .25, .75, .75).Rings[0])
	x := math.Nextafter(5.12345678912345, 6)
	y := 5.321987654321234
	precise := rect(x, y, x+.125, y+.125)
	precise.Rings[0][0] = append(precise.Rings[0][0], math.Nextafter(17.125, 18))
	precise.Rings[0][4] = append(precise.Rings[0][4], math.Nextafter(17.125, 18))
	f.geometry = []plss.Geometry{rect(0, 0, 10, 10), rect(0, 0, 4, 4), donut, rect(1, 0, 2, 1), precise, rect(179.7, 60, -179.8, 61)}
	township, section := "NV210010N0010E0", "NV210010N0010E0SN010"
	keys := keyindex.NewBuilder()
	f.paths["geometry"] = filepath.Join(dir, "exact.cgeom")
	geom, e := compactgeom.NewBuilder(f.paths["geometry"], decimals)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = geom.Close() })
	var entries []spatialindex.Entry
	var attrsFile *os.File
	var attrsWriter *columns.Writer
	if withAttributes {
		f.paths["attributes"] = filepath.Join(dir, "attrs.cols")
		attrsFile, e = os.Create(f.paths["attributes"])
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { _ = attrsFile.Close() })
		attrsWriter, e = columns.NewWriter(attrsFile, columns.Options{BlockSize: 2, Dictionary: true, Verify: true})
		if e != nil {
			t.Fatal(e)
		}
	}
	for i, g := range f.geometry {
		id := uint32(i + 1)
		layer := uint8(3)
		if i == 0 {
			layer = 1
		} else if i == 1 {
			layer = 2
		}
		s := keyindex.Source{ID: id, Layer: layer, Township: township}
		if layer >= 2 {
			s.First = section
		}
		if layer == 3 {
			s.Second = section + "ASWSE"
			if i == 4 {
				s.Second = section + "Aprecise"
			}
			if i == 5 {
				s.Second = section + "Adateline"
				s.Survey = "independent-survey"
			}
		}
		if e := keys.Add(s); e != nil {
			t.Fatal(e)
		}
		if e := geom.Add(id, g); e != nil {
			t.Fatal(e)
		}
		bounds, e := g.IndexBounds()
		if e != nil {
			t.Fatal(e)
		}
		for _, b := range bounds {
			entries = append(entries, spatialindex.Entry{MinLon: bound(b.MinLon, false), MaxLon: bound(b.MaxLon, true), MinLat: bound(b.MinLat, false), MaxLat: bound(b.MaxLat, true), ID: id, Layer: layer})
		}
		a := map[string]any{"PLSSID": s.Township, "FRSTDIVID": s.First, "SECDIVID": s.Second, "SURVID": s.Survey, "OBJECTID": json.Number(strconv.FormatInt(9007199254740993+int64(i), 10)), "source_note": "original source record", "nullable": nil, "array": []any{"ordered", json.Number("1.2300e+2"), nil}}
		f.attributes = append(f.attributes, a)
		if attrsWriter != nil {
			raw := map[string]json.RawMessage{}
			for k, v := range a {
				encoded, e := json.Marshal(v)
				if e != nil {
					t.Fatal(e)
				}
				raw[k] = encoded
			}
			if e := attrsWriter.Add(columns.Record{ID: id, Layer: layer, Attributes: raw}); e != nil {
				t.Fatal(e)
			}
		}
	}
	if _, e := geom.Finish(); e != nil {
		t.Fatal(e)
	}
	if attrsWriter != nil {
		if _, e := attrsWriter.Finish(); e != nil {
			t.Fatal(e)
		}
		if e := attrsFile.Close(); e != nil {
			t.Fatal(e)
		}
	}
	f.paths["keys"] = filepath.Join(dir, "keys.bin")
	kf, e := os.Create(f.paths["keys"])
	if e != nil {
		t.Fatal(e)
	}
	if _, e := keys.WriteTo(kf); e != nil {
		t.Fatal(e)
	}
	kf.Close()
	index, e := spatialindex.Build(entries, 4)
	if e != nil {
		t.Fatal(e)
	}
	if quantization != 0 {
		index, e = index.Quantize(quantization)
		if e != nil {
			t.Fatal(e)
		}
	}
	f.paths["index"] = filepath.Join(dir, "index.bin")
	ix, e := os.Create(f.paths["index"])
	if e != nil {
		t.Fatal(e)
	}
	if _, e := index.WriteTo(ix); e != nil {
		t.Fatal(e)
	}
	ix.Close()
	f.source = plss.PackMetadata{Version: 1, State: "NV", Source: "https://example.invalid/source", CreatedAt: "2026-10-27T00:00:00Z", Counts: map[string]int{"township": 1, "section": 1, "intersected": 4}, UnlocatedCounts: map[string]int{"township": 0, "section": 0, "intersected": 1}}
	f.audit = []plss.UnlocatedRecord{{Layer: "intersected", Attributes: map[string]any{"SECDIVID": "unlocated-id", "OBJECTID": json.Number("9007199254740993"), "SECDIVNOTE": "Requires Survey"}, Reason: "source has no geometry"}}
	if _, e := atlas.Build(f.path, f.source, "fixture-source-hash", f.paths, f.audit); e != nil {
		t.Fatal(e)
	}
	f.reader, e = atlas.Open(f.path)
	if decimals != 0 {
		if e == nil {
			f.reader.Close()
			t.Fatal("rounded geometry was accepted")
		}
		return f
	}
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = f.reader.Close() })
	return f
}

func TestAtlasExactLookupHolesBoundariesFallbackAndDateline(t *testing.T) {
	for _, quantization := range []int{0, 8, 16} {
		t.Run(strconv.Itoa(quantization), func(t *testing.T) {
			f := buildFixture(t, false, 0, quantization)
			for _, tc := range []struct {
				lat, lon float64
				layer    string
				ids      []uint32
			}{
				{.1, .1, "intersected", []uint32{3}}, {.5, .5, "section", []uint32{2}}, {.5, .25, "intersected", []uint32{3}}, {.5, 1, "intersected", []uint32{3, 4}}, {3, 3, "section", []uint32{2}}, {8, 8, "township", []uint32{1}}, {60.5, 180, "intersected", []uint32{6}}, {60.5, -180, "intersected", []uint32{6}},
			} {
				hits, e := f.reader.Lookup(context.Background(), tc.lat, tc.lon)
				if e != nil {
					t.Fatal(e)
				}
				got := map[uint32]bool{}
				for _, h := range hits {
					if h.Layer != tc.layer {
						t.Fatalf("wrong layer: %+v", hits)
					}
					got[h.FeatureID] = true
				}
				if len(hits) != len(tc.ids) {
					t.Fatalf("lookup(%g,%g): %+v", tc.lat, tc.lon, hits)
				}
				for _, id := range tc.ids {
					if !got[id] {
						t.Fatalf("missing ID%d at(%g,%g): %+v", id, tc.lat, tc.lon, hits)
					}
				}
			}
			if _, e := f.reader.Lookup(context.Background(), 20, 20); !errors.Is(e, plss.ErrNotFound) {
				t.Fatalf("missing coverage: %v", e)
			}
			if e := f.reader.Verify(); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestAtlasDuplicateIDsAuditAndOptionalAttributes(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(strconv.FormatBool(full), func(t *testing.T) {
			f := buildFixture(t, full, 0, 8)
			id := f.attributes[2]["SECDIVID"].(string)
			hits, e := f.reader.FindID("division", id)
			if e != nil || len(hits) != 2 || hits[0].FeatureID != 3 || hits[1].FeatureID != 4 {
				t.Fatalf("duplicate ID lost: %+v %v", hits, e)
			}
			if _, e := f.reader.FindID("division", "unlocated-id"); !errors.Is(e, plss.ErrNoGeometry) {
				t.Fatalf("unlocated ID: %v", e)
			}
			if _, e := f.reader.FindID("division", "missing-id"); !errors.Is(e, plss.ErrNotFound) {
				t.Fatalf("unknown ID: %v", e)
			}
			if f.reader.Manifest.Unlocated[0].Attributes["OBJECTID"] != json.Number("9007199254740993") {
				t.Fatal("unlocated numeric precision changed")
			}
			for i, g := range f.geometry {
				h := atlas.Hit{FeatureID: uint32(i + 1)}
				h.ID, _ = f.reader.Keys.ID(h.FeatureID)
				h.Layer = map[uint8]string{1: "township", 2: "section", 3: "intersected"}[f.reader.Keys.Layer(h.FeatureID)]
				m, e := f.reader.Full(h)
				if e != nil {
					t.Fatal(e)
				}
				assertGeometryBits(t, g, m.Geometry)
				if full {
					if !reflect.DeepEqual(m.Attributes, f.attributes[i]) {
						t.Fatalf("source attributes changed: %#v vs %#v", m.Attributes, f.attributes[i])
					}
				} else {
					if len(m.Attributes) != 4 {
						t.Fatalf("core invented source attributes: %+v", m.Attributes)
					}
					if _, ok := m.Attributes["OBJECTID"]; ok {
						t.Fatal("core should not claim source OBJECTID")
					}
				}
				inside, e := g.ContainsPoint(m.Representative)
				if e != nil || !inside {
					t.Fatalf("representative outside feature: %+v %v", m.Representative, e)
				}
			}
		})
	}
}

func assertGeometryBits(t *testing.T, want, got plss.Geometry) {
	t.Helper()
	if len(want.Rings) != len(got.Rings) {
		t.Fatal("ring count changed")
	}
	for i, ring := range want.Rings {
		if len(ring) != len(got.Rings[i]) {
			t.Fatal("vertex count changed")
		}
		for j, v := range ring {
			if len(v) != len(got.Rings[i][j]) {
				t.Fatal("ordinate count changed")
			}
			for k, x := range v {
				if math.Float64bits(x) != math.Float64bits(got.Rings[i][j][k]) {
					t.Fatalf("coordinate bits changed at%d/%d/%d", i, j, k)
				}
			}
		}
	}
}

func TestAtlasRejectsInvalidPointsCancellationAndRoundedGeometry(t *testing.T) {
	f := buildFixture(t, false, 0, 8)
	for _, p := range []plss.Point{{Latitude: math.NaN()}, {Longitude: math.Inf(1)}, {Latitude: 91}, {Longitude: 181}} {
		if _, e := f.reader.Lookup(context.Background(), p.Latitude, p.Longitude); e == nil {
			t.Fatal("invalid coordinate accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := f.reader.Lookup(ctx, .1, .1); !errors.Is(e, context.Canceled) {
		t.Fatalf("cancellation not propagated: %v", e)
	}
	buildFixture(t, false, 7, 8)
}

func TestAtlasIntegrityAndNoOverwrite(t *testing.T) {
	f := buildFixture(t, true, 0, 8)
	before, e := os.ReadFile(f.path)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := atlas.Build(f.path, f.source, "other", f.paths, nil); !errors.Is(e, os.ErrExist) {
		t.Fatalf("overwrite was not refused: %v", e)
	}
	after, e := os.ReadFile(f.path)
	if e != nil || !bytes.Equal(before, after) {
		t.Fatal("existing atlas changed")
	}
	truncated := filepath.Join(t.TempDir(), "truncated.atlas")
	if e := os.WriteFile(truncated, before[:len(before)-1], 0600); e != nil {
		t.Fatal(e)
	}
	if a, e := atlas.Open(truncated); e == nil {
		a.Close()
		t.Fatal("truncated atlas accepted")
	}
	section := f.reader.Manifest.Sections["attributes"]
	position := section.Offset + section.Size - 1
	write, e := os.OpenFile(f.path, os.O_RDWR, 0600)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := write.WriteAt([]byte{before[position] ^ 0x01}, position); e != nil {
		t.Fatal(e)
	}
	write.Close()
	if e := f.reader.Verify(); e == nil {
		t.Fatal("modified component checksum was accepted")
	}
}

func TestAtlasFullDerivesIdentityFromFeatureID(t *testing.T) {
	f := buildFixture(t, false, 0, 8)
	m, e := f.reader.Full(atlas.Hit{FeatureID: 3, Layer: "township", ID: "unrelated-label"})
	if e != nil {
		t.Fatal(e)
	}
	want, _ := f.reader.Keys.ID(3)
	if m.Layer != "intersected" || m.Label != want {
		t.Fatalf("caller-supplied stale labels were trusted: %+v", m)
	}
}

func TestSurveyIndexIgnoresNonIntersectedLayers(t *testing.T) {
	b := keyindex.NewBuilder()
	if e := b.Add(keyindex.Source{ID: 1, Layer: 1, Township: "town", Survey: "same-survey"}); e != nil {
		t.Fatal(e)
	}
	if e := b.Add(keyindex.Source{ID: 2, Layer: 3, Township: "town", Survey: "same-survey"}); e != nil {
		t.Fatal(e)
	}
	var data bytes.Buffer
	if _, e := b.WriteTo(&data); e != nil {
		t.Fatal(e)
	}
	index, e := keyindex.Open(bytes.NewReader(data.Bytes()), int64(data.Len()))
	if e != nil {
		t.Fatal(e)
	}
	ids, e := index.Find("survey", "same-survey")
	if e != nil || !reflect.DeepEqual(ids, []uint32{2}) {
		t.Fatalf("survey lookup returned non-intersected feature: %v %v", ids, e)
	}
}

func TestAtlasVerifyRejectsMissingOrMislabeledSpatialRecords(t *testing.T) {
	for _, problem := range []string{"omitted", "wrong-layer"} {
		t.Run(problem, func(t *testing.T) {
			f := buildFixture(t, false, 0, 8)
			var entries []spatialindex.Entry
			for i, g := range f.geometry {
				id := uint32(i + 1)
				if problem == "omitted" && id == 3 {
					continue
				}
				layer := f.reader.Keys.Layer(id)
				if problem == "wrong-layer" && id == 3 {
					layer = 2
				}
				bounds, e := g.IndexBounds()
				if e != nil {
					t.Fatal(e)
				}
				for _, b := range bounds {
					entries = append(entries, spatialindex.Entry{MinLon: bound(b.MinLon, false), MaxLon: bound(b.MaxLon, true), MinLat: bound(b.MinLat, false), MaxLat: bound(b.MaxLat, true), ID: id, Layer: layer})
				}
			}
			index, e := spatialindex.Build(entries, 4)
			if e != nil {
				t.Fatal(e)
			}
			index, e = index.Quantize(8)
			if e != nil {
				t.Fatal(e)
			}
			component := filepath.Join(t.TempDir(), "inconsistent-index.bin")
			file, e := os.Create(component)
			if e != nil {
				t.Fatal(e)
			}
			if _, e := index.WriteTo(file); e != nil {
				t.Fatal(e)
			}
			if e := file.Close(); e != nil {
				t.Fatal(e)
			}
			f.paths["index"] = component
			path := filepath.Join(t.TempDir(), "inconsistent.atlas")
			if _, e := atlas.Build(path, f.source, "fixture-source-hash", f.paths, f.audit); e != nil {
				t.Fatal(e)
			}
			a, e := atlas.Open(path)
			if e != nil {
				t.Fatalf("lightweight Open should leave coverage verification explicit: %v", e)
			}
			defer a.Close()
			// The component bytes and checksums are all valid. Only cross-component
			// ID/layer validation can detect this silent lookup inconsistency.
			e = a.Verify()
			if e == nil {
				t.Fatalf("Verify accepted %s spatial record", problem)
			}
			if problem == "wrong-layer" && !strings.Contains(strings.ToLower(e.Error()), "layer") {
				t.Fatalf("unexpected mismatch error: %v", e)
			}
		})
	}
}

func TestAtlasMappedMatchesBufferedReader(t *testing.T) {
	for _, withAttributes := range []bool{false, true} {
		for _, quantization := range []int{0, 8, 16} {
			t.Run(strconv.FormatBool(withAttributes)+"/"+strconv.Itoa(quantization), func(t *testing.T) {
				f := buildFixture(t, withAttributes, 0, quantization)
				mapped, e := atlas.OpenMapped(f.path)
				if e != nil {
					t.Fatal(e)
				}
				defer mapped.Close()
				if e := mapped.Verify(); e != nil {
					t.Fatal(e)
				}
				for _, p := range []plss.Point{{Latitude: .1, Longitude: .1}, {Latitude: .5, Longitude: .5}, {Latitude: .5, Longitude: .25}, {Latitude: .5, Longitude: 1}, {Latitude: 3, Longitude: 3}, {Latitude: 8, Longitude: 8}, {Latitude: 60.5, Longitude: 180}, {Latitude: 60.5, Longitude: -180}} {
					want, e := f.reader.Lookup(context.Background(), p.Latitude, p.Longitude)
					if e != nil {
						t.Fatal(e)
					}
					got, e := mapped.Lookup(context.Background(), p.Latitude, p.Longitude)
					if e != nil {
						t.Fatal(e)
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("mapped lookup differs at%+v: %+v vs %+v", p, got, want)
					}
					for _, hit := range got {
						wantFull, e := f.reader.Full(hit)
						if e != nil {
							t.Fatal(e)
						}
						gotFull, e := mapped.Full(hit)
						if e != nil {
							t.Fatal(e)
						}
						assertGeometryBits(t, wantFull.Geometry, gotFull.Geometry)
						if !reflect.DeepEqual(gotFull, wantFull) {
							t.Fatalf("mapped full record differs for%+v", hit)
						}
					}
				}
				for _, query := range []struct{ kind, id string }{{"township", f.attributes[0]["PLSSID"].(string)}, {"section", f.attributes[1]["FRSTDIVID"].(string)}, {"division", f.attributes[2]["SECDIVID"].(string)}, {"survey", "independent-survey"}} {
					want, e := f.reader.FindID(query.kind, query.id)
					if e != nil {
						t.Fatal(e)
					}
					got, e := mapped.FindID(query.kind, query.id)
					if e != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("mapped ID differs: %+v %v", got, e)
					}
				}
				if _, e := mapped.FindID("division", "unlocated-id"); !errors.Is(e, plss.ErrNoGeometry) {
					t.Fatalf("mapped audit error: %v", e)
				}
				if _, e := mapped.Lookup(context.Background(), 20, 20); !errors.Is(e, plss.ErrNotFound) {
					t.Fatalf("mapped absent coverage: %v", e)
				}
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if _, e := mapped.Lookup(ctx, .1, .1); !errors.Is(e, context.Canceled) {
					t.Fatalf("mapped cancellation: %v", e)
				}
			})
		}
	}
}

func TestAtlasMappedConcurrentMethodsAndClose(t *testing.T) {
	for _, withAttributes := range []bool{false, true} {
		t.Run(strconv.FormatBool(withAttributes), func(t *testing.T) {
			f := buildFixture(t, withAttributes, 0, 8)
			id := f.attributes[2]["SECDIVID"].(string)
			for round := 0; round < 8; round++ {
				mapped, e := atlas.OpenMapped(f.path)
				if e != nil {
					t.Fatal(e)
				}
				// Full returns owned values, so retaining a record across Close must
				// not retain pointers to the OS mapping.
				retained, e := mapped.Full(atlas.Hit{FeatureID: 5})
				if e != nil {
					t.Fatal(e)
				}
				const nworkers = 8
				ready := make(chan struct{}, nworkers)
				start := make(chan struct{})
				errs := make(chan error, nworkers)
				var workers sync.WaitGroup
				for worker := 0; worker < nworkers; worker++ {
					workers.Add(1)
					go func(worker int) {
						defer workers.Done()
						if _, e := mapped.Lookup(context.Background(), .5, 1); e != nil {
							errs <- e
							ready <- struct{}{}
							return
						}
						ready <- struct{}{}
						<-start
						for attempt := 0; attempt < 100; attempt++ {
							var e error
							switch (worker + attempt) % 4 {
							case 0:
								_, e = mapped.Lookup(context.Background(), .5, 1)
							case 1:
								_, e = mapped.FindID("division", id)
							case 2:
								_, e = mapped.Full(atlas.Hit{FeatureID: 5})
							case 3:
								e = mapped.Verify()
							}
							if e != nil && !errors.Is(e, os.ErrClosed) {
								errs <- e
								return
							}
							if attempt%8 == 0 {
								runtime.Gosched()
							}
						}
					}(worker)
				}
				for i := 0; i < nworkers; i++ {
					<-ready
				}
				close(start)
				runtime.Gosched()
				if e := mapped.Close(); e != nil {
					t.Fatal(e)
				}
				workers.Wait()
				close(errs)
				for e := range errs {
					t.Errorf("concurrent mapped operation: %v", e)
				}
				if e := mapped.Close(); e != nil {
					t.Fatalf("second Close: %v", e)
				}
				if _, e := mapped.Lookup(context.Background(), .1, .1); !errors.Is(e, os.ErrClosed) {
					t.Fatalf("Lookup after Close: %v", e)
				}
				if _, e := mapped.FindID("division", id); !errors.Is(e, os.ErrClosed) {
					t.Fatalf("FindID after Close: %v", e)
				}
				if _, e := mapped.Full(atlas.Hit{FeatureID: 5}); !errors.Is(e, os.ErrClosed) {
					t.Fatalf("Full after Close: %v", e)
				}
				if e := mapped.Verify(); !errors.Is(e, os.ErrClosed) {
					t.Fatalf("Verify after Close: %v", e)
				}
				assertGeometryBits(t, f.geometry[4], retained.Geometry)
				if withAttributes && !reflect.DeepEqual(retained.Attributes, f.attributes[4]) {
					t.Fatal("returned source attributes changed after unmapping")
				}
			}
		})
	}
}
