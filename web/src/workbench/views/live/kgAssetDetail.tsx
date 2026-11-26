"use client";

// 보고서 '자산 지식 그래프' 탭의 KG 조회 v1 몫(docs/contracts/kg-query-v1.md §11). 따로 글 목록으로 늘어놓지 않고
// 위 자산 관계 그래프(coverageGraphView)에 붙인다 — 노드를 고르면 그 자산의 포함 근거 · 출처가 상세에 나오고(KgAssetDetail),
// 그래프 아래에는 짧은 요약 한 줄 · 문제 · 그래프에 없는 포함 자산만 둔다(KgOverview). 두 응답은 자산 번호로 잇는다.
// 데이터는 lib/diagnosis/kg-query.ts 의 getKgQuery 하나만 부른다 — 레이아웃이 바뀌어도 그 seam 은 그대로 둔다.
// 계약에서 지키는 것: 포함(basis)과 출처를 따로 보인다(D03) · 원천 작업 포함은 '읽기 전용 상속' · 소유 · 정책 출처는 '미조회'(D05)이고
// '없음 · 허용 · 안전'으로 칠하지 않는다 · KG 조회 실패는 '포함 근거 조회 실패'로만 보이고 미조회 칸을 그리지 않는다(§11-1).

import { type ReactNode, useCallback, useEffect, useState } from "react";

import { getKgQuery, type KgIssue, type KgLoad, type KgProvenance } from "@/lib/diagnosis/kg-query";

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
import { HelpTip } from "../../ui/parts";
import s from "../../workbench.module.css";

const INHERITED = "읽기 전용 상속";

/** 화면이 쓰는 KG 상태. view 는 ready 일 때만 있다. */
export type KgState = {
  load: KgLoad | null;
  view: KgView | null;
  retry: () => void;
};

/**
 * taskId 의 KG 조회 v1 을 active(탭이 보일 때)일 때만 읽는다. 탭에 들어올 때마다 다시 읽고,
 * 결과는 어느 task 의 것인지 같이 들어 다른 점검의 늦은 응답을 쓰지 않는다(Job 전환 경쟁).
 */
export function useKgQuery(taskId: string, active: boolean): KgState {
  const [result, setResult] = useState<{ task: string; load: KgLoad; view: KgView | null } | null>(null);
  const [attempt, setAttempt] = useState(0);

  // biome-ignore lint/correctness/useExhaustiveDependencies: attempt 는 같은 task 를 다시 부르는 트리거(다시 읽기)
  useEffect(() => {
    if (!active) return;
    let live = true;
    const controller = new AbortController();
    // 새 조회가 시작되면 이전 결과를 지운다 — 실패를 이전 근거로 가리지 않는다(§9 Q16)
    setResult(null);
    getKgQuery(taskId, controller.signal)
      .then((load) => {
        if (live) setResult({ task: taskId, load, view: load.state === "ready" ? buildKgView(load.graph) : null });
      })
      .catch((err: unknown) => {
        if (!live || (err instanceof DOMException && err.name === "AbortError")) return;
        setResult({ task: taskId, load: { state: "error", failure: "failed", retryable: true }, view: null });
      });
    return () => {
      live = false;
      controller.abort();
    };
  }, [taskId, active, attempt]);

  const retry = useCallback(() => setAttempt((n) => n + 1), []);
  // effect 가 지우기 전 한 번의 렌더에서도 다른 task 의 결과는 보이지 않는다
  const mine = result && result.task === taskId ? result : null;
  return { load: mine?.load ?? null, view: mine?.view ?? null, retry };
}

function flatten(rows: AssetRow[]): AssetRow[] {
  return rows.flatMap((r) => [r, ...flatten(r.children)]);
}

/** 자산 번호(asset_id) → 화면 행 */
export function kgRowByAssetId(view: KgView, assetId: string): AssetRow | undefined {
  return flatten(view.roots).find((r) => r.asset.assetId === assetId);
}

/** '미조회' 세 칸 — 짧은 딱지로 늘 보이고, 뜻은 (?) 안에 둔다(D05 문구 그대로) */
function NotRequested() {
  return (
    <span className={s.tipline} style={{ display: "inline-flex", flexWrap: "wrap", alignItems: "center", gap: 4 }}>
      <span className={cx(s.tag, s.out)}>소유 미조회</span>
      <span className={cx(s.tag, s.out)}>정책 출처 미조회</span>
      <span className={cx(s.tag, s.out)}>관측 미확인</span>
      <HelpTip label="조회하지 않은 칸">
        <span style={{ display: "block" }}>{OWNERSHIP_NOT_REQUESTED}</span>
        <span style={{ display: "block" }}>{POLICY_SOURCE_NOT_REQUESTED}</span>
        <span style={{ display: "block" }}>{OBSERVATION_UNKNOWN}</span>
        <span style={{ display: "block", marginTop: 6 }}>
          '미조회'는 근거가 없다는 뜻이 아니라 이 조회가 읽지 않았다는 뜻입니다.
        </span>
      </HelpTip>
    </span>
  );
}

