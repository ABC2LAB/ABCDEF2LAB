import "@testing-library/jest-dom/vitest";
import { type ReactNode, useState } from "react";

import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { signIn, signOut } from "@/lib/diagnosis/api";
import { activeURLRun, endedURLRun, type URLRunSummary } from "@/lib/diagnosis/diagnosis-runs";
import { readURLHandle, storeURLHandle, type URLDiagnosisJob } from "@/lib/diagnosis/diagnosis-url";
import { catalog, owner, ready, running } from "@/lib/diagnosis/diagnosis-url-fixtures";

import { LiveGate, setLiveAuthState, useLiveAuthBoot } from "../live/auth";
import { LiveRailTasks } from "../live/rail";
import { useSource } from "../live/source";
import { resetUrl, runUrlAction, UrlFeeds, urlGates, useUrl } from "../live/urlDiagnosis";
import { urlHost, urlStateText } from "../live/urlMap";
import { loadUrlRuns, refreshUrlRunsQuietly, useUrlRuns } from "../live/urlRunList";
import { type Shell, ShellContext, type View } from "../shell";
import { DashboardByMode, FinishedByMode, LiveByMode, ReportByMode, RunningByMode } from "../views/live/byMode";
import { Download } from "../views/live/UrlLiveLive";
import s from "../workbench.module.css";

const B_OWNER = "b".repeat(64);
const a: URLDiagnosisJob = { ...running, id: "job-a", input: { ...running.input, display_name: "첫 번째 점검" } };
const b: URLDiagnosisJob = {
  ...running,
  id: "job-b",
  task_id: "43",
  revision: 7,
  state: "completed",
  phase: "report",
  drain_complete: true,
  result_ready: true,
  input: { ...running.input, display_name: "보존된 점검" },
};
const c: URLDiagnosisJob = {
  ...b,
  id: "job-c",
  task_id: "44",
  state: "failed",
  reason_code: "execution_failed",
  input: { ...running.input, display_name: "실패한 점검" },
};
const createdA = {
  ownerContext: owner,
  creationKey: "real-created-key",
  startKey: "real-start-key",
  startRevision: a.revision,
  input: a.input,
  id: a.id,
  revision: a.revision,
  sourceDigest: a.source_digest,
};
const toRow = (job: URLDiagnosisJob): URLRunSummary => ({
  id: job.id,
  revision: job.revision,
  display_name: job.input.display_name,
  state: job.state,
  phase: job.phase,
  reason_code: job.reason_code,
  task_id: job.task_id,
  started: job.started,
  // 서버가 확인한 시작 · 마감 시각은 있을 때만 싣는다(product-run-list 계약 선택 칸)
  ...(job.started_at ? { started_at: job.started_at } : {}),
  ...(job.finished_at ? { finished_at: job.finished_at } : {}),
  drain_complete: job.drain_complete,
  result_ready: job.result_ready,
  report_available: job.result_ready && !!job.task_id,
  approval_status: "not_revalidated",
  action_required: ["preparing", "running", "stopping", "awaiting_approval", "unknown", "failed"].includes(job.state),
});
const page = (rows: URLRunSummary[], over: Record<string, unknown> = {}, who = owner) => ({
  version: "diagnosis-api.v2",
  owner_context: who,
  page: { runs: rows, snapshot: "c".repeat(64), limit: 25, more: false, ...over },
});
const json = (data: unknown, status = 200) =>
  new Response(JSON.stringify(data), { status, headers: { "Content-Type": "application/json" } });
