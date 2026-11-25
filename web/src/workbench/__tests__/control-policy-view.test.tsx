import "@testing-library/jest-dom/vitest";
import { useContext } from "react";

import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// 실 seam(정규화·판정)은 그대로 쓰고 getControlPolicy 만 spy — 기본은 실 구현 위임(task 없음 → 예시), edge 는 per-test override.
vi.mock("@/lib/diagnosis/control-policy", async (importOriginal) => {
  const real = await importOriginal<typeof import("@/lib/diagnosis/control-policy")>();
  return { ...real, getControlPolicy: vi.fn().mockImplementation(real.getControlPolicy) };
});

import { type ControlPolicy, getControlPolicy, type PolicyLoad } from "@/lib/diagnosis/control-policy";

import { resetUrl, useUrl } from "../live/urlDiagnosis";
import { ShellContext } from "../shell";
import { UrlControlPolicyLive } from "../views/live/UrlControlPolicyLive";

const g = vi.mocked(getControlPolicy);
beforeEach(() => resetUrl());
afterEach(cleanup);

const good: ControlPolicy = {
  policyRef: "pol-1",
  policyRevision: "rev-1",
  fingerprint: "cd".repeat(32),
  approvalMode: "preauthorized_scope",
  headerMode: "test_account_passthrough",
  validFrom: "2000-01-01T00:00:00Z",
  validUntil: "2999-01-01T00:00:00Z",
  rules: [
    {
      ref: "r1",
      purpose: "",
      origin: "http://localhost:3000",
      connectIp: "127.0.0.1",
      pathMatch: "prefix",
      pathPrefix: "/",
      pathScopeRef: null,
      methods: ["GET"],
      tlsPinned: false,
    },
  ],
  limits: {
    maxRequests: 500,
    maxAttempts: 1000,
    maxConcurrent: 2,
    maxBodyBytes: 65536,
    maxResponseBodyBytes: 65536,
    requestTimeoutMs: 5000,
  },
  accounts: ["tester-a"],
  auditBound: true,
};
const ready = (p: Partial<ControlPolicy> = {}): PolicyLoad => ({ state: "ready", policy: { ...good, ...p } });
const withTask = (id: string) => useUrl.setState({ job: { task_id: id } as never });

describe("UrlControlPolicyLive (예시 — 점검 전)", () => {
  it("task 가 없으면 예시 배너와 승인 범위·상한을 그리고, 비밀 값은 없다", async () => {
    const { container } = render(<UrlControlPolicyLive />);
    expect(await screen.findByText(/예시 데이터/)).toBeInTheDocument();
    expect(g).toHaveBeenCalledWith(null, expect.any(AbortSignal));
    expect(screen.getByRole("heading", { name: "정책 요약" })).toBeInTheDocument();
    expect(screen.getAllByText("http://localhost:3000").length).toBeGreaterThan(0);
    expect(screen.getByText("작업대 승인(v1)", { exact: false })).toBeInTheDocument();
    expect(container.textContent ?? "").not.toMatch(/Bearer |Cookie:/i);
  });

  it("편집·저장·내보내기 버튼을 두지 않는다(계약 §6)", async () => {
    render(<UrlControlPolicyLive />);
    await screen.findByText(/예시 데이터/);
    for (const el of [...screen.queryAllByRole("button"), ...screen.queryAllByRole("link")]) {
      expect(el.textContent ?? "").not.toMatch(/저장|편집|수정|내보내기|공유|export/i);
    }
    expect(screen.queryByRole("textbox")).toBeNull();
  });
});

