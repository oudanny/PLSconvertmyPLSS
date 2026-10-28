package plss

import (
	"strings"
	"testing"
)

func TestDescribeIdentifiers(t *testing.T) {
	tests := []struct {
		name, township, section, division, survey, text, detail string
	}{
		{"Reno", "NV210190N0190E0", "NV210190N0190E0SN110", "NV210190N0190E0SN110ASWNE", "", "SW¼ of NE¼ · Sec. 11 · T. 19 N · R. 19 E", "Mount Diablo Meridian · Nevada"},
		{"Boulder without parents", "", "", "CO060010N0700W0SN300ASWSE", "", "SW¼ of SE¼ · Sec. 30 · T. 1 N · R. 70 W", "6th Meridian · Colorado"},
		{"half within quarters", "", "", "UT260020S0020W0SN310AS2SENE", "", "S½ of SE¼ of NE¼ · Sec. 31 · T. 2 S · R. 2 W", "Salt Lake Meridian · Utah"},
		{"government lot", "", "", "NV210010N0310E0SN100L005", "", "Gov. lot 5 · Sec. 10 · T. 1 N · R. 31 E", "Mount Diablo Meridian · Nevada"},
		{"lot suffix preserved", "", "", "NV210010N0310E0SN100L005A", "", "Gov. lot 005A · Sec. 10 · T. 1 N · R. 31 E", "Mount Diablo Meridian · Nevada"},
		{"remainder", "", "", "NV210010N0310E0SN100BNENW", "", "Remainder of NE¼ of NW¼ · Sec. 10 · T. 1 N · R. 31 E", "Mount Diablo Meridian · Nevada"},
		{"fraction codes", "NV210011N0193E0", "", "", "", "T. 1 ¼ N · R. 19 ¾ E", "Mount Diablo Meridian · Nevada"},
		{"half township", "NV210102N0410E0", "", "", "", "T. 10 ½ N · R. 41 E", "Mount Diablo Meridian · Nevada"},
		{"unknown fraction and duplicate", "NV210010N031AEA", "", "", "", "T. 1 N · R. 31 (fraction code A) E (township duplicate A)", "Mount Diablo Meridian · Nevada"},
		{"Ohio directions", "OH380010E0010N0", "", "", "", "T. 1 E · R. 1 N", "Ohio River Base · Ohio"},
		{"section larger than 36", "", "OH380010E0010N0SN120A", "", "", "Sec. 120 (duplicate A) · T. 1 E · R. 1 N", "Ohio River Base · Ohio"},
		{"unsurveyed protracted parent", "", "AK280830S2610W0UP290", "", "", "Unsurveyed protracted division 29 · T. 83 S · R. 261 W", "Seward Meridian · Alaska"},
		{"unknown subdivision code", "", "", "NV210050N0280E0SN190V6", "", "Subdivision V 6 · Sec. 19 · T. 5 N · R. 28 E", "Mount Diablo Meridian · Nevada"},
		{"unknown aliquot suffix", "", "", "NV210190N0190E0SN110ASWNER", "", "Subdivision A SWNER · Sec. 11 · T. 19 N · R. 19 E", "Mount Diablo Meridian · Nevada"},
		{"no IDs", "", "", "", "", "Published polygon (no cadastral identifier)", "The source record has no cadastral identifier."},
		{"unknown standalone ID", "", "", "source-123", "", "Subdivision source-123", "Identifier format is not decoded."},
		{"malformed township", "NV210190N0190E0EXTRA", "", "", "", "Township NV210190N0190E0EXTRA", "Identifier format is not decoded."},
		{"unknown meridian", "ZZ990010N0010E0", "", "", "", "T. 1 N · R. 1 E", "Principal meridian code 99 · State code ZZ"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DescribeIdentifiers(tc.township, tc.section, tc.division, tc.survey)
			if got.Text != tc.text || got.Detail != tc.detail {
				t.Fatalf("got %#v; want text=%q detail=%q", got, tc.text, tc.detail)
			}
		})
	}
}

func TestIdentifierFallbackDoesNotInventOrMixParent(t *testing.T) {
	got := DescribeIdentifiers("NV210190N0190E0", "unknown-section", "unknown-subdivision", "")
	if got.Text != "Subdivision unknown-subdivision" || !strings.Contains(got.Detail, "First division unknown-section") || !strings.Contains(got.Detail, "T. 19 N") {
		t.Fatalf("fallback lost source identity or parent: %#v", got)
	}
	got = DescribeIdentifiers("NV210190N0190E0", "NV210190N0190E0SN110", "", "independent-special-survey")
	if got.Text != "Survey independent-special-survey" || !strings.Contains(got.Detail, "Sec. 11") {
		t.Fatalf("special survey incorrectly presented as parent: %#v", got)
	}
	// A mismatched parent must not override the finest ID's own encoded origin.
	got = DescribeIdentifiers("NV210190N0190E0", "NV210190N0190E0SN110", "CO060010N0700W0SN300ASWSE", "")
	if !strings.Contains(got.Text, "Sec. 30") || got.Detail != "6th Meridian · Colorado" {
		t.Fatalf("conflicting parent mixed into subdivision: %#v", got)
	}
}
