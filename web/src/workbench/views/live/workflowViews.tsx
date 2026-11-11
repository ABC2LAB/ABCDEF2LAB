"use client";

// 탐색 · 선택 워크플로 화면 (시안 짜임): 자산 탐색 · 범위 확인, 그리고 점검 진행의 계획 · 활동.
// 서버가 저장한 발견 · 관찰만 보인다. 묶음은 경로 규칙으로 나눈 것이라 AI 추정이라고 부르지 않는다.
// 백분율 · 남은 시간 · 로그인 필요 여부 · 경로 설명은 서버가 주지 않아 만들지 않는다.

import { type ReactNode, useMemo, useState } from "react";

import { cx } from "../../lib/cx";
import { clockOf, planState } from "../../live/map";
import { retryWorkflowAction, runUrlAction, runWorkflowAction, useUrl, useUrlGates } from "../../live/urlDiagnosis";
import { flowStageText, type WorkflowEndpoint, type WorkflowSnapshot } from "../../live/workflow";
import { useShell } from "../../shell";
import { Icon } from "../../ui/icons";
import { CloseInspButton, CodeCard, InspOpenButton, Kv, PaneToggle } from "../../ui/parts";
import s from "../../workbench.module.css";
import { UrlHead, UrlNotices, UrlRefreshButton, useUrlHost } from "./urlParts";

/** 경로 → 묶음 이름 (경로 규칙). */
export function group(e: WorkflowEndpoint): string {
  if (/user|account|auth|login/i.test(e.path)) return "계정·인증";
  if (/cart|basket|order/i.test(e.path)) return "장바구니·주문";
  if (/admin/i.test(e.path)) return "관리자";
  if (/product|review/i.test(e.path)) return "상품·리뷰";
  if (e.kind === "script") return "정적 자원";
  return "공개 페이지·API";
}

/** 경로 종류: 제외 · 보낼 때 승인 · 자동 포함(사전 확인 읽기). */
type Kind = "ex" | "ap" | "get";
function kindOf(e: WorkflowEndpoint): Kind {
  if (!e.in_scope || e.change === "blocked") return "ex";
  return e.change === "possible" ? "ap" : "get";
}
const KIND_TEXT: Readonly<Record<Kind, string>> = { ex: "제외", ap: "보낼 때 승인", get: "자동 포함" };
/** 종류 → 딱지 색 클래스. */
const kindTag = (k: Kind) => ({ ex: s.ex, ap: s.ap, get: s.in })[k];

/** 탐색 화면 상태 알약 색 · 글. */
function crawlTone(flow: WorkflowSnapshot): string | undefined {
  if (flow.paused) return s.wait;
  return flow.stage === "crawling" ? s.run : s.ok;
}
function crawlPill(flow: WorkflowSnapshot, verified: boolean): string {
  if (!verified) return "현재 상태 미확인";
  return flow.paused ? "일시정지" : flowStageText[flow.stage];
}
/** 범위 확인 화면 상태 알약 글. */
function scopePill(flow: WorkflowSnapshot, verified: boolean): string {
  if (!verified) return "현재 상태 미확인";
  return flow.stage === "awaiting_selection" ? "탐색 완료 · 확인 대기" : flowStageText[flow.stage];
}

/** 상태 코드 딱지 (2xx · 3xx · 4xx · 5xx). */
function Sc({ status }: { status?: number }) {
  if (status === undefined) return null;
  return <span className={cx(s.sc, s[`s${String(status)[0]}`])}>{status}</span>;
}

/** 경로 안 숫자 · {param} 이 있으면 ID 포함. */
const hasId = (p: string) => /\/\d+(\/|$)|\{\w+\}/.test(p);

/** 시각 → 24시간 HH:MM:SS. */
const hms = (at: string) => clockOf(at) || "—";

