// live 보고서: 메타(시작 · 소요 · 커버리지 · 모델 추정) · 요약 띠 · 발견 목록(심각도 순) ↔ 상세 · 우선 조치(조치 자료 ·
// finding-N ↔ finding_id 심각도 · 가림 · 미지원 접힘) · 내려받기(downloadDiagnostic · 미지원 접힘) · 엔진 미지원 칸. live/api 를 모의한다.

import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../live/api", async (importOriginal) => {
  const real = await importOriginal<typeof import("../live/api")>();
  return {
    ...real,
    fetchTasks: vi.fn(),
    fetchPendingApprovals: vi.fn(),
    fetchTaskFindings: vi.fn(),
    fetchFinding: vi.fn(),
    fetchCoverage: vi.fn(),
    fetchLlmProfiles: vi.fn(),
    fetchRemediation: vi.fn(),
    downloadDiagnostic: vi.fn(),
  };
});

import { ApiError } from "@/abc2lab-ui/lib/api";

import type { LiveFinding, LiveTask, LlmProfile } from "../live/api";
import * as api from "../live/api";
import { setLiveAuthState } from "../live/auth";
import { LiveFeeds, resetFeeds } from "../live/feeds";
import { useSource } from "../live/source";
import { type Shell, ShellContext, type View } from "../shell";
import { ReportLive } from "../views/live/ReportLive";

const m = {
  fetchTasks: vi.mocked(api.fetchTasks),
  fetchPendingApprovals: vi.mocked(api.fetchPendingApprovals),
  fetchTaskFindings: vi.mocked(api.fetchTaskFindings),
  fetchFinding: vi.mocked(api.fetchFinding),
  fetchCoverage: vi.mocked(api.fetchCoverage),
  fetchLlmProfiles: vi.mocked(api.fetchLlmProfiles),
  fetchRemediation: vi.mocked(api.fetchRemediation),
  downloadDiagnostic: vi.mocked(api.downloadDiagnostic),
};

const NOW = Math.floor(Date.now() / 1000);
const C0 = NOW - 7200;

function mkTask(p: Partial<LiveTask>): LiveTask {
  return {
    id: "8",
    name: "블로그",
    description: "https://blog.example.com 점검",
    goal: "",
    status: "done",
    created_at: "",
    created_unix: C0,
    completed_at: "",
    completed_unix: C0 + 28 * 60,
    last_activity_unix: 0,
    paused: false,
    queued: false,
    tokens: { input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 },
    goals_total: 0,
    goals_met: 0,
    in_flight: 0,
    findings: { critical: 1, high: 0, medium: 1, low: 1 },
    llm_profile_ids: [3],
    active_llm_profile_id: 3,
    llm_failover_state: "",
    coverage_enabled: true,
    source_task_ids: [],
    ...p,
  };
}

function mkFinding(p: Partial<LiveFinding>): LiveFinding {
  return {
    id: "n0",
    finding_id: "",
    vulnclass: "IDOR",
    name: "",
    severity: "high",
    status: "pending",
    summary: "",
    evidence: "",
    traffic_count: 0,
    assets: [],
    task_id: "8",
    source_task_id: "",
    inherited: false,
    ts: "",
    ...p,
  };
}

const PROFILES: LlmProfile[] = [
  {
    id: "3",
    name: "분석용",
    format: "openai",
    base_url: "https://api.example.net/v1",
    model: "qwen/qwen3.6-35b-a3b",
    is_default: false,
    priority: 0,
  },
  {
    id: "4",
    name: "로컬",
    format: "openai",
    base_url: "http://127.0.0.1:1234/v1",
    model: "llama-local",
    is_default: true,
    priority: 1,
  },
];

// API 순서(최신순) — 화면은 심각도 순으로 보인다
const FINDINGS: LiveFinding[] = [
  mkFinding({ id: "a", finding_id: "70", name: "쿠키 보안 속성 없음", severity: "low", status: "fixed" }),
  mkFinding({
    id: "b",
    finding_id: "77",
    name: "주문 조회 IDOR",
    severity: "critical",
    status: "pending",
    summary: "목록 요약",
    assets: [{ id: "9", type: "endpoint", label: "GET https://blog.example.com/api/orders/1" }],
  }),
  mkFinding({ id: "c", finding_id: "", name: "응답에 내부 경로", severity: "medium", status: "in_progress" }),
  mkFinding({ id: "d", finding_id: "", name: "정보 노출", severity: "info", status: "pending" }),
];

