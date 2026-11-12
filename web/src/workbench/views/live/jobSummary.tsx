"use client";

// 이어 보는 진단 하나의 오른쪽 상세 (점검 정보 · 갈 화면 · 다시 점검). 대시보드 · 진행 중인 점검 · 끝난 점검이 같이 쓴다.
// 값은 서버 스토어(useUrl)의 것만 쓰고, 화면마다 더 보일 줄(rows) · 버튼(acts)만 덧붙인다.

import type { ReactNode } from "react";

import { cx } from "../../lib/cx";
import { disconnectUrl, useUrl } from "../../live/urlDiagnosis";
import { scanViewFor, URL_ENDED_STATES, urlHost, urlOutcomeText, urlStateText } from "../../live/urlMap";
import { flowStageText } from "../../live/workflow";
import { useShell } from "../../shell";
import { useWb } from "../../store";
import { Icon } from "../../ui/icons";
import { InspHead, Isec, Kv } from "../../ui/parts";
import s from "../../workbench.module.css";
import { UrlNotices, UrlPill } from "./urlParts";

const MODE_TEXT = { ai_auto_v1: "AI 자동", user_approval_v1: "사용자 승인", none: "설치 범위" } as const;

/** 이어 보는 진단의 상세. 이어 보는 진단이 없으면 아무것도 그리지 않는다(화면이 빈 안내를 그린다). */
export function JobSummary({
  rows = [],
  acts,
}: {
  rows?: ReadonlyArray<readonly [ReactNode, ReactNode]>;
  acts?: ReactNode;
}) {
  const { goTo } = useShell();
  const handle = useUrl((st) => st.handle);
  const job = useUrl((st) => st.job);
  const verified = useUrl((st) => st.verified);
  const flow = useUrl((st) => st.flow);
  const cap = useUrl((st) => st.cap);
  if (!handle) return null;

  const ended = !!job && URL_ENDED_STATES.includes(job.state);
  const host = urlHost(handle.input.target_url);
  const name = handle.input.display_name?.trim() || null;
  const to = job ? scanViewFor(job, flow?.stage ?? null) : null;
  let stageText = "—";
  if (flow) stageText = flowStageText[flow.stage];
  else if (job) stageText = urlStateText(job.state, verified);
  const profile = cap.kind === "v2" ? cap.catalog.profiles.find((p) => p.id === handle.input.profile_id) : undefined;
  const outcomeText = job ? urlOutcomeText(job) : "";

  return (
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
            ...(outcomeText ? [["실행 결과", outcomeText] as const] : []),
            ...rows,
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
          {acts}
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
}
