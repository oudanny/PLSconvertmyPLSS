// Command plssatlas performs offline conversion with experimental compact atlases.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/oudanny/PLSconvertmyPLSS/coord"
	"github.com/oudanny/PLSconvertmyPLSS/internal/regionatlas"
	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

const usage = `plssatlas — experimental offline PLSS atlas converter

Usage:
  plssatlas latlon --value "39.5296,-119.8138" --data PACK.atlas [--json]
  plssatlas utm --value "11N 258166 4378953" --data PACK.atlas [--json]
  plssatlas plss --id PUBLISHED_ID --layer division --data PACK.atlas [--json]

Uses a read-only memory mapping by default; --buffered uses ordinary file reads.
--data accepts one state atlas or a region containing several state atlases.
Region lookups preserve every match at the finest available PLSS layer.
--metadata loads original source attributes and requires an atlas containing
the optional full-attributes component. Core atlases support coordinate and
exact published-ID conversion. This experimental command never uses a network.
Run a command with --help for its flags.
`

type coordinates struct {
	Latitude  float64    `json:"latitude"`
	Longitude float64    `json:"longitude"`
	Datum     string     `json:"datum"`
	DD        string     `json:"dd"`
	DDM       string     `json:"ddm"`
	DMS       string     `json:"dms"`
	ISO6709   string     `json:"iso6709"`
	UTM       *coord.UTM `json:"utm,omitempty"`
}

type match struct {
	regionatlas.Hit
	Attributes map[string]any `json:"attributes,omitempty"`
}

type result struct {
	Coordinates    coordinates `json:"coordinates"`
	Representative bool        `json:"representative,omitempty"`
	Matches        []match     `json:"plss"`
}

