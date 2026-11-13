import { beforeEach, expect, it, vi } from "vitest";
import { apiFetch } from "./api";
import {
  createURLDraft,
  parseURLJob,
  sameURLInput,
  storeURLHandle,
  readURLHandle,
  urlDiagnosisInput,
} from "./diagnosis-url";
import { draft, envelope, handle, owner } from "./diagnosis-url-fixtures";
import { readExternalTestAccountPresence } from "./external-account-consent";
vi.mock("./api", async (original) => ({ ...(await original<typeof import("./api")>()), apiFetch: vi.fn() }));
beforeEach(() => {
  vi.resetAllMocks();
  sessionStorage.clear();
  localStorage.clear();
});
it("미선택은 생략하고 명시적 true만 정본 입력과 서버 응답으로 받는다", async () => {
  const plain = urlDiagnosisInput("https://target.example/", "profile-1", "");
  const consent = urlDiagnosisInput("https://target.example/", "profile-1", "", undefined, "", true);
  expect(plain).not.toHaveProperty("external_test_accounts");
  expect(consent.external_test_accounts).toBe(true);
  expect(sameURLInput(plain, consent)).toBe(false);
  vi.mocked(apiFetch).mockResolvedValue(envelope({ ...draft, input: consent }));
  expect((await createURLDraft(consent, "consented-draft", owner)).input).toEqual(consent);
  expect(JSON.parse(String(vi.mocked(apiFetch).mock.calls[0][1]?.body)).input).toEqual(consent);
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
it("정확한 승인 대상의 실제 시험 계정 등록만 조회한다", async () => {
  vi.mocked(apiFetch)
    .mockResolvedValueOnce({ target: { id: "target-1", origin: "https://target.example", state: "approved" } })
    .mockResolvedValueOnce({ accounts: [{ label: "tester", classification: "test" }] });
  expect(await readExternalTestAccountPresence("https://target.example/app")).toBe(true);
  expect(vi.mocked(apiFetch).mock.calls[1][0]).toBe("/api/product/targets/target-1/accounts");
});
it.each([
  {},
  { accounts: [] },
  { accounts: [{ label: "customer", classification: "real" }] },
])("미등록·실계정에서 시험 계정을 추정하지 않는다", async (result) => {
  vi.mocked(apiFetch)
    .mockResolvedValueOnce({ target: { id: "target-1", origin: "https://target.example", state: "approved" } })
    .mockResolvedValueOnce(result);
  expect(await readExternalTestAccountPresence("https://target.example/")).toBe(false);
});
it("계정 조회 실패·다른 origin은 계정 없음으로 바꾸지 않는다", async () => {
  vi.mocked(apiFetch)
    .mockResolvedValueOnce({ target: { id: "target-1", origin: "https://target.example", state: "approved" } })
    .mockRejectedValueOnce(new Error("fixed-error"));
  await expect(readExternalTestAccountPresence("https://target.example/")).rejects.toThrow();
  vi.mocked(apiFetch).mockResolvedValueOnce({
    target: { id: "target-2", origin: "https://other.example", state: "approved" },
  });
  await expect(readExternalTestAccountPresence("https://target.example/")).rejects.toThrow();
});
it("서버가 동의를 거절하면 성공 초안으로 바꾸지 않는다", async () => {
  vi.mocked(apiFetch).mockRejectedValue(new Error("external_consent_required"));
  await expect(
    createURLDraft({ ...draft.input, external_test_accounts: true }, "consent-denied", owner),
  ).rejects.toThrow();
});
