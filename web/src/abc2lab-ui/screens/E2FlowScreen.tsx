import { useMemo } from "react";

import { Screen } from "../components/Screen";
import { TopBar } from "../components/TopBar";
import { Strong } from "../components/ui";
import { useApiResource } from "../lib/api";
import {
  AUDIT_LABEL,
  DECISION_LABEL,
  type DecisionClass,
  decisionClass,
  type E2Attempt,
  getE2Flow,
  normalizeAttempt,
  RESP_LABEL,
  STAGE_LABEL,
  summarize,
  WRITE_LABEL,
} from "../lib/e2flow";
import { useSelection } from "../lib/selection";
import { useGo } from "../nav";

const noticeBox = {
  fontSize: 13,
  color: "var(--muted)",
  background: "var(--surface-2)",
  border: "1px dashed var(--line-2)",
  borderRadius: 6,
  padding: "10px 12px",
  marginBottom: 14,
} as const;

/** 3분류 칩 색 — 허용(teal)·차단(danger)·불명(warn). AccessMap 과 같은 fail-closed 3분. */
const CLASS_STYLE: Record<DecisionClass, React.CSSProperties> = {
  allow: { background: "var(--teal-weak)", color: "var(--teal-ink)", border: "1px solid var(--teal-border)" },
  deny: { background: "var(--danger-weak)", color: "var(--danger)", border: "1px solid var(--danger-border)" },
  unknown: { background: "var(--warn-weak)", color: "var(--warn)", border: "1px solid var(--warn-border)" },
};
const CLASS_LABEL: Record<DecisionClass, string> = { allow: "통과", deny: "차단", unknown: "불명" };

const chip: React.CSSProperties = {
  fontSize: 12,
  fontWeight: 700,
  borderRadius: 4,
  padding: "2px 8px",
  flex: "none",
};
const tag: React.CSSProperties = {
  fontSize: 12,
  color: "var(--faint)",
  background: "var(--surface-2)",
  border: "1px solid var(--line-2)",
  borderRadius: 4,
  padding: "1px 7px",
};

