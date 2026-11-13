"use client";

// 탐색 · 선택 워크플로 화면 (시안 짜임): 자산 탐색 · 범위 확인, 그리고 점검 진행의 계획 · 활동.
// 서버가 저장한 발견 · 관찰만 보인다. 묶음은 경로 규칙으로 나눈 것이라 AI 추정이라고 부르지 않는다.
// 서버가 주지 않는 시안 칸(로그인 필요 여부 · 경로 기능 설명 · 요청 횟수 · 시작 전 점검 · 새 경로 처리 규칙 · 기록 한 줄 설명)은
// 자리를 두지 않고 뺀다. 탐색 막대는 서버 진행률이 아니라 페이지 상한 대비 방문 수다.

import { Fragment, type ReactNode, useEffect, useId, useRef, useState } from "react";

import { cx } from "../../lib/cx";
import { clockOf, planState } from "../../live/map";
import { maskSecrets } from "../../live/mask";
import { retryWorkflowAction, runUrlAction, runWorkflowAction, useUrl, useUrlGates } from "../../live/urlDiagnosis";
import { flowStageText, type WorkflowEndpoint, type WorkflowSnapshot } from "../../live/workflow";
import { useShell, type View } from "../../shell";
import { Icon } from "../../ui/icons";
import { CloseInspButton, CodeCard, InspOpenButton, Kv, PaneToggle } from "../../ui/parts";
import s from "../../workbench.module.css";
import { UrlHead, UrlNotices, UrlRefreshButton, useUrlHost } from "./urlParts";

/** 경로 규칙 묶음 전체 (group 이 돌려줄 수 있는 이름). 자산 탐색 묶음 카드 순서 (시안 GORD + 정적 자원). */
export const GROUPS = [
  "공개 페이지·API",
  "상품·리뷰",
  "계정·인증",
  "장바구니·주문",
  "결제·쿠폰",
  "관리자",
  "정적 자원",
] as const;
/** 범위 확인 표의 묶음 순서 (시안 SG + 정적 자원). */
const SCOPE_GROUPS: readonly string[] = [
  "계정·인증",
  "장바구니·주문",
  "결제·쿠폰",
  "관리자",
  "상품·리뷰",
  "공개 페이지·API",
  "정적 자원",
];
/** 찾은 경로가 아직 없는 묶음. */
export const missingGroups = (endpoints: readonly WorkflowEndpoint[]) =>
  GROUPS.filter((g) => !endpoints.some((e) => group(e) === g));
/** 서버가 아직 주지 않는 시안 칸에 붙이는 작은 딱지. 칸 모양은 시안대로 두고 값은 비운다(지어내지 않음). */
export function Soon({ why }: { why: string }) {
  return (
    <span className={cx(s.tag, s.out)} title={why}>
      연결 전<span className={s["sr-only"]}>{` · ${why}`}</span>
    </span>
  );
}
/** 값 자리 (서버가 주지 않음). */
export const NONE = "—";

/** 찾은 경로 칸이 비었을 때 글 (시안 v32: 거르기마다 다르게). */
function treeEmpty(filter: string, total: number): string {
  if (!total) return "아직 찾은 경로가 없습니다.";
  if (filter === "ap") return "승인이 필요한 경로가 아직 없습니다.";
  if (filter === "ex") return "제외한 경로가 아직 없습니다.";
  return "조건에 맞는 경로가 없습니다.";
}

/** 경로 → 묶음 이름 (경로 규칙). */
export function group(e: WorkflowEndpoint): string {
  // 관리자 · 결제를 먼저 본다: /admin/users 는 관리자, /checkout 은 주문보다 결제 (시안 묶음과 같게)
  if (/admin/i.test(e.path)) return "관리자";
  if (/pay|checkout|coupon|wallet/i.test(e.path)) return "결제·쿠폰";
  if (/user|account|auth|login/i.test(e.path)) return "계정·인증";
  if (/cart|basket|order/i.test(e.path)) return "장바구니·주문";
  if (/product|review/i.test(e.path)) return "상품·리뷰";
  if (e.kind === "script") return "정적 자원";
  return "공개 페이지·API";
}
/** 보이는 묶음을 정해진 순서로 (목록에 없는 이름은 뒤에). */
function orderGroups(names: Iterable<string>, order: readonly string[]): string[] {
  const rank = (g: string) => (order.includes(g) ? order.indexOf(g) : order.length);
  return [...new Set(names)].sort((a, b) => rank(a) - rank(b));
}
/** 메서드 순서 (시안 MORD): 묶음 안 경로 알약 정렬. */
const MORD = ["GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"];
const byMethodPath = (a: WorkflowEndpoint, b: WorkflowEndpoint) => {
  const r = (m: string) => (MORD.includes(m) ? MORD.indexOf(m) : MORD.length);
  return r(a.method) - r(b.method) || Number(a.path > b.path) - Number(a.path < b.path);
};
/** 경로의 {변수} 를 시안처럼 따로 표시 (원본 pth). */
export function Pth({ p }: { p: string }) {
  const parts = p.split(/(\{\w+\})/);
  return (
    <>
      {parts.map((x, i) =>
        // biome-ignore lint/suspicious/noArrayIndexKey: 고정된 조각 목록
        /^\{\w+\}$/.test(x) ? <i key={i}>{x}</i> : <Fragment key={i}>{x}</Fragment>,
      )}
    </>
  );
}

/** 경로 종류: 제외 · 보낼 때 승인 · 자동 포함(사전 확인 읽기). */
type Kind = "ex" | "ap" | "get";
function kindOf(e: WorkflowEndpoint): Kind {
  if (!e.in_scope || e.change === "blocked") return "ex";
  return e.change === "possible" ? "ap" : "get";
}
const KIND_TEXT: Readonly<Record<Kind, string>> = { ex: "제외", ap: "보낼 때 승인", get: "자동 포함" };
/** 알약 읽기 글에 붙는 처리 (시안 pill aria-label). */
const PILL_FLAG: Readonly<Record<Kind, string>> = { ap: ", 승인 후 전송", ex: ", 제외", get: "" };
/** 종류 → 딱지 색 클래스. */
const kindTag = (k: Kind) => ({ ex: s.ex, ap: s.ap, get: s.in })[k];

