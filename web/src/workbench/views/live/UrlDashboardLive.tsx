"use client";

// 대시보드 (시안 짜임: 요약 4칸 · 발견 · 검토 대상 · 진행 중인 점검 · 최근 끝난 점검 + 오른쪽 상세).
// 서버 보존 목록을 표시하고, 판정 집계는 선택한 진단의 저장된 보고서를 조회해 표시한다.
// 발견 목록은 그 진단의 저장된 결과(보고서와 같은 원장)에서 정상 대조를 뺀 것만 보인다. 확정만 취약점이므로 제목은
// 중립 이름("발견 · 검토 대상")으로 두고 건수 옆에 확정 수를 따로 보인다(검토 필요 · 미실행 · 결과 불명은 취약점이
// 아님, 절대 규칙 5). 결과가 저장되기 전에는 비워 두고 "없음"이라고 말하지 않는다(절대 규칙 5).
// 결정 대기(요청 승인 · 경로 선택)는 시안처럼 요약 칸과 점검 상세에서 보이고, 승인 요청은 상세에서 연다.

import { type ReactNode, useEffect, useState } from "react";

import { activeURLRun, endedURLRun, type URLRunSummary } from "@/lib/diagnosis/diagnosis-runs";
import { diagnosticOutcome } from "@/lib/diagnosis/diagnostic-results";
import { type E2Counts, type E2Flow, getE2Flow } from "@/lib/diagnosis/e2flow";
import type { RemediationEntry } from "@/lib/diagnosis/remediation";
import type { RequestApproval } from "@/lib/diagnosis/request-approvals";
import { entrySeverity } from "@/lib/diagnosis/severity";

import { SEVL, type SevKey } from "../../data/findings";
import { cx } from "../../lib/cx";
import { fetchRemediation } from "../../live/api";
import { useHiddenRunIDs } from "../../live/hiddenRuns";
import { dateTimeOf, fmtTokens, sevKey } from "../../live/map";
import { maskSecrets } from "../../live/mask";
import { LoadingRow, NotYet } from "../../live/parts";
import { useRunInput } from "../../live/runInput";
import { selectUrlRun, useUrl, useUrlGates } from "../../live/urlDiagnosis";
import { URL_ENDED_STATES, urlHost, urlStateText } from "../../live/urlMap";
import { useUrlRuns } from "../../live/urlRunList";
import { type Poll, usePoll } from "../../live/usePoll";
import { useShell } from "../../shell";
import { Icon } from "../../ui/icons";
import { InspHead, InspOpenButton, Isec, PaneToggle, useScrollTopOnChange } from "../../ui/parts";
import s from "../../workbench.module.css";
import { ApprovalActions, ApprovalFacts, ApprovalRequestCode, useRequestApprovals } from "./approvals";
import { DashOverview } from "./DashOverview";
import { Ring, RingCard, type RingSeg } from "./dashRings";
import { JobSummary, jobResult, pendingRows, useCoveragePct } from "./jobSummary";
import { ReportReadError, RunListControls, RunListStatus, RunRow, SelectedRunStatus } from "./runRows";
import { EntryEp, FindingDetail, ORDER, Outcome, Sev, UnfollowedActiveRuns } from "./UrlLiveLive";
import { UrlHead } from "./urlParts";
import { type Chip, Chips } from "./urlRecords";

/** 쓰기 메서드 (승인 요청 딱지). 메서드 기준 이름일 뿐, GET · HEAD · query 도 상태를 바꿀 수 있다. */
const WRITE = new Set(["POST", "PUT", "PATCH", "DELETE"]);

type Sel = { kind: "job" } | { kind: "approval"; id: string } | { kind: "finding"; id: string };

/** 대시보드는 칸마다 최대 3개만 보인다 (시안 v55). 전체는 '전체 보기' → 진행 중인 점검 · 끝난 점검 화면. */
export const DASH_MAX = 3;
/** 발견 목록은 처음에 위험한 순으로 6개만, 나머지는 '모두 보기' (시안 v88). */
export const FIND_MAX = 6;

/** 위험도 칸. 서버가 심각도를 안 줬거나 모르는 값이면 none(등급 없음). */
type SevF = SevKey | "none";
const SEV_RANK: readonly SevF[] = ["crit", "hi", "mid", "lo", "info", "none"];
const FIND_CHIPS: ReadonlyArray<Chip<SevF>> = [
  ["all", "전체", true, "저장된 발견 모두 (정상 대조 결과 제외)"],
  ["crit", SEVL.crit, false, "심각도 심각"],
  ["hi", SEVL.hi, false, "심각도 높음"],
  ["mid", SEVL.mid, false, "심각도 중간"],
  ["lo", SEVL.lo, false, "심각도 낮음"],
  ["info", SEVL.info, false, "정보성 심각도"],
  ["none", "위험도 미평가", false, "서버가 심각도를 주지 않은 발견"],
];
const sevOf = (e: RemediationEntry): SevF => sevKey(entrySeverity(e) ?? "") ?? "none";
/** 위험도 막대 색 (시안 v77 요약 칸). 등급 없음은 옅은 선색. */

