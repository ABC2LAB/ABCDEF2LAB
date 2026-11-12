import { describe, expect, it } from "vitest";

import {
  type AccountClass,
  type ClassifiedAccount,
  classLabel,
  getAccounts,
  normalizeClass,
  summarizeAccounts,
  usableAsTestAccount,
} from "../live/accounts";

const acct = (cls: AccountClass): ClassifiedAccount => ({ ref: `r-${cls}`, origin: "https://t.example", cls });

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

describe("summarizeAccounts", () => {
  it("상태별로 세고 usable 은 승인 시험만(미등록 cls 는 실계정으로)", () => {
    const weird = { ref: "x", origin: "o", cls: "weird" } as unknown as ClassifiedAccount;
    const s = summarizeAccounts([acct("test_approved"), acct("test_pending"), acct("unmarked"), weird]);
    expect(s).toMatchObject({ total: 4, testApproved: 1, testPending: 1, unmarked: 2, usable: 1 });
  });
});

describe("getAccounts", () => {
  it("지금은 목업을 준다(저장 위치 미정)", async () => {
    const r = await getAccounts(null);
    expect(r.mock).toBe(true);
    expect(r.accounts.length).toBeGreaterThan(0);
  });
});