/** 탐색 명령 결과 · 상한 안내 (서버 사실만). */
export function WorkflowNotice() {
  const flow = useUrl((st) => st.flow);
  const error = useUrl((st) => st.flowError),
    uncertain = useUrl((st) => st.flowUncertain),
    verified = useUrl((st) => st.flowVerified);
  return (
    <>
      {flow?.failure ? (
        <div className={cx(s.note, s.warn)} role="alert">
          <Icon name="alert" />
          <span>{`탐색 실패 · ${flow.failure}`}</span>
        </div>
      ) : null}
      {flow?.counts.omitted ? (
        <p role="status" className={s.help}>{`발견 상한으로 ${flow.counts.omitted}건을 생략했습니다.`}</p>
      ) : null}
      {flow?.detail_truncated ? (
        <p role="status" className={s.help}>
          응답 상한으로 일부 관찰 상세를 생략했습니다.
        </p>
      ) : null}
      {error ? (
        <p role="alert" className={s.help}>
          {error}
        </p>
      ) : null}
      {uncertain ? <p role="status">명령 결과 불명 · 조회로 확인하기 전에는 변경 명령을 보내지 않습니다.</p> : null}
      {uncertain && verified ? (
        <div>
          <button type="button" className={cx(s.btn, s.sm)} onClick={() => void retryWorkflowAction()}>
            같은 명령 확인 · 재시도
          </button>
        </div>
      ) : null}
    </>
  );
}

/** 일시정지 · 다시 시작 버튼 (탐색 · 심화 진단 중에만). */
export function PauseButton() {
  const flow = useUrl((st) => st.flow),
    busy = useUrl((st) => st.flowBusy),
    verified = useUrl((st) => st.flowVerified),
    uncertain = useUrl((st) => st.flowUncertain),
    job = useUrl((st) => st.job),
    jobVerified = useUrl((st) => st.verified),
    jobUncertain = useUrl((st) => st.uncertain);
  const active =
    job && ["preparing", "running"].includes(job.state) && flow && ["crawling", "diagnosing"].includes(flow.stage);
  if (!active) return null;
  return (
    <button
      className={s.btn}
      type="button"
      disabled={
        !verified || uncertain || busy || !jobVerified || jobUncertain || (!flow.paused && flow.commands.length >= 31)
      }
      aria-pressed={flow.paused}
      onClick={() => void runWorkflowAction(flow.paused ? "resume" : "pause")}
    >
      <Icon name={flow.paused ? "play" : "pause"} />
      <span>{flow.paused ? "다시 시작" : "일시정지"}</span>
    </button>
  );
}

/** 점검 종료(중지) 버튼 + 확인 상자. 종료는 되돌릴 수 없어 한 번 더 묻는다. */
export function useStopConfirm(label: string): { button: ReactNode; box: ReactNode } {
  const g = useUrlGates();
  const jobUncertain = useUrl((st) => st.uncertain);
  const jobVerified = useUrl((st) => st.verified);
  const [open, setOpen] = useState(false);
  const button = (
    <>
      {jobUncertain || !jobVerified ? <UrlRefreshButton small={false} /> : null}
      {g.canStop ? (
        <button className={cx(s.btn, s.danger)} type="button" aria-expanded={open} onClick={() => setOpen(true)}>
          <Icon name="stop" />
          {label}
        </button>
      ) : null}
    </>
  );
  const box =
    open && g.canStop ? (
      <div className={s.confirm} role="alertdialog" aria-labelledby="stopConfirmH">
        <h2 id="stopConfirmH">{`${label}할까요? 다시 시작할 수 없습니다.`}</h2>
        <ul>
          <li>새 요청을 보내지 않습니다. 이미 보낸 요청은 마감 때 결과를 기록합니다.</li>
          <li>그때까지 저장된 결과로 보고서를 만듭니다.</li>
          <li>잠시 멈추려면 일시정지를 쓰세요.</li>
        </ul>
        <div className={s.acts}>
          <button
            className={cx(s.btn, s.danger)}
            type="button"
            onClick={() => {
              setOpen(false);
              void runUrlAction("stop");
            }}
          >
            {label}
          </button>
          <button className={s.btn} type="button" onClick={() => setOpen(false)}>
            계속 점검
          </button>
        </div>
      </div>
    ) : null;
  return { button, box };
}

