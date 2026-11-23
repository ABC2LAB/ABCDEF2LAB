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

const NOW = "2026-11-23T05:03:00Z";
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
  exploration_session: "none",
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
  vi.setSystemTime(new Date(NOW));
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

const healthReady = () => Promise.resolve(new Response(JSON.stringify({ ready: true }), { status: 200 }));

// 해제 가능 화면에서 사유·동의를 넣고 보낸다. post는 해제 POST의 결과(응답 유실이면 TypeError)다.
async function sendRelease(post: () => Promise<unknown>) {
  fetchMock.mockResolvedValueOnce(eligible);
  render(<StuckReleaseBox />);
  await act(() => Promise.resolve());
  fireEvent.change(screen.getByLabelText("해제 사유"), { target: { value: "응답 없음" } });
  fireEvent.click(screen.getByLabelText("해제 영향 동의"));
  fetchMock.mockImplementationOnce(post);
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "멈춘 작업 해제" }));
  });
}

describe("StuckReleaseBox — 재접속 판정", () => {
  it("브라우저 시계가 서버와 달라도 이번 요청의 기록을 알아본다", async () => {
    vi.setSystemTime(new Date("2026-11-23T09:00:00Z")); // 서버 기록 시각보다 4시간 앞
    vi.stubGlobal("fetch", vi.fn().mockImplementation(healthReady));
    await sendRelease(() => Promise.reject(new TypeError("Failed to fetch")));
    fetchMock.mockResolvedValueOnce(releasedView);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_100);
    });
    expect(screen.getByRole("status")).toHaveTextContent("해제를 마쳤어요");
  });

  it("1분이 지나도 다시 붙지 않으면 복구 명령을 안내한다", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("끊김")));
    await sendRelease(() => Promise.reject(new TypeError("Failed to fetch")));
    expect(screen.queryByText(/product-compose\.sh up -d/)).not.toBeInTheDocument();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(62_000);
    });
    expect(screen.getByRole("alert")).toHaveTextContent("./scripts/product-compose.sh up -d");
    expect(screen.getByRole("status")).toHaveTextContent("재시작 중이에요");
  });

  it("10분 넘어 '다시 연결'해도 원래 요청의 기록을 알아본다", async () => {
    const health = vi.fn().mockRejectedValue(new TypeError("끊김"));
    vi.stubGlobal("fetch", health);
    await sendRelease(() => Promise.reject(new TypeError("Failed to fetch")));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10 * 60_000 + 4_000);
    });
    expect(screen.getByRole("alert")).toHaveTextContent("재시작이 확인되지 않아요");
    health.mockImplementation(healthReady);
    fetchMock.mockResolvedValueOnce(releasedView);
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "다시 연결" }));
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_100);
    });
    expect(screen.getByRole("status")).toHaveTextContent("해제를 마쳤어요");
  });

  it("같은 요청이 이미 기록됐으면(409 release_pending_restart) 재시작을 기다린다", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("끊김")));
    await sendRelease(() => Promise.reject(new ApiError(409, "release_pending_restart")));
    expect(screen.getByRole("status")).toHaveTextContent("재시작 중이에요");
  });

  it("막는 작업이 없다가 생기면 주기적으로 다시 읽어 보인다", async () => {
    fetchMock.mockResolvedValueOnce(notStuck);
    const { container } = render(<StuckReleaseBox />);
    await act(() => Promise.resolve());
    expect(container).toBeEmptyDOMElement();
    fetchMock.mockResolvedValueOnce(blocked({}));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_100);
    });
    expect(screen.getByText(/새 점검 시작을 막고 있어요/)).toBeInTheDocument();
  });

  it("해제를 마친 뒤 다른 작업이 막으면 버튼이 다시 켜진다", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation(healthReady));
    await sendRelease(() => Promise.reject(new TypeError("Failed to fetch")));
    fetchMock.mockResolvedValueOnce(releasedView);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_100);
    });
    expect(screen.getByRole("status")).toHaveTextContent("해제를 마쳤어요");
    fetchMock.mockResolvedValueOnce(
      blocked({ job_id: "prep-def456", eligible: true, refusal: undefined, eligible_at: "2026-11-23T04:40:00Z" }),
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_100);
    });
    fireEvent.change(screen.getByLabelText("해제 사유"), { target: { value: "두 번째" } });
    fireEvent.click(screen.getByLabelText("해제 영향 동의"));
    expect(screen.getByRole("button", { name: "멈춘 작업 해제" })).toBeEnabled();
  });

  it("사유의 서식 문자·줄 구분자를 받지 않는다(서버와 같음)", () => {
    expect(validReason("정상‮사유")).toBe(false);
    expect(validReason("폭없는​문자")).toBe(false);
    expect(validReason("줄 구분")).toBe(false);
  });
});

describe("StuckReleaseBox — 이전 기록과 이번 기록 가르기", () => {
  it("같은 작업의 이전 기록만 다시 오면 '해제가 기록되지 않았어요'로 돌아간다", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation(healthReady));
    const before = blocked({
      eligible: true,
      refusal: undefined,
      eligible_at: "2026-11-23T04:40:00Z",
      record: record(false),
    });
    fetchMock.mockResolvedValueOnce(before);
    render(<StuckReleaseBox />);
    await act(() => Promise.resolve());
    fireEvent.change(screen.getByLabelText("해제 사유"), { target: { value: "응답 없음" } });
    fireEvent.click(screen.getByLabelText("해제 영향 동의"));
    fetchMock.mockImplementationOnce(() => Promise.reject(new TypeError("Failed to fetch")));
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "멈춘 작업 해제" }));
    });
    fetchMock.mockResolvedValueOnce(before); // 다시 붙었는데 이전 요청(product-release-1) 기록 그대로
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_100);
    });
    expect(screen.getByRole("alert")).toHaveTextContent("해제가 기록되지 않았어요");
    expect(screen.queryByText(/재시작 중이에요/)).not.toBeInTheDocument();
  });

  it("경계 확인이 2분 넘게 끝나지 않으면 탐색 경계를 보라고 안내한다", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation(healthReady));
    await sendRelease(() => Promise.reject(new TypeError("Failed to fetch")));
    fetchMock.mockResolvedValue(pending);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_100);
    });
    expect(screen.getByRole("status")).toHaveTextContent("탐색 경계 세션 없음을 확인하고 있어요");
    expect(screen.queryByText(/explore-boundary/)).not.toBeInTheDocument();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2 * 60_000 + 6_000);
    });
    expect(screen.getByRole("alert")).toHaveTextContent("explore-boundary");
  });

  it("기록의 경계 세션 값은 none·closed만 받는다", () => {
    expect(
      parseReleaseView(blocked({ record: { ...record(false), exploration_session: "closed" } })).record
        ?.explorationSession,
    ).toBe("closed");
    expect(() => parseReleaseView(blocked({ record: { ...record(false), exploration_session: "failed" } }))).toThrow(
      ApiError,
    );
  });

  it("기록의 request_id가 비었거나 모양 밖이면 받지 않는다", () => {
    expect(() => parseReleaseView(blocked({ record: { ...record(false), request_id: "" } }))).toThrow(ApiError);
    expect(() => parseReleaseView(blocked({ record: { ...record(false), request_id: "bad id!" } }))).toThrow(ApiError);
  });
});
