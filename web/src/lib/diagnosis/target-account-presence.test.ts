import { beforeEach, expect, it, vi } from "vitest";

import { apiFetch } from "./api";
import {
  createURLDraft,
  parseURLJob,
  readURLHandle,
  sameURLInput,
  storeURLHandle,
  urlDiagnosisInput,
} from "./diagnosis-url";
import { draft, envelope, handle, owner } from "./diagnosis-url-fixtures";
import { readTargetAccountPresence } from "./target-account-presence";

vi.mock("./api", async (original) => ({ ...(await original<typeof import("./api")>()), apiFetch: vi.fn() }));
beforeEach(() => {
  vi.resetAllMocks();
  sessionStorage.clear();
  localStorage.clear();
});
// 외부 전송 동의는 설치 단위라(2026-11-26) 새 입력은 회차별 동의(external_test_accounts)를 만들지 않는다. 이미 저장된
// 회차 입력의 true 는 그대로 읽고, 다른 입력으로 본다(다른 초안의 응답을 이 초안으로 받지 않음).
it("새 입력은 회차별 동의를 만들지 않고, 저장된 true 는 그대로 읽는다", async () => {
  const plain = urlDiagnosisInput("https://target.example/", "profile-1", "");
  expect(plain).not.toHaveProperty("external_test_accounts");
  const stored = { ...plain, external_test_accounts: true as const };
  expect(sameURLInput(plain, stored)).toBe(false);
  vi.mocked(apiFetch).mockResolvedValue(envelope({ ...draft, input: plain }));
  expect((await createURLDraft(plain, "plain-draft", owner)).input).toEqual(plain);
  expect(JSON.parse(String(vi.mocked(apiFetch).mock.calls[0][1]?.body)).input).not.toHaveProperty(
    "external_test_accounts",
  );
  expect(
    parseURLJob(envelope({ ...draft, input: { ...draft.input, external_test_accounts: true } }), owner).input,
  ).toMatchObject({ external_test_accounts: true });
});
it.each([false, "true", 1, 0, null])("%s 값으로 외부 전송 동의를 만들지 않는다", (value) => {
  expect(() =>
    parseURLJob(envelope({ ...draft, input: { ...draft.input, external_test_accounts: value } }), owner),
  ).toThrow();
});
it("동의는 메모리용 핸들에만 두고 브라우저 저장소로 복원하지 않는다", () => {
  storeURLHandle(handle);
  expect(readURLHandle(owner)).not.toBeNull();
  const consented = { ...handle, input: { ...handle.input, external_test_accounts: true as const } };
  storeURLHandle(consented);
  expect(readURLHandle(owner)).toBeNull();
  expect(sessionStorage.length).toBe(0);
  expect(localStorage.length).toBe(0);
  sessionStorage.setItem("abc2lab.urlDiagnosisHandle.v2", JSON.stringify(consented));
  expect(readURLHandle(owner)).toBeNull();
});
it("시작을 보낸 동의 회차는 새로고침 뒤에도 이어 보도록 저장하고, 시작 전 동의는 여전히 남기지 않는다", () => {
  const consented = { ...handle, input: { ...handle.input, external_test_accounts: true as const } };
  // 시작 키가 있으면 같은 진단을 다시 시작할 수 없어 동의가 다시 쓰이지 않는다 → 이어 보기용으로 저장
  const started = { ...consented, startKey: "start-1", startRevision: handle.revision };
  storeURLHandle(started);
  const restored = readURLHandle(owner);
  if (!restored) throw new Error("시작한 동의 회차의 핸들이 저장소에 없다");
  expect(restored.startKey).toBe("start-1");
  expect(restored.input.external_test_accounts).toBe(true);
  expect(localStorage.length).toBe(0);
  // 시작 전(시작 키 없음) 동의 핸들로 바꾸면 저장소에서 지운다
  storeURLHandle(consented);
  expect(readURLHandle(owner)).toBeNull();
  expect(sessionStorage.length).toBe(0);
});
it("정확한 승인 대상의 자격이 저장된 등록 계정만 센다", async () => {
  vi.mocked(apiFetch)
    .mockResolvedValueOnce({ target: { id: "target-1", origin: "https://target.example", state: "approved" } })
    .mockResolvedValueOnce({ accounts: [{ label: "tester", credential_configured: true }] });
  expect(await readTargetAccountPresence("https://target.example/app")).toEqual({
    hasTestAccounts: true,
    loginMethodMissing: false,
    targetId: "target-1",
  });
  expect(vi.mocked(apiFetch).mock.calls[1][0]).toBe("/api/product/targets/target-1/accounts");
});
// #914: 자격은 저장됐는데 대상 로그인 방식이 없으면 서버가 login_method_missing: true 를 준다. 자격 있음일 때만 본다.
// 계정 분류 폐지(2026-11-26) 뒤라 서버가 아직 싣는 classification 은 보지 않는다(real 이어도 결속되므로 같이 센다)
it.each([
  [{ label: "tester", credential_configured: true, login_method_missing: true }, true],
  [{ label: "tester", credential_configured: false, login_method_missing: true }, false],
  [{ label: "tester", credential_configured: true }, false],
  [{ label: "customer", classification: "real", credential_configured: true, login_method_missing: true }, true],
  [{ label: "tester", credential_configured: true, login_method_missing: "true" }, false],
] as const)("로그인 방식 없음은 자격 있음 · 참일 때만(%#)", async (account, missing) => {
  vi.mocked(apiFetch)
    .mockResolvedValueOnce({ target: { id: "target-1", origin: "https://target.example", state: "approved" } })
    .mockResolvedValueOnce({ accounts: [account] });
  expect((await readTargetAccountPresence("https://target.example/")).loginMethodMissing).toBe(missing);
});
it.each([
  {},
  { accounts: [] },
  { accounts: [{ label: "tester" }] },
  { accounts: [{ label: "tester", classification: "test", credential_configured: false }] },
  { accounts: [{ label: "tester", credential_configured: "true" }] },
])("자격이 없는 계정에서 시험 계정을 추정하지 않는다(%#)", async (result) => {
  vi.mocked(apiFetch)
    .mockResolvedValueOnce({ target: { id: "target-1", origin: "https://target.example", state: "approved" } })
    .mockResolvedValueOnce(result);
  expect(await readTargetAccountPresence("https://target.example/")).toEqual({
    hasTestAccounts: false,
    loginMethodMissing: false,
    targetId: "target-1",
  });
});
it("계정 조회 실패·다른 origin은 계정 없음으로 바꾸지 않는다", async () => {
  vi.mocked(apiFetch)
    .mockResolvedValueOnce({ target: { id: "target-1", origin: "https://target.example", state: "approved" } })
    .mockRejectedValueOnce(new Error("fixed-error"));
  await expect(readTargetAccountPresence("https://target.example/")).rejects.toThrow();
  vi.mocked(apiFetch).mockResolvedValueOnce({
    target: { id: "target-2", origin: "https://other.example", state: "approved" },
  });
  await expect(readTargetAccountPresence("https://target.example/")).rejects.toThrow();
});
it.each([
  { target: { id: "target-1", origin: "https://other.example", state: "approved" } },
  { target: { id: "target-1", origin: "https://target.example", state: "pending" } },
  { target: { id: "target-1", origin: "https://target.example", state: "revoked" } },
])("정상 계정 응답이 뒤에 있어도 다른 origin/미승인 대상은 거절한다", async (resolved) => {
  vi.mocked(apiFetch)
    .mockResolvedValueOnce(resolved)
    .mockResolvedValueOnce({ accounts: [{ label: "tester", credential_configured: true }] });
  await expect(readTargetAccountPresence("https://target.example/")).rejects.toThrow();
  expect(apiFetch).toHaveBeenCalledTimes(1);
});
it("계정 분류 칸은 보지 않는다 — 어떤 값이든 · 없어도 자격 등록 여부로만 센다", async () => {
  for (const classification of ["real", "future", undefined]) {
    vi.mocked(apiFetch)
      .mockResolvedValueOnce({ target: { id: "target-1", origin: "https://target.example", state: "approved" } })
      .mockResolvedValueOnce({ accounts: [{ label: "tester", classification, credential_configured: true }] });
    expect((await readTargetAccountPresence("https://target.example/")).hasTestAccounts, String(classification)).toBe(
      true,
    );
  }
});
it("라벨 형식 밖 계정이 있으면 응답 전체를 믿지 않는다", async () => {
  vi.mocked(apiFetch)
    .mockResolvedValueOnce({ target: { id: "target-1", origin: "https://target.example", state: "approved" } })
    .mockResolvedValueOnce({ accounts: [{ label: "Tester@x", credential_configured: true }] });
  await expect(readTargetAccountPresence("https://target.example/")).rejects.toThrow();
});
