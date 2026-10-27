// Package plss looks up published BLM cadastral polygons. A PLSS description
// identifies an area, not a unique coordinate; returned points are representatives.
package plss

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const DefaultURL = "https://gis.blm.gov/natlplss/rest/services/Cadastral/BLM_Natl_PLSS_CadNSDI/MapServer"

var ErrNotFound = errors.New("no matching published BLM PLSS polygon; this location or description may lack coverage")

// Client can be configured to use an ArcGIS-compatible mirror. Its zero value
// uses the public BLM endpoint and a 30-second timeout. No API key is required.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

func NewClient() *Client {
	return &Client{BaseURL: DefaultURL, HTTPClient: &http.Client{Timeout: 30 * time.Second}}
}

type Point struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

// Geometry is an Esri polygon in WGS84 (EPSG:4326). Each vertex is [longitude,
// latitude]. Rings may include holes and disconnected parts.
type Geometry struct {
	Rings [][][]float64 `json:"rings,omitempty"`
}

type Match struct {
	Layer          string         `json:"layer"`
	Label          string         `json:"label"`
	Attributes     map[string]any `json:"attributes"`
	Geometry       Geometry       `json:"geometry"`
	Representative Point          `json:"representative"`
	Approximate    bool           `json:"approximate"`
	Source         string         `json:"source"`
}

// Attribute returns a string attribute, treating null/missing values as empty.
func (m Match) Attribute(name string) string { return attr(m.Attributes, name) }

// ID returns the finest available published cadastral identifier. Multiple
// polygons can share an identifier, so callers must preserve all matches.
func (m Match) ID() string {
	for _, key := range []string{"SECDIVID", "SURVID", "FRSTDIVID", "PLSSID"} {
		if v := m.Attribute(key); v != "" {
			return v
		}
	}
	return ""
}

// Description describes a rectangular survey. Meridian is the BLM two-digit
// principal meridian code, not a longitude. Empty fraction codes mean "0".
// Section=0 requests a township. Aliquot uses smallest-to-largest order, e.g.
// SWSE means SW 1/4 of SE 1/4. Find returns actual published component polygons;
// it does not construct an idealized grid or dissolve their boundaries.
type Description struct {
	State             string `json:"state"`
	Meridian          string `json:"meridian"`
	Township          int    `json:"township"`
	TownshipDirection string `json:"township_direction"`
	TownshipFraction  string `json:"township_fraction,omitempty"`
	Range             int    `json:"range"`
	RangeDirection    string `json:"range_direction"`
	RangeFraction     string `json:"range_fraction,omitempty"`
	Section           int    `json:"section"`
	Aliquot           string `json:"aliquot,omitempty"`
	Lot               string `json:"lot,omitempty"`
}

// Lookup returns every intersecting polygon at the finest available layer.
// Boundary points can have multiple matches. If the intersected layer has no
// coverage, section and then township are tried and Layer reports the fallback.
func (c *Client) Lookup(ctx context.Context, latitude, longitude float64) ([]Match, error) {
	if !finite(latitude) || !finite(longitude) || latitude < -90 || latitude > 90 || longitude < -180 || longitude > 180 {
		return nil, errors.New("latitude must be in [-90,90] and longitude in [-180,180]")
	}
	ctx, cancel := requestContext(ctx)
	defer cancel()
	p := url.Values{"where": {"1=1"}, "geometry": {fmt.Sprintf("%.12f,%.12f", longitude, latitude)}, "geometryType": {"esriGeometryPoint"}, "inSR": {"4326"}, "spatialRel": {"esriSpatialRelIntersects"}}
	for _, layer := range []int{3, 2, 1} {
		matches, err := c.query(ctx, layer, p)
		if err != nil {
			return nil, err
		}
		if len(matches) > 0 {
			return matches, nil
		}
	}
	return nil, ErrNotFound
}

