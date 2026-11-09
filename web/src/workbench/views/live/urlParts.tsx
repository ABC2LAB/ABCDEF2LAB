"use client";

// URL 진단(v2) 화면 넷(새 점검 · 범위 확인 · 점검 진행 · 보고서)이 함께 쓰는 부품.
// 서버가 준 값만 그대로 보이고(텍스트로만), 진행률 · 남은 시간 · 판정은 만들지 않는다.

import type { ReactNode } from "react";

import {
  type URLDiagnosisJob,
  type URLDiagnosisScope,
  urlPhaseLabels,
  urlReasonLabels,
} from "@/lib/diagnosis/diagnosis-url";

import { cx } from "../../lib/cx";
import { disconnectUrl, runUrlAction, useUrl, useUrlGates } from "../../live/urlDiagnosis";
import {
  AUTHORITY_TEXT,
  approvalModeText,
  factText,
  URL_PHASES,
  urlHost,
  urlPhaseIndex,
  urlStateText,
  urlTone,
  usdText,
} from "../../live/urlMap";
import { Icon } from "../../ui/icons";
import { Kv, MenuButton } from "../../ui/parts";
import s from "../../workbench.module.css";

/** 화면 머리 (위치 · h1 · 상태 알약 · 버튼 · 메타 줄). */
export function UrlHead({
  title,
  crumb,
  host,
  pill,
  acts,
  meta,
}: {
  title: string;
  crumb: string;
  host?: string | null;
  pill?: ReactNode;
  acts?: ReactNode;
  meta?: ReactNode;
}) {
  return (
    <header className={s.vh}>
      <div className={s["vh-top"]}>
        <MenuButton />
        <nav className={s.crumb} aria-label="위치">
          <a href="#dashboard">작업대</a>
          <span className={s.sep}>/</span>
          {host ? (
            <>
              <span className={s.host}>{host}</span>
              <span className={s.sep}>/</span>
            </>
          ) : null}
          <span>{crumb}</span>
        </nav>
      </div>
      <div className={s["vh-row"]}>
        <div className={s["vh-title"]}>
          <h1 tabIndex={-1}>{title}</h1>
          {pill}
        </div>
        {acts !== undefined ? <div className={s["vh-acts"]}>{acts}</div> : null}
      </div>
      {meta !== undefined ? <div className={s["vh-meta"]}>{meta}</div> : null}
    </header>
  );
}

/** 진단 상태 알약. 확인 못 한 상태는 "현재 상태 미확인"(대기색). */
export function UrlPill({ job, verified }: { job: Pick<URLDiagnosisJob, "state">; verified: boolean }) {
  const tone = verified ? urlTone(job.state) : "wait";
  return <span className={cx(s.pill, tone !== "stop" && s[tone])}>{urlStateText(job.state, verified)}</span>;
}

/** 서버 단계 띠 (초안 → … → 결과 저장). 지난 단계만 체크, 지금 단계는 aria-current. */
export function UrlPhaseBar({ job }: { job: Pick<URLDiagnosisJob, "phase" | "state"> }) {
  const at = urlPhaseIndex(job.phase);
  const finished = job.state === "completed";
  return (
    <nav className={cx(s.phases, s["lv-phases"])} aria-label="진단 단계">
      {URL_PHASES.map((p, i) => {
        const done = i < at || (finished && i === at);
        return (
          <span key={p} className={cx(s.ph, done && s.done)} aria-current={i === at ? "step" : undefined}>
            <span className={s.n}>{done ? <Icon name="check" /> : i + 1}</span>
            {urlPhaseLabels[p]}
          </span>
        );
      })}
    </nav>
  );
}

/** 서버 사유 글 (모르는 코드는 원문 그대로). */
export function reasonText(code: string): string {
  return `${urlReasonLabels[code] ?? code} (${code})`;
}

