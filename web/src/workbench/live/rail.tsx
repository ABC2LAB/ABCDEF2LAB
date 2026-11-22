"use client";

// 레일의 실제 값 조각: 결정 대기 배지 · 이 브라우저가 이어 보는 점검 줄(진행 중 · 끝난 점검 묶음) · 계정 메뉴의 분석 모델 · 진단 모드.
// 서버에 진단 목록 API 가 없어 점검 줄은 이어 보는 진단 하나뿐이다(전체 목록·집계를 만들어 채우지 않는다).

import { type ReactNode, useEffect, useState } from "react";

import { activeURLRun, endedURLRun } from "@/lib/diagnosis/diagnosis-runs";
import { urlPhaseLabels } from "@/lib/diagnosis/diagnosis-url";
import { readRequestApprovalState } from "@/lib/diagnosis/request-approvals";

import { useShell, type View } from "../shell";
import { Icon } from "../ui/icons";
import s from "../workbench.module.css";
import { useUrl } from "./urlDiagnosis";
import { scanViewFor, URL_ENDED_STATES, URL_PRESTART_STATES, urlHost, urlStateText } from "./urlMap";
import { useUrlRuns } from "./urlRunList";
import { usePoll } from "./usePoll";
import { flowStageText } from "./workflow";

/** 점검 하나를 보는 화면들 (레일 줄의 현재 표시 계산용). */
const SCAN_VIEWS: readonly View[] = ["crawl", "scope", "live", "report"];

/** 대시보드 링크의 배지: 현재 회차의 HTTP 요청 승인 대기 수. 미지원·0 이면 숨김. */
export function LiveRailBadge() {
  const job = useUrl((st) => st.job);
  const flow = useUrl((st) => st.flow);
  const taskId = job?.task_id;
  const enabled = !!taskId && job?.input.workflow_mode === "user_approval_v1" && !URL_ENDED_STATES.includes(job.state);
  const state = usePoll(() => readRequestApprovalState(taskId as string), [taskId], {
    enabled,
    intervalMs: enabled ? 2000 : 0,
  });
  const matches = enabled && state.data && flow && state.data.run.run_ref === flow.run_ref && !state.error;
  const count = matches ? (state.data?.approvals ?? []).filter((a) => a.state === "pending").length : 0;
  return count > 0 ? (
    <span className={s.badge} title="결정 대기" role="status" aria-label={`결정 대기 ${count}건`}>
      {count}
    </span>
  ) : null;
}

/** 레일 줄 아래 글: 탐색 단계가 있으면 그 단계, 없으면 서버 상태 · 단계. */
function useRowSub(): string {
  const job = useUrl((st) => st.job);
  const flow = useUrl((st) => st.flow);
  const verified = useUrl((st) => st.verified);
  const busy = useUrl((st) => st.busy);
  if (!job) return busy ? "상태 확인 중" : "상태 미확인";
  if (!verified) return urlStateText(job.state, false);
  if (flow && !URL_ENDED_STATES.includes(job.state))
    return `${flowStageText[flow.stage]}${flow.paused ? " · 일시정지" : ""}`;
  if (URL_PRESTART_STATES.includes(job.state) || URL_ENDED_STATES.includes(job.state)) return urlStateText(job.state);
  return `${urlStateText(job.state)} · ${urlPhaseLabels[job.phase]}`;
}

/** 레일 묶음 하나: 머리를 누르면 아래 목록을 접고 펼친다(이 브라우저에 기억). 접어도 건수는 보인다. */
function RailSection({
  id,
  title,
  count,
  children,
}: {
  id: string;
  title: string;
  count: number;
  children: ReactNode;
}) {
  const key = `abc2lab-rsec-${id}`;
  const [open, setOpen] = useState(true);
  useEffect(() => {
    try {
      if (localStorage.getItem(key) === "0") setOpen(false);
    } catch {
      // 저장소를 못 쓰면 펼친 채로
    }
  }, [key]);
  const toggle = () => {
    const next = !open;
    setOpen(next);
    try {
      localStorage.setItem(key, next ? "1" : "0");
    } catch {
      // 이번만 적용
    }
  };
  const listId = `rsec-${id}`;
  return (
    <div className={s.rsec}>
      <button className={s.rh} type="button" aria-expanded={open} aria-controls={listId} onClick={toggle}>
        <Icon name="chev" />
        {title}
        <span className={s.c} role="img" aria-label={`${count}건`}>
          {count}
        </span>
      </button>
      <div className={s.rlist} id={listId} hidden={!open}>
        {children}
      </div>
    </div>
  );
}

/**
 * 진행 중 / 끝난 점검. 이 브라우저가 이어 보는 진단 한 줄을 알맞은 묶음에 둔다(결과 불명은 끝난 것으로 두지 않는다).
 * 줄을 누르면 진행 중이면 진행 중인 점검, 끝났으면 끝난 점검 화면으로 간다 (시안 v54).
 * 그 목록 화면이나 이 점검의 화면을 보고 있으면 현재 표시를 단다.
 */
