// 자산 탐색 · 범위 확인(live) 전용 순수 함수: 엔드포인트 → 화면 줄(경로 · 묶음 · 종류 추정), 거르기 · 정렬,
// 엔드포인트에 맞는 트래픽 기록 고르기, 일시정지/재개 동작 고르기. 상태를 갖지 않는다.

import type { PKind } from "../lib/kind";
import type { LiveEndpoint, LiveTask, TrafficRow } from "./api";
import { groupOf, kindOf, pathOf } from "./map";

/** 화면 한 줄 (엔드포인트 하나). */
export interface EpRow {
  id: number;
  /** 대문자 메서드 ("" = 모름) */
  m: string;
  /** 경로 (스킴·호스트·쿼리 뗀 것, `{param}` 유지) */
  p: string;
  /** 묶음 = 첫 경로 조각 */
  g: string;
  /** 종류 (추정) */
  k: PKind;
  /** 경로에 `{param}` 이 있음 */
  hasId: boolean;
  ep: LiveEndpoint;
}

/** 엔드포인트 → 화면 줄. */
export function epRow(ep: LiveEndpoint): EpRow {
  const p = pathOf(ep.url);
  return { id: ep.id, m: ep.method, p, g: groupOf(p), k: kindOf(ep.method, p), hasId: /\{[^/{}]+\}/.test(p), ep };
}

/** 종류 표시 순서. */
export const KIND_ORDER: readonly PKind[] = ["get", "login", "ap", "ex"];

/** 종류 이름 (추정값이라 화면에 EstimateTag 를 함께 둔다). ex 는 lib/kind 의 기본 제외 모양(로그아웃·삭제 등). */
export const KIND_LABEL: Readonly<Record<PKind, string>> = {
  get: "조회",
  login: "로그인",
  ap: "데이터 변경",
  ex: "제외 후보",
};

/** 종류별 개수. */
export function kindCounts(rows: readonly Pick<EpRow, "k">[]): Record<PKind, number> {
  const c: Record<PKind, number> = { get: 0, login: 0, ap: 0, ex: 0 };
  for (const r of rows) c[r.k]++;
  return c;
}

const MORD = ["GET", "POST", "PUT", "PATCH", "DELETE"];

/** 메서드 순서 (모르는 메서드는 뒤로). */
function mRank(m: string): number {
  const i = MORD.indexOf(m);
  return i < 0 ? MORD.length : i;
}

/** 같은 묶음 안: 경로 글자 순, 같은 경로면 메서드 순, 그다음 id. */
export function byPathMethod(a: EpRow, b: EpRow): number {
  if (a.p !== b.p) return a.p < b.p ? -1 : 1;
  const d = mRank(a.m) - mRank(b.m);
  return d || a.id - b.id;
}

/** 묶음별로 나눈다. 묶음은 "/" 먼저, 그다음 글자 순. 줄은 byPathMethod. */
export function groupRows(rows: readonly EpRow[]): Array<{ g: string; rows: EpRow[] }> {
  const by = new Map<string, EpRow[]>();
  for (const r of rows) {
    const list = by.get(r.g);
    if (list) list.push(r);
    else by.set(r.g, [r]);
  }
  return [...by.keys()]
    .sort((a, b) => {
      if (a === b) return 0;
      if (a === "/") return -1;
      if (b === "/") return 1;
      return a < b ? -1 : 1;
    })
    .map((g) => ({ g, rows: (by.get(g) as EpRow[]).sort(byPathMethod) }));
}

/** 범위 확인 보기 필터 키. */
export type LiveScopeFilter = "all" | "get" | "ap" | "login" | "ex" | "id";

/** [키, 이름, 조건] — 표시 순서. 종류 필터는 추정값, ID 포함은 경로에서 본 사실. */
export const SCOPE_FILTERS: ReadonlyArray<readonly [LiveScopeFilter, string, (r: EpRow) => boolean]> = [
  ["all", "전체", () => true],
  ["get", KIND_LABEL.get, (r) => r.k === "get"],
  ["ap", KIND_LABEL.ap, (r) => r.k === "ap"],
  ["login", KIND_LABEL.login, (r) => r.k === "login"],
  ["ex", KIND_LABEL.ex, (r) => r.k === "ex"],
  ["id", "ID 포함", (r) => r.hasId],
];

/** 검색어(소문자, 앞뒤 공백 뗀 것)가 메서드·경로·묶음·도메인·파라미터 이름에 있는가. 빈 검색어는 true. */
export function matchesQuery(r: EpRow, q: string): boolean {
  if (!q) return true;
  const hay = `${r.m} ${r.p} ${r.g} ${r.ep.domain} ${r.ep.params.map((x) => x.name).join(" ")}`.toLowerCase();
  return hay.includes(q);
}

/** 실제 경로가 엔드포인트 경로(템플릿)와 맞는가. `{x}` 조각은 빈 조각이 아닌 아무 조각과 맞는다. 끝 "/" 는 무시. */
export function pathMatches(actual: string, pattern: string): boolean {
  const norm = (x: string) => (x.length > 1 ? x.replace(/\/+$/, "") : x);
  const a = norm(actual).split("/");
  const b = norm(pattern).split("/");
  if (a.length !== b.length) return false;
  return b.every((seg, i) => (/^\{[^/{}]+\}$/.test(seg) ? a[i] !== "" : seg === a[i]));
}

/** 트래픽 path 거르기에 쓸 앞부분: 첫 `{` 앞까지 (서버는 url_template 부분 일치). "/" 만 남으면 "" (거르지 않음). */
export function trafficPathPrefix(p: string): string {
  const i = p.indexOf("{");
  const head = i < 0 ? p : p.slice(0, i);
  return head === "/" ? "" : head;
}

/**
 * 엔드포인트에 맞는 가장 최근 기록 (rows 는 서버가 최신순으로 준 것). 메서드(알면) · 호스트(대소문자 무시, 같아야 함) ·
 * 경로 템플릿이 모두 맞아야 한다. 서버의 host·path 거르기는 부분 일치라 여기서 다시 확인한다.
 */
export function pickExchange(rows: readonly TrafficRow[], ep: Pick<EpRow, "m" | "p">, host: string): TrafficRow | null {
  const h = host.toLowerCase();
  return (
    rows.find(
      (r) =>
        (!ep.m || r.method.toUpperCase() === ep.m) &&
        (!h || r.host.toLowerCase() === h) &&
        pathMatches(pathOf(r.url), ep.p),
    ) ?? null
  );
}

/** 바이트 → "512 B" · "1.2 KB" · "3.4 MB". 0 이하는 "". */
export function fmtBytes(n: number): string {
  if (!(n > 0)) return "";
  if (n < 1024) return `${Math.round(n)} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

/** 일시정지/재개 중 지금 할 수 있는 것: 일시정지됨 → resume, 점검 중 → pause, 그 밖(준비·대기열·끝남) → null. */
export function ctlAction(task: Pick<LiveTask, "status" | "paused">): "pause" | "resume" | null {
  if (task.paused || task.status === "paused") return "resume";
  if (task.status === "running") return "pause";
  return null;
}

/** 긴 글은 앞 max 자만 (요청·응답 원문이 클 수 있음). */
export function clip(text: string, max = 20_000): { text: string; clipped: boolean } {
  return text.length > max ? { text: text.slice(0, max), clipped: true } : { text, clipped: false };
}
