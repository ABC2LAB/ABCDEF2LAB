"use client";

// 기존 workflow snapshot의 관찰 커버리지. 집계·정규화는 lib seam, 기존 표시 구조를 재사용한다.

import type { CSSProperties } from "react";
import { useMemo } from "react";

import {
  attempted,
  type CoverageArea,
  type CoverageData,
  type CoverageStatus,
  coverageIssues,
  parseCoverage,
  WORKFLOW_COUNT_LABEL,
  STATUS_LABEL,
  STATUS_ORDER,
  summarize,
  total,
} from "@/lib/diagnosis/coverage";

import { useUrl } from "../../live/urlDiagnosis";
import { cx } from "../../lib/cx";
import { Icon } from "../../ui/icons";
import s from "../../workbench.module.css";
import { UrlHead } from "./urlParts";

/**
 * status → 색(인라인 React.CSSProperties). coverage.ts 의 STATUS_STYLE 은 옛 abc2lab.css 토큰
 * (--danger·--viol·--teal·--surface-2·--line-2·--faint)을 참조해 지금 안 먹는다. 실재하는 workbench 토큰으로
 * 새로 매핑하고(라이트·다크 자동), 접근통제 지도(UrlAccessMapLive)와 같은 팔레트로 맞춘다.
 * 정상색(초록)은 아무 상태에도 없다 — 미시도·기록없음·판정불가를 '안전'으로 칠하지 않는다(절대규칙 5).
 */
const STATUS_STYLE: Record<CoverageStatus, CSSProperties> = {
  confirmed: { background: "var(--crit-bg)", color: "var(--crit)", borderColor: "var(--crit)" },
  suspected: { background: "var(--mid-bg)", color: "var(--mid)", borderColor: "var(--mid)" },
  undecidable: { background: "var(--und-bg)", color: "var(--und)", borderColor: "var(--rule-2)" },
  in_progress: { background: "var(--lo-bg)", color: "var(--lo)", borderColor: "var(--lo)" },
  blank: { background: "var(--well)", color: "var(--ink-3)", borderColor: "var(--rule)" },
  // 미시도는 정상색이 아님 — 점선·빈 칸으로 '시도 안 함'을 드러낸다(절대규칙 5).
  untested: { background: "transparent", color: "var(--ink-3)", borderColor: "var(--rule-2)", borderStyle: "dashed" },
  na: { background: "var(--well)", color: "var(--ink-3)", borderColor: "var(--rule)" },
};

const AXIS_LABEL: Record<CoverageArea["axis"], string> = {
  workflow: "워크플로 관찰",
  horizontal: "수평",
  vertical: "수직",
  "state-change": "상태 변경",
};

// ── 인라인 스타일. 색은 STATUS_STYLE, 토큰은 workbench.module.css 의 것만 쓴다.
const chipRow: CSSProperties = { display: "flex", flexWrap: "wrap", gap: 6, marginTop: 6 };
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
const areaBox: CSSProperties = { marginTop: 14 };
const areaHead: CSSProperties = { display: "flex", flexWrap: "wrap", alignItems: "baseline", gap: 8 };
const areaName: CSSProperties = { fontWeight: 600, color: "var(--ink)" };
const axisChip: CSSProperties = {
  display: "inline-flex",
  padding: "1px 7px",
  borderRadius: 999,
  border: "1px solid var(--rule)",
  background: "var(--well)",
  color: "var(--ink-3)",
  fontSize: 11,
};
const barWrap: CSSProperties = {
  display: "flex",
  alignItems: "stretch",
  gap: 2,
  width: "100%",
  height: 18,
  marginTop: 6,
};
const barSeg: CSSProperties = { borderRadius: 3, border: "1px solid", minWidth: 6 };

/** 상태별 count 칩(색으로 구분). count 0 은 생략. */
function StatusChips({ counts }: { counts: Record<CoverageStatus, number> }) {
  const shown = STATUS_ORDER.filter((st) => counts[st] > 0);
  if (!shown.length) return <p className={s.help}>집계된 상태가 없습니다.</p>;
  return (
    <div style={chipRow}>
      {shown.map((st) => (
        <span key={st} style={{ ...chipBase, ...STATUS_STYLE[st] }}>
          {STATUS_LABEL[st]} <b>{counts[st]}</b>
        </span>
      ))}
    </div>
  );
}

