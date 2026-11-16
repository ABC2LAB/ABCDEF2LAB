"use client";

import { useEffect, useState } from "react";
import {
  COUNT_LABEL,
  decisionClass,
  type DecisionClass,
  type E2Flow,
  getE2Flow,
  mergeE2Flow,
  REASON_LABEL,
  STAGE_LABEL,
} from "@/lib/diagnosis/e2flow";
import { useUrl } from "../../live/urlDiagnosis";
import { useShell } from "../../shell";
import s from "../../workbench.module.css";
import { UrlHead } from "./urlParts";

const CLASS_LABEL: Record<DecisionClass, string> = {
  allow: "허용",
  deny: "송신 전 차단",
  transient: "송신 실패",
  unknown: "불명",
};
const COLORS: Record<DecisionClass, string> = {
  allow: "var(--lo)",
  deny: "var(--ok)",
  transient: "var(--mid)",
  unknown: "var(--und)",
};
const DECISIONS = { allowed: "허용", denied: "거부", unsupported: "미지원", unknown: "불명" };
export function UrlE2FlowLive() {
  const { goTo } = useShell();
  const task = useUrl((st) => st.job?.task_id ?? null);
  const [state, setState] = useState<{ task: string | null; flow: E2Flow | null; failed: boolean }>({
    task: null,
    flow: null,
    failed: false,
  });
  useEffect(() => {
    if (!task) return;
    let current = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let flow: E2Flow | null = null;
    const controller = new AbortController();
    const poll = async () => {
      try {
        const after = flow?.journal === "verified" ? flow.last_index : undefined;
        const next = await getE2Flow(task, after, controller.signal);
        if (!current) return;
        if (next.more && next.last_index <= (after ?? 0)) throw new Error("invalid_e2_cursor");
        flow = mergeE2Flow(flow, next);
        setState({ task, flow, failed: false });
        // invalid 저널에서는 사건·커서를 지우고 첫 페이지부터 다시 확인한다.
        if (next.more) {
          void poll();
        } else {
          timer = setTimeout(() => void poll(), 2000);
        }
      } catch {
        if (current) {
          flow = null;
          setState({ task, flow: null, failed: true });
          timer = setTimeout(() => void poll(), 2000);
        }
      }
    };
    void poll();
    return () => {
      current = false;
      controller.abort();
      clearTimeout(timer);
    };
  }, [task]);
  const flow = state.task === task ? state.flow : null;
  const failed = state.task === task && state.failed;
  return (
    <>
      <UrlHead
        title="전송 통제 흐름"
        crumb="전송 통제 흐름"
        acts={
          <button className={s.btn} type="button" onClick={() => goTo("controlpolicy")}>
            전송 통제 정책
          </button>
        }
      />
      <div className={s.vb}>
        <div className={s.vm}>
          <p className={s.note}>
            명령 원문·URL query·헤더·본문은 표시하지 않습니다. 실제 호출 참조·시도 ID·가린 경로·관측값만 보입니다.
          </p>
          {!task ? <p className={s.empty}>실제 진단 작업을 선택하세요.</p> : null}
          {task && !flow && !failed ? <p className={s.empty}>불러오는 중…</p> : null}
          {failed ? (
            <p className={s.empty} role="alert">
              전송 통제 흐름을 확인하지 못했습니다. 이전 사건은 표시하지 않습니다.
            </p>
          ) : null}
          {flow?.journal === "invalid" ? (
            <p className={s.note} role="alert">
              감사 저널 검증 실패 — 사건과 집계를 표시하지 않습니다.
            </p>
          ) : null}
          {flow?.journal === "verified" ? (
            <>
              <section className={s.isec} aria-label="전송 통제 요약">
                <h2>전송 통제 요약</h2>
                <p className={s.help}>
                  서버 전체 저널 누계입니다. 허용은 송신 결정이며 대상 도달·완전 쓰기의 증거가 아닙니다. 차단은 송신 전
                  확인된 경우만 표시합니다.
                </p>
                <div className={s.chiprow}>
                  {(Object.keys(COUNT_LABEL) as (keyof typeof COUNT_LABEL)[]).map((k) => (
                    <span key={k}>
                      {COUNT_LABEL[k]} <b>{flow.counts[k]}</b>
                    </span>
                  ))}
                </div>
                <p className={s.help}>
                  마지막 사건 번호 {flow.last_index} · 이전 호출 {flow.omitted_calls}개 생략
                </p>
              </section>
              <section className={s.isec} aria-label="호출 · 시도">
                <h2>호출 · 시도</h2>
                {!flow.calls.length ? (
                  <p className={s.empty}>기록된 호출이 없습니다. 취약점 없음이나 안전을 뜻하지 않습니다.</p>
                ) : null}
                {flow.calls.map((call) => (
                  <article key={call.call_ref} className={s.isec}>
                    <p>
                      호출 <code>{call.call_ref}</code> · 의도 {call.intent_id} ·{" "}
                      {call.closed ? `마감(사건 ${call.closed_index})` : "미완결(중간 상태) · 최종 집계가 아닙니다"}
                    </p>
                    {call.attempts.map((a) => {
                      const cls = decisionClass(a);
                      return (
                        <div
                          key={a.attempt_id}
                          data-class={cls}
                          style={{ borderLeft: `3px solid ${COLORS[cls]}`, padding: 10, marginTop: 8 }}
                        >
                          <p>
                            <b>{CLASS_LABEL[cls]}</b> · 시도 <code>{a.attempt_id}</code> · 사건 {a.index} · 시도 번호{" "}
                            {a.sequence}
                          </p>
                          <p>
                            <code>
                              {a.method} {a.path_pattern || "경로 없음"}
                            </code>
                          </p>
                          {a.stage === "intent" ? (
                            <p>
                              송신 전 감사(intent)만 있습니다. 송신·쓰기·응답 결과는 미확인이며,{" "}
                              {call.closed
                                ? "호출은 마감됐습니다."
                                : "결과 사건을 기다리는 중이거나 결과가 불명입니다."}
                            </p>
                          ) : (
                            <>
                              <p>
                                판정 {DECISIONS[a.decision]} · 사유{" "}
                                {a.reason === "complete" && !a.response_received
                                  ? "응답 미확인"
                                  : REASON_LABEL[a.reason]}{" "}
                                · 단계 {STAGE_LABEL[a.stage]} ({a.stage})
                              </p>
                              <p>
                                송신 시도(연결 포함) {a.forward_attempted ? "있음" : "없음"} · 요청 쓰기{" "}
                                {a.request_written ? "관찰" : "없음"} · 응답 {a.status_code || "응답 없음"} · 수신{" "}
                                {a.response_received ? "있음" : "없음"} · 완전 수신{" "}
                                {a.response_complete ? "확인" : "미확인"} · 응답 잘림{" "}
                                {a.response_truncated ? "있음" : "없음"}
                              </p>
                            </>
                          )}
                          <p className={s.help}>
                            규칙 {a.rule_ref || "없음"} · {a.at}
                          </p>
                        </div>
                      );
                    })}
                  </article>
                ))}
              </section>
            </>
          ) : null}
        </div>
      </div>
    </>
  );
}
