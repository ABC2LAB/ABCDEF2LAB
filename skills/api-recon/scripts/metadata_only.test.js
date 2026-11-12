const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const metadata = require("./metadata_only.js");
const baseURL = "https://owned.test/read/";

test("extracts key names without query, body, header or response values", () => {
  const result = metadata.requestMetadata("/read/api?token=QUERY_SECRET&page=7", { baseURL, method: "POST", contentType: "application/json; charset=utf-8", body: JSON.stringify({ password: "PASSWORD_SECRET", token: "BODY_SECRET", filter: { id: "NESTED_SECRET" } }) });
  assert.deepEqual(result, { method: "POST", path: "/read/api", source: "api_recon_runtime", transport: "http", query_keys: ["page", "token"], body_keys: ["filter", "password", "token"], content_type: "application/json" });
  assert.doesNotMatch(JSON.stringify(result), /SECRET/);
  assert.equal(metadata.requestMetadata("https://other.test/read", { baseURL }), null);
  assert.equal(metadata.requestMetadata("https://user:PASS_SECRET@owned.test/read", { baseURL }), null);
  assert.equal(metadata.requestMetadata("/read/%2fhidden", { baseURL }), null);
  assert.equal(metadata.requestMetadata("/read", { baseURL, method: "BREW" }), null);
});

test("handles form names, bounds and invalid body without reading values", () => {
  const form = new URLSearchParams("password=FORM_SECRET&q=SEARCH_SECRET&q=OTHER_SECRET");
  assert.deepEqual(metadata.requestMetadata("/read/search", { baseURL, method: "POST", contentType: "application/x-www-form-urlencoded", body: form }).body_keys, ["password", "q"]);
  const body = Object.fromEntries(Array.from({ length: 40 }, (_, i) => [`k${i}`, "VALUE_SECRET"]));
  assert.equal(metadata.requestMetadata("/read/keys", { baseURL, contentType: "application/json", body: JSON.stringify(body) }).body_keys.length, 32);
  assert.equal(metadata.requestMetadata("/read/keys", { baseURL, contentType: "application/json", body: "INVALID_SECRET" }).body_keys, undefined);
});

function fakePage() {
  const calls = [], listeners = new Map();
  class XHR {
    open() { calls.push("xhr_open"); }
    setRequestHeader() { calls.push("xhr_header"); }
    send() { calls.push("xhr_send"); }
  }
  class WS { constructor(url) { this.url = url; calls.push("ws_open"); } }
  class SSE { constructor(url) { this.url = url; calls.push("sse_open"); } }
  const page = { location: { href: baseURL }, fetch() { calls.push("fetch"); return Promise.resolve({ secret: "RESPONSE_SECRET" }); }, XMLHttpRequest: XHR, WebSocket: WS, EventSource: SSE, document: { addEventListener(name, handler) { listeners.set(name, handler); }, removeEventListener(name) { listeners.delete(name); } } };
  return { page, calls, listeners };
}

test("page hooks forward native calls unchanged and keep HTTP/SSE/WS inferred metadata", async () => {
  const { page, calls, listeners } = fakePage();
  const nativeFetch = page.fetch, nativeWS = page.WebSocket, nativeOpen = page.XMLHttpRequest.prototype.open;
  const handle = metadata.install({}, page);
  assert.equal(metadata.install({}, page), handle);
  await page.fetch("/read/api?sort=QUERY_SECRET", { method: "POST", headers: { Authorization: "HEADER_SECRET", "content-type": "application/json" }, body: '{"q":"BODY_SECRET"}' });
  const xhr = new page.XMLHttpRequest();
  xhr.open("POST", "/read/xhr"); xhr.setRequestHeader("Cookie", "COOKIE_SECRET"); xhr.setRequestHeader("Content-Type", "application/x-www-form-urlencoded"); xhr.send("q=FORM_SECRET");
  new page.WebSocket("wss://owned.test/read/ws?token=WS_SECRET");
  new page.EventSource("/read/events?topic=SSE_SECRET");
  listeners.get("submit")({ target: { tagName: "FORM", method: "GET", action: "/read/form", elements: [{ name: "search", value: "INPUT_SECRET" }] } });
  const result = handle.drain();
  assert.equal(result.confidence, "inferred"); assert.equal(result.discoveries.length, 5);
  assert.deepEqual(result.discoveries.map(r => r.transport), ["http", "http", "websocket", "sse", "http"]);
  assert.deepEqual(result.discoveries[4].query_keys, ["search"]);
  assert.doesNotMatch(JSON.stringify(result), /SECRET|authorization|cookie|response/i);
  assert.deepEqual(calls, ["fetch", "xhr_open", "xhr_header", "xhr_header", "xhr_send", "ws_open", "sse_open"]);
  assert.equal(handle.drain().discoveries.length, 0);
  handle.stop();
  assert.equal(page.fetch, nativeFetch); assert.equal(page.WebSocket, nativeWS); assert.equal(page.XMLHttpRequest.prototype.open, nativeOpen); assert.equal(listeners.size, 0);
});

test("deduplicates shapes, reports cap loss and never opens a connection by itself", async () => {
  const { page, calls } = fakePage();
  const handle = metadata.install({ maxRecords: 1 }, page);
  assert.equal(calls.length, 0);
  await page.fetch("/read/a?x=ONE_SECRET"); await page.fetch("/read/a?y=TWO_SECRET"); await page.fetch("/read/b");
  const result = handle.drain();
  assert.equal(result.omitted, 1); assert.equal(result.discoveries.length, 1); assert.deepEqual(result.discoveries[0].query_keys, ["x", "y"]);
  handle.stop();
});

test("the standalone source loads in a page evaluate context without require or a network client", () => {
  const context = { URL, URLSearchParams, globalThis: null };
  context.globalThis = context;
  vm.createContext(context);
  vm.runInContext(fs.readFileSync(require.resolve("./metadata_only.js"), "utf8"), context);
  assert.equal(typeof context.__apiReconMetadata.install, "function");
  assert.equal(context.__apiReconMetadata.requestMetadata("/read/page", { baseURL }).source, "api_recon_runtime");
});