type Route = (url: URL, init: RequestInit) => Promise<Response> | Response | undefined;
let jobs: Record<string, URLDiagnosisJob>;
let route: Route;
let requests: Array<{ path: string; method: string; body?: unknown; authorization: string }>;
const fakeFetch = async (input: RequestInfo | URL, init: RequestInit = {}) => {
  const url = new URL(String(input), "http://workbench.test");
  const authorization = new Headers(init.headers).get("Authorization") ?? "";
  requests.push({
    path: url.pathname + url.search,
    method: init.method ?? "GET",
    body: init.body ? JSON.parse(String(init.body)) : undefined,
    authorization,
  });
  if (url.pathname === "/api/auth/login") {
    const user = JSON.parse(String(init.body)).username;
    return json({ token: user === "other" ? "synthetic-b-token" : "synthetic-a-token" });
  }
  if (url.pathname === "/api/auth/status") return json({ initialized: true });
  if (!authorization) return json({ error: "authentication_required" }, 401);
  const intercepted = await route(url, init);
  if (intercepted) return intercepted;
  const who = authorization.includes("synthetic-b-token") ? B_OWNER : owner;
  if (url.pathname.endsWith("/catalog"))
    return json({ version: "diagnosis-api.v2", owner_context: who, profiles: catalog.profiles });
  if (url.pathname.endsWith("/runs"))
    return json(
      page(
        who === owner
          ? Object.values(jobs)
              .map(toRow)
              .sort((x, y) =>
                activeURLRun(x) !== activeURLRun(y)
                  ? Number(activeURLRun(y)) - Number(activeURLRun(x))
                  : y.id.localeCompare(x.id),
              )
          : [],
        {},
        who,
      ),
    );
  const match = /\/drafts\/([^/]+)\/status$/.exec(url.pathname);
  if (match) {
    const job = jobs[match[1]];
    return job
      ? json({ version: "diagnosis-api.v2", owner_context: who, draft: job })
      : json({ error: "not_found" }, 404);
  }
  if (url.pathname === "/api/exploration/activity") {
    const task = url.searchParams.get("task");
    return json({
      items: [
        {
          seq: 1,
          intent_id: "7",
          worker: "worker-1",
          ts: "",
          kind: "tool",
          tool: "bash",
          is_error: false,
          summary: `서버 활동 task ${task}`,
        },
      ],
      cursor: 1,
    });
  }
  if (url.pathname === "/api/report/diagnostic/remediation")
    return json({ task_id: Number(url.searchParams.get("task")), entries: [] });
  if (url.pathname === "/api/tasks") throw new Error("일반 tasks API로 제품 목록을 대체함");
  return json({ error: "fixture_not_provided" }, 404);
};
function Harness({ initial = "dashboard" }: { initial?: View }) {
  const [view, setView] = useState<View>(initial);
  useLiveAuthBoot();
  const shell: Shell = {
    route: view,
    navSeq: 1,
    narrow: false,
    phone: false,
    reduce: false,
    inspOpen: true,
    railOpen: false,
    railNewAtRoute: false,
    goTo: setView,
    openInsp: () => undefined,
    closeInsp: () => undefined,
    openRail: () => undefined,
    closeRail: () => undefined,
    foldRail: () => undefined,
    dashPick: () => undefined,
  };
  const body: Partial<Record<View, ReactNode>> = {
    dashboard: <DashboardByMode />,
    running: <RunningByMode />,
    finished: <FinishedByMode />,
    live: <LiveByMode />,
    report: <ReportByMode />,
  };
  return (
    <ShellContext.Provider value={shell}>
      <UrlFeeds />
      <LiveGate title="점검 목록">
        <nav>
          {(["dashboard", "running", "finished", "live", "report"] as const).map((v) => (
            <button key={v} type="button" onClick={() => setView(v)}>{`테스트 이동 ${v}`}</button>
          ))}
        </nav>
        {body[view]}
      </LiveGate>
    </ShellContext.Provider>
  );
}
const posts = () => requests.filter((r) => r.method !== "GET" && !r.path.includes("/auth/login"));
const pick = async (name: string) => {
  fireEvent.click(await screen.findByRole("button", { name: new RegExp(name) }));
  await waitFor(() => expect(useUrl.getState().job?.input.display_name).toBe(name));
};

beforeEach(async () => {
  cleanup();
  signOut();
  sessionStorage.clear();
  localStorage.clear();
  resetUrl();
  // 이 파일의 대시보드 시험은 점검별(점검 하나) 대시보드를 본다 — 기본 보기는 전체(통합 대시보드, 사용자 11/26)
  localStorage.setItem("abc2lab-dash-mode", "run");
  jobs = structuredClone({ [a.id]: a, [b.id]: b, [c.id]: c });
  route = () => undefined;
  requests = [];
  vi.stubGlobal("fetch", vi.fn(fakeFetch));
  await signIn("operator", "synthetic-password");
  setLiveAuthState("ready");
  useSource.setState({ source: "live" });
  requests = [];
});
afterEach(() => {
  cleanup();
  resetUrl();
  vi.unstubAllGlobals();
});

