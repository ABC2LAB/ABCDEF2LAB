"use client";

// URL 진단(v2) 화면 넷(새 점검 · 범위 확인 · 점검 진행 · 보고서)이 함께 쓰는 부품.
// 서버가 준 값만 그대로 보이고(텍스트로만), 진행률 · 남은 시간 · 판정은 만들지 않는다.

import { Fragment, type ReactNode } from "react";

import { type URLDiagnosisJob, urlReasonLabels } from "@/lib/diagnosis/diagnosis-url";

import { cx } from "../../lib/cx";
import { runUrlAction, useUrl, useUrlGates } from "../../live/urlDiagnosis";
import { AUTHORITY_TEXT, factText, urlHost, urlStateText, urlTone } from "../../live/urlMap";
import { Icon } from "../../ui/icons";
import { Kv, MenuButton, type PhaseKey, Phases } from "../../ui/parts";
import s from "../../workbench.module.css";

/**
 * 화면 머리 (위치 · h1 · 상태 알약 · 버튼 · 메타 줄 · 점검 단계).
 * 위치 줄은 상위 화면 · 주소가 있을 때만 그린다(시안 v83: "작업대" 뿌리는 뺐다). 위치가 제목과 같기만 하면
 * 줄을 그리지 않고, 좁은 화면에서만 메뉴 버튼 줄이 남는다.
 */
export function UrlHead({
  title,
  crumb,
  parent,
  host,
  pill,
  acts,
  meta,
  phase,
}: {
  title: string;
  crumb: string;
  /** 위치에서 주소 앞에 둘 상위 화면 링크 (예: 보고서 → 끝난 점검) */
  parent?: { href: string; label: string };
  host?: string | null;
  pill?: ReactNode;
  acts?: ReactNode;
  meta?: ReactNode;
  /** 점검 하나를 보는 네 화면이면 단계 표시를 붙인다 */
  phase?: PhaseKey;
}) {
  // crumb 이 빈 글이면 위치 끝에 화면 이름을 붙이지 않는다 (시안 보고서: 끝난 점검 / 주소)
  const items: Array<readonly [string, ReactNode]> = [];
  if (parent)
    items.push([
      "parent",
      <a key="parent" href={parent.href}>
        {parent.label}
      </a>,
    ]);
  if (host)
    items.push([
      "host",
      <span key="host" className={s.host}>
        {host}
      </span>,
    ]);
  if (crumb && (items.length || crumb !== title)) items.push(["crumb", <span key="crumb">{crumb}</span>]);
  const path = items.length > 0;
  return (
    <header className={cx(s.vh, phase && s["has-ph"])}>
      <div className={cx(s["vh-top"], !path && s["menu-only"])}>
        <MenuButton />
        {path ? (
          <nav className={s.crumb} aria-label="위치">
            {items.map(([k, x], i) => (
              <Fragment key={k}>
                {i ? <span className={s.sep}>/</span> : null}
                {x}
              </Fragment>
            ))}
          </nav>
        ) : null}
      </div>
      <div className={s["vh-row"]}>
        <div className={s["vh-title"]}>
          <h1 tabIndex={-1}>{title}</h1>
          {pill}
        </div>
        {acts !== undefined ? <div className={s["vh-acts"]}>{acts}</div> : null}
      </div>
      {meta !== undefined ? <div className={s["vh-meta"]}>{meta}</div> : null}
      {phase ? <Phases phase={phase} /> : null}
    </header>
  );
}

/** 진단 상태 알약. 확인 못 한 상태는 "현재 상태 미확인"(대기색). */
export function UrlPill({ job, verified }: { job: Pick<URLDiagnosisJob, "state">; verified: boolean }) {
  const tone = verified ? urlTone(job.state) : "wait";
  return <span className={cx(s.pill, tone !== "stop" && s[tone])}>{urlStateText(job.state, verified)}</span>;
}

/** 서버 사유 글 (모르는 코드는 원문 그대로). */
export function reasonText(code: string): string {
  return `${urlReasonLabels[code] ?? code} (${code})`;
}

/** 진단 식별 · 서버가 확인한 사실 (준비 시작 · 권위 · 엔진 시작 · 마감 · 결과 저장). */
export function UrlFacts({ job }: { job: URLDiagnosisJob }) {
  return (
    <Kv
      rows={[
        ["진단", <span key="job" className={s.mono}>{`${job.id} · 수정 ${job.revision}`}</span>],
        ["실제 task", job.task_id ? <span key="task" className={s.mono}>{`#${job.task_id}`}</span> : "아직 없음"],
        [
          "설정 지문",
          <span key="digest" className={s.mono} title={job.source_digest}>
            {`${job.source_digest.slice(0, 16)}…`}
          </span>,
        ],
        ["모델 · 작업 준비 시작", factText(job.preparation_started)],
        ["실행 권위", AUTHORITY_TEXT[job.authority_state]],
        ["엔진 시작", factText(job.started)],
        ["마감 확인", factText(job.drain_complete)],
        ["결과 저장", factText(job.result_ready)],
      ]}
    />
  );
}

/** 오류 글 · 결과 불명 · 재전송 금지 안내 (팀 화면과 같은 뜻). */
export function UrlNotices() {
  const error = useUrl((st) => st.error);
  const uncertain = useUrl((st) => st.uncertain);
  const handle = useUrl((st) => st.handle);
  const job = useUrl((st) => st.job);
  const bound = !!handle?.startKey || !!handle?.bindingLocked;
  return (
    <>
      {error ? (
        <div className={cx(s.note, s.warn)} role="alert">
          <Icon name="alert" />
          <span>{error}</span>
        </div>
      ) : null}
      {uncertain || (bound && job?.state === "ready") ? (
        <p className={s.help}>
          시작 · 모델 호출을 자동으로 다시 보내지 않습니다. 같은 진단의 상태를 다시 조회하세요. 미확인은 미실행을 뜻하지
          않습니다.
        </p>
      ) : null}
    </>
  );
}

/** 상태 다시 조회 버튼. */
export function UrlRefreshButton({ small = true }: { small?: boolean }) {
  const g = useUrlGates();
  return (
    <button
      className={cx(s.btn, small && s.sm)}
      type="button"
      disabled={!g.canRefresh}
      onClick={() => void runUrlAction("refresh")}
    >
      <Icon name="refresh" />
      상태 다시 조회
    </button>
  );
}

/** 진단의 대상 host (핸들 · 진단 어느 쪽이든). */
export function useUrlHost(): string | null {
  const job = useUrl((st) => st.job);
  const handle = useUrl((st) => st.handle);
  const target = job?.input.target_url ?? handle?.input.target_url;
  return target ? urlHost(target) : null;
}
