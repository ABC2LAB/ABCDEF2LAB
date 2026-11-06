# api-recon — reference manual

Grep recipes, `config.json` template, and troubleshooting. All greps run against the `js/` directory. When a bundle is on a single line, first `js-beautify` or `sed 's/}/}\n/g'`; usually a raw grep with a context window is enough.

## Script notes

Every file under `scripts/` is a [reference template] that must be adjusted for the target site before running. Typical change points:

| Script | Common items to adjust |
|---|---|
| `harvest_static.py` | endpoint regex, webpack/Vite manifest parsing, micro-frontend publicPath, retry/concurrency |
| `runtime_harvest.js` | neutralize field names and success value, stub match rules and body structure, routes source, WS recording, `waitUntil`/`routeTimeout`/`proxy` |
| `preload.js` | `loginPathRe`, L1 stubs, `neutralize.fields`, `apiPattern`, whether to enable L3, `recordDetail`, `observe.*`, `neutralizeVueRouter` |
| `spider_mpa.py` | `--exclude` destructive links, cookie, depth/max, same-origin filter |
| `extract_route_map.py` | `routeMap` / `routeLink` regex, KEY naming pattern |
| `build_perm_tree.py` | `userRouteAuth` parsing, `ROOTS`/`PREFIX_PARENT` hierarchy heuristics, stub outer field names |
| `config.json` | the single entry point for all of the above site-specific parameters |

Put the adjusted files in the task working directory (e.g. `recon/`), and note the specific changes relative to the reference scripts in the report.

---

## A. Reversing the three gates

### A1. Render gate — "How does it decide you are logged in?"

```bash
grep -rhoaE '.{0,40}(isLogin|isAuthenticated|loggedIn|hasLogin|requireAuth)\b.{0,80}' js | head
grep -rhoaE 'function (getUser|getToken|getAuth)[0-9]?\([^)]*\)\{.{0,200}' js | head
grep -rhoaE '(localStorage|sessionStorage)\.getItem\("[^"]+"\)' js | sort -u
grep -rhoaE '(Cookies?|cookie)\.(get|load)\("[^"]+"\)' js | sort -u
grep -rhoaE '\batob\(|JSON\.parse\(|jwt|decode' js | head
```

Find the chain `isLogin = f(getUser())` -> `getUser = decode(storage.read(KEY))`, and determine the [storage key], the [container] (Cookie vs localStorage), and the [encoding]:

| Encoding | How to forge in config |
|---|---|
| Plaintext string / `"1"` / token | `"value": "anything-truthy"` |
| `JSON.parse(x)` | `"value": "json:{\"id\":1,\"username\":\"admin\"}"` |
| `JSON.parse(atob(x))` | `"value": "b64json:{\"id\":1,\"username\":\"admin\"}"` |
| JWT | unsigned/`alg:none` JWT, or sign with a key found in the bundle |
| Encrypted (SM2/AES/RSA) | find a hard-coded key; the render gate only needs a decodable blob to forge; otherwise fall back to static |

-> write into `cookies` / `localStorage`.

### A2. Interceptor gate — "What triggers the jump to /login?"

```bash
grep -rhoaE '.{0,60}(interceptors\.response|axios|request\.use).{0,120}' js | head
grep -rhoaE '.{0,40}(response_code|errcode|errno|\bcode\b|\bret\b|\bstatus\b)\s*[=!]==?\s*[\-0-9]{1,4}.{0,60}' js | head -20
grep -rhoaE '.{0,40}(未登录|请重新登录|登录已过期|unauthorized|登录失效|授权|token.{0,10}invalid).{0,40}' js | head
grep -rhoaE '.{0,30}(location\.href|router\.(push|replace)|navigate)\([^)]*login[^)]*\)' js | head
```

Determine: the [field name], the [success value] (usually `0` or `200`), and the [failure value that triggers the redirect]. Verify with a junk session:

```bash
curl -sk -X POST -H 'Cookie: <fakekey>=junk' https://target/api/<protected> -d '{}' -H 'Content-Type: application/json'
```

-> write into `neutralize.fields` + `neutralize.success`.

