// 제품 진행 중 후보 조회 파서 (#694 계약). 서버 productCandidatePage.valid · 중계 UTC 고정이 만들 수 있는 모양만 받고,
// 서버 정상값(소수 없는 시각 · 9자리 나노초 · int64 최대 ID · 등급 없는 행)은 막지 않는다.

import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/diagnosis/api", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/api")>()),
  apiFetchLimitedJSON: vi.fn(),
}));

import { ApiError, apiFetchLimitedJSON } from "@/lib/diagnosis/api";

import {
  candidateCountText,
  candidateErrorText,
  candidateSeverityText,
  fetchLiveCandidates,
  isCandidateRunNotActive,
  isLiveCandidatesShapeError,
  LIVE_CANDIDATES_MAX_BYTES,
  MAX_LIVE_CANDIDATES,
  parseLiveCandidates,
} from "../live/diagnosticCandidates";

const item = (id: string, extra: Record<string, unknown> = {}) => ({
  candidate_id: id,
  state: "manual_review_required",
  observed_at: "2026-11-18T02:20:01.481Z",
  ...extra,
});
const page = (items: unknown[], extra: Record<string, unknown> = {}) => ({
  task_id: "41",
  items,
  returned: items.length,
  candidate_total: items.length,
  truncated: false,
  ...extra,
});
const bad = (raw: unknown, task = "41") => {
  let err: unknown = null;
  try {
    parseLiveCandidates(raw, task);
  } catch (e) {
    err = e;
  }
  return isLiveCandidatesShapeError(err);
};

afterEach(() => vi.resetAllMocks());

describe("parseLiveCandidates", () => {
  it("계약 예시를 그대로 받는다", () => {
    const p = parseLiveCandidates(page([item("902", { suggested_severity: "medium" })]), "41");
    expect(p).toEqual({
      taskId: "41",
      items: [
        {
          candidateId: "902",
          state: "manual_review_required",
          observedAt: "2026-11-18T02:20:01.481Z",
          suggestedSeverity: "medium",
        },
      ],
      returned: 1,
      total: 1,
      truncated: false,
    });
  });

  it("서버 정상값을 막지 않는다: 빈 목록 · 등급 없음 · 소수 없는 시각 · 9자리 나노초 · int64 최대 · 윤년 2/29", () => {
    expect(parseLiveCandidates(page([]), "41").items).toEqual([]);
    const p = parseLiveCandidates(
      page([
        item("9223372036854775807", { observed_at: "2024-02-29T23:59:59.123456789Z" }),
        item("10", { observed_at: "2026-11-18T02:20:01Z", suggested_severity: "info" }),
        item("1"),
      ]),
      "41",
    );
    expect(p.items.map((x) => x.suggestedSeverity)).toEqual([null, "info", null]);
    for (const sev of ["critical", "high", "medium", "low", "info"])
      expect(bad(page([item("1", { suggested_severity: sev })]))).toBe(false);
  });

  it("일부만 오면 전체 수는 null 로 두고 받은 수만 쓴다", () => {
    const ids = Array.from({ length: MAX_LIVE_CANDIDATES }, (_, i) => item(String(1000 - i)));
    const p = parseLiveCandidates(page(ids, { candidate_total: null, truncated: true }), "41");
    expect(p).toMatchObject({ returned: 200, total: null, truncated: true });
    expect(candidateCountText(p)).toBe("최근 200건만 표시 · 전체 수 미상");
    // 바이트 상한으로 짧아진 쪽도 truncated 다(행 수가 200 보다 적어도)
    const short = parseLiveCandidates(page([item("5")], { candidate_total: null, truncated: true }), "41");
    expect(candidateCountText(short)).toBe("최근 1건만 표시 · 전체 수 미상");
    expect(candidateCountText(parseLiveCandidates(page([item("5")]), "41"))).toBe("1건");
  });

  it.each([
    ["다른 task", page([]), "42"],
    ["숫자 task_id", { ...page([]), task_id: 41 }, "41"],
    ["모르는 위 칸", { ...page([]), extra: 1 }, "41"],
    ["빠진 위 칸", (({ truncated, ...r }) => r)(page([])), "41"],
    ["items null", { ...page([]), items: null }, "41"],
    ["201행", page(Array.from({ length: 201 }, (_, i) => item(String(500 - i)))), "41"],
    ["returned 불일치", page([item("2")], { returned: 2 }), "41"],
    ["returned 문자열", page([item("2")], { returned: "1" }), "41"],
    ["다 왔는데 total 다름", page([item("2")], { candidate_total: 3 }), "41"],
    ["다 왔는데 total null", page([item("2")], { candidate_total: null }), "41"],
    ["일부인데 total 있음", page([item("2")], { truncated: true }), "41"],
    ["일부인데 빈 목록", page([], { candidate_total: null, truncated: true }), "41"],
    ["truncated 문자열", page([], { truncated: "false" }), "41"],
    ["오름차순", page([item("1"), item("2")]), "41"],
    ["중복 ID", page([item("2"), item("2")]), "41"],
    ["ID 0", page([item("0")]), "41"],
    ["ID 앞자리 0", page([item("01")]), "41"],
    ["ID int64 넘음", page([item("9223372036854775808")]), "41"],
    ["ID 숫자", page([{ ...item("1"), candidate_id: 1 }]), "41"],
    ["다른 상태", page([item("1", { state: "confirmed" })]), "41"],
    ["모르는 등급", page([item("1", { suggested_severity: "none" })]), "41"],
    ["빈 등급", page([item("1", { suggested_severity: "" })]), "41"],
    ["null 등급", page([item("1", { suggested_severity: null })]), "41"],
    ["대문자 등급", page([item("1", { suggested_severity: "HIGH" })]), "41"],
    ["모르는 행 칸", page([item("1", { title: "제목" })]), "41"],
    ["시각 없음", page([(({ observed_at, ...r }) => r)(item("1"))]), "41"],
    ["UTC 아닌 시각", page([item("1", { observed_at: "2026-11-18T11:20:01+09:00" })]), "41"],
    ["0 시각", page([item("1", { observed_at: "0001-01-01T00:00:00Z" })]), "41"],
    ["끝 0 소수", page([item("1", { observed_at: "2026-11-18T02:20:01.480Z" })]), "41"],
    ["10자리 소수", page([item("1", { observed_at: "2026-11-18T02:20:01.1234567891Z" })]), "41"],
    ["없는 날", page([item("1", { observed_at: "2026-02-29T00:00:00Z" })]), "41"],
    ["없는 시", page([item("1", { observed_at: "2026-11-18T24:00:00Z" })]), "41"],
    ["행이 배열", page([["1"]]), "41"],
  ])("계약 밖이면 응답 전체를 버린다: %s", (_name, raw, task) => {
    expect(bad(raw, task)).toBe(true);
  });
});

