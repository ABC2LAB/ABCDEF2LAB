import { useMemo } from "react";

import { Screen } from "../components/Screen";
import { TopBar } from "../components/TopBar";
import { MockBanner, Pill, Sev, Strong, Sub, VerdictBadge } from "../components/ui";
import { useApiResource } from "../lib/api";
import {
  COMPARISON_SIGNALS,
  type ComparisonRow,
  confirmedEvidenceComplete,
  getFinding,
  missingEvidence,
  ORACLE_BADGE,
  SIGNAL_LABEL,
} from "../lib/finding";
import { useGo } from "../nav";

const bodyText = { fontSize: 14, color: "var(--muted)", lineHeight: 1.7, margin: 0 } as const;
const listText = { margin: 0, paddingLeft: 18, fontSize: 14, color: "var(--muted)", lineHeight: 1.8 } as const;
const cellSub = { color: "var(--muted)", fontSize: 13 } as const;

/** 비교 행 판정 → 셀 배지. */
const ROW_CELL: Record<ComparisonRow["row"], { label: string; style: React.CSSProperties }> = {
  ok: { label: "정상", style: {} },
  vuln: { label: "취약", style: { background: "var(--danger-weak)", color: "var(--danger)" } },
  sus: { label: "의심", style: { background: "var(--warn-weak)", color: "var(--warn)" } },
  und: { label: "판단 불가", style: { background: "var(--warn-weak)", color: "var(--warn)" } },
};

/** 통제변수 한 줄(감사·재현). */
function Meta({ k, v }: { k: string; v: string }) {
  return (
    <div style={{ display: "flex", gap: 8, fontSize: 12, lineHeight: 1.8 }}>
      <span style={{ color: "var(--faint)", flex: "none", minWidth: 96 }}>{k}</span>
      <code style={{ color: "var(--muted)", wordBreak: "break-all" }}>{v}</code>
    </div>
  );
}

