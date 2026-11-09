"use client";

// API 상세 (live). 범위 확인의 상세 패널과 자산 탐색의 찾은 경로 패널이 함께 쓴다. demo ApiDetail 과 같은 머리 · 탭 구조.
// - 기본 정보: 관찰한 사실(엔드포인트 자산 칸) · 추정(종류 · 묶음) · 관련 기록(asset-refs 의 의도 · 사실 · 발견)
// - 요청·응답 예시: 트래픽 기록에서 맞는 가장 최근 요청을 찾아 원문을 가려서(maskSecrets) 보인다.
// visible=false(숨은 화면 · 닫힌 패널)면 부르지 않는다. 탭은 보이는 탭의 것만 부른다.

import { type KeyboardEvent, type ReactNode, useRef } from "react";

import { type ApiTab, useApiTab } from "../../lib/apiTab";
import { cx } from "../../lib/cx";
import { type AssetRef, fetchAssetRefs, fetchTraffic, fetchTrafficExchange } from "../../live/api";
import { clip, type EpRow, pickExchange, trafficPathPrefix } from "../../live/crawlScope";
import { clockOf, pathOf, planState, relTime } from "../../live/map";
import { maskSecrets } from "../../live/mask";
import { ErrorNote, EstimateTag, LoadingRow, MaskedNote, ModeNote, useNow } from "../../live/parts";
import { usePoll } from "../../live/usePoll";
import { Icon } from "../../ui/icons";
import { CodeCard, Ep, InspHead, Isec, Kv } from "../../ui/parts";
import s from "../../workbench.module.css";
import { KindTag, TrafficOffNote } from "./crawlScopeParts";

/** 트래픽에서 맞는 기록을 찾을 때 받아 볼 최근 기록 수. */
const LOOKUP_SIZE = 50;
/** 관련 기록 묶음마다 보일 최대 줄 수. */
const REF_MAX = 20;

const STEP: Partial<Record<string, number>> = { ArrowRight: 1, ArrowLeft: -1 };
const TABS: ReadonlyArray<readonly [ApiTab, string]> = [
  ["info", "기본 정보"],
  ["ex", "요청·응답 예시"],
];

/** 관련 기록 한 묶음 (의도 · 사실 · 발견). 요약은 서버 원문이라 maskSecrets. */
function RefGroup({ title, items, intent }: { title: string; items: AssetRef[]; intent?: boolean }) {
  if (!items.length) return null;
  const shown = items.slice(0, REF_MAX);
  return (
    <>
      <p className={s.help}>{`${title} ${items.length}건`}</p>
      <ul className={s.rules} aria-label={title}>
        {shown.map((r) => {
          const state = intent ? planState(r.state).label : r.state || "알 수 없음";
          return (
            <li key={r.id}>
              <span>
                {maskSecrets(r.summary) || "(요약 없음)"}
                {r.inherited ? " · 이어받음" : ""}
              </span>
              <span className={cx(s.tag, s.out)}>{state}</span>
            </li>
          );
        })}
      </ul>
      {items.length > shown.length ? <p className={s.help}>{`외 ${items.length - shown.length}건`}</p> : null}
    </>
  );
}

/** 응답 없음 상자 (demo 의 cb-none 모양). */
function NoExchange({ children }: { children: ReactNode }) {
  return (
    <div className={s.cb}>
      <div className={s["cb-h"]}>
        <h3>요청·응답 예시</h3>
        <span className={s["cb-src"]}>없음</span>
      </div>
      <div className={s["cb-none"]}>
        <Icon name="info" />
        <span>{children}</span>
      </div>
    </div>
  );
}

/**
 * API 상세 본문. px 는 id 접두어(자산 탐색 "lc" · 범위 확인 "ls") — 탭·복사 대상 id 가 문서 안에서 겹치지 않게.
 * host 는 점검 대상 호스트(추정) — 엔드포인트에 도메인이 없을 때 트래픽을 거르는 데 쓴다.
 */