/** 색 범례(§4.2(3) 결 — 색을 글로도 설명). */
function Legend() {
  return (
    <ul style={legendList} aria-label="범례">
      {STATUS_ORDER.map((st) => (
        <li key={st} style={legendItem}>
          <span aria-hidden="true" style={{ ...swatch, ...STATUS_STYLE[st] }} />
          <span>{STATUS_LABEL[st]}</span>
        </li>
      ))}
    </ul>
  );
}

/** 한 영역의 세그먼트 바(상태별 비율). 미시도는 점선·빈 칸으로 '시도 안 함'이 드러난다. */
function CoverageBar({ a }: { a: CoverageArea }) {
  const t = total(a);
  const shown = STATUS_ORDER.filter((st) => a.counts[st] > 0);
  return (
    <div
      style={barWrap}
      role="img"
      aria-label={`${a.label} 커버리지: ${shown.map((st) => `${STATUS_LABEL[st]} ${a.counts[st]}`).join(", ") || "집계 없음"}`}
    >
      {shown.map((st) => (
        <span
          key={st}
          data-status={st}
          title={`${STATUS_LABEL[st]} ${a.counts[st]}`}
          style={{ ...barSeg, ...STATUS_STYLE[st], flexGrow: a.counts[st] }}
        />
      ))}
      {t === 0 ? <span style={{ ...barSeg, ...STATUS_STYLE.blank, flexGrow: 1 }} /> : null}
    </div>
  );
}

/** 한 영역 블록(제목·축·수치 + 바 + 텍스트 분해). */
function AreaBlock({ a }: { a: CoverageArea }) {
  const shown = STATUS_ORDER.filter((st) => a.counts[st] > 0);
  return (
    <div style={areaBox}>
      <div style={areaHead}>
        <span style={areaName}>{a.label}</span>
        <span style={axisChip}>{AXIS_LABEL[a.axis]}</span>
        <small className={s.help}>
          관찰·시도 기록 {attempted(a)} / 표시 끝점 {total(a)}
        </small>
      </div>
      <CoverageBar a={a} />
      <p className={s.help}>{shown.map((st) => `${STATUS_LABEL[st]} ${a.counts[st]}`).join(" · ") || "집계 없음"}</p>
    </div>
  );
}