### A3. Content gate — "Where do menus/permissions come from?"

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl|resource|nav)[^"]*"' js | sort -u
grep -rhoaE '.{0,30}(menus|permissions|menuList|routeList|authList|role_permissions)\b.{0,120}' js | head
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|hasPermission|checkAuth' js | head
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head
```

[Two-layer data] (common in enterprise back offices):

| API | Typical payload | Consumer |
|---|---|---|
| `.../role_permissions` | `{ permissions: string[], role_type }` | route guard, button-level ACL |
| `.../permissions/all` | `tree[{ code, position, children }]` | sidebar menu rendering |
| `userRouteAuth` in the bundle | `{ CODE: { url, name? } }` | code -> frontend path |
| `routeMap` in the bundle | `{ KEY: { name, link } }` | alias resolution (webpack `o.DASHBOARD`) |

Read the consumer code to confirm: how `getResultTree(tree, permissions)` filters, and which field `v-if` / `hasAuth(code)` checks.

[Manual forge] (small sites): build a permissive payload -> `stubs`.

[Full permission tree reconstruction] (large sites, sidebar/submodules still blank): see [section I].

---

## B. config.json template

```json
{
  "baseUrl": "https://target/",
  "runtimeMode": "both",
  "chromium": "/usr/bin/chromium",

  "cookies": [
    { "name": "auth", "value": "b64json:{\"id\":1,\"username\":\"admin\",\"role\":\"admin\",\"func\":{},\"permissions\":[\"*\"]}" }
  ],
  "localStorage": { "token": "faketoken", "isLogin": "1" },

  "neutralize": {
    "fields": ["response_code", "code", "errno", "ret", "status"],
    "success": 0,
    "flags": { "success": true, "message": "ok" }
  },
  "forward": true,
  "loginUrlPattern": "/login",
  "apiPattern": "/api/|/rest/|/graphql",

  "mockTier": "L1+L2",
  "recordDetail": true,
  "observe": {
    "storageReads": false,
    "cookieReads": false,
    "xhrHeaders": true
  },
  "neutralizeVueRouter": true,
  "stubs": [
    {
      "match": "permissions/all|/menu|role_permissions",
      "body": {
        "response_code": 0, "code": 0,
        "data": {
          "permissions": ["*"],
          "menus": [
            { "name": "dashboard", "path": "/dashboard", "show": true, "children": [] },
            { "name": "alert", "path": "/alert", "show": true, "children": [] }
          ]
        }
      }
    }
  ],

  "explore": {
    "clickTabs": true,
    "clickTables": true,
    "pushStateFallback": true,
    "maxMenuItems": 50
  },

  "routes": ["/dashboard", "/alert", "/asset", "/device", "/report", "/config", "/system"],
  "waitMs": 1500, "perRouteMs": 900, "headless": true,
  "waitUntil": "domcontentloaded",
  "routeTimeout": 12000,
  "proxy": "",

  "captureResponses": true, "recordWs": true, "respMax": 600
}
```

Field notes:
- `runtimeMode`: `depth` (Puppeteer), `coverage` (browser MCP), `both`
- `cookies[].value` prefix: `b64json:` -> base64(JSON); `json:` -> raw JSON; no prefix -> literal
- `forward: true` forwards the real request and rewrites the code field; `false` fully offline stub
- `mockTier`: the preload tiers enabled in coverage mode, e.g. `L1+L2`, `L1+L2+L3`
- `routes` come from `routes.txt`; after forging the menu, the harness auto-appends `<a href>`
- `captureResponses` / `recordWs` only take effect in depth mode
- `waitUntil`: for a large SPA use `domcontentloaded`, avoid `networkidle2` hangs
- `routeTimeout`: single-route `page.goto` timeout (ms)
- `proxy`: Puppeteer `--proxy-server`; can also set `HTTP_PROXY` / `HTTPS_PROXY`

### B1. Dual-stub template (role_permissions + permissions/all)

```json
"stubs": [
  {
    "match": "role_permissions",
    "body": {
      "response_code": 0,
      "data": {
        "permissions": ["MONITOR", "MONITOR_ALERT", "THREAT", "ASSETS_RISK"],
        "role_type": "SUPER_ADMIN"
      }
    }
  },
  {
    "match": "permissions/all",
    "body": {
      "response_code": 0,
      "data": [
        {
          "code": "MONITOR",
          "position": 1,
          "children": [
            { "code": "MONITOR_ALERT", "position": 1, "children": [] }
          ]
        }
      ]
    }
  }
]
```

The outer field names (`response_code` / `code` / `data`) must match the A2 interceptor gate; `permissions` must cover every leaf code in the tree.

---

## C. coverage mode: preload config

Edit the `CONFIG` object at the top of `scripts/preload.js`, or replace it before CDP injection:

```javascript
const CONFIG = {
  loginPathRe: /\/(login|signin)(\/|$|\?)/i,
  mockTier: 'L1+L2',
  forward: true,
  recordDetail: true,
  extractUrlsFromResponse: true,
  neutralizeVueRouter: true,
  observe: { storageReads: false, cookieReads: false, xhrHeaders: true },
  neutralize: { fields: ['response_code', 'code'], success: 0 },
  stubs: [ /* same as config.json stubs */ ],
  apiPattern: /\/(api|apis|v\d+|dev|internal|graphql)\//i,
};
```

Verify: `window.__API_RECON_PRELOAD__ === true` and the pathname is stable.

Export the recording:

```javascript
JSON.stringify({
  apis: [...window.__API_RECON_LOG__],
  detail: window.__API_RECON_DETAIL__,
  routes: [...(window.__API_RECON_ROUTES__ || [])],
  observe: window.__API_RECON_OBSERVE__,
}, null, 2)
```

---

## D. preload / runtime hook capabilities

The browser hook capabilities built into preload (coverage) and runtime_harvest (depth), and their coverage:

| Hook capability | Value for API discovery | Coverage |
|---|---|---|
| Hook fetch / XHR.open | record request URL/method | OK `recordDetail` + `__API_RECON_LOG__` |
| Hook XHR.setRequestHeader | discover headers like Authorization | OK `observe.xhrHeaders` |
| Hook localStorage/cookie reads | confirm session key names | Warning: optional `observe.storageReads/cookieReads` |
| Vue route fetch | complete frontendRoutes | OK `__API_RECON_ROUTES__` (already-loaded routes) |
| Vue route guard neutralization / login-redirect block | prop open modules to trigger APIs | OK `neutralizeVueRouter` + native redirect neutralization |
| React route fetch | complete routes | Warning: static + clicks; no dedicated hook |
| Page-redirect block (login path) | stay on the page to analyze | Warning: only block the login path, avoid blocking business navigation |
| Hook crypto libs (CryptoJS/SM, etc.) | encrypted params -> plaintext API body | NO must manually hook the encryption function's input args; write the conclusion to config |
| Anti-debug bypass | otherwise runtime cannot record APIs | NO must handle manually; static still usable |

---

## E. Endpoint extraction regex (when static is too sparse)

Loosen `extract_endpoints` in `harvest_static.py`, or do it manually:

```bash
grep -rhoaE '"/[a-z][A-Za-z0-9_/\-]{3,}"' js | sort -u
grep -rhoaE '/api/[a-zA-Z0-9_./-]+' js | sort -u
```

---

## F. Troubleshooting

| Symptom | Cause -> fix |
|---|---|
| Very few static APIs | endpoint dialect does not match -> loosen the regex (section D) |
| chunk count << manifest | CSS-only or undeployed chunks; 404 already retried |
| runtime still shows login page | wrong render gate -> recheck A1: key name, container, encoding, domain |
| into the shell but modules blank | content gate -> forge the menu (A3); the `routes` path may be wrong |
| each route only bootstrap/locale | permission codes incomplete -> section I permission tree reconstruction; check the `role_permissions` + `permissions/all` dual stub |
| sidebar has items but subpages blank | tree is missing an intermediate node, or a code does not match `userRouteAuth` |
| every API jumps to login | interceptor gate -> confirm `neutralize`; nested fields need extended walk logic |
| WS frames are 0 | subscribe only happens after user interaction; raise `perRouteMs` |
| empty response body | real responses only when `forward: true` |
| Chromium missing | install chromium or set `config.chromium` / `CHROMIUM` |
| lots of mock but still returns login | hook too late or missing `location.href` setter -> document-start + preload |
| list all empty | L3 empty array is normal; keep clicking tabs/settings/detail |
| mistaking a Redux action for a route | filter out internal paths containing get/set/change/clear/toggle/upload |
| Vue still jumps to login | preload not at document-start -> change injection timing; or clear the guard manually when `neutralizeVueRouter: false` |
| response has URLs but they are not logged | enable `extractUrlsFromResponse`; or extract manually from `__API_RECON_DETAIL__` |
| do not know the Authorization header name | enable `observe.xhrHeaders` or check the request headers in DevTools |
| runtime very slow / timing out | set `waitUntil: domcontentloaded`; lower `routeTimeout`; do not use `networkidle2` |
| proxy connection fails | check `proxy` / environment variables; keep Puppeteer and curl on the same proxy port |

---

## G. Hardened targets

When the server validates the session progressively (unforgeable signed cookies, a server-rendered menu that cannot be stubbed), runtime gets stuck at the shell. Expected behavior:

- [Static is enough for endpoint enumeration] — module paths are in the code
- If authorized, run the same harness with a [real session]: `forward: true`, no neutralize needed, capture real methods/params/responses

---

## H. Single-task checklist

1. Confirm the authorized scope
2. [Read] `scripts/harvest_static.py` -> adjust for the target -> run -> review `api_static.txt`, `routes.txt`
3. [Phase 1b]: path anchor window expansion + binding layer -> `param_candidates.json` (section J)
4. Reverse A1/A2/A3 -> write a site-specific `config.json`
5. [Read and adjust] `runtime_harvest.js` / `preload.js` before running
6. `runtimeMode=depth`: `npm install` -> run the adjusted harvest script
7. `runtimeMode=coverage/both`: document-start inject the adjusted preload -> browser MCP dynamic enumeration + [parameter trigger matrix]
8. Modules do not render -> [section I permission tree reconstruction] -> patch stubs -> rerun
9. Parameter multi-sample diff + error-based inference -> `params_merged.json`
10. Merge -> `site_map.json` + `api_merged.txt`, honestly noting coverage, gaps, and script change points

---

## I. Permission tree reconstruction (Phase 4 deep dive)

Use this when forging a simple `menus: [{ path, show: true }]` does not work and submodules still do not mount.

### I1. Locate the auth module

```bash
grep -l 'userRouteAuth' js/*.js
grep -l 'routeMap\|routeLink' js/*.js
grep -rhoaE 'getResultTree|role_permissions|permissions/all' js | head
```

Record: the [permission API path], the [response field names], the [consumer chunk file name].

### I2. Extract routeMap

```bash
python3 scripts/extract_route_map.py recon/js recon/
# outputs recon/route_map.json
```

If `[!] no routeMap pattern found`: loosen the regex in `extract_route_map.py`, or grep manually:

```bash
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head -20
```

### I3. Build the permission tree + stub

```bash
python3 scripts/build_perm_tree.py recon/js recon/ --config recon/config.json
```

Script logic:
1. Parse `userRouteAuth={MONITOR:{url:...},...}` (incl. webpack alias `He=o.DASHBOARD`)
2. Use `route_map.json` to resolve alias -> real path
3. Infer parent by code prefix (`MONITOR_ALERT` -> `MONITOR`)
4. Output `permissions_tree.json`, `permissions_all_stub.json`, `role_permissions_stub.json`
5. With `--config`, auto-write the `stubs` and extend `routes` in `config.json`

[Adjust for the target] (at the top of the script):
- `DEFAULT_ROOTS`: top-level module code list
- `DEFAULT_PREFIX_PARENT`: `PREFIX_` -> parent mapping
- `DEFAULT_EXTRA_PARENT`: orphan nodes with non-prefix relations

### I4. Validate stub consistency

```bash
# permissions count should ~= userRouteAuth entry count
wc -l recon/perm_codes_all.txt
# routes should cover all links in route_map
python3 -c "import json; m=json.load(open('recon/route_map.json')); r=set(json.load(open('recon/config.json'))['routes']); print('missing', [v['link'] for v in m.values() if v['link'] not in r])"
```

### I5. Rerun runtime and compare

```bash
node recon/runtime_harvest.js recon/config.json
# compare runtime_api.json counts before/after forge; check whether /attack, /asset, etc. surface module APIs
```

| Before forge | After forge (success) |
|---|---|
| Same 3-5 bootstrap per route | different routes trigger different module APIs |
| Only `/api/locale/language` | `/api/web/...` module endpoints appear |
| Single-digit routes in `routes.txt` | 80-110+ `routes` from route_map |

### I6. When it still fails

- [coverage mode]: click the sidebar + tabs; permission gating may request only after interaction
- [stub fields]: compare the real API (curl + real session) against the stub nesting
- [extra guards]: grep `hasPermission|checkRole|func.` and other button-level checks, extend `role_permissions.permissions`
- [static fallback]: module API paths are still in `api_static.txt`, runtime only fills METHOD/body; parameters keep `param_candidates.json` + the recorded samples

---

## J. Parameter reversing (Phase 1b / 5b / 5c)

[Methodology, not a universal script.] Find paths with regex; find parameters with anchor window expansion + UI binding chain + multi-sample diff + error-based inference.

### J1. Anchor window expansion — find the packing object from the path

```bash
# anchor on a path known from Phase 1
grep -n '"/api/user/list"' js/*.js
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' js | head
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' js | head
grep -rhoaE '(get|post|put|delete|patch)\([^,]+,\s*\{' js | head
```

### J2. Wrapper layer and transport form

```bash
# axios / unified request
grep -rhoaE '(axios|request)\.(get|post|put|delete|patch)\(' js | head
grep -rhoaE 'interceptors\.(request|response)' js | head

# GraphQL
grep -rhoaE '(query|mutation)\s+\w+|gql`|graphql\(' js | head
grep -rhoaE '\$[a-zA-Z_]+\s*:\s*(Int|String|Boolean|\[)' js | head

# FormData / multipart
grep -rhoaE 'FormData|\.append\(' js | head

# path parameters
grep -rhoaE 'path:\s*"/[^"]*:[^"]+"' js | head
grep -rhoaE 'useParams|route\.params|\$route\.params' js | head
```

### J3. Validation gate — required / format / enum

```bash
grep -rhoaE '(required|message|pattern|enum|validator)\s*:' js | head
grep -rhoaE 'yup\.|zod\.|async-validator|Form\.Item|a-form-item|el-form-item' js | head
grep -rhoaE 'rules\s*:\s*\[|name:\s*["\'][a-zA-Z_]+["\']' js | head
grep -rhoaE 'label.*value|options\s*:\s*\[' js | head
```

### J4. Binding layer — form -> API

```bash
grep -rhoaE 'onFinish|handleSubmit|getFieldsValue|validateFields' js | head
grep -rhoaE '(pick|omit|transform|dayjs|moment)\(' js | head
```

Runtime fill-in: DevTools -> Network -> request -> [Initiator] (call stack) trace up from `fetch`/`send` to the packing function.

### J5. Encrypted parameters

```bash
grep -rhoaE 'encrypt|decrypt|sign|CryptoJS|sm2|sm3|sm4|RSA|AES' js | head
```

[Do not guess fields on ciphertext] — hook the encryption function's [input args] and record the plaintext payload before encryption; write the conclusion to `config.json` / `param_candidates.json`.

### J6. Parameter trigger matrix (Phase 3 required)

Record once per operation per module, and diff the request body/query:

| Operation | Focus |
|---|---|
| List first screen | pagination defaults |
| Search | keyword, filters |
| Advanced filter | optional fields |
| New/edit | full entity |
| Bulk/export | `ids[]`, `exportType` |
| Sort/paginate | `sortField`, `order` |

Output `param_samples.json`: `[{ "path", "method", "action": "search", "body", "query", "headers" }]`

### J7. Confidence rules

| Confidence | Condition |
|---|---|
| [High] | static callsite + runtime >=2 consistent samples |
| [Medium] | static only, or a single runtime hit |
| [Low] | inferred from response/error, not re-verified |
| [Pending-trigger] | field known from static, UI/permission not reached |

### J8. Scenario quick-config

| Scenario | Order |
|---|---|
| REST list page | J1 packing object -> J6 four-way diff -> J3 rules |
| New/edit form | J3 Form name -> J4 submit chain -> runtime submit + intentionally blank to see 400 |
| GraphQL | J2 variables declaration -> runtime record variables per operation |
| Encrypted body | J5 hook input args -> the pre-encryption fields are the real params |

### J9. Mapping to api-recon stages

| api-recon | Parameter recon |
|---|---|
| Phase 1 static | J1 anchor window expansion |
| Phase 2 A2 interceptor | global injected fields (tenantId, sign) |
| Phase 3 runtime | J6 trigger matrix + `param_samples.json` |
| Phase 4 permission tree | different modules have different forms -> full fields trigger only with enough permission |
| Phase 5 merge | `params_merged.json` + confidence; do not decide required from a single sample |

### J10. Troubleshooting

| Symptom | Fix |
|---|---|
| static has a field name but runtime never shows it | tag "pending-trigger"; add the permission tree / click advanced filter / each option of a coupled select |
| same path, different body shapes | normal — record per `action`, do not force-merge the schema |
| stub response is fake but you want params | [look at the outbound request] body/headers, do not infer from the stub response |
| 400 reports a nested field | watch the outer wrapper `data`/`bizData`/`variables` |
| GraphQL only shows the operation name | expand the `variables` JSON; find `$var: Type` in static |

---