function tsLabel(ts: number): string {
  if (!ts) return "—";
  const dt = new Date(ts);
  if (Number.isNaN(dt.getTime())) return "—";
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(dt.getHours())}:${p(dt.getMinutes())}:${p(dt.getSeconds())}`;
}

/** 한 시도 행 — 결정 칩 + 단계 + 안전 사유 + 관측 단서(쓰기·응답·감사저장). */
function AttemptRow({ raw }: { raw: E2Attempt }) {
  const a = normalizeAttempt(raw);
  const cls = decisionClass(a.decision);
  return (
    <div className="logrow">
      <span className="ts">{tsLabel(a.observedAt)}</span>
      <code style={{ flex: "none", color: "var(--faint)", fontSize: 12 }}>{a.attemptId}</code>
      <span style={{ ...chip, ...CLASS_STYLE[cls] }} title={DECISION_LABEL[a.decision]}>
        {CLASS_LABEL[cls]}
      </span>
      <span className="msg" style={{ minWidth: 0 }}>
        <span style={{ display: "block" }}>
          <Strong>{DECISION_LABEL[a.decision]}</Strong>
          <span style={{ color: "var(--faint)" }}> · {STAGE_LABEL[a.stage]}</span>
          {a.reason ? <span style={{ color: "var(--muted)" }}> · {a.reason}</span> : null}
        </span>
        {/* 관측 단서 — admitted(통과)라도 complete·저장을 단정하지 않는다(#183 §5·#189 §5.3). */}
        <span style={{ display: "flex", gap: 6, flexWrap: "wrap", marginTop: 6 }}>
          <span style={tag}>예약 {a.reserved ? "O" : "X"}</span>
          <span style={tag}>{WRITE_LABEL[a.requestWrite]}</span>
          <span style={tag}>{RESP_LABEL[a.response]}</span>
          <span
            style={{
              ...tag,
              ...(a.auditStore === "acked" ? {} : { color: "var(--warn)", borderColor: "var(--warn-border)" }),
            }}
          >
            {AUDIT_LABEL[a.auditStore]}
          </span>
        </span>
      </span>
    </div>
  );
}

/** ===== 전송 통제 흐름 (E②) — Bash 명령 → 그 요청 → 허용·차단 ===== */
export default function E2FlowScreen() {
  const go = useGo();
  const { taskId } = useSelection();
  const { data, loading, error } = useApiResource(() => getE2Flow(taskId), [taskId]);
  const sum = useMemo(() => (data ? summarize(data) : null), [data]);

  return (
    <Screen id="e2-flow">
      <div className="frame">
        <TopBar current="settings" />
        <div style={{ padding: "22px 24px" }}>
          <div className="crumb">
            {/* biome-ignore lint/a11y/useValidAnchor: 시안 crumb 이동(앵커 스타일 유지) */}
            <a
              href="#settings"
              onClick={(event) => {
                event.preventDefault();
                go("settings");
              }}
            >
              설정
            </a>{" "}
            › <a onClick={() => go("audit")}>통제 기록</a> › <span>전송 통제 흐름</span>
          </div>
          <h1 className="big" style={{ marginBottom: 4 }}>
            전송 통제 흐름
          </h1>
          <p className="lead">
            한 <Strong>Bash 호출</Strong>이 낸 HTTP 요청마다, 전송 통제(E②)가 <Strong>송신 전에</Strong> 허용·차단을
            정해요. 호출 하나에 요청(시도) 여러 개가 붙을 수 있어요 — 승인 범위 안은 통과, 범위를 넘으면 송신 전 차단.
          </p>

          {data?.mock && (
            <div style={noticeBox}>
              <Strong>프로토타입</Strong> — E② 네트워크 경계 집행·감사 "표시용 투영"은 아직 미구현·운영 비활성이에요
              (근거 초안 #183 §5·#189 §2). 범용 경계 감사 사건 표현은 <Strong>버전 합의 전 제안</Strong>이라, 확정되면
              이 화면은 <Strong>getE2Flow 한 함수만</Strong> 실데이터로 바꾸면 그대로 살아요. 아래는 그 shape 를 따른{" "}
              <Strong>예시</Strong>예요.
            </div>
          )}

          {loading ? (
            <div style={{ textAlign: "center", padding: "40px 0", color: "var(--muted)" }}>불러오는 중…</div>
          ) : error || !data ? (
            <div style={{ textAlign: "center", padding: "40px 0", color: "var(--muted)" }}>
              흐름을 불러오지 못했어요.{error ? ` (${error})` : ""}
            </div>
          ) : (
            <>
              {sum && (
                <div className="search" style={{ gap: 8, marginBottom: 6 }}>
                  <span className="pill">통과 {sum.allow}</span>
                  <span className="pill" style={{ background: "var(--danger-weak)", color: "var(--danger)" }}>
                    차단 {sum.deny}
                  </span>
                  <span className="pill" style={{ background: "var(--warn-weak)", color: "var(--warn)" }}>
                    불명 {sum.unknown}
                  </span>
                  {sum.pending > 0 && (
                    <span className="pill" style={{ background: "var(--surface-2)", color: "var(--muted)" }}>
                      미완료(중간) {sum.pending}
                    </span>
                  )}
                </div>
              )}
              <p style={{ fontSize: 12, color: "var(--faint)", margin: "0 0 14px" }}>
                실행 {data.runRef} · 통과 = 송신 허용일 뿐 <Strong>대상 도달·저장의 증명이 아니에요</Strong> —
                도달·저장은 각 시도의 관측값으로만 봐요.
              </p>

              <div style={{ display: "grid", gap: 14 }}>
                {data.calls.map((call) => (
                  <div
                    key={call.callRef}
                    style={{ border: "1px solid var(--line-2)", borderRadius: 8, overflow: "hidden" }}
                  >
                    <div
                      style={{
                        display: "flex",
                        gap: 10,
                        alignItems: "center",
                        flexWrap: "wrap",
                        padding: "10px 14px",
                        background: "var(--surface-2)",
                        borderBottom: "1px solid var(--line-2)",
                      }}
                    >
                      <code style={{ fontSize: 13, color: "var(--fg)" }}>{call.callRef}</code>
                      <span style={tag}>{call.tool}</span>
                      <span style={{ flex: 1 }} />
                      <span
                        className="badge"
                        style={
                          call.finalized
                            ? { color: "var(--muted)" }
                            : { color: "var(--warn)", background: "var(--warn-weak)" }
                        }
                      >
                        {call.finalized ? "집계 완결" : "미완료(중간 집계)"}
                      </span>
                    </div>
                    <div className="log" style={{ border: "none", borderRadius: 0 }}>
                      {call.attempts.map((at) => (
                        <AttemptRow key={at.attemptId} raw={at} />
                      ))}
                    </div>
                  </div>
                ))}
              </div>

              <div className="hint">
                <Strong>통과 ≠ 대상 도달 ≠ 감사 저장</Strong>이에요 — 통과는 송신 허용일 뿐, 쓰기·응답·감사 저장은 각
                시도의 관측값으로만 표시해요(#183 §5). 종료·배수가 불명(<Strong>drain_unknown</Strong>)이거나 감사
                저장이 불명이면 <Strong>성공으로 축소하지 않고 미완료로 남겨요</Strong>(#189 §5.3). 명령
                원문·URL·헤더·토큰은 이 화면에 넣지 않아요(절대규칙 2) — 불투명한 <Strong>호출 참조·시도 ID</Strong>로만
                이어 봐요.
              </div>
            </>
          )}
        </div>
      </div>
    </Screen>
  );
}
