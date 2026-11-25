"use client";

// 레일의 실제 값 조각: 결정 대기 배지 · 이 브라우저가 이어 보는 점검 줄(진행 중 · 끝난 점검 묶음) · 계정 메뉴의 분석 모델 · 진단 모드.
// 서버에 진단 목록 API 가 없어 점검 줄은 이어 보는 진단 하나뿐이다(전체 목록·집계를 만들어 채우지 않는다).

import { Fragment, type ReactNode, useEffect, useState } from "react";

import { activeURLRun, endedURLRun, type URLRunSummary } from "@/lib/diagnosis/diagnosis-runs";
import { urlPhaseLabels } from "@/lib/diagnosis/diagnosis-url";
import { readRequestApprovalState } from "@/lib/diagnosis/request-approvals";

import { cx } from "../lib/cx";
import { useShell, type View } from "../shell";
import { Icon } from "../ui/icons";
import s from "../workbench.module.css";
import { useHiddenRunIDs } from "./hiddenRuns";
import { clockOf } from "./map";
import { maskSecrets } from "./mask";
import { runModeText, useRunInput } from "./runInput";
import { selectUrlRun, useUrl } from "./urlDiagnosis";
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
  if (URL_ENDED_STATES.includes(job.state))
    return [
      urlStateText(job.state),
      runModeText(job.input.workflow_mode),
      job.started_at ? clockOf(job.started_at).slice(0, 5) : null,
    ]
      .filter(Boolean)
      .join(" · ");
  if (URL_PRESTART_STATES.includes(job.state)) return urlStateText(job.state);
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

const NO_RUNS: URLRunSummary[] = [];
/** 레일 한 묶음에 줄을 몇 개까지 그리는가. 넘으면 마지막에 '전체 n건'(그 목록 화면으로) */
const RAIL_MAX_ROWS = 6;

/** 서버 목록의 점검 한 줄: 주소(못 읽으면 이름) · 상태 · 진단 모드 · 시작 시각. 누르면 그 점검을 열고 그 목록 화면으로(이어 보는 줄과 같다) */
function ServerRunRow({ run, list }: { run: URLRunSummary; list: View }) {
  const { goTo } = useShell();
  const owner = useUrlRuns((st) => st.owner);
  const writeBusy = useUrl((st) => st.writeBusy);
  const info = useRunInput(run.id, owner);
  const name = run.display_name?.trim();
  let title = run.task_id ? `작업 ${run.task_id}` : "점검";
  if (info) title = info.host;
  else if (name) title = maskSecrets(name);
  // 같은 주소 점검을 구분하게 진단 모드 · 시작 시각을 붙인다(사용자 11/24)
  const sub = [
    urlStateText(run.state),
    info?.mode,
    run.started_at ? clockOf(run.started_at).slice(0, 5) : null,
    // 끝난 점검의 승인 만료엔 '확인 필요'를 붙이지 않는다(승인 만료 뒤 서버가 끝난 점검까지 켠다, 사용자 11/24). 철회 ·
    // 변경 · 불명은 끝난 점검에도 붙인다 — '점검 완료'로만 보이면 정상 성공처럼 읽힌다(product-run-list 계약 화면 3, #917 리뷰 P2)
    run.action_required && !(endedURLRun(run) && run.approval_status === "expired") ? "확인 필요" : null,
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <button
      className={s.rs}
      type="button"
      data-url-run={run.id}
      disabled={writeBusy}
      onClick={() =>
        void selectUrlRun(run).then((ok) => {
          if (ok) goTo(list);
        })
      }
    >
      <span className={s.mk} aria-hidden="true" />
      <span className={s.host}>{title}</span>
      <span className={s.rsub}>{sub}</span>
    </button>
  );
}

/** 묶음 안 줄들. RAIL_MAX_ROWS 를 넘으면 앞 줄만 두고 마지막에 '전체 n건'(그 목록 화면으로) */
function RailRows({ items, list }: { items: ReactNode[]; list: View }) {
  const { goTo } = useShell();
  if (items.length <= RAIL_MAX_ROWS) return <>{items}</>;
  return (
    <>
      {items.slice(0, RAIL_MAX_ROWS - 1)}
      <button className={cx(s.rs, s["rs-more"])} type="button" onClick={() => goTo(list)}>
        <span className={s.mk} aria-hidden="true" />
        <span className={s.host}>{`전체 ${items.length}건`}</span>
      </button>
    </>
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
  // 대시보드도 이 점검을 오른쪽에 보이므로 현재 표시(빨간 점)를 단다(사용자 11/24: 대시보드 목록에서 눌러도 레일에 안 떴다)
  const here = route === list || route === "dashboard" || (to === "new" ? route === "new" : SCAN_VIEWS.includes(route));
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
  // 서버 점검 목록의 점검도 줄마다 주소 · 상태 한 줄로 보인다(사용자 11/24: '이어 보지 않는 … n건 · 목록에서 열기' 같은 말
  // 없이). 이 브라우저가 이어 보는 점검은 그 줄(위 row, 실시간 상태)로 바꿔 끼운다. 진행 중은 서버의 진행 중 점검(브라우저를
  // 다시 띄워도 승인 대기를 놓치지 않게 — 1차 리허설 F14), 끝난 점검은 화면에서 숨긴 것(live/hiddenRuns)을 뺀 것
  const runOwner = useUrlRuns((st) => st.owner);
  const hiddenRuns = useHiddenRunIDs(runOwner);
  const serverRuns = useUrlRuns((st) => st.page?.runs ?? NO_RUNS);
  const followedID = handle?.id ?? job?.id ?? null;
  const section = (pick: (r: URLRunSummary) => boolean, mine: boolean) => {
    const rows = serverRuns.filter((r) => pick(r) && !hiddenRuns.includes(r.id));
    const items: ReactNode[] = rows.map((r) =>
      row && r.id === followedID ? (
        // 이어 보는 줄은 서버 목록에 들어오기 전후로 같은 열쇠를 써서 다시 만들지 않는다(누르던 줄이 바뀌지 않게)
        <Fragment key="followed">{row}</Fragment>
      ) : (
        <ServerRunRow key={r.id} run={r} list={endedURLRun(r) ? "finished" : "running"} />
      ),
    );
    // 이어 보는 점검이 아직 서버 목록에 없으면(막 만든 초안 · 목록 갱신 전) 맨 위에 둔다
    if (mine && row && !rows.some((r) => r.id === followedID)) items.unshift(<Fragment key="followed">{row}</Fragment>);
    return items;
  };
  const runItems = section(activeURLRun, !!row && !ended);
  const doneItems = section(endedURLRun, !!row && ended);
  const runCount = runItems.length;
  const doneCount = doneItems.length;
  const runBody: ReactNode = runCount ? (
    <RailRows items={runItems} list="running" />
  ) : (
    <div className={s["lv-rempty"]}>진행 중인 점검 없음</div>
  );
  const doneBody: ReactNode = doneCount ? (
    <RailRows items={doneItems} list="finished" />
  ) : (
    <div className={s["lv-rempty"]}>끝난 점검 없음</div>
  );
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
