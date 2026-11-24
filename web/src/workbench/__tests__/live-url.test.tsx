import { SERVER_EXTERNAL_ACCOUNT_STATEMENT } from "@/lib/diagnosis/external-account-consent-fixtures";
// live URL 진단(diagnosis-api.v2): 서버 방식 확인(catalog) → 새 점검(초안 + 범위 확인 → 확인 상자 + 시작)
// → 점검 진행(상태 조회 · 중지 · 실제 task 전송 통제) → 보고서(결과 저장 확인 뒤). 팀 lib 의 요청 함수만 모의하고
// 검증 · 결속 함수(matchURLJob · matchURLHandle · urlDiagnosisInput · 핸들 저장)는 진짜를 쓴다.

import "@testing-library/jest-dom/vitest";
import { useState } from "react";

import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// 제품 판정 목록(#801)은 이 시험 범위가 아니다 — 제품 관리 중계가 아닌 서버처럼(404) 구획을 두지 않게 한다.
// vi.fn 이 아니어서 resetAllMocks 에도 그대로다. 보고서 요약 칸 시험만 pv.reply 로 판정 응답을 준다(afterEach 가 되돌림)
const pv = vi.hoisted(() => ({ reply: null as null | (() => Promise<unknown>) }));
vi.mock("../live/productVerdicts", async (original) => {
  const real = await original<typeof import("../live/productVerdicts")>();
  const { ApiError: NotInstalled } = await import("@/lib/diagnosis/api");
  return {
    ...real,
    fetchProductVerdicts: async () => {
      if (pv.reply) return pv.reply();
      throw new NotInstalled(404, "not found");
    },
  };
});
vi.mock("@/lib/diagnosis/diagnosis-url", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/diagnosis-url")>()),
  readURLCatalog: vi.fn(),
  createURLDraft: vi.fn(),
  commandURLDiagnosis: vi.fn(),
  readURLReceipt: vi.fn(),
  readURLStatus: vi.fn(),
}));
vi.mock("@/lib/diagnosis/diagnosis-runs", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/diagnosis-runs")>()),
  readURLRunPage: vi.fn(),
}));
vi.mock("@/lib/diagnosis/api", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/api")>()),
  apiFetch: vi.fn(),
  readAuthStatus: vi.fn(),
  restoreSession: vi.fn(),
}));
vi.mock("@/lib/diagnosis/remediation", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/diagnosis/remediation")>()),
  generateRemediation: vi.fn(),
}));
vi.mock("../live/api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../live/api")>()),
  fetchRequestApprovals: vi.fn(),
  fetchRemediation: vi.fn(),
  fetchDiagnosisRun: vi.fn(),
  fetchCoverage: vi.fn(),
  fetchTasks: vi.fn(),
}));
vi.mock("../live/diagnosticCandidates", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../live/diagnosticCandidates")>()),
  fetchLiveCandidates: vi.fn(),
}));

import * as teamApi from "@/lib/diagnosis/api";
import { ApiError } from "@/lib/diagnosis/api";
import { readURLRunPage } from "@/lib/diagnosis/diagnosis-runs";
import * as url from "@/lib/diagnosis/diagnosis-url";
import {
  catalog,
  draft,
  externalModel,
  externalReady,
  handle,
  owner,
  ready,
  running,
  scope,
} from "@/lib/diagnosis/diagnosis-url-fixtures";

import * as api from "../live/api";
import { checkLiveAuth, getLiveAuthState, setLiveAuthState } from "../live/auth";
import * as cands from "../live/diagnosticCandidates";
import { LiveFeeds } from "../live/feeds";
import { clockOf, dateTimeOf } from "../live/map";
import { LiveRailTasks } from "../live/rail";
import {
  disconnectUrl,
  recheckUrl,
  resetUrl,
  runUrlAction,
  URL_POLL_MS,
  UrlFeeds,
  urlGates,
  useUrl,
} from "../live/urlDiagnosis";
import { urlAccountAuthRows, urlOutcomeText, urlStateText, urlTargetHint, urlTone, urlViewFor } from "../live/urlMap";
import { flowStageText } from "../live/workflow";
import { type Shell, ShellContext, type View } from "../shell";
import { createInitialState, useWb } from "../store";
import { Markdown } from "../ui/Markdown";
import { DashboardByMode, LiveByMode, NewScanByMode, ReportByMode, ScopeByMode } from "../views/live/byMode";
import { UrlFindingLive } from "../views/live/UrlFindingLive";
import { buildPocMarkdown, entryEp, FindingDetail, LIVE_DETAIL_POLL_MS } from "../views/live/UrlLiveLive";
import {
  BACKUP_CONFIRMED_STATEMENT,
  EXCLUDE_EXAMPLE,
  EXCLUSIONS_REVIEWED_STATEMENT,
  excludedLines,
  excludedOneLine,
} from "../views/live/UrlNewScanLive";
import { UrlFacts } from "../views/live/urlParts";
import { WorkflowCrawlLive } from "../views/live/workflowViews";
import s from "../workbench.module.css";

const u = vi.mocked(url);
const a = vi.mocked(api);
const c = vi.mocked(cands);
/** 서버 모양 그대로의 후보 쪽 (진짜 파서를 지나게 해 계약 밖 모의값이 화면에 닿지 않게). */
function candPage(ids: number[], opts: { truncated?: boolean; sev?: Record<number, string> } = {}) {
  return cands.parseLiveCandidates(
    {
      task_id: "42",
      items: ids.map((id) => ({
        candidate_id: String(id),
        state: "manual_review_required",
        observed_at: `2026-11-11T01:00:${String(id % 60).padStart(2, "0")}Z`,
        ...(opts.sev?.[id] ? { suggested_severity: opts.sev[id] } : {}),
      })),
      returned: ids.length,
      candidate_total: opts.truncated ? null : ids.length,
      truncated: !!opts.truncated,
    },
    "42",
  );
}
const noop = () => undefined;
/** 시작 확인 두 진술이 기록된 입력 칸. */
const START_CONFIRMED = { exclusions_reviewed: true, backup_confirmed: true } as const;
const exclusionsBox = () => screen.getByRole("checkbox", { name: EXCLUSIONS_REVIEWED_STATEMENT });
const backupBox = () => screen.getByRole("checkbox", { name: BACKUP_CONFIRMED_STATEMENT });
/** 오른쪽 요약 '약관 동의'에서 시작 확인 두 진술에 체크한다(사용자 11/24, 원래 2쪽 점검 설정). 둘 다 있어야 '승인 범위 확인'이 켜진다. 이미 체크한 칸은 그대로 둔다. */
function tickStartConfirm() {
  for (const box of [exclusionsBox(), backupBox()]) if (!(box as HTMLInputElement).checked) fireEvent.click(box);
}
/** 오른쪽 요약 '약관 동의'의 시작 동의(범위 확인 뒤). 같은 칸에 시작 확인 두 칸도 있어 문장으로 찾는다 */
const START_CONSENT = /위 범위로 점검을 시작합니다|이에 동의하고 비용 상한 안에서/;
const startConsent = () =>
  within(screen.getByRole("complementary", { name: "점검 요약" })).getByRole("checkbox", { name: START_CONSENT });

it("URL 입력 뒤 외부 LLM을 펼쳐 모델·키를 등록하고 범위 조회만 한다", async () => {
  u.readURLCatalog.mockResolvedValue({ ...catalog, profiles: [{ ...catalog.profiles[0], model: externalModel }] });
  vi.mocked(teamApi.apiFetch).mockImplementation(
    async (path) => (String(path).includes("targets/resolve") ? {} : { configured: true }) as never,
  );
  mount("new");
  const target = await screen.findByLabelText(/점검할 사이트 주소/);
  fireEvent.change(target, { target: { value: "https://target.example/app" } });
  // 분석 모델은 1쪽 대상 밑(사용자 11/24). 설치된 프로필이 외부 모델뿐이면 '외부 API' 가 골라져 있다
  expect(screen.getByRole("radio", { name: "외부 API" })).toBeChecked();
  // 범위 확인 전에도 외부 모델 전송은 미리 보인다 (리뷰 #507, 절대규칙 8). '범위 확인 전 기본 원칙' 안내 글은 뺐다(사용자 11/18)
  const sum = screen.getByRole("complementary", { name: "점검 요약" });
  expect(within(sum).queryByText(/^범위 확인 전 기본 원칙/)).toBeNull();
  expect(within(sum).getByText("외부 모델로 대상 응답 전송")).toBeInTheDocument();
  // 시안 #nApi: 외부 API 는 모델 대신 제공자(연결 방식 · 승인한 endpoint)와 API 키
  expect(screen.getByLabelText("제공자")).toHaveDisplayValue(`OpenRouter · ${externalModel.provider}`);
  expect(screen.queryByLabelText("모델")).toBeNull();
  const key = screen.getByLabelText("API 키");
  fireEvent.change(key, { target: { value: "synthetic-input-provider-key" } });
  fireEvent.click(screen.getByRole("button", { name: "키 등록" }));
  expect(key).toHaveValue("");
  // 1쪽 → 다음 → 2쪽(점검 설정) (시안 v64 ~ v67)
  tickStartConfirm();
  await waitFor(() => expect(screen.getAllByRole("button", { name: "승인 범위 확인" })[0]).toBeEnabled());
  expect(u.createURLDraft).not.toHaveBeenCalled();
  expect(u.commandURLDiagnosis).not.toHaveBeenCalled();
  fireEvent.click(screen.getAllByRole("button", { name: "승인 범위 확인" })[0]);
  await waitFor(() => expect(u.createURLDraft).toHaveBeenCalledTimes(1));
  const input = u.createURLDraft.mock.calls[0][0];
  expect(Object.keys(input).sort()).toEqual(["backup_confirmed", "exclusions_reviewed", "profile_id", "target_url"]);
  expect(JSON.stringify(input)).not.toContain("synthetic-input-provider-key");
  expect(u.commandURLDiagnosis).not.toHaveBeenCalled();
});

const preparing: url.URLDiagnosisJob = {
  ...ready,
  revision: 3,
  state: "preparing",
  phase: "task_creation",
  preparation_started: true,
  authority_state: "issued",
};
const completed: url.URLDiagnosisJob = {
  ...running,
  revision: 7,
  state: "completed",
  phase: "report",
  drain_complete: true,
  result_ready: true,
};

/** 서버 확인 시각(diagnosis-run-timing.responses.example.json 의 완료 사례와 같은 값, 소요 5분). */
const STARTED_AT = "2026-11-17T01:02:03.123456789Z";
const FINISHED_AT = "2026-11-17T01:07:03.123456789Z";

function shellValue(over: Partial<Shell> = {}): Shell {
  return {
    route: "new",
    navSeq: 1,
    narrow: false,
    phone: false,
    reduce: false,
    inspOpen: false,
    railOpen: false,
    railNewAtRoute: false,
    goTo: noop,
    openInsp: noop,
    closeInsp: noop,
    openRail: noop,
    closeRail: noop,
    foldRail: noop,
    dashPick: noop,
    ...over,
  };
}

/** UrlFeeds(확인 · 주기 조회) + 화면 하나. */
function mount(view: "dashboard" | "new" | "scope" | "live" | "report") {
  const goTo = vi.fn<(v: View) => void>();
  const body = {
    dashboard: <DashboardByMode />,
    new: <NewScanByMode />,
    scope: <ScopeByMode />,
    live: <LiveByMode />,
    report: <ReportByMode />,
  }[view];
  const r = render(
    <ShellContext.Provider value={shellValue({ route: view, goTo })}>
      <UrlFeeds />
      {body}
    </ShellContext.Provider>,
  );
  return { ...r, goTo };
}

/** 이 브라우저에 이어 볼 진단이 있다(팀 핸들 칸). */
function saveHandle(h: url.URLDiagnosisHandle = handle) {
  url.storeURLHandle(h);
}

const APPROVALS: api.RequestApprovalState = {
  approvals: [],
  run: {
    run_ref: "run-1",
    phase: "running",
    outcome: "",
    stop_requested: false,
    context_cancelled: false,
    drain_complete: false,
    closure_failed: false,
  },
};

/** 저장된 실행 종료 기록 (task 42, 정상 종료 · 마감 · 감사 완결). */
const RUN_RECORD: api.DiagnosisRunView = {
  kind: "run",
  savedAt: "2026-11-09T12:20:00Z",
  run: {
    ...APPROVALS.run,
    phase: "closed",
    outcome: "completed",
    drain_complete: true,
    execution: {
      approval_mode: "preauthorized_scope",
      header_mode: "none",
      version: "1",
      scope: { RunRef: "run-1", TaskRef: "42", CaseRef: "case-1", ResetRef: "reset-1", PolicyRef: "policy-1" },
      intent_id: 7,
      intent_ids: [7],
      owner_ref: "owner-1",
      policy_revision: "rev-1",
      state: "active",
      expires_at: "2030-01-01T00:00:00Z",
      reason: "",
      audit_complete: true,
      counts: {
        attempts: 3,
        allowed: 1,
        denied: 2,
        unsupported: 0,
        forward_attempted: 1,
        responses: 1,
        unknown: 0,
        calls: 3,
      },
    },
  },
};

/** 확정 발견 한 건 (팀 diagnosticOutcome 이 confirmed 로 분류하는 모양). */
function confirmed(n: number, severity?: string): api.Remediation["entries"][number] {
  return {
    ...candidate(n),
    entry_id: `finding-${n}`,
    title: `발견 ${n}`,
    outcome: "confirmed",
    verification: "stored_verification",
    severity,
  };
}

/** 정상 대조 한 건 (negative · 발견 목록에서 빠진다). */
function negative(n: number): api.Remediation["entries"][number] {
  return {
    ...candidate(n),
    entry_id: `verdict-${n}`,
    title: `정상 ${n}`,
    outcome: "negative",
    status: "allowed",
    verification: "stored_verification",
  };
}

/** 후보 한 건 (팀 diagnosticOutcome 이 review 로 분류하는 모양). */
function candidate(n: number): api.Remediation["entries"][number] {
  return {
    severity_source: "worker_estimate",
    severity_rationale: "실제 응답에서 관찰한 문서 노출의 접근통제 영향을 검토해야 합니다.",
    entry_id: `candidate-${n}`,
    title: `다른 계정 책 조회 ${n}`,
    outcome: "review",
    status: "completed",
    verification: "manual_review_required",
    evidence_digest: "e".repeat(64),
    report_version: "1",
    run: "run-1",
    intent_id: 7,
    observed_at: "2026-11-09T12:10:00Z",
    evidence_version: 1,
    source_digest: "d".repeat(64),
    summary: "다른 계정 응답 후보",
    evidence: "",
    report: "",
    remediation: "",
    available: true,
    unavailable_reason: "",
  };
}

beforeEach(() => {
  vi.resetAllMocks();
  sessionStorage.clear();
  resetUrl();
  setLiveAuthState("ready");
  useWb.setState(createInitialState());
  u.readURLCatalog.mockResolvedValue(catalog);
  u.createURLDraft.mockResolvedValue(draft);
  u.commandURLDiagnosis.mockResolvedValue(ready);
  u.readURLStatus.mockResolvedValue(ready);
  vi.mocked(readURLRunPage).mockImplementation(async () => {
    const result = u.readURLStatus.mock.results.at(-1);
    const job = result ? await result.value : ready;
    return {
      snapshot: "c".repeat(64),
      limit: 25,
      more: false,
      runs: url.readURLHandle(owner)
        ? [
            {
              id: job.id,
              revision: job.revision,
              display_name: job.input.display_name,
              state: job.state,
              phase: job.phase,
              started: job.started,
              drain_complete: job.drain_complete,
              result_ready: job.result_ready,
              task_id: job.task_id,
              // 서버 목록은 상태와 같은 저장 시각을 싣는다(diagnosis-run-timing.md) — 빠지면 목록 ↔ 상태 결속이 어긋난다
              ...(job.started_at === undefined ? {} : { started_at: job.started_at }),
              ...(job.finished_at === undefined ? {} : { finished_at: job.finished_at }),
              report_available: job.result_ready && !!job.task_id,
              approval_status: "not_revalidated",
              action_required: true,
            },
          ]
        : [],
    };
  });
  u.readURLReceipt.mockResolvedValue(draft);
  a.fetchRequestApprovals.mockResolvedValue(APPROVALS);
  a.fetchRemediation.mockResolvedValue({ task_id: 42, entries: [] });
  a.fetchDiagnosisRun.mockResolvedValue(RUN_RECORD);
  a.fetchCoverage.mockRejectedValue(new ApiError(503, "coverage disabled"));
  c.fetchLiveCandidates.mockResolvedValue(candPage([]));
  a.fetchTasks.mockResolvedValue({ active: "", tasks: [] });
  vi.mocked(teamApi.readAuthStatus).mockResolvedValue({ initialized: true });
  vi.mocked(teamApi.restoreSession).mockResolvedValue(true);
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  pv.reply = null;
});

describe("URL 진단 계산", () => {
  it("끝까지 마친 점검(completed)은 '실행 마감' 대신 '점검 완료', 나머지는 팀 lib 글 그대로", () => {
    expect(urlStateText("completed")).toBe("점검 완료");
    expect(flowStageText.completed).toBe("점검 완료");
    expect(urlStateText("cancelled")).toBe("중지됨");
    expect(urlStateText("running")).toBe("엔진 실행 중");
    expect(urlStateText("completed", false)).toBe("현재 상태 미확인");
  });
  it("주소 안내: HTTPS 만 · 계정 정보 · query/fragment · 인코딩", () => {
    expect(urlTargetHint("")).toBe("");
    expect(urlTargetHint("ftp://target.example/")).toBe("http 또는 https 주소만 받습니다.");
    expect(urlTargetHint("http://target.example/app")).toBeNull();
    expect(urlTargetHint("http://u:p@target.example/")).toBe("계정 정보(user:pass@)는 넣지 마세요.");
    expect(urlTargetHint("https://u:p@target.example/")).toBe("계정 정보(user:pass@)는 넣지 마세요.");
    expect(urlTargetHint("https://target.example/?a=1")).toBe("query · fragment(? #)는 넣지 마세요.");
    expect(urlTargetHint("https://target.example/%2e")).toBe("퍼센트 인코딩 · 역슬래시는 쓸 수 없습니다.");
    expect(urlTargetHint("https://target.example/app")).toBeNull();
  });

  it("갈 화면: 시작 전 → 새 점검(범위 확인 · 시작), 결과 저장 확인 → 보고서, 그 밖 → 점검 진행", () => {
    expect(urlViewFor(draft)).toBe("new");
    expect(urlViewFor(ready)).toBe("new");
    expect(urlViewFor(running)).toBe("live");
    expect(urlViewFor(completed)).toBe("report");
    expect(urlViewFor({ ...completed, result_ready: false })).toBe("live");
    expect(urlTone("failed")).toBe("stop");
    expect(urlTone("running")).toBe("run");
  });

  it("요청별 승인 UI가 없는 모드는 ready여도 시작하지 않는다", () => {
    const base = { handle, job: ready, verified: true, uncertain: false, busy: false };
    expect(urlGates(base).canStart).toBe(true);
    const perRequest = { ...ready, scope: { ...scope, approval_mode: "confirm_every_request" as const } };
    expect(urlGates({ ...base, job: perRequest }).canStart).toBe(false);
    expect(urlGates({ ...base, job: { ...running, scope: perRequest.scope } }).canStop).toBe(true);
  });

  it("시작 조건: 확인된 ready + 범위, 시작 키를 보낸 뒤 · 결과 불명 · 요청 중이면 막는다", () => {
    const base = { handle, job: ready, verified: true, uncertain: false, busy: false };
    expect(urlGates(base).canStart).toBe(true);
    expect(urlGates({ ...base, handle: { ...handle, startKey: "start-1", startRevision: 2 } }).canStart).toBe(false);
    expect(urlGates({ ...base, uncertain: true }).canStart).toBe(false);
    expect(urlGates({ ...base, busy: true }).canStart).toBe(false);
    expect(urlGates({ ...base, verified: false }).canStart).toBe(false);
    expect(urlGates({ ...base, job: { ...ready, scope: undefined } }).canStart).toBe(false);
    expect(urlGates({ ...base, job: running }).canStop).toBe(true);
    expect(urlGates({ ...base, job: completed }).reportReadable).toBe(true);
    expect(urlGates({ ...base, job: running }).reportReadable).toBe(false);
  });
});