/** ===== 발견 상세 (판정 레코드) ===== */
export default function FindingScreen() {
  const go = useGo();
  const { data, loading, error } = useApiResource(() => getFinding(null), []);
  const badge = useMemo(() => (data ? ORACLE_BADGE[data.oracleValue] : null), [data]);
  const complete = useMemo(() => (data ? confirmedEvidenceComplete(data) : false), [data]);
  const missing = useMemo(() => (data ? missingEvidence(data) : []), [data]);

  return (
    <Screen id="finding">
      <div className="frame">
        <TopBar current="history" />
        <div style={{ padding: "22px 24px", maxWidth: 760 }}>
          <div className="crumb">
            {/* biome-ignore lint/a11y/useValidAnchor: 시안 crumb 링크(href 로 키보드 이동 가능) */}
            <a
              href="#history"
              onClick={(e) => {
                e.preventDefault();
                go("history");
              }}
            >
              점검 기록
            </a>{" "}
            › {/* biome-ignore lint/a11y/useValidAnchor: 시안 crumb 링크(href 로 키보드 이동 가능) */}
            <a
              href="#report"
              onClick={(e) => {
                e.preventDefault();
                go("report");
              }}
            >
              보고서
            </a>{" "}
            › <span>발견</span>
          </div>

          <MockBanner>
            판정 레코드(verdict-record) 연동 전 예시예요. 아래 판정·비교·증거는 실데이터가 아니며, 실제 발견은 E③ 오라클
            레코드로 채워져요. <Strong>구조·마스킹은 실제와 같은 계약</Strong>이라 연동 시 그대로 살아요.
          </MockBanner>

          {loading || !data || !badge ? (
            <div style={{ textAlign: "center", padding: "40px 0", color: "var(--muted)" }}>
              {error ? `불러오지 못했어요. (${error})` : "불러오는 중…"}
            </div>
          ) : (
            <>
              <div className="row" style={{ alignItems: "center", gap: 8, marginBottom: 6, flexWrap: "wrap" }}>
                <Sev level={data.severity} />
                {badge.verdict ? <VerdictBadge verdict={badge.verdict} /> : <Pill tone="muted">{badge.label}</Pill>}
                <Pill tone="muted">
                  {data.vulnClass} · {data.axis === "horizontal" ? "수평" : "수직"}
                </Pill>
              </div>
              <h1 className="big" style={{ marginBottom: 4 }}>
                {data.title}
              </h1>
              <div className="ep" style={{ fontFamily: "var(--mono)", fontSize: 13, color: "var(--muted)" }}>
                {data.resourceRef} <span style={{ color: "var(--faint)" }}>· 논리 자원 참조(raw URL 아님)</span>
              </div>

              <div className="card" style={{ marginTop: 16 }}>
                <h3>무엇이 관찰됐나</h3>
                <p style={bodyText}>
                  <Strong>{data.subjects.evaluatedRef}</Strong>(무관한 사용자)로{" "}
                  <Strong>{data.subjects.ownerRef}</Strong>의 자원(<code>{data.resourceRef}</code>)을 교차 요청했을 때,{" "}
                  <Strong>{data.subjects.ownerRef}에만 심은 비자명 표식</Strong>이 응답 필드에 그대로 나타났어요. 요청
                  반사·공통 템플릿은 배제됐고요 — "{data.subjects.evaluatedRef}가 {data.subjects.ownerRef}의 실제 자원을
                  받았다"는 신호예요.
                </p>
              </div>

              <div className="card">
                <h3>
                  판정 <Sub>· 세 체크(정책·신원·데이터)</Sub>
                </h3>
                <div className="row" style={{ gap: 8, flexWrap: "wrap", marginBottom: 6 }}>
                  <Pill tone={data.checks.policy === "yes" ? undefined : "warn"}>
                    정책 {SIGNAL_LABEL[data.checks.policy]}
                  </Pill>
                  <Pill tone={data.checks.identity === "yes" ? undefined : "warn"}>
                    신원 {SIGNAL_LABEL[data.checks.identity]}
                  </Pill>
                  <Pill tone={data.checks.data === "yes" ? undefined : "warn"}>
                    데이터 {SIGNAL_LABEL[data.checks.data]}
                  </Pill>
                  <Pill tone="muted">정책 관찰: {data.accessPolicyResult.replace(/_/g, " ")}</Pill>
                </div>
                <div className="hint">
                  사유 코드: <code>{data.reasonCodes.join(" · ")}</code>
                </div>
              </div>

              <div className="card">
                <h3>
                  다계정 비교 <Sub>· 같은 자원, 다른 주체 (신호 기반 — 요청/응답 원문 아님)</Sub>
                </h3>
                <div className="scrollx">
                  <table className="cmp">
                    <thead>
                      <tr>
                        <th>요청 주체</th>
                        <th>자원</th>
                        <th>관측</th>
                        <th>판정</th>
                      </tr>
                    </thead>
                    <tbody>
                      {data.comparison.map((rowData) => {
                        const cell = ROW_CELL[rowData.row];
                        return (
                          <tr
                            key={rowData.label}
                            style={rowData.row === "vuln" ? { background: "var(--danger-weak)" } : undefined}
                          >
                            <td>
                              <strong>{rowData.label}</strong>
                              <br />
                              <span style={cellSub}>{rowData.roleNote}</span>
                            </td>
                            <td>
                              <code>{rowData.resourceRef}</code>
                            </td>
                            <td style={cellSub}>{rowData.observation}</td>
                            <td>
                              <span className="vbadge2" style={{ fontSize: 12, ...cell.style }}>
                                {cell.label}
                              </span>
                            </td>
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                </div>
              </div>

              <div className="card">
                <h3>
                  증거 <Sub>· 이름·신호만 저장(값·본문 마스킹)</Sub>
                </h3>
                <p style={{ ...bodyText, marginBottom: 10 }}>
                  표식이 산 응답 필드:{" "}
                  {data.canaryFields.map((f) => (
                    <code key={f}>{f}</code>
                  ))}{" "}
                  · 값은 <span style={{ color: "var(--danger)" }}>[REDACTED]</span> (절대규칙 2 — canary 값·본문·쿠키
                  저장 안 함).
                </p>
                <div className="row" style={{ gap: 6, flexWrap: "wrap", marginBottom: 8 }}>
                  {COMPARISON_SIGNALS.map((s) => (
                    <Pill key={s.key} tone={data.signals[s.key] === "yes" ? undefined : "warn"}>
                      {s.label} {SIGNAL_LABEL[data.signals[s.key]]}
                    </Pill>
                  ))}
                </div>
                <div className="hint">
                  트래픽 증거 ref: <code>{data.trafficEvidenceRefs.join(", ") || "—"}</code> (본문 아님, 참조 id만).
                </div>
              </div>

              <div className="card">
                <h3>왜 이 판정인가</h3>
                {data.oracleValue === "confirmed" && complete ? (
                  <>
                    <ul style={listText}>
                      <li>
                        <Strong>{data.subjects.evaluatedRef}</Strong> 응답에{" "}
                        <Strong>{data.subjects.ownerRef}의 표식</Strong>이 그대로 담겼다(실제로 받았음)
                      </li>
                      <li>비자명 표식이고 요청 반사·공통 템플릿이 배제됐다(대조군과 다름)</li>
                      <li>세 체크(정책·신원·데이터) 모두 충족</li>
                    </ul>
                    <div className="hint">
                      증거 3조건이 모두 모여 <Strong>확정</Strong>. 하나라도 비면 <Strong>판단 불가</Strong>로 남겨요.
                    </div>
                  </>
                ) : (
                  <>
                    <p style={{ ...bodyText, marginBottom: 8 }}>
                      {data.oracleValue === "confirmed"
                        ? "확정 배지가 붙었지만 아래 증거가 비어 있어요 — 연동 시 이런 경우는 판단 불가로 내려야 해요."
                        : `${ORACLE_BADGE[data.oracleValue].label} — 아래 증거가 부족해 확정으로 올리지 않아요.`}
                    </p>
                    <ul style={listText}>
                      {missing.length > 0 ? (
                        missing.map((m) => <li key={m}>{m}</li>)
                      ) : (
                        <li>부족 신호 없음(레코드 신호를 다시 확인)</li>
                      )}
                    </ul>
                    <div className="hint">
                      미실행·판단 불가를 <Strong>"취약 없음"·"안전"으로 적지 않아요</Strong>(절대규칙 5). 확인 못 한 건
                      확인 못 했다고 남겨요.
                    </div>
                  </>
                )}
              </div>

              <div className="card">
                <h3>
                  통제변수 <Sub>· 재현·감사</Sub>
                </h3>
                <Meta k="정책 버전" v={data.control.policyVersion} />
                <Meta k="정책=정본" v={data.control.hintMatchesCanonical ? "일치" : "불일치(정책 무효)"} />
                <Meta k="기대 접근" v={data.control.expectedAccess} />
                <Meta k="reset 세대" v={data.control.resetGeneration} />
                <Meta k="관찰 시각" v={new Date(data.control.observedAt).toISOString()} />
                <Meta k="모델 태그" v={data.control.modelTag} />
                <Meta
                  k="주체 상태"
                  v={`${data.subjects.ownerRef}=${data.subjects.ownerStatus} · ${data.subjects.evaluatedRef}=${data.subjects.evaluatedStatus}`}
                />
              </div>

              <div className="card" style={{ marginBottom: 0 }}>
                <h3>고치려면</h3>
                <p style={bodyText}>
                  자원 조회 시 <code>자원.소유자 == 세션 주체</code>를 서버에서 검사하세요. 번호 추측을 막으려면 순차 ID
                  대신 추측 어려운 식별자를 쓰는 것도 방법이에요.
                </p>
              </div>
            </>
          )}
        </div>
      </div>
    </Screen>
  );
}
