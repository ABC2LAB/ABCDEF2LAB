"use client";

// 레일의 live 부분: 대시보드 배지(승인 대기) · 진행 중 / 최근 끝난 점검(tasks) · 분석 모델(llm, 로컬 추정) ·
// 로그인 상태 · 데이터 원천 스위치. demo 레일은 ui/Rail.tsx 그대로이고, live 일 때만 이 조각으로 바꿔 끼운다.
// URL 진단(v2) 서버는 점검 목록 · 엔진 LLM 설정을 주지 않으므로, 이 브라우저가 이어 보는 진단 한 줄과 설치 프로필 모델을 보인다.

import type { ReactNode } from "react";

import { urlPhaseLabels } from "@/abc2lab-ui/lib/diagnosis-url";

import { cx } from "../lib/cx";
import { useShell, type View } from "../shell";
import { Icon } from "../ui/icons";
import s from "../workbench.module.css";
import { fetchLlm, type LiveTask } from "./api";
import { useLiveAuth } from "./auth";
import { useLivePending, useLiveTasks } from "./feeds";
import { errorText, isActive, isLocalBaseUrl, relTime, taskLabel, taskPhase } from "./map";
import { useNow } from "./parts";
import { canGoLive, useSource } from "./source";
import { useIsUrlMode, useUrl } from "./urlDiagnosis";
import { URL_ENDED_STATES, urlHost, urlStateText, urlTone, urlViewFor } from "./urlMap";
import { usePoll } from "./usePoll";

/** 레일 '진행 중'에 보일 최대 줄 수 (넘치면 "외 N건"). */
export const RAIL_ACTIVE_MAX = 6;
/** 레일 '최근 끝난 점검'에 보일 최대 줄 수. */
export const RAIL_DONE_MAX = 5;

/** 진행 중 점검을 누르면 가는 화면들 (aria-current 계산용). */
const ACTIVE_VIEWS: readonly View[] = ["crawl", "scope", "live"];

/** 대시보드 링크의 배지: 도구 호출 승인 대기 수. 미지원·0 이면 숨김. */
export function LiveRailBadge() {
  const p = useLivePending();
  const n = p.unsupported ? 0 : (p.data?.length ?? 0);
  return (
    <span className={s.badge} id="railBadge" title="승인 대기" hidden={!n}>
      {n}
    </span>
  );
}

function findingsTotal(t: LiveTask): number {
  const f = t.findings;
  return f.critical + f.high + f.medium + f.low;
}