/** 탐색 화면 상태 알약 색 · 글 (시안 cState: 탐색 중 · 일시정지됨 · 탐색 완료 · 확인 대기). */
function crawlTone(flow: WorkflowSnapshot, verified: boolean): string | undefined {
  if (!verified) return s.wait;
  if (flow.paused) return undefined;
  if (flow.stage === "crawling" || flow.stage === "diagnosing") return s.run;
  if (flow.stage === "awaiting_selection") return s.wait;
  return flow.stage === "completed" ? s.ok : undefined;
}
function crawlPill(flow: WorkflowSnapshot, verified: boolean): string {
  if (!verified) return "현재 상태 미확인";
  if (flow.paused) return "일시정지됨";
  if (flow.stage === "crawling") return "탐색 중";
  if (flow.stage === "awaiting_selection") return "탐색 완료 · 확인 대기";
  return flowStageText[flow.stage];
}
/**
 * 탐색에 걸린 시간(초). 서버 기록의 탐색 시작(crawl_started) ~ 탐색 끝(crawl_finished), 탐색 중이면 마지막 갱신 시각까지.
 * 기록은 최근 32건만 남아 시작 줄이 빠졌거나, 끝 줄 없이 탐색이 끝났으면 모르는 것으로 둔다(null).
 */
export function crawlElapsed(flow: WorkflowSnapshot): number | null {
  const start = flow.events.find((e) => e.kind === "crawl_started");
  const end =
    flow.events.find((e) => e.kind === "crawl_finished")?.at ?? (flow.stage === "crawling" ? flow.updated_at : null);
  if (!start || !end) return null;
  const ms = Date.parse(end) - Date.parse(start.at);
  return Number.isFinite(ms) && ms >= 0 ? Math.round(ms / 1000) : null;
}
/** 초 → "10초" · "3분 5초" · "1시간 2분". */
export function durText(sec: number): string {
  if (sec < 60) return `${sec}초`;
  if (sec < 3600) return `${Math.floor(sec / 60)}분 ${sec % 60}초`;
  return `${Math.floor(sec / 3600)}시간 ${Math.floor((sec % 3600) / 60)}분`;
}
/**
 * 탐색 막대 % — 실제 방문 수 ÷ 페이지 상한(서버 진행률 아님). 첫 탐색 단계가 끝났다는 initial_completed 는 완전 탐색을
 * 보장하지 않아(부분 완료 · 상한 소진에서도 true) 막대에 섞지 않고 따로 보인다 (리뷰 #479).
 */
export function crawlPct(flow: WorkflowSnapshot): number {
  return Math.min(100, Math.round((flow.counts.visited / Math.max(1, flow.limits.max_pages)) * 100));
}
/** 서버 탐색 상태 (snapshot.exploration_status) → 글. */
const EXPLORE_TEXT = {
  RUNNING: "탐색 중",
  COMPLETED: "탐색 완료",
  PARTIAL: "부분 완료",
  STOPPED: "중지됨",
  NOT_RUN: "탐색 안 함",
} as const;
/**
 * 서버 탐색 상태 · 사유. 파서(live/workflow.ts)에 아직 칸이 없어 응답 원본에서 아는 값만 읽는다
 * (모르는 상태 · 형식이 다른 사유는 버림).
 */
export function exploreState(flow: WorkflowSnapshot): { text: string; reason?: string } | null {
  const raw = flow as unknown as Record<string, unknown>;
  const st = raw.exploration_status;
  if (typeof st !== "string" || !Object.hasOwn(EXPLORE_TEXT, st)) return null;
  const r = raw.exploration_reason;
  const reason = typeof r === "string" && /^[a-z0-9_]{1,64}$/.test(r) ? r : undefined;
  return { text: EXPLORE_TEXT[st as keyof typeof EXPLORE_TEXT], reason };
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

/**
 * 서버 탐색 기록 종류(diagnosisflow 의 기록 kind) → 글 · 결과 칸 딱지 색. 단계 이름 기록은 단계 글을 쓰고,
 * 모르는 종류는 코드 그대로 보인다(뜻을 지어내지 않음).
 */
const EVENT_TEXT: Readonly<Record<string, readonly [string, "in" | "ex" | "out" | ""]>> = {
  created: ["점검을 만들었습니다", ""],
  crawl_started: ["탐색 시작", ""],
  crawl_finished: ["탐색을 마쳤습니다", ""],
  visit_started: ["방문 시작", ""],
  visit_finished: ["관찰함", "in"],
  page_failed: ["관찰 실패", "ex"],
  discovery: ["새 경로 보고 받음", ""],
  api_recon_static: ["정적 분석으로 찾음", "out"],
  exploration_bound: ["탐색 실행기 연결", ""],
  exploration_not_run: ["탐색 실행기를 돌리지 않음", ""],
  exploration_page: ["관찰함", "in"],
  exploration_observed: ["관찰함", "in"],
  exploration_request_failed: ["요청 실패", "ex"],
  exploration_partial: ["탐색 일부만 끝남", ""],
  exploration_finished: ["탐색 실행기 끝", ""],
  exploration_static_finished: ["정적 분석 끝", ""],
  selection: ["점검할 범위를 고름", ""],
  pause: ["일시정지", ""],
  resume: ["다시 시작", ""],
  deep_worker_started: ["심화 점검 시작", ""],
  deep_worker_finished: ["심화 점검 끝", ""],
  failed: ["실패", ""],
};
/** 기록 종류 → 글. */
export function eventText(kind: string): string {
  if (Object.hasOwn(EVENT_TEXT, kind)) return EVENT_TEXT[kind][0];
  if (Object.hasOwn(flowStageText, kind)) return flowStageText[kind as WorkflowSnapshot["stage"]];
  return kind;
}
/**
 * 경로 기록 줄의 결과 칸: 응답 코드가 있으면 코드, 실패 기록이면 '실패' 딱지(시안의 요청 안 함 딱지 자리), 그 밖은 —.
 * 무슨 기록인지는 줄 아래 설명(.ln)에 적는다.
 */
function EventResult({ event }: { event: FlowEvent }) {
  if (event.status) return <Sc status={event.status} />;
  const tone = Object.hasOwn(EVENT_TEXT, event.kind) ? EVENT_TEXT[event.kind][1] : "";
  if (tone === "ex") return <span className={cx(s.tag, s.ex)}>실패</span>;
  return <span className={s.mono}>{NONE}</span>;
}

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

/**
 * 점검 종료(중지) 버튼 + 확인 상자 (시안 lEnd · lConfirm). 종료는 되돌릴 수 없어 한 번 더 묻는다.
 * "종료"(점검 진행)는 빨간 버튼, "점검 취소"(자산 탐색 · 범위 확인)는 시안처럼 보통 버튼이다.
 * 상자가 열리면 초점은 종료 버튼으로, '계속 점검'을 누르면 처음 버튼으로 돌아간다.
 */
export function useStopConfirm(label: "종료" | "점검 취소"): { button: ReactNode; box: ReactNode } {
  const g = useUrlGates();
  const jobUncertain = useUrl((st) => st.uncertain);
  const jobVerified = useUrl((st) => st.verified);
  const flow = useUrl((st) => st.flow);
  const [open, setOpen] = useState(false);
  const opener = useRef<HTMLButtonElement>(null);
  const yes = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (open) yes.current?.focus();
  }, [open]);
  const verb = label === "종료" ? "종료" : "취소";
  // 일시정지는 탐색 흐름이 있는 점검의 탐색 · 심화 진단 중에만 있다 (PauseButton 과 같은 조건)
  const canPause = !!flow && ["crawling", "diagnosing"].includes(flow.stage);
  const button = (
    <>
      {jobUncertain || !jobVerified ? <UrlRefreshButton small={false} /> : null}
      {g.canStop ? (
        <button
          ref={opener}
          className={cx(s.btn, label === "종료" && s.danger)}
          type="button"
          aria-expanded={open}
          onClick={() => setOpen(true)}
        >
          {label === "종료" ? <Icon name="stop" /> : null}
          {label}
        </button>
      ) : null}
    </>
  );
  const box =
    open && g.canStop ? (
      <div className={s.confirm} role="alertdialog" aria-labelledby="stopConfirmH">
        <h2 id="stopConfirmH">{`점검을 ${verb}할까요? ${verb}하면 다시 시작할 수 없습니다.`}</h2>
        <ul>
          <li>새 요청을 보내지 않고, 응답을 받지 못한 요청은 전송 불명으로 기록합니다.</li>
          <li>{`모은 증거와 발견은 남기고, ${verb} 시점까지의 결과로 보고서를 만듭니다.`}</li>
          <li>
            {`끝내지 못한 항목은 미실행으로 남깁니다.${canPause ? " 나중에 이어 하려면 일시정지를 쓰세요." : ""}`}
          </li>
        </ul>
        <div className={s.acts}>
          <button
            ref={yes}
            className={cx(s.btn, s.danger)}
            type="button"
            onClick={() => {
              setOpen(false);
              void runUrlAction("stop");
            }}
          >
            {label}
          </button>
          <button
            className={s.btn}
            type="button"
            onClick={() => {
              setOpen(false);
              opener.current?.focus();
            }}
          >
            계속 점검
          </button>
        </div>
      </div>
    ) : null;
  return { button, box };
}