/** 실제 task/job 결속이 있는 workflow만 표시한다. */
export function UrlCoverageLive() {
  const job = useUrl((st) => st.job);
  const available = !!job?.task_id && !!job.input.workflow_mode;
  const flow = useUrl((st) => st.flow);
  const flowVerified = useUrl((st) => st.flowVerified);
  const flowUncertain = useUrl((st) => st.flowUncertain);
  const flowError = useUrl((st) => st.flowError);
  // UrlFeeds가 이미 읽은 같은 workflow만 투영한다. 숨겨진 화면에서 별도 폴링하지 않는다.
  const { data, failed } = useMemo<{ data: CoverageData | null; failed: boolean }>(() => {
    if (!available || !job) return { data: null, failed: false };
    if (flowError || flowUncertain || (flow && !flowVerified)) return { data: null, failed: true };
    if (!flow || !flowVerified) return { data: null, failed: false };
    try {
      return { data: parseCoverage({ workflow: flow }, job), failed: false };
    } catch {
      return { data: null, failed: true };
    }
  }, [available, job, flow, flowVerified, flowUncertain, flowError]);

  const sum = data ? summarize(data) : null;
  const issues = data ? coverageIssues(data) : null;

  return (
    <>
      <UrlHead title="커버리지" crumb="커버리지" />
      <div className={s.vb}>
        <div className={s.vm}>
          {data?.mock ? (
            <div className={cx(s.note, s.warn)} role="note">
              <Icon name="alert" />
              <span>예시 데이터입니다. 실제 커버리지는 집계 API 연결 후 나옵니다.</span>
            </div>
          ) : null}
          {failed ? <p className={s.empty}>커버리지를 불러오지 못했습니다.</p> : null}
          {!available ? (
            <p className={s.empty}>실제 워크플로 진단을 선택하세요. 미실행을 안전으로 판단하지 않습니다.</p>
          ) : null}
          {available && !data && !failed ? <p className={s.empty}>불러오는 중…</p> : null}

          {/* 집계 무결성 경고 — 계획과 상태 합이 다른 영역을 숨기지 않고 센다(fail-closed) */}
          {data && issues && issues.mismatched > 0 ? (
            <div className={cx(s.note, s.warn)} role="alert">
              <Icon name="alert" />
              <span>
                집계 불일치: 서버 누계 또는 표시 집계가 맞지 않는 항목 {issues.mismatched}개. 숨기지 않고 표시합니다.
              </span>
            </div>
          ) : null}

          {data && sum ? (
            <section className={s.isec} aria-label="커버리지 요약">
              <h2>검사 커버리지 요약</h2>
              <p className={s.help}>
                현재 workflow의 방문·선택·응답 관찰입니다. ‘미시도’는 조회 안 함이며 ‘취약 없음·안전’이 아닙니다. 방문과
                HTTP 응답은 취약점 검사 완료나 E③ 판정을 뜻하지 않습니다.
              </p>
              <div style={chipRow}>
                <span style={neutralChip}>
                  표시 끝점 <b>{sum.planned}</b>
                </span>
                <span style={neutralChip}>
                  관찰·시도 기록 <b>{sum.attempted}</b>
                </span>
              </div>
              <StatusChips counts={sum.byStatus} />
              <Legend />
            </section>
          ) : null}

          {data?.workflowCounts ? (
            <section className={s.isec} aria-label="워크플로 서버 누계">
              <h2>워크플로 서버 누계</h2>
              <p className={s.help}>
                관찰 갱신판 {data.snapshotRevision} · 상태{" "}
                {
                  (
                    {
                      crawling: "탐색 중",
                      awaiting_selection: "대상 선택 대기",
                      diagnosing: "진단 중",
                      completed: "마감",
                      stopped: "중지",
                      failed: "실패",
                      unknown: "불명",
                    } as Record<string, string>
                  )[data.stage ?? "unknown"]
                }{" "}
                · 누계와 표시 끝점 수는 다를 수 있습니다.
              </p>
              <div style={chipRow}>
                {Object.entries(WORKFLOW_COUNT_LABEL).map(([key, label]) => (
                  <span key={key} style={neutralChip}>
                    {label} <b>{data.workflowCounts?.[key] ?? "불명"}</b>
                  </span>
                ))}
              </div>
            </section>
          ) : null}
          {data?.endpoints ? (
            <section className={s.isec} aria-label="끝점별 관찰">
              <h2>끝점별 관찰</h2>
              {!data.endpoints.length ? (
                <p className={s.empty}>표시할 끝점이 없습니다. 검사 완료나 안전을 뜻하지 않습니다.</p>
              ) : null}
              {data.endpoints.map((e) => (
                <article key={e.id} style={areaBox}>
                  <p>
                    <code>
                      {e.method} {e.path || "경로 불명"}
                    </code>{" "}
                    · {e.source}
                  </p>
                  <p>
                    {e.visited === null ? "방문 불명" : e.visited ? "방문함" : "미방문"} ·{" "}
                    {e.selected === null ? "선택 불명" : e.selected ? "선택됨" : "미선택"} · 응답{" "}
                    {e.status ?? "관찰 없음"} · {STATUS_LABEL[e.coverage]}
                  </p>
                  {e.observationError ? <p className={s.help}>{e.observationError}</p> : null}
                </article>
              ))}
            </section>
          ) : null}
          {data ? (
            <section className={s.isec} aria-label="영역별 커버리지">
              <h2>영역별 커버리지</h2>
              {data.areas.length ? (
                data.areas.map((a) => <AreaBlock key={a.id} a={a} />)
              ) : (
                <p className={s.empty}>집계된 검사 영역이 없습니다.</p>
              )}
              {/* 스코프 표기 — 정책 버전을 변형·절단 없이 그대로(감사 표기와 1:1) */}
              <p className={s.help}>
                자료 범위: 워크플로 관찰 · E③ 판정 아님 · 정책 판은 이 자료에서 제공하지 않습니다.
              </p>
            </section>
          ) : null}
        </div>
      </div>
    </>
  );
}
