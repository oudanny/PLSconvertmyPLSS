// Package coord parses common geographic coordinate notations and converts
// WGS84 latitude/longitude and Universal Transverse Mercator coordinates.
package coord

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Point is a WGS84 position in decimal degrees, in latitude/longitude order.
type Point struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

func finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }

// Validate checks that coordinates are finite and within geographic bounds.
func (p Point) Validate() error {
	if !finite(p.Latitude) || p.Latitude < -90 || p.Latitude > 90 {
		return fmt.Errorf("latitude must be finite and between -90 and 90 degrees")
	}
	if !finite(p.Longitude) || p.Longitude < -180 || p.Longitude > 180 {
		return fmt.Errorf("longitude must be finite and between -180 and 180 degrees")
	}
	return nil
}

var (
	numberRE         = regexp.MustCompile(`[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)`)
	unitWordsRE      = regexp.MustCompile(`(?i)degrees?|deg|minutes?|min|seconds?|sec`)
	directionWordsRE = regexp.MustCompile(`(?i)north|south|east|west`)
	isoRE            = regexp.MustCompile(`^([+-][0-9]+(?:\.[0-9]+)?)([+-][0-9]+(?:\.[0-9]+)?)/?$`)
)

// ParseLatitude accepts decimal degrees, degrees/decimal minutes, or DMS,
// with optional N/S direction before or after the angle.
func ParseLatitude(s string) (float64, error) { return parseAngle(s, true) }

// ParseLongitude accepts decimal degrees, degrees/decimal minutes, or DMS,
// with optional E/W direction before or after the angle.
func ParseLongitude(s string) (float64, error) { return parseAngle(s, false) }

func normalizeAngle(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "−", "-"))
	s = unitWordsRE.ReplaceAllStringFunc(s, func(v string) string {
		switch strings.ToLower(v)[0] {
		case 'd':
			return "°"
		case 'm':
			return "′"
		default:
			return "″"
		}
	})
	s = directionWordsRE.ReplaceAllStringFunc(s, func(v string) string { return strings.ToUpper(v[:1]) })
	// Lowercase d/m/s are accepted as unit symbols when d or m is present.
	if strings.ContainsAny(s, "dm") {
		s = strings.NewReplacer("d", "°", "m", "′", "s", "″").Replace(s)
	}
	return strings.ToUpper(s)
}

func parseAngle(s string, latitude bool) (float64, error) {
	s = normalizeAngle(s)
	if len(s) == 0 || len(s) > 256 {
		return 0, fmt.Errorf("empty or overly long angle")
	}
	hemi := byte(0)
	if strings.ContainsAny(s[:1], "NSEW") {
		hemi, s = s[0], strings.TrimSpace(s[1:])
	}
	if len(s) > 0 && strings.ContainsAny(s[len(s)-1:], "NSEW") {
		if hemi != 0 {
			return 0, fmt.Errorf("specify a hemisphere only once")
		}
		hemi, s = s[len(s)-1], strings.TrimSpace(s[:len(s)-1])
	}
	if hemi != 0 && (latitude && hemi != 'N' && hemi != 'S' || !latitude && hemi != 'E' && hemi != 'W') {
		return 0, fmt.Errorf("hemisphere %c does not match coordinate axis", hemi)
	}
	parts := numberRE.FindAllString(s, -1)
	if len(parts) < 1 || len(parts) > 3 {
		return 0, fmt.Errorf("expected degrees, degrees/minutes, or degrees/minutes/seconds")
	}
	remaining := numberRE.ReplaceAllString(s, "")
	for _, r := range remaining {
		if !unicode.IsSpace(r) && !strings.ContainsRune("°º′'″\":", r) {
			return 0, fmt.Errorf("unexpected character %q in angle", r)
		}
	}
	values := make([]float64, len(parts))
	for i, part := range parts {
		v, err := strconv.ParseFloat(part, 64)
		if err != nil || !finite(v) {
			return 0, fmt.Errorf("invalid angle number %q", part)
		}
		if i > 0 && (strings.ContainsAny(part, "+-") || v >= 60) {
			return 0, fmt.Errorf("minutes and seconds must be unsigned and less than 60")
		}
		if i < len(parts)-1 && v != math.Trunc(v) {
			return 0, fmt.Errorf("only the final angle component may have a fraction")
		}
		values[i] = v
	}
	// Compact DDMM[SS] or DDDMM[SS] is unambiguous with a hemisphere.
	if len(parts) == 1 && hemi != 0 {
		unsigned := strings.TrimLeft(parts[0], "+-")
		width := len(strings.SplitN(unsigned, ".", 2)[0])
		degreesWidth := 3
		if latitude {
			degreesWidth = 2
		}
		if width == degreesWidth+2 || width == degreesWidth+4 {
			v, err := parseCompact(parts[0], degreesWidth)
			if err != nil {
				return 0, err
			}
			values[0] = v
		}
	}
	value := math.Abs(values[0])
	if len(values) > 1 {
		value += values[1] / 60
	}
	if len(values) > 2 {
		value += values[2] / 3600
	}
	negative := strings.HasPrefix(parts[0], "-")
	if hemi != 0 {
		southWest := hemi == 'S' || hemi == 'W'
		if negative && !southWest || strings.HasPrefix(parts[0], "+") && southWest {
			return 0, fmt.Errorf("sign and hemisphere disagree")
		}
		negative = southWest
	}
	if negative {
		value = math.Copysign(value, -1)
	}
	limit := 180.0
	if latitude {
		limit = 90
	}
	if math.Abs(value) > limit {
		return 0, fmt.Errorf("angle is outside ±%.0f degrees", limit)
	}
	return value, nil
}

