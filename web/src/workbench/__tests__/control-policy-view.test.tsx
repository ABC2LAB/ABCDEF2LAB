import "@testing-library/jest-dom/vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// 실 seam(정규화·판정)은 그대로 쓰고 getControlPolicy 만 spy — 기본은 실 구현 위임(task 없음 → 예시), edge 는 per-test override.
vi.mock("@/lib/diagnosis/control-policy", async (importOriginal) => {
  const real = await importOriginal<typeof import("@/lib/diagnosis/control-policy")>();
  return { ...real, getControlPolicy: vi.fn().mockImplementation(real.getControlPolicy) };
});

import { type ControlPolicy, getControlPolicy, type PolicyLoad } from "@/lib/diagnosis/control-policy";

import { resetUrl, useUrl } from "../live/urlDiagnosis";
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
