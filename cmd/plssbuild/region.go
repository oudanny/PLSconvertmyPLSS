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

	"github.com/oudanny/PLSconvertmyPLSS/internal/atlas"
	"github.com/oudanny/PLSconvertmyPLSS/internal/atlasbuild"
	"github.com/oudanny/PLSconvertmyPLSS/internal/regionatlas"
	"github.com/oudanny/PLSconvertmyPLSS/plss"
)

type values []string

func (v *values) String() string     { return strings.Join(*v, ",") }
func (v *values) Set(s string) error { *v = append(*v, s); return nil }
func emit(out io.Writer, v any) error {
	e := json.NewEncoder(out)
	e.SetIndent("", "  ")
	return e.Encode(v)
}

func runBundle(args []string, out, log io.Writer) error {
	fs := flag.NewFlagSet("bundle", flag.ContinueOnError)
	fs.SetOutput(log)
	dest := fs.String("out", "", "new single-file region atlas")
	var files values
	fs.Var(&files, "atlas", "complete state atlas (repeat for each state)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *dest == "" || len(files) == 0 {
		return errors.New("provide --out and one or more --atlas files")
	}
	inputs := make([]regionatlas.ShardInput, len(files))
	for i, path := range files {
		inputs[i] = regionatlas.ShardInput{Path: path}
	}
	if err := os.MkdirAll(filepath.Dir(*dest), 0755); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	m, err := regionatlas.BuildContext(ctx, *dest, inputs)
	if err != nil {
		return err
	}
	return emit(out, m)
}

func runVerify(args []string, out, log io.Writer) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(log)
	path := fs.String("data", "", "state or region atlas")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *path == "" {
		return errors.New("provide --data")
	}
	a, err := regionatlas.OpenMapped(*path)
	if err != nil {
		return err
	}
	defer a.Close()
	if err = a.Verify(); err != nil {
		return err
	}
	return emit(out, map[string]any{"verified": true, "sources": a.Sources()})
}

func runWest(args []string, out, log io.Writer) error {
	return runRegionBuild("west", plss.WesternDownloadStates(), args, out, log)
}

func runNational(args []string, out, log io.Writer) error {
	return runRegionBuild("national", append(plss.SupportedDownloadStates(), "ZZ"), args, out, log)
}

