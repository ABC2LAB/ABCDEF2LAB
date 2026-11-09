"use client";

// 여러 화면이 함께 쓰는 작은 부품. 원본 js 의 문자열 헬퍼(ep · pth · sev · vd · head · isec · kv · closeBtn ·
// buildPhases · copyText · cbCard · hlHttp)를 JSX 로 옮긴 것. 문구·속성은 원본과 같다.

import { Fragment, type ReactNode, useEffect, useState } from "react";

import { type LiveVerdict, SEVL, type SevKey, type Verdict as VerdictValue } from "../data/findings";
import { cx } from "../lib/cx";
import { useUrl } from "../live/urlDiagnosis";
import { useShell } from "../shell";
import s from "../workbench.module.css";
import { Icon } from "./icons";

/** 경로 안 `{param}` 을 <i> 로 감싼다. 원본 `pth(p)`. */
export function Pth({ p }: { p: string }) {
  const parts = p.split(/(\{\w+\})/);
  return (
    <>
      {parts.map((x, i) =>
        // 홀수 자리가 {param}
        i % 2 ? (
          // biome-ignore lint/suspicious/noArrayIndexKey: 고정 문자열을 쪼갠 조각이라 순서가 곧 정체성
          <i key={i}>{x}</i>
        ) : (
          x
        ),
      )}
    </>
  );
}

/** 메서드 + 경로. 원본 `ep(m,p)` = `<span class="m GET">GET</span> <span class="p">…</span>`. */
export function Ep({ m, p }: { m: string; p: string }) {
  return (
    <>
      <span className={cx(s.m, s[m])}>{m}</span>{" "}
      <span className={s.p}>
        <Pth p={p} />
      </span>
    </>
  );
}

/** 진행 작업의 대상. "GET /path" 꼴이면 Ep, 아니면 <code>. 원본 `onHtml(on)` (대시보드 · 점검 진행). */
export function On({ on }: { on: string }) {
  const m = on.match(/^(GET|POST|PUT|PATCH|DELETE) (.+)$/);
  return m ? <Ep m={m[1]} p={m[2]} /> : <code>{on}</code>;
}

/** 위험도 딱지. 원본 `sev(k)`. 글자를 바꾸려면 children (대시보드의 '심각 1'). */
export function Sev({ k, children }: { k: SevKey; children?: ReactNode }) {
  return <span className={cx(s.sev, s[k])}>{children ?? SEVL[k]}</span>;
}

/** 판정 딱지. 원본 `vd(v)`: 의심 → sus, 판단 불가 → und. */
export function Verdict({ v }: { v: VerdictValue }) {
  return <span className={cx(s.vd, v === "의심" && s.sus, v === "판단 불가" && s.und)}>{v}</span>;
}

/** 진행 중 판정 딱지. 원본 `vdLive(v)`: '판정 중' 은 und 모양. */
export function VerdictLive({ v }: { v: LiveVerdict }) {
  return v === "판정 중" ? <span className={cx(s.vd, s.und)}>판정 중</span> : <Verdict v={v} />;
}

/**
 * 상세 서랍 닫기 버튼 (좁은 화면에서만 보임). 원본 `closeBtn(label)`.
 * data-insp-close 는 openInsp 가 포커스할 버튼을 찾는 표식이라 지우지 말 것.
 */
export function CloseInspButton({ label }: { label: string }) {
  const { closeInsp } = useShell();
  return (
    <button
      className={cx(s["icon-btn"], s["insp-only"])}
      type="button"
      data-insp-close=""
      aria-label={`${label} 닫기`}
      onClick={closeInsp}
    >
      <Icon name="x" />
    </button>
  );
}

/** 원본의 `x ? … : ''` 처럼 비어 있지 않은 내용인가 (null · undefined · false · "" 는 빈 것). */
export function has(x: ReactNode): boolean {
  return x !== null && x !== undefined && x !== false && x !== "";
}

/** 상세 패널 머리 (작은 제목 · h2 · 닫기 버튼 '상세 닫기' · 딱지 줄). 원본 `head(eyebrow,title,chips)`. */
export function InspHead({ eyebrow, title, chips }: { eyebrow?: ReactNode; title: ReactNode; chips?: ReactNode }) {
  return (
    <>
      <div className={s["vi-head"]}>
        <div>
          {has(eyebrow) ? <div className={s.eyebrow}>{eyebrow}</div> : null}
          <h2>{title}</h2>
        </div>
        <CloseInspButton label="상세" />
      </div>
      {has(chips) ? <div className={s["vi-chips"]}>{chips}</div> : null}
    </>
  );
}

/** 상세 패널의 한 단락 (h3 는 t 가 있을 때만). 원본 `isec(t,body)`. */
export function Isec({ t, children }: { t?: ReactNode; children?: ReactNode }) {
  return (
    <div className={s.isec}>
      {has(t) ? <h3>{t}</h3> : null}
      {children}
    </div>
  );
}

