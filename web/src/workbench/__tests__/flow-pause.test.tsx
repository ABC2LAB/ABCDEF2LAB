// 점검 진행 · 자산 탐색 화면의 일시정지 · 다시 시작 (개발 계획 2026-11-24 사용자 결정, 11/24 화면 VAmPI 회차 2 §10).
// 흐름 API(POST /api/product/workflows/{id}/pause · resume)만 쓰고 점검 제어 API(/api/tasks/{id}/control)는 부르지 않는다.
// 버튼은 서버 흐름 조건(crawling · diagnosing, 명령 31개 미만)일 때만 있고, 결과는 서버 스냅숏(paused)으로만 그린다.

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/diagnosis/api", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/api")>()),
  apiFetch: vi.fn(),
}));

import { ApiError, apiFetch } from "@/lib/diagnosis/api";
import { catalog, handle, running, scope } from "@/lib/diagnosis/diagnosis-url-fixtures";

import { resetUrl, useUrl } from "../live/urlDiagnosis";
import { FLOW_VERSION, type WorkflowSnapshot } from "../live/workflow";
import { PAUSE_HINT, PauseButton, useStopConfirm } from "../views/live/workflowViews";

const job = {
  ...running,
  input: { ...running.input, workflow_mode: "ai_auto_v1" as const },
  scope: { ...scope, approval_mode: "workbench_selective_v1" as const, methods: ["GET", "POST"] },
};

const flow: WorkflowSnapshot = {
  format: FLOW_VERSION,
  job_id: job.id,
  task_id: job.task_id as string,
  run_ref: `run-${job.id}`,
  source_digest: job.source_digest,
  revision: 125,
  mode: "ai_auto_v1",
  stage: "diagnosing",
  paused: false,
  initial_completed: true,
  reversible_writes: true,
  deep_runs: 1,
  deep_closed: 0,
  limits: {
    format: FLOW_VERSION,
    max_pages: 4,
    max_endpoints: 32,
    max_response_bytes: 4096,
    allow_reversible_writes: true,
  },
  counts: { found: 0, visited: 0, queued: 0, sent: 0, attempted: 0, unknown: 0, blocked: 0 },
  endpoints: [],
  events: [],
  commands: [],
  updated_at: "2026-11-24T06:25:50Z",
};

type Call = { path: string; key: string; body: Record<string, unknown> };
let calls: Call[] = [];

/** 서버처럼: 명령 키를 스냅숏 commands 에 남기고 판을 올린 응답. fail 이면 그 오류를 던진다. */
function serve(fail?: Error) {
  vi.mocked(apiFetch).mockImplementation(async (path: string, init?: RequestInit) => {
    const key = (init?.headers as Record<string, string>)["Idempotency-Key"];
    const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
    calls.push({ path, key, body });
    if (fail) throw fail;
    const operation = path.endsWith("/pause") ? "pause" : "resume";
    const current = useUrl.getState().flow as WorkflowSnapshot;
    const revision = current.revision + 1;
    return {
      workflow: {
        ...current,
        revision,
        paused: operation === "pause",
        commands: [...current.commands, { key, revision, operation }],
      },
    } as never;
  });
}

function setFlow(over: Partial<WorkflowSnapshot> = {}, more: Record<string, unknown> = {}) {
  useUrl.setState({
    handle,
    job,
    verified: true,
    uncertain: false,
    flow: { ...flow, ...over },
    flowVerified: true,
    flowUncertain: false,
    flowBusy: false,
    ...more,
  });
}

beforeEach(() => {
  vi.resetAllMocks();
  calls = [];
  resetUrl({ kind: "v2", catalog });
});
afterEach(cleanup);

