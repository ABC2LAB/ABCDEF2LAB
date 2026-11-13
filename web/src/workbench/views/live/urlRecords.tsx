"use client";

// 진행 중인 점검 · 끝난 점검 (시안 v54): 점검 기록을 두 화면으로 나눴다. 레일에서 진행 중 점검을 누르면 진행 중인 점검,
// 끝난 점검을 누르면 끝난 점검 화면으로 온다. 화면마다 목록 위에 필터 칩 · 건수, 오른쪽에 고른 점검의 상세.
// 서버에 진단 목록 API 가 없어 줄은 이 브라우저가 이어 보는 진단 하나뿐이다(레일과 같은 줄 · 목록을 만들어 채우지 않는다).
// 필터는 서버 값으로만 나눈다: 진행 중은 진단 상태 · 탐색 단계 · 요청 승인 대기, 끝난 점검은 팀 lib 의 판정 분류(보고서 저장 뒤).
// 끝난 점검의 결과 묶음은 실행 마감(completed)된 점검만 나눈다 — 중지 · 실패는 보고서가 있어도 끝까지 보지 않았으므로 나누지 않는다.
// '발견 없음' 은 근거가 있을 때만: 정상 대조 결과가 있고 나머지 판정이 0 일 때. 판정 기록이 없으면 발견 없음이 아니다(절대규칙 5).
// 어느 묶음에도 들지 않는 값(상태 미확인 · 결과 불명 · 중지 · 실패 · 판정 기록 없음 · 보고서 없음)은 '전체' 에만 보인다.

import { useState } from "react";

import type { URLDiagnosisJob } from "@/lib/diagnosis/diagnosis-url";

import { cx } from "../../lib/cx";
import { fetchRemediation } from "../../live/api";
import { useUrl, useUrlGates } from "../../live/urlDiagnosis";
import { URL_ENDED_STATES, type UrlState } from "../../live/urlMap";
import { usePoll } from "../../live/usePoll";
import { useShell } from "../../shell";
import { Icon } from "../../ui/icons";
import { InspHead, InspOpenButton, Isec, PaneToggle } from "../../ui/parts";
import s from "../../workbench.module.css";
import { useRequestApprovals } from "./approvals";
import {
  type DoneKind,
  JobRow,
  JobSummary,
  jobResult,
  pendingRows,
  type RunKind,
  recRunKind,
  useCoveragePct,
} from "./jobSummary";
import { UrlHead } from "./urlParts";

export { type DoneKind, type RunKind, recDoneKinds, recRunKind } from "./jobSummary";

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

