import { Fragment, type ReactNode, useMemo, useRef, useState } from "react";

import { useApiResource } from "../lib/api";
import {
  type AgBlock,
  type AgEndpoint,
  type AgStatus,
  buildAssetGraph,
  rankEndpoints,
  visibleEndpoints,
} from "../lib/asset-graph";
import { type EngineFinding, getCoverageGraph } from "../lib/engine";
import type { Severity } from "../types";

const SEV_LABEL: Record<Severity, string> = { critical: "심각", high: "높음", medium: "중간", low: "낮음" };
const ST_LABEL: Record<AgStatus, string> = { ok: "검사함", wait: "미검사", out: "범위 밖" };
const bodyText = { fontSize: 14, color: "var(--muted)", lineHeight: 1.7, margin: 0 } as const;

/** 그래프 배치: 한 줄 높이 · 위아래 여백 · 단계별 x 좌표(도메인 → 서비스 → 묶음 → 엔드포인트) */
const ROW = 28;
const PAD = 16;
const X = { d: 6, s: 170, g: 372, l: 512 };
const W = 920;
/** 묶음 → 엔드포인트 세로 줄기의 x */
const SX = X.l - 18;
/** 묶음 하나에 그리는 엔드포인트 수. 넘치면 중요한 것만 그리고 나머지는 "+ N개 더" */
const MAX_PER_GROUP = 12;

function curve(x1: number, y1: number, x2: number, y2: number): string {
  const m = (x1 + x2) / 2;
  return `M${x1 + 5} ${y1} C${m} ${y1} ${m} ${y2} ${x2 - 5} ${y2}`;
}

/** 묶음 → 엔드포인트: 세로 줄기에서 가지를 낸다. 수십 개여도 선이 부채꼴로 퍼지지 않는다. */
function branch(x1: number, y1: number, x2: number, y2: number): string {
  if (Math.abs(y2 - y1) < 1) return `M${x1 + 5} ${y1} H${x2 - 5}`;
  const dir = y2 > y1 ? 1 : -1;
  const r = Math.min(6, Math.abs(y2 - y1));
  return `M${x1 + 5} ${y1} H${SX} V${y2 - dir * r} Q${SX} ${y2} ${SX + r} ${y2} H${x2 - 5}`;
}

/** 줄인 엔드포인트의 상태별 개수: "미검사 2 · 검사함 26" */
function hiddenText(hidden: AgEndpoint[]): string {
  return (["wait", "out", "ok"] as AgStatus[])
    .map((st) => [st, hidden.filter((e) => e.status === st).length] as const)
    .filter(([, n]) => n > 0)
    .map(([st, n]) => `${ST_LABEL[st]} ${n}`)
    .join(" · ");
}

function FindingChip({ f, onOpen }: { f: EngineFinding; onOpen: (id: string) => void }) {
  const sev = (["critical", "high", "medium", "low"] as Severity[]).find((s) => s === f.severity);
  const label = sev ? SEV_LABEL[sev] : "미분류";
  let name = "이름 없는 발견";
  if (f.name?.length) name = f.name;
  else if (f.vulnclass.length > 0) name = f.vulnclass;
  return (
    <button
      type="button"
      className="achip"
      title={name}
      aria-label={`발견 열기: ${name} · 위험도 ${label}`}
      onClick={() => onOpen(f.id)}
    >
      <span className={`sev${sev ? ` ${sev}` : ""}`}>{label}</span>
      <span className="achip-t">{name}</span>
    </button>
  );
}

function Node({
  x,
  y,
  dot,
  path,
  className = "",
  children,
}: {
  x: number;
  y: number;
  dot: string;
  path: string;
  className?: string;
  children: ReactNode;
}) {
  return (
    <>
      <span className={`adot ${dot}`} style={{ left: x - 5, top: y - 5 }} />
      <div className={`alab ${className}`} style={{ left: x + 12, top: y - 10 }} data-path={path}>
        {children}
      </div>
    </>
  );
}

function countOf(eps: { status: AgStatus }[]) {
  const inScope = eps.filter((e) => e.status !== "out");
  return {
    inScope: inScope.length,
    tested: inScope.filter((e) => e.status === "ok").length,
    out: eps.length - inScope.length,
  };
}

