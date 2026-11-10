#!/usr/bin/env node
// 실제 Chrome CDP·Next·v2 API만 사용한다. 응답 본문을 위조하거나 UI 내부 상태를 변경하지 않는다.
const fs = require("node:fs");
const path = require("node:path");
const [phase, ui, targetURL] = process.argv.slice(2);
const cdp = process.env.ARTEX_FIRST_DIAGNOSIS_CDP || "http://127.0.0.1:49339";
const outputDir = process.env.ARTEX_FIRST_DIAGNOSIS_BROWSER_OUTPUT;
if (!outputDir) throw new Error("전용 브라우저 출력 디렉터리가 필요합니다");
for (const value of [ui, cdp, targetURL]) {
  if (!["127.0.0.1", "localhost", "[::1]"].includes(new URL(value).hostname))
    throw new Error("소유 loopback 시험 주소만 허용합니다");
}
fs.mkdirSync(outputDir, { recursive: true, mode: 0o700 });
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
let assertions = 0;
let diagnostic = async () => ({});
function check(value, code) { if (!value) throw new Error(code); assertions++; }
(async () => {
  const pages = await (await fetch(cdp + "/json/list")).json();
  let page = pages.find(p => p.type === "page" && p.url.startsWith(ui));
  if (!page) page = await (await fetch(cdp + "/json/new?" + encodeURIComponent(ui), { method: "PUT" })).json();
  const socket = new WebSocket(page.webSocketDebuggerUrl);
  await new Promise((resolve, reject) => { socket.onopen = resolve; socket.onerror = reject; });
  let nextID = 0;
  const pending = new Map();
  const requests = [];
  const responses = [];
  let startReceipt;
  let droppedStartResponse = false;
  let dropError = false;
  const send = (method, params = {}) => new Promise((resolve, reject) => {
    const id = ++nextID;
    pending.set(id, { resolve, reject });
    socket.send(JSON.stringify({ id, method, params }));
  });
  socket.onmessage = async event => {
    const message = JSON.parse(event.data);
    if (message.id) {
      const waiter = pending.get(message.id);
      if (!waiter) return;
      pending.delete(message.id);
      if (message.error) waiter.reject(new Error("cdp_operation_failed"));
      else waiter.resolve(message.result);
      return;
    }
    if (message.method === "Network.requestWillBeSent") {
      const request = message.params.request;
      const pathname = new URL(request.url).pathname;
      if (pathname.startsWith("/api/")) requests.push({ path: pathname, method: request.method });
    }
    if (message.method === "Network.responseReceived") {
      const response = message.params.response;
      const pathname = new URL(response.url).pathname;
      if (pathname.startsWith("/api/")) responses.push({ path: pathname, status: response.status });
    }
    if (message.method === "Fetch.requestPaused") {
      try {
        const params = message.params;
        const requestPath = new URL(params.request.url).pathname;
        if (phase === "url-lost-start" && requestPath.endsWith("/start") && !droppedStartResponse) {
          check(params.responseStatusCode >= 200 && params.responseStatusCode < 300, "start_not_accepted_before_loss");
          const body = JSON.parse(params.request.postData);
          const key = Object.entries(params.request.headers).find(([name]) => name.toLowerCase() === "idempotency-key")?.[1];
          const id = requestPath.split("/").at(-2);
          check(typeof key === "string" && key.length > 0 && body.expected_revision > 0, "start_binding_missing");
          startReceipt = { id, revision: body.expected_revision, key };
          await send("Fetch.failRequest", { requestId: params.requestId, errorReason: "Aborted" });
          droppedStartResponse = true;
        } else {
          await send("Fetch.continueRequest", { requestId: params.requestId });
        }
      } catch { dropError = true; }
    }
  };
  const evaluate = async expression => {
    const answer = await send("Runtime.evaluate", { expression, returnByValue: true, awaitPromise: true, userGesture: true });
    if (answer.exceptionDetails) throw new Error("browser_evaluation_failed");
    return answer.result.value;
  };
  const wait = async (expression, code, timeout = 30000) => {
    const end = Date.now() + timeout;
    while (Date.now() < end) {
      if (await evaluate(expression)) { assertions++; return; }
      await sleep(100);
    }
    throw new Error(code);
  };
  const hasText = text => "document.body.innerText.includes(" + JSON.stringify(text) + ")";
  const button = text => "Array.from(document.querySelectorAll('button')).find(b=>b.offsetWidth&&!b.disabled&&b.textContent.trim()===" + JSON.stringify(text) + ")";
  const click = async text => {
    await wait("!!(" + button(text) + ")", "button_unavailable:" + text);
    check(await evaluate("(()=>{const b=" + button(text) + ";if(!b)return false;b.click();return true})()"), "button_missing");
  };
  const fill = async (selector, value) => {
    check(await evaluate("(()=>{const e=document.querySelector(" + JSON.stringify(selector) + ");if(!e)return false;Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,'value').set.call(e," + JSON.stringify(value) + ");e.dispatchEvent(new Event('input',{bubbles:true}));return true})()"), "input_missing");
  };
  const select = async (selector, value) => {
    check(await evaluate("(()=>{const e=document.querySelector(" + JSON.stringify(selector) + ");if(!e)return false;e.value=" + JSON.stringify(value) + ";e.dispatchEvent(new Event('change',{bubbles:true}));return true})()"), "select_missing");
  };
  const startCount = () => requests.filter(r => r.method === "POST" && r.path.endsWith("/start")).length;
  diagnostic = async () => ({
    requestCounts: { reads: requests.filter(r => r.method === "GET").length, starts: startCount(), mutations: requests.filter(r => r.method !== "GET").length },
    responseCounts: responses.reduce((counts, r) => ({ ...counts, [r.status]: (counts[r.status] || 0) + 1 }), {}),
    visibleCounts: await evaluate("({inputs:document.querySelectorAll('input').length,alerts:document.querySelectorAll('[role=alert]').length,statuses:document.querySelectorAll('[role=status]').length})")
  });
  await send("Page.enable");
  await send("Runtime.enable");
  await send("Network.enable");
  await send("Browser.setDownloadBehavior", { behavior: "allow", downloadPath: outputDir });

  if (phase === "url-draft") {
    await send("Page.navigate", { url: ui + "/#entry" });
    await wait("document.querySelector('#login-pass')!==null", "login_missing");
    await fill("#login-pass", "synthetic-owned-combined-fixture");
    await click("로그인");
    await wait("document.querySelector('#url-diagnosis-profile')?.options.length>1", "real_v2_catalog_missing");
    await fill("#url-diagnosis-target", targetURL);
    await select("#url-diagnosis-profile", "owned-url");
    await fill("#url-diagnosis-name", "URL 브라우저 합성 수용");
    await click("승인 범위 확인");
    await wait(hasText("실행 전 검증 완료"), "approved_scope_not_ready");
    check(await evaluate("!!document.querySelector('[aria-label=\"서버가 확인한 승인 범위\"]')"), "scope_projection_missing");
    check(await evaluate("Array.from(document.querySelectorAll('button')).find(b=>b.textContent.trim()==='진단 시작')?.disabled===true"), "start_without_explicit_confirmation");
    check(startCount() === 0, "draft_started_execution");
    check(requests.filter(r => r.method === "POST" && r.path === "/api/diagnoses/v2/drafts").length === 1, "draft_creation_count");
    check(!requests.some(r => r.path.startsWith("/api/diagnoses/v1")), "v2_fell_back");
    const mutations = requests.filter(r => r.method === "POST").length;
    await click("초안 화면 닫기");
    await click("새 진단 초안");
    await wait(hasText("실행 전 검증 완료"), "reopen_did_not_restore");
    await send("Page.reload");
    await wait(hasText("실행 전 검증 완료"), "refresh_did_not_restore");
    check(requests.filter(r => r.method === "POST").length === mutations, "refresh_or_reopen_replayed_mutation");
  } else if (phase === "url-lost-start") {
    await send("Fetch.enable", { patterns: [{ urlPattern: "*://*/api/diagnoses/v2/drafts/*/start", requestStage: "Response" }] });
    check(await evaluate("(()=>{const e=document.querySelector('[aria-label=\"URL 기반 새 진단\"] input[type=checkbox]');if(!e||e.disabled)return false;e.click();return true})()"), "start_confirmation_missing");
    await click("진단 시작");
    const end = Date.now() + 15000;
    while (!droppedStartResponse && !dropError && Date.now() < end) await sleep(50);
    check(droppedStartResponse && !dropError && !!startReceipt, "real_start_response_not_dropped");
    await send("Fetch.disable");
    await wait(hasText("시작·모델 호출을 자동으로 재전송하지 않아요"), "uncertain_response_not_explained");
    await sleep(500);
    check(startCount() === 1, "lost_start_was_retried");
  } else if (phase === "url-recover") {
    await send("Page.reload");
    await wait(hasText("진단 준비 중"), "api_restart_preparation_not_restored");
    check(await evaluate("document.querySelector('[aria-label=\"URL 기반 새 진단\"] [role=status]')?.innerText.includes('task #')"), "new_real_task_missing");
    check(await evaluate("document.querySelector('[aria-label=\"URL 기반 새 진단\"] [role=status]')?.innerText.includes('엔진 시작: 미확인')"), "planning_was_claimed_as_engine_start");
    await click("진단 상태 다시 조회");
    await wait(hasText("진단 준비 중"), "query_recovery_failed");
    check(startCount() === 0 && !requests.some(r => r.method !== "GET"), "recovery_replayed_mutation");
  } else if (phase === "url-report") {
    await click("진단 상태 다시 조회");
    await wait(hasText("실행 마감"), "real_execution_not_completed");
    await wait(hasText("결과 저장: 확인됨"), "saved_result_missing");
    await click("저장 보고서 보기");
    await wait(hasText("첫 통합 진단 보고서"), "real_report_connection_failed");
    await wait(hasText("검토 필요"), "manual_review_candidate_missing");
    check(await evaluate("(()=>{const root=document.querySelector('[aria-label=\"진단 판정 집계\"]');if(!root)return false;const counts=Object.fromEntries(Array.from(root.querySelectorAll('dt')).map(e=>[e.textContent.trim(),e.nextElementSibling.textContent.trim()]));return counts['검토 필요']==='1건'&&counts['확정']==='0건'})()"), "candidate_or_confirmed_count_mismatch");
    const began = Date.now();
    await click("종합 HTML 다운로드");
    await wait(hasText("다운로드를 요청했어요"), "html_download_not_requested");
    let downloaded;
    for (let n = 0; n < 80; n++) {
      downloaded = fs.readdirSync(outputDir).find(name => name.endsWith(".html") && fs.statSync(path.join(outputDir, name)).mtimeMs >= began);
      if (downloaded) break;
      await sleep(100);
    }
    check(!!downloaded, "html_download_file_missing");
    check(fs.readFileSync(path.join(outputDir, downloaded), "utf8").includes("<html"), "html_download_invalid");
    check(startCount() === 0 && !requests.some(r => r.method !== "GET"), "report_caused_execution_mutation");
  } else {
    throw new Error("unknown_phase");
  }
  const summary = { phase, status: "PASS", assertions, ...(startReceipt ? { startReceipt } : {}) };
  // 인증값·모델 원문·DOM·네트워크 원문은 보관하거나 출력하지 않는다.
  fs.appendFileSync(path.join(outputDir, "url-browser-summary.jsonl"), JSON.stringify({ phase, status: "PASS", assertions, ...(await diagnostic()) }) + "\n", { mode: 0o600 });
  console.log(JSON.stringify(summary));
  socket.close();
})().catch(async error => {
  let counts = {};
  try { counts = await diagnostic(); } catch {}
  console.error(JSON.stringify({ phase, status: "FAIL", reason: error.message, ...counts }));
  process.exit(1);
});
