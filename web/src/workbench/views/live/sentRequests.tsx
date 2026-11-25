"use client";

// '보낸 요청' 칸(사용자 2026-11-25 결정): 숫자는 대상에 나간 요청 총계(E② 저널의 모델 · 로그인 · 확정 + 탐색 경계, #929
// target_requests), 마우스를 올리면 내역('모델 50 · 로그인 6'). 설명 문장은 두지 않는다. 읽기 프로그램에는 내역을 같이 읽힌다.

import { type E2Flow, getE2Flow, sentRequestsOf } from "@/lib/diagnosis/e2flow";

import { type Poll, usePoll } from "../../live/usePoll";
import s from "../../workbench.module.css";

/** 진행 중 점검의 전송 기록을 다시 읽는 간격(ms). 끝난 점검은 한 번만 */
const SENT_POLL_MS = 10_000;

/** 시작한 점검(task)의 e2-flow. 화면이 보일 때만 읽는다. */
export function useE2FlowFor(taskId: string | null, visible: boolean, ended: boolean): Poll<E2Flow> {
  return usePoll((signal) => getE2Flow(taskId, undefined, signal), [taskId], {
    enabled: visible && !!taskId,
    intervalMs: ended ? 0 : SENT_POLL_MS,
  });
}

/** 총계 + 마우스 올림 내역. 아직 못 읽었거나 내역이 없는 서버면 '—'(모델 수로 대신하지 않음). */
export function SentRequestsValue({ flow }: { flow: E2Flow | null | undefined }) {
  const sent = sentRequestsOf(flow);
  if (!sent)
    return (
      <span title="전송 기록을 아직 읽지 못했습니다" data-sent="none">
        —
      </span>
    );
  return (
    <span title={sent.detail} data-sent={sent.value}>
      {sent.value}
      <span className={s["sr-only"]}>{` (${sent.detail})`}</span>
    </span>
  );
}
