// Command plssconvert converts geographic coordinates, UTM, and BLM PLSS areas.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/oudanny/PLSconvertmyPLSS/coord"
	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

const usage = `plssconvert — convert latitude/longitude, UTM, and PLSS

Usage:
  plssconvert latlon --lat LAT --lon LON [--plss] [--json]
  plssconvert latlon --value "LAT, LON" [--plss] [--json]
  plssconvert utm --value "13N 476915.239 4429457.113" [--plss] [--json]
  plssconvert utm --zone 13 --hemisphere N --easting E --northing N
  plssconvert plss --state CO --meridian 06 --township 1N --range 70W --section 30
  plssconvert plss --id CO060010N0700W0SN300ASWSE --layer intersected

Coordinate commands output DD, DDM, DMS, and WGS84 UTM. Add --plss for live
BLM lookup. PLSS areas produce an approximate interior representative point.
Run a command with --help to see its flags. No network is needed without PLSS.
`

type options struct {
	json, geometry, lookup bool
	serviceURL             string
	timeout                time.Duration
}

type coordinateOutput struct {
	Latitude  float64    `json:"latitude"`
	Longitude float64    `json:"longitude"`
	Datum     string     `json:"datum"`
	DD        string     `json:"dd"`
	DDM       string     `json:"ddm"`
	DMS       string     `json:"dms"`
	ISO6709   string     `json:"iso6709"`
	UTM       *coord.UTM `json:"utm,omitempty"`
}

type result struct {
	Coordinates coordinateOutput `json:"coordinates"`
	PLSS        []plss.Match     `json:"plss,omitempty"`
}

