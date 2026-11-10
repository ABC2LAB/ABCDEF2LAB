"use client";

import { useMemo, useState } from "react";

import { cx } from "../../lib/cx";
import { refreshWorkflow, runUrlAction, runWorkflowAction, useUrl, useUrlGates } from "../../live/urlDiagnosis";
import { flowStageText, type WorkflowEndpoint, type WorkflowSnapshot } from "../../live/workflow";
import { useShell } from "../../shell";
import { Icon } from "../../ui/icons";
import { CloseInspButton, InspOpenButton } from "../../ui/parts";
import s from "../../workbench.module.css";
import { UrlHead, UrlNoJob, UrlNotices, UrlRefreshButton, useUrlHost } from "./urlParts";

function group(e: WorkflowEndpoint): string {
  if (/user|account|auth|login/i.test(e.path)) return "계정·인증";
  if (/cart|basket|order/i.test(e.path)) return "장바구니·주문";
  if (/admin/i.test(e.path)) return "관리자";
  if (/product|review/i.test(e.path)) return "상품·리뷰";
  if (e.kind === "script") return "정적 자원";
  return "공개 페이지·API";
}
function changeText(e: WorkflowEndpoint): string {
  if (e.change === "blocked") return "제외 · 원 승인/규칙";
  return e.change === "possible" ? "전송 전 개별 승인" : "사전 확인 읽기 범위";
}
export function WorkflowNotice() {
  const error = useUrl((st) => st.flowError),
    uncertain = useUrl((st) => st.flowUncertain),
    verified = useUrl((st) => st.flowVerified);
  return (
    <>
      {error ? (
        <p role="alert" className={s.help}>
          {error}
        </p>
      ) : null}
      {uncertain ? <p role="status">명령 결과 불명 · 조회로 확인하기 전에는 변경 명령을 보내지 않습니다.</p> : null}
      {!uncertain && !verified ? <p className={s.help}>현재 탐색 상태를 확인하는 중입니다.</p> : null}
    </>
  );
}
export function WorkflowControls() {
  const flow = useUrl((st) => st.flow),
    busy = useUrl((st) => st.flowBusy),
    verified = useUrl((st) => st.flowVerified),
    uncertain = useUrl((st) => st.flowUncertain),
    g = useUrlGates(),
    job = useUrl((st) => st.job),
    jobVerified = useUrl((st) => st.verified),
    jobUncertain = useUrl((st) => st.uncertain);
  const active =
    job && ["preparing", "running"].includes(job.state) && flow && ["crawling", "diagnosing"].includes(flow.stage);
  return (
    <>
      {active ? (
        <button
          className={s.btn}
          type="button"
          disabled={!verified || uncertain || busy || !jobVerified || jobUncertain}
          aria-pressed={flow.paused}
          onClick={() => void runWorkflowAction(flow.paused ? "resume" : "pause")}
        >
          <Icon name={flow.paused ? "play" : "pause"} />
          {flow.paused ? "다시 시작" : "일시정지"}
        </button>
      ) : null}
      {jobUncertain || !jobVerified ? <UrlRefreshButton /> : null}
      <button className={s.btn} type="button" disabled={busy} onClick={() => void refreshWorkflow()}>
        탐색 상태 다시 조회
      </button>
      {g.canStop ? (
        <button className={cx(s.btn, s.danger)} type="button" onClick={() => void runUrlAction("stop")}>
          <Icon name="stop" />
          점검 중지
        </button>
      ) : null}
    </>
  );
}
function EndpointDetail({ endpoint }: { endpoint: WorkflowEndpoint | null }) {
  const [tab, setTab] = useState("info");
  const e = endpoint;
  return (
    <>
      <div className={s["vi-head"]}>
        <div>
          <div className={s.eyebrow}>서버가 저장한 발견·관찰</div>
          <h2>{e ? `${e.method} ${e.path}` : "경로를 선택하세요"}</h2>
        </div>
        <CloseInspButton label="경로 상세" />
      </div>
      {e ? (
        <>
          <div className={s["vi-chips"]}>
            <span className={s.tag}>{changeText(e)}</span>
            <span className={s.tag}>{e.visited ? "응답 관찰" : "미방문"}</span>
          </div>
          <div className={s["seg-tabs"]} role="tablist" aria-label="엔드포인트 상세">
            {[
              ["info", "기본 정보"],
              ["request", "실제 요청"],
              ["response", "실제 응답"],
            ].map(([id, label]) => (
              <button key={id} type="button" role="tab" aria-selected={tab === id} onClick={() => setTab(id)}>
                {label}
              </button>
            ))}
          </div>
          {tab === "info" ? (
            <dl className={s.kv}>
              <dt>발견 유형</dt>
              <dd>{e.kind}</dd>
              <dt>출처</dt>
              <dd>
                {e.source} · {e.from}
              </dd>
              <dt>응답</dt>
              <dd>{e.status ?? "관찰 없음"}</dd>
              <dt>승인 범위</dt>
              <dd>{e.in_scope ? "범위 안" : "제외"}</dd>
              <dt>중복 발견</dt>
              <dd>{e.duplicates}</dd>
              <dt>증거 연결</dt>
              <dd>{e.call_ref ?? "전송 근거 없음"}</dd>
            </dl>
          ) : (
            <pre className={s.code}>
              {(tab === "request" ? e.request : e.response) ||
                "실제 관찰이 없습니다. 예시 요청·응답을 만들지 않습니다."}
            </pre>
          )}
          <p className={s.help}>
            비민감 메타데이터입니다. 본문·자격값은 이 탐색 projection에 포함하지 않습니다. 방문·응답은 취약점 확정이
            아닙니다.
          </p>
        </>
      ) : null}
    </>
  );
}
function EndpointGroups({
  endpoints,
  onSelect,
}: {
  endpoints: WorkflowEndpoint[];
  onSelect: (e: WorkflowEndpoint) => void;
}) {
  const groups = [...new Set(endpoints.map(group))];
  return (
    <div className={s.gcs}>
      {groups.map((name) => (
        <section className={s.gc} key={name}>
          <div className={s["gc-h"]}>
            <b>{name}</b>
            <span className={s.n}>{endpoints.filter((e) => group(e) === name).length}</span>
          </div>
          <div className={s.pills}>
            {endpoints
              .filter((e) => group(e) === name)
              .map((e) => (
                <button className={s.pp} key={e.id} type="button" onClick={() => onSelect(e)}>
                  <span className={s.m}>{e.method}</span> <span className={s.p}>{e.path}</span>
                </button>
              ))}
          </div>
        </section>
      ))}
    </div>
  );
}
export function WorkflowCrawlLive() {
  const job = useUrl((st) => st.job),
    flow = useUrl((st) => st.flow),
    verified = useUrl((st) => st.flowVerified),
    host = useUrlHost(),
    { goTo, openInsp } = useShell();
  const [selected, setSelected] = useState<string | null>(null);
  const [view, setView] = useState("card");
  const [filter, setFilter] = useState("all");
  const endpoints = flow?.endpoints ?? [];
  const shown = endpoints.filter((e) => filter === "all" || (filter === "ap" ? e.change === "possible" : !e.in_scope));
  const detail = endpoints.find((e) => e.id === selected) ?? null;
  const select = (e: WorkflowEndpoint) => {
    setSelected(e.id);
    openInsp();
  };
  return (
    <>
      <UrlHead
        title="자산 탐색"
        crumb="자산 탐색"
        host={host}
        pill={
          flow ? (
            <span className={cx(s.pill, flow.paused ? s.wait : s.run)}>
              {!verified ? "현재 상태 미확인" : flowStageText[flow.stage]}
              {verified && flow.paused ? " · 일시정지됨" : ""}
            </span>
          ) : undefined
        }
        acts={
          <>
            <WorkflowControls />
            <InspOpenButton>찾은 경로</InspOpenButton>
            <button
              type="button"
              className={cx(s.btn, s.pri)}
              disabled={!verified || !flow?.initial_completed}
              onClick={() =>
                goTo(flow?.mode === "user_approval_v1" && flow.stage === "awaiting_selection" ? "scope" : "live")
              }
            >
              {flow?.stage === "awaiting_selection" ? "범위 확인" : "점검 진행 보기"}
              <Icon name="arrow" />
            </button>
          </>
        }
      />
      <div className={s.vb}>
        <div className={s.vm}>
          <UrlNotices />
          <WorkflowNotice />
          {!job ? <UrlNoJob what="자산 탐색" /> : null}
          {job && flow ? (
            <>
              <div className={s["crwl-top"]}>
                <div className={s.now}>
                  <span className={s.k}>지금 보는 곳</span>
                  <span id="cNow">{flow.current_path || flowStageText[flow.stage]}</span>
                </div>
                <div
                  className={s.prog}
                  role="progressbar"
                  aria-label="탐색 진행"
                  aria-valuetext="서버는 백분율을 제공하지 않습니다"
                />
                <div className={s.pmeta}>
                  <span>
                    응답 관찰 <b id="cVis">{flow.counts.visited}</b> / 발견 <b id="cKnown">{flow.counts.found}</b>
                  </span>
                  <span>
                    미방문 읽기 경로 <b id="cQueue">{flow.counts.queued}</b>
                  </span>
                </div>
              </div>
              <div className={s.cnts}>
                {[
                  ["cFound", "찾은 경로", flow.counts.found],
                  ["cSent", "요청 쓰기 확인", flow.counts.sent],
                  ["cAp", "전송 전 확인 후보", endpoints.filter((e) => e.in_scope && e.change === "possible").length],
                  ["cSkip", "차단된 요청", flow.counts.blocked],
                ].map(([id, label, value]) => (
                  <div className={s.cn} key={id}>
                    <div className={s.v} id={String(id)}>
                      {value}
                    </div>
                    <div className={s.k}>{label}</div>
                  </div>
                ))}
              </div>
              <section className={s.sec}>
                <div className={s.sh}>
                  <h2>탐색 기록</h2>
                  <span className={s.aside}>최근 저장 이벤트 {flow.events.length}개</span>
                </div>
                <div className={s.log} id="cLog">
                  <div className={s.lh}>
                    <span>시각</span>
                    <span>메서드</span>
                    <span>경로</span>
                    <span>결과</span>
                  </div>
                  {flow.events.map((event) => (
                    <div className={s.lr} key={`${event.at}:${event.kind}:${event.method}:${event.path}`}>
                      <time className={s.t}>{new Date(event.at).toLocaleTimeString("ko-KR")}</time>
                      <span className={s.m}>{event.method ?? "—"}</span>
                      <span className={s.p}>{event.path || "—"}</span>
                      <span>{event.status || event.kind}</span>
                    </div>
                  ))}
                </div>
              </section>
              <p className={s.help}>
                최대 {flow.limits.max_pages}개 페이지의 제한된 정적 탐색입니다. 동적 SPA 전체 탐색이나 미방문 경로의
                성공을 뜻하지 않습니다. 전송 시도 {flow.counts.attempted} · 결과 불명 {flow.counts.unknown}
              </p>
            </>
          ) : null}
          {job && !flow ? (
            <p className={s.empty}>실제 작업을 준비하고 있습니다. 시작한 회차의 탐색 결과가 저장되면 표시합니다.</p>
          ) : null}
        </div>
        <aside className={s.vi} aria-label="찾은 경로">
          {detail ? (
            <>
              <button className={s.back} type="button" onClick={() => setSelected(null)}>
                찾은 경로 목록
              </button>
              <EndpointDetail endpoint={detail} />
            </>
          ) : (
            <>
              <div className={s["vi-head"]}>
                <div>
                  <div className={s.eyebrow}>실제 발견이 저장되는 대로 표시</div>
                  <h2>
                    찾은 경로 <span className={s.num}>{endpoints.length}</span>
                  </h2>
                </div>
                <CloseInspButton label="찾은 경로" />
              </div>
              <div className={s.isec}>
                <div className={s.seg} role="radiogroup" aria-label="보기 방식">
                  {[
                    ["card", "묶음별"],
                    ["tree", "주소 구조"],
                  ].map(([id, label]) => (
                    <label key={id}>
                      <input type="radio" name="cView" value={id} checked={view === id} onChange={() => setView(id)} />
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
                <div id="cTree">
                  {view === "card" ? (
                    <EndpointGroups endpoints={shown} onSelect={select} />
                  ) : (
                    <ul className={s["sm-root"]}>
                      {[...shown]
                        .sort((a, b) => a.path.localeCompare(b.path))
                        .map((e) => (
                          <li key={e.id}>
                            <button type="button" className={s["sm-t"]} onClick={() => select(e)}>
                              {e.method} {e.path}
                            </button>
                          </li>
                        ))}
                    </ul>
                  )}
                </div>
                <p className={s.help}>묶음은 경로를 기준으로 표시합니다.</p>
              </div>
            </>
          )}
        </aside>
      </div>
    </>
  );
}
function ScopeSelection({ flow }: { flow: WorkflowSnapshot }) {
  const [selected, setSelected] = useState<Set<string>>(
    () => new Set(flow.endpoints.filter((e) => e.selected).map((e) => e.id)),
  );
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState("all");
  const [open, setOpen] = useState<Record<string, boolean>>({});
  const [detail, setDetail] = useState<string | null>(null);
  const busy = useUrl((st) => st.flowBusy),
    verified = useUrl((st) => st.flowVerified),
    uncertain = useUrl((st) => st.flowUncertain),
    { goTo, openInsp } = useShell();
  const waiting = flow.stage === "awaiting_selection";
  const eligible = (e: WorkflowEndpoint) => e.in_scope && (e.change === "attested_read" || flow.reversible_writes);
  const shown = useMemo(
    () =>
      flow.endpoints.filter(
        (e) =>
          (filter === "all" ||
            (filter === "chg" && e.change === "possible") ||
            (filter === "id" && /\d/.test(e.path)) ||
            (filter === "off" && !e.in_scope)) &&
          `${e.method} ${e.path}`.toLowerCase().includes(query.toLowerCase()),
      ),
    [flow.endpoints, filter, query],
  );
  const groups = [...new Set(shown.map(group))];
  const choose = (ids: string[]) =>
    setSelected((old) => {
      const next = new Set(old);
      const all = ids.every((id) => next.has(id));
      for (const id of ids) {
        if (all) next.delete(id);
        else next.add(id);
      }
      return next;
    });
  return (
    <div className={s.vb}>
      <div className={s.vm}>
        <UrlNotices />
        <WorkflowNotice />
        <div className={s.scount} id="sCount">
          <span>
            엔드포인트 <b>{flow.endpoints.length}</b>
          </span>
          <span>
            선택 <b>{waiting ? selected.size : flow.endpoints.filter((e) => e.selected).length}</b>
          </span>
          <span>
            제외 <b>{flow.endpoints.filter((e) => !eligible(e)).length}</b>
          </span>
        </div>
        <p className={s.help}>
          엔드포인트 선택은 검사 대상 선택입니다. 개별 HTTP 송신 승인을 대신하지 않습니다. 새로 발견한 경로는 심화 검사
          범위에 자동 추가되지 않습니다.
        </p>
        <div className={s.filters}>
          <fieldset className={s.filters} aria-label="보기 필터" style={{ border: 0, padding: 0, margin: 0 }}>
            {[
              ["all", "전체"],
              ["chg", "데이터 변경 가능"],
              ["id", "숫자 경로"],
              ["off", "제외됨"],
            ].map(([id, label]) => (
              <button
                className={s.fchip}
                key={id}
                type="button"
                aria-pressed={filter === id}
                onClick={() => setFilter(id)}
              >
                {label}
              </button>
            ))}
          </fieldset>
          <input
            className={cx(s.inp, s.search)}
            id="sQ"
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="경로 검색"
            aria-label="경로 검색"
          />
        </div>
        <div className={s.stbl} id="sTable">
          {groups.map((name) => {
            const rows = shown.filter((e) => group(e) === name),
              expanded = open[name] ?? !!query;
            return (
              <div key={name}>
                <div className={s.sh}>
                  <button
                    type="button"
                    className={s.sgh}
                    aria-expanded={expanded}
                    onClick={() => setOpen((v) => ({ ...v, [name]: !expanded }))}
                  >
                    <Icon name="chev" />
                    {name}
                    <span className={s.c}>{rows.length}</span>
                  </button>
                  <button
                    className={cx(s.btn, s.sm)}
                    type="button"
                    disabled={!waiting || busy || !verified || uncertain}
                    onClick={() => choose(rows.filter(eligible).map((e) => e.id))}
                  >
                    묶음 선택/해제
                  </button>
                </div>
                <div hidden={!expanded} className={s.sgrp}>
                  {rows.map((e) => (
                    <div className={cx(s.srow, !eligible(e) && s.out, detail === e.id && s.sel)} key={e.id}>
                      <span className={s.ck}>
                        <input
                          type="checkbox"
                          aria-label={`${e.method} ${e.path} 점검에 포함`}
                          checked={waiting ? selected.has(e.id) : e.selected}
                          disabled={!waiting || !eligible(e) || busy || uncertain || !verified}
                          onChange={() => choose([e.id])}
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
                        <span className={s.m}>{e.method}</span>
                        <span className={s.p}>{e.path}</span>
                        <span className={s.tag}>{changeText(e)}</span>
                      </button>
                    </div>
                  ))}
                </div>
              </div>
            );
          })}
        </div>
        {!waiting ? (
          <p className={s.help}>
            서버가 선택을 확정했거나 아직 초기 탐색 중입니다. 이 단계에서 선택 범위를 변경하지 않습니다.
          </p>
        ) : (
          <button
            id="sStart"
            className={cx(s.btn, s.pri)}
            type="button"
            disabled={!selected.size || busy || uncertain || !verified || flow.paused}
            onClick={() =>
              void runWorkflowAction("selection", [...selected]).then((ok) => {
                if (ok) goTo("live");
              })
            }
          >
            선택한 범위로 심화 점검 시작
            <Icon name="arrow" />
          </button>
        )}
        <button
          type="button"
          className={s.btn}
          onClick={() => setOpen(Object.fromEntries(groups.map((g) => [g, !groups.every((g) => open[g])])))}
        >
          모두 펼치기/접기
        </button>
      </div>
      <aside className={s.vi} id="sInsp" aria-label="선택한 경로 상세">
        <EndpointDetail endpoint={flow.endpoints.find((e) => e.id === detail) ?? null} />
      </aside>
    </div>
  );
}
export function WorkflowScopeLive() {
  const flow = useUrl((st) => st.flow),
    host = useUrlHost();
  return (
    <>
      <UrlHead
        title="범위 확인"
        crumb="범위 확인"
        host={host}
        acts={
          <>
            <WorkflowControls />
            <InspOpenButton>상세</InspOpenButton>
          </>
        }
      />
      {flow ? (
        <ScopeSelection key={flow.job_id} flow={flow} />
      ) : (
        <div className={s.vm}>
          <UrlNotices />
          <WorkflowNotice />
          <p className={s.empty}>저장된 탐색 결과를 기다리고 있습니다.</p>
        </div>
      )}
    </>
  );
}
export function WorkflowActivity() {
  const flow = useUrl((st) => st.flow);
  if (!flow) return null;
  return (
    <>
      <section className={s.sec}>
        <div className={s.sh}>
          <h2>점검 계획</h2>
          <span className={s.n}>{flow.plan?.length ?? "—"}</span>
        </div>
        <ol className={s.plan} id="lPlan">
          {flow.plan?.map((p) => (
            <li key={p.id}>
              <span>{p.title}</span>
              <span className={s.pill}>{p.state}</span>
            </li>
          ))}
        </ol>
      </section>
      <section className={s.sec}>
        <div className={s.sh}>
          <h2>지난 활동</h2>
        </div>
        <ul className={s.feed} id="lFeed">
          {flow.activity?.map((a) => (
            <li key={a.id}>
              <time>{new Date(a.at).toLocaleTimeString("ko-KR")}</time>
              <span>{a.worker}</span>
              <span>{a.summary}</span>
            </li>
          ))}
        </ul>
      </section>
    </>
  );
}

export function WorkflowReport() {
  const flow = useUrl((st) => st.flow);
  if (!flow) return null;
  return (
    <section className={s.sec} aria-labelledby="rWorkflowH">
      <div className={s.sh}>
        <h2 id="rWorkflowH">실제 탐색·선택 범위</h2>
      </div>
      <WorkflowNotice />
      <p>
        {flow.mode === "ai_auto_v1" ? "AI 자동" : "사용자 승인"} · {flowStageText[flow.stage]} · 심화 Worker 마감{" "}
        {flow.deep_closed}/{flow.deep_runs}
      </p>
      <div className={s.meters}>
        {[
          ["발견", flow.counts.found],
          ["응답 관찰", flow.counts.visited],
          ["요청 쓰기 확인", flow.counts.sent],
          ["차단", flow.counts.blocked],
          ["불명", flow.counts.unknown],
        ].map(([k, v]) => (
          <div className={s.cell} key={k}>
            <div className={s.k}>{k}</div>
            <div className={s.v}>{v}</div>
          </div>
        ))}
      </div>
      <div className={s.list}>
        {flow.endpoints.map((e) => (
          <div className={s.row} key={e.id}>
            <span className={s["r-main"]}>
              <b>
                {e.method} {e.path}
              </b>
              <span className={s["r-s"]}>
                {e.source} · {changeText(e)} · {e.visited ? `응답 ${e.status ?? "미확인"}` : "미방문"}
              </span>
            </span>
            <span>{e.selected ? "심화 선택" : "미선택"}</span>
          </div>
        ))}
      </div>
      <p className={s.help}>
        발견·방문·선택은 취약점 확정과 별개입니다. 동적 SPA 전체 탐색은 지원하지 않습니다. 저장 HTML에 당시 탐색 결과와
        승인·거부 이력이 포함됩니다.
      </p>
    </section>
  );
}