describe("서버 방식 확인 (catalog)", () => {
  it("catalog 가 오면 URL 진단 폼, 서버가 받지 않는 시안 칸은 '연결 전'이지만 고칠 수 있고 서버로 보내지 않는다", async () => {
    mount("new");
    const target = await screen.findByLabelText(/점검할 사이트 주소/);
    // 1쪽: 최근 점검한 주소는 줄 없이 빈 안내. 로그인은 지금 실제 동작(로그인 없이)이 기본이고 시안처럼 바꿀 수 있다
    // '기존 점검 다시 열기' 칸은 두지 않는다(사용자 11/18)
    expect(screen.queryByText("기존 점검 다시 열기")).toBeNull();
    expect(screen.queryByRole("link", { name: "진행·시작 전 점검 목록" })).toBeNull();
    expect(screen.queryAllByRole("button", { name: /\.(com|io)$/ })).toHaveLength(0);
    // 계정 분류 · 정책 선언 칸 대신 전 로그인 칸(사용자 11/24 저녁 되돌림): 로그인 여부 · 방법(아이디 · 비밀번호 / 쿠키) · 계정 A · B
    expect(screen.queryByRole("region", { name: "계정 분류" })).toBeNull();
    expect(screen.queryByRole("region", { name: "정책 선언" })).toBeNull();
    const login = screen.getByRole("radiogroup", { name: "로그인 여부" });
    for (const r of within(login).getAllByRole("radio")) expect(r).toBeEnabled();
    expect(within(login).getByRole("radio", { name: "로그인 없이 점검" })).toBeChecked();
    expect(screen.getByText("계정이 있어야 함")).toBeVisible();
    expect(screen.queryByRole("radiogroup", { name: "로그인 방법" })).toBeNull();
    expect(screen.getAllByText(/^연결 전/).length).toBeGreaterThanOrEqual(2);
    // 로그인해서 점검: 시안처럼 방법 · 계정 칸이 나오고 입력할 수 있다
    fireEvent.click(within(login).getByRole("radio", { name: "로그인해서 점검" }));
    const how = screen.getByRole("radiogroup", { name: "로그인 방법" });
    expect(within(how).getByRole("radio", { name: "아이디 · 비밀번호" })).toBeChecked();
    const accounts = screen.getAllByLabelText(/^계정 [AB] /);
    expect(accounts).toHaveLength(4);
    for (const el of accounts) {
      expect(el).toBeEnabled();
      expect(el).toHaveValue("");
    }
    fireEvent.change(accounts[0], { target: { value: "typed-local-id" } });
    fireEvent.change(accounts[1], { target: { value: "typed-local-secret" } });
    fireEvent.click(within(how).getByRole("radio", { name: "쿠키 붙여넣기" }));
    expect(screen.getAllByPlaceholderText("Cookie: session=…")).toHaveLength(2);
    fireEvent.click(within(how).getByRole("radio", { name: "아이디 · 비밀번호" }));
    // 1쪽: LLM 서버 주소 · 점검 목표도 고를 수 있다 (지어낸 값 · 연결 상태 없음)
    fireEvent.change(target, { target: { value: "https://target.example/" } });
    expect(screen.getByLabelText("LLM 서버 주소")).toBeEnabled();
    expect(screen.getByLabelText("LLM 서버 주소")).toHaveValue("");
    expect(screen.queryByText(/연결됨/)).toBeNull();
    for (const box of within(screen.getByRole("group", { name: "점검 목표" })).getAllByRole("checkbox")) {
      expect(box).toBeEnabled();
      expect(box).not.toBeChecked();
    }
    // 꺼진 칸은 시작 조건에 들지 않는다: 주소 · 모델과 시작 확인만으로 범위 확인을 보낼 수 있다
    tickStartConfirm();
    expect(screen.getAllByRole("button", { name: "승인 범위 확인" })[0]).toBeEnabled();
    // 2쪽(점검 범위 · 워커 수 · 쓰기 메서드 요청 안내)은 없앴다(사용자 11/24)
    expect(screen.queryByRole("radiogroup", { name: "점검 범위" })).toBeNull();
    expect(screen.queryByLabelText("워커 수")).toBeNull();
    expect(screen.queryByText("쓰기 요청 범위는 설치 정책과 진단 모드가 정합니다.")).toBeNull();
    // 승인 없이 쓰기 요청을 보내는 선택지는 없다 — 실행 허용은 설치 정책 · 진단 모드가 정함 (절대규칙 3 · 6, 리뷰 #479)
    expect(screen.queryByText(/승인 없이/)).toBeNull();
    expect(screen.queryByText("전체 허용")).toBeNull();
    // 연결 전 칸에 넣은 값은 서버 요청 어디에도 실리지 않는다
    fireEvent.click(screen.getAllByRole("button", { name: "승인 범위 확인" })[0]);
    await waitFor(() => expect(u.createURLDraft).toHaveBeenCalled());
    const sent = JSON.stringify(u.createURLDraft.mock.calls);
    for (const v of ["typed-local-id", "typed-local-secret"]) expect(sent).not.toContain(v);
    // 요약 패널: 시안 줄 그대로, 서버 값이 없는 줄(로그인 · 점검 범위 · 점검 목표)은 '—' 와 연결 전 딱지
    const sum = screen.getByRole("complementary", { name: "점검 요약" });
    expect([...sum.querySelectorAll("dt")].map((dt) => dt.textContent)).toEqual([
      "사이트",
      "로그인",
      "분석 모델",
      "점검 범위",
      "점검 목표",
      "쓰기 요청",
    ]);
    for (const t of ["로그인", "점검 범위", "점검 목표"])
      expect(within(sum).getByText(t).nextElementSibling).toHaveTextContent(/^— 연결 전/);
    // 워크플로 없는 프로필은 제외 경로가 없다
    expect(screen.queryByLabelText(/^제외 경로/)).toBeNull();
    expect(u.readURLCatalog).toHaveBeenCalledTimes(1);
  });

  it("작업 이름 칸은 없고, 탐색 흐름이 있는 프로필만 '점검 설정' 칸(진단 모드 · 제외 경로)이 보인다 (시안 v39)", async () => {
    const limits = {
      format: "abc2lab.workbench-flow/v1" as const,
      max_pages: 10,
      max_endpoints: 20,
      max_response_bytes: 1000,
      allow_reversible_writes: false,
    };
    u.readURLCatalog.mockResolvedValue({ ...catalog, profiles: [{ ...catalog.profiles[0], workflow: limits }] });
    mount("new");
    const target = await screen.findByLabelText(/점검할 사이트 주소/);
    // 점검 이름은 선택 칸(사용자 11/18)
    expect(screen.getByLabelText("점검 이름 (선택)")).toHaveValue("");
    // 칸 아래 안내 문장은 두지 않는다(사용자 11/23)
    expect(screen.getByLabelText("점검 이름 (선택)")).not.toHaveAccessibleDescription();
    // 점검 설정(진단 모드 · 제외 경로 · 시작 확인)은 1쪽 맨 아래(사용자 11/24, 원래 2쪽)
    expect(screen.getByRole("heading", { name: "점검 설정" })).toBeInTheDocument();
    fireEvent.change(target, { target: { value: "https://target.example/" } });
    // 제외 경로 칸의 흐린 예시는 안전 장치 (?) 메모와 같은 주소 (시안 v50)
    // 한 줄에 쉼표로 나눠 적는다(사용자 11/24)
    expect(screen.getByLabelText(/^제외 경로/)).toHaveAttribute("placeholder", "/logout, /delete");
    // 점검 이름 없이 주소와 시작 확인만으로 범위 확인을 보낼 수 있고, 입력에 display_name 이 없다
    tickStartConfirm();
    const send = screen.getAllByRole("button", { name: "승인 범위 확인" })[0];
    expect(send).toBeEnabled();
    await act(async () => {
      fireEvent.click(send);
    });
    expect(u.createURLDraft.mock.calls[0][0]).toEqual({
      target_url: "https://target.example/",
      profile_id: "profile-1",
      workflow_mode: "user_approval_v1",
      ...START_CONFIRMED,
    });
  });

  it("점검 목표 '기타'를 고르면 옆에 직접 적는 칸이 나오고, 적은 글은 서버로 보내지 않는다 (사용자 11/24)", async () => {
    mount("new");
    fireEvent.change(await screen.findByLabelText(/점검할 사이트 주소/), {
      target: { value: "https://target.example/" },
    });
    const goals = screen.getByRole("group", { name: "점검 목표" });
    const other = within(goals).getByRole("checkbox", { name: "기타" });
    expect(other).not.toBeChecked();
    expect(screen.queryByRole("textbox", { name: "기타 점검 목표" })).toBeNull();
    fireEvent.click(other);
    const box = within(goals).getByRole("textbox", { name: "기타 점검 목표" });
    // 한 줄 칸: 제어 문자는 남지 않는다
    fireEvent.change(box, { target: { value: "쿠폰\t중복 사용" } });
    expect(box).toHaveValue("쿠폰중복 사용");
    tickStartConfirm();
    await act(async () => {
      fireEvent.click(screen.getAllByRole("button", { name: "승인 범위 확인" })[0]);
    });
    expect(JSON.stringify(u.createURLDraft.mock.calls)).not.toContain("쿠폰중복");
    // 다시 끄면 칸이 사라진다
    fireEvent.click(other);
    expect(screen.queryByRole("textbox", { name: "기타 점검 목표" })).toBeNull();
  });

  it("제외 경로는 한 줄에 쉼표로 나눠 적고, 서버에는 줄마다 하나로 보낸다 (사용자 11/24)", async () => {
    const limits = {
      format: "abc2lab.workbench-flow/v1" as const,
      max_pages: 10,
      max_endpoints: 20,
      max_response_bytes: 1000,
      allow_reversible_writes: false,
    };
    u.readURLCatalog.mockResolvedValue({ ...catalog, profiles: [{ ...catalog.profiles[0], workflow: limits }] });
    mount("new");
    fireEvent.change(await screen.findByLabelText(/점검할 사이트 주소/), {
      target: { value: "https://target.example/" },
    });
    const excl = screen.getByLabelText(/^제외 경로/);
    expect(excl.tagName).toBe("INPUT");
    // 쉼표 앞뒤 공백 · 빈 칸은 지운다
    fireEvent.change(excl, { target: { value: " /logout ,/delete, , /api/reset " } });
    expect(excl).toHaveValue(" /logout ,/delete, , /api/reset ");
    tickStartConfirm();
    await act(async () => {
      fireEvent.click(screen.getAllByRole("button", { name: "승인 범위 확인" })[0]);
    });
    expect(u.createURLDraft.mock.calls[0][0].excluded_paths).toBe("/logout\n/delete\n/api/reset");
  });

  it("제외 경로 한 줄 ↔ 줄 바꾸기는 서로 되돌아온다", () => {
    expect(excludedLines("/a, /b,/c")).toBe("/a\n/b\n/c");
    expect(excludedLines(" , ")).toBe("");
    expect(excludedOneLine("/a\n/b\n")).toBe("/a, /b");
    expect(excludedLines(excludedOneLine(EXCLUDE_EXAMPLE))).toBe(EXCLUDE_EXAMPLE);
  });

  it("점검 이름을 쓰면 앞뒤 공백을 뺀 이름이 display_name 으로 가고, 제어 문자는 칸에 남지 않는다", async () => {
    u.readURLCatalog.mockResolvedValue(catalog);
    mount("new");
    fireEvent.change(await screen.findByLabelText(/점검할 사이트 주소/), {
      target: { value: "https://target.example/" },
    });
    const name = screen.getByLabelText("점검 이름 (선택)");
    fireEvent.change(name, { target: { value: "  쇼핑몰\t주문 점검  " } });
    expect(name).toHaveValue("  쇼핑몰주문 점검  ");
    tickStartConfirm();
    await act(async () => {
      fireEvent.click(screen.getAllByRole("button", { name: "승인 범위 확인" })[0]);
    });
    expect(u.createURLDraft.mock.calls[0][0]).toMatchObject({
      target_url: "https://target.example/",
      display_name: "쇼핑몰주문 점검",
    });
  });

  // 범위 확인 전 요약의 쓰기 줄은 고른 모드를 따른다. AI 자동 모드는 대상이 쓰기를 승인했으면 건별 확인 없이
  // 보낸다(C21, #525 이경준 리뷰). 안전 장치 목록의 같은 뜻 줄은 요약 줄과 겹쳐 뺐다(사용자 11/18)
  it("범위 확인 전 쓰기 안내가 진단 모드를 따른다", async () => {
    const limits = {
      format: "abc2lab.workbench-flow/v1" as const,
      max_pages: 10,
      max_endpoints: 20,
      max_response_bytes: 1000,
      allow_reversible_writes: true,
    };
    u.readURLCatalog.mockResolvedValue({ ...catalog, profiles: [{ ...catalog.profiles[0], workflow: limits }] });
    mount("new");
    fireEvent.change(await screen.findByLabelText(/점검할 사이트 주소/), {
      target: { value: "https://target.example/" },
    });
    const sum = screen.getByRole("complementary", { name: "점검 요약" });
    expect(within(sum).getByText("쓰기 요청").nextElementSibling).toHaveTextContent("승인 후 전송");
    expect(within(sum).queryByText("쓰기 요청은 승인 후 전송")).toBeNull();
    fireEvent.click(screen.getByRole("radio", { name: /AI 자동/ }));
    expect(within(sum).getByText("쓰기 요청").nextElementSibling).toHaveTextContent(
      "건별 확인 없이 (대상 쓰기 승인 시)",
    );
    expect(within(sum).queryByText("대상이 쓰기를 승인했으면 건별 확인 없이 전송")).toBeNull();
    // 사이트 줄과 겹치는 '이 사이트에만 요청' · 늘 같은 '모든 요청 · 승인 기록 남김'도 없다
    expect(within(sum).queryByText("이 사이트에만 요청")).toBeNull();
    expect(within(sum).queryByText("모든 요청 · 승인 기록 남김")).toBeNull();
  });

  it("제외 경로 흐린 예시는 실제 입력 검사를 통과하고, query 가 든 값은 막힌다 (경로 단위)", () => {
    const mode = "user_approval_v1" as const;
    expect(url.urlDiagnosisInput("https://target.example/", "profile-1", "", mode, EXCLUDE_EXAMPLE)).toEqual({
      target_url: "https://target.example/",
      profile_id: "profile-1",
      workflow_mode: mode,
      excluded_paths: EXCLUDE_EXAMPLE,
    });
    expect(() => url.urlDiagnosisInput("https://target.example/", "profile-1", "", mode, "/delete?id=3")).toThrow();
  });

  it("탐색 흐름이 없는 프로필도 1쪽 '점검 설정'(진단 모드 '연결 전' · 시작 확인)은 보이고 제외 경로 칸은 없다", async () => {
    mount("new");
    await screen.findByLabelText(/점검할 사이트 주소/);
    expect(screen.getByRole("heading", { name: "점검 설정" })).toBeInTheDocument();
    expect(screen.getByRole("radiogroup", { name: "진단 모드" }).parentElement).toHaveTextContent("연결 전");
    expect(exclusionsBox()).toBeInTheDocument();
    expect(screen.queryByLabelText(/^제외 경로/)).toBeNull();
    expect(screen.queryByLabelText(/작업 이름/)).toBeNull();
  });

  it.each([404, 501])("%s 이면 설치 안내만 표시하고 진단 생성은 막는다", async (status) => {
    u.readURLCatalog.mockRejectedValue(new ApiError(status, "unsupported"));
    mount("new");
    expect(await screen.findByRole("alert")).toHaveTextContent("설치");
    expect(screen.queryByLabelText(/점검할 사이트 주소/)).toBeNull();
    expect(useUrl.getState().cap.kind).toBe("unavailable");
    expect(u.createURLDraft).not.toHaveBeenCalled();
  });

  it("확인 불가(503)면 어느 쪽으로도 만들지 않고, 다시 시도는 catalog 만 다시 읽는다", async () => {
    u.readURLCatalog.mockRejectedValueOnce(new ApiError(503, "preparation_service_unavailable"));
    mount("new");
    expect(await screen.findByRole("alert")).toHaveTextContent("준비 서비스의 응답을 확인하지 못했어요");
    expect(screen.queryByLabelText(/점검할 사이트 주소/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "다시 조회" }));
    expect(await screen.findByLabelText(/점검할 사이트 주소/)).toBeInTheDocument();
    expect(u.readURLCatalog).toHaveBeenCalledTimes(2);
    expect(u.createURLDraft).not.toHaveBeenCalled();
  });

  it("로그인 전에는 확인 중으로 두고 catalog 를 부르지 않는다", () => {
    setLiveAuthState("login");
    mount("new");
    expect(screen.getByRole("status")).toHaveTextContent("URL 진단 서비스를 확인하는 중");
    expect(u.readURLCatalog).not.toHaveBeenCalled();
  });
});

describe("새 점검 → 범위 확인 (URL 진단)", () => {
  it("새 점검은 한 쪽: 대상 · 분석 모델 · 로그인 · 점검 설정이 차례로, 주소 칸 Enter 는 범위 확인 제출 (2쪽 없음, 사용자 11/24)", async () => {
    mount("new");
    const target = await screen.findByLabelText(/점검할 사이트 주소/);
    const order = ["대상", "분석 모델", "로그인", "점검 설정"].map((name) => screen.getByRole("heading", { name }));
    for (let i = 1; i < order.length; i++)
      expect(order[i - 1].compareDocumentPosition(order[i]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(screen.getByRole("radiogroup", { name: "LLM 실행 위치" })).toBeVisible();
    expect(screen.getByRole("radiogroup", { name: "진단 모드" })).toBeVisible();
    // 2쪽 칸(추가 설정 · 점검 범위 · 워커 수)과 쪽 이동 버튼은 없다
    expect(screen.queryByRole("heading", { name: "추가 설정" })).toBeNull();
    expect(screen.queryByLabelText("워커 수")).toBeNull();
    expect(screen.queryByRole("button", { name: /다음/ })).toBeNull();
    expect(screen.queryByRole("button", { name: "이전" })).toBeNull();
    // 동의 칸(시작 확인 두 칸)은 오른쪽 요약 '약관 동의'에 모은다
    const summary = screen.getByRole("complementary", { name: "점검 요약" });
    expect(within(summary).getByRole("heading", { name: "약관 동의" })).toBeInTheDocument();
    expect(summary).toContainElement(exclusionsBox());
    expect(summary).toContainElement(backupBox());
    expect(screen.getByRole("group", { name: "대상 · 분석 모델 · 로그인 · 점검 설정" })).not.toContainElement(
      exclusionsBox(),
    );
    // 주소 칸 Enter(폼 제출)는 범위 확인 — 약관 동의 전에는 초안을 만들지 않는다
    fireEvent.change(target, { target: { value: "https://target.example/" } });
    fireEvent.submit(target.closest("form") as HTMLFormElement);
    expect(u.createURLDraft).not.toHaveBeenCalled();
    tickStartConfirm();
    await act(async () => {
      fireEvent.submit(target.closest("form") as HTMLFormElement);
    });
    expect(u.createURLDraft).toHaveBeenCalledTimes(1);
  });

  it("바닥 줄에는 [승인 범위 확인] 하나와 막힌 이유뿐 · 입력 묶음과 요약 패널에는 진행 버튼이 없다", async () => {
    mount("new");
    const target = await screen.findByLabelText(/점검할 사이트 주소/);
    const form = target.closest("form") as HTMLFormElement;
    const group = screen.getByRole("group", { name: "대상 · 분석 모델 · 로그인 · 점검 설정" });
    const summary = screen.getByRole("complementary", { name: "점검 요약" });
    // 화면 머리에는 [취소]만 (계정 분류 버튼은 두지 않는다)
    expect(screen.getByRole("link", { name: "취소" })).toHaveAttribute("href", "#dashboard");
    expect(screen.queryByRole("link", { name: "계정 분류" })).toBeNull();
    const send = within(form).getByRole("button", { name: "승인 범위 확인" });
    expect(send).toHaveAttribute("type", "submit");
    expect(send).toBeDisabled();
    expect(group.contains(send)).toBe(false);
    expect(within(summary).queryByRole("button", { name: /승인 범위 확인|다음/ })).toBeNull();
    expect(send.parentElement?.lastElementChild).toBe(send);
    expect(send.parentElement).toHaveTextContent("주소를 입력하세요");
    fireEvent.change(target, { target: { value: "https://target.example/" } });
    expect(send.parentElement).not.toHaveTextContent("주소를 입력하세요");
    tickStartConfirm();
    expect(send).toBeEnabled();
  });

  it("외부 API 는 설치된 외부 프로필이 없어도 눌러 칸 모양(제공자 · API 키 · 경고)을 보고, 범위 확인은 보내지 않는다", async () => {
    mount("new");
    const target = await screen.findByLabelText(/점검할 사이트 주소/);
    fireEvent.change(target, { target: { value: "https://target.example/" } });
    // 분석 모델은 1쪽 대상 밑(사용자 11/24)
    const ext = screen.getByRole("radio", { name: "외부 API" });
    expect(ext).toBeEnabled();
    fireEvent.click(ext);
    expect(ext).toBeChecked();
    expect(screen.getByLabelText("제공자")).toHaveDisplayValue("설치된 외부 모델 없음");
    expect(screen.getByText(/운영자가 외부 모델 승인/)).toBeInTheDocument();
    // 외부 제공자 전송 안내 상자는 뺐다(사용자 11/24, 팀 공유)
    expect(screen.queryByText(/대상 데이터와 프롬프트가 외부 제공자로 전송됩니다/)).toBeNull();
    const send = screen.getByRole("button", { name: "승인 범위 확인" });
    expect(send).toBeDisabled();
    expect(send.parentElement).toHaveTextContent("설치된 외부 모델 프로필이 없습니다");
    // 1쪽에서 로컬로 돌아오면 모델 칸 · 범위 확인이 그대로
    fireEvent.click(screen.getByRole("radio", { name: "로컬 실행" }));
    expect(screen.getByLabelText("모델")).toBeInTheDocument();
    tickStartConfirm();
    expect(screen.getByRole("button", { name: "승인 범위 확인" })).toBeEnabled();
  });

  it("진단 모드는 탐색 흐름이 없는 프로필에서도 보이고 고를 수 있지만 '연결 전' · 초안에 넣지 않는다", async () => {
    mount("new");
    const target = await screen.findByLabelText(/점검할 사이트 주소/);
    fireEvent.change(target, { target: { value: "https://target.example/" } });
    const modes = screen.getByRole("radiogroup", { name: "진단 모드" });
    expect(modes.parentElement).toHaveTextContent("연결 전");
    const auto = within(modes).getByRole("radio", { name: /^AI 자동/ });
    const user = within(modes).getByRole("radio", { name: /^사용자 승인/ });
    expect(auto).toBeEnabled();
    fireEvent.click(auto);
    expect(auto).toBeChecked();
    fireEvent.click(user);
    expect(user).toBeChecked();
    fireEvent.click(auto);
    // 진단 모드를 바꾸면 시작 확인이 풀리므로 모드를 정한 뒤 체크한다
    tickStartConfirm();
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "승인 범위 확인" }));
    });
    expect(u.createURLDraft.mock.calls[0][0]).toEqual({
      target_url: "https://target.example/",
      profile_id: "profile-1",
      ...START_CONFIRMED,
    });
  });

  it("범위 확인한 진단을 이어 보면 입력이 잠긴 이유와 [새로 입력]을 알린다", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue({ ...draft, revision: 2, reason_code: "approval_required" });
    mount("new");
    await waitFor(() => expect(screen.getByText(/범위 확인한 입력은 잠겨 있습니다/)).toBeInTheDocument());
    expect(screen.getByRole("button", { name: "새로 입력" })).toBeEnabled();
    expect(screen.getByLabelText(/점검할 사이트 주소/)).toBeDisabled();
  });

  it("끝난 점검을 보다가 새 점검에 오면 그 점검을 떼고 바로 빈 입력 — 잠금 안내 · [새로 입력] 없음(사용자 11/24)", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(completed);
    mount("new");
    await waitFor(() => expect(screen.getByLabelText(/점검할 사이트 주소/)).toBeEnabled());
    expect(screen.getByLabelText(/점검할 사이트 주소/)).toHaveValue("");
    expect(useUrl.getState().handle).toBeNull();
    expect(screen.queryByText(/입력이 잠겨 있습니다/)).toBeNull();
    expect(screen.queryByRole("button", { name: "새로 입력" })).toBeNull();
    fireEvent.change(screen.getByLabelText(/점검할 사이트 주소/), { target: { value: "https://target.example/" } });
    const auto = screen.getByRole("radio", { name: /^AI 자동/ });
    fireEvent.click(auto);
    expect(auto).toBeChecked();
  });

  it("범위 확인 뒤 단계(승인 범위 다시 확인)는 요약 패널에, 바닥 줄에는 진행 버튼 · 패널 여는 버튼이 없다", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue({ ...draft, revision: 2, reason_code: "approval_required" });
    mount("new");
    const again = await screen.findByRole("button", { name: "승인 범위 다시 확인" });
    const summary = screen.getByRole("complementary", { name: "점검 요약" });
    const form = (await screen.findByLabelText(/점검할 사이트 주소/)).closest("form") as HTMLFormElement;
    expect(summary.contains(again)).toBe(true);
    expect(form.contains(again)).toBe(false);
    // 패널을 접어도 바닥 줄에 패널 여는 버튼('시작 단계 보기')을 두지 않는다
    expect(within(form).queryByRole("button", { name: "시작 단계 보기" })).toBeNull();
    // 범위 확인은 이미 했고 다시 확인은 패널에 있어 바닥 줄에 진행 버튼이 없다(쪽 이동도 없음)
    expect(within(form).queryByRole("button", { name: /승인 범위|다음|이전/ })).toBeNull();
  });

  it("주소를 지우면 주소 밑에 입력 안내, 처음 빈 칸에는 띄우지 않는다 (시안 #nHost)", async () => {
    mount("new");
    const target = await screen.findByLabelText(/점검할 사이트 주소/);
    const line = document.getElementById("luHost") as HTMLElement;
    expect(line).toBeEmptyDOMElement();
    fireEvent.change(target, { target: { value: "https://target.example/" } });
    expect(line).toBeEmptyDOMElement();
    fireEvent.change(target, { target: { value: "" } });
    expect(line).toHaveTextContent("점검할 사이트 주소를 입력하세요.");
  });

  it("이어 보는 점검이 아직 도는 중이면 주소 밑에 '이미 점검 중인 사이트'와 진행 중 점검 링크", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(running);
    mount("new");
    const link = await screen.findByRole("link", { name: "진행 중 점검 보기" });
    expect(link).toHaveAttribute("href", "#running");
    expect(document.getElementById("luHost")).toHaveTextContent("이미 점검 중인 사이트");
    expect(screen.getByLabelText(/점검할 사이트 주소/)).toBeDisabled();
  });

  it("URL · 프로필로 초안을 한 번 만들고 범위 확인까지 한 뒤 서버가 정한 범위를 오른쪽에 보인다", async () => {
    // 서버는 받은 입력을 그대로 돌려준다(시작 확인 진술 포함). 입력이 다르면 화면이 응답을 버린다
    const confirmedDraft = { ...draft, input: { ...draft.input, ...START_CONFIRMED } };
    const confirmedReady = { ...ready, input: confirmedDraft.input };
    u.createURLDraft.mockResolvedValue(confirmedDraft);
    u.commandURLDiagnosis.mockResolvedValue(confirmedReady);
    u.readURLStatus.mockResolvedValue(confirmedReady);
    const { goTo } = mount("new");
    const target = (await screen.findByLabelText(/점검할 사이트 주소/)) as HTMLInputElement;
    const send = () => screen.getAllByRole("button", { name: "승인 범위 확인" });
    // 주소가 없거나 틀리면 범위 확인 버튼이 꺼져 있다(한 쪽, 사용자 11/24)
    expect(send()[0]).toBeDisabled();
    fireEvent.change(target, { target: { value: "ftp://target.example/" } });
    expect(screen.getAllByText("http 또는 https 주소만 받습니다.").length).toBeGreaterThan(0);
    expect(send()[0]).toBeDisabled();
    fireEvent.change(target, { target: { value: "https://target.example/" } });
    expect((screen.getByLabelText("모델") as HTMLSelectElement).value).toBe("profile-1");
    // 범위 확인 전에는 서버 범위가 없다
    expect(screen.queryByText("승인된 사이트 및 경로에만 요청")).toBeNull();
    // 시작 확인 두 진술(오른쪽 약관 동의)이 다 있어야 범위 확인을 보낼 수 있다: 하나만으로는 꺼져 있고, 눌러도 초안을 만들지 않는다
    expect(send()[0]).toBeDisabled();
    expect(send()[0].parentElement).toHaveTextContent("오른쪽 약관 동의 두 가지에 체크하세요");
    fireEvent.click(exclusionsBox());
    expect(send()[0]).toBeDisabled();
    fireEvent.submit(target.closest("form") as HTMLFormElement);
    expect(u.createURLDraft).not.toHaveBeenCalled();
    fireEvent.click(backupBox());
    expect(send()[0]).toBeEnabled();
    await act(async () => {
      fireEvent.click(send()[0]);
    });
    expect(u.createURLDraft).toHaveBeenCalledTimes(1);
    const [input, key, who] = u.createURLDraft.mock.calls[0];
    expect(input).toEqual({ target_url: "https://target.example/", profile_id: "profile-1", ...START_CONFIRMED });
    expect(typeof key).toBe("string");
    expect(who).toBe(owner);
    expect(u.commandURLDiagnosis).toHaveBeenCalledWith(confirmedDraft, "validate", owner);
    // 같은 화면에서 서버가 돌려준 범위 · 상한과 시작 단계가 나온다(다른 화면으로 가지 않는다)
    expect(await screen.findByText("승인된 사이트 및 경로에만 요청")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "점검 시작" })).toBeDisabled();
    expect(goTo).not.toHaveBeenCalled();
    // 조회 핸들만 남는다(영수증 키 · 결속). 입력은 잠긴다.
    const saved = url.readURLHandle(owner);
    expect(saved).toMatchObject({ creationKey: key, id: ready.id, revision: ready.revision });
    expect(target).toBeDisabled();
    expect(useUrl.getState().job?.state).toBe("ready");
  });

  it("범위 확인: 서버가 돌려준 범위를 보이고, 확인 상자를 켜야 한 번만 시작한다", async () => {
    saveHandle();
    u.commandURLDiagnosis.mockResolvedValue(preparing);
    const { goTo } = mount("new");
    expect(await screen.findByText("https://target.example · / · GET")).toBeInTheDocument();
    const summary = screen.getByRole("complementary", { name: "점검 요약" });
    // 상한 · 승인 만료는 한 줄(사용자 11/19): '요청 n회 · 시간 · 승인 만료 HH:MM'. 로컬 모델이라 비용은 없다
    const limit = within(summary).getByText("상한").parentElement as HTMLElement;
    // 시험 범위의 만료(2030-01-01)는 오늘이 아니라 날짜도 붙는다
    expect(limit.querySelector("small")?.textContent).toMatch(/^요청 3회 · 1분 · 승인 만료 \d\d-\d\d \d\d:\d\d$/);
    expect(summary).not.toHaveTextContent("모델 요청");
    const start = screen.getByRole("button", { name: "점검 시작" });
    expect(screen.getByRole("complementary", { name: "점검 요약" }).contains(start)).toBe(true);
    expect(start).toBeDisabled();
    fireEvent.click(startConsent());
    expect(start).toBeEnabled();
    await act(async () => {
      fireEvent.click(start);
      fireEvent.click(start);
    });
    expect(u.commandURLDiagnosis).toHaveBeenCalledTimes(1);
    const [job, action, who, key] = u.commandURLDiagnosis.mock.calls[0];
    expect(job).toEqual(ready);
    expect(action).toBe("start");
    expect(who).toBe(owner);
    expect(typeof key).toBe("string");
    await waitFor(() => expect(goTo).toHaveBeenCalledWith("live"));
    expect(url.readURLHandle(owner)).toMatchObject({ startKey: key, bindingLocked: true });
    // 시작 뒤에는 같은 진단을 다시 시작하지 않는다
    expect(screen.queryByRole("button", { name: "점검 시작" })).toBeNull();
  });

  it("작업대의 명시적 외부 전송·USD 동의를 lib에 전달한다", async () => {
    saveHandle();
    u.readURLCatalog.mockResolvedValue({ ...catalog, profiles: [{ ...catalog.profiles[0], model: externalModel }] });
    u.readURLStatus.mockResolvedValue(externalReady);
    u.commandURLDiagnosis.mockResolvedValue({ ...preparing, scope: externalReady.scope });
    mount("new");
    // 11/23 결정: 시험 계정이 없어도 외부 모델 회차는 대상 정보의 외부 전송과 사용자 책임을 시작 확인에서 고지한다.
    const consent = await screen.findByRole("checkbox", { name: /외부 전송된 정보에 대한 책임은 사용자에게 있습니다/ });
    expect(consent).toHaveAccessibleName(/대상이 돌려준 비밀번호·개인정보 등 포함/);
    // 서버가 돌려준 범위의 제공자는 요약 패널에 (분석 모델 칸의 제공자 고르기와 별개)
    const sum = screen.getByRole("complementary", { name: "점검 요약" });
    expect(within(sum).getByText(new RegExp(externalModel.provider.replace("/", "\\/")))).toBeInTheDocument();
    expect(consent).not.toBeChecked();
    const start = screen.getByRole("button", { name: "점검 시작" });
    expect(start).toBeDisabled();
    fireEvent.click(start);
    expect(u.commandURLDiagnosis).not.toHaveBeenCalled();
    fireEvent.click(consent);
    fireEvent.click(start);
    await waitFor(() =>
      expect(u.commandURLDiagnosis).toHaveBeenCalledWith(externalReady, "start", owner, expect.any(String), {
        externalTransferConfirmed: true,
      }),
    );
    expect(sessionStorage.getItem("abc2lab.urlDiagnosisHandle.v2")).not.toContain("externalTransferConfirmed");
  });
  it("외부 모델 + 탐색 프로필이면 외부 CDN 사전고지를 띄운다", async () => {
    const limits = {
      format: "abc2lab.workbench-flow/v1" as const,
      max_pages: 10,
      max_endpoints: 20,
      max_response_bytes: 1000,
      allow_reversible_writes: false,
    };
    saveHandle();
    u.readURLCatalog.mockResolvedValue({
      ...catalog,
      profiles: [{ ...catalog.profiles[0], model: externalModel, workflow: limits }],
    });
    u.readURLStatus.mockResolvedValue(externalReady);
    mount("new");
    expect(
      await screen.findByText(/브라우저 탐색은 외부 자원\(CDN 등\)을 외부 호스트에서 받습니다/),
    ).toBeInTheDocument();
    // 범위 확인 뒤에는 전송 제공자를, 승인된 비용 상한은 상한 줄에 함께 보인다.
    const ext = screen.getByText("외부 모델로 대상 응답 전송").parentElement as HTMLElement;
    expect(ext.querySelector("small")).toHaveTextContent(externalModel.provider);
    const limit = screen.getByText("상한").parentElement as HTMLElement;
    expect(limit.querySelector("small")).toHaveTextContent(/ · 비용 \$6 · 승인 만료 (\d\d-\d\d )?\d\d:\d\d$/);
  });
  it("로컬 모델(완전 로컬)에서는 외부 CDN 사전고지를 띄우지 않는다", async () => {
    const limits = {
      format: "abc2lab.workbench-flow/v1" as const,
      max_pages: 10,
      max_endpoints: 20,
      max_response_bytes: 1000,
      allow_reversible_writes: false,
    };
    saveHandle();
    u.readURLCatalog.mockResolvedValue({ ...catalog, profiles: [{ ...catalog.profiles[0], workflow: limits }] });
    u.readURLStatus.mockResolvedValue(ready);
    mount("new");
    // 안전 장치(scope)는 떠도 CDN 고지는 없다
    expect(await screen.findByText("승인된 사이트 및 경로에만 요청")).toBeInTheDocument();
    expect(screen.queryByText(/브라우저 탐색은 외부 자원/)).toBeNull();
  });
  it("시작 결과가 불명이면 다시 보내지 않고 상태 조회만 남긴다", async () => {
    saveHandle();
    u.commandURLDiagnosis.mockRejectedValue(new TypeError("network"));
    const { goTo } = mount("new");
    await screen.findByText("https://target.example · / · GET");
    fireEvent.click(startConsent());
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "점검 시작" }));
    });
    expect(await screen.findByRole("alert")).toHaveTextContent("자동 재전송하지 않고");
    expect(goTo).not.toHaveBeenCalled();
    expect(screen.getByText(/시작 · 모델 호출을 자동으로 다시 보내지 않습니다/)).toBeInTheDocument();
    expect(useUrl.getState().uncertain).toBe(true);
    expect(url.readURLHandle(owner)?.startKey).toBeTruthy();
    expect(u.commandURLDiagnosis).toHaveBeenCalledTimes(1);
  });

  it("매 요청 확인 범위는 안내하고 체크박스·시작 요청을 차단한다", async () => {
    saveHandle();
    const perRequest = { ...ready, scope: { ...scope, approval_mode: "confirm_every_request" as const } };
    u.readURLStatus.mockResolvedValue(perRequest);
    mount("new");
    expect(await screen.findByText(/요청마다 확인하는 프로필은 작업대에서 시작할 수 없습니다/)).toBeVisible();
    expect(startConsent()).toBeDisabled();
    expect(screen.getByRole("button", { name: "점검 시작" })).toBeDisabled();
    act(() => useUrl.setState({ confirmed: true }));
    await expect(runUrlAction("start")).resolves.toBe(false);
    expect(u.commandURLDiagnosis).not.toHaveBeenCalled();
    expect(url.readURLHandle(owner)?.startKey).toBeUndefined();
    expect(useUrl.getState().job?.scope?.approval_mode).toBe("confirm_every_request");
  });

  it("설치자 승인 대기면 사유를 보이고 시작 상자가 없다", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue({ ...draft, revision: 2, reason_code: "approval_required" });
    mount("new");
    expect(await screen.findByText(/설치자 승인이 필요해요/)).toBeInTheDocument();
    expect(
      within(screen.getByRole("complementary", { name: "점검 요약" })).queryByRole("checkbox", { name: START_CONSENT }),
    ).toBeNull();
  });

  it("이어 볼 진단이 없으면 새 점검으로 안내한다", async () => {
    mount("scope");
    expect(await screen.findByText(/이 브라우저에서 이어 보는 점검이 없습니다/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /새 점검/ })).toHaveAttribute("href", "#new");
  });
});