/**
 * 대시보드 맨 위 요약 4칸 (시안 strip). 서버에 진단 목록 · 30일 집계가 없어 이 브라우저가 이어 보는 점검 하나의
 * 실제 값만 센다. 모르는 값은 "—". 시안의 토큰 30일 집계 · 점검당 평균은 서버 집계가 없어 칸만 두고
 * '연결 전' 딱지를 붙이며, 이 점검의 서버 기록(model_usage)은 보조 줄에 보인다.
 */
/** 맨 위 요약 칸: 숫자만(사용자 11/24 — 분포는 그 아래 원형 게이지 줄이 보인다) */
function DashStrip({
  found,
  ready,
  approvals,
  selecting,
  tokens,
}: {
  /** 정상 대조를 뺀 발견 (결과 저장 전에는 ready=false) */
  found: readonly RemediationEntry[];
  ready: boolean;
  /** 승인 대기 수 (모르면 null) */
  approvals: number | null;
  /** 점검할 경로 선택 대기 중인가 */
  selecting: boolean;
  /** 이 점검의 모델 토큰 (서버 기록이 없으면 null) */
  tokens: { input: number; output: number } | null;
}) {
  const byOutcome = (o: string) => found.filter((e) => diagnosticOutcome(e) === o).length;
  const bySev = (k: SevF) => found.filter((e) => diagnosticOutcome(e) === "confirmed" && sevOf(e) === k).length;
  const sevSub = SEV_RANK.filter((k) => k !== "none" || bySev(k) > 0)
    .map((k) => `확정 ${k === "none" ? "위험도 미평가" : SEVL[k]} ${bySev(k)}`)
    .join(" · ");
  const estimates = SEV_RANK.filter((k) => k !== "none")
    .map((k) => {
      const count = found.filter((e) => diagnosticOutcome(e) === "review" && sevOf(e) === k).length;
      return count ? `검토 필요 ${SEVL[k]} ${count}` : "";
    })
    .filter(Boolean)
    .join(" · ");
  const decide = approvals === null ? null : approvals + (selecting ? 1 : 0);
  // 시안 결정 대기 보조 글: 남은 것만 ('승인 1 · 경로 선택 1'), 없으면 '모두 결정함'
  const decideSub = [approvals ? `승인 ${approvals}` : null, selecting ? "경로 선택 1" : null]
    .filter(Boolean)
    .join(" · ");
  const check = byOutcome("unknown") + byOutcome("not_executed");
  const tokText = (n: number) => {
    const f = fmtTokens(n);
    return `${f.n}${f.unit}`;
  };
  return (
    <section className={s.strip} aria-label="요약">
      {/* 빨간 숫자는 확정이 있을 때만 (검토 필요 · 미실행 · 결과 불명은 취약점이 아님 · 리뷰 #467) */}
      <div className={cx(s.cell, ready && byOutcome("confirmed") > 0 && s.crit)}>
        <div className={s.k}>발견 · 검토 대상</div>
        <div className={s.v} id="dSevN">
          {ready ? found.length : "—"}
        </div>
        <div className={s.sub} id="dSevSub">
          {/* 위험도 계약: 확정 분포와 예상(검토 필요) 분포를 섞지 않고 따로 적는다 */}
          {ready ? [sevSub, estimates].filter(Boolean).join(" · ") : "결과가 저장되면 셉니다"}
        </div>
      </div>
      <div className={cx(s.cell, !!decide && s.warn)} id="dDecide">
        <div className={s.k}>결정 대기</div>
        <div className={s.v} id="dDecideN">
          {decide ?? "—"}
        </div>
        <div className={s.sub} id="dDecideSub">
          {decide === null ? "승인 대기를 확인하는 중" : decideSub || "모두 결정함"}
        </div>
      </div>
      <div className={s.cell}>
        <div className={s.k}>결과 확인</div>
        <div className={s.v}>{ready ? check : "—"}</div>
        <div className={s.sub}>
          {ready
            ? `결과 불명 ${byOutcome("unknown")} · 미실행 ${byOutcome("not_executed")}`
            : "판단 불가 · 미검사 영역"}
        </div>
      </div>
      <div className={s.cell}>
        <div className={s.k}>토큰 사용량 · 최근 30일</div>
        <div className={s.v}>
          {/* 시안 큰 수는 30일 집계라 서버 값이 없다: '—' 와 딱지만 두고, 이 점검의 서버 기록은 아래 줄에 */}—{" "}
          <NotYet why="서버에 30일 토큰 집계가 없습니다" />
        </div>
        <div className={s.sub}>
          {tokens ? `이 점검 · 입력 ${tokText(tokens.input)} · 출력 ${tokText(tokens.output)}` : "점검당 평균 —"}
        </div>
      </div>
    </section>
  );
}

