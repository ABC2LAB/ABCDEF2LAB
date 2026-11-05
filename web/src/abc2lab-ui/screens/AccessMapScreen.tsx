import { useMemo } from "react";

import { Screen } from "../components/Screen";
import { TopBar } from "../components/TopBar";
import { Pill, Strong } from "../components/ui";
import { useApiResource } from "../lib/api";
import {
  type AccessDecision,
  buildCellResolver,
  countDecisions,
  DECISION_BADGE,
  DECISION_CELL,
  DECISION_LABEL,
  getAccessMap,
  HELD_DECISIONS,
} from "../lib/kg";
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

/** 범례 점 색: 셀 판정 → dot 스타일 */
const DOT_STYLE: Record<AccessDecision, React.CSSProperties> = {
  "allow-ok": { background: "var(--teal)" },
  "deny-ok": { background: "var(--line-2)" },
  vuln: { background: "var(--danger)" },
  suspected: { background: "var(--viol)" },
  unknown: { background: "var(--warn)" },
  untested: { background: "transparent", border: "1px dashed var(--line-2)" },
  blank: { background: "var(--surface-2)", border: "1px solid var(--line-2)" },
  na: { background: "var(--faint)" },
};
const LEGEND_ORDER: AccessDecision[] = ["allow-ok", "deny-ok", "vuln", "suspected", "unknown", "untested", "blank"];

/** 칸 본문 — 확정/의심은 색이 1차 구분, 본문도 서로 다른 말로(§4.2(7)·N7). 나머지는 긴 라벨. */
const CELL_BODY: Partial<Record<AccessDecision, string>> = { vuln: "취약", suspected: "의심" };

/** ===== 접근통제 지도 (KG) ===== */
export default function AccessMapScreen() {
  const go = useGo();
  const { taskId } = useSelection();
  const { data, loading, error } = useApiResource(() => getAccessMap(taskId), [taskId]);

  // 셀 선택·fail-closed 규칙은 공유 리졸버(lib/kg)로 일원화. UI 는 조합·판정하지 않는다(§4.2(1)).
  const resolve = useMemo(() => buildCellResolver(data?.cells ?? []), [data]);
  // 헤드라인 칩은 판정 축(수평 H)만 센다 — 수직(V)은 후속 미리보기라 확정 수에 섞지 않는다(§4.2(4)).
  const counts = useMemo(() => (data ? countDecisions(data, "H") : null), [data]);

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
              <Strong>프로토타입</Strong> — 소유·권한 KG 저장소·서버 projection API는 아직 미구현(kg-policy §4.2(1)).
              아래는 계약 shape를 따른 <Strong>예시 데이터</Strong>로, 실 KG 연동 시 그대로 실데이터가 채워져요.{" "}
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
              {counts && (
                <div className="search" style={{ gap: 8, marginBottom: 12 }}>
                  {counts.vuln > 0 && <Pill tone="warn">취약 확정 {counts.vuln}</Pill>}
                  {counts.suspected > 0 && <Pill tone="warn">취약 의심 {counts.suspected}</Pill>}
                  {counts.unknown > 0 && <Pill tone="warn">판정 불가 {counts.unknown}</Pill>}
                  {counts.untested > 0 && <Pill tone="muted">미검사 {counts.untested}</Pill>}
                </div>
              )}

              <div className="scrollx">
                <table className="matrix">
                  <thead>
                    <tr>
                      <th />
                      {data.resources.map((r) => (
                        <th key={r.id}>
                          {r.label}
                          <span style={{ display: "block", fontSize: 12, fontWeight: 500, color: "var(--faint)" }}>
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
                          const { decision, note, projectionError } = resolve(p.ref, r.id);
                          const badge = DECISION_BADGE[decision];
                          const title = projectionError
                            ? `판정 불가(projection): ${projectionError}`
                            : note || DECISION_LABEL[decision];
                          return (
                            <td
                              key={r.id}
                              className={DECISION_CELL[decision]}
                              title={title}
                              style={p.axis === "V" ? { opacity: 0.6 } : undefined}
                            >
                              {/* 확정/의심은 색으로 1차 구분하고 배지로 보강(§4.2(7)). 본문도 서로 다른 말(취약/의심). */}
                              <span>{CELL_BODY[decision] ?? DECISION_LABEL[decision]}</span>
                              {badge && <span className="cell-badge">{badge}</span>}
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
                    {HELD_DECISIONS.has(d) ? " (보류)" : ""}
                  </span>
                ))}
              </div>

              <div className="hint">
                <Strong>확정·의심·판정 불가·미검사·기록 없음은 서로 다른 칸</Strong>이에요. 의심(SUSPECTED)은 확정으로
                올리지 않고, 안 찔러본 칸(미검사)·근거가 전무한 칸(기록 없음)·정책을 못 푼 칸(판정 불가)을{" "}
                <Strong>“취약 없음”으로 칠하지 않아요</Strong> — 조회 안 함 ≠ 없음(fail-closed, kg-policy §4.2·절대규칙
                5). 정상 차단은 앱 인가 차단 관측 계약 전까지 <Strong>보류</Strong>예요(§4.2(3)).
              </div>
            </>
          )}
        </div>
      </div>
    </Screen>
  );
}