describe("복구 · 점검 진행 · 보고서 (URL 진단)", () => {
  it("저장된 핸들로는 GET 만 한다(초안 · 시작을 다시 보내지 않음)", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(running);
    mount("live");
    // 점검 진행의 상태 알약은 시안 글 (실행 중 = 진행 중)
    expect(await screen.findByText("진행 중", { selector: "h1 + span" })).toBeInTheDocument();
    expect(u.readURLStatus).toHaveBeenCalledWith(handle.id, owner, expect.any(AbortSignal));
    expect(u.createURLDraft).not.toHaveBeenCalled();
    expect(u.commandURLDiagnosis).not.toHaveBeenCalled();
    // 서버에 진행률 · 범위 밖 전송 집계가 없어 그 칸은 두지 않는다('—' · '연결 전' 자리도 없음). 0 % 로 지어내지 않는다
    expect(screen.queryByRole("group", { name: "점검 진행률" })).toBeNull();
    expect(screen.queryByText(/^0%$/)).toBeNull();
    expect(screen.queryByText(/범위 밖으로 보낸 요청|승인 없이 보낸 상태 변경/)).toBeNull();
    expect(screen.queryByText(/^연결 전/)).toBeNull();
  });

  it("시작을 보낸 외부 시험 계정 동의 회차도 새로고침 뒤 이어 보고(중지 가능), 다시 시작하지 않는다", async () => {
    const consentedRun = { ...running, input: { ...running.input, external_test_accounts: true as const } };
    saveHandle({ ...handle, input: consentedRun.input, startKey: "start-1", startRevision: handle.revision });
    expect(url.readURLHandle(owner)).not.toBeNull();
    u.readURLStatus.mockResolvedValue(consentedRun);
    mount("live");
    expect(await screen.findByRole("button", { name: /종료/ })).toBeEnabled();
    expect(u.readURLStatus).toHaveBeenCalledWith(handle.id, owner, expect.any(AbortSignal));
    expect(u.createURLDraft).not.toHaveBeenCalled();
    expect(u.commandURLDiagnosis).not.toHaveBeenCalled();
    expect(urlGates(useUrl.getState()).canStart).toBe(false);
  });

  it("점검 진행: 모델 사용량(입 · 출)은 가운데 상태 카드, 오른쪽 진행 요약의 커버리지는 서버 값, 시작 · 경과 · 남은 시간 · 오른쪽 발견 · 판정 칸은 없다", async () => {
    saveHandle();
    const usage = {
      requests: 3,
      input_tokens: 51_234_567,
      output_tokens: 868_400,
      reserved_input_tokens: 0,
      reserved_output_tokens: 0,
      cost_usd_micros: 0,
      usage_known: true,
    };
    u.readURLStatus.mockResolvedValue({ ...running, model_usage: usage });
    a.fetchCoverage.mockResolvedValue({
      enabled: true,
      scopeRows: 1,
      denominator: 10,
      tested: 4,
      pct: 0.4,
      byType: [],
    });
    mount("live");
    expect(await screen.findByText("진행 중", { selector: "h1 + span" })).toBeInTheDocument();
    const insp = screen.getByRole("complementary", { name: "진행 요약" });
    expect(within(insp).getByRole("heading", { level: 2, name: "진행 요약" })).toBeInTheDocument();
    // 모델 사용량: 이 진단의 예산 기록(model_usage) 입 · 출 칩 한 줄 (캐시는 없음), 가운데 상태 카드 안 (오른쪽에는 없다).
    // 제품 서버가 열지 않는 원본 토큰 집계(/api/exploration/tokens)는 부르지 않는다
    expect(screen.getByText(/^작업 합계/).parentElement).toHaveTextContent(/^작업 합계 · 입 51M 출 868k$/);
    expect(vi.mocked(teamApi.apiFetch).mock.calls.some(([path]) => String(path).includes("/exploration/tokens"))).toBe(
      false,
    );
    const line = screen.getByText(/^작업 합계/).parentElement as HTMLElement;
    expect(insp).not.toContainElement(line);
    expect(within(insp).queryByText("모델 사용량")).toBeNull();
    // 지금 하는 일과 같은 상태 카드 한 장에
    const card = screen.getByRole("region", { name: "지금 상태" });
    expect(card).toContainElement(line);
    expect(card).toContainElement(document.getElementById("lTask"));
    // 줄 하나에 두 수의 정확한 값을 한꺼번에 (칩마다 따로 두지 않음)
    expect(line).toHaveAttribute("title", "입력 51,234,567 토큰\n출력 868,400 토큰");
    expect(line.querySelectorAll("[title]")).toHaveLength(0);
    // 커버리지는 서버가 켰을 때만
    await waitFor(() => expect(within(insp).getByText("4")).toBeInTheDocument());
    expect(within(insp).getByText(/\/ 10 자산/)).toBeInTheDocument();
    expect(within(insp).getByText("40%")).toBeInTheDocument();
    // 서버가 주지 않는 시작 · 경과 · 남은 시간, 점검 중 발견 · 판정 · 결정 대기는 칸 자체가 없다
    expect(screen.queryByText(/^(시작|경과|남은 시간|토큰) /)).toBeNull();
    expect(within(insp).queryByRole("heading", { name: /^발견/ })).toBeNull();
    for (const k of ["판정", "발견 · 최근 순", "결정 대기"]) expect(within(insp).queryByText(k)).toBeNull();
    // 머리의 '발견' 버튼 대신 좁은 화면 서랍 버튼은 '진행 요약'
    expect(screen.queryByRole("button", { name: "발견" })).toBeNull();
    expect(screen.getByRole("button", { name: "진행 요약" })).toBeInTheDocument();
  });

  it("점검 진행: task A coverage 403 뒤 같은 화면에서 task B로 바뀌면 B coverage를 부른다(#851 리뷰)", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(running);
    const covB = { enabled: true, scopeRows: 1, denominator: 10, tested: 4, pct: 0.4, byType: [] };
    a.fetchCoverage.mockImplementation(async (id) => {
      if (id === "42") throw new ApiError(403, "승인된 작업의 공개 진단 API만 사용할 수 있습니다");
      return covB;
    });
    mount("live");
    const insp = await screen.findByRole("complementary", { name: "진행 요약" });
    await waitFor(() => expect(a.fetchCoverage).toHaveBeenCalledWith("42"));
    expect(within(insp).queryByText("커버리지")).toBeNull(); // 403이면 칸을 두지 않는다
    // 같은 화면(같은 컴포넌트)에서 task 가 B로 바뀐다 — A의 403이 B에 붙으면 B를 한 번도 부르지 않는다
    u.readURLStatus.mockResolvedValue({ ...running, task_id: "43" });
    act(() => useUrl.setState({ job: { ...running, task_id: "43" } }));
    await waitFor(() => expect(a.fetchCoverage).toHaveBeenCalledWith("43"));
    await waitFor(() => expect(within(insp).getByText("40%")).toBeInTheDocument());
  });
  it("점검 진행: 커버리지가 꺼졌으면 오른쪽에 커버리지 칸을 두지 않는다", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(running);
    mount("live");
    const insp = await screen.findByRole("complementary", { name: "진행 요약" });
    await waitFor(() => expect(a.fetchCoverage).toHaveBeenCalled());
    expect(within(insp).queryByText("커버리지")).toBeNull();
    expect(within(insp).queryByText(/자산/)).toBeNull();
    expect(within(insp).getByText("점검 정보")).toBeInTheDocument();
  });

  it("점검 진행 모델 사용량: 사용량을 못 받은 호출이 있으면 마우스를 올린 값에 최대치라고 적고, 기록이 없으면 줄을 두지 않는다", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue({
      ...running,
      model_usage: {
        requests: 2,
        input_tokens: 1200,
        output_tokens: 300,
        reserved_input_tokens: 0,
        reserved_output_tokens: 0,
        cost_usd_micros: 0,
        usage_known: true,
        unknown_usage_calls: 1,
      },
    });
    mount("live");
    const line = (await screen.findByText(/^작업 합계/)).parentElement as HTMLElement;
    // 예약량으로 센 호출이 있어도 점검 진행 칩 옆엔 '(최대)'(사용자 11/18) · (?) 설명(사용자 11/19)을 두지 않고,
    // 마우스를 올린 정확한 값에 최대치를 남긴다
    expect(line).toHaveTextContent(/^작업 합계 · 입 1.2k 출 300$/);
    expect(within(line).queryByRole("button")).toBeNull();
    expect(line).toHaveAttribute("title", "입력 1,200 토큰 (최대치)\n출력 300 토큰 (최대치)");
    cleanup();
    resetUrl();
    u.readURLStatus.mockResolvedValue(running);
    mount("live");
    expect(await screen.findByText("진행 중", { selector: "h1 + span" })).toBeInTheDocument();
    expect(screen.queryByText(/^작업 합계/)).toBeNull();
  });

  it("점검 진행 발견 후보: 진행 중엔 5초 쉬고 다시 읽고, 화면을 연 뒤 새로 들어온 후보에만 '새로'", async () => {
    vi.useFakeTimers();
    saveHandle();
    u.readURLStatus.mockResolvedValue(running);
    c.fetchLiveCandidates.mockResolvedValue(candPage([7], { sev: { 7: "high" } }));
    mount("live");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(c.fetchLiveCandidates).toHaveBeenCalledWith("42", expect.any(AbortSignal));
    const finds = screen.getByRole("region", { name: "발견한 취약점" });
    expect(within(finds).getByText("1건 · 최근 순")).toBeInTheDocument();
    expect(within(finds).queryByText("새로")).toBeNull();
    c.fetchLiveCandidates.mockResolvedValue(candPage([8, 7], { sev: { 7: "high" } }));
    const calls = c.fetchLiveCandidates.mock.calls.length;
    // 5초 전에는 다시 부르지 않는다(공식 측정 중 화면을 켜 둬도 제품에 부담 없게)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(cands.LIVE_CANDIDATES_POLL_MS - 1);
    });
    expect(c.fetchLiveCandidates.mock.calls.length).toBe(calls);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });
    expect(c.fetchLiveCandidates.mock.calls.length).toBe(calls + 1);
    expect(within(finds).getByText("2건 · 최근 순")).toBeInTheDocument();
    expect(within(finds).getByText("새로").closest("li")).toHaveTextContent("취약점 #8");
    expect(within(finds).getAllByText("새로")).toHaveLength(1);
    expect(within(finds).getByText("취약점 #8").closest("li")).toHaveTextContent(/^위험도 미평가/);
  });

  // 사용자 11/23: 점검 중에 나온 후보를 바로 보고 싶다 — 누르면 오른쪽에 PoC · 증거(보고서와 같은 상세)
  it("점검 진행 발견 후보를 누르면 오른쪽에 그 후보 상세를 띄우고, [진행 요약으로]로 돌아간다", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(running);
    c.fetchLiveCandidates.mockResolvedValue(candPage([2, 1]));
    a.fetchRemediation.mockResolvedValue({ task_id: 42, entries: [candidate(1)] });
    mount("live");
    const finds = await screen.findByRole("region", { name: "발견한 취약점" });
    expect(await screen.findByRole("complementary", { name: "진행 요약" })).toBeInTheDocument();
    // 후보 줄에는 무슨 취약점인지(보고서 자료의 그 후보 제목)를 붙인다 — 고르기 전에도 읽는다(사용자 11/24). 아직 저장 안 된
    // 후보는 번호 그대로
    await waitFor(() => expect(a.fetchRemediation).toHaveBeenCalledWith("42", "internal"));
    const row1 = (await within(finds).findByText("다른 계정 책 조회 1")).closest("li") as HTMLElement;
    expect(row1).toHaveTextContent(/다른 계정 책 조회 1관측/);
    expect(row1).not.toHaveTextContent("후보");
    expect(within(finds).getByText("취약점 #2").closest("li")).toHaveTextContent(/^위험도 미평가취약점 #2관측/);
    fireEvent.click(await within(finds).findByRole("button", { name: /다른 계정 책 조회 1/ }));
    expect(within(finds).getByRole("button", { name: /다른 계정 책 조회 1/ })).toHaveAttribute("aria-current", "true");
    const side = await screen.findByRole("complementary", { name: "취약점 상세" });
    expect(a.fetchRemediation).toHaveBeenCalledWith("42", "internal");
    expect(await within(side).findByRole("heading", { name: "다른 계정 책 조회 1" })).toBeInTheDocument();
    expect(side).toHaveTextContent("검토 필요");
    // 아직 저장되지 않은 후보는 그렇게 적는다('없음'으로 단정하지 않음)
    fireEvent.click(within(finds).getByRole("button", { name: /취약점 #2/ }));
    expect(await within(side).findByText(/이 취약점의 상세가 아직 저장되지 않았습니다/)).toBeInTheDocument();
    fireEvent.click(within(side).getByRole("button", { name: "진행 요약으로 돌아가기" }));
    expect(await screen.findByRole("complementary", { name: "진행 요약" })).toBeInTheDocument();
    expect(screen.queryByRole("complementary", { name: "취약점 상세" })).toBeNull();
  });

  // #874 이동찬 P2: 한 번 읽은 뒤 다시 읽기가 실패하면 오래된 상세를 최신처럼 두지 않는다
  it("후보 상세를 읽은 뒤 다시 읽기가 실패하면 갱신 실패와 [다시 읽기]를 보이고, 다시 읽으면 사라진다", async () => {
    vi.useFakeTimers();
    saveHandle();
    u.readURLStatus.mockResolvedValue(running);
    c.fetchLiveCandidates.mockResolvedValue(candPage([1]));
    a.fetchRemediation.mockResolvedValue({ task_id: 42, entries: [candidate(1)] });
    mount("live");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    fireEvent.click(
      within(screen.getByRole("region", { name: "발견한 취약점" })).getByRole("button", {
        name: /다른 계정 책 조회 1/,
      }),
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    const side = screen.getByRole("complementary", { name: "취약점 상세" });
    expect(within(side).getByRole("heading", { name: "다른 계정 책 조회 1" })).toBeInTheDocument();
    expect(within(side).queryByRole("alert")).toBeNull();
    a.fetchRemediation.mockRejectedValue(new ApiError(503, "unavailable"));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(LIVE_DETAIL_POLL_MS);
    });
    const warn = within(side).getByRole("alert");
    expect(warn).toHaveTextContent("상세를 다시 읽지 못해 갱신이 멈췄습니다 (HTTP 503). 아래는 마지막으로 읽은 상세라");
    // 마지막으로 읽은 상세는 그대로 남는다
    expect(within(side).getByRole("heading", { name: "다른 계정 책 조회 1" })).toBeInTheDocument();
    a.fetchRemediation.mockResolvedValue({ task_id: 42, entries: [candidate(1)] });
    fireEvent.click(within(warn).getByRole("button", { name: "다시 읽기" }));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(within(side).queryByText(/상세를 다시 읽지 못해/)).toBeNull();
  });

  it("점검 진행 발견 후보: 폴링 간격은 5초 이상, 문서가 숨으면 멈추고 다시 보이면 바로 읽는다", async () => {
    expect(cands.LIVE_CANDIDATES_POLL_MS).toBeGreaterThanOrEqual(5000);
    vi.useFakeTimers();
    saveHandle();
    u.readURLStatus.mockResolvedValue(running);
    mount("live");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    const before = c.fetchLiveCandidates.mock.calls.length;
    const vis = vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    try {
      await act(async () => {
        document.dispatchEvent(new Event("visibilitychange"));
        await vi.advanceTimersByTimeAsync(cands.LIVE_CANDIDATES_POLL_MS * 6);
      });
      expect(c.fetchLiveCandidates.mock.calls.length).toBe(before);
      vis.mockReturnValue("visible");
      await act(async () => {
        document.dispatchEvent(new Event("visibilitychange"));
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(c.fetchLiveCandidates.mock.calls.length).toBe(before + 1);
    } finally {
      vis.mockRestore();
    }
  });

  it("점검 진행 발견 후보: 일부만 오면(truncated) 받은 수로 '최근 N건만 표시', 전체 수는 미상", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(running);
    const ids = Array.from({ length: 200 }, (_, i) => 400 - i);
    c.fetchLiveCandidates.mockResolvedValue(candPage(ids, { truncated: true, sev: { 400: "critical" } }));
    mount("live");
    const finds = await screen.findByRole("region", { name: "발견한 취약점" });
    expect(await within(finds).findByText("최근 200건만 표시 · 전체 수 미상 · 최근 순")).toBeInTheDocument();
    expect(
      within(finds).getByText(/취약점이 많아 최근 200건만 받았고, 위 집계도 받은 200건 기준입니다/),
    ).toBeInTheDocument();
    expect((document.getElementById("lFinds") as HTMLElement).children).toHaveLength(200);
    // 상태 카드 숫자 칸도 받은 수 + '+' (받은 수를 전체로 쓰지 않는다)
    const stat = within(document.getElementById("lMeta") as HTMLElement).getByText("발견한 취약점")
      .parentElement as HTMLElement;
    expect(stat).toHaveTextContent(/^발견한 취약점200\+최근 200건만 받음$/);
  });

  it("점검 진행 발견 후보: 409 candidate_run_not_active 는 오류로 보이지 않고 폴링을 멈춘다", async () => {
    vi.useFakeTimers();
    saveHandle();
    u.readURLStatus.mockResolvedValue(running);
    c.fetchLiveCandidates.mockResolvedValue(candPage([3]));
    mount("live");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    const finds = screen.getByRole("region", { name: "발견한 취약점" });
    c.fetchLiveCandidates.mockRejectedValue(new ApiError(409, "candidate_run_not_active"));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(cands.LIVE_CANDIDATES_POLL_MS);
    });
    const calls = c.fetchLiveCandidates.mock.calls.length;
    expect(within(finds).queryByRole("alert")).toBeNull();
    expect(within(finds).getByRole("status")).toHaveTextContent(
      "점검 실행이 닫혀(시작 확인 전 · 마감 · 결과 불명) 취약점을 더 읽지 않습니다. 아래는 마지막으로 읽은 목록입니다. 끝난 점검의 결과와 판정은 보고서에서 봅니다.",
    );
    expect(within(finds).getByText("1건 · 마지막으로 읽은 목록")).toBeInTheDocument();
    expect(within(finds).queryByRole("button", { name: "다시 읽기" })).toBeNull();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(cands.LIVE_CANDIDATES_POLL_MS * 4);
    });
    expect(c.fetchLiveCandidates.mock.calls.length).toBe(calls);
  });

  it("점검 진행 발견 후보: 시작 확인 전 · 결과 불명이면 부르지 않는다(409 를 일부러 만들지 않음)", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue({ ...running, started: false, state: "stopping", phase: "closure" });
    mount("live");
    const finds = await screen.findByRole("region", { name: "발견한 취약점" });
    expect(within(finds).getByText("점검 시작이 확인되면 취약점을 읽습니다.")).toBeInTheDocument();
    expect(c.fetchLiveCandidates).not.toHaveBeenCalled();
  });

  it("점검 진행 발견 후보: 응답이 계약과 다르면 일부만 그리지 않고 실패로 알린다", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(running);
    c.fetchLiveCandidates.mockImplementation(async () =>
      cands.parseLiveCandidates({ task_id: "42", items: [], returned: 0, candidate_total: 3, truncated: false }, "42"),
    );
    mount("live");
    const finds = await screen.findByRole("region", { name: "발견한 취약점" });
    expect(await within(finds).findByRole("alert")).toHaveTextContent(
      "발견한 취약점을 읽지 못했습니다 (HTTP 422). 서버 응답 형식이 계약과 달라 취약점을 표시하지 않습니다.",
    );
    expect(document.getElementById("lFinds")).toBeNull();
  });

  it("점검 진행 발견 후보: 한 번 읽은 뒤 다시 읽다 실패(503)해도 숨기지 않고, 마지막 목록이라고 알리며 다시 읽을 수 있다", async () => {
    vi.useFakeTimers();
    saveHandle();
    u.readURLStatus.mockResolvedValue(running);
    mount("live");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    const finds = screen.getByRole("region", { name: "발견한 취약점" });
    expect(within(finds).getByText("0건 · 최근 순")).toBeInTheDocument();
    c.fetchLiveCandidates.mockRejectedValue(new ApiError(503, "candidate_read_unavailable"));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(cands.LIVE_CANDIDATES_POLL_MS);
    });
    expect(within(finds).getByRole("alert")).toHaveTextContent(
      "발견한 취약점을 다시 읽지 못해 갱신이 멈췄습니다 (HTTP 503). 취약점 저장 근거를 지금 읽을 수 없습니다. 아래는 마지막으로 읽은 목록이라 최신이 아닐 수 있습니다.",
    );
    expect(within(finds).getByText("0건 · 마지막으로 읽은 목록")).toBeInTheDocument();
    const calls = c.fetchLiveCandidates.mock.calls.length;
    c.fetchLiveCandidates.mockResolvedValue(candPage([]));
    await act(async () => {
      fireEvent.click(within(finds).getByRole("button", { name: "다시 읽기" }));
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(c.fetchLiveCandidates.mock.calls.length).toBe(calls + 1);
    expect(within(finds).queryByRole("alert")).toBeNull();
    expect(within(finds).getByText("0건 · 최근 순")).toBeInTheDocument();
  });

  it("탐색 흐름 없는 점검은 점검 중에만 서버 진단 활동을 두고(활동 기록 대신), 끝나면 두지 않는다", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(running);
    const { unmount } = mount("live");
    expect(await screen.findByRole("region", { name: "서버 진단 활동" })).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "활동 기록" })).toBeNull();
    unmount();
    resetUrl();
    u.readURLStatus.mockResolvedValue(completed);
    mount("live");
    expect(await screen.findByText(/점검을 마쳤습니다/)).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "서버 진단 활동" })).toBeNull();
  });

  it("점검 진행 화면은 '종료' 하나만 두고 확인 전에는 보내지 않는다", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(running);
    mount("live");
    const stop = await screen.findByRole("button", { name: "종료" });
    expect(screen.getAllByRole("button", { name: "종료" })).toHaveLength(1);
    expect(screen.queryByRole("button", { name: /^(중지|일시정지|다시 시작|재개)$/ })).toBeNull();
    fireEvent.click(stop);
    const dialog = screen.getByRole("alertdialog", { name: /점검을 종료할까요/ });
    expect(dialog).not.toHaveTextContent("일시정지를 쓰세요");
    expect(u.commandURLDiagnosis).not.toHaveBeenCalled();
    fireEvent.click(within(dialog).getByRole("button", { name: "계속 점검" }));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(u.commandURLDiagnosis).not.toHaveBeenCalled();
  });
  it("서버 진단 활동 줄은 순번 · 시각 · 주체 · 의도 · 오류만 보이고 원문은 없다고 말한다", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(running);
    const ts = "2026-11-19T01:02:03Z";
    vi.mocked(teamApi.apiFetch).mockImplementation(async (path) =>
      String(path).startsWith("/api/exploration/activity?")
        ? ({
            items: [
              {
                seq: 3,
                ts,
                worker: "planner",
                intent_id: "7",
                kind: "status",
                is_error: true,
                summary: "진단 활동 기록",
              },
              { seq: 4, ts: "", worker: "unknown-role", intent_id: "", kind: "status", is_error: false, summary: "" },
            ],
            cursor: 4,
          } as never)
        : (undefined as never),
    );
    mount("live");
    const region = await screen.findByRole("region", { name: "서버 진단 활동" });
    await waitFor(() => expect(within(region).getAllByRole("listitem")).toHaveLength(2));
    const [newest, older] = within(region).getAllByRole("listitem");
    expect(older).toHaveTextContent(`#3 ${clockOf(ts)} · 계획 · 의도 #7 · 진단 활동 기록 · 오류`);
    // 시각 · 주체 · 의도가 없으면 그 칸을 두지 않는다
    expect(newest).toHaveTextContent(/^#4 · 진단 활동 기록$/);
    expect(region).toHaveTextContent(
      "원문 명령 · 모델 글 · 요청 내용은 비밀이 섞일 수 있어 공개 활동에 싣지 않습니다.",
    );
  });

  it("점검 진행 발견 후보가 없으면 '없음'을 안전으로 읽히지 않게 말한다", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(running);
    mount("live");
    const finds = await screen.findByRole("region", { name: "발견한 취약점" });
    expect(
      await within(finds).findByText("아직 나온 취약점이 없습니다. 없다고 안전하다는 뜻은 아닙니다."),
    ).toBeInTheDocument();
  });

  it("영수증만 있으면(id 없음) 생성 영수증을 조회한다", async () => {
    saveHandle({ ownerContext: owner, creationKey: "create-1", input: draft.input });
    mount("new");
    await waitFor(() => expect(u.readURLReceipt).toHaveBeenCalledWith("create-1", owner));
    expect(u.createURLDraft).not.toHaveBeenCalled();
    // 이어 보는 진단이 있으면 입력을 잠그고 그 주소를 보인다
    expect(await screen.findByRole("button", { name: "새로 입력" })).toBeInTheDocument();
    expect(screen.getByLabelText(/점검할 사이트 주소/)).toBeDisabled();
  });

  it("조회 권한이 없으면(403) 핸들을 지운다", async () => {
    saveHandle();
    u.readURLStatus.mockRejectedValue(new ApiError(403, "forbidden"));
    mount("live");
    expect(await screen.findByText(/이 진단을 조회하거나 실행할 권한이 없어요/)).toBeInTheDocument();
    expect(url.readURLHandle(owner)).toBeNull();
  });

  it("진행 중이면 상태만 주기적으로 다시 읽고, 실제 task 의 전송 통제를 부른다", async () => {
    vi.useFakeTimers();
    saveHandle();
    u.readURLStatus.mockResolvedValue(running);
    mount("live");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(u.readURLStatus).toHaveBeenCalledTimes(1);
    expect(a.fetchRequestApprovals).toHaveBeenCalledWith("42");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(URL_POLL_MS);
    });
    expect(u.readURLStatus).toHaveBeenCalledTimes(2);
    expect(u.commandURLDiagnosis).not.toHaveBeenCalled();
  });

  it("주기 조회 중에도 종료 버튼은 사라지지 않고(깜빡임 없음), 그 사이 누른 중지는 조회가 끝난 뒤 한 번만 보낸다", async () => {
    vi.useFakeTimers();
    saveHandle();
    u.readURLStatus.mockResolvedValue(running);
    mount("live");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    const stop = screen.getByRole("button", { name: /종료/ });
    // 다음 주기 조회를 붙잡아 둔다
    let release: (job: typeof running) => void = () => undefined;
    u.readURLStatus.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          release = resolve;
        }),
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(URL_POLL_MS);
    });
    expect(u.readURLStatus).toHaveBeenCalledTimes(2);
    expect(useUrl.getState().busy).toBe(false);
    expect(stop).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /종료/ })).toBeEnabled();
    // 조회 중에 누른 중지는 버리지 않고 기다린다
    u.commandURLDiagnosis.mockResolvedValue({ ...running, revision: running.revision + 1 });
    let sent: Promise<boolean> = Promise.resolve(false);
    act(() => {
      sent = runUrlAction("stop");
    });
    expect(u.commandURLDiagnosis).not.toHaveBeenCalled();
    await act(async () => {
      u.readURLStatus.mockResolvedValue(running);
      release(running);
      await sent;
    });
    expect(u.commandURLDiagnosis).toHaveBeenCalledTimes(1);
    expect(u.commandURLDiagnosis.mock.calls[0][1]).toBe("stop");
  });

  /** 주기 조회 하나를 붙잡아 두고, 그 사이에 중지를 누른 상태로 만든다. 붙잡은 조회를 풀 함수와 중지 약속을 준다. */
  async function stopDuringQuietRefresh(next: (id: string) => Promise<url.URLDiagnosisJob> | null = () => null) {
    vi.useFakeTimers();
    saveHandle();
    u.readURLStatus.mockResolvedValue(running);
    mount("live");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    let release: (job: typeof running) => void = () => undefined;
    u.readURLStatus.mockImplementation(
      (id: string) =>
        next(id) ??
        new Promise((resolve) => {
          release = resolve;
        }),
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(URL_POLL_MS);
    });
    u.commandURLDiagnosis.mockResolvedValue({ ...running, revision: running.revision + 1 });
    let sent: Promise<boolean> = Promise.resolve(false);
    act(() => {
      sent = runUrlAction("stop");
    });
    return { release: (job: typeof running) => release(job), sent: () => sent };
  }

  it("조회를 기다리는 사이 다른 진단으로 다시 붙었으면(진단 교체) 기다리던 중지는 새 진단으로 보내지 않는다 (리뷰 #601)", async () => {
    const other = { ...running, id: "job-2" };
    const pending = await stopDuringQuietRefresh((id) => (id === other.id ? Promise.resolve(other) : null));
    // 다른 탭에서 저장한 job-2 핸들로 다시 확인 → 재연결
    url.storeURLHandle({ ...handle, id: other.id });
    await act(async () => {
      recheckUrl();
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(useUrl.getState().job?.id).toBe(other.id);
    await act(async () => {
      pending.release(running);
      await pending.sent();
    });
    expect(await pending.sent()).toBe(false);
    expect(u.commandURLDiagnosis).not.toHaveBeenCalled();
  });

  it("조회를 기다리는 사이 로그아웃했으면 기다리던 중지는 보내지 않는다 (리뷰 #601)", async () => {
    const pending = await stopDuringQuietRefresh();
    await act(async () => {
      setLiveAuthState("login");
      await vi.advanceTimersByTimeAsync(0);
    });
    await act(async () => {
      pending.release(running);
      await pending.sent();
    });
    expect(await pending.sent()).toBe(false);
    expect(u.commandURLDiagnosis).not.toHaveBeenCalled();
  });

  it("이미 진행 중인 요청별 확인 회차는 실제 요청 승인 목록과 중지를 제공한다", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue({
      ...running,
      scope: { ...scope, approval_mode: "confirm_every_request" },
    });
    mount("live");
    expect(await screen.findByRole("button", { name: /종료/ })).toBeEnabled();
    expect(screen.queryByText(/이 작업대에서는 요청별 허용·거절을 할 수 없습니다/)).toBeNull();
    await waitFor(() => expect(a.fetchRequestApprovals).toHaveBeenCalledWith("42"));
    expect(u.commandURLDiagnosis).not.toHaveBeenCalled();
  });

  it("전송 통제: 허용 판정을 실제 전송으로 보이지 않는다 (allowed 1 · 전송 시도 0)", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(running);
    a.fetchRequestApprovals.mockResolvedValue({
      approvals: [],
      run: {
        ...APPROVALS.run,
        execution: {
          ...(RUN_RECORD.kind === "run" ? RUN_RECORD.run.execution : undefined),
          counts: {
            attempts: 1,
            allowed: 1,
            denied: 0,
            unsupported: 0,
            forward_attempted: 0,
            responses: 0,
            unknown: 0,
            calls: 1,
          },
        } as api.RequestApprovalRun["execution"],
      },
    });
    const { container } = mount("live");
    // 큰 숫자는 실제 대상 전송(forward_attempted)이고, 허용 판정은 따로 작게 보인다
    await waitFor(() => expect(container.querySelector("#luAllowed")).toHaveTextContent("0"));
    const cell = container.querySelector("#luAllowed")?.parentElement as HTMLElement;
    expect(within(cell).getByText("허용 범위 안 전송")).toBeInTheDocument();
    expect(within(cell).getByText("허용 판정 1")).toBeInTheDocument();
  });

  it("결과 불명(unknown · 마감 미확인)을 끝난 진단으로 안내하지 않는다 — 화면 · 레일", async () => {
    const unknownJob: url.URLDiagnosisJob = { ...running, revision: 6, state: "unknown" };
    saveHandle();
    u.readURLStatus.mockResolvedValue(unknownJob);
    const goTo = vi.fn<(v: View) => void>();
    render(
      <ShellContext.Provider value={shellValue({ route: "live", goTo })}>
        <UrlFeeds />
        <LiveByMode />
        <LiveRailTasks />
      </ShellContext.Provider>,
    );
    const note = await screen.findByText(/결과 불명 · 끝난 것으로 보지 않고 자동으로 다시 시작하지 않습니다/);
    expect(note).toHaveTextContent("엔진 시작 확인됨 · 마감 미확인");
    expect(screen.queryByText(/점검이 끝났습니다/)).toBeNull();
    expect(screen.queryByText(/점검을 마쳤습니다/)).toBeNull();
    expect(screen.getByText(/결과 불명인 점검은 전송 상태를 이어 조회하지 않습니다/)).toBeInTheDocument();
    expect(a.fetchRequestApprovals).not.toHaveBeenCalled();
    // 레일: 최근 끝난 점검이 아니라 위(진행 중) 묶음에
    expect(screen.getByText("끝난 점검 없음")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /target\.example/ })).toHaveTextContent("결과 불명");
    // 종료는 보내지 않는다(준비 · 실행 중만)
    expect(screen.queryByRole("button", { name: /종료/ })).toBeNull();
  });

  it("점검 정보는 대상 · 방식 · 모델만 줄로 — 진행 확인 · 기술 정보(진단 ID · task · 설정 지문 · 실행 권위)는 두지 않는다", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(completed);
    mount("live");
    const insp = await screen.findByRole("complementary", { name: "진행 요약" });
    const facts = within(insp).getByRole("heading", { name: "점검 정보" }).parentElement as HTMLElement;
    expect(facts.querySelector("dl")).toHaveTextContent(/^대상target\.example방식설치 범위모델합성 모델 · 로컬$/);
    expect(facts).not.toHaveTextContent(/진행 확인|기술 정보|#42|실행 권위|설정 지문|job-1/);
    expect(facts.querySelector("details")).toBeNull();
  });

  it("마감된 진단은 요청 승인을 다시 부르지 않고 전송 상태를 더 조회하지 않는다고 안내한다", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(completed);
    const { goTo } = mount("live");
    expect(await screen.findByText(/점검을 마쳤습니다/)).toBeInTheDocument();
    // 완료 카드를 따로 두지 않고 상태 카드 한 장에: 완료 딱지 · 점검을 마쳤습니다 · 결과 저장 확인 · 보고서 보기
    const card = screen.getByRole("region", { name: "지금 상태" });
    expect(document.getElementById("lTask")).toHaveTextContent(/^점검을 마쳤습니다$/);
    expect(card).toHaveTextContent(/완료점검을 마쳤습니다보고서 보기.*결과 저장 확인/);
    expect(screen.getAllByText(/점검을 마쳤습니다/)).toHaveLength(1);
    expect(screen.queryByText("실행을 마감했습니다")).toBeNull();
    expect(screen.getByText(/점검이 끝나 전송 상태를 더 조회하지 않습니다/)).toBeInTheDocument();
    expect(a.fetchRequestApprovals).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: /보고서 보기/ }));
    expect(goTo).toHaveBeenCalledWith("report");
  });

  it.each([
    [
      "partial",
      { done: 1, policy_limited: 0, incomplete: 2, unknown: 0, stopped: 0 },
      "일부만 점검하고 마감했습니다",
      "일부 완료 · 남은 작업 2",
    ],
    [
      "no_intents",
      { done: 0, policy_limited: 0, incomplete: 0, unknown: 0, stopped: 0 },
      "검사하지 않고 마감했습니다",
      "시도한 계획 없음(검사 안 함)",
    ],
  ] as const)("completed + %s 는 '점검을 마쳤습니다'·완료 톤 대신 실행 결과를 같이 보인다", async (outcome, counts, task, line) => {
    saveHandle();
    u.readURLStatus.mockResolvedValue({ ...completed, outcome, intent_outcomes: counts });
    mount("live");
    expect(await screen.findByText(task)).toBeInTheDocument();
    const card = screen.getByRole("region", { name: "지금 상태" });
    expect(card).toHaveTextContent(line);
    expect(card).toHaveTextContent("마감");
    expect(card).not.toHaveTextContent("완료점검");
    expect(card).not.toHaveClass(s.ok);
    expect(screen.queryByText(/점검을 마쳤습니다/)).toBeNull();
  });

  it("끝난 점검의 발견은 저장된 결과를 보고서 '발견 목록'과 같은 줄로 보이고(점검 중 발견 목록은 안 부름), 줄을 누르면 오른쪽에 상세", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(completed);
    a.fetchRemediation.mockResolvedValue({
      task_id: 42,
      entries: [
        { ...candidate(1), vuln_class: "idor", context: { endpoint: "/api/items/{id}", observations: [] } },
        candidate(2),
      ],
    });
    const { goTo } = mount("live");
    const sec = await screen.findByRole("region", { name: "발견 목록" });
    // 머리 수는 'n건'만('· 저장된 결과'는 뺐다, 사용자 11/18)
    await waitFor(() => expect(within(sec).getByText("2건")).toBeInTheDocument());
    expect(sec).not.toHaveTextContent("저장된 결과");
    // 보고서와 같은 줄: 위험도(없으면 '등급 없음') · 제목 · 엔드포인트 · 유형 · 판정. 누르면 오른쪽에 상세(사용자 11/23 —
    // 11/17 의 '읽기 줄'에서 바뀜)
    expect(within(sec).queryAllByRole("button")).toHaveLength(2);
    const rows = [...(document.getElementById("lSaved") as HTMLElement).children] as HTMLElement[];
    expect(rows).toHaveLength(2);
    expect(rows[0].tagName).toBe("BUTTON");
    expect(rows[0]).toHaveTextContent(/^위험도 미평가다른 계정 책 조회 1\/api\/items\/\{id\}idor검토 필요$/);
    expect(a.fetchRemediation).toHaveBeenCalledWith("42", "internal");
    // 끝난 점검은 제품 서버가 점검 중 후보 조회를 닫으므로(409) 부르지 않는다. '점검이 끝나 진행 중 후보 목록을 …' 안내
    // 문장은 뺐다(사용자 11/23)
    expect(c.fetchLiveCandidates).not.toHaveBeenCalled();
    expect(screen.queryByRole("region", { name: "발견한 취약점" })).toBeNull();
    expect(document.getElementById("lFindsEnded")).toBeNull();
    expect(document.body).not.toHaveTextContent("점검이 끝나 진행 중 후보 목록");
    // 제품 판정 경로가 없는 서버(404)는 기존 합계만 표시한다.
    await waitFor(() =>
      expect(within(document.getElementById("lMeta") as HTMLElement).getByText("발견").parentElement).toHaveTextContent(
        /^발견2판정 확정 0$/,
      ),
    );
    fireEvent.click(rows[1]);
    expect(goTo).not.toHaveBeenCalledWith("report");
    const side = await screen.findByRole("complementary", { name: "취약점 상세" });
    expect(within(side).getByRole("heading", { name: "다른 계정 책 조회 2" })).toBeInTheDocument();
    // 끝난 점검은 이미 읽은 저장 결과를 쓴다(다시 부르지 않음)
    expect(a.fetchRemediation).toHaveBeenCalledTimes(1);
    fireEvent.click(within(side).getByRole("button", { name: "진행 요약으로 돌아가기" }));
    expect(await screen.findByRole("complementary", { name: "진행 요약" })).toBeInTheDocument();
  });

  it("중지 요청은 같은 진단에 한 번 (expected revision 은 팀 lib 이 넣는다)", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(running);
    u.commandURLDiagnosis.mockResolvedValue({ ...running, revision: 6, state: "stopping" });
    mount("live");
    // 종료는 되돌릴 수 없어 확인 상자를 거친다. 머리 버튼만으로는 보내지 않는다
    fireEvent.click(await screen.findByRole("button", { name: /종료/ }));
    expect(u.commandURLDiagnosis).not.toHaveBeenCalled();
    const dialog = screen.getByRole("alertdialog");
    await act(async () => {
      fireEvent.click(within(dialog).getByRole("button", { name: "종료" }));
    });
    expect(u.commandURLDiagnosis).toHaveBeenCalledTimes(1);
    expect(u.commandURLDiagnosis).toHaveBeenCalledWith(running, "stop", owner);
    expect(await screen.findByText("중지·마감 중")).toBeInTheDocument();
  });

  it("보고서 머리: 서버 시작 · 마감 시각이 둘 다 있으면 시작 시각과 소요(finished − started)를 보인다", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue({ ...completed, started_at: STARTED_AT, finished_at: FINISHED_AT });
    a.fetchRemediation.mockResolvedValue({ task_id: 42, entries: [] });
    mount("report");
    const header = (await screen.findByRole("heading", { level: 1, name: "target.example 보고서" })).closest(
      "header",
    ) as HTMLElement;
    await waitFor(() => expect(within(header).getByText(/^소요/)).toHaveTextContent(/^소요 5분$/));
    expect(within(header).getByText(/^시작/)).toHaveTextContent(`시작 ${dateTimeOf(STARTED_AT)}`);
    expect(header).not.toHaveTextContent("연결 전");
  });

  it.each([
    ["mixed_integrity", "판정 확정1증거 충분 · 확정 단계 확정 1"],
    ["source_not_found", "판정 확정0증거 충분"],
  ])("보고서 요약에 검증된 제품 확정만 합산한다 (%s)", async (fixture, summary) => {
    const { parseProductVerdicts } = await import("../live/productVerdicts");
    const golden = (await import("./product-verdicts.server-golden.json")).default as Record<
      string,
      { task_id: string }
    >;
    // 당시 원본 미확인 행에는 confirmed 값이 있어도 요약에 더해지면 안 된다.
    pv.reply = async () => parseProductVerdicts(golden[fixture], golden[fixture].task_id);
    saveHandle();
    u.readURLStatus.mockResolvedValue(completed);
    a.fetchRemediation.mockResolvedValue({ task_id: 42, entries: [candidate(1)] });
    mount("report");
    const strip = await screen.findByRole("region", { name: "판정 집계" });
    const cell = within(strip).getByText("판정 확정").parentElement as HTMLElement;
    if (fixture === "source_not_found") {
      expect(await screen.findAllByText(/당시 원본 미확인 · 당시 정책 동결본/)).toHaveLength(2);
    }
    await waitFor(() => expect(cell.textContent).toBe(summary));
  });

  it("끝난 점검의 진행 요약에도 검증된 제품 확정 수를 합산한다", async () => {
    const { parseProductVerdicts } = await import("../live/productVerdicts");
    const { mixed_integrity: golden } = (await import("./product-verdicts.server-golden.json")).default;
    pv.reply = async () => parseProductVerdicts(golden, golden.task_id);
    saveHandle();
    u.readURLStatus.mockResolvedValue(completed);
    a.fetchRemediation.mockResolvedValue({ task_id: 42, entries: [candidate(1)] });
    mount("live");
    await waitFor(() =>
      expect(within(document.getElementById("lMeta") as HTMLElement).getByText("발견").parentElement).toHaveTextContent(
        /^발견1판정 확정 1$/,
      ),
    );
  });

  it.each(["live", "report"] as const)("제품 판정 조회 중과 503 실패를 확정 합계에 표시한다 (%s)", async (view) => {
    const { ApiError } = await import("@/lib/diagnosis/api");
    const pending = Promise.withResolvers<never>();
    pv.reply = () => pending.promise;
    saveHandle();
    u.readURLStatus.mockResolvedValue(completed);
    a.fetchRemediation.mockResolvedValue({ task_id: 42, entries: [confirmed(1)] });
    mount(view);
    const summary =
      view === "live"
        ? await within(await screen.findByRole("region", { name: "지금 상태" })).findByText("발견")
        : within(await screen.findByRole("region", { name: "판정 집계" })).getByText("판정 확정");
    const cell = summary.parentElement as HTMLElement;
    const known = view === "live" ? "판정 확정 1" : "판정 확정1";
    expect(cell).toHaveTextContent(known);
    expect(cell).toHaveTextContent("확정 단계 판정 읽는 중");

    await act(async () => pending.reject(new ApiError(503, "unavailable")));

    const unreadable = "확정 단계 판정 읽지 못함 · 합계에 빠짐";
    expect(cell).toHaveTextContent(known);
    expect(cell).toHaveTextContent(unreadable);
    expect(cell).not.toHaveTextContent("읽는 중");
    if (view === "report") {
      fireEvent.click(screen.getByRole("tab", { name: "접근통제 지도" }));
      expect(cell).toBeVisible();
      expect(cell).toHaveTextContent(unreadable);
      fireEvent.click(screen.getByRole("tab", { name: "보고서" }));
      expect(screen.getByRole("region", { name: "취약점 점검 보고서" })).toHaveTextContent(unreadable);
    }
  });

  it("보고서 Markdown의 합계와 제품 확정 출처를 함께 표시한다", () => {
    const md = buildPocMarkdown([candidate(1)], {
      targetUrl: "https://target.example/",
      mode: null,
      model: null,
      excluded: null,
      crawl: null,
      productConfirmed: 2,
    });
    expect(md).toContain("- 취약점: 확정 2건 · 검토 필요 1건");
    expect(md).toContain("- 제품 확정 판정: 2건");
    expect(md).not.toContain("제품 E③ 확정 단계");
  });

  it("보고서는 결과 저장이 확인된 뒤에만 실제 task 의 조치 자료를 부른다", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(running);
    mount("report");
    expect(await screen.findByText(/결과가 저장되면 보고서가 나옵니다/)).toBeInTheDocument();
    expect(a.fetchRemediation).not.toHaveBeenCalled();
    expect(a.fetchCoverage).not.toHaveBeenCalled();
    cleanup();
    resetUrl();
    u.readURLStatus.mockResolvedValue(completed);
    // 맥락에 경로 템플릿 · 관찰 메서드가 있으면 시안 ep(m,p) 처럼 줄 · 상세에 엔드포인트를 보인다
    a.fetchRemediation.mockResolvedValue({
      task_id: 42,
      entries: [
        {
          ...candidate(1),
          vuln_class: "idor",
          context: { endpoint: "/api/items/{id}", observations: [{ endpoint: "/api/items/{id}", method: "get" }] },
        },
      ],
    });
    a.fetchCoverage.mockResolvedValue({
      enabled: true,
      scopeRows: 1,
      denominator: 10,
      tested: 4,
      pct: 0.4,
      byType: [],
    });
    mount("report");
    expect(await screen.findByRole("heading", { level: 1, name: "target.example 보고서" })).toBeInTheDocument();
    // 우선 조치 칸은 두지 않는다 (사용자 11/17)
    expect(screen.queryByRole("heading", { name: "우선 조치" })).toBeNull();
    // 위치: 끝난 점검 / 주소 (시안: 보고서 이름은 제목에만, "작업대" 뿌리는 뺐다)
    const crumb = screen.getByRole("navigation", { name: "위치" });
    expect(within(crumb).getByRole("link", { name: "끝난 점검" })).toHaveAttribute("href", "#finished");
    expect(crumb).toHaveTextContent(/^끝난 점검\/target\.example$/);
    expect(crumb).not.toHaveTextContent("작업대");
    await waitFor(() => expect(a.fetchRemediation).toHaveBeenCalledWith("42", "internal"));
    // 저장된 실행 기록(diagnosis-run)은 보고서에서 쓰지 않아 부르지 않는다(사용자 11/19 — 실행 종료 기록 · 요청 수를 뺌)
    expect(a.fetchDiagnosisRun).not.toHaveBeenCalled();
    // 판정 집계 (시안 짜임: 발견 · 판정 확정 · 의심/판단 불가 · 커버리지): 후보는 검토 필요, 확정 0 (팀 lib 분류).
    // 판단 불가 = 미실행 + 결과 불명. 커버리지는 서버 값 그대로
    const strip = await screen.findByRole("region", { name: "판정 집계" });
    const cell = (label: string) => within(strip).getByText(label).parentElement as HTMLElement;
    await waitFor(() =>
      expect(cell("검토 필요 / 판단 불가")).toHaveTextContent(/^검토 필요 \/ 판단 불가1\/ 0추가 확인 권장$/),
    );
    expect(cell("판정 확정")).toHaveTextContent(/^판정 확정0증거 충분$/);
    expect(within(cell("취약점")).getByText("1")).toBeInTheDocument();
    // 자산 커버리지(방문 · 검사한 자산 비율)라고 이름 붙인다 — 판정 커버리지로 읽히지 않게 (리뷰 #479)
    await waitFor(() => expect(cell("자산 커버리지")).toHaveTextContent("40%"));
    expect(cell("자산 커버리지")).toHaveTextContent("미검사 60%");
    // 머리 줄: 시작 · 소요 · 모델 · 토큰(모델 사용량, 사용자 11/19 짧게). 서버가 주지 않는 시작 · 소요는 '연결 전'(값을
    // 만들지 않음). 자산 커버리지는 요약 칸에만 두고 머리에서 뺐다(사용자 11/17). 모델 사용량 기록이 없으면 '—'
    const header = screen
      .getByRole("heading", { level: 1, name: "target.example 보고서" })
      .closest("header") as HTMLElement;
    expect(header).not.toHaveTextContent("자산 커버리지");
    expect(within(header).getByText(/^토큰/)).toHaveTextContent(/^토큰 —$/);
    expect(header).toHaveTextContent("모델 합성 모델 · 로컬");
    // 단계 줄: 보고서가 지금 단계이고, 앞 단계(자산 탐색 · 범위 확인 · 점검 진행)로 눌러서 돌아간다 (사용자 11/17)
    const steps = within(header).getByRole("navigation", { name: "점검 단계" });
    expect(within(steps).getByRole("link", { name: /보고서/ })).toHaveAttribute("aria-current", "step");
    expect(within(steps).getByRole("link", { name: /자산 탐색/ })).toHaveAttribute("href", "#crawl");
    expect(within(steps).getByRole("link", { name: /범위 확인/ })).toHaveAttribute("href", "#scope");
    expect(within(steps).getByRole("link", { name: /점검 진행/ })).toHaveAttribute("href", "#live");
    expect(within(header).getByText(/^시작/)).toHaveTextContent("연결 전");
    expect(within(header).getByText(/^소요/)).toHaveTextContent("연결 전");
    expect(header).not.toHaveTextContent(/task #|모델 요청/);
    // 시안 머리 버튼은 '발견 상세'(좁은 화면) 하나
    expect(within(header).queryByRole("button", { name: /새로고침/ })).toBeNull();
    // 발견 목록에 후보가 있다. 위험도 딱지가 맨 앞(심각도 없으면 '위험도 미평가'), 아래 줄은 엔드포인트 · 유형
    const row = within(screen.getByRole("tabpanel", { name: "취약점 목록" })).getByRole("button", {
      name: /다른 계정 책 조회 1/,
    });
    expect(row.firstElementChild).toHaveTextContent("위험도 미평가");
    // 위험도 미평가는 회색 딱지(사용자 11/23)
    expect(row.firstElementChild?.className).toMatch(/sev-none/);
    expect(row).toHaveTextContent("GET /api/items/{id}idor");
    expect(within(row).getByText("{id}").tagName).toBe("I");
    // 상세 머리: 유형 · 주소, 엔드포인트 (시안 findingHtml)
    const insp = screen.getByRole("complementary", { name: "취약점 상세" });
    expect(within(insp).getByText(/^idor ·/)).toBeInTheDocument();
    expect(within(insp).getByText("GET")).toBeInTheDocument();
    // 대표 경로는 취약점 확정이 아닌 관련 경로로 표시한다. 여기서 바꾸는 손잡이는 없다
    const state = within(insp).getByRole("heading", { name: "상태" }).parentElement as HTMLElement;
    expect(state).toHaveTextContent(
      /^상태IDcandidate-1위험도위험도 미평가판정검토 필요유형idor관련 경로GET \/api\/items\/\{id\}발견 시각\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/,
    );
    expect(within(state).queryByRole("combobox")).toBeNull();
    // 상세는 정리했다(사용자 11/17): '연결 전' 자리(계정별 응답 비교 · 다음 단계)와 머리와 겹치는 분류는 두지 않는다
    expect(within(insp).queryByText("계정별 응답 비교")).toBeNull();
    expect(within(insp).queryByText("다음 단계")).toBeNull();
    expect(within(insp).queryByText("분류")).toBeNull();
    // 탭은 화살표로 옮긴다 (시안 selTab): 발견 목록 → 자산 지식 그래프, 고른 탭만 Tab 순서에
    const listTab = screen.getByRole("tab", { name: /취약점 목록/ });
    expect(listTab).toHaveAttribute("tabindex", "0");
    fireEvent.keyDown(listTab, { key: "ArrowRight" });
    expect(screen.getByRole("tab", { name: "자산 지식 그래프" })).toHaveAttribute("aria-selected", "true");
    expect(listTab).toHaveAttribute("tabindex", "-1");
    // 자산 지식 그래프 탭은 KG 조회 v1 을 읽는다(그리기·실패는 report-asset-graph.test.tsx). '연결 전' 자리는 없다
    const graphTab = screen.getByRole("tabpanel", { name: "자산 지식 그래프" });
    expect(within(graphTab).queryByText(/^연결 전/)).toBeNull();
    expect(graphTab).toHaveTextContent(/자산 지식 그래프를 불러오는 중|그래프 조회 실패/);
    // 접근통제 지도 탭은 이 점검의 저장 지도를 읽는다(그리기·409·경쟁은 report-access-map.test.tsx). '연결 전' 자리는 없고
    // 지도 화면 바로가기는 있다
    fireEvent.click(screen.getByRole("tab", { name: "접근통제 지도" }));
    const mapTab = screen.getByRole("tabpanel", { name: "접근통제 지도" });
    expect(within(mapTab).queryByText(/^연결 전/)).toBeNull();
    expect(within(mapTab).getByRole("button", { name: /접근통제 지도 화면 열기/ })).toBeInTheDocument();
    // '증거 · 요청 기록' 탭은 없고 머리 줄은 토큰(입 · 출)에서 끝난다(사용자 11/19 — 요청 수는 두지 않음).
    // 실행 종료 기록(단계 · 결과 · 실행 ID · 감사)은 어디에도 없다
    expect(screen.queryByRole("tab", { name: "증거 · 요청 기록" })).toBeNull();
    expect(within(header).queryByText(/^요청/)).toBeNull();
    // ('정상 종료'는 오른쪽 취약점 상세의 안내 글에도 있어 실행 종료 기록 칸 자체로 확인한다. 실행 ID 는 기술 정보를 뺀 뒤로
    // 화면 어디에도 없다)
    expect(screen.queryByRole("region", { name: "실행 종료 기록" })).toBeNull();
    expect(screen.queryByText(/요청 접수|실행 종료 기록|run-1/)).toBeNull();
    // 내려받기는 보고서 탭 문서 아래
    fireEvent.click(screen.getByRole("tab", { name: "보고서" }));
    const docTab = screen.getByRole("tabpanel", { name: "보고서" });
    expect(within(docTab).getByRole("heading", { name: "내려받기" })).toBeInTheDocument();
    expect(within(docTab).getByRole("button", { name: "공유용 사본" })).toBeInTheDocument();
    // 모델 사용량은 머리 줄에(따로 제목 칸을 두지 않는다)
    expect(screen.queryByRole("heading", { name: "모델 사용량" })).toBeNull();
  });

  it("보고서 접근통제 지도 탭은 보고서가 보이고 그 탭일 때만 지도를 읽고, 탭이나 화면을 떠났다 오면 다시 읽는다 (#771 리뷰)", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(completed);
    a.fetchRemediation.mockResolvedValue({ task_id: 42, entries: [candidate(1)] });
    const mapReads = () =>
      vi.mocked(teamApi.apiFetch).mock.calls.filter(([path]) => String(path) === "/api/tasks/42/access-map").length;
    // Workbench 처럼 보고서를 두고 route 로 보였다 숨겼다 한다(보고서 화면 visible = route === "report")
    let go: (v: View) => void = noop;
    function Shell() {
      const [route, setRoute] = useState<View>("report");
      go = setRoute;
      return (
        <ShellContext.Provider value={shellValue({ route, goTo: setRoute })}>
          <UrlFeeds />
          <section hidden={route !== "report"}>
            <ReportByMode />
          </section>
        </ShellContext.Provider>
      );
    }
    render(<Shell />);
    await screen.findByRole("tabpanel", { name: "취약점 목록" });
    // 다른 탭(취약점 목록 · 자산 지식 그래프)에서는 지도를 읽지 않는다 — 탭을 무시하면(active={visible}) 여기서 걸린다
    fireEvent.click(screen.getByRole("tab", { name: "자산 지식 그래프" }));
    await act(async () => undefined);
    expect(mapReads()).toBe(0);
    // 지도 탭으로 가면 한 번
    fireEvent.click(screen.getByRole("tab", { name: "접근통제 지도" }));
    await waitFor(() => expect(mapReads()).toBe(1));
    // 다른 탭에 갔다 돌아오면 새 진입이라 한 번 더
    fireEvent.click(screen.getByRole("tab", { name: /취약점 목록/ }));
    await act(async () => undefined);
    expect(mapReads()).toBe(1);
    fireEvent.click(screen.getByRole("tab", { name: "접근통제 지도" }));
    await waitFor(() => expect(mapReads()).toBe(2));
    // 지도 탭에 둔 채 다른 화면으로 가면 읽지 않고, 돌아오면 다시 읽는다 — 화면을 무시하면(active={tab === "map"}) 여기서 걸린다
    act(() => go("dashboard"));
    await act(async () => undefined);
    expect(mapReads()).toBe(2);
    act(() => go("report"));
    await waitFor(() => expect(mapReads()).toBe(3));
  });

  it("오른쪽 발견 상세 머리의 '상세 보기'는 고른 발견의 상세 페이지(왼쪽 카드 · 오른쪽 상태 카드)로 가운데를 바꾸고, '발견 목록으로' 돌아온다", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(completed);
    a.fetchRemediation.mockResolvedValue({
      task_id: 42,
      entries: [candidate(1), { ...candidate(2), vuln_class: "idor", report: "## 재현\n\n- 계정 B 조회" }],
    });
    mount("report");
    const list = await screen.findByRole("tabpanel", { name: "취약점 목록" });
    // 줄에는 상세 보기를 두지 않는다 — 오른쪽 패널 머리 하나
    expect(within(list).queryByRole("button", { name: /상세 보기/ })).toBeNull();
    fireEvent.click(within(list).getByRole("button", { name: /다른 계정 책 조회 2/ }));
    const insp = screen.getByRole("complementary", { name: "취약점 상세" });
    fireEvent.click(within(insp).getByRole("button", { name: "상세 보기" }));
    const page = screen.getByRole("region", { name: "취약점 상세 페이지" });
    expect(within(page).getByRole("heading", { level: 2, name: "다른 계정 책 조회 2" })).toHaveFocus();
    // ARTEX 발견 상세 짜임: 오른쪽 상태 카드(점검 대상 포함), 왼쪽 요약 → 증거 / PoC → 상세 보고서. 보안 조치 · AI 프롬프트는 뺐다(사용자 11/19)
    const side = within(page).getByRole("complementary", { name: "취약점 상태" });
    expect(side).toHaveTextContent(/IDcandidate-2/);
    expect(side).toHaveTextContent(/유형idor/);
    expect(side).toHaveTextContent(/점검 대상target\.example/);
    expect(
      within(page)
        .getAllByRole("heading", { level: 3 })
        .map((h) => h.textContent),
    ).toEqual(["요약", "증거 / PoC", "상세 보고서", "상태"]);
    expect(
      within(within(page).getByRole("region", { name: "상세 보고" })).getByRole("heading", { name: "재현" }),
    ).toBeInTheDocument();
    // 페이지를 연 동안 목록 · 오른쪽 발견 상세 패널은 두지 않는다
    expect(screen.queryByRole("tabpanel", { name: "취약점 목록" })).toBeNull();
    expect(screen.queryByRole("complementary", { name: "취약점 상세" })).toBeNull();
    fireEvent.click(within(page).getByRole("button", { name: "취약점 목록으로 돌아가기" }));
    expect(screen.queryByRole("region", { name: "취약점 상세 페이지" })).toBeNull();
    const back = screen.getByRole("tabpanel", { name: "취약점 목록" });
    // 돌아오면 연 발견이 골라져 있다
    expect(within(back).getByRole("button", { name: /다른 계정 책 조회 2/ })).toHaveAttribute("aria-current", "true");
  });

  it("발견 상세 페이지에서 머리 단계 줄의 '보고서'를 눌러도 ← 처럼 발견 목록으로 돌아간다", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(completed);
    a.fetchRemediation.mockResolvedValue({ task_id: 42, entries: [candidate(1), candidate(2)] });
    mount("report");
    const list = await screen.findByRole("tabpanel", { name: "취약점 목록" });
    fireEvent.click(within(list).getByRole("button", { name: /다른 계정 책 조회 2/ }));
    fireEvent.click(
      within(screen.getByRole("complementary", { name: "취약점 상세" })).getByRole("button", { name: "상세 보기" }),
    );
    expect(screen.getByRole("region", { name: "취약점 상세 페이지" })).toBeInTheDocument();
    const steps = screen.getByRole("navigation", { name: "점검 단계" });
    fireEvent.click(within(steps).getByRole("link", { name: /보고서/ }));
    expect(screen.queryByRole("region", { name: "취약점 상세 페이지" })).toBeNull();
    const back = screen.getByRole("tabpanel", { name: "취약점 목록" });
    expect(within(back).getByRole("button", { name: /다른 계정 책 조회 2/ })).toHaveAttribute("aria-current", "true");
  });

  it("단독 발견 상세(#finding)에서 '발견 목록으로'를 누르면 보고서는 같은 상세가 아니라 발견 목록을 연다 (#695 리뷰)", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(completed);
    a.fetchRemediation.mockResolvedValue({ task_id: 42, entries: [candidate(1), candidate(2)] });
    // Workbench 처럼 두 화면을 함께 두고 route 로 하나만 보인다
    let go: (v: View) => void = noop;
    function Both() {
      const [route, setRoute] = useState<View>("report");
      go = setRoute;
      return (
        <ShellContext.Provider value={shellValue({ route, goTo: setRoute })}>
          <UrlFeeds />
          <section hidden={route !== "report"}>
            <ReportByMode />
          </section>
          <section hidden={route !== "finding"}>
            <UrlFindingLive />
          </section>
        </ShellContext.Provider>
      );
    }
    render(<Both />);
    const list = await screen.findByRole("tabpanel", { name: "취약점 목록" });
    fireEvent.click(within(list).getByRole("button", { name: /다른 계정 책 조회 2/ }));
    fireEvent.click(
      within(screen.getByRole("complementary", { name: "취약점 상세" })).getByRole("button", { name: "상세 보기" }),
    );
    expect(screen.getByRole("region", { name: "취약점 상세 페이지" })).toBeInTheDocument();
    // 주소창에 #finding — 같은 발견이 단독 화면에 뜬다
    act(() => go("finding"));
    expect(await screen.findByRole("heading", { level: 2, name: "다른 계정 책 조회 2" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "취약점 목록으로 돌아가기" }));
    // 보고서로 돌아오면 상세 페이지가 아니라 발견 목록
    expect(await screen.findByRole("tabpanel", { name: "취약점 목록" })).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "취약점 상세 페이지" })).toBeNull();
  });

  it("보고서 발견 목록은 위험도로 거르고(있는 등급만 · 수) 판정 · 위험도 · 발견 시각 순으로 정렬한다", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(completed);
    a.fetchRemediation.mockResolvedValue({
      task_id: 42,
      entries: [
        { ...candidate(1), severity: "low", observed_at: "2026-11-09T12:01:00Z" },
        { ...candidate(2), severity: "high", observed_at: "2026-11-09T12:03:00Z" },
        { ...candidate(3), observed_at: "2026-11-09T12:02:00Z" },
        // 시각을 못 읽으면 결과 불명이라 예상 등급을 세지 않는다(#666 entrySeverity)
        { ...candidate(4), severity: "high", observed_at: "not-a-time" },
      ],
    });
    mount("report");
    const list = await screen.findByRole("tabpanel", { name: "취약점 목록" });
    // 발견 칸 보조 글은 0건 등급을 빼고 확정 · 검토 필요 · 위험도 미평가로 묶는다(사용자 11/18 '간단하게', 11/24 '예상' 뺌).
    // 확정 0 이라 '확정 …'은 없고, 결과 불명(다른 계정 책 조회 4)의 높음은 예상으로 세지 않는다
    const strip = screen.getByRole("region", { name: "판정 집계" });
    expect((within(strip).getByText("취약점").parentElement as HTMLElement).lastElementChild).toHaveTextContent(
      /^검토 필요 높음 1, 낮음 1 · 위험도 미평가 2$/,
    );
    const names = () =>
      [...(document.getElementById("rList") as HTMLElement).querySelectorAll("button")].map(
        (b) => /다른 계정 책 조회 \d/.exec(b.textContent ?? "")?.[0],
      );
    const sevs = within(list).getByRole("group", { name: "위험도로 거르기" });
    // 줄이 있는 등급만 수와 함께 (심각 · 중간은 없음, '등급 없음'은 거르기로 두지 않고 전체에서 본다).
    // '전체 n개' 칸은 오른쪽 'n / n건'과 겹쳐 두지 않는다(사용자 11/18)
    expect(
      within(sevs)
        .getAllByRole("button")
        .map((r) => r.textContent),
    ).toEqual(["높음 1개", "낮음 1개"]);
    expect(within(list).getByText("4 / 4건")).toBeInTheDocument();
    fireEvent.click(within(sevs).getByRole("button", { name: "높음 1개", pressed: false }));
    expect(within(sevs).getByRole("button", { name: "높음 1개", pressed: true })).toBeInTheDocument();
    expect(names()).toEqual(["다른 계정 책 조회 2"]);
    expect(within(list).getByText("1 / 4건")).toBeInTheDocument();
    // 오른쪽 상세도 보이는 줄로 옮긴다
    expect(
      within(screen.getByRole("complementary", { name: "취약점 상세" })).getByRole("heading", { level: 2 }),
    ).toHaveTextContent("다른 계정 책 조회 2");
    // 고른 등급을 다시 누르면 전체
    fireEvent.click(within(sevs).getByRole("button", { name: "높음 1개", pressed: true }));
    expect(within(sevs).getByRole("button", { name: "높음 1개", pressed: false })).toBeInTheDocument();
    expect(within(list).getByText("4 / 4건")).toBeInTheDocument();
    const order = within(list).getByRole("combobox", { name: "정렬" });
    fireEvent.change(order, { target: { value: "new" } });
    // 시각을 못 읽는 줄은 시각 순에서 맨 뒤
    expect(names()).toEqual([
      "다른 계정 책 조회 2",
      "다른 계정 책 조회 3",
      "다른 계정 책 조회 1",
      "다른 계정 책 조회 4",
    ]);
    fireEvent.change(order, { target: { value: "old" } });
    expect(names()).toEqual([
      "다른 계정 책 조회 1",
      "다른 계정 책 조회 3",
      "다른 계정 책 조회 2",
      "다른 계정 책 조회 4",
    ]);
    fireEvent.change(order, { target: { value: "sev" } });
    // 위험도 순: 높음 → 낮음 → 미평가(판정 순 유지: 검토 필요 다른 계정 책 조회 3 → 결과 불명 다른 계정 책 조회 4)
    expect(names()).toEqual([
      "다른 계정 책 조회 2",
      "다른 계정 책 조회 1",
      "다른 계정 책 조회 3",
      "다른 계정 책 조회 4",
    ]);
  });

  it("보고서 발견 목록: 재조회로 고른 발견의 위험도가 바뀌어 거르기에서 빠지면 오른쪽 상세 · '상세 보기'도 보이는 줄로 옮긴다 (#677 리뷰)", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(completed);
    a.fetchRemediation.mockResolvedValue({
      task_id: 42,
      entries: [
        { ...candidate(1), severity: "low" },
        { ...candidate(2), severity: "high" },
        { ...candidate(3), severity: "low" },
      ],
    });
    const { rerender, goTo } = mount("report");
    // 보고서에 다시 들어오면(다른 화면 → 보고서) 저장 결과를 다시 읽는다. 오른쪽 '생성·저장' 버튼은 뺐다(사용자 11/19)
    // 두 화면 전환을 한 act 에 묶으면 React 가 합쳐 그려 다시 읽지 않으므로 따로 그린다
    const show = (route: "live" | "report") =>
      act(() =>
        rerender(
          <ShellContext.Provider value={shellValue({ route, goTo })}>
            <UrlFeeds />
            <ReportByMode />
          </ShellContext.Provider>,
        ),
      );
    const reenter = () => {
      show("live");
      show("report");
    };
    const list = await screen.findByRole("tabpanel", { name: "취약점 목록" });
    const insp = () => screen.getByRole("complementary", { name: "취약점 상세" });
    fireEvent.click(within(list).getByRole("button", { name: /다른 계정 책 조회 2/ }));
    fireEvent.click(within(list).getByRole("button", { name: "높음 1개", pressed: false }));
    expect(within(insp()).getByRole("heading", { level: 2 })).toHaveTextContent("다른 계정 책 조회 2");
    // 다시 들어와 저장 결과를 다시 읽는다: 이번엔 다른 계정 책 조회 2 가 낮음, 다른 계정 책 조회 3 이 높음
    a.fetchRemediation.mockResolvedValue({
      task_id: 42,
      entries: [
        { ...candidate(1), severity: "low" },
        { ...candidate(2), severity: "low" },
        { ...candidate(3), severity: "high" },
      ],
    });
    reenter();
    // 다시 들어오면 목록 요소가 새로 그려지므로 매번 찾는다
    const listNow = () => screen.getByRole("tabpanel", { name: "취약점 목록" });
    await waitFor(() =>
      expect(within(listNow()).getByRole("button", { name: /다른 계정 책 조회 3/ })).toBeInTheDocument(),
    );
    expect(within(listNow()).queryByRole("button", { name: /다른 계정 책 조회 2/ })).toBeNull();
    // '높음' 거르기는 그대로, 목록은 다른 계정 책 조회 3 하나 — 오른쪽 상세 · 줄 표시도 다른 계정 책 조회 3
    expect(within(listNow()).getByRole("button", { name: "높음 1개", pressed: true })).toBeInTheDocument();
    expect(within(listNow()).getByRole("button", { name: /다른 계정 책 조회 3/ })).toHaveAttribute(
      "aria-current",
      "true",
    );
    expect(within(insp()).getByRole("heading", { level: 2 })).toHaveTextContent("다른 계정 책 조회 3");
    fireEvent.click(within(insp()).getByRole("button", { name: "상세 보기" }));
    expect(
      within(screen.getByRole("region", { name: "취약점 상세 페이지" })).getByRole("heading", { level: 2 }),
    ).toHaveTextContent("다른 계정 책 조회 3");
  });

  it("보고서 탭: 참조형 이미지 `![글][이름]` 의 대체 글에 엔티티로 숨긴 비밀도 가린다 (#677 리뷰)", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
    saveHandle();
    u.readURLStatus.mockResolvedValue(completed);
    const hidden = "&#112;&#97;&#115;&#115;&#119;&#111;&#114;&#100;&#61;synthetic-reference-mask";
    a.fetchRemediation.mockResolvedValue({
      task_id: 42,
      entries: [{ ...candidate(1), summary: `![${hidden}][proof]\n\n[proof]: /synthetic.png` }],
    });
    mount("report");
    fireEvent.click(await screen.findByRole("tab", { name: "보고서" }));
    const doc = screen.getByRole("region", { name: "취약점 점검 보고서" });
    expect(doc).not.toHaveTextContent("synthetic-reference-mask");
    // 이미지는 불러오지 않는다
    expect(doc.querySelector("img")).toBeNull();
    expect(doc).not.toHaveTextContent("synthetic.png");
    fireEvent.click(within(doc).getByRole("button", { name: "복사" }));
    await within(doc).findByText("보고서 글을 복사했습니다.");
    expect(writeText.mock.calls[0][0]).not.toMatch(/synthetic-reference-mask|&#/);
  });

  it("보고서 탭: 발견마다 위험도 · 판정 · 요약 · 재현 절차(PoC) · 요청·응답을 Markdown 문서 한 장으로, 정상 대조는 뺀다", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
    saveHandle();
    u.readURLStatus.mockResolvedValue(completed);
    const step = {
      task_id: 42,
      entry_id: "candidate-2",
      id: `step-${"b".repeat(24)}`,
      number: 1,
      title: "계정 B로 계정 A 주문 조회",
      source_step_id: "cross-read",
      run_ref: "run-1",
      call_ref: "",
      evidence_ref: "cross-proof",
      evidence_version: 1,
      evidence_digest: "e".repeat(64),
      binding_digest: "b".repeat(64),
      observed_at: "2026-11-09T12:10:00Z",
      method: "GET",
      path: "/api/orders/2",
      prerequisites: "시험 계정 A · B",
      observation: "200 응답에 계정 A 주문",
      truncated: false,
      capture: { status: "not_captured" as const },
    };
    a.fetchRemediation.mockResolvedValue({
      task_id: 42,
      entries: [
        candidate(1),
        {
          ...candidate(2),
          vuln_class: "idor",
          summary: "&#112;&#97;&#115;&#115;&#119;&#111;&#114;&#100;=synthetic-poc-entity",
          evidence: JSON.stringify({ status: 200, headers: { Authorization: "Bearer synthetic-poc-token" } }),
          steps: [step],
        },
        {
          ...candidate(3),
          entry_id: "verdict-3",
          outcome: "negative",
          status: "allowed",
          verification: "stored_verification",
          title: "정상 대조 줄",
        },
      ],
    });
    mount("report");
    fireEvent.click(await screen.findByRole("tab", { name: "보고서" }));
    // ARTEX '침투 테스트 보고서 (Markdown)' 모양: 아이콘 · 제목, [복사], 상자 안 문서(제목 → 설명 → 정보 → 요약 → 취약점)
    const doc = screen.getByRole("region", { name: "취약점 점검 보고서" });
    expect(within(doc).getByRole("heading", { level: 3 })).toHaveTextContent("취약점 점검 보고서 (Markdown)");
    expect(within(doc).getByRole("heading", { level: 1 })).toHaveTextContent(
      "취약점 점검 보고서: https://target.example/",
    );
    expect(doc).toHaveTextContent("'검토 필요'는 확정이 아니며");
    expect(doc).toHaveTextContent("점검 방식: 설치 범위");
    expect(doc).toHaveTextContent("모델: 합성 모델 · 로컬");
    // 요약은 확정 · 검토 필요를 나눠 센다(정상 대조는 빼고). '확인된 취약점'으로 묶지 않는다
    // 합계 '취약점 n건'은 확정 수처럼 읽혀 두지 않고 판정별로만, 확정이 맨 앞(#773 P2, 절대규칙 5)
    expect(doc).toHaveTextContent("취약점: 확정 0건 · 검토 필요 2건 · 미실행 0건 · 결과 불명 0건");
    expect(doc).not.toHaveTextContent(/취약점: \d+건/);
    expect(doc).not.toHaveTextContent("확인된 취약점");
    // 취약점: '1. [등급 · 판정] 제목' 은 제목(h2)이고 취약점 사이에 구분선 — 어디까지가 몇 번인지 보인다. 정상 대조는 없다
    const items = within(doc)
      .getAllByRole("heading", { level: 2 })
      .map((x) => x.textContent);
    expect(items).toEqual([
      "1. [위험도 미평가 · 검토 필요] 다른 계정 책 조회 1",
      "2. [위험도 미평가 · 검토 필요] 다른 계정 책 조회 2",
    ]);
    expect(doc.querySelectorAll("hr")).toHaveLength(1);
    expect(doc).not.toHaveTextContent("정상 대조 줄");
    // 오른쪽 상세와 같은 이름: 거부된 시도도 있으므로 송신·응답을 단정하지 않는다.
    expect(doc).toHaveTextContent("요청 시도 · 응답 관찰:");
    expect(doc).not.toHaveTextContent(/PoC \/ 증거|ID candidate-/);
    expect(doc).toHaveTextContent("구조화된 실제 재현 단계 미확보");
    expect(doc).toHaveTextContent("요청 1 · 계정 B로 계정 A 주문 조회 — GET /api/orders/2");
    expect(doc).toHaveTextContent("사전 조건: 시험 계정 A · B");
    // 요청·응답은 코드 상자, 비밀은 가린다(엔티티로 숨긴 값도 풀리지 않는다)
    expect(doc.querySelector("pre code")).not.toBeNull();
    expect(doc).not.toHaveTextContent(/synthetic-poc-token|synthetic-poc-entity/);
    fireEvent.click(within(doc).getByRole("button", { name: "복사" }));
    await within(doc).findByText("보고서 글을 복사했습니다.");
    expect(writeText.mock.calls[0][0]).not.toMatch(/synthetic-poc-token|synthetic-poc-entity|&#/);
  });

  it("대표 경로와 E② 요청 시도의 관찰 경로를 구분하고 취약한 경로로 단정하지 않는다", () => {
    const obs = (endpoint: string, method?: string) => ({ endpoint, method });
    const withCtx = (context: Record<string, unknown>) =>
      ({ ...candidate(1), context }) as unknown as Parameters<typeof entryEp>[0];
    // 서버가 엔진 후보의 대표 경로를 채우지 않음('미확인') → 관찰 경로
    expect(entryEp(withCtx({ endpoint: "미확인", observations: [obs("GET /users/v1/name1", "GET")] }))).toEqual({
      m: "GET",
      p: "/users/v1/name1",
      more: 0,
    });
    // 같은 경로는 한 번, 다른 경로는 '외 n개'
    expect(
      entryEp(
        withCtx({
          observations: [
            obs("GET /users/v1/name1", "GET"),
            obs("GET /users/v1/name1", "GET"),
            obs("GET /users/v1/name2", "GET"),
          ],
        }),
      ),
    ).toEqual({ m: "GET", p: "/users/v1/name1", more: 1 });
    // 경로 글에 메서드가 없으면 따로 받은 메서드, 경로가 / 로 시작하지 않거나 미확인이면 버린다
    expect(entryEp(withCtx({ observations: [obs("/books/v1", "post")] }))).toEqual({
      m: "POST",
      p: "/books/v1",
      more: 0,
    });
    expect(entryEp(withCtx({ observations: [obs("unknown"), obs("http://x.test/a")] }))).toBeNull();
    // 대표 경로가 있으면 그것(메서드가 글에 있으면 그대로, 없으면 같은 경로 관찰이 한 가지일 때만)
    expect(entryEp(withCtx({ endpoint: "PUT /api/orders/{id}" }))).toEqual({
      m: "PUT",
      p: "/api/orders/{id}",
      more: 0,
    });
    expect(
      entryEp(withCtx({ endpoint: "/api/orders/{id}", observations: [obs("/api/orders/{id}", "DELETE")] })),
    ).toEqual({ m: "DELETE", p: "/api/orders/{id}", more: 0 });
    // 보고서 문서에도 같은 경로와 나머지 수
    const md = buildPocMarkdown(
      [withCtx({ observations: [obs("GET /users/v1/name1", "GET"), obs("GET /users/v1/name2", "GET")] })],
      { targetUrl: "https://target.example/", mode: null, model: null, excluded: null, crawl: null },
    );
    expect(md).toContain("관찰된 요청 경로 GET /users/v1/name1 외 1개");
    expect(md).not.toContain("취약한 경로");
    // 전송되지 않은 E② 요청 시도도 관찰에 남는다. 그 경로가 취약하다는 뜻으로 표시하면 안 된다.
    const denied = buildPocMarkdown(
      [withCtx({ observations: [{ endpoint: "GET /api/me", method: "GET", observation: "전송 시도=false" }] })],
      { targetUrl: "https://target.example/", mode: null, model: null, excluded: null, crawl: null },
    );
    expect(denied).toContain("관찰된 요청 경로 GET /api/me");
    expect(denied).not.toContain("취약한 경로");
    expect(denied).toContain("**요청 시도 · 응답 관찰:**");
    expect(denied).not.toContain("**실제 요청 · 응답:**");
    const view = render(
      <FindingDetail
        e={withCtx({ observations: [{ endpoint: "GET /api/me", method: "GET", observation: "전송 시도=false" }] })}
        host={null}
        taskId="42"
        onReload={noop}
      />,
    );
    expect(within(view.container).getByText("관찰된 요청 경로")).toBeInTheDocument();
    expect(within(view.container).queryByText("취약한 경로")).toBeNull();
    view.unmount();
    // 문서도 오른쪽 상세와 같은 이름: 발견 시각, 내부 ID 없음, 서버 기본 단계 제목은 '요청 n'(사용자 11/19 · 11/20)
    expect(md).toMatch(/발견 시각 \d{4}-/);
    expect(md).not.toMatch(/ID candidate-|저장된 실행 관찰/);
    const withStep = buildPocMarkdown(
      [
        {
          ...candidate(1),
          steps: [
            {
              task_id: 42,
              entry_id: "candidate-1",
              id: `step-${"c".repeat(24)}`,
              number: 1,
              title: "저장된 실행 관찰 1",
              source_step_id: "s1",
              run_ref: "run-1",
              call_ref: "",
              evidence_ref: "e1",
              evidence_version: 1,
              evidence_digest: "e".repeat(64),
              binding_digest: "b".repeat(64),
              observed_at: "2026-11-09T12:10:00Z",
              method: "GET",
              path: "/users/v1/name1",
              prerequisites: "",
              observation: "200",
              truncated: false,
              capture: { status: "not_captured" as const },
            },
          ],
        },
      ],
      { targetUrl: "https://target.example/", mode: null, model: null, excluded: null, crawl: null },
    );
    expect(withStep).toContain("- **요청 1** — GET /users/v1/name1");
    expect(withStep).not.toContain("저장된 실행 관찰");
  });

  it("PoC 문서: 코드 상자 울타리는 본문의 ` 줄보다 길게, 목록 줄 글의 줄바꿈은 띄어쓰기로", () => {
    const md = buildPocMarkdown([{ ...candidate(1), title: "줄\n바꿈", evidence: "a ```` b" }], {
      targetUrl: "https://target.example/",
      mode: null,
      model: null,
      excluded: null,
      crawl: { visited: 3, found: 2 },
    });
    expect(md).toContain("## 1. [위험도 미평가 · 검토 필요] 줄 바꿈");
    // 제목 끝의 '#'는 Markdown 제목 닫기로 읽히지 않게 이스케이프한다(#709 리뷰)
    const hashes = buildPocMarkdown([{ ...candidate(2), title: "다른 계정 책 조회 2 ###" }], {
      targetUrl: null,
      mode: null,
      model: null,
      excluded: null,
      crawl: null,
    });
    expect(hashes).toContain("## 1. [위험도 미평가 · 검토 필요] 다른 계정 책 조회 2 \\#\\#\\#");
    render(<Markdown text={hashes} />);
    expect(screen.getByRole("heading", { level: 2 })).toHaveTextContent(
      /^1\. \[위험도 미평가 · 검토 필요\] 다른 계정 책 조회 2 ###$/,
    );
    expect(md).not.toContain("---"); // 구분선은 취약점 사이에만
    expect(md).toContain("- 탐색: 방문 3 · 발견한 API 2");
    expect(md).toContain("`````text\na ```` b\n`````");
  });

  it("보고서 머리 줄의 모델 사용량은 점검 진행과 같은 입 · 출 칩, 마우스를 올리면 정확한 값과 최대치", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue({
      ...completed,
      model_usage: {
        requests: 2,
        input_tokens: 1200,
        output_tokens: 300,
        reserved_input_tokens: 0,
        reserved_output_tokens: 0,
        cost_usd_micros: 0,
        usage_known: true,
        unknown_usage_calls: 1,
      },
    });
    a.fetchRemediation.mockResolvedValue({ task_id: 42, entries: [candidate(1)] });
    mount("report");
    const header = (await screen.findByRole("heading", { level: 1, name: "target.example 보고서" })).closest(
      "header",
    ) as HTMLElement;
    const use = await within(header).findByText(/^토큰/);
    expect(use).toHaveTextContent(/^토큰 입 1.2k 출 300 \(최대\)$/);
    expect(use).toHaveAttribute("title", "입력 1,200 토큰 (최대치)\n출력 300 토큰 (최대치)");
    expect(header).not.toHaveTextContent("자산 커버리지");
  });

  it("다른 진단 만들기: 조회 연결만 끊고 폼을 비운다", async () => {
    saveHandle();
    mount("new");
    const again = await screen.findByRole("button", { name: "새로 입력" });
    expect((screen.getByLabelText(/점검할 사이트 주소/) as HTMLInputElement).value).toBe("https://target.example/");
    act(() => {
      fireEvent.click(again);
    });
    expect(url.readURLHandle(owner)).toBeNull();
    const target = screen.getByLabelText(/점검할 사이트 주소/) as HTMLInputElement;
    expect(target.value).toBe("");
    expect(target).toBeEnabled();
    expect(u.commandURLDiagnosis).not.toHaveBeenCalled();
  });
});

