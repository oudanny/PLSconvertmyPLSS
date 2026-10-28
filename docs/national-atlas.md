# National PLSS atlas

The national builder includes all published records in BLM's township, section,
and intersected polygon layers. The source currently contains 30 recognized
state prefixes: AK, AL, AR, AZ, CA, CO, FL, IA, ID, IL, IN, KS, LA, MI, MN, MO,
MS, MT, ND, NE, NM, NV, OH, OK, OR, SD, UT, WA, WI, and WY.

This is national published PLSS coverage, not a promise of a polygon at every
U.S. coordinate. Other states use different land-description systems, and the
published PLSS states themselves contain gaps. Coordinate notation and UTM
conversion remain independent of PLSS coverage.

## Completed national file

`data/us.atlas` is **1,362,497,879 bytes (1.362 GB / 1.269 GiB)**. It contains
**30,226,208 polygons and 27 unlocated source records** across 30 real states
and the unassigned partition. This fits within a single 2 GiB download asset.
The source SQLite packs total 26,894,188,544 bytes. The compact file is 94.93%
smaller, while ancillary attributes for located records remain in those source
packs. It stores approximately 45.08 bytes per polygon, including all indexes.

Every decoded polygon and all four cadastral identifier fields were verified
against the source records during the state builds. All per-layer record totals
also match the independent national source-prefix inventory. Build reports,
source hashes, geometry verification hashes, and component sizes are preserved
in [`national-builds.json`](../internal/westbench/national-builds.json).

The national SHA-256 is
`d403e367eda3b9cdb524929d91179181bd4ab16ca2ca439108de5fd21f637dce`.

| Partition | Polygons | Unlocated | Atlas MB |
| --- | ---: | ---: | ---: |
| Alaska | 1,242,091 | 0 | 103.05 |
| Alabama | 187,971 | 0 | 7.66 |
| Arkansas | 591,196 | 1 | 29.63 |
| Arizona | 1,542,351 | 0 | 62.05 |
| California | 2,281,307 | 18 | 104.36 |
| Colorado | 2,067,634 | 0 | 88.93 |
| Florida | 206,130 | 0 | 10.14 |
| Iowa | 948,937 | 0 | 63.80 |
| Idaho | 1,249,811 | 1 | 47.07 |
| Illinois | 152,196 | 0 | 6.61 |
| Indiana | 105,434 | 0 | 4.09 |
| Kansas | 183,709 | 0 | 8.13 |
| Louisiana | 154,021 | 0 | 7.58 |
| Michigan | 982,242 | 0 | 41.13 |
| Minnesota | 1,385,084 | 0 | 61.95 |
| Missouri | 232,667 | 0 | 9.29 |
| Mississippi | 761,437 | 1 | 29.84 |
| Montana | 2,425,196 | 2 | 99.06 |
| North Dakota | 1,205,217 | 0 | 48.08 |
| Nebraska | 204,526 | 0 | 7.49 |
| New Mexico | 1,860,015 | 1 | 70.73 |
| Nevada | 1,510,482 | 1 | 58.68 |
| Ohio | 103,363 | 0 | 8.41 |
| Oklahoma | 1,212,392 | 0 | 48.16 |
| Oregon | 1,579,039 | 1 | 65.02 |
| South Dakota | 1,336,571 | 0 | 53.68 |
| Utah | 1,315,533 | 0 | 51.23 |
| Washington | 631,123 | 0 | 32.50 |
| Wisconsin | 937,742 | 0 | 45.47 |
| Wyoming | 1,564,124 | 0 | 62.26 |
| Unassigned source records (ZZ) | 66,667 | 1 | 26.35 |

The national directory adds 65,536 bytes to these state files. Component totals
are 869,628,662 bytes of geometry, 320,401,984 bytes of spatial indexes,
170,370,081 bytes of identifiers, and 2,097,152 bytes of state/region headers.

## Native lookup validation and performance

The national atlas passed **42,296 spatial queries and 8,602 reverse-ID checks**
against the original SQLite candidates and full source polygons. The spatial
checks include 31,000 spread points plus source vertices, edge midpoints, and
near-boundary points. Comparisons preserve duplicate records and use the same
finest-layer rule across all partitions. Of the spatial checks, 36,796 had
matches. These are sampled query checks, in addition to the exhaustive geometry
and identifier checks at build time.

Measured on Windows amd64, Intel Core i7-1195G7, Go 1.27.3, with warm OS file
pages and a persistent native Go reader:

