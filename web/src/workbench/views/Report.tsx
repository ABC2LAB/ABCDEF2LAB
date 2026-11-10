"use client";

// 보고서 화면. 원본 보고서 HTML + renderReport (발견 목록) · 보고서 탭 · renderRInsp (발견 상세 / 그래프 노드 상세) ·
// kgPickNode · kgPickFinding · 경로 고르기 · 전체 보기, 그리고 setReportHost 가 그리던 크럼 · 제목 · 알약 · rMeta · 예시 안내.
// 보고서 대상은 스토어의 report 스냅숏으로 그리고, 그것이 바뀌면(seq) 그래프 선택과 노드 상세를 지운다(원본처럼).

import { type KeyboardEvent, type MouseEvent, useEffect, useRef, useState } from "react";

import { flushSync } from "react-dom";

import { F, type FindingId, findingById } from "../data/findings";
import { KG_BY_ID, KGP, type KgNode, type KgSel, type KgState, krels, STC, STL } from "../data/kg";
import { cx } from "../lib/cx";
import { hms } from "../lib/time";
import { useShell } from "../shell";
import { useWb } from "../store";
import { Icon } from "../ui/icons";
import { Ep, InspHead, InspOpenButton, Isec, MenuButton, Sev, Verdict } from "../ui/parts";
import s from "../workbench.module.css";
import { FindingDetail, FindingEp } from "./FindingDetail";
import { KnowledgeGraph } from "./KnowledgeGraph";

type Tab = "list" | "graph" | "map" | "ev";
const TABS: ReadonlyArray<readonly [Tab, string]> = [
  ["list", "발견 목록"],
  ["graph", "자산 지식 그래프"],
  ["map", "접근통제 지도"],
  ["ev", "증거 · 감사"],
];

const TL: Readonly<Record<KgNode["t"], string>> = {
  role: "계정 · 역할",
  ep: "엔드포인트",
  res: "자원 · 이름은 AI 추정",
};
const CHIP_ORDER: readonly KgState[] = ["vuln", "sus", "untested", "block", "ok"];

/** 그래프 노드 상세. 원본 `kgNodeHtml(n)`. 관계를 누르면 그 노드로(onSel), 관련 발견을 누르면 발견으로(onFinding). */
function KgNodeDetail({
  n,
  host,
  onSel,
  onFinding,
}: {
  n: KgNode;
  host: string;
  onSel: (id: string) => void;
  onFinding: (id: FindingId) => void;
}) {
  const rels = krels(n.id);
  const cnt: Partial<Record<KgState, number>> = {};
  for (const { e } of rels) if (e[2] !== "own") cnt[e[2]] = (cnt[e[2]] ?? 0) + 1;
  const chips = CHIP_ORDER.filter((k) => cnt[k]).map((k) => (
    <span key={k} className={cx(s.ac, s[STC[k]])}>
      {`${STL[k]} ${cnt[k]}`}
    </span>
  ));
  const fids: FindingId[] = [];
  for (const { e } of rels) if (e[4] && !fids.includes(e[4])) fids.push(e[4]);
  return (
    <>
      <InspHead
        eyebrow={
          <>
            {TL[n.t]} · <span className={s.host}>{host}</span>
          </>
        }
        title={n.t === "ep" ? <Ep m={n.m} p={n.p} /> : n.label}
        chips={chips.length ? chips : null}
      />
      {n.t !== "ep" && n.sub ? (
        <p className={s.help} style={{ marginTop: 8 }}>
          {n.sub}
        </p>
      ) : null}
      <Isec t={`관계 ${rels.length}`}>
        <ul className={s.rel}>
          {rels.map(({ e, i }) => {
            const out = e[0] === n.id;
            const o = KG_BY_ID[out ? e[1] : e[0]];
            return (
              <li key={i}>
                <button type="button" data-kgsel={o.id} onClick={() => onSel(o.id)}>
                  {/* biome-ignore lint/a11y/useAriaPropsSupportedByRole: 원본 그대로 화살표에 방향 이름을 단다 */}
                  <span className={s.dir} aria-label={out ? "나가는 관계" : "들어오는 관계"}>
                    {out ? "→" : "←"}
                  </span>
                  <span className={s.who}>{o.t === "ep" ? <Ep m={o.m} p={o.p} /> : <b>{o.label}</b>}</span>
                  <span className={s.st}>
                    <span className={cx(s.ac, s[STC[e[2]]])}>{STL[e[2]]}</span>
                  </span>
                  <span className={s.lab}>{e[3]}</span>
                </button>
              </li>
            );
          })}
        </ul>
      </Isec>
      {fids.length ? (
        <Isec t="관련 발견">
          <div className={s.flist}>
            {fids.map((id) => {
              const f = findingById(id);
              return f ? (
                <button key={id} className={s.fli} type="button" data-kgf={id} onClick={() => onFinding(id)}>
                  <Sev k={f.sev} />
                  <span>{f.title}</span>
                </button>
              ) : null;
            })}
          </div>
        </Isec>
      ) : null}
    </>
  );
}