describe("서버 목록에서 새 세션으로 기존 Job 다시 연결", () => {
  it("브라우저 핸들 없이 보존 Job 세 개를 표시하고 URL·시각·판정을 만들지 않는다", async () => {
    render(<Harness />);
    for (const name of ["첫 번째 점검", "보존된 점검", "실패한 점검"])
      expect(await screen.findByRole("button", { name: new RegExp(name) })).toBeInTheDocument();
    expect(document.querySelectorAll("[data-url-run-id]")).toHaveLength(3);
    expect(screen.queryByText("https://target.example/")).toBeNull();
    expect(screen.queryByText("취약점 없음")).toBeNull();
    expect(useUrl.getState().handle).toBeNull();
    expect(posts()).toEqual([]);
    expect(requests.some((r) => r.path === "/api/tasks")).toBe(false);
  });
  it("같은 이름 · 같은 대상의 끝난 점검도 줄마다 주소 · 진단 모드 · 시작 · 소요로 구분한다(사용자 11/24)", async () => {
    const x: URLDiagnosisJob = {
      ...b,
      id: "job-x",
      task_id: "51",
      input: { ...b.input, display_name: "같은 이름", workflow_mode: "user_approval_v1" },
      started_at: "2026-11-24T04:46:56Z",
      finished_at: "2026-11-24T05:12:24Z",
    };
    const y: URLDiagnosisJob = {
      ...x,
      id: "job-y",
      task_id: "52",
      input: { ...x.input, workflow_mode: "ai_auto_v1" },
      started_at: "2026-11-24T06:17:32Z",
      finished_at: "2026-11-24T06:44:05Z",
    };
    jobs = structuredClone({ [x.id]: x, [y.id]: y });
    render(<Harness initial="finished" />);
    const rowX = await screen.findByRole("button", { name: /51\.\s*같은 이름/ });
    const rowY = screen.getByRole("button", { name: /52\.\s*같은 이름/ });
    // 주소 · 모드는 그 점검 상태를 한 번 읽어 채운다(목록 응답엔 없다)
    await waitFor(() => expect(rowX).toHaveTextContent("사용자 승인"));
    await waitFor(() => expect(rowY).toHaveTextContent("AI 자동"));
    expect(rowX).toHaveTextContent(urlHost(b.input.target_url));
    // 시각은 서버가 확인한 값만: 시작(MM-DD HH:MM) · 소요
    expect(rowX).toHaveTextContent(/\d{2}-\d{2} \d{2}:\d{2} 시작/);
    expect(rowX).toHaveTextContent("소요 25분");
    expect(rowY).toHaveTextContent("소요 26분");
    expect(requests.filter((r) => /\/drafts\/job-[xy]\/status$/.test(r.path)).length).toBeGreaterThanOrEqual(2);
  });
  it("대시보드엔 [새로고침] · [목록 새로 조회]가 없고 점검 목록은 바탕에서 깜빡임 없이 다시 읽는다(사용자 11/24)", async () => {
    const { unmount } = render(<Harness initial="dashboard" />);
    expect(await screen.findByRole("button", { name: /첫 번째 점검/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "새로고침" })).toBeNull();
    expect(screen.queryByRole("button", { name: "목록 새로 조회" })).toBeNull();
    // 다른 브라우저가 새 점검을 시작하면 바탕 다시 읽기로 목록에 들어온다. 읽는 동안 보던 목록은 그대로
    const d: URLDiagnosisJob = { ...a, id: "job-d", input: { ...a.input, display_name: "다른 곳에서 시작한 점검" } };
    jobs[d.id] = structuredClone(d);
    let during: number | null = null;
    const done = new Promise<void>((resolve) => {
      const stop = useUrlRuns.subscribe((st) => {
        if (st.loading && during === null) during = st.page?.runs.length ?? 0;
        if (!st.loading && during !== null) {
          stop();
          resolve();
        }
      });
    });
    act(() => refreshUrlRunsQuietly(owner));
    await act(async () => {
      await done;
    });
    expect(during).toBe(3);
    expect(await screen.findByRole("button", { name: /다른 곳에서 시작한 점검/ })).toBeInTheDocument();
    expect(useUrlRuns.getState().error).toBe("");
    unmount();
    // 끝난 점검 목록 화면엔 [목록 새로 조회]가 그대로 있다
    render(<Harness initial="finished" />);
    expect(await screen.findByRole("button", { name: "목록 새로 조회" })).toBeInTheDocument();
  });
  it("선택한 ID의 status를 검증하고 생성·시작 키 없이 task 활동·보고서를 연다", async () => {
    render(<Harness />);
    await pick("첫 번째 점검");
    expect(useUrl.getState().handle).toMatchObject({
      kind: "recovered",
      id: a.id,
      revision: a.revision,
      sourceDigest: a.source_digest,
    });
    expect(useUrl.getState().handle?.creationKey).toBeUndefined();
    expect(useUrl.getState().handle?.startKey).toBeUndefined();
    expect(requests.some((r) => r.path === `/api/diagnoses/v2/drafts/${a.id}/status`)).toBe(true);
    expect(urlGates(useUrl.getState()).canStart).toBe(false);
    expect(urlGates(useUrl.getState()).canValidate).toBe(false);
    expect(await runUrlAction("start")).toBe(false);
    expect(await runUrlAction("validate")).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "테스트 이동 live" }));
    expect(await screen.findByText("서버 활동 task 42")).toBeInTheDocument();
    // #641의 진행 요약 짜임에서도 #642의 복구 task 활동과 조회 전용 결속을 함께 보존한다.
    const summary = screen.getByRole("complementary", { name: "진행 요약" });
    expect(within(summary).getByRole("heading", { level: 3, name: "전송 통제" })).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "서버 진단 활동" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "테스트 이동 finished" }));
    await pick("보존된 점검");
    fireEvent.click(screen.getByRole("button", { name: "테스트 이동 report" }));
    await waitFor(() =>
      expect(requests.some((r) => r.path.startsWith("/api/report/diagnostic/remediation?task=43"))).toBe(true),
    );
    expect(posts()).toEqual([]);
    const saved = sessionStorage.getItem("abc2lab.urlDiagnosisSelection.v1");
    expect(saved && JSON.parse(saved)).toEqual({ ownerContext: owner, id: b.id });
  });
  it("목록 revision보다 새 status를 받되 다른 task 응답은 붙이지 않는다", async () => {
    route = (url) =>
      url.pathname.endsWith(`/${a.id}/status`)
        ? json({ version: "diagnosis-api.v2", owner_context: owner, draft: { ...a, revision: 9 } })
        : undefined;
    render(<Harness />);
    await pick("첫 번째 점검");
    expect(useUrl.getState().job?.revision).toBe(9);
    route = (url) =>
      url.pathname.endsWith(`/${b.id}/status`)
        ? json({ version: "diagnosis-api.v2", owner_context: owner, draft: { ...b, task_id: "99" } })
        : undefined;
    fireEvent.click(screen.getByRole("button", { name: /보존된 점검/ }));
    await waitFor(() => expect(useUrl.getState().error).toMatch(/계약/));
    expect(useUrl.getState().job).toBeNull();
    expect(posts()).toEqual([]);
  });
  it("Job A 조회 중 B를 고르면 A의 늦은 응답을 폐기한다", async () => {
    let finish: (r: Response) => void = () => undefined;
    route = (url) =>
      url.pathname.endsWith(`/${a.id}/status`)
        ? new Promise<Response>((resolve) => {
            finish = resolve;
          })
        : undefined;
    render(<Harness />);
    fireEvent.click(await screen.findByRole("button", { name: /첫 번째 점검/ }));
    await pick("보존된 점검");
    await act(async () => {
      finish(json({ version: "diagnosis-api.v2", owner_context: owner, draft: a }));
    });
    expect(useUrl.getState().job?.id).toBe(b.id);
    expect(useUrl.getState().flow).toBeNull();
    expect(posts()).toEqual([]);
  });
  it("다른 브라우저처럼 저장소를 비워도 같은 운영자의 서버 Job을 다시 찾는다", async () => {
    const first = render(<Harness />);
    await pick("보존된 점검");
    first.unmount();
    sessionStorage.clear();
    resetUrl();
    render(<Harness />);
    await pick("보존된 점검");
    expect(useUrl.getState().job?.task_id).toBe("43");
    expect(posts()).toEqual([]);
  });
  it("복구 선택 힌트만 있어도 새로고침 후 status를 다시 확인한다", async () => {
    const first = render(<Harness />);
    await pick("보존된 점검");
    first.unmount();
    resetUrl();
    render(<Harness />);
    await waitFor(() => expect(useUrl.getState().job?.id).toBe(b.id));
    expect(useUrl.getState().handle?.creationKey).toBeUndefined();
    expect(posts()).toEqual([]);
  });
  it("기존 생성 핸들을 유지하면서 서버의 다른 회차도 표시한다", async () => {
    storeURLHandle({
      ownerContext: owner,
      creationKey: "real-created-key",
      input: a.input,
      id: a.id,
      revision: a.revision,
      sourceDigest: a.source_digest,
    });
    render(<Harness />);
    await waitFor(() => expect(useUrl.getState().job?.id).toBe(a.id));
    expect(await screen.findByRole("button", { name: /보존된 점검/ })).toBeInTheDocument();
    expect(useUrl.getState().handle?.creationKey).toBe("real-created-key");
    expect(posts()).toEqual([]);
  });

  it("다른 회차를 보고 돌아와도 만든 회차의 생성·시작 핸들이 남는다", async () => {
    storeURLHandle({
      ownerContext: owner,
      creationKey: "real-created-key",
      startKey: "real-start-key",
      startRevision: a.revision,
      input: a.input,
      id: a.id,
      revision: a.revision,
      sourceDigest: a.source_digest,
    });
    render(<Harness />);
    await waitFor(() => expect(useUrl.getState().job?.id).toBe(a.id));
    fireEvent.click(screen.getByRole("button", { name: "테스트 이동 finished" }));
    await pick("보존된 점검");
    expect(useUrl.getState().handle?.kind).toBe("recovered");
    expect(readURLHandle(owner)?.id).toBe(a.id);
    fireEvent.click(screen.getByRole("button", { name: "테스트 이동 running" }));
    await pick("첫 번째 점검");
    expect(useUrl.getState().handle?.kind).not.toBe("recovered");
    expect(useUrl.getState().handle?.creationKey).toBe("real-created-key");
    expect(useUrl.getState().handle?.startKey).toBe("real-start-key");
    expect(posts()).toEqual([]);
  });

  it("다른 회차를 선택한 채 새로고침해도 만든 회차의 키와 조회 선택을 분리해 보존한다", async () => {
    storeURLHandle({
      ownerContext: owner,
      creationKey: "real-created-key",
      startKey: "real-start-key",
      startRevision: a.revision,
      input: a.input,
      id: a.id,
      revision: a.revision,
      sourceDigest: a.source_digest,
    });
    const first = render(<Harness />);
    await waitFor(() => expect(useUrl.getState().job?.id).toBe(a.id));
    fireEvent.click(screen.getByRole("button", { name: "테스트 이동 finished" }));
    await pick("보존된 점검");
    first.unmount();
    resetUrl();
    render(<Harness />);
    await waitFor(() => expect(useUrl.getState().job?.id).toBe(b.id));
    expect(useUrl.getState().handle?.kind).toBe("recovered");
    expect(readURLHandle(owner)?.startKey).toBe("real-start-key");
    fireEvent.click(screen.getByRole("button", { name: "테스트 이동 running" }));
    await pick("첫 번째 점검");
    expect(useUrl.getState().handle?.kind).not.toBe("recovered");
    expect(useUrl.getState().handle?.startKey).toBe("real-start-key");
    expect(posts()).toEqual([]);
  });
  it.each([
    "revoked",
    "expired",
    "changed",
    "unavailable",
  ] as const)("승인 %s 상태를 정상 실행 권한으로 표시하지 않는다", async (approval) => {
    route = (url) =>
      url.pathname.endsWith("/runs") ? json(page([{ ...toRow(a), approval_status: approval }])) : undefined;
    render(<Harness />);
    const row = await screen.findByRole("button", { name: /첫 번째 점검/ });
    const labels = {
      revoked: "승인 철회",
      expired: "승인 만료",
      changed: "승인 변경",
      unavailable: "승인 상태 확인 불가",
    };
    expect(row).toHaveTextContent(labels[approval]);
    const pill = row.querySelector(`.${s.pill}`);
    expect(pill).toHaveTextContent(`엔진 실행 중 · ${labels[approval]}`);
    expect(pill).toHaveClass(s.wait);
    expect(posts()).toEqual([]);
  });
  it("저장소에 없는 생성 핸들도 같은 브라우저 메모리에서 보존한다", async () => {
    storeURLHandle(createdA);
    render(<Harness />);
    await waitFor(() => expect(useUrl.getState().job?.id).toBe(a.id));
    sessionStorage.removeItem("abc2lab.urlDiagnosisHandle.v2");
    fireEvent.click(screen.getByRole("button", { name: "테스트 이동 finished" }));
    await pick("보존된 점검");
    expect(readURLHandle(owner)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "테스트 이동 running" }));
    await pick("첫 번째 점검");
    expect(useUrl.getState().handle?.startKey).toBe(createdA.startKey);
    expect(useUrl.getState().handle?.kind).not.toBe("recovered");
    expect(posts()).toEqual([]);
  });

  it("복구한 다른 Job의 403이 만든 회차의 키를 지우지 않는다", async () => {
    storeURLHandle(createdA);
    const first = render(<Harness />);
    await waitFor(() => expect(useUrl.getState().job?.id).toBe(a.id));
    fireEvent.click(screen.getByRole("button", { name: "테스트 이동 finished" }));
    await pick("보존된 점검");
    first.unmount();
    resetUrl();
    route = (url) => (url.pathname.endsWith(`/drafts/${b.id}/status`) ? json({ error: "forbidden" }, 403) : undefined);
    render(<Harness />);
    await waitFor(() => expect(useUrl.getState().error).not.toBe(""));
    expect(readURLHandle(owner)?.startKey).toBe(createdA.startKey);
    fireEvent.click(screen.getByRole("button", { name: "테스트 이동 running" }));
    await pick("첫 번째 점검");
    expect(useUrl.getState().handle?.startKey).toBe(createdA.startKey);
    expect(posts()).toEqual([]);
  });

  it("저장한 생성 키가 있어도 바뀐 실행 결속에는 붙이지 않는다", async () => {
    storeURLHandle(createdA);
    render(<Harness />);
    await waitFor(() => expect(useUrl.getState().job?.id).toBe(a.id));
    fireEvent.click(screen.getByRole("button", { name: "테스트 이동 finished" }));
    await pick("보존된 점검");
    jobs[a.id] = { ...a, source_digest: "e".repeat(64) };
    fireEvent.click(screen.getByRole("button", { name: "테스트 이동 running" }));
    fireEvent.click(await screen.findByRole("button", { name: /첫 번째 점검/ }));
    await waitFor(() => expect(useUrl.getState().error).not.toBe(""));
    expect(useUrl.getState().verified).toBe(false);
    expect(useUrl.getState().handle).toBeNull();
    expect(posts()).toEqual([]);
  });

  it("이름 없는 회차의 Job ID는 행에 한 번만 표시한다", async () => {
    route = (url) =>
      url.pathname.endsWith("/runs") ? json(page([{ ...toRow(a), display_name: undefined }])) : undefined;
    render(<Harness />);
    const row = await screen.findByRole("button", { name: new RegExp(a.id) });
    expect(within(row).getAllByText(a.id)).toHaveLength(1);
    expect(posts()).toEqual([]);
  });

  it("unknown과 시작 전 회차를 활성·끝난 상태에 섞지 않는다", async () => {
    const rows = [
      { ...toRow(a), state: "unknown" as const },
      { ...toRow(ready), id: "job-0", display_name: undefined, started: false, action_required: false },
    ];
    route = (url) => (url.pathname.endsWith("/runs") ? json(page(rows)) : undefined);
    render(<Harness initial="running" />);
    expect(await screen.findByRole("button", { name: /상태 확인 필요 · 결과 불명/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /job-0/ })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "테스트 이동 finished" }));
    expect(document.querySelectorAll("[data-url-run-id]")).toHaveLength(0);
  });
});

