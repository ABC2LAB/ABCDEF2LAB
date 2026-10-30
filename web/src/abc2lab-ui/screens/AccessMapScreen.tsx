import { useMemo } from "react";
import { Screen } from "../components/Screen";
import { TopBar } from "../components/TopBar";
import { Pill, Strong, Sub } from "../components/ui";
import { useApiResource } from "../lib/api";
import { type AccessDecision, DECISION_CELL, DECISION_LABEL, getAccessMap } from "../lib/kg";
import { useSelection } from "../lib/selection";
import { useGo } from "../nav";

const noticeBox = {
  fontSize: 12.5,
  color: "var(--muted)",
  background: "var(--surface-2)",
  border: "1px dashed var(--line-2)",
  borderRadius: 8,
  padding: "10px 12px",
  marginBottom: 14,
} as const;

/** 범례 점 색: 셀 판정 → dot 스타일 */
const DOT_STYLE: Record<AccessDecision, React.CSSProperties> = {
  "allow-ok": { background: "var(--teal)" },
  "deny-ok": { background: "var(--line-2)" },
  vuln: { background: "var(--danger)" },
  unknown: { background: "var(--warn)" },
  untested: { background: "transparent", border: "1px dashed var(--line-2)" },
  na: { background: "var(--faint)" },
};
const LEGEND_ORDER: AccessDecision[] = ["allow-ok", "deny-ok", "vuln", "unknown", "untested"];

/** ===== 접근통제 지도 (KG) ===== */
export default function AccessMapScreen() {
  const go = useGo();
  const { taskId } = useSelection();
  const { data, loading, error } = useApiResource(() => getAccessMap(taskId), [taskId]);

  const cellIndex = useMemo(() => {
    const m = new Map<string, { decision: AccessDecision; note?: string }>();
    for (const c of data?.cells ?? []) m.set(`${c.principalRef}|${c.resourceId}`, c);
    return m;
  }, [data]);

  const counts = useMemo(() => {
    let vuln = 0;
    let unknown = 0;
    let untested = 0;
    for (const c of data?.cells ?? []) {
      if (c.decision === "vuln") vuln += 1;
      else if (c.decision === "unknown") unknown += 1;
      else if (c.decision === "untested") untested += 1;
    }
    return { vuln, unknown, untested };
  }, [data]);

  return (
    <Screen id="access-map">
      <div className="frame">
        <TopBar current="history" />
        <div style={{ padding: "22px 24px" }}>
          <div className="crumb">
            <a onClick={() => go("history")}>점검 기록</a> › <a onClick={() => go("report")}>보고서</a> ›{" "}
            <span>접근통제 지도</span>
          </div>
          <h1 className="big" style={{ marginBottom: 4 }}>
            접근통제 지도
          </h1>
          <p className="lead">
            행은 <Strong>신원(계정·역할)</Strong>, 열은 <Strong>자원(소유자)</Strong>. 닿으면 안 되는 칸이 열리면 취약 —
            같은 역할에서 남의 자원이 열리면 <Strong>수평 IDOR</Strong>, 역할을 넘으면 <Strong>수직 권한 상승</Strong>.
          </p>

          {data?.mock && (
            <div style={noticeBox}>
              <Strong>프로토타입</Strong> — 소유·권한 KG 저장소·API는 아직 미구현(kg-policy §5). 아래는 계약 shape를
              따른 <Strong>예시 데이터</Strong>로, 실 KG 연동 시 그대로 실데이터가 채워져요.{" "}
              <Strong>v0.1 = 수평(교차 계정) 판정</Strong>, 수직(역할 레벨)은 후속.
            </div>
          )}

          {loading ? (
            <div style={{ textAlign: "center", padding: "40px 0", color: "var(--muted)" }}>불러오는 중…</div>
          ) : error || !data ? (
            <div style={{ textAlign: "center", padding: "40px 0", color: "var(--muted)" }}>
              지도를 불러오지 못했어요.{error ? ` (${error})` : ""}
            </div>
          ) : (
            <>
              <div className="search" style={{ gap: 8, marginBottom: 12 }}>
                {counts.vuln > 0 && <Pill tone="warn">취약 {counts.vuln}</Pill>}
                {counts.unknown > 0 && <Pill tone="warn">판정 불가 {counts.unknown}</Pill>}
                {counts.untested > 0 && <Pill tone="muted">미검사 {counts.untested}</Pill>}
              </div>

              <div className="scrollx">
                <table className="matrix">
                  <thead>
                    <tr>
                      <th />
                      {data.resources.map((r) => (
                        <th key={r.id}>
                          {r.label}
                          <span style={{ display: "block", fontSize: 10.5, fontWeight: 500, color: "var(--faint)" }}>
                            소유 {r.ownerRef}
                          </span>
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {data.principals.map((p) => (
                      <tr key={p.ref}>
                        <td className="rowh">
                          {p.ref}
                          <span className="sm">
                            {p.role}
                            {p.axis === "V" ? " · 수직(후속)" : ""}
                          </span>
                        </td>
                        {data.resources.map((r) => {
                          const cell = cellIndex.get(`${p.ref}|${r.id}`);
                          const decision: AccessDecision = cell?.decision ?? "untested";
                          return (
                            <td
                              key={r.id}
                              className={DECISION_CELL[decision]}
                              title={cell?.note || DECISION_LABEL[decision]}
                              style={p.axis === "V" ? { opacity: 0.6 } : undefined}
                            >
                              {DECISION_LABEL[decision]}
                            </td>
                          );
                        })}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>

              <div className="leg">
                {LEGEND_ORDER.map((d) => (
                  <span key={d}>
                    <span className="dot" style={DOT_STYLE[d]} />
                    {DECISION_LABEL[d]}
                  </span>
                ))}
              </div>

              <div className="hint">
                <Strong>미검사·판정 불가·취약는 서로 다른 칸</Strong>이에요. 안 찔러본 칸(미검사)이나 정책을 못 푼
                칸(판정 불가)을 <Strong>“취약 없음”으로 칠하지 않아요</Strong> — 조회 안 함 ≠ 없음(fail-closed,
                kg-policy §4·절대규칙 5).
              </div>
            </>
          )}
        </div>
      </div>
    </Screen>
  );
}