/** 서버가 확인한 승인 범위 (실제 origin · 경로 · 메서드 · query/body · 승인 방식 · 만료 · 예산). */
export function UrlScopeList({ scope }: { scope: URLDiagnosisScope }) {
  const b = scope.budget;
  return (
    <Kv
      rows={[
        [
          "실제 대상",
          <span key="origin" className={s.mono}>
            {scope.origin}
          </span>,
        ],
        [
          "경로",
          <span key="paths">
            <span className={s.mono}>{scope.path_prefixes.join(", ")}</span> (각 경로 자신과 하위 경로)
          </span>,
        ],
        ["메서드", scope.methods.join(", ")],
        ["query · body", `임의 query 허용 · body 최대 ${scope.max_body_bytes} bytes`],
        ["승인 방식", approvalModeText(scope.approval_mode)],
        [
          "만료",
          <span key="expires" className={s.mono}>
            {scope.expires_at}
          </span>,
        ],
        ["대상 요청 · 시간 상한", `${b.requests}회 · ${b.duration_seconds}초`],
        ["모델 요청 상한", `${b.model_requests}회`],
        ["토큰 상한", `입력 ${b.input_tokens} · 출력 ${b.output_tokens}`],
        ["비용 상한", usdText(b.cost_usd_micros)],
        ...(scope.model
          ? ([
              ["분석 모델", `${scope.model.model} · OpenRouter`],
              ["승인된 제공자", scope.model.provider],
              [
                "호출당 토큰 상한",
                `입력 ${scope.model.input_tokens_per_call} · 출력 ${scope.model.output_tokens_per_call}`,
              ],
              [
                "외부 전송",
                "대상 응답·도구 출력·진단 지시가 승인된 OpenRouter 제공자에게 전송됩니다. 개인정보·비밀이 없는 승인된 범위만 사용하세요.",
              ],
            ] as [string, ReactNode][])
          : []),
      ]}
    />
  );
}

/** 모델 사용량 (서버 기록) — 상한(범위 예산)과 나란히. 기록이 없으면 그렇게 말한다. */
export function UrlUsage({ job }: { job: URLDiagnosisJob }) {
  const u = job.model_usage;
  const b = job.scope?.budget;
  if (!u) return <p className={s.help}>모델 사용량 · 비용 기록은 아직 제공되지 않았습니다.</p>;
  const of = (n: number, cap: number | undefined) => (cap === undefined ? `${n}` : `${n} / ${cap}`);
  return (
    <>
      <Kv
        rows={[
          ["모델 요청", of(u.requests, b?.model_requests)],
          ...(u.reserved_cost_usd_micros === undefined
            ? []
            : [["예약 비용", usdText(u.reserved_cost_usd_micros)] as [string, ReactNode]]),
          ["입력 토큰", `${of(u.input_tokens, b?.input_tokens)} · 예약 ${u.reserved_input_tokens}`],
          ["출력 토큰", `${of(u.output_tokens, b?.output_tokens)} · 예약 ${u.reserved_output_tokens}`],
          [
            "비용",
            u.usage_known
              ? `확인된 비용 ${usdText(u.cost_usd_micros)}${b ? ` / 상한 ${usdText(b.cost_usd_micros)}` : ""}`
              : "사용량 · 비용 불명",
          ],
        ]}
      />
      <p className={s.help}>서버가 센 값입니다. 오른쪽 숫자는 승인 범위의 상한입니다.</p>
    </>
  );
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
          <span key="digest" className={cx(s.mono, s["lv-pre"])}>
            {job.source_digest}
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

/** 조회 연결 해제 (다른 진단 만들기). 서버 진단은 그대로 둔다. */
export function UrlDisconnect() {
  const busy = useUrl((st) => st.busy);
  const handle = useUrl((st) => st.handle);
  const uncertain = useUrl((st) => st.uncertain);
  if (!handle && !uncertain) return null;
  return (
    <div className={s.isec}>
      <button className={cx(s.btn, s.sm)} type="button" disabled={busy} onClick={disconnectUrl}>
        다른 진단 만들기 · 조회 연결 해제
      </button>
      <p className={s.help}>
        이 화면의 조회 연결만 끊습니다. 서버의 진단을 중지하거나 지우지 않습니다. 결과 불명인 진단을 겹쳐 만들지 않도록
        주의하세요.
      </p>
    </div>
  );
}

/** 이어 볼 진단이 없을 때 (새 점검에서 URL 로 시작). */
export function UrlNoJob({ what }: { what: string }) {
  return (
    <>
      <div className={s.note} role="note">
        <Icon name="info" />
        <span>{`${what}: 이 브라우저에서 이어 볼 URL 진단이 없습니다. 새 점검에서 URL 로 범위 확인부터 하세요.`}</span>
      </div>
      <UrlNotices />
      <div>
        <a className={cx(s.btn, s.pri)} href="#new">
          새 점검
          <Icon name="arrow" />
        </a>
      </div>
    </>
  );
}

/** 진단의 대상 host (핸들 · 진단 어느 쪽이든). */
export function useUrlHost(): string | null {
  const job = useUrl((st) => st.job);
  const handle = useUrl((st) => st.handle);
  const target = job?.input.target_url ?? handle?.input.target_url;
  return target ? urlHost(target) : null;
}