/** 원 색(위험도). 정보성 · 미평가는 '없음'에 가까워 회색 */
const SEV_RING: Readonly<Record<SevF, string>> = {
  crit: "var(--ring-crit)",
  hi: "var(--ring-hi)",
  mid: "var(--ring-mid)",
  lo: "var(--ring-lo)",
  info: "var(--ring-gray)",
  none: "var(--ring-gray-2)",
};
/** 진행 중 점검의 전송 통제 수를 다시 읽는 간격(ms). 끝난 점검은 한 번만 */
const E2_RING_POLL_MS = 10_000;

/**
 * 요약 숫자 칸 아래 원형 게이지 4개(사용자 11/24, dashRings.tsx 설명). 이어 보는 점검 하나의 값이다. 서버 값이 없으면 '—'와
 * 이유를 두고 지어내지 않는다. 발견 수는 결과가 저장된 뒤에만(보고서와 같은 원장), 검토 필요는 확정이 아니다(규칙 5).
 */
function DashRingRow({
  found,
  ready,
  explore,
  e2,
  e2Note,
  approvals,
  selecting,
}: {
  /** 정상 대조를 뺀 발견 (결과 저장 전에는 ready=false) */
  found: readonly RemediationEntry[];
  ready: boolean;
  /** 탐색 흐름 수(흐름이 없거나 아직 못 읽었으면 null) */
  explore: { found: number; visited: number } | null;
  /** E② 전송 수(못 읽었으면 null) */
  e2: E2Counts | null;
  /** e2 가 없을 때 이유 */
  e2Note: string;
  /** 승인 대기 수 (모르면 null) */
  approvals: number | null;
  /** 점검할 경로 선택 대기 중인가 */
  selecting: boolean;
}) {
  const by = (o: string) => found.filter((e) => diagnosticOutcome(e) === o).length;
  const total = found.length;
  // ① 위험도: 확정된 것만 센다(diagnostic-candidate-severity 계약: 확정 분포엔 확정만). 후보의 예상 등급은 이 원에 넣지 않고
  // '예상'이라는 말도 두지 않는다(사용자 11/24: 너무 별로). 후보(검토 필요) 수는 옆 '판정' 원에 있다
  const sevSegs: RingSeg[] = SEV_RANK.map((k) => ({
    key: k,
    label: k === "none" ? "위험도 미평가" : SEVL[k],
    value: found.filter((e) => diagnosticOutcome(e) === "confirmed" && sevOf(e) === k).length,
    color: SEV_RING[k],
  }));
  // 범례: 심각 · 높음 · 중간 · 낮음은 0 이어도 늘 둔다(사용자 11/24), 정보성 · 미평가는 있을 때만
  const always: readonly string[] = ["crit", "hi", "mid", "lo"];
  const sevRows = sevSegs.filter((g) => always.includes(g.key) || g.value > 0);
  const sumOf = (segs: readonly RingSeg[]) => segs.reduce((n, g) => n + g.value, 0);
  // ② 판정
  const confirmed = by("confirmed");
  const review = by("review");
  const undecided = by("unknown") + by("not_executed");
  const verdictSegs: RingSeg[] = [
    { key: "confirmed", label: "확정", value: confirmed, color: "var(--ring-crit)" },
    { key: "review", label: "검토 필요", value: review, color: "var(--ring-lo)" },
    { key: "undecided", label: "판단 불가", value: undecided, color: "var(--ring-gray)" },
  ];
  // ③ 탐색
  const visitPct = explore && explore.found > 0 ? Math.round((explore.visited / explore.found) * 100) : null;
  // ④ 전송 통제: 서버는 결정(허용 · 거부 · 미지원)과 사유(결과 불명 · 송신 실패)를 따로 센다 — 보내다 결과를 모르게 된 시도는
  // 허용이면서 결과 불명이다(server/first_diagnosis_attempts.go). 조각이 겹치지 않게 허용은 '시도 − 막음 − 결과 불명 · 실패'
  // (응답까지 받은 허용)로 나눠 합이 시도 수와 같게 한다(#917 리뷰 P2). 막음 + 결과 불명 · 실패가 시도보다 많으면 수가 맞지
  // 않는 기록이라 원을 그리지 않는다
  const blocked = e2 ? e2.denied + e2.unsupported : 0;
  const unsure = e2 ? e2.unknown + e2.transient : 0;
  const e2Mismatch = !!e2 && blocked + unsure > e2.attempts;
  const allowedOk = e2 && !e2Mismatch ? e2.attempts - blocked - unsure : 0;
  const e2Segs: RingSeg[] =
    e2 && !e2Mismatch
      ? [
          { key: "allowed", label: "허용", value: allowedOk, color: "var(--ring-allow)" },
          { key: "blocked", label: "막음", value: blocked, color: "var(--ring-block)" },
          { key: "unsure", label: "결과 불명 · 실패", value: unsure, color: "var(--ring-gray)" },
        ]
      : [];
  const waiting = [approvals ? `승인 대기 ${approvals}` : null, selecting ? "경로 선택 대기" : null].filter(Boolean);
  const dash = "—";
  let sevNote = "결과가 저장되면 셉니다";
  if (ready && total) sevNote = "확정된 취약점의 위험도";
  else if (ready) sevNote = "저장된 결과에 검토 대상이 없습니다(취약점이 없다는 뜻은 아닙니다)";
  let e2Text = e2 ? "E② 가 받은 진단 요청(탐색 요청은 제외)" : e2Note;
  if (e2Mismatch) e2Text = "전송 기록의 수가 맞지 않아 나누어 보이지 않습니다";
  return (
    <section className={s.drings} aria-label="분포">
      <RingCard
        title="위험도"
        ring={
          <Ring
            segs={sevSegs}
            total={ready ? sumOf(sevSegs) : 0}
            center={ready ? sumOf(sevSegs) : dash}
            caption="확정"
            label={
              ready
                ? `확정 ${sumOf(sevSegs)}건${sevRows.map((g) => ` · ${g.label} ${g.value}`).join("")}`
                : "결과 저장 전"
            }
          />
        }
        legend={sevRows.map((g) => ({ key: g.key, label: g.label, value: g.value, color: g.color }))}
        note={sevNote}
      />
      <RingCard
        title="판정"
        ring={
          <Ring
            segs={verdictSegs}
            total={ready ? total : 0}
            center={ready ? `${confirmed}/${total}` : dash}
            caption="확정"
            label={ready ? `확정 ${confirmed} · 검토 필요 ${review} · 판단 불가 ${undecided}` : "결과 저장 전"}
          />
        }
        legend={ready ? verdictSegs.map((g) => ({ key: g.key, label: g.label, value: g.value, color: g.color })) : []}
        note={ready ? "판단 불가 = 결과 불명 + 미실행" : "결과가 저장되면 셉니다"}
      />
      <RingCard
        title="탐색"
        ring={
          <Ring
            segs={
              explore ? [{ key: "visited", label: "방문", value: explore.visited, color: "var(--ring-allow)" }] : []
            }
            total={explore ? explore.found : 0}
            center={visitPct === null ? dash : `${visitPct}%`}
            caption="방문"
            label={explore ? `발견한 API ${explore.found} 중 방문 ${explore.visited}` : "탐색 기록 없음"}
          />
        }
        legend={
          explore
            ? [
                { key: "found", label: "발견한 API", value: explore.found },
                { key: "visited", label: "방문", value: explore.visited, color: "var(--ring-allow)" },
              ]
            : []
        }
        note={explore ? "탐색이 찾은 API 중 직접 방문한 비율" : "탐색 흐름이 있는 점검에서 셉니다"}
      />
      <RingCard
        title="전송 통제"
        warn={waiting.length > 0}
        ring={
          <Ring
            segs={e2Segs}
            total={e2 && !e2Mismatch ? e2.attempts : 0}
            center={e2 ? e2.attempts : dash}
            caption="진단 요청"
            label={
              e2 && !e2Mismatch
                ? `진단 요청 ${e2.attempts} · 허용 ${allowedOk} · 막음 ${blocked} · 결과 불명 · 실패 ${unsure}`
                : e2Text
            }
          />
        }
        legend={e2Segs.map((g) => ({ key: g.key, label: g.label, value: g.value, color: g.color }))}
        note={waiting.length ? <span className={s.warn}>{waiting.join(" · ")}</span> : e2Text}
      />
    </section>
  );
}

