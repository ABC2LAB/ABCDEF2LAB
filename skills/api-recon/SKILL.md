---
name: api-recon
description: Invoke this skill when collecting a website's API endpoints.
---

# API Recon (frontend API reconnaissance)

Under an [authorized] premise, discover as completely as possible: [backend APIs] (paths, methods, parameters, response bodies), [frontend routes], and [UI feature triggers] (tabs, modals, table actions, etc.).

---

## Boundaries and prohibitions (Agent must read, a violation is out of scope)

This skill [only does API / parameter-surface recon]; it is not a vulnerability-hunting or exploitation stage.

### Task boundaries

| Area | Allowed | Prohibited |
|---|---|---|
| [Target] | Enumerate path, method, parameters, routes, UI triggers | SQLi/XSS/privilege-escalation/brute-force/fuzz vulns, packet tampering attacks, destructive operations |
| [Auth] | Hook + stub/mock to bypass the [client-side] login gate | Asking the user for or guessing accounts/passwords; attempting a real login-form submission |
| [Runtime] | With no credentials, hook endpoints and use mock responses to drive the SPA into its post-login shell | Flows that require a real backend session to continue |

### Credential-free dynamic analysis (Phase 3 default)

1. Use `preload.js` / `runtime_harvest.js` to [intercept and stub] bootstrap endpoints such as login, permissions, and menu;
2. Return mock bodies for business query endpoints that are [structurally correct, business-code success, data may be empty];
3. Let the frontend still render post-login pages without a backend or under 401, so it triggers more XHR/fetch/WebSocket;
4. [Empty data, blank tables, and placeholder UI are all expected] — do not switch to a real login or vuln test over this.

[In one sentence]: use mocks to prop open the frontend routes and component mounts, and [only record outbound requests]; what the backend returns does not matter, what matters is [which endpoints the frontend still calls].

### Hard process prohibitions

| Prohibited | Replacement |
|---|---|
| Before Phase 1 is done, grep/curl/Read the main entry `index-*.js` to extract API paths | Run `OUTDIR/harvest_static.py` |
| Hand-writing scripts like `extract_apis.py` that replace harvest | Edit `OUTDIR/harvest_static.py` and rerun |
| Repeating the same grep/command after it failed >=2 times | Switch strategy: read tool_logs, edit harvest, check reference |
| Skipping gate A/B and running the original `scripts/` directly | Copy to OUTDIR and adjust for the target |
| Real username/password, OTP, OAuth and other auth | stub/mock (see above) |
| Skipping stub "to get real data" and doing privilege-escalation/injection tests | Only record outbound; that is the recon boundary |
| Delete, export of sensitive data, bulk writes and other irreversible operations | Same for coverage clicks |
| Claiming all pages and endpoints are captured without finishing runtime + dynamic enumeration | See "Definition of done" or note the limitation |
| Claiming all parameters are captured without finishing the parameter trigger matrix + diff | Phase 3b matrix + Phase 5 diff |
| Inferring required/optional from a single runtime sample | Multi-sample diff or inferring from validation rules/errors |

---

## Two-layer model + run modes

| Layer | Output | Ceiling |
|---|---|---|
| [Static] (JS bundle) | Full endpoint paths, draft routes, packing-site field candidates | No HTTP method; parameters need Phase 1b; misses runtime-assembled URLs |
| [Runtime] (live session) | Method + body + response + dynamic URL + WS/SSE; multi-sample diff fills in parameters | Pages must actually render to send requests; a single sample is not enough to decide required/optional |

| Run mode | Engine | Fit |
|---|---|---|
| [depth] | `runtime_harvest.js` (Puppeteer) | API inventory, METHOD/params/response body, WS/SSE, reproducible batch runs |
| [coverage] | browser + `preload.js` | Click tabs/modals/tables, deeper feature coverage |
| [both] | depth first, then coverage | Most complete, longest running |

[Parameter methodology] (no universal script): paths via harvest/regex; parameters via [anchor window expansion + UI binding chain + multi-sample diff + error-based inference] (grep recipes in section J of [reference.md](reference.md)).

---

## Definition of done

All must hold before claiming recon is complete:

