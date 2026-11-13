import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/diagnosis/api", () => ({ apiFetch: vi.fn() }));

import { apiFetch } from "@/lib/diagnosis/api";

import {
  type AccountClass,
  type ClassifiedAccount,
  classFromServer,
  classLabel,
  getAccounts,
  getProductTargets,
  isValidAccountLabel,
  markTestAccount,
  normalizeClass,
  type ProductTargetRef,
  registerAccount,
  summarizeAccounts,
  unmarkAccount,
  usableAsTestAccount,
} from "../live/accounts";

const api = vi.mocked(apiFetch);
afterEach(() => api.mockReset());

const acct = (cls: AccountClass): ClassifiedAccount => ({ ref: `r-${cls}`, origin: "https://t.example", cls });
const target: ProductTargetRef = { id: "t1", origin: "https://a.example", state: "approved" };

describe("usableAsTestAccount (§8 fail-closed)", () => {
  it("승인된 시험 표시만 쓸 수 있다", () => {
    expect(usableAsTestAccount("test_approved")).toBe(true);
    expect(usableAsTestAccount("test_pending")).toBe(false);
    expect(usableAsTestAccount("unmarked")).toBe(false);
  });
  it("미등록·판단 불가 값은 실계정(false)", () => {
    expect(usableAsTestAccount("")).toBe(false);
    expect(usableAsTestAccount("approved")).toBe(false);
    expect(usableAsTestAccount("constructor")).toBe(false);
  });
});

describe("normalizeClass / classLabel", () => {
  it("아는 값은 그대로, 밖은 unmarked(실계정 취급)", () => {
    expect(normalizeClass("test_approved")).toBe("test_approved");
    expect(normalizeClass("bogus")).toBe("unmarked");
    expect(classLabel("bogus")).toBe("미표시 (실계정 취급)");
  });
});

describe("classFromServer (§8 fail-closed)", () => {
  it("'test'만 승인된 시험, 그 밖은 전부 실계정", () => {
    expect(classFromServer("test")).toBe("test_approved");
    expect(classFromServer("real")).toBe("unmarked");
    expect(classFromServer("")).toBe("unmarked");
    expect(classFromServer("pending")).toBe("unmarked");
  });
});

describe("summarizeAccounts", () => {
  it("상태별로 세고 usable 은 승인 시험만(미등록 cls 는 실계정으로)", () => {
    const weird = { ref: "x", origin: "o", cls: "weird" } as unknown as ClassifiedAccount;
    const s = summarizeAccounts([acct("test_approved"), acct("test_pending"), acct("unmarked"), weird]);
    expect(s).toMatchObject({ total: 4, testApproved: 1, testPending: 1, unmarked: 2, usable: 1 });
  });
});

describe("getProductTargets", () => {
  it("id·origin 있는 대상만 받고 형식 깨진 건 버린다", async () => {
    api.mockResolvedValueOnce({
      targets: [
        { id: "t1", origin: "https://a", state: "approved" },
        { id: "t2", origin: "https://b" },
        { origin: "https://c" }, // id 없음 → 버림
        { id: "t4" }, // origin 없음 → 버림
        "nope", // 객체 아님 → 버림
      ],
    });
    expect(await getProductTargets()).toEqual([
      { id: "t1", origin: "https://a", state: "approved" },
      { id: "t2", origin: "https://b", state: "" },
    ]);
  });
  it("targets 가 없거나 모양이 틀리면 빈 목록", async () => {
    api.mockResolvedValueOnce({});
    expect(await getProductTargets()).toEqual([]);
  });
});

