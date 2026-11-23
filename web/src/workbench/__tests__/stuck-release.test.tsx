import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/diagnosis/api", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/api")>()),
  apiFetch: vi.fn(),
}));

import { ApiError, apiFetch } from "@/lib/diagnosis/api";

import { parseReleaseView, refusalText, validReason } from "../live/stuckRelease";
import { StuckReleaseBox } from "../views/live/StuckReleaseBox";

const NOW = "2026-11-23T05:00:00Z";
// 서버 diagnosisprep.ReleaseView 모양
const notStuck = { release: { eligible: false, refusal: "not_stuck", quiet_seconds: 1200, now: NOW } };
const blocked = (extra: Record<string, unknown>) => ({
  release: {
    job_id: "prep-abc123",
    job_revision: 7,
    job_state: "unknown",
    eligible: false,
    refusal: "not_quiet",
    quiet_seconds: 1200,
    now: NOW,
    receipt_updated_at: "2026-11-23T04:50:00Z",
    eligible_at: "2026-11-23T05:10:00Z",
    ...extra,
  },
});
const eligible = blocked({ eligible: true, refusal: undefined, eligible_at: "2026-11-23T04:40:00Z" });
const record = (completed: boolean) => ({
  job_id: "prep-abc123",
  principal: "ARTEX",
  request_id: "product-release-1",
  reason: "응답 없음",
  requested_at: "2026-11-23T05:01:00Z",
  receipt_updated_at: "2026-11-23T04:50:00Z",
  quiet_seconds: 1200,
  job_state: "unknown",
  previous_init: "pid:[1]@5",
  exploration_closed: false,
  ...(completed
    ? { new_init: "pid:[1]@9", completed_at: "2026-11-23T05:02:00Z", boundary_checked_at: "2026-11-23T05:02:00Z" }
    : {}),
});
const pending = blocked({ refusal: "pending_restart", record: record(false) });
const releasedView = {
  release: { eligible: false, refusal: "not_stuck", quiet_seconds: 1200, now: NOW, record: record(true) },
};

const fetchMock = vi.mocked(apiFetch);

