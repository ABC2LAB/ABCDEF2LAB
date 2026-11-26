"use client";

// 진행 중인 점검 · 끝난 점검 (시안 v54): 점검 기록을 두 화면으로 나눴다. 레일에서 진행 중 점검을 누르면 진행 중인 점검,
// 끝난 점검을 누르면 끝난 점검 화면으로 온다. 화면마다 목록 위에 필터 칩 · 건수, 오른쪽에 고른 점검의 상세.
// 서버에 보존된 본인 회차를 페이지로 읽고, 선택한 Job의 최신 상태를 다시 검증한다.
// 필터는 서버 값으로만 나눈다: 진행 중은 진단 상태 · 탐색 단계 · 요청 승인 대기, 끝난 점검은 팀 lib 의 판정 분류(보고서 저장 뒤).
// 끝난 점검의 결과 묶음은 실행 마감(completed)된 점검만 나눈다 — 중지 · 실패는 보고서가 있어도 끝까지 보지 않았으므로 나누지 않는다.
// '발견 없음' 은 근거가 있을 때만: 정상 대조 결과가 있고 나머지 판정이 0 일 때. 판정 기록이 없으면 발견 없음이 아니다(절대규칙 5).
// 어느 묶음에도 들지 않는 값(상태 미확인 · 결과 불명 · 중지 · 실패 · 판정 기록 없음 · 보고서 없음)은 '전체' 에만 보인다.

import { type ReactNode, useState } from "react";

import { activeURLRun, endedURLRun, type URLRunSummary } from "@/lib/diagnosis/diagnosis-runs";

import { cx } from "../../lib/cx";
import { fetchRemediation } from "../../live/api";
import { setShowingHiddenRuns, useHiddenRunIDs, useHiddenRuns } from "../../live/hiddenRuns";
import { useUrl, useUrlGates, useUrlSelectionKey } from "../../live/urlDiagnosis";
import { useUrlRuns } from "../../live/urlRunList";
import { usePoll } from "../../live/usePoll";
import { useShell } from "../../shell";
import { Icon } from "../../ui/icons";
import { InspHead, InspOpenButton, Isec, PaneToggle } from "../../ui/parts";
import s from "../../workbench.module.css";
import { useRequestApprovals } from "./approvals";
import { type DoneKind, JobSummary, jobResult, pendingRows, type RunKind, useCoveragePct } from "./jobSummary";
import { ReportReadError, RunListControls, RunListStatus, RunRow, SelectedRunStatus } from "./runRows";
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
  ["review", "검토 필요", false, "사람이 검토할 취약점이 있는 점검"],
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

/** 목록 행의 저장 상태로만 필터링한다. 판정 집계는 선택한 Job의 보고서를 실제로 읽은 뒤에만 보인다. */
const RUN_STATES: ReadonlyArray<Chip<string>> = [
  ["all", "전체", true, "활성·시작 전·결과 불명 회차"],
  ["preparing", "진단 준비", true, "진단 준비 중인 회차"],
  ["running", "엔진 실행", true, "저장된 상태가 엔진 실행인 회차"],
  ["stopping", "중지·마감", false, "중지와 마감을 처리 중인 회차"],
  ["prestart", "시작 전", true, "승인 대기 또는 실행 전 검증 완료"],
  ["unknown", "상태 확인 필요", true, "실행 또는 결과가 불명인 회차"],
];
const DONE_STATES: ReadonlyArray<Chip<string>> = [
  ["all", "전체", true, "보존된 마감·중지·실패 회차"],
  ["completed", "점검 완료", true, "서버가 실행 마감을 기록한 회차 (일부 완료 · 검사 안 함 포함)"],
  ["cancelled", "중지됨", true, "중지한 회차, 부분 보고서가 있을 수 있음"],
  ["failed", "진단 실패", true, "실패한 회차, 부분 보고서가 있을 수 있음"],
];

function NoPick({ title }: { title: string }) {
  return (
    <>
      <InspHead title={title} />
      <Isec>
        <SelectedRunStatus />
        <p className={s.empty}>목록에서 점검을 선택해 현재 상태와 기존 기록을 다시 여세요.</p>
        <a className={cx(s.btn, s.pri)} href="#new">
          <Icon name="plus" />새 점검
        </a>
      </Isec>
    </>
  );
}

// 점검을 고르면 상태를 다시 읽는 동안 상세가 비어 '목록에서 점검을 선택…' 안내가 잠깐 떴다 사라졌다(사용자 11/23 깜빡임).
// 읽는 중에는 고른 줄의 이름만 두고 그 안내를 그리지 않는다
function PickLoading({ title, name }: { title: string; name: string }) {
  return (
    <>
      <InspHead title={name || title} />
      <Isec>
        <p className={s.empty} role="status">
          불러오는 중…
        </p>
      </Isec>
    </>
  );
}

