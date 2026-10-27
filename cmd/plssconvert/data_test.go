package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

type noNetworkTransport struct{ calls int }

func (r *noNetworkTransport) RoundTrip(*http.Request) (*http.Response, error) {
	r.calls++
	return nil, errors.New("network forbidden in offline test")
}

func testNevadaPack(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nevada.sqlite")
	w, err := plss.CreatePack(path, plss.PackMetadata{State: "NV", Source: plss.DefaultURL})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	attrs := map[string]any{"OBJECTID": 1, "STATEABBR": "NV", "PRINMERCD": "21", "TWNSHPNO": "019", "TWNSHPDIR": "N", "TWNSHPFRAC": "0", "RANGENO": "019", "RANGEDIR": "E", "RANGEFRAC": "0", "PLSSID": "NV210190N0190E0", "FRSTDIVID": "NV210190N0190E0SN120", "FRSTDIVNO": "12", "FRSTDIVTYP": "SN", "SECDIVID": "NV210190N0190E0SN120ANE", "SECDIVNO": "NE", "SECDIVTYP": "A"}
	geometry := plss.Geometry{Rings: [][][]float64{{{-119.82, 39.52}, {-119.82, 39.54}, {-119.80, 39.54}, {-119.80, 39.52}, {-119.82, 39.52}}}}
	for layer := 1; layer <= 3; layer++ {
		if err = w.Add(layer, attrs, geometry); err != nil {
			t.Fatal(err)
		}
	}
	if err = w.AddUnlocated(3, map[string]any{"OBJECTID": 99, "STATEABBR": "NV", "PLSSID": "NV210190N0190E0", "SECDIVID": "NV_UNLOCATED", "SECDIVNOTE": "Requires Survey"}); err != nil {
		t.Fatal(err)
	}
	if _, err = w.Finish(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLocalPackNeverUsesNetwork(t *testing.T) {
	path := testNevadaPack(t)
	transport := &noNetworkTransport{}
	original := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, args := range [][]string{
		{"latlon", "--value", "39.5296,-119.8138"},
		{"plss", "--state", "NV", "--meridian", "21", "--township", "19N", "--range", "19E", "--section", "12", "--aliquot", "NE"},
		{"plss", "--id", "NV210190N0190E0SN120ANE"},
	} {
		args = append(args, "--db", path, "--json", "--geometry")
		out, _, err := execute(t, args...)
		if err != nil {
			t.Fatal(err)
		}
		if out.OfflinePack == nil || out.OfflinePack.State != "NV" {
			t.Fatalf("missing pack provenance: %+v", out)
		}
		if len(out.Results) != 1 || len(out.Results[0].PLSS) != 1 || out.Results[0].PLSS[0].Layer != "intersected" {
			t.Fatalf("wrong local results: %+v", out)
		}
	}
	if _, _, err := execute(t, "latlon", "--value", "40,-105", "--db", path); err == nil {
		t.Fatal("outside pack unexpectedly matched")
	}
	if transport.calls != 0 {
		t.Fatalf("offline commands made %d network requests", transport.calls)
	}
}

func TestPackInfoAndFlags(t *testing.T) {
	path := testNevadaPack(t)
	var stdout, stderr bytes.Buffer
	if err := run([]string{"data", "info", "--db", path, "--json"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"NV"`) || !strings.Contains(stdout.String(), `"bytes"`) {
		t.Fatalf("bad info: %s", stdout.String())
	}
	stdout.Reset()
	if err := run([]string{"data", "unlocated", "--db", path, "--json"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "NV_UNLOCATED") || !strings.Contains(stdout.String(), "Requires Survey") {
		t.Fatalf("missing source audit: %s", stdout.String())
	}
	for _, args := range [][]string{
		{"data", "bogus"}, {"data", "info"}, {"data", "download", "--workers", "0"},
		{"data", "download", "--timeout", "0s"}, {"data", "download", "--state", "TX"},
		{"latlon", "--value", "39,-119", "--db", path, "--service-url", "https://example.invalid"},
		{"latlon", "--value", "39,-119", "--db", filepath.Join(t.TempDir(), "missing.sqlite")},
	} {
		if _, _, err := execute(t, args...); err == nil {
			t.Errorf("invalid flags accepted: %v", args)
		}
	}
	for _, args := range [][]string{{"data"}, {"data", "--help"}, {"data", "download", "--help"}, {"data", "info", "--help"}, {"data", "unlocated", "--help"}} {
		if _, _, err := execute(t, args...); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWesternStateDownloadCommand(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		if r.Form.Get("returnIdsOnly") == "true" {
			if r.Form.Get("where") != "PLSSID LIKE 'CA%'" {
				t.Errorf("wrong state filter: %v", r.Form)
			}
			ids := []int{}
			if strings.HasPrefix(r.URL.Path, "/1/") {
				ids = []int{1}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"objectIdFieldName": "OBJECTID", "objectIds": ids})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"features": []any{map[string]any{
			"attributes": map[string]any{"OBJECTID": 1, "STATEABBR": "CA", "PLSSID": "CA210010N0010E0"},
			"geometry":   plss.Geometry{Rings: [][][]float64{{{-120, 39}, {-119.99, 39}, {-119.99, 39.01}, {-120, 39.01}, {-120, 39}}}},
		}}})
	}))
	defer s.Close()
	path := filepath.Join(t.TempDir(), "california.sqlite")
	var stdout, stderr bytes.Buffer
	err := run([]string{"data", "download", "--state", " ca ", "--out", path, "--service-url", s.URL, "--json"}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Path     string            `json:"path"`
		Metadata plss.PackMetadata `json:"metadata"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Path != path || result.Metadata.State != "CA" || result.Metadata.Counts["township"] != 1 || result.Metadata.Counts["section"] != 0 || result.Metadata.Counts["intersected"] != 0 {
		t.Fatalf("download result: %+v", result)
	}
	if !strings.Contains(stderr.String(), "Layer 3: 0 / 0 features") {
		t.Fatalf("empty layer omitted from progress: %s", stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if err := run([]string{"data", "download", "--help"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, state := range plss.SupportedDownloadStates() {
		if !strings.Contains(stderr.String(), state) {
			t.Errorf("download help omitted %s: %s", state, stderr.String())
		}
	}
}