function Failure({ kg }: { kg: KgState }) {
  if (kg.load?.state !== "error") return null;
  // v1 실패는 조회 실패로만 보이고 소유 · 정책 출처 칸은 그리지 않는다(§11-1)
  return (
    <div className={cx(s.note, s.warn)} role="alert">
      <Icon name="alert" />
      <span className={s["lv-grow"]}>{`포함 근거 조회 실패 · ${FAILURE_TEXT[kg.load.failure]}`}</span>
      {kg.load.retryable ? (
        <button className={cx(s.btn, s.sm)} type="button" onClick={kg.retry}>
          다시 읽기
        </button>
      ) : null}
    </div>
  );
}

function Bases({ row }: { row: AssetRow }) {
  if (!row.inclusions.length) return <span className={s.help}>이 작업 문맥의 포함 근거 없음</span>;
  return (
    <>
      {row.inclusions.map((inc) => (
        <span key={inc.taskId} style={{ display: "inline-flex", flexWrap: "wrap", gap: 4, alignItems: "center" }}>
          <span className={s.help}>{`작업 #${inc.taskId}`}</span>
          {inc.bases.map((b) => (
            <span key={b} className={cx(s.tag, s.lg)} title={BASIS_HELP[b]}>
              {BASIS_TEXT[b]}
            </span>
          ))}
          {inc.inherited ? <span className={cx(s.tag, s.ex)}>{INHERITED}</span> : null}
        </span>
      ))}
    </>
  );
}

function ProvenanceItem({ p }: { p: KgProvenance }) {
  return (
    <li>
      <span>{provenanceTitle(p)}</span>{" "}
      {"inherited" in p && p.inherited ? <span className={cx(s.tag, s.ex)}>{INHERITED}</span> : null}{" "}
      {"observation" in p ? (
        <span className={s.help}>{p.observation ? observationText(p.observation) : "관측 참조 없음"}</span>
      ) : null}
    </li>
  );
}

function issuesOf(view: KgView, issues: KgIssue[], row: AssetRow): KgIssue[] {
  const refs = new Set([row.asset.id, ...(view.provenanceByAsset.get(row.asset.assetId) ?? []).map((p) => p.id)]);
  return issues.filter((x) => x.refs.some((r) => refs.has(r)));
}

function IssueItem({ view, x }: { view: KgView; x: KgIssue }) {
  return (
    <li>
      <span className={cx(s.tag, x.state === "conflict" ? s.ap : s.out)}>{ISSUE_STATE_TEXT[x.state]}</span>{" "}
      <span>{ISSUE_TEXT[x.code]}</span>
      {x.refs.length ? <span className={s.help}>{` · ${x.refs.map(view.refLabel).join(", ")}`}</span> : null}
    </li>
  );
}

/** 고른 자산 하나의 포함 근거 · 출처 · 그 자산에 걸린 문제. 그래프 노드 상세와 '그래프에 없는 포함 자산' 목록이 같이 쓴다. */
function AssetKg({ view, issues, row }: { view: KgView; issues: KgIssue[]; row: AssetRow }) {
  const provenance = view.provenanceByAsset.get(row.asset.assetId) ?? [];
  const mine = issuesOf(view, issues, row);
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
      <div style={{ display: "flex", flexWrap: "wrap", gap: 6, alignItems: "center" }} data-kg="basis">
        <span className={s.help}>포함 근거</span>
        <Bases row={row} />
        {row.rule ? <span className={s.help}>{`· 포함 관계 ${RULE_TEXT[row.rule]}(저장 필드 파생)`}</span> : null}
      </div>
      <div data-kg="provenance">
        <span className={s.help}>{`출처 ${provenance.length}`}</span>
        {provenance.length ? (
          <ul className={s.tml}>
            {provenance.map((p) => (
              <ProvenanceItem key={p.id} p={p} />
            ))}
          </ul>
        ) : (
          <span className={s.help}> · 이 자산에 공개된 출처가 없습니다.</span>
        )}
      </div>
      {mine.length ? (
        <ul className={s.tml} aria-label="이 자산의 문제">
          {mine.map((x) => (
            <IssueItem key={`${x.code}|${x.state}|${x.refs.join(",")}`} view={view} x={x} />
          ))}
        </ul>
      ) : null}
    </div>
  );
}