/** 서비스 하나의 그래프. 노드에 마우스를 올리거나 발견 칩에 초점이 가면 위쪽 경로를 강조한다. */
function BlockGraph({
  block,
  onOpen,
  onMore,
}: {
  block: AgBlock;
  onOpen: (id: string) => void;
  onMore: (groupKey: string) => void;
}) {
  const [hl, setHl] = useState<string[]>([]);
  let k = 0;
  const rowY = () => {
    const y = PAD + k * ROW + ROW / 2;
    k += 1;
    return y;
  };
  const groups = block.groups.map((g, gi) => {
    const { shown, hidden } = visibleEndpoints(g.eps, MAX_PER_GROUP);
    const eps = shown.map((e, ei) => ({ e, y: rowY(), key: `L${gi}-${ei}` }));
    const more = hidden.length ? { hidden, y: rowY(), key: `M${gi}` } : null;
    const last = more ? more.y : eps[eps.length - 1].y;
    return { g, gi, eps, more, y: (eps[0].y + last) / 2 };
  });
  const H = PAD * 2 + Math.max(k, 1) * ROW;
  const sy = groups.length ? (groups[0].y + groups[groups.length - 1].y) / 2 : PAD + ROW / 2;
  const all = countOf(block.groups.flatMap((g) => g.eps));
  const pick = (t: EventTarget) =>
    setHl(((t as Element).closest?.("[data-path]")?.getAttribute("data-path") ?? "").split(" ").filter(Boolean));

  // 강조한 선은 마지막에 그려서 겹친 다른 가지에 가려지지 않게 한다
  const paths = [
    { key: "S", d: curve(X.d, sy, X.s, sy), dashed: false },
    ...groups.map((G) => ({ key: `G${G.gi}`, d: curve(X.s, sy, X.g, G.y), dashed: false })),
    ...groups.flatMap((G) => [
      ...G.eps.map((p) => ({ key: p.key, d: branch(X.g, G.y, X.l, p.y), dashed: p.e.status === "out" })),
      ...(G.more ? [{ key: G.more.key, d: branch(X.g, G.y, X.l, G.more.y), dashed: true }] : []),
    ]),
  ]
    .map((p) => ({ ...p, on: hl.includes(p.key) }))
    .sort((a, b) => Number(a.on) - Number(b.on));

  return (
    <div className="ag-blk">
      {block.siteFindings.length > 0 && (
        <div className="ag-site" data-path="S" onPointerEnter={() => setHl(["S"])} onPointerLeave={() => setHl([])}>
          <span>서비스 전체 발견</span>
          {block.siteFindings.map((f) => (
            <FindingChip key={f.id} f={f} onOpen={onOpen} />
          ))}
        </div>
      )}
      {/* biome-ignore lint/a11y/noStaticElementInteractions: 강조는 보조 표시 — 같은 정보가 목록 보기에 있다 */}
      <div
        className="agc"
        style={{ height: H }}
        onPointerOver={(e) => pick(e.target)}
        onPointerLeave={() => setHl([])}
        onFocus={(e) => pick(e.target)}
        onBlur={() => setHl([])}
      >
        <svg width={W} height={H} aria-hidden="true">
          {paths.map((p) => (
            <path
              key={p.key}
              d={p.d}
              className={[p.on ? "on" : "", p.dashed ? "out" : ""].join(" ").trim() || undefined}
            />
          ))}
        </svg>
        <Node x={X.d} y={sy} dot="int" path="" className="col-d">
          <div className="row1">
            <span className="nm" title={block.domain}>
              {block.domain}
            </span>
          </div>
          <span className="sub">도메인</span>
        </Node>
        <Node x={X.s} y={sy} dot="int" path="S" className="col-s">
          <div className="row1">
            <span className="nm" title={block.service}>
              {block.service}
            </span>
          </div>
          <span className="sub">
            서비스 · 검사 {all.tested}/{all.inScope}
          </span>
        </Node>
        {groups.map((G) => {
          const c = countOf(G.g.eps);
          return (
            <Fragment key={G.g.key}>
              <Node x={X.g} y={G.y} dot="int" path={`G${G.gi} S`} className="col-g">
                <div className="row1">
                  <span className="k">{G.g.key}</span>
                  {G.g.name && <span className="nm">{G.g.name}</span>}
                </div>
                <span className="sub">
                  검사 {c.tested}/{c.inScope}
                  {c.out > 0 && ` · 범위 밖 ${c.out}`}
                </span>
              </Node>
              {G.eps.map((p) => (
                <Node key={p.key} x={X.l} y={p.y} dot={p.e.status} path={`${p.key} G${G.gi} S`} className={p.e.status}>
                  <div className="row1">
                    <span className="k">{p.e.path}</span>
                    <span className="ag-sr">{ST_LABEL[p.e.status]}</span>
                    {p.e.findings.map((f) => (
                      <FindingChip key={f.id} f={f} onOpen={onOpen} />
                    ))}
                  </div>
                </Node>
              ))}
              {G.more && (
                <Node x={X.l} y={G.more.y} dot="more" path={`${G.more.key} G${G.gi} S`}>
                  <div className="row1">
                    <button
                      type="button"
                      className="agmore"
                      aria-label={`${G.g.key} 묶음의 나머지 ${G.more.hidden.length}개를 목록으로 보기`}
                      onClick={() => onMore(G.g.key)}
                    >
                      + {G.more.hidden.length}개 더 · {hiddenText(G.more.hidden)}
                    </button>
                  </div>
                </Node>
              )}
            </Fragment>
          );
        })}
      </div>
    </div>
  );
}

