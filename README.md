# PLSconvertmyPLSS

A Go command-line app and reusable library for converting latitude/longitude and UTM coordinates, and finding the corresponding Public Land Survey System (PLSS) land descriptions through public BLM data.

- Latitude/longitude: decimal degrees (DD), degrees and decimal minutes (DDM), and degrees/minutes/seconds (DMS).
- UTM: WGS 84, northern and southern hemispheres, with Norway and Svalbard zone exceptions.
- PLSS: township/range/section, available aliquot subdivisions and government lots, or exact BLM identifiers.
- Plain-text or JSON output, with optional polygon geometry.
- Coordinate conversions run offline. PLSS queries can use BLM's public REST service, a state SQLite pack, or an experimental compact western-US atlas; no API key is required.

PLSS describes an **area**, so converting PLSS to latitude/longitude returns a representative point inside a published polygon. It is not a surveyed corner or a unique coordinate for the land description. Coordinate-to-PLSS results depend on published coverage and may include multiple matches on boundaries.

## Install

Requires Go 1.26 or newer. SQLite support uses the pure-Go `modernc.org/sqlite` driver; no C compiler, CGO or separate SQLite installation is required.

```sh
go install github.com/oudanny/PLSconvertmyPLSS/cmd/plssconvert@latest
```

Or run from source:

```sh
git clone https://github.com/oudanny/PLSconvertmyPLSS.git
cd PLSconvertmyPLSS
go run ./cmd/plssconvert --help
```

After installation, ensure your Go binary directory (`go env GOPATH`, then `bin`) is on `PATH`. Windows builds produce `plssconvert.exe`.

## Use

Convert latitude/longitude to DD, DDM, DMS, compact ISO 6709 and UTM:

```sh
plssconvert latlon --value '40.015,-105.2705'
plssconvert latlon --lat '40°0′54″N' --lon '105°16′13.8″W'
```

Add a live PLSS lookup:

```sh
plssconvert latlon --value '40.015,-105.2705' --plss --json
```

Convert a UTM coordinate, optionally looking up PLSS:

```sh
plssconvert utm --value '13N 476915.239 4429457.113'
plssconvert utm --zone 13 --hemisphere N --easting 476915.239 --northing 4429457.113 --plss
```

Find a section or a published aliquot subdivision:

```sh
plssconvert plss --state CO --meridian 06 --township 1N --range 70W --section 30
plssconvert plss --state CO --meridian 06 --township 1N --range 70W --section 30 --aliquot SWSE --json --geometry
```

The principal meridian is required because township and range repeat across meridians. `06` is the Sixth Principal Meridian. Supply `--lot` instead of `--aliquot` for a government lot. Availability of these subdivisions varies by location.

Use a BLM identifier to select a specific published feature:

```sh
plssconvert plss --layer division --id CO060010N0700W0SN300ASWSE --json --geometry
```

For exact IDs, `--layer` accepts `township` (`PLSSID`), `section` (`FRSTDIVID`), `division` / `intersected` (`SECDIVID`), or `survey` (`SURVID`). Omit `--section` in a structured query to request a township. `--township-fraction` and `--range-fraction` accept the source's one-digit BLM fraction codes, defaulting to `0`.

Run `plssconvert <command> --help` for all flags. Place flags after the command. Quote coordinate strings containing spaces or symbols. `--json` produces machine-readable output; `--geometry` includes rings with coordinates ordered **longitude, latitude** and requires `--json`. `--timeout` controls the online request deadline, and `--service-url` can point to a compatible ArcGIS service.

## Offline state PLSS

Download a state's published townships, sections and intersected subdivisions once, then use the resulting SQLite pack without a network connection. Supported states are AZ, CA, CO, ID, MT, NM, NV, OR, UT, WA and WY:

```sh
plssconvert data download --state NV --out data/nevada.sqlite
plssconvert data info --db data/nevada.sqlite
plssconvert data unlocated --db data/nevada.sqlite --json
plssconvert latlon --value '39.5296,-119.8138' --db data/nevada.sqlite --json
```

`--db` also works with `utm` and `plss`. On coordinate commands it implies a PLSS lookup, so `--plss` is optional. It selects local data exclusively: a missing match or invalid pack returns an error without trying the online service. `--db` and `--service-url` cannot be combined.

Packs retain published attributes and available geometry, use a spatial index to narrow candidates, and test the actual polygons for containment. Source records without geometry are preserved separately for inspection with `data unlocated`; they cannot produce a spatial match or representative coordinate. Each pack records its source, creation time, usable-polygon counts and unlocated-record counts. It is a dated copy of the available source records; it does not fill gaps in BLM's coverage. Downloading requires internet access, can take substantial time, and never overwrites an existing pack. See [the offline Nevada guide](docs/offline-nevada.md) for download options, refreshes, coverage and performance measurement.

## Coordinate conventions

Latitude comes before longitude in coordinate pairs. Geographic coordinates use WGS 84; latitude is between −90° and +90°, and longitude between −180° and +180°. UTM applies between 80°S and 84°N. Points outside that band can still be displayed as latitude/longitude.

Accepted latitude/longitude input includes:

| Form | Example |
| --- | --- |
| Signed decimal degrees | `37.7749,-122.4194` |
| Hemisphere prefix or suffix | `N37.7749,W122.4194` |
| Degrees and decimal minutes | `37°46.494′N,122°25.164′W` |
| Degrees, minutes and seconds | `37°46′29.64″N,122°25′9.84″W` |
| Colon-separated DMS | `37:46:29.64N,122:25:9.84W` |
| Words | `37 degrees 46 minutes 29.64 seconds north,122 degrees 25 minutes 9.84 seconds west` |
| Compact DDM / DMS | `3746.494N,12225.164W` / `374629.64N,1222509.84W` |
| Compact signed ISO 6709 | `+37.7749-122.4194/`, `+3746.494-12225.164/`, `+374629.64-1222509.84/` |

