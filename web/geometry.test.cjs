"use strict";
const assert = require("node:assert/strict");
const test = require("node:test");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { normalizeLongitude, displayRings, shiftGeoJSON, shiftPoint } = require("./geometry.js");

function freeze(value) {
  if (value && typeof value === "object") {
    Object.freeze(value);
    for (const child of Object.values(value)) freeze(child);
  }
  return value;
}

function inside(point, rings) {
  let hit = false;
  const [y, x] = point;
  for (const ring of rings) {
    for (let i = 0, j = ring.length - 1; i < ring.length; j = i++) {
      const [yi, xi] = ring[i], [yj, xj] = ring[j];
      if ((yi > y) !== (yj > y) && x < (xj - xi) * (y - yi) / (yj - yi) + xi) hit = !hit;
    }
  }
  return hit;
}

test("normalization handles both datelines, multiple world copies, and signed zero", () => {
  for (const [input, output] of [[180, -180], [-180, -180], [540, -180], [-540, -180],
    [181, -179], [-181, 179], [720, 0], [-720, 0], [-0, 0], [-119.81479999664111, -119.81479999664111]]) {
    assert.equal(normalizeLongitude(input), output);
  }
  for (const value of [NaN, Infinity, -Infinity, "180", undefined]) assert.throws(() => normalizeLongitude(value));
});

test("ordinary rings remain precise and all return values are independent", () => {
  const source = freeze([[[ -119.81479999664111, 39.52903379032697, 14, 22 ], [-119, 39], [-119, 40], [-119.81479999664111, 39.52903379032697, 14, 22]]]);
  const result = displayRings(source, -120);
  assert.deepEqual(result[0][0], [39.52903379032697, -119.81479999664111]);
  result[0][0][0] = 0;
  assert.equal(source[0][0][1], 39.52903379032697);
});

test("dateline crossings stay compact with a hole whose first point is on the other side", () => {
  const outer = [[179, 50], [-179, 50], [-179, 54], [179, 54], [179, 50]];
  const hole = [[-179.5, 51], [179.5, 51], [179.5, 53], [-179.5, 53], [-179.5, 51]];
  const source = freeze([outer, hole]);
  for (const center of [-180, 180, -540, 540, 0]) {
    const result = displayRings(source, center);
    const xs = result.flat().map((p) => p[1]);
    assert.equal(Math.max(...xs) - Math.min(...xs), 2);
    const mid = (Math.max(...xs) + Math.min(...xs)) / 2;
    assert.equal(inside([50.5, mid], result), true);
    assert.equal(inside([52, mid], result), false);
    for (const ring of result) assert.deepEqual(ring[0], ring.at(-1));
  }
});

test("opposite-meridian means cannot split a hole from its exterior", () => {
  const result = displayRings([
    [[178, 0], [-179, 0], [-179, 4], [178, 4], [178, 0]],
    [[-179.2, 1], [179.6, 1], [179.6, 3], [-179.2, 3], [-179.2, 1]],
  ], 0);
  assert.equal(inside([2, 179.8], result), false);
  assert.equal(inside([0.5, 179.8], result), true);
  assert.equal(Math.max(...result.flat().map((p) => p[1])) - Math.min(...result.flat().map((p) => p[1])), 3);
});

test("disconnected BLM rings keep order and work with even-odd filling", () => {
  const source = freeze([
    [[178, 0], [179, 0], [179, 1], [178, 1], [178, 0]],
    [[-179, 2], [-178, 2], [-178, 3], [-179, 3], [-179, 2]],
  ]);
  const result = displayRings(source, 180);
  assert.equal(inside([0.5, 178.5], result), true);
  assert.equal(inside([2.5, 181.5], result), true);
  assert.equal(inside([1.5, 180], result), false);
  assert.deepEqual(displayRings([...source].reverse(), 180).reverse(), result);
});

test("mixed +180/-180 closure, reversed orientation, and empty rings are preserved", () => {
  const rings = [[], [[180, 0], [-179, 0], [-179, 1], [-180, 0]]];
  const result = displayRings(rings, -180);
  assert.deepEqual(result[0], []);
  assert.deepEqual(result[1][0], result[1].at(-1));
  assert.deepEqual(displayRings([rings[1].slice().reverse()], -180)[0].reverse(), result[1]);
  assert.deepEqual(displayRings([], 0), []);
  assert.deepEqual(displayRings([[]], 0), [[]]);
});

