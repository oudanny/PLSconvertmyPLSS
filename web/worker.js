/* The Go runtime and all atlas I/O live off the map's main UI thread. */
"use strict";
let ready = false;
let failed = false;
async function start() {
  try {
    importScripts("runtime/wasm_exec.js");
    const go = new Go();
    const response = await fetch("runtime/plss.wasm");
    if (!response.ok) throw new Error("The WebAssembly engine is missing. Rebuild the GUI launcher.");
    const bytes = await response.arrayBuffer();
    const instance = await WebAssembly.instantiate(bytes, go.importObject);
    const running = go.run(instance.instance);
    running.catch(error => {
      failed = true;
      postMessage({ type: "fatal", error: error.message || String(error) });
    });
    // Go registers its bridge synchronously before waiting for worker requests.
    if (typeof globalThis.plssEngine !== "function") throw new Error("The Go engine did not initialize its bridge.");
    ready = true;
    postMessage({ type: "ready" });
  } catch (error) {
    failed = true;
    postMessage({ type: "fatal", error: error.message || String(error) });
  }
}
self.onmessage = event => {
  const { id, command, payload, file } = event.data;
  if (!ready || failed) {
    postMessage({ id, ok: false, error: "The Go engine is not ready." });
    return;
  }
  const begin = performance.now();
  try {
    const response = JSON.parse(globalThis.plssEngine(command, JSON.stringify(payload || {}), file));
    postMessage({ ...response, id, elapsed_ms: performance.now() - begin });
  } catch (error) {
    postMessage({ id, ok: false, error: error.message || String(error) });
  }
};
start();