/** 경로 상세 (범위 확인 · 자산 탐색 공통): 기본 정보 · 실제 요청 · 응답. */
function EndpointDetail({ endpoint, back }: { endpoint: WorkflowEndpoint | null; back?: () => void }) {
  const [tab, setTab] = useState<"info" | "http">("info");
  const e = endpoint;
  if (!e) {
    return (
      <>
        <div className={s["vi-head"]}>
          <div>
            <h2>경로를 고르세요</h2>
          </div>
          <CloseInspButton label="경로 상세" />
        </div>
        <p className={s.empty}>왼쪽에서 경로를 누르면 서버가 저장한 관찰이 나옵니다.</p>
      </>
    );
  }
  const k = kindOf(e);
  return (
    <>
      {back ? (
        <button className={s.back} type="button" onClick={back}>
          <Icon name="chev" />
          찾은 경로 목록
        </button>
      ) : null}
      <div className={s["vi-head"]}>
        <div>
          <div className={s.eyebrow}>{`찾은 경로 · ${group(e)}`}</div>
          <h2 className={s["vi-ep"]}>
            <span className={cx(s.m, s[e.method])}>{e.method}</span> <span className={s.p}>{e.path}</span>
          </h2>
        </div>
        <CloseInspButton label="경로 상세" />
      </div>
      <div className={s["vi-chips"]}>
        <span className={cx(s.tag, kindTag(k))}>{KIND_TEXT[k]}</span>
        {hasId(e.path) ? <span className={s.tag}>ID 포함</span> : null}
        <span className={s.tag}>{e.visited ? "응답 관찰" : "미방문"}</span>
      </div>
      <div className={s["seg-tabs"]} role="tablist" aria-label="경로 상세">
        {(
          [
            ["info", "기본 정보"],
            ["http", "요청 · 응답"],
          ] as const
        ).map(([id, label]) => (
          <button key={id} type="button" role="tab" aria-selected={tab === id} onClick={() => setTab(id)}>
            {label}
          </button>
        ))}
      </div>
      {tab === "info" ? (
        <div className={s.isec}>
          <h3>관찰한 사실</h3>
          <Kv
            rows={[
              ["응답", e.status !== undefined ? <Sc key="sc" status={e.status} /> : "관찰 없음"],
              [
                "찾은 곳",
                <span key="from" className={s.mono}>
                  {e.from}
                </span>,
              ],
              ["출처", e.source],
              ["종류", e.kind],
              ["중복 발견", `${e.duplicates}회`],
              ...(e.observation_error ? [["관찰 실패", e.observation_error] as [string, ReactNode]] : []),
            ]}
          />
        </div>
      ) : (
        <div className={s.isec}>
          {e.request || e.response ? (
            <>
              {e.request ? <CodeCard title="요청" src="실제 관찰" text={e.request} id={`ep-req-${e.id}`} /> : null}
              {e.response ? <CodeCard title="응답" src="실제 관찰" text={e.response} id={`ep-res-${e.id}`} /> : null}
            </>
          ) : (
            <p className={s.empty}>저장된 요청 · 응답이 없습니다.</p>
          )}
        </div>
      )}
    </>
  );
}

/** 찾은 경로 묶음 카드 (자산 탐색 오른쪽). */
function EndpointCards({
  endpoints,
  onSelect,
}: {
  endpoints: WorkflowEndpoint[];
  onSelect: (e: WorkflowEndpoint) => void;
}) {
  const groups = [...new Set(endpoints.map(group))];
  return (
    <div className={s.gcs}>
      {groups.map((name) => {
        const rows = endpoints.filter((e) => group(e) === name);
        const n = (k: Kind) => rows.filter((e) => kindOf(e) === k).length;
        return (
          <section className={s.gc} key={name} aria-label={`${name} ${rows.length}개`}>
            <div className={s["gc-h"]}>
              <b>{name}</b>
              <span className={s.n}>{rows.length}</span>
              <span className={s["gc-s"]}>
                {n("ap") ? <span className={s.w}>{`승인 ${n("ap")}`}</span> : null}
                {n("ex") ? <span>{`제외 ${n("ex")}`}</span> : null}
              </span>
            </div>
            <div className={s.kbar} aria-hidden="true">
              {(["get", "ap", "ex"] as const).map((k) =>
                n(k) ? <i key={k} className={s[`k-${k}`]} style={{ flex: n(k) }} /> : null,
              )}
            </div>
            <div className={s.pills}>
              {rows.map((e) => {
                const k = kindOf(e);
                return (
                  <button
                    className={cx(s.pp, k === "ap" && s.ap, k === "ex" && s.ex, k === "get" && s.get)}
                    key={e.id}
                    type="button"
                    aria-label={`${e.method} ${e.path}, 상세 보기`}
                    onClick={() => onSelect(e)}
                  >
                    <span className={cx(s.pm, s[e.method])}>{e.method}</span>
                    <span className={s.pt}>{e.path}</span>
                    {k !== "get" ? <span className={s.pf}>{k === "ap" ? "승인" : "제외"}</span> : null}
                  </button>
                );
              })}
            </div>
          </section>
        );
      })}
    </div>
  );
}

