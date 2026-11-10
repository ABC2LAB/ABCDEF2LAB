import { beforeEach, describe, expect, it } from "vitest";

import { auth } from "./auth";

beforeEach(() => localStorage.clear());

describe("설치 관리자 표시 이름", () => {
  it("기존 승인 주체로 발급된 세션도 ABC2LAB 계정으로 표시한다", () => {
    localStorage.setItem("artex_token", `header.${btoa(JSON.stringify({ sub: "ARTEX" }))}.signature`);
    expect(auth.getCurrentUser()).toMatchObject({ username: "ABC2LAB", name: "ABC2LAB" });
  });

  it("다른 주체를 설치 관리자 이름으로 바꾸지 않는다", () => {
    localStorage.setItem("artex_token", `header.${btoa(JSON.stringify({ sub: "other" }))}.signature`);
    expect(auth.getCurrentUser()).toMatchObject({ username: "other", name: "other" });
  });
});
