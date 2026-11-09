"use client";

// 실제 URL 진단·설치 모델·인증 상태를 기존 Workbench 레일에 표시한다.
// 서버가 제공하지 않는 전체 목록·승인 대기 수를 만들어 채우지 않는다.

import type { ReactNode } from "react";

import { urlPhaseLabels } from "@/lib/diagnosis/diagnosis-url";

import { cx } from "../lib/cx";
import { useShell, type View } from "../shell";
import { Icon } from "../ui/icons";
import s from "../workbench.module.css";
import { useLiveAuth } from "./auth";
import { useUrl } from "./urlDiagnosis";
import { URL_ENDED_STATES, urlHost, urlStateText, urlTone, urlViewFor } from "./urlMap";

/** 레일 '진행 중'에 보일 최대 줄 수 (넘치면 "외 N건"). */
export const RAIL_ACTIVE_MAX = 6;
/** 레일 '최근 끝난 점검'에 보일 최대 줄 수. */
export const RAIL_DONE_MAX = 5;

/** 진행 중 점검을 누르면 가는 화면들 (aria-current 계산용). */
const _ACTIVE_VIEWS: readonly View[] = ["crawl", "scope", "live"];

/** 대시보드 링크의 배지: 도구 호출 승인 대기 수. 미지원·0 이면 숨김. */
export function LiveRailBadge() {
  return null;
}

/** 진행 중 / 최근 끝난 점검 두 묶음. URL 진단 서버면 이어 보는 진단 한 줄. */
export function LiveRailTasks() {
  return <UrlRailTasks />;
}

/**
 * URL 진단: 이 브라우저가 이어 보는 진단 한 줄. 마감이 확인된 끝난 상태만 아래(최근 끝난 점검)에 두고,
 * 시작 전 · 진행 중 · 결과 불명(unknown)은 위에 둔다(결과 불명을 끝난 것으로 보이지 않게). 누르면 알맞은 화면으로.
 */
function UrlRailTasks() {
  const { route, goTo } = useShell();
  const handle = useUrl((st) => st.handle);
  const job = useUrl((st) => st.job);
  const verified = useUrl((st) => st.verified);
  const busy = useUrl((st) => st.busy);
  const open = !job || !URL_ENDED_STATES.includes(job.state);
  let row: ReactNode = null;
  if (handle) {
    const to = job ? urlViewFor(job) : "live";
    const tone = job && verified ? urlTone(job.state) : "wait";
    let sub = busy ? "상태 확인 중" : "상태 미확인";
    if (job) sub = `${urlStateText(job.state, verified)} · ${urlPhaseLabels[job.phase]}`;
    row = (
      <button
        className={s.rs}
        type="button"
        data-url-job={job?.id ?? ""}
        aria-current={route === to ? "page" : undefined}
        onClick={() => goTo(to)}
      >
        <span className={cx(s.dot, s[tone])} />
        <span className={s.host}>{urlHost(handle.input.target_url)}</span>
        <span className={s.rsub}>{sub}</span>
      </button>
    );
  }
  return (
    <>
      <div className={s.rsec}>
        <div className={s.rh}>진행 중</div>
        {open ? row : null}
        {!handle || !open ? <div className={s["lv-rempty"]}>진행 중인 URL 진단 없음</div> : null}
      </div>
      <div className={s.rsec}>
        <div className={s.rh}>최근 끝난 점검</div>
        {handle && !open ? row : <div className={s["lv-rempty"]}>이 브라우저에서 이어 보는 끝난 진단 없음</div>}
      </div>
    </>
  );
}

/** 레일 아래 분석 모델 줄. URL 진단 서버면 이어 보는 진단의 설치 프로필 모델. */
export function LiveRailModel() {
  return <UrlRailModel />;
}

/** URL 진단: 진단마다 설치 프로필이 모델을 정한다. 로컬/외부는 catalog 에 없어 짐작하지 않는다. */
function UrlRailModel() {
  const cap = useUrl((st) => st.cap);
  const profileId = useUrl((st) => st.job?.input.profile_id ?? st.handle?.input.profile_id ?? null);
  const profile = cap.kind === "v2" && profileId ? cap.catalog.profiles.find((p) => p.id === profileId) : undefined;
  return (
    <div className={s.rf} id="railModel">
      <Icon name="chip" />
      <div>
        {profile ? "분석 모델 · 설치 프로필" : "분석 모델 · 진단마다 설치 프로필"}
        {profile ? <b>{profile.model_label}</b> : null}
      </div>
    </div>
  );
}

/** 레일 아래 로그인 상태 줄 + 로그아웃 (ready 일 때만). */
export function LiveRailAccount() {
  const { state, logout } = useLiveAuth();
  if (state !== "ready") return null;
  return (
    <div className={s.rf}>
      <Icon name="lock" />
      <div>
        엔진에 로그인됨{" "}
        <button className={s["lv-link"]} type="button" onClick={logout}>
          로그아웃
        </button>
      </div>
    </div>
  );
}