func runRegionBuild(name string, supported []string, args []string, out, log io.Writer) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(log)
	defaultDir := "data/west"
	if name == "national" {
		defaultDir = "data/us"
	}
	dir := fs.String("work-dir", defaultDir, "downloaded source packs and completed state atlases")
	dest := fs.String("out", "", "new single-file region atlas")
	workers := fs.Int("workers", 4, "concurrent BLM requests (1-8); states run sequentially")
	vertices := fs.String("vertices", "packed", "exact vertex storage: packed, raw, or compressed (zlib)")
	var selected string
	if name == "national" {
		fs.StringVar(&selected, "states", "", "optional comma-separated subset for staged builds; ZZ preserves unassigned source IDs")
	}
	var overrides values
	fs.Var(&overrides, "source", "reuse complete source STATE=path.sqlite (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *dest == "" {
		return errors.New("provide --out with no positional arguments")
	}
	if *workers < 1 || *workers > 8 {
		return errors.New("--workers must be 1-8")
	}
	if *vertices != "raw" && *vertices != "compressed" && *vertices != "packed" {
		return errors.New("--vertices must be packed, raw, or compressed")
	}
	if _, err := os.Lstat(*dest); err == nil {
		return errors.New("output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	allowed := map[string]bool{}
	for _, s := range supported {
		allowed[s] = true
	}
	if selected != "" {
		var subset []string
		seen := map[string]bool{}
		for _, s := range strings.Split(selected, ",") {
			s = strings.ToUpper(strings.TrimSpace(s))
			if !allowed[s] || seen[s] {
				return fmt.Errorf("invalid or duplicate --states entry %q", s)
			}
			seen[s] = true
			subset = append(subset, s)
		}
		supported = subset
		allowed = seen
	}
	sources := map[string]string{}
	for _, s := range overrides {
		k, v, ok := strings.Cut(s, "=")
		k = strings.ToUpper(k)
		if !ok || !allowed[k] || v == "" {
			return fmt.Errorf("invalid --source %q; use STATE=path.sqlite", s)
		}
		if sources[k] != "" {
			return fmt.Errorf("duplicate source for %s", k)
		}
		sources[k] = v
	}
	if err := os.MkdirAll(*dir, 0755); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var inputs []regionatlas.ShardInput
	for _, state := range supported {
		if err := ctx.Err(); err != nil {
			return err
		}
		source := sources[state]
		if source == "" {
			source = filepath.Join(*dir, strings.ToLower(state)+".sqlite")
		}
		if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
			if sources[state] != "" {
				return fmt.Errorf("explicit source missing: %s", source)
			}
			fmt.Fprintf(log, "%s: downloading source\n", state)
			last := time.Time{}
			options := plss.DownloadOptions{State: state, Workers: *workers, Progress: func(p plss.DownloadProgress) {
				if p.Completed == 0 || p.Completed == p.Total || time.Since(last) > 5*time.Second {
					fmt.Fprintf(log, "%s layer%d: %d / %d\n", state, p.Layer, p.Completed, p.Total)
					last = time.Now()
				}
			}}
			if state == "ZZ" {
				options.State = ""
				_, err = plss.DownloadUnassignedPack(ctx, source, options)
			} else {
				_, err = plss.DownloadPack(ctx, source, options)
			}
			if err != nil {
				if state == "ZZ" && errors.Is(err, plss.ErrNoPublishedRecords) {
					fmt.Fprintln(log, "ZZ: source has no unassigned records; no shard needed")
					continue
				}
				return err
			}
		} else if err != nil {
			return err
		}
		p, err := plss.OpenPack(source)
		if err != nil {
			return fmt.Errorf("%s source: %w", state, err)
		}
		meta, err := p.Metadata()
		p.Close()
		if err != nil {
			return err
		}
		if meta.State != state {
			return fmt.Errorf("source %s contains %s, expected %s", source, meta.State, state)
		}
		path := filepath.Join(*dir, strings.ToLower(state)+"-"+*vertices+".atlas")
		if _, err = os.Stat(path); errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(log, "%s: building verified atlas\n", state)
			r, err := atlasbuild.Build(ctx, source, path, atlasbuild.Options{CompressedVertices: *vertices == "compressed", PackedVertices: *vertices == "packed", Progress: func(phase string, n, total uint32) { fmt.Fprintf(log, "%s %s: %d / %d\n", state, phase, n, total) }})
			if err != nil {
				return err
			}
			// Reports are reproducible build evidence; only successful builds
			// produce one. The atlas itself carries source hashes and counts.
			f, err := os.OpenFile(path+".json", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			err = emit(f, r)
			closeErr := f.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		} else if err != nil {
			return err
		} else {
			a, err := atlas.OpenMapped(path)
			if err != nil {
				return err
			}
			if a.Manifest.Source.State != state {
				a.Close()
				return fmt.Errorf("wrong state in %s", path)
			}
			if err = a.Verify(); err != nil {
				a.Close()
				return err
			}
			h, err := atlasbuild.SourceHash(ctx, source)
			if err != nil {
				a.Close()
				return err
			}
			matches := h == a.Manifest.SourceSHA256
			a.Close()
			if !matches {
				return fmt.Errorf("%s atlas belongs to a different source; use a new work directory", state)
			}
			fmt.Fprintf(log, "%s: verified and reused completed atlas\n", state)
		}
		inputs = append(inputs, regionatlas.ShardInput{Path: path, State: state})
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*dest), 0755); err != nil {
		return err
	}
	m, err := regionatlas.BuildContext(ctx, *dest, inputs)
	if err != nil {
		return err
	}
	return emit(out, m)
}