describe("레일 (URL 진단)", () => {
  it("이어 보는 진단 한 줄을 보이고 누르면 알맞은 화면으로 간다", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(running);
    const goTo = vi.fn<(v: View) => void>();
    render(
      <ShellContext.Provider value={shellValue({ route: "dashboard", goTo })}>
        <UrlFeeds />
        <LiveRailTasks />
      </ShellContext.Provider>,
    );
    const row = await screen.findByRole("button", { name: /target\.example/ });
    expect(within(row).getByText("엔진 실행 중 · 대상 진단")).toBeInTheDocument();
    // 진행 중 점검의 레일 줄은 진행 중인 점검 화면으로 간다 (시안 v54)
    fireEvent.click(row);
    expect(goTo).toHaveBeenCalledWith("running");
    expect(screen.getByText("끝난 점검 없음")).toBeInTheDocument();
    // 대시보드는 이 점검을 오른쪽에 보이므로 빨간 현재 표시를 단다(사용자 11/24)
    expect(row).toHaveAttribute("aria-current", "true");
  });
});

describe("대시보드 · 인증 (URL 진단)", () => {
  it("대시보드: 이어 보는 진단은 진행 중인 점검에 한 줄, 상세에서 알맞은 화면으로 간다", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(running);
    const { goTo } = mount("dashboard");
    const active = await screen.findByRole("region", { name: "진행 중인 점검" });
    const row = await within(active).findByRole("button", { name: /job-1/ });
    expect(within(row).getByText("엔진 실행 중")).toBeInTheDocument();
    expect(screen.getByText("끝난 점검이 없습니다.")).toBeInTheDocument();
    // 칸 제목 옆에 전체 건수, 오른쪽에 그 목록 화면으로 가는 '전체 보기' (시안 v55)
    expect(within(active).getByText("1건")).toBeInTheDocument();
    expect(within(active).getByRole("link", { name: "전체 보기" })).toHaveAttribute("href", "#running");
    const done = screen.getByRole("region", { name: "보존된 끝난 점검" });
    expect(within(done).getByText("0건")).toBeInTheDocument();
    expect(within(done).getByRole("link", { name: "전체 보기" })).toHaveAttribute("href", "#finished");
    // 줄 오른쪽은 시안 딱지 '점검 중'
    expect(within(row).getByText("엔진 실행 중")).toBeInTheDocument();
    await act(async () => {
      fireEvent.click(row);
    });
    expect(screen.getByText("합성 모델")).toBeInTheDocument();
    // 상세 (시안 act-shop): 진행 중 · 주소, 점검 진행, 진행 화면 열기
    const detail = screen.getByRole("complementary", { name: "선택한 항목 상세" });
    expect(within(detail).getByRole("heading", { level: 2, name: "점검 진행" })).toBeInTheDocument();
    fireEvent.click(within(detail).getByRole("button", { name: /진행 화면 열기/ }));
    expect(goTo).toHaveBeenCalledWith("live");
  });

  it("진행 중 상세: 시작 칸은 서버 시작 시각과 그 기준 경과 — 화면이 새 스톱워치를 만들지 않는다", async () => {
    // 새로 연 화면이라도 경과는 서버 started_at 부터 센다(시작 = 지금 − 90초 → '1분'). 시계는 모의하지 않는다
    const startedAt = new Date(Date.now() - 90_000).toISOString();
    saveHandle();
    u.readURLStatus.mockResolvedValue({ ...running, started_at: startedAt });
    mount("dashboard");
    const region = await screen.findByRole("region", { name: "진행 중인 점검" });
    const activeRow = await within(region).findByRole("button", { name: /job-1/ });
    await act(async () => {
      fireEvent.click(activeRow);
    });
    const detail = screen.getByRole("complementary", { name: "선택한 항목 상세" });
    await waitFor(() =>
      expect(within(detail).getByText("시작").nextElementSibling).toHaveTextContent(
        `${dateTimeOf(startedAt)} · 경과 1분`,
      ),
    );
    // 남은 시간은 계약상 만들지 않는다
    expect(within(detail).getByText("남은 시간").nextElementSibling).toHaveTextContent("연결 전");
  });

  it("대시보드: 결과가 저장되기 전에는 발견을 부르지 않고 '없음'이라 하지 않는다 · 위치 줄 없음", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(running);
    mount("dashboard");
    const sec = await screen.findByRole("region", { name: "발견 · 검토 대상" });
    expect(await within(sec).findByText(/결과가 저장되면 여기에 나옵니다/)).toBeInTheDocument();
    expect(a.fetchRemediation).not.toHaveBeenCalled();
    expect(within(sec).queryByText(/저장된 발견이 없습니다/)).toBeNull();
    expect(within(sec).queryByRole("group", { name: "위험도로 거르기" })).toBeNull();
    // 위치가 제목과 같기만 해서 위치 줄을 그리지 않는다 (시안 v83)
    expect(screen.queryByRole("navigation", { name: "위치" })).toBeNull();
    // 요약 4칸(숫자만): 결과 저장 전 발견 · 결과 확인은 0 이 아니라 "—", 서버에 없는 토큰 30일 집계는 자리만
    const strip = screen.getByRole("region", { name: "요약" });
    const cell = (k: string) => within(strip).getByText(k).parentElement as HTMLElement;
    expect(cell("발견 · 검토 대상")).toHaveTextContent(/^발견 · 검토 대상—결과가 저장되면 셉니다$/);
    expect(cell("결과 확인")).toHaveTextContent(/^결과 확인—/);
    expect(cell("토큰 사용량 · 최근 30일")).toHaveTextContent(/^토큰 사용량 · 최근 30일— 연결 전.*점검당 평균 —$/);
    // 그 아래 원형 게이지 4개(사용자 11/24): 결과 저장 전 위험도 · 판정은 "—"와 이유, 탐색 흐름이 없는 점검은 탐색도 "—"
    const rings = screen.getByRole("region", { name: "분포" });
    const ring = (k: string) => within(rings).getByRole("region", { name: k });
    expect(ring("위험도")).toHaveTextContent("—");
    expect(ring("위험도")).toHaveTextContent("결과가 저장되면 셉니다");
    expect(ring("판정")).toHaveTextContent("결과가 저장되면 셉니다");
    expect(ring("탐색")).toHaveTextContent("탐색 흐름이 있는 점검에서 셉니다");
    expect(within(ring("위험도")).getByRole("img")).toHaveAccessibleName("결과 저장 전");
    // 진행 중 줄 (시안 activeRowHtml): 진행 막대 · % 자리는 두되 서버에 진행률이 없어 빈 막대 · '—%'(0% 로 만들지 않음)
    const activeRow = within(screen.getByRole("region", { name: "진행 중인 점검" })).getByRole("button", {
      name: /job-1/,
    });
    expect(activeRow).not.toHaveTextContent(/%/);
    expect(activeRow).not.toHaveTextContent(/\d+%/);
    await act(async () => {
      fireEvent.click(activeRow);
    });
    // 상세 (시안 act-shop): 지금 하는 일은 서버 단계, 시작 · 남은 시간 · 발견은 '연결 전', 결정 대기는 서버 값
    const detail = screen.getByRole("complementary", { name: "선택한 항목 상세" });
    expect(within(detail).getByText("지금 하는 일")).toBeInTheDocument();
    for (const k of ["시작", "남은 시간", "발견"])
      expect(within(detail).getByText(k).nextElementSibling).toHaveTextContent("연결 전");
    expect(within(detail).getByText("결정 대기").nextElementSibling).toHaveTextContent("없음");
    expect(within(detail).queryByText("소요")).toBeNull();
  });

  it("대시보드: 저장된 결과에서 정상 대조를 빼고 위험도 순으로 보이며, 위험도 칩으로 거른다", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(completed);
    a.fetchRemediation.mockResolvedValue({
      task_id: 42,
      entries: [
        { ...candidate(1), severity: "medium" },
        confirmed(2, "critical"),
        negative(3),
        confirmed(4, "high"),
        candidate(5),
      ],
    });
    mount("dashboard");
    const sec = await screen.findByRole("region", { name: "발견 · 검토 대상" });
    await waitFor(() => expect(a.fetchRemediation).toHaveBeenCalledWith("42", "internal"));
    const titles = () =>
      Array.from(
        sec.querySelectorAll("[data-dsel^='fd-']"),
        (x) => x.textContent?.match(/(발견|다른 계정 책 조회) \d+/)?.[0],
      );
    await waitFor(() => expect(titles()).toEqual(["발견 2", "발견 4", "다른 계정 책 조회 1", "다른 계정 책 조회 5"]));
    // 건수 옆에 확정 수를 따로 (확정만 취약점 · 절대 규칙 5)
    expect(within(sec).getByText("4건 · 확정 2")).toBeInTheDocument();
    // 심각도를 안 준 발견은 '위험도 미평가', 날짜는 없다 (시안 v90)
    const noSev = sec.querySelector("[data-dsel='fd-candidate-5']") as HTMLElement;
    expect(within(noSev).getByText("위험도 미평가")).toBeInTheDocument();
    for (const r of sec.querySelectorAll("[data-dsel^='fd-']")) expect(r.textContent).not.toMatch(/\d{1,2}:\d{2}/);
    // 요약 4칸(숫자만): 정상 대조를 뺀 4건 · 확정 · 검토 필요 수 · 결과 확인(결과 불명 + 미실행) 0
    const strip = screen.getByRole("region", { name: "요약" });
    const cell = (k: string) => within(strip).getByText(k).parentElement as HTMLElement;
    expect(within(cell("발견 · 검토 대상")).getByText("4")).toBeInTheDocument();
    expect(cell("발견 · 검토 대상")).toHaveTextContent(
      "확정 심각 1 · 확정 높음 1 · 확정 중간 0 · 확정 낮음 0 · 확정 정보성 0 · 검토 필요 중간 1",
    );
    expect(strip.querySelector("#dSevStack")).toBeNull();
    expect(cell("결과 확인")).toHaveTextContent("결과 불명 0 · 미실행 0");
    // 끝난 점검이라 결정할 것이 없다 (시안 v77 '모두 결정함')
    expect(cell("결정 대기")).toHaveTextContent(/^결정 대기0모두 결정함$/);
    // 그 아래 원형 게이지(사용자 11/24): 정상 대조를 뺀 4건을 위험도 · 판정으로 나눠 보이고, 끝난 점검이라 승인 대기 줄은 없다
    const rings = screen.getByRole("region", { name: "분포" });
    const ring = (k: string) => within(rings).getByRole("region", { name: k });
    // 위험도: 확정된 것만(가운데 2) — 후보의 예상 등급은 넣지 않고 '예상'이라는 말도 없다(사용자 11/24, 위험도 계약)
    expect(within(ring("위험도")).getByRole("img")).toHaveAccessibleName(
      "확정 2건 · 심각 1 · 높음 1 · 중간 0 · 낮음 0",
    );
    expect(ring("위험도")).not.toHaveTextContent("예상");
    // 심각 · 높음 · 중간 · 낮음은 0 이어도 늘 두고(사용자 11/24), 정보성 · 미평가는 있을 때만(여기선 확정 미평가가 없다)
    const sevLegend = [...ring("위험도").querySelectorAll("li")].map((li) => li.textContent);
    expect(sevLegend).toEqual(["심각1", "높음1", "중간0", "낮음0"]);
    // 판정: 확정 2 / 전체 4, 판단 불가(결과 불명 + 미실행) 0
    expect(ring("판정")).toHaveTextContent("2/4");
    expect(within(ring("판정")).getByRole("img")).toHaveAccessibleName("확정 2 · 검토 필요 2 · 판단 불가 0");
    // 끝난 점검이라 승인 대기 줄은 없다(카드가 노랗지 않음)
    expect(ring("전송 통제")).not.toHaveTextContent("승인 대기");
    // 처음 상세는 가장 위험한 발견. 끝난 점검의 발견이라 보고서로 간다 (시안 inspFor fd-blog)
    const detail = screen.getByRole("complementary", { name: "선택한 항목 상세" });
    expect(within(detail).getByText("발견 2")).toBeInTheDocument();
    expect(within(detail).getByRole("link", { name: /보고서 열기/ })).toHaveAttribute("href", "#report");
    // 발견 상세에 '연결 전' 자리(계정별 응답 비교 · 다음 단계)는 두지 않는다 (사용자 11/17)
    expect(within(detail).queryByText("계정별 응답 비교")).toBeNull();
    // 최근 끝난 점검 줄 (시안 doneRowHtml): 확정 발견의 위험도 딱지 + 발견 수
    const done = within(screen.getByRole("region", { name: "보존된 끝난 점검" })).getByRole("button", {
      name: /job-1/,
    });
    expect(done).not.toHaveTextContent(/심각|높음|발견 4/);
    // 칩: 전체 4 · 심각 1 · 높음 1 · 중간 1 · 위험도 미평가 1 (0건인 낮음은 숨김)
    const chips = within(sec).getByRole("group", { name: "위험도로 거르기" });
    expect(within(chips).queryByRole("button", { name: /낮음/ })).toBeNull();
    fireEvent.click(within(chips).getByRole("button", { name: /높음/ }));
    expect(titles()).toEqual(["발견 4"]);
    expect(within(sec).getByText("1건 · 확정 1")).toBeInTheDocument();
    // 보던 발견(발견 2)이 걸러지면 남은 첫 줄로 상세가 바뀐다
    expect(within(detail).getByText("발견 4")).toBeInTheDocument();
    fireEvent.click(within(chips).getByRole("button", { name: /전체/ }));
    fireEvent.click(within(sec).getByRole("button", { name: /다른 계정 책 조회 5/ }));
    expect(within(detail).getByText("다른 계정 책 조회 5")).toBeInTheDocument();
  });

  it("대시보드: 확정 0 · 미실행 · 결과 불명만 있으면 '취약점 n건'으로 보이지 않는다 (절대 규칙 5)", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(completed);
    a.fetchRemediation.mockResolvedValue({
      task_id: 42,
      entries: [
        { ...candidate(1), outcome: "not_executed", status: "policy_not_executed" },
        { ...candidate(2), verification: "" },
      ],
    });
    mount("dashboard");
    const sec = await screen.findByRole("region", { name: "발견 · 검토 대상" });
    await waitFor(() => expect(sec.querySelectorAll("[data-dsel^='fd-']")).toHaveLength(2));
    expect(within(sec).getByText("2건 · 확정 0")).toBeInTheDocument();
    expect(within(sec).getByText("미실행")).toBeInTheDocument();
    expect(within(sec).getByText("결과 불명")).toBeInTheDocument();
    // 목록 제목 · 건수 어디에도 "취약점"이라 부르지 않는다
    expect(within(sec).queryByText(/취약점/)).toBeNull();
    expect(screen.queryByRole("heading", { name: /취약점/ })).toBeNull();
  });

  it("대시보드: 발견이 6개를 넘으면 처음 6개만, '모두 보기'로 전부 · '접기'로 다시 6개", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(completed);
    a.fetchRemediation.mockResolvedValue({
      task_id: 42,
      entries: Array.from({ length: 8 }, (_, i) => candidate(i + 1)),
    });
    mount("dashboard");
    const sec = await screen.findByRole("region", { name: "발견 · 검토 대상" });
    const rows = () => sec.querySelectorAll("[data-dsel^='fd-']").length;
    await waitFor(() => expect(rows()).toBe(6));
    fireEvent.click(within(sec).getByRole("button", { name: "모두 보기" }));
    expect(rows()).toBe(8);
    fireEvent.click(within(sec).getByRole("button", { name: "접기" }));
    expect(rows()).toBe(6);
  });

  it("토큰은 v2 catalog 로 먼저 확인한다: 401 → 로그인, /api/tasks 는 부르지 않는다", async () => {
    u.readURLCatalog.mockRejectedValue(new ApiError(401, "authentication_required"));
    await act(async () => {
      await checkLiveAuth();
    });
    expect(getLiveAuthState()).toBe("login");
    expect(a.fetchTasks).not.toHaveBeenCalled();
  });

  it("v2 catalog 가 받아 주면 ready — URL 진단 서버의 /api/tasks(하위 경로 리다이렉트)를 부르지 않고 실행 요청도 없다", async () => {
    await act(async () => {
      await checkLiveAuth();
    });
    expect(getLiveAuthState()).toBe("ready");
    expect(a.fetchTasks).not.toHaveBeenCalled();
    expect(u.createURLDraft).not.toHaveBeenCalled();
    expect(u.commandURLDiagnosis).not.toHaveBeenCalled();
  });

  it("v2 준비 서비스만 안 되면(503) ready 로 두고 /api/tasks 로 넘어가지 않는다", async () => {
    u.readURLCatalog.mockRejectedValue(new ApiError(503, "preparation_service_unavailable"));
    await act(async () => {
      await checkLiveAuth();
    });
    expect(getLiveAuthState()).toBe("ready");
    expect(a.fetchTasks).not.toHaveBeenCalled();
  });

  it("v2 가 아니면(404) 기존처럼 /api/tasks 로 확인한다: 성공 → ready, 401 → 로그인", async () => {
    u.readURLCatalog.mockRejectedValue(new ApiError(404, "HTTP 404"));
    await act(async () => {
      await checkLiveAuth();
    });
    expect(a.fetchTasks).toHaveBeenCalledTimes(1);
    expect(getLiveAuthState()).toBe("ready");
    a.fetchTasks.mockRejectedValue(new ApiError(401, "expired"));
    await act(async () => {
      await checkLiveAuth();
    });
    expect(getLiveAuthState()).toBe("login");
  });

  it("공용 조회(점검 목록 · 승인 대기)는 v2 서버와 확인 중에는 부르지 않는다", async () => {
    resetUrl({ kind: "checking" });
    const { unmount } = render(<LiveFeeds />);
    await act(async () => {
      await Promise.resolve();
    });
    expect(a.fetchTasks).not.toHaveBeenCalled();
    act(() => useUrl.setState({ cap: { kind: "v2", catalog } }));
    await act(async () => {
      await Promise.resolve();
    });
    expect(a.fetchTasks).not.toHaveBeenCalled();
    act(() => useUrl.setState({ cap: { kind: "unavailable", message: "미설치" } }));
    await act(async () => {
      await Promise.resolve();
    });
    expect(a.fetchTasks).not.toHaveBeenCalled();
    unmount();
  });
});

