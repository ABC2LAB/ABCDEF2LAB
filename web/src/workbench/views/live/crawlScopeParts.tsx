"use client";

// 자산 탐색 · 범위 확인(live)이 함께 쓰는 부품: 화면 머리(위치 · 상태 알약 · 단계 표시), 종류 딱지(추정), 트래픽 꺼짐 안내.

import type { ReactNode } from "react";

import { cx } from "../../lib/cx";
import type { PKind } from "../../lib/kind";
import type { LiveTask } from "../../live/api";
import { KIND_LABEL } from "../../live/crawlScope";
import { isActive, taskLabel } from "../../live/map";
import { PhasePill } from "../../live/parts";
import { Icon } from "../../ui/icons";
import { has, MenuButton, PH, type PhaseKey } from "../../ui/parts";
import s from "../../workbench.module.css";

const KIND_CLS: Readonly<Record<PKind, string | undefined>> = { get: s.in, login: s.lg, ap: s.ap, ex: s.ex };

/** 종류 딱지 (메서드·경로로 추정한 값). */
export function KindTag({ k }: { k: PKind }) {
  return <span className={cx(s.tag, KIND_CLS[k])}>{KIND_LABEL[k]}</span>;
}

/** 트래픽 기록이 꺼져 있을 때 (GET /api/traffic 의 enabled:false). */
export function TrafficOffNote() {
  return (
    <div className={s.note} role="note">
      <Icon name="info" />
      <span>트래픽 기록이 꺼져 있습니다 (설정에서 traffic_capture)</span>
    </div>
  );
}

/**
 * 단계 표시 (live). 엔진에는 탐색 → 범위 → 진행 단계가 따로 없어 지난 단계 체크는 하지 않고 화면 이동만 한다.
 * 보고서는 점검이 끝나야 링크.
 */
function LivePhases({ phase, task }: { phase: PhaseKey; task: LiveTask | null }) {
  const rep = !!task && !isActive(task);
  return (
    <nav className={s.phases} aria-label="점검 단계" data-phase={phase}>
      {PH.map(([k, label], i) => {
        const inner = (
          <>
            <span className={s.n}>{i + 1}</span>
            {label}
          </>
        );
        if (k === "report" && !rep) {
          return (
            <span key={k} className={cx(s.ph, s.later)} title="점검이 끝나면 볼 수 있습니다">
              {inner}
            </span>
          );
        }
        return (
          <a key={k} className={s.ph} href={`#${k}`} aria-current={k === phase ? "step" : undefined}>
            {inner}
          </a>
        );
      })}
    </nav>
  );
}

/** 화면 머리: 위치(작업대 / 점검 / 화면) · h1 · 상태 알약 · 동작 버튼 · 단계 표시(점검을 골랐을 때만). */
export function LiveHead({
  title,
  phase,
  taskId,
  task,
  children,
}: {
  title: string;
  phase: PhaseKey;
  taskId: string | null;
  task: LiveTask | null;
  children?: ReactNode;
}) {
  let label: string | null = null;
  if (task) label = taskLabel(task);
  else if (taskId) label = `점검 #${taskId}`;
  return (
    <header className={cx(s.vh, taskId && s["has-ph"])}>
      <div className={s["vh-top"]}>
        <MenuButton />
        <nav className={s.crumb} aria-label="위치">
          <a href="#dashboard">작업대</a>
          {label ? (
            <>
              <span className={s.sep}>/</span>
              <span className={cx(s.host, s["t-host"])}>{label}</span>
            </>
          ) : null}
          <span className={s.sep}>/</span>
          <span>{title}</span>
        </nav>
      </div>
      <div className={s["vh-row"]}>
        <div className={s["vh-title"]}>
          <h1 tabIndex={-1}>{title}</h1>
          {task ? <PhasePill task={task} /> : null}
        </div>
        {has(children) ? <div className={s["vh-acts"]}>{children}</div> : null}
      </div>
      {taskId ? <LivePhases phase={phase} task={task} /> : null}
    </header>
  );
}

/** 고른 점검이 목록에 없을 때 (지워졌거나 다른 엔진). */
export function MissingTaskNote() {
  return (
    <div className={cx(s.note, s.warn)} role="alert">
      <Icon name="alert" />
      <span>고른 점검을 엔진 목록에서 찾지 못했습니다. 다른 점검을 고르세요.</span>
    </div>
  );
}
