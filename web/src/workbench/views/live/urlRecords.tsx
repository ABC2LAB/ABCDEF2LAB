"use client";

// 진행 중인 점검 · 끝난 점검 (시안 v54): 점검 기록을 두 화면으로 나눴다. 레일에서 진행 중 점검을 누르면 진행 중인 점검,
// 끝난 점검을 누르면 끝난 점검 화면으로 온다. 화면마다 목록 위에 필터 칩 · 건수, 오른쪽에 고른 점검의 상세.
// 서버에 진단 목록 API 가 없어 줄은 이 브라우저가 이어 보는 진단 하나뿐이다(레일과 같은 줄 · 목록을 만들어 채우지 않는다).
// 필터는 서버 값으로만 나눈다: 진행 중은 진단 상태 · 탐색 단계 · 요청 승인 대기, 끝난 점검은 팀 lib 의 판정 분류(보고서 저장 뒤).
// 끝난 점검의 결과 묶음은 실행 마감(completed)된 점검만 나눈다 — 중지 · 실패는 보고서가 있어도 끝까지 보지 않았으므로 나누지 않는다.
// '발견 없음' 은 근거가 있을 때만: 정상 대조 결과가 있고 나머지 판정이 0 일 때. 판정 기록이 없으면 발견 없음이 아니다(절대규칙 5).
// 어느 묶음에도 들지 않는 값(상태 미확인 · 결과 불명 · 중지 · 실패 · 판정 기록 없음 · 보고서 없음)은 '전체' 에만 보인다.

import { type ReactNode, useState } from "react";

import type { URLDiagnosisJob } from "@/lib/diagnosis/diagnosis-url";
import type { DiagnosticOutcome } from "@/lib/diagnosis/diagnostic-results";
import { diagnosticOutcomeLabels, summarizeDiagnosticEntries } from "@/lib/diagnosis/diagnostic-results";

import { cx } from "../../lib/cx";
import { fetchRemediation } from "../../live/api";
import { clockOf } from "../../live/map";
import { NotYet } from "../../live/parts";
import { refreshWorkflow, runUrlAction, useUrl, useUrlGates } from "../../live/urlDiagnosis";
import { URL_ACTIVE_STATES, URL_ENDED_STATES, type UrlState, urlHost } from "../../live/urlMap";
import { usePoll } from "../../live/usePoll";
import { flowStageText } from "../../live/workflow";
import { useShell } from "../../shell";
import { Icon } from "../../ui/icons";
import { InspHead, InspOpenButton, Isec, PaneToggle } from "../../ui/parts";
import s from "../../workbench.module.css";
import { useRequestApprovals } from "./approvals";
import { JobSummary } from "./jobSummary";
import { UrlHead, UrlPill } from "./urlParts";

export type RunKind = "run" | "wait" | "scope";
export type DoneKind = "hit" | "review" | "clean" | "und";
type FlowStage = "crawling" | "awaiting_selection" | "diagnosing" | "completed" | "stopped" | "failed" | "unknown";

/**
 * 진행 중 점검의 상태 묶음. 승인 대기(시작 전 범위 승인 · 요청 승인 대기) → 범위 확인 대기(탐색 완료 · 경로 선택 대기)
 * → 점검 중(엔진이 움직이는 상태). 상태를 확인하지 못했거나 어느 것도 아니면 null.
 */
export function recRunKind(
  state: UrlState,
  verified: boolean,
  stage: FlowStage | null,
  pending: number,
): RunKind | null {
  if (!verified) return null;
  if (state === "awaiting_approval" || pending > 0) return "wait";
  if (stage === "awaiting_selection") return "scope";
  return URL_ACTIVE_STATES.includes(state) ? "run" : null;
}

/**
 * 끝난 점검의 결과 묶음 (팀 lib 판정 집계 그대로). 실행 마감(completed)된 점검만 나눈다.
 * 확정 → 발견 있음, 검토 필요 → 검토 필요, 미실행 · 결과 불명 → 판단 불가,
 * 정상 대조 결과가 있고 그 넷이 0 이며 실행 결과가 complete(계획한 작업을 모두 마침) → 발견 없음.
 * partial(끝나지 않은 · 중지된 작업이 있음)은 정상 대조 결과만 있어도 발견 없음으로 두지 않는다(절대규칙 5, #440).
 * 보고서를 못 읽었거나 판정 기록이 없으면 빈 목록('전체' 에만).
 */
