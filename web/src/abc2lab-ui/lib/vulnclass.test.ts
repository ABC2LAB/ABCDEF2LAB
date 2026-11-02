import { describe, expect, it } from "vitest";

import { canonicalVulnClass } from "./vulnclass";

// 실 35B 런(artexrun) findings 의 vulnclass 45건=35종을 그대로 고정한다.
// 표시용 계열 분류기가 이 변형들을 7계열로 모으는지 회귀로 잡는다.
const CASES: [string, string][] = [
  // 접근통제 · IDOR
  ["IDOR", "access"],
  ["IDOR / Insecure Direct Object Reference", "access"],
  ["Insecure Direct Object Reference", "access"],
  ["BOLA/IDOR", "access"],
  ["CWE-639", "access"],
  ["Broken Access Control", "access"],
  ["A01:2021-Broken Access Control", "access"],
  // 인젝션(SQL·명령)
  ["SQL Injection", "injection"],
  ["SQL Injection (CWE-89)", "injection"],
  ["Command Injection (CWE-78)", "injection"],
  ["CWE-78 (OS Command Injection)", "injection"],
  // XSS
  ["XSS", "xss"],
  ["XSS-Reflected", "xss"],
  ["XSS-Stored", "xss"],
  ["Stored XSS", "xss"],
  // 인증 · 세션
  ["Broken Authentication", "auth"],
  ["Authentication Bypass", "auth"],
  ["A07:2021-Identify and Authentication Failures", "auth"],
  ["A07: Using Broken User Session Management", "auth"],
  ["Session Fixation", "auth"],
  ["2FA Bypass", "auth"],
  ["Brute Force", "auth"],
  ["Weak Password", "auth"],
  ["JWT weak secret", "auth"],
  ["JWT Algorithm Confusion / Authentication Bypass", "auth"],
  ["JWT alg:none", "auth"],
  ["JWT sensitive data exposure", "auth"], // 우선순위: infodisc 보다 auth(jwt) 가 먼저
  // 비즈니스 로직
  ["Business Logic - Negative Quantity", "bizlogic"],
  ["Business Logic - Price Manipulation", "bizlogic"],
  ["Business Logic - Payment Bypass", "bizlogic"],
  ["business_logic", "bizlogic"],
  // 정보 노출
  ["Information Disclosure", "infodisc"],
  ["Sensitive Data Exposure", "infodisc"],
  ["CWE-200: Information Exposure", "infodisc"],
  // 기타
  ["A05:2021 Security Misconfiguration", "other"],
  ["LFI/RFI", "other"],
];

describe("canonicalVulnClass", () => {
  it.each(CASES)("%s → %s", (raw, key) => {
    expect(canonicalVulnClass(raw).key).toBe(key);
  });

  it("빈 값·null·undefined 는 기타", () => {
    expect(canonicalVulnClass("").key).toBe("other");
    expect(canonicalVulnClass("   ").key).toBe("other");
    expect(canonicalVulnClass(null).key).toBe("other");
    expect(canonicalVulnClass(undefined).key).toBe("other");
  });

  it("label 은 계열 표시명을 돌려준다", () => {
    expect(canonicalVulnClass("IDOR").label).toBe("접근통제 · IDOR");
    expect(canonicalVulnClass("nope-unknown").label).toBe("기타");
  });
});
