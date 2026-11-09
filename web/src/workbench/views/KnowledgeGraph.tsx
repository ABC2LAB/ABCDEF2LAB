"use client";

// 보고서 '자산 지식 그래프' 탭 내용. 원본 renderKg · kgSvg · legSw · kgApply 를 JSX 로 옮긴 것.
// 고른 것(sel)과 고르는 동작은 보고서 화면이 갖고, 여기서는 그리기와 강조(has-sel · on · sel · aria-pressed)만 한다.

import { type KeyboardEvent, useMemo } from "react";

import { findingById, SEVL } from "../data/findings";
import {
  KG_BY_ID,
  KGE,
  KGN,
  KGP,
  type KgNode,
  type KgSel,
  type KgState,
  KX,
  kbox,
  kcurve,
  kgCount,
  kgHighlight,
  kname,
  kstate,
} from "../data/kg";
import { cx } from "../lib/cx";
import { Sev, Verdict } from "../ui/parts";
import s from "../workbench.module.css";

const H = 490;
const STATES: readonly KgState[] = ["ok", "block", "vuln", "sus", "untested", "own"];

// 좌표는 고정 데이터에서 한 번만 계산한다 (원본은 그릴 때마다 같은 값을 계산)
const EDGES = KGE.map((e, i) => ({ i, st: e[2], d: kcurve(KG_BY_ID[e[0]], KG_BY_ID[e[1]]).d }));

/** 발견 표시: 발견마다 처음 나오는 관계 위 한 곳 (역할에서 나가면 0.78, 아니면 곡선 가운데). */
const BADGES = KGE.flatMap((e, i) => {
  const fid = e[4];
  if (!fid || KGE.findIndex((x) => x[4] === fid) !== i) return [];
  const f = findingById(fid);
  if (!f) return [];
  const a = KG_BY_ID[e[0]];
  const pt = kcurve(a, KG_BY_ID[e[1]]).at(a.t === "role" ? 0.78 : 0.5);
  return [{ i, f, pt }];
});

const BW = 36;

/** 노드 모양 (역할 알약 · 엔드포인트 상자 · 자원 접힌 종이). 원본 kgSvg 의 inner. */
function NodeShape({ n }: { n: KgNode }) {
  const b = kbox(n);
  if (n.t === "role") {
    return (
      <>
        <rect className={s.sh} x={b.x} y={b.y} width={b.w} height={b.h} rx="23" />
        <text className={s["t-lab"]} x={KX.role} y={n.y - 2} textAnchor="middle">
          {n.label}
        </text>
        <text className={s["t-sub"]} x={KX.role} y={n.y + 13} textAnchor="middle">
          {n.sub.split(" · ")[0]}
        </text>
      </>
    );
  }
  if (n.t === "ep") {
    return (
      <>
        <rect className={s.sh} x={b.x} y={b.y} width={b.w} height={b.h} rx="5" />
        <text x={b.x + 12} y={n.y + 4}>
          <tspan className={cx(s["t-m"], s[n.m])}>{n.m}</tspan>
          <tspan className={s["t-p"]} dx="8">
            {n.p}
          </tspan>
        </text>
      </>
    );
  }
  return (
    <>
      <path className={s.sh} d={`M${b.x} ${b.y}H${b.x + b.w - 12}L${b.x + b.w} ${b.y + 12}V${b.y + b.h}H${b.x}Z`} />
      <path className={s.fold} d={`M${b.x + b.w - 12} ${b.y}V${b.y + 12}H${b.x + b.w}`} />
      <text className={s["t-lab"]} x={b.x + 12} y={n.y - 3}>
        {n.label}
      </text>
      <text className={s["t-sub"]} x={b.x + 12} y={n.y + 13}>
        {n.sub.split(" · ")[0]}
      </text>
    </>
  );
}