/** 원문이 없을 때의 카드 (시안 cb-none). */
function NoneCard({ title, text }: { title: string; text: ReactNode }) {
  return (
    <div className={s.cb}>
      <div className={s["cb-h"]}>
        <h3>{title}</h3>
        <span className={s["cb-src"]}>없음</span>
      </div>
      <div className={s["cb-none"]}>
        <Icon name="lock" />
        <span>{text}</span>
      </div>
    </div>
  );
}

/** 탭 줄 화살표 → 옮길 칸 수. */
const TAB_STEP: Readonly<Record<string, number>> = { ArrowRight: 1, ArrowLeft: -1 };
const EP_TABS = [
  ["info", "기본 정보"],
  ["http", "요청·응답"],
] as const;

/**
 * 경로 상세 (범위 확인 · 자산 탐색 공통, 시안 apiDetail): 기본 정보 · 실제 요청 · 응답 탭.
 * 시안의 요청 횟수 · 근거 · 기능(AI 추정)은 서버가 경로마다 주지 않아 뺀다. 요청 · 응답은 예시가 아니라 서버가 저장한 관찰이다.
 */
function EndpointDetail({
  endpoint,
  back,
  chip,
}: {
  endpoint: WorkflowEndpoint | null;
  back?: () => void;
  /** 처리 딱지 (범위 확인은 고른 상태를 따른다, 시안 scopeTag). 없으면 경로 종류 딱지 */
  chip?: ReactNode;
}) {
  const [tab, setTab] = useState<"info" | "http">("info");
  const uid = useId();
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
  const tabId = (id: string) => `${uid}-t-${id}`;
  const panelId = (id: string) => `${uid}-p-${id}`;
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
          <h2>
            <span className={cx(s.m, s[e.method])}>{e.method}</span>{" "}
            <span className={s.p}>
              <Pth p={e.path} />
            </span>
          </h2>
        </div>
        <CloseInspButton label="경로 상세" />
      </div>
      <div className={s["vi-chips"]}>
        {chip ?? <span className={cx(s.tag, kindTag(k))}>{KIND_TEXT[k]}</span>}
        {hasId(e.path) ? <span className={cx(s.tag, s.out)}>ID 포함</span> : null}
      </div>
      <div
        className={cx(s["seg-tabs"], s["api-tabs"])}
        role="tablist"
        aria-label="경로 상세"
        onKeyDown={(ev) => {
          // 시안: 왼쪽 · 오른쪽 화살표로 탭 이동
          const d = TAB_STEP[ev.key] ?? 0;
          if (!d) return;
          ev.preventDefault();
          const i = EP_TABS.findIndex(([id]) => id === tab);
          const next = EP_TABS[(i + d + EP_TABS.length) % EP_TABS.length][0];
          setTab(next);
          document.getElementById(tabId(next))?.focus();
        }}
      >
        {EP_TABS.map(([id, label]) => (
          <button
            key={id}
            id={tabId(id)}
            type="button"
            role="tab"
            aria-controls={panelId(id)}
            aria-selected={tab === id}
            tabIndex={tab === id ? 0 : -1}
            onClick={() => setTab(id)}
          >
            {label}
          </button>
        ))}
      </div>
      <div role="tabpanel" id={panelId("info")} aria-labelledby={tabId("info")} hidden={tab !== "info"}>
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
              // 시안의 요청 횟수 · 근거는 서버가 경로마다 주지 않아 값을 비운다
              ["요청 횟수", NONE],
              ["근거", NONE],
              ["출처", e.source],
              ["종류", e.kind],
              ["중복 발견", `${e.duplicates}회`],
              ...(e.observation_error ? [["관찰 실패", e.observation_error] as [string, ReactNode]] : []),
            ]}
          />
        </div>
        <div className={s.isec}>
          <h3>AI 추정</h3>
          <Kv
            rows={[
              [
                "기능",
                <>
                  {`${NONE} `}
                  <Soon why="서버가 경로의 기능 설명을 주지 않습니다" />
                </>,
              ],
              // 묶음은 AI 추정이 아니라 경로 규칙으로 나눈 것이라 그렇게 적는다
              ["묶음", `${group(e)} · 경로 규칙`],
            ]}
          />
        </div>
      </div>
      <div role="tabpanel" id={panelId("http")} aria-labelledby={tabId("http")} hidden={tab !== "http"}>
        {/* 서버가 가린 값이지만 화면 쪽에서도 한 번 더 가린다 (대시보드와 같은 방어 · 리뷰 #473) */}
        <div className={s.expanel}>
          {e.request ? (
            <CodeCard title="요청" src="실제 관찰" text={maskSecrets(e.request)} id={`ep-req-${e.id}`} />
          ) : (
            <NoneCard title="요청" text="저장된 요청이 없습니다." />
          )}
          {e.response ? (
            <CodeCard title="응답" src="실제 관찰" text={maskSecrets(e.response)} id={`ep-res-${e.id}`} />
          ) : (
            <NoneCard title="응답" text="저장된 응답이 없습니다." />
          )}
        </div>
      </div>
    </>
  );
}

