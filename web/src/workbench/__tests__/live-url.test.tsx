// live URL 진단(diagnosis-api.v2): 서버 방식 확인(catalog) → 새 점검(초안 + 범위 확인 → 확인 상자 + 시작)
// → 점검 진행(상태 조회 · 중지 · 실제 task 전송 통제) → 보고서(결과 저장 확인 뒤). 팀 lib 의 요청 함수만 모의하고
// 검증 · 결속 함수(matchURLJob · matchURLHandle · urlDiagnosisInput · 핸들 저장)는 진짜를 쓴다.

import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/diagnosis/diagnosis-url", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/diagnosis-url")>()),
  readURLCatalog: vi.fn(),
  createURLDraft: vi.fn(),
  commandURLDiagnosis: vi.fn(),
  readURLReceipt: vi.fn(),
  readURLStatus: vi.fn(),
}));
vi.mock("@/lib/diagnosis/api", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/api")>()),
  apiFetch: vi.fn(),
  readAuthStatus: vi.fn(),
  restoreSession: vi.fn(),
}));
vi.mock("../live/api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../live/api")>()),
  fetchRequestApprovals: vi.fn(),
  fetchRemediation: vi.fn(),
  fetchDiagnosisRun: vi.fn(),
  fetchTasks: vi.fn(),
}));

import * as teamApi from "@/lib/diagnosis/api";
import { ApiError } from "@/lib/diagnosis/api";
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
import { LiveFeeds } from "../live/feeds";
import { LiveRailTasks } from "../live/rail";
import { resetUrl, runUrlAction, URL_POLL_MS, UrlFeeds, urlGates, useUrl } from "../live/urlDiagnosis";
import { urlTargetHint, urlTone, urlViewFor } from "../live/urlMap";
import { type Shell, ShellContext, type View } from "../shell";
import { createInitialState, useWb } from "../store";
import { DashboardByMode, LiveByMode, NewScanByMode, ReportByMode, ScopeByMode } from "../views/live/byMode";
import { EXCLUDE_EXAMPLE } from "../views/live/UrlNewScanLive";
import { WorkflowCrawlLive } from "../views/live/workflowViews";

const u = vi.mocked(url);
const a = vi.mocked(api);
const noop = () => undefined;

