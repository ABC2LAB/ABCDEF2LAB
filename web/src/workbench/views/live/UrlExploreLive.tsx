"use client";

// 탐색 결과 화면 (운영자 전용). 공유 워크플로 스냅샷(UrlFeeds 가 이미 읽고 Job 과 대조한 것)의 끝점에서 발견 경로 ·
// 상태 · 출처를 보인다 — 추가 조회 없음. 값(헤더 · 쿠키 · 본문 · query 값)은 애초에 없다(절대규칙 2).
// ※ §4: 운영자 전용이다. 이 화면은 내보내기 · 공유 · 모델 입력에 넣지 않는다 → export 버튼을 두지 않는다.
// ※ 탐색 경계 기록(§4 — 전달 · 차단 사유)은 서버 경로가 없어 이 화면에 두지 않는다(빈 칸으로도 만들지 않음).

import type { CSSProperties } from "react";
import { useMemo } from "react";

import { observationErrorLabel, WORKFLOW_COUNT_LABEL, workflowSourceLabel } from "@/lib/diagnosis/coverage";

import { cx } from "../../lib/cx";
import {
  changeLabel,
  discoveriesFromWorkflow,
  EVIDENCE_LABEL,
  EXPLORATION_STATUS_LABEL,
  explorationStateOf,
  summarizeWorkflowDiscoveries,
  type WorkflowDiscovery,
} from "../../live/exploration";
import { useUrl } from "../../live/urlDiagnosis";
import { Icon } from "../../ui/icons";
import s from "../../workbench.module.css";
import { UrlHead } from "./urlParts";

// ── 인라인 스타일(뼈대) — 전송 통제 정책 · 커버리지 화면과 같은 칩 · 테두리 상자. 색은 실재 workbench 토큰만.
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
const outChip: CSSProperties = {
  ...chipBase,
  background: "var(--mid-bg)",
  color: "var(--mid)",
  borderColor: "var(--mid)",
};
const plainList: CSSProperties = { listStyle: "none", margin: 0, padding: 0 };
const itemBox: CSSProperties = {
  marginTop: 10,
  border: "1px solid var(--rule)",
  borderRadius: 8,
  padding: "10px 12px",
};
const itemHead: CSSProperties = { display: "flex", flexWrap: "wrap", alignItems: "baseline", gap: 8 };

/** 수 칩 하나 (이름 + 값). */
function Stat({ label, value }: { label: string; value: number }) {
  return (
    <li style={neutralChip}>
      {label} <b>{value}</b>
    </li>
  );
}

/**
 * 상태 글: 방문(응답 코드) · 미방문 과 관찰 오류를 따로 붙인다 — 방문 전에 요청이 실패한 행(visited=false + request_failed)을
 * 시도하지 않은 행과 구분한다(#691 리뷰). 응답 코드가 없으면 만들지 않는다.
 */
function stateText(d: WorkflowDiscovery): string {
  let base = "미방문";
  if (d.visited) base = d.status !== null ? `방문 ${d.status}` : "방문 · 응답 코드 없음";
  return d.observationError ? `${base} · ${observationErrorLabel(d.observationError)}` : base;
}

/** 경로 한 줄(테두리 상자). */
function Row({ d }: { d: WorkflowDiscovery }) {
  return (
    <li style={itemBox}>
      <div style={itemHead}>
        <span className={s.mono} style={{ fontWeight: 600, color: "var(--ink)" }}>
          {d.method} {d.path ?? "형식 밖 경로"}
        </span>
        <span style={d.inScope ? neutralChip : outChip}>{d.inScope ? "범위 안" : "범위 밖"}</span>
        {d.evidence ? <span style={neutralChip}>{EVIDENCE_LABEL[d.evidence]}</span> : null}
      </div>
      <p className={s.help}>
        {stateText(d)} · {workflowSourceLabel(d.source)}
        {d.from && !d.preRegistered ? ` · 발견 위치 ${d.from}` : ""} · {changeLabel(d.change)}
        {d.duplicates ? ` · 중복 ${d.duplicates}` : ""}
      </p>
      {d.queryKeys.length || d.bodyKeys.length || d.contentType ? (
        <p className={s.help}>
          {[
            d.queryKeys.length ? `query: ${d.queryKeys.join(", ")}` : "",
            d.bodyKeys.length ? `body: ${d.bodyKeys.join(", ")}` : "",
            d.contentType ?? "",
          ]
            .filter(Boolean)
            .join(" · ")}
        </p>
      ) : null}
    </li>
  );
}