/** 항목-값 목록. 원본 `kv(rows)`. */
export function Kv({ rows }: { rows: ReadonlyArray<readonly [ReactNode, ReactNode]> }) {
  return (
    <dl className={s.kv}>
      {rows.map(([k, v], i) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: 고정된 줄 목록
        <Fragment key={i}>
          <dt>{k}</dt>
          <dd>{v}</dd>
        </Fragment>
      ))}
    </dl>
  );
}

/** 화면 머리의 메뉴 버튼 (휴대폰에서만 보임) → 레일 서랍. 원본 `button.menu-btn[data-rail-open]`. */
export function MenuButton() {
  const { openRail } = useShell();
  return (
    <button className={cx(s["icon-btn"], s["menu-btn"])} type="button" aria-label="메뉴 열기" onClick={openRail}>
      <Icon name="menu" />
    </button>
  );
}

/** 화면 머리의 '상세' 버튼 (좁은 화면에서만 보임) → 상세 서랍. 원본 `button.btn.insp-only[data-insp-open]`. */
export function InspOpenButton({ children, id }: { children: ReactNode; id?: string }) {
  const { openInsp } = useShell();
  return (
    <button className={cx(s.btn, s["insp-only"])} type="button" id={id} onClick={openInsp}>
      <Icon name="panel" />
      {children}
    </button>
  );
}

/**
 * 보고서로 가는 링크 (`href="#report"`). 누르면 먼저 보고서 대상을 바꾸고(원본 data-rhost → setReportHost) 이동은 href 가 한다.
 * kind: blog = 끝난 예시 점검(레일 · 대시보드) · live = 방금 끝낸 점검(단계 표시 · 진행 화면 '보고서 보기').
 */
export function ReportLink({
  kind,
  className,
  current,
  children,
}: {
  kind: "blog" | "live";
  className?: string;
  /** aria-current 값 (레일은 "page", 단계 표시는 "step") */
  current?: "page" | "step";
  children: ReactNode;
}) {
  return (
    // biome-ignore lint/a11y/useValidAnchor: 해시 라우팅 링크라 이동은 href 가 하고, onClick 은 보고서 대상만 바꾼다
    <a className={className} href="#report" aria-current={current}>
      {children}
    </a>
  );
}

/** 단계: 자산 탐색 → 범위 확인 → 점검 진행 → 보고서 */
export const PH = [
  ["crawl", "자산 탐색"],
  ["scope", "범위 확인"],
  ["live", "점검 진행"],
  ["report", "보고서"],
] as const;
export type PhaseKey = (typeof PH)[number][0];

/**
 * 단계 표시 `nav.phases`. 원본 `buildPhases()`: 지난 단계는 체크·done, 지금 단계는 aria-current="step",
 * 보고서는 점검이 끝나거나(done) 종료(ended)되기 전에는 링크가 아닌 span.later. 보고서 링크를 누르면 setReportHost('live').
 */
export function Phases({ phase }: { phase: PhaseKey }) {
  const rep = useUrl((st) => st.job?.result_ready === true);
  const at = PH.findIndex((p) => p[0] === phase);
  return (
    <nav className={s.phases} aria-label="점검 단계" data-phase={phase}>
      {PH.map(([k, label], i) => {
        const n = i < at ? <Icon name="check" /> : i + 1;
        if (k === "report" && !rep) {
          return (
            <span key={k} className={cx(s.ph, s.later)} title="점검이 끝나면 만들어집니다">
              <span className={s.n}>{n}</span>
              {label}
            </span>
          );
        }
        const inner = (
          <>
            <span className={s.n}>{n}</span>
            {label}
          </>
        );
        if (k === "report") {
          return (
            <ReportLink
              key={k}
              kind="live"
              className={cx(s.ph, i < at && s.done)}
              current={i === at ? "step" : undefined}
            >
              {inner}
            </ReportLink>
          );
        }
        return (
          <a key={k} className={cx(s.ph, i < at && s.done)} href={`#${k}`} aria-current={i === at ? "step" : undefined}>
            {inner}
          </a>
        );
      })}
    </nav>
  );
}

/**
 * 복사 버튼. 원본 `copyText(id, btn)`: id 요소의 글자를 클립보드로, 되면 1.2초 동안 '복사됨'.
 * 클립보드를 못 쓰면 그 요소의 글자를 선택해 둔다(직접 복사하도록).
 */
