import { useMemo } from "react";

import { Screen } from "../components/Screen";
import { TopBar } from "../components/TopBar";
import { MockBanner, Pill, Sev, Strong, Sub, VerdictBadge } from "../components/ui";
import { useApiResource } from "../lib/api";
import {
  COMPARISON_SIGNALS,
  type ComparisonRow,
  dataAccessProven,
  displayComparisonRow,
  displayVerdict,
  type FindingRecord,
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

/**
 * 판정 레코드 본문 — record 하나를 fail-closed 로 그린다(화면 테스트가 임의 레코드로 직접 렌더).
 * 화면 판정은 `displayVerdict`(증거 부족 시 보수 하향), 원값은 감사용으로 함께 표시한다.
 */
export function FindingDetail({ record }: { record: FindingRecord }) {
  const display = displayVerdict(record);
  const dbadge = ORACLE_BADGE[display];
  const rawBadge = ORACLE_BADGE[record.oracleValue];
  const overridden = display !== record.oracleValue;
  const proven = dataAccessProven(record);
  const missing = missingEvidence(record);
  const confirmedSignals = COMPARISON_SIGNALS.filter((s) => record.signals[s.key] === "yes");
  const openSignals = COMPARISON_SIGNALS.filter((s) => record.signals[s.key] !== "yes");

  return (
    <>
      <div className="row" style={{ alignItems: "center", gap: 8, marginBottom: 6, flexWrap: "wrap" }}>
        <Sev level={record.severity} />
        {dbadge.verdict ? <VerdictBadge verdict={dbadge.verdict} /> : <Pill tone="muted">{dbadge.label}</Pill>}
        <Pill tone="muted">
          {record.vulnClass} · {record.axis === "horizontal" ? "수평" : "수직"}
        </Pill>
      </div>
      <h1 className="big" style={{ marginBottom: 4 }}>
        {record.title}
      </h1>
      <div className="ep" style={{ fontFamily: "var(--mono)", fontSize: 13, color: "var(--muted)" }}>
        {record.resourceRef} <span style={{ color: "var(--faint)" }}>· 논리 자원 참조(raw URL 아님)</span>
      </div>

      {/* 원값을 숨기지 않되(감사 보존), 증거 부족으로 화면 판정을 내렸음을 명시. */}
      {overridden && (
        <div
          style={{
            fontSize: 13,
            color: "var(--warn)",
            background: "var(--warn-weak)",
            border: "1px solid var(--warn-border)",
            borderRadius: 6,
            padding: "9px 12px",
            marginTop: 10,
          }}
        >
          오라클 <Strong>원값 {rawBadge.label}</Strong>(감사용 보존) — 증거 3조건이 불완비라 화면 판정은 보수적으로{" "}
          <Strong>{dbadge.label}</Strong>로 내렸어요(절대규칙 5).
        </div>
      )}

      <div className="card" style={{ marginTop: 16 }}>
        <h3>무엇이 관찰됐나</h3>
        {proven ? (
          <p style={bodyText}>
            <Strong>{record.subjects.evaluatedRef}</Strong>(무관한 사용자)로 <Strong>{record.subjects.ownerRef}</Strong>
            의 자원(<code>{record.resourceRef}</code>)을 교차 요청했을 때,{" "}
            <Strong>{record.subjects.ownerRef}에만 심은 비자명 표식</Strong>이 응답 필드에 그대로 나타났고 요청
            반사·공통 템플릿은 배제됐어요 — "{record.subjects.evaluatedRef}가 {record.subjects.ownerRef}의 실제 자원을
            받았다"는 신호예요.
          </p>
        ) : (
          <>
            <p style={{ ...bodyText, marginBottom: 8 }}>
              <Strong>{record.subjects.evaluatedRef}</Strong>로 <Strong>{record.subjects.ownerRef}</Strong>의 자원(
              <code>{record.resourceRef}</code>)을 교차 요청했어요. 아래 신호 중{" "}
              <Strong>핵심 증거가 비어 있어 교차 데이터 접근을 단정하지 않아요</Strong>(증거 없는 취약 관찰 금지).
            </p>
            <ul style={listText}>
              <li>확인된 신호: {confirmedSignals.map((s) => s.label).join(" · ") || "없음"}</li>
              <li>
                미확인: {openSignals.map((s) => `${s.label}(${SIGNAL_LABEL[record.signals[s.key]]})`).join(" · ")}
              </li>
            </ul>
          </>
        )}
      </div>

      <div className="card">
        <h3>
          판정 <Sub>· 세 체크(정책·신원·데이터)</Sub>
        </h3>
        <div className="row" style={{ gap: 8, flexWrap: "wrap", marginBottom: 6 }}>
          <Pill tone={record.checks.policy === "yes" ? undefined : "warn"}>
            정책 {SIGNAL_LABEL[record.checks.policy]}
          </Pill>
          <Pill tone={record.checks.identity === "yes" ? undefined : "warn"}>
            신원 {SIGNAL_LABEL[record.checks.identity]}
          </Pill>
          <Pill tone={record.checks.data === "yes" ? undefined : "warn"}>
            데이터 {SIGNAL_LABEL[record.checks.data]}
          </Pill>
          <Pill tone="muted">정책 관찰: {record.accessPolicyResult.replace(/_/g, " ")}</Pill>
        </div>
        <div className="hint">
          사유 코드: <code>{record.reasonCodes.join(" · ")}</code>
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
              {record.comparison.map((rowData) => {
                // 화면 판정이 확정이 아니면 행의 'vuln' 도 보수적으로 '판단 불가'로(상단 배지와 일관).
                const shownRow = displayComparisonRow(display, rowData.row);
                const cell = ROW_CELL[shownRow];
                return (
                  <tr
                    key={rowData.label}
                    style={shownRow === "vuln" ? { background: "var(--danger-weak)" } : undefined}
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
        {display !== "confirmed" && record.comparison.some((c) => c.row === "vuln") && (
          <div className="hint">
            화면 판정이 <Strong>{dbadge.label}</Strong>라, 표의 '취약' 행은 보수적으로 <Strong>판단 불가</Strong>로
            표시했어요(상단 판정과 일관 — 증거 불완비).
          </div>
        )}
      </div>

      <div className="card">
        <h3>
          증거 <Sub>· 이름·신호만 저장(값·본문 마스킹)</Sub>
        </h3>
        <p style={{ ...bodyText, marginBottom: 10 }}>
          표식이 산 응답 필드:{" "}
          {record.canaryFields.map((f) => (
            <code key={f}>{f}</code>
          ))}{" "}
          · 값은 <span style={{ color: "var(--danger)" }}>[REDACTED]</span> (절대규칙 2 — canary 값·본문·쿠키 저장 안
          함).
        </p>
        <div className="row" style={{ gap: 6, flexWrap: "wrap", marginBottom: 8 }}>
          {COMPARISON_SIGNALS.map((s) => (
            <Pill key={s.key} tone={record.signals[s.key] === "yes" ? undefined : "warn"}>
              {s.label} {SIGNAL_LABEL[record.signals[s.key]]}
            </Pill>
          ))}
        </div>
        <div className="hint">
          트래픽 증거 ref: <code>{record.trafficEvidenceRefs.join(", ") || "—"}</code> (본문 아님, 참조 id만).
        </div>
      </div>

      <div className="card">
        <h3>왜 이 판정인가</h3>
        {display === "confirmed" ? (
          <>
            <ul style={listText}>
              <li>
                <Strong>{record.subjects.evaluatedRef}</Strong> 응답에{" "}
                <Strong>{record.subjects.ownerRef}의 표식</Strong>이 그대로 담겼다(실제로 받았음)
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
              화면 판정 <Strong>{dbadge.label}</Strong>
              {overridden ? `(오라클 원값 ${rawBadge.label})` : ""} — 아래 증거가 비어 확정으로 올리지 않아요.
            </p>
            <ul style={listText}>
              {missing.length > 0 ? (
                missing.map((m) => <li key={m}>{m}</li>)
              ) : (
                <li>부족 신호 없음(레코드 신호를 다시 확인)</li>
              )}
            </ul>
            <div className="hint">
              미실행·판단 불가를 <Strong>"취약 없음"·"안전"으로 적지 않아요</Strong>(절대규칙 5). 확인 못 한 건 확인 못
              했다고 남겨요.
            </div>
          </>
        )}
      </div>

      <div className="card">
        <h3>
          통제변수 <Sub>· 재현·감사</Sub>
        </h3>
        <Meta k="오라클 원값" v={record.oracleValue} />
        <Meta k="정책 버전" v={record.control.policyVersion} />
        <Meta k="정책=정본" v={record.control.hintMatchesCanonical ? "일치" : "불일치(정책 무효)"} />
        <Meta k="기대 접근" v={record.control.expectedAccess} />
        <Meta k="reset 세대" v={record.control.resetGeneration} />
        <Meta k="관찰 시각" v={new Date(record.control.observedAt).toISOString()} />
        <Meta k="모델 태그" v={record.control.modelTag} />
        <Meta
          k="주체 상태"
          v={`${record.subjects.ownerRef}=${record.subjects.ownerStatus} · ${record.subjects.evaluatedRef}=${record.subjects.evaluatedStatus}`}
        />
      </div>

      <div className="card" style={{ marginBottom: 0 }}>
        <h3>고치려면</h3>
        <p style={bodyText}>
          자원 조회 시 <code>자원.소유자 == 세션 주체</code>를 서버에서 검사하세요. 번호 추측을 막으려면 순차 ID 대신
          추측 어려운 식별자를 쓰는 것도 방법이에요.
        </p>
      </div>
    </>
  );
}

/** ===== 발견 상세 (판정 레코드) ===== */
export default function FindingScreen() {
  const go = useGo();
  const { data, loading, error } = useApiResource(() => getFinding(null), []);

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

          {loading || !data ? (
            <div style={{ textAlign: "center", padding: "40px 0", color: "var(--muted)" }}>
              {error ? `불러오지 못했어요. (${error})` : "불러오는 중…"}
            </div>
          ) : (
            <FindingDetail record={data} />
          )}
        </div>
      </div>
    </Screen>
  );
}