type FlowEvent = WorkflowSnapshot["events"][number];

/**
 * 기록 줄 ↔ 저장된 원문의 관계. `endpoints`는 요청별 기록이 아니라 메서드 · 경로별 현재 상태다(서버 Controller.After 는
 * 송신 때 Request/Response 를 덮어쓰고, 미송신 때는 이전 값을 그대로 둔다). 이벤트에 call_ref 가 없어 건별로 묶을 수 없으므로
 * - unknown: 응답 코드가 없는 줄 → 송신 여부를 단정하지 않고 원문도 붙이지 않는다. 보냈지만 응답을 못 받은 경우
 *   (StatusCode 0)도 status 가 빠져 미송신과 구분되지 않는다(리뷰 #473)
 * - stale: 같은 경로에 더 나중(또는 같은 시각) 송신이 있거나 저장된 응답 코드가 이 줄과 다름 → 이 줄의 원문이 아님
 * - latest: 이 경로의 마지막 송신 줄이고 응답 코드도 같음 → "경로의 마지막 관찰"로 분리해 보인다(같은 요청이라고 단정하지 않음)
 */
export type LogRaw = "unknown" | "stale" | "latest";
export function logRawOf(events: readonly FlowEvent[], i: number, endpoint: WorkflowEndpoint | undefined): LogRaw {
  const ev = events[i];
  if (!ev?.status) return "unknown";
  const at = Date.parse(ev.at);
  const later = events.some(
    (o, j) => j !== i && o.status && o.method === ev.method && o.path === ev.path && !(Date.parse(o.at) < at),
  );
  if (later || !endpoint || endpoint.status !== ev.status) return "stale";
  return "latest";
}

/**
 * 탐색 기록 한 줄을 펼친 칸 (시안 logDetail). 이 기록의 결과와, 같은 경로의 서버 정보(찾은 곳 · 출처 · 종류)를 줄 바로 아래에
 * 보인다. 저장된 요청/응답 원문은 이 줄과 묶였다고 볼 수 있을 때(logRawOf = latest)만 "이 경로의 마지막 관찰"로 보이고,
 * 응답 코드가 없는 줄 · 나중 요청이 있는 줄에는 붙이지 않는다(값을 지어내지 않음 · 서버가 가린 그대로).
 * Esc · 접기로 닫고 초점은 그 줄로 돌아간다.
 */
function LogDetail({
  id,
  event,
  endpoint,
  raw,
  onClose,
}: {
  id: string;
  event: FlowEvent;
  endpoint: WorkflowEndpoint | undefined;
  raw: LogRaw;
  onClose: () => void;
}) {
  let cards: ReactNode;
  if (!endpoint)
    cards = (
      <>
        <NoneCard title="요청" text="이 기록과 같은 찾은 경로 기록이 없어 결과만 보입니다." />
        <NoneCard title="응답" text="저장된 응답이 없습니다." />
      </>
    );
  else if (raw === "unknown")
    cards = (
      <>
        <NoneCard title="요청" text="응답 코드가 없는 기록이라 보냈는지 · 응답을 받았는지 확인할 수 없습니다." />
        <NoneCard title="응답" text="서버에 남은 원문도 이 줄의 것인지 알 수 없어 보이지 않습니다." />
      </>
    );
  else if (raw === "stale")
    cards = (
      <>
        <NoneCard title="요청" text="이 경로에 더 나중 기록이 있어, 서버에 남은 원문은 이 줄의 것이 아닙니다." />
        <NoneCard title="응답" text="서버는 경로마다 마지막 관찰만 저장합니다." />
      </>
    );
  else
    cards = (
      <>
        {endpoint.request ? (
          <CodeCard
            title="요청"
            src="이 경로의 마지막 관찰"
            text={maskSecrets(endpoint.request)}
            id={`lg-req-${endpoint.id}`}
          />
        ) : (
          <NoneCard title="요청" text="저장된 요청이 없습니다." />
        )}
        {endpoint.response ? (
          <CodeCard
            title="응답"
            src="이 경로의 마지막 관찰"
            text={maskSecrets(endpoint.response)}
            id={`lg-res-${endpoint.id}`}
          />
        ) : (
          <NoneCard title="응답" text="저장된 응답이 없습니다." />
        )}
      </>
    );
  return (
    <section
      className={s.lgx}
      id={id}
      aria-label={`${event.method} ${event.path ?? ""} 요청 · 응답`}
      onKeyDown={(e) => {
        if (e.key !== "Escape") return;
        // 작업대 전역 Esc(상세 서랍 닫기)까지 가지 않게 여기서 멈춘다 (리뷰 #473 · HelpTip 과 같은 처리)
        e.stopPropagation();
        onClose();
      }}
    >
      <div className={s["lgx-top"]}>
        <span className={cx(s.m, event.method ? s[event.method] : undefined)}>{event.method}</span>
        <code>{event.path || "—"}</code>
        <button className={cx(s.btn, s.sm)} type="button" onClick={onClose}>
          <Icon name="x" />
          접기
        </button>
      </div>
      <Kv
        rows={[
          ["응답", event.status ? <Sc key="sc" status={event.status} /> : "코드 없음"],
          ["기록", <span key="kind">{eventText(event.kind)}</span>],
          ...(endpoint
            ? ([
                [
                  "찾은 곳",
                  <span key="from" className={s.mono}>
                    {endpoint.from}
                  </span>,
                ],
                ["출처", endpoint.source],
                ["종류", endpoint.kind],
                ...(endpoint.observation_error ? [["관찰 실패", endpoint.observation_error]] : []),
              ] as [string, ReactNode][])
            : []),
          // 시안의 근거 · 기능(AI 추정)은 서버가 주지 않아 값을 비운다
          ["근거", NONE],
          ["기능 (AI 추정)", NONE],
        ]}
      />
      <div className={s["lgx-ex"]}>{cards}</div>
      {endpoint && raw === "latest" && (endpoint.request || endpoint.response) ? (
        <p className={s.help}>
          서버가 이 줄과 원문을 묶어 주지 않아(call_ref 없음), 같은 경로의 마지막 관찰을 보입니다.
        </p>
      ) : null}
    </section>
  );
}

