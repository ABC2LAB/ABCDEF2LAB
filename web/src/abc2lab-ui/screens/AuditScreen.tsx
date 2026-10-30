import { useMemo, useState } from "react";
import { Screen } from "../components/Screen";
import { Tabs } from "../components/Tabs";
import { TopBar } from "../components/TopBar";
import { Strong } from "../components/ui";
import { useApiResource } from "../lib/api";
import { type EngineControlEvent, getControlLog } from "../lib/engine";
import { useSelection } from "../lib/selection";
import { useGo } from "../nav";

// 누가 통제를 정했나 (decision_source)
// 서버 decision_source 값: rule | model | unknown (dto)
const SOURCE_LABEL: Record<string, string> = { rule: "규칙", model: "모델 판정", unknown: "미상" };
function sourceLabel(s: string): string {
  return SOURCE_LABEL[s] ?? (s || "—");
}
// 범주색 재사용: 규칙=시스템, 모델판정=에이전트, 미상=op
const SOURCE_WHO: Record<string, string> = { rule: "sys", model: "agent", unknown: "op" };

type FilterKey = "all" | "rule" | "model" | "unknown";

/** status → 표시 라벨·색 */
function statusInfo(status: string): { label: string; style: React.CSSProperties } {
  const s = status.toLowerCase();
  if (s.includes("den") || s.includes("block"))
    return { label: "차단", style: { color: "var(--danger)", fontWeight: 700 } };
  if (s.includes("pend")) return { label: "대기", style: { color: "var(--muted)", fontWeight: 700 } };
  if (s.includes("allow") || s.includes("approv"))
    return { label: "허용", style: { color: "var(--teal-ink)", fontWeight: 700 } };
  return { label: status || "—", style: { color: "var(--muted)", fontWeight: 700 } };
}