export function CopyButton({
  targetId,
  className,
  children = "복사",
}: {
  /** 복사할 요소의 id (그 요소의 textContent 를 누를 때 읽는다) */
  targetId: string;
  /** 덧붙일 클래스 (예: s.copy) */
  className?: string;
  children?: ReactNode;
}) {
  const [copied, setCopied] = useState(0);
  useEffect(() => {
    if (!copied) return;
    const t = window.setTimeout(() => setCopied(0), 1200);
    return () => window.clearTimeout(t);
  }, [copied]);
  const onClick = () => {
    const el = document.getElementById(targetId);
    if (!el) return;
    const txt = el.textContent ?? "";
    const done = () => setCopied((n) => n + 1);
    const fallback = () => {
      try {
        const r = document.createRange();
        r.selectNodeContents(el);
        const sel = window.getSelection();
        sel?.removeAllRanges();
        sel?.addRange(r);
      } catch {
        // 선택도 안 되면 그대로 둔다
      }
    };
    try {
      void navigator.clipboard.writeText(txt).then(done, fallback);
    } catch {
      fallback();
    }
  };
  return (
    <button className={cx(s.btn, s.sm, className)} type="button" onClick={onClick}>
      {copied ? "복사됨" : children}
    </button>
  );
}

// ---------- HTTP 예시 강조 (원본 hlHttp) ----------

/** `<한글…>` 자리표시자를 c-ph 로. (원본은 이스케이프된 &lt;…&gt; 에 같은 규칙을 걸었다) */
function placeholders(seg: string, key: string): ReactNode[] {
  const out: ReactNode[] = [];
  let last = 0;
  let n = 0;
  for (const m of seg.matchAll(/<([가-힣][^&<>]*?)>/g)) {
    const at = m.index ?? 0;
    if (at > last) out.push(seg.slice(last, at));
    out.push(
      <span key={`${key}p${n++}`} className={s["c-ph"]}>
        {`<${m[1]}>`}
      </span>,
    );
    last = at + m[0].length;
  }
  if (last < seg.length) out.push(seg.slice(last));
  return out;
}

/** ⟦강조⟧ 를 c-hl 로, 그 안팎의 자리표시자도 처리. */
function inline(str: string, key: string): ReactNode[] {
  const out: ReactNode[] = [];
  str.split(/⟦(.*?)⟧/).forEach((seg, j) => {
    const ph = placeholders(seg, `${key}s${j}`);
    if (j % 2) {
      out.push(
        // biome-ignore lint/suspicious/noArrayIndexKey: 고정 문자열을 쪼갠 조각이라 순서가 곧 정체성
        <span key={`${key}h${j}`} className={s["c-hl"]}>
          {ph}
        </span>,
      );
    } else out.push(...ph);
  });
  return out;
}

/**
 * HTTP 요청·응답 글을 줄별로 강조. 원본 `hlHttp(text)`:
 * 첫 줄 c-rl, 빈 줄 전까지 헤더 이름 c-hk, 그 뒤 본문의 JSON 키 c-jk, `<자리표시자>` c-ph, ⟦강조⟧ c-hl.
 */
export function hlHttp(text: string): ReactNode {
  let inBody = false;
  return text.split("\n").map((l, i) => {
    const nl = i ? "\n" : "";
    const key = `l${i}`;
    let body: ReactNode;
    if (i === 0) body = <span className={s["c-rl"]}>{l}</span>;
    else if (!inBody && l === "") {
      inBody = true;
      body = null;
    } else if (!inBody) {
      const m = l.match(/^([A-Za-z-]+:)/);
      body = m ? (
        <>
          <span className={s["c-hk"]}>{m[1]}</span>
          {inline(l.slice(m[1].length), key)}
        </>
      ) : (
        inline(l, key)
      );
    } else {
      const m = l.match(/^(\s*)("[^"]+")(:)/);
      body = m ? (
        <>
          {m[1]}
          <span className={s["c-jk"]}>{inline(m[2], `${key}k`)}</span>
          {m[3]}
          {inline(l.slice(m[0].length), key)}
        </>
      ) : (
        inline(l, key)
      );
    }
    return (
      <Fragment key={key}>
        {nl}
        {body}
      </Fragment>
    );
  });
}

/**
 * 요청·응답 예시 카드 (제목 · 출처 · 복사 버튼 · 줄 번호 · 강조한 본문). 원본 `cbCard(title, src, text, id)`.
 * id 는 문서 안에서 유일해야 한다(복사 대상). 원본은 접두어로 구분: 자산 탐색 'c-req' · 범위 's-req' 등.
 */
export function CodeCard({ title, src, text, id }: { title: ReactNode; src: ReactNode; text: string; id: string }) {
  const n = text.replace(/[⟦⟧]/g, "").split("\n").length;
  const gutter = Array.from({ length: n }, (_, i) => i + 1).join("\n");
  return (
    <div className={s.cb}>
      <div className={s["cb-h"]}>
        <h3>{title}</h3>
        <span className={s["cb-src"]}>{src}</span>
        <CopyButton targetId={id} />
      </div>
      <div className={s["cb-b"]}>
        <pre className={s["cb-g"]} aria-hidden="true">
          {gutter}
        </pre>
        <pre className={s["cb-c"]} id={id}>
          {hlHttp(text)}
        </pre>
      </div>
    </div>
  );
}