/** 그래프 노드 상세 안의 'KG 포함 근거 · 출처' 칸. assetId 가 없거나(접힌 · 이름 비공개 노드) KG 에 없으면 그 뜻대로 적는다. */
export function KgAssetDetail({ kg, assetId }: { kg: KgState; assetId: number | null }) {
  let body: ReactNode;
  if (kg.load === null) {
    body = (
      <p className={s.help} role="status">
        포함 근거를 불러오는 중…
      </p>
    );
  } else if (kg.load.state === "error" || !kg.view) {
    // 경고 상자 · 다시 읽기는 그래프 아래 요약(KgOverview) 한 곳에만 둔다 — 여기는 짧게
    body = (
      <p className={s.help}>
        {kg.load.state === "error" ? `포함 근거 조회 실패 · ${FAILURE_TEXT[kg.load.failure]}` : "포함 근거 조회 실패"}
      </p>
    );
  } else if (assetId === null) {
    body = <p className={s.help}>자산 번호가 없는 노드라 포함 근거와 이을 수 없습니다.</p>;
  } else {
    const row = kgRowByAssetId(kg.view, String(assetId));
    body = row ? (
      <AssetKg view={kg.view} issues={kg.load.graph.issues} row={row} />
    ) : (
      <p className={s.help}>이 작업 문맥의 포함 자산이 아닙니다(포함 근거 없음 — 검사 결과가 아닙니다).</p>
    );
  }
  return (
    <section aria-label="포함 근거 · 출처" data-testid="kg-asset-detail">
      <h4 style={{ display: "flex", flexWrap: "wrap", alignItems: "center", gap: 6, margin: "8px 0 4px" }}>
        포함 근거 · 출처
        {kg.view ? <NotRequested /> : null}
      </h4>
      {body}
    </section>
  );
}

/**
 * 그래프 아래 요약 한 줄. 출처 · 문제 수, 직접 원천 작업(읽기 전용 상속), 미조회 칸, 문제가 있을 때만 경고 상자,
 * 그리고 그래프에서 고를 수 없는 포함 자산(접어 둠). graphAssetIds 는 지금 그래프에 그려진 자산 번호들이다(그래프를 못 읽었으면 빈 집합).
 */
export function KgOverview({ kg, graphAssetIds }: { kg: KgState; graphAssetIds: ReadonlySet<string> }) {
  if (kg.load === null) {
    return (
      <p className={s.help} role="status">
        포함 근거 · 출처(KG 조회 v1)를 불러오는 중…
      </p>
    );
  }
  if (kg.load.state === "error" || !kg.view) return <Failure kg={kg} />;
  const { view } = kg;
  const { issues } = kg.load.graph;
  const all = flatten(view.roots);
  const offGraph = all.filter((r) => !graphAssetIds.has(r.asset.assetId));
  return (
    <section aria-label="포함 근거 요약" style={{ display: "flex", flexDirection: "column", gap: 8 }}>
      <div style={{ display: "flex", flexWrap: "wrap", alignItems: "center", gap: 8 }}>
        <span className={s.help}>
          {`포함 근거 · 출처(KG 조회 v1) · 작업 #${view.currentTaskId} · 포함 자산 ${view.counts.assets} · 출처 ${view.counts.provenance} · 문제 ${view.counts.issues}`}
        </span>
        {view.sourceTaskIds.map((t) => (
          <span key={t} style={{ display: "inline-flex", gap: 4, alignItems: "center" }}>
            <span
              className={s.help}
              title="원천 작업의 정책 · 실행 · 리셋 권한을 이 점검에 다시 묶지 않습니다."
            >{`직접 원천 작업 #${t}`}</span>
            <span className={cx(s.tag, s.ex)}>{INHERITED}</span>
          </span>
        ))}
        <NotRequested />
      </div>
      {all.length === 0 ? (
        <p className={s.help}>
          이 작업 문맥에 포함된 자산이 없습니다. 검사 결과가 아니라 연결 · 범위 · 앵커 근거가 없다는 뜻입니다.
        </p>
      ) : (
        <p className={s.help}>그래프에서 노드를 고르면 그 자산의 포함 근거와 출처를 봅니다.</p>
      )}
      {issues.length ? (
        <div className={cx(s.note, s.warn)} role="note" aria-label="문제">
          <Icon name="alert" />
          <div className={s["lv-grow"]}>
            <b>{`문제 ${issues.length}`}</b>
            <ul className={s.tml}>
              {issues.map((x) => (
                <IssueItem key={`${x.code}|${x.state}|${x.refs.join(",")}`} view={view} x={x} />
              ))}
            </ul>
          </div>
        </div>
      ) : null}
      {offGraph.length ? (
        <details>
          <summary className={s.help}>{`그래프에서 고를 수 없는 포함 자산 ${offGraph.length}`}</summary>
          <ul className={s.tml} aria-label="그래프에 없는 포함 자산">
            {offGraph.map((r) => (
              <li key={r.asset.id}>
                <span className={s.mono}>{r.label}</span>
                <AssetKg view={view} issues={issues} row={r} />
              </li>
            ))}
          </ul>
        </details>
      ) : null}
    </section>
  );
}
