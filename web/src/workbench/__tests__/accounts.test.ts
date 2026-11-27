import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/diagnosis/api", () => ({ apiFetch: vi.fn() }));

import { apiFetch } from "@/lib/diagnosis/api";

import {
  getAccounts,
  getProductTargets,
  isValidAccountLabel,
  type ProductTargetRef,
  parseLoginMethod,
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
          loginMethod: null,
        },
        {
          ref: "peer",
          origin: "https://a.example",
          credentialConfigured: false,
          registeredAt: null,
          loginMethod: null,
        },
        // 자격 등록 여부는 credential_configured 칸으로만 나른다(값 없음). 분류가 real 이어도 같다
        {
          ref: "guest",
          origin: "https://a.example",
          credentialConfigured: true,
          registeredAt: null,
          loginMethod: null,
        },
        { ref: "odd", origin: "https://a.example", credentialConfigured: false, registeredAt: null, loginMethod: null },
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

// 배운 로그인 방법(서버 login_method, 계약 model-login-attempts §6-1-3). 닫힌 칸 · 값 목록이고 모르는 값은 받지 않는다.
describe("login_method 파서", () => {
  const method = {
    http_method: "POST",
    path: "/login",
    format: "form",
    identifier_field: "email",
    password_field: "password",
    other_fields: ["_csrf"],
    fields_from_response: ["_csrf"],
    session_source: "set_cookie",
    session_names: ["connect.sid"],
    session_sent_as: "cookie",
  };
  const learned = {
    at: "2026-11-26T12:00:00Z",
    run_ref: "run-1",
    stage: "diagnosis",
    attempt_ref: "diagnosis-3",
    evidence: "observed_contrast",
  };
  const verified = {
    at: "2026-11-26T12:30:00Z",
    run_ref: "run-1",
    attempt_ref: "confirmation-1",
    evidence: "e3_relogin",
  };

  it("계약 예시(verified)를 그대로 읽는다", () => {
    expect(parseLoginMethod({ state: "verified", learned, verified, method })).toEqual({
      state: "verified",
      clearedReason: null,
      learned: {
        at: Date.parse(learned.at),
        runRef: "run-1",
        stage: "diagnosis",
        attemptRef: "diagnosis-3",
        evidence: "observed_contrast",
      },
      verified: { at: Date.parse(verified.at), runRef: "run-1", attemptRef: "confirmation-1", evidence: "e3_relogin" },
      method: {
        httpMethod: "POST",
        path: "/login",
        format: "form",
        identifierField: "email",
        passwordField: "password",
        otherFields: ["_csrf"],
        fieldsFromResponse: ["_csrf"],
        sessionSource: "set_cookie",
        sessionNames: ["connect.sid"],
        sessionSentAs: "cookie",
      },
    });
  });

  it("none · learned · 칸 없음", () => {
    expect(parseLoginMethod({ state: "none" })).toEqual({
      state: "none",
      clearedReason: null,
      learned: null,
      verified: null,
      method: null,
    });
    expect(parseLoginMethod({ state: "none", cleared_reason: "credential_changed" })).toMatchObject({
      clearedReason: "credential_changed",
    });
    expect(parseLoginMethod({ state: "learned", learned, method })).toMatchObject({ state: "learned", verified: null });
    // 서버가 이 칸을 싣기 전 응답 → 칸 없음(화면은 '아직 없음')
    expect(parseLoginMethod(undefined)).toBeNull();
  });

  it("모르는 값 · 닫힌 칸 밖 · 상태와 안 맞는 칸 묶음은 받지 않는다", () => {
    const bad: unknown[] = [
      null,
      "verified",
      { state: "maybe" },
      { state: "maybe", learned, method },
      { state: "none", cleared_reason: "other" },
      { state: "learned", cleared_reason: "target_reapproved", learned, method },
      { state: "none", learned },
      { state: "learned", method },
      { state: "learned", learned },
      { state: "learned", learned: { ...learned, evidence: "guess" }, method },
      { state: "learned", learned: { ...learned, stage: "confirmation" }, method },
      { state: "learned", learned: { ...learned, at: "어제" }, method },
      { state: "learned", learned, method, verified },
      { state: "verified", learned, method },
      { state: "verified", learned, method, verified: { ...verified, evidence: "observed_contrast" } },
      { state: "learned", learned, method: { ...method, format: "xml" } },
      { state: "learned", learned, method: { ...method, session_source: "query" } },
      { state: "learned", learned, method: { ...method, other_fields: "x" } },
      { state: "learned", learned, method: { ...method, password: "seeded" } },
      { state: "learned", learned: { ...learned, value: "x" }, method },
      { state: "none", extra: true },
      // 서버가 만들 수 없는 값(#996 경준 리뷰 P3): POST 밖 메서드 · 날짜만 있는 시각 · 모양 밖 세션 전달 방식
      { state: "learned", learned, method: { ...method, http_method: "GET" } },
      { state: "learned", learned: { ...learned, at: "2026-11-27" }, method },
      { state: "verified", learned, method, verified: { ...verified, at: "2026-11-27" } },
      { state: "learned", learned, method: { ...method, session_sent_as: "somewhere" } },
      { state: "learned", learned, method: { ...method, identifier_field: 7 } },
    ];
    for (const v of bad) expect(parseLoginMethod(v)).toBe("invalid");
  });

  it("서버가 정상으로 내는 값은 받는다 — 빈 아이디 칸(비밀번호만 보내는 로그인) · 헤더 세션 전달 · 빈 세션 전달 방식(#996 경준 리뷰 P2 · P3)", () => {
    expect(parseLoginMethod({ state: "learned", learned, method: { ...method, identifier_field: "" } })).toMatchObject({
      method: { identifierField: "" },
    });
    const header = {
      ...method,
      format: "json",
      other_fields: [],
      fields_from_response: [],
      session_source: "json_field",
      session_names: ["token"],
      session_sent_as: "Authorization:Bearer ",
    };
    expect(parseLoginMethod({ state: "learned", learned, method: header })).toMatchObject({
      method: { sessionSentAs: "Authorization:Bearer " },
    });
    // 세션 전달 방식이 아직 없으면 빈 값이다(계약 §3 · §6-1-2, #996 경준 리뷰 P3)
    expect(parseLoginMethod({ state: "learned", learned, method: { ...method, session_sent_as: "" } })).toMatchObject({
      method: { sessionSentAs: "" },
    });
  });

  it("getAccounts 가 계정마다 login_method 를 싣는다(형식 밖이면 invalid, 계정 행은 그대로)", async () => {
    api.mockResolvedValueOnce({
      accounts: [
        { label: "owner", credential_configured: true, login_method: { state: "learned", learned, method } },
        { label: "peer", login_method: { state: "learned" } },
      ],
    });
    const r = await getAccounts(target);
    expect(
      r.accounts.map((a) => [a.ref, typeof a.loginMethod === "object" ? a.loginMethod?.state : a.loginMethod]),
    ).toEqual([
      ["owner", "learned"],
      ["peer", "invalid"],
    ]);
  });
});