export function recDoneKinds(
  state: UrlState,
  counts: Record<DiagnosticOutcome, number> | null,
  outcome?: URLDiagnosisJob["outcome"],
): DoneKind[] {
  if (state !== "completed" || !counts) return [];
  const out: DoneKind[] = [];
  if (counts.confirmed > 0) out.push("hit");
  if (counts.review > 0) out.push("review");
  if (counts.not_executed + counts.unknown > 0) out.push("und");
  if (!out.length && counts.negative > 0 && outcome === "complete") out.push("clean");
  return out;
}

/** 필터 칩: [값, 글, 늘 보이는가(아니면 해당 점검이 있을 때만), 뜻]. */
export type Chip<K extends string> = readonly [K | "all", string, boolean, string];
export const RUN_CHIPS: ReadonlyArray<Chip<RunKind>> = [
  ["all", "전체", true, "진행 중인 점검 모두"],
  ["run", "점검 중", true, "엔진이 진단을 진행하는 점검"],
  ["wait", "승인 대기", true, "시작 전 범위 승인 또는 보낼 요청의 승인을 기다리는 점검"],
  ["scope", "범위 확인 대기", false, "탐색을 마치고 점검할 경로 선택을 기다리는 점검"],
];
export const DONE_CHIPS: ReadonlyArray<Chip<DoneKind>> = [
  ["all", "전체", true, "끝난 점검 모두"],
  ["hit", "발견 있음", true, "확정 발견이 있는 점검"],
  ["review", "검토 필요", false, "사람이 검토할 후보가 있는 점검"],
  [
    "clean",
    "발견 없음",
    true,
    "계획한 작업을 모두 마쳤고 정상 대조 결과만 있는 점검(확정 · 검토 필요 · 미실행 · 결과 불명 0건) · 발견이 없다고 안전하다는 뜻은 아닙니다",
  ],
  ["und", "판단 불가", true, "미실행 또는 결과 불명이 있는 점검"],
];

/** 칩 줄. count 가 null 이면 아직 모름(보고서를 읽는 중) → '—'. */
export function Chips<K extends string>({
  label,
  chips,
  value,
  count,
  onPick,
}: {
  label: string;
  chips: ReadonlyArray<Chip<K>>;
  value: K | "all";
  count: (k: K | "all") => number | null;
  onPick: (k: K | "all") => void;
}) {
  return (
    // biome-ignore lint/a11y/useSemanticElements: 칩 묶음 (fieldset 으로 바꾸면 제목 옆 한 줄 모양이 깨짐)
    <div className={s.filters} role="group" aria-label={label}>
      {chips.map(([k, text, always, title]) => {
        const c = count(k);
        if (!always && !c && value !== k) return null;
        return (
          <button
            key={k}
            className={s.fchip}
            type="button"
            title={title}
            aria-pressed={value === k}
            onClick={() => onPick(k)}
          >
            {text}
            <span className={s.c}>{c ?? "—"}</span>
          </button>
        );
      })}
    </div>
  );
}

/** 이어 보는 진단 한 줄 (두 화면이 같이 쓴다). 누르면 오른쪽 상세(좁은 화면은 서랍). */
function JobRow() {
  const { openInsp } = useShell();
  const handle = useUrl((st) => st.handle);
  const job = useUrl((st) => st.job);
  const verified = useUrl((st) => st.verified);
  const flow = useUrl((st) => st.flow);
  if (!handle) return null;
  const ended = !!job && URL_ENDED_STATES.includes(job.state);
  const name = handle.input.display_name?.trim() || null;
  return (
    <button
      className={cx(s.row, s.sel)}
      type="button"
      data-url-job={job?.id ?? ""}
      aria-current="true"
      onClick={openInsp}
    >
      <span className={s["r-main"]}>
        <span className={s["r-t"]}>
          <span className={s["r-host"]}>{urlHost(handle.input.target_url)}</span>
        </span>
        <span className={s["r-s"]}>
          {[name, flow && !ended ? flowStageText[flow.stage] : null, job?.task_id ? `task #${job.task_id}` : null]
            .filter(Boolean)
            .join(" · ") || "상태 확인 중"}
        </span>
        {/* 끝난 점검 줄의 소요 · 커버리지 (시안 v55): 서버가 목록 줄에 주지 않아 칸만 둔다(커버리지는 보고서에서) */}
        {ended ? (
          <span className={s["r-s"]}>
            {"소요 "}
            <NotYet why="서버가 점검 소요 시간을 주지 않습니다" />
            {" · 자산 커버리지 "}
            <NotYet why="점검 목록에는 자산 커버리지가 없습니다. 보고서에서 볼 수 있습니다" />
          </span>
        ) : null}
      </span>
      <span className={s["r-side"]}>
        {job ? <UrlPill job={job} verified={verified} /> : <span className={cx(s.pill, s.wait)}>상태 미확인</span>}
      </span>
    </button>
  );
}

