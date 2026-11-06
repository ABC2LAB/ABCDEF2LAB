import { useMemo } from "react";

import { Screen } from "../components/Screen";
import { TopBar } from "../components/TopBar";
import { Pill, Strong } from "../components/ui";
import { useApiResource } from "../lib/api";
import {
  attempted,
  type CoverageData,
  coverageIssues,
  getCoverage,
  STATUS_LABEL,
  STATUS_ORDER,
  STATUS_STYLE,
  summarize,
  total,
} from "../lib/coverage";
import { useSelection } from "../lib/selection";
import { useGo } from "../nav";

const AXIS_LABEL: Record<CoverageData["areas"][number]["axis"], string> = {
  horizontal: "수평",
  vertical: "수직(후속)",
  "state-change": "상태 변경",
};

const warnBox = {
  fontSize: 13,
  color: "var(--warn)",
  background: "var(--warn-weak)",
  border: "1px solid var(--warn-border)",
  borderRadius: 6,
  padding: "10px 12px",
  marginBottom: 12,
} as const;

/**
 * 커버리지 본문(presentational) — data 하나로 렌더. 레이아웃은 여기만, 상태·집계·fail-closed 는 lib/coverage.
 */
export function CoveragePanel({ data }: { data: CoverageData }) {
  const sum = useMemo(() => summarize(data), [data]);
  const issues = useMemo(() => coverageIssues(data), [data]);

  return (
    <>
      {data.mock && (
        <div
          style={{
            fontSize: 13,
            color: "var(--muted)",
            background: "var(--surface-2)",
            border: "1px dashed var(--line-2)",
            borderRadius: 6,
            padding: "10px 12px",
            marginBottom: 14,
          }}
        >
          <Strong>프로토타입</Strong> — 커버리지 집계 API(검사계획+시도상태)는 아직 미구현. 아래는 계약 shape 예시예요.
          실 연동 시 <Strong>getCoverage 한 함수만</Strong> 교체하면 그대로 살아요.
        </div>
      )}

      {issues.mismatched > 0 && (
        <div style={warnBox}>
          집계 무결성 경고 — 상태 합이 검사 계획 수와 다른 영역 {issues.mismatched}개. 조용히 맞추지 않고 표시해요
          (fail-closed). 실 연동 시 어댑터가 계획↔상태합을 일치시켜야 해요.
        </div>
      )}

      <div className="search" style={{ gap: 8, marginBottom: 6, flexWrap: "wrap" }}>
        <Pill tone="muted">검사 계획 {sum.planned}</Pill>
        <Pill tone="muted">시도 {sum.attempted}</Pill>
        {sum.byStatus.confirmed > 0 && <Pill tone="warn">확정 {sum.byStatus.confirmed}</Pill>}
        {sum.byStatus.suspected > 0 && <Pill tone="warn">의심 {sum.byStatus.suspected}</Pill>}
        {sum.byStatus.undecidable > 0 && <Pill tone="warn">판정 불가 {sum.byStatus.undecidable}</Pill>}
        {sum.byStatus.untested > 0 && <Pill tone="muted">미시도 {sum.byStatus.untested}</Pill>}
      </div>
      <p style={{ fontSize: 12, color: "var(--faint)", margin: "0 0 14px" }}>
        실행 범위 {data.policyVersion} · <Strong>시도 ≠ 완료, 미시도 ≠ 취약 없음</Strong> — 아래 '미시도'는 정상색으로
        칠하지 않아요.
      </p>

      <div style={{ display: "grid", gap: 14 }}>
        {data.areas.map((area) => {
          const t = total(area);
          return (
            <div key={area.id} style={{ border: "1px solid var(--line-2)", borderRadius: 8, padding: "12px 14px" }}>
              <div style={{ display: "flex", gap: 8, alignItems: "baseline", flexWrap: "wrap", marginBottom: 8 }}>
                <Strong>{area.label}</Strong>
                <span style={{ fontSize: 12, color: "var(--faint)" }}>{AXIS_LABEL[area.axis]}</span>
                <span style={{ flex: 1 }} />
                <span style={{ fontSize: 12, color: "var(--muted)" }}>
                  시도 {attempted(area)} / 계획 {area.planned}
                </span>
              </div>
              {/* 세그먼트 바 — 상태별 비율. untested 는 점선(정상색 아님). */}
              <div style={{ display: "flex", height: 16, borderRadius: 4, overflow: "hidden", gap: 1 }}>
                {STATUS_ORDER.filter((s) => area.counts[s] > 0).map((s) => (
                  <div
                    key={s}
                    title={`${STATUS_LABEL[s]} ${area.counts[s]}`}
                    style={{ ...STATUS_STYLE[s], flex: `${area.counts[s]} 0 0`, minWidth: 6 }}
                  />
                ))}
                {t === 0 && <div style={{ flex: 1, ...STATUS_STYLE.untested }} />}
              </div>
              <div style={{ display: "flex", gap: 10, flexWrap: "wrap", marginTop: 8 }}>
                {STATUS_ORDER.filter((s) => area.counts[s] > 0).map((s) => (
                  <span
                    key={s}
                    style={{
                      fontSize: 12,
                      color: "var(--muted)",
                      display: "inline-flex",
                      gap: 5,
                      alignItems: "center",
                    }}
                  >
                    <span style={{ width: 10, height: 10, borderRadius: 2, flex: "none", ...STATUS_STYLE[s] }} />
                    {STATUS_LABEL[s]} {area.counts[s]}
                  </span>
                ))}
              </div>
            </div>
          );
        })}
      </div>

      <div className="leg" style={{ marginTop: 14 }}>
        {STATUS_ORDER.map((s) => (
          <span key={s}>
            <span className="dot" style={{ ...STATUS_STYLE[s] }} />
            {STATUS_LABEL[s]}
          </span>
        ))}
      </div>

      <div className="hint">
        <Strong>조회 안 함 ≠ 없음</Strong>이에요 — '미시도'(검사 계획 있으나 안 해봄)·'기록 없음'(조회했으나 근거
        0)·'판정 불가'는 서로 다른 칸이고, 어느 것도 <Strong>"취약 없음"으로 칠하지 않아요</Strong>(절대규칙 5).
        '시도'는 완료가 아니라 검사를 걸어본 수예요.
      </div>
    </>
  );
}

/** ===== 커버리지 (검사 범위·결과 시각화) ===== */
export default function CoverageScreen() {
  const go = useGo();
  const { taskId } = useSelection();
  const { data, loading, error } = useApiResource(() => getCoverage(taskId), [taskId]);

  return (
    <Screen id="coverage">
      <div className="frame">
        <TopBar current="history" />
        <div style={{ padding: "22px 24px" }}>
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
            › <span>커버리지</span>
          </div>
          <h1 className="big" style={{ marginBottom: 4 }}>
            커버리지
          </h1>
          <p className="lead">
            무엇을 <Strong>얼마나 검사했는지</Strong>를 영역별로 봐요. 안 해본 건 안 해봤다고(미시도) 남기고, 검사했는데
            결론을 못 낸 건(판정 불가) 따로 표시해요.
          </p>

          {loading || !data ? (
            <div style={{ textAlign: "center", padding: "40px 0", color: "var(--muted)" }}>
              {error ? `불러오지 못했어요. (${error})` : "불러오는 중…"}
            </div>
          ) : (
            <CoveragePanel data={data} />
          )}
        </div>
      </div>
    </Screen>
  );
}