- [ ] [Static]: Phase 1 harvest produced `api_static.txt`, `routes.txt`, `js/`
- [ ] [Runtime]: at least one of depth or coverage; coverage/both must have [hook in effect + dynamic enumeration loop]
- [ ] [Into the shell]: visiting a business path is not `/login` (watch for hash routes)
- [ ] [Parameters]: coverage/both finished the parameter trigger matrix + `param_samples.json`; Phase 5 merged `params_merged.json`
- [ ] [Depth] (if module pages are blank): Phase 4 reconstruct the permission tree and rerun until [module-level APIs] appear (not just locale/bootstrap)
- [ ] [Delivery]: Phase 5 output complete (see Phase 5 output table); `insert_assets` writes the service and endpoint assets

---

## Scripts and gates

`scripts/` are reference templates only; [do not] run the originals directly and treat them as final results.

[Rule]: read first -> adjust for the target -> write into `OUTDIR` (e.g. `recon/`) -> record `CHANGES.md`; if it does not match, rewrite per the methodology, borrowing only the structure.

| Gate | When | Reference script -> OUTDIR copy | Common must-change items |
|---|---|---|---|
| [A (static)] | After Phase 0, before the [first] harvest/spider run | `harvest_static.py` / `spider_mpa.py` | [Most sites run with the default regex as-is]; only when the manifest/dialect does not match, adjust the endpoint regex, webpack/Vite `publicPath`, MPA exclude/cookie |
| [B (runtime)] | After Phase 2, before running depth/coverage | `runtime_harvest.js` / `preload.js` + `config.json` | Cookie/localStorage keys, neutralize success value, stubs, login regex, api prefix, hash/history |

[SPA mandatory order] (not interchangeable; Phase numbering takes priority over "explore before scripting"):

| Step | Must | Prohibited |
|---|---|---|
| After Phase 0 is done | Next Bash = `python3 OUTDIR/harvest_static.py <URL> OUTDIR` | curl/grep/Read the main entry `index-*.js` (usually >500KB) |
| Gate A | Copy script -> small tweaks as needed -> [run immediately] | Manually extract APIs first, then decide whether to harvest |
| Before Phase 1 is done | `wc -l` to validate output; on 404 edit harvest and retry | Hand-write an extract script; repeatedly grep a URL not yet downloaded |
| From Phase 1b | grep only `OUTDIR/js/*.js` | Use the main bundle in place of harvest |

- OK: Copy `harvest_static.py` -> (optional) edit regex -> [run immediately]
- NO: curl main bundle -> grep repeatedly -> write a temp extract -> harvest only at the end
- [MPA]: after Phase 0 the next Bash = `python3 OUTDIR/spider_mpa.py ...`

---

## Tool and output constraints

| Constraint | Note |
|---|---|
| Large files | `index-*.js` >100KB must [not] be Read/grep'd into context; batch-process with OUTDIR scripts |
| grep output | Must use `\| head -20` or `-m 5`; keep only a path summary in the conversation, do not paste bundle fragments |
| Validation | Use `wc -l`, `ls \| wc -l`; do not Read whole directories |
| regex probing | Optional, <=1 time, only on a <=50KB small chunk or HTML; the authoritative static pass is harvest |
| reference | Recipes/templates/troubleshooting in [reference.md](reference.md); do not repeat the full text inline |

---

## Execution roadmap

```
Phase 0 classify + OUTDIR
  -> Gate A -> Phase 1 harvest (* run immediately *)
  -> Phase 1b parameter reversing
  -> Phase 2 three auth gates -> config.json
  -> Gate B -> Phase 3 runtime + parameter matrix
  -> Phase 4 permission tree (when needed) -> rerun Phase 3
  -> Phase 5 merge report + insert_assets to bulk-insert all discovered service and endpoint API assets; in any case, do not omit a discovered asset at insert time
```

Check off in order; [you may not enter the next Phase before the previous item is done].

