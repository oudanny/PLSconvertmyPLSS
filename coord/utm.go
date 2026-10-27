package coord

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// UTM is a WGS84 UTM position in meters. Hemisphere is N or S. Band is optional
// (C through X, excluding I/O); when supplied it is checked against the result.
type UTM struct {
	Zone       int     `json:"zone"`
	Hemisphere string  `json:"hemisphere"`
	Easting    float64 `json:"easting"`
	Northing   float64 `json:"northing"`
	Band       string  `json:"band,omitempty"`
}

func (u UTM) String() string {
	return fmt.Sprintf("%d%s %.3f %.3f", u.Zone, u.Hemisphere, u.Easting, u.Northing)
}

const (
	wgs84A   = 6378137.0
	wgs84F   = 1 / 298.257223563
	utmScale = 0.9996
	degrees  = math.Pi / 180
	bands    = "CDEFGHJKLMNPQRSTUVWX"
)

// Sixth-order Krüger coefficients and conformal latitude follow the primary
// mathematical definition in PROJ's Transverse Mercator documentation:
// https://proj.org/en/stable/operations/projections/tmerc.html
// This avoids the loss of accuracy of low-order longitude series near the
// widened Norway/Svalbard zones.
func projectionCoefficients() (a float64, alpha, beta [6]float64) {
	n := wgs84F / (2 - wgs84F)
	n2, n3, n4, n5, n6 := n*n, n*n*n, math.Pow(n, 4), math.Pow(n, 5), math.Pow(n, 6)
	a = wgs84A / (1 + n) * (1 + n2/4 + n4/64 + n6/256)
	alpha = [6]float64{
		n/2 - 2*n2/3 + 5*n3/16 + 41*n4/180 - 127*n5/288 + 7891*n6/37800,
		13*n2/48 - 3*n3/5 + 557*n4/1440 + 281*n5/630 - 1983433*n6/1935360,
		61*n3/240 - 103*n4/140 + 15061*n5/26880 + 167603*n6/181440,
		49561*n4/161280 - 179*n5/168 + 6601661*n6/7257600,
		34729*n5/80640 - 3418889*n6/1995840,
		212378941 * n6 / 319334400,
	}
	beta = [6]float64{
		-n/2 + 2*n2/3 - 37*n3/96 + n4/360 + 81*n5/512 - 96199*n6/604800,
		-n2/48 - n3/15 + 437*n4/1440 - 46*n5/105 + 1118711*n6/3870720,
		-17*n3/480 + 37*n4/840 + 209*n5/4480 - 5569*n6/90720,
		-4397*n4/161280 + 11*n5/504 + 830251*n6/7257600,
		-4583*n5/161280 + 108847*n6/3991680,
		-20648693 * n6 / 638668800,
	}
	return
}

func latitudeBand(lat float64) string {
	i := int(math.Floor((lat + 80) / 8))
	if i < 0 {
		i = 0
	}
	if i >= len(bands) {
		i = len(bands) - 1
	}
	return bands[i : i+1]
}