test("GeoJSON parts shift independently while holes, extra ordinates, and properties survive", () => {
  const source = freeze({type: "FeatureCollection", bbox: [-180, 0, 180, 10], features: [{
    type: "Feature", id: "alaska", bbox: [-180, 0, 180, 10], properties: {name: "Alaska", nested: {value: [1]}, bbox: [1]},
    geometry: {type: "MultiPolygon", bbox: [-180, 0, 180, 10], coordinates: [
      [[[179, 0, 100, 5], [-179, 0, 200, 6], [-179, 4, 300, 7], [179, 4, 400, 8], [179, 0, 100, 5]],
       [[-179.5, 1, 12], [179.5, 1, 13], [179.5, 3, 14], [-179.5, 3, 15], [-179.5, 1, 12]]],
      [[[-157, 5], [-156, 5], [-156, 6], [-157, 5]]],
    ]},
  }]});
  const result = shiftGeoJSON(source, -160);
  const f = result.features[0];
  assert.equal("bbox" in result, false);
  assert.equal("bbox" in f, false);
  assert.equal("bbox" in f.geometry, false);
  assert.deepEqual(f.properties.bbox, [1]);
  assert.deepEqual(f.geometry.coordinates[0][0][0], [-181, 0, 100, 5]);
  assert.deepEqual(f.geometry.coordinates[0][1][0], [-179.5, 1, 12]);
  assert.deepEqual(f.geometry.coordinates[1][0][0], [-157, 5]);
  f.properties.nested.value[0] = 9;
  f.geometry.coordinates[0][0][0][2] = 999;
  assert.equal(source.features[0].properties.nested.value[0], 1);
  assert.equal(source.features[0].geometry.coordinates[0][0][0][2], 100);
});

test("points, lines, null feature geometry, and geometry collections are supported", () => {
  assert.deepEqual(shiftPoint([179, 52, 8], -170), [-181, 52, 8]);
  assert.deepEqual(shiftGeoJSON({type: "LineString", coordinates: [[179, 0], [-179, 1]]}, -180).coordinates, [[-181, 0], [-179, 1]]);
  assert.deepEqual(shiftGeoJSON({type: "Feature", properties: {}, geometry: null}, 0), {type: "Feature", properties: {}, geometry: null});
  assert.deepEqual(shiftGeoJSON({type: "GeometryCollection", geometries: [
    {type: "Point", coordinates: [179, 52]},
    {type: "MultiPoint", coordinates: [[180, 50], [-179, 51]]},
    {type: "MultiLineString", coordinates: [[[179, 0], [-179, 1]]]},
  ]}, -180).geometries.map((g) => g.coordinates), [[-181, 52], [[-180, 50], [-179, 51]], [[[-181, 0], [-179, 1]]]]);
});

test("bad XY input is rejected instead of reaching Leaflet as NaN", () => {
  for (const rings of [null, [[[]]], [[[NaN, 0]]], [[[0, Infinity]]], [[[0, 91]]]]) assert.throws(() => displayRings(rings, 0));
  assert.throws(() => displayRings([], NaN));
  assert.throws(() => shiftGeoJSON({type: "Unknown"}, 0));
});

test("browser UMD global does not require Node or a DOM", () => {
  const context = vm.createContext({});
  vm.runInContext(fs.readFileSync(path.join(__dirname, "geometry.js"), "utf8"), context);
  assert.equal(context.MapGeometry.normalizeLongitude(181), -179);
});

test("bundled real geography retains all source vertices and remains unchanged", () => {
  for (const filename of ["land.geojson", "states.geojson", "lakes.geojson", "cities.geojson"]) {
    const source = freeze(JSON.parse(fs.readFileSync(path.join(__dirname, "data", filename), "utf8")));
    const copy = shiftGeoJSON(source, -160);
    assert.equal(copy.features.length, source.features.length);
    const check = (a, b) => {
      assert.equal(a.length, b.length);
      if (a.length === 0) return;
      if (typeof a[0] === "number") {
        assert.ok(Math.abs((b[0] - a[0]) / 360 - Math.round((b[0] - a[0]) / 360)) < 1e-12);
        assert.deepEqual(b.slice(1), a.slice(1));
      } else {
        for (let i = 0; i < a.length; i++) check(a[i], b[i]);
      }
    };
    for (let i = 0; i < source.features.length; i++) check(source.features[i].geometry.coordinates, copy.features[i].geometry.coordinates);
  }
});