| Workload | Queries | Median | p95 |
| --- | ---: | ---: | ---: |
| First pass, points scattered across all partitions | 31,000 | 32.4 us | 82.5 us |
| Repeat the scattered workload | 31,000 | 5.7 us | 36.4 us |
| Repeated representative points across all partitions | 10,000 | 2.4 us | 5.1 us |

Opening measured 46.2 ms. The first-pass test starts with empty prepared-polygon
and geometry caches after verifying the file and initializing its indexes. It
does not measure cold disk access. Its allocation pass uses a separate fresh
reader. The query mixture includes Alaska and the broad residual bounds, so
these medians should not be compared directly with the earlier western-only
workload as a speedup. These are native reader measurements, not browser/WASM
timings. Raw results and methodology are in
[`national-results.json`](../internal/westbench/national-results.json).

```sh
go run ./internal/westbench --data data/us.atlas --source-dir data/west --nv-db data/nevada.sqlite --points-per-state 1000
```

## Preserve records with missing identifiers

State packs select `PLSSID LIKE 'XX%'`. A separate **ZZ** partition selects the
exact complement of all 30 state filters, including null PLSSID values. ZZ means
**unassigned source records**; it is not a state, and its features are not moved
or assigned invented identifiers. Original attributes remain in the source
SQLite pack. A polygon without a published identifier can still match a point.
The residual contains 60,552 polygons with all four cadastral identifier fields
empty. For example, `61.2181,-149.9003` in Anchorage matches residual atlas
record 64500 but has no published PLSS identifier. That local record number is
not a BLM OBJECTID and does not create a legal land description.

The source-prefix inventory on October 28, 2026 UTC counted **30,226,235 source
records**: 85,984 townships, 2,773,890 sections, and 27,366,361 intersected
records. Of those, 66,668 have absent or malformed state prefixes and belong in
the residual partition. These are source record counts, including any records
without geometry, rather than assertions that all records can be located.
See [`national-source-inventory.json`](../internal/westbench/national-source-inventory.json).

ArcGIS returned `objectIds:null` for the empty residual township layer. The
downloader accepts that case only after a second query with the identical filter
explicitly returns `count:0`; missing, malformed, truncated, or nonzero count
responses remain errors. It never infers completeness merely from a null ID
list.

## Build and resume

```sh
go build -trimpath -ldflags '-s -w' -o plssbuild ./cmd/plssbuild
plssbuild national --work-dir data/us --out data/us.atlas
```

On Windows, use `plssbuild.exe`. The national command downloads missing state
sources, builds compact state atlases, verifies every geometry and identifier,
and combines the completed files. Finished state files are reused only after
their checksums and source hashes are verified. Interrupted state downloads
restart that state. Existing output files are never overwritten.

To reuse the previous western-state source packs and atlases:

```sh
plssbuild national --work-dir data/west --source NV=data/nevada.sqlite --out data/us.atlas
```

The original `plssbuild west` command retains its eleven-state scope. A national
build can also be staged with `--states AK,AL,...`; a subset file contains only
those partitions. Include `ZZ` to request the unassigned partition. The default
national build includes every partition, omitting ZZ only if all three source
layers explicitly report no unassigned records.

```sh
plssbuild verify --data data/us.atlas
plssatlas latlon --value '52.965892378387224,173.16897992989695' --data data/us.atlas
```

The second example is a real published polygon near Attu, Alaska, on the
positive-longitude side of the Aleutians. The output remains WGS 84; this point
uses UTM zone 59 north. Results preserve all matches at the finest available
layer, including matches from the unassigned partition.

## Data provenance and limits

The reader uses the same exact compact geometry and identifiers as the
[western atlas](western-atlas.md). Source attributes beyond the conversion
fields remain in the SQLite source packs. Records without geometry remain in
the unlocated audit and cannot produce coordinates. Latitude/longitude-to-PLSS
lookup reports coverage gaps explicitly; it never invents a regular survey
grid to fill them.

The captured source is BLM's [national PLSS service](https://gis.blm.gov/natlplss/rest/services/Cadastral/BLM_Natl_PLSS_CadNSDI/MapServer).
It changes over time, so separately captured states are not a transactional
snapshot from one instant. Reused western captures and new national captures
retain their own timestamps and hashes. See [source attribution and accuracy
limits](data-sources.md) for BLM's public-domain metadata and survey limitations.