export function LiveRailTasks() {
  const { route, goTo } = useShell();
  const handle = useUrl((st) => st.handle);
  const job = useUrl((st) => st.job);
  const stage = useUrl((st) => st.flow?.stage ?? null);
  const sub = useRowSub();
  const ended = !!job && URL_ENDED_STATES.includes(job.state);
  const to: View = job ? scanViewFor(job, stage) : "live";
  const list: View = ended ? "finished" : "running";
  const here = route === list || (to === "new" ? route === "new" : SCAN_VIEWS.includes(route));
  const row = handle ? (
    <button
      className={s.rs}
      type="button"
      data-url-job={job?.id ?? ""}
      aria-current={here ? "true" : undefined}
      onClick={() => goTo(list)}
    >
      <span className={s.mk} aria-hidden="true" />
      <span className={s.host}>{urlHost(handle.input.target_url)}</span>
      <span className={s.rsub}>{sub}</span>
    </button>
  ) : null;
  const running = !!row && !ended;
  const done = !!row && ended;
  // 이 브라우저가 이어 보는 점검이 없어도 서버에 진행 중 점검이 있으면 그 수를 세고 진행 목록으로 안내한다(사용자 2026-11-22,
  // 1차 리허설 F14: 브라우저를 다시 띄운 뒤 "진행 중 0"으로 보여 승인 대기를 놓칠 수 있었다)
  const serverActive = useUrlRuns((st) => (st.page?.runs ?? []).filter(activeURLRun).length);
  const unfollowed = !handle && serverActive > 0;
  let runCount = 0;
  let runBody: ReactNode = <div className={s["lv-rempty"]}>진행 중인 점검 없음</div>;
  if (running) {
    runCount = 1;
    runBody = row;
  } else if (unfollowed) {
    runCount = serverActive;
    runBody = (
      <button className={s.rs} type="button" onClick={() => goTo("running")}>
        <span className={s.mk} aria-hidden="true" />
        <span className={s.host}>{`이어 보지 않는 진행 중 점검 ${serverActive}건`}</span>
        <span className={s.rsub}>진행 중인 점검 목록에서 열기</span>
      </button>
    );
  }
  // 끝난 점검도 같다: 이어 보는 점검이 없으면 서버 끝난 점검 수를 세고 끝난 점검 목록으로 안내한다(2026-11-22 화면 수정 확인
  // F14b: 브라우저를 다시 띄운 뒤 서버에 끝난 점검이 있어도 "끝난 점검 없음"이었다. 목록 화면 판정과 같은 endedURLRun)
  const serverEnded = useUrlRuns((st) => (st.page?.runs ?? []).filter(endedURLRun).length);
  let doneCount = 0;
  let doneBody: ReactNode = <div className={s["lv-rempty"]}>끝난 점검 없음</div>;
  if (done) {
    doneCount = 1;
    doneBody = row;
  } else if (!handle && serverEnded > 0) {
    doneCount = serverEnded;
    doneBody = (
      <button className={s.rs} type="button" onClick={() => goTo("finished")}>
        <span className={s.mk} aria-hidden="true" />
        <span className={s.host}>{`이어 보지 않는 끝난 점검 ${serverEnded}건`}</span>
        <span className={s.rsub}>끝난 점검 목록에서 열기</span>
      </button>
    );
  }
  return (
    <>
      <RailSection id="run" title="진행 중" count={runCount}>
        {runBody}
      </RailSection>
      <RailSection id="done" title="끝난 점검" count={doneCount}>
        {doneBody}
      </RailSection>
    </>
  );
}

/** 계정 메뉴의 분석 모델 · 진단 모드 줄 (이어 보는 진단의 설치 프로필 기준). */
export function LiveAccountModel() {
  const cap = useUrl((st) => st.cap);
  const input = useUrl((st) => st.job?.input ?? st.handle?.input ?? null);
  const profile = cap.kind === "v2" && input ? cap.catalog.profiles.find((p) => p.id === input.profile_id) : undefined;
  const approval = input?.workflow_mode === "user_approval_v1";
  return (
    <>
      <div className={s["am-info"]} id="railModel">
        <Icon name="chip" />
        <div>
          {profile ? `분석 모델 · ${profile.model?.external_transfer ? "외부" : "로컬"}` : "분석 모델"}
          <b>{profile ? profile.model_label : "점검마다 설치 프로필"}</b>
        </div>
      </div>
      {input?.workflow_mode ? (
        <div className={s["am-info"]}>
          <Icon name="shield" />
          <div>
            {approval ? "사용자 승인 모드" : "AI 자동 모드"}
            <span>{approval ? "상태 변경은 승인 후 전송" : "대상이 쓰기를 승인했으면 건별 확인 없이 전송"}</span>
          </div>
        </div>
      ) : null}
    </>
  );
}