// ToUTM selects the standard zone, including Norway and Svalbard exceptions.
// UTM covers latitudes from 80°S through 84°N. Polar UPS is not UTM.
func ToUTM(p Point) (UTM, error) {
	if err := p.Validate(); err != nil {
		return UTM{}, err
	}
	if p.Latitude < -80 || p.Latitude > 84 {
		return UTM{}, fmt.Errorf("UTM covers latitude -80 through 84 degrees; use UPS for polar coordinates")
	}
	zone := int(math.Floor((p.Longitude+180)/6)) + 1
	if zone > 60 {
		zone = 60
	}
	if p.Latitude >= 56 && p.Latitude < 64 && p.Longitude >= 3 && p.Longitude < 12 {
		zone = 32
	}
	if p.Latitude >= 72 && p.Latitude <= 84 {
		switch {
		case p.Longitude >= 0 && p.Longitude < 9:
			zone = 31
		case p.Longitude >= 9 && p.Longitude < 21:
			zone = 33
		case p.Longitude >= 21 && p.Longitude < 33:
			zone = 35
		case p.Longitude >= 33 && p.Longitude < 42:
			zone = 37
		}
	}
	lat := p.Latitude * degrees
	lon := (p.Longitude - (float64(zone)*6 - 183)) * degrees
	e := math.Sqrt(wgs84F * (2 - wgs84F))
	chi := math.Atan(math.Sinh(math.Asinh(math.Tan(lat)) - e*math.Atanh(e*math.Sin(lat))))
	tau := math.Tan(chi)
	xiPrime := math.Atan2(tau, math.Cos(lon))
	etaPrime := math.Asinh(math.Sin(lon) / math.Hypot(tau, math.Cos(lon)))
	a, alpha, _ := projectionCoefficients()
	xi, eta := xiPrime, etaPrime
	for j, coefficient := range alpha {
		k := 2 * float64(j+1)
		xi += coefficient * math.Sin(k*xiPrime) * math.Cosh(k*etaPrime)
		eta += coefficient * math.Cos(k*xiPrime) * math.Sinh(k*etaPrime)
	}
	u := UTM{Zone: zone, Hemisphere: "N", Easting: 500000 + utmScale*a*eta, Northing: utmScale * a * xi, Band: latitudeBand(p.Latitude)}
	if p.Latitude < 0 {
		u.Hemisphere = "S"
		u.Northing += 10000000
	}
	return u, nil
}

func normalizeUTM(u UTM) (UTM, error) {
	u.Hemisphere = strings.ToUpper(strings.TrimSpace(u.Hemisphere))
	u.Band = strings.ToUpper(strings.TrimSpace(u.Band))
	if u.Zone < 1 || u.Zone > 60 {
		return u, fmt.Errorf("UTM zone must be 1 through 60")
	}
	if u.Band != "" {
		if len(u.Band) != 1 || !strings.Contains(bands, u.Band) {
			return u, fmt.Errorf("UTM band must be C through X, excluding I and O")
		}
		hemi := "N"
		if u.Band < "N" {
			hemi = "S"
		}
		if u.Hemisphere == "" {
			u.Hemisphere = hemi
		}
		if u.Hemisphere != hemi {
			return u, fmt.Errorf("UTM band and hemisphere disagree")
		}
	}
	if u.Hemisphere != "N" && u.Hemisphere != "S" {
		return u, fmt.Errorf("UTM hemisphere must be N or S")
	}
	if !finite(u.Easting) || u.Easting < 100000 || u.Easting >= 1000000 {
		return u, fmt.Errorf("UTM easting must be finite and at least 100000 and less than 1000000 meters")
	}
	if !finite(u.Northing) || u.Northing < 0 || u.Northing > 10000000 {
		return u, fmt.Errorf("UTM northing must be finite and between 0 and 10000000 meters")
	}
	return u, nil
}

