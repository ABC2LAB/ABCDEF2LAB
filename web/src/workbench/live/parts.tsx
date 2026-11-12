"use client";

// live 화면이 함께 쓰는 작은 부품. 모두 작업대 CSS 모듈 클래스로만 그린다.
// 서버 글은 전부 텍스트로만(innerHTML 금지), 대상 URL 은 링크로 만들지 않는다.

import { type ReactNode, useEffect, useState } from "react";

import { cx } from "../lib/cx";
import { Icon } from "../ui/icons";
import s from "../workbench.module.css";
import type { LiveTask } from "./api";
import { useLiveTasks } from "./feeds";
import { errorText, isActive, relTime, taskLabel, taskPhase } from "./map";
import { useSource } from "./source";

/**
 * "엔진 미지원" 표시 — 백엔드에 API 가 없는 기능 자리에 가짜 값 대신 둔다.
 * chip=true 면 줄 안의 작은 딱지, 아니면 안내 상자(what 이 있으면 "what: 엔진 미지원 …").
 */
export function Unsupported({ what, chip = false }: { what?: string; chip?: boolean }) {
  if (chip) {
    return (
      <span className={cx(s.tag, s.out)} title="백엔드에 아직 없는 기능입니다">
        엔진 미지원
      </span>
    );
  }
  return (
    <div className={s.note} role="note">
      <Icon name="info" />
      <span>{what ? `${what}: ` : ""}엔진 미지원 — 백엔드에 아직 없는 기능입니다.</span>
    </div>
  );
}

/** 시안에는 있으나 서버 · 화면 연결이 아직 없는 칸에 넣는 글. */
export const NOT_YET = "아직 구현 안 됨";

/**
 * 시안 칸 자리 표시 — 서버에 입력 · 데이터가 없어 아직 못 채운 칸에 가짜 값 대신 둔다(절대규칙 5).
 * why 는 무엇이 없어서인지(마우스 설명 · 읽기 프로그램). block 이면 안내 상자, 아니면 줄 안의 작은 딱지.
 */
export function NotYet({ why, block = false }: { why: string; block?: boolean }) {
  if (block) {
    return (
      <div className={s.note} role="note">
        <Icon name="info" />
        <span>{`${NOT_YET} · ${why}`}</span>
      </div>
    );
  }
  return (
    <span className={cx(s.tag, s.out)} title={why}>
      {NOT_YET}
      <span className={s["sr-only"]}>{` · ${why}`}</span>
    </span>
  );
}

/** 이 서버 모드(일반 / 첫 진단)에서 안 되는 섹션을 접을 때 (usePoll().unsupported). what 이 있으면 앞에 붙인다. */
export function ModeNote({ what }: { what?: string }) {
  return (
    <div className={s.note} role="note">
      <Icon name="info" />
      <span>{what ? `${what}: ` : ""}이 서버 모드에서는 제공되지 않음</span>
    </div>
  );
}

/** 불러오는 중 한 줄 (role=status). */
export function LoadingRow({ text = "불러오는 중…" }: { text?: string }) {
  return (
    <p className={s.empty} role="status">
      {text}
    </p>
  );
}

/** 오류 안내 + 다시 시도 (onRetry 가 있으면). 미지원 오류면 ModeNote 로 바꿔 보인다. */
export function ErrorNote({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  const text = errorText(error);
  if (text === "이 서버 모드에서는 제공되지 않음") return <ModeNote />;
  return (
    <div className={cx(s.note, s.warn)} role="alert">
      <Icon name="alert" />
      <span className={s["lv-grow"]}>{text}</span>
      {onRetry ? (
        <button className={cx(s.btn, s.sm)} type="button" onClick={onRetry}>
          다시 시도
        </button>
      ) : null}
    </div>
  );
}

/** 가림 안내. 요청·응답 원문 · 도구 입력 · 증거처럼 maskSecrets 를 거친 글 곁에 둔다. */
export function MaskedNote() {
  return (
    <p className={s.help}>
      <Icon name="lock" /> 화면 표시용 가림 — 서버 원문은 가려지지 않습니다
    </p>
  );
}

/** "추정" 딱지. 엔진이 주지 않아 화면이 짐작한 값(호스트 · 경로 종류 · 로컬 모델 …) 곁에. */
export function EstimateTag({ title = "엔진이 주지 않는 값을 화면이 추정했습니다" }: { title?: string }) {
  return (
    <span className={cx(s.tag, s.ex)} title={title}>
      추정
    </span>
  );
}

/** 렌더 중에 시계를 읽지 않도록 마운트 뒤 채우는 지금 시각(ms). everyMs 마다 갱신(기본 30초). 처음 렌더는 0. */
export function useNow(everyMs = 30_000): number {
  const [now, setNow] = useState(0);
  useEffect(() => {
    setNow(Date.now());
    const t = window.setInterval(() => setNow(Date.now()), everyMs);
    return () => window.clearInterval(t);
  }, [everyMs]);
  return now;
}

/** 점검 상태 알약 (`.pill` + run·wait·ok, 끝났거나 실패면 회색). */
export function PhasePill({ task }: { task: Pick<LiveTask, "status"> }) {
  const ph = taskPhase(task);
  return <span className={cx(s.pill, ph.tone !== "stop" && s[ph.tone])}>{ph.label}</span>;
}

/**
 * 점검 고르기: 선택이 없을 때 화면 본문에 둔다. 줄을 누르면 selectTask(id) 만 하고 지금 화면에 머문다.
 * which: all 전체 · active 진행 중만 · done 끝난 것만. 목록은 공용 조회(useLiveTasks)를 쓴다.
 */
export function TaskPicker({
  which = "all",
  title = "점검을 고르세요",
}: {
  which?: "all" | "active" | "done";
  title?: string;
}) {
  const feed = useLiveTasks();
  const selectTask = useSource((st) => st.selectTask);
  const now = useNow();
  const all = feed.data?.tasks ?? [];
  const keep = (t: LiveTask) => {
    if (which === "all") return true;
    return which === "active" ? isActive(t) : !isActive(t);
  };
  const list = all.filter(keep).sort((a, b) => b.created_unix - a.created_unix);

  let body: ReactNode;
  if (feed.unsupported) body = <ModeNote what="점검 목록" />;
  else if (!feed.data && feed.error) body = <ErrorNote error={feed.error} onRetry={feed.reload} />;
  else if (!feed.data) body = <LoadingRow />;
  else if (!list.length) body = <p className={s.empty}>고를 점검이 없습니다.</p>;
  else {
    body = (
      <div className={s.list}>
        {list.map((t) => {
          const label = taskLabel(t);
          const name = t.name.trim();
          return (
            <button key={t.id} className={s.row} type="button" onClick={() => selectTask(t.id)}>
              <span className={s["r-main"]}>
                <span className={s["r-t"]}>
                  <span className={s["r-host"]}>{label}</span>
                </span>
                <span className={s["r-s"]}>{name && name !== label ? name : `점검 #${t.id}`}</span>
              </span>
              <span className={s["r-side"]}>
                <PhasePill task={t} />
                <span className={s["r-time"]}>{now ? relTime(t.created_unix, now) : ""}</span>
              </span>
            </button>
          );
        })}
      </div>
    );
  }
  return (
    <section className={s.sec} aria-label={title}>
      <div className={s.sh}>
        <h2>{title}</h2>
        {feed.data ? <span className={s.n}>{`${list.length}건`}</span> : null}
      </div>
      {body}
    </section>
  );
}
