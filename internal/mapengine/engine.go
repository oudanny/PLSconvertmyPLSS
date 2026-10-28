// Package mapengine exposes the offline converter to a map interface without
// depending on a browser. The WebAssembly bridge only supplies file access.
package mapengine

import (
	"context"
	"errors"
	"strings"

	"github.com/oudanny/PLSconvertmyPLSS/coord"
	"github.com/oudanny/PLSconvertmyPLSS/offline"
	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

type Coordinates struct {
	Latitude  float64    `json:"latitude"`
	Longitude float64    `json:"longitude"`
	Datum     string     `json:"datum"`
	DD        string     `json:"dd"`
	DDM       string     `json:"ddm"`
	DMS       string     `json:"dms"`
	ISO6709   string     `json:"iso6709"`
	UTM       *coord.UTM `json:"utm,omitempty"`
	UTMText   string     `json:"utm_text,omitempty"`
}

type Match struct {
	offline.Hit
	Label             string        `json:"label"`
	Description       string        `json:"description"`
	DescriptionDetail string        `json:"description_detail"`
	Identifiers       Identifiers   `json:"identifiers"`
	Geometry          plss.Geometry `json:"geometry"`
	Representative    Coordinates   `json:"representative"`
}

// Identifiers distinguishes a record's actual published identifiers from the
// display ID, which can fall back to its parent when a subdivision has none.
type Identifiers struct {
	Township string `json:"township"`
	Section  string `json:"section"`
	Division string `json:"division"`
	Survey   string `json:"survey"`
}

type Result struct {
	Coordinates    *Coordinates `json:"coordinates,omitempty"`
	Matches        []Match      `json:"matches"`
	NoMatch        bool         `json:"no_match,omitempty"`
	Representative bool         `json:"representative,omitempty"`
	Notes          []string     `json:"notes,omitempty"`
}

type Request struct {
	Kind      string  `json:"kind"`
	Value     string  `json:"value"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	ID        string  `json:"id"`
}

type Reader interface {
	Lookup(context.Context, float64, float64) ([]offline.Hit, error)
	FindID(string, string) ([]offline.Hit, error)
	Full(offline.Hit) (plss.Match, error)
}

func Format(p coord.Point) Coordinates {
	c := Coordinates{Latitude: p.Latitude, Longitude: p.Longitude, Datum: "WGS84", DD: coord.FormatDD(p), DDM: coord.FormatDDM(p), DMS: coord.FormatDMS(p), ISO6709: coord.FormatISO6709(p)}
	if u, err := coord.ToUTM(p); err == nil {
		c.UTM = &u
		c.UTMText = u.String()
	}
	return c
}

// Execute never makes network requests. A valid coordinate outside published
// coverage remains a successful conversion with NoMatch set.
func Execute(ctx context.Context, reader Reader, command string, request Request) (Result, error) {
	r := Result{Matches: []Match{}}
	var p coord.Point
	var hits []offline.Hit
	var err error
	switch command {
	case "convert":
		switch request.Kind {
		case "", "latlon":
			p, err = coord.ParsePair(request.Value)
		case "utm":
			var u coord.UTM
			u, err = coord.ParseUTM(request.Value)
			if err == nil {
				p, err = coord.FromUTM(u)
			}
		default:
			err = errors.New("coordinate kind must be latlon or utm")
		}
	case "lookup":
		p = coord.Point{Latitude: request.Latitude, Longitude: request.Longitude}
		err = p.Validate()
	case "find":
		if reader == nil {
			return r, errors.New("open an atlas before searching PLSS identifiers")
		}
		id := strings.TrimSpace(request.ID)
		if id == "" {
			return r, errors.New("provide a published PLSS identifier")
		}
		kind := strings.ToLower(strings.TrimSpace(request.Kind))
		if kind == "" {
			kind = "division"
		}
		hits, err = reader.FindID(kind, id)
		r.Representative = true
		r.Notes = append(r.Notes, "PLSS identifies an area; representative coordinates are interior points, not surveyed corners.")
	default:
		return r, errors.New("unknown conversion command")
	}
	if command != "find" {
		if err != nil {
			return r, err
		}
		c := Format(p)
		r.Coordinates = &c
		if c.UTM == nil {
			r.Notes = append(r.Notes, "UTM is unavailable outside 80 degrees south to 84 degrees north.")
		}
		if reader == nil {
			return r, nil
		}
		hits, err = reader.Lookup(ctx, p.Latitude, p.Longitude)
	}
	if errors.Is(err, plss.ErrNotFound) {
		r.NoMatch = true
		return r, nil
	}
	if err != nil {
		return r, err
	}
	for _, hit := range hits {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		m, err := reader.Full(hit)
		if err != nil {
			return Result{}, err
		}
		rep := Format(coord.Point{Latitude: m.Representative.Latitude, Longitude: m.Representative.Longitude})
		ids := Identifiers{Township: m.Attribute("PLSSID"), Section: m.Attribute("FRSTDIVID"), Division: m.Attribute("SECDIVID"), Survey: m.Attribute("SURVID")}
		description := plss.DescribeIdentifiers(ids.Township, ids.Section, ids.Division, ids.Survey)
		if hit.Layer == "intersected" && ids.Division == "" && ids.Survey == "" && (ids.Section != "" || ids.Township != "") {
			description.Detail += " · Subdivision identifier unavailable; showing parent description."
		} else if hit.Layer == "section" && ids.Section == "" && ids.Township != "" {
			description.Detail += " · Section identifier unavailable; showing parent township."
		}
		r.Matches = append(r.Matches, Match{Hit: hit, Label: m.Label, Description: description.Text, DescriptionDetail: description.Detail, Identifiers: ids, Geometry: m.Geometry, Representative: rep})
		if r.Coordinates == nil {
			c := rep
			r.Coordinates = &c
		}
	}
	return r, nil
}