// FromUTM converts a WGS84 UTM position to decimal degrees. A supplied latitude
// band is verified, with a small tolerance for coordinates rounded to meters.
func FromUTM(u UTM) (Point, error) {
	u, err := normalizeUTM(u)
	if err != nil {
		return Point{}, err
	}
	a, _, beta := projectionCoefficients()
	x, y := u.Easting-500000, u.Northing
	if u.Hemisphere == "S" {
		y -= 10000000
	}
	xi, eta := y/(utmScale*a), x/(utmScale*a)
	xiPrime, etaPrime := xi, eta
	for j, coefficient := range beta {
		k := 2 * float64(j+1)
		xiPrime += coefficient * math.Sin(k*xi) * math.Cosh(k*eta)
		etaPrime += coefficient * math.Cos(k*xi) * math.Sinh(k*eta)
	}
	chi := math.Atan2(math.Sin(xiPrime), math.Hypot(math.Sinh(etaPrime), math.Cos(xiPrime)))
	lon := math.Atan2(math.Sinh(etaPrime), math.Cos(xiPrime))
	e := math.Sqrt(wgs84F * (2 - wgs84F))
	psi := math.Asinh(math.Tan(chi))
	lat := chi
	for i := 0; i < 12; i++ {
		next := math.Atan(math.Sinh(psi + e*math.Atanh(e*math.Sin(lat))))
		if math.Abs(next-lat) < 1e-15 {
			lat = next
			break
		}
		lat = next
	}
	p := Point{Latitude: lat / degrees, Longitude: lon/degrees + float64(u.Zone)*6 - 183}
	if p.Longitude > 180 {
		p.Longitude -= 360
	}
	if p.Longitude < -180 {
		p.Longitude += 360
	}
	// The 1e-5° allowance accommodates whole-meter values on outer boundaries.
	if p.Latitude < -80-1e-5 || p.Latitude > 84+1e-5 {
		return Point{}, fmt.Errorf("UTM position falls outside the -80 to 84 degree latitude coverage")
	}
	if u.Hemisphere == "N" && p.Latitude < -1e-8 || u.Hemisphere == "S" && p.Latitude > 1e-8 {
		return Point{}, fmt.Errorf("UTM position falls outside its hemisphere")
	}
	if u.Band != "" {
		index := strings.Index(bands, u.Band)
		lower, upper := float64(index)*8-80, float64(index+1)*8-80
		if u.Band == "X" {
			upper = 84
		}
		if p.Latitude < lower-1e-5 || p.Latitude > upper+1e-5 {
			return Point{}, fmt.Errorf("UTM position does not fall in latitude band %s", u.Band)
		}
	}
	if p.Latitude < -80 {
		p.Latitude = -80
	}
	if p.Latitude > 84 {
		p.Latitude = 84
	}
	return p, p.Validate()
}

var utmRE = regexp.MustCompile(`(?i)^\s*(?:UTM\s+)?([0-9]{1,2})\s*(?:([NS])|(?:BAND\s*([C-HJ-NP-X]))|(?:([C-HJ-NP-X])-BAND)|([C-HJ-MP-RT-X]))\s+([0-9]+(?:\.[0-9]+)?)\s+([0-9]+(?:\.[0-9]+)?)\s*$`)

// ParseUTM accepts "10N 551130 4180998" (zone + hemisphere + meters).
// Band input uses "10 band S ..." or "10S-band ..."; other unambiguous
// letters are also accepted directly, e.g. "10T ...". Bare N/S always mean
// hemispheres, never latitude bands.
func ParseUTM(s string) (UTM, error) {
	if len(s) > 256 {
		return UTM{}, fmt.Errorf("overly long UTM coordinate")
	}
	s = strings.ReplaceAll(s, ",", " ")
	m := utmRE.FindStringSubmatch(s)
	if m == nil {
		return UTM{}, fmt.Errorf("expected UTM zone/hemisphere easting northing, e.g. 10N 551130 4180998; use '10 band S' for a latitude band")
	}
	zone, _ := strconv.Atoi(m[1])
	easting, err := strconv.ParseFloat(m[6], 64)
	if err != nil {
		return UTM{}, fmt.Errorf("invalid UTM easting")
	}
	northing, err := strconv.ParseFloat(m[7], 64)
	if err != nil {
		return UTM{}, fmt.Errorf("invalid UTM northing")
	}
	u := UTM{Zone: zone, Hemisphere: strings.ToUpper(m[2]), Easting: easting, Northing: northing}
	for _, band := range m[3:6] {
		if band != "" {
			u.Band = strings.ToUpper(band)
		}
	}
	u, err = normalizeUTM(u)
	if err != nil {
		return UTM{}, err
	}
	if _, err := FromUTM(u); err != nil {
		return UTM{}, err
	}
	return u, nil
}