const ENTRIES = [
  {
    entry_id: "candidate-5",
    title: "후보 항목",
    outcome: "unknown",
    summary: "후보 요약",
    remediation: "",
    available: false,
    unavailable_reason: "현재 근거로 조치를 생성할 수 없습니다.",
  },
  {
    entry_id: "finding-70",
    title: "쿠키 속성",
    outcome: "review",
    summary: "낮음 요약",
    remediation: "HttpOnly 지정",
    available: true,
  },
  {
    entry_id: "finding-77",
    title: "주문 조회 IDOR token=abc123",
    outcome: "confirmed",
    summary: "B 계정으로 A 주문 Authorization: Bearer abcDEF123456",
    remediation: "소유자 검사 추가",
    available: true,
    record: {
      id: 1,
      entry_id: "finding-77",
      stale: false,
      bundle: { immediate_mitigation: ["주문 조회 API 를 잠시 막는다"], root_fix: ["owner_id 와 세션 사용자 비교"] },
    },
  },
];

function shell(route: View): Shell {
  const noop = () => undefined;
  return {
    route,
    navSeq: 0,
    narrow: false,
    phone: false,
    reduce: false,
    inspOpen: false,
    railOpen: false,
    goTo: noop,
    openInsp: noop,
    closeInsp: noop,
    openRail: noop,
    closeRail: noop,
    dashPick: noop,
  };
}

function renderReport(route: View = "report") {
  return render(
    <ShellContext.Provider value={shell(route)}>
      <LiveFeeds />
      <ReportLive />
    </ShellContext.Provider>,
  );
}

function serve(tasks: LiveTask[] = [mkTask({})]) {
  m.fetchTasks.mockResolvedValue({ active: "", tasks });
}

/** 머리 메타 줄에서 label 로 시작하는 칸. */
function metaCell(container: HTMLElement, label: string): HTMLElement {
  const meta = container.querySelector("#rvMeta") as HTMLElement;
  const el = [...meta.children].find((x) => x.textContent?.startsWith(label));
  if (!el) throw new Error(`no meta ${label}`);
  return el as HTMLElement;
}