describe("UrlControlPolicyLive (실 정책)", () => {
  it("현재 점검 task 로 읽고 예시 배너 없이 승인 방식·헤더 방식·계정 라벨·상한을 보인다", async () => {
    withTask("42");
    g.mockResolvedValueOnce(ready());
    render(<UrlControlPolicyLive />);
    expect(await screen.findByText(/사전 승인 범위/)).toBeInTheDocument();
    expect(g).toHaveBeenCalledWith("42", expect.any(AbortSignal));
    expect(screen.queryByText(/예시 데이터/)).toBeNull();
    expect(screen.getByText(/시험 계정 자격 사용/)).toBeInTheDocument();
    expect(screen.getByText("tester-a")).toBeInTheDocument();
    expect(screen.getByText(/최대 시도/)).toHaveTextContent("1000");
    expect(screen.getByText(/요청 시간/)).toHaveTextContent("5000");
  });

  it("확정 단계 몫: 0 은 '꺼짐', 양수는 'n건 · m초', 칸 없음은 '정하지 않음(이전 회차)' — 셋을 섞지 않는다", async () => {
    withTask("42");
    for (const [share, text] of [
      [{ requests: 0, timeoutMs: 0 }, "꺼짐(0)"],
      [{ requests: 12, timeoutMs: 90_000 }, "12건 · 90초"],
      [{ requests: 3, timeoutMs: 1_500 }, "3건 · 1.5초"],
      [undefined, "정하지 않음(이전 회차)"],
    ] as const) {
      g.mockResolvedValueOnce(
        ready({ limits: { ...good.limits, ...(share ? { e3Confirmation: { ...share } } : {}) } }),
      );
      const { unmount } = render(<UrlControlPolicyLive />);
      const chip = await screen.findByText(/확정 단계 몫/);
      expect(chip).toHaveTextContent(`확정 단계 몫 ${text}`);
      unmount();
    }
  });

  it("http 대상은 'TLS 없음(평문 전송)' 경고, https 대상은 없다", async () => {
    withTask("1");
    g.mockResolvedValueOnce(ready());
    const { unmount } = render(<UrlControlPolicyLive />);
    expect((await screen.findAllByText(/TLS 없음/)).length).toBeGreaterThan(0);
    unmount();
    g.mockResolvedValueOnce(
      ready({ rules: [{ ...good.rules[0], origin: "https://secure.example", tlsPinned: true }] }),
    );
    render(<UrlControlPolicyLive />);
    await screen.findByText("https://secure.example");
    expect(screen.queryByText(/TLS 없음/)).toBeNull();
    expect(screen.getByText("TLS 핀 결속")).toBeInTheDocument();
  });

  it("정확 경로는 =/… 로, 인증 준비 규칙은 따로 표시한다", async () => {
    withTask("1");
    g.mockResolvedValueOnce(
      ready({
        rules: [
          { ...good.rules[0], ref: "x", pathMatch: "exact", pathPrefix: "/rest/basket/1" },
          { ...good.rules[0], ref: "auth", purpose: "auth_prep", pathPrefix: "/rest/user/login", methods: ["POST"] },
        ],
      }),
    );
    render(<UrlControlPolicyLive />);
    expect(await screen.findByText(/정확 경로 =\/rest\/basket\/1/)).toBeInTheDocument();
    expect(screen.getByText("인증 준비 전용")).toBeInTheDocument();
    expect(screen.getByText(/모델이 만든 진단 요청은 이 규칙으로 나가지 않습니다/)).toBeInTheDocument();
  });

  it("빈 메서드 규칙은 '전부 허용'이 아니라 차단으로 명시(fail-closed)", async () => {
    withTask("1");
    g.mockResolvedValueOnce(ready({ rules: [{ ...good.rules[0], methods: [] }] }));
    render(<UrlControlPolicyLive />);
    expect(await screen.findByText(/승인 메서드가 비어 있는 규칙/)).toHaveTextContent("1개");
    expect(screen.getByText(/승인 메서드 없음 — 이 규칙으로는 전송 차단/)).toBeInTheDocument();
  });

  it("유효기간 밖 정책은 만료 경고, '유효'로 칠하지 않는다", async () => {
    withTask("1");
    g.mockResolvedValueOnce(ready({ validUntil: "2000-02-01T00:00:00Z" }));
    render(<UrlControlPolicyLive />);
    expect(await screen.findByText(/지금은 정책 유효기간 밖입니다/)).toBeInTheDocument();
    expect(screen.queryByText("유효기간 내")).toBeNull();
  });

  it("감사 미결속은 경고한다", async () => {
    withTask("1");
    g.mockResolvedValueOnce(ready({ auditBound: false }));
    render(<UrlControlPolicyLive />);
    expect(await screen.findByText(/감사 키가 결속되지 않았습니다/)).toBeInTheDocument();
  });
});

