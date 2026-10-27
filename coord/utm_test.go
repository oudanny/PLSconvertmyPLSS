package coord

import (
	"math"
	"testing"
)

// Reference values from the primary PROJ UTM examples:
// https://proj.org/en/stable/operations/projections/utm.html
// Those examples use GRS80; the WGS84 difference is below their 1 cm rounding.
func TestUTMReferenceInverse(t *testing.T) {
	for _, tt := range []struct {
		u    UTM
		want Point
	}{
		{UTM{Zone: 32, Hemisphere: "N", Easting: 687071.44, Northing: 6210141.33}, Point{56, 12}},
		{UTM{Zone: 59, Hemisphere: "S", Easting: 740526.32, Northing: 5123750.87}, Point{-44, 174}},
		{UTM{Zone: 31, Hemisphere: "N", Easting: 500000, Northing: 0}, Point{0, 3}},
	} {
		got, err := FromUTM(tt.u)
		if err != nil || !closePoint(got, tt.want, 1e-7) {
			t.Errorf("FromUTM(%+v) = %+v, %v; want %+v", tt.u, got, err, tt.want)
		}
	}
}

func TestUTMReferenceForward(t *testing.T) {
	// The central meridian is exactly 500000 m east; its meridional distance
	// provides an independent WGS84 reference for the forward implementation.
	u, err := ToUTM(Point{45, 3})
	if err != nil || u.Zone != 31 || math.Abs(u.Easting-500000) > 1e-8 || math.Abs(u.Northing-4982950.400226552) > 0.001 {
		t.Fatalf("unexpected forward projection: %+v, %v", u, err)
	}
}

func TestUTMRoundTripsAndZones(t *testing.T) {
	for _, tt := range []struct {
		p    Point
		zone int
	}{
		{Point{37.7749, -122.4194}, 10}, {Point{-33.8688, 151.2093}, 56},
		{Point{60, 3}, 32}, {Point{60, 11.9}, 32}, {Point{75, 8.9}, 31},
		{Point{75, 9}, 33}, {Point{75, 20.9}, 33}, {Point{75, 21}, 35},
		{Point{75, 32.9}, 35}, {Point{75, 33}, 37}, {Point{75, 42}, 38},
		{Point{0, -180}, 1}, {Point{0, 180}, 60}, {Point{-80, 0}, 31}, {Point{84, 0}, 31},
	} {
		u, err := ToUTM(tt.p)
		if err != nil {
			t.Fatal(err)
		}
		if u.Zone != tt.zone {
			t.Errorf("ToUTM(%+v) zone=%d want%d", tt.p, u.Zone, tt.zone)
		}
		got, err := FromUTM(u)
		if err != nil || !closePoint(got, tt.p, 1e-10) {
			t.Errorf("roundtrip %+v -> %+v -> %+v, %v", tt.p, u, got, err)
		}
	}
	for lat := -80.; lat <= 84; lat += 4 {
		for lon := -180.; lon <= 180; lon += 3 {
			p := Point{lat, lon}
			u, err := ToUTM(p)
			if err != nil {
				t.Fatal(err)
			}
			got, err := FromUTM(u)
			if err != nil || !closePoint(got, p, 1e-10) {
				t.Fatalf("grid roundtrip %+v -> %+v -> %+v, %v", p, u, got, err)
			}
		}
	}
}

func TestParseUTM(t *testing.T) {
	for _, input := range []string{"10N 551130 4180998", "10 N 551130 4180998", "UTM 10N 551130 4180998", "10N, 551130, 4180998", "10 band S 551130 4180998", "10S-band 551130 4180998"} {
		u, err := ParseUTM(input)
		if err != nil || u.Zone != 10 || u.Hemisphere != "N" || u.Easting != 551130 {
			t.Errorf("ParseUTM(%q) = %+v, %v", input, u, err)
		}
	}
	u, err := ParseUTM("56S 334368.634 6250948.345")
	if err != nil || u.Hemisphere != "S" || u.Band != "" {
		t.Errorf("S must mean south hemisphere: %+v, %v", u, err)
	}
	u, err = ParseUTM("10T 500000 4982950.4")
	if err != nil || u.Hemisphere != "N" || u.Band != "T" {
		t.Errorf("unambiguous band T: %+v, %v", u, err)
	}
	for _, bad := range []string{"", "0N 500000 0", "61N 500000 0", "31N 0 0", "31N 500000 10000001", "31I 500000 0", "31N NaN 0", "31N 500000 -1", "31 band C 500000 4982950", "10 band T 551130 4180998"} {
		if _, err := ParseUTM(bad); err == nil {
			t.Errorf("ParseUTM(%q) should fail", bad)
		}
	}
}

func TestUTMValidation(t *testing.T) {
	for _, p := range []Point{{84.1, 0}, {-80.1, 0}, {0, 181}, {math.Inf(1), 0}, {0, math.NaN()}} {
		if _, err := ToUTM(p); err == nil {
			t.Errorf("ToUTM(%+v) should fail", p)
		}
	}
	for _, u := range []UTM{
		{Zone: 31, Hemisphere: "N", Easting: math.NaN(), Northing: 0},
		{Zone: 31, Hemisphere: "N", Easting: 500000, Northing: math.Inf(1)},
		{Zone: 31, Hemisphere: "N", Easting: 500000, Northing: 9999999},
		{Zone: 31, Hemisphere: "S", Easting: 500000, Northing: 1},
		{Zone: 31, Hemisphere: "N", Easting: 500000, Northing: 0, Band: "C"},
	} {
		if _, err := FromUTM(u); err == nil {
			t.Errorf("FromUTM(%+v) should fail", u)
		}
	}
}

func FuzzParseUTM(f *testing.F) {
	for _, s := range []string{"31N 500000 0", "10 band S 551130 4180998", "56S 334368.634 6250948.345", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		u, err := ParseUTM(s)
		if err == nil {
			if _, err := FromUTM(u); err != nil {
				t.Fatalf("parsed invalid UTM: %+v, %v", u, err)
			}
		}
	})
}