describe("getAccounts (실 GET · 매핑 · fail-closed)", () => {
  it("대상이 없으면 빈 목록(실 호출 안 함)", async () => {
    const r = await getAccounts(null);
    expect(r).toEqual({ accounts: [], statement: "", mock: false });
    expect(api).not.toHaveBeenCalled();
  });

  it("test→시험 계정, real→실계정 취급으로 매핑하고 origin 은 대상 것, 형식 밖 라벨은 버린다", async () => {
    api.mockResolvedValueOnce({
      accounts: [
        { label: "owner", classification: "test", classified_by: "op-1", classified_at: "2026-11-10T00:00:00Z" },
        { label: "peer", classification: "real", reason: "target_reapproved" },
        { label: "guest", classification: "real", credential_configured: true },
        { label: "User@x", classification: "test" }, // 라벨 형식 밖 → 버림
        { classification: "test" }, // 라벨 없음 → 버림
      ],
    });
    const r = await getAccounts(target);
    expect(api).toHaveBeenCalledWith(
      "/api/product/targets/t1/accounts",
      expect.objectContaining({ cache: "no-store" }),
    );
    expect(r.mock).toBe(false);
    expect(r.accounts).toEqual([
      {
        ref: "owner",
        origin: "https://a.example",
        cls: "test_approved",
        approvedBy: "op-1",
        approvedAt: "2026-11-10T00:00:00Z",
        note: undefined,
      },
      {
        ref: "peer",
        origin: "https://a.example",
        cls: "unmarked",
        approvedBy: undefined,
        approvedAt: undefined,
        note: "실계정 전환 사유: target_reapproved",
      },
      {
        ref: "guest",
        origin: "https://a.example",
        cls: "unmarked",
        approvedBy: undefined,
        approvedAt: undefined,
        note: "자격 설정됨(값 비표시)",
      },
    ]);
  });

  it("accounts 모양이 틀리면 빈 목록(fail-closed)", async () => {
    api.mockResolvedValueOnce({ accounts: "nope" });
    expect((await getAccounts(target)).accounts).toEqual([]);
  });
  it("account_statement 를 함께 준다(mark-test 동의 문장)", async () => {
    api.mockResolvedValueOnce({ accounts: [], account_statement: "소유·승인된 일회용 시험 계정입니다." });
    expect((await getAccounts(target)).statement).toBe("소유·승인된 일회용 시험 계정입니다.");
  });
});

describe("isValidAccountLabel", () => {
  it("소문자·숫자·_·- 만, 사용자명 원문 차단", () => {
    expect(isValidAccountLabel("owner")).toBe(true);
    expect(isValidAccountLabel("acct-1_x")).toBe(true);
    expect(isValidAccountLabel("User@x")).toBe(false);
    expect(isValidAccountLabel("")).toBe(false);
    expect(isValidAccountLabel("A")).toBe(false);
  });
});

describe("쓰기(등록·시험 표시·해제) — 자격 값 없이 label만", () => {
  it("registerAccount: POST accounts {label}, 형식 밖 라벨은 아예 보내지 않는다", async () => {
    api.mockResolvedValueOnce({});
    await registerAccount("t1", "owner");
    expect(api).toHaveBeenCalledWith(
      "/api/product/targets/t1/accounts",
      expect.objectContaining({ method: "POST", body: JSON.stringify({ label: "owner" }) }),
    );
    api.mockClear();
    await expect(registerAccount("t1", "User@x")).rejects.toThrow();
    expect(api).not.toHaveBeenCalled();
  });
  it("markTestAccount: POST mark-test {statement_confirmed:true}", async () => {
    api.mockResolvedValueOnce({});
    await markTestAccount("t1", "owner");
    expect(api).toHaveBeenCalledWith(
      "/api/product/targets/t1/accounts/owner/mark-test",
      expect.objectContaining({ method: "POST", body: JSON.stringify({ statement_confirmed: true }) }),
    );
  });
  it("unmarkAccount: POST unmark {}", async () => {
    api.mockResolvedValueOnce({});
    await unmarkAccount("t1", "owner");
    expect(api).toHaveBeenCalledWith(
      "/api/product/targets/t1/accounts/owner/unmark",
      expect.objectContaining({ method: "POST", body: "{}" }),
    );
  });
});
