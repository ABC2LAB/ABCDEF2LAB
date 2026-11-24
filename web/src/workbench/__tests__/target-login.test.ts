import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/diagnosis/api", () => ({ apiFetch: vi.fn() }));

import { apiFetch } from "@/lib/diagnosis/api";

import {
  accountWriteFailureText,
  getTargetLogin,
  parseCookieNames,
  setAccountCredential,
  setTargetLogin,
  type TargetLogin,
  validAccountCredential,
  validTargetLogin,
  WRITE_FAILURE_DEFAULT,
} from "../live/accounts";
import { LOGIN_CASES } from "./target-login-cases";

const api = vi.mocked(apiFetch);
afterEach(() => api.mockReset());

// 합성 로그인 방식(특정 대상 앱 값이 아님).
const header: TargetLogin = {
  path: "/session/new",
  format: "json",
  usernameField: "user_name",
  passwordField: "pass_word",
  sessionKind: "header",
  cookies: [],
  jsonField: "data.access",
  header: "X-Session",
  prefix: "Token ",
};
const cookies: TargetLogin = { ...header, format: "form", sessionKind: "cookies", cookies: ["sid", "csrf"] };

describe("validTargetLogin (서버 ValidTargetLogin 과 같은 규칙)", () => {
  // 같은 표를 Go diagnosisprep.ValidTargetLogin 에도 대어 결과가 같은지 확인했다(PR 본문).
  it.each(LOGIN_CASES)("$name → $valid", ({ login, valid }) => {
    expect(validTargetLogin(login)).toBe(valid);
  });

  it("쿠키 이름은 쉼표 · 공백으로 나눈다", () => {
    expect(parseCookieNames(" sid, csrf  x ,,")).toEqual(["sid", "csrf", "x"]);
  });
});

describe("getTargetLogin / setTargetLogin (실 GET · PUT)", () => {
  it("설정 전이면 null, 있으면 화면 모양으로 바꾼다", async () => {
    api.mockResolvedValueOnce({});
    expect(await getTargetLogin("t 1")).toBeNull();
    expect(api).toHaveBeenCalledWith(
      "/api/product/targets/t%201/login",
      expect.objectContaining({ cache: "no-store" }),
    );
    api.mockResolvedValueOnce({
      login: {
        method: "POST",
        path: "/session/new",
        format: "form",
        username_field: "u",
        password_field: "p",
        cookies: ["sid"],
      },
    });
    expect(await getTargetLogin("t1")).toEqual({
      path: "/session/new",
      format: "form",
      usernameField: "u",
      passwordField: "p",
      sessionKind: "cookies",
      cookies: ["sid"],
      jsonField: "",
      header: "",
      prefix: "",
    });
  });

  it("형식 밖 login 은 미설정으로 보이지 않고 예외다(fail-closed)", async () => {
    api.mockResolvedValueOnce({ login: { method: "GET", path: "/x", format: "json" } });
    await expect(getTargetLogin("t1")).rejects.toThrow("invalid_login_response");
  });

  it("헤더 방식은 쿠키 칸 없이, 쿠키 방식은 헤더 칸 없이 보낸다. 빈 앞붙임은 뺀다", async () => {
    api.mockResolvedValue({
      login: {
        method: "POST",
        path: "/session/new",
        format: "json",
        username_field: "user_name",
        password_field: "pass_word",
        json_field: "data.access",
        header: "X-Session",
      },
    });
    await setTargetLogin("t1", { ...header, prefix: "", cookies: ["ignored"] });
    expect(api).toHaveBeenLastCalledWith(
      "/api/product/targets/t1/login",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({
          method: "POST",
          path: "/session/new",
          format: "json",
          username_field: "user_name",
          password_field: "pass_word",
          json_field: "data.access",
          header: "X-Session",
        }),
      }),
    );
    api.mockResolvedValue({
      login: {
        method: "POST",
        path: "/session/new",
        format: "form",
        username_field: "user_name",
        password_field: "pass_word",
        cookies: ["sid", "csrf"],
      },
    });
    const saved = await setTargetLogin("t1", cookies);
    expect(JSON.parse(String(api.mock.lastCall?.[1]?.body))).toEqual({
      method: "POST",
      path: "/session/new",
      format: "form",
      username_field: "user_name",
      password_field: "pass_word",
      cookies: ["sid", "csrf"],
    });
    expect(saved.cookies).toEqual(["sid", "csrf"]);
  });

  it("형식 밖이면 보내지 않는다", async () => {
    await expect(setTargetLogin("t1", { ...header, path: "relative" })).rejects.toThrow("invalid_target_login");
    expect(api).not.toHaveBeenCalled();
  });
});