ASCII quotes, whitespace and lowercase hemisphere letters also work. A comma or semicolon separates a pair; whitespace is accepted when the split is unambiguous. Conflicting signs/hemispheres and out-of-range minutes or seconds are rejected. Compact ISO 6709 supports DD, DDM and DMS without altitude or CRS suffixes; the trailing slash is optional.

In UTM text, `13N` and `13S` mean **hemispheres**. For a latitude band, use an explicit form such as `13 band T 476915.239 4429457.113` or the `--band T` flag. JSON output reports the hemisphere and latitude band separately. This distinction matters because band `S` is north of the equator. MGRS, UPS, altitude and arbitrary datum conversion are outside the current scope.

## Go libraries

`coord` contains coordinate parsing, formatting and WGS 84 / UTM conversion. `plss` contains the HTTP client and polygon lookup logic. Both can be imported independently of the CLI:

```go
package main

import (
    "fmt"
    "log"

    "github.com/oudanny/PLSconvertmyPLSS/coord"
)

func main() {
    point, err := coord.ParsePair("40.015,-105.2705")
    if err != nil {
        log.Fatal(err)
    }
    utm, err := coord.ToUTM(point)
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(coord.FormatDMS(point))
    fmt.Println(utm.String())
}
```

The formatters are `FormatDD`, `FormatDDM`, `FormatDMS` and `FormatISO6709`. To query PLSS from a library, pass a context with a deadline:

```go
client := plss.NewClient()
ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
defer cancel()
matches, err := client.Lookup(ctx, 40.015, -105.2705)
if err != nil {
    return err
}
for _, match := range matches {
    fmt.Println(match.ID(), match.Label, match.Representative)
}
```

The snippet belongs inside a function returning `error` and uses imports `context`, `fmt`, `time`, and `github.com/oudanny/PLSconvertmyPLSS/plss`. `Find(ctx, plss.Description{...})` accepts a structured description; `FindID(ctx, "division", id)` accepts an exact identifier. Each match retains the original BLM attributes and polygon, the representative point, the source service and the resolution layer. `Lookup` tries intersected polygons, then sections, then townships, and reports the layer it found. Preserve all returned matches.

For offline queries, `plss.OpenPack(path)` returns a local provider with the same `Lookup`, `Find` and `FindID` methods. Close it when finished. Both the HTTP client and local pack implement `plss.Provider`; see the [offline library example](docs/offline-nevada.md#go-library).

The public `offline` library and `plssatlas` CLI read compact state or multi-state files. A western bundle contains the **11 contiguous western states in one file**, preserving original polygon coordinates and published identifiers. State readers open on demand. See [western atlas builds and measurements](docs/western-atlas.md) for exact scope, sizes, verification and remaining limitations. The stable `plssconvert` command continues to use SQLite packs and supports structured township/range descriptions; the experimental compact reader currently accepts exact published IDs.

```sh
go install github.com/oudanny/PLSconvertmyPLSS/cmd/plssbuild@latest
go install github.com/oudanny/PLSconvertmyPLSS/cmd/plssatlas@latest
plssbuild west --work-dir data/west --out data/western-us.atlas
plssatlas latlon --value '40.015,-105.2705' --data data/western-us.atlas
```

The builder downloads missing sources, verifies every geometry and identifier,
and reuses completed state files on a later run. An interrupted state download
restarts that state. Build on a machine with network access and temporary disk
space; the finished converter needs only its executable and the atlas file.
To reuse an existing Nevada source, add `--source NV=data/nevada.sqlite`.

For repeated lookups, keep one `offline.Reader` open:

```go
reader, err := offline.Open("data/western-us.atlas")
if err != nil { return err }
defer reader.Close()
hits, err := reader.Lookup(context.Background(), 40.015, -105.2705)
if err != nil { return err }
for _, hit := range hits { fmt.Println(hit.State, hit.ID) }
```

This snippet uses `github.com/oudanny/PLSconvertmyPLSS/offline`, `context`, and
`fmt` inside a function returning `error`. `FindID` and `Representative` perform
the reverse conversion without loading archival attributes. The compact format
is experimental; retain source packs and use a matching builder/reader version.
The [original Nevada report](docs/compact-atlas.md) records the earlier design.

## Data source and accuracy

PLSS data comes from the [BLM National PLSS Public Land Survey System Polygons dataset](https://gbp-blm-egis.hub.arcgis.com/datasets/283939812bc34c11bad695a1c8152faf/about). BLM identifies these data as public domain and requests attribution. Its published GIS data have varying accuracy and completeness and are not legal land surveys. See [data sources and limitations](docs/data-sources.md) for the service URL, provenance, available layers and download information.

This project uses BLM data but is not affiliated with or endorsed by BLM. It does not reconstruct missing aliquots by dividing an idealized square or infer legal ownership from PLSS boundaries.

## Development

```sh
go test ./...
go vet ./...
go build ./cmd/...
```

Unit tests use local HTTP fixtures and small generated packs, so normal test runs do not depend on the live BLM service or a full Nevada download. GitHub Actions runs tests, vet and builds on Linux, Windows and macOS, including the minimum supported Go version on Linux. The full downloaded data packs are excluded from Git.
