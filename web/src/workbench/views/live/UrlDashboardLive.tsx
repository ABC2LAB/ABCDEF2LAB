"use client";

// URL 진단(v2) 서버의 대시보드. 이 서버에는 진단 목록 · 집계 API 가 없어(catalog · 영수증 조회 · 진단별 상태만)
// 이 브라우저 탭이 이어 보는 진단 하나와 설치된 진단 프로필만 실제 값으로 보이고, 나머지는 미제공으로 둔다.

import { urlPhaseLabels } from "@/lib/diagnosis/diagnosis-url";

import { cx } from "../../lib/cx";
import { ModeNote } from "../../live/parts";
import { useUrl } from "../../live/urlDiagnosis";
import { urlHost, urlViewFor } from "../../live/urlMap";
import { useShell } from "../../shell";
import { Icon } from "../../ui/icons";
import s from "../../workbench.module.css";
import { UrlHead, UrlNotices, UrlPill } from "./urlParts";

/** 이어 보는 진단 한 줄 (누르면 상태에 맞는 화면으로). */
function CurrentJob() {
  const { goTo } = useShell();
  const handle = useUrl((st) => st.handle);
  const job = useUrl((st) => st.job);
  const verified = useUrl((st) => st.verified);
  const busy = useUrl((st) => st.busy);
  if (!handle) {
    return (
      <>
        <p className={s.empty}>이 브라우저 탭에서 이어 보는 URL 진단이 없습니다.</p>
        <UrlNotices />
      </>
    );
  }
  const to = job ? urlViewFor(job) : "live";
  const name = handle.input.display_name?.trim();
  let sub = busy ? "상태 확인 중" : "상태 미확인";
  if (job) sub = `${urlPhaseLabels[job.phase]}${job.task_id ? ` · 실제 task #${job.task_id}` : ""}`;
  return (
    <>
      <div className={s.list}>
        <button className={s.row} type="button" data-url-job={job?.id ?? ""} onClick={() => goTo(to)}>
          <span className={s["r-main"]}>
            <span className={s["r-t"]}>
              <span className={s["r-host"]}>{urlHost(handle.input.target_url)}</span>
            </span>
            <span className={s["r-s"]}>{name ? `${name} · ${sub}` : sub}</span>
          </span>
          <span className={s["r-side"]}>
            {job ? <UrlPill job={job} verified={verified} /> : <span className={cx(s.pill, s.wait)}>상태 미확인</span>}
          </span>
        </button>
      </div>
      <UrlNotices />
    </>
  );
}

/** live 대시보드 (URL 진단 서버). */
export function UrlDashboardLive() {
  const cap = useUrl((st) => st.cap);
  const profiles = cap.kind === "v2" ? cap.catalog.profiles : [];
  return (
    <>
      <UrlHead
        title="대시보드"
        crumb="대시보드"
        acts={
          <a className={cx(s.btn, s.pri)} href="#new">
            <Icon name="plus" />새 점검
          </a>
        }
      />
      <div className={s.vm}>
        <section className={s.sec} aria-labelledby="luDashJobH">
          <div className={s.sh}>
            <h2 id="luDashJobH">이어 보는 URL 진단</h2>
          </div>
          <CurrentJob />
          <p className={s.help}>
            이 서버에는 진단 목록 API 가 없어, 이 브라우저 탭이 영수증으로 이어 보는 진단만 보입니다. 다른 브라우저 ·
            탭에서 시작한 진단은 여기에 나오지 않습니다.
          </p>
        </section>
        <section className={s.sec} aria-labelledby="luDashProfH">
          <div className={s.sh}>
            <h2 id="luDashProfH">설치된 진단 프로필</h2>
            <span className={s.n}>{`${profiles.length}개`}</span>
          </div>
          {profiles.length ? (
            <div className={s.list}>
              {profiles.map((p) => (
                <div key={p.id} className={s.row}>
                  <span className={s["r-main"]}>
                    <span className={s["r-t"]}>{p.label || p.id}</span>
                    <span className={s["r-s"]}>{p.model_label || p.model_profile_ref}</span>
                  </span>
                  <span className={cx(s["r-side"], s.mono)}>{p.id}</span>
                </div>
              ))}
            </div>
          ) : (
            <p className={s.empty}>설치된 진단 프로필이 없습니다. 설치자 설정이 필요합니다.</p>
          )}
          <p className={s.help}>모델 · 대상 · 예산은 설치된 승인 범위가 정합니다. 화면에서 바꿀 수 없습니다.</p>
        </section>
        <section className={s.sec} aria-labelledby="luDashMoreH">
          <div className={s.sh}>
            <h2 id="luDashMoreH">점검 목록 · 최근 30일 집계 · 승인 대기 · 토큰 사용량</h2>
          </div>
          <ModeNote what="점검 목록 · 집계" />
        </section>
      </div>
    </>
  );
}
