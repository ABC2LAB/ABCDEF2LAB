import { describe, expect, it } from "vitest";

import { approvedPathOf, isExactPath, parseURLJob } from "./diagnosis-url";
import { envelope, owner, ready, scope } from "./diagnosis-url-fixtures";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

// 서버 계약(docs/contracts/exact-path-approval.md)의 응답 예시를 화면 파서가 그대로 받는지 본다.
// 예시는 레포의 계약 파일에서 읽으므로, 서버가 표현을 바꾸면 예시 갱신과 함께 이 시험이 먼저 깨진다.
const example = JSON.parse(
  readFileSync(resolve(process.cwd(), "../docs/contracts/exact-path-approval.status.example.json"), "utf8"),
);

describe("정확 일치 승인 경로(=/…) 계약", () => {
  it("서버 응답 예시의 정확 일치·접두 경로를 표현 그대로 보존한다", () => {
    const job = parseURLJob(example, example.owner_context);
    if (!job.scope) throw new Error("scope 없음");
    const paths = job.scope.path_prefixes;
    expect(paths).toEqual(["=/", "/users/v1", "=/a/"]);
    expect(paths.map(isExactPath)).toEqual([true, false, true]);
  });
  it("=를 떼서 접두로 바꾸지 않는다", () => {
    expect(approvedPathOf("=/")).toBe("/");
    expect(isExactPath("/")).toBe(false);
    expect(isExactPath("=/")).toBe(true);
  });
  it.each([
    "=",
    "==/",
    "=a",
    "=/?x=1",
    "=/?",
    "=/#f",
    "=/a\\b",
    "=/a/../b",
    "=//a",
    "/a?x=1",
  ])("잘못된 경로 표현은 응답 전체를 거절한다: %s", (p) => {
    const job = { ...ready, scope: { ...scope, path_prefixes: ["/", p] } };
    expect(() => parseURLJob(envelope(job), owner)).toThrow();
  });
});