describe("fetchLiveCandidates", () => {
  it("query · 본문 없이 정규 task 경로를 64 KiB 상한 · no-store · 중단 신호로 부른다", async () => {
    vi.mocked(apiFetchLimitedJSON).mockResolvedValue(page([item("3")]));
    const ctrl = new AbortController();
    const p = await fetchLiveCandidates("41", ctrl.signal);
    expect(p.items).toHaveLength(1);
    expect(apiFetchLimitedJSON).toHaveBeenCalledWith("/api/tasks/41/diagnostic-candidates", LIVE_CANDIDATES_MAX_BYTES, {
      cache: "no-store",
      signal: ctrl.signal,
    });
    expect(LIVE_CANDIDATES_MAX_BYTES).toBe(64 * 1024);
  });

  it("정규가 아닌 task ID 는 부르지 않는다", async () => {
    for (const t of ["", "0", "01", "-1", "1/2", "9223372036854775808", "%31"]) {
      await expect(fetchLiveCandidates(t)).rejects.toSatisfy(isLiveCandidatesShapeError);
    }
    expect(apiFetchLimitedJSON).not.toHaveBeenCalled();
  });
});

describe("표시 글", () => {
  it("409 candidate_run_not_active 만 '실행 닫힘'으로 본다", () => {
    expect(isCandidateRunNotActive(new ApiError(409, "candidate_run_not_active"))).toBe(true);
    expect(isCandidateRunNotActive(new ApiError(409, "busy"))).toBe(false);
    expect(isCandidateRunNotActive(new ApiError(503, "candidate_run_not_active"))).toBe(false);
  });

  it("등급 없음은 미평가로 두고 낮음 · 정보성으로 바꾸지 않는다", () => {
    expect(candidateSeverityText(null)).toBe("위험도 미평가");
    // '예상' 말은 붙이지 않는다(사용자 11/24) — 확정이 아님은 줄의 '검토 필요'가 말한다
    expect(candidateSeverityText("high")).toBe("높음");
    expect(candidateSeverityText("info")).toBe("정보성");
  });

  it("오류 글은 상태별 고정 문장이고 서버 원문을 싣지 않는다", () => {
    expect(candidateErrorText(new ApiError(403, "candidate_scope_denied"))).toMatch(/권한/);
    expect(candidateErrorText(new ApiError(503, "secret-ish raw"))).not.toContain("secret-ish");
    expect(candidateErrorText(new ApiError(422, "response_limit"))).toMatch(/계약과 달라/);
    expect(candidateErrorText(new Error("x"))).toBe("취약점을 읽지 못했습니다.");
  });
});
