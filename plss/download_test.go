package plss

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func downloadTestFeature(id int64) map[string]any {
	return map[string]any{"attributes": map[string]any{"OBJECTID": id, "PLSSID": "NV210010N0010E0", "STATEABBR": "NV", "TWNSHPLAB": "1N 1E"}, "geometry": Geometry{Rings: [][][]float64{{{-116, 39}, {-115.99, 39}, {-115.99, 39.01}, {-116, 39.01}, {-116, 39}}}}}
}

func downloadTestService(t *testing.T, mutate func(int, []int64, []any) []any) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Error("download did not use POST")
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		var layer int
		if _, err := fmt.Sscanf(r.URL.Path, "/%d/query", &layer); err != nil {
			t.Error(err)
			return
		}
		if r.Form.Get("returnIdsOnly") == "true" {
			if r.Form.Get("where") != "PLSSID LIKE 'NV%'" {
				t.Errorf("wrong state filter: %s", r.Form.Get("where"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"objectIdFieldName": "OBJECTID", "objectIds": []int64{int64(layer*10 + 2), int64(layer*10 + 1), int64(layer*10 + 1)}})
			return
		}
		if r.Form.Get("returnGeometry") != "true" || r.Form.Get("outFields") != "*" || (r.Form.Get("outSR") != "4326" && r.Form.Get("outSR") != "") {
			t.Errorf("incomplete geometry query: %v", r.Form)
		}
		var ids []int64
		for _, s := range strings.Split(r.Form.Get("objectIds"), ",") {
			id, e := strconv.ParseInt(s, 10, 64)
			if e != nil {
				t.Error(e)
				return
			}
			ids = append(ids, id)
		}
		var features []any
		for i := len(ids) - 1; i >= 0; i-- {
			features = append(features, downloadTestFeature(ids[i]))
		}
		if mutate != nil {
			features = mutate(layer, ids, features)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"features": features})
	}))
	t.Cleanup(s.Close)
	return s
}

func TestDownloadPackComplete(t *testing.T) {
	s := downloadTestService(t, nil)
	dir := t.TempDir()
	path := filepath.Join(dir, "nevada.sqlite")
	progress := map[int]int{}
	meta, err := DownloadPack(context.Background(), path, DownloadOptions{BaseURL: s.URL, HTTPClient: s.Client(), Workers: 2, Progress: func(p DownloadProgress) {
		if p.Completed > p.Total {
			t.Error("invalid progress")
		}
		progress[p.Layer] = p.Completed
	}})
	if err != nil {
		t.Fatal(err)
	}
	if meta.State != "NV" || meta.Source != s.URL || meta.Counts["township"] != 2 || meta.Counts["section"] != 2 || meta.Counts["intersected"] != 2 {
		t.Fatalf("wrong metadata: %+v", meta)
	}
	for layer := 1; layer <= 3; layer++ {
		if progress[layer] != 2 {
			t.Fatalf("incomplete progress: %v", progress)
		}
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	header := make([]byte, 16)
	if _, err := file.Read(header); err != nil {
		t.Fatal(err)
	}
	if string(header) != "SQLite format 3\x00" {
		t.Fatalf("wrong format: %q", header)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files not removed: %v %v", entries, err)
	}
}

func TestDownloadPackMissingFeatureDoesNotPublish(t *testing.T) {
	s := downloadTestService(t, func(layer int, ids []int64, features []any) []any {
		if layer == 2 {
			return features[:1]
		}
		return features
	})
	dir := t.TempDir()
	path := filepath.Join(dir, "nevada.sqlite")
	_, err := DownloadPack(context.Background(), path, DownloadOptions{BaseURL: s.URL, HTTPClient: s.Client()})
	if err == nil || !strings.Contains(err.Error(), "omitted expected object ID") {
		t.Fatalf("wrong error: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial pack was published: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed temporary pack not removed: %v %v", entries, err)
	}
}

func TestDownloadPackPreservesConfirmedUnlocatedRecord(t *testing.T) {
	s := downloadTestService(t, func(layer int, ids []int64, features []any) []any {
		if layer == 3 {
			for _, raw := range features {
				feature := raw.(map[string]any)
				attrs := feature["attributes"].(map[string]any)
				if attrs["OBJECTID"] == int64(31) {
					delete(feature, "geometry")
					attrs["SHAPE.STArea()"] = nil
					attrs["GISACRE"] = 67.1 // stale descriptive acreage does not imply stored geometry
					attrs["SECDIVNOTE"] = "Requires Survey"
					attrs["SECDIVID"] = "NV-no-survey"
				}
			}
		}
		return features
	})
	path := filepath.Join(t.TempDir(), "nevada.sqlite")
	meta, err := DownloadPack(context.Background(), path, DownloadOptions{BaseURL: s.URL, HTTPClient: s.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if meta.Counts["intersected"] != 1 || meta.UnlocatedCounts["intersected"] != 1 {
		t.Fatalf("unlocated record not accounted for: %+v", meta)
	}
	p, err := OpenPack(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	audit, err := p.Unlocated(context.Background())
	if err != nil || len(audit) != 1 {
		t.Fatalf("missing audit row: %+v %v", audit, err)
	}
	if audit[0].Attributes["SECDIVNOTE"] != "Requires Survey" || audit[0].Attributes["SECDIVID"] != "NV-no-survey" {
		t.Fatalf("unlocated attributes changed: %+v", audit[0])
	}
	if _, err := p.FindID(context.Background(), "division", "NV-no-survey"); !errors.Is(err, ErrNoGeometry) {
		t.Fatalf("unlocated ID should not have an invented coordinate: %v", err)
	}
}

func TestDownloadRejectsMissingProjectedGeometryWithNativePolygon(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		feature := downloadTestFeature(1)
		feature["attributes"].(map[string]any)["SHAPE.STArea()"] = nil
		if r.Form.Get("outSR") == "4326" {
			delete(feature, "geometry")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"features": []any{feature}})
	}))
	defer s.Close()
	d := packDownloader{base: s.URL, http: s.Client()}
	if _, err := d.batch(context.Background(), 3, "OBJECTID", []int64{1}); err == nil || !strings.Contains(err.Error(), "has native geometry") {
		t.Fatalf("projection omission was accepted: %v", err)
	}
}