it.each([
  "ai_auto_v1",
  "user_approval_v1",
] as const)("초안 저장 어댑터가 %s 모드와 제외 경로를 보존한다", async (mode) => {
  const input = url.urlDiagnosisInput("https://target.example/", "profile-1", "소유 워크플로", mode, "/excluded");
  resetUrl({ kind: "v2", catalog });
  u.createURLDraft.mockResolvedValue({ ...draft, input });
  u.commandURLDiagnosis.mockResolvedValue({ ...ready, input });
  expect(await runUrlAction("validate", input)).toBe(true);
  expect(u.createURLDraft.mock.calls[0][0]).toEqual(input);
  expect(useUrl.getState().handle?.input.workflow_mode).toBe(mode);
  expect(useUrl.getState().job?.input.excluded_paths).toBe("/excluded");
});

it("탐색 화면에서 중지 버전 충돌을 안내하고 GET 확인 전에는 중지를 다시 보내지 않는다", async () => {
  useUrl.setState({
    cap: { kind: "v2", catalog },
    handle,
    job: running,
    verified: false,
    uncertain: true,
    error: "revision_conflict",
  });
  u.readURLStatus.mockResolvedValue(running);
  render(
    <ShellContext.Provider value={shellValue({ route: "crawl" })}>
      <WorkflowCrawlLive />
    </ShellContext.Provider>,
  );
  expect(screen.getByRole("alert")).toHaveTextContent("revision_conflict");
  expect(screen.queryByRole("button", { name: "점검 취소" })).not.toBeInTheDocument();
  expect(u.commandURLDiagnosis).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "상태 다시 조회" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "점검 취소" })).toBeEnabled());
  expect(u.readURLStatus).toHaveBeenCalledTimes(1);
  expect(u.commandURLDiagnosis).not.toHaveBeenCalled();
});

