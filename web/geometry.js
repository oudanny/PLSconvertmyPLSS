/* Display-only longitude transforms. Source atlas coordinates are never changed. */
(function (root, factory) {
  "use strict";
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  else root.MapGeometry = api;
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
  "use strict";

  function finite(value, name) {
    if (typeof value !== "number" || !Number.isFinite(value)) {
      throw new TypeError(name + " must be a finite number");
    }
    return value;
  }

  // Preserve already-normalized values without introducing rounding through
  // modulo arithmetic. Both dateline spellings normalize to -180, and -0 to 0.
  function normalizeLongitude(longitude) {
    finite(longitude, "longitude");
    if (longitude >= -180 && longitude < 180) return longitude === 0 ? 0 : longitude;
    const remainder = longitude % 360;
    const result = remainder >= 180 ? remainder - 360 : remainder < -180 ? remainder + 360 : remainder;
    return result === 0 ? 0 : result;
  }

  function position(value) {
    if (!Array.isArray(value) || value.length < 2) throw new TypeError("position needs longitude and latitude");
    finite(value[0], "longitude");
    finite(value[1], "latitude");
    if (value[1] < -90 || value[1] > 90) throw new RangeError("latitude is outside -90..90");
    return value.slice();
  }

  function unwrap(line) {
    if (!Array.isArray(line)) throw new TypeError("ring or line must be an array");
    const result = line.map(position);
    if (result.length === 0) return result;
    result[0][0] = normalizeLongitude(result[0][0]);
    for (let i = 1; i < result.length; i++) {
      const longitude = result[i][0];
      const previous = result[i - 1][0];
      // An exactly 180-degree edge is ambiguous; preserve its source direction.
      if (Math.abs(longitude - previous) > 180) {
        result[i][0] += 360 * Math.round((previous - longitude) / 360);
      }
    }
    return result;
  }

  function meanLongitude(line) {
    if (line.length === 0) return 0;
    let count = line.length;
    const first = line[0], last = line[count - 1];
    if (count > 1 && first[0] === last[0] && first[1] === last[1]) count--;
    let sum = 0;
    for (let i = 0; i < count; i++) sum += line[i][0];
    return sum / count;
  }

  function offset(line, amount) {
    // All callers pass freshly allocated coordinates, including extra ordinates.
    if (amount !== 0) for (const p of line) p[0] += amount;
    return line;
  }

  // Flat BLM rings do not encode an exterior/hole tree. Align every ring to one
  // common longitude plane, then move the whole group toward the map center.
  // Independently moving holes nearest the center can separate a hole from its
  // exterior when their means straddle the center's opposite meridian.
  function polygon(rings, centerLongitude) {
    finite(centerLongitude, "center longitude");
    if (!Array.isArray(rings)) throw new TypeError("rings must be an array");
    const result = rings.map(unwrap);
    const anchorRing = result.find((ring) => ring.length !== 0);
    if (!anchorRing) return result;
    const anchor = meanLongitude(anchorRing);
    const worldOffset = 360 * Math.round((centerLongitude - anchor) / 360);
    for (const ring of result) {
      if (ring.length === 0) continue;
      const alignment = 360 * Math.round((anchor - meanLongitude(ring)) / 360);
      offset(ring, alignment + worldOffset);
    }
    return result;
  }

  // Return NEW Leaflet [latitude, longitude] rings. Use fillRule: "evenodd";
  // source order/orientation, holes, disconnected rings, and closure are kept.
  // Extra source ordinates are deliberately omitted from display coordinates.
  function displayRings(rings, centerLongitude) {
    return polygon(rings, centerLongitude).map((ring) => ring.map((p) => [p[1], p[0]]));
  }

  function shiftPoint(coordinate, centerLongitude) {
    finite(centerLongitude, "center longitude");
    const result = position(coordinate);
    result[0] = normalizeLongitude(result[0]);
    result[0] += 360 * Math.round((centerLongitude - result[0]) / 360);
    return result;
  }

  function clone(value) {
    if (Array.isArray(value)) return value.map(clone);
    if (value !== null && typeof value === "object") {
      return Object.fromEntries(Object.entries(value).map(([key, child]) => [key, clone(child)]));
    }
    return value;
  }

  function metadata(value, omitted) {
    // Original bounding boxes become invalid after a display shift. Properties
    // (including a user property called bbox) are copied without reinterpretation.
    return Object.fromEntries(Object.entries(value)
      .filter(([key]) => key !== "bbox" && !omitted.includes(key))
      .map(([key, child]) => [key, clone(child)]));
  }

  function line(coordinates, centerLongitude) {
    const result = unwrap(coordinates);
    if (result.length) offset(result, 360 * Math.round((centerLongitude - meanLongitude(result)) / 360));
    return result;
  }

  // Return a deep display copy of GeoJSON. Polygon parts are independently
  // positioned, while each part and its holes share the same longitude plane.
  // The resulting longitudes may lie outside -180..180: do not export this copy
  // as the source geometry or use it for geographic calculations.
  function shiftGeoJSON(value, centerLongitude) {
    finite(centerLongitude, "center longitude");
    if (value === null) return null;
    if (!value || typeof value !== "object") throw new TypeError("GeoJSON must be an object");
    if (value.type === "FeatureCollection") {
      if (!Array.isArray(value.features)) throw new TypeError("features must be an array");
      return { ...metadata(value, ["features"]), features: value.features.map((f) => shiftGeoJSON(f, centerLongitude)) };
    }
    if (value.type === "Feature") {
      return { ...metadata(value, ["geometry"]), geometry: shiftGeoJSON(value.geometry, centerLongitude) };
    }
    if (value.type === "GeometryCollection") {
      if (!Array.isArray(value.geometries)) throw new TypeError("geometries must be an array");
      return { ...metadata(value, ["geometries"]), geometries: value.geometries.map((g) => shiftGeoJSON(g, centerLongitude)) };
    }
    let coordinates;
    switch (value.type) {
      case "Point": coordinates = shiftPoint(value.coordinates, centerLongitude); break;
      case "MultiPoint": coordinates = value.coordinates.map((p) => shiftPoint(p, centerLongitude)); break;
      case "LineString": coordinates = line(value.coordinates, centerLongitude); break;
      case "MultiLineString": coordinates = value.coordinates.map((p) => line(p, centerLongitude)); break;
      case "Polygon": coordinates = polygon(value.coordinates, centerLongitude); break;
      case "MultiPolygon": coordinates = value.coordinates.map((p) => polygon(p, centerLongitude)); break;
      default: throw new TypeError("unsupported GeoJSON type: " + value.type);
    }
    return { ...metadata(value, ["coordinates"]), coordinates };
  }

  return Object.freeze({ normalizeLongitude, displayRings, shiftGeoJSON, shiftPoint });
});
