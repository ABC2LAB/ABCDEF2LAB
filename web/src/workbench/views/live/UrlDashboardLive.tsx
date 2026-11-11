"use client";

// 대시보드 (시안 짜임: 지금 처리할 일 · 진행 중인 점검 · 최근 끝난 점검 + 오른쪽 상세).
// 서버에 진단 목록 · 집계 API 가 없어 이 브라우저가 이어 보는 진단 하나만 실제 값으로 보인다.
// 지금 처리할 일은 그 진단의 실제 상태(요청 승인 대기 · 범위 선택 대기 · 결과 저장 · 결과 불명)에서만 만든다.

import { type ReactNode, useState } from "react";

import type { RequestApproval } from "@/lib/diagnosis/request-approvals";

import { cx } from "../../lib/cx";
import { clockOf } from "../../live/map";
import { disconnectUrl, refreshWorkflow, runUrlAction, useUrl, useUrlGates } from "../../live/urlDiagnosis";
import { scanViewFor, URL_ENDED_STATES, urlHost, urlStateText } from "../../live/urlMap";
import { flowStageText } from "../../live/workflow";
import { useShell } from "../../shell";
import { useWb } from "../../store";
import { Icon } from "../../ui/icons";
import { InspHead, InspOpenButton, Isec, Kv, PaneToggle } from "../../ui/parts";
import s from "../../workbench.module.css";
import { ApprovalActions, ApprovalRequestCode, useRequestApprovals } from "./approvals";
import { UrlHead, UrlNotices, UrlPill } from "./urlParts";

/** 상태를 바꿀 수 있는 메서드 (승인 요청 딱지). */
const WRITE = new Set(["POST", "PUT", "PATCH", "DELETE"]);

type Sel = { kind: "job" } | { kind: "approval"; id: string };

const MODE_TEXT = { ai_auto_v1: "AI 자동", user_approval_v1: "사용자 승인", none: "설치 범위" } as const;

