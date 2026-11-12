"use client";

// 발견 상세 화면 (구조화 판정 레코드 읽기, 지금은 예시). verdict-record §2~§4 shape.
// 데이터는 lib/diagnosis/finding seam(getFinding·getFindingHeld)에서만 온다. 판정·보수 하향·부족 증거 산출은
// 전부 seam(lib)이고 화면은 표현만 한다(finding.test.ts 가 로직을 이미 검증). 표현/데이터 분리.
// 마스킹(절대규칙 2·8 하드 규칙, verdict-record §3): 응답 본문·canary 값·자격 원문·raw URL 은 seam 에 애초에 없다 —
//   화면은 필드명·신호 enum·증거 ref·주체 라벨·논리 resource_ref 만 그린다. 값은 넣지 않는다.
// ※ live 점등은 판정 레코드 읽기 API 연동 시 getFinding(findingId) 만 교체(이 화면은 안 바뀐다). 지금은 예시.

import type { CSSProperties } from "react";
import { useEffect, useState } from "react";

import {
  allEvidenceUnknown,
  COMPARISON_SIGNALS,
  type ComparisonRow,
  confirmedEvidenceComplete,
  displayComparisonRow,
  displayVerdict,
  type FindingRecord,
  getFinding,
  getFindingHeld,
  heldBeforeSend,
  missingEvidence,
  notTransmitted,
  ORACLE_BADGE,
  type OracleValue,
  SIGNAL_LABEL,
  type Signal,
} from "@/lib/diagnosis/finding";
import { SEVERITY_LABEL, type Severity } from "@/lib/diagnosis-types";

import { cx } from "../../lib/cx";
import { Icon } from "../../ui/icons";
import s from "../../workbench.module.css";
import { UrlHead } from "./urlParts";

/**
 * OracleValue → 색. ORACLE_BADGE 의 verdict 키("ok/sus/und")는 옛 VerdictBadge 스타일 키일 뿐 색이 아니다 —
 * 색은 oracle 의미로 직접 정한다(confirmed=취약 확정이라 빨강, allowed=정책상 허용이라 초록).
 * 접근통제 지도·커버리지와 같은 팔레트(실재 workbench 토큰, 라이트·다크 자동).
 */
const VERDICT_STYLE: Record<OracleValue, CSSProperties> = {
  confirmed: { background: "var(--crit-bg)", color: "var(--crit)", borderColor: "var(--crit)" },
  suspected: { background: "var(--mid-bg)", color: "var(--mid)", borderColor: "var(--mid)" },
  undecidable: { background: "var(--und-bg)", color: "var(--und)", borderColor: "var(--rule-2)" },
  allowed: { background: "var(--ok-bg)", color: "var(--ok)", borderColor: "var(--ok)" },
};
/** 신호 3값 색 — yes(충족)=초록, no(불충족)=주황(주의), unknown(불명)=회색. 모름을 아님으로 접지 않는다. */
const SIGNAL_STYLE: Record<Signal, CSSProperties> = {
  yes: { background: "var(--ok-bg)", color: "var(--ok)", borderColor: "var(--ok)" },
  no: { background: "var(--mid-bg)", color: "var(--mid)", borderColor: "var(--mid)" },
  unknown: { background: "var(--und-bg)", color: "var(--und)", borderColor: "var(--rule-2)" },
};
/** 비교 행 표시 판정 색(ok/sus/und/vuln). */
const ROW_STYLE: Record<ComparisonRow["row"], CSSProperties> = {
  ok: { background: "var(--ok-bg)", color: "var(--ok)", borderColor: "var(--ok)" },
  sus: { background: "var(--mid-bg)", color: "var(--mid)", borderColor: "var(--mid)" },
  und: { background: "var(--und-bg)", color: "var(--und)", borderColor: "var(--rule-2)" },
  vuln: { background: "var(--crit-bg)", color: "var(--crit)", borderColor: "var(--crit)" },
};
const ROW_LABEL: Record<ComparisonRow["row"], string> = { ok: "정상", sus: "의심", und: "판단 불가", vuln: "취약" };
const SEV_STYLE: Record<Severity, CSSProperties> = {
  critical: { background: "var(--crit-bg)", color: "var(--crit)", borderColor: "var(--crit)" },
  high: { background: "var(--hi-bg)", color: "var(--hi)", borderColor: "var(--hi)" },
  medium: { background: "var(--mid-bg)", color: "var(--mid)", borderColor: "var(--mid)" },
  low: { background: "var(--lo-bg)", color: "var(--lo)", borderColor: "var(--lo)" },
};
/** access_policy_result enum → 표시 글(밑줄 제거). */
const POLICY_RESULT_LABEL: Record<string, string> = {
  위반_후보: "위반 후보",
  위반_미관찰: "위반 미관찰",
  판정_불가: "판정 불가",
};