beforeEach(() => {
  vi.resetAllMocks();
  vi.useFakeTimers({ shouldAdvanceTime: true });
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("parseReleaseView — 계약과 다르면 해제 가능으로 추정하지 않는다", () => {
  it("막는 작업 없음 · 기준 전 · 해제 가능 · 대기를 읽는다", () => {
    expect(parseReleaseView(notStuck)).toMatchObject({ jobId: "", refusal: "not_stuck", eligible: false });
    expect(parseReleaseView(blocked({}))).toMatchObject({ jobId: "prep-abc123", refusal: "not_quiet", jobRevision: 7 });
    expect(parseReleaseView(eligible)).toMatchObject({ eligible: true, refusal: "" });
    expect(parseReleaseView(pending).record).toMatchObject({ jobId: "prep-abc123", completedAt: "" });
  });

  it.each([
    ["release 칸 없음", {}],
    ["가능인데 사유 있음", blocked({ eligible: true })],
    ["모르는 사유", blocked({ refusal: "maybe" })],
    ["작업 없는데 다른 사유", { release: { ...notStuck.release, refusal: "not_quiet" } }],
    ["판 없음", blocked({ job_revision: 0 })],
    ["완료 시각만 있음", blocked({ record: { ...record(false), completed_at: NOW } })],
  ])("%s", (_name, res) => {
    expect(() => parseReleaseView(res)).toThrow(ApiError);
  });

  it("사유는 비지 않고 500바이트 이하 · 제어 문자 없음", () => {
    expect(validReason("응답 없음")).toBe(true);
    expect(validReason("   ")).toBe(false);
    // 한글 한 자 = UTF-8 3바이트: 166자 = 498바이트, 167자 = 501바이트
    expect(validReason("가".repeat(166))).toBe(true);
    expect(validReason("가".repeat(167))).toBe(false);
    expect(validReason("줄\n바꿈")).toBe(false);
    expect(refusalText({ refusal: "job_running", quietSeconds: 1200 })).toContain("종료");
  });
});

describe("StuckReleaseBox", () => {
  it("막는 작업이 없거나 관리자가 아니면 그리지 않는다", async () => {
    fetchMock.mockResolvedValueOnce(notStuck);
    const { container } = render(<StuckReleaseBox />);
    await act(() => Promise.resolve());
    expect(container).toBeEmptyDOMElement();
    cleanup();
    fetchMock.mockRejectedValueOnce(new ApiError(403, "operator_required"));
    const second = render(<StuckReleaseBox />);
    await act(() => Promise.resolve());
    expect(second.container).toBeEmptyDOMElement();
  });

  it("기준 시간 전에는 해제 버튼 없이 사유를 보인다", async () => {
    fetchMock.mockResolvedValueOnce(blocked({}));
    render(<StuckReleaseBox />);
    await act(() => Promise.resolve());
    expect(screen.getByText(/새 점검 시작을 막고 있어요/)).toBeInTheDocument();
    expect(screen.getByText(/최근 20분 안에/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "멈춘 작업 해제" })).not.toBeInTheDocument();
  });

  it("사유와 동의가 있어야 보내고, 재시작 중을 보인 뒤 다시 붙으면 해제 완료를 보인다", async () => {
    fetchMock.mockResolvedValueOnce(eligible);
    const health = vi
      .fn()
      .mockRejectedValueOnce(new TypeError("끊김"))
      .mockImplementation(() => Promise.resolve(new Response(JSON.stringify({ ready: true }), { status: 200 })));
    vi.stubGlobal("fetch", health);
    render(<StuckReleaseBox />);
    await act(() => Promise.resolve());
    const button = screen.getByRole("button", { name: "멈춘 작업 해제" });
    expect(button).toBeDisabled();
    fireEvent.change(screen.getByLabelText("해제 사유"), { target: { value: "응답 없음" } });
    expect(button).toBeDisabled();
    fireEvent.click(screen.getByLabelText("해제 영향 동의"));
    expect(button).toBeEnabled();
    // 감사 기록 뒤 준비 서비스가 바로 끝나 응답을 잃은 경우
    fetchMock.mockRejectedValueOnce(new TypeError("Failed to fetch"));
    await act(async () => {
      fireEvent.click(button);
    });
    const [, init] = fetchMock.mock.calls[1];
    expect(JSON.parse(String(init?.body))).toEqual({
      job_id: "prep-abc123",
      expected_revision: 7,
      reason: "응답 없음",
    });
    expect(screen.getByRole("status")).toHaveTextContent("재시작 중이에요. 잠시 뒤 다시 연결해요.");
    fetchMock.mockResolvedValueOnce(pending);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(4_100);
    });
    expect(screen.getByRole("status")).toHaveTextContent("탐색 경계 세션 없음을 확인하고 있어요");
    fetchMock.mockResolvedValueOnce(releasedView);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_100);
    });
    expect(screen.getByRole("status")).toHaveTextContent("해제를 마쳤어요");
  });

  it("거절되면 사유 문구를 보이고 재시작으로 넘어가지 않는다", async () => {
    fetchMock.mockResolvedValueOnce(eligible);
    render(<StuckReleaseBox />);
    await act(() => Promise.resolve());
    fireEvent.change(screen.getByLabelText("해제 사유"), { target: { value: "응답 없음" } });
    fireEvent.click(screen.getByLabelText("해제 영향 동의"));
    fetchMock.mockRejectedValueOnce(new ApiError(409, "release_exploration_open"));
    fetchMock.mockResolvedValueOnce(eligible);
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "멈춘 작업 해제" }));
    });
    expect(screen.getByRole("alert")).toHaveTextContent("탐색 경계 세션을 닫거나 닫힘을 확인하지 못했어요");
    expect(screen.queryByText(/재시작 중이에요/)).not.toBeInTheDocument();
  });
});