/** live 대시보드 (URL 진단 서버). */
export function UrlDashboardLive() {
  const { route, goTo, openInsp } = useShell();
  const handle = useUrl((st) => st.handle);
  const job = useUrl((st) => st.job);
  const verified = useUrl((st) => st.verified);
  const flow = useUrl((st) => st.flow);
  const cap = useUrl((st) => st.cap);
  const g = useUrlGates();
  const visible = route === "dashboard";
  const ended = !!job && URL_ENDED_STATES.includes(job.state);
  const taskId = job?.task_id ?? "";
  const approvalsOn = !!job?.task_id && job.input.workflow_mode === "user_approval_v1" && !ended;
  const a = useRequestApprovals(taskId, visible && approvalsOn);
  const [picked, setSel] = useState<Sel | null>(null);
  // 고르기 전에는 시안처럼 맨 위 할 일(승인 대기)을, 없으면 점검 정보를 보인다
  const first = a.pending[0];
  const sel: Sel = picked ?? (first ? { kind: "approval", id: first.id } : { kind: "job" });

  const host = handle ? urlHost(handle.input.target_url) : null;
  const name = handle?.input.display_name?.trim() || null;
  const to = job ? scanViewFor(job, flow?.stage ?? null) : null;
  let stageText = "—";
  if (flow) stageText = flowStageText[flow.stage];
  else if (job) stageText = urlStateText(job.state, verified);
  const profile =
    cap.kind === "v2" && handle ? cap.catalog.profiles.find((p) => p.id === handle.input.profile_id) : undefined;

  const pick = (next: Sel) => {
    setSel(next);
    openInsp();
  };
  const refresh = () => {
    if (g.canRefresh) void runUrlAction("refresh");
    void refreshWorkflow();
    a.poll.reload();
  };

  // 지금 처리할 일 (실제 상태에서만)
  const todo: ReactNode[] = [];
  for (const r of a.pending) {
    const selected = sel.kind === "approval" && sel.id === r.id;
    todo.push(
      <button
        key={r.id}
        className={cx(s.row, selected && s.sel)}
        type="button"
        data-dsel={`appr-${r.id}`}
        aria-current={selected ? "true" : undefined}
        onClick={() => pick({ kind: "approval", id: r.id })}
      >
        <span className={s["r-main"]}>
          <span className={s["r-t"]}>
            <span className={s["r-host"]}>{host}</span>
            {r.method} {r.path}
          </span>
          <span className={s["r-s"]}>보내기 전 승인 필요</span>
        </span>
        <span className={s["r-side"]}>
          <span className={cx(s.pill, s.wait)}>승인 대기</span>
          <span className={s["r-time"]}>{clockOf(r.created_at)}</span>
        </span>
      </button>,
    );
  }
  if (job && flow?.stage === "awaiting_selection") {
    todo.push(
      <button key="scope" className={s.row} type="button" data-dsel="scope" onClick={() => goTo("scope")}>
        <span className={s["r-main"]}>
          <span className={s["r-t"]}>
            <span className={s["r-host"]}>{host}</span>점검할 경로 선택
          </span>
          <span className={s["r-s"]}>{`탐색 완료 · 찾은 경로 ${flow.endpoints.length}개`}</span>
        </span>
        <span className={s["r-side"]}>
          <span className={cx(s.pill, s.wait)}>결정 대기</span>
        </span>
      </button>,
    );
  }
  if (job && g.reportReadable) {
    todo.push(
      <button key="report" className={s.row} type="button" data-dsel="report" onClick={() => goTo("report")}>
        <span className={s["r-main"]}>
          <span className={s["r-t"]}>
            <span className={s["r-host"]}>{host}</span>보고서 저장됨
          </span>
          <span className={s["r-s"]}>{urlStateText(job.state)}</span>
        </span>
        <span className={s["r-side"]}>
          <span className={s.pill}>결과 확인</span>
        </span>
      </button>,
    );
  }
  if (job?.state === "unknown") {
    todo.push(
      <button key="unknown" className={s.row} type="button" data-dsel="unknown" onClick={() => pick({ kind: "job" })}>
        <span className={s["r-main"]}>
          <span className={s["r-t"]}>
            <span className={s["r-host"]}>{host}</span>결과 불명
          </span>
          <span className={s["r-s"]}>상태를 다시 조회해 확인하세요</span>
        </span>
        <span className={s["r-side"]}>
          <span className={cx(s.pill, s.wait)}>확인 필요</span>
        </span>
      </button>,
    );
  }

  const jobRow = handle ? (
    <button
      className={cx(s.row, sel.kind === "job" && s.sel)}
      type="button"
      data-url-job={job?.id ?? ""}
      aria-current={sel.kind === "job" ? "true" : undefined}
      onClick={() => pick({ kind: "job" })}
    >
      <span className={s["r-main"]}>
        <span className={s["r-t"]}>
          <span className={s["r-host"]}>{host}</span>
        </span>
        <span className={s["r-s"]}>
          {[name, flow && !ended ? flowStageText[flow.stage] : null, job?.task_id ? `task #${job.task_id}` : null]
            .filter(Boolean)
            .join(" · ") || "상태 확인 중"}
        </span>
      </span>
      <span className={s["r-side"]}>
        {job ? <UrlPill job={job} verified={verified} /> : <span className={cx(s.pill, s.wait)}>상태 미확인</span>}
      </span>
    </button>
  ) : null;

  // 오른쪽 상세
  const chosen: RequestApproval | undefined =
    sel.kind === "approval" ? a.pending.find((r) => r.id === sel.id) : undefined;
  let insp: ReactNode;
  if (chosen) {
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
              {WRITE.has(chosen.method) ? <span className={cx(s.tag, s.ap)}>데이터를 바꾸는 요청</span> : null}
            </>
          }
        />
        <Isec t="보낼 요청">
          <ApprovalRequestCode row={chosen} />
        </Isec>
        <Isec>
          <ApprovalActions a={a} row={chosen} />
        </Isec>
      </>
    );
  } else if (handle) {
    insp = (
      <>
        <InspHead
          eyebrow={<span className={s.host}>{host}</span>}
          title={name ?? host ?? "점검 정보"}
          chips={job ? <UrlPill job={job} verified={verified} /> : null}
        />
        <Isec t="점검 정보">
          <Kv
            rows={[
              [
                "대상",
                <span key="t" className={s.mono}>
                  {handle.input.target_url}
                </span>,
              ],
              ["분석 모델", profile ? profile.model_label : handle.input.profile_id],
              ["진단 모드", MODE_TEXT[handle.input.workflow_mode ?? "none"]],
              ["단계", stageText],
              ["실제 task", job?.task_id ? `#${job.task_id}` : "아직 없음"],
            ]}
          />
        </Isec>
        <Isec>
          <div className={s.acts}>
            {to ? (
              <button className={cx(s.btn, s.pri)} type="button" onClick={() => goTo(to)}>
                점검 보기
                <Icon name="arrow" />
              </button>
            ) : null}
            {ended ? (
              <button
                className={s.btn}
                type="button"
                onClick={() => {
                  const url = handle.input.target_url;
                  disconnectUrl();
                  useWb.getState().nPrefill(url);
                  goTo("new");
                }}
              >
                이 주소로 다시 점검
              </button>
            ) : null}
          </div>
          {ended ? <p className={s.help}>다시 점검하면 이 점검은 이 브라우저 목록에서 빠집니다.</p> : null}
          <UrlNotices />
        </Isec>
      </>
    );
  } else {
    insp = (
      <>
        <InspHead title="점검 정보" />
        <Isec>
          <p className={s.empty}>이 브라우저에서 이어 보는 점검이 없습니다.</p>
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
          <section className={s.sec} aria-labelledby="dTodoH">
            <div className={s.sh}>
              <h2 id="dTodoH">지금 처리할 일</h2>
              <span className={s.n} id="dTodoN">{`${todo.length}건`}</span>
            </div>
            {todo.length ? (
              <div className={s.list} id="dTodo">
                {todo}
              </div>
            ) : (
              <p className={s.empty}>처리할 일이 없습니다.</p>
            )}
          </section>
          <section className={s.sec} aria-labelledby="dActH">
            <div className={s.sh}>
              <h2 id="dActH">진행 중인 점검</h2>
              <span className={s.n}>{jobRow && !ended ? "1건" : "0건"}</span>
            </div>
            {jobRow && !ended ? (
              <div className={s.list} id="dActive">
                {jobRow}
              </div>
            ) : (
              <p className={s.empty}>진행 중인 점검이 없습니다.</p>
            )}
          </section>
          <section className={s.sec} aria-labelledby="dDoneH">
            <div className={s.sh}>
              <h2 id="dDoneH">최근 끝난 점검</h2>
            </div>
            {jobRow && ended ? (
              <div className={s.list} id="dDone">
                {jobRow}
              </div>
            ) : (
              <p className={s.empty}>끝난 점검이 없습니다.</p>
            )}
          </section>
        </div>
        <aside className={s.vi} id="dInsp" aria-label="선택한 항목 상세">
          {insp}
        </aside>
        <PaneToggle />
      </div>
    </>
  );
}