/** 범례 선 견본. 원본 `legSw(cls)`. */
function LegSw({ st }: { st: KgState }) {
  return (
    <svg width="26" height="8" aria-hidden="true">
      <path className={cx(s["kg-e"], s[`e-${st}`])} d="M1 4H25" style={{ opacity: 1 }} />
    </svg>
  );
}

/** Enter · Space 로 누르기 (원본 keydown: [data-kgn],[data-kgb] 에 click 을 보냄) */
function pressKey(e: KeyboardEvent, go: () => void) {
  if (e.key !== "Enter" && e.key !== " ") return;
  e.preventDefault();
  go();
}

export function KnowledgeGraph({
  host,
  sel,
  onNode,
  onFinding,
  onPath,
  onClear,
}: {
  /** 보고서 대상 host (원본 rHost) */
  host: string;
  /** 고른 것 (원본 kgSel) */
  sel: KgSel;
  /** 노드 누름 (원본 kgPickNode) */
  onNode: (id: string) => void;
  /** 발견 표시 · 발견 버튼 누름 (원본 kgPickFinding) */
  onFinding: (id: string) => void;
  /** 공격 경로 누름 (원본 data-kgp) */
  onPath: (id: string) => void;
  /** '전체 보기' (원본 data-kgclear) */
  onClear: () => void;
}) {
  const { nodes, edges } = useMemo(() => kgHighlight(sel), [sel]);
  const f6 = findingById("f6");
  return (
    <>
      <div className={s.kgtop}>
        <span>
          대상 <span className={s.mono}>{host}</span>
        </span>
        <span>
          응답 헤더 <code>Server: nginx/1.21.0</code>
        </span>
        <button className={s.kgf} type="button" data-kgf="f6" onClick={() => onFinding("f6")}>
          <Sev k="lo" />
          {f6?.title}
        </button>
      </div>
      <p className={s.lead}>
        점검 중 실제로 주고받은 요청과 응답으로 만든 소유·권한 관계입니다. 누가(역할) 어떤 엔드포인트로 어떤 자원에
        닿았는지 이어 보면 공격 경로가 보입니다. 노드나 발견 표시를 누르면 오른쪽에 관계가 나옵니다.
      </p>
      <div className={s.sec}>
        <div className={s.sh}>
          <h2>확인된 공격 경로</h2>
          <span className={s.n}>{KGP.length}</span>
        </div>
        <div className={s.kgpaths}>
          {KGP.map((p) => {
            const f = findingById(p.f);
            return (
              <button
                key={p.id}
                className={s.kgp}
                type="button"
                data-kgp={p.id}
                aria-pressed={sel?.type === "path" && sel.id === p.id}
                onClick={() => onPath(p.id)}
              >
                <b>
                  {f ? <Sev k={f.sev} /> : null}
                  {f ? <Verdict v={f.vd} /> : null}
                </b>
                <b>{p.label}</b>
                <small>{p.sub}</small>
              </button>
            );
          })}
        </div>
      </div>
      <div className={s.sec}>
        <div className={s.sh}>
          <h2>관계 그래프</h2>
          <span className={s.aside}>역할 4 · 엔드포인트 7 · 자원 6 · 관계 {KGE.length}</span>
        </div>
        <div className={s.kgwrap}>
          {/* biome-ignore lint/a11y/useSemanticElements: SVG 는 fieldset 으로 바꿀 수 없어 원본처럼 role="group" */}
          <svg
            className={cx(s.kg, sel && s["has-sel"])}
            id="kgSvg"
            viewBox={`0 0 860 ${H}`}
            role="group"
            aria-label="자산 지식 그래프. 역할에서 엔드포인트를 거쳐 자원으로 이어지는 관계"
          >
            <defs>
              {STATES.map((k) => (
                <marker
                  key={k}
                  id={`ah-${k}`}
                  viewBox="0 0 8 8"
                  refX="6"
                  refY="4"
                  markerWidth="7"
                  markerHeight="7"
                  orient="auto-start-reverse"
                >
                  <path className={cx(s.ah, s[k])} d="M0 0L8 4L0 8z" />
                </marker>
              ))}
            </defs>
            <text className={s["t-col"]} x={KX.role} y="20" textAnchor="middle">
              계정 · 역할
            </text>
            <text className={s["t-col"]} x={KX.ep} y="20" textAnchor="middle">
              엔드포인트
            </text>
            <text className={s["t-col"]} x={KX.res} y="20" textAnchor="middle">
              자원 · 이름은 AI 추정
            </text>
            {/* biome-ignore lint/a11y/noAriaHiddenOnFocusable: 관계 선(path)은 포커스를 받지 않는다 — 원본처럼 읽기 도구에서 숨김 */}
            <g aria-hidden="true">
              {EDGES.map((e) => (
                <path
                  key={e.i}
                  className={cx(s["kg-e"], s[`e-${e.st}`], edges.has(e.i) && s.on)}
                  data-ei={e.i}
                  d={e.d}
                  markerEnd={`url(#ah-${e.st})`}
                />
              ))}
            </g>
            <g>
              {KGN.map((n) => {
                const rels = KGE.filter((e) => e[0] === n.id || e[1] === n.id).length;
                return (
                  // biome-ignore lint/a11y/useSemanticElements: SVG 안이라 <button> 을 쓸 수 없어 원본처럼 role="button" 인 g
                  <g
                    key={n.id}
                    className={cx(
                      s["kg-n"],
                      s[n.t],
                      kstate(n.id) && s[kstate(n.id)],
                      nodes.has(n.id) && s.on,
                      sel?.type === "node" && sel.id === n.id && s.sel,
                    )}
                    data-kgn={n.id}
                    tabIndex={0}
                    role="button"
                    aria-label={`${kname(n)}, 관계 ${rels}개`}
                    onClick={() => onNode(n.id)}
                    onKeyDown={(e) => pressKey(e, () => onNode(n.id))}
                  >
                    <NodeShape n={n} />
                  </g>
                );
              })}
            </g>
            <g>
              {BADGES.map(({ i, f, pt }) => (
                // biome-ignore lint/a11y/useSemanticElements: SVG 안이라 <button> 을 쓸 수 없어 원본처럼 role="button" 인 g
                <g
                  key={f.id}
                  className={cx(s["kg-b"], s[f.sev], edges.has(i) && s.on)}
                  data-kgb={f.id}
                  data-ei={i}
                  tabIndex={0}
                  role="button"
                  aria-label={`발견: ${f.title}`}
                  onClick={() => onFinding(f.id)}
                  onKeyDown={(e) => pressKey(e, () => onFinding(f.id))}
                >
                  <rect x={pt.x - BW / 2} y={pt.y - 9} width={BW} height="18" rx="4" />
                  <text x={pt.x} y={pt.y + 4} textAnchor="middle">
                    {SEVL[f.sev]}
                  </text>
                </g>
              ))}
            </g>
          </svg>
        </div>
        <div className={s.kgleg}>
          <span>
            <LegSw st="ok" />
            허용 {kgCount("ok")}
          </span>
          <span>
            <LegSw st="block" />
            차단 {kgCount("block")}
          </span>
          <span>
            <LegSw st="vuln" />
            취약 {kgCount("vuln")}
          </span>
          <span>
            <LegSw st="sus" />
            의심 {kgCount("sus")}
          </span>
          <span>
            <LegSw st="untested" />
            미검사 {kgCount("untested")}
          </span>
          <span>
            <LegSw st="own" />
            소유 · 선택할 때 보임
          </span>
          <button
            className={cx(s.btn, s.sm)}
            type="button"
            data-kgclear=""
            style={{ marginLeft: "auto" }}
            onClick={onClear}
          >
            전체 보기
          </button>
        </div>
        <p className={s.help}>
          관계의 상태는 관찰한 사실이고, 자원 이름(주문, 프로필)은 응답 내용을 보고 AI가 붙였습니다. 미검사는 안전하다는
          뜻이 아닙니다.
        </p>
      </div>
    </>
  );
}