// Find resolves a structured description. An unsupplied duplicate township code
// intentionally leaves all duplicates in the result instead of guessing.
func (c *Client) Find(ctx context.Context, d Description) ([]Match, error) {
	d, err := normalize(d)
	if err != nil {
		return nil, err
	}
	ctx, cancel := requestContext(ctx)
	defer cancel()
	where := fmt.Sprintf("STATEABBR='%s' AND PRINMERCD='%s' AND TWNSHPNO='%03d' AND TWNSHPDIR='%s' AND TWNSHPFRAC='%s' AND RANGENO='%03d' AND RANGEDIR='%s' AND RANGEFRAC='%s'", d.State, d.Meridian, d.Township, d.TownshipDirection, d.TownshipFraction, d.Range, d.RangeDirection, d.RangeFraction)
	townships, err := c.query(ctx, 1, url.Values{"where": {where}})
	if err != nil {
		return nil, err
	}
	if len(townships) == 0 {
		return nil, ErrNotFound
	}
	if d.Section == 0 {
		return townships, nil
	}
	ids := make([]string, 0, len(townships))
	seen := map[string]bool{}
	for _, t := range townships {
		id := t.Attribute("PLSSID")
		if id == "" {
			return nil, errors.New("BLM township response is missing PLSSID")
		}
		if !seen[id] {
			ids = append(ids, quote(id))
			seen[id] = true
		}
	}
	where = "PLSSID IN (" + strings.Join(ids, ",") + ") AND FRSTDIVTYP='SN' AND " + fmt.Sprintf("(FRSTDIVNO='%d' OR FRSTDIVNO='%02d' OR FRSTDIVNO='%03d')", d.Section, d.Section, d.Section)
	layer := 2
	if d.Aliquot != "" {
		layer = 3
		// The SECDIVNO suffix describes an enclosing aliquot. Some states
		// instead populate QSEC/QQSEC, so retain those exact alternatives.
		where += " AND ((SECDIVTYP='A' AND (SECDIVNO=" + quote(d.Aliquot) + " OR SECDIVNO LIKE " + quote("%"+d.Aliquot) + "))"
		if len(d.Aliquot) == 2 {
			where += " OR QSEC=" + quote(d.Aliquot)
		} else if len(d.Aliquot) == 4 {
			where += " OR QQSEC=" + quote(d.Aliquot)
		}
		where += ")"
	}
	if d.Lot != "" {
		layer = 3
		lotValues := []string{quote(d.Lot)}
		if n, e := strconv.Atoi(d.Lot); e == nil {
			lotValues = append(lotValues, quote(fmt.Sprintf("%02d", n)), quote(fmt.Sprintf("%03d", n)))
		}
		in := strings.Join(lotValues, ",")
		where += " AND ((SECDIVTYP='L' AND SECDIVNO IN (" + in + ")) OR GOVLOT IN (" + in + "))"
	}
	matches, err := c.query(ctx, layer, url.Values{"where": {where}})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, ErrNotFound
	}
	// Section features omit township attributes. Enrich them from the real
	// township record, without overwriting fields from the section layer.
	for i := range matches {
		for _, t := range townships {
			if matches[i].Attribute("PLSSID") == t.Attribute("PLSSID") {
				for k, v := range t.Attributes {
					if _, ok := matches[i].Attributes[k]; !ok {
						matches[i].Attributes[k] = v
					}
				}
				break
			}
		}
		matches[i].Label = label(matches[i])
	}
	return matches, nil
}

// FindID resolves a published identifier. kind is township (PLSSID), section
// (FRSTDIVID), division/intersected (SECDIVID), or survey (SURVID). Exact IDs also support
// special surveys that cannot be expressed by Description.
func (c *Client) FindID(ctx context.Context, kind, id string) ([]Match, error) {
	fields := map[string]string{"township": "PLSSID", "section": "FRSTDIVID", "division": "SECDIVID", "intersected": "SECDIVID", "survey": "SURVID"}
	layers := map[string]int{"township": 1, "section": 2, "division": 3, "intersected": 3, "survey": 3}
	kind = strings.ToLower(strings.TrimSpace(kind))
	field, ok := fields[kind]
	if !ok {
		return nil, errors.New("PLSS ID kind must be township, section, division (or intersected), or survey")
	}
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 100 || strings.ContainsAny(id, "\r\n\x00") {
		return nil, errors.New("invalid PLSS identifier")
	}
	ctx, cancel := requestContext(ctx)
	defer cancel()
	matches, err := c.query(ctx, layers[kind], url.Values{"where": {field + "=" + quote(id)}})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, ErrNotFound
	}
	return matches, nil
}

var statePattern = regexp.MustCompile(`^[A-Z]{2}$`)
var aliquotPattern = regexp.MustCompile(`^(NE|NW|SE|SW){1,4}$`)
var lotPattern = regexp.MustCompile(`^[A-Z0-9-]{1,12}$`)

func normalize(d Description) (Description, error) {
	d.State = strings.ToUpper(strings.TrimSpace(d.State))
	d.TownshipDirection = strings.ToUpper(d.TownshipDirection)
	d.RangeDirection = strings.ToUpper(d.RangeDirection)
	mer, e := strconv.Atoi(d.Meridian)
	if !statePattern.MatchString(d.State) || e != nil || mer < 1 || mer > 99 {
		return d, errors.New("PLSS requires a two-letter state and principal meridian code 01–99")
	}
	d.Meridian = fmt.Sprintf("%02d", mer)
	if d.Township < 0 || d.Township > 999 || d.Range < 0 || d.Range > 999 {
		return d, errors.New("township and range must be between 0 and 999")
	}
	for _, v := range []string{d.TownshipDirection, d.RangeDirection} {
		if len(v) != 1 || !strings.Contains("NSEW", v) {
			return d, errors.New("township and range directions must be N, S, E, or W")
		}
	}
	if d.TownshipFraction == "" {
		d.TownshipFraction = "0"
	}
	if d.RangeFraction == "" {
		d.RangeFraction = "0"
	}
	for _, v := range []string{d.TownshipFraction, d.RangeFraction} {
		if len(v) != 1 || v[0] < '0' || v[0] > '9' {
			return d, errors.New("fraction must be a one-digit BLM fraction code")
		}
	}
	if d.Section < 0 || d.Section > 36 {
		return d, errors.New("section must be 1–36, or 0 for a township")
	}
	d.Aliquot = strings.ToUpper(strings.TrimSpace(d.Aliquot))
	d.Aliquot = strings.NewReplacer("1/4", "", "¼", "", " ", "", "/", "").Replace(d.Aliquot)
	d.Lot = strings.ToUpper(strings.TrimSpace(d.Lot))
	if d.Aliquot != "" && !aliquotPattern.MatchString(d.Aliquot) {
		return d, errors.New("aliquot must be quarter parts, e.g. NE or SWSE (SW 1/4 of SE 1/4)")
	}
	if d.Lot != "" && !lotPattern.MatchString(d.Lot) {
		return d, errors.New("invalid government lot identifier")
	}
	if d.Aliquot != "" && d.Lot != "" {
		return d, errors.New("specify either aliquot or lot, not both")
	}
	if d.Section == 0 && (d.Aliquot != "" || d.Lot != "") {
		return d, errors.New("an aliquot or lot requires a section")
	}
	return d, nil
}

