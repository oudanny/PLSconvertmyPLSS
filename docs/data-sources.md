# PLSS data source

Sources inspected October 27, 2026.

## Public BLM dataset

The app queries **BLM National PLSS Public Land Survey System Polygons**, published by the Bureau of Land Management:

- [Dataset landing page and available downloads](https://gbp-blm-egis.hub.arcgis.com/datasets/283939812bc34c11bad695a1c8152faf/about)
- [BLM dataset metadata](https://www.arcgis.com/sharing/rest/content/items/283939812bc34c11bad695a1c8152faf/info/metadata/metadata.xml?format=default&output=html)
- [Current ArcGIS REST service](https://gis.blm.gov/natlplss/rest/services/Cadastral/BLM_Natl_PLSS_CadNSDI/MapServer)

The metadata describes a national compilation of rectangular and nonrectangular surveys, derived from cadastral survey records and supporting geographic information. It identifies the data as public domain, requests BLM attribution, and says the publication may lag the state offices' data. Coverage, detail and positional accuracy vary. The polygons are not legal documents or land surveys. Consult original survey records for legal boundary work. [Source: BLM metadata](https://www.arcgis.com/sharing/rest/content/items/283939812bc34c11bad695a1c8152faf/info/metadata/metadata.xml?format=default&output=html).

Suggested attribution for exported results: **“PLSS data: Bureau of Land Management, National PLSS Public Land Survey System Polygons; accessed YYYY-MM-DD. Representative points calculated by PLSconvertmyPLSS.”** Describe any additional changes you make to the data and do not imply BLM endorsement.

BLM's [GIS Data page](https://www.blm.gov/services/geospatial/GISData) directs users to its Hub for downloads. Formats vary by item and can include File Geodatabase, Shapefile and GeoJSON. This repository does not bundle a national dataset, cache one locally or currently ingest offline downloads; it requests the relevant published features on demand.

The small regression fixture [`plss/testdata/blm_boulder.json`](../plss/testdata/blm_boulder.json) contains a BLM response fetched October 27, 2026 for latitude `40.015`, longitude `-105.2705`. Its feature attributes and polygon rings are preserved; unrelated response field metadata was omitted. Credit: Bureau of Land Management. The fixture is test input, not an offline coverage dataset.

## Endpoint migration

The default service is:

```text
https://gis.blm.gov/natlplss/rest/services/Cadastral/BLM_Natl_PLSS_CadNSDI/MapServer
```

BLM announced this URL on October 14, 2026. The previous `/arcgis/rest/services/` endpoint is scheduled to remain active until February 1, 2027. This app uses the new endpoint. See [BLM's migration announcement](https://www.blm.gov/announcement/blm-changes-links-national-public-land-survey-system-gis-dataset).

## Layers and coordinate reference systems

The current [service directory](https://gis.blm.gov/natlplss/rest/services/Cadastral/BLM_Natl_PLSS_CadNSDI/MapServer) exposes these relevant layers:

| Layer | Contents | Identifier |
| --- | --- | --- |
| `1` | PLSS Township | `PLSSID` |
| `2` | PLSS Section / first division | `FRSTDIVID` |
| `3` | PLSS Intersected / available subdivisions | `SECDIVID`, `SURVID`, and parent IDs |

The service's native map reference is Web Mercator (EPSG:3857). The app requests longitude/latitude output in WGS 84 (EPSG:4326), leaving the service to project its geometry. The application's UTM calculations also use WGS 84. It does not perform an epoch-aware or survey-grade transformation from source datums such as NAD83. Output decimal precision does not establish positional accuracy.

A township/range/section description needs its state and principal meridian to avoid commonplace ambiguity. Duplicate, fractional and special surveys can still require an exact published identifier. Subdivision labels and coverage are whatever the source supplies; the app never fills missing divisions by assuming every section is a regular one-mile square. An aliquot such as `SWSE` means southwest quarter of the southeast quarter, from smallest to largest. A larger quarter request can return multiple published component polygons; these are not dissolved into a new boundary.

## Meaning of a result

For coordinate-to-PLSS, the app asks which published polygons intersect a point. A point on a shared boundary may have multiple matches; gaps and uncovered places cannot be assigned a PLSS description reliably.

For PLSS-to-coordinate, the app returns the matching feature and a calculated point within that feature. A description may match more than one published polygon. Representative points are convenient for display or subsequent UTM conversion, but they are not surveyed monuments, parcel corners or evidence of ownership. Request JSON geometry to retain the actual published boundary alongside the point.

The default service is public and needs no API key. PLSS requests require internet access and depend on BLM availability. A timeout, HTTP failure or no-match result should be handled as a failed lookup, not an authoritative statement about whether surveyed land exists.