/** 오른쪽 상세: 목록에 보이는 점검이 있으면 그 점검, 필터에 가려졌으면 '고른 점검 없음', 아예 없으면 빈 안내 + 새 점검. */
function RecordInsp({
  title,
  shown,
  has,
  none,
  rows,
  acts,
}: {
  title: string;
  shown: boolean;
  has: boolean;
  none: string;
  rows: Array<readonly [ReactNode, ReactNode]>;
  acts?: ReactNode;
}) {
  if (shown) return <JobSummary rows={rows} acts={acts} />;
  return (
    <>
      <InspHead title={title} />
      <Isec>
        <p className={s.empty}>{has ? "고른 점검이 없습니다." : none}</p>
        {has ? null : (
          <a className={cx(s.btn, s.pri)} href="#new">
            <Icon name="plus" />새 점검
          </a>
        )}
      </Isec>
    </>
  );
}

/** 건수: 전체면 'n건', 필터 중이면 '보이는 수 / 전체 n건'. */
const ofText = (shown: boolean, all: boolean, f: string) => {
  const n = (b: boolean) => (b ? 1 : 0);
  return f === "all" ? `${n(all)}건` : `${n(shown)} / ${n(all)}건`;
};

/** live 진행 중인 점검 (URL 진단 서버). 필터는 진단 상태 · 탐색 단계 · 요청 승인 대기. */
export function UrlRunningLive() {
  const { route, navSeq, goTo } = useShell();
  const visible = route === "running";
  const handle = useUrl((st) => st.handle);
  const job = useUrl((st) => st.job);
  const verified = useUrl((st) => st.verified);
  const flow = useUrl((st) => st.flow);
  const g = useUrlGates();
  const ended = !!job && URL_ENDED_STATES.includes(job.state);
  const taskId = job?.task_id ?? "";
  const approvalsOn = !!job?.task_id && job.input.workflow_mode === "user_approval_v1" && !ended;
  const a = useRequestApprovals(taskId, visible && approvalsOn);
  const [runF, setRunF] = useState<RunKind | "all">("all");

  const runKind = job && !ended ? recRunKind(job.state, verified, flow?.stage ?? null, a.pending.length) : null;
  const hasRun = !!handle && !ended;
  const runOk = (k: RunKind | "all") => hasRun && (k === "all" || runKind === k);

  // 레일에서 이 화면으로 오면, 이어 보는 점검이 필터에 가려져 있을 때 필터를 푼다 (렌더 중 상태 맞추기)
  const [seenNav, setSeenNav] = useState(navSeq);
  if (seenNav !== navSeq) {
    setSeenNav(navSeq);
    if (visible && hasRun && !runOk(runF)) setRunF("all");
  }

  const refresh = () => {
    if (g.canRefresh) void runUrlAction("refresh");
    void refreshWorkflow();
    a.poll.reload();
  };

  // 승인 대기는 두 종류를 나눠 보인다: 시작 전 범위 승인 · 보낼 요청의 승인(만료가 있어 가장 빠른 만료를 같이)
  const extraRows: Array<readonly [ReactNode, ReactNode]> = [];
  if (job?.state === "awaiting_approval" && verified) extraRows.push(["승인 대기", "시작 전 범위 승인"]);
  if (a.pending.length) {
    const soonest = Math.min(...a.pending.map((r) => Date.parse(r.expires_at)).filter((t) => !Number.isNaN(t)));
    const exp = Number.isFinite(soonest) ? ` · 가장 빠른 만료 ${clockOf(soonest)}` : "";
    extraRows.push(["승인 대기", `보낼 요청 ${a.pending.length}건${exp}`]);
  }
  const extraActs = a.pending.length ? (
    <button className={s.btn} type="button" onClick={() => goTo("dashboard")}>
      승인 요청 보기
    </button>
  ) : null;

  const runShown = runOk(runF);
  return (
    <>
      <UrlHead
        title="진행 중인 점검"
        crumb="진행 중인 점검"
        acts={
          <>
            <button className={s.btn} type="button" onClick={refresh}>
              <Icon name="refresh" />
              새로고침
            </button>
            <InspOpenButton>상세</InspOpenButton>
          </>
        }
      />
      <div className={s.vb}>
        <div className={s.vm}>
          <section className={s.sec} aria-label="진행 중인 점검 목록">
            <div className={cx(s.sh, s["rec-sh"])}>
              <Chips
                label="상태로 거르기"
                chips={RUN_CHIPS}
                value={runF}
                count={(k) => (runOk(k) ? 1 : 0)}
                onPick={setRunF}
              />
              <span className={s.n}>{ofText(runShown, hasRun, runF)}</span>
            </div>
            {runShown ? (
              <div className={s.list} id="recRun">
                <JobRow />
              </div>
            ) : (
              <p className={s.empty}>{hasRun ? "이 상태의 점검이 없습니다." : "진행 중인 점검이 없습니다."}</p>
            )}
          </section>
        </div>
        <aside className={s.vi} id="runInsp" aria-label="선택한 점검">
          <RecordInsp
            title="진행 중인 점검"
            shown={runShown}
            has={hasRun}
            none="진행 중인 점검이 없습니다."
            rows={extraRows}
            acts={extraActs}
          />
        </aside>
        <PaneToggle />
      </div>
    </>
  );
}