/** 자산 탐색. */
export function WorkflowCrawlLive() {
  const job = useUrl((st) => st.job),
    flow = useUrl((st) => st.flow),
    verified = useUrl((st) => st.flowVerified),
    host = useUrlHost(),
    { goTo, openInsp } = useShell();
  const [selected, setSelected] = useState<string | null>(null);
  const [view, setView] = useState("card");
  const [filter, setFilter] = useState("all");
  const stop = useStopConfirm("점검 취소");
  const endpoints = flow?.endpoints ?? [];
  const shown = endpoints.filter((e) => filter === "all" || kindOf(e) === filter);
  const detail = endpoints.find((e) => e.id === selected) ?? null;
  const count = (k: Kind) => endpoints.filter((e) => kindOf(e) === k).length;
  const select = (e: WorkflowEndpoint) => {
    setSelected(e.id);
    openInsp();
  };
  const next = flow?.mode === "user_approval_v1" ? "scope" : "live";
  const ready = verified && !!flow?.initial_completed;
  return (
    <>
      <UrlHead
        title="자산 탐색"
        crumb="자산 탐색"
        host={host}
        phase="crawl"
        pill={flow ? <span className={cx(s.pill, crawlTone(flow))}>{crawlPill(flow, verified)}</span> : undefined}
        acts={
          <>
            <PauseButton />
            {stop.button}
            <InspOpenButton>찾은 경로</InspOpenButton>
            <button className={cx(s.btn, s.pri)} type="button" disabled={!ready} onClick={() => goTo(next)}>
              {next === "scope" ? "범위 확인" : "점검 진행"}
              <Icon name="arrow" />
            </button>
          </>
        }
      />
      <div className={s.vb}>
        <div className={s.vm}>
          {stop.box}
          <UrlNotices />
          <WorkflowNotice />
          {!job ? <p className={s.empty}>이 브라우저에서 이어 보는 점검이 없습니다.</p> : null}
          {job && !flow ? (
            <p className={s.empty}>
              {job.input.workflow_mode
                ? "탐색 결과가 저장되면 여기에 나옵니다."
                : "이 점검은 자산 탐색 단계가 없습니다."}
            </p>
          ) : null}
          {job && flow ? (
            <>
              <div className={s["crwl-top"]}>
                <div className={s.now}>
                  <span className={s.k}>지금 보는 곳</span>
                  <span id="cNow" className={s.mono}>
                    {flow.current_path || flowStageText[flow.stage]}
                  </span>
                </div>
                <div className={s.pmeta}>
                  <span>
                    방문 <b id="cVis">{flow.counts.visited}</b> / 찾은 경로 <b id="cKnown">{flow.counts.found}</b>
                  </span>
                  <span>
                    방문 대기 <b id="cQueue">{flow.counts.queued}</b>
                  </span>
                  <span>
                    페이지 상한 <b>{flow.limits.max_pages}</b>
                  </span>
                </div>
              </div>
              <div className={s.cnts}>
                {(
                  [
                    ["cFound", "찾은 경로", flow.counts.found, null],
                    ["cSent", "보낸 요청", flow.counts.sent, null],
                    ["cAp", "보낼 때 승인", count("ap"), "w"],
                    ["cSkip", "막은 요청", flow.counts.blocked, null],
                  ] as const
                ).map(([id, label, value, tone]) => (
                  <div className={s.cn} key={id}>
                    <div className={cx(s.v, tone && s[tone])} id={id}>
                      {value}
                    </div>
                    <div className={s.k}>{label}</div>
                  </div>
                ))}
              </div>
              <section className={s.sec} aria-labelledby="cLogH">
                <div className={s.sh}>
                  <h2 id="cLogH">탐색 기록</h2>
                </div>
                <div className={s.log} id="cLog">
                  <div className={s.lh}>
                    <span>시각</span>
                    <span>메서드</span>
                    <span>경로</span>
                    <span>결과</span>
                  </div>
                  {flow.events.length ? (
                    flow.events.map((event) =>
                      event.method ? (
                        <div className={s.lr} key={`${event.at}:${event.kind}:${event.method}:${event.path}`}>
                          <span className={s.t}>{hms(event.at)}</span>
                          <span className={cx(s.m, s[event.method])}>{event.method}</span>
                          <span className={s.p}>{event.path || "—"}</span>
                          <span>{event.status ? <Sc status={event.status} /> : event.kind}</span>
                        </div>
                      ) : (
                        <div className={cx(s.lr, s.sys)} key={`${event.at}:${event.kind}`}>
                          <span className={s.t}>{hms(event.at)}</span>
                          <span>{event.kind}</span>
                        </div>
                      ),
                    )
                  ) : (
                    <p className={s.empty}>아직 기록이 없습니다.</p>
                  )}
                </div>
              </section>
            </>
          ) : null}
        </div>
        <aside className={s.vi} aria-label="찾은 경로">
          {detail ? (
            <EndpointDetail endpoint={detail} back={() => setSelected(null)} />
          ) : (
            <>
              <div className={s["vi-head"]}>
                <div>
                  <div className={s.eyebrow}>탐색하는 대로 채워짐</div>
                  <h2>
                    찾은 경로 <span className={s.num}>{endpoints.length}</span>
                  </h2>
                </div>
                <CloseInspButton label="찾은 경로" />
              </div>
              {endpoints.length ? (
                <div style={{ display: "flex", flexDirection: "column", gap: 7, marginTop: 10 }}>
                  <div className={s.kbar} aria-hidden="true">
                    {(["get", "ap", "ex"] as const).map((k) =>
                      count(k) ? <i key={k} className={s[`k-${k}`]} style={{ flex: count(k) }} /> : null,
                    )}
                  </div>
                  <div className={s.kleg}>
                    <span>
                      <i style={{ background: "var(--accent-line)" }} />
                      {`자동 포함 ${count("get")}`}
                    </span>
                    <span>
                      <i style={{ background: "var(--warn)" }} />
                      {`보낼 때 승인 ${count("ap")}`}
                    </span>
                    <span>
                      <i
                        className={s["k-ex"]}
                        style={{
                          background: "repeating-linear-gradient(135deg,var(--rule-2) 0 3px,transparent 3px 6px)",
                        }}
                      />
                      {`제외 ${count("ex")}`}
                    </span>
                  </div>
                </div>
              ) : null}
              <div className={s.isec} style={{ gap: 12 }}>
                <div className={s.fctl}>
                  <div className={s.seg} role="radiogroup" aria-label="보기 방식">
                    {[
                      ["card", "묶음별"],
                      ["tree", "주소 구조"],
                    ].map(([id, label]) => (
                      <label key={id}>
                        <input
                          type="radio"
                          name="cView"
                          value={id}
                          checked={view === id}
                          onChange={() => setView(id)}
                        />
                        <span>{label}</span>
                      </label>
                    ))}
                  </div>
                  <div className={s.seg} role="radiogroup" aria-label="보이는 경로">
                    {[
                      ["all", "전체"],
                      ["ap", "승인 필요"],
                      ["ex", "제외"],
                    ].map(([id, label]) => (
                      <label key={id}>
                        <input type="radio" name="cKf" checked={filter === id} onChange={() => setFilter(id)} />
                        <span>{label}</span>
                      </label>
                    ))}
                  </div>
                </div>
                <div id="cTree">
                  {view === "card" ? (
                    <EndpointCards endpoints={shown} onSelect={select} />
                  ) : (
                    <ul className={s["sm-root"]}>
                      {[...shown]
                        .sort((a, b) => a.path.localeCompare(b.path))
                        .map((e) => (
                          <li key={e.id}>
                            <button type="button" className={s["sm-t"]} onClick={() => select(e)}>
                              <span className={cx(s.smm, s[e.method], kindOf(e) === "ex" && s.off)}>{e.method}</span>{" "}
                              {e.path}
                            </button>
                          </li>
                        ))}
                    </ul>
                  )}
                </div>
              </div>
            </>
          )}
        </aside>
        <PaneToggle />
      </div>
    </>
  );
}