/** 진행 중 / 최근 끝난 점검 두 묶음. URL 진단 서버면 이어 보는 진단 한 줄. */
export function LiveRailTasks() {
  return useIsUrlMode() ? <UrlRailTasks /> : <TaskRailTasks />;
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

/** 진행 중 / 최근 끝난 점검 두 묶음. 진행 중은 점검 진행으로, 끝난 것은 보고서로 간다(그 점검을 고른 뒤). */
function TaskRailTasks() {
  const { route, goTo } = useShell();
  const auth = useLiveAuth().state;
  const feed = useLiveTasks();
  const taskId = useSource((st) => st.taskId);
  const selectTask = useSource((st) => st.selectTask);
  const now = useNow();

  const tasks = feed.data?.tasks ?? [];
  const active = tasks.filter(isActive).sort((a, b) => b.created_unix - a.created_unix);
  const done = tasks
    .filter((t) => !isActive(t))
    .sort((a, b) => (b.completed_unix || b.created_unix) - (a.completed_unix || a.created_unix))
    .slice(0, RAIL_DONE_MAX);

  // 목록을 못 쓰는 까닭 (있으면 진행 중 묶음에 한 줄로)
  let status: string | null = null;
  if (auth === "checking") status = "엔진 연결 확인 중";
  else if (auth === "login") status = "로그인 필요";
  else if (auth === "offline") status = "엔진에 연결할 수 없음";
  else if (auth === "uninitialized") status = "엔진 초기 설정 필요";
  else if (feed.unsupported) status = "이 서버 모드에서는 제공되지 않음";
  else if (!feed.data && feed.error) status = errorText(feed.error);
  else if (!feed.data) status = "불러오는 중…";

  const open = (t: LiveTask, to: View) => {
    selectTask(t.id);
    goTo(to);
  };
  const cur = (t: LiveTask, views: readonly View[]) =>
    taskId === t.id && views.includes(route) ? ("page" as const) : undefined;

  return (
    <>
      <div className={s.rsec}>
        <div className={s.rh}>진행 중</div>
        {status ? <div className={s["lv-rempty"]}>{status}</div> : null}
        {!status && !active.length ? <div className={s["lv-rempty"]}>진행 중인 점검 없음</div> : null}
        {active.slice(0, RAIL_ACTIVE_MAX).map((t) => {
          const ph = taskPhase(t);
          const goals = t.goals_total > 0 ? `목표 ${t.goals_met}/${t.goals_total}` : "";
          return (
            <button
              key={t.id}
              className={s.rs}
              type="button"
              data-task={t.id}
              aria-current={cur(t, ACTIVE_VIEWS)}
              onClick={() => open(t, "live")}
            >
              <span className={cx(s.dot, s[ph.tone])} />
              <span className={s.host}>{taskLabel(t)}</span>
              <span className={s.rsub}>{goals ? `${ph.label} · ${goals}` : ph.label}</span>
              {t.goals_total > 0 ? (
                <span className={cx(s.rbar, ph.tone === "wait" && s.wait)}>
                  <i style={{ width: `${Math.round((100 * t.goals_met) / t.goals_total)}%` }} />
                </span>
              ) : null}
            </button>
          );
        })}
        {active.length > RAIL_ACTIVE_MAX ? (
          <div className={s["lv-rempty"]}>{`외 ${active.length - RAIL_ACTIVE_MAX}건`}</div>
        ) : null}
      </div>
      <div className={s.rsec}>
        <div className={s.rh}>최근 끝난 점검</div>
        {!status && !done.length ? <div className={s["lv-rempty"]}>끝난 점검 없음</div> : null}
        {done.map((t) => {
          const ph = taskPhase(t);
          const n = findingsTotal(t);
          const when = now ? relTime(t.completed_unix || t.created_unix, now) : "";
          const parts = [ph.tone === "ok" ? "" : ph.label, when, n ? `발견 ${n}` : "발견 없음"].filter(Boolean);
          return (
            <button
              key={t.id}
              className={s.rs}
              type="button"
              data-task={t.id}
              aria-current={cur(t, ["report"])}
              onClick={() => open(t, "report")}
            >
              <span className={cx(s.dot, s[ph.tone])} />
              <span className={s.host}>{taskLabel(t)}</span>
              <span className={s.rsub}>{parts.join(" · ")}</span>
            </button>
          );
        })}
      </div>
    </>
  );
}

/** 레일 아래 분석 모델 줄. URL 진단 서버면 이어 보는 진단의 설치 프로필 모델. */
export function LiveRailModel() {
  return useIsUrlMode() ? <UrlRailModel /> : <TaskRailModel />;
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

/** 레일 아래 분석 모델 줄: GET /api/llm 의 model, base_url 로 로컬/외부를 **추정**. */
function TaskRailModel() {
  const ready = useLiveAuth().state === "ready";
  const llm = usePoll(() => fetchLlm(), [], { intervalMs: 60_000, enabled: ready });
  let head = "분석 모델";
  let model: string | null = null;
  if (llm.unsupported) head = "분석 모델 · 확인 불가";
  else if (llm.data && !llm.data.configured) head = "분석 모델 · 미설정";
  else if (llm.data) {
    model = llm.data.model || "(이름 없음)";
    head = isLocalBaseUrl(llm.data.base_url) ? "분석 모델 · 로컬(추정)" : "분석 모델 · 외부(추정)";
  }
  return (
    <div className={s.rf} id="railModel">
      <Icon name="chip" />
      <div>
        {head}
        {model ? <b>{model}</b> : null}
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

/** 데이터 원천 스위치 (예시 재생 / 실제 엔진). MOCK 빌드에서는 숨김. */
export function SourceSwitch() {
  const source = useSource((st) => st.source);
  const setSource = useSource((st) => st.setSource);
  if (!canGoLive()) return null;
  return (
    // biome-ignore lint/a11y/useSemanticElements: 강조색 시안 묶음과 같은 모양을 쓰려고 div[role=group]
    <div className={s.swatches} role="group" aria-label="데이터 원천">
      <span>데이터</span>
      <div className={cx(s["sw-row"], s["lv-two"])}>
        <button className={s.sw} type="button" aria-pressed={source === "demo"} onClick={() => setSource("demo")}>
          예시 재생
        </button>
        <button className={s.sw} type="button" aria-pressed={source === "live"} onClick={() => setSource("live")}>
          실제 엔진
        </button>
      </div>
    </div>
  );
}