/** 저장된 결과 → 발견 목록: 정상 대조를 빼고 위험도 → 판정 순. */
export function dashFindings(entries: readonly RemediationEntry[]): RemediationEntry[] {
  return entries
    .filter((e) => diagnosticOutcome(e) !== "negative")
    .sort(
      (x, y) =>
        SEV_RANK.indexOf(sevOf(x)) - SEV_RANK.indexOf(sevOf(y)) ||
        ORDER.indexOf(diagnosticOutcome(x)) - ORDER.indexOf(diagnosticOutcome(y)),
    );
}

/**
 * live 대시보드(2026-11-26). 보기 둘: 전체 = 최근 점검을 모은 통합 대시보드(DashOverview), 점검별 = 고른 점검 하나 기준의
 * 대시보드(UrlDashboardSingle).
 */
/**
 * 대시보드 보기(사용자 11/26): 전체(모든 점검을 모은 통합 대시보드) · 점검별(고른 점검 하나 — 예전 대시보드).
 * 마지막으로 고른 보기는 이 브라우저에 기억한다(못 읽으면 전체).
 */
type DashMode = "all" | "run";
const DASH_MODE_KEY = "abc2lab-dash-mode";
const DASH_MODES: readonly [DashMode, string][] = [
  ["all", "전체"],
  ["run", "점검별"],
];