// ── 인라인 스타일(뼈대). 색은 위 STYLE, 토큰은 workbench.module.css 의 것만 쓴다.
const chipBase: CSSProperties = {
  display: "inline-flex",
  alignItems: "center",
  gap: 5,
  padding: "3px 9px",
  borderRadius: 999,
  border: "1px solid",
  fontSize: 12,
};
const badgeBase: CSSProperties = {
  display: "inline-flex",
  alignItems: "center",
  padding: "2px 10px",
  borderRadius: 6,
  border: "1px solid",
  fontSize: 13,
  fontWeight: 600,
};
const chipRow: CSSProperties = { display: "flex", flexWrap: "wrap", gap: 6, marginTop: 6 };
const tableStyle: CSSProperties = { borderCollapse: "collapse", width: "100%", fontSize: 13, marginTop: 6 };
const thCell: CSSProperties = {
  textAlign: "left",
  padding: "6px 10px",
  borderBottom: "1px solid var(--rule)",
  color: "var(--ink-2)",
  fontWeight: 600,
};
const tdCell: CSSProperties = { padding: "6px 10px", borderBottom: "1px solid var(--rule)", verticalAlign: "top" };
const sampleRow: CSSProperties = { display: "flex", flexWrap: "wrap", gap: 6, marginBottom: 4 };

/** oracle 원값 보존 배지(감사용). 화면 판정과 다를 때 함께 보인다. */
function VerdictBadge({ value }: { value: OracleValue }) {
  return (
    <span data-verdict={value} style={{ ...badgeBase, ...VERDICT_STYLE[value] }}>
      {ORACLE_BADGE[value].label}
    </span>
  );
}

/** 신호 칩(라벨 + 충족/불충족/불명). */
function SignalChip({ label, value }: { label: string; value: Signal }) {
  return (
    <span style={{ ...chipBase, ...SIGNAL_STYLE[value] }} data-signal={value}>
      {label} <b>{SIGNAL_LABEL[value]}</b>
    </span>
  );
}

const fmtTime = (ms: number): string => {
  try {
    return `${new Date(ms).toISOString().replace("T", " ").slice(0, 19)} UTC`;
  } catch {
    return "—";
  }
};