describe("UrlControlPolicyLive (정책을 못 보일 때)", () => {
  it.each([
    [{ state: "unbound" } as PolicyLoad, /아직 이 점검에 결속된 전송 통제 정책이 없습니다/],
    [{ state: "mismatch" } as PolicyLoad, /실행 정책이 운영자 승인과 다릅니다/],
    [{ state: "invalid" } as PolicyLoad, /정책 투영을 확인할 수 없습니다/],
  ])("%o → 안내만, 정책 칸은 그리지 않는다", async (load, text) => {
    withTask("1");
    g.mockResolvedValueOnce(load);
    render(<UrlControlPolicyLive />);
    expect(await screen.findByText(text)).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "정책 요약" })).toBeNull();
    expect(screen.queryByText(/예시 데이터/)).toBeNull();
  });

  it("조회 실패는 '불러오지 못함'", async () => {
    withTask("1");
    g.mockRejectedValueOnce(new Error("down"));
    render(<UrlControlPolicyLive />);
    expect(await screen.findByText(/전송 통제 정책을 불러오지 못했습니다/)).toBeInTheDocument();
  });

  it("'다시 읽기'는 같은 task 를 다시 부르고, 성공하면 정책을 보인다", async () => {
    withTask("7");
    g.mockClear();
    g.mockRejectedValueOnce(new Error("down"));
    g.mockResolvedValueOnce(ready({ policyRef: "pol-retry" }));
    render(<UrlControlPolicyLive />);
    await screen.findByText(/전송 통제 정책을 불러오지 못했습니다/);
    act(() => screen.getByRole("button", { name: "다시 읽기" }).click());
    expect(await screen.findByText(/pol-retry/)).toBeInTheDocument();
    expect(screen.queryByText(/불러오지 못했습니다/)).toBeNull();
    expect(g).toHaveBeenCalledTimes(2);
    expect(g).toHaveBeenLastCalledWith("7", expect.any(AbortSignal));
  });

  it("예시(점검 전)에는 '다시 읽기'를 두지 않는다", async () => {
    render(<UrlControlPolicyLive />);
    await screen.findByText(/예시 데이터/);
    expect(screen.queryByRole("button", { name: "다시 읽기" })).toBeNull();
  });

  it("task 가 바뀌면 이전 task 정책을 보이지 않는다", async () => {
    withTask("1");
    g.mockResolvedValueOnce(ready({ policyRef: "pol-old" }));
    g.mockImplementationOnce(() => new Promise<never>(() => undefined)); // 새 task 응답은 아직
    render(<UrlControlPolicyLive />);
    expect(await screen.findByText(/pol-old/)).toBeInTheDocument();
    act(() => withTask("2"));
    expect(screen.queryByText(/pol-old/)).toBeNull();
    expect(screen.getByText(/불러오는 중/)).toBeInTheDocument();
    expect(g).toHaveBeenLastCalledWith("2", expect.any(AbortSignal));
  });
});

