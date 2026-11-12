"use client";

// 전송 통제 흐름 화면 (E② — "Bash 명령 → 거기서 난 요청 → 허용·차단", 지금은 예시).
// 데이터는 lib/diagnosis/e2flow seam(getE2Flow)에서만 온다. 3분류(허용/차단/불명)·정규화·집계는 전부 seam(lib),
// 화면은 표현만 한다(e2flow.test.ts 가 로직을 이미 검증). 표현/데이터 분리.
// 안전 규칙(절대규칙 2·#189 §5.1): 명령 원문·URL·query·헤더·본문·세션/키 원문·프록시 토큰은 seam 에 애초에 없다 —
//   화면은 도구 호출 참조·시도 ID·단계·안전 사유·관측값·감사 저장 상태만 그린다.
// ※ live 점등은 E② 감사 "표시용 투영" API 연동 시 getE2Flow(taskId) 만 교체(이 화면은 안 바뀐다). 지금은 예시.

import type { CSSProperties } from "react";
import { useEffect, useState } from "react";

import {
  AUDIT_LABEL,
  DECISION_LABEL,
  type DecisionClass,
  decisionClass,
  type E2Attempt,
  type E2Call,
  type E2Flow,
  getE2Flow,
  normalizeAttempt,
  RESP_LABEL,
  STAGE_LABEL,
  summarize,
  WRITE_LABEL,
} from "@/lib/diagnosis/e2flow";

import { cx } from "../../lib/cx";
import { useShell } from "../../shell";
import { Icon } from "../../ui/icons";
import s from "../../workbench.module.css";
import { UrlHead } from "./urlParts";

/**
 * 3분류(허용/차단/불명) → 색. 접근통제 지도·커버리지·발견 상세와 같은 실재 토큰 팔레트.
 * 의미가 다른 뷰라 매핑도 다르다: 차단(통제가 송신 전 막음) = 초록(안전쪽 결과), 통과(송신 허용) = 파랑(정보),
 * 불명(이미 나갔을 수 있음·감사 실패) = 회색(fail-closed). 색을 화면이 추정하지 않고 decisionClass 결과로만 칠한다.
 */
const CLASS_STYLE: Record<DecisionClass, CSSProperties> = {
  allow: { background: "var(--lo-bg)", color: "var(--lo)", borderColor: "var(--lo)" },
  deny: { background: "var(--ok-bg)", color: "var(--ok)", borderColor: "var(--ok)" },
  unknown: { background: "var(--und-bg)", color: "var(--und)", borderColor: "var(--rule-2)" },
};
const CLASS_LABEL: Record<DecisionClass, string> = { allow: "통과", deny: "차단", unknown: "불명" };
/** 범례 보조 설명 — 색만으로 오인하지 않게 글로도 뜻을 단다. */
const CLASS_HINT: Record<DecisionClass, string> = {
  allow: " (송신 허용)",
  deny: " (송신 전 차단)",
  unknown: " (이미 나갔을 수 있음·fail-closed)",
};
const CLASS_ORDER: DecisionClass[] = ["allow", "deny", "unknown"];

