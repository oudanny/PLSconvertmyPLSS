package plss

import (
	"regexp"
	"strings"
)

// IdentifierDescription is a display interpretation of the published identifiers.
// Text contains the finest available designation, and Detail supplies its origin
// and any qualifications. The identifiers themselves remain the lookup keys.
type IdentifierDescription struct {
	Text   string
	Detail string
}

// DescribeIdentifiers formats the four CadNSDI identifiers without reconstructing
// geometry or inventing missing survey information. Unknown codes are retained
// explicitly. Aliquots keep the source's smallest-to-largest ordering.
//
// Layout/domain references:
// https://www.fgdc.gov/standards/projects/cadastral/cadnsdi-publicationhandbookoct2016.pdf/at_download/file
// https://gis.utah.gov/products/sgid/cadastre/plss-quarter-quarter-sections/
// https://pubs.usgs.gov/circ/1984/0895g/report.pdf (principal-meridian codes)
func DescribeIdentifiers(township, section, division, survey string) IdentifierDescription {
	kind, id := "Township", township
	switch {
	case division != "":
		kind, id = "Subdivision", division
	case survey != "":
		kind, id = "Survey", survey
	case section != "":
		kind, id = "First division", section
	}
	if id == "" {
		return IdentifierDescription{"Published polygon (no cadastral identifier)", "The source record has no cadastral identifier."}
	}

	t, rest, ok := parseIdentifierTownship(id)
	if !ok {
		// An independent special-survey ID may still have a useful parent, but it
		// must not be presented as if the parent fully identifies that survey.
		parent := ""
		if kind != "Township" {
			parentSection := ""
			if kind == "Subdivision" || kind == "Survey" {
				parentSection = section
			}
			p := DescribeIdentifiers(township, parentSection, "", "")
			if township != "" || parentSection != "" {
				parent = p.Text + " · " + p.Detail
			}
		}
		return IdentifierDescription{kind + " " + id, strings.Trim(parent+" · Identifier format is not decoded.", " ·")}
	}
	if kind == "Township" {
		if rest != "" {
			return IdentifierDescription{"Township " + id, "Identifier format is not decoded."}
		}
		return IdentifierDescription{t.text, t.detail}
	}
	if kind == "Survey" {
		return IdentifierDescription{"Survey " + id, t.text + " · " + t.detail}
	}

	var first, second string
	if kind == "First division" {
		first = rest
	} else if strings.HasPrefix(id, section) && section != "" && len(section) >= len(t.id) && strings.HasPrefix(section, t.id) {
		first, second = section[len(t.id):], id[len(section):]
	} else {
		var decoded bool
		first, second, decoded = splitInferredDivision(rest)
		if !decoded {
			return IdentifierDescription{"Subdivision " + id, t.text + " · " + t.detail + " · Subdivision identifier format is not decoded."}
		}
	}
	firstText := describeFirstDivision(first)
	parts := []string{}
	if kind == "Subdivision" {
		if second == "" {
			parts = append(parts, "Subdivision identifier equals parent identifier")
		} else {
			parts = append(parts, describeSecondDivision(second))
		}
	}
	parts = append(parts, firstText, t.text)
	detail := t.detail
	if survey != "" && survey != division {
		detail += " · Survey ID " + survey
	}
	return IdentifierDescription{strings.Join(parts, " · "), detail}
}

// A standard township ID consists of state, meridian, two five-character
// number/fraction/direction groups, and its duplicate code. Both direction
// groups allow N/S/E/W: some Ohio survey systems reverse the usual axes.
var townshipIdentifier = regexp.MustCompile(`^([A-Z]{2})([0-9]{2})([0-9]{3})([0-9A-Z])([NSEW])([0-9]{3})([0-9A-Z])([NSEW])([0-9A-Z])`)
var firstDivision = regexp.MustCompile(`^([A-Z]{2})([0-9]+)([0-9A-Z])$`)
var digitsOnly = regexp.MustCompile(`^[0-9]+$`)

func splitInferredDivision(value string) (string, string, bool) {
	// Without FRSTDIVID, a three-digit section followed by a duplicate letter
	// can be ambiguous with a two-digit section and subdivision suffix. Decode
	// only a unique supported interpretation, preferring valid aliquot syntax.
	type candidate struct{ first, second string }
	var candidates, recognized []candidate
	for n := 1; n <= 3; n++ {
		end := 2 + n + 1
		if end+1 >= len(value) || !firstDivision.MatchString(value[:end]) {
			continue
		}
		suffix := value[end:]
		if !strings.Contains("ABLUVZ", suffix[:1]) {
			continue
		}
		c := candidate{value[:end], suffix}
		candidates = append(candidates, c)
		if suffix[0] == 'A' || suffix[0] == 'B' {
			if _, ok := describeAliquot(suffix[1:]); ok {
				recognized = append(recognized, c)
			}
		} else {
			recognized = append(recognized, c)
		}
	}
	if len(recognized) == 1 {
		return recognized[0].first, recognized[0].second, true
	}
	if len(recognized) == 0 && len(candidates) == 1 {
		return candidates[0].first, candidates[0].second, true
	}
	return "", "", false
}

type identifierTownship struct{ id, text, detail string }

func parseIdentifierTownship(id string) (identifierTownship, string, bool) {
	m := townshipIdentifier.FindStringSubmatch(id)
	if m == nil {
		return identifierTownship{}, "", false
	}
	t := identifierTownship{id: m[0]}
	t.text = "T. " + identifierNumber(m[3], m[4]) + " " + m[5] + " · R. " + identifierNumber(m[6], m[7]) + " " + m[8]
	if m[9] != "0" {
		t.text += " (township duplicate " + m[9] + ")"
	}
	meridian := meridianNames[m[2]]
	if meridian == "" {
		meridian = "Principal meridian code " + m[2]
	}
	state := identifierStateNames[m[1]]
	if state == "" {
		state = "State code " + m[1]
	}
	t.detail = meridian + " · " + state
	return t, id[len(m[0]):], true
}

