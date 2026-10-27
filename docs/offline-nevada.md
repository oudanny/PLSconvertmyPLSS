# Offline Nevada PLSS

A Nevada pack lets the CLI and Go library resolve PLSS descriptions and coordinates without contacting BLM. Build the pack while online, then pass its path with `--db`.

## Build and inspect a pack

```sh
plssconvert data download --state NV --out data/nevada.sqlite
plssconvert data info --db data/nevada.sqlite
plssconvert data info --db data/nevada.sqlite --json
plssconvert data unlocated --db data/nevada.sqlite --json
```

The downloader currently supports `NV` only. It downloads township, section and intersected records from the [BLM national PLSS service](https://gis.blm.gov/natlplss/rest/services/Cadastral/BLM_Natl_PLSS_CadNSDI/MapServer), then creates a SQLite database with spatial and identifier indexes for usable polygons and a separate collection of source records without geometry. No BLM account, API key, C compiler or separate SQLite installation is needed.

For a slower connection or a longer build:

```sh
plssconvert data download --state NV --out data/nevada-2026-10-27.sqlite --workers 2 --timeout 120m --json
```

| Option | Meaning | Default |
| --- | --- | --- |
| `--state` | State to download; currently only `NV` | `NV` |
| `--out` | New destination file; it must not already exist | `data/nevada.sqlite` |
| `--workers` | Concurrent feature requests, from 1 through 8 | `4` |
| `--timeout` | Total download and database-build deadline | `60m` |
| `--service-url` | Compatible BLM ArcGIS MapServer override | Current BLM national endpoint |
| `--json` | Print completed pack metadata as JSON | Off |

Progress goes to standard error, leaving standard output suitable for JSON. Downloads retry transient request failures. Press Ctrl+C to cancel. A failed or canceled attempt does not publish a partial destination file, and retries start a new download; there is no resume mode.

The output is first built in a temporary sibling directory and published only after completeness checks. Publication refuses an existing destination atomically and requires a filesystem supporting hard links. Use a local filesystem such as NTFS if a removable drive reports that hard links are unsupported. Existing packs are never overwritten.

## Query without a network

For a coordinate in Reno:

```sh
plssconvert latlon --value '39.5296,-119.8138' --db data/nevada.sqlite
plssconvert latlon --value '39.5296,-119.8138' --db data/nevada.sqlite --json --geometry
plssconvert plss --state NV --meridian 21 --township 19N --range 19E --section 11 --aliquot SWNE --db data/nevada.sqlite
plssconvert plss --layer division --id NV210190N0190E0SN110ASWNE --db data/nevada.sqlite --json
```

On `latlon` or `utm`, `--db` implies `--plss`. The same flag selects the pack for structured `plss` queries or exact identifier queries. Use IDs and descriptions from your lookup results to request the corresponding published polygons.

The selected pack is the sole PLSS provider. The app does not contact the source URL stored in its metadata. Missing coverage, invalid files and unsupported database versions return errors without a network fallback. `--db` and `--service-url` are mutually exclusive.

Output includes the pack's creation time and source. JSON includes an `offline_pack` object alongside the usual conversion results. `data info` reports the pack's state, source, date, layer counts and file size without a network request. Metadata `counts` records usable polygons by layer; `unlocated_counts` records source records lacking geometry. Their sum accounts for the captured source records in each layer.

## Coverage and freshness

The download selects every record whose `PLSSID` starts with `NV` in these BLM layers:

| Layer | Source record count observed October 27, 2026 |
| --- | ---: |
| Township (`1`) | 3,380 |
| Section / first division (`2`) | 112,133 |
| Intersected subdivision (`3`) | 1,394,970 |

These are source-record totals for the Nevada identifier filter, including records with no geometry; they are not usable-polygon counts or promised counts for every future build. They were obtained using `returnCountOnly=true` and `where=PLSSID LIKE 'NV%'` on each layer's REST query endpoint. Inspect your actual pack with `data info`.

The downloader captures object-ID lists, retrieves each expected record and verifies completeness before publishing. This establishes complete retrieval of the captured IDs; it does not establish seamless coverage of every acre of Nevada. Source gaps, records without geometry, overlapping polygons, unsurveyed areas and differences in subdivision detail remain in the pack. Where geometry exists, the pack retains the coordinates returned by BLM in WGS 84 without simplification or a requested precision reduction. Geometry is not clipped to a new state outline.

A pack is a dated local copy, not a continuously updated dataset. BLM can change records while a download runs, and its service does not provide a transactional snapshot for this workflow. Missing expected records cause an error; attribute edits during the download can reflect different moments in time. The pack timestamp is a build timestamp, not a survey date. PLSS-to-coordinate results remain approximate representative points inside source polygons. See [source attribution and accuracy](data-sources.md).

To refresh, build a new file and choose it explicitly:

```sh
plssconvert data download --state NV --out data/nevada-next.sqlite
plssconvert data info --db data/nevada-next.sqlite
plssconvert latlon --value '39.5296,-119.8138' --db data/nevada-next.sqlite
```

Keep the previous file if you need repeatable results from that snapshot. Downloaded packs and SQLite sidecar files are ignored by Git; distribute large datasets separately from the source repository, retaining BLM attribution and pack metadata.

## Source records without geometry

Inspect records that were retrieved but cannot be placed on the map:

```sh
plssconvert data unlocated --db data/nevada.sqlite
plssconvert data unlocated --db data/nevada.sqlite --json
```

These commands read the pack locally. Text output lists identifiers and reasons; JSON also includes the saved source attributes. For example, the October 27, 2026 source included intersected-layer object ID `7362838`, division ID `NV210050N0280E0SN190V6`, labeled `RequiresSurvey`, without geometry even when queried in the service's native reference system.

The audit listing is limited to 10,000 records. If a pack exceeds that limit, the command returns an error rather than a truncated list; inspect its `unlocated` SQLite table directly for a full audit.

When a downloaded feature has no WGS 84 geometry, the downloader rechecks its native geometry. A confirmed absence is retained as an unlocated record. It does not trigger an invented polygon, a guessed coordinate or a dropped source ID. An invalid nonempty polygon or a failed recheck still fails the import.

Unlocated records never participate in coordinate-to-PLSS matching. An exact ID that exists only in this collection returns a no-geometry error (`plss.ErrNoGeometry`) when requested through `FindID`, explaining that the source record exists but cannot yield a representative coordinate. This differs from an ID absent from the pack. Counts of usable polygons and unlocated records are reported separately so a complete record download is not mistaken for complete spatial coverage.

## Query behavior and performance

Point queries first use SQLite's R-tree bounding-box index to find candidates, then test their actual polygon rings, including holes and boundaries. Bounding-box overlap alone is not a match. The provider searches intersected polygons, then sections, then townships and reports the available layer. It retains multiple matches at shared boundaries. Description and ID searches use indexed attributes. Available coordinates and geometry remain available for output; unlocated records have neither.

A spatial index avoids scanning every Nevada polygon on each point lookup. Download time, pack size and query latency depend on source complexity, disk and hardware. No fixed size or latency guarantee follows from the source-record counts above.

For a useful measurement, record the pack's metadata and file size, machine and Go version. Measure full download/build time separately from lookup time. For lookup timing, use a mix of urban and rural points, boundaries, holes and uncovered points; compare the first lookup after opening a pack with repeated lookups in a reused process. Report how many points were tested and their result counts. Timing a new CLI process for every point also includes process startup, database opening and output formatting; a library benchmark with one open `Pack` measures a different workload.

## Go library

`plss.Pack` and `plss.Client` both implement `plss.Provider`. Keep a pack open for repeated local queries and close it afterward:

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/oudanny/PLSconvertmyPLSS/plss"
)

func main() {
    pack, err := plss.OpenPack("data/nevada.sqlite")
    if err != nil {
        log.Fatal(err)
    }
    defer pack.Close()

    matches, err := pack.Lookup(context.Background(), 39.5296, -119.8138)
    if err != nil {
        log.Fatal(err)
    }
    for _, match := range matches {
        fmt.Println(match.ID(), match.Label, match.Representative)
    }
}
```

Use `pack.Find(ctx, description)` and `pack.FindID(ctx, kind, id)` for the other conversion directions. `pack.Unlocated(ctx)` returns the records without geometry for auditing, with the same 10,000-record limit as the CLI. `plss.DownloadPack(ctx, path, plss.DownloadOptions{State: "NV"})` builds a pack through the library; give downloads a context deadline and close any packs you open.
