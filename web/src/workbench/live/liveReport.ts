// 점검 진행 · 보고서(live) 화면 전용 순수 함수. 상태·시계를 갖지 않는다(지금 시각은 인자로).

import type { RemediationEntry } from "@/abc2lab-ui/lib/remediation";

import type { SevKey } from "../data/findings";
import type { LiveCoverage, LiveFinding, LiveIntent, LiveTask, LlmProfile } from "./api";
import { fmtDur, isActive, isLocalBaseUrl, type PlanState, parseIntentSummary, planState, sevKey, toMs } from "./map";
import { maskSecrets } from "./mask";

// ---------- 점검 계획 ----------

/** 계획 한 줄 (요약은 이미 가린 글). */
export interface PlanRow {
  key: string;
  text: string;
  state: PlanState;
}

const PLAN_ORDER: Readonly<Record<PlanState["row"], number>> = { run: 0, wait: 1, done: 2, other: 3 };

/**
 * 의도 목록(API 는 최신순) → 계획 줄. 진행 중 → 대기 → 완료 → 그 밖(blocked 등, 원문 상태) 순서이고
 * 같은 묶음 안에서는 API 순서를 지킨다. 요약은 payload 에서 꺼내 maskSecrets.
 */
export function planRows(intents: readonly LiveIntent[]): PlanRow[] {
  return intents
    .map((n, i) => ({ n, i, st: planState(n.state) }))
    .sort((a, b) => PLAN_ORDER[a.st.row] - PLAN_ORDER[b.st.row] || a.i - b.i)
    .map(({ n, st }) => {
      const sum = parseIntentSummary(n.payload).summary.trim();
      return { key: n.id, text: maskSecrets(sum) || n.type || `의도 #${n.id}`, state: st };
    });
}

// ---------- 발견 ----------

/** 심각도별 수 (other = 엔진 심각도 넷에 없는 값). */
export type SevCounts = Record<SevKey, number> & { other: number; total: number };

/** 발견 목록 → 심각도별 수. */
export function sevCounts(fs: readonly Pick<LiveFinding, "severity">[]): SevCounts {
  const c: SevCounts = { crit: 0, hi: 0, mid: 0, lo: 0, other: 0, total: fs.length };
  for (const f of fs) {
    const k = sevKey(f.severity);
    if (k) c[k]++;
    else c.other++;
  }
  return c;
}

const SEV_RANK: Readonly<Record<SevKey, number>> = { crit: 0, hi: 1, mid: 2, lo: 3 };

/** 심각도 순위 (높을수록 작다, 모르는 값은 4). */
export function sevRank(severity: string): number {
  const k = sevKey(severity);
  return k ? SEV_RANK[k] : 4;
}

/** 심각도 순으로 (같으면 API 순서 유지). */
export function bySeverity<T extends Pick<LiveFinding, "severity">>(fs: readonly T[]): T[] {
  return fs
    .map((f, i) => ({ f, i }))
    .sort((a, b) => sevRank(a.f.severity) - sevRank(b.f.severity) || a.i - b.i)
    .map((x) => x.f);
}

/** 발견 제목: name → vulnclass → "발견 #id" (가림 거침). */
export function findingTitle(f: Pick<LiveFinding, "id" | "name" | "vulnclass">): string {
  return maskSecrets(f.name.trim() || f.vulnclass.trim()) || `발견 #${f.id}`;
}

// ---------- 진행률 · 커버리지 ----------

/** 진행률과 그 기준. goals = 목표 달성(goals_met/total), coverage = 커버리지 pct. 둘 다 없으면 null. */
export interface Progress {
  pct: number | null;
  basis: "goals" | "coverage" | null;
}

/** 목표가 있으면 목표 달성률, 없으면 켜진 커버리지 pct, 그것도 없으면 없음. 남은 시간 같은 짐작은 하지 않는다. */
export function progressOf(task: Pick<LiveTask, "goals_total" | "goals_met">, cov: LiveCoverage | null): Progress {
  if (task.goals_total > 0) {
    const p = Math.round((100 * task.goals_met) / task.goals_total);
    return { pct: Math.min(100, Math.max(0, p)), basis: "goals" };
  }
  if (cov?.enabled && cov.pct !== null) return { pct: Math.round(cov.pct * 100), basis: "coverage" };
  return { pct: null, basis: null };
}

/** 커버리지의 endpoint 줄 (by_type). 없으면 null. */
export function endpointCov(cov: LiveCoverage | null): { tested: number; total: number } | null {
  const row = cov?.byType.find((t) => t.type === "endpoint");
  return row ? { tested: row.tested, total: row.total } : null;
}

/** 커버리지 % (켜져 있고 분모가 있을 때만), 아니면 null. */
export function covPct(cov: LiveCoverage | null): number | null {
  return cov?.enabled && cov.pct !== null ? Math.round(cov.pct * 100) : null;
}

// ---------- 시각 ----------

const pad = (n: number) => `0${n}`.slice(-2);

