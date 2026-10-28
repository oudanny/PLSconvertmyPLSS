// Executes the actual Go WASM binary with bounded local-file reads in Node.
// Browser FileReaderSync is replaced only at the I/O boundary; all conversion,
// parsing, indexing, containment, and formatting still run in WebAssembly.
// node cmd/plsswasm/smoke.mjs bin/plss.wasm data/western-us.atlas GO_ROOT/lib/wasm/wasm_exec.js [native CLI]
import fs from 'node:fs';
import vm from 'node:vm';
import assert from 'node:assert/strict';
import { webcrypto } from 'node:crypto';
import { execFileSync } from 'node:child_process';

const [wasmPath, atlasPath, runtimePath, nativeCLI] = process.argv.slice(2);
const rangeMode=process.env.PLSS_SMOKE_RANGE==='1';
if (!runtimePath) throw new Error('Provide WASM, atlas, and matching wasm_exec.js paths.');
globalThis.crypto ??= webcrypto;
vm.runInThisContext(fs.readFileSync(runtimePath, 'utf8'), { filename: runtimePath });
const go = new Go();
const { instance } = await WebAssembly.instantiate(fs.readFileSync(wasmPath), go.importObject);
const running = go.run(instance);
await new Promise(resolve => setTimeout(resolve, 0));
assert.equal(globalThis.plssEngineReady, true);