// 자동 모드(preauthorized_scope)는 범위에 쓰기 메서드가 있으면 건별 확인 없이 보낸다(C21). 시작 전 동의 문구도 그렇게 말한다.
it.each([
  [["GET", "HEAD", "POST"], "쓰기(POST) 건별 확인 없이 보냄"],
  [["GET"], "쓰기 요청 없음 (읽기만)"],
])("자동 모드 범위 %j 는 쓰기를 어떻게 보내는지 표시한다", async (methods, short) => {
  const input = { ...ready.input, workflow_mode: "ai_auto_v1" as const };
  u.readURLStatus.mockResolvedValue({
    ...ready,
    input,
    scope: { ...scope, approval_mode: "preauthorized_scope", methods },
  });
  saveHandle({ ...handle, input });
  mount("new");
  // 요약 '쓰기 요청' 줄이 말한다(안전 장치 목록의 긴 승인 방식 줄은 겹쳐 뺐다, 사용자 11/18)
  await screen.findByText("승인된 사이트 및 경로에만 요청");
  const sum = screen.getByRole("complementary", { name: "점검 요약" });
  expect(within(sum).getByText("쓰기 요청").nextElementSibling).toHaveTextContent(short);
  expect(within(sum).queryByText(/임의 query 로 진행/)).toBeNull();
});

