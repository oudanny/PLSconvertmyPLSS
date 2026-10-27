package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func execute(t *testing.T, args ...string) (output, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := run(args, &stdout, &stderr)
	var out output
	if err == nil && strings.HasPrefix(stdout.String(), "{") {
		if e := json.Unmarshal(stdout.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
	}
	return out, stdout.String(), err
}

func TestOfflineConversions(t *testing.T) {
	for _, args := range [][]string{
		{"latlon", "--value", "40.015,-105.2705", "--json"},
		{"latlon", "--lat", "40°00′54″N", "--lon", "105°16′13.8″W", "--json"},
		{"latlon", "--value", "+40.015-105.2705/", "--json"},
		{"utm", "--value", "13N 476915.239 4429457.113", "--json"},
		{"utm", "--zone", "13", "--band", "T", "--easting", "476915.239", "--northing", "4429457.113", "--json"},
	} {
		t.Run(strings.Join(args[:2], "_"), func(t *testing.T) {
			out, _, err := execute(t, args...)
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Results) != 1 {
				t.Fatalf("results=%d", len(out.Results))
			}
			c := out.Results[0].Coordinates
			if math.Abs(c.Latitude-40.015) > 1e-7 || math.Abs(c.Longitude+105.2705) > 1e-7 {
				t.Fatalf("wrong point: %+v", c)
			}
			if c.UTM == nil || c.UTM.Zone != 13 || c.DD == "" || c.DDM == "" || c.DMS == "" || c.ISO6709 == "" {
				t.Fatalf("missing representations: %+v", c)
			}
		})
	}
}

func TestPolarCoordinatesStillWork(t *testing.T) {
	out, _, err := execute(t, "latlon", "--lat", "89", "--lon", "0", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if out.Results[0].Coordinates.UTM != nil || len(out.Warnings) != 1 {
		t.Fatalf("expected polar UTM warning: %+v", out)
	}
}

func TestInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		{"bogus"},
		{"latlon"},
		{"latlon", "--lat", "91", "--lon", "0"},
		{"latlon", "--lat", "40", "--lon", "NaN"},
		{"latlon", "--value", "40,-105", "--lat", "40"},
		{"latlon", "--value", "40,-105", "--timeout", "0s"},
		{"latlon", "--value", "40,-105", "--geometry"},
		{"latlon", "--value", "40,-105", "extra"},
		{"utm", "--zone", "13", "--easting", "500000", "--northing", "0"},
		{"utm", "--value", "13N 500000 0", "--zone", "13"},
		{"plss", "--id", "test", "--state", "CO"},
		{"plss", "--id", "test", "--township-fraction", "1"},
		{"plss", "--layer", "section"},
		{"plss", "--township", "1X", "--range", "70W"},
	} {
		if _, _, err := execute(t, args...); err == nil {
			t.Errorf("accepted invalid arguments: %v", args)
		}
	}
}

func TestPLSSLookupAndReverse(t *testing.T) {
	const id = "CO060010N0700W0SN300ASWSE"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/query" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("outSR") != "4326" {
			t.Error("must request WGS84")
		}
		if !strings.Contains(q.Get("where"), id) && q.Get("geometry") == "" {
			t.Errorf("missing ID or geometry: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"spatialReference":{"wkid":4326},"features":[{"attributes":{"OBJECTID":1,"SECDIVID":"CO060010N0700W0SN300ASWSE","STATEABBR":"CO","FRSTDIVNO":"30","SECDIVNO":"SWSE"},"geometry":{"rings":[[[-105.28,40.01],[-105.28,40.02],[-105.26,40.02],[-105.26,40.01],[-105.28,40.01]]]}}]}`)
	}))
	defer server.Close()
	for _, command := range [][]string{
		{"latlon", "--value", "40.015,-105.2705", "--plss"},
		{"utm", "--value", "13N 476915.239 4429457.113", "--plss"},
		{"plss", "--id", id, "--layer", "intersected"},
	} {
		args := append(append([]string{}, command...), "--service-url", server.URL, "--json", "--geometry")
		out, _, err := execute(t, args...)
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Results) != 1 || len(out.Results[0].PLSS) != 1 {
			t.Fatalf("unexpected matches %+v", out)
		}
		m := out.Results[0].PLSS[0]
		if m.ID() != id || len(m.Geometry.Rings) == 0 || !m.Approximate {
			t.Fatalf("unexpected match %+v", m)
		}
		if command[0] == "plss" && len(out.Warnings) == 0 {
			t.Fatal("reverse lookup must explain representative point")
		}
		args = args[:len(args)-1] // Without --geometry the large polygon is excluded.
		out, _, err = execute(t, args...)
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Results[0].PLSS[0].Geometry.Rings) != 0 {
			t.Fatal("unexpected polygon without --geometry")
		}
	}
}

func TestHelp(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"latlon", "--help"}, {"utm", "--help"}, {"plss", "--help"}} {
		if _, _, err := execute(t, args...); err != nil {
			t.Fatal(err)
		}
	}
}