describe("setAccountCredential (실 PUT, 값은 본문에만)", () => {
  it("본문은 username·password 둘뿐이고, 돌려주는 것은 credential_configured 참 여부뿐이다", async () => {
    api.mockResolvedValueOnce({ account: { label: "acct-a", classification: "test", credential_configured: true } });
    await expect(setAccountCredential("t1", "acct-a", "synthetic-user", "synthetic-pass")).resolves.toBe(true);
    expect(api).toHaveBeenCalledWith(
      "/api/product/targets/t1/accounts/acct-a/credential",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({ username: "synthetic-user", password: "synthetic-pass" }),
      }),
    );
    api.mockResolvedValueOnce({ account: { label: "acct-a", classification: "test" } });
    await expect(setAccountCredential("t1", "acct-a", "synthetic-user", "synthetic-pass")).resolves.toBe(false);
  });

  it.each([
    ["라벨 형식 밖", "Acct@A", "u", "p", "invalid_account_label"],
    ["빈 비밀번호", "acct-a", "u", "", "invalid_account_credential"],
    ["빈 사용자명", "acct-a", "", "p", "invalid_account_credential"],
    ["사용자명 앞 공백", "acct-a", " u", "p", "invalid_account_credential"],
    ["사용자명 뒤 공백", "acct-a", "u ", "p", "invalid_account_credential"],
    ["사용자명 뒤 유니코드 공백", "acct-a", "u\u00a0", "p", "invalid_account_credential"],
    ["사용자명 앞 NEL 공백", "acct-a", "\u0085u", "p", "invalid_account_credential"],
    ["줄바꿈", "acct-a", "u", "p\nq", "invalid_account_credential"],
    ["비밀번호 513바이트", "acct-a", "u", "x".repeat(513), "invalid_account_credential"],
  ])("%s 이면 보내지 않는다", async (_name, label, username, password, code) => {
    await expect(setAccountCredential("t1", label, username, password)).rejects.toThrow(code);
    expect(api).not.toHaveBeenCalled();
  });

  it("사용자명 내부 공백과 비밀번호 앞뒤 공백을 그대로 보낸다", async () => {
    const username = "synthetic user";
    const password = " synthetic pass ";
    api.mockResolvedValueOnce({ account: { credential_configured: true } });
    await expect(setAccountCredential("t1", "acct-a", username, password)).resolves.toBe(true);
    expect(api).toHaveBeenCalledWith(
      "/api/product/targets/t1/accounts/acct-a/credential",
      expect.objectContaining({ body: JSON.stringify({ username, password }) }),
    );
  });

  it("자격 길이는 UTF-8 바이트로 센다(서버와 같게)", () => {
    expect(validAccountCredential("u", "가".repeat(170))).toBe(true); // 510바이트
    expect(validAccountCredential("u", "가".repeat(171))).toBe(false); // 513바이트
    expect(validAccountCredential("u".repeat(256), "p")).toBe(true);
    expect(validAccountCredential("u".repeat(257), "p")).toBe(false);
  });
});

describe("accountWriteFailureText (서버 사유 → 한국어)", () => {
  it.each([
    ["invalid_target_login", /승인 경로 밖/],
    ["invalid_account_credential", /사용자명은 앞뒤 공백 없이/],
    ["target_not_approved", /철회된 대상/],
    ["account_not_found", /계정을 찾지 못했습니다/],
    ["product_control_unavailable", /제품 제어 서비스/],
  ])("%s", (code, text) => {
    expect(accountWriteFailureText(new Error(code))).toMatch(text);
  });

  it("모르는 사유는 코드 모양일 때만 붙이고, 그 밖은 일반 문구다", () => {
    expect(accountWriteFailureText(new Error("brand_new_reason"))).toBe(
      `${WRITE_FAILURE_DEFAULT} (사유: brand_new_reason)`,
    );
    expect(accountWriteFailureText(new Error("Failed to fetch"))).toBe(WRITE_FAILURE_DEFAULT);
    expect(accountWriteFailureText("x")).toBe(WRITE_FAILURE_DEFAULT);
  });
});
