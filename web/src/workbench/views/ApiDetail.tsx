"use client";

// API 상세 (기본 정보 · 요청·응답 예시 탭). 범위 확인의 상세 패널과 자산 탐색의 찾은 경로 패널이 함께 쓴다.
// 원본 apiDetail(e, px) · scopeTag · whyText · selApiTab(+화살표 키). px 는 id 접두어('s' 범위 · 'c' 자산 탐색).

import { type KeyboardEvent, type ReactNode, useRef } from "react";

import { GL, OFF } from "../data/crawl";
import { type Endpoint, epOn } from "../data/scope";
import { type ApiTab, useApiTab } from "../lib/apiTab";
import { cx } from "../lib/cx";
import { exFor } from "../lib/exFor";
import { useCurHost, useWb } from "../store";
import { Icon } from "../ui/icons";
import { CodeCard, Ep, InspHead, Isec, Kv } from "../ui/parts";
import s from "../workbench.module.css";

/** 범위 표시 딱지. 원본 `scopeTag(e)` (on = 지금 포함 여부). */
export function ScopeTag({ e, on }: { e: Endpoint; on: boolean }) {
  if (e.lock) {
    return (
      <span className={cx(s.tag, s.ex)}>
        <Icon name="lock" />
        전역 제외
      </span>
    );
  }
  const off = OFF[`${e.m} ${e.p}`];
  if (!on) return <span className={cx(s.tag, s.ex)}>제외{off ? ` · ${off}` : ""}</span>;
  if (e.k === "login") return <span className={cx(s.tag, s.lg)}>테스트 계정</span>;
  if (e.chg) return <span className={cx(s.tag, s.ap)}>포함 · 보낼 때 승인</span>;
  return <span className={cx(s.tag, s.in)}>자동 포함</span>;
}

/** 처리 설명. 원본 `whyText(e)`. */
function whyText(e: Endpoint, on: boolean): ReactNode {
  if (e.lock) {
    return (
      <>
        전역 제외 규칙 <code>/admin/delete*</code>에 걸려 이 점검에서는 보내지 않습니다. 규칙은 설정 › 통제 정책에서
        바꿉니다.
      </>
    );
  }
  if (!on) {
    const off = OFF[`${e.m} ${e.p}`];
    if (!off) return "직접 제외했습니다. 체크하면 다시 포함합니다.";
    const what = off
      .replace("세션이 끊김", "세션이 끊깁니다")
      .replace("메일 발송", "실제 메일이 발송됩니다")
      .replace("삭제 요청", "데이터가 지워집니다");
    return `보내면 ${what}. 그래서 기본으로 제외했습니다. 범위 확인에서 체크하면 포함하고, 보낼 때 승인을 받습니다.`;
  }
  if (e.k === "login") return "입력한 테스트 계정으로만 로그인합니다.";
  if (e.chg) return "데이터를 바꾸는 요청이라 범위에 넣어도 보낼 때마다 승인을 받습니다.";
  return "보기만 하는 요청이라 자동으로 포함합니다.";
}

/** 탭 사이 이동 키 → 방향 */
const STEP: Partial<Record<string, number>> = { ArrowRight: 1, ArrowLeft: -1 };

const TABS: ReadonlyArray<readonly [ApiTab, string]> = [
  ["info", "기본 정보"],
  ["ex", "요청·응답 예시"],
];

/**
 * API 상세 본문 (머리 · 탭 · 두 탭 패널). 원본 `apiDetail(e, px)`.
 * 탭은 두 화면이 같은 값을 쓴다(원본 전역 apiTab). 좌우 화살표로 옮기면 그 탭을 고르고 포커스한다.
 */
export function ApiDetail({ e, px }: { e: Endpoint; px: "s" | "c" }) {
  const on = useWb((st) => epOn(e, st.scopeOn));
  const host = useCurHost();
  const tab = useApiTab((t) => t.tab);
  const setTab = useApiTab((t) => t.setTab);
  const listRef = useRef<HTMLDivElement>(null);
  const x = exFor(e, host);

  // 원본 keydown: 누른 탭 기준으로 좌우 이동(끝에서 돌아감), 고르고 포커스
  const onKeyDown = (ev: KeyboardEvent<HTMLButtonElement>, k: ApiTab) => {
    const d = STEP[ev.key];
    if (!d) return;
    ev.preventDefault();
    const at = TABS.findIndex((t) => t[0] === k);
    const next = TABS[(at + d + TABS.length) % TABS.length][0];
    setTab(next);
    listRef.current?.querySelector<HTMLElement>(`[data-apitab="${next}"]`)?.focus();
  };

  const src = x.sent ? "예시 · 관찰" : "예시 · AI 추정";
  return (
    <>
      <InspHead
        eyebrow={`찾은 경로 · ${GL[e.g]}`}
        title={<Ep m={e.m} p={e.p} />}
        chips={
          <>
            <ScopeTag e={e} on={on} />
            {e.hasId ? <span className={cx(s.tag, s.out)}>ID 포함</span> : null}
            {e.auth ? <span className={cx(s.tag, s.out)}>로그인 필요</span> : null}
          </>
        }
      />
      <div className={cx(s["seg-tabs"], s["api-tabs"])} role="tablist" aria-label="경로 상세" ref={listRef}>
        {TABS.map(([k, label]) => {
          const sel = tab === k;
          return (
            <button
              key={k}
              type="button"
              role="tab"
              id={`${px}-t-${k}`}
              aria-controls={`${px}-p-${k}`}
              aria-selected={sel}
              tabIndex={sel ? undefined : -1}
              data-apitab={k}
              onClick={() => setTab(k)}
              onKeyDown={(ev) => onKeyDown(ev, k)}
            >
              {label}
            </button>
          );
        })}
      </div>
      <div role="tabpanel" id={`${px}-p-info`} aria-labelledby={`${px}-t-info`} hidden={tab !== "info"}>
        <Isec t="관찰한 사실">
          <Kv
            rows={[
              ["응답", e.st],
              ["찾은 곳", e.from],
              ["요청 횟수", `${e.n}회`],
              ["근거", e.basis],
            ]}
          />
        </Isec>
        <Isec t="AI 추정">
          <Kv
            rows={[
              ["기능", e.d],
              ["묶음", GL[e.g]],
            ]}
          />
        </Isec>
        <Isec t="처리">
          <p>{whyText(e, on)}</p>
        </Isec>
      </div>
      <div role="tabpanel" id={`${px}-p-ex`} aria-labelledby={`${px}-t-ex`} hidden={tab !== "ex"}>
        <div className={s.expanel}>
          <p className={s.help}>{x.note}</p>
          <CodeCard title="요청 예시" src={x.sent ? "예시 · 관찰" : "예시 · 찾은 모양"} text={x.req} id={`${px}-req`} />
          {x.res !== null ? (
            <CodeCard title="응답 예시" src={src} text={x.res} id={`${px}-res`} />
          ) : (
            <div className={s.cb}>
              <div className={s["cb-h"]}>
                <h3>응답 예시</h3>
                <span className={s["cb-src"]}>없음</span>
              </div>
              <div className={s["cb-none"]}>
                <Icon name="lock" />
                <span>{x.none}</span>
              </div>
            </div>
          )}
        </div>
      </div>
    </>
  );
}