let calls = 0, maxRead = 0;
globalThis.FileReaderSync = class {
  readAsArrayBuffer(slice) {
    const count = slice.end - slice.start;
    assert(count <= 1024 * 1024, 'bridge attempted a whole-file read');
    calls++; maxRead = Math.max(maxRead, count);
    const buffer = Buffer.alloc(count);
    const got = fs.readSync(slice.fd, buffer, 0, count, slice.start);
    assert.equal(got, count);
    return buffer.buffer.slice(buffer.byteOffset, buffer.byteOffset + got);
  }
};
function invoke(command, request = {}, file) {
  return JSON.parse(globalThis.plssEngine(command, JSON.stringify(request), file));
}
function success(command, request = {}, file) {
  const response = invoke(command, request, file);
  assert.equal(response.ok, true, response.error);
  return response;
}
assert.equal(success('convert', { value: '39.5296,-119.8138' }).result.coordinates.utm.zone, 11);
assert.equal(invoke('lookup', {}).ok, false);
assert.equal(invoke('find', { id: 'missing' }).ok, false);
assert.equal(invoke('lookup', { latitude: 92, longitude: 0 }).ok, false);
const fd = fs.openSync(atlasPath, 'r');
const file = { name: atlasPath, size: fs.fstatSync(fd).size, slice(start, end) { return { fd, start, end }; } };
globalThis.location={href:'http://localhost/worker.js',origin:'http://localhost'};
globalThis.XMLHttpRequest=class{
  open(method,url,async){assert.equal(method,'GET');assert.equal(url,'http://localhost/atlas');assert.equal(async,false);}
  setRequestHeader(name,value){assert.equal(name,'Range');const m=/^bytes=(\d+)-(\d+)$/.exec(value);assert(m);this.start=Number(m[1]);this.end=Number(m[2]);}
  set responseType(value){assert.equal(value,'arraybuffer');}
  send(){const n=this.end-this.start+1;assert(n<=8*1024*1024);calls++;maxRead=Math.max(maxRead,n);const b=Buffer.alloc(n);assert.equal(fs.readSync(fd,b,0,n,this.start),n);this.response=b.buffer.slice(b.byteOffset,b.byteOffset+n);this.status=206;}
  getResponseHeader(name){assert.equal(name,'Content-Range');return `bytes ${this.start}-${this.end}/${file.size}`;}
};
const opened = rangeMode?success('openURL',{url:'/atlas',size:file.size,name:atlasPath}):success('open', {}, file);
assert(opened.result.features > 0);
assert(opened.stats.bytes_read < file.size / 5, 'opening read an unreasonable fraction of data');
const points = process.env.PLSS_SMOKE_POINTS?JSON.parse(fs.readFileSync(process.env.PLSS_SMOKE_POINTS,'utf8')):[
  ['NV', 39.5296, -119.8138], ['CO', 40.015, -105.2705],
  ['CA', 34.0522, -118.2437], ['WA', 47.6062, -122.3321],
  ['UT', 40.7608, -111.891], ['AZ', 33.4484, -112.074],
  ['OR', 45.5152, -122.6784], ['ID', 43.615, -116.2023],
  ['MT', 46.5891, -112.0391], ['WY', 41.14, -104.8202], ['NM', 35.0844, -106.6504],
];
const summaries = [];
let reverseChecks = 0, recordsWithoutLayerID = 0;
for (const [state, latitude, longitude] of points) {
  const response = success('lookup', { latitude, longitude });
  const { matches, coordinates } = response.result;
  assert(matches.some(m => m.state === state), `${state}: expected coverage`);
  assert.equal(coordinates.latitude, latitude);
  assert.equal(coordinates.longitude, longitude);
  for (const match of matches) {
    assert(match.geometry.rings.length > 0);
    assert.equal(typeof match.description, 'string');
    assert(match.description.length > 0, 'published matches need a readable description or explicit fallback');
    let kind = match.layer === 'intersected' ? 'division' : match.layer;
    if (kind === 'division' && !match.identifiers.division && match.identifiers.survey) kind = 'survey';
    const id = match.identifiers[kind];
    if (!id) { recordsWithoutLayerID++; continue; }
    const reverse = success('find', { kind, id });
    assert(reverse.result.matches.some(m => m.state === match.state && m.feature_id === match.feature_id), `${match.state} ${id}: reverse record mismatch`);
    reverseChecks++;
  }
  if (nativeCLI) {
    const native = JSON.parse(execFileSync(nativeCLI, ['latlon', '--value', `${latitude},${longitude}`, '--data', atlasPath, '--json'], { encoding: 'utf8' }));
    const keys = items => items.map(h => `${h.state}:${h.layer}:${h.feature_id}:${h.id}`).sort();
    assert.deepEqual(keys(matches), keys(native.results[0].plss));
    assert.equal(coordinates.dd, native.results[0].coordinates.dd);
    assert.equal(coordinates.dms, native.results[0].coordinates.dms);
  }
  summaries.push({ state, matches: matches.length, elapsed_ms: response.stats.elapsed_ms, heap: response.stats.go_heap_bytes, stats:response.stats });
}
const gap = success('lookup', { latitude: 40.7128, longitude: -74.0060 });
assert.equal(gap.result.no_match, true);
assert.equal(gap.result.coordinates.latitude, 40.7128);
assert.equal(invoke('find', { id: 'NV210050N0280E0SN190V6' }).ok, false);
const readableReno = success('lookup', { latitude: 39.5296, longitude: -119.8138 }).result.matches.find(m => m.id === 'NV210190N0190E0SN110ASWNE');
assert(readableReno, 'Reno subdivision missing');
assert.equal(readableReno.description, 'SW¼ of NE¼ · Sec. 11 · T. 19 N · R. 19 E');
assert.equal(readableReno.description_detail, 'Mount Diablo Meridian · Nevada');
const nearbyBefore = success('info').stats;
const nearbyTimes = [];
for (let i = 0; i < 200; i++) {
  const nearby = success('lookup', { latitude: 39.5296 + (i % 10) * 1e-6, longitude: -119.8138 + Math.floor(i / 10) * 1e-6 });
  assert(nearby.result.matches.some(m => m.state === 'NV'));
  nearbyTimes.push(nearby.stats.elapsed_ms);
}
nearbyTimes.sort((a, b) => a - b);
const nearbyAfter = success('info').stats;
const nearby = { queries: nearbyTimes.length, description: '200 nearby Reno points after warming that state; includes exact containment, geometry, representative, and coordinate formats', median_ms: nearbyTimes[99], p95_ms: nearbyTimes[189], additional_reads: nearbyAfter.read_calls-nearbyBefore.read_calls, additional_bytes:nearbyAfter.bytes_read-nearbyBefore.bytes_read };
assert.equal(nearby.additional_reads,0,'nearby queries thrashed state caches');
// This point falls in five overlapping state rectangles in the national
// snapshot (CA, ID, NV, OR, ZZ), even though only exact polygons can match.
const overlappingPoint = {latitude:41.99218450317383,longitude:-117.24165262451172};
success('lookup',overlappingPoint);
const overlapBefore=success('info').stats;
const overlapTimes=[];
for(let i=0;i<200;i++)overlapTimes.push(success('lookup',overlappingPoint).stats.elapsed_ms);
overlapTimes.sort((a,b)=>a-b);
const overlapAfter=success('info').stats;
const overlapping={queries:200,point:overlappingPoint,median_ms:overlapTimes[99],p95_ms:overlapTimes[189],additional_reads:overlapAfter.read_calls-overlapBefore.read_calls};
assert.equal(overlapping.additional_reads,0,'overlapping state rectangles thrashed caches');
const before = success('info');
const invalid = { ...file, slice() { throw new Error('file permission revoked'); } };
assert.equal(invoke('open', {}, invalid).ok, false);
assert.equal(success('info').result.features, before.result.features, 'failed replacement lost current atlas');
assert.equal(invoke('openURL',{url:'https://example.invalid/atlas',size:file.size}).ok,false);
assert(maxRead <= (rangeMode?8:1) * 1024 * 1024);
assert(before.stats.cache_bytes <= 8 * 1024 * 1024);
const closed = success('close');
assert.equal(closed.result.loaded, false);
fs.closeSync(fd);
console.log(JSON.stringify({ mode: rangeMode?'Node WASM; synchronous XHR simulated with fs.readSync (no HTTP round-trip latency)':'Node WASM; FileReaderSync adapter uses fs.readSync', opened: opened.result, opening_stats: opened.stats, queries: summaries, nearby_points: nearby, overlapping_states:overlapping, reverse_checks:reverseChecks, records_without_layer_identifier:recordsWithoutLayerID, final_stats: before.stats, actual_bridge_calls: calls, actual_bridge_max_read: maxRead, native_comparison: !!nativeCLI }, null, 2));
process.exit(0);