/** ISO/epoch → HH:MM:SS */
function tsLabel(ts: number): string {
  if (!ts) return "—";
  const dt = new Date(ts);
  if (Number.isNaN(dt.getTime())) return "—";
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(dt.getHours())}:${p(dt.getMinutes())}:${p(dt.getSeconds())}`;
}

const oneLine = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" } as const;
const noticeBox = {
  fontSize: 12.5,
  color: "var(--muted)",
  background: "var(--surface-2)",
  border: "1px dashed var(--line-2)",
  borderRadius: 8,
  padding: "10px 12px",
  marginBottom: 14,
} as const;

/** ===== 통제 기록(감사) — ARTEX 승인 게이트(intercept)가 개입한 요청 ===== */
export default function AuditScreen() {
  const go = useGo();
  const { taskId } = useSelection();
  const [filter, setFilter] = useState<FilterKey>("all");

  const { data, loading, error } = useApiResource(() => getControlLog(taskId ?? ""), [taskId]);
  const events = useMemo<EngineControlEvent[]>(() => data ?? [], [data]);

  const counts = useMemo(() => {
    let rule = 0;
    let model = 0;
    let unknown = 0;
    for (const e of events) {
      if (e.source === "rule") rule += 1;
      else if (e.source === "model") model += 1;
      else unknown += 1;
    }
    return { all: events.length, rule, model, unknown };
  }, [events]);

  const rows = useMemo(() => {
    if (filter === "all") return events;
    return events.filter((e) => e.source === filter);
  }, [events, filter]);

  if (!taskId) {
    return (
      <Screen id="audit">
        <div className="frame">
          <TopBar current="settings" />
          <div style={{ textAlign: "center", padding: "64px 24px", color: "var(--muted)" }}>
            <p style={{ marginBottom: 16 }}>통제 기록을 볼 점검을 먼저 골라 주세요.</p>
            <button className="btn primary" type="button" onClick={() => go("history")}>
              점검 기록 열기
            </button>
          </div>
        </div>
      </Screen>
    );
  }

  return (
    <Screen id="audit">
      <div className="frame">
        <TopBar current="settings" />
        <div style={{ padding: "22px 24px" }}>
          <div className="crumb">
            {/* biome-ignore lint/a11y/useValidAnchor: 원본 시안 crumb 이동(앵커 스타일 유지) */}
            <a onClick={() => go("settings")}>설정</a> › <span>통제 기록</span>
          </div>
          <div className="rhead" style={{ marginBottom: 14 }}>
            <div>
              <h1 className="big" style={{ marginBottom: 4 }}>
                통제 기록
              </h1>
              <p className="lead" style={{ marginBottom: 0 }}>
                {loading
                  ? "불러오는 중…"
                  : error
                    ? "불러오지 못했어요."
                    : `승인 게이트(ARTEX intercept) 개입 ${events.length}건`}
              </p>
            </div>
            {/* 변조탐지 해시체인은 우리 기여(미구현) — "무결"을 단정하지 않는다 */}
            <span className="badge" style={{ alignSelf: "center", color: "var(--muted)" }}>
              변조 탐지 준비 중
            </span>
          </div>

          <div style={noticeBox}>
            ARTEX <Strong>승인 게이트(intercept)</Strong>가 에이전트 도구 호출을 <Strong>차단</Strong>했거나 승인 대기로
            잡은 기록이에요(허용까지 남기는 원장은 아님). 우리 <Strong>전송 통제(E², 네트워크 경계)</Strong>와{" "}
            <Strong>변조 탐지 해시체인·로그 권한 분리</Strong>는 <Strong>연동 전</Strong> — 연동되면 각 줄에 체인 해시가
            붙어요.
          </div>

          <Tabs<FilterKey>
            value={filter}
            onChange={setFilter}
            items={[
              { key: "all", label: "전체", count: counts.all },
              { key: "rule", label: "규칙", count: counts.rule },
              { key: "model", label: "모델 판정", count: counts.model },
              { key: "unknown", label: "미상", count: counts.unknown },
            ]}
          />

          {error ? (
            <div style={{ textAlign: "center", padding: "32px 0", color: "var(--muted)" }}>
              통제 기록을 불러오지 못했어요. ({error})
            </div>
          ) : !loading && events.length === 0 ? (
            <div style={{ textAlign: "center", padding: "40px 0", color: "var(--muted)" }}>
              이 점검에서 통제가 개입한 기록이 없어요.
            </div>
          ) : (
            <div className="log">
              {rows.map((e) => {
                const st = statusInfo(e.status);
                const who = SOURCE_WHO[e.source] ?? "sys";
                const blocked = st.label === "차단";
                return (
                  <div key={e.id} className="logrow" style={blocked ? { background: "var(--warn-weak)" } : undefined}>
                    <span className="ts">{tsLabel(e.ts)}</span>
                    <span className={`who ${who}`}>{sourceLabel(e.source)}</span>
                    <span className="msg" style={{ minWidth: 0 }}>
                      <span
                        style={{ ...(oneLine as React.CSSProperties), display: "block" }}
                        title={e.reason || e.ruleName}
                      >
                        {e.agent ? <span style={{ color: "var(--faint)" }}>{e.agent} · </span> : null}
                        <code>{e.tool || "요청"}</code> · <strong style={st.style}>{st.label}</strong>
                        {e.reason ? ` · ${e.reason}` : ""}
                      </span>
                      {e.ruleName ? (
                        <span
                          style={{
                            ...(oneLine as React.CSSProperties),
                            display: "block",
                            fontSize: 11,
                            color: "var(--faint)",
                          }}
                          title={e.ruleName}
                        >
                          {e.ruleName}
                        </span>
                      ) : null}
                    </span>
                    <span className="hash" style={{ color: "var(--muted)" }}>
                      준비 중
                    </span>
                  </div>
                );
              })}
              {!loading && rows.length === 0 && events.length > 0 && (
                <div style={{ textAlign: "center", padding: "24px 0", color: "var(--muted)" }}>
                  이 분류에 해당하는 기록이 없어요.
                </div>
              )}
            </div>
          )}

          <div className="hint">
            각 줄은 ARTEX 승인 게이트가 막거나 승인 대기로 잡은 도구 호출이에요. 전송 통제(E²)·변조 탐지 체인·권한
            분리가 연동되면 한 줄이라도 바뀔 때 <Strong>그 뒤 체인이 전부 깨져서 변조가 드러나요.</Strong>
          </div>
        </div>
      </div>
    </Screen>
  );
}