/**
 * live 끝난 점검 (URL 진단 서버). 필터는 팀 lib 의 판정 분류(보고서 저장 뒤 · 실행 마감된 점검만).
 * 어느 묶음에도 들지 않는 점검은 '전체' 에만 보이고 그 까닭을 목록 아래에 적는다.
 */
export function UrlFinishedLive() {
  const { route, navSeq } = useShell();
  const visible = route === "finished";
  const handle = useUrl((st) => st.handle);
  const job = useUrl((st) => st.job);
  const g = useUrlGates();
  const ended = !!job && URL_ENDED_STATES.includes(job.state);
  const reportId = g.reportReadable && job?.task_id ? job.task_id : null;
  const rem = usePoll(() => fetchRemediation(reportId as string, "internal"), [reportId], {
    enabled: visible && !!reportId,
  });
  const [doneF, setDoneF] = useState<DoneKind | "all">("all");

  const remFor = rem.data && reportId && String(rem.data.task_id) === reportId ? rem.data : null;
  const counts = remFor ? summarizeDiagnosticEntries(remFor.entries) : null;
  const doneKinds = ended && job ? recDoneKinds(job.state, counts, job.outcome) : [];
  // 결과를 아직 모름(읽는 중 · 실패) → 칩 개수는 '—'. 읽는 중일 때만 레일 이동의 필터 해제를 보류하고, 실패하면 보류를 푼다
  const doneUnknown = job?.state === "completed" && !!reportId && !remFor;
  const remFailed = doneUnknown && !!rem.error;
  const doneLoading = doneUnknown && !rem.error;
  const hasDone = !!handle && ended;
  const doneOk = (k: DoneKind | "all") => hasDone && (k === "all" || doneKinds.includes(k));

  // 레일에서 이 화면으로 오면, 이어 보는 점검이 필터에 가려져 있을 때 필터를 푼다 (렌더 중 상태 맞추기)
  // 보고서를 읽는 중이면 묶음을 아직 모르므로, 해제 판단을 보류했다가 보고서가 오면 한 번만 다시 판단한다.
  // 그 사이 사용자가 필터를 직접 고르면 보류를 버린다(사용자가 고른 필터를 조회마다 초기화하지 않게).
  const [seenNav, setSeenNav] = useState(navSeq);
  const [revealDone, setRevealDone] = useState(false);
  if (seenNav !== navSeq) {
    setSeenNav(navSeq);
    if (visible && hasDone) {
      if (doneLoading) setRevealDone(true);
      else if (!doneOk(doneF)) setDoneF("all");
    }
  } else if (revealDone && !doneLoading) {
    setRevealDone(false);
    if (hasDone && !doneOk(doneF)) setDoneF("all");
  }
  const pickDone = (k: DoneKind | "all") => {
    setRevealDone(false);
    setDoneF(k);
  };

  const refresh = () => {
    if (g.canRefresh) void runUrlAction("refresh");
    rem.reload();
  };

  const extraRows: Array<readonly [ReactNode, ReactNode]> = [];
  if (counts)
    extraRows.push([
      "결과",
      (["confirmed", "review", "not_executed", "unknown"] as const)
        .map((k) => `${diagnosticOutcomeLabels[k]} ${counts[k]}`)
        .join(" · "),
    ]);

  const doneCount = (k: DoneKind | "all"): number | null => {
    if (k !== "all" && doneUnknown) return null;
    return doneOk(k) ? 1 : 0;
  };
  const doneShown = doneOk(doneF);
  return (
    <>
      <UrlHead
        title="끝난 점검"
        crumb="끝난 점검"
        acts={
          <>
            <button className={s.btn} type="button" onClick={refresh}>
              <Icon name="refresh" />
              새로고침
            </button>
            <InspOpenButton>상세</InspOpenButton>
          </>
        }
      />
      <div className={s.vb}>
        <div className={s.vm}>
          <section className={s.sec} aria-label="끝난 점검 목록">
            <div className={cx(s.sh, s["rec-sh"])}>
              <Chips label="결과로 거르기" chips={DONE_CHIPS} value={doneF} count={doneCount} onPick={pickDone} />
              <span className={s.n}>{ofText(doneShown, hasDone, doneF)}</span>
            </div>
            {doneShown ? (
              <div className={s.list} id="recDone">
                <JobRow />
              </div>
            ) : (
              <p className={s.empty}>{doneEmptyText(hasDone, doneLoading, remFailed)}</p>
            )}
            {hasDone && !doneLoading && !doneKinds.length ? (
              <p className={s.help} role="note">
                {unsortedText(job?.state, !!counts, remFailed, job?.outcome)}
              </p>
            ) : null}
          </section>
        </div>
        <aside className={s.vi} id="doneInsp" aria-label="선택한 점검">
          <RecordInsp title="끝난 점검" shown={doneShown} has={hasDone} none="끝난 점검이 없습니다." rows={extraRows} />
        </aside>
        <PaneToggle />
      </div>
    </>
  );
}

