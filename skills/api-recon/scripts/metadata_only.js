/* 원본 preload의 fetch/XHR 관찰 방식만 재사용한다. 브라우저 밖 송신·TLS 변경·응답/자격 값 저장은 없다. */
(function (root, build) {
  const api = build();
  if (typeof module === "object" && module.exports) module.exports = api;
  else Object.defineProperty(root, "__apiReconMetadata", { value: api, configurable: true });
})(typeof globalThis === "object" ? globalThis : this, function () {
  "use strict";
  const MAX_KEYS = 32;
  const MAX_RECORDS = 256;
  const MAX_BODY_BYTES = 64 * 1024;
  const keyPattern = /^[A-Za-z0-9_.\-\[\]]{1,64}$/;
  const methodPattern = /^(GET|HEAD|POST|PUT|PATCH|DELETE|OPTIONS)$/;
  const mediaPattern = /^[a-z0-9][a-z0-9!#$&^_.+-]{0,63}\/[a-z0-9][a-z0-9!#$&^_.+-]{0,63}$/;

  function names(values) {
    return [...new Set(values.filter(v => typeof v === "string" && keyPattern.test(v)))].sort().slice(0, MAX_KEYS);
  }
  function mediaType(value) {
    const type = typeof value === "string" ? value.split(";", 1)[0].trim().toLowerCase() : "";
    return mediaPattern.test(type) ? type : "";
  }
  function bodyNames(body, contentType) {
    try {
      if (!body) return [];
      const tag = Object.prototype.toString.call(body);
      if ((tag === "[object FormData]" || tag === "[object URLSearchParams]") && typeof body.keys === "function") {
        const keys = [];
        for (const key of body.keys()) { if (keys.length >= MAX_KEYS) break; keys.push(key); }
        return names(keys);
      }
      if (typeof body !== "string" || body.length > MAX_BODY_BYTES) return [];
      if (contentType === "application/x-www-form-urlencoded") return names([...new URLSearchParams(body).keys()]);
      if (contentType === "application/json" || contentType.endsWith("+json")) {
        const value = JSON.parse(body);
        if (value && !Array.isArray(value) && typeof value === "object") return names(Object.keys(value));
      }
    } catch (_) { /* 파싱 실패는 미확인 모양이다. 원문 오류·본문을 출력하지 않는다. */ }
    return [];
  }
  function httpOrigin(u) {
    return u.origin.replace(/^ws:/, "http:").replace(/^wss:/, "https:");
  }
  function requestMetadata(input, options = {}) {
    try {
      const raw = typeof input === "string" ? input : input && input.url;
      if (typeof raw !== "string" || raw.length > 2048 || /[\r\n\\]/.test(raw)) return null;
      const base = new URL(options.baseURL);
      const target = new URL(raw, base);
      if (!/^https?:$/.test(base.protocol) || !/^(https?|wss?):$/.test(target.protocol) || target.username || target.password || httpOrigin(target) !== base.origin) return null;
      const path = target.pathname || "/";
      if (path.length > 128 || /[%\\\u0000-\u001f\u007f]/.test(path) || path.includes("//")) return null;
      const method = String(options.method || input.method || "GET").toUpperCase();
      const transport = options.transport || "http";
      if (!methodPattern.test(method) || !["http", "sse", "websocket"].includes(transport)) return null;
      const contentType = mediaType(options.contentType);
      const result = { method, path, source: "api_recon_runtime", transport };
      const query = names([...target.searchParams.keys(), ...(options.queryKeys || [])]);
      const body = names([...(options.bodyKeys || []), ...bodyNames(options.body, contentType)]);
      if (query.length) result.query_keys = query;
      if (body.length) result.body_keys = body;
      if (contentType) result.content_type = contentType;
      return result;
    } catch (_) { return null; }
  }
  function contentTypeFrom(headers) {
    try {
      if (!headers) return "";
      if (typeof headers.get === "function") return mediaType(headers.get("content-type"));
      if (Array.isArray(headers)) {
        const entry = headers.find(([key]) => String(key).toLowerCase() === "content-type");
        return entry ? mediaType(entry[1]) : "";
      }
      const key = Object.keys(headers).find(name => name.toLowerCase() === "content-type");
      return key ? mediaType(headers[key]) : "";
    } catch (_) { return ""; }
  }
  function install(options = {}, page = globalThis) {
    if (page.__apiReconMetadataHooks) return page.__apiReconMetadataHooks;
    const baseURL = options.baseURL || page.location.href;
    const maxRecords = Number.isInteger(options.maxRecords) ? Math.max(1, Math.min(MAX_RECORDS, options.maxRecords)) : MAX_RECORDS;
    const records = new Map();
    const restore = [];
    let omitted = 0, invalid = 0, stopped = false;
    function record(input, shape) {
      if (stopped) return;
      const row = requestMetadata(input, { baseURL, ...shape });
      if (!row) { invalid++; return; }
      const key = `${row.method} ${row.path} ${row.transport}`;
      const old = records.get(key);
      if (old) {
        const query = names([...(old.query_keys || []), ...(row.query_keys || [])]);
        const body = names([...(old.body_keys || []), ...(row.body_keys || [])]);
        if (query.length) old.query_keys = query;
        if (body.length) old.body_keys = body;
        if (!old.content_type && row.content_type) old.content_type = row.content_type;
      } else if (records.size >= maxRecords) omitted++;
      else records.set(key, row);
    }
    function replace(object, property, wrapper) {
      const original = object[property];
      object[property] = wrapper(original);
      const installed = object[property];
      restore.push(() => { if (object[property] === installed) object[property] = original; });
    }
    if (typeof page.fetch === "function") replace(page, "fetch", original => function (input, init = {}) {
      try {
        const options = init || {};
        record(input, { method: options.method || input && input.method, body: options.body, contentType: contentTypeFrom(options.headers || input && input.headers) });
      } catch (_) { invalid++; }
      return Reflect.apply(original, this, arguments);
    });
    if (page.XMLHttpRequest && page.XMLHttpRequest.prototype) {
      const states = new WeakMap();
      const proto = page.XMLHttpRequest.prototype;
      replace(proto, "open", original => function (method, target) {
        states.set(this, { method, target, contentType: "" });
        return Reflect.apply(original, this, arguments);
      });
      replace(proto, "setRequestHeader", original => function (name, value) {
        const state = states.get(this);
        if (state && String(name).toLowerCase() === "content-type") state.contentType = mediaType(value);
        return Reflect.apply(original, this, arguments);
      });
      replace(proto, "send", original => function (body) {
        const state = states.get(this);
        if (state) record(state.target, { method: state.method, body, contentType: state.contentType });
        return Reflect.apply(original, this, arguments);
      });
    }
    for (const [property, transport] of [["WebSocket", "websocket"], ["EventSource", "sse"]]) {
      if (typeof page[property] === "function") replace(page, property, original => new Proxy(original, {
        construct(target, args, newTarget) {
          record(args[0], { method: "GET", transport });
          return Reflect.construct(target, args, newTarget);
        }
      }));
    }
    if (page.document && typeof page.document.addEventListener === "function") {
      const form = event => {
        const element = event.target;
        if (!element || String(element.tagName).toLowerCase() !== "form") return;
        const method = String(element.method || "GET").toUpperCase();
        const fields = names(Array.from(element.elements || []).filter(e => !e.disabled && e.name).map(e => e.name));
        record(element.action || baseURL, { method, queryKeys: method === "GET" ? fields : [], bodyKeys: method === "GET" ? [] : fields, contentType: method === "GET" ? "" : mediaType(element.enctype || "application/x-www-form-urlencoded") });
      };
      page.document.addEventListener("submit", form, true);
      restore.push(() => page.document.removeEventListener("submit", form, true));
    }
    const handle = {
      drain() {
        const discoveries = [...records.values()].map(row => JSON.parse(JSON.stringify(row)));
        records.clear();
        return { confidence: "inferred", discoveries, omitted, invalid };
      },
      stop() {
        stopped = true;
        for (const undo of restore.reverse()) undo();
        if (page.__apiReconMetadataHooks === handle) delete page.__apiReconMetadataHooks;
      }
    };
    Object.defineProperty(page, "__apiReconMetadataHooks", { value: handle, configurable: true });
    return handle;
  }
  return { requestMetadata, install };
});