/** 시각 → 로컬 "MM/DD HH:MM" (nowMs 와 해가 다르면 "YYYY/MM/DD HH:MM"). 못 읽으면 "". */
export function dateTimeOf(ts: string | number | null | undefined, nowMs = 0): string {
  const t = toMs(ts);
  if (t === null) return "";
  const d = new Date(t);
  const md = `${pad(d.getMonth() + 1)}/${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
  return nowMs && new Date(nowMs).getFullYear() !== d.getFullYear() ? `${d.getFullYear()}/${md}` : md;
}

/** 보고서 '소요': 끝났으면 completed - created, 안 끝났으면 "진행 중", 시각이 없으면 "—". */
export function durationText(
  task: Pick<LiveTask, "status" | "created_unix" | "completed_unix">,
  nowMs: number,
): { text: string; running: boolean } {
  if (task.created_unix && task.completed_unix) {
    return { text: fmtDur(Math.max(0, task.completed_unix - task.created_unix)), running: false };
  }
  if (isActive(task)) {
    const el = task.created_unix && nowMs ? fmtDur(Math.max(0, Math.floor(nowMs / 1000) - task.created_unix)) : "";
    return { text: el ? `진행 중 · 경과 ${el}` : "진행 중", running: true };
  }
  return { text: "—", running: false };
}

// ---------- 모델 ----------

/** 보고서 '모델'. estimated = 지금 활성 프로필이 아니라 지정 목록·기본 프로필에서 짐작함. local = base_url 로 로컬 추정. */
export interface ModelInfo {
  text: string;
  estimated: boolean;
  local: boolean;
}

/**
 * task.active_llm_profile_id → 프로필 이름(모델). 없으면 llm_profile_ids 첫 항목, 그것도 없으면 기본(is_default) 프로필 — 이 둘은 추정.
 * 프로필 목록을 못 받았으면 "프로필 #id". 아무 단서도 없으면 null.
 */
export function modelOf(
  task: Pick<LiveTask, "active_llm_profile_id" | "llm_profile_ids">,
  profiles: readonly LlmProfile[] | null,
): ModelInfo | null {
  let id: number | null = task.active_llm_profile_id;
  let estimated = false;
  if (id === null && task.llm_profile_ids.length) {
    id = task.llm_profile_ids[0];
    estimated = true;
  }
  let p: LlmProfile | undefined;
  if (id !== null) p = profiles?.find((x) => x.id === String(id));
  else {
    // 지정이 없으면 엔진 기본 프로필로 돌았을 것 — 짐작
    p = profiles?.find((x) => x.is_default);
    estimated = true;
  }
  if (p) return { text: p.model || p.name || `프로필 #${p.id}`, estimated, local: isLocalBaseUrl(p.base_url) };
  if (id !== null) return { text: `프로필 #${id}`, estimated, local: false };
  return null;
}

// ---------- 우선 조치 ----------

/** 보고서 분류(remediation outcome) 이름. 판정 집계가 아니라 항목마다 서버가 붙인 분류다. */
export const OUTCOME_LABEL: Readonly<Record<string, string>> = {
  review: "검토 필요",
  confirmed: "저장된 검증 있음",
  not_executed: "실행 안 됨",
  unknown: "결과 불명",
};

/** outcome → 이름 (모르는 값은 원문). */
export function outcomeLabel(outcome: string): string {
  return OUTCOME_LABEL[outcome] ?? (outcome || "분류 없음");
}

/**
 * 조치 항목 ↔ 발견. server/diagnostic_report.go 가 발견 항목의 entry_id 를 `finding-<findings.id>` 로 만들고,
 * 발견 목록의 finding_id 가 같은 findings.id 다(server/dto.go). candidate-* 등 다른 항목은 발견이 아니라 붙이지 않는다.
 */
export function findingForEntry(entryId: string, byFindingId: ReadonlyMap<string, LiveFinding>): LiveFinding | null {
  const m = /^finding-([1-9][0-9]*)$/.exec(entryId);
  return m ? (byFindingId.get(m[1]) ?? null) : null;
}

/** 조치 항목 + 이어진 발견(있으면). 이어진 것은 심각도 순으로 앞에, 나머지는 서버 순서대로 뒤에. */
export function prioRows(
  entries: readonly RemediationEntry[],
  findings: readonly LiveFinding[] | null,
): Array<{ entry: RemediationEntry; finding: LiveFinding | null }> {
  const byFid = new Map<string, LiveFinding>();
  for (const f of findings ?? []) if (f.finding_id) byFid.set(f.finding_id, f);
  return entries
    .map((entry, i) => ({ entry, finding: findingForEntry(entry.entry_id, byFid), i }))
    .sort(
      (a, b) =>
        (a.finding ? sevRank(a.finding.severity) : 9) - (b.finding ? sevRank(b.finding.severity) : 9) || a.i - b.i,
    )
    .map(({ entry, finding }) => ({ entry, finding }));
}
