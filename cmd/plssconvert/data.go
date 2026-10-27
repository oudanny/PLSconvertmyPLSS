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
	"path/filepath"
	"strings"
	"time"

	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

const dataUsage = `Manage offline PLSS data packs:
  plssconvert data download --state NV --out data/nevada.sqlite
  plssconvert data info --db data/nevada.sqlite [--json]
  plssconvert data unlocated --db data/nevada.sqlite [--json]

Supported states: AZ, CA, CO, ID, MT, NM, NV, OR, UT, WA, WY.
Download retrieves the selected state's published townships, sections, and subdivisions
from BLM and builds a local SQLite spatial index. It requires internet access
and may take several minutes. Existing output files are never overwritten.
Empty source layers are retained as zero counts; an entirely empty state is rejected.
Use a completed pack with --db on any conversion command; no network is used.
`

func runData(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		_, err := io.WriteString(stdout, dataUsage)
		return err
	}
	command := args[0]
	if command != "download" && command != "info" && command != "unlocated" {
		return fmt.Errorf("unknown data command %q", command)
	}
	fs := flag.NewFlagSet("data "+command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var path, state, serviceURL string
	var asJSON bool
	var workers int
	var timeout time.Duration
	fs.BoolVar(&asJSON, "json", false, "write pack metadata as JSON")
	if command == "download" {
		fs.StringVar(&path, "out", "", "new output pack path (default data/<state>.sqlite; NV: data/nevada.sqlite)")
		fs.StringVar(&state, "state", "NV", "state abbreviation: "+strings.Join(plss.SupportedDownloadStates(), ", "))
		fs.StringVar(&serviceURL, "service-url", "", "override BLM service URL")
		fs.IntVar(&workers, "workers", 4, "concurrent download requests (1–8)")
		fs.DurationVar(&timeout, "timeout", 60*time.Minute, "total download and build deadline")
	} else {
		fs.StringVar(&path, "db", "", "local SQLite pack to inspect")
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if command == "download" && path == "" {
		state = strings.ToUpper(strings.TrimSpace(state))
		if state == "" {
			state = "NV"
		}
		name := strings.ToLower(state)
		if state == "NV" {
			name = "nevada"
		}
		path = filepath.Join("data", name+".sqlite")
	}
	if path == "" {
		return errors.New("provide a pack path")
	}
	var metadata plss.PackMetadata
	var err error
	if command == "download" {
		if timeout <= 0 {
			return errors.New("--timeout must be positive")
		}
		if workers < 1 || workers > 8 {
			return errors.New("--workers must be between 1 and 8")
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		lastProgress := time.Time{}
		progress := func(p plss.DownloadProgress) {
			if p.Completed == 0 || p.Completed == p.Total || time.Since(lastProgress) >= 5*time.Second {
				fmt.Fprintf(stderr, "Layer %d: %d / %d features\n", p.Layer, p.Completed, p.Total)
				lastProgress = time.Now()
			}
		}
		metadata, err = plss.DownloadPack(ctx, path, plss.DownloadOptions{State: state, BaseURL: serviceURL, Workers: workers, Progress: progress})
	} else {
		var pack *plss.Pack
		pack, err = plss.OpenPack(path)
		if err == nil {
			if command == "unlocated" {
				defer pack.Close()
				records, err := pack.Unlocated(context.Background())
				if err != nil {
					return err
				}
				if asJSON {
					enc := json.NewEncoder(stdout)
					enc.SetIndent("", "  ")
					return enc.Encode(records)
				}
				if len(records) == 0 {
					_, err = fmt.Fprintln(stdout, "No source records without polygon geometry.")
					return err
				}
				for _, record := range records {
					id := ""
					for _, key := range []string{"SECDIVID", "SURVID", "FRSTDIVID", "PLSSID"} {
						if value, ok := record.Attributes[key].(string); ok && value != "" {
							id = value
							break
						}
					}
					if _, err = fmt.Fprintf(stdout, "%s [%s]: %s\n", id, record.Layer, record.Reason); err != nil {
						return err
					}
				}
				return nil
			}
			metadata, err = pack.Metadata()
			closeErr := pack.Close()
			if err == nil {
				err = closeErr
			}
		}
	}
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Path     string            `json:"path"`
			Bytes    int64             `json:"bytes"`
			Metadata plss.PackMetadata `json:"metadata"`
		}{path, info.Size(), metadata})
	}
	_, err = fmt.Fprintf(stdout, "Pack: %s\nState: %s\nCreated: %s\nSize: %.1f MiB\nSource: %s\n", path, metadata.State, metadata.CreatedAt, float64(info.Size())/(1024*1024), metadata.Source)
	if err != nil {
		return err
	}
	for _, layer := range []string{"township", "section", "intersected"} {
		if _, err = fmt.Fprintf(stdout, "%s: %d polygons; %d source records without geometry\n", layer, metadata.Counts[layer], metadata.UnlocatedCounts[layer]); err != nil {
			return err
		}
	}
	return nil
}
