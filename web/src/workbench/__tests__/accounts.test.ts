import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/diagnosis/api", () => ({ apiFetch: vi.fn() }));

import { apiFetch } from "@/lib/diagnosis/api";

import {
  getAccounts,
  getProductTargets,
  isValidAccountLabel,
  type ProductTargetRef,
  registerAccount,
} from "../live/accounts";

const api = vi.mocked(apiFetch);
afterEach(() => api.mockReset());

const target: ProductTargetRef = { id: "t1", origin: "https://a.example", state: "approved" };

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

describe("getAccounts (실 GET · 매핑)", () => {
  it("대상이 없으면 빈 목록(실 호출 안 함)", async () => {
    const r = await getAccounts(null);
    expect(r).toEqual({ accounts: [] });
    expect(api).not.toHaveBeenCalled();
  });

  // 계정 분류 폐지(사용자 결정 2026-11-26): 서버가 아직 싣는 classification · 표시자 · 사유 · account_statement 는 읽지 않는다
  it("라벨 · 자격 등록 여부만 읽고 origin 은 대상 것, 형식 밖 라벨은 버린다", async () => {
    api.mockResolvedValueOnce({
      accounts: [
        {
          label: "owner",
          classification: "test",
          classified_by: "op-1",
          classified_at: "2026-11-10T00:00:00Z",
          registered_at: "2026-11-09T01:02:03.5Z",
        },
        { label: "peer", classification: "real", reason: "target_reapproved" },
        { label: "guest", classification: "real", credential_configured: true },
        { label: "odd", credential_configured: "true", registered_at: "어제" }, // 참(boolean)이 아니면 미등록, 시각 형식 밖은 null
        { label: "User@x", credential_configured: true }, // 라벨 형식 밖 → 버림
        { credential_configured: true }, // 라벨 없음 → 버림
      ],
      account_statement: "소유·승인된 일회용 시험 계정입니다.",
    });
    const r = await getAccounts(target);
    expect(api).toHaveBeenCalledWith(
      "/api/product/targets/t1/accounts",
      expect.objectContaining({ cache: "no-store" }),
    );
    expect(r).toEqual({
      accounts: [
        {
          ref: "owner",
          origin: "https://a.example",
          credentialConfigured: false,
          registeredAt: Date.parse("2026-11-09T01:02:03.5Z"),
        },
        { ref: "peer", origin: "https://a.example", credentialConfigured: false, registeredAt: null },
        // 자격 등록 여부는 credential_configured 칸으로만 나른다(값 없음). 분류가 real 이어도 같다
        { ref: "guest", origin: "https://a.example", credentialConfigured: true, registeredAt: null },
        { ref: "odd", origin: "https://a.example", credentialConfigured: false, registeredAt: null },
      ],
    });
  });

  it("accounts 모양이 틀리면 빈 목록", async () => {
    api.mockResolvedValueOnce({ accounts: "nope" });
    expect((await getAccounts(target)).accounts).toEqual([]);
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

describe("등록 쓰기 — 자격 값 없이 label만", () => {
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
});