/** 기억한 보기. 읽기 전(null)에는 본문을 그리지 않는다 — 전체가 잠깐 그려졌다 바뀌며 깜빡이고 모든 점검 결과를 읽지 않게 */
function useDashMode(): [DashMode | null, (m: DashMode) => void] {
  const [mode, setMode] = useState<DashMode | null>(null);
  useEffect(() => {
    let saved: DashMode = "all";
    try {
      if (localStorage.getItem(DASH_MODE_KEY) === "run") saved = "run";
    } catch {
      // 저장소를 못 쓰면 전체로 연다
    }
    setMode(saved);
  }, []);
  const change = (m: DashMode) => {
    setMode(m);
    try {
      localStorage.setItem(DASH_MODE_KEY, m);
    } catch {
      // 기억하지 못해도 지금 화면은 바뀐다
    }
  };
  return [mode, change];
}

/** 점검 고르기 줄 하나: 번호 · 주소 · 상태 · 시각 */
function RunOption({ row, owner }: { row: URLRunSummary; owner: string | null }) {
  const info = useRunInput(row.id, owner);
  const when = row.finished_at ?? row.started_at;
  return (
    <option value={row.id}>
      {[
        `${row.task_id ? `${row.task_id}. ` : ""}${info ? info.host : maskSecrets(row.display_name?.trim() || row.id)}`,
        urlStateText(row.state),
        when ? dateTimeOf(when).slice(5, 16).replace("-", "/") : null,
      ]
        .filter(Boolean)
        .join(" · ")}
    </option>
  );
}

/** 점검별 보기에서 볼 점검 고르기(왼쪽 목록에서 고른 것과 같다 — 고르면 이어 보는 점검이 바뀐다) */
function DashRunPicker() {
  const page = useUrlRuns((st) => st.page);
  const owner = useUrlRuns((st) => st.owner);
  const hidden = useHiddenRunIDs(owner);
  const current = useUrl((st) => st.handle?.id ?? null);
  const rows = (page?.runs ?? []).filter((r) => !(endedURLRun(r) && hidden.includes(r.id)));
  if (!rows.length) return null;
  return (
    <select
      className={cx(s.inp, s["dash-pick"])}
      aria-label="볼 점검"
      value={current ?? ""}
      onChange={(e) => {
        const row = rows.find((r) => r.id === e.target.value);
        if (row) void selectUrlRun(row);
      }}
    >
      {current ? null : (
        <option value="" disabled>
          점검 고르기
        </option>
      )}
      {current && !rows.some((r) => r.id === current) ? <option value={current}>지금 이어 보는 점검</option> : null}
      {rows.map((r) => (
        <RunOption key={r.id} row={r} owner={owner} />
      ))}
    </select>
  );
}

/**
 * 점검 정보 '보낸 요청' 칸에 넘길 e2-flow. 재조회가 한 번 실패해도 같은 점검에서 마지막으로 받은 값은 그대로 넘긴다 — usePoll 은
 * 오류에도 data 를 지우지 않고, 점검(task)이 바뀌면 비운다. '읽기 실패'는 받은 값이 하나도 없을 때만이다(#976 이동찬 P2).
 */
export function dashSentFlow(
  e2Task: string | null,
  e2: Pick<Poll<E2Flow>, "data" | "error">,
): { e2Flow: E2Flow | null; e2FlowFailed: boolean } {
  if (!e2Task) return { e2Flow: null, e2FlowFailed: false };
  return { e2Flow: e2.data ?? null, e2FlowFailed: !!e2.error && !e2.data };
}