describe("PauseButton — 흐름 API 일시정지 · 다시 시작", () => {
  it("일시정지 → 흐름 pause 를 한 번 보내고 서버 스냅숏(paused)대로 '다시 시작'으로 바뀐다, 다시 시작 → resume", async () => {
    serve();
    setFlow();
    render(<PauseButton />);
    fireEvent.click(screen.getByRole("button", { name: "일시정지" }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "다시 시작" })).toHaveAttribute("aria-pressed", "true"),
    );
    expect(useUrl.getState().flow?.paused).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "다시 시작" }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "일시정지" })).toHaveAttribute("aria-pressed", "false"),
    );
    expect(calls.map((c) => c.path)).toEqual([
      `/api/product/workflows/${job.id}/pause`,
      `/api/product/workflows/${job.id}/resume`,
    ]);
    // 지금 판을 기대 판으로 보내고, 명령마다 다른 재시도 키
    expect(calls.map((c) => c.body.expected_revision)).toEqual([125, 126]);
    expect(new Set(calls.map((c) => c.key)).size).toBe(2);
    // 점검 제어 API(회차 중지)는 부르지 않는다
    expect(calls.some((c) => c.path.includes("/control"))).toBe(false);
  });

  it("탐색 중(crawling)에도 있고, 범위 확인 대기 · 끝난 흐름에는 없다", () => {
    setFlow({ stage: "crawling" });
    const a = render(<PauseButton />);
    expect(screen.getByRole("button", { name: "일시정지" })).toBeEnabled();
    a.unmount();
    for (const stage of ["awaiting_selection", "completed", "stopped", "failed", "unknown"] as const) {
      setFlow({ stage });
      const r = render(<PauseButton />);
      expect(screen.queryByRole("button", { name: /일시정지|다시 시작/ }), stage).toBeNull();
      r.unmount();
    }
    setFlow({}, { job: { ...job, state: "completed" } });
    render(<PauseButton />);
    expect(screen.queryByRole("button", { name: /일시정지|다시 시작/ })).toBeNull();
  });

  it("명령 31개부터 일시정지는 잠그고(서버 거부 조건), 이미 일시정지면 다시 시작은 연다", () => {
    const commands = Array.from({ length: 31 }, (_, i) => ({ key: `k${i}`, revision: i + 1, operation: "selection" }));
    setFlow({ commands } as Partial<WorkflowSnapshot>);
    const a = render(<PauseButton />);
    expect(screen.getByRole("button", { name: "일시정지" })).toBeDisabled();
    a.unmount();
    setFlow({ commands, paused: true } as Partial<WorkflowSnapshot>);
    render(<PauseButton />);
    expect(screen.getByRole("button", { name: "다시 시작" })).toBeEnabled();
  });

  it("상태를 확인하지 못했거나 명령 결과가 불명이면 잠근다, 목록에서 다시 연 점검은 누를 수 있다(F16)", () => {
    for (const more of [
      { flowVerified: false },
      { flowUncertain: true },
      { flowBusy: true },
      { verified: false },
      { uncertain: true },
    ]) {
      setFlow({}, more);
      const r = render(<PauseButton />);
      expect(screen.getByRole("button", { name: "일시정지" }), JSON.stringify(more)).toBeDisabled();
      r.unmount();
    }
    setFlow(
      {},
      {
        handle: {
          kind: "recovered",
          ownerContext: catalog.ownerContext,
          input: job.input,
          id: job.id,
          revision: job.revision,
          sourceDigest: job.source_digest,
          bindingLocked: true,
        },
      },
    );
    render(<PauseButton />);
    expect(screen.getByRole("button", { name: "일시정지" })).toBeEnabled();
  });

  it("결과 불명(연결 실패)이면 일시정지됐다고 그리지 않고 다시 보내지 못하게 잠근다", async () => {
    serve(new ApiError(0, "network"));
    setFlow();
    render(<PauseButton />);
    fireEvent.click(screen.getByRole("button", { name: "일시정지" }));
    await waitFor(() => expect(useUrl.getState().flowUncertain).toBe(true));
    expect(useUrl.getState().flow?.paused).toBe(false);
    expect(screen.getByRole("button", { name: "일시정지" })).toBeDisabled();
    expect(useUrl.getState().flowError).toContain("재전송하지 말고");
  });
});

describe("종료 확인 상자 — 일시정지 안내", () => {
  function Stop() {
    const stop = useStopConfirm("종료");
    return (
      <>
        {stop.button}
        {stop.box}
      </>
    );
  }

  const openBox = () => {
    const r = render(<Stop />);
    fireEvent.click(screen.getByRole("button", { name: "종료" }));
    const box = screen.getByRole("alertdialog");
    return { r, box };
  };

  it("지금 일시정지를 누를 수 있을 때만 안내하고, 시간 상한은 계속 흐른다고 쓴다(#913 리뷰)", () => {
    setFlow();
    const a = openBox();
    expect(a.box).toHaveTextContent(PAUSE_HINT);
    expect(PAUSE_HINT).toContain("일시정지 중에도 점검 시간 상한은 흐릅니다");
    expect(a.box).not.toHaveTextContent("나중에 이어 하려면");
    a.r.unmount();
    // 범위 확인 대기(일시정지 없는 단계)
    setFlow({ stage: "awaiting_selection" });
    const b = openBox();
    expect(b.box).not.toHaveTextContent("일시정지");
    b.r.unmount();
  });

  it("명령 31개(일시정지 잠김) · 이미 일시정지 · 결과 불명이면 안내를 빼서 버튼과 어긋나지 않는다(#913 리뷰 이동찬 P2)", () => {
    const commands = Array.from({ length: 31 }, (_, i) => ({ key: `k${i}`, revision: i + 1, operation: "selection" }));
    const cases: [string, Partial<WorkflowSnapshot>, Record<string, unknown>][] = [
      ["명령 31개", { commands } as Partial<WorkflowSnapshot>, {}],
      ["이미 일시정지", { paused: true }, {}],
      ["명령 결과 불명", {}, { flowUncertain: true }],
    ];
    for (const [name, over, more] of cases) {
      setFlow(over, more);
      const pause = render(<PauseButton />);
      const button = screen.queryByRole("button", { name: "일시정지" });
      // 버튼이 없거나([다시 시작]) 잠겨 있다
      if (button) expect(button, name).toBeDisabled();
      pause.unmount();
      const x = openBox();
      expect(x.box, name).not.toHaveTextContent(PAUSE_HINT);
      x.r.unmount();
    }
  });
});