/** 끝난 점검을 결과로 나누지 않은 까닭 ('전체' 에만 보일 때). */
function unsortedText(
  state: UrlState | undefined,
  readable: boolean,
  failed: boolean,
  outcome?: URLDiagnosisJob["outcome"],
): string {
  if (failed) return "보고서를 읽지 못해 결과로 나누지 않고 '전체' 에만 보입니다. 새로고침으로 다시 읽을 수 있습니다.";
  if (state !== "completed") return "중지 · 실패한 점검은 끝까지 보지 않아 결과로 나누지 않고 '전체' 에만 보입니다.";
  if (!readable) return "결과 저장이 확인되지 않아 결과로 나누지 않고 '전체' 에만 보입니다.";
  if (outcome === "partial") return "계획한 작업 일부가 끝나지 않아 '발견 없음' 으로 나누지 않고 '전체' 에만 보입니다.";
  return "정상 대조 결과가 없어 '발견 없음' 으로 나누지 않고 '전체' 에만 보입니다.";
}

/** 끝난 점검 칸이 비었을 때의 글. */
function doneEmptyText(hasDone: boolean, loading: boolean, failed: boolean): string {
  if (!hasDone) return "끝난 점검이 없습니다.";
  if (failed) return "보고서를 읽지 못해 결과를 모릅니다.";
  return loading ? "보고서를 읽는 중입니다." : "이 결과의 끝난 점검이 없습니다.";
}
