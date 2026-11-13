import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/diagnosis/api", () => ({ apiFetch: vi.fn() }));

import { apiFetch } from "@/lib/diagnosis/api";

import { AccountClassifyLive } from "../views/live/AccountClassifyLive";

const api = vi.mocked(apiFetch);

/** 경로·메서드별 응답. GET=대상/계정 목록, POST=쓰기(빈 응답). 쓰기 뒤 뷰는 GET 으로 다시 읽는다. */
const OMIT = Symbol("진술 칸 없음");
function serve(statement: unknown = "소유·승인된 일회용 시험 계정이라는 진술") {
  api.mockImplementation((path: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    if (path === "/api/product/targets")
      return Promise.resolve({ targets: [{ id: "t1", origin: "https://a.example", state: "approved" }] }) as never;
    if (path === "/api/product/targets/t1/accounts" && method === "GET")
      return Promise.resolve({
        accounts: [
          { label: "owner", classification: "test", classified_by: "op-1" },
          { label: "legacy", classification: "real" },
        ],
        ...(statement === OMIT ? {} : { account_statement: statement }),
      }) as never;
    if (method === "POST") return Promise.resolve({}) as never; // 등록·mark-test·unmark
    return Promise.reject(new Error(`unexpected ${method} ${path}`)) as never;
  });
}

afterEach(() => {
  cleanup();
  api.mockReset();
});

async function pickTarget() {
  fireEvent.change(await screen.findByLabelText("분류할 대상"), { target: { value: "t1" } });
  await screen.findByLabelText("계정 목록");
}

describe("AccountClassifyLive (실 API 읽기·쓰기)", () => {
  it("규칙2 고지 + 대상 선택 후 분류 목록(시험/실계정)을 그린다", async () => {
    serve();
    render(<AccountClassifyLive />);
    expect(screen.getByText(/비밀번호 · 토큰 · 세션 값은 읽지도 보이지도 않/)).toBeInTheDocument();
    await pickTarget();
    const list = within(screen.getByLabelText("계정 목록"));
    expect(list.getByText("owner")).toBeInTheDocument();
    expect(list.getAllByText("시험 계정").length).toBeGreaterThan(0);
    expect(list.getAllByText(/실계정 취급/).length).toBeGreaterThan(0);
  });

  it("§2: 내보내기·공유·export 버튼을 두지 않는다", async () => {
    serve();
    render(<AccountClassifyLive />);
    await pickTarget();
    for (const el of [...screen.queryAllByRole("button"), ...screen.queryAllByRole("link")]) {
      expect(el.textContent ?? "").not.toMatch(/내보내기|공유|export/i);
    }
  });

  it("승인된 시험 계정도 '모델 입력 가능'으로 보이지 않는다(권한은 별도)", async () => {
    serve();
    render(<AccountClassifyLive />);
    await pickTarget();
    const list = within(screen.getByLabelText("계정 목록"));
    expect(list.queryByText(/모델 입력 가능/)).toBeNull();
    const owner = list.getByText("owner").closest("li") as HTMLElement;
    expect(within(owner).getByText(/권한은 별도/)).toBeInTheDocument();
  });

  it("계정 등록: 라벨 입력 → POST accounts {label}", async () => {
    serve();
    render(<AccountClassifyLive />);
    await pickTarget();
    fireEvent.change(screen.getByLabelText("새 계정 라벨"), { target: { value: "newacct" } });
    fireEvent.click(screen.getByRole("button", { name: "계정 등록" }));
    expect(api).toHaveBeenCalledWith(
      "/api/product/targets/t1/accounts",
      expect.objectContaining({ method: "POST", body: JSON.stringify({ label: "newacct" }) }),
    );
  });

  it("형식 밖 라벨이면 등록 버튼이 막힌다", async () => {
    serve();
    render(<AccountClassifyLive />);
    await pickTarget();
    fireEvent.change(screen.getByLabelText("새 계정 라벨"), { target: { value: "User@x" } });
    expect(screen.getByRole("button", { name: "계정 등록" })).toBeDisabled();
  });

  it("시험 표시: 진술 동의 뒤에만 POST mark-test", async () => {
    serve();
    render(<AccountClassifyLive />);
    await pickTarget();
    const legacy = within(screen.getByLabelText("계정 목록")).getByText("legacy").closest("li") as HTMLElement;
    fireEvent.click(within(legacy).getByRole("button", { name: "시험 표시" }));
    // 진술 문장을 보여 주고 동의를 받는다
    expect(within(legacy).getByText(/소유·승인된 일회용 시험 계정이라는 진술/)).toBeInTheDocument();
    fireEvent.click(within(legacy).getByRole("button", { name: "동의하고 표시" }));
    expect(api).toHaveBeenCalledWith(
      "/api/product/targets/t1/accounts/legacy/mark-test",
      expect.objectContaining({ method: "POST", body: JSON.stringify({ statement_confirmed: true }) }),
    );
  });

  // 서버는 mark-test 때 자기 진술 문장의 해시를 동의 기록으로 남긴다. 화면이 그 문장을 못 받았으면 대체 문구로 동의를
  // 받지 않는다(fail-closed, #538 윤재상 리뷰).
  it.each([
    ["없음", OMIT],
    ["빈 문자열", ""],
    ["제어 문자", "진술\u0000문장"],
    ["문자열 아님", 7],
  ])("진술이 %s 이면 시험 표시 동의 버튼이 막히고 mark-test 를 보내지 않는다", async (_name, statement) => {
    serve(statement);
    render(<AccountClassifyLive />);
    await pickTarget();
    const legacy = within(screen.getByLabelText("계정 목록")).getByText("legacy").closest("li") as HTMLElement;
    fireEvent.click(within(legacy).getByRole("button", { name: "시험 표시" }));
    expect(within(legacy).getByText(/진술을 받지 못해 시험 표시할 수 없습니다/)).toBeInTheDocument();
    const agree = within(legacy).getByRole("button", { name: "동의하고 표시" });
    expect(agree).toBeDisabled();
    fireEvent.click(agree);
    expect(api).not.toHaveBeenCalledWith(expect.stringContaining("/mark-test"), expect.anything());
  });

  it("표시 해제: POST unmark", async () => {
    serve();
    render(<AccountClassifyLive />);
    await pickTarget();
    const owner = within(screen.getByLabelText("계정 목록")).getByText("owner").closest("li") as HTMLElement;
    fireEvent.click(within(owner).getByRole("button", { name: "표시 해제" }));
    expect(api).toHaveBeenCalledWith(
      "/api/product/targets/t1/accounts/owner/unmark",
      expect.objectContaining({ method: "POST", body: "{}" }),
    );
  });

  it("승인된 대상이 없으면 안내만 보이고 계정 목록은 없다", async () => {
    api.mockImplementation((path: string) =>
      path === "/api/product/targets"
        ? (Promise.resolve({ targets: [] }) as never)
        : (Promise.reject(new Error("x")) as never),
    );
    render(<AccountClassifyLive />);
    expect(await screen.findByText(/승인된 대상이 없습니다/)).toBeInTheDocument();
    expect(screen.queryByLabelText("계정 목록")).toBeNull();
  });
});
