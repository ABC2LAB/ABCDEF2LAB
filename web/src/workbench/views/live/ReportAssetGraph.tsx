"use client";

// 보고서 '자산 지식 그래프' 탭 — KG 조회 v1(docs/contracts/kg-query-v1.md §11-4 PR 2).
// 포함 트리(자산 필드 파생 contained_by) · 포함 근거(basis) · 출처 패널 · 읽기 전용 상속 · 문제(issues)를 보인다.
// 데이터는 lib/diagnosis/kg-query.ts 의 getKgQuery 하나만 부른다 — 레이아웃이 바뀌어도 그 seam 은 그대로 둔다.

import { useEffect, useState } from "react";

import { getKgQuery, type KgIssue, type KgLoad } from "@/lib/diagnosis/kg-query";

import { cx } from "../../lib/cx";
import {
  type AssetRow,
  BASIS_HELP,
  BASIS_TEXT,
  buildKgView,
  FAILURE_TEXT,
  ISSUE_STATE_TEXT,
  ISSUE_TEXT,
  type KgView,
  OBSERVATION_UNKNOWN,
  OWNERSHIP_NOT_REQUESTED,
  observationText,
  POLICY_SOURCE_NOT_REQUESTED,
  provenanceTitle,
  RULE_TEXT,
} from "../../live/kgGraph";
import { Icon } from "../../ui/icons";
import s from "../../workbench.module.css";

const INHERITED = "읽기 전용 상속";

/**
 * taskId 의 그래프를 active(탭이 보일 때)일 때만 읽는다. 탭에 들어올 때마다 다시 읽고,
 * 결과는 어느 task 의 것인지 같이 들어 다른 점검의 늦은 응답을 그리지 않는다(Job 전환 경쟁).
 */
export function ReportAssetGraph({ taskId, active }: { taskId: string; active: boolean }) {
  const [result, setResult] = useState<{ task: string; load: KgLoad } | null>(null);
  const [attempt, setAttempt] = useState(0);
  // 고른 자산은 그 조회 결과(load 객체)에 묶는다 — 다시 읽거나 A→B→A 로 돌아오면 이번 결과의 첫 자산부터
  const [pick, setPick] = useState<{ load: KgLoad; id: string } | null>(null);

  // biome-ignore lint/correctness/useExhaustiveDependencies: attempt 는 같은 task 를 다시 부르는 트리거(다시 읽기)
  useEffect(() => {
    if (!active) return;
    let live = true;
    const controller = new AbortController();
    // 새 조회가 시작되면 이전 결과를 지운다 — 실패를 이전 그래프로 가리지 않는다(§9 Q16)
    setResult(null);
    getKgQuery(taskId, controller.signal)
      .then((load) => {
        if (live) setResult({ task: taskId, load });
      })
      .catch((err: unknown) => {
        if (!live || (err instanceof DOMException && err.name === "AbortError")) return;
        setResult({ task: taskId, load: { state: "error", failure: "failed", retryable: true } });
      });
    return () => {
      live = false;
      controller.abort();
    };
  }, [taskId, active, attempt]);

  // effect 가 지우기 전 한 번의 렌더에서도 다른 task 의 결과는 보이지 않는다
  const load = result && result.task === taskId ? result.load : null;

  if (load === null) {
    return (
      <p className={s.empty} role="status">
        자산 지식 그래프를 불러오는 중…
      </p>
    );
  }
  if (load.state === "error") {
    // v1 실패는 그래프 조회 실패로만 보이고 소유·정책 출처 칸은 그리지 않는다(§11-1)
    return (
      <div className={cx(s.note, s.warn)} role="alert">
        <Icon name="alert" />
        <span className={s["lv-grow"]}>{`그래프 조회 실패 · ${FAILURE_TEXT[load.failure]}`}</span>
        {load.retryable ? (
          <button className={cx(s.btn, s.sm)} type="button" onClick={() => setAttempt((n) => n + 1)}>
            다시 읽기
          </button>
        ) : null}
      </div>
    );
  }

  const view = buildKgView(load.graph);
  const all = flatten(view.roots);
  const chosen = (pick && pick.load === load ? all.find((r) => r.asset.id === pick.id) : undefined) ?? all[0];
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 16 }}>
      <ul className={s.tml} aria-label="조회하지 않은 칸">
        <li>
          <span className={cx(s.tag, s.out)}>소유 미조회</span>{" "}
          <span className={s.help}>{OWNERSHIP_NOT_REQUESTED}</span>
        </li>
        <li>
          <span className={cx(s.tag, s.out)}>정책 출처 미조회</span>{" "}
          <span className={s.help}>{POLICY_SOURCE_NOT_REQUESTED}</span>
        </li>
        <li>
          <span className={cx(s.tag, s.out)}>관측 미확인</span> <span className={s.help}>{OBSERVATION_UNKNOWN}</span>
        </li>
      </ul>

      <Summary view={view} />

      {all.length === 0 ? (
        <p className={s.empty}>
          이 작업 문맥에 포함된 자산이 없습니다. 검사 결과가 아니라 연결·범위·앵커 근거가 없다는 뜻입니다.
        </p>
      ) : (
        <>
          <section aria-label="포함 자산">
            <h3>포함 자산</h3>
            <AssetTree rows={view.roots} chosen={chosen?.asset.id ?? null} onPick={(id) => setPick({ load, id })} />
          </section>
          {chosen ? <ProvenancePanel view={view} row={chosen} /> : null}
        </>
      )}

      <Issues view={view} issues={load.graph.issues} />
    </div>
  );
}

