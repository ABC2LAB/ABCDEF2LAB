"use client";

import { useEffect, useState } from "react";

import { ApiError } from "@/lib/diagnosis/api";
import { endedURLRun } from "@/lib/diagnosis/diagnosis-runs";
import {
  COUNT_LABEL,
  type DecisionClass,
  decisionClass,
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
/** 첫 진단 의도 전에는 실행 저널이 없어 서버가 journal "none"(이전 서버는 404 "전송 통제 기록이 아직 없습니다")을 준다 — 실패가 아니라 대기 상태다. */
const PRE_DIAGNOSIS_RETRY_MS = 10000;
const FLOW_RETRY_MS = 2000;

/** 기록이 없을 때 보이는 문장. 끝난 회차는 기록이 더 생기지 않으므로(확정 단계는 E②를 따로 잡지 않음, 계약 §4) 기다린다고 하지 않는다. */
function noRecordText(ended: boolean, bound: boolean, flow: E2Flow | null): string {
  if (!ended) return "진단 요청 전 · 아직 전송 통제 기록이 없습니다. 첫 진단 요청이 나가면 기록이 생깁니다.";
  // 404 = 실행 결속 전(실행 디렉터리 · 정책 없음)에 끝난 회차
  if (!bound) return "실행 결속 전에 끝나 전송 통제 기록이 없습니다.";
  const t = flow?.target_requests;
  const explored = t?.exploration_state === "closed" && t.exploration ? t.exploration : 0;
  return [
    "이 회차는 진단 요청 없이 끝나 전송 통제 기록이 없습니다. 진단을 하지 않았다는 뜻이고, 취약점이 없다는 뜻은 아닙니다.",
    explored ? `탐색 요청 ${explored}건은 전송 통제 밖 탐색 경계로 나갔습니다.` : null,
  ]
    .filter(Boolean)
    .join(" ");
}

export function UrlE2FlowLive() {
  const { goTo, route } = useShell();
  const task = useUrl((st) => st.job?.task_id ?? null);
  // 끝난 회차(completed · cancelled · failed)는 저널이 더 생기지 않는다. 기록 없음(none · 404)을 받으면 다시 읽지 않는다.
  // 보는 중에 회차가 끝나면 effect 가 다시 돌아 한 번 더 읽고 멈춘다(그사이 저널이 생겼을 수 있다).
  const ended = useUrl((st) => (st.job ? endedURLRun(st.job) : false));
  // 작업대는 모든 화면을 시작 때 숨긴 채 붙인다. 이 화면이 보일 때만 읽는다(1차 리허설 F5: 안 보이는데도 2초마다 불러
  // 첫 진단 의도 전 404가 수백 건 쌓였다)
  const visible = route === "e2flow";
  const [state, setState] = useState<{ task: string | null; flow: E2Flow | null; failed: boolean; waiting: boolean }>({
    task: null,
    flow: null,
    failed: false,
    waiting: false,
  });
  useEffect(() => {
    if (!task || !visible) return;
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
        setState({ task, flow, failed: false, waiting: false });
        // invalid 저널에서는 사건·커서를 지우고 첫 페이지부터 다시 확인한다.
        if (next.more) {
          void poll();
        } else if (next.journal === "none") {
          // 저널 없음(진단 요청 전)은 404 대기와 같은 간격으로 다시 읽는다. 끝난 회차는 멈춘다
          if (!ended) timer = setTimeout(() => void poll(), PRE_DIAGNOSIS_RETRY_MS);
        } else if (!(ended && next.journal === "verified")) {
          // 끝난 회차의 검증된 저널은 더 늘지 않으므로 끝 페이지까지 읽으면 멈춘다(#994 윤재상 P3). invalid 는 지금처럼 다시 확인한다
          timer = setTimeout(() => void poll(), FLOW_RETRY_MS);
        }
      } catch (error) {
        if (current) {
          flow = null;
          const waiting = error instanceof ApiError && error.status === 404;
          setState({ task, flow: null, failed: !waiting, waiting });
          if (!(waiting && ended))
            timer = setTimeout(() => void poll(), waiting ? PRE_DIAGNOSIS_RETRY_MS : FLOW_RETRY_MS);
        }
      }
    };
    void poll();
    return () => {
      current = false;
      controller.abort();
      clearTimeout(timer);
    };
  }, [task, visible, ended]);
  const flow = state.task === task ? state.flow : null;
  const failed = state.task === task && state.failed;
  const waiting = state.task === task && state.waiting;
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
          {task && !flow && !failed && !waiting ? <p className={s.empty}>불러오는 중…</p> : null}
          {waiting || flow?.journal === "none" ? (
            <p className={s.empty} role="status">
              {noRecordText(ended, !waiting, flow)}
            </p>
          ) : null}
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
                  화면용 저널 누계입니다. E③ 확정 단계의 호출·시도는 저장된 실행 기록에 포함됩니다. 허용은 송신 결정이며
                  차단은 송신 전 확인된 경우만 표시합니다.
                </p>
                <div className={s.chiprow}>
                  {(Object.keys(COUNT_LABEL) as (keyof typeof COUNT_LABEL)[]).map((k) => (
                    <span key={k}>
                      {COUNT_LABEL[k]} <b>{flow.counts[k]}</b>
                    </span>
                  ))}
                </div>
                {flow.model_writes && (flow.model_writes.responded_2xx > 0 || flow.model_writes.unverified > 0) ? (
                  <p className={s.help}>
                    모델 단계 쓰기 요청 중 2xx 응답 {flow.model_writes.responded_2xx}건 · 결과 확인 필요{" "}
                    {flow.model_writes.unverified}건. 다음 점검 전에 대상의 상태를 확인하세요.
                  </p>
                ) : null}
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
