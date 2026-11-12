import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

// 실 seam(정규화·판정)은 그대로 쓰고 getControlPolicy 만 spy — 기본은 실 MOCK 위임, edge 는 per-test override.
vi.mock("@/lib/diagnosis/control-policy", async (importOriginal) => {
  const real = await importOriginal<typeof import("@/lib/diagnosis/control-policy")>();
  return { ...real, getControlPolicy: vi.fn().mockImplementation(real.getControlPolicy) };
});

import { type ControlPolicy, getControlPolicy } from "@/lib/diagnosis/control-policy";

import { UrlControlPolicyLive } from "../views/live/UrlControlPolicyLive";

const g = vi.mocked(getControlPolicy);
afterEach(cleanup);

const good: ControlPolicy = {
  policyRef: "pol-1",
  policyRevision: "rev-1",
  approvalMode: "workbench_v1",
  validFrom: "2000-01-01T00:00:00Z",
  validUntil: "2999-01-01T00:00:00Z",
  rules: [
    {
      ref: "r1",
      origin: "http://localhost:3000",
      connectIp: "127.0.0.1",
      pathPrefix: "/",
      pathScopeRef: null,
      methods: ["GET"],
      credentialHeaderNames: ["Authorization"],
      tlsPinned: false,
    },
  ],
  limits: { maxRequests: 500, maxConcurrent: 2, outputLimit: 65536, budgetRef: "b1" },
  auditBound: true,
};

describe("UrlControlPolicyLive (예시)", () => {
  it("마스킹 고지·예시 배너와 승인 범위·상한을 그린다", async () => {
    const { container } = render(<UrlControlPolicyLive />);
    expect(await screen.findByText(/예시 데이터/)).toBeInTheDocument();
    expect(screen.getByText(/자격 헤더는 이름만/)).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "정책 요약" })).toBeInTheDocument();
    // 승인된 origin·메서드·상한이 보인다(비밀 아님)
    expect(screen.getAllByText("http://localhost:3000").length).toBeGreaterThan(0);
    expect(screen.getByText("작업대 승인(v1)", { exact: false })).toBeInTheDocument();
    // 자격 헤더는 '이름'만 — 값(Bearer 토큰·Cookie 문자열)은 없다
    expect(container.textContent ?? "").toContain("Authorization");
    expect(container.textContent ?? "").not.toMatch(/Bearer /);
    expect(container.textContent ?? "").not.toMatch(/Cookie:/i);
  });

  it("빈 메서드 규칙은 '전부 허용'이 아니라 차단으로 명시(fail-closed)", async () => {
    g.mockResolvedValueOnce({ ...good, rules: [{ ...good.rules[0], methods: [] }] });
    render(<UrlControlPolicyLive />);
    expect(await screen.findByText(/승인 메서드가 비어 있는 규칙/)).toHaveTextContent("1개");
    expect(screen.getByText(/승인 메서드 없음 — 이 규칙으로는 전송 차단/)).toBeInTheDocument();
  });

  it("유효기간 밖 정책은 만료 경고(fail-closed), '유효'로 칠하지 않는다", async () => {
    g.mockResolvedValueOnce({ ...good, validUntil: "2000-02-01T00:00:00Z" });
    render(<UrlControlPolicyLive />);
    expect(await screen.findByText(/지금은 정책 유효기간 밖입니다/)).toBeInTheDocument();
    expect(screen.getByText(/유효기간 밖\(만료·미발효\)/)).toBeInTheDocument();
    expect(screen.queryByText("유효기간 내")).toBeNull();
  });

  it("투영을 정규화 못 하면 표시하지 않는다(fail-closed)", async () => {
    g.mockResolvedValueOnce({} as unknown as ControlPolicy);
    render(<UrlControlPolicyLive />);
    expect(await screen.findByText(/정책 투영을 확인할 수 없습니다/)).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "정책 요약" })).toBeNull();
  });

  it("내보내기·공유·export 버튼을 두지 않는다", async () => {
    render(<UrlControlPolicyLive />);
    await screen.findByText(/예시 데이터/);
    for (const el of [...screen.queryAllByRole("button"), ...screen.queryAllByRole("link")]) {
      expect(el.textContent ?? "").not.toMatch(/내보내기|공유|export/i);
    }
  });
});
