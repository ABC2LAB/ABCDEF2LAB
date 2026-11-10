#!/usr/bin/env node
// 실제 Chrome CDP와 Next 개발 서버를 사용한다. fetch 응답을 대체하지 않는다.
const fs = require("node:fs");
const path = require("node:path");
const [phase, ui, task] = process.argv.slice(2);
const cdp = process.env.ARTEX_FIRST_DIAGNOSIS_CDP || "http://127.0.0.1:49329";
const artifactDir = process.env.ARTEX_FIRST_DIAGNOSIS_BROWSER_OUTPUT;
if (!artifactDir) throw new Error("ARTEX_FIRST_DIAGNOSIS_BROWSER_OUTPUT에 비공개 출력 디렉터리를 지정하세요");
fs.mkdirSync(artifactDir, { recursive: true, mode: 0o700 });
for (const address of [cdp, ui]) { if (!["127.0.0.1", "localhost", "[::1]"].includes(new URL(address).hostname)) throw new Error("loopback 시험 주소만 허용"); }
let assertions = 0;
let diagnose = async () => ({});
function check(value, message) { if (!value) throw new Error(message); assertions++; }
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
(async () => {
  let targets = await (await fetch(cdp + "/json/list")).json();
  let target = targets.find(t => t.type === "page" && t.url.startsWith(ui));
  if (!target) target = await (await fetch(cdp + "/json/new?" + encodeURIComponent(ui), { method: "PUT" })).json();
  const socket = new WebSocket(target.webSocketDebuggerUrl);
  await new Promise((resolve, reject) => { socket.onopen = resolve; socket.onerror = reject; });
  let id = 0;
  const pending = new Map();
  const apiStatuses = [];
  const managementResponses = [];
  let draftBinding;
  socket.onmessage = e => {
    const message = JSON.parse(e.data);
    if (message.id) {
      const p = pending.get(message.id); if (!p) return;
      pending.delete(message.id);
      message.error ? p.reject(new Error(message.error.message)) : p.resolve(message.result);
    } else if (message.method === "Network.responseReceived" && message.params.response.url.includes("/api/")) {
      const { url, status } = message.params.response;
      apiStatuses.push({ path: new URL(url).pathname, status });
      if (new URL(url).pathname.startsWith("/api/diagnoses/v1/drafts") && status >= 200 && status < 300) managementResponses.push({requestId:message.params.requestId,path:new URL(url).pathname});
    }
  };
  const send = (method, params = {}) => new Promise((resolve, reject) => {
    const n = ++id; pending.set(n, { resolve, reject });
    socket.send(JSON.stringify({ id:n, method, params }));
  });
  const evaluate = async expression => {
    const result = await send("Runtime.evaluate", { expression, returnByValue:true, awaitPromise:true, userGesture:true });
    if (result.exceptionDetails) throw new Error("브라우저 실행 오류");
    return result.result.value;
  };
  const wait = async (expression, label, timeout = 30000) => {
    const end = Date.now() + timeout;
    while (Date.now() < end) { if (await evaluate(expression)) { assertions++; return; } await sleep(100); }
    throw new Error(label);
  };
  diagnose = async () => ({apiStatuses:[...new Map(apiStatuses.map(r=>[r.path+":"+r.status,r])).values()], ui: await evaluate("({url:location.href,inputs:Array.from(document.querySelectorAll('input')).map(e=>({id:e.id,label:e.getAttribute('aria-label'),type:e.type})),buttons:Array.from(document.querySelectorAll('button')).filter(b=>b.offsetWidth).map(b=>b.textContent.trim()),alerts:Array.from(document.querySelectorAll('[role=alert]')).map(e=>e.textContent)})")});
  const visibleText = () => evaluate("document.body.innerText");
  const textContains = text => "document.body.innerText.includes(" + JSON.stringify(text) + ")";
  const click = async text => {
    await wait("Array.from(document.querySelectorAll('button')).some(b=>b.offsetWidth && !b.disabled && b.textContent.trim()===" + JSON.stringify(text) + ")", "버튼 준비 실패: " + text);
    check(await evaluate("(()=>{const b=Array.from(document.querySelectorAll('button')).find(b=>b.offsetWidth && !b.disabled && b.textContent.trim()===" + JSON.stringify(text) + ");b?.click();return !!b})()"), "버튼 없음");
  };
  const fill = async (selector, value) => {
    check(await evaluate("(()=>{const e=document.querySelector(" + JSON.stringify(selector) + ");if(!e)return false;Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,'value').set.call(e," + JSON.stringify(value) + ");e.dispatchEvent(new Event('input',{bubbles:true}));return true})()"), "입력 필드 없음");
  };
  // URL 진입은 초안을 처음부터 연다. 닫힌 경우에만 실제 버튼으로 열고 기존 v1 폼을 확인한다.
  const openManagementDraft = async () => {
    const openButton = "Array.from(document.querySelectorAll('button')).some(b=>b.offsetWidth && !b.disabled && b.textContent.trim()==='새 진단 초안')";
    await wait("Array.from(document.querySelectorAll('button')).some(b=>b.offsetWidth && !b.disabled && ['새 진단 초안','초안 화면 닫기'].includes(b.textContent.trim()))", "draft panel toggle missing");
    if (await evaluate(openButton)) await click("새 진단 초안");
    await wait("document.querySelector('#diagnosis-profile')?.offsetWidth > 0", "visible v1 draft form missing");
    check(await evaluate("document.querySelector('#diagnosis-profile')?.closest('section')?.getAttribute('aria-label') === '새 진단 초안'"), "v1 draft form binding missing");
  };
  await send("Page.enable"); await send("Runtime.enable"); await send("Network.enable");
  await send("Browser.setDownloadBehavior", {behavior:"allow", downloadPath:artifactDir});
  await send("Browser.grantPermissions", {origin:ui, permissions:["clipboardReadWrite","clipboardSanitizedWrite"]});
  if (phase === "management-draft") {
    await send("Page.navigate", {url:ui});
    await wait("document.querySelector('#login-pass') !== null", "management login missing");
    await fill("#login-pass", "synthetic-owned-combined-fixture");
    await click("로그인");
    await openManagementDraft();
    await wait("document.querySelector('#diagnosis-profile')?.options.length > 1", "operator catalog missing");
    await click("실행 없이 초안 생성");
    await wait("document.querySelector('[role=status]')?.innerText.includes('설정 지문')", "draft response missing");
    const created = managementResponses.find(r=>r.path==="/api/diagnoses/v1/drafts");
    check(!!created, "real draft response missing");
    const response = await send("Network.getResponseBody", {requestId:created.requestId});
    const value = JSON.parse(response.base64Encoded ? Buffer.from(response.body,"base64").toString("utf8") : response.body).draft;
    check(value.state === "draft" && value.revision === 1 && /^[0-9a-f]{64}$/.test(value.source_digest), "draft binding invalid");
    draftBinding = {id:value.id,revision:value.revision,source_digest:value.source_digest};
    await click("실행 전 검증");
    await wait(textContains("운영자 승인 대기"), "unapproved draft did not wait");
    check(!apiStatuses.some(r=>/\/(start|stop)$/.test(r.path)), "draft caused start/stop");
    await send("Page.reload");
    await wait("document.querySelector('#diagnosis-task') !== null", "reload entry missing");
    await openManagementDraft();
    await wait(textContains("운영자 승인 대기"), "saved draft handle not recovered");
    check(!apiStatuses.some(r=>/\/start$/.test(r.path)), "reload caused start");
  } else if (phase === "management-start") {
    await click("실행 전 검증");
    await wait(textContains("실행 준비 확인"), "bound draft not prepared");
    check(await evaluate("Array.from(document.querySelectorAll('button')).find(b=>b.textContent.trim()==='승인된 진단 시작')?.disabled === true"), "start lacks explicit confirmation");
    check(await evaluate("(()=>{const e=document.querySelector('section[aria-label] input[type=checkbox]');e?.click();return !!e})()"), "confirmation checkbox missing");
    await click("승인된 진단 시작");
    await wait("document.querySelector('[role=status]')?.innerText.match(/실행 중|실행 마감|중지 후 마감|실행 실패/)", "start not acknowledged");
    check(apiStatuses.filter(r=>/\/start$/.test(r.path)).length === 1, "start was duplicated");
    check(!apiStatuses.some(r=>/\/api\/(models|tasks)$/.test(r.path)), "management used general APIs");
  } else if (phase === "management-report") {
    await click("초안·실행 상태 다시 조회");
    await wait(textContains("실행 마감"), "independent engine not drained");
    await click("실제 task 상태·보고서 연결");
    await wait(textContains("첫 통합 진단 보고서"), "same-origin report connection failed");
    await wait(textContains("검토 필요"), "candidate report absent");
    const began = Date.now();
    await click("종합 HTML 다운로드");
    await wait(textContains("다운로드를 요청했어요"), "management HTML download failed");
    let downloaded = false;
    for(let n=0;n<50;n++) { downloaded=fs.readdirSync(artifactDir).some(f=>f.endsWith(".html")&&fs.statSync(path.join(artifactDir,f)).mtimeMs>=began); if(downloaded)break; await sleep(100); }
    check(downloaded, "actual management HTML file absent");
    check(!apiStatuses.some(r=>/\/(start|stop)$/.test(r.path)), "report issued mutation");
  } else if (phase === "prepared") {
    await send("Page.navigate", {url:ui});
    await wait("document.querySelector('#login-pass') !== null", "로그인 화면 없음");
    await fill("#login-pass", "synthetic-owned-combined-fixture");
    await click("로그인");
    await wait("document.querySelector('#diagnosis-task') !== null", "첫 진단 연결 입력 없음");
    await fill('#diagnosis-task', task);
    await click("실행 연결");
    await wait(textContains("실행 준비됨"), "준비 run 표시 없음");
    check(!(await visibleText()).includes("예상 소요"), "예상 시간을 실제 측정으로 표시");
    const forbidden = apiStatuses.filter(r => /\/(activity|tokens|coverage|intercept)(\/|$)/.test(r.path));
    check(forbidden.length === 0, "첫 진단 화면에서 ARTEX 전용 API 호출");
  } else if (phase === "approvals") {
    // 화면 이동은 실제 네비게이션만 사용한다.
    await click("요청 승인·중지 확인");
    for (const [requestPath, decision] of [["/allowed","이 HTTP 요청만 허용"],["/denied","이 HTTP 요청 거절"]]) {
      const rowExpr = "Array.from(document.querySelectorAll('.recent-row')).find(r=>r.offsetWidth&&r.textContent.includes(" + JSON.stringify(requestPath) + ")&&r.textContent.includes('승인 대기'))";
      await wait("!!(" + rowExpr + ")", "실제 승인행 없음: " + requestPath, 14000);
      check(await evaluate("(()=>{const r=" + rowExpr + ";r?.querySelector('button')?.click();return !!r})()"), "승인행 선택 실패");
      await click(decision);
      await wait(textContains(decision.includes("허용") ? "허용 결정을 저장했어요" : "거절 결정을 저장했어요"), "결정 저장 UI 없음", 5000);
      await click("요청 상세 닫기");
    }
    await wait("Array.from(document.querySelectorAll('button')).filter(b=>b.offsetWidth && b.textContent.includes('전송 중지')).every(b=>b.disabled)", "완료 후 중지 활성");
    await click("실행 상태");
    await wait(textContains("실행 마감"), "실제 실행 마감 표시 없음");
  } else if (phase === "report") {
    // 이전 실행 서버가 닫힌 뒤 최소 권한 report 서버에서 새 로그인한다.
    await evaluate("localStorage.clear()");
    await send("Page.navigate", {url:ui + "?task=" + task + "&server=report#entry"});
    await wait("document.querySelector('#login-pass') !== null", "보고서 새 로그인 없음");
    await fill("#login-pass", "synthetic-owned-combined-fixture"); await click("로그인");
    await wait("document.querySelector('#diagnosis-task') !== null", "보고서 task 입력 없음");
    await fill('#diagnosis-task', task);
    await click("저장 결과 연결");
    await wait(textContains("수동 검토 후보"), "후보가 보고서 목록에 없음");
    await wait(textContains("검토 필요"), "후보 판정 구분 없음");
    check(await evaluate("(()=>{const root=document.querySelector('[aria-label=\"진단 판정 집계\"]');const counts=Object.fromEntries(Array.from(root.querySelectorAll('dt')).map(e=>[e.textContent.trim(),e.nextElementSibling.textContent.trim()]));return counts['검토 필요']==='1건'&&counts['확정']==='0건'})()"), "후보1/확정0 집계 불일치");
    check(!apiStatuses.some(r => /\/api\/tasks\/[^/]+$/.test(r.path)), "report 서버에서 일반 task 상세 호출");
    const downloadStarted = Date.now();
    await click("종합 HTML 다운로드");
    await wait(textContains("다운로드를 요청했어요"), "HTML 다운로드 UI 실패");
    let html = [];
    for (let n=0;n<50;n++) { html=fs.readdirSync(artifactDir).filter(f=>f.endsWith(".html") && fs.statSync(path.join(artifactDir,f)).mtimeMs >= downloadStarted); if(html.length)break; await sleep(100); }
    check(html.length > 0, "이번 브라우저 HTML 파일 저장 안 됨");
    check(fs.readFileSync(path.join(artifactDir,html[0]),"utf8").includes("<html"), "HTML 파일 내용 없음");
    await click("저장 조치·AI 수정 프롬프트");
    await wait("document.querySelector('[aria-label=\"상세 공개 범위\"]') !== null", "조치 공개 범위 없음");
    check(await evaluate("(()=>{const e=document.querySelector('[aria-label=\"상세 공개 범위\"]');e.value='internal';e.dispatchEvent(new Event('change',{bubbles:true}));return true})()"), "내부 상세 선택 실패");
    await wait(textContains("수동 검토 후보"), "조치 항목 없음");
    // 기술 입력은 선택 사항이다. 빈 정보로도 현재 증거에 결속한 조치를 생성한다.
    check(await evaluate("(()=>{const d=Array.from(document.querySelectorAll('details')).find(d=>d.textContent.includes('실제 프로젝트 수정용 맥락'));if(d)d.open=true;return !!d})()"), "조치 입력 설명 없음");
    await click("수정 제안 생성·저장");
    await wait(textContains("현재 버전 확인 후 프롬프트 복사"), "생성된 프롬프트 없음");
    await evaluate("navigator.clipboard.writeText('')");
    await click("현재 버전 확인 후 프롬프트 복사");
    await wait("document.body.innerText.includes('복사했')", "실제 복사 성공 표시 없음");
    const clipboard = await evaluate("navigator.clipboard.readText()");
    check(typeof clipboard === "string" && clipboard.length > 100, "클립보드 프롬프트 없음");
    check(apiStatuses.some(r=>r.path==="/api/report/diagnostic/remediation/prompt" && r.status===200), "복사 전 서버 검증 없음");
    check(!apiStatuses.some(r=>r.path.includes("request-approvals") || /\/api\/tasks\/[^/]+$/.test(r.path)), "report 모드에서 실행 API 호출");
  } else if (phase === "stale") {
    const previousClipboard = await evaluate("navigator.clipboard.readText()");
    await click("현재 버전 확인 후 프롬프트 복사");
    await wait(textContains("증거 또는 수정안이 바뀌었어요"), "stale 안내 없음");
    check(await evaluate("navigator.clipboard.readText()") === previousClipboard, "stale 복사가 클립보드를 변경");
    check(apiStatuses.some(r=>r.path==="/api/report/diagnostic/remediation/prompt" && r.status===409), "stale 복사 거부 없음");
  } else throw new Error("알 수 없는 시험 단계");
  const screenshot = await send("Page.captureScreenshot", {format:"png", captureBeyondViewport:false});
  fs.writeFileSync(path.join(artifactDir, phase + "-screen.png"), Buffer.from(screenshot.data,"base64"));
  fs.writeFileSync(path.join(artifactDir, phase + "-summary.json"), JSON.stringify({phase,assertions,apiStatuses}));
  console.log(JSON.stringify({phase,assertions,status:"PASS",...(draftBinding?{draftBinding}:{})}));
  socket.close();
})().catch(async error => { let detail={};try{detail=await diagnose()}catch{}console.error(JSON.stringify({phase,status:"FAIL",reason:error.message,detail}));process.exit(1); });