// http 대상(C22)은 범위 확인 뒤 안전 장치에 평문 경고를 보인다. https 대상에는 없다(#527 이경준 리뷰).
it.each([
  ["http://target.example", true],
  ["https://target.example", false],
])("범위 origin %s 의 평문 경고 표시: %s", async (origin, warned) => {
  u.readURLStatus.mockResolvedValue({ ...ready, scope: { ...scope, origin } });
  saveHandle(handle);
  mount("new");
  await screen.findByText("승인된 사이트 및 경로에만 요청");
  expect(screen.queryByText("암호화하지 않는 주소 (http)") !== null).toBe(warned);
});

// 전송 통제 정책은 결과가 아니라 범위라 안전 장치 제목 옆에서 연다(작업대 실데이터 계약 §7-(1)).
it("안전 장치 제목 옆 '정책 전체 보기'는 전송 통제 정책 화면으로 간다", async () => {
  u.readURLStatus.mockResolvedValue(ready);
  saveHandle(handle);
  const { goTo } = mount("new");
  await screen.findByRole("heading", { name: "안전 장치" });
  fireEvent.click(screen.getByRole("button", { name: "정책 전체 보기" }));
  expect(goTo).toHaveBeenCalledWith("controlpolicy");
});

it("선택적 승인 범위는 읽기 사전 승인과 쓰기 건별 확인을 구분해 표시한다", async () => {
  const input = { ...ready.input, workflow_mode: "user_approval_v1" as const };
  u.readURLStatus.mockResolvedValue({ ...ready, input, scope: { ...scope, approval_mode: "workbench_selective_v1" } });
  saveHandle({ ...handle, input });
  mount("new");
  await screen.findByText("승인된 사이트 및 경로에만 요청");
  // 요약 '쓰기 요청' 줄이 범위 확인 뒤에는 서버 승인 방식을 따른다 (리뷰 #507). 같은 뜻 긴 줄은 안전 장치에서 뺐다(사용자 11/18)
  expect(screen.queryByText("쓰기 메서드 요청은 한 건씩 승인한 것만 보냄")).toBeNull();
  const sum = screen.getByRole("complementary", { name: "점검 요약" });
  expect(within(sum).getByText("쓰기 요청").nextElementSibling).toHaveTextContent("쓰기 요청 한 건씩 승인");
  expect(within(sum).queryByText(/^범위 확인 전 기본 원칙/)).toBeNull();
  expect(screen.getByText("https://target.example · / · GET")).toBeInTheDocument();
  expect(screen.getByRole("heading", { name: "안전 장치" })).toBeInTheDocument();
  expect(screen.queryByText("모든 요청 · 승인 기록 남김")).toBeNull();
  // 절대규칙 6 고지 (시안 v52): 할 일을 경고 줄 제목으로, 자세한 설명과 원래 고지 문장은 (?) 메모에
  expect(screen.getByText("열면 지워지는 주소는 빼 두세요")).toBeInTheDocument();
  const tip = screen.getByRole("button", { name: "설명 보기" });
  const note = document.getElementById(tip.getAttribute("aria-controls") ?? "") as HTMLElement;
  expect(tip).toHaveAttribute("aria-expanded", "false");
  expect(note).not.toBeVisible();
  fireEvent.click(tip);
  expect(tip).toHaveAttribute("aria-expanded", "true");
  expect(note).toBeVisible();
  expect(note).toHaveTextContent("GET · HEAD 와 query 도 상태를 바꿀 수 있고, 서버는 그 무해성을 검증하지 않습니다");
  expect(note).toHaveTextContent("/delete?id=3");
  // 제외 경로는 경로 단위: query 없이 넣으면 된다는 안내
  expect(note).toHaveTextContent("/delete처럼 경로만 넣으면 query 와 상관없이 빠집니다");
  // 경고 줄 아래 짧은 고지는 기본으로 보인다 (비차단 리뷰 반영)
  expect(screen.getByText("조회 요청도 데이터를 바꿀 수 있고, 서버는 검증하지 않음")).toBeVisible();
  // Esc 로 메모만 닫힌다: 바깥(상세 서랍을 닫는 전역 Esc)으로 퍼지지 않고, 포커스는 (?) 버튼으로
  const outer = vi.fn();
  document.addEventListener("keydown", outer);
  tip.focus();
  fireEvent.keyDown(tip, { key: "Escape" });
  document.removeEventListener("keydown", outer);
  expect(note).not.toBeVisible();
  expect(outer).not.toHaveBeenCalled();
  expect(tip).toHaveFocus();
  // 메모가 닫혀 있을 때의 Esc 는 그대로 전역으로 간다
  const after = vi.fn();
  document.addEventListener("keydown", after);
  fireEvent.keyDown(tip, { key: "Escape" });
  document.removeEventListener("keydown", after);
  expect(after).toHaveBeenCalledTimes(1);
  // 바깥 클릭으로도 닫힌다
  fireEvent.click(tip);
  fireEvent.mouseDown(document.body);
  expect(note).not.toBeVisible();
});

describe("실행 결과 한 줄(urlOutcomeText)", () => {
  const counts = (
    c: Partial<Record<"done" | "policy_limited" | "incomplete" | "unknown" | "stopped" | "goal_met", number>>,
  ) => ({
    done: 0,
    policy_limited: 0,
    incomplete: 0,
    unknown: 0,
    stopped: 0,
    ...c,
  });
  it.each([
    [
      { state: "completed", outcome: "partial", intent_outcomes: counts({ done: 1, incomplete: 2 }) },
      "일부 완료 · 남은 작업 2",
    ],
    [{ state: "completed", outcome: "no_intents", intent_outcomes: counts({}) }, "시도한 계획 없음(검사 안 함)"],
    [
      { state: "completed", outcome: "complete", intent_outcomes: counts({ done: 1, policy_limited: 2 }) },
      "계획한 작업 모두 마감 · 정책상 검증 안 됨 2",
    ],
    [
      {
        state: "completed",
        outcome: "partial",
        closing_reason: "budget_limited",
        intent_outcomes: counts({ incomplete: 1 }),
      },
      "일부 완료 · 남은 작업 1 · 예산 상한에서 마감 · 찾은 취약점은 보존",
    ],
    [{ state: "cancelled", intent_outcomes: counts({ done: 1, stopped: 2 }) }, "중지됨 · 끝난 1 / 끊긴 2"],
    [
      { state: "cancelled", intent_outcomes: counts({ done: 1, goal_met: 2, stopped: 4 }) },
      "중지됨 · 끝난 1 · 목표 달성(계획자 판단)으로 중단 2 / 끊긴 4",
    ],
    [
      { state: "completed", outcome: "partial", intent_outcomes: counts({ done: 1, incomplete: 1, unknown: 2 }) },
      "일부 완료 · 남은 작업 1 · 결과 불명 2",
    ],
    [
      { state: "completed", outcome: "partial", intent_outcomes: counts({ done: 1, unknown: 1 }) },
      "일부 완료 · 결과 불명 1",
    ],
    // 목표 달성으로 작업이 끝나 멈춘 작업은 남은 작업이 아니라 complete에 따로 보인다(#875 발견 6).
    [
      { state: "completed", outcome: "complete", intent_outcomes: counts({ done: 3, policy_limited: 2, goal_met: 2 }) },
      "계획한 작업 모두 마감 · 정책상 검증 안 됨 2 · 목표 달성(계획자 판단)으로 중단 2",
    ],
    [{ state: "completed" }, ""],
  ] as const)("%o → %s", (job, text) => {
    expect(urlOutcomeText(job as Parameters<typeof urlOutcomeText>[0])).toBe(text);
  });
});

describe("시험 계정 인증 표시(urlAccountAuthRows, §3.4·§4)", () => {
  it("결과를 §4 문구로 매핑하고 계정 라벨 순으로 정렬한다", () => {
    expect(
      urlAccountAuthRows({ shopper: "login_failed", "acct-a": "authenticated", admin_2: "session_not_found" }),
    ).toEqual([
      ["acct-a", "인증됨"],
      ["admin_2", "비인증 진행 · 세션 없음"],
      ["shopper", "비인증 진행 · 로그인 실패"],
    ]);
  });
  it("인증 못 한 결과는 전부 '비인증 진행'으로 보인다(접근 차단·성공 아님, 절대 규칙 5)", () => {
    const rows = urlAccountAuthRows({ a: "login_failed", b: "session_not_found", c: "result_unknown", d: "not_sent" });
    for (const [, label] of rows) {
      expect(label).toMatch(/^비인증 진행/);
      expect(label).not.toMatch(/접근|차단|성공|확인됨/);
    }
  });
});

describe("외부 모델 시험 계정 동의", () => {
  async function externalTarget(hasTest = true, failAccounts = false, withStatement = true) {
    u.readURLCatalog.mockResolvedValue({
      ...catalog,
      profiles: [
        { ...catalog.profiles[0], model: externalModel },
        { ...catalog.profiles[0], id: "external-2", model: externalModel },
      ],
    });
    vi.mocked(teamApi.apiFetch).mockImplementation(async (path) => {
      const p = String(path);
      if (p.includes("targets/resolve"))
        return {
          target: {
            id: "target-1",
            origin: new URL(new URL(p, "http://fixture.test").searchParams.get("url") ?? "https://target.example/")
              .origin,
            state: "approved",
          },
        } as never;
      if (p.endsWith("/accounts") && failAccounts) throw new Error("fixed-account-unavailable");
      if (p.endsWith("/accounts"))
        return {
          accounts: hasTest ? [{ label: "tester", classification: "test" }] : [],
          ...(withStatement ? { external_consent_statement: SERVER_EXTERNAL_ACCOUNT_STATEMENT } : {}),
        } as never;
      return { credential: { configured: true, model: externalModel.model } } as never;
    });
    u.createURLDraft.mockImplementation(async (input) => ({ ...draft, input }));
    u.commandURLDiagnosis.mockImplementation(async (current) => ({ ...ready, input: current.input }));
    mount("new");
    const target = await screen.findByLabelText(/점검할 사이트 주소/);
    fireEvent.change(target, { target: { value: "https://target.example/app" } });
    // 분석 모델 · 외부 전송 동의 칸은 1쪽(사용자 11/24). 범위 확인은 toStart() 로 2쪽에 가서
    await screen.findByText("등록됨");
    return target;
  }
  it("새 점검에 다시 들어오면 그사이 등록한 시험 계정을 다시 읽어 동의 칸을 보인다(F2와 같은 종류, 2026-11-22)", async () => {
    // 처음엔 시험 계정이 없다 → 동의 칸 없음. 계정 분류 화면에서 계정을 등록한 뒤 새 점검으로 돌아오면 다시 읽어야 한다
    let registered = false;
    const view = await (async () => {
      u.readURLCatalog.mockResolvedValue({ ...catalog, profiles: [{ ...catalog.profiles[0], model: externalModel }] });
      vi.mocked(teamApi.apiFetch).mockImplementation(async (path) => {
        const p = String(path);
        if (p.includes("targets/resolve"))
          return { target: { id: "target-1", origin: "https://target.example", state: "approved" } } as never;
        if (p.endsWith("/accounts"))
          return {
            accounts: registered ? [{ label: "tester", classification: "test" }] : [],
            external_consent_statement: SERVER_EXTERNAL_ACCOUNT_STATEMENT,
          } as never;
        return { credential: { configured: true, model: externalModel.model } } as never;
      });
      const r = mount("new");
      fireEvent.change(await screen.findByLabelText(/점검할 사이트 주소/), {
        target: { value: "https://target.example/app" },
      });
      await screen.findByText("등록됨");
      return r;
    })();
    expect(screen.queryByRole("checkbox", { name: /이 점검 회차에서 시험 계정 세션/ })).toBeNull();
    registered = true; // 계정 분류 화면에서 시험 계정 등록
    view.rerender(
      <ShellContext.Provider value={shellValue({ route: "classify", goTo: view.goTo })}>
        <UrlFeeds />
        <NewScanByMode />
      </ShellContext.Provider>,
    );
    view.rerender(
      <ShellContext.Provider value={shellValue({ route: "new", goTo: view.goTo })}>
        <UrlFeeds />
        <NewScanByMode />
      </ShellContext.Provider>,
    );
    // 새 점검에 다시 들어오면 1쪽부터
    expect(await screen.findByRole("checkbox", { name: /이 점검 회차에서 시험 계정 세션/ })).toBeInTheDocument();
  });
  it("기본 미선택·명시적 동의만 초안에 전송하고 저장소에는 남기지 않는다", async () => {
    await externalTarget();
    const checkbox = await screen.findByRole("checkbox", { name: /이 점검 회차에서 시험 계정 세션/ });
    expect(checkbox).not.toBeChecked();
    // 외부 전송 동의도 오른쪽 요약 '약관 동의'에(사용자 11/24, 원래 분석 모델 칸)
    expect(screen.getByRole("complementary", { name: "점검 요약" })).toContainElement(checkbox);
    expect(screen.getByText(/체크하지 않으면 시험 계정 없이 진행/)).toBeInTheDocument();
    // 체크하지 않아도 범위 확인은 보낼 수 있다
    tickStartConfirm();
    expect(screen.getAllByRole("button", { name: "승인 범위 확인" })[0]).toBeEnabled();
    fireEvent.click(checkbox);
    fireEvent.click(screen.getAllByRole("button", { name: "승인 범위 확인" })[0]);
    await waitFor(() => expect(u.createURLDraft).toHaveBeenCalledTimes(1));
    expect(u.createURLDraft.mock.calls[0][0].external_test_accounts).toBe(true);
    expect(JSON.stringify(sessionStorage)).not.toContain("external_test_accounts");
    expect(JSON.stringify(localStorage)).not.toContain("external_test_accounts");
  });
  it("대상 변경 뒤 돌아와도 이전 동의가 살아나지 않는다", async () => {
    const target = await externalTarget();
    fireEvent.click(await screen.findByRole("checkbox", { name: /이 점검 회차에서 시험 계정 세션/ }));
    fireEvent.change(target, { target: { value: "https://second.example/app" } });
    fireEvent.change(target, { target: { value: "https://target.example/app" } });
    expect(await screen.findByRole("checkbox", { name: /이 점검 회차에서 시험 계정 세션/ })).not.toBeChecked();
  });
  it("모델을 바꿨다가 돌아와도 동의는 초기화된다", async () => {
    await externalTarget();
    fireEvent.click(await screen.findByRole("checkbox", { name: /이 점검 회차에서 시험 계정 세션/ }));
    fireEvent.change(screen.getByLabelText("제공자"), { target: { value: "external-2" } });
    fireEvent.change(screen.getByLabelText("제공자"), { target: { value: "profile-1" } });
    expect(await screen.findByRole("checkbox", { name: /이 점검 회차에서 시험 계정 세션/ })).not.toBeChecked();
  });
  it("계정 조회 실패는 동의를 만들지 않고 시험 계정 없이 진행할 수 있다", async () => {
    await externalTarget(true, true);
    expect(await screen.findByRole("alert")).toHaveTextContent("시험 계정 등록 상태를 확인하지 못했습니다");
    // [계정 다시 조회] 버튼은 뺐다(사용자 11/24, 팀 공유)
    expect(screen.queryByRole("button", { name: "계정 다시 조회" })).toBeNull();
    expect(screen.queryByRole("checkbox", { name: /이 점검 회차에서 시험 계정 세션/ })).toBeNull();
    tickStartConfirm();
    expect(screen.getAllByRole("button", { name: "승인 범위 확인" })[0]).toBeEnabled();
    expect(u.createURLDraft).not.toHaveBeenCalled();
  });
  it("서버 진술이 없으면 체크 칸 없이 시험 계정 없는 점검을 허용한다", async () => {
    await externalTarget(true, false, false);
    expect(await screen.findByText(/외부 전송 동의 문장을 받지 못해 시험 계정 없이 진행/)).toBeInTheDocument();
    expect(screen.queryByRole("checkbox", { name: /이 점검 회차에서 시험 계정 세션/ })).toBeNull();
    tickStartConfirm();
    expect(screen.getAllByRole("button", { name: "승인 범위 확인" })[0]).toBeEnabled();
  });
  it("새 초안은 이전 회차 동의를 초기화한다", async () => {
    await externalTarget();
    fireEvent.click(await screen.findByRole("checkbox", { name: /이 점검 회차에서 시험 계정 세션/ }));
    act(() => disconnectUrl());
    fireEvent.change(screen.getByLabelText(/점검할 사이트 주소/), { target: { value: "https://target.example/app" } });
    expect(await screen.findByRole("checkbox", { name: /이 점검 회차에서 시험 계정 세션/ })).not.toBeChecked();
  });
  it("실제 시험 계정이 없으면 동의 칸을 만들지 않는다", async () => {
    await externalTarget(false);
    expect(screen.queryByRole("checkbox", { name: /이 점검 회차에서 시험 계정 세션/ })).toBeNull();
    tickStartConfirm();
    await waitFor(() => expect(screen.getAllByRole("button", { name: "승인 범위 확인" })[0]).toBeEnabled());
  });
});

it("외부 모델에서 실제 시험 계정이 있어도 미선택이면 동의 필드를 생략한다", async () => {
  u.readURLCatalog.mockResolvedValue({ ...catalog, profiles: [{ ...catalog.profiles[0], model: externalModel }] });
  vi.mocked(teamApi.apiFetch).mockImplementation(async (path) => {
    if (String(path).includes("targets/resolve"))
      return { target: { id: "target-1", origin: "https://target.example", state: "approved" } } as never;
    if (String(path).endsWith("/accounts"))
      return {
        accounts: [{ label: "tester", classification: "test" }],
        external_consent_statement: SERVER_EXTERNAL_ACCOUNT_STATEMENT,
      } as never;
    return { credential: { configured: true, model: externalModel.model } } as never;
  });
  mount("new");
  fireEvent.change(await screen.findByLabelText(/점검할 사이트 주소/), {
    target: { value: "https://target.example/app" },
  });
  const checkbox = await screen.findByRole("checkbox", { name: /이 점검 회차에서 시험 계정 세션/ });
  expect(checkbox).not.toBeChecked();
  tickStartConfirm();
  await waitFor(() => expect(screen.getAllByRole("button", { name: "승인 범위 확인" })[0]).toBeEnabled());
  fireEvent.click(screen.getAllByRole("button", { name: "승인 범위 확인" })[0]);
  await waitFor(() => expect(u.createURLDraft).toHaveBeenCalledTimes(1));
  expect(u.createURLDraft.mock.calls[0][0]).not.toHaveProperty("external_test_accounts");
});