type output struct {
	Experimental bool                `json:"experimental"`
	Data         string              `json:"data"`
	Source       *plss.PackMetadata  `json:"source,omitempty"`
	Sources      []plss.PackMetadata `json:"sources,omitempty"`
	Results      []result            `json:"results"`
	Notes        []string            `json:"notes,omitempty"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(stdout, usage)
		return err
	}
	command := args[0]
	if command != "latlon" && command != "utm" && command != "plss" {
		return fmt.Errorf("unknown command %q; run plssatlas --help", command)
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var data, value, id, layer string
	var asJSON, metadata, buffered bool
	fs.StringVar(&data, "data", "", "path to an experimental state or region .atlas file (required)")
	fs.BoolVar(&asJSON, "json", false, "write structured JSON")
	fs.BoolVar(&metadata, "metadata", false, "load original attributes; requires a full-attributes atlas")
	fs.BoolVar(&buffered, "buffered", false, "use ordinary file reads instead of memory mapping")
	if command == "plss" {
		fs.StringVar(&id, "id", "", "exact published BLM identifier (required)")
		fs.StringVar(&layer, "layer", "division", "identifier kind: township, section, division/intersected, or survey")
	} else {
		fs.StringVar(&value, "value", "", "latitude/longitude pair or UTM string (required)")
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
	if strings.TrimSpace(data) == "" {
		return errors.New("provide --data with an experimental atlas file")
	}
	var point coord.Point
	var err error
	switch command {
	case "latlon":
		if strings.TrimSpace(value) == "" {
			return errors.New("provide --value with a latitude/longitude pair")
		}
		point, err = coord.ParsePair(value)
	case "utm":
		if strings.TrimSpace(value) == "" {
			return errors.New("provide --value with a UTM coordinate")
		}
		var u coord.UTM
		u, err = coord.ParseUTM(value)
		if err == nil {
			point, err = coord.FromUTM(u)
		}
	case "plss":
		id = strings.TrimSpace(id)
		if id == "" {
			return errors.New("provide --id with an exact published BLM identifier")
		}
		layer = strings.ToLower(strings.TrimSpace(layer))
		switch layer {
		case "township", "section", "division", "intersected", "survey":
		default:
			return errors.New("--layer must be township, section, division/intersected, or survey")
		}
	}
	if err != nil {
		return err
	}
	var reader *regionatlas.Reader
	if buffered {
		reader, err = regionatlas.Open(data)
	} else {
		reader, err = regionatlas.OpenMapped(data)
	}
	if err != nil {
		return fmt.Errorf("open atlas: %w", err)
	}
	defer reader.Close()
	full := func(hit regionatlas.Hit) (plss.Match, error) {
		has, err := reader.HasFullAttributes(hit)
		if err != nil {
			return plss.Match{}, err
		}
		if !has {
			return plss.Match{}, fmt.Errorf("--metadata requires full source attributes; the %s atlas contains identifiers and geometry or a conversion-only attribute subset", hit.State)
		}
		return reader.Full(hit)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	out := output{Experimental: true, Data: data}
	sources := reader.Sources()
	if len(sources) == 1 {
		out.Source = &sources[0]
	} else {
		out.Sources = sources
	}
	if command == "plss" {
		hits, err := reader.FindID(layer, id)
		if err != nil {
			return err
		}
		for _, hit := range hits {
			m := match{Hit: hit}
			var representative plss.Point
			if metadata {
				full, err := full(hit)
				if err != nil {
					return err
				}
				m.Attributes = full.Attributes
				representative = full.Representative
			} else {
				representative, err = reader.Representative(hit)
				if err != nil {
					return err
				}
			}
			c, note := formatCoordinates(coord.Point{Latitude: representative.Latitude, Longitude: representative.Longitude})
			if note != "" {
				out.Notes = appendUnique(out.Notes, note)
			}
			out.Results = append(out.Results, result{Coordinates: c, Representative: true, Matches: []match{m}})
		}
		out.Notes = append(out.Notes, "PLSS identifies an area; coordinates are approximate interior representative points, not surveyed corners.")
	} else {
		hits, err := reader.Lookup(ctx, point.Latitude, point.Longitude)
		if err != nil {
			return err
		}
		c, note := formatCoordinates(point)
		if note != "" {
			out.Notes = append(out.Notes, note)
		}
		r := result{Coordinates: c}
		for _, hit := range hits {
			m := match{Hit: hit}
			if metadata {
				full, err := full(hit)
				if err != nil {
					return err
				}
				m.Attributes = full.Attributes
			}
			r.Matches = append(r.Matches, m)
		}
		out.Results = append(out.Results, r)
	}
	return writeOutput(stdout, out, asJSON)
}

func appendUnique(items []string, s string) []string {
	for _, item := range items {
		if item == s {
			return items
		}
	}
	return append(items, s)
}
func formatCoordinates(p coord.Point) (coordinates, string) {
	c := coordinates{Latitude: p.Latitude, Longitude: p.Longitude, Datum: "WGS84", DD: coord.FormatDD(p), DDM: coord.FormatDDM(p), DMS: coord.FormatDMS(p), ISO6709: coord.FormatISO6709(p)}
	u, err := coord.ToUTM(p)
	if err != nil {
		return c, "UTM is unavailable outside its latitude coverage (80°S–84°N)."
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
	if _, err := fmt.Fprintln(w, "Experimental PLSS atlas — offline"); err != nil {
		return err
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
		for _, m := range r.Matches {
			if _, err := fmt.Fprintf(w, "PLSS [%s]: %s\n", m.Layer, m.ID); err != nil {
				return err
			}
			if m.Attributes != nil {
				raw, err := json.Marshal(m.Attributes)
				if err != nil {
					return err
				}
				if _, err := fmt.Fprintf(w, "Metadata: %s\n", raw); err != nil {
					return err
				}
			}
		}
	}
	for _, note := range out.Notes {
		if _, err := fmt.Fprintln(w, "Note:", note); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "Data: %s\n", out.Data); err != nil {
		return err
	}
	sources := out.Sources
	if out.Source != nil {
		sources = []plss.PackMetadata{*out.Source}
	}
	for _, source := range sources {
		if _, err := fmt.Fprintf(w, "Source [%s]: %s (snapshot %s)\n", source.State, source.Source, source.CreatedAt); err != nil {
			return err
		}
	}
	return nil
}