func (c *Client) query(ctx context.Context, layer int, parameters url.Values) ([]Match, error) {
	base := DefaultURL
	httpClient := &http.Client{Timeout: 30 * time.Second}
	if c != nil {
		if c.BaseURL != "" {
			base = strings.TrimRight(c.BaseURL, "/")
		}
		if c.HTTPClient != nil {
			httpClient = c.HTTPClient
		}
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("PLSS service URL must be an absolute http(s) URL")
	}
	endpoint := base + "/" + strconv.Itoa(layer) + "/query"
	p := url.Values{}
	for k, v := range parameters {
		p[k] = append([]string(nil), v...)
	}
	p.Set("f", "json")
	p.Set("outFields", "*")
	p.Set("returnGeometry", "true")
	p.Set("outSR", "4326")
	p.Set("orderByFields", "OBJECTID")
	p.Set("resultRecordCount", "1000")
	var matches []Match
	for offset := 0; ; {
		p.Set("resultOffset", strconv.Itoa(offset))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+p.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "PLSconvertmyPLSS/1.0 (Go; BLM public data client)")
		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("BLM request: %w", err)
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024+1))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("BLM service returned HTTP %d", resp.StatusCode)
		}
		if readErr != nil {
			return nil, fmt.Errorf("read BLM response: %w", readErr)
		}
		if len(body) > 32*1024*1024 {
			return nil, errors.New("BLM response exceeds 32 MiB")
		}
		var result struct {
			Features *[]struct {
				Attributes map[string]any `json:"attributes"`
				Geometry   Geometry       `json:"geometry"`
			} `json:"features"`
			ExceededTransferLimit bool `json:"exceededTransferLimit"`
			Error                 *struct {
				Code    int      `json:"code"`
				Message string   `json:"message"`
				Details []string `json:"details"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("decode BLM response: %w", err)
		}
		if result.Error != nil {
			return nil, fmt.Errorf("BLM service error %d: %s %s", result.Error.Code, result.Error.Message, strings.Join(result.Error.Details, "; "))
		}
		if result.Features == nil {
			return nil, errors.New("BLM response has no feature collection")
		}
		for _, f := range *result.Features {
			if f.Attributes == nil {
				return nil, errors.New("BLM feature has no attributes")
			}
			point, err := f.Geometry.InteriorPoint()
			if err != nil {
				return nil, fmt.Errorf("BLM polygon: %w", err)
			}
			m := Match{Layer: map[int]string{1: "township", 2: "section", 3: "intersected"}[layer], Attributes: f.Attributes, Geometry: f.Geometry, Representative: point, Approximate: true, Source: base}
			m.Label = label(m)
			matches = append(matches, m)
		}
		if !result.ExceededTransferLimit {
			break
		}
		if len(*result.Features) == 0 {
			return nil, errors.New("BLM pagination returned an empty page with more results indicated")
		}
		offset += len(*result.Features)
		if offset >= 10000 {
			return nil, errors.New("PLSS query exceeds 10000 polygons; narrow the description")
		}
	}
	return matches, nil
}

func requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, 30*time.Second)
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
func finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }
func attr(a map[string]any, k string) string {
	if v, ok := a[k].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}
func label(m Match) string {
	parts := []string{}
	if v := m.Attribute("STATEABBR"); v != "" {
		parts = append(parts, v)
	}
	if v := m.Attribute("TWNSHPLAB"); v != "" {
		parts = append(parts, v)
	}
	if v := m.Attribute("FRSTDIVLAB"); v != "" {
		typ := m.Attribute("FRSTDIVTXT")
		if typ == "" {
			typ = "Division"
		}
		parts = append(parts, typ+" "+v)
	}
	if v := m.Attribute("SECDIVLAB"); v != "" {
		parts = append(parts, v)
	}
	if v := m.Attribute("PRINMER"); v != "" {
		parts = append(parts, v)
	}
	if len(parts) == 0 {
		return m.ID()
	}
	return strings.Join(parts, ", ")
}
