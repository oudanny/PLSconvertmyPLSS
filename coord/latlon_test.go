package coord

import (
	"math"
	"strings"
	"testing"
)

func closePoint(a, b Point, tolerance float64) bool {
	return math.Abs(a.Latitude-b.Latitude) <= tolerance && math.Abs(a.Longitude-b.Longitude) <= tolerance
}

func TestParseNotations(t *testing.T) {
	want := Point{37.7749, -122.4194}
	for _, input := range []string{
		"37.7749, -122.4194", "37.7749 -122.4194", "37.7749N 122.4194W", "N37.7749 W122.4194",
		"37°46.494′N, 122°25.164′W", "37 46.494 N; 122 25.164 W",
		"37°46′29.64″N, 122°25′9.84″W", `37°46'29.64"N, 122°25'9.84"W`,
		"N37:46:29.64 W122:25:9.84", "37d46m29.64sN, 122d25m9.84sW",
		"37 degrees 46 minutes 29.64 seconds north, 122 degrees 25 minutes 9.84 seconds west",
		"3746.494N, 12225.164W", "374629.64N, 1222509.84W",
		"+37.7749-122.4194/", "+3746.494-12225.164/", "+374629.64-1222509.84/",
	} {
		t.Run(input, func(t *testing.T) {
			got, err := ParsePair(input)
			if err != nil || !closePoint(got, want, 1e-10) {
				t.Fatalf("ParsePair(%q) = %+v, %v; want %+v", input, got, err, want)
			}
		})
	}
}

func TestAngleValidation(t *testing.T) {
	for _, bad := range []string{"", "NaN", "Inf", "91", "90 0 1", "37 60", "37 1 60", "37 -1", "37.5 1", "-37N", "+37S", "37E", "37N N", "N37N", "north nonsense", "1e2", "3760N", "376000N"} {
		if _, err := ParseLatitude(bad); err == nil {
			t.Errorf("ParseLatitude(%q) should fail", bad)
		}
	}
	for _, bad := range []string{"181", "180 0 1", "122N", "-122E", "+122W", "1226000W"} {
		if _, err := ParseLongitude(bad); err == nil {
			t.Errorf("ParseLongitude(%q) should fail", bad)
		}
	}
	for _, bad := range []string{"37,122,3", ",37,122", "37,,122", "37 46 30 20", "+3799-12200/", "+91.0-122.0/", "+37.7-122.4+001.0/"} {
		if _, err := ParsePair(bad); err == nil {
			t.Errorf("ParsePair(%q) should fail", bad)
		}
	}
	for _, p := range []Point{{math.NaN(), 0}, {0, math.Inf(1)}, {91, 0}, {0, -181}} {
		if p.Validate() == nil {
			t.Errorf("Validate(%+v) should fail", p)
		}
	}
}

func TestFormattingRoundTripAndCarry(t *testing.T) {
	for _, p := range []Point{{37.7749, -122.4194}, {-33.8688, 151.2093}, {89.999999999999, 179.999999999999}, {0, 0}, {math.Copysign(0, -1), -0.00000001}, {-90, 180}} {
		for _, format := range []func(Point) string{FormatDD, FormatDDM, FormatDMS, FormatISO6709} {
			s := format(p)
			got, err := ParsePair(s)
			if err != nil || !closePoint(got, p, 2e-8) {
				t.Errorf("roundtrip %q = %+v, %v; want %+v", s, got, err, p)
			}
			if strings.Contains(s, "60.000") {
				t.Errorf("format failed to carry: %q", s)
			}
		}
	}
}

func FuzzParsePair(f *testing.F) {
	for _, s := range []string{"37.7749, -122.4194", "N37:46:29.64 W122:25:9.84", "+374629.64-1222509.84/", "NaN", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		p, err := ParsePair(s)
		if err == nil && p.Validate() != nil {
			t.Fatalf("parser returned invalid point %+v for %q", p, s)
		}
	})
}
