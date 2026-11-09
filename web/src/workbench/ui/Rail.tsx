"use client";

// 왼쪽 레일: 메뉴 · 점검 목록 · 운영 상태 · 강조색 시안. 원본 aside#rail 과 그것을 고치던 syncDecisions · railNew · liveRender 부분.

import { cx } from "../lib/cx";
import { railApi } from "../lib/decisions";
import { railShop } from "../lib/liveState";
import type { View } from "../shell";
import { useShell } from "../shell";
import { type Accent, useCurHost, usePending, useWb } from "../store";
import s from "../workbench.module.css";
import { BrandMark, Icon } from "./icons";
import { ReportLink } from "./parts";

const SWATCHES: ReadonlyArray<readonly [Accent, string, string]> = [
  ["petrol", "#0F6D8A", "페트롤"],
  ["cobalt", "#2457D6", "코발트"],
  ["mono", "#E3EBEE", "모노"],
];

/** 강조색을 고르고 이 브라우저에만 기억 (원본 setAccent). */
function pickAccent(k: Accent) {
  useWb.getState().setAccent(k);
  try {
    localStorage.setItem("abc2lab-accent", k);
  } catch {
    // 저장소를 못 쓰면 이번만 적용
  }
}

export function Rail() {
  const { route, railOpen, railNewAtRoute, closeRail, dashPick } = useShell();
  const n = usePending();
  const appr = useWb((st) => st.S.appr);
  const phase = useWb((st) => st.railNew);
  const crawlDone = useWb((st) => st.crawl.done);
  const crawlPaused = useWb((st) => st.crawl.paused);
  const live = useWb((st) => st.live);
  const accent = useWb((st) => st.accent);
  const host = useCurHost();

  // 원본 route(): data-nav 목록에 지금 화면이 있고 그 링크가 숨겨지지 않았으면 aria-current="page"
  const cur = (nav: readonly View[], visible = true) => (visible && nav.includes(route) ? "page" : undefined);
  const shop = railShop(live);
  const api = railApi({ appr, path: null });
  const newRun = phase === "crawl" && !crawlDone && !crawlPaused;
  let newSub = "범위 확인 대기";
  if (phase === "crawl") newSub = crawlDone ? "탐색 완료 · 범위 확인 대기" : "자산 탐색 중";

  return (
    <aside className={cx(s.rail, railOpen && s.open)} id="rail" aria-label="주 메뉴">
      <div className={s["rail-top"]}>
        <a className={s.brand} href="#dashboard">
          <BrandMark />
          ABC2LAB
        </a>
        <button
          className={cx(s["icon-btn"], s["rail-x"])}
          type="button"
          data-rail-close=""
          aria-label="메뉴 닫기"
          onClick={closeRail}
        >
          <Icon name="x" />
        </button>
      </div>
      <a className={s["new-btn"]} href="#new" aria-current={cur(["new"])}>
        <Icon name="plus" />새 점검
      </a>
      <nav className={s.rnav} aria-label="화면">
        <a className={s.rl} href="#dashboard" aria-current={cur(["dashboard"])}>
          <Icon name="grid" />
          대시보드
          <span className={s.badge} id="railBadge" title="결정 대기" hidden={!n}>
            {n}
          </span>
        </a>
      </nav>
      <div className={s.rsec}>
        <div className={s.rh}>진행 중</div>
        <a
          className={s.rs}
          href={`#${phase ?? "crawl"}`}
          id="railNew"
          hidden={!phase}
          aria-current={cur(["crawl", "scope"], railNewAtRoute)}
        >
          <span className={cx(s.dot, newRun ? s.run : s.wait)} id="railNewDot" />
          <span className={cx(s.host, s["t-host"])}>{host}</span>
          <span className={s.rsub} id="railNewSub">
            {newSub}
          </span>
        </a>
        <a className={s.rs} href="#live" aria-current={cur(["live"])}>
          <span className={cx(s.dot, s[shop.dot])} id="railShopDot" />
          <span className={cx(s.host, s["t-host"])}>{host}</span>
          <span className={s.rsub} id="railShopSub">
            {shop.sub}
          </span>
          <span className={s.rbar}>
            <i id="railShopBar" style={{ width: `${shop.pct}%` }} />
          </span>
        </a>
        <button className={s.rs} type="button" onClick={() => dashPick("appr")}>
          <span className={cx(s.dot, s[api.dot])} id="railApiDot" />
          <span className={s.host}>api.store-dev.com</span>
          <span className={s.rsub} id="railApiSub">
            {api.sub}
          </span>
          <span className={cx(s.rbar, api.wait && s.wait)} id="railApiBar">
            <i style={{ width: "35%" }} />
          </span>
        </button>
      </div>
      <div className={s.rsec}>
        <div className={s.rh}>최근 끝난 점검</div>
        <ReportLink kind="blog" className={s.rs} current={cur(["report"])}>
          <span className={cx(s.dot, s.ok)} />
          <span className={s.host}>blog.example.com</span>
          <span className={s.rsub}>10/25 · 발견 6</span>
        </ReportLink>
        <button className={s.rs} type="button" onClick={() => dashPick("done-docs")}>
          <span className={cx(s.dot, s.stop)} />
          <span className={s.host}>docs.example.com</span>
          <span className={s.rsub}>10/24 · 발견 없음</span>
        </button>
        <button className={s.rs} type="button" onClick={() => dashPick("done-app")}>
          <span className={cx(s.dot, s.stop)} />
          <span className={s.host}>app.sample.io</span>
          <span className={s.rsub}>10/23 · 판단 불가 2</span>
        </button>
      </div>
      <div className={s["rail-foot"]}>
        <div className={s.rf}>
          <Icon name="chip" />
          <div>
            분석 모델 · 로컬<b>qwen/qwen3.6-35b-a3b</b>
          </div>
        </div>
        <div className={s.rf}>
          <Icon name="shield" />
          <div>
            완전 로컬 모드
            <br />
            상태 변경은 승인 후 전송
          </div>
        </div>
        {/* biome-ignore lint/a11y/useSemanticElements: 원본 마크업 그대로 (fieldset 으로 바꾸면 모양이 달라짐) */}
        <div className={s.swatches} role="group" aria-label="강조색 시안">
          <span>강조색 시안</span>
          <div className={s["sw-row"]}>
            {SWATCHES.map(([k, color, label]) => (
              <button key={k} className={s.sw} type="button" aria-pressed={accent === k} onClick={() => pickAccent(k)}>
                <i style={{ background: color }} />
                {label}
              </button>
            ))}
          </div>
        </div>
        <div className={s.mock}>시안 화면 · 모든 숫자와 기록은 예시입니다</div>
      </div>
    </aside>
  );
}