func identifierNumber(number, fraction string) string {
	number = trimNumericZeroes(number)
	switch fraction {
	case "0":
		return number
	case "1":
		return number + " ¼"
	case "2":
		return number + " ½"
	case "3":
		return number + " ¾"
	default:
		return number + " (fraction code " + fraction + ")"
	}
}

func trimNumericZeroes(value string) string {
	if !digitsOnly.MatchString(value) {
		return value
	}
	// Do not use an integer conversion: exceptional survey numbers may be long.
	value = strings.TrimLeft(value, "0")
	if value == "" {
		return "0"
	}
	return value
}

func describeFirstDivision(value string) string {
	m := firstDivision.FindStringSubmatch(value)
	if m == nil {
		return "First division " + value
	}
	names := map[string]string{"SN": "Sec.", "PB": "Protraction block", "UP": "Unsurveyed protracted division"}
	name := names[m[1]]
	if name == "" {
		name = "First division " + m[1]
	}
	text := name + " " + trimNumericZeroes(m[2])
	if m[3] != "0" {
		text += " (duplicate " + m[3] + ")"
	}
	return text
}

func describeSecondDivision(value string) string {
	if len(value) < 2 {
		return "Subdivision " + value
	}
	kind, number := value[:1], value[1:]
	switch kind {
	case "L":
		return "Gov. lot " + trimNumericZeroes(number)
	case "A", "B":
		if text, ok := describeAliquot(number); ok {
			if kind == "B" {
				return "Remainder of " + text
			}
			return text
		}
	}
	return "Subdivision " + kind + " " + number
}

func describeAliquot(value string) (string, bool) {
	if value == "ALL" {
		return "All", true
	}
	parts := []string{}
	for len(value) > 0 {
		if len(value) < 2 {
			return "", false
		}
		part := value[:2]
		switch part {
		case "NE", "NW", "SE", "SW":
			parts = append(parts, part+"¼")
		case "N2", "S2", "E2", "W2":
			parts = append(parts, part[:1]+"½")
		default:
			return "", false
		}
		value = value[2:]
	}
	return strings.Join(parts, " of "), len(parts) > 0
}

// These are the named BLM principal-meridian codes, not meridian longitudes.
// Unknown or jurisdiction-specific codes are displayed literally above.
var meridianNames = map[string]string{
	"01": "1st Meridian", "02": "2nd Meridian", "03": "3rd Meridian", "04": "4th Meridian", "05": "5th Meridian", "06": "6th Meridian",
	"07": "Black Hills Meridian", "08": "Boise Meridian", "09": "Chickasaw Meridian", "10": "Choctaw Meridian", "11": "Cimarron Meridian",
	"12": "Copper River Meridian", "13": "Fairbanks Meridian", "14": "Gila-Salt River Meridian", "15": "Humboldt Meridian", "16": "Huntsville Meridian",
	"17": "Indian Meridian", "18": "Louisiana Meridian", "19": "Michigan Meridian", "20": "Montana Meridian", "21": "Mount Diablo Meridian",
	"22": "Navajo Meridian", "23": "New Mexico Meridian", "24": "St. Helena Meridian", "25": "St. Stephens Meridian", "26": "Salt Lake Meridian",
	"27": "San Bernardino Meridian", "28": "Seward Meridian", "29": "Tallahassee Meridian", "30": "Uintah Meridian", "31": "Ute Meridian",
	"32": "Washington Meridian", "33": "Willamette Meridian", "34": "Wind River Meridian", "35": "Ohio River Survey", "36": "Between the Miamis",
	"37": "Muskingum River", "38": "Ohio River Base", "39": "First Scioto River", "40": "Second Scioto River", "41": "Third Scioto River",
	"42": "Ellicott's Line", "43": "Twelve-Mile Square", "44": "Kateel River Meridian", "45": "Umiat Meridian", "46": "Extended 4th Meridian",
	"47": "West of the Great Miami", "48": "U.S. Military Survey",
}

var identifierStateNames = map[string]string{
	"AK": "Alaska", "AL": "Alabama", "AR": "Arkansas", "AZ": "Arizona", "CA": "California", "CO": "Colorado", "CT": "Connecticut", "DC": "District of Columbia",
	"DE": "Delaware", "FL": "Florida", "GA": "Georgia", "HI": "Hawaii", "IA": "Iowa", "ID": "Idaho", "IL": "Illinois", "IN": "Indiana", "KS": "Kansas",
	"KY": "Kentucky", "LA": "Louisiana", "MA": "Massachusetts", "MD": "Maryland", "ME": "Maine", "MI": "Michigan", "MN": "Minnesota", "MO": "Missouri",
	"MS": "Mississippi", "MT": "Montana", "NC": "North Carolina", "ND": "North Dakota", "NE": "Nebraska", "NH": "New Hampshire", "NJ": "New Jersey", "NM": "New Mexico",
	"NV": "Nevada", "NY": "New York", "OH": "Ohio", "OK": "Oklahoma", "OR": "Oregon", "PA": "Pennsylvania", "RI": "Rhode Island", "SC": "South Carolina",
	"SD": "South Dakota", "TN": "Tennessee", "TX": "Texas", "UT": "Utah", "VA": "Virginia", "VT": "Vermont", "WA": "Washington", "WI": "Wisconsin", "WV": "West Virginia", "WY": "Wyoming",
}
