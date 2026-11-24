"use client";

// 업무 규칙 검토(#914 인계, practical-diagnostic-report 계약). 접근통제 · 업무 로직 후보 가운데 주 호출에서 감사된 성공 응답을
// 재현 단계와 이을 수 있는 것만 서버가 '관찰'(재현 단계 번호 · 메서드 · 응답 코드)과 '검토 질문'을 준다. 읽기 전용이고, 후보의
// 검토 상태 · 제품 E③ 판정은 바꾸지 않는다. 응답 코드로 신원 · 역할 · 소유 · 권한을 짐작하지 않는다(서버 문구 그대로).
//   GET /api/report/diagnostic?task=<id>&audience=internal&format=review-json → { task_id: "<id>", entries: [...] }
// 모양이 어긋나면 화면에 쓰지 않는다(fail-closed). 끝난 점검은 바뀌지 않아 점검마다 한 번만 읽는다.

import { ApiError, apiFetchLimitedJSON } from "@/lib/diagnosis/api";

export type BusinessRuleReview = {
  /** 보고서 항목 ID('candidate-<id>') */
  id: string;
  status: "deferred";
  reason: "business_rule_unverified";
  /** 예: '재현 단계 2 (GET, HTTP 200), 재현 단계 3 (PUT, HTTP 204)' */
  observation: string;
  question: string;
};

/** 응답 상한(바이트) · 글 상한(글자) */
export const BUSINESS_RULE_REVIEW_MAX_BYTES = 256 << 10;
const MAX_TEXT = 2000;
const ENTRY_KEYS = ["id", "status", "reason", "observation", "question"];

export class BusinessRuleReviewShapeError extends Error {}

const object = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
/** 제어 문자가 있는가(줄바꿈 등 — 서버 문구는 한 줄이다) */
const hasControl = (v: string) => [...v].some((c) => c.charCodeAt(0) < 0x20 || c.charCodeAt(0) === 0x7f);
const text = (v: unknown): v is string =>
  typeof v === "string" && v.trim().length > 0 && v.length <= MAX_TEXT && !hasControl(v);

/** 응답 → 항목 ID 별 검토. 다른 점검 · 모양 밖이면 BusinessRuleReviewShapeError */
export function parseBusinessRuleReviews(raw: unknown, taskId: string): ReadonlyMap<string, BusinessRuleReview> {
  if (!object(raw) || raw.task_id !== taskId || !Array.isArray(raw.entries))
    throw new BusinessRuleReviewShapeError("envelope");
  const out = new Map<string, BusinessRuleReview>();
  for (const e of raw.entries) {
    if (
      !object(e) ||
      Object.keys(e).some((k) => !ENTRY_KEYS.includes(k)) ||
      typeof e.id !== "string" ||
      !/^candidate-[1-9][0-9]*$/.test(e.id) ||
      e.status !== "deferred" ||
      e.reason !== "business_rule_unverified" ||
      !text(e.observation) ||
      !text(e.question) ||
      out.has(e.id)
    )
      throw new BusinessRuleReviewShapeError("entry");
    out.set(e.id, {
      id: e.id,
      status: "deferred",
      reason: "business_rule_unverified",
      observation: e.observation,
      question: e.question,
    });
  }
  return out;
}

const cache = new Map<string, Promise<ReadonlyMap<string, BusinessRuleReview>>>();

/** 점검 하나의 업무 규칙 검토(점검마다 한 번 — 실패하면 다음에 다시 읽는다) */
export function fetchBusinessRuleReviews(taskId: string): Promise<ReadonlyMap<string, BusinessRuleReview>> {
  if (!/^[1-9][0-9]*$/.test(taskId)) return Promise.reject(new BusinessRuleReviewShapeError("task_id"));
  const hit = cache.get(taskId);
  if (hit) return hit;
  const q = new URLSearchParams({ task: taskId, audience: "internal", format: "review-json" });
  const p = apiFetchLimitedJSON(`/api/report/diagnostic?${q}`, BUSINESS_RULE_REVIEW_MAX_BYTES, {
    cache: "no-store",
    redirect: "error",
  }).then((raw) => parseBusinessRuleReviews(raw, taskId));
  cache.set(taskId, p);
  p.catch(() => cache.delete(taskId));
  return p;
}

/** 이 서버에 검토 형식이 없는가(옛 서버) — 이때는 칸을 두지 않는다 */
export function reviewUnsupported(error: unknown): boolean {
  return error instanceof ApiError && [400, 404, 501].includes(error.status);
}

/** 시험용: 캐시 비우기 */
export function resetBusinessRuleReviews(): void {
  cache.clear();
}