func parseCompact(s string, degreesWidth int) (float64, error) {
	sign := 1.0
	if strings.HasPrefix(s, "-") {
		sign = -1
	}
	s = strings.TrimLeft(s, "+-")
	width := len(strings.SplitN(s, ".", 2)[0])
	if width == degreesWidth {
		v, err := strconv.ParseFloat(s, 64)
		return sign * v, err
	}
	if width != degreesWidth+2 && width != degreesWidth+4 {
		return 0, fmt.Errorf("invalid compact coordinate width")
	}
	d, _ := strconv.ParseFloat(s[:degreesWidth], 64)
	minuteEnd := len(s)
	if width == degreesWidth+4 {
		minuteEnd = degreesWidth + 2
	}
	m, err := strconv.ParseFloat(s[degreesWidth:minuteEnd], 64)
	if err != nil || m >= 60 {
		return 0, fmt.Errorf("compact minutes must be less than 60")
	}
	sec := 0.0
	if width == degreesWidth+4 {
		sec, err = strconv.ParseFloat(s[minuteEnd:], 64)
		if err != nil || sec >= 60 {
			return 0, fmt.Errorf("compact seconds must be less than 60")
		}
	}
	return sign * (d + m/60 + sec/3600), nil
}

// ParsePair accepts latitude followed by longitude. Commas or semicolons make
// component boundaries explicit; unambiguous space-separated and hemisphere
// forms are also supported. ISO 6709 signed DD, DDM and DMS pairs are supported
// without altitude, CRS suffixes, or decimal commas.
func ParsePair(s string) (Point, error) {
	s = strings.TrimSpace(s)
	if len(s) == 0 || len(s) > 512 {
		return Point{}, fmt.Errorf("empty or overly long coordinate pair")
	}
	if match := isoRE.FindStringSubmatch(s); match != nil {
		lat, err := parseCompact(match[1], 2)
		if err != nil {
			return Point{}, err
		}
		lon, err := parseCompact(match[2], 3)
		if err != nil {
			return Point{}, err
		}
		p := Point{lat, lon}
		return p, p.Validate()
	}
	parse := func(a, b string) (Point, error) {
		lat, err := ParseLatitude(a)
		if err != nil {
			return Point{}, err
		}
		lon, err := ParseLongitude(b)
		if err != nil {
			return Point{}, err
		}
		return Point{lat, lon}, nil
	}
	if strings.ContainsAny(s, ",;") {
		parts := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' })
		if len(parts) != 2 || strings.Count(s, ",")+strings.Count(s, ";") != 1 {
			return Point{}, fmt.Errorf("expected exactly one latitude/longitude separator")
		}
		return parse(parts[0], parts[1])
	}
	// Consider only meaningful boundaries, never split the digits of a number.
	runes := []rune(s)
	var matches []Point
	for i := 1; i < len(runes); i++ {
		if !unicode.IsSpace(runes[i-1]) && !unicode.IsSpace(runes[i]) && !strings.ContainsRune("NSEWnsew", runes[i-1]) && !strings.ContainsRune("NSEWnsew", runes[i]) {
			continue
		}
		p, err := parse(string(runes[:i]), string(runes[i:]))
		if err != nil {
			continue
		}
		if len(matches) == 0 || matches[len(matches)-1] != p {
			matches = append(matches, p)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return Point{}, fmt.Errorf("ambiguous coordinate pair; separate latitude and longitude with a comma")
	}
	return Point{}, fmt.Errorf("could not parse latitude/longitude; use e.g. 37.7749, -122.4194 or 37°46′29.64″N, 122°25′9.84″W")
}

// FormatDD returns signed decimal degrees with eight decimal places.
func FormatDD(p Point) string { return fmt.Sprintf("%.8f, %.8f", p.Latitude, p.Longitude) }

func hemisphere(v float64, positive, negative string) string {
	if math.Signbit(v) {
		return negative
	}
	return positive
}

func formatAngle(v float64, dms bool, positive, negative string) string {
	if dms {
		// Round the entire angle first so 59.99999 seconds carries correctly.
		ticks := int64(math.Round(math.Abs(v) * 3600 * 10000))
		d := ticks / (3600 * 10000)
		ticks %= 3600 * 10000
		m := ticks / (60 * 10000)
		ticks %= 60 * 10000
		return fmt.Sprintf("%d°%02d′%07.4f″%s", d, m, float64(ticks)/10000, hemisphere(v, positive, negative))
	}
	ticks := int64(math.Round(math.Abs(v) * 60 * 1000000))
	d := ticks / (60 * 1000000)
	ticks %= 60 * 1000000
	return fmt.Sprintf("%d°%09.6f′%s", d, float64(ticks)/1000000, hemisphere(v, positive, negative))
}

// FormatDDM returns degrees and decimal minutes with hemisphere letters.
func FormatDDM(p Point) string {
	return formatAngle(p.Latitude, false, "N", "S") + ", " + formatAngle(p.Longitude, false, "E", "W")
}

// FormatDMS returns degrees, minutes and seconds with hemisphere letters.
func FormatDMS(p Point) string {
	return formatAngle(p.Latitude, true, "N", "S") + ", " + formatAngle(p.Longitude, true, "E", "W")
}

// FormatISO6709 returns a signed decimal-degree ISO 6709 pair (no altitude).
func FormatISO6709(p Point) string { return fmt.Sprintf("%+012.8f%+013.8f/", p.Latitude, p.Longitude) }
