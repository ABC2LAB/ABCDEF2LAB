import { beforeEach, expect, it, vi } from "vitest";

import { apiFetch } from "./api";
import {
  EXTERNAL_TRANSFER_CONSENT_PATH,
  parseExternalTransferConsent,
  readExternalTransferConsent,
  recordExternalTransferConsent,
} from "./external-transfer-consent";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

vi.mock("./api", async (original) => ({ ...(await original<typeof import("./api")>()), apiFetch: vi.fn() }));
beforeEach(() => vi.resetAllMocks());

const statement = "외부 모델로 대상 응답을 보냅니다.";
const consented = {
  required: true,
  statement,
  consented: true,
  source: "install",
  consented_by: "ARTEX",
  consented_at: "2026-11-26T09:00:00.123456789Z",
};

// 서버 validProductExternalConsent 와 같은 규칙으로 닫는다 — 서버가 만들 수 있는 모양은 받고, 만들 수 없는 모양은 거부한다.
it.each([
  [{ required: false, consented: false }, { required: false }],
  [
    { required: true, statement, consented: false },
    { required: true, statement, consented: false },
  ],
  [
    consented,
    {
      required: true,
      statement,
      consented: true,
      source: "install",
      consentedBy: "ARTEX",
      consentedAt: consented.consented_at,
    },
  ],
  [{ ...consented, source: "admin" }, expect.objectContaining({ source: "admin" })],
])("서버가 만드는 동의 상태를 읽는다 (%#)", (c, want) => {
  expect(parseExternalTransferConsent({ external_consent: c })).toEqual(want);
});

it.each([
  ["다른 최상위 칸", { external_consent: { required: false, consented: false }, accounts: [] }],
  ["동의 칸 없음", {}],
  ["모르는 칸", { external_consent: { required: false, consented: false, revoked: true } }],
  ["필요 없음 + 문장", { external_consent: { required: false, consented: false, statement } }],
  ["필요 없음 + 동의", { external_consent: { required: false, consented: true } }],
  ["필요 + 문장 없음", { external_consent: { required: true, consented: false } }],
  ["빈 문장", { external_consent: { required: true, statement: "", consented: false } }],
  ["제어 문자", { external_consent: { required: true, statement: "문장\u0007", consented: false } }],
  ["C1 제어 문자", { external_consent: { required: true, statement: "문장\u0085", consented: false } }],
  ["긴 문장", { external_consent: { required: true, statement: "가".repeat(683), consented: false } }],
  ["미동의 + 출처", { external_consent: { required: true, statement, consented: false, source: "admin" } }],
  [
    "미동의 + 시각",
    { external_consent: { required: true, statement, consented: false, consented_at: consented.consented_at } },
  ],
  ["모르는 출처", { external_consent: { ...consented, source: "runner" } }],
  ["주체 형식", { external_consent: { ...consented, consented_by: "a b" } }],
  ["주체 없음", { external_consent: { ...consented, consented_by: undefined } }],
  ["시각 형식", { external_consent: { ...consented, consented_at: "어제" } }],
  ["0 시각", { external_consent: { ...consented, consented_at: "0001-01-01T00:00:00Z" } }],
  ["참이 아닌 값", { external_consent: { required: "true", consented: false } }],
])("서버가 만들 수 없는 모양은 거부한다: %s", (_, body) => {
  expect(() => parseExternalTransferConsent(body)).toThrow();
});

it("2048바이트 문장은 받는다(서버 상한과 같음)", () => {
  // 한글 한 글자 3바이트 × 682 = 2046바이트 + 'ab'
  const edge = `${"가".repeat(682)}ab`;
  expect(new TextEncoder().encode(edge).length).toBe(2048);
  expect(
    parseExternalTransferConsent({ external_consent: { required: true, statement: edge, consented: false } }),
  ).toEqual({ required: true, statement: edge, consented: false });
});

it("조회 · 동의는 정해진 경로 하나만 쓰고, 동의 응답이 동의가 아니면 성공으로 보지 않는다", async () => {
  vi.mocked(apiFetch).mockResolvedValueOnce({ external_consent: { required: true, statement, consented: false } });
  await readExternalTransferConsent();
  expect(vi.mocked(apiFetch).mock.calls[0][0]).toBe(EXTERNAL_TRANSFER_CONSENT_PATH);
  vi.mocked(apiFetch).mockResolvedValueOnce({ external_consent: consented });
  expect((await recordExternalTransferConsent()).required).toBe(true);
  const [path, init] = vi.mocked(apiFetch).mock.calls[1];
  expect(path).toBe(EXTERNAL_TRANSFER_CONSENT_PATH);
  expect(init?.method).toBe("POST");
  expect(JSON.parse(String(init?.body))).toEqual({ statement_confirmed: true });
  vi.mocked(apiFetch).mockResolvedValueOnce({ external_consent: { required: true, statement, consented: false } });
  await expect(recordExternalTransferConsent()).rejects.toThrow();
});

it("서버 경로 · 출처 값과 화면이 같다", () => {
  const route = readFileSync(resolve(process.cwd(), "../server/product_http.go"), "utf8");
  expect(route).toContain(`const productExternalConsentPath = "${EXTERNAL_TRANSFER_CONSENT_PATH}"`);
  const consent = readFileSync(
    resolve(process.cwd(), "../cmd/first-controlled-diagnosis/product_external_consent.go"),
    "utf8",
  );
  expect(consent).toMatch(/productExternalConsentInstall\s+= "install"/);
  expect(consent).toMatch(/productExternalConsentAdmin\s+= "admin"/);
});