/* ---------------- 자산 그래프 탭 ---------------- */
export default function AssetGraphPanel({
  taskId,
  findings,
  onOpenFinding,
}: {
  taskId: string;
  findings: EngineFinding[];
  onOpenFinding: (id: string) => void;
}) {
  const res = useApiResource(() => getCoverageGraph(taskId), [taskId]);
  const [view, setView] = useState<"graph" | "table">("graph");
  /** "+ N개 더"로 들어오면 목록에서 그 묶음만 본다 */
  const [only, setOnly] = useState<{ service: string; key: string } | null>(null);
  const cardRef = useRef<HTMLDivElement>(null);
  const graph = useMemo(() => (res.data ? buildAssetGraph(res.data, findings) : null), [res.data, findings]);
  const ready = !!graph && graph.blocks.length > 0;
  const show = (v: "graph" | "table") => {
    setView(v);
    if (v === "graph") setOnly(null);
  };
  const openGroup = (service: string, key: string) => {
    setOnly({ service, key });
    setView("table");
    cardRef.current?.scrollIntoView({ block: "start", behavior: "smooth" });
  };

  let body: ReactNode;
  if (res.loading) body = <p style={bodyText}>불러오는 중…</p>;
  else if (res.error) body = <p style={bodyText}>자산 그래프를 불러오지 못했어요.</p>;
  else if (!res.data) body = <p style={bodyText}>자산 저장소가 꺼져 있어 그래프를 그릴 수 없어요.</p>;
  else if (!graph || !ready) body = <p style={bodyText}>아직 범위 안에 집계된 자산이 없어요.</p>;
  else {
    const { stats, blocks, loose } = graph;
    const multi = blocks.length > 1;
    const focus = only && blocks.find((b) => b.service === only.service)?.groups.find((g) => g.key === only.key);
    const focusCount = focus ? countOf(focus.eps) : null;
    const pct = stats.inScope ? Math.round((stats.tested * 100) / stats.inScope) : 0;
    body = (
      <>
        <p className="ag-sum">
          범위 안 엔드포인트{" "}
          <b>
            {stats.inScope}개 중 {stats.tested}개 검사
          </b>{" "}
          ({pct}%) · 미검사 <b>{stats.inScope - stats.tested}</b>
          {stats.out > 0 && (
            <>
              {" "}
              · 범위 밖 <b>{stats.out}</b>
            </>
          )}{" "}
          · 발견 <b>{findings.length}건</b>
        </p>
        <div className="leg" style={{ margin: "0 0 14px" }}>
          <span>
            <span className="adot ok" />
            검사함
          </span>
          <span>
            <span className="adot wait" />
            미검사
          </span>
          {stats.out > 0 && (
            <span>
              <span className="adot out" />
              범위 밖
            </span>
          )}
          <span>
            <span className="achip demo">
              <span className="sev medium">위험도</span>발견
            </span>
          </span>
        </div>
        {view === "graph" ? (
          <div className="scrollx">
            {blocks.map((b) => (
              <BlockGraph
                key={b.service}
                block={b}
                onOpen={onOpenFinding}
                onMore={(key) => openGroup(b.service, key)}
              />
            ))}
          </div>
        ) : (
          <>
            {focus && focusCount && (
              <div className="ag-filter" role="status">
                <span>
                  <b>
                    {multi && `${only?.service} `}
                    {focus.key}
                    {focus.name && ` ${focus.name}`}
                  </b>{" "}
                  묶음 {focus.eps.length}개만 보는 중 · 미검사 {focusCount.inScope - focusCount.tested}
                  {focusCount.out > 0 && ` · 범위 밖 ${focusCount.out}`} · 중요한 순서
                </span>
                <button type="button" onClick={() => setOnly(null)}>
                  전체 보기
                </button>
              </div>
            )}
            <div className="scrollx">
              <table className="vtable agt">
                <thead>
                  <tr>
                    <th>묶음</th>
                    <th>요청</th>
                    <th>상태</th>
                    <th>발견</th>
                  </tr>
                </thead>
                <tbody>
                  {blocks.map((b) => {
                    if (focus && b.service !== only?.service) return null;
                    return (
                      <Fragment key={b.service}>
                        {!focus && b.siteFindings.length > 0 && (
                          <tr>
                            <td>서비스 전체</td>
                            <td className="k">{b.service}</td>
                            <td>—</td>
                            <td>
                              {b.siteFindings.map((f) => (
                                <FindingChip key={f.id} f={f} onOpen={onOpenFinding} />
                              ))}
                            </td>
                          </tr>
                        )}
                        {b.groups.map((g) => {
                          if (focus && g !== focus) return null;
                          return (focus ? rankEndpoints(g.eps) : g.eps).map((e, i) => (
                            // 같은 URL의 다른 메서드는 경로가 같아서 순번을 함께 쓴다
                            // biome-ignore lint/suspicious/noArrayIndexKey: 줄에 상태가 없어 순서가 바뀌어도 다시 그리기만 한다
                            <tr key={`${g.key}-${i}`} className={e.status === "out" ? "out" : undefined}>
                              <td>
                                {multi && `${b.service} `}
                                {g.key}
                                {g.name && ` ${g.name}`}
                              </td>
                              <td className="k">{e.path}</td>
                              <td>
                                <span className={`agst ${e.status}`}>{ST_LABEL[e.status]}</span>
                              </td>
                              <td>
                                {e.findings.length
                                  ? e.findings.map((f) => <FindingChip key={f.id} f={f} onOpen={onOpenFinding} />)
                                  : "—"}
                              </td>
                            </tr>
                          ));
                        })}
                      </Fragment>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </>
        )}
        {loose.length > 0 && (
          <div className="ag-site" style={{ marginTop: 10 }}>
            <span>위치를 특정하지 못한 발견</span>
            {loose.map((f) => (
              <FindingChip key={f.id} f={f} onOpen={onOpenFinding} />
            ))}
          </div>
        )}
        <div className="hint" style={{ marginTop: 12 }}>
          미검사 항목은 안전하다는 뜻이 아니에요.
        </div>
      </>
    );
  }

  return (
    <div className="rpanel">
      <div className="card" ref={cardRef}>
        <div className="ag-head">
          <h3 style={{ marginBottom: 0 }}>자산 그래프</h3>
          {ready && (
            <fieldset className="seg ag-seg">
              <legend className="seg-lab">보기</legend>
              <button type="button" aria-pressed={view === "graph"} onClick={() => show("graph")}>
                그래프
              </button>
              <button type="button" aria-pressed={view === "table"} onClick={() => show("table")}>
                목록
              </button>
            </fieldset>
          )}
        </div>
        {body}
      </div>
    </div>
  );
}