/** 접근통제 지도 한 칸 (ac 딱지) */
function Ac({ k, children }: { k: "al" | "bl" | "vu" | "nt" | "na"; children: string }) {
  return <span className={cx(s.ac, s[k])}>{children}</span>;
}

export function Report() {
  const report = useWb((st) => st.report);
  const { openInsp } = useShell();
  const host = report.host;

  // 원본 rSel · rMode · kgSel. 노드 상세(rNode)와 그래프 선택은 보고서 대상이 바뀌면(seq) 지운다 — 고른 seq 와 함께 둔다.
  const [rSel, setRSel] = useState<FindingId>("f1");
  const [node, setNode] = useState<{ id: string; seq: number } | null>(null);
  const [kg, setKg] = useState<{ sel: KgSel; seq: number }>({ sel: null, seq: report.seq });
  const [tab, setTab] = useState<Tab>("list");
  const rNode = node && node.seq === report.seq ? node.id : null;
  const kgSel = kg.seq === report.seq ? kg.sel : null;

  const vbRef = useRef<HTMLDivElement>(null);
  const inspRef = useRef<HTMLElement>(null);
  const tabRefs = useRef<Partial<Record<Tab, HTMLButtonElement | null>>>({});

  const setSel = (sel: KgSel) => setKg({ sel, seq: report.seq });
  /** 원본 renderRInsp 의 box.scrollTop=0 */
  const inspTop = () => {
    if (inspRef.current) inspRef.current.scrollTop = 0;
  };

  // 보고서 대상이 바뀌면 상세 패널도 맨 위로 (원본 setReportHost → renderReport → renderRInsp)
  const seqSeen = useRef(report.seq);
  useEffect(() => {
    if (seqSeen.current === report.seq) return;
    seqSeen.current = report.seq;
    if (inspRef.current) inspRef.current.scrollTop = 0;
  }, [report.seq]);

  /** 원본 kgPickNode(id, focus) */
  const pickNode = (id: string, focus = false) => {
    flushSync(() => {
      setSel({ type: "node", id });
      setNode({ id, seq: report.seq });
    });
    inspTop();
    if (focus) vbRef.current?.querySelector<SVGGElement>(`#kgSvg [data-kgn="${id}"]`)?.focus();
    openInsp();
  };
  /** 원본 kgPickFinding(id): 그래프 탭이 보일 때만 그래프에서도 그 발견을 강조 */
  const pickFinding = (id: string) => {
    const f = findingById(id);
    if (!f) return;
    setRSel(f.id);
    setNode(null);
    if (tab === "graph") setSel({ type: "f", id: f.id });
    inspTop();
    openInsp();
  };
  /** 원본 data-kgp: 같은 경로를 다시 누르면 강조만 풀고 상세는 그대로 */
  const pickPath = (id: string) => {
    const p = KGP.find((x) => x.id === id);
    if (!p) return;
    if (kgSel?.type === "path" && kgSel.id === p.id) {
      setSel(null);
      return;
    }
    setSel({ type: "path", id: p.id });
    setRSel(p.f);
    setNode(null);
    inspTop();
    openInsp();
  };
  /** 원본 data-kgsel: 노드 상세의 관계 → 그래프 탭으로 바꾸고 그 노드를 골라 포커스 */
  const relSel = (id: string) => {
    if (tab !== "graph") flushSync(() => setTab("graph"));
    pickNode(id, true);
  };
  /** 원본 rList 클릭: 고르고 그 줄에 포커스 */
  const pickRow = (id: FindingId, e: MouseEvent<HTMLButtonElement>) => {
    setRSel(id);
    setNode(null);
    inspTop();
    e.currentTarget.focus();
    openInsp();
  };
  /** 원본 rTabs keydown: 좌우 화살표로 옮기고 포커스 */
  const tabKey = (i: number, e: KeyboardEvent<HTMLButtonElement>) => {
    if (e.key !== "ArrowRight" && e.key !== "ArrowLeft") return;
    const d = e.key === "ArrowRight" ? 1 : -1;
    e.preventDefault();
    const next = TABS[(i + d + TABS.length) % TABS.length][0];
    setTab(next);
    tabRefs.current[next]?.focus();
  };

  const live = report.kind === "live";
  const ended = live && report.ended;
  const nodeN = rNode ? KG_BY_ID[rNode] : undefined;
  const sel = findingById(rSel);

  return (
    <>
      <header className={s.vh}>
        <div className={s["vh-top"]}>
          <MenuButton />
          <nav className={s.crumb} aria-label="위치">
            <a href="#dashboard">작업대</a>
            <span className={s.sep}>/</span>
            <span>점검 기록</span>
            <span className={s.sep}>/</span>
            <span className={s.host} id="rCrumbHost">
              {host}
            </span>
          </nav>
        </div>
        <div className={s["vh-row"]}>
          <div className={s["vh-title"]}>
            <h1 tabIndex={-1} id="rTitle">
              {`${host} 보고서`}
            </h1>
            <span className={ended ? s.pill : cx(s.pill, s.ok)} id="rPill">
              {ended ? "종료됨" : "점검 완료"}
            </span>
          </div>
          <div className={s["vh-acts"]}>
            <InspOpenButton>발견 상세</InspOpenButton>
          </div>
        </div>
        <div className={s["vh-meta"]} id="rMeta">
          {live ? (
            <>
              <span>
                시작 <b>오늘 {hms(report.start).slice(0, 5)}</b>
              </span>
              <span>
                소요 <b>{report.mins}분</b>
              </span>
              <span>
                모델 <b>qwen/qwen3.6-35b-a3b · 로컬</b>
              </span>
              <span>
                커버리지 <b>예시</b>
              </span>
              {ended ? <span>조기 종료 · 실제로는 종료 시점까지만 집계</span> : null}
            </>
          ) : (
            <>
              <span>
                시작 <b>10/25 14:03</b>
              </span>
              <span>
                소요 <b>28분</b>
              </span>
              <span>
                모델 <b>qwen/qwen3.6-35b-a3b · 로컬</b>
              </span>
              <span>
                커버리지 <b>91%</b>
              </span>
            </>
          )}
        </div>
      </header>
      <div className={s.vb} ref={vbRef}>
        <div className={s.vm}>
          {/* 시안: 아래 strip·발견 목록·그래프는 완료된 예시(blog) 고정값이라 방금 끝낸 점검의 실제 결과가 아니다 */}
          <div className={cx(s.note, s.warn)} id="rExample" hidden={!live}>
            <Icon name="alert" />
            <div className={s["rex-t"]}>
              {live
                ? `방금 ${ended ? "종료" : "완료"}한 점검의 실제 결과가 아닙니다. 아래 발견·판정·커버리지·그래프는 완료된 예시 점검(blog.example.com)의 고정 샘플입니다.`
                : null}
            </div>
          </div>
          <div className={s.strip}>
            <div className={s.cell}>
              <div className={s.k}>발견</div>
              <div className={s.v}>6</div>
              <div className={s.sub}>심각 1 · 높음 1 · 중간 3 · 낮음 1</div>
            </div>
            <div className={s.cell}>
              <div className={s.k}>판정 확정</div>
              <div className={s.v}>3</div>
              <div className={s.sub}>증거 충분</div>
            </div>
            <div className={s.cell}>
              <div className={s.k}>의심 / 판단 불가</div>
              <div className={s.v}>
                2<small>/ 1</small>
              </div>
              <div className={s.sub}>추가 확인 권장</div>
            </div>
            <div className={s.cell}>
              <div className={s.k}>커버리지</div>
              <div className={s.v}>
                91<small>%</small>
              </div>
              <div className={s.sub}>미검사 9%</div>
            </div>
          </div>
          <section className={s.sec} aria-labelledby="rPrioH">
            <div className={s.sh}>
              <h2 id="rPrioH">우선 조치</h2>
              <span className={s.aside}>위험도와 판정 순</span>
            </div>
            <ol className={s.prio}>
              <li>
                <span className={s.pt}>다른 사용자의 주문 내역이 조회됨</span>
                <span className={s.pc}>
                  <Sev k="crit" />
                  <Verdict v="확정" />
                </span>
                <span className={s.pf}>
                  주문 조회에 <code>order.owner_id == 세션 사용자</code> 검사 추가
                </span>
              </li>
              <li>
                <span className={s.pt}>비로그인 상태에서 관리자 통계가 열림</span>
                <span className={s.pc}>
                  <Sev k="hi" />
                  <Verdict v="확정" />
                </span>
                <span className={s.pf}>
                  <code>/admin</code> 전체에 서버 쪽 인증·관리자 권한 검사
                </span>
              </li>
              <li>
                <span className={s.pt}>쿠키에 보안 속성이 빠져 있음</span>
                <span className={s.pc}>
                  <Sev k="mid" />
                  <Verdict v="확정" />
                </span>
                <span className={s.pf}>
                  세션 쿠키에 <code>HttpOnly</code> <code>Secure</code> <code>SameSite</code> 지정
                </span>
              </li>
            </ol>
          </section>
          <section className={s.sec}>
            <div className={s["seg-tabs"]} role="tablist" aria-label="보고서 내용" id="rTabs">
              {TABS.map(([k, label], i) => (
                <button
                  key={k}
                  type="button"
                  role="tab"
                  id={`rtab-${k}`}
                  aria-controls={`rp-${k}`}
                  aria-selected={tab === k}
                  tabIndex={tab === k ? 0 : -1}
                  ref={(el) => {
                    tabRefs.current[k] = el;
                  }}
                  onClick={() => setTab(k)}
                  onKeyDown={(e) => tabKey(i, e)}
                >
                  {label}
                  {k === "list" ? <span className={s.c}>6</span> : null}
                </button>
              ))}
            </div>
            <div role="tabpanel" id="rp-list" aria-labelledby="rtab-list" hidden={tab !== "list"}>
              <div className={s.list} id="rList">
                {F.map((f) => (
                  <button
                    key={f.id}
                    className={cx(s.frow, rSel === f.id && s.sel)}
                    type="button"
                    data-rsel={f.id}
                    aria-current={rSel === f.id ? "true" : undefined}
                    onClick={(e) => pickRow(f.id, e)}
                  >
                    <Sev k={f.sev} />
                    <span className={s.fm}>
                      <span className={s.ft}>{f.title}</span>
                      <span className={s.fe}>
                        <FindingEp f={f} />
                        <span>{f.type}</span>
                      </span>
                    </span>
                    <span className={s.fs}>
                      <Verdict v={f.vd} />
                    </span>
                  </button>
                ))}
              </div>
            </div>
            <div role="tabpanel" id="rp-graph" aria-labelledby="rtab-graph" hidden={tab !== "graph"}>
              <div style={{ display: "flex", flexDirection: "column", gap: 14 }} id="kgBox">
                <KnowledgeGraph
                  host={host}
                  sel={kgSel}
                  onNode={(id) => pickNode(id)}
                  onFinding={pickFinding}
                  onPath={pickPath}
                  onClear={() => setSel(null)}
                />
              </div>
            </div>
            <div role="tabpanel" id="rp-map" aria-labelledby="rtab-map" hidden={tab !== "map"}>
              <div className={s.tblwrap}>
                <table className={s.amap}>
                  <thead>
                    <tr>
                      <th scope="col">계정 · 역할</th>
                      <th scope="col">내 자원</th>
                      <th scope="col">남의 자원</th>
                      <th scope="col">결제·주문 변경</th>
                      <th scope="col">관리자 기능</th>
                      <th scope="col">사용자 관리</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr>
                      <th scope="row">
                        계정 A<small>일반 사용자</small>
                      </th>
                      <td>
                        <Ac k="al">허용</Ac>
                      </td>
                      <td>
                        <Ac k="vu">취약</Ac>
                      </td>
                      <td>
                        <Ac k="al">허용</Ac>
                      </td>
                      <td>
                        <Ac k="bl">차단</Ac>
                      </td>
                      <td>
                        <Ac k="bl">차단</Ac>
                      </td>
                    </tr>
                    <tr>
                      <th scope="row">
                        계정 B<small>일반 사용자</small>
                      </th>
                      <td>
                        <Ac k="al">허용</Ac>
                      </td>
                      <td>
                        <Ac k="vu">취약</Ac>
                      </td>
                      <td>
                        <Ac k="nt">미검사</Ac>
                      </td>
                      <td>
                        <Ac k="bl">차단</Ac>
                      </td>
                      <td>
                        <Ac k="bl">차단</Ac>
                      </td>
                    </tr>
                    <tr>
                      <th scope="row">관리자</th>
                      <td>
                        <Ac k="al">허용</Ac>
                      </td>
                      <td>
                        <Ac k="al">허용</Ac>
                      </td>
                      <td>
                        <Ac k="al">허용</Ac>
                      </td>
                      <td>
                        <Ac k="al">허용</Ac>
                      </td>
                      <td>
                        <Ac k="al">허용</Ac>
                      </td>
                    </tr>
                    <tr>
                      <th scope="row">
                        비로그인<small>게스트</small>
                      </th>
                      <td>
                        <Ac k="na">—</Ac>
                      </td>
                      <td>
                        <Ac k="bl">차단</Ac>
                      </td>
                      <td>
                        <Ac k="bl">차단</Ac>
                      </td>
                      <td>
                        <Ac k="vu">취약</Ac>
                      </td>
                      <td>
                        <Ac k="bl">차단</Ac>
                      </td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </div>
            <div role="tabpanel" id="rp-ev" aria-labelledby="rtab-ev" hidden={tab !== "ev"}>
              <div style={{ display: "flex", flexDirection: "column", gap: 14 }}>
                <ul className={s.evi}>
                  <li>
                    <code>evidence/order-idor</code>
                    <span>요청·응답 2건</span>
                    <span className={cx(s.tag, s.in)}>canary 일치</span>
                  </li>
                  <li>
                    <code>evidence/admin-stats</code>
                    <span>비로그인 응답 1건 · 대조군 1건</span>
                  </li>
                  <li>
                    <code>evidence/cookie-flags</code>
                    <span>응답 헤더 1건</span>
                  </li>
                </ul>
              </div>
            </div>
          </section>
        </div>
        <aside className={s.vi} id="rInsp" aria-label="발견 상세" ref={inspRef}>
          {nodeN ? <KgNodeDetail n={nodeN} host={host} onSel={relSel} onFinding={pickFinding} /> : null}
          {!nodeN && sel ? <FindingDetail f={sel} host={host} ctx="report" /> : null}
        </aside>
      </div>
    </>
  );
}