type output struct {
	Results  []result `json:"results"`
	Warnings []string `json:"warnings,omitempty"`
	Source   string   `json:"source,omitempty"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func commonFlags(fs *flag.FlagSet, o *options, lookup bool) {
	fs.BoolVar(&o.json, "json", false, "write structured JSON")
	fs.BoolVar(&o.geometry, "geometry", false, "include PLSS polygon rings in JSON (longitude, latitude)")
	fs.StringVar(&o.serviceURL, "service-url", "", "override the BLM ArcGIS MapServer URL")
	fs.DurationVar(&o.timeout, "timeout", 30*time.Second, "total PLSS request timeout, e.g. 30s")
	if lookup {
		fs.BoolVar(&o.lookup, "plss", false, "look up containing PLSS areas using BLM (requires internet)")
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(stdout, usage)
		return err
	}
	command := args[0]
	if command != "latlon" && command != "utm" && command != "plss" {
		return fmt.Errorf("unknown command %q; run plssconvert --help", command)
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o options
	commonFlags(fs, &o, command != "plss")
	var value, lat, lon, hemisphere, band string
	var zone int
	var easting, northing float64
	var d plss.Description
	var township, rangeValue, id, layer string
	switch command {
	case "latlon":
		fs.StringVar(&value, "value", "", "latitude/longitude pair (DD, DDM, DMS, or compact ISO 6709)")
		fs.StringVar(&lat, "lat", "", "latitude with optional N/S hemisphere")
		fs.StringVar(&lon, "lon", "", "longitude with optional E/W hemisphere")
	case "utm":
		fs.StringVar(&value, "value", "", "UTM zone, hemisphere, easting, northing; e.g. 13N 476915.239 4429457.113")
		fs.IntVar(&zone, "zone", 0, "UTM zone 1–60")
		fs.StringVar(&hemisphere, "hemisphere", "", "N or S (hemisphere, not latitude band)")
		fs.StringVar(&band, "band", "", "explicit UTM latitude band C–X, excluding I and O")
		fs.Float64Var(&easting, "easting", 0, "easting in metres")
		fs.Float64Var(&northing, "northing", 0, "northing in metres")
	case "plss":
		fs.StringVar(&d.State, "state", "", "two-letter state code")
		fs.StringVar(&d.Meridian, "meridian", "", "BLM principal meridian code, e.g. 06 (Sixth Principal Meridian)")
		fs.StringVar(&township, "township", "", "township and direction, e.g. 1N")
		fs.StringVar(&rangeValue, "range", "", "range and direction, e.g. 70W")
		fs.StringVar(&d.TownshipFraction, "township-fraction", "0", "one-digit BLM township fraction code")
		fs.StringVar(&d.RangeFraction, "range-fraction", "0", "one-digit BLM range fraction code")
		fs.IntVar(&d.Section, "section", 0, "section number 1–36; omit for the entire township")
		fs.StringVar(&d.Aliquot, "aliquot", "", "BLM aliquot code, e.g. SWSE; uses actual BLM polygons")
		fs.StringVar(&d.Lot, "lot", "", "government lot number, where present in BLM data")
		fs.StringVar(&id, "id", "", "exact BLM identifier (alternative to structured description)")
		fs.StringVar(&layer, "layer", "intersected", "identifier kind: township, section, intersected (division), or survey")
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q; use named flags", fs.Arg(0))
	}
	if o.timeout <= 0 {
		return errors.New("--timeout must be positive")
	}
	if o.geometry && !o.json {
		return errors.New("--geometry requires --json")
	}
	visited := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })
	ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
	defer cancel()
	client := plss.NewClient()
	if o.serviceURL != "" {
		client.BaseURL = o.serviceURL
	}
	client.HTTPClient = &http.Client{Timeout: o.timeout}
	out := output{}
	var p coord.Point
	var err error
	switch command {
	case "latlon":
		if value != "" {
			if visited["lat"] || visited["lon"] {
				return errors.New("use --value or --lat/--lon, not both")
			}
			p, err = coord.ParsePair(value)
		} else {
			if lat == "" || lon == "" {
				return errors.New("provide --lat and --lon, or --value")
			}
			p.Latitude, err = coord.ParseLatitude(lat)
			if err == nil {
				p.Longitude, err = coord.ParseLongitude(lon)
			}
		}
	case "utm":
		var u coord.UTM
		if value != "" {
			for _, f := range []string{"zone", "hemisphere", "band", "easting", "northing"} {
				if visited[f] {
					return fmt.Errorf("--value cannot be combined with --%s", f)
				}
			}
			u, err = coord.ParseUTM(value)
		} else {
			if !visited["zone"] || !visited["easting"] || !visited["northing"] || (hemisphere == "" && band == "") {
				return errors.New("provide --value, or --zone, --hemisphere (or --band), --easting, and --northing")
			}
			u = coord.UTM{Zone: zone, Hemisphere: strings.ToUpper(hemisphere), Band: strings.ToUpper(band), Easting: easting, Northing: northing}
		}
		if err == nil {
			p, err = coord.FromUTM(u)
		}
	case "plss":
		var matches []plss.Match
		if id != "" {
			for _, f := range []string{"state", "meridian", "township", "range", "section", "aliquot", "lot", "township-fraction", "range-fraction"} {
				if visited[f] {
					return fmt.Errorf("--id cannot be combined with --%s", f)
				}
			}
			matches, err = client.FindID(ctx, layer, id)
		} else {
			if visited["layer"] {
				return errors.New("--layer is only used with --id")
			}
			d.Township, d.TownshipDirection, err = parseTR(township, "NSEW", "township")
			if err == nil {
				d.Range, d.RangeDirection, err = parseTR(rangeValue, "NSEW", "range")
			}
			if err == nil {
				matches, err = client.Find(ctx, d)
			}
		}
		if err != nil {
			return err
		}
		if len(matches) == 0 {
			return errors.New("no matching PLSS areas in the BLM dataset")
		}
		out.Source = client.BaseURL
		out.Warnings = append(out.Warnings, "PLSS identifies an area. Coordinates are approximate interior representative points, not survey corners or legal boundary determinations.")
		for _, m := range matches {
			p = coord.Point{Latitude: m.Representative.Latitude, Longitude: m.Representative.Longitude}
			c, warning := coordinates(p)
			if warning != "" {
				out.Warnings = append(out.Warnings, warning)
			}
			if !o.geometry {
				m.Geometry = plss.Geometry{}
			}
			out.Results = append(out.Results, result{Coordinates: c, PLSS: []plss.Match{m}})
		}
		return writeOutput(stdout, out, o.json)
	}
	if err != nil {
		return err
	}
	c, warning := coordinates(p)
	if warning != "" {
		out.Warnings = append(out.Warnings, warning)
	}
	r := result{Coordinates: c}
	if o.lookup {
		r.PLSS, err = client.Lookup(ctx, p.Latitude, p.Longitude)
		if err != nil {
			return err
		}
		if len(r.PLSS) == 0 {
			return errors.New("no PLSS coverage at this coordinate in the BLM dataset")
		}
		out.Source = client.BaseURL
		if !o.geometry {
			for i := range r.PLSS {
				r.PLSS[i].Geometry = plss.Geometry{}
			}
		}
	}
	out.Results = append(out.Results, r)
	return writeOutput(stdout, out, o.json)
}

var trPattern = regexp.MustCompile(`^([0-9]+)([NSEW])$`)

func parseTR(value, directions, name string) (int, string, error) {
	value = strings.ToUpper(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, " ", "")
	if name == "township" {
		value = strings.TrimPrefix(value, "T")
	} else {
		value = strings.TrimPrefix(value, "R")
	}
	m := trPattern.FindStringSubmatch(value)
	if m == nil || !strings.Contains(directions, m[2]) {
		return 0, "", fmt.Errorf("--%s needs a number and direction, e.g. %s", name, map[string]string{"township": "1N", "range": "70W"}[name])
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, "", fmt.Errorf("invalid %s number", name)
	}
	return n, m[2], nil
}

func coordinates(p coord.Point) (coordinateOutput, string) {
	c := coordinateOutput{Latitude: p.Latitude, Longitude: p.Longitude, Datum: "WGS84", DD: coord.FormatDD(p), DDM: coord.FormatDDM(p), DMS: coord.FormatDMS(p), ISO6709: coord.FormatISO6709(p)}
	u, err := coord.ToUTM(p)
	if err != nil {
		return c, "UTM unavailable: " + err.Error()
	}
	c.UTM = &u
	return c, ""
}

func writeOutput(w io.Writer, out output, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	for i, r := range out.Results {
		if len(out.Results) > 1 {
			if _, err := fmt.Fprintf(w, "Result %d\n", i+1); err != nil {
				return err
			}
		}
		c := r.Coordinates
		if _, err := fmt.Fprintf(w, "DD:  %s\nDDM: %s\nDMS: %s\nISO: %s\n", c.DD, c.DDM, c.DMS, c.ISO6709); err != nil {
			return err
		}
		if c.UTM != nil {
			if _, err := fmt.Fprintf(w, "UTM: %s (WGS84)\n", c.UTM.String()); err != nil {
				return err
			}
		}
		for _, m := range r.PLSS {
			if _, err := fmt.Fprintf(w, "PLSS [%s]: %s\n  ID: %s\n", m.Layer, m.Label, m.ID()); err != nil {
				return err
			}
		}
	}
	for _, warning := range out.Warnings {
		if _, err := fmt.Fprintln(w, "Note:", warning); err != nil {
			return err
		}
	}
	if out.Source != "" {
		if _, err := fmt.Fprintln(w, "Source: Bureau of Land Management —", out.Source); err != nil {
			return err
		}
	}
	return nil
}