describe("UrlControlPolicyLive (#689 리뷰 — 결속·진입·시각·계정 결과)", () => {
  const job = (over: Record<string, unknown>) => useUrl.setState({ job: { task_id: "42", ...over } as never });

  it("같은 task 라도 결속 사실(준비 중→실행 중)이 바뀌면 다시 읽어 결속된 정책을 보인다", async () => {
    g.mockClear();
    job({ state: "preparing", phase: "preparing", started: false, authority_state: "not_issued" });
    g.mockResolvedValueOnce({ state: "unbound" });
    g.mockResolvedValueOnce(ready({ policyRef: "pol-bound" }));
    render(<UrlControlPolicyLive />);
    expect(await screen.findByText(/아직 이 점검에 결속된/)).toBeInTheDocument();
    act(() => job({ state: "running", phase: "running", started: true, authority_state: "issued" }));
    expect(await screen.findByText(/pol-bound/)).toBeInTheDocument();
    expect(g).toHaveBeenCalledTimes(2);
    expect(g).toHaveBeenLastCalledWith("42", expect.any(AbortSignal));
  });

  it("숨겨 마운트된 화면에 들어오면 같은 task 의 정책을 다시 확인한다", async () => {
    g.mockClear();
    withTask("42");
    g.mockResolvedValueOnce({ state: "unbound" });
    g.mockResolvedValueOnce(ready({ policyRef: "pol-entry" }));
    function At({ route }: { route: "dashboard" | "controlpolicy" }) {
      const shell = useContext(ShellContext);
      return (
        <ShellContext.Provider value={{ ...shell, route }}>
          <UrlControlPolicyLive />
        </ShellContext.Provider>
      );
    }
    const { rerender } = render(<At route="dashboard" />);
    expect(await screen.findByText(/아직 이 점검에 결속된/)).toBeInTheDocument();
    rerender(<At route="controlpolicy" />);
    expect(await screen.findByText(/pol-entry/)).toBeInTheDocument();
    expect(g).toHaveBeenCalledTimes(2);
  });

  // 뒤로 가기(사용자 11/25): 들어오기 직전 화면으로 — 전송 통제 흐름에서 왔으면 그리로, 그 밖은 새 점검
  it("뒤로 가기는 들어오기 직전 화면(전송 통제 흐름 · 새 점검)으로 간다", async () => {
    g.mockResolvedValue({ state: "unbound" });
    const goTo = vi.fn();
    function At({ route }: { route: "new" | "e2flow" | "dashboard" | "controlpolicy" }) {
      const shell = useContext(ShellContext);
      return (
        <ShellContext.Provider value={{ ...shell, route, goTo }}>
          <UrlControlPolicyLive />
        </ShellContext.Provider>
      );
    }
    const { rerender } = render(<At route="e2flow" />);
    rerender(<At route="controlpolicy" />);
    fireEvent.click(screen.getByRole("button", { name: "전송 통제 흐름으로 돌아가기" }));
    expect(goTo).toHaveBeenLastCalledWith("e2flow");
    rerender(<At route="dashboard" />);
    rerender(<At route="controlpolicy" />);
    fireEvent.click(screen.getByRole("button", { name: "새 점검으로 돌아가기" }));
    expect(goTo).toHaveBeenLastCalledWith("new");
    rerender(<At route="new" />);
    rerender(<At route="controlpolicy" />);
    expect(screen.getByRole("button", { name: "새 점검으로 돌아가기" })).toBeInTheDocument();
  });

  it("열어 둔 채 유효기간이 지나면 '유효기간 내'를 지우고 만료 안내를 띄운다", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    try {
      vi.setSystemTime(new Date("2030-01-01T00:00:00Z"));
      withTask("42");
      g.mockResolvedValueOnce(ready({ validFrom: "2029-12-31T00:00:00Z", validUntil: "2030-01-01T00:01:00Z" }));
      render(<UrlControlPolicyLive />);
      await act(async () => {
        await Promise.resolve();
      });
      expect(screen.getByText("유효기간 내")).toBeInTheDocument();
      await act(async () => {
        vi.advanceTimersByTime(61_000);
      });
      expect(screen.queryByText("유효기간 내")).toBeNull();
      expect(screen.getByText(/지금은 정책 유효기간 밖입니다/)).toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  it("A→B→A 로 빠르게 돌아오면 처음 A 정책이 아니라 이번 A 조회가 끝날 때까지 불러오는 중이다", async () => {
    g.mockClear();
    withTask("1");
    g.mockResolvedValueOnce(ready({ policyRef: "pol-a-first" }));
    g.mockImplementationOnce(() => new Promise<never>(() => undefined)); // B 응답은 아직
    let finishA: (l: PolicyLoad) => void = () => undefined;
    g.mockImplementationOnce(
      () =>
        new Promise<PolicyLoad>((res) => {
          finishA = res;
        }),
    );
    render(<UrlControlPolicyLive />);
    expect(await screen.findByText(/pol-a-first/)).toBeInTheDocument();
    act(() => withTask("2"));
    act(() => withTask("1"));
    expect(screen.queryByText(/pol-a-first/)).toBeNull();
    expect(screen.getByText(/불러오는 중/)).toBeInTheDocument();
    expect(g).toHaveBeenCalledTimes(3);
    expect(g).toHaveBeenLastCalledWith("1", expect.any(AbortSignal));
    await act(async () => finishA(ready({ policyRef: "pol-a-second" })));
    expect(screen.getByText(/pol-a-second/)).toBeInTheDocument();
    expect(screen.queryByText(/pol-a-first/)).toBeNull();
  });

  it("미리 마운트된 화면도 정책을 받은 실제 시각으로 유효기간을 재고, 그 시각 기준으로 다음 경계를 잡는다", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    try {
      vi.setSystemTime(new Date("2030-01-01T10:00:00Z"));
      withTask("42");
      let finish: (l: PolicyLoad) => void = () => undefined;
      g.mockImplementationOnce(
        () =>
          new Promise<PolicyLoad>((res) => {
            finish = res;
          }),
      );
      render(<UrlControlPolicyLive />);
      vi.setSystemTime(new Date("2030-01-01T11:30:00Z"));
      await act(async () => finish(ready({ validFrom: "2030-01-01T11:00:00Z", validUntil: "2030-01-01T12:00:00Z" })));
      expect(screen.getByText("유효기간 내")).toBeInTheDocument();
      expect(screen.queryByText(/지금은 정책 유효기간 밖입니다/)).toBeNull();
      // 11:30 기준 다음 경계는 12:00(30분 뒤) — 10:00 기준 11:00 경계로 잡히면 여기서 바뀌지 않는다
      await act(async () => {
        vi.advanceTimersByTime(30 * 60_000 + 1_000);
      });
      expect(screen.queryByText("유효기간 내")).toBeNull();
      expect(screen.getByText(/지금은 정책 유효기간 밖입니다/)).toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  it("자격 헤더는 정책 설정으로 표시하고, 모든 시험 계정 로그인이 실패한 회차는 실제 결과를 함께 보인다", async () => {
    job({ state: "completed", account_auth: { "tester-a": "login_failed" } });
    g.mockResolvedValueOnce(ready());
    render(<UrlControlPolicyLive />);
    expect(await screen.findByText(/자격 헤더\(정책 설정\): 시험 계정 자격 사용/)).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "이 점검의 시험 계정 인증 결과" })).toBeInTheDocument();
    expect(screen.getByText("비인증 진행 · 로그인 실패")).toBeInTheDocument();
    expect(screen.getByText(/이 점검에서 인증된 시험 계정이 없습니다/)).toBeInTheDocument();
  });

  it("인증된 계정이 하나라도 있으면 '인증된 계정 없음' 경고는 없다", async () => {
    job({ state: "completed", account_auth: { "tester-a": "authenticated", "tester-b": "login_failed" } });
    g.mockResolvedValueOnce(ready());
    render(<UrlControlPolicyLive />);
    expect(await screen.findByText("인증됨")).toBeInTheDocument();
    expect(screen.queryByText(/인증된 시험 계정이 없습니다/)).toBeNull();
  });
});