it("URL 입력 뒤 외부 LLM을 펼쳐 모델·키를 등록하고 범위 조회만 한다", async () => {
  u.readURLCatalog.mockResolvedValue({ ...catalog, profiles: [{ ...catalog.profiles[0], model: externalModel }] });
  vi.mocked(teamApi.apiFetch).mockResolvedValue({ configured: true });
  mount("new");
  const target = await screen.findByLabelText(/점검할 사이트 주소/);
  fireEvent.change(target, { target: { value: "https://target.example/app" } });
  // 설치된 프로필이 외부 모델뿐이면 '외부 API' 가 골라져 있다
  expect(screen.getByRole("radio", { name: "외부 API" })).toBeChecked();
  expect(screen.getByLabelText("모델 이름")).toHaveValue(externalModel.model);
  fireEvent.change(screen.getByLabelText("API 키"), { target: { value: "synthetic-input-provider-key" } });
  fireEvent.click(screen.getByRole("button", { name: "키 등록" }));
  expect(screen.getByLabelText("API 키")).toHaveValue("");
  await waitFor(() => expect(screen.getAllByRole("button", { name: "승인 범위 확인" })[0]).toBeEnabled());
  expect(u.createURLDraft).not.toHaveBeenCalled();
  expect(u.commandURLDiagnosis).not.toHaveBeenCalled();
  fireEvent.click(screen.getAllByRole("button", { name: "승인 범위 확인" })[0]);
  await waitFor(() => expect(u.createURLDraft).toHaveBeenCalledTimes(1));
  const input = u.createURLDraft.mock.calls[0][0];
  expect(Object.keys(input).sort()).toEqual(["profile_id", "target_url"]);
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

/** 후보 한 건 (팀 diagnosticOutcome 이 review 로 분류하는 모양). */
function candidate(n: number): api.Remediation["entries"][number] {
  return {
    entry_id: `candidate-${n}`,
    title: `후보 ${n}`,
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
  u.readURLReceipt.mockResolvedValue(draft);
  a.fetchRequestApprovals.mockResolvedValue(APPROVALS);
  a.fetchRemediation.mockResolvedValue({ task_id: 42, entries: [] });
  a.fetchDiagnosisRun.mockResolvedValue(RUN_RECORD);
  a.fetchTasks.mockResolvedValue({ active: "", tasks: [] });
  vi.mocked(teamApi.readAuthStatus).mockResolvedValue({ initialized: true });
  vi.mocked(teamApi.restoreSession).mockResolvedValue(true);
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe("URL 진단 계산", () => {
  it("주소 안내: HTTPS 만 · 계정 정보 · query/fragment · 인코딩", () => {
    expect(urlTargetHint("")).toBe("");
    expect(urlTargetHint("http://target.example/")).toBe("HTTPS 주소만 받습니다.");
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
  it("catalog 가 오면 URL 진단 폼, 기존 v1 칸은 없다", async () => {
    mount("new");
    expect(await screen.findByLabelText(/점검할 사이트 주소/)).toBeInTheDocument();
    // 서버가 받지 않는 칸(계정 로그인 · 점검 목표 · 범위 · 워커 수)은 없다. 워크플로 없는 프로필은 제외 경로도 없다
    expect(screen.queryByLabelText(/계정 A/)).toBeNull();
    expect(screen.queryByLabelText(/워커 수/)).toBeNull();
    expect(screen.queryByLabelText(/제외 경로/)).toBeNull();
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
    expect(screen.queryByLabelText(/작업 이름/)).toBeNull();
    expect(screen.getByRole("heading", { name: "점검 설정" })).toBeInTheDocument();
    // 제외 경로 칸의 흐린 예시는 안전 장치 (?) 메모와 같은 주소 (시안 v50)
    expect(screen.getByLabelText(/제외 경로/)).toHaveAttribute("placeholder", "/logout\n/delete");
    // 작업 이름 없이 주소만으로 범위 확인을 보낼 수 있고, 입력에 display_name 이 없다
    fireEvent.change(target, { target: { value: "https://target.example/" } });
    const send = screen.getAllByRole("button", { name: "승인 범위 확인" })[0];
    expect(send).toBeEnabled();
    await act(async () => {
      fireEvent.click(send);
    });
    expect(u.createURLDraft.mock.calls[0][0]).toEqual({
      target_url: "https://target.example/",
      profile_id: "profile-1",
      workflow_mode: "user_approval_v1",
    });
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

  it("탐색 흐름이 없는 프로필은 '점검 설정' 칸을 그리지 않는다", async () => {
    mount("new");
    await screen.findByLabelText(/점검할 사이트 주소/);
    expect(screen.queryByRole("heading", { name: "점검 설정" })).toBeNull();
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
  it("URL · 프로필로 초안을 한 번 만들고 범위 확인까지 한 뒤 서버가 정한 범위를 오른쪽에 보인다", async () => {
    const { goTo } = mount("new");
    const target = (await screen.findByLabelText(/점검할 사이트 주소/)) as HTMLInputElement;
    const send = () => screen.getAllByRole("button", { name: "승인 범위 확인" });
    expect(send()[0]).toBeDisabled();
    fireEvent.change(target, { target: { value: "http://target.example/" } });
    expect(screen.getAllByText("HTTPS 주소만 받습니다.").length).toBeGreaterThan(0);
    expect(send()[0]).toBeDisabled();
    fireEvent.change(target, { target: { value: "https://target.example/" } });
    expect((screen.getByLabelText("모델") as HTMLSelectElement).value).toBe("profile-1");
    // 범위 확인 전에는 서버 범위가 없다
    expect(screen.queryByText("승인된 사이트 · 경로에만 요청")).toBeNull();
    await act(async () => {
      fireEvent.click(send()[0]);
    });
    expect(u.createURLDraft).toHaveBeenCalledTimes(1);
    const [input, key, who] = u.createURLDraft.mock.calls[0];
    expect(input).toEqual({ target_url: "https://target.example/", profile_id: "profile-1" });
    expect(typeof key).toBe("string");
    expect(who).toBe(owner);
    expect(u.commandURLDiagnosis).toHaveBeenCalledWith(draft, "validate", owner);
    // 같은 화면에서 서버가 돌려준 범위 · 상한과 시작 단계가 나온다(다른 화면으로 가지 않는다)
    expect(await screen.findByText("승인된 사이트 · 경로에만 요청")).toBeInTheDocument();
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
    expect(
      screen.getByText(/^대상 요청 3회 · 60초 · 모델 요청 2회 · 토큰 입력 \d+ · 출력 \d+ · body 최대 \d+ bytes$/),
    ).toBeInTheDocument();
    const start = screen.getByRole("button", { name: "점검 시작" });
    expect(start).toBeDisabled();
    fireEvent.click(screen.getByRole("checkbox"));
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
    const consent = await screen.findByRole("checkbox", { name: /대상 응답·도구 출력의 외부 전송/ });
    expect(screen.getByText(new RegExp(externalModel.provider.replace("/", "\\/")))).toBeInTheDocument();
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
  it("시작 결과가 불명이면 다시 보내지 않고 상태 조회만 남긴다", async () => {
    saveHandle();
    u.commandURLDiagnosis.mockRejectedValue(new TypeError("network"));
    const { goTo } = mount("new");
    await screen.findByText("https://target.example · / · GET");
    fireEvent.click(screen.getByRole("checkbox"));
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
    expect(screen.getByRole("checkbox")).toBeDisabled();
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
    expect(screen.queryByRole("checkbox")).toBeNull();
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
    expect(await screen.findByText("엔진 실행 중")).toBeInTheDocument();
    expect(u.readURLStatus).toHaveBeenCalledWith(handle.id, owner);
    expect(u.createURLDraft).not.toHaveBeenCalled();
    expect(u.commandURLDiagnosis).not.toHaveBeenCalled();
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

  it("마감된 진단은 요청 승인을 다시 부르지 않고 보고서의 실행 종료 기록으로 안내한다", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(completed);
    const { goTo } = mount("live");
    expect(await screen.findByText(/점검을 마쳤습니다/)).toBeInTheDocument();
    expect(screen.getByText(/마감된 점검의 전송 기록은 보고서의 증거 · 감사에 있습니다/)).toBeInTheDocument();
    expect(a.fetchRequestApprovals).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: /보고서 보기/ }));
    expect(goTo).toHaveBeenCalledWith("report");
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
    expect(u.commandURLDiagnosis).toHaveBeenCalledWith(running, "stop", owner, undefined);
    expect(await screen.findByText("중지·마감 중")).toBeInTheDocument();
  });

  it("보고서는 결과 저장이 확인된 뒤에만 실제 task 의 조치 자료를 부른다", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(running);
    mount("report");
    expect(await screen.findByText(/결과가 저장되면 보고서가 나옵니다/)).toBeInTheDocument();
    expect(a.fetchRemediation).not.toHaveBeenCalled();
    cleanup();
    resetUrl();
    u.readURLStatus.mockResolvedValue(completed);
    a.fetchRemediation.mockResolvedValue({ task_id: 42, entries: [candidate(1)] });
    mount("report");
    expect(await screen.findByRole("heading", { level: 1, name: "target.example 보고서" })).toBeInTheDocument();
    await waitFor(() => expect(a.fetchRemediation).toHaveBeenCalledWith("42", "internal"));
    expect(a.fetchDiagnosisRun).toHaveBeenCalledWith("42");
    // 판정 집계: 후보는 검토 필요, 확정 0 (팀 lib 분류)
    const strip = await screen.findByRole("region", { name: "판정 집계" });
    const cell = (label: string) => within(strip).getByText(label).parentElement as HTMLElement;
    await waitFor(() => expect(within(cell("검토 필요")).getByText("1")).toBeInTheDocument());
    expect(within(cell("확정")).getByText("0")).toBeInTheDocument();
    // 발견 목록에 후보가 있다
    expect(
      within(screen.getByRole("tabpanel", { name: "발견 목록" })).getByRole("button", { name: /후보 1/ }),
    ).toBeInTheDocument();
    // 실행 종료 기록: 실행 ID · 정상 종료 · 감사 · E② 집계는 저장 값 그대로 (증거 · 감사 탭)
    fireEvent.click(screen.getByRole("tab", { name: "증거 · 감사" }));
    const record = await screen.findByRole("region", { name: "실행 종료 기록" });
    expect(within(record).getByText("정상 종료")).toBeInTheDocument();
    expect(within(record).getByText("run-1")).toBeInTheDocument();
    expect(within(record).getByText("완결")).toBeInTheDocument();
    expect(within(within(record).getByText("차단").parentElement as HTMLElement).getByText("2")).toBeInTheDocument();
    // 모델 사용량은 서버 기록이 없으면 그렇다고 말한다
    expect(screen.getByText("모델 사용량 기록이 아직 없습니다.")).toBeInTheDocument();
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
    // 레일 줄은 점검 기록 화면으로 간다 (시안 v27)
    fireEvent.click(row);
    expect(goTo).toHaveBeenCalledWith("records");
    expect(screen.getByText("끝난 점검 없음")).toBeInTheDocument();
    // 지금 보는 화면이 이 점검이 아니면 빨간 현재 표시를 하지 않는다
    expect(row).not.toHaveAttribute("aria-current");
  });
});

describe("대시보드 · 인증 (URL 진단)", () => {
  it("대시보드: 이어 보는 진단은 진행 중인 점검에 한 줄, 상세에서 알맞은 화면으로 간다", async () => {
    saveHandle();
    u.readURLStatus.mockResolvedValue(running);
    const { goTo } = mount("dashboard");
    const active = await screen.findByRole("region", { name: "진행 중인 점검" });
    const row = await within(active).findByRole("button", { name: /target\.example/ });
    expect(within(row).getByText("엔진 실행 중")).toBeInTheDocument();
    expect(screen.getByText("끝난 점검이 없습니다.")).toBeInTheDocument();
    fireEvent.click(row);
    expect(screen.getByText("합성 모델")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /점검 보기/ }));
    expect(goTo).toHaveBeenCalledWith("live");
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

it("선택적 승인 범위는 읽기 사전 승인과 쓰기 건별 확인을 구분해 표시한다", async () => {
  const input = { ...ready.input, workflow_mode: "user_approval_v1" as const };
  u.readURLStatus.mockResolvedValue({ ...ready, input, scope: { ...scope, approval_mode: "workbench_selective_v1" } });
  saveHandle({ ...handle, input });
  mount("new");
  expect(await screen.findByText("쓰기 메서드 요청은 한 건씩 승인한 것만 보냄")).toBeInTheDocument();
  expect(screen.getByText("https://target.example · / · GET")).toBeInTheDocument();
  expect(screen.getByRole("heading", { name: "안전 장치" })).toBeInTheDocument();
  expect(screen.getByText("모든 요청 · 승인 기록 남김")).toBeInTheDocument();
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
