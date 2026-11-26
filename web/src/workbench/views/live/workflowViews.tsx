"use client";

// 탐색 · 선택 워크플로 화면 (시안 짜임): 자산 탐색 · 범위 확인, 그리고 점검 진행의 계획 · 활동.
// 서버가 저장한 발견 · 관찰만 보인다. 묶음은 경로 규칙으로 나눈 것이라 AI 추정이라고 부르지 않는다.
// 서버가 주지 않는 시안 칸(로그인 필요 여부 · 경로 기능 설명 · 요청 횟수 · 시작 전 점검 · 새 경로 처리 규칙 · 기록 한 줄 설명)은
// 자리를 두지 않고 뺀다. 탐색 막대는 서버 진행률이 아니라 페이지 상한 대비 방문 수다.

import { Fragment, type ReactElement, type ReactNode, useEffect, useId, useMemo, useRef, useState } from "react";

import { ApiError } from "@/lib/diagnosis/api";
import { type E2Flow, REASON_LABEL } from "@/lib/diagnosis/e2flow";

import { cx } from "../../lib/cx";
import {
  DECISION_TEXT,
  type E2DetailRead,
  type E2Field,
  type E2RequestRow,
  type E2RequestsPage,
  FIELD_REASON_TEXT,
  pathDetailText,
  readE2RequestDetail,
  readE2Requests,
  requestKindText,
} from "../../live/e2Requests";
import { clockOf } from "../../live/map";
import { maskSecrets } from "../../live/mask";
import { retryWorkflowAction, runUrlAction, runWorkflowAction, useUrl, useUrlGates } from "../../live/urlDiagnosis";
import { URL_ENDED_STATES } from "../../live/urlMap";
import { usePoll } from "../../live/usePoll";
import { flowStageText, type WorkflowEndpoint, type WorkflowSnapshot } from "../../live/workflow";
import { useShell } from "../../shell";
import { Icon } from "../../ui/icons";
import { CloseInspButton, CodeCard, InspOpenButton, Isec, Kv, PaneToggle } from "../../ui/parts";
import s from "../../workbench.module.css";
import { SentRequestsValue, useE2FlowFor } from "./sentRequests";
import { reasonText, UrlHead, UrlNotices, UrlRefreshButton, useUrlHost } from "./urlParts";

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
  if (!total) return "아직 발견한 API가 없습니다.";
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
  if (flow.stage === "failed") return s.fail;
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
 * 탐색 막대 % — 실제 방문 수 ÷ 페이지 상한(서버 진행률 아님). 첫 탐색 단계가 끝났다는 initial_completed 는 완전 탐색을
 * 보장하지 않아(부분 완료 · 상한 소진에서도 true) 막대에 섞지 않는다 (리뷰 #479). 완료 상태도 실제 방문 비율을 알린다.
 */
export function crawlPct(flow: WorkflowSnapshot): number {
  return Math.min(100, Math.round((flow.counts.visited / Math.max(1, flow.limits.max_pages)) * 100));
}
/**
 * 자산 탐색 화면 막대 (사용자 11/18): 탐색 중엔 페이지 상한 대비 방문 비율. 탐색이 끝나면 끝까지 채운다 — 끝난 뒤 상한 대비
 * 몇 %만 남은 막대가 진행이 멈춘 것처럼 보여서다. 색은 점검 진행 단계 게이지와 같게(사용자 11/19 '색 통일'): 탐색 중 파랑,
 * 끝남 초록, 중지 · 탐색 안 함 · 실패는 회색. 다 돌지 못한 끝(부분 완료 · 상태 없음)은 색이 아니라 위 탐색 줄
 * (crawlEndText)과 aria 글이 말하고(리뷰 #479 · #709), 실제 방문 수는 aria 글에 남긴다.
 */
export function crawlBar(flow: WorkflowSnapshot): {
  /** 막대에 채우는 너비(%). 끝난 모양은 100 */
  fill: number;
  /** 페이지 상한 대비 실제 방문 비율(%) — aria-valuenow (#709 리뷰: 끝난 모양이어도 값은 실제 비율) */
  pct: number;
  tone: "run" | "ok" | "stop";
  text: string;
} {
  const pct = crawlPct(flow);
  if (exploreCompleted(flow))
    return {
      fill: 100,
      pct,
      tone: "ok",
      text: `탐색 완료 · 방문 ${flow.counts.visited} / 페이지 상한 ${flow.limits.max_pages}`,
    };
  const st = exploreState(flow);
  // 점검 진행 게이지의 '멈춤'과 같은 경우: 탐색이 중지 · 미실행이거나 점검이 멈춤 · 실패. 첫 탐색을 마쳤는지
  // (initial_completed)와 따로 본다 — 탐색 전에 실패하면 서버가 NOT_RUN · initial_completed=false 를 저장한 뒤 failed 로
  // 바꿀 수 있다(#765 이동찬 리뷰: 그때도 탐색 중 파랑이 아니라 회색)
  const stopped =
    st?.status === "STOPPED" || st?.status === "NOT_RUN" || flow.stage === "stopped" || flow.stage === "failed";
  if (stopped || (flow.stage !== "crawling" && flow.initial_completed)) {
    let what = "완료 확인 안 됨";
    if (st && st.status !== "RUNNING") what = st.text;
    else if (flow.stage === "failed") what = "실패";
    else if (flow.stage === "stopped") what = "중지됨";
    return {
      fill: 100,
      pct,
      tone: stopped ? "stop" : "ok",
      text: `탐색 끝 · ${what} · 방문 ${flow.counts.visited} / 페이지 상한 ${flow.limits.max_pages}`,
    };
  }
  return { fill: pct, pct, tone: "run", text: `${pct}% · 페이지 상한 대비 방문` };
}
/** 서버가 탐색을 끝까지 마쳤다고 했는가 (exploration_status COMPLETED). */
function exploreCompleted(flow: WorkflowSnapshot): boolean {
  return (flow as unknown as Record<string, unknown>).exploration_status === "COMPLETED";
}
/**
 * 서버 탐색 상태 (snapshot.exploration_status) → 글. 화면은 '탐색: 일부 완료'처럼 앞에 '탐색:'을 붙여 쓴다(사용자 11/19).
 * 다 못 돈 PARTIAL 을 '완료'로 줄이지 않는다(절대규칙 5) — 오른쪽 패널 실행 결과의 '일부 완료'와 같은 말.
 */
const EXPLORE_TEXT = {
  RUNNING: "진행 중",
  COMPLETED: "완료",
  PARTIAL: "일부 완료",
  STOPPED: "중지됨",
  NOT_RUN: "안 함",
} as const;
/**
 * 서버가 실제 내보내는 탐색 종료·미실행 사유만 한글로 매핑한다(팀 결정: 화면은 status 중심, 사유는 뜻을 더할 때만).
 *  - 부분/중지 종료 (diagnosisflow FinishExploration): cap_exhausted · time_budget · page_limit · model_error ·
 *    boundary_unavailable · runtime_unavailable · target_ended · cancelled
 *  - 실행 전 미가용 (RecordExplorationUnavailable, NOT_RUN): boundary_unavailable · model_unavailable ·
 *    runtime_unavailable · browser_unavailable
 * completed 처럼 status 와 겹치거나 모순되는 사유 · 모르는 토큰은 화면에서 숨긴다. 원본 reason 은 서버 진단 기록에 그대로 남는다.
 */
const EXPLORE_REASON_TEXT: Record<string, string> = {
  cap_exhausted: "요청 상한 소진",
  time_budget: "시간 상한 도달",
  page_limit: "페이지 상한 도달",
  model_error: "모델 오류",
  boundary_unavailable: "탐색 경계 사용 불가",
  runtime_unavailable: "실행 런타임 사용 불가",
  model_unavailable: "모델 사용 불가",
  browser_unavailable: "브라우저 사용 불가",
  target_ended: "대상 연결 종료",
  cancelled: "취소됨",
};
/**
 * 서버 탐색 상태 · 사유. 파서(live/workflow.ts)에 아직 칸이 없어 응답 원본에서 아는 값만 읽는다
 * (모르는 상태 · 형식이 다른 사유는 버림).
 */
export function exploreState(
  flow: WorkflowSnapshot,
): { status: keyof typeof EXPLORE_TEXT; text: string; reason?: string } | null {
  const raw = flow as unknown as Record<string, unknown>;
  const st = raw.exploration_status;
  if (typeof st !== "string" || !Object.hasOwn(EXPLORE_TEXT, st)) return null;
  const status = st as keyof typeof EXPLORE_TEXT;
  const r = raw.exploration_reason;
  const reason = typeof r === "string" && /^[a-z0-9_]{1,64}$/.test(r) ? r : undefined;
  return { status, text: EXPLORE_TEXT[status], reason };
}
/**
 * 탐색이 끝난 뒤 위 탐색 줄(cNow) 글 (사용자 11/19 — 위 '탐색을 마쳤습니다'와 아래 '탐색: 일부 완료'가 따로 있어 헷갈림 →
 * 한 줄로). 다 돌지 못한 끝(PARTIAL · 중지 · 탐색 안 함 · 상태 없음)은 '마쳤다'만 적지 않고 같은 줄에 붙인다(절대규칙 5,
 * #709 리뷰: 탐색 완료 여부가 잘못 읽히지 않게). 사유는 뜻을 더하는 것만 괄호로.
 */
const CRAWL_END_TEXT: Readonly<Record<keyof typeof EXPLORE_TEXT, string | null>> = {
  COMPLETED: "탐색을 마쳤습니다",
  PARTIAL: "탐색을 마쳤습니다 · 일부 페이지는 탐색하지 못했습니다",
  STOPPED: "탐색이 중간에 멈췄습니다",
  NOT_RUN: "탐색을 하지 않았습니다",
  // 끝났는데 서버가 아직 탐색 중이라고 하면 상태 없음과 같이 본다
  RUNNING: null,
};
/** 서버가 탐색 상태를 주지 않았을 때 — 다 돌았다고 적지 않는다(#709 리뷰 '완료 확인 안 됨'). */
const CRAWL_END_UNKNOWN = "탐색을 마쳤습니다 · 끝까지 봤는지는 확인되지 않았습니다";
function crawlEndText(explore: ReturnType<typeof exploreState>): string {
  const base = (explore && CRAWL_END_TEXT[explore.status]) ?? CRAWL_END_UNKNOWN;
  // 표에 있는 사유만(Object 기본 속성 이름 — constructor 등 — 은 사유가 아니다). 서버는 COMPLETED 에 completed 만 준다
  const why =
    explore?.reason && Object.hasOwn(EXPLORE_REASON_TEXT, explore.reason)
      ? EXPLORE_REASON_TEXT[explore.reason]
      : undefined;
  return why && base !== CRAWL_END_UNKNOWN ? `${base} (${why})` : base;
}

/** 범위 확인 화면 상태 알약 색: 확인 대기 · 미확인은 노랑, 실행 실패는 빨강(사용자 11/26), 나머지는 초록. */
function scopeTone(flow: WorkflowSnapshot, verified: boolean): string | undefined {
  if (!verified || flow.stage === "awaiting_selection") return s.wait;
  return flow.stage === "failed" ? s.fail : s.ok;
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
  created: ["점검 시작", ""],
  // 심화 점검 단계의 진단 요청(E② 를 지난 요청). 보낸 줄에만 요청 식별자가 붙는다
  request: ["진단 요청", ""],
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
  exploration_observed_skipped: ["형식 밖 경로라 목록에 안 올림", ""],
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
 * 무슨 기록인지는 옆 '기록' 칸에 적는다.
 */
function EventResult({ event }: { event: FlowEvent }) {
  if (event.status) return <Sc status={event.status} />;
  const tone = Object.hasOwn(EVENT_TEXT, event.kind) ? EVENT_TEXT[event.kind][1] : "";
  if (tone === "ex") return <span className={cx(s.tag, s.ex)}>실패</span>;
  return <span className={s.mono}>{NONE}</span>;
}

/** 탐색 명령 결과 · 상한 안내 (서버 사실만). '목록에서 다시 연 점검은 조회 전용' 줄은 두지 않는다(사용자 11/17). */
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
        <p
          role="status"
          className={s.help}
        >{`발견 목록 크기 상한(개수·화면 전달 크기)으로 ${flow.counts.omitted}건을 생략했습니다.`}</p>
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

/** 일시정지가 가능한 흐름 단계(서버 diagnosisflow controller 의 pause 조건과 같다). */
const PAUSABLE_STAGES: readonly string[] = ["crawling", "diagnosing"];
/** 서버가 받는 명령 기록 상한(32) — 일시정지는 31개부터 거부되고 재개는 늘 받는다(diagnosisflow controller). */
const PAUSE_COMMAND_LIMIT = 31;
/**
 * 종료 확인 상자의 대안 안내. 일시정지는 모델 요청 관문일 뿐이라 회차 시간 상한은 계속 흐른다
 * (docs/contracts/e3-product-confirmation-interface.md §7) — 시간도 멈추는 것처럼 읽히지 않게 쓴다(#913 리뷰 윤재상 P3).
 */
export const PAUSE_HINT = "잠시 멈추려면 일시정지를 쓰세요(일시정지 중에도 점검 시간 상한은 흐릅니다).";

/**
 * 일시정지 · 다시 시작 버튼 (탐색 · 심화 진단 중에만). 흐름 API(POST /api/product/workflows/{id}/pause · resume)만 쓴다 —
 * 점검 제어 API(/api/tasks/{id}/control)의 일시정지는 회차를 끝내는 중지라 부르지 않는다(개발 계획 2026-11-24 사용자 결정,
 * 11/24 화면 VAmPI 회차 2: 일시정지 30초 동안 대상 송신 0, 재개 뒤 바로 다시 송신). 결과는 서버 흐름 스냅숏(paused)으로만 그린다.
 */
type PauseState = {
  flow: WorkflowSnapshot | null;
  flowBusy: boolean;
  flowVerified: boolean;
  flowUncertain: boolean;
  job: { state: string } | null;
  verified: boolean;
  uncertain: boolean;
};

/** 버튼을 둘 단계인가: 실행 중인 점검의 탐색 · 심화 진단(서버 pause 단계 조건). 일시정지 중이면 [다시 시작]으로 둔다. */
function pauseShown(st: PauseState): st is PauseState & { flow: WorkflowSnapshot } {
  return (
    !!st.job && ["preparing", "running"].includes(st.job.state) && !!st.flow && PAUSABLE_STAGES.includes(st.flow.stage)
  );
}

/** 지금 누를 수 있는가: 흐름 · 점검 상태를 확인했고 명령 처리 중 · 결과 불명이 아님. */
function pauseReady(st: PauseState): boolean {
  return st.flowVerified && !st.flowUncertain && !st.flowBusy && st.verified && !st.uncertain;
}

/**
 * 지금 [일시정지]를 실제로 누를 수 있는가(버튼 · 종료 확인 상자 안내가 같은 판정을 쓴다, #913 리뷰). 이미 일시정지 중이거나
 * 명령 기록이 서버 상한(31)에 닿았으면 아니다.
 */
export function canPauseNow(st: PauseState): boolean {
  return pauseShown(st) && pauseReady(st) && !st.flow.paused && st.flow.commands.length < PAUSE_COMMAND_LIMIT;
}

export function PauseButton() {
  // 목록에서 다시 연 점검(조회 결속)도 누를 수 있다 — 범위 선택과 같다(사용자 2026-11-22, F16). 서버가 흐름 결속 · 주체를 다시 본다
  const st: PauseState = {
    flow: useUrl((x) => x.flow),
    flowBusy: useUrl((x) => x.flowBusy),
    flowVerified: useUrl((x) => x.flowVerified),
    flowUncertain: useUrl((x) => x.flowUncertain),
    job: useUrl((x) => x.job),
    verified: useUrl((x) => x.verified),
    uncertain: useUrl((x) => x.uncertain),
  };
  if (!pauseShown(st)) return null;
  const paused = st.flow.paused;
  // 다시 시작은 서버가 명령 수와 상관없이 받는다 — 상태만 확인됐으면 연다
  const enabled = paused ? pauseReady(st) : canPauseNow(st);
  return (
    <button
      className={s.btn}
      type="button"
      disabled={!enabled}
      aria-pressed={paused}
      onClick={() => void runWorkflowAction(paused ? "resume" : "pause")}
    >
      <Icon name={paused ? "play" : "pause"} />
      <span>{paused ? "다시 시작" : "일시정지"}</span>
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
  const [open, setOpen] = useState(false);
  const opener = useRef<HTMLButtonElement>(null);
  const yes = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (open) yes.current?.focus();
  }, [open]);
  const verb = label === "종료" ? "종료" : "취소";
  // 이어 하기 안내는 지금 [일시정지]를 실제로 누를 수 있을 때만(이미 일시정지 · 명령 상한이면 빼고, #913 리뷰 이동찬 P2)
  const canPause = useUrl((x) => canPauseNow(x));
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
          <li>{`끝내지 못한 항목은 미실행으로 남깁니다.${canPause ? ` ${PAUSE_HINT}` : ""}`}</li>
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
 * 경로 상세 (범위 확인 오른쪽 패널, 시안 apiDetail): 기본 정보 · 실제 요청 · 응답 탭.
 * 시안의 요청 횟수 · 근거 · 기능(AI 추정)은 서버가 경로마다 주지 않아 뺀다. 요청 · 응답은 예시가 아니라 서버가 저장한 관찰이다.
 */
function EndpointDetail({
  endpoint,
  chip,
  extra,
}: {
  endpoint: WorkflowEndpoint;
  /** 처리 딱지 (범위 확인은 고른 상태를 따른다, 시안 scopeTag). 없으면 경로 종류 딱지 */
  chip?: ReactNode;
  /** 딱지 줄 아래 (범위 확인의 점검 포함 체크) */
  extra?: ReactNode;
}) {
  const [tab, setTab] = useState<"info" | "http">("info");
  const uid = useId();
  const e = endpoint;
  const k = kindOf(e);
  const tabId = (id: string) => `${uid}-t-${id}`;
  const panelId = (id: string) => `${uid}-p-${id}`;
  return (
    <>
      <div className={s["vi-head"]}>
        <div>
          <div className={s.eyebrow}>{`발견한 API · ${group(e)}`}</div>
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
      {extra}
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
 * 탐색 기록에 남기는 경로 없는 줄: 처음(점검 시작)과 끝(점검 완료 · 중지됨 · 실행 실패)만(사용자 11/25). 탐색 시작 · 심화 점검
 * 시작/끝 · 범위 확인 대기 같은 중간 줄은 숨긴다. 중지 · 실패도 끝 줄로 남겨 끝난 까닭을 감추지 않는다(규칙 5)
 */
const LOG_SYSTEM_KINDS: ReadonlySet<string> = new Set(["created", "completed", "stopped", "failed"]);

/**
 * 자산 탐색 숫자 칸 3개(발견한 API · 보낸 요청 · 막은 요청). 자산 탐색 화면과 보고서가 같이 쓴다(사용자 11/25 — 보고서에도).
 * 보고서 판정 집계 · 점검 진행 전송 통제와 같은 모양: 이름 · 수 · 설명. 발견한 API 설명은 경로 종류(조회 · 데이터 변경 · 제외)로
 * 나눠 합이 발견 수와 맞게 적는다. '승인 후 전송'은 데이터 변경과 같은 경로를 다시 센 수라 두지 않는다
 */
export function CrawlMeters({
  flow,
  e2,
  e2Failed = false,
}: {
  flow: WorkflowSnapshot;
  e2: E2Flow | null | undefined;
  /** e2-flow 조회 실패(보낸 요청 칸이 '0건'이 아니라 '읽기 실패') */
  e2Failed?: boolean;
}) {
  const count = (k: Kind) => flow.endpoints.filter((e) => kindOf(e) === k).length;
  // 탐색 단계에서는 GET 만 보낸다(서버 Controller.Before). 심화 점검이 시작되면 보낸 요청에 그 요청도 들어간다
  const readOnly = flow.deep_runs === 0 && (flow.stage === "crawling" || flow.stage === "awaiting_selection");
  return (
    <div className={cx(s.meters, s.m3)}>
      {(
        [
          [
            "cFound",
            "발견한 API",
            flow.counts.found,
            `조회 ${count("get")} · 데이터 변경 ${count("ap")} · 제외 ${count("ex")}`,
          ],
          [
            "cSent",
            "보낸 요청",
            // 대상에 나간 요청 총계(탐색 경계 + E② 모델 · 로그인 · 확정), 마우스 올리면 내역(사용자 11/25)
            <SentRequestsValue key="sent" flow={e2} failed={e2Failed} />,
            `${readOnly ? "조회만" : "심화 점검 포함"}${flow.counts.unknown ? ` · 결과 불명 ${flow.counts.unknown}` : ""}`,
          ],
          // 서버 counts.blocked 는 경로 수가 아니라 전송 통제가 거부 · 미지원으로 막은 요청 수
          ["cSkip", "막은 요청", flow.counts.blocked, "범위 밖 · 안전 규칙"],
        ] as const
      ).map(([id, label, value, sub]) => (
        <div className={s.cell} key={id}>
          <div className={s.k}>{label}</div>
          <div className={s.v} id={id}>
            {value}
          </div>
          <div className={s.sub}>{sub}</div>
        </div>
      ))}
    </div>
  );
}

/** 탐색 기록 줄을 가리키는 키. 서버 기록에 id 가 없어 시각 · 종류 · 메서드 · 경로로 가리킨다. */
const logKeyOf = (ev: FlowEvent) => `${ev.at}:${ev.kind}:${ev.method}:${ev.path}`;

/** 헤더 · 본문 칸 글: 지금 서버는 저장하지 않는다(not_stored). 모르는 모양은 지어내지 않고 모른다고 적는다 */
const fieldText = (f: E2Field) => (f.state === "not_stored" ? "저장 안 함" : "이 화면이 모르는 상태");

/**
 * 요청 · 응답 헤더 · 본문 칸(전송 기록 상세의 칸 그대로). 저장 안 된 이유 글과 이유 코드를 같이 보인다(전수조사 11/26).
 * 값이 오는 판(DLP 폐기 뒤 헤더 · 본문 저장)이 되면 이 칸을 채운다
 */
function StoredFields({ title, head, body }: { title: string; head: E2Field; body: E2Field }) {
  const codes = [...new Set([head.code, body.code])];
  const why = codes.map((c) => FIELD_REASON_TEXT[c]).find(Boolean) ?? "서버가 이 화면이 모르는 상태로 보냈어요";
  return (
    <div className={s.cb}>
      <div className={s["cb-h"]}>
        <h3>{title}</h3>
        <span className={s["cb-src"]}>전송 기록</span>
      </div>
      <div className={s["cb-none"]}>
        <Icon name="lock" />
        <span>
          {head.code === body.code
            ? `헤더 · 본문 ${fieldText(head)}`
            : `헤더 ${fieldText(head)} · 본문 ${fieldText(body)}`}
          <span className={s["cb-why"]}>
            {why} <span className={s.mono}>{codes.join(" · ")}</span>
          </span>
        </span>
      </div>
    </div>
  );
}

/** 보냄 칸: 대상에 요청을 썼는가 */
function sentText(d: { forward_attempted: boolean; request_written: boolean }): string {
  if (d.request_written) return "보냄";
  return d.forward_attempted ? "보내려 했으나 쓰지 못함" : "보내지 않음";
}
/** 응답 칸: 받음 · 다 받음 · 잘림 */
function receivedText(d: { response_received: boolean; response_complete: boolean; response_truncated: boolean }) {
  if (!d.response_received) return "받지 못함";
  return `${d.response_complete ? "다 받음" : "일부만 받음"}${d.response_truncated ? " · 잘림" : ""}`;
}

const DETAIL_MISS_TEXT: Readonly<Record<Exclude<E2DetailRead, { ok: true }>["why"], string>> = {
  not_found: "이 회차 전송 기록에 이 요청이 없어요.",
  journal_invalid: "전송 기록 검증 실패 — 내용을 보이지 않아요.",
  forbidden: "이 점검의 전송 기록을 볼 권한이 없어요.",
  error: "전송 기록을 읽지 못했어요.",
};

/**
 * 전송 결과(요청 상세, 계약 workbench-live-data §4.1): 이 줄의 E② 호출 · 시도 하나를 읽어 결정 · 사유 · 보냄 · 응답을 보인다.
 * 서버 판정 그대로이고 화면은 다시 판정하지 않는다. 헤더 · 본문은 서버가 저장하지 않아 '저장 안 함' 칸만 둔다
 */
export function RequestSendResult({ task, callRef, attemptId }: { task: string; callRef: string; attemptId: string }) {
  const read = usePoll((signal) => readE2RequestDetail(task, callRef, attemptId, signal), [task, callRef, attemptId]);
  if (read.error)
    return (
      <div className={s.isec}>
        <p className={s.err} role="alert">
          {DETAIL_MISS_TEXT.error}
        </p>
        <button className={s.btn} type="button" onClick={read.reload}>
          다시 읽기
        </button>
      </div>
    );
  if (!read.data) return <p className={s.empty}>전송 기록을 읽는 중</p>;
  if (!read.data.ok) {
    const miss = read.data;
    return (
      <div className={s.isec}>
        <p className={miss.why === "journal_invalid" ? s.err : s.empty}>
          {`${DETAIL_MISS_TEXT[miss.why]}${miss.why === "error" && miss.status ? ` (HTTP ${miss.status})` : ""}`}
        </p>
      </div>
    );
  }
  const d = read.data.detail;
  const reason = d.reason ? REASON_LABEL[d.reason] : d.reasonCode;
  return (
    <>
      <Isec t="전송 결과">
        <Kv
          rows={[
            [
              "결정",
              <span key="dec" className={cx(s.tag, d.decision === "allowed" ? s.in : s.deny)}>
                {DECISION_TEXT[d.decision]}
              </span>,
            ],
            [
              "사유",
              <span key="why">
                {reason} <span className={cx(s.mono, s.help)}>{d.reasonCode}</span>
              </span>,
            ],
            ["보냄", sentText(d)],
            ["응답", d.status_code ? <Sc key="sc" status={d.status_code} /> : "코드 없음"],
            ["받은 응답", receivedText(d)],
            [
              "경로",
              d.path_pattern && d.path_detail_state !== "unavailable" ? (
                <span key="pp" className={s.mono}>
                  {d.path_pattern}
                </span>
              ) : (
                NONE
              ),
            ],
            ["경로 표시", pathDetailText(d.path_detail_state, d.path_pattern)],
            [
              "규칙",
              d.rule_ref ? (
                <span key="rule" className={s.mono}>
                  {d.rule_ref}
                </span>
              ) : (
                NONE
              ),
            ],
            ["요청 종류", requestKindText(d.kind)],
            ["기록", `${hms(d.at)} · 전송 기록 #${d.index}`],
          ]}
        />
      </Isec>
      <div className={s.expanel}>
        <StoredFields title="요청" head={d.fields.request_headers} body={d.fields.request_body} />
        <StoredFields title="응답" head={d.fields.response_headers} body={d.fields.response_body} />
      </div>
    </>
  );
}

/** 오른쪽 패널 틀: 머리(어떤 기록 · 메서드 · 경로) + 기록 칸 + 전송 결과 */
function DetailFrame({
  eyebrow,
  method,
  path,
  rows,
  children,
}: {
  eyebrow: string;
  method?: string;
  path?: string;
  rows: [string, ReactNode][];
  children: ReactNode;
}) {
  return (
    <section aria-label={`${method ?? ""} ${path ?? ""} 요청 · 응답`}>
      <div className={s["vi-head"]}>
        <div>
          <div className={s.eyebrow}>{eyebrow}</div>
          <h2>
            <span className={cx(s.m, method ? s[method] : undefined)}>{method}</span>{" "}
            <span className={s.p}>{path ? <Pth p={path} /> : "—"}</span>
          </h2>
        </div>
        <CloseInspButton label="기록 상세" />
      </div>
      {rows.length ? (
        <div className={s.isec}>
          <Kv rows={rows} />
        </div>
      ) : null}
      {children}
    </section>
  );
}

/**
 * 탐색 기록 한 줄의 상세 (자산 탐색 오른쪽 패널, 시안 logDetail). 이 기록의 결과와 같은 경로의 서버 정보(찾은 곳 · 출처 · 종류)에,
 * 요청을 보낸 줄이면 그 줄의 전송 결과(요청 식별자로 읽은 E② 기록)를 붙인다. 예전에 붙이던 '이 경로의 마지막 관찰' 한 줄
 * (서버가 메서드 · 경로 · 상태 코드로 만든 'GET /p HTTP/1.1' · 'HTTP status 200')은 이 줄의 것이 아닐 수 있고 원문도 아니라 뺐다(11/26)
 */
function LogDetail({
  event,
  endpoint,
  task,
}: {
  event: FlowEvent;
  endpoint: WorkflowEndpoint | undefined;
  task: string | null;
}) {
  let send: ReactNode;
  if (task && event.call_ref && event.attempt_id)
    send = <RequestSendResult task={task} callRef={event.call_ref} attemptId={event.attempt_id} />;
  else if (event.kind === "request")
    send = (
      <div className={s.expanel}>
        <NoneCard
          title="전송 결과"
          text="요청 식별자가 없는 줄이에요 — 보내지 않은 요청이거나 11/25 이전 점검이라 전송 기록을 열 수 없어요."
        />
      </div>
    );
  else
    send = (
      <div className={s.expanel}>
        <NoneCard title="요청 · 응답" text="탐색 단계 요청이에요 — 탐색 경계를 지났고 요청 원문 기록은 없어요." />
      </div>
    );
  return (
    <DetailFrame
      eyebrow={`탐색 기록 · ${hms(event.at)}`}
      method={event.method}
      path={event.path}
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
    >
      {send}
    </DetailFrame>
  );
}

/** 진단 요청 전체 기록의 한 줄 상세 */
function RequestRowDetail({ row, task }: { row: E2RequestRow; task: string }) {
  return (
    <DetailFrame
      eyebrow={`진단 요청 전체 · ${hms(row.at)}`}
      method={row.method}
      path={row.path}
      // 응답 · 요청 종류 · 경로 표시는 아래 전송 결과가 같은 값을 보여 위 칸은 두지 않는다(두 번 보이지 않게)
      rows={[]}
    >
      <RequestSendResult task={task} callRef={row.call_ref} attemptId={row.attempt_id} />
    </DetailFrame>
  );
}

/** 전체 기록을 못 읽은 까닭 글(계약 §4.1) */
function requestsMissText(error: unknown): string {
  if (error instanceof ApiError && error.status === 404)
    return "아직 전송 기록이 없어요 — 진단 요청을 보내기 전이거나 전송 통제를 거치지 않은 점검이에요.";
  if (error instanceof ApiError && error.status === 409) return "전송 기록 검증 실패 — 내용을 보이지 않아요.";
  if (error instanceof ApiError && error.status === 403) return "이 점검의 전송 기록을 볼 권한이 없어요.";
  return "전송 기록을 읽지 못했어요.";
}

const reqKeyOf = (r: Pick<E2RequestRow, "call_ref" | "attempt_id">) => `${r.call_ref}/${r.attempt_id}`;

/**
 * 진단 요청 전체 기록(E② 저널 기준, 쪽 단위 — 계약 §4.1). 흐름 기록은 최근 32줄뿐이라 끝난 점검의 앞 요청이 빠지는데, 이
 * 목록은 대상에 보낸 진단 · 로그인 · 확정 요청을 처음부터 다 보인다. 탐색 단계 요청은 E② 저널에 없어 여기 없다
 */
function E2RequestLog({
  task,
  current,
  onPick,
}: {
  task: string;
  current: string | null;
  onPick: (row: E2RequestRow) => void;
}) {
  const first = usePoll((signal) => readE2Requests(task, null, signal), [task]);
  const [more, setMore] = useState<{ base: number | null; pages: E2RequestsPage[] }>({ base: null, pages: [] });
  const [moreError, setMoreError] = useState(false);
  const [busy, setBusy] = useState(false);
  // 첫 쪽을 다시 읽으면 뒤에 붙인 쪽은 버린다(같은 판끼리만 잇는다)
  const pages = first.data && more.base === first.fetchedAt ? more.pages : [];
  if (first.error && !first.data)
    return (
      <div className={s["log-miss"]}>
        <p className={first.error instanceof ApiError && first.error.status === 409 ? s.err : s.empty}>
          {requestsMissText(first.error)}
        </p>
        <button className={s.btn} type="button" onClick={first.reload}>
          다시 읽기
        </button>
      </div>
    );
  if (!first.data) return <p className={s.empty}>전송 기록을 읽는 중</p>;
  const all = [first.data, ...pages];
  const rows = all.flatMap((pg) => pg.rows);
  const next = all[all.length - 1].next_cursor;
  const loadMore = () => {
    if (next === null || busy) return;
    setBusy(true);
    setMoreError(false);
    const base = first.fetchedAt;
    readE2Requests(task, next)
      .then((pg) => setMore((m) => ({ base, pages: [...(m.base === base ? m.pages : []), pg] })))
      .catch(() => setMoreError(true))
      .finally(() => setBusy(false));
  };
  return (
    <>
      <div className={s.log} id="cReqLog">
        <div className={s.lh}>
          <span>시각</span>
          <span>메서드</span>
          <span>경로</span>
          <span>요청 종류</span>
          <span>기록 번호</span>
          <span>결과</span>
        </div>
        {rows.length ? (
          rows.map((row) => {
            const key = reqKeyOf(row);
            const on = current === key;
            return (
              <button
                key={key}
                className={cx(s.lr, on && s.on)}
                type="button"
                aria-current={on ? "true" : undefined}
                onClick={() => onPick(row)}
              >
                <span className={s.t}>{hms(row.at)}</span>
                <span className={cx(s.m, row.method ? s[row.method] : undefined)}>{row.method ?? NONE}</span>
                <span
                  className={cx(s.p, row.path_detail_state === "unavailable" && s["sap-nopath"])}
                  title={pathDetailText(row.path_detail_state, row.path)}
                >
                  {row.path && row.path_detail_state !== "unavailable" ? <Pth p={row.path} /> : "경로 틀 없음"}
                </span>
                <span className={s.lk}>{requestKindText(row.kind)}</span>
                <span className={cx(s.lk, s.mono)}>{`#${row.index}`}</span>
                <span>
                  <Sc status={row.status} />
                </span>
              </button>
            );
          })
        ) : (
          <p className={s.empty}>대상에 보낸 진단 요청이 없어요.</p>
        )}
      </div>
      <div className={s["log-more"]}>
        <span className={s.help}>{`${rows.length} / ${first.data.total}건`}</span>
        {next !== null ? (
          <button className={s.btn} type="button" disabled={busy} onClick={loadMore}>
            {busy ? "읽는 중" : "더 보기"}
          </button>
        ) : null}
        {moreError ? <span className={s.err}>다음 쪽을 읽지 못했어요.</span> : null}
      </div>
    </>
  );
}

/** 범위 확인의 점검 포함 고르기 (가운데 묶음 카드가 쓴다). 실제 상태는 ScopeSelection 이 쥔다. */
interface ScopePick {
  isOn: (e: WorkflowEndpoint) => boolean;
  /** 고를 수 있는 경로인가 (범위 안 · 읽기이거나 사전 쓰기 승인이 있는 데이터 변경) */
  eligible: (e: WorkflowEndpoint) => boolean;
  locked: boolean;
  /** 오른쪽 상세에 보이는 경로 */
  current: string | null;
  /** 경로 이름 누름: 오른쪽 상세 */
  show: (e: WorkflowEndpoint) => void;
  /**
   * 경로 오른쪽 네모칸 누름: 바로 점검에 넣거나 뺀다(사용자 11/25, 여러 개를 차례로 체크). Shift 를 누르면 같은 묶음에서 앞서
   * 누른 네모칸부터 이 칸까지 같은 쪽으로 맞춘다. order 는 그 묶음 경로의 보이는 순서
   */
  toggle: (e: WorkflowEndpoint, shift: boolean, order: readonly string[]) => void;
  /** 묶음 전체 넣기 · 빼기 */
  setGroup: (rows: readonly WorkflowEndpoint[], on: boolean) => void;
  /** 사용자가 점검에서 뺀 경로 (다시 넣으면 빠진다). 처음부터 안 들어가 있던 경로는 들지 않는다 */
  dropped: ReadonlySet<string>;
}

/**
 * 찾은 경로 묶음 카드 (범위 확인 가운데, 시안 renderTree). 묶음 머리의 수는 거르기와 상관없이 그 묶음 전체,
 * 알약은 거른 경로만 메서드 · 경로 순으로. 탐색 중 · 전체 보기에서는 아직 경로가 안 나온 묶음을 아래에 적는다.
 * 경로 줄은 이름(누르면 오른쪽 상세, aria-current)과 오른쪽 네모칸(체크 = 점검에 포함, 누르면 바로 넣고 뺌)으로 나뉜다
 * (사용자 11/25: 여러 개는 네모칸을 차례로 체크). 묶음 머리의 체크는 그 묶음 전체(거르기와 상관없이)를 넣고 뺀다.
 */
function EndpointCards({
  all,
  shown,
  missing,
  pick,
}: {
  all: WorkflowEndpoint[];
  shown: WorkflowEndpoint[];
  missing: readonly string[];
  pick: ScopePick;
}) {
  const groups = orderGroups(shown.map(group), GROUPS);
  return (
    <div className={s.gcs}>
      {groups.map((name) => {
        const rows = all.filter((e) => group(e) === name);
        const pills = shown.filter((e) => group(e) === name).sort(byMethodPath);
        const order = pills.map((e) => e.id);
        const can = rows.filter(pick.eligible);
        const onN = can.filter(pick.isOn).length;
        // 묶음 체크는 ✓ 아니면 빈칸만 쓴다(반쯤 체크 '−' 없음): 고를 수 있는 게 전부 들어 있을 때만 ✓
        const full = can.length > 0 && onN === can.length;
        return (
          <section className={s.gc} key={name} aria-label={`${name} ${rows.length}개`}>
            <div className={s["gc-h"]}>
              <input
                type="checkbox"
                className={s["gc-ck"]}
                aria-label={`${name} 묶음 전체 점검에 포함`}
                checked={full}
                disabled={pick.locked || !can.length}
                onChange={() => pick.setGroup(rows, !full)}
              />
              <b>{name}</b>
              <span className={s.n}>{rows.length}</span>
              {/* 이 묶음에서 점검에 넣은 경로 수(사용자 11/24: 포함 표시가 약했다). 없으면 두지 않는다 */}
              {onN ? <span className={s["gc-inc"]}>{`포함 ${onN}`}</span> : null}
            </div>
            <div className={s.pills}>
              {pills.map((e) => {
                const k = kindOf(e);
                const on = pick.isOn(e);
                const out = !on && pick.dropped.has(e.id);
                const cur = pick.current === e.id;
                return (
                  <div
                    className={cx(
                      s.pp,
                      k === "ap" && s.ap,
                      k === "ex" && s.ex,
                      k === "get" && s.get,
                      on && s.inc,
                      out && s.drop,
                      cur && s.cur,
                    )}
                    key={e.id}
                    data-ep={e.id}
                  >
                    <button
                      className={s.ppb}
                      type="button"
                      aria-current={cur ? "true" : undefined}
                      aria-label={`${e.method} ${e.path}${PILL_FLAG[k]}${on ? ", 점검에 포함" : ""}${out ? ", 점검에서 제외함" : ""}, 상세 보기`}
                      onClick={() => pick.show(e)}
                    >
                      <span className={cx(s.pm, s[e.method])}>{e.method === "DELETE" ? "DEL" : e.method}</span>
                      <span className={s.pt}>
                        <Pth p={e.path} />
                      </span>
                      {k !== "get" ? <span className={s.pf}>{k === "ap" ? "승인" : "제외"}</span> : null}
                    </button>
                    {/* 오른쪽 네모칸: 체크 = 점검에 포함. 누르면 바로 넣고 뺀다. 고를 수 없는 경로(범위 밖 · 사전 쓰기 승인 없는 데이터
                        변경)는 꺼 둔다 */}
                    <input
                      className={s.pcb}
                      type="checkbox"
                      aria-label={`${e.method} ${e.path} 점검에 포함`}
                      checked={on}
                      disabled={pick.locked || !pick.eligible(e)}
                      onChange={(ev) => pick.toggle(e, (ev.nativeEvent as MouseEvent).shiftKey === true, order)}
                    />
                  </div>
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

/**
 * 찾은 경로 (범위 확인 가운데, 시안 renderTree — 자산 탐색 오른쪽 패널에서 옮김): 거르기 · 검색 · 묶음 카드.
 * 알약을 누르면 오른쪽 경로 상세에서 점검에 넣을지 고른다. 거르기 옆(tools)과 카드 위(bar)는 부모가 채운다.
 * 검색은 메서드 · 경로 글자로 알약을 거른다(대소문자 무시). 검색으로 안 보이게 된 알약의 고르기는 그대로 둔다(직접 고른 것).
 */
function FoundPaths({
  endpoints,
  crawling,
  filter,
  onFilter,
  pick,
  tools,
  bar,
  excludedInput = [],
}: {
  endpoints: WorkflowEndpoint[];
  crawling: boolean;
  filter: string;
  onFilter: (id: string) => void;
  pick: ScopePick;
  tools?: ReactNode;
  bar?: ReactNode;
  /** 새 점검에서 운영자가 넣은 제외 경로 패턴(job.input.excluded_paths 한 줄에 하나) */
  excludedInput?: readonly string[];
}) {
  const [query, setQuery] = useState("");
  const q = query.trim().toLowerCase();
  const shown = endpoints.filter(
    (e) => (filter === "all" || kindOf(e) === filter) && (!q || `${e.method} ${e.path}`.toLowerCase().includes(q)),
  );
  let empty = treeEmpty(filter, endpoints.length);
  if (q && endpoints.length) empty = "검색과 맞는 경로가 없습니다.";
  return (
    // 제목(발견한 API · 수)은 화면에 두지 않는다. 읽기 이름만 둔다
    <section className={s.sec} aria-label="발견한 API">
      <div className={cx(s.isec, s.nohead)} style={{ gap: 12 }}>
        {/* 찾은 경로는 묶음 카드로만 본다 (시안 v32: 주소 구조 보기 뺌) */}
        <div className={s.fctl}>
          <div className={s.seg} role="radiogroup" aria-label="보이는 경로">
            {[
              ["all", "전체"],
              ["ap", "승인 필요"],
              ["ex", "제외"],
            ].map(([id, label]) => (
              <label key={id}>
                <input type="radio" name="sKf" checked={filter === id} onChange={() => onFilter(id)} />
                <span>{label}</span>
              </label>
            ))}
          </div>
          {/* 제외 경로(여러 개 고르기)는 거르기(전체 · 승인 필요 · 제외) 바로 옆, 검색은 오른쪽 끝 */}
          {tools}
          <input
            className={cx(s.inp, s.search, s["fctl-q"])}
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="경로 검색"
            aria-label="경로 검색"
          />
        </div>
        {bar}
        {/* '제외' 거르기에는 새 점검에서 넣은 제외 경로 패턴을 먼저 보인다(사용자 11/17 '제외 경로가 안 나옴'). 서버는 이 경로를
            탐색부터 방문하지 않아(diagnosisflow allowedPath · 탐색 경계) 아래 목록에는 다른 페이지 링크로 발견된 것만 나온다 */}
        {filter === "ex" && excludedInput.length ? (
          <section className={s.exin} aria-label="새 점검에서 뺀 경로">
            <div className={s["exin-h"]}>새 점검에서 뺀 경로</div>
            <div className={s.chips}>
              {excludedInput.map((p) => (
                <span key={p} className={s.chip}>
                  {p}
                </span>
              ))}
            </div>
            <p className={s.help}>
              이 경로는 탐색 · 진단에서 아예 보내지 않아 아래 목록에 없을 수 있습니다. 다른 페이지 링크로 발견된 것만
              아래 '제외'에 나옵니다.
            </p>
          </section>
        ) : null}
        <div id="sTree">
          {shown.length ? (
            <EndpointCards
              all={endpoints}
              shown={shown}
              missing={filter === "all" && !q && crawling ? missingGroups(endpoints) : []}
              pick={pick}
            />
          ) : (
            <p className={s.empty}>{empty}</p>
          )}
        </div>
      </div>
    </section>
  );
}

/**
 * 탐색 중 마지막으로 새로 찾은 경로 (자산 탐색 위 한 줄, 사용자 11/17 "찾을 때마다 어디 찾았다고").
 * 앞 조회에 없던 경로 id 를 새로 찾은 것으로 본다. 서버는 찾은 순서대로 뒤에 붙이지만 상한에 걸리면 범위 밖 자리를
 * 바꿔 끼우므로(diagnosisflow controller.go) 순서가 아니라 id 로 비교한다. 화면을 처음 열었을 때는 비교할 앞 조회가
 * 없어 맨 뒤(가장 나중에 붙은) 경로를 보인다. more = 같은 조회에서 함께 찾은 나머지 수.
 */
function useLatestFound(flow: WorkflowSnapshot | null): { e: WorkflowEndpoint; more: number } | null {
  const seen = useRef<{ job: string; ids: Set<string> } | null>(null);
  const [found, setFound] = useState<{ e: WorkflowEndpoint; more: number } | null>(null);
  useEffect(() => {
    if (!flow) return;
    const list = flow.endpoints;
    const prev = seen.current?.job === flow.job_id ? seen.current.ids : null;
    seen.current = { job: flow.job_id, ids: new Set(list.map((e) => e.id)) };
    if (!prev) {
      const last = list.at(-1);
      setFound(last ? { e: last, more: 0 } : null);
      return;
    }
    const fresh = list.filter((e) => !prev.has(e.id));
    if (fresh.length) setFound({ e: fresh[fresh.length - 1], more: fresh.length - 1 });
  }, [flow]);
  return found;
}

/** 자산 탐색 (시안 data-view="crawl"). */
export function WorkflowCrawlLive() {
  const job = useUrl((st) => st.job),
    flow = useUrl((st) => st.flow),
    verified = useUrl((st) => st.flowVerified),
    host = useUrlHost(),
    { goTo, openInsp } = useShell();
  // 오른쪽 패널에 보이는 탐색 기록 줄 (한 번에 하나). 패널은 고르기 전에도 늘 펼쳐 두고 고르라는 안내를 보인다
  // (사용자 11/17 '계속 열려있게'). 접기 · 펼치기는 작업대 공통 PaneToggle 로만
  // 패널에 보이는 줄: 탐색 기록 줄 또는 진단 요청 전체의 줄(11/26)
  const [pick, setPick] = useState<{ kind: "event"; key: string } | { kind: "req"; row: E2RequestRow } | null>(null);
  const logKey = pick?.kind === "event" ? pick.key : null;
  // 기록 보기: 최근 기록(흐름 기록 최근 32줄 · 탐색 포함) · 진단 요청 전체(E② 저널, 쪽 단위 — 탐색 요청은 없음)
  const [logView, setLogView] = useState<"recent" | "all">("recent");
  const stop = useStopConfirm("점검 취소");
  const found = useLatestFound(flow);
  // 끝난 점검은 한 번만 읽는다(진행 중일 때만 주기 조회, #938 윤재상 P3)
  const task = job?.started && job.task_id ? job.task_id : null;
  const e2 = useE2FlowFor(task, true, !!job && URL_ENDED_STATES.includes(job.state));
  // 진단 요청 전체 수 = 전송 통제 집계의 모델 · 로그인 · 확정 · 미분류(계약 §4.1 total 과 같음)
  const tr = e2.data?.target_requests;
  const reqTotal = tr ? tr.model + tr.login + tr.confirmation + tr.unclassified : null;
  const endpoints = flow?.endpoints ?? [];
  const events = flow?.events ?? [];
  const logAt = logKey ? events.findIndex((ev) => logKeyOf(ev) === logKey) : -1;
  const logEvent = logAt >= 0 ? events[logAt] : undefined;
  const logEndpoint = logEvent
    ? endpoints.find((e) => e.method === logEvent.method && e.path === logEvent.path)
    : undefined;
  // 탐색 기록 줄의 출처 칸: 메서드 · 경로가 같은 발견한 API
  const endpointOf = useMemo(
    () => new Map((flow?.endpoints ?? []).map((e) => [`${e.method} ${e.path}`, e])),
    [flow?.endpoints],
  );
  const pickLog = (key: string) => {
    setPick({ kind: "event", key });
    openInsp();
  };
  const pickReq = (row: E2RequestRow) => {
    setPick({ kind: "req", row });
    openInsp();
  };
  let detail: ReactNode = null;
  if (pick?.kind === "req" && task) detail = <RequestRowDetail row={pick.row} task={task} />;
  else if (logEvent) detail = <LogDetail event={logEvent} endpoint={logEndpoint} task={task} />;
  const next = flow?.mode === "user_approval_v1" ? "scope" : "live";
  const ready = verified && !!flow?.initial_completed;
  const crawling = flow?.stage === "crawling";
  const explore = flow ? exploreState(flow) : null;
  // 막대는 아래 flow 가 있을 때만 그린다(없을 때 값은 쓰이지 않음)
  const bar = flow ? crawlBar(flow) : { fill: 0, pct: 0, tone: "run" as const, text: "" };
  // 탐색 줄: 탐색 중이면 새로 찾은 경로('… 찾음', 같은 조회에서 여럿이면 '외 N개'), 끝났으면 '탐색을 마쳤습니다'
  // (다 돌지 못했으면 같은 줄에 그 사실까지, crawlEndText). AI 자동 모드는 끝나자마자 심화 진단으로 넘어가고(서버
  // diagnosisflow crawl.go) 그 요청도 아래 기록에 이어서 쌓여, 끝 글만 두면 탐색이 계속되는 것처럼 보여 지금 하는 일을 같이 적는다
  const ended =
    !crawling &&
    (!!flow?.initial_completed || (!!explore && explore.status !== "RUNNING" && explore.status !== "COMPLETED"));
  let nowLine: ReactElement | string | null = null;
  if (crawling && found)
    nowLine = (
      <>
        <span className={cx(s.m, s[found.e.method])}>{found.e.method}</span>{" "}
        <span className={s.p}>
          <Pth p={found.e.path} />
        </span>
        {` 찾음${found.more ? ` (외 ${found.more}개)` : ""}`}
      </>
    );
  else if (flow && ended)
    nowLine =
      flow.stage === "diagnosing"
        ? `${crawlEndText(explore)} · 지금은 심화 진단 중 (아래 기록에 심화 진단 요청이 이어서 쌓입니다)`
        : crawlEndText(explore);
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
            <InspOpenButton>기록 상세</InspOpenButton>
            {/* [탐색 결과] · [진단 결과▾](예시 화면 진입)는 뺐다(사용자 11/17). 화면 자체는 남아 주소(#explore 등)로 열린다 */}
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
          {job?.reason_code ? <p className={s.help}>{`서버 사유: ${reasonText(job.reason_code)}`}</p> : null}
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
              {/* '지금 보는 곳' 이름표와 방문 · 방문 대기 · 페이지 상한 · 경과 줄은 지웠다(사용자 11/17).
                  이름표 없이 탐색 중엔 방문하는 경로, 끝나면 '탐색을 마쳤습니다' 한 줄만 */}
              <div className={s["crwl-top"]}>
                {nowLine ? (
                  <div className={s.now} id="cNow">
                    {nowLine}
                  </div>
                ) : null}
                {/* 탐색 진행: 서버에 진행률이 없어 탐색 중엔 페이지 상한 대비 방문 수로 그린다 (리뷰 #479). 끝나면 끝까지
                    채우고 색은 점검 진행 게이지와 같게 — 끝남 초록 · 멈춤 회색 (crawlBar) */}
                <div
                  className={cx(s.prog, bar.tone === "ok" && s["prog-ok"], bar.tone === "stop" && s["prog-stop"])}
                  id="cBar"
                  role="progressbar"
                  aria-label="탐색 진행 (페이지 상한 대비 방문)"
                  aria-valuemin={0}
                  aria-valuemax={100}
                  aria-valuenow={bar.pct}
                  aria-valuetext={bar.text}
                >
                  <i style={{ width: `${bar.fill}%` }} />
                </div>
                {/* 다 돌지 못한 끝(부분 완료 · 중지 · 탐색 안 함 · 상태 없음)은 막대 아래 따로 두지 않고 위 탐색 줄에 붙인다
                    (crawlEndText, 사용자 11/19) */}
              </div>
              {/* 숫자 칸 3개(발견한 API · 보낸 요청 · 막은 요청) — 보고서도 같은 부품을 쓴다 */}
              <CrawlMeters flow={flow} e2={e2.data} e2Failed={!!e2.error && !e2.data} />
              <section className={s.sec} aria-labelledby="cLogH">
                <div className={s.sh}>
                  <h2 id="cLogH">탐색 기록</h2>
                  {task ? (
                    <div className={s.seg} role="radiogroup" aria-label="기록 보기">
                      {(
                        [
                          ["recent", "최근 기록"],
                          ["all", `진단 요청 전체${reqTotal === null ? "" : ` ${reqTotal}`}`],
                        ] as const
                      ).map(([id, label]) => (
                        <label key={id}>
                          <input
                            type="radio"
                            name="cLogView"
                            checked={logView === id}
                            onChange={() => setLogView(id)}
                          />
                          <span>{label}</span>
                        </label>
                      ))}
                    </div>
                  ) : null}
                </div>
                {logView === "all" && task ? (
                  <E2RequestLog
                    task={task}
                    current={pick?.kind === "req" ? reqKeyOf(pick.row) : null}
                    onPick={pickReq}
                  />
                ) : (
                  <div className={s.log} id="cLog">
                    {/* 탐색 기록 칸(사용자 11/25): 같은 경로의 서버 출처를 줄마다, 결과는 맨 끝. 데이터 변경(조회 · 변경 가능)은
                      범위 확인 화면 딱지와 같은 분류라, 본문 칸(body_keys)은 거의 비어 있어 뺐다.
                      응답 · 요청 원문 칸은 서버가 한 줄('HTTP status 200' ·
                      'GET /path HTTP/1.1')만 저장해 결과 · 메서드 · 경로와 같아 뺐고, 근거는 서버 값이 없어, 찾은 곳은 실제
                      데이터에서 모두 시작 페이지('/')라 뺐다(오른쪽 패널엔 남김) */}
                    <div className={s.lh}>
                      <span>시각</span>
                      <span>메서드</span>
                      <span>경로</span>
                      <span>기록</span>
                      <span>출처</span>
                      <span>결과</span>
                    </div>
                    {flow.events.length ? (
                      flow.events.map((event) => {
                        if (event.method) {
                          const key = logKeyOf(event);
                          const endpoint = endpointOf.get(`${event.method} ${event.path ?? ""}`);
                          const on = logKey === key;
                          // 누르면 그 기록이 오른쪽 패널에 (줄 아래로 펼치지 않는다)
                          return (
                            <button
                              key={key}
                              className={cx(s.lr, on && s.on)}
                              type="button"
                              aria-current={on ? "true" : undefined}
                              onClick={() => pickLog(key)}
                            >
                              <span className={s.t}>{hms(event.at)}</span>
                              <span className={cx(s.m, s[event.method])}>{event.method}</span>
                              <span className={s.p}>{event.path ? <Pth p={event.path} /> : "—"}</span>
                              <span className={s.lk}>{eventText(event.kind)}</span>
                              {/* 같은 경로의 서버 출처(오른쪽 패널과 같은 값). 발견한 API 에 없는 경로면 — */}
                              <span className={cx(s.lk, s.mono)}>{endpoint?.source ?? NONE}</span>
                              <span>
                                <EventResult event={event} />
                              </span>
                            </button>
                          );
                        }
                        if (!LOG_SYSTEM_KINDS.has(event.kind)) return null;
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
                )}
              </section>
            </>
          ) : null}
        </div>
        <aside className={s.vi} aria-label="기록 상세">
          {detail !== null ? (
            detail
          ) : (
            <>
              <div className={s["vi-head"]}>
                <div>
                  <h2>기록을 고르세요</h2>
                </div>
                <CloseInspButton label="기록 상세" />
              </div>
              <p className={s.empty}>탐색 기록을 누르면 그 기록의 결과와 요청 · 응답이 여기에 나옵니다.</p>
            </>
          )}
        </aside>
        <PaneToggle />
      </div>
    </>
  );
}

/**
 * 범위 확인 (시안 data-view="scope"): 찾은 경로 중 심화 점검할 경로를 고른다. 선택은 개별 HTTP 송신 승인을 대신하지 않는다.
 * 가운데는 찾은 경로 묶음 카드(자산 탐색 오른쪽 패널에서 옮김)이고, 알약을 누르면 오른쪽 경로 상세에서 점검에 넣을지 고른다.
 * 오른쪽 패널은 경로를 고르기 전에는 접어 둔다.
 */
function ScopeSelection({ flow, head }: { flow: WorkflowSnapshot; head: (start: ReactNode) => ReactNode }) {
  const excludedRaw = useUrl((st) => st.job?.input.excluded_paths ?? "");
  const excludedInput = excludedRaw
    .split("\n")
    .map((p) => p.trim())
    .filter(Boolean);
  // 고를 수 있는 경로 = 서버 선택 검사와 같다(diagnosisflow controller.go 선택: 범위 밖 · 막힘(blocked) · 쓰기 미승인은 거부).
  // 막힘을 빼먹으면 그 경로를 넣었을 때 서버가 선택 전체를 거절했다(11/25 목업에서 /logout · DELETE)
  const eligible = (e: WorkflowEndpoint) =>
    e.in_scope && e.change !== "blocked" && (e.change === "attested_read" || flow.reversible_writes);
  const waiting = flow.stage === "awaiting_selection";
  // 사전 확인한 읽기 경로는 기본 포함, 데이터를 바꿀 수 있는 경로는 직접 고른다
  const [selected, setSelected] = useState<Set<string>>(() => {
    const server = flow.endpoints.filter((e) => e.selected).map((e) => e.id);
    if (server.length || !waiting) return new Set(server);
    return new Set(flow.endpoints.filter((e) => eligible(e) && e.change === "attested_read").map((e) => e.id));
  });
  // 오른쪽 상세에 보이는 경로
  const [detail, setDetail] = useState<string | null>(null);
  // 찾은 경로 카드의 거르기
  const [treeFilter, setTreeFilter] = useState("all");
  // 경로 네모칸 Shift 범위의 시작 칸(앞서 누른 네모칸)
  const [anchor, setAnchor] = useState<string | null>(null);
  // 사용자가 점검에서 뺀 경로 — 알약을 흐리게 하고 경로에 취소선을 긋는다. 다시 넣으면 지운다
  const [dropped, setDropped] = useState<ReadonlySet<string>>(() => new Set());
  const busy = useUrl((st) => st.flowBusy),
    verified = useUrl((st) => st.flowVerified),
    uncertain = useUrl((st) => st.flowUncertain),
    { openInsp } = useShell();
  const isOn = (e: WorkflowEndpoint) => (waiting ? selected.has(e.id) : e.selected);
  /** 점검 포함을 바꾸고, 들어 있던 경로를 빼면 뺀 경로로 기억한다(다시 넣으면 지운다). 원래 빠져 있던 경로는 들지 않는다 */
  const include = (ids: readonly string[], on: boolean) => {
    const was = new Set(ids.filter((id) => selected.has(id)));
    setSelected((old) => {
      const next = new Set(old);
      for (const id of ids) {
        if (on) next.add(id);
        else next.delete(id);
      }
      return next;
    });
    setDropped((old) => {
      const next = new Set(old);
      for (const id of ids) {
        if (on) next.delete(id);
        else if (was.has(id)) next.add(id);
      }
      return next;
    });
  };
  const toggle = (id: string) => include([id], !selected.has(id));
  // 진행 목록에서 다시 연 점검도 경로를 고를 수 있다(사용자 2026-11-22, F16)
  const locked = !waiting || busy || uncertain || !verified;
  const current = flow.endpoints.find((e) => e.id === detail) ?? null;
  const showDetail = (e: WorkflowEndpoint) => {
    setDetail(e.id);
    openInsp();
  };
  /**
   * 여러 경로를 한 번에 넣거나 뺀다. 고를 수 없는 경로(범위 밖 · 사전 쓰기 승인 없는 데이터 변경)는 건너뛴다.
   * 결과는 알약의 체크 · 흐림으로 바로 보여 따로 안내 글을 두지 않는다.
   * 넣어도 데이터 변경 경로는 보낼 때 승인 그대로다 (선택은 개별 송신 승인을 대신하지 않는다).
   */
  const setMany = (list: readonly WorkflowEndpoint[], on: boolean) =>
    include(
      list.filter(eligible).map((e) => e.id),
      on,
    );
  const byId = new Map(flow.endpoints.map((e) => [e.id, e]));
  const pick: ScopePick = {
    isOn,
    eligible,
    locked,
    current: current?.id ?? null,
    show: showDetail,
    toggle: (e, shift, order) => {
      const on = !isOn(e);
      const from = anchor ? order.indexOf(anchor) : -1;
      const to = order.indexOf(e.id);
      const ids = shift && from >= 0 && to >= 0 ? order.slice(Math.min(from, to), Math.max(from, to) + 1) : [e.id];
      setMany(
        ids.map((id) => byId.get(id)).filter((x): x is WorkflowEndpoint => !!x),
        on,
      );
      setAnchor(e.id);
    },
    setGroup: (rows, on) => setMany(rows, on),
    dropped,
  };
  /** 경로 딱지 (시안 scopeTag): 빠진 경로는 제외, 넣은 경로는 자동 포함 · 포함 · 보낼 때 승인. */
  const tag = (e: WorkflowEndpoint) => {
    if (!eligible(e) || !isOn(e)) return <span className={cx(s.tag, s.ex)}>제외</span>;
    if (e.change === "possible") return <span className={cx(s.tag, s.ap)}>포함 · 보낼 때 승인</span>;
    return <span className={cx(s.tag, s.in)}>자동 포함</span>;
  };
  return (
    <>
      {head(<ScopeStart flow={flow} ids={[...selected]} id="sStart" />)}
      {/* 오른쪽 패널은 경로를 고르기 전에도 늘 펼쳐 두고 고르라는 안내를 보인다(사용자 11/17 '계속 열어둬') */}
      <div className={s.vb}>
        <div className={s.vm}>
          <UrlNotices />
          <WorkflowNotice />
          <FoundPaths
            endpoints={flow.endpoints}
            excludedInput={excludedInput}
            crawling={flow.stage === "crawling"}
            filter={treeFilter}
            onFilter={(id) => {
              // 거르기로 안 보이게 된 알약이 고른 채 남지 않게 고르기를 비운다
              setTreeFilter(id);
              setAnchor(null);
            }}
            pick={pick}
          />
        </div>
        <aside className={s.vi} id="sInsp" aria-label="경로 상세">
          {current ? (
            <EndpointDetail
              endpoint={current}
              chip={tag(current)}
              extra={
                <label className={s.ckline}>
                  <input
                    type="checkbox"
                    aria-label={`${current.method} ${current.path} 점검에 포함`}
                    checked={isOn(current)}
                    disabled={locked || !eligible(current)}
                    onChange={() => toggle(current.id)}
                  />
                  <span>점검에 포함</span>
                </label>
              }
            />
          ) : (
            <>
              <div className={s["vi-head"]}>
                <div>
                  <h2>경로를 고르세요</h2>
                </div>
                <CloseInspButton label="경로 상세" />
              </div>
              <p className={s.empty}>
                가운데 발견한 API에서 경로를 누르면 그 경로 상세가 나오고, 점검에 넣을지 고를 수 있습니다.
              </p>
            </>
          )}
        </aside>
        <PaneToggle />
      </div>
    </>
  );
}

/** '선택한 범위로 점검 시작' 버튼 (화면 머리, 시안 sStart). */
function ScopeStart({ flow, ids, id }: { flow: WorkflowSnapshot; ids: string[]; id?: string }) {
  // 진행 목록에서 다시 연 점검(조회 결속)도 선택을 보낸다(사용자 2026-11-22, F16). 진단 상태를 확인했고 실행 중일 때만 선택을 보낸다(runWorkflowAction 과 같은 조건). 바탕 상태 조회가 실패하면
  // 진단이 결과 불명이 되어 보내지 않으므로, 버튼을 켜 둔 채 눌러도 아무 일 없게 두지 않고 끄고 까닭을 보인다
  const jobReady = useUrl(
    (st) => !!st.job && st.verified && !st.uncertain && ["preparing", "running"].includes(st.job.state),
  );
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
      disabled={!jobReady || !ids.length || busy || uncertain || !verified || flow.paused}
      title={jobReady ? undefined : "점검 상태를 확인하지 못해 보내지 않습니다. 상태를 다시 조회하세요."}
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
    <span className={cx(s.pill, scopeTone(flow, verified))}>{scopePill(flow, verified)}</span>
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
          {job ? "탐색이 끝나면 발견한 API가 여기에 나옵니다." : "이 브라우저에서 이어 보는 점검이 없습니다."}
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

/** 보고서 '탐색 · 선택 범위'에서 늘 보이는 고른 경로 줄 수. 나머지는 [더 보기]로 그 밑에 펼친다(사용자 11/25) */
const REPORT_PICKED_PREVIEW = 5;

/**
 * 탐색 · 선택 요약 (보고서 탭 아래). 고른 경로 목록이 길어(사용자 11/25) 앞 몇 줄만 보이고, 밑의 [나머지 n개 더 보기]를 누르면
 * 그 밑으로 나머지를 펼친다(다시 누르면 접기). 제목 옆 글(방식 · 단계 · 발견한 API · 방문 · 고른 경로 수)은 늘 보인다.
 */
export function WorkflowReport() {
  const flow = useUrl((st) => st.flow);
  const [open, setOpen] = useState(false);
  if (!flow) return null;
  const picked = flow.endpoints.filter((e) => e.selected);
  const more = picked.length - REPORT_PICKED_PREVIEW;
  const shown = open || more <= 0 ? picked : picked.slice(0, REPORT_PICKED_PREVIEW);
  return (
    <section className={s.sec} aria-labelledby="rWorkflowH">
      <div className={s.sh}>
        <h2 id="rWorkflowH">탐색 · 선택 범위</h2>
        {/* 탐색 수(발견한 API · 방문)는 보고서 머리 줄이 길어 여기 옆 글로(사용자 11/19) */}
        <span
          className={s.aside}
        >{`${flow.mode === "ai_auto_v1" ? "AI 자동" : "사용자 승인"} · ${flowStageText[flow.stage]} · 발견한 API ${flow.counts.found} · 방문 ${flow.counts.visited} · 고른 경로 ${picked.length}`}</span>
      </div>
      <div className={s.list} id="rWorkflowList">
        {shown.map((e) => (
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
      {more > 0 ? (
        <button
          className={s["sec-fold"]}
          type="button"
          aria-expanded={open}
          aria-controls="rWorkflowList"
          onClick={() => setOpen(!open)}
        >
          <Icon name="chev" />
          {open ? "접기" : `나머지 ${more}개 더 보기`}
        </button>
      ) : null}
    </section>
  );
}