// ── 인라인 스타일(뼈대). 색은 CLASS_STYLE, 토큰은 workbench.module.css 의 것만 쓴다.
const chipBase: CSSProperties = {
  display: "inline-flex",
  alignItems: "center",
  gap: 5,
  padding: "3px 9px",
  borderRadius: 999,
  border: "1px solid",
  fontSize: 12,
};
const neutralChip: CSSProperties = {
  ...chipBase,
  background: "var(--well)",
  color: "var(--ink-2)",
  borderColor: "var(--rule)",
};
const badgeBase: CSSProperties = {
  display: "inline-flex",
  alignItems: "center",
  padding: "2px 10px",
  borderRadius: 6,
  border: "1px solid",
  fontSize: 12,
  fontWeight: 600,
};
const chipRow: CSSProperties = { display: "flex", flexWrap: "wrap", gap: 6, marginTop: 6 };
const legendList: CSSProperties = {
  display: "flex",
  flexWrap: "wrap",
  gap: "6px 14px",
  listStyle: "none",
  padding: 0,
  margin: "6px 0 0",
};
const legendItem: CSSProperties = {
  display: "inline-flex",
  alignItems: "center",
  gap: 6,
  fontSize: 12,
  color: "var(--ink-2)",
};
const swatch: CSSProperties = { width: 14, height: 14, borderRadius: 4, border: "1px solid", flex: "0 0 auto" };
const callBox: CSSProperties = {
  marginTop: 12,
  border: "1px solid var(--rule)",
  borderRadius: 8,
  padding: "10px 12px",
};
const callHead: CSSProperties = { display: "flex", flexWrap: "wrap", alignItems: "baseline", gap: 8 };
const attemptBox: CSSProperties = {
  marginTop: 8,
  paddingTop: 8,
  borderTop: "1px solid var(--rule)",
};
const obsRow: CSSProperties = { display: "flex", flexWrap: "wrap", gap: 6, marginTop: 4 };

const fmtTime = (ms: number): string => {
  try {
    return `${new Date(ms).toISOString().replace("T", " ").slice(0, 19)} UTC`;
  } catch {
    return "—";
  }
};

/** 색 범례 — 색을 글로도 설명(축소 화면 오인 방지). */
function Legend() {
  return (
    <ul style={legendList} aria-label="범례">
      {CLASS_ORDER.map((c) => (
        <li key={c} style={legendItem}>
          <span aria-hidden="true" style={{ ...swatch, ...CLASS_STYLE[c] }} />
          <span>
            {CLASS_LABEL[c]}
            {CLASS_HINT[c]}
          </span>
        </li>
      ))}
    </ul>
  );
}

/** 한 시도 — 집행 결정 배지(3분류 색) + 단계·쓰기·응답·감사 관측. seam 이 준 masked 필드만. */
function AttemptBlock({ attempt }: { attempt: E2Attempt }) {
  const a = normalizeAttempt(attempt); // 미등록 enum 은 여기서 안전쪽(unknown)으로 내려온다
  const cls = decisionClass(a);
  return (
    <div style={attemptBox}>
      <div style={chipRow}>
        <span data-class={cls} data-decision={a.decision} style={{ ...badgeBase, ...CLASS_STYLE[cls] }}>
          {DECISION_LABEL[a.decision]}
        </span>
        <span style={neutralChip}>{STAGE_LABEL[a.stage]}</span>
      </div>
      <div style={obsRow}>
        <span style={neutralChip}>{WRITE_LABEL[a.requestWrite]}</span>
        <span style={neutralChip}>{RESP_LABEL[a.response]}</span>
        <span style={neutralChip}>{AUDIT_LABEL[a.auditStore]}</span>
      </div>
      {a.reason ? <p className={s.help}>{a.reason}</p> : null}
      <p className={s.help}>
        <span className={s.mono}>{a.attemptId}</span> · {fmtTime(a.observedAt)}
      </p>
    </div>
  );
}

/** 한 도구(Bash) 호출과 그 호출이 낸 시도들. 미완결(finalized=false)은 중간 상태로 표시. */
function CallBlock({ call }: { call: E2Call }) {
  return (
    <div style={callBox}>
      <div style={callHead}>
        <span style={{ fontWeight: 600, color: "var(--ink)" }}>{call.tool}</span>
        <span className={s.mono} style={{ color: "var(--ink-3)", fontSize: 12 }}>
          {call.callRef}
        </span>
        {call.finalized ? null : <span style={{ ...chipBase, ...CLASS_STYLE.unknown }}>미완결(중간 상태)</span>}
      </div>
      {call.attempts.map((at) => (
        <AttemptBlock key={at.attemptId} attempt={at} />
      ))}
    </div>
  );
}