describe("페이지·오류·명시적 중지", () => {
  it("more cursor로 다음 페이지를 읽고 snapshot 충돌 때 옛 페이지를 버린다", async () => {
    const cursor = btoa("synthetic-page-2").replace(/=+$/, "");
    let fresh = false;
    route = (url) => {
      if (!url.pathname.endsWith("/runs")) return;
      if (url.searchParams.has("cursor")) {
        expect(url.searchParams.get("cursor")).toBe(cursor);
        fresh = true;
        return json({ error: "snapshot_changed" }, 409);
      }
      return fresh
        ? json(page([toRow(c)], { snapshot: "d".repeat(64) }))
        : json(page([toRow(a)], { more: true, next_cursor: cursor }));
    };
    render(<Harness initial="running" />);
    await screen.findByRole("button", { name: /첫 번째 점검/ });
    fireEvent.click(screen.getByRole("button", { name: "더 불러오기" }));
    await waitFor(() => expect(useUrlRuns.getState().page?.runs.map((r) => r.id)).toEqual([c.id]));
    expect(screen.queryByRole("button", { name: /첫 번째 점검/ })).toBeNull();
    expect(requests.filter((r) => r.path.startsWith("/api/diagnoses/v2/runs"))).toHaveLength(3);
    expect(posts()).toEqual([]);
  });
  it("반복되는 409는 무한 조회하지 않는다", async () => {
    route = (url) => (url.pathname.endsWith("/runs") ? json({ error: "snapshot_changed" }, 409) : undefined);
    render(<Harness />);
    await waitFor(() => expect(useUrlRuns.getState().error).toMatch(/변경/));
    expect(requests.filter((r) => r.path.endsWith("/runs"))).toHaveLength(2);
    expect(useUrlRuns.getState().page).toBeNull();
  });
  it.each([403, 404, 503])("목록 HTTP %s를 빈 목록이나 데이터 삭제로 표시하지 않는다", async (status) => {
    route = (url) => (url.pathname.endsWith("/runs") ? json({ error: "unavailable" }, status) : undefined);
    render(<Harness />);
    await waitFor(() => expect(useUrlRuns.getState().error).not.toBe(""));
    expect(document.querySelectorAll("[data-url-run-id]")).toHaveLength(0);
    expect(useUrlRuns.getState().page).toBeNull();
    expect(posts()).toEqual([]);
  });
  it.each([403, 404, 503])("Job status HTTP %s 뒤 자동 초안·재시작·보고서 조회를 하지 않는다", async (status) => {
    route = (url) => (url.pathname.endsWith(`/${a.id}/status`) ? json({ error: "unavailable" }, status) : undefined);
    render(<Harness />);
    fireEvent.click(await screen.findByRole("button", { name: /첫 번째 점검/ }));
    await waitFor(() => expect(useUrl.getState().error).not.toBe(""));
    expect(useUrl.getState().job).toBeNull();
    expect(posts()).toEqual([]);
    expect(requests.some((r) => r.path.startsWith("/api/report/"))).toBe(false);
  });
  it("report_available가 있어도 실제 보고서 GET 503은 오류로 보인다", async () => {
    route = (url) => (url.pathname === "/api/report/diagnostic" ? json({ error: "unavailable" }, 503) : undefined);
    render(<Download taskId="43" />);
    fireEvent.click(screen.getByRole("button", { name: "공유용 사본" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("HTTP 503");
    expect(screen.queryByText("이 점검은 보고서 파일을 제공하지 않습니다.")).toBeNull();
    expect(posts()).toEqual([]);
  });
  it("중지는 클릭 후 최신 revision을 다시 읽고 그 값으로 한 번만 보낸다", async () => {
    render(<Harness />);
    await pick("첫 번째 점검");
    expect(posts()).toEqual([]);
    const latest = { ...a, revision: 8 };
    jobs[a.id] = latest;
    route = (url, init) => {
      if (!url.pathname.endsWith(`/${a.id}/stop`)) return;
      expect(init.method).toBe("POST");
      expect(JSON.parse(String(init.body)).expected_revision).toBe(8);
      jobs[a.id] = {
        ...latest,
        revision: 9,
        state: "cancelled",
        phase: "report",
        drain_complete: true,
        result_ready: true,
      };
      return json({ version: "diagnosis-api.v2", owner_context: owner, draft: jobs[a.id] });
    };
    await act(async () => {
      await runUrlAction("stop");
    });
    expect(useUrl.getState().job?.state).toBe("cancelled");
    expect(posts()).toHaveLength(1);
  });
  it("중지 전 이미 마감됐다면 POST를 보내지 않는다", async () => {
    render(<Harness />);
    await pick("첫 번째 점검");
    jobs[a.id] = { ...a, revision: 8, state: "completed", phase: "report", drain_complete: true, result_ready: true };
    await act(async () => {
      await runUrlAction("stop");
    });
    expect(useUrl.getState().job?.state).toBe("completed");
    expect(posts()).toEqual([]);
  });
  it("401 뒤 주체 데이터와 cursor를 버리고 다른 운영자 로그인에서 재사용하지 않는다", async () => {
    storeURLHandle(createdA);
    render(<Harness />);
    await waitFor(() => expect(useUrl.getState().job?.id).toBe(a.id));
    await pick("보존된 점검");
    route = (url) => (url.pathname.endsWith("/runs") ? json({ error: "authentication_required" }, 401) : undefined);
    await act(async () => {
      await loadUrlRuns(owner);
    });
    expect(useUrl.getState().job).toBeNull();
    expect(useUrl.getState().createdHandle).toBeNull();
    expect(useUrlRuns.getState().page).toBeNull();
    expect(await screen.findByRole("button", { name: "로그인" })).toBeInTheDocument();
    route = () => undefined;
    fireEvent.change(screen.getByLabelText("아이디"), { target: { value: "other" } });
    fireEvent.change(screen.getByLabelText("비밀번호"), { target: { value: "synthetic-other-password" } });
    fireEvent.click(screen.getByRole("button", { name: "로그인" }));
    await waitFor(() => expect(useUrlRuns.getState().owner).toBe(B_OWNER));
    expect(useUrl.getState().job).toBeNull();
    expect(screen.queryByRole("button", { name: /보존된 점검/ })).toBeNull();
    expect(sessionStorage.getItem("abc2lab.urlDiagnosisSelection.v1")).toBeNull();
  });
});

describe("다른 Job·인증 문맥으로 바뀐 뒤의 늦은 HTTP 응답", () => {
  it("활동 A가 늦게 도착해도 새 task B의 활동에 섞이지 않는다", async () => {
    jobs[c.id] = { ...a, id: c.id, task_id: "44", input: c.input };
    let finish: (response: Response) => void = () => undefined;
    route = (u) =>
      u.pathname === "/api/exploration/activity" && u.searchParams.get("task") === "42"
        ? new Promise((resolve) => {
            finish = resolve;
          })
        : undefined;
    render(<Harness />);
    await pick("첫 번째 점검");
    fireEvent.click(screen.getByRole("button", { name: "테스트 이동 live" }));
    await waitFor(() => expect(requests.some((r) => r.path.includes("activity?task=42"))).toBe(true));
    fireEvent.click(screen.getByRole("button", { name: "테스트 이동 dashboard" }));
    await pick("실패한 점검");
    fireEvent.click(screen.getByRole("button", { name: "테스트 이동 live" }));
    expect(await screen.findByText("서버 활동 task 44")).toBeInTheDocument();
    await act(async () =>
      finish(
        json({
          items: [
            {
              seq: 9,
              intent_id: "7",
              worker: "worker-1",
              ts: "",
              kind: "tool",
              tool: "bash",
              is_error: false,
              summary: "지난 task A의 활동",
            },
          ],
          cursor: 9,
        }),
      ),
    );
    expect(screen.queryByText("지난 task A의 활동")).toBeNull();
    expect(screen.getByText("서버 활동 task 44")).toBeInTheDocument();
    expect(posts()).toEqual([]);
  });
  it("보고서 A의 늦은 실패가 B의 보고서 성공을 덮지 않는다", async () => {
    jobs[a.id] = { ...b, id: a.id, task_id: "42", input: a.input };
    let finish: (response: Response) => void = () => undefined;
    route = (u) =>
      u.pathname.endsWith("/remediation") && u.searchParams.get("task") === "42"
        ? new Promise((resolve) => {
            finish = resolve;
          })
        : undefined;
    render(<Harness />);
    await pick("첫 번째 점검");
    fireEvent.click(screen.getByRole("button", { name: "테스트 이동 report" }));
    await waitFor(() => expect(requests.some((r) => r.path.includes("remediation?task=42"))).toBe(true));
    fireEvent.click(screen.getByRole("button", { name: "테스트 이동 dashboard" }));
    await pick("보존된 점검");
    fireEvent.click(screen.getByRole("button", { name: "테스트 이동 report" }));
    expect(
      await screen.findByText("저장된 결과가 없습니다. 결과가 없다고 안전하다는 뜻은 아닙니다."),
    ).toBeInTheDocument();
    await act(async () => finish(json({ error: "unavailable" }, 503)));
    expect(screen.queryByText(/보고서 조회 실패/)).toBeNull();
    expect(useUrl.getState().job?.task_id).toBe("43");
    expect(posts()).toEqual([]);
  });
  it("목록 A를 기다리다 B로 로그인하면 A 목록을 폐기한다", async () => {
    let finish: (response: Response) => void = () => undefined;
    route = (u, init) =>
      u.pathname.endsWith("/runs") && new Headers(init.headers).get("Authorization")?.includes("synthetic-a-token")
        ? new Promise((resolve) => {
            finish = resolve;
          })
        : undefined;
    render(<Harness />);
    await waitFor(() => expect(useUrlRuns.getState().loading).toBe(true));
    await act(async () => {
      signOut();
      await signIn("other", "synthetic-password");
      setLiveAuthState("ready");
    });
    await waitFor(() => expect(useUrlRuns.getState().owner).toBe(B_OWNER));
    await act(async () => finish(json(page([toRow(a)]))));
    expect(useUrlRuns.getState().page?.runs).toEqual([]);
    expect(screen.queryByRole("button", { name: /첫 번째 점검/ })).toBeNull();
  });
  it("선택한 Job의 owner가 바뀐 status 응답을 거부한다", async () => {
    route = (u) =>
      u.pathname.endsWith(`/${b.id}/status`)
        ? json({ version: "diagnosis-api.v2", owner_context: B_OWNER, draft: b })
        : undefined;
    render(<Harness />);
    fireEvent.click(await screen.findByRole("button", { name: /보존된 점검/ }));
    await waitFor(() => expect(useUrl.getState().error).not.toBe(""));
    expect(useUrl.getState().job).toBeNull();
    expect(posts()).toEqual([]);
  });
});

it.each([403, 404, 503])("활동 조회 HTTP %s를 미지원이나 빈 활동으로 바꾸지 않는다", async (status) => {
  route = (u) =>
    u.pathname === "/api/exploration/activity" ? json({ error: "activity_unavailable" }, status) : undefined;
  render(<Harness />);
  await pick("첫 번째 점검");
  fireEvent.click(screen.getByRole("button", { name: "테스트 이동 live" }));
  await waitFor(() =>
    expect(screen.getByRole("region", { name: "서버 진단 활동" })).toHaveTextContent(`활동 조회 실패 (HTTP ${status})`),
  );
  expect(screen.getByRole("region", { name: "서버 진단 활동" })).not.toHaveTextContent(
    /제공되지 않음|반환된 활동 기록이 없습니다/,
  );
  expect(posts()).toEqual([]);
});

describe("브라우저를 다시 띄운 뒤 진행 중 점검 찾기(사용자 2026-11-22, 1차 리허설 F14·F16)", () => {
  it("이어 보는 점검이 없어도 진행 화면이 서버의 진행 중 점검을 '이 점검 열기'로 보이고, 열면 조회 결속으로 이어 본다", async () => {
    render(<Harness initial="live" />);
    const note = await screen.findByRole("note", { name: "이어 보지 않는 진행 중 점검" });
    expect(note).toHaveTextContent("첫 번째 점검");
    expect(note).not.toHaveTextContent("보존된 점검"); // 끝난 점검은 넣지 않는다
    expect(useUrl.getState().handle).toBeNull(); // 자동으로 고르지 않는다
    fireEvent.click(within(note).getByRole("button", { name: "이 점검 열기" }));
    await waitFor(() => expect(useUrl.getState().job?.id).toBe("job-a"));
    expect(useUrl.getState().handle?.kind).toBe("recovered");
    expect(urlGates(useUrl.getState()).canStart).toBe(false); // 생성·시작 키는 물려받지 않는다
    expect(posts()).toEqual([]);
  });

  it("레일은 이어 보지 않는 서버 점검도 줄마다 주소 · 상태로 보이고, 누르면 그 점검을 열어 목록 화면으로 간다(사용자 11/24)", async () => {
    let goTo: View | null = null;
    const shell = {
      route: "dashboard" as View,
      navSeq: 1,
      narrow: false,
      phone: false,
      reduce: false,
      inspOpen: false,
      railOpen: false,
      railNewAtRoute: false,
      goTo: (v: View) => {
        goTo = v;
      },
      openInsp: () => undefined,
      closeInsp: () => undefined,
      openRail: () => undefined,
      closeRail: () => undefined,
      foldRail: () => undefined,
      dashPick: () => undefined,
    } as Shell;
    await act(async () => {
      await loadUrlRuns(owner);
    });
    render(
      <ShellContext.Provider value={shell}>
        <LiveRailTasks />
      </ShellContext.Provider>,
    );
    // '이어 보지 않는 … n건 · 목록에서 열기' 같은 안내 줄은 없다 — 이어 보는 줄과 같은 모양(주소 · 상태)
    expect(screen.queryByText(/이어 보지 않는/)).toBeNull();
    const runs = useUrlRuns.getState().page?.runs ?? [];
    const runList = document.getElementById("rsec-run") as HTMLElement;
    const doneList = document.getElementById("rsec-done") as HTMLElement;
    const activeCount = runs.filter(activeURLRun).length;
    const ended = runs.filter(endedURLRun).length;
    expect(activeCount).toBeGreaterThan(0);
    expect(ended).toBeGreaterThan(0);
    await waitFor(() =>
      expect(within(runList).getAllByRole("button")[0]).toHaveTextContent(urlHost(running.input.target_url)),
    );
    expect(within(runList).getAllByRole("button")).toHaveLength(activeCount);
    expect(within(runList).getAllByRole("button")[0]).toHaveTextContent(urlStateText("running"));
    expect(within(doneList).getAllByRole("button")).toHaveLength(ended);
    expect(screen.queryByText("끝난 점검 없음")).toBeNull();
    // 줄을 누르면 그 점검을 열고(상태 다시 읽기) 그 목록 화면으로. 여는 건 서버 방식(v2)을 확인한 뒤라 그 상태로 둔다
    act(() => useUrl.setState({ cap: { kind: "v2", catalog } }));
    fireEvent.click(within(runList).getAllByRole("button")[0]);
    await waitFor(() => expect(goTo).toBe("running"));
    expect(useUrl.getState().selectedID).toBe(runs.find(activeURLRun)?.id);
  });
});