/** 오른쪽 상세가 비었을 때 (시안 NO_PICK): 필터에 가려졌으면 '고른 점검 없음', 아예 없으면 빈 안내 + 새 점검. */
function NoPick({ title, has, none }: { title: string; has: boolean; none: string }) {
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

/** 건수 (시안 runN · doneN): 전체면 'n건', 필터 중이면 '보이는 수 / 전체 n건'. */
const ofText = (shown: boolean, all: boolean, f: string) => {
  const n = (b: boolean) => (b ? 1 : 0);
  return f === "all" ? `${n(all)}건` : `${n(shown)} / ${n(all)}건`;
};

/** live 진행 중인 점검 (URL 진단 서버). 필터는 진단 상태 · 탐색 단계 · 요청 승인 대기. */
export function UrlRunningLive() {
  const { route, navSeq, goTo, openInsp } = useShell();
  const visible = route === "running";
  const handle = useUrl((st) => st.handle);
  const job = useUrl((st) => st.job);
  const verified = useUrl((st) => st.verified);
  const flow = useUrl((st) => st.flow);
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

  const runShown = runOk(runF);
  // 시안 머리에는 새로고침이 없다: 진행 중 진단은 UrlFeeds 가 주기적으로 다시 읽고, 결과 불명이면 상세의 '상태 다시 조회'
  return (
    <>
      <UrlHead title="진행 중인 점검" crumb="진행 중인 점검" acts={<InspOpenButton>상세</InspOpenButton>} />
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
              <span className={s.aside}>{ofText(runShown, hasRun, runF)}</span>
            </div>
            {runShown ? (
              <div className={s.list} id="runList">
                <JobRow sel onPick={openInsp} pending={a.pending.length} />
              </div>
            ) : (
              <p className={s["rec-none"]}>{runEmptyText(hasRun, runF)}</p>
            )}
          </section>
        </div>
        <aside className={s.vi} id="runInsp" aria-label="선택한 점검">
          {runShown ? (
            <JobSummary approvals={pendingRows(approvalsOn, a)} onApprovals={() => goTo("dashboard")} />
          ) : (
            <NoPick title="진행 중인 점검" has={hasRun} none="진행 중인 점검이 없습니다." />
          )}
        </aside>
        <PaneToggle />
      </div>
    </>
  );
}

/** 진행 중 칸이 비었을 때의 글 (시안: 승인 대기 칩이면 승인 문구). */
function runEmptyText(hasRun: boolean, f: RunKind | "all"): string {
  if (!hasRun) return "진행 중인 점검이 없습니다.";
  return f === "wait" ? "승인을 기다리는 점검이 없습니다." : "이 상태의 점검이 없습니다.";
}

/**
 * live 끝난 점검 (URL 진단 서버). 필터는 팀 lib 의 판정 분류(보고서 저장 뒤 · 실행 마감된 점검만).
 * 어느 묶음에도 들지 않는 점검은 '전체' 에만 보이고 그 까닭을 목록 아래에 적는다.
 */
export function UrlFinishedLive() {
  const { route, navSeq, openInsp } = useShell();
  const visible = route === "finished";
  const handle = useUrl((st) => st.handle);
  const job = useUrl((st) => st.job);
  const g = useUrlGates();
  const ended = !!job && URL_ENDED_STATES.includes(job.state);
  const reportId = g.reportReadable && job?.task_id ? job.task_id : null;
  const rem = usePoll(() => fetchRemediation(reportId as string, "internal"), [reportId], {
    enabled: visible && !!reportId,
  });
  const coverage = useCoveragePct(reportId, visible);
  const [doneF, setDoneF] = useState<DoneKind | "all">("all");

  const remFor = rem.data && reportId && String(rem.data.task_id) === reportId ? rem.data : null;
  const result = remFor && job ? jobResult(job, remFor.entries) : null;
  const doneKinds = ended && result ? result.kinds : [];
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

  const doneCount = (k: DoneKind | "all"): number | null => {
    if (k !== "all" && doneUnknown) return null;
    return doneOk(k) ? 1 : 0;
  };
  const doneShown = doneOk(doneF);
  // 시안 머리에는 새로고침이 없다: 보고서를 못 읽었으면 목록 아래 안내의 '다시 읽기' 로 다시 부른다
  return (
    <>
      <UrlHead title="끝난 점검" crumb="끝난 점검" acts={<InspOpenButton>상세</InspOpenButton>} />
      <div className={s.vb}>
        <div className={s.vm}>
          <section className={s.sec} aria-label="끝난 점검 목록">
            <div className={cx(s.sh, s["rec-sh"])}>
              <Chips label="결과로 거르기" chips={DONE_CHIPS} value={doneF} count={doneCount} onPick={pickDone} />
              <span className={s.aside}>{ofText(doneShown, hasDone, doneF)}</span>
            </div>
            {doneShown ? (
              <div className={s.list} id="doneList">
                <JobRow sel onPick={openInsp} pending={0} result={result} coverage={coverage} />
              </div>
            ) : (
              <p className={s["rec-none"]}>{doneEmptyText(hasDone, doneLoading, remFailed)}</p>
            )}
            {hasDone && !doneLoading && !doneKinds.length ? (
              <p className={s.help} role="note">
                {unsortedText(job?.state, !!result, remFailed, job?.outcome)}
                {remFailed ? (
                  <>
                    {" "}
                    <button className={cx(s.btn, s.sm)} type="button" onClick={rem.reload}>
                      다시 읽기
                    </button>
                  </>
                ) : null}
              </p>
            ) : null}
          </section>
        </div>
        <aside className={s.vi} id="doneInsp" aria-label="선택한 점검">
          {doneShown ? (
            <JobSummary result={result} coverage={coverage} />
          ) : (
            <NoPick title="끝난 점검" has={hasDone} none="끝난 점검이 없습니다." />
          )}
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
  if (failed) return "보고서를 읽지 못해 결과로 나누지 않고 '전체' 에만 보입니다.";
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