it("결속된 외부 시험 계정 동의 회차는 정규화·새로고침 뒤에도 '시험 계정 로그인'으로 보인다 (리뷰 이동찬)", async () => {
  // 저장된 회차 입력은 external_test_accounts=true(끝에 / 로 정규화된 target_url). 새 초안용 consentChecked 는 false 지만,
  // 안내·요약은 저장된 서버 입력을 기준으로 한다(정규화로 consentKey 가 달라지거나 새로고침으로 로컬 상태가 초기화돼도).
  u.readURLCatalog.mockResolvedValue({ ...catalog, profiles: [{ ...catalog.profiles[0], model: externalModel }] });
  vi.mocked(teamApi.apiFetch).mockResolvedValue({} as never);
  const consented = { ...draft.input, external_test_accounts: true as const };
  saveHandle({ ...handle, input: consented, startKey: "start-1", startRevision: handle.revision });
  u.readURLStatus.mockResolvedValue({ ...running, input: consented });
  mount("new");
  const sum = await screen.findByRole("complementary", { name: "점검 요약" });
  const login = () => within(sum).getByText("로그인").nextElementSibling;
  await waitFor(() => expect(login()).toHaveTextContent("시험 계정 로그인"));
  expect(login()).not.toHaveTextContent("로그인 없이 진행");
});

it("로컬 모델 회차도 대상에 시험 계정이 있으면 '시험 계정 로그인'으로 안내한다 (리뷰 윤재상)", async () => {
  // 로컬 모델(external_transfer 없음)은 외부 전송 동의 없이도 시험 계정을 결속한다(서버 runBindsAccounts). 조회해서 있으면 로그인으로 안내한다.
  vi.mocked(teamApi.apiFetch).mockImplementation(async (path) => {
    const p = String(path);
    if (p.includes("targets/resolve"))
      return { target: { id: "target-1", origin: "https://target.example", state: "approved" } } as never;
    if (p.endsWith("/accounts")) return { accounts: [{ label: "tester", classification: "test" }] } as never;
    return { configured: true } as never;
  });
  mount("new");
  fireEvent.change(await screen.findByLabelText(/점검할 사이트 주소/), {
    target: { value: "https://target.example/app" },
  });
  const sum = screen.getByRole("complementary", { name: "점검 요약" });
  await waitFor(() => expect(within(sum).getByText("로그인").nextElementSibling).toHaveTextContent("시험 계정 로그인"));
  // 로컬은 외부 전송 동의 체크 칸을 만들지 않는다(동의 없이 결속)
  expect(screen.queryByRole("checkbox", { name: /이 점검 회차에서 시험 계정 세션/ })).toBeNull();
});

it("보고서 재조회가 실패하면 이전 성공 자료로 이번 오류를 숨기지 않는다", async () => {
  useUrl.setState({ cap: { kind: "v2", catalog }, handle, job: completed, selectedID: completed.id, verified: true });
  a.fetchRemediation.mockResolvedValue({ task_id: 42, entries: [candidate(1)] });
  const frame = (route: View) => (
    <ShellContext.Provider value={shellValue({ route })}>
      <ReportByMode />
    </ShellContext.Provider>
  );
  const view = render(frame("report"));
  await waitFor(() => expect(screen.getByRole("region", { name: "판정 집계" })).toBeInTheDocument());
  view.rerender(frame("live"));
  a.fetchRemediation.mockRejectedValue(new ApiError(503, "unavailable"));
  view.rerender(frame("report"));
  expect(await screen.findByRole("alert")).toHaveTextContent("보고서 조회 실패 (HTTP 503)");
  expect(screen.queryByRole("region", { name: "판정 집계" })).toBeNull();
  expect(screen.queryByRole("button", { name: /다른 계정 책 조회 1/ })).toBeNull();
});

it("대시보드 보고서 재조회 오류도 이전 성공의 발견 목록으로 가리지 않는다", async () => {
  useUrl.setState({ cap: { kind: "v2", catalog }, handle, job: completed, selectedID: completed.id, verified: true });
  a.fetchRemediation.mockResolvedValue({ task_id: 42, entries: [candidate(1)] });
  const frame = (route: View) => (
    <ShellContext.Provider value={shellValue({ route })}>
      <DashboardByMode />
    </ShellContext.Provider>
  );
  const view = render(frame("dashboard"));
  await screen.findByRole("button", { name: /다른 계정 책 조회 1/ });
  view.rerender(frame("live"));
  a.fetchRemediation.mockRejectedValue(new ApiError(503, "unavailable"));
  view.rerender(frame("dashboard"));
  expect(await screen.findByRole("alert")).toHaveTextContent("보고서 조회 실패 (HTTP 503)");
  expect(screen.queryByRole("button", { name: /다른 계정 책 조회 1/ })).toBeNull();
});

it("주소 옆 [URL 인증]: 등록 전엔 버튼, 누르면 주소 바로 밑에 소유 확인 등록 칸, 승인되면 '인증됨' (사용자 11/24)", async () => {
  const approvedTarget = {
    id: `target-${"b".repeat(32)}`,
    origin: "https://target.example",
    state: "approved",
    requests: 3,
    port: 443,
    path_prefixes: ["/"],
    methods: ["GET"],
    addresses: ["10.200.1.10"],
    tls_spki_sha256: "c".repeat(64),
    expires_at: "2099-01-01T00:00:00Z",
  };
  let resolveReply: unknown = {};
  vi.mocked(teamApi.apiFetch).mockImplementation(async (path) => {
    if (String(path).includes("targets/resolve")) return resolveReply as never;
    return { configured: true } as never;
  });
  mount("new");
  const urlInput = await screen.findByLabelText(/점검할 사이트 주소/);
  // 주소 전에는 눌러도 갈 곳이 없어 꺼 둔다
  expect(screen.getByRole("button", { name: "URL 인증" })).toBeDisabled();
  fireEvent.change(urlInput, { target: { value: "https://target.example/app" } });
  const auth = screen.getByRole("button", { name: "URL 인증" });
  await waitFor(() => expect(auth).toBeEnabled());
  // 주소와 같은 줄
  expect(urlInput.parentElement).toContainElement(auth);
  expect(screen.queryByRole("button", { name: "대상 등록하기" })).toBeNull();
  expect(auth).toHaveAttribute("aria-expanded", "false");
  fireEvent.click(auth);
  const basis = await screen.findByRole("checkbox", { name: /점검 권한이 있고/ });
  expect(auth).toHaveAttribute("aria-expanded", "true");
  // 등록 칸은 점검 이름보다 위(주소 바로 밑)
  const name = screen.getByLabelText(/점검 이름/);
  expect(basis.compareDocumentPosition(name) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  // 다시 누르면 닫히고, 또 누르면 열린다(접기 버튼 없음)
  expect(screen.queryByRole("button", { name: "접기" })).toBeNull();
  fireEvent.click(auth);
  await waitFor(() => expect(screen.queryByRole("checkbox", { name: /점검 권한이 있고/ })).toBeNull());
  expect(auth).toHaveAttribute("aria-expanded", "false");
  fireEvent.click(auth);
  expect(await screen.findByRole("checkbox", { name: /점검 권한이 있고/ })).toBeInTheDocument();
  cleanup();
  resolveReply = { target: approvedTarget };
  mount("new");
  fireEvent.change(await screen.findByLabelText(/점검할 사이트 주소/), {
    target: { value: "https://target.example/app" },
  });
  expect(await screen.findByText("인증됨")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "URL 인증" })).toBeNull();
  expect(screen.getByRole("button", { name: "승인 철회" })).toBeInTheDocument();
  // 서버가 답하지 않아도(형 서버 꺼짐 · 503) 눌러서 등록 칸을 열 수 있다 · 등록은 확인될 때까지 꺼짐
  cleanup();
  resolveReply = new ApiError(503, "mirror_offline_not_saved");
  vi.mocked(teamApi.apiFetch).mockImplementation(async (path) => {
    if (String(path).includes("targets/resolve")) throw resolveReply;
    return { configured: true } as never;
  });
  mount("new");
  fireEvent.change(await screen.findByLabelText(/점검할 사이트 주소/), {
    target: { value: "https://target.example/app" },
  });
  const retry = await screen.findByRole("button", { name: "대상 승인 다시 조회" });
  expect(retry).toBeInTheDocument();
  const again = screen.getByRole("button", { name: "URL 인증" });
  await waitFor(() => expect(again).toBeEnabled());
  fireEvent.click(again);
  expect(await screen.findByRole("checkbox", { name: /점검 권한이 있고/ })).toBeDisabled();
  expect(screen.getByRole("button", { name: "대상 등록" })).toBeDisabled();
});

describe("새 점검의 E③ 확정 단계 안내 · 대상 리셋 확인", () => {
  const ZERO = "0001-01-01T00:00:00Z";
  // 대상 칸이 받는 서버 승인 대상 모양(TargetApprovalPanel targetReply): id 형식 · 승인 칸 · TLS 고정값
  const TARGET_ID = `target-${"a".repeat(32)}`;
  const declaration = (roles = true) => ({
    version: "e3-product-declaration/v1",
    target_ref: TARGET_ID,
    target_approval_revision: "3",
    declared_by: "operator-1",
    declared_at: "2026-11-18T00:00:00.000000Z",
    identity: { path: "/api/me", subject_id_path: "id" },
    types: [
      {
        type: "note",
        sharing: "owner_only",
        owner_only_fields: ["body"],
        create: {
          path: "/api/notes",
          method: "POST",
          body_kind: "json",
          body_template: '{"title":"t"}',
          marker_field: "body",
          id_path: "id",
        },
        read: { path_template: "/api/notes/{id}", data_path: "." },
      },
    ],
    ...(roles
      ? { owner_account_label: "tester-a", cross_account_label: "tester-b", accounts_same_privilege: true }
      : {}),
  });
  /** accounts: 대상 계정 응답(없으면 계정 없음) · "fail" 조회 실패 · "pending" 끝나지 않음. decl 이 "pending" 이면 선언 조회가 끝나지 않음 */
  function serve(decl: unknown, reset: unknown, accounts?: unknown[] | "fail" | "pending") {
    vi.mocked(teamApi.apiFetch).mockImplementation(async (path) => {
      const p = String(path);
      if (p.endsWith(`/${TARGET_ID}/accounts`)) {
        if (accounts === "fail") throw new Error("fixed-account-unavailable");
        if (accounts === "pending") return new Promise(() => undefined) as never;
        return { accounts: accounts ?? [] } as never;
      }
      if (p.includes("targets/resolve"))
        return {
          target: {
            id: TARGET_ID,
            origin: new URL(new URL(p, "http://fixture.test").searchParams.get("url") ?? "https://target.example/")
              .origin,
            state: "approved",
            requests: 3,
            port: 443,
            path_prefixes: ["/"],
            methods: ["GET"],
            addresses: ["10.200.1.10"],
            tls_spki_sha256: "a".repeat(64),
            expires_at: "2099-01-01T00:00:00Z",
          },
        } as never;
      if (p.endsWith(`/${TARGET_ID}/declaration`))
        return (decl === "pending" ? new Promise(() => undefined) : decl) as never;
      if (p.endsWith(`/${TARGET_ID}/reset-confirmation`)) return reset as never;
      return { configured: true } as never;
    });
  }
  const resetRequired = {
    reset: {
      reset_required: true,
      last_writes_job: "job-7",
      last_writes_at: "2026-11-20T01:02:03Z",
      last_writes_objects: 1,
      last_writes_ref: "b".repeat(64),
      confirmed_at: ZERO,
      statement: "확정 단계가 남긴 시험 객체를 지웠습니다.",
    },
  };
  const noWrites = { reset: { reset_required: false, last_writes_at: ZERO, confirmed_at: ZERO, statement: "s" } };
  const active = (decl: ReturnType<typeof declaration>) => ({
    state: "active",
    target_revision: 3,
    declared_by: decl.declared_by,
    declared_at: decl.declared_at,
    declaration: decl,
  });

  it("선언이 확정 단계를 켤 모양이면 시작 요약에 시험 객체 안내를, 리셋 확인 전이면 경고와 확인 상자를 보인다", async () => {
    serve(active(declaration()), resetRequired);
    saveHandle();
    mount("new");
    const summary = await screen.findByRole("complementary", { name: "점검 요약" });
    expect(await within(summary).findByText("확정 단계: 대상에 시험 객체를 만듭니다")).toBeInTheDocument();
    expect(summary).toHaveTextContent("소유자 전용 종류 1개 · 계정 tester-a가 만들고 tester-b가 읽어 봄");
    expect(await within(summary).findByText("대상 리셋 확인 전")).toBeInTheDocument();
    const box = await screen.findByRole("region", { name: "대상 리셋 확인" });
    expect(box).toHaveTextContent("시험 객체 1개");
    expect(within(box).getByRole("button", { name: "리셋 확인" })).toBeDisabled();
  });

  it("역할이 없는 선언·쓰기 기록 없음이면 안내도 리셋 상자도 없다", async () => {
    serve(active(declaration(false)), noWrites);
    saveHandle();
    mount("new");
    const summary = await screen.findByRole("complementary", { name: "점검 요약" });
    await within(summary).findByText("승인된 사이트 및 경로에만 요청");
    await waitFor(() =>
      expect(
        vi
          .mocked(teamApi.apiFetch)
          .mock.calls.some(([path]) => String(path).endsWith(`/${TARGET_ID}/reset-confirmation`)),
      ).toBe(true),
    );
    expect(within(summary).queryByText("확정 단계: 대상에 시험 객체를 만듭니다")).toBeNull();
    expect(within(summary).queryByText("확정 단계 여부를 확인하지 못했습니다")).toBeNull();
    expect(screen.queryByRole("region", { name: "대상 리셋 확인" })).toBeNull();
  });

  // 시작 거절 경고는 서버 리셋 게이트(e3RunStartRefusal)와 같은 조건일 때만: 유효한 선언 + 지난 쓰기 + 리셋 확인 없음
  it("처음 점검하는 대상(쓰기 기록 없음)은 선언이 있어도 시작 거절 경고가 없다", async () => {
    serve(active(declaration()), noWrites);
    saveHandle();
    mount("new");
    const summary = await screen.findByRole("complementary", { name: "점검 요약" });
    expect(await within(summary).findByText("확정 단계: 대상에 시험 객체를 만듭니다")).toBeInTheDocument();
    expect(within(summary).queryByText("대상 리셋 확인 전")).toBeNull();
    expect(screen.queryByRole("region", { name: "대상 리셋 확인" })).toBeNull();
  });

  // 사용자 11/24: 수평만이라 시험 계정은 A(소유) · B(교차) 둘 — 이름을 적어 추가하는 상자 대신 두 줄 고정. 이름은 정책 선언의
  // 소유 · 교차 계정 이름. 아래 따로 있던 계정 목록은 뺐고 '이번 회차 로그인' 줄만. 정책 선언 상자는 선언만
  const STATEMENT = "소유 · 승인된 일회용 시험 계정이라는 진술";
  function serveQuick(initial: Array<Record<string, unknown>>, statement = STATEMENT) {
    serve(active(declaration()), noWrites, initial);
    const base = vi.mocked(teamApi.apiFetch).getMockImplementation();
    const calls: string[] = [];
    const sent: string[] = [];
    let rows = [...initial];
    vi.mocked(teamApi.apiFetch).mockImplementation(async (path, init) => {
      const p = String(path);
      const m = init?.method ?? "GET";
      if (p.includes(`/${TARGET_ID}/accounts`) && m !== "GET") {
        calls.push(`${m} ${p.split(`/${TARGET_ID}`)[1]}`);
        sent.push(String(init?.body ?? ""));
        const label = p.split("/accounts/")[1]?.split("/")[0];
        if (m === "POST" && p.endsWith("/accounts"))
          rows.push({ label: JSON.parse(String(init?.body)).label, classification: "real" });
        if (p.endsWith("/mark-test"))
          rows = rows.map((r) => (r.label === label ? { ...r, classification: "test" } : r));
        if (p.endsWith("/unmark")) rows = rows.map((r) => (r.label === label ? { ...r, classification: "real" } : r));
        if (p.endsWith("/credential")) {
          rows = rows.map((r) => (r.label === label ? { ...r, credential_configured: true } : r));
          return { account: { label, credential_configured: true } } as never;
        }
        return {} as never;
      }
      if (p.endsWith(`/${TARGET_ID}/accounts`))
        return { accounts: rows, ...(statement ? { account_statement: statement } : {}) } as never;
      return (base as NonNullable<typeof base>)(path, init);
    });
    return { calls, sent };
  }

  // #914 인계: 자격은 저장됐는데 대상 로그인 방식이 없으면(login_method_missing) 시작 전에 알리고, '이번에는 익명으로 점검'을
  // 직접 골라야 넘어간다
  it("로그인 방식이 없으면 안내하고 '이번에는 익명으로 점검'을 골라야 시작할 수 있다", async () => {
    serveQuick([
      { label: "tester-a", classification: "test", credential_configured: true, login_method_missing: true },
      { label: "tester-b", classification: "test", credential_configured: true, login_method_missing: true },
    ]);
    saveHandle();
    mount("new");
    const note = await screen.findByRole("note", { name: "로그인 방식 없음" });
    expect(note).toHaveTextContent(
      "계정은 저장됐습니다. 이 대상의 로그인 방식이 설정되지 않아 인증 점검을 시작할 수 없습니다.",
    );
    // MFA · 지원 안 함 같은 짐작은 하지 않는다
    expect(note).not.toHaveTextContent(/MFA|지원하지 않/);
    // 안내는 되돌린 로그인 칸 안에 있고(사용자 11/24 저녁: 계정 분류 · 정책 선언 칸 대신 전 로그인 칸), 이번 회차는 로그인하지 않는다
    expect(screen.queryByRole("region", { name: "계정 분류" })).toBeNull();
    const summary = screen.getByRole("complementary", { name: "점검 요약" });
    expect(summary).not.toHaveTextContent("시험 계정 로그인");
    // 범위 확인 뒤(초안 있음)라 시작 칸에서 막는다
    expect(screen.getByText(/이번에는 익명으로 점검'을 고르면 시작할 수 있습니다/)).toBeInTheDocument();
    const anon = within(note).getByRole("checkbox", { name: "이번에는 익명으로 점검" });
    expect(anon).not.toBeChecked();
    fireEvent.click(anon);
    expect(anon).toBeChecked();
    expect(screen.queryByText(/이번에는 익명으로 점검'을 고르면 시작할 수 있습니다/)).toBeNull();
  });

  it("로컬 모델 · 시험 계정 등록됨이면 점검 요약 로그인 칸이 '시험 계정 로그인'", async () => {
    serve(active(declaration()), noWrites, [{ label: "tester", classification: "test" }]);
    saveHandle();
    mount("new");
    const summary = await screen.findByRole("complementary", { name: "점검 요약" });
    await waitFor(() => expect(summary).toHaveTextContent("시험 계정 로그인"));
  });
  it("선언이 없으면 지난 쓰기가 있어도 서버가 거절하지 않으므로 경고는 없고 리셋 확인 상자만 둔다", async () => {
    serve({ state: "none", declaration: null }, resetRequired);
    saveHandle();
    mount("new");
    const summary = await screen.findByRole("complementary", { name: "점검 요약" });
    expect(await screen.findByRole("region", { name: "대상 리셋 확인" })).toHaveTextContent("시험 객체 1개");
    expect(within(summary).queryByText("대상 리셋 확인 전")).toBeNull();
    expect(within(summary).queryByText("확정 단계 여부를 확인하지 못했습니다")).toBeNull();
  });

  it("역할이 없는 선언이라도 유효하면 서버가 거절하므로 리셋 확인 전 경고를 보인다", async () => {
    serve(active(declaration(false)), resetRequired);
    saveHandle();
    mount("new");
    const summary = await screen.findByRole("complementary", { name: "점검 요약" });
    expect(await within(summary).findByText("대상 리셋 확인 전")).toBeInTheDocument();
    expect(within(summary).queryByText("확정 단계: 대상에 시험 객체를 만듭니다")).toBeNull();
  });

  it("선언을 읽지 못하면 '확정 단계 없음'으로 숨기지 않고 모른다고 보인다", async () => {
    serve({ state: "broken" }, noWrites);
    saveHandle();
    mount("new");
    const summary = await screen.findByRole("complementary", { name: "점검 요약" });
    expect(await within(summary).findByText("확정 단계 여부를 확인하지 못했습니다")).toBeInTheDocument();
    expect(within(summary).queryByText("확정 단계: 대상에 시험 객체를 만듭니다")).toBeNull();
  });
});

describe("새 점검의 시작 확인 (제외 경로 검토 · 복사본/백업)", () => {
  const limits = {
    format: "abc2lab.workbench-flow/v1" as const,
    max_pages: 10,
    max_endpoints: 20,
    max_response_bytes: 1000,
    allow_reversible_writes: false,
  };
  const confirmedInput = { ...draft.input, ...START_CONFIRMED };
  const send = () => screen.getAllByRole("button", { name: "승인 범위 확인" })[0];
  /** 탐색 흐름이 있는 프로필로 주소까지 넣는다(1쪽 점검 설정에 제외 경로 칸이 보인다, 사용자 11/24). */
  async function page2() {
    u.readURLCatalog.mockResolvedValue({ ...catalog, profiles: [{ ...catalog.profiles[0], workflow: limits }] });
    mount("new");
    const target = await screen.findByLabelText(/점검할 사이트 주소/);
    fireEvent.change(target, { target: { value: "https://target.example/" } });
    return target;
  }
  /** [승인 범위 확인]이 켜졌는지 */
  const sendEnabled = () => !(send() as HTMLButtonElement).disabled;

  it("GET 으로 상태가 바뀌는 주소 · 제외 경로 안내 문장은 두지 않고(사용자 11/24, 팀 공유), 제외 경로 칸은 비어 있다", async () => {
    await page2();
    // 안내 상자(예시 · 'GET 요청은 따로 막지 않습니다')만 뺐다. 약관 동의의 제외 경로 검토 진술 문장은 그대로
    expect(screen.queryByText(/로그아웃, 삭제 링크, 구독 해지/)).toBeNull();
    expect(screen.queryByText(/GET 요청은 따로 막지 않습니다/)).toBeNull();
    expect(exclusionsBox()).toBeInTheDocument();
    expect(screen.getByLabelText(/^제외 경로/)).toHaveValue("");
    expect(exclusionsBox()).not.toBeChecked();
    expect(backupBox()).not.toBeChecked();
  });

  it("두 진술이 다 있어야 초안을 만들고, 입력에 두 진술이 true 로 실린다", async () => {
    await page2();
    expect(sendEnabled()).toBe(false);
    fireEvent.click(backupBox());
    expect(sendEnabled()).toBe(false);
    fireEvent.click(exclusionsBox());
    expect(sendEnabled()).toBe(true);
    // 하나를 풀면 다시 꺼진다
    fireEvent.click(backupBox());
    expect(sendEnabled()).toBe(false);
    fireEvent.click(backupBox());
    await act(async () => {
      fireEvent.click(send());
    });
    expect(u.createURLDraft).toHaveBeenCalledTimes(1);
    expect(u.createURLDraft.mock.calls[0][0]).toEqual({
      target_url: "https://target.example/",
      profile_id: "profile-1",
      workflow_mode: "user_approval_v1",
      exclusions_reviewed: true,
      backup_confirmed: true,
    });
  });

  it("제외 경로 · 진단 모드 · 대상을 바꾸면 앞서 한 확인이 풀린다", async () => {
    const target = await page2();
    tickStartConfirm();
    expect(sendEnabled()).toBe(true);
    // 제외 경로를 고치면 그 목록을 보고 한 확인이 아니게 된다
    fireEvent.change(screen.getByLabelText(/^제외 경로/), { target: { value: "/logout" } });
    expect(exclusionsBox()).not.toBeChecked();
    expect(backupBox()).not.toBeChecked();
    expect(sendEnabled()).toBe(false);
    tickStartConfirm();
    expect(sendEnabled()).toBe(true);
    fireEvent.click(screen.getByRole("radio", { name: /^AI 자동/ }));
    expect(exclusionsBox()).not.toBeChecked();
    expect(backupBox()).not.toBeChecked();
    tickStartConfirm();
    // 대상을 바꿨다가 같은 주소로 돌아와도 이전 확인은 살아나지 않는다
    fireEvent.change(target, { target: { value: "https://second.example/" } });
    fireEvent.change(target, { target: { value: "https://target.example/" } });
    expect(exclusionsBox()).not.toBeChecked();
    expect(backupBox()).not.toBeChecked();
    expect(sendEnabled()).toBe(false);
    expect(u.createURLDraft).not.toHaveBeenCalled();
  });

  it("초안이 있으면 서버에 기록된 진술을 읽기 전용으로 보인다", async () => {
    saveHandle({ ...handle, input: confirmedInput });
    u.readURLStatus.mockResolvedValue({ ...ready, input: confirmedInput });
    mount("new");
    await screen.findByText("https://target.example · / · GET");
    expect(exclusionsBox()).toBeChecked();
    expect(exclusionsBox()).toBeDisabled();
    expect(backupBox()).toBeChecked();
    expect(backupBox()).toBeDisabled();
    expect(screen.getByText("이 점검을 만들 때 기록된 확인입니다.")).toBeVisible();
  });

  it("진술이 없는 초안(이 칸이 생기기 전 · CLI)은 체크된 것처럼 보이지 않는다", async () => {
    saveHandle();
    mount("new");
    await screen.findByText("https://target.example · / · GET");
    expect(exclusionsBox()).not.toBeChecked();
    expect(exclusionsBox()).toBeDisabled();
    expect(backupBox()).not.toBeChecked();
    expect(backupBox()).toBeDisabled();
  });

  it("점검 정보에는 기록된 진술만 '시작 전 확인' 줄로 보인다", () => {
    const view = render(<UrlFacts job={{ ...running, input: confirmedInput }} />);
    expect(screen.getByText("시작 전 확인").nextElementSibling).toHaveTextContent(
      "제외 경로 검토함 · 복사본/백업 있음",
    );
    view.rerender(<UrlFacts job={{ ...running, input: { ...draft.input, backup_confirmed: true } }} />);
    expect(screen.getByText("시작 전 확인").nextElementSibling).toHaveTextContent(/^복사본\/백업 있음$/);
    view.rerender(<UrlFacts job={running} />);
    expect(screen.queryByText("시작 전 확인")).toBeNull();
  });
});
