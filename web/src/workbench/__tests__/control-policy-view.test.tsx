import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError, apiFetch } from "@/lib/diagnosis/api";
import { serverPolicy } from "@/lib/diagnosis/control-policy-fixtures";
import { running } from "@/lib/diagnosis/diagnosis-url-fixtures";

import { resetUrl, useUrl } from "../live/urlDiagnosis";
import { UrlControlPolicyLive } from "../views/live/UrlControlPolicyLive";

vi.mock("@/lib/diagnosis/api", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/api")>()),
  apiFetch: vi.fn(),
}));

const f = vi.mocked(apiFetch);
const other = { ...running, id: "job-2", task_id: "43" };
const rules = (over: Record<string, unknown>) => ({
  rules: [{ ...(serverPolicy().rules as Record<string, unknown>[])[0], ...over }],
});
/** 응답을 시험이 정한 때에 돌려준다(늦게 온 이전 task 응답을 흉내). */
function deferred() {
  let resolve: (v: unknown) => void = () => undefined;
  const promise = new Promise<unknown>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

beforeEach(() => {
  vi.resetAllMocks();
  resetUrl();
  useUrl.setState({ job: running });
});
afterEach(cleanup);

describe("UrlControlPolicyLive (실제 정책 · 계약 §3)", () => {
  it("현재 task 의 정책을 읽어 승인 범위 · 상한 · 계정 라벨을 그리고, 예시 배너는 없다", async () => {
    f.mockResolvedValue(serverPolicy());
    const { container } = render(<UrlControlPolicyLive />);
    expect(await screen.findByRole("heading", { name: "정책 요약" })).toBeInTheDocument();
    expect(f).toHaveBeenCalledWith(
      "/api/tasks/42/control-policy",
      expect.objectContaining({ cache: "no-store" }),
      false,
    );
    expect(screen.queryByText(/예시 데이터/)).toBeNull();
    expect(screen.getAllByText("https://shop.example")).toHaveLength(2);
    expect(screen.getByText("승인 방식: 사전 승인 범위")).toBeInTheDocument();
    expect(screen.getByText("자격 헤더: 모델 자격 헤더 거부")).toBeInTheDocument();
    expect(screen.getByText("결속된 시험 계정: tester-a (라벨만)")).toBeInTheDocument();
    expect(screen.getByText(`지문 ${"a".repeat(12)}…`)).toBeInTheDocument();
    expect(container.querySelector('[data-rule="target-path-b"] [data-method="POST"]')).not.toBeNull();
    for (const t of [
      "최대 요청 64",
      "최대 시도 200",
      "최대 동시 1",
      "요청 본문 상한 65536 B",
      "요청 제한 시간 5000 ms",
    ])
      expect(container.textContent).toContain(t);
  });

  it("http 대상은 'TLS 없음(평문 전송)', https 는 경고 없이 TLS 핀 결속", async () => {
    f.mockResolvedValueOnce(serverPolicy(rules({ origin: "http://shop.example", tls_pinned: false })));
    const v = render(<UrlControlPolicyLive />);
    expect(await screen.findByText("TLS 없음 (평문 전송)")).toBeInTheDocument();
    v.unmount();
    f.mockResolvedValueOnce(serverPolicy());
    render(<UrlControlPolicyLive />);
    expect(await screen.findAllByText("TLS 핀 결속")).toHaveLength(2);
    expect(screen.queryByText(/TLS 없음/)).toBeNull();
  });

  it("정확 경로 · 인증 준비 규칙을 따로 표시한다", async () => {
    f.mockResolvedValue(
      serverPolicy(rules({ path_match: "exact", purpose: "auth_prep", path_prefix: "/login", methods: ["POST"] })),
    );
    render(<UrlControlPolicyLive />);
    expect(await screen.findByText("정확 경로 /login")).toBeInTheDocument();
    expect(screen.getByText("인증 준비 전용 — 모델 요청은 쓰지 못함")).toBeInTheDocument();
  });

  it("빈 메서드 규칙은 '전부 허용'이 아니라 차단으로 명시(fail-closed)", async () => {
    f.mockResolvedValue(serverPolicy(rules({ methods: [] })));
    render(<UrlControlPolicyLive />);
    expect(await screen.findByText(/승인 메서드가 비어 있는 규칙/)).toHaveTextContent("1개");
    expect(screen.getByText(/승인 메서드 없음 — 이 규칙으로는 전송 차단/)).toBeInTheDocument();
  });

  it("유효기간 밖 정책은 만료 경고(fail-closed), '유효'로 칠하지 않는다", async () => {
    f.mockResolvedValue(serverPolicy({ valid_until: "2000-02-01T00:00:00Z", valid_from: "2000-01-01T00:00:00Z" }));
    render(<UrlControlPolicyLive />);
    expect(await screen.findByText(/지금은 정책 유효기간 밖입니다/)).toBeInTheDocument();
    expect(screen.queryByText("유효기간 내")).toBeNull();
  });

  it.each([
    ["계약과 다른 응답", serverPolicy({ mock: true }), /정책 응답을 확인할 수 없습니다/],
    ["404 결속 전", new ApiError(404, "x"), /아직 전송 통제 정책에 결속되지 않았습니다/],
    ["409 지문 불일치", new ApiError(409, "x"), /실행 정책이 승인 기록과 달라/],
    ["403 권한 거부", new ApiError(403, "x"), /정책을 볼 권한을 확인하지 못했습니다/],
    ["503 장애", new ApiError(503, "x"), /불러오지 못했습니다 \(HTTP 503\)/],
  ])("%s 이면 정책을 그리지 않고 이유만 보인다", async (_, result, text) => {
    if (result instanceof ApiError) f.mockRejectedValue(result);
    else f.mockResolvedValue(result);
    render(<UrlControlPolicyLive />);
    expect(await screen.findByRole("alert")).toHaveTextContent(text);
    expect(screen.queryByRole("heading", { name: "정책 요약" })).toBeNull();
    expect(screen.queryByText("https://shop.example")).toBeNull();
  });

  it("다시 읽기는 같은 task 를 다시 부르고, 성공하면 오류를 지운다", async () => {
    f.mockRejectedValueOnce(new ApiError(503, "x")).mockResolvedValueOnce(serverPolicy());
    render(<UrlControlPolicyLive />);
    fireEvent.click(await screen.findByRole("button", { name: "다시 읽기" }));
    expect(await screen.findByRole("heading", { name: "정책 요약" })).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(f).toHaveBeenCalledTimes(2);
    expect(f.mock.calls.every(([path]) => path === "/api/tasks/42/control-policy")).toBe(true);
  });

  it("Job 을 바꾸면 이전 task 의 정책을 바로 버리고, 늦게 온 이전 응답도 그리지 않는다", async () => {
    const late = deferred();
    f.mockReturnValueOnce(late.promise as never).mockResolvedValueOnce(serverPolicy({ policy_ref: "scope-target-43" }));
    render(<UrlControlPolicyLive />);
    act(() => useUrl.setState({ job: other }));
    expect(await screen.findByText("정책 scope-target-43")).toBeInTheDocument();
    expect(f.mock.calls.map(([path]) => path)).toEqual([
      "/api/tasks/42/control-policy",
      "/api/tasks/43/control-policy",
    ]);
    // 전환 때 이전 요청을 끊는다
    expect((f.mock.calls[0][1] as RequestInit).signal?.aborted).toBe(true);
    await act(async () => late.resolve(serverPolicy({ policy_ref: "scope-target-42" })));
    expect(screen.queryByText("정책 scope-target-42")).toBeNull();
    expect(screen.getByText("정책 scope-target-43")).toBeInTheDocument();
  });

  it("새 Job 의 응답을 기다리는 동안 이전 정책을 보이지 않는다", async () => {
    f.mockResolvedValueOnce(serverPolicy()).mockReturnValueOnce(new Promise(() => undefined) as never);
    render(<UrlControlPolicyLive />);
    await screen.findByRole("heading", { name: "정책 요약" });
    act(() => useUrl.setState({ job: other }));
    expect(screen.getByText("불러오는 중…")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "정책 요약" })).toBeNull();
  });

  it("Job 이 빠지면(권한 철회 · 재로그인) 정책을 지우고 예시로 채우지 않는다", async () => {
    f.mockResolvedValue(serverPolicy());
    render(<UrlControlPolicyLive />);
    await screen.findByRole("heading", { name: "정책 요약" });
    act(() => useUrl.setState({ job: null }));
    expect(screen.getByText("실제 진단 작업을 선택하세요.")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "정책 요약" })).toBeNull();
    expect(f).toHaveBeenCalledTimes(1);
  });

  it("task 가 없으면 부르지 않고 예시도 보이지 않는다", () => {
    useUrl.setState({ job: { ...running, task_id: undefined } as never });
    render(<UrlControlPolicyLive />);
    expect(screen.getByText("실제 진단 작업을 선택하세요.")).toBeInTheDocument();
    expect(f).not.toHaveBeenCalled();
    expect(screen.queryByText(/예시/)).toBeNull();
  });

  it("내보내기·공유·export 버튼을 두지 않는다", async () => {
    f.mockResolvedValue(serverPolicy());
    render(<UrlControlPolicyLive />);
    await screen.findByRole("heading", { name: "정책 요약" });
    for (const el of [...screen.queryAllByRole("button"), ...screen.queryAllByRole("link")]) {
      expect(el.textContent ?? "").not.toMatch(/내보내기|공유|export/i);
    }
  });
});