/** 탐색 결과 (운영자 전용 · 현재 Job 의 공유 워크플로 스냅샷). */
export function UrlExploreLive() {
  const job = useUrl((st) => st.job);
  const flow = useUrl((st) => st.flow);
  const flowVerified = useUrl((st) => st.flowVerified);
  const flowUncertain = useUrl((st) => st.flowUncertain);
  const flowError = useUrl((st) => st.flowError);
  const available = !!job?.task_id && !!job.input.workflow_mode;
  // 커버리지와 같은 조건: 확인되지 않았거나 다른 Job · 회차의 스냅샷이면 그리지 않는다(이전 회차 · 예시 없음)
  const { list, failed } = useMemo<{ list: WorkflowDiscovery[] | null; failed: boolean }>(() => {
    if (!available || !job) return { list: null, failed: false };
    if (flowError || flowUncertain || (flow && !flowVerified)) return { list: null, failed: true };
    if (!flow || !flowVerified) return { list: null, failed: false };
    if (
      flow.job_id !== job.id ||
      flow.task_id !== job.task_id ||
      flow.source_digest !== job.source_digest ||
      flow.run_ref !== `run-${job.id}`
    )
      return { list: null, failed: true };
    return { list: discoveriesFromWorkflow(flow), failed: false };
  }, [available, job, flow, flowVerified, flowUncertain, flowError]);
  const sum = list ? summarizeWorkflowDiscoveries(list) : null;
  const state = list && flow ? explorationStateOf(flow) : null;
  const found = list ? list.filter((d) => !d.preRegistered) : [];
  const registered = list ? list.filter((d) => d.preRegistered) : [];
  const notRun = state?.status === "NOT_RUN";
  const counts = list && flow ? flow.counts : null;
  const omitted = counts?.omitted ?? 0;
  const invalidPaths = counts?.invalid_paths ?? 0;

  return (
    <>
      <UrlHead title="탐색 결과" crumb="탐색 결과" />
      <div className={s.vb}>
        <div className={s.vm}>
          {/* §4 운영자 전용 고지 — 내보내기·공유·모델 입력 금지 */}
          <div className={s.note} role="note">
            <Icon name="info" />
            <span>발견 목록은 운영자 전용입니다. 내보내기·공유·모델 입력에 넣지 않습니다.</span>
          </div>
          {!available ? <p className={s.empty}>실제 워크플로 진단을 선택하세요.</p> : null}
          {failed ? (
            <p className={s.empty} role="alert">
              탐색 결과를 확인하지 못했습니다. 이전 회차 결과는 표시하지 않습니다.
            </p>
          ) : null}
          {available && !list && !failed ? <p className={s.empty}>불러오는 중…</p> : null}

          {list && sum && counts ? (
            <section className={s.isec} aria-label="발견">
              <h2>발견</h2>
              <p className={s.help}>탐색이 찾은 경로입니다. 진단 결과나 확정 취약점이 아닙니다.</p>
              {state?.status ? (
                <div style={chipRow}>
                  <span style={notRun ? outChip : neutralChip}>
                    {`탐색 상태: ${EXPLORATION_STATUS_LABEL[state.status]}${state.reason ? ` (${state.reason})` : ""}`}
                  </span>
                </div>
              ) : null}
              {notRun ? (
                <div className={cx(s.note, s.warn)} role="note">
                  <Icon name="alert" />
                  <span>이번 점검은 탐색을 실행하지 않았습니다. 아래 사전 등록 경로는 탐색이 찾은 것이 아닙니다.</span>
                </div>
              ) : null}
              <ul style={{ ...plainList, ...chipRow }} aria-label="발견 수">
                <Stat label="탐색 발견" value={sum.found} />
                <Stat label={WORKFLOW_COUNT_LABEL.visited} value={sum.visited} />
                <Stat label="범위 안" value={sum.inScope} />
                <Stat label="범위 밖" value={sum.outOfScope} />
                {sum.observed ? <Stat label={EVIDENCE_LABEL.observed} value={sum.observed} /> : null}
                {sum.observedDetailOmitted ? (
                  <Stat label={EVIDENCE_LABEL.observed_detail_omitted} value={sum.observedDetailOmitted} />
                ) : null}
                {sum.inferred ? <Stat label={EVIDENCE_LABEL.inferred} value={sum.inferred} /> : null}
                {sum.preRegistered ? <Stat label="사전 등록 경로" value={sum.preRegistered} /> : null}
                {omitted ? <Stat label="목록에서 생략" value={omitted} /> : null}
                {invalidPaths ? <Stat label={WORKFLOW_COUNT_LABEL.invalid_paths} value={invalidPaths} /> : null}
              </ul>
              {flow?.detail_truncated || sum.listed < counts.found ? (
                <p className={s.help}>
                  {`아래 목록은 서버가 화면에 준 ${sum.listed}개입니다. 서버 누계 ${counts.found}개 전체가 아닐 수 있습니다.`}
                </p>
              ) : null}

              <h3>출처</h3>
              {sum.bySource.length ? (
                <ul style={{ ...plainList, ...chipRow }} aria-label="출처별 발견">
                  {sum.bySource.map((r) => (
                    <Stat key={r.source} label={workflowSourceLabel(r.source)} value={r.count} />
                  ))}
                </ul>
              ) : (
                <p className={s.help}>없음</p>
              )}

              {registered.length ? (
                <>
                  <h3>사전 등록 경로</h3>
                  <p className={s.help}>점검을 시작할 때 승인한 경로입니다. 탐색이 찾은 것이 아닙니다.</p>
                  <ul style={plainList} aria-label="사전 등록 경로">
                    {registered.map((d) => (
                      <Row key={d.id} d={d} />
                    ))}
                  </ul>
                </>
              ) : null}

              <h3>발견 목록</h3>
              {found.length ? (
                <ul style={plainList} aria-label="발견 목록">
                  {found.map((d) => (
                    <Row key={d.id} d={d} />
                  ))}
                </ul>
              ) : (
                <p className={s.empty}>
                  {notRun
                    ? "탐색을 실행하지 않아 발견한 경로가 없습니다."
                    : "스냅샷에 탐색이 찾은 경로가 없습니다. 탐색이 아직 찾지 못했을 수 있습니다."}
                </p>
              )}
            </section>
          ) : null}
        </div>
      </div>
    </>
  );
}