function flatten(rows: AssetRow[]): AssetRow[] {
  return rows.flatMap((r) => [r, ...flatten(r.children)]);
}

function Summary({ view }: { view: KgView }) {
  return (
    <section aria-label="그래프 요약">
      <p className={s.help}>
        {`작업 #${view.currentTaskId} · 자산 ${view.counts.assets} · 출처 ${view.counts.provenance} · 문제 ${view.counts.issues}`}
      </p>
      {view.sourceTaskIds.length ? (
        <ul className={s.tml} aria-label="직접 원천 작업">
          {view.sourceTaskIds.map((t) => (
            <li key={t}>
              {`직접 원천 작업 #${t} `}
              <span className={cx(s.tag, s.ex)}>{INHERITED}</span>{" "}
              <span className={s.help}>원천 작업의 정책·실행·리셋 권한을 이 점검에 다시 묶지 않습니다.</span>
            </li>
          ))}
        </ul>
      ) : null}
    </section>
  );
}

function AssetTree({
  rows,
  chosen,
  onPick,
}: {
  rows: AssetRow[];
  chosen: string | null;
  onPick: (id: string) => void;
}) {
  return (
    <ul className={s.tml}>
      {rows.map((r) => (
        <li key={r.asset.id}>
          <button
            className={s.tli}
            type="button"
            aria-pressed={r.asset.id === chosen}
            onClick={() => onPick(r.asset.id)}
          >
            <span className={s.mono}>{r.label}</span>
            {r.rule ? <span className={s.help}>{`포함 · ${ruleText(r)}`}</span> : null}
            {r.inclusions.map((inc) => (
              <span key={inc.taskId} style={{ display: "inline-flex", gap: 4, alignItems: "center" }}>
                <span className={s.help}>{`작업 #${inc.taskId}`}</span>
                {inc.bases.map((b) => (
                  <span key={b} className={cx(s.tag, s.lg)} title={BASIS_HELP[b]}>
                    {BASIS_TEXT[b]}
                  </span>
                ))}
                {inc.inherited ? <span className={cx(s.tag, s.ex)}>{INHERITED}</span> : null}
              </span>
            ))}
          </button>
          {r.children.length ? (
            <div style={{ paddingLeft: 18 }}>
              <AssetTree rows={r.children} chosen={chosen} onPick={onPick} />
            </div>
          ) : null}
        </li>
      ))}
    </ul>
  );
}

function ruleText(r: AssetRow): string {
  return r.rule ? `${RULE_TEXT[r.rule]}(저장 필드 파생)` : "";
}

function ProvenancePanel({ view, row }: { view: KgView; row: AssetRow }) {
  const items = view.provenanceByAsset.get(row.asset.assetId) ?? [];
  return (
    <section aria-label={`출처 · ${row.label}`}>
      <h3>{`출처 · ${row.label}`}</h3>
      {items.length === 0 ? (
        <p className={s.empty}>이 자산에 공개된 출처가 없습니다.</p>
      ) : (
        <ul className={s.tml}>
          {items.map((p) => (
            <li key={p.id}>
              <span>{provenanceTitle(p)}</span>{" "}
              {"inherited" in p && p.inherited ? <span className={cx(s.tag, s.ex)}>{INHERITED}</span> : null}{" "}
              {"observation" in p ? (
                <span className={s.help}>{p.observation ? observationText(p.observation) : "관측 참조 없음"}</span>
              ) : null}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function Issues({ view, issues }: { view: KgView; issues: KgIssue[] }) {
  if (!issues.length) return null;
  return (
    <section aria-label="문제">
      <h3>{`문제 ${issues.length}`}</h3>
      <ul className={s.tml}>
        {issues.map((x) => (
          <li key={`${x.code}|${x.state}|${x.refs.join(",")}`}>
            <span className={cx(s.tag, x.state === "conflict" ? s.ap : s.out)}>{ISSUE_STATE_TEXT[x.state]}</span>{" "}
            <span>{ISSUE_TEXT[x.code]}</span>
            {x.refs.length ? <span className={s.help}>{` · ${x.refs.map(view.refLabel).join(", ")}`}</span> : null}
          </li>
        ))}
      </ul>
    </section>
  );
}
