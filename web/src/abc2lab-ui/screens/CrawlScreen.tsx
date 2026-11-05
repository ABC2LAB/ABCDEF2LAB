import { Fragment, type ReactNode, useEffect, useMemo, useRef, useState } from "react";

import { Brand } from "../components/Brand";
import { Screen } from "../components/Screen";
import { MockBanner } from "../components/ui";
import {
  CRAWL_EVENTS,
  CRAWL_GROUP_LABEL,
  CRAWL_START_SEC,
  CRAWL_WAIT_MS,
  type CrawlEvent,
  type CrawlGroup,
  type CrawlPath,
  isLockedPath,
} from "../lib/crawl-demo";
import { cx } from "../lib/cx";
import { useNav } from "../nav";
import type { Target } from "../types";

/* ===== 자산 탐색 (1차 탐색 · 크롤링) — 아티팩트 v74와 같은 구성 =====
 * [점검 시작] 뒤 링크 · 폼 · 스크립트를 따라가며 경로를 모으는 모습을 보여 주고, 다 모으면 [범위 확인]으로 넘어간다.
 * 엔진에는 이 단계가 따로 없어서 시안이다(lib/crawl-demo.ts 의 예시 기록을 차례로 보여 준다).
 */

const PIPELINE = ["자산 탐색", "범위 확인", "점검 계획", "취약점 점검", "검증", "마무리", "보고서"];
/** 각 줄까지의 누적 초 — 기록의 시각과 경과 시간 */
const AT = CRAWL_EVENTS.reduce<number[]>((acc, e) => {
  acc.push((acc[acc.length - 1] ?? 0) + e.sec);
  return acc;
}, []);
/** 예시 기록 줄 수 */
const TOTAL = CRAWL_EVENTS.length;
/** 시작하고 첫 줄이 나오기까지(ms) */
const FIRST_WAIT_MS = 500;
/** 새로 찾은 경로를 강조해 두는 줄 수 */
const NEW_SPAN = 2;

type Run = {
  started: boolean;
  /** 보여 준 줄 수 */
  step: number;
  paused: boolean;
  cancelled: boolean;
  /** 모두 펼친 묶음 */
  open: CrawlGroup[];
};
const IDLE: Run = { started: false, step: 0, paused: false, cancelled: false, open: [] };
const fresh = (): Run => ({ started: true, step: 0, paused: false, cancelled: false, open: [] });

type Found = CrawlPath & { at: number };