func TestDownloadRejectsUnconfirmedAbsentGeometry(t *testing.T) {
	for _, area := range []any{"missing", json.Number("123"), "invalid"} {
		t.Run(fmt.Sprint(area), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attrs := map[string]any{"OBJECTID": 1}
				if area != "missing" {
					attrs["SHAPE.STArea()"] = area
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"features": []any{map[string]any{"attributes": attrs}}})
			}))
			defer s.Close()
			d := packDownloader{base: s.URL, http: s.Client()}
			if _, err := d.batch(context.Background(), 3, "OBJECTID", []int64{1}); err == nil {
				t.Fatal("unconfirmed absent source geometry accepted")
			}
		})
	}
}

func TestDownloadConfirmsRealNevadaUnlocatedFixture(t *testing.T) {
	fixture, err := os.ReadFile("testdata/blm_nevada_unlocated.json")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		call := calls.Add(1)
		if r.Form.Get("objectIds") != "7362838" || r.Form.Get("returnGeometry") != "true" {
			t.Errorf("unexpected confirmation request: %v", r.Form)
		}
		if call == 1 && r.Form.Get("outSR") != "4326" {
			t.Error("initial download should request WGS84")
		}
		if call == 2 && r.Form.Get("outSR") != "" {
			t.Error("confirmation must retain native spatial reference")
		}
		_, _ = w.Write(fixture)
	}))
	defer s.Close()
	d := packDownloader{base: s.URL, http: s.Client()}
	features, err := d.batch(context.Background(), 3, "OBJECTID", []int64{7362838})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || len(features) != 1 || len(features[0].Geometry.Rings) != 0 || features[0].Attributes["SECDIVID"] != "NV210050N0280E0SN190V6" {
		t.Fatalf("source record was not preserved and confirmed: %+v calls=%d", features, calls.Load())
	}
}

func TestDownloadRefusesExistingOutput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "already.sqlite")
	original := []byte("keep this file")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	_, err := DownloadPack(context.Background(), path, DownloadOptions{})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("wrong error: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(original) {
		t.Fatalf("existing file changed: %q %v", got, err)
	}
}