function RecordList({ finished }: { finished: boolean }) {
  const { route, goTo } = useShell();
  const title = finished ? "끝난 점검" : "진행 중인 점검";
  const visible = route === (finished ? "finished" : "running");
  const page = useUrlRuns((st) => st.page);
  const owner = useUrlRuns((st) => st.owner);
  const hidden = useHiddenRunIDs(finished ? owner : null);
  const listed = (page?.runs ?? []).filter((row) => endedURLRun(row) === finished);
  // 점검을 고르면 이 화면이 다시 그려진다(byMode key) — 보는 목록은 화면 밖 상태로 지킨다(사용자 11/23)
  const showHidden = useHiddenRuns((st) => st.showing) && finished;
  // 화면에서 삭제한 끝난 점검(live/hiddenRuns)은 기본 목록에서 빼고 '삭제한 점검'에서만 보인다. 서버 기록은 그대로다
  const hiddenCount = finished ? listed.filter((row) => hidden.includes(row.id)).length : 0;
  const rows = finished ? listed.filter((row) => hidden.includes(row.id) === showHidden) : listed;
  const [filter, setFilter] = useState("all");
  const matches = (row: URLRunSummary, key: string) =>
    key === "all" || (key === "prestart" ? ["ready", "awaiting_approval"].includes(row.state) : row.state === key);
  const shown = rows.filter((row) => matches(row, filter));
  const selected = useUrl((st) => st.selectedID);
  const job = useUrl((st) => st.job);
  const handle = useUrl((st) => st.handle);
  const identity = useUrlSelectionKey();
  const gates = useUrlGates();
  const task = gates.reportReadable && job?.task_id ? job.task_id : null;
  const rem = usePoll(
    async () => ({ identity, report: await fetchRemediation(task as string, "internal") }),
    [task, identity],
    { enabled: visible && !!task },
  );
  const report =
    !rem.error && task && rem.data?.identity === identity && String(rem.data.report.task_id) === task
      ? rem.data.report
      : null;
  const result = report && job ? jobResult(job, report.entries) : null;
  const coverage = useCoveragePct(task, visible);
  const approvalsOn =
    !finished && !!job?.task_id && job.input.workflow_mode === "user_approval_v1" && gates.runReadable;
  const approvals = useRequestApprovals(job?.task_id ?? "", visible && approvalsOn);
  const selectedHere = !!handle && !!job && shown.some((row) => row.id === selected);
  const picking = useUrl((st) => st.busy) && !selectedHere ? shown.find((row) => row.id === selected) : undefined;
  let detail: ReactNode = <NoPick title={title} />;
  if (selectedHere)
    detail = (
      <JobSummary
        result={result}
        coverage={coverage}
        approvals={pendingRows(approvalsOn, approvals)}
        onApprovals={() => goTo("dashboard")}
      />
    );
  else if (picking) detail = <PickLoading title={title} name={picking.display_name?.trim() || picking.id} />;

  let emptyText = finished ? "이 상태의 끝난 점검이 없습니다." : "이 상태의 점검이 없습니다.";
  if (showHidden) emptyText = "삭제한 점검이 없습니다.";
  return (
    <>
      <UrlHead title={title} crumb={title} acts={<InspOpenButton>상세</InspOpenButton>} />
      <div className={s.vb}>
        <div className={s.vm}>
          <section className={s.sec} aria-label={finished ? "끝난 점검 목록" : "진행 중인 점검 목록"}>
            <div className={cx(s.sh, s["rec-sh"])}>
              <Chips
                label="상태로 거르기"
                chips={finished ? DONE_STATES : RUN_STATES}
                value={filter}
                count={(key) => (page ? rows.filter((row) => matches(row, key)).length : null)}
                onPick={setFilter}
              />
              {/* 건수 바로 옆에 '삭제한 점검' 보기(사용자 11/23). 삭제한 점검이 있을 때만 보인다 */}
              <span className={s["rec-end"]}>
                <span className={s.aside}>{page ? `${shown.length} / ${rows.length}건` : "—"}</span>
                {finished && (hiddenCount > 0 || showHidden) ? (
                  <button
                    className={s.aside}
                    type="button"
                    title="화면에서 삭제한 끝난 점검(기록은 남아 있음)"
                    aria-pressed={showHidden}
                    onClick={() => {
                      setShowingHiddenRuns(!showHidden);
                      setFilter("all");
                    }}
                  >
                    {`삭제한 점검 ${hiddenCount}`}
                  </button>
                ) : null}
              </span>
            </div>
            <RunListControls />
            <RunListStatus empty={shown.length ? undefined : emptyText} />
            <div className={s.list} id={finished ? "doneList" : "runList"}>
              {shown.map((row) => (
                <RunRow key={row.id} row={row} />
              ))}
            </div>
            {shown.some(activeURLRun) ? (
              <p className={s.help}>목록의 승인 표시는 실행 허가를 대신하지 않습니다.</p>
            ) : null}
            {selectedHere && rem.error ? <ReportReadError error={rem.error} onRetry={rem.reload} /> : null}
          </section>
        </div>
        <aside className={s.vi} id={finished ? "doneInsp" : "runInsp"} aria-label="선택한 점검">
          {detail}
        </aside>
        <PaneToggle />
      </div>
    </>
  );
}
export function UrlRunningLive() {
  return <RecordList finished={false} />;
}
export function UrlFinishedLive() {
  return <RecordList finished />;
}