beforeEach(() => {
  for (const f of Object.values(m)) f.mockReset();
  serve();
  m.fetchPendingApprovals.mockResolvedValue([]);
  m.fetchTaskFindings.mockResolvedValue(FINDINGS);
  m.fetchFinding.mockImplementation(async (fid) => {
    const row = FINDINGS.find((f) => f.finding_id === fid) as LiveFinding;
    return { ...row, summary: `상세 ${row.name}`, report: "", evidence_version: 1, report_stale: false };
  });
  m.fetchCoverage.mockResolvedValue({
    enabled: true,
    scopeRows: 1,
    denominator: 44,
    tested: 40,
    pct: 0.91,
    byType: [{ type: "endpoint", total: 44, tested: 40 }],
  });
  m.fetchLlmProfiles.mockResolvedValue(PROFILES);
  m.fetchRemediation.mockResolvedValue({ task_id: 8, entries: ENTRIES } as never);
  m.downloadDiagnostic.mockResolvedValue(undefined);
  setLiveAuthState("ready");
  resetFeeds();
  useSource.setState({ source: "live", taskId: "8" });
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("live 보고서", () => {
  it("머리: 제목 · 상태 · 시작 · 소요(끝-시작) · 커버리지 · 모델(활성 프로필, 추정 아님)", async () => {
    const { container } = renderReport();
    expect(await screen.findByRole("heading", { level: 1, name: "blog.example.com 보고서" })).toBeInTheDocument();
    expect(screen.getByText("점검 완료")).toBeInTheDocument();
    await waitFor(() => expect(metaCell(container, "모델")).toHaveTextContent("모델 qwen/qwen3.6-35b-a3b"));
    expect(metaCell(container, "모델")).not.toHaveTextContent("추정");
    expect(metaCell(container, "시작").textContent).toMatch(/^시작 \d\d\/\d\d \d\d:\d\d$/);
    expect(metaCell(container, "소요")).toHaveTextContent("소요 28분");
    await waitFor(() => expect(metaCell(container, "커버리지")).toHaveTextContent("커버리지 91%"));
    expect(metaCell(container, "대상")).toHaveTextContent("대상 blog.example.com 추정");
    const strip = container.querySelector("#rvStrip") as HTMLElement;
    expect(strip).toHaveTextContent("검사 40/44 · 미검사는 안전을 뜻하지 않음");
  });

  it("진행 중이면 소요는 '진행 중', 활성 프로필이 없으면 지정 목록에서 짐작(추정) · 로컬 추정", async () => {
    serve([
      mkTask({
        status: "running",
        completed_unix: 0,
        created_unix: NOW - 600,
        active_llm_profile_id: null,
        llm_profile_ids: [4],
      }),
    ]);
    const { container } = renderReport();
    await waitFor(() => expect(metaCell(container, "모델")).toHaveTextContent("모델 llama-local · 로컬 추정"));
    expect(metaCell(container, "소요")).toHaveTextContent(/^소요 진행 중 · 경과 10분$/);
    expect(screen.getByText("점검 중")).toBeInTheDocument();
  });

  it("요약 띠 · 발견 목록(심각도 순) · 첫 발견 상세, 고르면 바뀌고 처리 상태로 보인다", async () => {
    const { container } = renderReport();
    const list = await waitFor(() => {
      const el = container.querySelector("#rvList");
      if (!el) throw new Error("no list");
      return el as HTMLElement;
    });
    const rows = within(list).getAllByRole("button");
    expect(rows.map((b) => b.dataset.rvsel)).toEqual(["b", "c", "a", "d"]);
    const strip = container.querySelector("#rvStrip") as HTMLElement;
    expect(strip).toHaveTextContent("발견4");
    expect(strip).toHaveTextContent("심각 1 · 높음 0 · 중간 1 · 낮음 1 · 그 밖 1");
    expect(strip).toHaveTextContent("심각 · 높음1");
    expect(strip).toHaveTextContent("미검토2처리 상태 기준 · 판정 아님");
    expect(rows[0]).toHaveAttribute("aria-current", "true");
    expect(rows[0]).toHaveTextContent("주문 조회 IDOR");
    expect(rows[0]).toHaveTextContent("미검토");
    expect(rows[2]).toHaveTextContent("수정됨");

    const insp = container.querySelector("#rvInsp") as HTMLElement;
    await waitFor(() => expect(insp).toHaveTextContent("상세 주문 조회 IDOR"));
    expect(m.fetchFinding).toHaveBeenCalledWith("77");
    fireEvent.click(rows[2]);
    expect(rows[2]).toHaveAttribute("aria-current", "true");
    expect(rows[0]).not.toHaveAttribute("aria-current");
    expect(within(insp).getByRole("heading", { level: 2, name: "쿠키 보안 속성 없음" })).toBeInTheDocument();
    await waitFor(() => expect(insp).toHaveTextContent("상세 쿠키 보안 속성 없음"));
    expect(m.fetchFinding).toHaveBeenCalledWith("70");
    // finding_id 없는 발견은 조회하지 않고 목록 값
    fireEvent.click(rows[1]);
    expect(insp).toHaveTextContent("독립 발견 기록(finding_id)이 없어 목록 값으로 보입니다.");
  });

  it("우선 조치: 제목 · 분류 · 요약 · 권고 · 즉시 완화/근본 수정, finding-N 만 발견 심각도와 이어 앞에, 가림", async () => {
    const { container } = renderReport();
    await waitFor(() => expect(container.querySelector("#rvPrio")).not.toBeNull());
    // 발견이 오면 이어진 항목이 심각도 순으로 앞에
    // 조치 항목 = ol 의 바로 아래 li (안쪽 즉시 완화·근본 수정 목록은 빼고)
    const prio = () => [...(container.querySelector("#rvPrio") as HTMLElement).children] as HTMLElement[];
    await waitFor(() => expect(prio()[0]).toHaveTextContent("주문 조회 IDOR token=[REDACTED]"));
    const items = prio();
    expect(items).toHaveLength(3);
    expect(items[0]).toHaveTextContent("심각");
    expect(items[0]).toHaveTextContent("확정");
    expect(items[0]).toHaveTextContent("Authorization: Bearer [REDACTED]");
    expect(items[0]).toHaveTextContent("권고 소유자 검사 추가");
    expect(items[0]).toHaveTextContent("즉시 완화주문 조회 API 를 잠시 막는다");
    expect(items[0]).toHaveTextContent("근본 수정owner_id 와 세션 사용자 비교");
    expect(items[1]).toHaveTextContent("쿠키 속성");
    expect(items[1]).toHaveTextContent("낮음");
    expect(items[1]).toHaveTextContent("검토 필요");
    expect(items[2]).toHaveTextContent("후보 항목");
    expect(items[2]).toHaveTextContent("결과 불명");
    expect(items[2]).toHaveTextContent("현재 근거로 조치를 생성할 수 없습니다.");
    expect(items[2].textContent).not.toMatch(/심각|높음|중간|낮음/);
    expect(container).not.toHaveTextContent("abcDEF123456");
    expect(container).not.toHaveTextContent("abc123");
    expect(m.fetchRemediation).toHaveBeenCalledWith("8", "internal");
  });

  it("우선 조치가 이 서버 모드에서 안 되면(403) 그 섹션만 접는다 · 엔진 미지원 칸", async () => {
    m.fetchRemediation.mockRejectedValue(new ApiError(403, "forbidden"));
    const { container } = renderReport();
    expect(await screen.findByText("우선 조치: 이 서버 모드에서는 제공되지 않음")).toBeInTheDocument();
    expect(container.querySelector("#rvPrio")).toBeNull();
    await waitFor(() => expect(container.querySelector("#rvList")).not.toBeNull());
    expect(
      screen.getByText("판정 집계(확정 · 의심 · 판단 불가): 엔진 미지원 — 백엔드에 아직 없는 기능입니다."),
    ).toBeInTheDocument();
    expect(screen.getByText("확인된 공격 경로: 엔진 미지원 — 백엔드에 아직 없는 기능입니다.")).toBeInTheDocument();
    expect(screen.getByText("자산 지식 그래프: 엔진 미지원 — 백엔드에 아직 없는 기능입니다.")).toBeInTheDocument();
  });

  it("내려받기: 버튼이 downloadDiagnostic(task, audience) 를 부르고, 미지원이면 접는다", async () => {
    renderReport();
    const shared = await screen.findByRole("button", { name: "공유용 사본" });
    await act(async () => {
      fireEvent.click(shared);
    });
    expect(m.downloadDiagnostic).toHaveBeenCalledWith("8", "shared", expect.any(AbortSignal));
    expect(await screen.findByRole("status")).toHaveTextContent("공유용 사본 파일을 저장했습니다.");

    m.downloadDiagnostic.mockRejectedValueOnce(new ApiError(403, "forbidden"));
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "내부 상세 사본" }));
    });
    expect(m.downloadDiagnostic).toHaveBeenLastCalledWith("8", "internal", expect.any(AbortSignal));
    expect(await screen.findByText("보고서 내려받기: 이 서버 모드에서는 제공되지 않음")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "공유용 사본" })).toBeNull();
  });

  it("고른 점검이 없으면 전체 목록에서 고르기, 숨긴 화면은 부르지 않는다", async () => {
    serve([mkTask({}), mkTask({ id: "11", description: "https://shop.example.com", status: "running" })]);
    useSource.setState({ taskId: null });
    const { unmount } = renderReport();
    expect(await screen.findByRole("heading", { name: "보고서를 볼 점검을 고르세요" })).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: /shop\.example\.com/ })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /blog\.example\.com/ }));
    expect(useSource.getState().taskId).toBe("8");
    await waitFor(() => expect(m.fetchTaskFindings).toHaveBeenCalledWith("8"));
    unmount();

    for (const f of [m.fetchTaskFindings, m.fetchCoverage, m.fetchLlmProfiles, m.fetchRemediation, m.fetchFinding])
      f.mockClear();
    resetFeeds();
    renderReport("live");
    await screen.findByRole("heading", { level: 1, name: "blog.example.com 보고서" });
    await act(async () => {
      await new Promise((r) => setTimeout(r, 30));
    });
    expect(m.fetchTaskFindings).not.toHaveBeenCalled();
    expect(m.fetchCoverage).not.toHaveBeenCalled();
    expect(m.fetchLlmProfiles).not.toHaveBeenCalled();
    expect(m.fetchRemediation).not.toHaveBeenCalled();
    expect(m.fetchFinding).not.toHaveBeenCalled();
  });
});
