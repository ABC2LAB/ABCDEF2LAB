// live 화면용 순수 함수: 엔진 DTO → 작업대 화면 값(글·색 키). 상태를 갖지 않고 시계는 인자로 받는다.
// 추정값(호스트 뽑기 · 경로 종류 · 로컬 모델)은 화면에 "추정"을 붙인다(EstimateTag).

import { ApiError } from "@/lib/diagnosis/api";
import { isLocalUrl } from "@/lib/diagnosis/net";

import type { SevKey } from "../data/findings";
import { type PKind, pkind } from "../lib/kind";
import type { LiveActivity, LiveTask, RequestApproval, RequestApprovalRun } from "./api";

// ---------- 점검 ----------

const URL_RE = /\bhttps?:\/\/[^\s"'<>()[\]{}]+/i;

/** description · goal · name 순서로 첫 http(s) URL (끝의 문장부호는 뗀다). 없으면 null. 화면에 링크로 만들지 말 것. */
export function urlOf(task: Pick<LiveTask, "description" | "goal" | "name">): string | null {
  for (const text of [task.description, task.goal, task.name]) {
    const m = (text ?? "").match(URL_RE);
    if (!m) continue;
    const raw = m[0].replace(/[.,;:!?。，、]+$/, "");
    try {
      if (new URL(raw).hostname) return raw;
    } catch {
      // 다음 칸에서 찾는다
    }
  }
  return null;
}

/** 점검 대상 hostname (urlOf 에서). 엔진에 대상 칸이 없어 글에서 뽑은 **추정**값. 없으면 null. */
export function hostOf(task: Pick<LiveTask, "description" | "goal" | "name">): string | null {
  const u = urlOf(task);
  if (!u) return null;
  try {
    return new URL(u).hostname || null;
  } catch {
    return null;
  }
}

/** 목록·레일에 쓸 점검 이름: hostOf → name → description 앞부분 → "점검 #id". */
export function taskLabel(task: Pick<LiveTask, "id" | "description" | "goal" | "name">): string {
  const h = hostOf(task);
  if (h) return h;
  const name = task.name.trim();
  if (name) return name;
  const d = task.description.trim();
  if (d) return d.length > 40 ? `${d.slice(0, 40)}…` : d;
  return `점검 #${task.id}`;
}

/** 상태 색 키. 레일 `.dot.{tone}` · 알약 `.pill.{tone}` (stop 은 알약에 클래스 없음 = 회색). */
export type Tone = "run" | "wait" | "ok" | "stop";

/** 점검 상태 표시. */
export interface Phase {
  label: string;
  tone: Tone;
}

const PHASES: Readonly<Record<string, Phase>> = {
  created: { label: "준비 중", tone: "wait" },
  queued: { label: "대기열", tone: "wait" },
  running: { label: "점검 중", tone: "run" },
  paused: { label: "일시정지됨", tone: "wait" },
  done: { label: "점검 완료", tone: "ok" },
  failed: { label: "실패", tone: "stop" },
  timeout: { label: "시간 초과", tone: "stop" },
};

/** task.status → 글·색. 모르는 값은 원문 그대로 회색(stop). */
export function taskPhase(task: Pick<LiveTask, "status">): Phase {
  return PHASES[task.status] ?? { label: task.status || "알 수 없음", tone: "stop" };
}

/** 아직 안 끝난 점검인가 (created · queued · running · paused). 끝(done · failed · timeout)이면 false. */
export function isActive(task: Pick<LiveTask, "status">): boolean {
  return task.status !== "done" && task.status !== "failed" && task.status !== "timeout";
}

/** 시작부터 끝(안 끝났으면 nowMs)까지 초. created_unix 가 없으면 0. */
export function elapsed(task: Pick<LiveTask, "created_unix" | "completed_unix">, nowMs: number): number {
  if (!task.created_unix) return 0;
  const end = task.completed_unix || Math.floor(nowMs / 1000);
  return Math.max(0, end - task.created_unix);
}

/** 초 → "40초" · "12분" · "1시간 5분" · "2시간". */
export function fmtDur(sec: number): string {
  const s = Math.max(0, Math.floor(sec));
  if (s < 60) return `${s}초`;
  if (s < 3600) return `${Math.floor(s / 60)}분`;
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  return m ? `${h}시간 ${m}분` : `${h}시간`;
}

/** 시각 값 → ms. 문자열(RFC3339)은 Date.parse, 숫자는 1e12 미만이면 unix 초로 본다. 못 읽으면 null. */
export function toMs(ts: string | number | null | undefined): number | null {
  if (ts === null || ts === undefined || ts === "" || ts === 0) return null;
  if (typeof ts === "number") {
    if (!Number.isFinite(ts)) return null;
    return ts < 1e12 ? ts * 1000 : ts;
  }
  const t = Date.parse(ts);
  return Number.isNaN(t) ? null : t;
}

const pad = (n: number) => `0${n}`.slice(-2);

/**
 * 상대 시각 (로컬 시간대): 1분 미만 "방금", 1시간 미만 "12분 전", 하루 미만 "3시간 전",
 * 그 이상은 "10/23"(올해) 또는 "2025/10/23". 미래·못 읽는 값은 "".
 */
export function relTime(ts: string | number | null | undefined, nowMs: number): string {
  const t = toMs(ts);
  if (t === null) return "";
  const d = nowMs - t;
  if (d < -60_000) return "";
  if (d < 60_000) return "방금";
  if (d < 3_600_000) return `${Math.floor(d / 60_000)}분 전`;
  if (d < 86_400_000) return `${Math.floor(d / 3_600_000)}시간 전`;
  const a = new Date(t);
  const md = `${pad(a.getMonth() + 1)}/${pad(a.getDate())}`;
  return a.getFullYear() === new Date(nowMs).getFullYear() ? md : `${a.getFullYear()}/${md}`;
}

/** 시각 → 로컬 "HH:MM:SS" (못 읽으면 ""). 렌더 중 시계를 읽지 않으므로 써도 된다. */
export function clockOf(ts: string | number | null | undefined): string {
  const t = toMs(ts);
  if (t === null) return "";
  const d = new Date(t);
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

/** 수 → "12,345". */
export function fmtCount(n: number): string {
  return String(Math.round(n)).replace(/\B(?=(\d{3})+(?!\d))/g, ",");
}

/** 토큰 수 → 숫자와 단위 ("4.2" + "M", "830" + "K", "512" + ""). 대시보드 `.v` 의 `<small>` 단위에 맞춤. */
export function fmtTokens(n: number): { n: string; unit: "" | "K" | "M" } {
  const v = Math.max(0, n);
  if (v >= 1_000_000) return { n: (v / 1_000_000).toFixed(1), unit: "M" };
  if (v >= 1_000) return { n: (v / 1_000).toFixed(v >= 100_000 ? 0 : 1), unit: "K" };
  return { n: String(Math.round(v)), unit: "" };
}

// ---------- 발견 ----------

/** 엔진 심각도 순서 (높은 것부터). */
export const SEVERITIES = ["critical", "high", "medium", "low"] as const;
/** 엔진 심각도 값. */
export type Severity = (typeof SEVERITIES)[number];

const SEV: Readonly<Record<Severity, SevKey>> = { critical: "crit", high: "hi", medium: "mid", low: "lo" };

/** 엔진 심각도 → 작업대 Sev 키 (`<Sev k=…/>`). 모르는 값(info 등)은 null → 원문 표시. */
export function sevKey(severity: string): SevKey | null {
  return SEV[severity.toLowerCase() as Severity] ?? null;
}

/** 발견 처리 상태 이름 (워크플로 상태). **판정이 아니다** — 화면에서는 "처리 상태" 칸으로만 보인다. */
export const FINDING_STATUS_LABEL: Readonly<Record<string, string>> = {
  pending: "미검토",
  in_progress: "검토 중",
  confirmed: "검토 확인",
  resolved: "해결됨",
  fixed: "수정됨",
  false_positive: "오탐 처리",
  ignored: "무시함",
  duplicate: "중복",
  risk_accepted: "위험 수용",
};

/** finding.status → 처리 상태 글 (모르는 값은 원문). 판정 딱지(Verdict)로 쓰지 말 것. */
export function findingStatusLabel(status: string): string {
  return FINDING_STATUS_LABEL[status] ?? (status || "미검토");
}

// ---------- 경로 ----------

/** URL(또는 경로) → 경로만. 스킴·호스트·쿼리·조각을 떼고 `{param}` 은 그대로 둔다(URL 파서를 쓰지 않음). 빈 값은 "/". */
export function pathOf(url: string): string {
  let p = url.trim().replace(/^[a-z][a-z0-9+.-]*:\/\/[^/?#]*/i, "");
  p = p.replace(/[?#].*$/, "");
  if (!p) return "/";
  return p.startsWith("/") ? p : `/${p}`;
}

/** 경로 묶음 = 첫 경로 조각 ("/api/orders/1" → "/api", "/" → "/"). */
export function groupOf(path: string): string {
  const seg = pathOf(path).split("/").find(Boolean);
  return seg ? `/${seg}` : "/";
}

/**
 * 메서드·경로 → 화면 종류(get 조회 · login 로그인 · ap 승인 필요 · ex 제외). lib/kind 의 규칙을 그대로 쓴
 * **추정**값이다(엔진은 종류를 주지 않는다) → `<EstimateTag />` 를 함께 보일 것. 메서드를 모르면 ap(보수적으로).
 */
export function kindOf(method: string, path: string): PKind {
  return pkind({ m: method.toUpperCase(), p: pathOf(path) });
}

// ---------- 의도(점검 계획) ----------

/** 계획 한 줄 상태: done 완료 · run 진행 중 · wait 대기 · other 그 밖(원문 표시). `.plan li.done` · `li.run` 과 맞춤. */
export interface PlanState {
  row: "done" | "run" | "wait" | "other";
  label: string;
}

/** intent.state → 계획 줄 상태. open→대기, running→진행 중, done→완료, 그 밖(blocked·exhausted·stopped·paused·deleted)은 원문. */
export function planState(state: string): PlanState {
  if (state === "open") return { row: "wait", label: "대기" };
  if (state === "running") return { row: "run", label: "진행 중" };
  if (state === "done") return { row: "done", label: "완료" };
  return { row: "other", label: state || "알 수 없음" };
}

/** 의도 payload(JSON 문자열) → 요약과 자산 id. 깨진 JSON 이면 원문을 요약으로(최대 200자). */
export function parseIntentSummary(payload: string): { summary: string; assetIds: number[] } {
  let p: unknown;
  try {
    p = JSON.parse(payload);
  } catch {
    return { summary: payload.slice(0, 200), assetIds: [] };
  }
  if (!p || typeof p !== "object") return { summary: typeof p === "string" ? p : "", assetIds: [] };
  const o = p as Record<string, unknown>;
  const text = [o.summary, o.text, o.description].find((v): v is string => typeof v === "string" && v.trim() !== "");
  // asset_ids 가 정본, 예전 키 target_ids · target_id 도 읽는다 (agent/tools.go compactIntents)
  let ids: unknown = o.asset_ids ?? o.target_ids;
  if (ids === undefined && o.target_id !== undefined && o.target_id !== "") ids = [o.target_id];
  const assetIds = (Array.isArray(ids) ? ids : [])
    .map((v) => Number(v))
    .filter((n) => Number.isSafeInteger(n) && n > 0);
  return { summary: text ?? "", assetIds };
}

// ---------- 활동 ----------

/** 활동 종류 이름. */
export const ACTIVITY_KIND_LABEL: Readonly<Record<string, string>> = {
  text: "메모",
  tool_use: "도구 호출",
  result: "결과",
  round: "단계",
  llm_switch: "모델 전환",
  usage: "사용량",
  user: "사용자",
  thinking: "추론",
};

/** 지난 활동 한 줄. dot 은 `.fd.{dot}` 색 (ok 결과 · col 도구 호출 · blk 오류), 없으면 null. text 는 아직 안 가린 원문. */
export interface FeedRow {
  key: string;
  /** 로컬 "HH:MM:SS" */
  time: string;
  kindLabel: string;
  text: string;
  isError: boolean;
  dot: "ok" | "col" | "blk" | null;
}

/** 활동 → 지난 활동 줄. 요약이 비면 도구 이름. 화면에서는 text 에 maskSecrets 를 걸 것. */
export function feedRow(a: LiveActivity): FeedRow {
  let dot: FeedRow["dot"] = null;
  if (a.is_error) dot = "blk";
  else if (a.kind === "tool_use") dot = "col";
  else if (a.kind === "result") dot = "ok";
  const text = a.summary.trim() || (a.tool ? `${a.tool} 호출` : "");
  return {
    key: String(a.seq),
    time: clockOf(a.ts),
    kindLabel: ACTIVITY_KIND_LABEL[a.kind] ?? (a.kind || "활동"),
    text,
    isError: a.is_error,
    dot,
  };
}

// ---------- 전송 통제 ----------

/** 전송 통제 집계. known=false 면 실행 정보(run.execution)가 아직 없어 허용·막음·불명·전송 시도가 0 이다. */
export interface ApprovalCounts {
  /**
   * 허용 판정 (counts.allowed). 감독자가 허용한 수일 뿐 실제 전송 수가 아니다 —
   * 허용 뒤 대상에 보내기 전에 취소된 요청도 들어간다(e2proxy TestIntentACKThenCallerCancellationIsAllowedButUnobserved).
   */
  allowed: number;
  /** 대상 전송 시도 (counts.forward_attempted). 실제로 대상에 보내려 한 수 */
  forwarded: number;
  /** 막은 요청 (counts.denied + counts.unsupported) */
  blocked: number;
  /** 승인 필요로 보류 (approvals 중 state=pending) */
  held: number;
  /** 전송 불명 (counts.unknown) */
  unknown: number;
  known: boolean;
}

/** request-approvals 응답 → 전송 통제 네 칸. */
export function approvalCounts(
  run: RequestApprovalRun | null | undefined,
  approvals: readonly Pick<RequestApproval, "state">[] | null | undefined,
): ApprovalCounts {
  const c = run?.execution?.counts;
  const held = (approvals ?? []).filter((a) => a.state === "pending").length;
  if (!c) return { allowed: 0, forwarded: 0, blocked: 0, held, unknown: 0, known: false };
  return {
    allowed: c.allowed || 0,
    forwarded: c.forward_attempted || 0,
    blocked: (c.denied || 0) + (c.unsupported || 0),
    held,
    unknown: c.unknown || 0,
    known: true,
  };
}

// ---------- 모델 ----------

/** LLM base_url 이 로컬·사설망을 가리키는가 (localhost · 127.* · ::1 · host.docker.internal · 사설 IP · 점 없는 이름). **추정**. */
export function isLocalBaseUrl(url: string): boolean {
  return !!url && isLocalUrl(url);
}

// ---------- 오류 ----------

const UNSUPPORTED = new Set([403, 404, 409, 501, 503]);

/** 이 서버 모드에서 안 되는 기능 응답인가 (ApiError 403·404·409·501·503). 그 섹션만 "제공되지 않음"으로 접는다. */
export function isUnsupported(err: unknown): boolean {
  return err instanceof ApiError && UNSUPPORTED.has(err.status);
}

/**
 * 엔진에 닿지 못했는가: ApiError 0(연결 실패) · 502 · 504, 또는 본문 없는 500(개발 서버 프록시가 8787 에 못 붙음).
 * TypeError(fetch 실패)도 true.
 */
export function isOffline(err: unknown): boolean {
  if (err instanceof TypeError) return true;
  if (!(err instanceof ApiError)) return false;
  return (
    err.status === 0 || err.status === 502 || err.status === 504 || (err.status === 500 && err.message === "HTTP 500")
  );
}

/** 오류 → 짧은 안내 글. */
export function errorText(err: unknown): string {
  if (isOffline(err)) return "엔진에 연결하지 못했습니다.";
  if (err instanceof ApiError) {
    if (err.status === 401) return "로그인이 필요합니다.";
    if (UNSUPPORTED.has(err.status)) return "이 서버 모드에서는 제공되지 않음";
    if (err.status === 429) return "요청이 너무 많습니다. 잠시 뒤 다시 시도하세요.";
    return `엔진 오류 (HTTP ${err.status})${err.message && err.message !== `HTTP ${err.status}` ? `: ${err.message}` : ""}`;
  }
  return err instanceof Error && err.message ? err.message : "알 수 없는 오류";
}