function clock(sec: number): string {
  const x = CRAWL_START_SEC + sec;
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(Math.floor(x / 3600))}:${p(Math.floor((x % 3600) / 60))}:${p(x % 60)}`;
}
function elapsed(sec: number): string {
  return sec < 60 ? `${sec}초` : `${Math.floor(sec / 60)}분 ${sec % 60}초`;
}
const methodClass = (m?: string) => (m ? `m-${m.toLowerCase()}` : undefined);

/** ===== 자산 탐색 ===== run: [점검 시작]을 누를 때마다 1씩 늘어 처음부터 다시 돈다 */
export default function CrawlScreen({ target, run }: { target: Target; run: number }) {
  const { screen, go } = useNav();
  const active = screen === "crawl";
  const [state, setState] = useState<Run>(IDLE);
  const done = state.started && state.step >= TOTAL;

  // [점검 시작]마다 처음부터
  useEffect(() => {
    if (run > 0) setState(fresh());
  }, [run]);
  // 주소로 바로 들어왔거나, 취소한 뒤 다시 들어오면 처음부터. 끝난 뒤 돌아오면 끝난 모습 그대로
  useEffect(() => {
    if (active) setState((s) => (s.started && !s.cancelled ? s : fresh()));
  }, [active]);
  // 한 줄씩 진행 — 숨겨져도 계속 돈다(엔진 점검처럼). 일시정지 · 취소 · 끝나면 멈춘다
  useEffect(() => {
    if (!state.started || state.paused || state.cancelled || state.step >= TOTAL) return;
    const wait = state.step === 0 ? FIRST_WAIT_MS : CRAWL_WAIT_MS[CRAWL_EVENTS[state.step].kind];
    const id = window.setTimeout(() => setState((s) => ({ ...s, step: s.step + 1 })), wait);
    return () => window.clearTimeout(id);
  }, [state.started, state.paused, state.cancelled, state.step]);

  const stats = useMemo(() => {
    let sent = 0;
    let logins = 0;
    let skip = 0;
    const found: Found[] = [];
    CRAWL_EVENTS.slice(0, state.step).forEach((e, i) => {
      if (e.kind === "req" || e.kind === "login") sent++;
      if (e.kind === "login") logins++;
      if (e.kind === "skip") skip++;
      for (const f of e.found) found.push({ ...f, at: i });
    });
    const last = state.step > 0 ? CRAWL_EVENTS[state.step - 1] : undefined;
    return { sent, logins, skip, found, last, queue: last?.queue ?? 0, sec: last ? AT[state.step - 1] : 0 };
  }, [state.step]);

  // 찾은 경로 묶음: 처음 나온 순서대로, 묶음 안은 최신 경로가 앞
  const groups = useMemo(() => {
    const order: CrawlGroup[] = [];
    const by = new Map<CrawlGroup, Found[]>();
    for (const f of stats.found) {
      const list = by.get(f.g) ?? [];
      if (!by.has(f.g)) order.push(f.g);
      list.push(f);
      by.set(f.g, list);
    }
    return order.map((g) => ({ g, items: [...(by.get(g) ?? [])].reverse() }));
  }, [stats.found]);

  // 한 줄에 다 안 보이는 묶음은 오른쪽 끝을 흐리게(펼친 묶음 제외) — 화면 폭이 바뀌면 다시 잰다
  const treeRef = useRef<HTMLDivElement>(null);
  const [more, setMore] = useState("");
  const [, setResized] = useState(0);
  useEffect(() => {
    const onResize = () => setResized((n) => n + 1);
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, []);
  useEffect(() => {
    const el = treeRef.current;
    if (!el) return;
    const next = [...el.querySelectorAll<HTMLElement>("[data-ps]")]
      .filter((ps) => ps.scrollWidth > ps.clientWidth + 1)
      .map((ps) => ps.dataset.ps ?? "")
      .join(",");
    setMore((prev) => (prev === next ? prev : next));
  });

  const toggleGroup = (g: CrawlGroup) =>
    setState((s) => ({ ...s, open: s.open.includes(g) ? s.open.filter((x) => x !== g) : [...s.open, g] }));
  const togglePause = () => setState((s) => (s.step >= TOTAL ? s : { ...s, paused: !s.paused }));
  const cancel = () => {
    setState((s) => ({ ...s, cancelled: true }));
    go("entry");
  };

  const known = stats.sent + stats.queue;
  const pct = known ? Math.round((stats.sent / known) * 100) : 0;
  const reads = stats.found.filter((f) => f.m === "GET").length;
  const paused = state.paused && !done;
  const rows = CRAWL_EVENTS.slice(0, state.step)
    .map((e, i) => ({ e, i }))
    .reverse();

  let badge = <span className="badge ok">탐색 중</span>;
  if (done) badge = <span className="badge warn">⏸ 탐색 완료 · 확인 대기</span>;
  else if (paused) badge = <span className="badge warn">일시정지</span>;

  let nowLabel = "지금 보는 곳";
  if (done) nowLabel = "탐색 완료";
  else if (paused) nowLabel = "일시정지됨";

  let tail: ReactNode = (
    <>
      방문 대기 <b>{stats.queue}</b>
    </>
  );
  if (done) tail = stats.logins > 0 ? "로그인 영역 확인됨" : "로그인 영역 없음";

  let pol = "새 경로가 더 나오지 않으면 탐색을 마치고 범위 확인으로 넘어갑니다";
  if (done) pol = "탐색을 마쳤습니다. 범위 확인에서 점검할 경로를 고르세요.";
  else if (paused) pol = "일시정지 중입니다. 다시 시작하면 남은 링크부터 이어서 탐색합니다.";

  return (
    <Screen id="crawl">
      <div className="frame">
        <div className="top">
          <div style={{ minWidth: 0 }}>
            <Brand style={{ fontSize: 16 }}>ABC2LAB</Brand>
            <div style={{ fontSize: 13, color: "var(--muted)", marginTop: 3 }}>
              점검 대상 · <span className="t-url">{target.url}</span>
            </div>
          </div>
          {badge}
        </div>
        <div style={{ padding: "22px 24px 0" }}>
          <MockBanner>
            자산 탐색 화면은 시안이에요. 엔진에는 탐색 단계가 따로 없어서(정찰부터 공격까지 한 흐름) 아래 기록은
            예시예요. 실제 점검의 활동은 진행 화면에서 봐요.
          </MockBanner>
          <div className="pipe">
            {PIPELINE.map((label, i) => (
              <Fragment key={label}>
                {i > 0 && <span className={cx("d", done && i === 1 && "on")} />}
                <span className={cx("p", i === 0 && (done ? "done" : "now"), i === 1 && done && "now")}>{label}</span>
              </Fragment>
            ))}
          </div>
          <h1 className="big">자산 탐색</h1>
          <p className="lead">
            1차 탐색으로 사이트의 링크, 폼, 스크립트를 따라가며 페이지와 API 경로를 모읍니다. 끝나면 찾은 경로 중 점검할
            범위를 고릅니다.
          </p>

          <div className={cx("nowdoing", "cr-now", paused && "paused", done && "done")}>
            {done ? <CheckIcon /> : <span className="spin" aria-hidden="true" />}
            <div style={{ minWidth: 0 }}>
              <div style={{ fontSize: 13, color: "var(--muted)" }}>{nowLabel}</div>
              <div style={{ fontSize: 14, fontWeight: 600 }}>
                {done ? (
                  `찾은 경로 ${stats.found.length}개 · 새 경로가 더 나오지 않아 탐색을 마쳤습니다`
                ) : (
                  <NowText e={stats.last} host={target.host} />
                )}
              </div>
            </div>
            {!done && (
              <button
                className="btn"
                type="button"
                aria-pressed={paused}
                onClick={togglePause}
                style={{ marginLeft: "auto", flex: "none", padding: "8px 13px", fontSize: 14 }}
              >
                {paused ? "다시 시작" : "일시정지"}
              </button>
            )}
          </div>

          <div style={{ fontSize: 14, color: "var(--muted)", marginBottom: 4 }}>
            방문 <strong style={{ color: "var(--fg)" }}>{stats.sent}</strong> / 알려진 링크{" "}
            <strong style={{ color: "var(--fg)" }}>{known}</strong> · 경과 {elapsed(stats.sec)}
          </div>
          <div
            className="bar"
            role="progressbar"
            aria-label="탐색 진행"
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={pct}
          >
            <i style={{ width: `${pct}%` }} />
          </div>

          <div className="dgrid cr-tiles">
            <div className="tile">
              <div className="k">찾은 경로</div>
              <div className="v">{stats.found.length}</div>
              <div className="sub">
                조회 {reads} · 데이터 변경 {stats.found.length - reads}
              </div>
            </div>
            <div className="tile">
              <div className="k">보낸 요청</div>
              <div className="v">{stats.sent}</div>
              <div className="sub">{stats.logins ? `로그인 ${stats.logins}회 포함` : "GET만 보냄"}</div>
            </div>
            <div className="tile">
              <div className="k">요청 안 함</div>
              <div className="v">{stats.skip}</div>
              <div className="sub">규칙으로 막은 링크</div>
            </div>
            <div className="tile">
              <div className="k">방문 대기</div>
              <div className="v">{stats.queue}</div>
              <div className="sub">아직 열지 않은 링크</div>
            </div>
          </div>

          <div className="cr-safe">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" aria-hidden="true">
              <path
                d="M12 3l7 3v5c0 4.5-3 8.2-7 10-4-1.8-7-5.5-7-10V6l7-3z"
                stroke="currentColor"
                strokeWidth="1.8"
                strokeLinejoin="round"
              />
              <path
                d="M9 12l2 2 4-4"
                stroke="currentColor"
                strokeWidth="1.8"
                strokeLinecap="round"
                strokeLinejoin="round"
              />
            </svg>
            <div>
              탐색에서는 <b>보기만 하는 GET 요청</b>만 보냅니다. 데이터를 바꾸는 경로(POST · PUT · PATCH · DELETE)는
              찾아서 기록만 하고, 범위 확인 뒤 승인을 받아 보냅니다. 로그인은 입력한 테스트 계정으로만 합니다.
            </div>
          </div>

          <div className="cr-body">
            <section className="cr-logw" aria-labelledby="cr-log-h">
              <div className="cr-h">
                <h3 id="cr-log-h">탐색 기록</h3>
                <span className="cr-leg">
                  <span className="cr-st s2">200</span>보낸 요청의 응답
                  <span className="dtag ex">기록만</span>보내지 않음
                  <span className="dtag out">요청 안 함</span>규칙으로 막음
                </span>
              </div>
              <div className="cr-log">
                {rows.map(({ e, i }) => (
                  <LogRow key={i} e={e} at={AT[i]} host={target.host} />
                ))}
              </div>
            </section>
            <section className="cr-treew" aria-labelledby="cr-tree-h">
              <div className="cr-h">
                <h3 id="cr-tree-h">
                  찾은 경로 <span className="cnt">{stats.found.length}</span>
                </h3>
              </div>
              <div className="cr-tree" ref={treeRef}>
                {groups.length === 0 && <div className="cr-empty">아직 찾은 경로가 없습니다.</div>}
                {groups.map(({ g, items }) => {
                  const isOpen = state.open.includes(g);
                  return (
                    <div key={g} className={cx("cr-g", isOpen && "open")}>
                      <button
                        type="button"
                        className="cr-gh"
                        aria-expanded={isOpen}
                        aria-controls={`cr-ps-${g}`}
                        onClick={() => toggleGroup(g)}
                      >
                        <span className="cr-gt">{CRAWL_GROUP_LABEL[g]}</span>
                        <span className="cr-gn">{items.length}</span>
                        <span className="cr-chev" aria-hidden="true">
                          ▾
                        </span>
                      </button>
                      <div
                        id={`cr-ps-${g}`}
                        data-ps={g}
                        className={cx("cr-ps", !isOpen && more.split(",").includes(g) && "more")}
                      >
                        {items.map((f) => {
                          const lock = isLockedPath(f.p);
                          return (
                            <span
                              key={`${f.m} ${f.p}`}
                              className={cx(
                                "cr-chip",
                                methodClass(f.m),
                                state.step - 1 - f.at < NEW_SPAN && "new",
                                lock && "lock",
                              )}
                              title={`${f.m} ${f.p}${lock ? " · 전역 제외 규칙이라 요청하지 않음" : ""}`}
                            >
                              <i>{f.m}</i>
                              {f.p}
                            </span>
                          );
                        })}
                      </div>
                    </div>
                  );
                })}
              </div>
              <p className="hint">묶음 이름은 AI가 추정한 기능 분류이며, 범위 확인에서 그대로 이어집니다.</p>
            </section>
          </div>

          <div className="scope-bar">
            <div className="sb-sum">
              <div className="sb-n">
                찾은 경로 <b>{stats.found.length}</b> · {tail}
              </div>
              <div className="sb-pol" role="status">
                {pol}
              </div>
            </div>
            <div className="row" style={{ gap: 8, flex: "none" }}>
              <button className="btn" type="button" onClick={cancel}>
                점검 취소
              </button>
              <button className="btn primary cr-next" type="button" disabled={!done} onClick={() => go("scope")}>
                {done ? "범위 확인" : "탐색 중…"}
              </button>
            </div>
          </div>
        </div>
      </div>
    </Screen>
  );
}

/** 지금 보는 곳: 메서드 · 경로 · 설명 (알림 줄은 문장만) */
function NowText({ e, host }: { e: CrawlEvent | undefined; host: string }) {
  if (!e) return <>대상 연결 확인 중</>;
  if (e.kind === "sys") return <>{e.note || `대상 연결 확인 · ${host} 허용 목록에서 검증됨`}</>;
  return (
    <>
      <code>
        {e.m} {e.p}
      </code>{" "}
      · {e.note}
    </>
  );
}

/** 탐색 기록 한 줄 — 보낸 요청은 응답 코드, 찾기만 한 경로는 '기록만', 규칙에 막힌 링크는 '요청 안 함' */
function LogRow({ e, at, host }: { e: CrawlEvent; at: number; host: string }) {
  if (e.kind === "sys") {
    return (
      <div className="cr-row cr-sys">
        <span className="cr-t">{clock(at)}</span>
        <span className="cr-msg">{e.note || `대상 연결 확인 · ${host} 허용 목록에서 검증됨`}</span>
      </div>
    );
  }
  const n = e.found.length;
  const note = n > 0 && (e.kind === "req" || e.kind === "login") ? `${e.note} · 새 경로 ${n}` : e.note;
  const st = e.status ?? 0;
  let tag = <span className={cx("cr-st", st >= 200 && st < 300 && "s2", st >= 400 && "s4")}>{e.status}</span>;
  if (e.kind === "rec") tag = <span className="dtag ex">기록만</span>;
  if (e.kind === "skip") tag = <span className="dtag out">요청 안 함</span>;
  return (
    <div className="cr-row">
      <span className="cr-t">{clock(at)}</span>
      <span className={cx("op-m", methodClass(e.m))}>{e.m}</span>
      <code className="cr-path">{e.p}</code>
      <span className="cr-note">{note}</span>
      {tag}
    </div>
  );
}

function CheckIcon() {
  return (
    <span className="cr-ok" aria-hidden="true">
      <svg
        aria-hidden="true"
        width="16"
        height="16"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="2.2"
        strokeLinecap="round"
        strokeLinejoin="round"
      >
        <path d="M20 6 9 17l-5-5" />
      </svg>
    </span>
  );
}
