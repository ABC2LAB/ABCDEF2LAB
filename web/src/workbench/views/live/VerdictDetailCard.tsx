"use client";

// 후보 상세 페이지의 확정 단계 판정 (E③ 판정 상세, GET /api/tasks/{id}/finding-verdict-detail?candidate_id=…).
// 보고서 목록의 제품 판정 줄과 같은 부품(VerdictBody · HiddenVerdict)으로 그려 봉인 표시 · 사유 문구 · 값 숨김 규칙이 같다.
// 연결된 판정이 없으면(no_link) 두지 않는다 — '판정 없음' · '취약점 없음'으로 그리지 않는다(규칙 5). 확정이어도 후보는
// 수동 검토 필요 그대로다(OD05).

import { ApiError } from "@/lib/diagnosis/api";

import { cx } from "../../lib/cx";
import { candidateIdOf, fetchVerdictDetail, isProductVerdictsShapeError } from "../../live/productVerdicts";
import { usePoll } from "../../live/usePoll";
import { REQUIREMENT_TEXT } from "../../live/verdictReasons";
import { Icon } from "../../ui/icons";
import s from "../../workbench.module.css";
import { HiddenVerdict, VerdictBody } from "./productVerdictViews";

function failText(error: unknown): string {
  if (isProductVerdictsShapeError(error)) return "판정 상세 응답 형식을 확인하지 못해 표시하지 않습니다.";
  const status = error instanceof ApiError ? error.status : 0;
  const text: Record<number, string> = {
    0: "판정 상세를 읽지 못했습니다(연결 실패).",
    401: "판정 상세를 읽지 못했습니다 · 다시 로그인해 주세요.",
    403: "이 점검의 판정을 볼 권한이 없습니다.",
    409: "이 점검에 결속된 회차가 없어 판정을 읽을 수 없습니다.",
    // 저장 판정 진위 실패 · 이 후보를 참조하는 판정이 둘 이상(OD05) · 상한 초과
    422: "판정 기록을 확인하지 못해 표시하지 않습니다.",
    503: "판정 서비스가 응답하지 않습니다.",
  };
  return text[status] ?? `판정 상세를 읽지 못했습니다 (HTTP ${status}).`;
}

export function VerdictDetailCard({ taskId, entryId }: { taskId: string; entryId: string }) {
  const candidate = candidateIdOf(entryId);
  // 저장 자료라 한 번만 읽고 '다시 읽기'로만 갱신한다
  const poll = usePoll((signal) => fetchVerdictDetail(taskId, candidate as string, signal), [taskId, candidate], {
    enabled: candidate !== null,
  });
  if (candidate === null) return null;
  // 404 = 제품 관리 중계가 아니라 경로가 없거나 후보를 못 찾음 → 두지 않는다
  if (poll.error instanceof ApiError && poll.error.status === 404) return null;
  const title = <h3>확정 단계 판정</h3>;
  if (poll.error)
    return (
      <section className={s.isec} aria-label="확정 단계 판정">
        {title}
        <div className={cx(s.note, s.warn)} role="alert">
          <Icon name="alert" />
          <span className={s["lv-grow"]}>{failText(poll.error)}</span>
          <button className={cx(s.btn, s.sm)} type="button" onClick={poll.reload}>
            다시 읽기
          </button>
        </div>
      </section>
    );
  const d = poll.data;
  if (!d || d.availability === "no_link") return null;
  const r = d.row;
  return (
    <section className={s.isec} aria-label="확정 단계 판정">
      {title}
      {r.recordStatus === "record_integrity_failed" ? (
        <p>
          <HiddenVerdict id={r.verdictId} seal={r.evidenceSeal} />
        </p>
      ) : (
        <>
          <VerdictBody r={r} />
          {d.missingRequirements.length ? (
            <div className={s.help}>
              {`확정에 모자란 검사: ${d.missingRequirements.map((k) => REQUIREMENT_TEXT[k] ?? k).join(" · ")}`}
            </div>
          ) : null}
        </>
      )}
    </section>
  );
}
