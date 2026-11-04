import { Fragment, type ReactNode, useMemo, useState } from "react";

import { useApiResource } from "../lib/api";
import { type AgBlock, type AgStatus, buildAssetGraph } from "../lib/asset-graph";
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

function curve(x1: number, y1: number, x2: number, y2: number): string {
  const m = (x1 + x2) / 2;
  return `M${x1 + 5} ${y1} C${m} ${y1} ${m} ${y2} ${x2 - 5} ${y2}`;
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
function BlockGraph({ block, onOpen }: { block: AgBlock; onOpen: (id: string) => void }) {
  const [hl, setHl] = useState<string[]>([]);
  let k = 0;
  const groups = block.groups.map((g, gi) => {
    const eps = g.eps.map((e, ei) => {
      const y = PAD + k * ROW + ROW / 2;
      k += 1;
      return { e, y, key: `L${gi}-${ei}` };
    });
    return { g, gi, eps, y: (eps[0].y + eps[eps.length - 1].y) / 2 };
  });
  const H = PAD * 2 + Math.max(k, 1) * ROW;
  const sy = groups.length ? (groups[0].y + groups[groups.length - 1].y) / 2 : PAD + ROW / 2;
  const all = countOf(block.groups.flatMap((g) => g.eps));
  const pick = (t: EventTarget) =>
    setHl(((t as Element).closest?.("[data-path]")?.getAttribute("data-path") ?? "").split(" ").filter(Boolean));
  const cls = (key: string, out = false) =>
    [hl.includes(key) ? "on" : "", out ? "out" : ""].join(" ").trim() || undefined;

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
          <path d={curve(X.d, sy, X.s, sy)} className={cls("S")} />
          {groups.map((G) => (
            <path key={`G${G.gi}`} d={curve(X.s, sy, X.g, G.y)} className={cls(`G${G.gi}`)} />
          ))}
          {groups.map((G) =>
            G.eps.map((p) => (
              <path key={p.key} d={curve(X.g, G.y, X.l, p.y)} className={cls(p.key, p.e.status === "out")} />
            )),
          )}
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
  const graph = useMemo(() => (res.data ? buildAssetGraph(res.data, findings) : null), [res.data, findings]);
  const ready = !!graph && graph.blocks.length > 0;

  let body: ReactNode;
  if (res.loading) body = <p style={bodyText}>불러오는 중…</p>;
  else if (res.error) body = <p style={bodyText}>자산 그래프를 불러오지 못했어요.</p>;
  else if (!res.data) body = <p style={bodyText}>자산 저장소가 꺼져 있어 그래프를 그릴 수 없어요.</p>;
  else if (!graph || !ready) body = <p style={bodyText}>아직 범위 안에 집계된 자산이 없어요.</p>;
  else {
    const { stats, blocks, loose } = graph;
    const multi = blocks.length > 1;
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
              <BlockGraph key={b.service} block={b} onOpen={onOpenFinding} />
            ))}
          </div>
        ) : (
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
                {blocks.map((b) => (
                  <Fragment key={b.service}>
                    {b.siteFindings.length > 0 && (
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
                    {b.groups.map((g) =>
                      g.eps.map((e, i) => (
                        // 같은 URL의 다른 메서드는 경로가 같아서 순번을 함께 쓴다
                        // biome-ignore lint/suspicious/noArrayIndexKey: 목록은 서버 응답 순서 그대로이고 재정렬하지 않는다
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
                      )),
                    )}
                  </Fragment>
                ))}
              </tbody>
            </table>
          </div>
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
      <div className="card">
        <div className="ag-head">
          <h3 style={{ marginBottom: 0 }}>자산 그래프</h3>
          {ready && (
            <fieldset className="seg ag-seg">
              <legend className="seg-lab">보기</legend>
              <button type="button" aria-pressed={view === "graph"} onClick={() => setView("graph")}>
                그래프
              </button>
              <button type="button" aria-pressed={view === "table"} onClick={() => setView("table")}>
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
