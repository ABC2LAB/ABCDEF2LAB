import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  attempted,
  type CoverageArea,
  type CoverageData,
  type CoverageStatus,
  coverageIssues,
  getCoverage,
  STATUS_ORDER,
  summarize,
  total,
} from "./coverage";

// 커버리지 집계·무결성의 fail-closed 규칙 검증. 목업/계약 shape 로직 테스트이며 실 검사 건수 증명 아님.

function zero(): Record<CoverageStatus, number> {
  return { confirmed: 0, suspected: 0, undecidable: 0, blank: 0, in_progress: 0, untested: 0, na: 0 };
}
function area(over: Partial<CoverageArea> = {}): CoverageArea {
  return { id: "a", label: "x", axis: "horizontal", planned: 0, counts: zero(), ...over };
}
function data(areas: CoverageArea[]): CoverageData {
  return { areas, policyVersion: "t", mock: true };
}

describe("attempted / total", () => {
  it("attempted = 확정+의심+판정불가+기록없음+진행중 (미시도·해당없음 제외)", () => {
    const a = area({
      counts: { confirmed: 1, suspected: 1, undecidable: 1, blank: 1, in_progress: 1, untested: 3, na: 2 },
    });
    expect(attempted(a)).toBe(5);
    expect(total(a)).toBe(10);
  });
});

describe("coverageIssues (무결성 — 조용히 안 맞춤)", () => {
  it("상태합 = planned 면 경고 0", () => {
    const a = area({ planned: 4, counts: { ...zero(), confirmed: 1, untested: 3 } });
    expect(coverageIssues(data([a])).mismatched).toBe(0);
  });
  it("상태합 ≠ planned 면 그 영역을 센다(fail-closed)", () => {
    const a = area({ planned: 9, counts: { ...zero(), confirmed: 1, untested: 3 } }); // 합 4 ≠ 9
    expect(coverageIssues(data([a])).mismatched).toBe(1);
  });
});

describe("summarize", () => {
  it("영역 걸쳐 상태별 합 + 계획·시도 총계", () => {
    const s = summarize(
      data([
        area({ planned: 4, counts: { ...zero(), confirmed: 1, untested: 3 } }),
        area({ planned: 2, counts: { ...zero(), undecidable: 1, untested: 1 } }),
      ]),
    );
    expect(s.planned).toBe(6);
    expect(s.attempted).toBe(2); // confirmed 1 + undecidable 1
    expect(s.byStatus.untested).toBe(4);
    expect(s.byStatus.confirmed).toBe(1);
  });

  it("합계는 숫자로 유지(NaN 아님)", () => {
    const s = summarize(data([area({ planned: 1, counts: { ...zero(), blank: 1 } })]));
    expect(STATUS_ORDER.reduce((n, k) => n + s.byStatus[k], 0)).toBe(1);
  });
});

vi.mock("./api", async (original) => ({ ...(await original<typeof import("./api")>()), apiFetch: vi.fn() }));
import { apiFetch } from "./api";
import { parseCoverage } from "./coverage";
import { coverageJob, coverageSnapshot } from "./coverage-fixtures";
beforeEach(() => vi.resetAllMocks());
describe("실제 workflow snapshot", () => {
  it("현재 job/task/source/run에 결속해 기존 API만 읽는다", async () => {
    vi.mocked(apiFetch).mockResolvedValue(coverageSnapshot());
    const d = await getCoverage(coverageJob);
    expect(vi.mocked(apiFetch).mock.calls[0][0]).toBe(`/api/product/workflows/${coverageJob.id}`);
    expect(d.mock).toBe(false);
    expect(d.endpoints).toHaveLength(3);
    expect(d.areas[0].counts).toMatchObject({ blank: 1, untested: 1, undecidable: 1, confirmed: 0, suspected: 0 });
    expect(coverageIssues(d).mismatched).toBe(0);
  });
  it.each(["job_id", "task_id", "run_ref", "source_digest", "mode"])("다른 %s의 snapshot을 섞지 않는다", (key) => {
    const s = coverageSnapshot();
    Object.assign(s.workflow, { [key]: "different" });
    expect(() => parseCoverage(s, coverageJob)).toThrow();
  });
  it("방문·선택·출처·응답·누계 누락은 불명/미시도로 보존한다", () => {
    const s = coverageSnapshot();
    s.workflow.endpoints = [{ id: "unknown-endpoint" }] as never;
    delete (s.workflow as { counts?: unknown }).counts;
    const d = parseCoverage(s, coverageJob);
    expect(d.endpoints?.[0]).toMatchObject({
      visited: null,
      selected: null,
      status: null,
      source: "출처 불명",
      coverage: "untested",
    });
    expect(d.workflowCounts?.found).toBeNull();
    expect(summarize(d).attempted).toBe(0);
  });
  it("방문·2xx도 E③ 확정이나 안전으로 승격하지 않는다", () => {
    const d = parseCoverage(coverageSnapshot(), coverageJob);
    expect(summarize(d).byStatus.confirmed).toBe(0);
    expect(summarize(d).byStatus.suspected).toBe(0);
    expect(summarize(d).byStatus.na).toBe(0);
  });
  it("원문 관찰 오류·query·응답 본문은 화면 투영으로 복사하지 않는다", () => {
    const s = coverageSnapshot();
    Object.assign(s.workflow.endpoints[0], {
      path: "/api?token=secret",
      source: "secret-origin",
      observation_error: "secret-error",
      request: "secret-command",
      response: "secret-body",
    });
    const d = parseCoverage(s, coverageJob);
    expect(JSON.stringify(d)).not.toContain("secret-");
    expect(d.endpoints?.[0].path).toBe("");
    expect(d.endpoints?.[0].coverage).toBe("undecidable");
  });
  it("서버 거절·미실행은 예시·안전으로 대체하지 않는다", async () => {
    vi.mocked(apiFetch).mockRejectedValue(new Error("fixed-unavailable"));
    await expect(getCoverage(coverageJob)).rejects.toThrow();
    vi.mocked(apiFetch).mockClear();
    await expect(getCoverage(null)).rejects.toThrow();
    expect(apiFetch).not.toHaveBeenCalled();
  });
});
