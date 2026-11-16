import { expect, it } from "vitest";

import { parseWorkflowLimits } from "./diagnosis-url";

// 서버 diagnosisflow.Limits.Valid와 같은 경계다: 페이지 256, 공개 끝점 목록 64(VAmPI 실제 회차 24b).
const base = { format: "abc2lab.workbench-flow/v1", max_pages: 8, max_endpoints: 32, max_response_bytes: 4096, allow_reversible_writes: false };

it("accepts workflow limits up to the server caps", () => {
  expect(parseWorkflowLimits({ ...base, max_pages: 256 }).max_pages).toBe(256);
  expect(parseWorkflowLimits({ ...base, max_endpoints: 64 }).max_endpoints).toBe(64);
});

it("rejects workflow limits above the server caps", () => {
  expect(() => parseWorkflowLimits({ ...base, max_pages: 257 })).toThrow();
  expect(() => parseWorkflowLimits({ ...base, max_endpoints: 65 })).toThrow();
});