/** 전송 통제 흐름 (예시). */
export function UrlE2FlowLive() {
  const { goTo } = useShell();
  const [flow, setFlow] = useState<E2Flow | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let live = true;
    // TODO(이경준): E² 감사 표시용 투영 API 연동 시 현재 task id 를 넘겨 실 흐름으로 점등한다(지금은 예시). getE2Flow 만 교체.
    getE2Flow(null)
      .then((f) => {
        if (live) setFlow(f);
      })
      .catch(() => {
        if (live) setFailed(true);
      });
    return () => {
      live = false;
    };
  }, []);

  const sum = flow ? summarize(flow) : null;

  return (
    <>
      <UrlHead
        title="전송 통제 흐름"
        crumb="전송 통제 흐름"
        acts={
          // 자매 화면(승인 범위) 진입 — 잠정 자리, 이동찬 작업대 시안 정렬 대상.
          <button className={s.btn} type="button" onClick={() => goTo("controlpolicy")}>
            전송 통제 정책
          </button>
        }
      />
      <div className={s.vb}>
        <div className={s.vm}>
          <div className={s.note} role="note">
            <Icon name="info" />
            <span>명령 원문·URL·헤더·본문은 표시하지 않습니다 — 호출 참조·시도 ID·단계·사유·관측값만 보입니다.</span>
          </div>
          {flow?.mock ? (
            <div className={cx(s.note, s.warn)} role="note">
              <Icon name="alert" />
              <span>예시 데이터입니다. 실제 전송 통제 흐름은 E② 감사 투영 API 연결 후 나옵니다.</span>
            </div>
          ) : null}
          {failed ? <p className={s.empty}>전송 통제 흐름을 불러오지 못했습니다.</p> : null}
          {!flow && !failed ? <p className={s.empty}>불러오는 중…</p> : null}

          {flow && sum ? (
            <section className={s.isec} aria-label="전송 통제 요약">
              <h2>전송 통제 요약</h2>
              <p className={s.help}>
                진단 요청이 E② 전송 경계를 어떻게 지났는지입니다. ‘통과’는 게이트 통과일 뿐 대상 도달·완전 쓰기·감사
                저장의 증명이 아닙니다 — 관측값(쓰기·응답·감사)으로 따로 판단합니다.
              </p>
              <div style={chipRow}>
                <span style={neutralChip}>
                  호출 <b>{sum.calls}</b>
                </span>
                <span style={neutralChip}>
                  시도 <b>{sum.attempts}</b>
                </span>
                <span style={{ ...chipBase, ...CLASS_STYLE.allow }}>
                  통과 <b>{sum.allow}</b>
                </span>
                <span style={{ ...chipBase, ...CLASS_STYLE.deny }}>
                  차단 <b>{sum.deny}</b>
                </span>
                <span style={{ ...chipBase, ...CLASS_STYLE.unknown }}>
                  불명 <b>{sum.unknown}</b>
                </span>
              </div>
              {sum.pending > 0 ? (
                <div className={cx(s.note, s.warn)} role="note">
                  <Icon name="alert" />
                  <span>미완결(중간 상태) 호출 {sum.pending}개 — 최종 집계가 아닙니다(감사 마감·내구 저장 전).</span>
                </div>
              ) : null}
              <Legend />
            </section>
          ) : null}

          {flow ? (
            <section className={s.isec} aria-label="호출·시도">
              <h2>호출 · 시도</h2>
              {flow.calls.length ? (
                flow.calls.map((c) => <CallBlock key={c.callRef} call={c} />)
              ) : (
                <p className={s.empty}>기록된 호출이 없습니다.</p>
              )}
              <p className={s.help}>
                실행 참조: <span className={s.mono}>{flow.runRef}</span> · 정책 버전{" "}
                <span className={s.mono}>{flow.policyVersion}</span>
              </p>
            </section>
          ) : null}
        </div>
      </div>
    </>
  );
}