/**
 * 찾은 경로 묶음 카드 (자산 탐색 오른쪽, 시안 renderTree). 묶음 머리 · 막대의 수는 거르기와 상관없이 그 묶음 전체,
 * 알약은 거른 경로만 메서드 · 경로 순으로. 탐색 중 · 전체 보기에서는 아직 경로가 안 나온 묶음을 아래에 적는다.
 */
function EndpointCards({
  all,
  shown,
  missing,
  onSelect,
}: {
  all: WorkflowEndpoint[];
  shown: WorkflowEndpoint[];
  missing: readonly string[];
  onSelect: (e: WorkflowEndpoint) => void;
}) {
  const groups = orderGroups(shown.map(group), GROUPS);
  return (
    <div className={s.gcs}>
      {groups.map((name) => {
        const rows = all.filter((e) => group(e) === name);
        const pills = shown.filter((e) => group(e) === name).sort(byMethodPath);
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
              {pills.map((e) => {
                const k = kindOf(e);
                return (
                  <button
                    className={cx(s.pp, k === "ap" && s.ap, k === "ex" && s.ex, k === "get" && s.get)}
                    key={e.id}
                    type="button"
                    data-ep={e.id}
                    aria-label={`${e.method} ${e.path}${PILL_FLAG[k]}, 요청·응답 보기`}
                    onClick={() => onSelect(e)}
                  >
                    <span className={cx(s.pm, s[e.method])}>{e.method === "DELETE" ? "DEL" : e.method}</span>
                    <span className={s.pt}>
                      <Pth p={e.path} />
                    </span>
                    {k !== "get" ? <span className={s.pf}>{k === "ap" ? "승인" : "제외"}</span> : null}
                  </button>
                );
              })}
            </div>
          </section>
        );
      })}
      {missing.length ? <p className={s["gc-empty"]}>{`아직 찾지 못한 묶음 · ${missing.join(", ")}`}</p> : null}
    </div>
  );
}

// 결과·판정 읽기 화면 진입(접근통제 지도·커버리지·발견 상세·전송 통제 흐름). 전부 아직 예시·운영자 전용.
const RESULT_VIEWS: ReadonlyArray<readonly [View, string]> = [
  ["accessmap", "접근통제 지도"],
  ["coverage", "커버리지"],
  ["finding", "발견 상세"],
  ["e2flow", "전송 통제 흐름"],
];

/**
 * 자산 탐색 헤더의 '진단 결과' 묶음 — 결과·판정 읽기 화면 4개 진입을 버튼 하나로 모은다(헤더 과밀 해소).
 * 최종 자리는 작업대 시안에서 정렬(이동찬 리뷰) — 임시 묶음이라 포털·화살표 이동은 두지 않고 최소 구현한다
 * (바깥 클릭·Esc 로 닫힘). 색은 계정 메뉴와 같은 실재 토큰(--bg·--rule·--shadow)만 써서 라이트·다크 자동.
 */