/** 한 발견 레코드 상세. seam 이 준 masked 필드만 그린다(절대규칙 2·4). */
function FindingDetail({ r }: { r: FindingRecord }) {
  const shown = displayVerdict(r); // 증거 부족이면 보수적으로 하향된 화면 판정
  const downgraded = shown !== r.oracleValue;
  const missing = missingEvidence(r);
  const held = heldBeforeSend(r);

  return (
    <>
      <section className={s.isec} aria-label="판정">
        <h2>판정</h2>
        <div style={chipRow}>
          <VerdictBadge value={shown} />
          <span style={{ ...chipBase, ...SEV_STYLE[r.severity] }}>{SEVERITY_LABEL[r.severity]}</span>
          <span style={{ ...chipBase, background: "var(--well)", color: "var(--ink-2)", borderColor: "var(--rule)" }}>
            {r.vulnClass}
          </span>
          <span style={{ ...chipBase, background: "var(--well)", color: "var(--ink-2)", borderColor: "var(--rule)" }}>
            {r.axis === "horizontal" ? "수평" : "수직"}
          </span>
        </div>
        {downgraded ? (
          <p className={s.help}>
            오라클 원값: <b>{ORACLE_BADGE[r.oracleValue].label}</b> — 감사용으로 보존합니다. 화면 판정은 증거가 부족해
            보수적으로 내렸습니다(원값을 숨기지 않습니다).
          </p>
        ) : null}
        <p className={s.help}>
          정책 관찰: {POLICY_RESULT_LABEL[r.accessPolicyResult] ?? r.accessPolicyResult} · 논리 자원{" "}
          <span className={s.mono}>{r.resourceRef}</span>
        </p>
        <h3>세 체크</h3>
        <div style={chipRow}>
          <SignalChip label="정책" value={r.checks.policy} />
          <SignalChip label="신원" value={r.checks.identity} />
          <SignalChip label="데이터" value={r.checks.data} />
        </div>
        {r.reasonCodes.length ? <p className={s.help}>사유 코드: {r.reasonCodes.join(", ")}</p> : null}
        {/* 증거 부족·완전 미실행·E² 송신 전 차단 서사(절대규칙 5 — 미실행을 '없음'으로 기록하지 않음) */}
        {allEvidenceUnknown(r) ? (
          <div className={cx(s.note, s.warn)} role="note">
            <Icon name="alert" />
            <span>세 체크·비교 신호가 모두 불명입니다(완전 미실행). ‘취약 없음’이 아니라 ‘판단 불가’입니다.</span>
          </div>
        ) : null}
        {held ? (
          <div className={cx(s.note, s.warn)} role="note">
            <Icon name="alert" />
            <span>E②가 송신 전에 차단했습니다(미실행) → 전송·응답 없음 → 증거 없음 → 판단 불가.</span>
          </div>
        ) : null}
        {!confirmedEvidenceComplete(r) && missing.length ? (
          <>
            <h3>부족한 증거</h3>
            <ul className={s.policy} aria-label="부족한 증거">
              {missing.map((m) => (
                <li key={m}>
                  <span>{m}</span>
                </li>
              ))}
            </ul>
          </>
        ) : null}
        {r.intent ? <p className={s.help}>검사 의도: {r.intent}</p> : null}
        {r.stateChanging ? (
          <p className={s.help}>상태 변경 요청입니다(수평 읽기 밖 — 승인·격리 시험환경 필요, destructive-last).</p>
        ) : null}
      </section>

      <section className={s.isec} aria-label="비교 신호">
        <h2>비교 신호 (6)</h2>
        <p className={s.help}>여섯 신호가 모두 충족해야 교차 데이터 접근을 단정합니다(e3-oracle §3(c)).</p>
        <div style={chipRow}>
          {COMPARISON_SIGNALS.map((sig) => (
            <SignalChip key={sig.key} label={sig.label} value={r.signals[sig.key]} />
          ))}
        </div>
        <p className={s.help}>
          표식 필드: {r.canaryFields.length ? r.canaryFields.join(", ") : "없음"} (필드명만 — 값 없음)
        </p>
      </section>

      <section className={s.isec} aria-label="다계정 비교">
        <h2>다계정 비교</h2>
        {r.comparison.length ? (
          <table style={tableStyle} aria-label="다계정 비교 표">
            <thead>
              <tr>
                <th scope="col" style={thCell}>
                  주체
                </th>
                <th scope="col" style={thCell}>
                  논리 자원
                </th>
                <th scope="col" style={thCell}>
                  관측(신호 요약)
                </th>
                <th scope="col" style={thCell}>
                  판정
                </th>
              </tr>
            </thead>
            <tbody>
              {r.comparison.map((row) => {
                const disp = displayComparisonRow(shown, row.row);
                return (
                  <tr key={`${row.label}/${row.resourceRef}`}>
                    <td style={tdCell}>
                      {row.label}
                      <small className={s.help} style={{ display: "block" }}>
                        {row.roleNote}
                      </small>
                    </td>
                    <td style={tdCell}>
                      <span className={s.mono}>{row.resourceRef}</span>
                    </td>
                    <td style={tdCell}>{row.observation}</td>
                    <td style={tdCell}>
                      <span style={{ ...chipBase, ...ROW_STYLE[disp] }} data-row={disp}>
                        {ROW_LABEL[disp]}
                      </span>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        ) : (
          <p className={s.help}>전송·응답이 없어 비교가 없습니다{notTransmitted(r) ? "(전송 없음)" : ""}.</p>
        )}
      </section>

      <section className={s.isec} aria-label="통제변수·근거">
        <h2>통제변수 · 근거</h2>
        <ul className={s.policy}>
          <li>
            <span>소유자</span>
            <span>
              {r.subjects.ownerRef} ({r.subjects.ownerStatus})
            </span>
          </li>
          <li>
            <span>평가 주체</span>
            <span>
              {r.subjects.evaluatedRef} ({r.subjects.evaluatedStatus})
            </span>
          </li>
          <li>
            <span>정책 버전</span>
            <span className={s.mono}>{r.control.policyVersion}</span>
          </li>
          <li>
            <span>리셋 세대</span>
            <span className={s.mono}>{r.control.resetGeneration}</span>
          </li>
          <li>
            <span>관찰 시각</span>
            <span className={s.mono}>{fmtTime(r.control.observedAt)}</span>
          </li>
          <li>
            <span>모델 태그</span>
            <span className={s.mono}>{r.control.modelTag}</span>
          </li>
          <li>
            <span>정책 일치</span>
            <span>{r.control.hintMatchesCanonical ? "예" : "아니오"}</span>
          </li>
          <li>
            <span>기대 접근</span>
            <span>{r.control.expectedAccess}</span>
          </li>
        </ul>
        <p className={s.help}>
          전송 근거 ref: {r.trafficEvidenceRefs.length ? r.trafficEvidenceRefs.join(", ") : "없음"}
        </p>
      </section>
    </>
  );
}

type Sample = "confirmed" | "held";

/** 발견 상세 (예시). 두 예시(확정 / 송신 전 차단)를 전환해 보인다. */
export function UrlFindingLive() {
  const [sample, setSample] = useState<Sample>("confirmed");
  const [record, setRecord] = useState<FindingRecord | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let live = true;
    setRecord(null);
    setFailed(false);
    // TODO(이경준): 판정 레코드 읽기 API 연동 시 현재 finding id 를 넘겨 실 레코드로 점등한다(지금은 예시).
    const load = sample === "held" ? getFindingHeld(null) : getFinding(null);
    load
      .then((r) => {
        if (live) setRecord(r);
      })
      .catch(() => {
        if (live) setFailed(true);
      });
    return () => {
      live = false;
    };
  }, [sample]);

  return (
    <>
      <UrlHead title="발견 상세" crumb="발견 상세" />
      <div className={s.vb}>
        <div className={s.vm}>
          {/* 예시 레코드 전환(확정 사례 / E② 송신 전 차단 사례). 토글 버튼(aria-pressed)이라 그룹 role 은 두지 않는다. */}
          <div style={sampleRow}>
            <button
              type="button"
              className={cx(s.btn, s.sm, sample === "confirmed" && s.pri)}
              aria-pressed={sample === "confirmed"}
              onClick={() => setSample("confirmed")}
            >
              확정 사례
            </button>
            <button
              type="button"
              className={cx(s.btn, s.sm, sample === "held" && s.pri)}
              aria-pressed={sample === "held"}
              onClick={() => setSample("held")}
            >
              송신 전 차단 사례
            </button>
          </div>
          <div className={cx(s.note, s.warn)} role="note">
            <Icon name="alert" />
            <span>예시 데이터입니다. 실제 발견은 판정 레코드 API 연결 후 나옵니다.</span>
          </div>
          <div className={s.note} role="note">
            <Icon name="info" />
            <span>응답 본문·표식 값·자격 원문은 표시하지 않습니다 — 필드명·신호·증거 ref 만 보입니다.</span>
          </div>
          {failed ? <p className={s.empty}>발견을 불러오지 못했습니다.</p> : null}
          {!record && !failed ? <p className={s.empty}>불러오는 중…</p> : null}

          {record ? (
            <>
              {/* 발견 제목 — 화면 h1("발견 상세")과 별개라 heading 으로 올리지 않는다(섹션 h2 유지). */}
              <p style={{ fontSize: 18, fontWeight: 700, color: "var(--ink)", margin: "6px 0 0" }}>{record.title}</p>
              <FindingDetail r={record} />
            </>
          ) : null}
        </div>
      </div>
    </>
  );
}
