package plss

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func testServer(t *testing.T, f http.HandlerFunc) *Client {
	t.Helper()
	s := httptest.NewServer(f)
	t.Cleanup(s.Close)
	return &Client{BaseURL: s.URL, HTTPClient: s.Client()}
}
func polygonResponse(w http.ResponseWriter, attrs map[string]any, more bool) {
	_ = json.NewEncoder(w).Encode(map[string]any{"features": []any{map[string]any{"attributes": attrs, "geometry": Geometry{Rings: [][][]float64{{{-105, 40}, {-104, 40}, {-104, 41}, {-105, 41}, {-105, 40}}}}}}, "exceededTransferLimit": more})
}

func TestLookupBoulderFixture(t *testing.T) {
	fixture, err := os.ReadFile("testdata/blm_boulder.json")
	if err != nil {
		t.Fatal(err)
	}
	c := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/3/query" || q.Get("inSR") != "4326" || q.Get("outSR") != "4326" || q.Get("spatialRel") != "esriSpatialRelIntersects" || q.Get("geometry") != "-105.270500000000,40.015000000000" {
			t.Errorf("unexpected query: %s", r.URL)
		}
		_, _ = w.Write(fixture)
	})
	m, err := c.Lookup(context.Background(), 40.015, -105.2705)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 1 || m[0].ID() != "CO060010N0700W0SN300ASWSE" || m[0].Layer != "intersected" || !m[0].Approximate {
		t.Fatalf("unexpected matches: %+v", m)
	}
	if !inside(m[0].Geometry, m[0].Representative) {
		t.Fatal("representative lies outside polygon")
	}
}

func TestLookupFallbackAndNoCoverage(t *testing.T) {
	for _, found := range []bool{true, false} {
		calls := 0
		c := testServer(t, func(w http.ResponseWriter, r *http.Request) {
			calls++
			if found && r.URL.Path == "/2/query" {
				polygonResponse(w, map[string]any{"FRSTDIVID": "example"}, false)
			} else {
				_, _ = w.Write([]byte(`{"features":[]}`))
			}
		})
		m, e := c.Lookup(context.Background(), 40, -105)
		if found {
			if e != nil || len(m) != 1 || m[0].Layer != "section" || calls != 2 {
				t.Fatalf("fallback: %v %v calls=%d", m, e, calls)
			}
		} else if !errors.Is(e, ErrNotFound) || calls != 3 {
			t.Fatalf("no coverage: %v calls=%d", e, calls)
		}
	}
}

func TestFindStructuredAndAliquot(t *testing.T) {
	for _, aliquot := range []string{"", "SW1/4 SE1/4"} {
		c := testServer(t, func(w http.ResponseWriter, r *http.Request) {
			where := r.URL.Query().Get("where")
			if r.URL.Path == "/1/query" {
				for _, part := range []string{"STATEABBR='CO'", "PRINMERCD='06'", "TWNSHPNO='001'", "RANGENO='070'", "TWNSHPFRAC='0'"} {
					if !strings.Contains(where, part) {
						t.Errorf("missing %s in %s", part, where)
					}
				}
				polygonResponse(w, map[string]any{"PLSSID": "CO060010N0700W0", "STATEABBR": "CO", "PRINMER": "6th Meridian", "TWNSHPLAB": "1N 70W"}, false)
			} else {
				path := "/2/query"
				if aliquot != "" {
					path = "/3/query"
					if !strings.Contains(where, "SECDIVNO LIKE '%SWSE'") {
						t.Errorf("wrong aliquot: %s", where)
					}
				}
				if r.URL.Path != path || !strings.Contains(where, "FRSTDIVNO='30'") {
					t.Errorf("unexpected section query %s", r.URL)
				}
				polygonResponse(w, map[string]any{"PLSSID": "CO060010N0700W0", "FRSTDIVID": "CO060010N0700W0SN300", "FRSTDIVTXT": "Section", "FRSTDIVLAB": "30"}, false)
			}
		})
		m, err := c.Find(context.Background(), Description{State: "co", Meridian: "6", Township: 1, TownshipDirection: "N", Range: 70, RangeDirection: "W", Section: 30, Aliquot: aliquot})
		if err != nil {
			t.Fatal(err)
		}
		if len(m) != 1 || !strings.Contains(m[0].Label, "6th Meridian") {
			t.Fatalf("missing township enrichment: %+v", m)
		}
	}
}

func TestFindIDPaginationEscapes(t *testing.T) {
	calls := 0
	c := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		q := r.URL.Query()
		if q.Get("where") != "SECDIVID='test''id'" {
			t.Errorf("unescaped SQL: %s", q.Get("where"))
		}
		if calls == 2 && q.Get("resultOffset") != "1" {
			t.Error("wrong pagination offset")
		}
		polygonResponse(w, map[string]any{"SECDIVID": "test'id"}, calls == 1)
	})
	m, err := c.FindID(context.Background(), "division", "test'id")
	if err != nil || len(m) != 2 || calls != 2 {
		t.Fatalf("pagination: %v %v %d", m, err, calls)
	}
}

func TestServiceFailures(t *testing.T) {
	for _, tc := range []struct {
		body   string
		status int
		want   string
	}{
		{`{"error":{"code":400,"message":"bad query","details":["field missing"]}}`, 200, "field missing"},
		{`not json`, 200, "decode BLM"},
		{`{}`, 200, "no feature collection"},
		{`{"features":[{"attributes":{},"geometry":{"rings":[]}}]}`, 200, "polygon"},
		{`{"features":[],"exceededTransferLimit":true}`, 200, "empty page"},
		{`unavailable`, 503, "HTTP 503"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			c := testServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			_, e := c.Lookup(context.Background(), 40, -105)
			if e == nil || !strings.Contains(e.Error(), tc.want) {
				t.Fatalf("got %v, want %s", e, tc.want)
			}
		})
	}
}

func TestValidationAndCancellation(t *testing.T) {
	c := testServer(t, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid request reached network") })
	for _, p := range []Point{{91, 0}, {0, 181}, {math.NaN(), 0}, {0, math.Inf(1)}} {
		if _, e := c.Lookup(context.Background(), p.Latitude, p.Longitude); e == nil {
			t.Error("accepted invalid point")
		}
	}
	for _, d := range []Description{{}, {State: "CO'", Meridian: "6"}, {State: "CO", Meridian: "6", TownshipDirection: "N", RangeDirection: "W", Section: 40}, {State: "CO", Meridian: "6", TownshipDirection: "N", RangeDirection: "W", Aliquot: "NE"}, {State: "CO", Meridian: "6", TownshipDirection: "N", RangeDirection: "W", Section: 1, Aliquot: "NE", Lot: "1"}} {
		if _, e := c.Find(context.Background(), d); e == nil {
			t.Error("accepted invalid description")
		}
	}
	if _, e := c.FindID(context.Background(), "bad", "id"); e == nil {
		t.Error("accepted unknown ID kind")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := c.Lookup(ctx, 40, -105); !errors.Is(e, context.Canceled) {
		t.Fatalf("cancel: %v", e)
	}
}
