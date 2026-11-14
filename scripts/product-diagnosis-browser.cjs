#!/usr/bin/env node
// Docker 제품(/) URL v2 실브라우저 수용 드라이버. 실제 Chrome CDP·Next·Go v2 API만 쓰고 응답 본문을 위조하거나
// UI 내부 상태를 바꾸지 않는다. 단계 · 출력 계약은 scripts/url-diagnosis-browser.cjs(팀 화면)와 같아서
// 같은 Go 하네스(TestFirstDiagnosisURLBrowserIndependentProcessesOwnedPostgres)에
// ARTEX_FIRST_DIAGNOSIS_BROWSER_SCRIPT 로 이 파일을 넘기면 작업대 화면으로 같은 실제 흐름을 검증한다.
const fs = require("node:fs");
const path = require("node:path");
const [phase, ui, targetURL] = process.argv.slice(2);
const cdp = process.env.ARTEX_FIRST_DIAGNOSIS_CDP || "http://127.0.0.1:49339";
const outputDir = process.env.ARTEX_FIRST_DIAGNOSIS_BROWSER_OUTPUT;
if (!outputDir) throw new Error("전용 브라우저 출력 디렉터리가 필요합니다");
for (const value of [ui, cdp]) {
  if (!["127.0.0.1", "localhost", "[::1]"].includes(new URL(value).hostname))
    throw new Error("소유 loopback 시험 주소만 허용합니다");
}
// Docker의 소유된 일회용 설치 입력만 읽는다. 비밀은 출력·증거에 기록하지 않는다.
if (!["https://juice.test:9443/rest/products", "https://juice-a.test:9443/rest/products", "https://juice-b.test:9443/rest/products"].includes(targetURL)) throw new Error("owned_fixture_target_required");
const config = JSON.parse(fs.readFileSync(process.argv[5], "utf8"));
const FIXTURE_PASSWORD = config.admin_password;
const DYNAMIC_TARGETS = config.format === "abc2lab.product-install/v2";
const FIXTURE_PROFILE = DYNAMIC_TARGETS ? "product-model-local_only" : "product-local_only";
const OPENROUTER = process.env.ARTEX_URL_OPENROUTER_BROWSER === "1";
const WORKBENCH = `${ui}/`;
fs.mkdirSync(outputDir, { recursive: true, mode: 0o700 });
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
let assertions = 0;
let diagnostic = async () => ({});
function check(value, code) {
  if (!value) throw new Error(code);
  assertions++;
}
(async () => {
  const pages = await (await fetch(`${cdp}/json/list`)).json();
  let page = pages.find((p) => p.type === "page" && p.url.startsWith(ui));
  if (!page) page = await (await fetch(`${cdp}/json/new?${encodeURIComponent(WORKBENCH)}`, { method: "PUT" })).json();
  const socket = new WebSocket(page.webSocketDebuggerUrl);
  await new Promise((resolve, reject) => {
    socket.onopen = resolve;
    socket.onerror = reject;
  });
  let nextID = 0;
  const pending = new Map();
  const requests = [];
  const responses = [];
  let startReceipt;
  let draftID;
  let droppedStartResponse = false;
  let dropError = false;
  const send = (method, params = {}) =>
    new Promise((resolve, reject) => {
      const id = ++nextID;
      pending.set(id, { resolve, reject, method });
      socket.send(JSON.stringify({ id, method, params }));
    });
  socket.onmessage = async (event) => {
    const message = JSON.parse(event.data);
    if (message.id) {
      const waiter = pending.get(message.id);
      if (!waiter) return;
      pending.delete(message.id);
      if (message.error) waiter.reject(new Error(`cdp_operation_failed:${waiter.method}:${message.error.code}`));
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
          if (OPENROUTER) check(body.external_transfer_confirmed === true && Object.keys(body).sort().join(",") === "expected_revision,external_transfer_confirmed,version", "external_consent_body_or_credentials");
          const key = Object.entries(params.request.headers).find(
            ([name]) => name.toLowerCase() === "idempotency-key",
          )?.[1];
          const id = requestPath.split("/").at(-2);
          check(typeof key === "string" && key.length > 0 && body.expected_revision > 0, "start_binding_missing");
          startReceipt = { id, revision: body.expected_revision, key };
          // 서버는 실제로 시작했고 응답만 잃는다(응답 유실 복구 시험).
          await send("Fetch.failRequest", { requestId: params.requestId, errorReason: "Aborted" });
          droppedStartResponse = true;
        } else {
          await send("Fetch.continueRequest", { requestId: params.requestId });
        }
      } catch {
        dropError = true;
      }
    }
  };
  const evaluate = async (expression) => {
    const answer = await send("Runtime.evaluate", {
      expression,
      returnByValue: true,
      awaitPromise: true,
      userGesture: true,
    });
    if (answer.exceptionDetails) throw new Error("browser_evaluation_failed");
    return answer.result.value;
  };
  // 새로고침·이동 중에는 실행 컨텍스트가 잠깐 사라져 평가가 실패할 수 있다 → 기다리는 동안만 다시 시도한다
  const wait = async (expression, code, timeout = 30000) => {
    const end = Date.now() + timeout;
    while (Date.now() < end) {
      let ok = false;
      try {
        ok = await evaluate(expression);
      } catch {
        ok = false;
      }
      if (ok) {
        assertions++;
        return;
      }
      await sleep(100);
    }
    throw new Error(code);
  };
  // 작업대는 화면 여섯 개가 함께 마운트되고 숨긴 화면은 hidden 이다 → 보이는 요소(offsetWidth)만 본다.
  const visibleText =
    "Array.from(document.querySelectorAll('section[data-view]:not([hidden])')).map(e=>e.innerText).join('\\n')";
  const hasText = (text) => `(${visibleText}).includes(${JSON.stringify(text)})`;
  const button = (text) =>
    `Array.from(document.querySelectorAll('button,a')).find(b=>b.offsetWidth&&!b.disabled&&b.textContent.trim()===${JSON.stringify(text)})`;
  const click = async (text) => {
    await wait(`!!(${button(text)})`, `button_unavailable:${text}`);
    check(
      await evaluate(`(()=>{const b=${button(text)};if(!b)return false;b.click();return true})()`),
      "button_missing",
    );
  };
  const fill = async (selector, value) => {
    check(
      await evaluate(
        `(()=>{const e=document.querySelector(${JSON.stringify(selector)});if(!e)return false;Object.getOwnPropertyDescriptor(e.tagName==='TEXTAREA'?HTMLTextAreaElement.prototype:HTMLInputElement.prototype,'value').set.call(e,${JSON.stringify(value)});e.dispatchEvent(new Event('input',{bubbles:true}));return true})()`,
      ),
      "input_missing",
    );
  };
  const select = async (selector, value) => {
    check(
      await evaluate(
        `(()=>{const e=document.querySelector(${JSON.stringify(selector)});if(!e)return false;Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype,'value').set.call(e,${JSON.stringify(value)});e.dispatchEvent(new Event('change',{bubbles:true}));return true})()`,
      ),
      "select_missing",
    );
  };
  // 보이는 Kv(dt/dd) 한 줄의 값
  const fact = (label) =>
    `(Array.from(document.querySelectorAll('section[data-view]:not([hidden]) dt')).find(e=>e.textContent.trim()===${JSON.stringify(label)})?.nextElementSibling?.textContent.trim()??'')`;
  const startCount = () => requests.filter((r) => r.method === "POST" && r.path.endsWith("/start")).length;
  const mutationCount = () => requests.filter((r) => r.method !== "GET").length;
  // 통제 진단 시작이 일반 ARTEX 작업 생성(POST /api/tasks)이나 v1 관리 API 로 새지 않았는가
  const noLegacyExecution = () =>
    !requests.some((r) => (r.method === "POST" && r.path === "/api/tasks") || r.path.startsWith("/api/diagnoses/v1"));
  diagnostic = async () => ({
    requestCounts: {
      reads: requests.filter((r) => r.method === "GET").length,
      starts: startCount(),
      mutations: mutationCount(),
    },
    responseCounts: responses.reduce((counts, r) => {
      counts[r.status] = (counts[r.status] || 0) + 1;
      return counts;
    }, {}),
    visibleCounts: await evaluate(
      "({inputs:document.querySelectorAll('section[data-view]:not([hidden]) input').length,alerts:document.querySelectorAll('section[data-view]:not([hidden]) [role=alert]').length})",
    ),
    // 경로·상태 코드만 (식별자는 {id} 로 가리고 본문·헤더는 남기지 않는다)
    routeCounts: responses.reduce((counts, r) => {
      const key = `${r.status} ${r.path.replace(/\/drafts\/[^/]+\//, "/drafts/{id}/").replace(/\/tasks\/[0-9]+\//, "/tasks/{id}/")}`;
      counts[key] = (counts[key] || 0) + 1;
      return counts;
    }, {}),
    // 보이는 경고는 작업대 자신의 안내 글이다(서버 원문 아님)
    alertText: await evaluate(
      "Array.from(document.querySelectorAll('section[data-view]:not([hidden]) [role=alert]')).map(e=>e.innerText.slice(0,160)).join(' | ')",
    ),
    view: await evaluate("location.hash"),
  });
  await send("Page.enable");
  await send("Runtime.enable");
  await send("Network.enable");
  await send("Browser.setDownloadBehavior", { behavior: "allow", downloadPath: outputDir });

  async function openNew() {
    await wait("document.readyState==='complete'", "initial_document_not_ready");
    const previousDocument = await evaluate("performance.timeOrigin");
    await send("Page.reload");
    await wait(`performance.timeOrigin!==${JSON.stringify(previousDocument)}&&document.readyState==='complete'`, "new_document_not_ready");
    await click("새 점검");
    await wait("document.querySelector('section[data-view=new] input[autocomplete=current-password]')!==null||document.querySelector('#luProfile')?.options.length>=1", "login_or_catalog_missing");
    const loginPass = 'section[data-view="new"] input[autocomplete="current-password"]';
    if (await evaluate(`document.querySelector(${JSON.stringify(loginPass)})!==null`)) {
      await fill(loginPass, FIXTURE_PASSWORD);
      await click("로그인");
    }
    await wait("document.querySelector('#luProfile')?.options.length>=1", "real_v2_catalog_missing");
    if (await evaluate(`!!(${button("다른 진단 만들기 · 조회 연결 해제")})`)) await click("다른 진단 만들기 · 조회 연결 해제");
    await wait("document.querySelector('#luUrl')?.disabled===false", "new_target_input_locked");
    check(!(await evaluate(`!!(${button("실제 엔진으로 보기")})||!!(${button("예시 재생으로 보기")})`)), "demo_switch_present");
    check(await evaluate("document.querySelector('#lvUrl')===null"), "v1_task_form_shown_on_v2_server");
    await fill("#luUrl", targetURL);
    await select("#luProfile", FIXTURE_PROFILE);
  }
  if (phase === "external-key-save" || phase === "external-key-reuse" || phase === "external-scope") {
    check(DYNAMIC_TARGETS, "dynamic_install_required");
    check(config.openrouter_manifest?.provider_slug === "fixture/endpoint" && config.openrouter_manifest?.approval_id === "product-key-registration-fixture", "synthetic_configuration_only_fixture_required");
    await openNew();
    await click("외부 LLM 사용하기");
    await wait("document.querySelector('#externalModelName')?.value==='qwen/qwen3.6-35b-a3b'", "fixed_qwen_policy_missing");
    check(await evaluate("document.querySelector('#externalModelName').readOnly"), "unapproved_model_editable");
    if (phase === "external-key-save") {
      await fill("#externalModelKey", "owned-synthetic-key-no-real-account");
      await click("모델 연결 설정 저장");
      await wait(hasText("서버에 키를 등록했습니다"), "key_registration_failed");
      check(requests.filter(r=>r.method==="POST"&&r.path==="/api/product/model-credentials").length===1, "key_registration_replayed");
    } else {
      await wait(hasText("등록된 API 키를 재사용합니다"), "saved_key_not_reusable");
      check(mutationCount()===0, "reuse_wrote_or_executed");
    }
    check(await evaluate("document.querySelector('#externalModelKey').value===''"), "key_retained_in_input");
    check(await evaluate("![JSON.stringify(localStorage),JSON.stringify(sessionStorage),document.body.innerText].some(v=>v.includes('owned-synthetic-key-no-real-account'))"), "key_leaked_to_browser_storage_or_ui");
    if (phase === "external-scope") {
      await click("승인 범위 확인");
      await wait(hasText("실행 전 검증 완료"), "external_scope_not_ready");
      await wait(hasText("fixture/endpoint"), "external_provider_policy_missing");
      await wait(hasText("qwen/qwen3.6-35b-a3b"), "external_model_policy_missing");
      check((await evaluate(fact("비용 상한"))).includes("6"), "external_cost_policy_missing");
      check(await evaluate("Array.from(document.querySelectorAll('section[data-view=scope] button')).find(b=>b.textContent.trim()==='진단 시작')?.disabled===true"), "external_start_without_consent");
    }
    check(startCount()===0,"credential_operation_started_model");
    check(noLegacyExecution(),"credential_used_generic_engine");
  } else if (phase === "target-approve") {
    check(DYNAMIC_TARGETS, "dynamic_install_required");
    await openNew();
    await wait("document.querySelector('#targetReadonlyBasis')!==null", "operator_registration_missing");
    await fill("#targetReadonlyPaths", "/rest/products");
    await fill("#targetReadonlyBasis", "Owned Juice Shop fixture: the product listing GET/HEAD routes are approved for nonmutating diagnostic observation only.");
    await click("대상 등록·소유 확인 준비");
    await wait("document.querySelector('#targetChallengeValue')?.textContent.length>20", "ownership_challenge_missing");
    await click("소유 확인 후 대상 승인");
    await wait(hasText("DNS TXT 소유 증명이 아직 확인되지 않았습니다"), "declaration_alone_approved_target");
    const proof = await evaluate("({name:document.querySelector('#targetChallengeName').textContent,values:[document.querySelector('#targetChallengeValue').textContent]})");
    const tokenFile = process.env.ARTEX_PRODUCT_DNS_TOKEN_FILE;
    check(!!tokenFile, "protected_dns_control_token_required");
    const response = await fetch("http://127.0.0.1:49083/control", {method:"POST",headers:{"Content-Type":"application/json","X-Fixture-Control":fs.readFileSync(tokenFile,"utf8").trim()},body:JSON.stringify(proof)});
    check(response.status===204,"owned_dns_publication_failed");
    await click("소유 확인 후 대상 승인");
    await wait(hasText(`승인된 대상: ${new URL(targetURL).origin}`), "target_approval_failed");
    check(startCount()===0,"approval_started_diagnosis");
    check(noLegacyExecution(),"approval_used_generic_engine");
  } else if (phase === "url-draft") {
    await openNew();
    await click("승인 범위 확인");
    await wait(hasText("실행 전 검증 완료"), "approved_scope_not_ready");
    await wait(hasText("서버가 확인한 승인 범위"), "scope_projection_missing");
    check((await evaluate(fact("실제 대상"))) === new URL(targetURL).origin, "scope_origin_mismatch");
    check(
      await evaluate(
        "Array.from(document.querySelectorAll('section[data-view=\"scope\"] button')).find(b=>b.textContent.trim()==='진단 시작')?.disabled===true",
      ),
      "start_without_explicit_confirmation",
    );
    if (OPENROUTER) {
      await wait(hasText("qwen/qwen3.6-35b-a3b"),"approved_model_missing");
      await wait(hasText("fixture/endpoint"),"approved_provider_missing");
      await wait(hasText("외부 전송"),"external_transfer_notice_missing");
      check((await evaluate(fact("비용 상한"))).includes("6"),"approved_usd_cap_missing");
    }
    draftID = (await evaluate(fact("진단"))).match(/prep-[a-f0-9]{32}/)?.[0];
    check(!!draftID,"draft_id_missing");
    check(startCount() === 0, "draft_started_execution");
    check(
      requests.filter((r) => r.method === "POST" && r.path === "/api/diagnoses/v2/drafts").length === 1,
      "draft_creation_count",
    );
    check(noLegacyExecution(), "v2_fell_back_to_legacy_execution");
    const mutations = mutationCount();
    await send("Page.reload");
    await click("범위 확인");
    await wait(hasText("실행 전 검증 완료"), "refresh_did_not_restore");
    check(mutationCount() === mutations, "refresh_replayed_mutation");
  } else if (phase === "url-start") {
    await wait(hasText("실행 전 검증 완료"),"ready_scope_missing");
    check(await evaluate("(()=>{const e=document.querySelector('section[data-view=scope]:not([hidden]) input[type=checkbox]');if(!e||e.disabled)return false;e.click();return true})()"),"start_confirmation_missing");
    await click("진단 시작");
    await wait(hasText("진단 중지 요청"),"live_stop_missing");
    check(startCount()===1,"explicit_start_count");
  } else if (phase === "url-stop") {
    await click("상태 다시 조회");
    await wait(`!!(${button("진단 중지 요청")})`,"fresh_stop_unavailable");
    await click("진단 중지 요청");
    await wait(hasText("결과 불명"),"cancelled_usage_claimed_known",20000);
    check(requests.filter(r=>r.method==="POST"&&r.path.endsWith("/stop")).length===1,"stop_replayed");
    check(responses.some(r=>r.path.endsWith("/stop")&&r.status===200),"stop_not_accepted");
    check(startCount()===0,"stop_started_execution");
    check((await evaluate(fact("엔진 시작")))==="미확인","preparation_claimed_engine_started");
  } else if (phase === "url-lost-start") {
    await send("Fetch.enable", {
      patterns: [{ urlPattern: "*://*/api/diagnoses/v2/drafts/*/start", requestStage: "Response" }],
    });
    await wait(hasText("실행 전 검증 완료"), "ready_scope_missing");
    check(
      await evaluate(
        "(()=>{const e=document.querySelector('section[data-view=\"scope\"]:not([hidden]) input[type=checkbox]');if(!e||e.disabled)return false;e.click();return true})()",
      ),
      "start_confirmation_missing",
    );
    await click("진단 시작");
    const end = Date.now() + 15000;
    while (!droppedStartResponse && !dropError && Date.now() < end) await sleep(50);
    check(droppedStartResponse && !dropError && !!startReceipt, "real_start_response_not_dropped");
    await send("Fetch.disable");
    await wait(hasText("시작 · 모델 호출을 자동으로 다시 보내지 않습니다"), "uncertain_response_not_explained");
    await sleep(500);
    check(startCount() === 1, "lost_start_was_retried");
    check(noLegacyExecution(), "lost_start_fell_back");
  } else if (phase === "url-recover") {
    // 새로고침은 GET 만으로 같은 진단을 다시 읽는다
    await send("Page.reload");
    await wait(`!!(${button("점검 진행 보기")})`, "started_job_not_restored");
    await click("점검 진행 보기");
    await wait(hasText("진단 준비 중"), "api_restart_preparation_not_restored");
    await wait(`${fact("실제 task")}.startsWith('#')`, "new_real_task_missing");
    check((await evaluate(fact("엔진 시작"))) === "미확인", "planning_was_claimed_as_engine_start");
    await click("상태 다시 조회");
    await wait(hasText("진단 준비 중"), "query_recovery_failed");
    check(startCount() === 0 && mutationCount() === 0, "recovery_replayed_mutation");
  } else if (phase === "url-report") {
    await send("Page.navigate", { url: `${WORKBENCH}#live` });
    // 응답 유실 뒤에는 새로고침의 GET 조회로 같은 영수증을 복원한다.
    await send("Page.reload");
    await click("점검 진행");
    // 진행 중에는 작업대가 2초마다 상태만 다시 읽는다(버튼으로도 조회 가능)
    await wait(hasText("실행 마감"), "real_execution_not_completed", 75000);
    await wait(hasText("결과 저장을 확인했습니다"), "saved_result_missing");
    if (OPENROUTER) {
      await wait(hasText("확인된 비용"),"confirmed_model_cost_missing");
      await wait(hasText("예약 비용"),"reserved_model_cost_missing");
    }
    // 마감된 회차를 실행 중처럼 조회하지 않고, '서버 모드 미제공'으로 잘못 안내하지 않는다
    await wait(hasText("실행이 마감됐습니다"), "closed_run_not_explained");
    check(!(await evaluate(hasText("전송 통제: 이 서버 모드에서는 제공되지 않음"))), "closed_run_shown_as_unsupported");
    check((await evaluate(fact("결과 저장"))) === "확인됨", "result_ready_fact_missing");
    check((await evaluate(fact("마감 확인"))) === "확인됨", "drain_fact_missing");
    await click("보고서 보기");
    await wait(
      "(()=>{const r=document.querySelector('section[data-view=\"report\"]:not([hidden]) [aria-label=\"진단 판정 집계\"]');return !!r&&r.innerText.includes('검토 필요')&&/[0-9]+건/.test(r.innerText)})()",
      "outcome_strip_missing",
    );
    // 조치 자료를 받기 전에는 칸이 없다(설명 글에도 '검토 필요'가 있다) → 칸 값이 맞을 때까지 기다린다
    await wait(
      "(()=>{const root=document.querySelector('section[data-view=\"report\"] [aria-label=\"진단 판정 집계\"]');if(!root)return false;const cells=Array.from(root.querySelectorAll('div')).filter(d=>d.children.length===2);const v=l=>cells.find(c=>c.children[0].textContent.trim()===l)?.children[1].textContent.trim();return v('검토 필요')==='1건'&&v('확정')==='0건'})()",
      "candidate_or_confirmed_count_mismatch",
    );
    await wait(hasText("정상 종료"), "stored_run_record_missing");
    check((await evaluate(fact("실행 ID"))).length > 0, "run_ref_missing");
    const began = Date.now();
    await click("공유용 사본");
    await wait(hasText("공유용 사본 파일을 저장했습니다."), "html_download_not_requested");
    let downloaded;
    for (let n = 0; n < 80; n++) {
      downloaded = fs
        .readdirSync(outputDir)
        .find((name) => name.endsWith(".html") && fs.statSync(path.join(outputDir, name)).mtimeMs >= began);
      if (downloaded) break;
      await sleep(100);
    }
    check(!!downloaded, "html_download_file_missing");
    check(fs.readFileSync(path.join(outputDir, downloaded), "utf8").includes("<html"), "html_download_invalid");
    check(startCount() === 0 && mutationCount() === 0, "report_caused_execution_mutation");
    check(noLegacyExecution(), "report_used_legacy_execution");
  } else {
    throw new Error("unknown_phase");
  }
  const summary = { phase, status: "PASS", assertions, ...(draftID ? { draftID } : {}), ...(startReceipt ? { startReceipt } : {}) };
  // 인증값·모델 원문·DOM·네트워크 원문은 보관하거나 출력하지 않는다.
  fs.appendFileSync(
    path.join(outputDir, "product-browser-summary.jsonl"),
    `${JSON.stringify({ phase, status: "PASS", assertions, ...(await diagnostic()) })}\n`,
    { mode: 0o600 },
  );
  console.log(JSON.stringify(summary));
  socket.close();
})().catch(async (error) => {
  let counts = {};
  try {
    counts = await diagnostic();
  } catch {
    // 집계를 못 읽어도 실패 사유는 그대로 남긴다
  }
  console.error(JSON.stringify({ phase, status: "FAIL", reason: error.message, ...counts }));
  process.exit(1);
});