func TestDownloadPreservesOutputCreatedDuringDownload(t *testing.T) {
	s := downloadTestService(t, nil)
	dir := t.TempDir()
	path := filepath.Join(dir, "nevada.sqlite")
	_, err := DownloadPack(context.Background(), path, DownloadOptions{BaseURL: s.URL, HTTPClient: s.Client(), Progress: func(p DownloadProgress) {
		if p.Layer == 3 && p.Completed == p.Total {
			if err := os.WriteFile(path, []byte("created by someone else"), 0600); err != nil {
				t.Error(err)
			}
		}
	}})
	if err == nil || !strings.Contains(err.Error(), "publish data pack") {
		t.Fatalf("expected no-overwrite publishing failure, got %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "created by someone else" {
		t.Fatalf("concurrent output was replaced: %q %v", got, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary file cleanup failed: %v %v", entries, err)
	}
}

func TestDownloadCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { cancel(); w.WriteHeader(http.StatusTooManyRequests) }))
	defer s.Close()
	path := filepath.Join(t.TempDir(), "nevada.sqlite")
	_, err := DownloadPack(ctx, path, DownloadOptions{BaseURL: s.URL, HTTPClient: s.Client()})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("wrong cancellation: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled pack was published")
	}
}

func TestDownloadRetriesTransientFailures(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(429)
			return
		}
		_, _ = w.Write([]byte(`{"objectIdFieldName":"OBJECTID","objectIds":[2,1,2]}`))
	}))
	defer s.Close()
	d := packDownloader{base: s.URL, http: s.Client()}
	snapshot, err := d.snapshot(context.Background(), 1, "NV")
	if err != nil || calls.Load() != 2 || len(snapshot.ids) != 2 || snapshot.ids[0] != 1 {
		t.Fatalf("retry/snapshot failed: %+v %v calls=%d", snapshot, err, calls.Load())
	}
}

func TestDownloadBatchRejectsWrongIDs(t *testing.T) {
	for _, tc := range []struct {
		name      string
		ids       []int64
		truncated bool
	}{
		{"missing", []int64{1}, false},
		{"duplicate", []int64{1, 1}, false},
		{"unexpected", []int64{1, 3}, false},
		{"truncated", []int64{1, 2}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var f []any
				for _, id := range tc.ids {
					f = append(f, downloadTestFeature(id))
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"features": f, "exceededTransferLimit": tc.truncated})
			}))
			defer s.Close()
			d := packDownloader{base: s.URL, http: s.Client()}
			if _, err := d.batch(context.Background(), 1, "OBJECTID", []int64{1, 2}); err == nil {
				t.Fatal("accepted incomplete or incorrect batch")
			}
		})
	}
}