function ResultViewsMenu({ goTo }: { goTo: (name: View) => void }) {
  const [open, setOpen] = useState(false);
  const wrap = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const onDown = (e: globalThis.MouseEvent) => {
      if (!wrap.current?.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);
  return (
    <div ref={wrap} style={{ position: "relative", display: "inline-flex" }}>
      <button
        className={s.btn}
        type="button"
        aria-haspopup="true"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        진단 결과
        <Icon name="chev" />
      </button>
      {open ? (
        <div
          style={{
            position: "absolute",
            top: "calc(100% + 6px)",
            right: 0,
            zIndex: 40,
            minWidth: 184,
            display: "flex",
            flexDirection: "column",
            gap: 2,
            padding: 6,
            borderRadius: 12,
            border: "1px solid var(--rule)",
            background: "var(--bg)",
            color: "var(--ink)",
            boxShadow: "var(--shadow)",
          }}
        >
          {RESULT_VIEWS.map(([view, label]) => (
            <button
              key={view}
              className={s.btn}
              type="button"
              style={{ justifyContent: "flex-start", width: "100%", border: "none", background: "transparent" }}
              onClick={() => {
                setOpen(false);
                goTo(view);
              }}
            >
              {label}
            </button>
          ))}
          <p className={s.help} style={{ margin: "4px 8px 2px" }}>
            지금은 예시 · 운영자 전용 (API 연결 후 점등)
          </p>
        </div>
      ) : null}
    </div>
  );
}

/** 자산 탐색 (시안 data-view="crawl"). */
export function WorkflowCrawlLive() {
  const job = useUrl((st) => st.job),
    flow = useUrl((st) => st.flow),
    verified = useUrl((st) => st.flowVerified),
    host = useUrlHost(),
    { goTo, openInsp } = useShell();
  const [selected, setSelected] = useState<string | null>(null);
  const [filter, setFilter] = useState("all");
  // 탐색 기록에서 펼친 줄 (한 번에 하나, 같은 줄을 다시 누르면 접힘 · 시안 v75 ~ v76)
  const [openLog, setOpenLog] = useState<string | null>(null);
  const closeLog = (i: number) => {
    setOpenLog(null);
    document.getElementById(`lgRow${i}`)?.focus();
  };
  // 펼친 내용이 보이게 기록 칸을 내리되, 누른 줄은 머리줄 바로 아래까지만 (다시 눌러 접을 수 있게 늘 보이게 · 시안)
  useEffect(() => {
    if (!openLog) return;
    const log = document.getElementById("cLog");
    const row = log?.querySelector<HTMLElement>('button[aria-expanded="true"]');
    const box = row?.nextElementSibling;
    if (!log || !row || !box) return;
    const hh = log.firstElementChild instanceof HTMLElement ? log.firstElementChild.offsetHeight : 0;
    const rr = row.getBoundingClientRect(),
      rb = box.getBoundingClientRect(),
      lb = log.getBoundingClientRect();
    if (rb.bottom > lb.bottom) log.scrollTop += Math.max(0, Math.min(rb.bottom - lb.bottom + 8, rr.top - lb.top - hh));
  }, [openLog]);
  const stop = useStopConfirm("점검 취소");
  const endpoints = flow?.endpoints ?? [];
  const shown = endpoints.filter((e) => filter === "all" || kindOf(e) === filter);
  const detail = endpoints.find((e) => e.id === selected) ?? null;
  const count = (k: Kind) => endpoints.filter((e) => kindOf(e) === k).length;
  const select = (e: WorkflowEndpoint) => {
    setSelected(e.id);
    openInsp();
  };
  // 상세에서 목록으로 돌아오면 누른 알약으로 초점을 돌린다 (시안 cApiClose)
  const back = () => {
    const id = selected;
    setSelected(null);
    window.requestAnimationFrame(() => document.querySelector<HTMLElement>(`[data-ep="${id}"]`)?.focus());
  };
  const next = flow?.mode === "user_approval_v1" ? "scope" : "live";
  const ready = verified && !!flow?.initial_completed;
  const crawling = flow?.stage === "crawling";
  const elapsed = flow ? crawlElapsed(flow) : null;
  const explore = flow ? exploreState(flow) : null;
  // 탐색 단계에서는 GET 만 보낸다(서버 Controller.Before). 심화 점검이 시작되면 보낸 요청에 그 요청도 들어간다
  const readOnly = !!flow && flow.deep_runs === 0 && (crawling || flow.stage === "awaiting_selection");
  let now: ReactNode = null;
  if (flow) {
    // 서버 current_path 는 탐색 · 탐색 실행기의 GET 방문 경로다 (diagnosisflow crawl · exploration)
    if (crawling && flow.current_path)
      now = (
        <>
          <span className={cx(s.m, s.GET)}>GET</span>{" "}
          <span className={s.p}>
            <Pth p={flow.current_path} />
          </span>
        </>
      );
    else if (crawling) now = flowStageText.crawling;
    else now = flow.initial_completed ? "탐색을 마쳤습니다" : flowStageText[flow.stage];
  }
  return (
    <>
      <UrlHead
        title="자산 탐색"
        crumb="자산 탐색"
        host={host}
        phase="crawl"
        pill={
          flow ? <span className={cx(s.pill, crawlTone(flow, verified))}>{crawlPill(flow, verified)}</span> : undefined
        }
        acts={
          <>
            <PauseButton />
            {stop.button}
            <InspOpenButton>찾은 경로</InspOpenButton>
            {/* 탐색 결과(발견·경계 기록, 운영자 전용). 지금은 예시, live 점등은 탐색 실행기 착륙 후 */}
            <button className={s.btn} type="button" onClick={() => goTo("explore")}>
              탐색 결과
            </button>
            {/* 결과·판정 읽기 화면 4개(접근통제 지도·커버리지·발견 상세·전송 통제 흐름)는 전부 아직 예시·운영자
                전용 — '진단 결과' 묶음 버튼 하나로 모은다(헤더 과밀 해소). 최종 자리는 작업대 시안에서 정렬(이동찬
                리뷰). live 점등은 각 화면의 getter API 연결 후. */}
            <ResultViewsMenu goTo={goTo} />
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
                  <span id="cNow">{now}</span>
                </div>
                {/* 탐색 진행: 서버에 진행률이 없어 페이지 상한 대비 방문 수로 그린다 (리뷰 #479) */}
                <div
                  className={s.prog}
                  id="cBar"
                  role="progressbar"
                  aria-label="탐색 진행 (페이지 상한 대비 방문)"
                  aria-valuemin={0}
                  aria-valuemax={100}
                  aria-valuenow={crawlPct(flow)}
                  aria-valuetext={`${crawlPct(flow)}% · 페이지 상한 대비 방문`}
                >
                  <i style={{ width: `${crawlPct(flow)}%` }} />
                </div>
                <div className={s.pmeta}>
                  <span>
                    방문 <b id="cVis">{flow.counts.visited}</b> / 알려진 링크{" "}
                    <b id="cKnown">{flow.counts.visited + flow.counts.queued}</b>
                  </span>
                  <span>
                    방문 대기 <b id="cQueue">{flow.counts.queued}</b>
                  </span>
                  <span>
                    페이지 상한 <b>{flow.limits.max_pages}</b>
                  </span>
                  <span>
                    경과 <b id="cEl">{elapsed !== null ? durText(elapsed) : NONE}</b>
                  </span>
                  {flow.initial_completed ? <span>첫 탐색 단계 끝남</span> : null}
                  {explore ? (
                    <span>
                      탐색 상태 <b>{`${explore.text}${explore.reason ? ` · ${explore.reason}` : ""}`}</b>
                    </span>
                  ) : null}
                </div>
              </div>
              <div className={s.cnts}>
                {(
                  [
                    [
                      "cFound",
                      "찾은 경로",
                      flow.counts.found,
                      null,
                      `조회 ${endpoints.filter((e) => e.change === "attested_read").length} · 데이터 변경 ${endpoints.filter((e) => e.change === "possible").length}`,
                    ],
                    [
                      "cSent",
                      "보낸 요청",
                      flow.counts.sent,
                      null,
                      `${readOnly ? "조회만" : "심화 점검 포함"}${flow.counts.unknown ? ` · 결과 불명 ${flow.counts.unknown}` : ""}`,
                    ],
                    ["cAp", "승인 후 전송", count("ap"), "w", "보내기 전에 승인"],
                    ["cSkip", "요청 안 함", flow.counts.blocked, null, "규칙으로 막음"],
                  ] as const
                ).map(([id, label, value, tone, sub]) => (
                  // 시안 #cApTile: 승인 대기 수가 있으면 칸째 주황
                  <div className={cx(s.cn, tone === "w" && value > 0 && s.warn)} key={id}>
                    <div className={s.v} id={id}>
                      {value}
                    </div>
                    <div className={s.k}>{`${label} · ${sub}`}</div>
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
                    flow.events.map((event, i) => {
                      if (event.method) {
                        const key = `${event.at}:${event.kind}:${event.method}:${event.path}`;
                        const open = openLog === key;
                        const ep = open
                          ? endpoints.find((e) => e.method === event.method && e.path === event.path)
                          : undefined;
                        return (
                          <Fragment key={key}>
                            <button
                              id={`lgRow${i}`}
                              className={cx(s.lr, open && s.on)}
                              type="button"
                              aria-expanded={open}
                              aria-controls={open ? `lgx${i}` : undefined}
                              onClick={() => setOpenLog(open ? null : key)}
                            >
                              <span className={s.t}>{hms(event.at)}</span>
                              <span className={cx(s.m, s[event.method])}>{event.method}</span>
                              <span className={s.p}>{event.path ? <Pth p={event.path} /> : "—"}</span>
                              <span>
                                <EventResult event={event} />
                              </span>
                              <span className={s.ln}>{eventText(event.kind)}</span>
                            </button>
                            {open ? (
                              <LogDetail
                                id={`lgx${i}`}
                                event={event}
                                endpoint={ep}
                                raw={logRawOf(flow.events, i, ep)}
                                onClose={() => closeLog(i)}
                              />
                            ) : null}
                          </Fragment>
                        );
                      }
                      return (
                        <div className={cx(s.lr, s.sys)} key={`${event.at}:${event.kind}`}>
                          <span className={s.t}>{hms(event.at)}</span>
                          <span>{eventText(event.kind)}</span>
                        </div>
                      );
                    })
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
            <EndpointDetail endpoint={detail} back={back} />
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
              {/* 시안 범례의 '로그인'(시험 계정 로그인 경로) 수는 서버가 경로를 그렇게 나누지 않아 값을 비운다 */}
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
                  <span title="서버가 시험 계정 로그인 경로를 따로 나누지 않습니다">
                    <i style={{ background: "var(--accent)" }} />
                    {`로그인 ${NONE}`}
                  </span>
                  <span>
                    <i style={{ background: "var(--warn)" }} />
                    {`승인 후 전송 ${count("ap")}`}
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
              <div className={s.isec} style={{ gap: 12 }}>
                {/* 찾은 경로는 묶음 카드로만 본다 (시안 v32: 주소 구조 보기 뺌) */}
                <div className={s.fctl}>
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
                  {shown.length ? (
                    <EndpointCards
                      all={endpoints}
                      shown={shown}
                      missing={filter === "all" && crawling ? missingGroups(endpoints) : []}
                      onSelect={select}
                    />
                  ) : (
                    <p className={s.empty}>{treeEmpty(filter, endpoints.length)}</p>
                  )}
                </div>
                <p className={s["tree-hint"]}>묶음 이름 · 경로 규칙</p>
              </div>
            </>
          )}
        </aside>
        <PaneToggle />
      </div>
    </>
  );
}

/** 시작 전 점검 칸 (시안 pre). 서버가 시작 전 연결 확인 결과를 주지 않아 값은 비운다. */
const PRECHECKS = ["대상 연결", "계정 로그인", "모델 연결"] as const;
/** 점검 중 새로 찾은 경로 처리 규칙 (시안 규칙표 · 서버 계약 아님). */
const NEW_PATH_RULES = [
  ["허용 주소 밖", "요청 안 함 · 기록만", "out"],
  ["제외 규칙과 같은 모양", "자동 제외", "ex"],
  ["GET · 보기만 하는 요청", "자동 포함", "in"],
  ["POST · PUT · PATCH", "포함 · 보낼 때 승인", "ap"],
  ["DELETE · 초기화 · 로그아웃", "자동 제외", "ex"],
  ["관리자 · 처음 보는 권한 영역", "확인 요청", "ap"],
] as const;

/**
 * 범위 확인 (시안 data-view="scope"): 찾은 경로 중 심화 점검할 경로를 고른다. 선택은 개별 HTTP 송신 승인을 대신하지 않는다.
 * 시안의 '로그인 필요' 수 · 거르기, 시작 전 점검 줄, 점검 중 새 경로 처리 규칙은 서버가 주지 않거나 계약이 없어
 * 칸 모양만 두고 값은 비운다('연결 전').
 */
function ScopeSelection({ flow, head }: { flow: WorkflowSnapshot; head: (start: ReactNode) => ReactNode }) {
  const eligible = (e: WorkflowEndpoint) => e.in_scope && (e.change === "attested_read" || flow.reversible_writes);
  const waiting = flow.stage === "awaiting_selection";
  // 사전 확인한 읽기 경로는 기본 포함, 데이터를 바꿀 수 있는 경로는 직접 고른다
  const [selected, setSelected] = useState<Set<string>>(() => {
    const server = flow.endpoints.filter((e) => e.selected).map((e) => e.id);
    if (server.length || !waiting) return new Set(server);
    return new Set(flow.endpoints.filter((e) => eligible(e) && e.change === "attested_read").map((e) => e.id));
  });
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState("all");
  // 묶음은 기본으로 접는다. 거르기 · 검색 중에는 맞는 묶음을 펼친다. 직접 연 상태는 거르기 · 검색을 바꾸면 처음으로 (시안 sOpen)
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
    ["off", "제외됨", (e: WorkflowEndpoint) => !isOn(e)],
  ] as const;
  const test = FILTERS.find(([id]) => id === filter)?.[2] ?? (() => true);
  const q = query.trim().toLowerCase();
  const shown = flow.endpoints.filter((e) => test(e) && `${e.method} ${e.path}`.toLowerCase().includes(q));
  const groups = orderGroups(shown.map(group), SCOPE_GROUPS);
  const forced = filter !== "all" || !!q;
  const isOpen = (g: string) => open[g] ?? forced;
  const allOpen = groups.length > 0 && groups.every(isOpen);
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
  // 오른쪽 상세는 고른 경로, 아직 안 골랐으면 표의 첫 경로 (시안은 늘 한 경로를 보인다)
  const firstShown = groups.length ? shown.find((e) => group(e) === groups[0]) : undefined;
  const current = flow.endpoints.find((e) => e.id === detail) ?? firstShown ?? null;
  /** 경로 딱지 (시안 scopeTag): 빠진 경로는 제외, 넣은 경로는 자동 포함 · 포함 · 보낼 때 승인. */
  const tag = (e: WorkflowEndpoint) => {
    if (!eligible(e) || !isOn(e)) return <span className={cx(s.tag, s.ex)}>제외</span>;
    if (e.change === "possible") return <span className={cx(s.tag, s.ap)}>포함 · 보낼 때 승인</span>;
    return <span className={cx(s.tag, s.in)}>자동 포함</span>;
  };
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
              제외<b>{flow.endpoints.length - inCount}</b>
            </span>
            <span className={s.ap}>
              승인 필요<b>{apCount}</b>
            </span>
            <span title="서버가 경로의 로그인 필요 여부를 주지 않습니다">
              로그인 필요<b>{NONE}</b>
            </span>
          </div>
          {/* biome-ignore lint/a11y/useSemanticElements: 시안 마크업 그대로 */}
          <div className={s.pre} role="group" aria-label="시작 전 점검">
            <span className={s.k}>시작 전 점검</span>
            {PRECHECKS.map((k) => (
              <span key={k}>{`${k} ${NONE}`}</span>
            ))}
            <Soon why="서버가 시작 전 연결 확인 결과를 주지 않습니다" />
          </div>
          <div className={s.filters}>
            {/* biome-ignore lint/a11y/useSemanticElements: 시안 마크업 그대로 */}
            <div className={s.filters} role="group" aria-label="보기 필터" id="sFilters">
              {FILTERS.map(([id, label, f]) => (
                <Fragment key={id}>
                  {/* 시안의 '로그인 필요' 거르기 (제외됨 앞): 서버가 경로의 로그인 필요 여부를 주지 않아 누를 수 없다 */}
                  {id === "off" ? (
                    <button
                      className={s.fchip}
                      type="button"
                      aria-pressed={false}
                      disabled
                      title="서버가 경로의 로그인 필요 여부를 주지 않습니다"
                    >
                      로그인 필요<span className={s.c}>{NONE}</span>
                    </button>
                  ) : null}
                  <button
                    className={s.fchip}
                    type="button"
                    aria-pressed={filter === id}
                    onClick={() => {
                      setFilter(id);
                      setOpen({});
                    }}
                  >
                    {label}
                    <span className={s.c}>{flow.endpoints.filter(f).length}</span>
                  </button>
                </Fragment>
              ))}
            </div>
            <input
              className={cx(s.inp, s.search)}
              id="sQ"
              type="search"
              value={query}
              onChange={(e) => {
                setQuery(e.target.value);
                setOpen({});
              }}
              placeholder="경로 검색"
              aria-label="경로 검색"
            />
            <button
              className={s.sall}
              type="button"
              id="sAll"
              aria-controls="sTable"
              hidden={!groups.length}
              onClick={() => setOpen(Object.fromEntries(groups.map((g) => [g, !allOpen])))}
            >
              {allOpen ? "모두 접기" : "모두 펼치기"}
            </button>
          </div>
          <div className={s.stbl} id="sTable">
            {groups.length ? (
              groups.map((name, gi) => {
                const rows = shown.filter((e) => group(e) === name);
                const expanded = isOpen(name);
                const gid = `sg-${gi}`;
                const on = rows.filter(isOn);
                const ap = on.filter((e) => e.change === "possible").length;
                return (
                  <Fragment key={name}>
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
                      <span className={s.ai}>경로 규칙 묶음</span>
                    </button>
                    <div className={s.sgrp} id={gid} hidden={!expanded}>
                      {rows.map((e) => (
                        <div className={cx(s.srow, !isOn(e) && s.out, current?.id === e.id && s.sel)} key={e.id}>
                          <span className={s.ck}>
                            <input
                              type="checkbox"
                              aria-label={`${e.method} ${e.path} 점검에 포함`}
                              checked={isOn(e)}
                              disabled={locked || !eligible(e)}
                              onChange={() => {
                                toggle(e.id);
                                setDetail(e.id);
                              }}
                            />
                          </span>
                          <button
                            className={s.sb}
                            type="button"
                            aria-current={current?.id === e.id ? "true" : undefined}
                            onClick={() => {
                              setDetail(e.id);
                              openInsp();
                            }}
                          >
                            <span className={cx(s.m, s[e.method])}>{e.method}</span>
                            <span className={s.p}>
                              <Pth p={e.path} />
                            </span>
                            <span className={s.d}>{e.visited ? (e.status ?? "응답") : "미방문"}</span>
                            {tag(e)}
                          </button>
                        </div>
                      ))}
                    </div>
                  </Fragment>
                );
              })
            ) : (
              <p className={s.empty} style={{ padding: 14 }}>
                조건에 맞는 경로가 없습니다.
              </p>
            )}
          </div>
          {/* 점검 중 새로 찾은 경로 처리 규칙: 시안의 규칙표다. 서버가 점검 중 새 경로를 이 규칙대로 다룬다는 계약이
              아직 없어 '연결 전'으로 둔다 */}
          <details className={s.rulebox}>
            <summary>
              <Icon name="chev" />
              점검 중 새로 찾은 경로 처리 규칙<span className={s.c}>{NEW_PATH_RULES.length}</span>
              <Soon why="서버가 점검 중 새 경로를 이 규칙대로 다룬다는 계약이 아직 없습니다" />
            </summary>
            <ul className={s.rules}>
              {NEW_PATH_RULES.map(([r, label, tone]) => (
                <li key={r}>
                  <span>{r}</span>
                  <span className={cx(s.tag, s[tone])}>{label}</span>
                </li>
              ))}
            </ul>
          </details>
        </div>
        <aside className={s.vi} id="sInsp" aria-label="선택한 경로 상세">
          <EndpointDetail endpoint={current} chip={current ? tag(current) : undefined} />
        </aside>
        <PaneToggle />
      </div>
    </>
  );
}

/** '선택한 범위로 점검 시작' 버튼 (화면 머리, 시안 sStart). */
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
    <span className={cx(s.pill, !verified || flow.stage === "awaiting_selection" ? s.wait : s.ok)}>
      {scopePill(flow, verified)}
    </span>
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
            <ul className={cx(s.feed, more && activity.length > 5 && s.scroll)} id="lFeed">
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
                {more ? "최근 5건만 보기" : `이전 활동 ${activity.length - 5}건 더 보기`}
              </button>
            ) : null}
          </>
        ) : (
          <ul className={s.feed} id="lFeed">
            <li className={s.empty}>아직 끝난 일이 없습니다.</li>
          </ul>
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
