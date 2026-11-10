"use client";

import { type ReactNode, useState } from "react";

import { cx } from "../../lib/cx";
import type { Remediation } from "../../live/api";
import { outcomeLabel } from "../../live/liveReport";
import { maskSecrets } from "../../live/mask";
import { LoadingRow, ModeNote } from "../../live/parts";
import type { Poll } from "../../live/usePoll";
import { useShell } from "../../shell";
import { InspHead, Isec } from "../../ui/parts";
import s from "../../workbench.module.css";

// The report API already binds these entries to the task and stored evidence version.
// Candidate titles and evidence remain escaped text; no new finding/verdict is synthesized here.
export function WorkflowReportPanels({ rem, children }: { rem: Poll<Remediation>; children: ReactNode }) {
  const [tab, setTab] = useState("list"),
    [selected, setSelected] = useState<string | null>(null);
  const { openInsp } = useShell();
  const entries = rem.data?.entries ?? [];
  const entry = entries.find((e) => e.entry_id === selected) ?? entries[0];
  return (
    <div className={s.vb}>
      <div className={s.vm}>
        {children}
        <section className={s.sec} aria-label="저장 보고서 내용">
          <div className={s["seg-tabs"]} role="tablist" aria-label="보고서 내용" id="rTabs">
            {[
              ["list", "발견 목록"],
              ["graph", "자산 지식 그래프"],
              ["map", "접근통제 지도"],
              ["evidence", "증거 · 감사"],
            ].map(([id, label]) => (
              <button
                key={id}
                type="button"
                role="tab"
                aria-selected={tab === id}
                aria-controls={`workflow-report-${id}`}
                id={`workflow-report-tab-${id}`}
                onClick={() => setTab(id)}
              >
                {label}
                {id === "list" && rem.data ? <span className={s.c}>{entries.length}</span> : null}
              </button>
            ))}
          </div>
          <div role="tabpanel" id={`workflow-report-${tab}`} aria-labelledby={`workflow-report-tab-${tab}`}>
            {tab === "list" ? (
              <>
                {!rem.data && !rem.error ? <LoadingRow /> : null}
                {rem.error ? (
                  <p role="alert">저장된 발견 목록을 확인할 수 없습니다. 보고서를 다시 조회하세요.</p>
                ) : null}
                {rem.data && !entries.length ? <p className={s.empty}>저장된 발견·후보가 없습니다.</p> : null}
                <div className={s.list} id="rList">
                  {entries.map((e) => (
                    <button
                      className={cx(s.row, entry?.entry_id === e.entry_id && s.sel)}
                      key={e.entry_id}
                      type="button"
                      onClick={() => {
                        setSelected(e.entry_id);
                        openInsp();
                      }}
                    >
                      <span className={s["r-main"]}>
                        <b>{maskSecrets(e.title)}</b>
                        <span className={s["r-s"]}>{maskSecrets(e.summary)}</span>
                      </span>
                      <span className={s.tag}>{outcomeLabel(e.outcome)}</span>
                    </button>
                  ))}
                </div>
              </>
            ) : null}
            {tab === "graph" ? (
              <>
                <ModeNote what="자산 지식 그래프" />
                <p className={s.help}>
                  이 URL 진단 경로는 자산 그래프 projection을 제공하지 않습니다. 실제 발견 경로는 탐색·선택 범위에
                  표시합니다.
                </p>
              </>
            ) : null}
            {tab === "map" ? (
              <>
                <ModeNote what="계정별 접근통제 지도" />
                <p className={s.help}>
                  계정 A/B 연결과 교차 조회 판정이 준비되지 않아 접근 허용·취약 여부를 채우지 않습니다.
                </p>
              </>
            ) : null}
            {tab === "evidence" ? (
              <ul className={s.evi}>
                {entries.map((e) => (
                  <li key={e.entry_id}>
                    <code>{e.entry_id}</code>
                    <span>
                      실제 run {e.run} · intent {e.intent_id} · 증거 버전 {e.evidence_version}
                    </span>
                    <span className={s.tag}>{e.verification}</span>
                  </li>
                ))}
              </ul>
            ) : null}
          </div>
        </section>
      </div>
      <aside className={s.vi} id="rInsp" aria-label="발견 상세">
        <InspHead eyebrow="실제 저장 증거" title={entry ? maskSecrets(entry.title) : "발견 상세"} />
        {entry ? (
          <>
            <Isec>
              <p>
                {outcomeLabel(entry.outcome)} · {entry.verification}
              </p>
              <p>{maskSecrets(entry.summary)}</p>
              <p className={s.help}>실행 마감과 취약점 확정은 별개입니다. 판정은 서버에 저장된 검증 결과만 따릅니다.</p>
            </Isec>
            <Isec>
              <h3>실제 요청·응답 증거</h3>
              <pre className={s.code}>{maskSecrets(entry.evidence) || "저장된 요청·응답 본문 없음"}</pre>
              <p className={s.help}>
                증거 digest {entry.evidence_digest} · {entry.observed_at}
              </p>
            </Isec>
            <Isec>
              <h3>검토와 수정 권고</h3>
              <p className={s["lv-pre"]}>{maskSecrets(entry.report)}</p>
              <p className={s["lv-pre"]}>{maskSecrets(entry.remediation) || "저장된 수정 권고 없음"}</p>
            </Isec>
          </>
        ) : (
          <Isec>
            <p className={s.help}>실제 저장된 항목을 고르면 상세를 표시합니다.</p>
          </Isec>
        )}
      </aside>
    </div>
  );
}