func TestDownloadServiceErrorsAndOptions(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"bad query"}}`))
	}))
	defer s.Close()
	d := packDownloader{base: s.URL, http: s.Client()}
	if _, err := d.post(context.Background(), 1, url.Values{"f": {"json"}}); err == nil || !strings.Contains(err.Error(), "bad query") || calls.Load() != 1 {
		t.Fatalf("nonretryable error: %v", err)
	}
	for _, o := range []DownloadOptions{{State: "TX"}, {Workers: 9}, {Workers: -1}, {BaseURL: "file:///tmp"}, {BaseURL: "http://example.com?x=1"}} {
		if _, e := DownloadPack(context.Background(), filepath.Join(t.TempDir(), "x.sqlite"), o); e == nil {
			t.Errorf("accepted invalid options: %+v", o)
		}
	}
	if retryAfter("999999999") != 30*time.Second || retryAfter("-2") != 0 || retryAfter("2") != 2*time.Second {
		t.Fatal("incorrect Retry-After bounds")
	}
}

func TestSupportedDownloadStatesAreIndependent(t *testing.T) {
	want := []string{"AZ", "CA", "CO", "ID", "MT", "NM", "NV", "OR", "UT", "WA", "WY"}
	states := SupportedDownloadStates()
	if !reflect.DeepEqual(states, want) {
		t.Fatalf("supported states: %v", states)
	}
	states[0] = "TX"
	if !reflect.DeepEqual(SupportedDownloadStates(), want) {
		t.Fatal("caller changed the supported state list")
	}
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer s.Close()
	for _, state := range []string{"TX", "AK", "HI", "NV' OR 1=1 --", "N_V", "N%", "N V"} {
		_, err := DownloadPack(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"), DownloadOptions{State: state, BaseURL: s.URL, HTTPClient: s.Client()})
		if err == nil || !strings.Contains(err.Error(), "unsupported download state") {
			t.Errorf("accepted invalid state %q: %v", state, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid state caused %d network requests", calls.Load())
	}
}

func TestDownloadWesternStatesWithEmptyLayers(t *testing.T) {
	for index, state := range SupportedDownloadStates() {
		t.Run(state, func(t *testing.T) {
			// Rotate the populated layer so both empty parents and empty
			// subdivisions are exercised. Empty layers require no batch query.
			populated := 1 + index%3
			var batches atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
					return
				}
				var layer int
				if _, err := fmt.Sscanf(r.URL.Path, "/%d/query", &layer); err != nil {
					t.Error(err)
					return
				}
				if r.Form.Get("returnIdsOnly") == "true" {
					if got := r.Form.Get("where"); got != "PLSSID LIKE '"+state+"%'" {
						t.Errorf("state filter: %q", got)
					}
					ids := []int64{}
					if layer == populated {
						ids = []int64{7}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"objectIdFieldName": "OBJECTID", "objectIds": ids})
					return
				}
				batches.Add(1)
				if layer != populated || r.Form.Get("objectIds") != "7" {
					t.Errorf("unexpected batch: layer=%d params=%v", layer, r.Form)
				}
				feature := downloadTestFeature(7)
				attrs := feature["attributes"].(map[string]any)
				attrs["PLSSID"] = state + "210010N0010E0"
				attrs["STATEABBR"] = state
				_ = json.NewEncoder(w).Encode(map[string]any{"features": []any{feature}})
			}))
			defer s.Close()
			progress := map[int]DownloadProgress{}
			path := filepath.Join(t.TempDir(), "state.sqlite")
			meta, err := DownloadPack(context.Background(), path, DownloadOptions{State: " " + strings.ToLower(state) + " ", BaseURL: s.URL, HTTPClient: s.Client(), Progress: func(p DownloadProgress) { progress[p.Layer] = p }})
			if err != nil {
				t.Fatal(err)
			}
			if meta.State != state || batches.Load() != 1 {
				t.Fatalf("state/batches: %+v %d", meta, batches.Load())
			}
			for i, name := range []string{"township", "section", "intersected"} {
				want := 0
				if i+1 == populated {
					want = 1
				}
				p, ok := progress[i+1]
				if !ok || p.Completed != want || p.Total != want || meta.Counts[name] != want || meta.UnlocatedCounts[name] != 0 {
					t.Fatalf("incorrect count/progress for %s: %+v %+v", name, meta, progress)
				}
			}
			pack, err := OpenPack(path)
			if err != nil {
				t.Fatal(err)
			}
			defer pack.Close()
			hits, err := pack.Lookup(context.Background(), 39.005, -115.995)
			if err != nil || len(hits) != 1 {
				t.Fatalf("pack with empty layers failed lookup: %+v %v", hits, err)
			}
		})
	}
}

func TestDownloadSnapshotDistinguishesEmptyFromMalformed(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		valid          bool
	}{
		{"empty", `{"objectIdFieldName":"OBJECTID","objectIds":[]}`, true},
		{"missing_ids", `{"objectIdFieldName":"OBJECTID"}`, false},
		{"null_ids", `{"objectIdFieldName":"OBJECTID","objectIds":null}`, false},
		{"missing_field", `{"objectIds":[]}`, false},
		{"blank_field", `{"objectIdFieldName":" ","objectIds":[]}`, false},
		{"wrong_ids_type", `{"objectIdFieldName":"OBJECTID","objectIds":{}}`, false},
		{"truncated_empty", `{"objectIdFieldName":"OBJECTID","objectIds":[],"exceededTransferLimit":true}`, false},
		{"invalid_id", `{"objectIdFieldName":"OBJECTID","objectIds":[0]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tc.response)) }))
			defer s.Close()
			d := packDownloader{base: s.URL, http: s.Client()}
			snapshot, err := d.snapshot(context.Background(), 3, "WA")
			if (err == nil) != tc.valid {
				t.Fatalf("snapshot: %+v %v", snapshot, err)
			}
			if tc.valid && (snapshot.layer != 3 || snapshot.field != "OBJECTID" || len(snapshot.ids) != 0) {
				t.Fatalf("incorrect empty snapshot: %+v", snapshot)
			}
		})
	}
}

func TestDownloadEntirelyEmptyStateDoesNotPublish(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = r.ParseForm()
		if r.Form.Get("returnIdsOnly") != "true" {
			t.Error("empty state requested a feature batch")
		}
		_, _ = w.Write([]byte(`{"objectIdFieldName":"OBJECTID","objectIds":[]}`))
	}))
	defer s.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "state.sqlite")
	_, err := DownloadPack(context.Background(), path, DownloadOptions{State: "WA", BaseURL: s.URL, HTTPClient: s.Client()})
	if err == nil || !strings.Contains(err.Error(), "no published records for WA") || calls.Load() != 3 {
		t.Fatalf("entirely empty state: %v calls=%d", err, calls.Load())
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("empty state created files: %v %v", entries, err)
	}
}
