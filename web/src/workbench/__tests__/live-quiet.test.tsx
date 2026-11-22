import "@testing-library/jest-dom/vitest";
import { act, renderHook } from "@testing-library/react";
import { expect, it } from "vitest";

import { ApiError } from "@/lib/diagnosis/api";

import { heldReason, useCoverageForbidden } from "../views/live/UrlLiveLive";

it("'승인 필요로 보류' 설명은 실제 보류 요청의 메서드로 쓴다(1차 리허설 F13)", () => {
  expect(heldReason([])).toBe("보내기 전 사람 확인");
  expect(heldReason([{ method: "POST" }, { method: "PUT" }])).toBe("쓰기 메서드 · 보내기 전 확인");
  // 사용자 승인 모드: '승인 후 전송' 경로는 GET도 보류된다 — "쓰기 메서드"라고 쓰지 않는다
  expect(heldReason([{ method: "GET" }])).toBe("승인 후 전송 경로(읽기) · 보내기 전 확인");
  expect(heldReason([{ method: "GET" }, { method: "POST" }, { method: "HEAD" }])).toBe("쓰기 1 · 읽기 2 · 보내기 전 확인");
});

it("coverage 403을 한 번 받으면 그 task 에선 다시 부르지 않고, 다른 task 는 다시 한 번 부른다(1차 리허설 F6)", () => {
  const { result, rerender } = renderHook(({ task }) => useCoverageForbidden(task), { initialProps: { task: "1" as string | null } });
  expect(result.current.forbidden).toBe(false);
  act(() => result.current.observe(new ApiError(503, "x")));
  expect(result.current.forbidden).toBe(false); // 403이 아니면 멈추지 않는다(일시 오류)
  act(() => result.current.observe(new ApiError(403, "승인된 작업의 공개 진단 API만 사용할 수 있습니다")));
  expect(result.current.forbidden).toBe(true);
  rerender({ task: "2" });
  expect(result.current.forbidden).toBe(false);
});
