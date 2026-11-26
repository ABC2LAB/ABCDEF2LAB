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

vi.mock("./api", async (original) => ({ ...(await original<typeof import("./api")>()), apiFetch: vi.fn() }));
beforeEach(() => {
  vi.resetAllMocks();
  sessionStorage.clear();
  localStorage.clear();
});

const both = { exclusionsReviewed: true, backupConfirmed: true };

it("시작 확인은 체크한 진술만 true 로 싣고, 체크하지 않은 진술은 칸을 만들지 않는다", async () => {
  const plain = urlDiagnosisInput("https://target.example/", "profile-1", "");
  const confirmed = urlDiagnosisInput("https://target.example/", "profile-1", "", undefined, "", both);
  expect(plain).not.toHaveProperty("exclusions_reviewed");
  expect(plain).not.toHaveProperty("backup_confirmed");
  expect(confirmed).toEqual({
    target_url: "https://target.example/",
    profile_id: "profile-1",
    exclusions_reviewed: true,
    backup_confirmed: true,
  });
  const onlyReviewed = urlDiagnosisInput("https://target.example/", "profile-1", "", undefined, "", {
    exclusionsReviewed: true,
    backupConfirmed: false,
  });
  expect(onlyReviewed.exclusions_reviewed).toBe(true);
  expect(onlyReviewed).not.toHaveProperty("backup_confirmed");
  // 서버로 가는 본문과 서버 응답 모두 같은 입력이다
  vi.mocked(apiFetch).mockResolvedValue(envelope({ ...draft, input: confirmed }));
  expect((await createURLDraft(confirmed, "confirmed-draft", owner)).input).toEqual(confirmed);
  expect(JSON.parse(String(vi.mocked(apiFetch).mock.calls[0][1]?.body)).input).toEqual(confirmed);
});

it("시작 확인 진술이 다르면 다른 입력이다 (다른 초안의 응답을 이 초안으로 받지 않는다)", () => {
  const plain = urlDiagnosisInput("https://target.example/", "profile-1", "");
  const confirmed = urlDiagnosisInput("https://target.example/", "profile-1", "", undefined, "", both);
  const onlyBackup = urlDiagnosisInput("https://target.example/", "profile-1", "", undefined, "", {
    backupConfirmed: true,
  });
  expect(sameURLInput(confirmed, confirmed)).toBe(true);
  expect(sameURLInput(plain, confirmed)).toBe(false);
  expect(sameURLInput(onlyBackup, confirmed)).toBe(false);
  expect(sameURLInput(plain, onlyBackup)).toBe(false);
});

it.each([
  "exclusions_reviewed",
  "backup_confirmed",
] as const)("서버 응답의 %s 는 true 일 때만 진술로 읽는다", (field) => {
  for (const value of [false, "true", 1, 0, null])
    expect(() => parseURLJob(envelope({ ...draft, input: { ...draft.input, [field]: value } }), owner)).toThrow();
  expect(parseURLJob(envelope({ ...draft, input: { ...draft.input, [field]: true } }), owner).input[field]).toBe(true);
});

it("진술이 없는 응답(이 칸이 생기기 전 초안 · CLI · 평가 러너)도 그대로 읽고, 진술을 지어내지 않는다", () => {
  const job = parseURLJob(envelope(draft), owner);
  expect(job.input).not.toHaveProperty("exclusions_reviewed");
  expect(job.input).not.toHaveProperty("backup_confirmed");
});

it("시작 확인이 든 조회 핸들은 새로고침 뒤에도 같은 입력으로 복원된다", () => {
  const confirmed = {
    ...handle,
    input: { ...handle.input, exclusions_reviewed: true as const, backup_confirmed: true as const },
  };
  storeURLHandle(confirmed);
  expect(readURLHandle(owner)?.input).toEqual(confirmed.input);
});
