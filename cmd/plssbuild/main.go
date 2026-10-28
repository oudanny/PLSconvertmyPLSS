// Command plssbuild prepares verified offline atlases. Conversion itself is in
// plssatlas; this separate executable is only needed on a data-building machine.
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
	"time"

	"github.com/oudanny/PLSconvertmyPLSS/internal/atlasbuild"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
func run(args []string, out, log io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "help" {
		_, err := fmt.Fprintln(out, "plssbuild state --db SOURCE.sqlite --out NEW.atlas [--vertices packed|raw|compressed]\nplssbuild bundle --out REGION.atlas --atlas AZ.atlas --atlas CA.atlas ...\nplssbuild west --work-dir data/west --out data/western-us.atlas [--source NV=data/nevada.sqlite]\nplssbuild national --work-dir data/us --out data/us.atlas [--states AK,AL,...]\nplssbuild verify --data REGION.atlas\nCreates and verifies exact conversion files; never overwrites. Regional commands download missing sources and reuse complete state files. National includes 30 published PLSS states plus an unassigned-source shard; it does not invent coverage for states without PLSS data.")
		return err
	}
	if args[0] == "bundle" {
		return runBundle(args[1:], out, log)
	}
	if args[0] == "west" {
		return runWest(args[1:], out, log)
	}
	if args[0] == "national" {
		return runNational(args[1:], out, log)
	}
	if args[0] == "verify" {
		return runVerify(args[1:], out, log)
	}
	if args[0] != "state" {
		return fmt.Errorf("unknown build command %q", args[0])
	}
	fs := flag.NewFlagSet("state", flag.ContinueOnError)
	fs.SetOutput(log)
	db := fs.String("db", "", "complete source SQLite pack")
	dest := fs.String("out", "", "new output atlas")
	vertices := fs.String("vertices", "packed", "exact vertex storage: packed, raw, or compressed (zlib)")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || *db == "" || *dest == "" {
		return errors.New("provide --db and --out with no positional arguments")
	}
	if *vertices != "raw" && *vertices != "compressed" && *vertices != "packed" {
		return errors.New("--vertices must be packed, raw, or compressed")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	begin := time.Now()
	r, err := atlasbuild.Build(ctx, *db, *dest, atlasbuild.Options{CompressedVertices: *vertices == "compressed", PackedVertices: *vertices == "packed", Progress: func(phase string, done, total uint32) {
		fmt.Fprintf(log, "%s: %d / %d (%.1fs)\n", phase, done, total, time.Since(begin).Seconds())
	}})
	if err != nil {
		return err
	}
	e := json.NewEncoder(out)
	e.SetIndent("", "  ")
	return e.Encode(r)
}
