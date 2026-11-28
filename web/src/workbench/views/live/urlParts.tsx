"use client";

// URL 진단(v2) 화면 넷(새 점검 · 범위 확인 · 점검 진행 · 보고서)이 함께 쓰는 부품.
// 서버가 준 값만 그대로 보이고(텍스트로만), 진행률 · 남은 시간 · 판정은 만들지 않는다.

import { Fragment, type ReactNode } from "react";

import { type URLDiagnosisJob, urlReasonLabels } from "@/lib/diagnosis/diagnosis-url";

import { cx } from "../../lib/cx";
import { runUrlAction, useUrl, useUrlGates } from "../../live/urlDiagnosis";
import { urlHost, urlStateText, urlTone } from "../../live/urlMap";
import { Icon } from "../../ui/icons";
import { MenuButton, type PhaseKey, Phases } from "../../ui/parts";
import s from "../../workbench.module.css";

/** 받침 있는 말 뒤엔 '으로', 없거나 ㄹ 받침이면 '로' (읽기용 이름 '새 점검으로 돌아가기') */
function toParticle(word: string): string {
  const code = word.charCodeAt(word.length - 1) - 0xac00;
  if (code < 0 || code > 11171) return "(으)로";
  const final = code % 28;
  return final === 0 || final === 8 ? "로" : "으로";
}

/**
 * 뒤로 가기 버튼. 모든 화면이 같은 모양(작은 버튼 · 왼쪽 화살표)과 '← 갈 곳' 글을 쓴다(사용자 11/24: 모양 · 멘트 통합).
 * 읽기 프로그램에는 '갈 곳으로 돌아가기'로 읽힌다.
 */
export function BackButton({ to, onClick }: { to: string; onClick: () => void }) {
  return (
    <button
      className={cx(s.btn, s.sm, s["back-btn"])}
      type="button"
      onClick={onClick}
      aria-label={`${to}${toParticle(to)} 돌아가기`}
    >
      <Icon name="arrow" />
      {to}
    </button>
  );
}

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
  phaseActs,
  onCurrentPhase,
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
  /** 단계 줄 오른쪽 끝(메타 밑)에 둘 버튼. 줄 높이 안에 두어 머리 높이가 다른 화면과 같다(보고서 [이 주소로 다시 점검], 사용자 11/27) */
  phaseActs?: ReactNode;
  /** 단계 줄에서 지금 단계를 눌렀을 때 (안쪽 페이지를 닫고 그 화면 처음으로) */
  onCurrentPhase?: () => void;
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
        {/* 단계 줄이 있는 네 화면은 머리 높이가 같아야 단계를 눌러도 단계 줄이 오르내리지 않는다(사용자 11/25: 점검 진행 ·
            보고서로 가면 줄 위치가 바뀜). 그래서 메타 줄(보고서 시작 · 소요 · 모델 · 토큰)은 따로 줄을 두지 않고 제목 줄 오른쪽에 */}
        {phase && meta !== undefined ? <div className={s["vh-meta"]}>{meta}</div> : null}
        {acts !== undefined ? <div className={s["vh-acts"]}>{acts}</div> : null}
      </div>
      {!phase && meta !== undefined ? <div className={s["vh-meta"]}>{meta}</div> : null}
      {phase && phaseActs !== undefined ? (
        <div className={s["vh-phrow"]}>
          <Phases phase={phase} onCurrent={onCurrentPhase} />
          <div className={s["vh-phacts"]}>{phaseActs}</div>
        </div>
      ) : null}
      {phase && phaseActs === undefined ? <Phases phase={phase} onCurrent={onCurrentPhase} /> : null}
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

/** 점검 방식 글 (탐색 흐름이 없는 점검은 설치 범위로 바로 진단). */
const MODE_TEXT: Readonly<Record<NonNullable<URLDiagnosisJob["input"]["workflow_mode"]> | "none", string>> = {
  ai_auto_v1: "AI 자동",
  user_approval_v1: "사용자 승인",
  none: "설치 범위",
};

/** 초안 입력에 기록된 시작 확인 진술을 한 줄로. 기록된 것이 없으면 빈 글. */
export function startConfirmationText(input: URLDiagnosisJob["input"]): string {
  return [input.exclusions_reviewed ? "제외 경로 검토함" : "", input.backup_confirmed ? "복사본/백업 있음" : ""]
    .filter(Boolean)
    .join(" · ");
}

/**
 * 점검 정보 (진행 요약 맨 아래, 사용자 11/17 '깔끔하게'): 사람이 보는 값만 줄로 — 대상 · 방식 · 모델 · 제외 경로 ·
 * 시작 전 확인(기록된 운영자 진술).
 * 진행 확인(준비 · 시작 · 마감 · 저장)은 맨 위 단계 게이지와 겹쳐, 진단 ID · task · 설정 지문 · 실행 권위 같은 기록 값은
 * 운영자가 볼 일이 없어 두지 않는다(사용자 11/17 — 진단 ID 는 왼쪽 점검 목록 줄에 있다).
 */
export function UrlFacts({ job }: { job: URLDiagnosisJob }) {
  const cap = useUrl((st) => st.cap);
  const profile = cap.kind === "v2" ? cap.catalog.profiles.find((p) => p.id === job.input.profile_id) : undefined;
  return (
    <dl className={s.frows}>
      <div>
        <dt>대상</dt>
        <dd>{urlHost(job.input.target_url)}</dd>
      </div>
      <div>
        <dt>방식</dt>
        <dd>{MODE_TEXT[job.input.workflow_mode ?? "none"]}</dd>
      </div>
      {profile ? (
        <div>
          <dt>모델</dt>
          <dd>{`${profile.model_label} · ${profile.model?.external_transfer ? "외부" : "로컬"}`}</dd>
        </div>
      ) : null}
      {job.input.excluded_paths ? (
        <div>
          <dt>제외 경로</dt>
          <dd>{job.input.excluded_paths}</dd>
        </div>
      ) : null}
      {/* 초안 입력에 기록된 운영자 진술만 보인다. 없는 진술을 '확인함'으로 채우지 않는다(옛 초안 · CLI · 평가 러너) */}
      {startConfirmationText(job.input) ? (
        <div>
          <dt>시작 전 확인</dt>
          <dd>{startConfirmationText(job.input)}</dd>
        </div>
      ) : null}
    </dl>
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