/** 범위 확인: 찾은 경로 중 심화 점검할 경로를 고른다. 선택은 개별 HTTP 송신 승인을 대신하지 않는다. */
function ScopeSelection({ flow, head }: { flow: WorkflowSnapshot; head: (start: ReactNode) => ReactNode }) {
  const eligible = (e: WorkflowEndpoint) => e.in_scope && (e.change === "attested_read" || flow.reversible_writes);
  const waiting = flow.stage === "awaiting_selection";
  // 시안처럼 사전 확인한 읽기 경로는 기본 포함, 데이터를 바꿀 수 있는 경로는 직접 고른다
  const [selected, setSelected] = useState<Set<string>>(() => {
    const server = flow.endpoints.filter((e) => e.selected).map((e) => e.id);
    if (server.length || !waiting) return new Set(server);
    return new Set(flow.endpoints.filter((e) => eligible(e) && e.change === "attested_read").map((e) => e.id));
  });
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState("all");
  const [open, setOpen] = useState<Record<string, boolean>>({});
  const [detail, setDetail] = useState<string | null>(null);
  const busy = useUrl((st) => st.flowBusy),
    verified = useUrl((st) => st.flowVerified),
    uncertain = useUrl((st) => st.flowUncertain),
    { openInsp } = useShell();
  const isOn = (e: WorkflowEndpoint) => (waiting ? selected.has(e.id) : e.selected);
  const FILTERS = [
    ["all", "전체", () => true],
    ["chg", "데이터 변경", (e: WorkflowEndpoint) => e.change === "possible"],
    ["id", "ID 포함", (e: WorkflowEndpoint) => hasId(e.path)],
    ["off", "제외됨", (e: WorkflowEndpoint) => !eligible(e)],
  ] as const;
  const test = FILTERS.find(([id]) => id === filter)?.[2] ?? (() => true);
  const shown = useMemo(
    () =>
      flow.endpoints.filter(
        (e) => test(e) && `${e.method} ${e.path}`.toLowerCase().includes(query.trim().toLowerCase()),
      ),
    [flow.endpoints, test, query],
  );
  const groups = [...new Set(shown.map(group))];
  const allOpen = groups.length > 0 && groups.every((g) => open[g] ?? !!query);
  const toggle = (id: string) =>
    setSelected((old) => {
      const next = new Set(old);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  const locked = !waiting || busy || uncertain || !verified;
  const inCount = flow.endpoints.filter(isOn).length;
  const apCount = flow.endpoints.filter((e) => isOn(e) && e.change === "possible").length;
  return (
    <>
      {head(<ScopeStart flow={flow} ids={[...selected]} id="sStart" />)}
      <div className={s.vb}>
        <div className={s.vm}>
          <UrlNotices />
          <WorkflowNotice />
          <div className={s.scount} id="sCount">
            <span>
              엔드포인트<b>{flow.endpoints.length}</b>
            </span>
            <span>
              포함<b>{inCount}</b>
            </span>
            <span>
              제외<b>{flow.endpoints.filter((e) => !eligible(e)).length}</b>
            </span>
            <span className={s.ap}>
              승인 필요<b>{apCount}</b>
            </span>
          </div>
          <div className={s.filters}>
            {/* biome-ignore lint/a11y/useSemanticElements: 시안 마크업 그대로 */}
            <div className={s.filters} role="group" aria-label="보기 필터" id="sFilters">
              {FILTERS.map(([id, label, f]) => (
                <button
                  className={s.fchip}
                  key={id}
                  type="button"
                  aria-pressed={filter === id}
                  onClick={() => setFilter(id)}
                >
                  {label}
                  <span className={s.c}>{flow.endpoints.filter(f).length}</span>
                </button>
              ))}
            </div>
            <input
              className={cx(s.inp, s.search)}
              id="sQ"
              type="search"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="경로 검색"
              aria-label="경로 검색"
            />
            <button
              className={s.sall}
              type="button"
              id="sAll"
              aria-controls="sTable"
              onClick={() => setOpen(Object.fromEntries(groups.map((g) => [g, !allOpen])))}
            >
              {allOpen ? "모두 접기" : "모두 펼치기"}
            </button>
          </div>
          {groups.length ? (
            <div className={s.stbl} id="sTable">
              {groups.map((name) => {
                const rows = shown.filter((e) => group(e) === name);
                const expanded = open[name] ?? !!query;
                const gid = `sg-${groups.indexOf(name)}`;
                const on = rows.filter(isOn);
                const ap = on.filter((e) => e.change === "possible").length;
                return (
                  <div key={name}>
                    <button
                      type="button"
                      className={s.sgh}
                      aria-expanded={expanded}
                      aria-controls={gid}
                      onClick={() => setOpen((v) => ({ ...v, [name]: !expanded }))}
                    >
                      <Icon name="chev" />
                      {name}
                      <span className={s.c}>{rows.length}</span>
                      <span className={s.sum}>{`포함 ${on.length}${ap ? ` · 승인 필요 ${ap}` : ""}`}</span>
                    </button>
                    <div className={s.sgrp} id={gid} hidden={!expanded}>
                      {rows.map((e) => {
                        const k = kindOf(e);
                        return (
                          <div className={cx(s.srow, !eligible(e) && s.out, detail === e.id && s.sel)} key={e.id}>
                            <span className={s.ck}>
                              <input
                                type="checkbox"
                                aria-label={`${e.method} ${e.path} 점검에 포함`}
                                checked={isOn(e)}
                                disabled={locked || !eligible(e)}
                                onChange={() => toggle(e.id)}
                              />
                            </span>
                            <button
                              className={s.sb}
                              type="button"
                              onClick={() => {
                                setDetail(e.id);
                                openInsp();
                              }}
                            >
                              <span className={cx(s.m, s[e.method])}>{e.method}</span>
                              <span className={s.p}>{e.path}</span>
                              <span className={s.d}>{e.visited ? (e.status ?? "응답") : "미방문"}</span>
                              <span className={cx(s.tag, kindTag(k))}>
                                {k === "ap" && isOn(e) ? "포함 · 보낼 때 승인" : KIND_TEXT[k]}
                              </span>
                            </button>
                          </div>
                        );
                      })}
                    </div>
                  </div>
                );
              })}
            </div>
          ) : (
            <p className={s.empty}>조건에 맞는 경로가 없습니다.</p>
          )}
          <div className={s.formbar}>
            <ScopeStart flow={flow} ids={[...selected]} />
          </div>
        </div>
        <aside className={s.vi} id="sInsp" aria-label="선택한 경로 상세">
          <EndpointDetail endpoint={flow.endpoints.find((e) => e.id === detail) ?? null} />
        </aside>
        <PaneToggle />
      </div>
    </>
  );
}

/** '선택한 범위로 점검 시작' 버튼 (머리 · 좁은 화면 아래 줄). */
function ScopeStart({ flow, ids, id }: { flow: WorkflowSnapshot; ids: string[]; id?: string }) {
  const busy = useUrl((st) => st.flowBusy),
    verified = useUrl((st) => st.flowVerified),
    uncertain = useUrl((st) => st.flowUncertain),
    { goTo } = useShell();
  if (flow.stage !== "awaiting_selection") {
    return flow.stage === "crawling" ? null : (
      <button className={cx(s.btn, s.pri)} type="button" onClick={() => goTo("live")}>
        점검 진행 보기
        <Icon name="arrow" />
      </button>
    );
  }
  return (
    <button
      id={id}
      className={cx(s.btn, s.pri)}
      type="button"
      disabled={!ids.length || busy || uncertain || !verified || flow.paused}
      onClick={() =>
        void runWorkflowAction("selection", ids).then((ok) => {
          if (ok) goTo("live");
        })
      }
    >
      선택한 범위로 점검 시작
      <Icon name="arrow" />
    </button>
  );
}

/** 범위 확인 화면. 머리의 시작 버튼이 선택 상태를 함께 쓰도록 ScopeSelection 이 머리까지 그린다. */
export function WorkflowScopeLive() {
  const flow = useUrl((st) => st.flow),
    job = useUrl((st) => st.job),
    verified = useUrl((st) => st.flowVerified),
    host = useUrlHost();
  const stop = useStopConfirm("점검 취소");
  const pill = flow ? (
    <span className={cx(s.pill, flow.stage === "awaiting_selection" ? s.wait : s.ok)}>{scopePill(flow, verified)}</span>
  ) : undefined;
  const head = (start: ReactNode) => (
    <>
      <UrlHead
        title="범위 확인"
        crumb="범위 확인"
        host={host}
        phase="scope"
        pill={pill}
        acts={
          <>
            {stop.button}
            <InspOpenButton>상세</InspOpenButton>
            {start}
          </>
        }
      />
      {stop.box !== null ? (
        <div className={s.vm} style={{ flex: "none" }}>
          {stop.box}
        </div>
      ) : null}
    </>
  );
  if (flow) return <ScopeSelection key={`${flow.job_id}:${flow.stage}`} flow={flow} head={head} />;
  return (
    <>
      {head(null)}
      <div className={s.vm}>
        <UrlNotices />
        <WorkflowNotice />
        <p className={s.empty}>
          {job ? "탐색이 끝나면 찾은 경로가 여기에 나옵니다." : "이 브라우저에서 이어 보는 점검이 없습니다."}
        </p>
        {job ? null : (
          <div>
            <a className={cx(s.btn, s.pri)} href="#new">
              <Icon name="plus" />새 점검
            </a>
          </div>
        )}
      </div>
    </>
  );
}

/** 계획 상태 → 시안 줄 모양 (done · run · 그 밖). */
function planClass(state: string): string | undefined {
  const p = planState(state).row;
  if (p === "done") return s.done;
  if (p === "run") return s.run;
  return undefined;
}

/** 점검 진행의 점검 계획 · 지난 활동 (두 칸). */
export function WorkflowActivity() {
  const flow = useUrl((st) => st.flow);
  const [more, setMore] = useState(false);
  if (!flow) return null;
  const plan = flow.plan ?? [];
  const done = plan.filter((p) => planState(p.state).row === "done").length;
  const activity = [...(flow.activity ?? [])].reverse();
  const shown = more ? activity : activity.slice(0, 5);
  return (
    <div className={s["two-col"]}>
      <section className={s.sec} aria-labelledby="lPlanH">
        <div className={s.sh}>
          <h2 id="lPlanH">점검 계획</h2>
          <span className={s.n} id="lPlanN">
            {plan.length ? `${done}/${plan.length} 완료` : ""}
          </span>
        </div>
        {plan.length ? (
          <ol className={s.plan} id="lPlan">
            {plan.map((p) => (
              <li key={p.id} className={planClass(p.state)}>
                <span className={s.st}>{planState(p.state).row === "done" ? <Icon name="check" /> : null}</span>
                <span>
                  {p.title}
                  {planState(p.state).row === "run" || planState(p.state).row === "other"
                    ? ` · ${planState(p.state).label}`
                    : ""}
                </span>
              </li>
            ))}
          </ol>
        ) : (
          <p className={s.empty}>심화 점검이 시작되면 계획이 나옵니다.</p>
        )}
      </section>
      <section className={s.sec} aria-labelledby="lFeedH">
        <div className={s.sh}>
          <h2 id="lFeedH">
            지난 활동 <span className={s.n} id="lFeedN">{`${activity.length}건`}</span>
          </h2>
        </div>
        {activity.length ? (
          <>
            <ul className={s.feed} id="lFeed">
              {shown.map((a) => (
                <li key={a.id}>
                  <span className={s.t}>{hms(a.at)}</span>
                  <span className={cx(s.fd, a.kind === "approval" ? s.blk : s.ok)} />
                  <span>
                    {a.summary}
                    <span className={s.res}>{a.worker}</span>
                  </span>
                </li>
              ))}
            </ul>
            {activity.length > 5 ? (
              <button
                className={cx(s.btn, s.sm, s["feed-more"])}
                type="button"
                aria-controls="lFeed"
                aria-expanded={more}
                onClick={() => setMore(!more)}
              >
                {more ? "접기" : `이전 활동 ${activity.length - 5}건 더 보기`}
              </button>
            ) : null}
          </>
        ) : (
          <p className={s.empty}>아직 활동이 없습니다.</p>
        )}
      </section>
    </div>
  );
}

/** 탐색 · 선택 요약 (보고서 증거 · 감사 탭). */
export function WorkflowReport() {
  const flow = useUrl((st) => st.flow);
  if (!flow) return null;
  return (
    <section className={s.sec} aria-labelledby="rWorkflowH">
      <div className={s.sh}>
        <h2 id="rWorkflowH">탐색 · 선택 범위</h2>
        <span
          className={s.aside}
        >{`${flow.mode === "ai_auto_v1" ? "AI 자동" : "사용자 승인"} · ${flowStageText[flow.stage]}`}</span>
      </div>
      <div className={s.meters}>
        {(
          [
            ["찾은 경로", flow.counts.found],
            ["방문", flow.counts.visited],
            ["보낸 요청", flow.counts.sent],
            ["막은 요청", flow.counts.blocked],
          ] as const
        ).map(([k, v]) => (
          <div className={s.cell} key={k}>
            <div className={s.k}>{k}</div>
            <div className={s.v}>{v}</div>
          </div>
        ))}
      </div>
      <div className={s.list}>
        {flow.endpoints
          .filter((e) => e.selected)
          .map((e) => (
            <div className={s.row} key={e.id}>
              <span className={s["r-main"]}>
                <span className={s["r-t"]}>
                  <span className={cx(s.m, s[e.method])}>{e.method}</span> <span className={s.p}>{e.path}</span>
                </span>
              </span>
              <span className={s["r-side"]}>
                <span className={s.tag}>심화 점검</span>
              </span>
            </div>
          ))}
      </div>
    </section>
  );
}