export function ApiDetailLive({
  taskId,
  row,
  host,
  px,
  visible,
}: {
  taskId: string;
  row: EpRow;
  host: string | null;
  px: "lc" | "ls";
  visible: boolean;
}) {
  const ep = row.ep;
  const tab = useApiTab((t) => t.tab);
  const setTab = useApiTab((t) => t.setTab);
  const listRef = useRef<HTMLDivElement>(null);
  const now = useNow();

  const refs = usePoll(() => fetchAssetRefs(taskId, ep.id), [taskId, ep.id], { enabled: visible && tab === "info" });

  // 요청·응답 예시: 호스트를 모르면 전역 기록이 섞이므로 찾지 않는다
  const lookHost = ep.domain || (host ?? "");
  const look = usePoll(
    () =>
      fetchTraffic({
        host: lookHost,
        method: row.m || undefined,
        path: trafficPathPrefix(row.p) || undefined,
        size: LOOKUP_SIZE,
      }),
    [taskId, ep.id, lookHost],
    { enabled: visible && tab === "ex" && !!lookHost },
  );
  const hit = look.data ? pickExchange(look.data.exchanges, row, lookHost) : null;
  const ex = usePoll(() => fetchTrafficExchange(hit?.id as string), [hit?.id ?? null], {
    enabled: visible && tab === "ex" && !!hit,
  });

  const onKeyDown = (ev: KeyboardEvent<HTMLButtonElement>, k: ApiTab) => {
    const d = STEP[ev.key];
    if (!d) return;
    ev.preventDefault();
    const at = TABS.findIndex((t) => t[0] === k);
    const next = TABS[(at + d + TABS.length) % TABS.length][0];
    setTab(next);
    listRef.current?.querySelector<HTMLElement>(`[data-apitab="${next}"]`)?.focus();
  };

  // ---- 관련 기록 ----
  let refsBody: ReactNode;
  if (refs.unsupported) refsBody = <ModeNote what="관련 기록" />;
  else if (!refs.data && refs.error) refsBody = <ErrorNote error={refs.error} onRetry={refs.reload} />;
  else if (!refs.data) refsBody = <LoadingRow />;
  else if (!refs.data.intents.length && !refs.data.facts.length && !refs.data.findings.length) {
    refsBody = <p className={s.empty}>이 경로에 걸린 의도·사실·발견이 아직 없습니다.</p>;
  } else {
    refsBody = (
      <>
        <RefGroup title="의도" items={refs.data.intents} intent />
        <RefGroup title="사실" items={refs.data.facts} />
        <RefGroup title="발견" items={refs.data.findings} />
        <MaskedNote />
      </>
    );
  }

  // ---- 요청·응답 예시 ----
  let exBody: ReactNode;
  const off = look.data && !look.data.enabled ? <TrafficOffNote /> : null;
  if (!lookHost) exBody = <NoExchange>대상 호스트를 몰라 트래픽 기록을 찾지 않았습니다.</NoExchange>;
  else if (look.unsupported) exBody = <ModeNote what="요청·응답 예시" />;
  else if (!look.data && look.error) exBody = <ErrorNote error={look.error} onRetry={look.reload} />;
  else if (!look.data) exBody = <LoadingRow />;
  else if (!hit) {
    exBody = (
      <>
        {off}
        <NoExchange>{`이 경로와 맞는 기록된 요청이 없습니다 (최근 ${LOOKUP_SIZE}건 기준).`}</NoExchange>
      </>
    );
  } else if (ex.unsupported) exBody = <ModeNote what="요청·응답 원문" />;
  else if (!ex.data && ex.error) exBody = <ErrorNote error={ex.error} onRetry={ex.reload} />;
  else if (!ex.data) exBody = <LoadingRow />;
  else {
    const req = clip(ex.data.reqMasked);
    const res = clip(ex.data.respMasked);
    const at = clockOf(hit.ts);
    exBody = (
      <>
        {off}
        <p className={s.help}>
          {`트래픽 기록에서 찾은 가장 최근 요청입니다${at ? ` · ${at}` : ""}${hit.status ? ` · ${hit.status}` : ""}`}
        </p>
        <CodeCard title="요청 예시" src="기록 · 관찰" text={req.text} id={`${px}-req`} />
        {res.text ? (
          <CodeCard title="응답 예시" src="기록 · 관찰" text={res.text} id={`${px}-res`} />
        ) : (
          <NoExchange>기록에 응답이 없습니다.</NoExchange>
        )}
        {req.clipped || res.clipped ? <p className={s.help}>긴 원문은 앞 20,000자만 보입니다.</p> : null}
        <MaskedNote />
      </>
    );
  }

  const seen = relTime(ep.last_seen, now);
  const params = ep.params.map((x) => (x.location ? `${x.name} (${x.location})` : x.name)).join(", ");
  const facts: Array<readonly [ReactNode, ReactNode]> = [
    ["메서드", ep.method || "알 수 없음"],
    ["경로", <code key="p">{maskSecrets(pathOf(ep.url))}</code>],
  ];
  if (ep.domain) facts.push(["도메인", `${ep.domain}${ep.port ? `:${ep.port}` : ""}`]);
  if (ep.status_code !== null) facts.push(["응답 코드", String(ep.status_code)]);
  if (ep.page_title) facts.push(["페이지 제목", maskSecrets(ep.page_title)]);
  facts.push(["파라미터", params ? `${ep.params.length}개 · ${params}` : "없음"]);
  facts.push(["마지막 관찰", (now && seen) || ep.last_seen || "기록 없음"]);
  facts.push(["관련 점검", `${ep.task_ids.length}개`]);

  return (
    <>
      <InspHead
        eyebrow={`찾은 경로 · ${row.g}`}
        title={<Ep m={row.m || "?"} p={row.p} />}
        chips={
          <>
            <KindTag k={row.k} />
            <EstimateTag title="종류는 메서드·경로로 화면이 추정했습니다" />
            {row.hasId ? <span className={cx(s.tag, s.out)}>ID 포함</span> : null}
          </>
        }
      />
      <div className={cx(s["seg-tabs"], s["api-tabs"])} role="tablist" aria-label="경로 상세" ref={listRef}>
        {TABS.map(([k, label]) => {
          const sel = tab === k;
          return (
            <button
              key={k}
              type="button"
              role="tab"
              id={`${px}-t-${k}`}
              aria-controls={`${px}-p-${k}`}
              aria-selected={sel}
              tabIndex={sel ? undefined : -1}
              data-apitab={k}
              onClick={() => setTab(k)}
              onKeyDown={(ev) => onKeyDown(ev, k)}
            >
              {label}
            </button>
          );
        })}
      </div>
      <div role="tabpanel" id={`${px}-p-info`} aria-labelledby={`${px}-t-info`} hidden={tab !== "info"}>
        <Isec t="관찰한 사실">
          <Kv rows={facts} />
        </Isec>
        <Isec t="추정">
          <Kv
            rows={[
              ["종류", <KindTag key="k" k={row.k} />],
              ["묶음", `${row.g} (경로 첫 조각)`],
            ]}
          />
        </Isec>
        <Isec t="관련 기록">{refsBody}</Isec>
      </div>
      <div role="tabpanel" id={`${px}-p-ex`} aria-labelledby={`${px}-t-ex`} hidden={tab !== "ex"}>
        <div className={s.expanel}>{exBody}</div>
      </div>
    </>
  );
}