1. [ ] [Phase 0]: initial SPA/MPA probe; create `OUTDIR` -> [Phase 0](#phase-0--classify)
2. [ ] [Gate A + Phase 1]: copy script -> [immediately] harvest -> `wc -l` validate -> [Phase 1](#phase-1--static)
3. [ ] [Phase 1b]: anchor window expansion + binding layer -> `param_candidates.json` -> [Phase 1b](#phase-1b--parameter-reversing)
4. [ ] [Phase 2]: three auth gates -> `config.json` -> [Phase 2](#phase-2--three-auth-gates)
5. [ ] [Gate B]: adjust runtime scripts -> [Phase 3](#phase-3--runtime)
6. [ ] [Phase 3]: depth / coverage / both; confirm into the shell; parameter trigger matrix -> `param_samples.json`
7. [ ] [Phase 4] (if needed): permission tree -> patch stubs -> rerun Phase 3 -> [Phase 4](#phase-4--permission-tree-reconstruction)
8. [ ] [Phase 5]: merge output + report + `insert_assets` -> [Phase 5](#phase-5--merge-and-report)

---

## Phase 0 — Classify

Fetch the entry HTML and [create `OUTDIR`] (do not edit `scripts/` inside the skill):

- [SPA]: empty shell + `<div id=app>` + chunk -> Phase 1-5
- [MPA]: SSR + `<form>`, no endpoint bundle -> after Gate A:

```bash
python3 recon/spider_mpa.py <BASE_URL> <OUTDIR> [--cookie "session=..."] [--max 300] [--depth 5] [--exclude "logout|delete|destroy"]
```

Output `forms.txt`, `links.txt`, `api_inline.txt`. For an SPA, if forms ~= 0 -> switch to Phase 1.

---

## Phase 1 — Static

Follow [Scripts and gates](#scripts-and-gates) and [Tool and output constraints](#tool-and-output-constraints).

```bash
python3 recon/harvest_static.py <BASE_URL> <OUTDIR>
```

harvest: parse HTML script -> webpack/Vite manifest -> download all lazy chunks -> output `js/`, `api_static.txt`, `routes.txt`, `chunkmap.txt`.

```bash
wc -l OUTDIR/api_static.txt OUTDIR/routes.txt
ls OUTDIR/js | wc -l
```

- chunk count vs manifest: on 404, edit harvest and retry, do not manually curl each chunk
- `api_static.txt` too small -> loosen the endpoint regex inside OUTDIR and rerun (see reference)

### Phase 1b — Parameter reversing

Paths come from Phase 1; parameter fields must be recon'd separately. See grep rules in [Tool and output constraints](#tool-and-output-constraints).

[Completion criteria]: for important endpoints you can answer — field name, transport location, inferred type, whether required, sample value, confidence.

#### 1b.0 — Transport form

| Form | Where the params are | Static: look first at |
|---|---|---|
| REST JSON | body + query | `(params\|data\|body)\s*:\s*\{` near the path anchor |
| GraphQL | `variables` | gql template, `$page: Int` |
| Classic form | urlencoded | `<form>`, `FormData` |
| File upload | multipart | `FormData.append` |
| Path parameter | `/user/:id` | route table + `useParams` / `$route.params` |
| Encrypted/signed | wrapped into `sign`/`data` | Hook the encryption function's input args (reference section D) |

Output: tag each endpoint with `transport: query|json|form|graphql|encrypted`.

#### 1b.1 — Anchor window expansion

Using a known path as the anchor, expand the window to find the packing object:

```bash
grep -n '"/api/user/list"' OUTDIR/js/*.js | head -20
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' OUTDIR/js/*.js | head -20
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' OUTDIR/js/*.js | head -20
```

| Wrapper layer | Parameter clue |
|---|---|
| axios instance | `data` / `params` |
| unified request | interceptor injects global fields |
| OpenAPI client | generated method signature |
| React Query / SWR | hook second argument |
| Vue composable | composable arguments |

Type residue: `yup`/`zod`/rules, `Form.Item name=`, inline Swagger.

-> `param_candidates.json`: `{ path, fields[], source: "static-callsite", confidence }`

#### 1b.2 — Binding layer

```
Form field -> onFinish/handleSubmit -> transform -> API payload
```

| Binding source | Technique |
|---|---|
| Form submit | follow submit -> transform -> API |
| Table search | `getFieldsValue()` -> `params` |
| Route | `:id` / `?tab=` |
| Interceptor | global `tenantId`, pagination, sign |
| Enum select | `options` -> API enum values |

Use the DevTools call stack to trace up from `fetch`/`XHR.send` to the packing function.

#### 1b.3 — Three packing questions (!= Phase 2 auth gates)

| Question | What to answer |
|---|---|
| [Assembly] | where the payload is built, transform traces |
| [Validation] | required, pattern, enum |
| [Transport] | path / query / body / multipart / header |

The interceptor gate (Phase 2) also reads global injected fields (Authorization, `X-Tenant-Id`, sign).

#### 1b.4 — Handoff to Phase 3

Candidate fields come from static/binding layers; [required/optional/conditional dependencies] need the Phase 3 parameter matrix + diff + Phase 5 error-based inference.

---

## Phase 2 — Three auth gates

grep inside `OUTDIR/js/` (with `head`) and write `config.json` (recipes in reference):

| Gate | Question | Keywords |
|---|---|---|
| [Render gate] | How does it decide you are logged in? | `isLogin`, `getToken`, Cookie/localStorage |
| [Interceptor gate] | What triggers the jump to `/login`? | `response_code`, `errno`, axios interceptor |
| [Content gate] | Where do menus/permissions come from? | `menu`, `permission`, `role`, `acl`, `routes` |

Do not treat a localStorage key name as a credential — confirm it from the chunk/request chain.

[Exit = Gate B]: the conclusions land in `config.json`, and edit `OUTDIR/runtime_harvest.js` / `preload.js`.

### Phase 2b — API observation (optional)

Use `preload.js` inside OUTDIR to confirm session key names, Authorization, and nested API URLs:

| Config | Output |
|---|---|
| `recordDetail: true` | `__API_RECON_DETAIL__` |
| `observe.xhrHeaders: true` | header observation |
| `extractUrlsFromResponse: true` | sub-APIs inside responses |
| `observe.storageReads/cookieReads: true` | backfill config |
| `neutralizeVueRouter: true` | `__API_RECON_ROUTES__` |

Each coverage round exports: `__API_RECON_LOG__`, `__API_RECON_DETAIL__`, `__API_RECON_ROUTES__`, `__API_RECON_OBSERVE__`.

---

## Phase 3 — Runtime

Must have passed Gate B; follow [Boundaries and prohibitions](#boundaries-and-prohibitions-agent-must-read-a-violation-is-out-of-scope) and the credential-free mock strategy.

Set `config.json` `"runtimeMode": "depth" | "coverage" | "both"` (template in reference).

### Hook and stub (shared by depth + coverage)

| Layer | Scope | Purpose |
|---|---|---|
| L1 precise | auth/permission/bootstrap stub | pass first-screen auth |
| L2 negative fix | all JSON responses | not-logged-in code -> success |
| L3 fallback | `/api` etc. not hit by L1 | empty success body, prop open the UI |

- [depth]: fake auth + `forward` to rewrite business codes + `stubs`; traverse `routes` (hash/history); output `runtime_api.json`
- [coverage]: inject `preload.js` at [document-start] (CDP `addScriptToEvaluateOnNewDocument` or Userscript)

Verify: `window.__API_RECON_PRELOAD__` exists; a business path does not return `/login`.

```bash
cd recon && npm install
node runtime_harvest.js config.json
```

### 3b — coverage dynamic enumeration (required)

1. Main nav/sidebar — click each item, wait 1-3s for network
2. Tabs — `role=tab`, `.ant-tabs-tab`
3. Tables — view/edit/detail on the first row
4. Toolbar — export, filter, new ([avoid irreversible delete])
5. On entering each module — merge APIs/routes
6. SPA — controlled `pushState` for paths not covered in `routes.txt` (prohibited for MPA)

[Parameter trigger matrix] (required): record once per operation type per module, [diff multiple samples]:

| Operation | Typically extra parameters |
|---|---|
| List first screen | pagination + default filter |
| Click search | keyword, filter |
| Advanced filter | more optional |
| New/edit | full entity |
| Bulk/export/sort | `ids[]`, `exportType`, `sortField` |

[Under a stub the outbound body/headers are still real] — take the request as authoritative. Record -> `scan_raw.json`, `param_samples.json`, `api_detail.json`.

- [Vue]: `neutralizeVueRouter: true` + document-start preload
- [React]: `routes.txt` + sidebar clicks + `pushState`
- [both]: 3a depth first, then 3b coverage

---

## Phase 4 — Permission tree reconstruction

[Trigger]: module page blank / each route has only bootstrap (e.g. locale) -> content gate not passed.

| Symptom | Meaning |
|---|---|
| Into the shell succeeded | render gate + interceptor gate passed |
| Missing sidebar items / clicks blank | stub shape or permission codes incomplete |
| Every route has the same, very few APIs | `v-if permission` not passing |
| `routes.txt` far fewer than the bundle | must be completed from the auth module |

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl)[^"]*"' OUTDIR/js/*.js | sort -u | head -30
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|menuList|authList' OUTDIR/js/*.js | head -20
```

Typical chain: `role_permissions` (flat codes) + `permissions/all` (tree) -> `getResultTree` -> `userRouteAuth[CODE].url`.

```bash
python3 recon/extract_route_map.py recon/js recon/
python3 recon/build_perm_tree.py recon/js recon/ --config recon/config.json
```

Intermediate output: `route_map.json`, `userRouteAuth.json`, `permissions_tree.json`, `*_stub.json`, `perm_codes_all.txt`.

stub check: the outer `response_code` matches the interceptor gate; flat codes align with the tree; `routes` cover all links in `route_map`.

After updating `config.json`, [rerun Phase 3]. For a large SPA you can tune `waitUntil`, `routeTimeout`, `perRouteMs` (see reference sections A3/I).

---

## Phase 5 — Merge and report

### Output table

| File | Stage | Content |
|---|---|---|
| `js/`, `api_static.txt`, `routes.txt`, `chunkmap.txt` | 1 | static bundle and paths |
| `param_candidates.json` | 1b | static parameter field candidates |
| `config.json` | 2 | three gates + runtime config |
| `runtime_api.json` | 3a | depth detailed recording (incl. WS/SSE) |
| `param_samples.json`, `scan_raw.json`, `api_detail.json` | 3b | multi-sample, click log, detail |
| `route_map.json` etc. | 4 | permission tree intermediate files (if run) |
| `params_merged.json` | 5 | merged parameter fields + confidence |
| `api_merged.txt` | 5 | `METHOD /path [params] [static\|runtime\|both]` |
| `site_map.json` | 5 | routes, API, params, feature points, limitations |
| [insert_assets] | 5 | write all service and endpoint assets into the asset store |

### 5b — Parameter merge

Diff from `param_samples.json`; [no universal merge script]. Confidence rules in reference J7 (high/medium/low/pending-trigger).

### 5c — Error-based inference

Within the authorized scope you may send incomplete requests to read a 400 ([this is parameter recon, not a vuln test]): `field 'x' is required`, enum errors, etc. Watch for the `data` wrapper, `variables`, and the pre-encryption `bizData`.

The report must note: runtimeMode, static/runtime API count, parameter confidence, uncovered modules, and a `CHANGES.md` summary relative to the reference scripts.

Suggested `site_map.json` structure:

```json
{
  "site": "https://example.com",
  "runtimeMode": "both",
  "appType": "vue-spa",
  "routeGuardStrategy": ["nav-neutralize", "L1-auth", "L2-patch", "forward"],
  "apisFromStatic": [],
  "apisFromRuntime": [],
  "apis": [],
  "params": [{ "method": "POST", "path": "/api/user/list", "transport": "json", "fields": [] }],
  "frontendRoutes": [],
  "routesVerifiedByClick": [],
  "featuresTriggered": [],
  "limitations": ""
}
```

More fields and grep recipes in [reference.md](reference.md).

---

## General notes

- [Framework-agnostic]: webpack/Vite/Angular lazy load use the same method
- [Transport]: REST/JSON, GraphQL, WebSocket, SSE; gRPC-web out of scope
- [SSR]: client-side fetch is recordable; RSC/Server Actions are not fully enumerable
- [Blind spots]: JSVMP, WASM, HMAC/mTLS strong validation -> static + note the limitation
- [Parameter blind spots]: conditional coupling, hidden params, WASM packing -> "pending-trigger" / "unreachable"
- [Static is the safety net]: when runtime is blocked, static can still enumerate endpoints

---

## Additional resources

- Grep recipes, `config.json` template, troubleshooting, hooks, parameter-reversing section J, site_map template: [reference.md](reference.md)
- Reference script paths in the [Scripts and gates](#scripts-and-gates) table