export function UrlDashboardLive() {
  const [mode, setMode] = useDashMode();
  const tabs = (
    <span className={s["dash-mode"]}>
      <span className={s.seg} role="radiogroup" aria-label="대시보드 보기">
        {DASH_MODES.map(([id, label]) => (
          <label key={id}>
            <input type="radio" name="dashMode" checked={mode === id} onChange={() => setMode(id)} />
            <span>{label}</span>
          </label>
        ))}
      </span>
      {mode === "run" ? <DashRunPicker /> : null}
    </span>
  );
  if (mode === null) return <UrlHead title="대시보드" crumb="대시보드" />;
  return mode === "run" ? <UrlDashboardSingle tabs={tabs} /> : <DashOverview tabs={tabs} />;
}

/** 점검별 대시보드(이 브라우저가 이어 보는 점검 하나 기준, 예전 대시보드). */
export function UrlDashboardSingle({ tabs }: { tabs?: ReactNode }) {
  const { route, openInsp } = useShell();
  const handle = useUrl((st) => st.handle);
  const page = useUrlRuns((st) => st.page);
  const rows = page?.runs ?? [];
  const listReady = useUrlRuns((st) => !!st.page && !st.error);
  const hiddenRuns = useHiddenRunIDs(useUrlRuns((st) => st.owner));
  const job = useUrl((st) => st.job);
  const verified = useUrl((st) => st.verified);
  const flow = useUrl((st) => st.flow);
  const g = useUrlGates();
  const visible = route === "dashboard";
  const ended = !!job && URL_ENDED_STATES.includes(job.state);
  const taskId = job?.task_id ?? "";
  const approvalsOn = !!job?.task_id && job.input.workflow_mode === "user_approval_v1" && !ended;
  const a = useRequestApprovals(taskId, visible && approvalsOn);
  // 발견: 결과가 저장된 진단만 (보고서와 같은 원장 · 같은 조건)
  const reportTask = g.reportReadable && job?.task_id ? job.task_id : null;
  const rem = usePoll(() => fetchRemediation(reportTask as string, "internal"), [reportTask], {
    enabled: visible && !!reportTask,
  });
  const remFor = !rem.error && reportTask && rem.data && String(rem.data.task_id) === reportTask ? rem.data : null;
  const found = remFor ? dashFindings(remFor.entries) : [];
  const result = remFor && job ? jobResult(job, remFor.entries) : null;
  // 전송 통제 원: E② 전송 기록 수(끝난 점검도 읽힌다). 시작한 점검만, 진행 중이면 쉬었다가 다시 읽는다
  const e2Task = job?.started && job.task_id ? job.task_id : null;
  const e2 = usePoll((signal) => getE2Flow(e2Task, undefined, signal), [e2Task], {
    enabled: visible && !!e2Task,
    intervalMs: ended ? 0 : E2_RING_POLL_MS,
  });
  let e2Note = "점검이 시작되면 셉니다";
  if (e2Task && e2.error) e2Note = "전송 기록을 읽지 못했습니다";
  // 저널 검증 실패면 서버가 수를 전부 0으로 보낸다 — '아무것도 안 보냄'으로 그리지 않는다(#917 리뷰 P2, 규칙 5)
  else if (e2Task && e2.data?.journal === "invalid") e2Note = "전송 기록 검증 실패";
  else if (e2Task) e2Note = "전송 기록을 읽는 중";
  const [ff, setFf] = useState<SevF | "all">("all");
  const [findAll, setFindAll] = useState(false);
  const shown = ff === "all" ? found : found.filter((e) => sevOf(e) === ff);
  const [picked, setSel] = useState<Sel | null>(null);
  // 고르기 전에는 가장 위험한 발견(시안 처음 선택), 없으면 승인 대기 요청(보내기 전에 사람이 정해야 함), 없으면 점검 정보
  const first = a.pending[0];
  let sel: Sel = { kind: "job" };
  // 다시 연 점검(조회 결속)도 같은 순서로 고른다 — 승인 대기가 먼저 보여야 운영자가 놓치지 않는다(사용자 2026-11-22, F16)
  if (picked) sel = picked;
  else if (found[0]) sel = { kind: "finding", id: found[0].entry_id };
  else if (first) sel = { kind: "approval", id: first.id };

  // 다른 항목을 고르면 오른쪽 패널을 맨 위로(사용자 11/24)
  const inspRef = useScrollTopOnChange<HTMLElement>(sel.kind === "job" ? "job" : `${sel.kind}:${sel.id}`);
  const host = handle ? urlHost(handle.input.target_url) : null;
  // 보낼 요청 승인 대기 줄: 사용자 승인 모드가 아니면 없음([]), 아직 못 읽었으면 모름(null)
  const pending = pendingRows(approvalsOn, a);
  const coverage = useCoveragePct(reportTask, visible);

  const pick = (next: Sel) => {
    setSel(next);
    openInsp();
  };
  // 위험도 칩: 바꾸면 처음 6개부터. 보던 발견이 빠지면 남은 첫 줄을 고른다
  const pickSev = (k: SevF | "all") => {
    setFf(k);
    setFindAll(false);
    const rest = k === "all" ? found : found.filter((e) => sevOf(e) === k);
    if (sel.kind === "finding" && rest[0] && !rest.some((e) => e.entry_id === sel.id))
      setSel({ kind: "finding", id: rest[0].entry_id });
  };

  // 발견 목록 본문 (결과가 저장되기 전에는 "없음"이라고 하지 않는다)
  let findBody: ReactNode;
  if (!handle) findBody = <p className={s.empty}>이 브라우저에서 이어 보는 점검이 없습니다.</p>;
  else if (!reportTask)
    findBody = (
      <p className={s.empty}>
        {`점검이 끝나고 결과가 저장되면 여기에 나옵니다 · 지금 ${job ? urlStateText(job.state, verified) : "상태 확인 중"}`}
      </p>
    );
  else if (!remFor && rem.error) findBody = <ReportReadError error={rem.error} onRetry={rem.reload} />;
  else if (!remFor) findBody = <LoadingRow />;
  else if (!found.length)
    findBody = <p className={s.empty}>저장된 발견이 없습니다. 결과가 없다고 안전하다는 뜻은 아닙니다.</p>;
  else
    findBody = (
      <div className={s.list} id="dTodo">
        {(findAll ? shown : shown.slice(0, FIND_MAX)).map((e) => {
          const on = sel.kind === "finding" && sel.id === e.entry_id;
          return (
            <button
              key={e.entry_id}
              className={cx(s.frow, on && s.sel)}
              type="button"
              data-dsel={`fd-${e.entry_id}`}
              aria-current={on ? "true" : undefined}
              onClick={() => pick({ kind: "finding", id: e.entry_id })}
            >
              <Sev e={e} />
              <span className={s.fm}>
                <span className={s.ft}>{maskSecrets(e.title) || e.entry_id}</span>
                <span className={s.fe}>
                  <span className={s.host}>{host}</span>
                  <EntryEp e={e} />
                  {e.vuln_class ? <span>{maskSecrets(e.vuln_class)}</span> : null}
                </span>
              </span>
              <span className={s.fs}>
                <Outcome o={diagnosticOutcome(e)} short />
              </span>
            </button>
          );
        })}
      </div>
    );

  const actRows = rows.filter(activeURLRun);
  const endedRows = rows.filter(endedURLRun);
  // 화면에서 숨긴 끝난 점검(live/hiddenRuns)은 대시보드 목록에서도 뺀다. 서버 기록은 그대로다
  const doneRows = endedRows.filter((row) => !hiddenRuns.includes(row.id));
  const otherCount = rows.length - actRows.length - endedRows.length;

  // 오른쪽 상세
  const chosen: RequestApproval | undefined =
    sel.kind === "approval" ? a.pending.find((r) => r.id === sel.id) : undefined;
  const chosenF = sel.kind === "finding" ? found.find((e) => e.entry_id === sel.id) : undefined;
  let insp: ReactNode;
  if (chosenF && reportTask) {
    insp = (
      <>
        <FindingDetail e={chosenF} host={host} taskId={reportTask} onReload={rem.reload} />
        <Isec>
          <div className={s.acts}>
            <a className={cx(s.btn, s.pri)} href="#report">
              보고서 열기
              <Icon name="arrow" />
            </a>
          </div>
        </Isec>
      </>
    );
  } else if (chosen) {
    insp = (
      <>
        <InspHead
          eyebrow={
            <>
              승인 요청 · <span className={s.host}>{host}</span>
            </>
          }
          title={`${chosen.method} ${chosen.path}`}
          chips={
            <>
              <span className={cx(s.pill, s.wait)}>승인 대기</span>
              {WRITE.has(chosen.method) ? <span className={cx(s.tag, s.ap)}>쓰기 메서드</span> : null}
            </>
          }
        />
        <Isec t="보낼 요청">
          <ApprovalRequestCode row={chosen} />
        </Isec>
        <Isec t="승인 정보">
          <ApprovalFacts row={chosen} />
        </Isec>
        <Isec>
          <ApprovalActions a={a} row={chosen} />
        </Isec>
      </>
    );
  } else if (handle) {
    insp = (
      <JobSummary
        approvals={pending}
        onApprovals={first ? () => pick({ kind: "approval", id: first.id }) : undefined}
        result={result}
        coverage={coverage}
        {...dashSentFlow(e2Task, e2)}
      />
    );
  } else {
    insp = (
      <>
        <InspHead title="점검 정보" />
        <Isec>
          <p className={s.empty}>이 브라우저에서 이어 보는 점검이 없습니다.</p>
          <UnfollowedActiveRuns />
          <a className={cx(s.btn, s.pri)} href="#new">
            <Icon name="plus" />새 점검
          </a>
        </Isec>
      </>
    );
  }

  return (
    <>
      <UrlHead
        title="대시보드"
        crumb="대시보드"
        pill={tabs}
        // [새로고침]은 뺐다(사용자 11/24) — 점검 목록 · 지금 점검 상태 · 승인 요청 · 탐색 흐름이 알아서 다시 읽힌다(UrlFeeds)
        acts={<InspOpenButton>상세</InspOpenButton>}
      />
      <div className={s.vb}>
        <div className={s.vm}>
          <RunListStatus />
          <SelectedRunStatus />
          {/* 머리 [새로고침]이 목록까지 다시 읽어 [목록 새로 조회]는 두지 않는다(사용자 11/24) */}
          <RunListControls refresh={false} />
          <DashStrip
            found={found}
            ready={!!remFor}
            approvals={pending ? pending.length : null}
            selecting={!!job && flow?.stage === "awaiting_selection"}
            tokens={
              job?.model_usage?.usage_known
                ? { input: job.model_usage.input_tokens, output: job.model_usage.output_tokens }
                : null
            }
          />
          <DashRingRow
            found={found}
            ready={!!remFor}
            explore={
              flow && job?.input.workflow_mode ? { found: flow.counts.found, visited: flow.counts.visited } : null
            }
            e2={e2Task && !e2.error && e2.data?.journal === "verified" ? e2.data.counts : null}
            e2Note={e2Note}
            approvals={pending ? pending.length : null}
            selecting={!!job && flow?.stage === "awaiting_selection"}
          />
          <section className={s.sec} aria-labelledby="dTodoH">
            <div className={s.sh}>
              <div className={s["sh-t"]}>
                <h2 id="dTodoH">발견 · 검토 대상</h2>
                {remFor ? (
                  <span className={s.n} id="dTodoN">
                    {`${shown.length}건 · 확정 ${shown.filter((e) => diagnosticOutcome(e) === "confirmed").length}`}
                  </span>
                ) : null}
              </div>
              {shown.length > FIND_MAX ? (
                <button
                  className={s.aside}
                  type="button"
                  id="dFindMore"
                  aria-expanded={findAll}
                  onClick={() => setFindAll((v) => !v)}
                >
                  {findAll ? "접기" : "모두 보기"}
                </button>
              ) : null}
            </div>
            {remFor ? (
              <Chips
                label="위험도로 거르기"
                chips={FIND_CHIPS}
                value={ff}
                count={(k) => (k === "all" ? found.length : found.filter((e) => sevOf(e) === k).length)}
                onPick={pickSev}
              />
            ) : null}
            {findBody}
          </section>
          <section className={s.sec} aria-labelledby="dActH">
            <div className={s.sh}>
              <div className={s["sh-t"]}>
                <h2 id="dActH">진행 중인 점검</h2>
                <span className={s.n} id="dActN">
                  {listReady ? `${actRows.length}건` : "—"}
                </span>
              </div>
              <a className={s.aside} href="#running">
                전체 보기
              </a>
            </div>
            {actRows.length ? (
              <div className={s.list} id="dActive">
                {actRows.slice(0, DASH_MAX).map((row) => (
                  <RunRow key={row.id} row={row} />
                ))}
              </div>
            ) : (
              <RunListStatus empty="진행 중인 점검이 없습니다." />
            )}
          </section>
          {otherCount > 0 ? (
            <p className={s.help}>
              시작 전·상태 확인이 필요한 점검 {otherCount}건은 <a href="#running">진행·시작 전 점검 목록</a>에서
              확인하세요.
            </p>
          ) : null}
          <section className={s.sec} aria-labelledby="dDoneH">
            <div className={s.sh}>
              <div className={s["sh-t"]}>
                <h2 id="dDoneH">보존된 끝난 점검</h2>
                <span className={s.n} id="dDoneN">
                  {listReady ? `${doneRows.length}건` : "—"}
                </span>
              </div>
              <a className={s.aside} href="#finished">
                전체 보기
              </a>
            </div>
            {doneRows.length ? (
              <div className={s.list} id="dDone">
                {doneRows.slice(0, DASH_MAX).map((row) => (
                  <RunRow key={row.id} row={row} />
                ))}
              </div>
            ) : (
              <RunListStatus empty="끝난 점검이 없습니다." />
            )}
          </section>
        </div>
        <aside className={s.vi} id="dInsp" ref={inspRef} aria-label="선택한 항목 상세">
          {insp}
        </aside>
        <PaneToggle />
      </div>
    </>
  );
}
