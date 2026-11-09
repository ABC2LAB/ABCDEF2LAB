"use client";

// 대시보드: 요약 칸 · 지금 처리할 일 · 진행 중인 점검 · 최근 끝난 점검 · 선택한 항목 상세.
// 원본 syncDecisions 의 대시보드 부분 · renderTodo · renderActive · renderDone · dInsp · pathDecideInsp · 대시보드 클릭 처리.
// 줄을 누르면 useShell().dashPick (dSel 바꾸기 + 줄 스크롤 + 상세 서랍). 줄에는 data-dsel 을 남긴다(dashPick 이 찾는다).

import { type ReactNode, useEffect, useState } from "react";

import { REQ_CHECKOUT, TODO } from "../data/dashboard";
import { cx } from "../lib/cx";
import { apiActive, type PillSpec, shopMeta, todoOpen, todoPill } from "../lib/dashboard";
import { pending, pendingSub } from "../lib/decisions";
import { lcur, livePill, lpct, lremain } from "../lib/liveState";
import { clock, hms } from "../lib/time";
import { useShell } from "../shell";
import { type DSel, useCurHost, useWb } from "../store";
import { Icon } from "../ui/icons";
import { Ep, InspHead, InspOpenButton, Isec, Kv, MenuButton, On, ReportLink, Sev } from "../ui/parts";
import s from "../workbench.module.css";

/** 상태 알약 `<span class="pill …">`. */
function Pill({ p }: { p: PillSpec }) {
  return <span className={cx(s.pill, p.cls && s[p.cls])}>{p.label}</span>;
}

/** 맨 위 요약 칸 네 개. '결정 대기' 칸만 결정에 따라 바뀐다 (원본 syncDecisions 의 dDecide). */
function Strip() {
  const S = useWb((st) => st.S);
  const n = pending(S);
  return (
    <div className={s.strip}>
      <div className={cx(s.cell, s.crit)}>
        <div className={s.k}>발견된 취약점 · 최근 30일</div>
        <div className={s.v}>12</div>
        <div className={s.stack} aria-hidden="true">
          <i style={{ flex: 3, background: "var(--crit)" }} />
          <i style={{ flex: 2, background: "var(--hi)" }} />
          <i style={{ flex: 6, background: "var(--mid)" }} />
          <i style={{ flex: 1, background: "var(--lo)" }} />
        </div>
        <div className={s.sub}>심각 3 · 높음 2 · 중간 6 · 낮음 1</div>
      </div>
      <div className={cx(s.cell, n > 0 && s.warn)} id="dDecide">
        <div className={s.k}>결정 대기</div>
        <div className={s.v} id="dDecideN">
          {n}
        </div>
        <div className={s.sub} id="dDecideSub">
          {pendingSub(S)}
        </div>
      </div>
      <div className={s.cell}>
        <div className={s.k}>결과 확인</div>
        <div className={s.v}>2</div>
        <div className={s.sub}>판단 불가 · 미검사 영역</div>
      </div>
      <div className={s.cell}>
        <div className={s.k}>토큰 사용량 · 최근 30일</div>
        <div className={s.v}>
          4.2<small>M</small>
        </div>
        <div className={s.sub}>점검당 평균 0.9M</div>
      </div>
    </div>
  );
}

/** 목록 한 줄 `button.row[data-dsel]`. aria-current 는 '지금 처리할 일' 줄에만 붙는다(원본 그대로). */
function Row({ id, current, children }: { id: DSel; current?: boolean; children: ReactNode }) {
  const { dashPick } = useShell();
  const sel = useWb((st) => st.dSel === id);
  return (
    <button
      className={cx(s.row, sel && s.sel)}
      type="button"
      data-dsel={id}
      aria-current={current && sel ? "true" : undefined}
      onClick={() => dashPick(id)}
    >
      {children}
    </button>
  );
}

/** 지금 처리할 일. 원본 renderTodo. */
function TodoSection() {
  const S = useWb((st) => st.S);
  return (
    <section className={s.sec} aria-labelledby="dTodoH">
      <div className={s.sh}>
        <h2 id="dTodoH">지금 처리할 일</h2>
        <span className={s.n} id="dTodoN">
          {todoOpen(TODO, S)}건
        </span>
      </div>
      <div className={s.list} id="dTodo">
        {TODO.map((r) => (
          <Row key={r.id} id={r.id} current>
            <span className={s["r-main"]}>
              <span className={s["r-t"]}>
                <span className={s["r-host"]}>{r.host}</span>
                {r.t}
              </span>
              <span className={s["r-s"]}>{r.s}</span>
            </span>
            <span className={s["r-side"]}>
              <Pill p={todoPill(r.id, S)} />
              <span className={s["r-time"]}>{r.time}</span>
            </span>
          </Row>
        ))}
      </div>
    </section>
  );
}

/** 진행 중인 점검. 원본 renderActive (점검 진행 재생에 따라 shop 줄이 바뀐다). */
function ActiveSection() {
  const S = useWb((st) => st.S);
  const live = useWb((st) => st.live);
  const host = useCurHost();
  const api = apiActive(S);
  const rows: ReadonlyArray<{ id: DSel; host: string; meta: string; pill: PillSpec; pct: number; wait: boolean }> = [
    { id: "act-shop", host, meta: shopMeta(live), pill: livePill(live), pct: lpct(live), wait: false },
    {
      id: "act-api",
      host: "api.store-dev.com",
      meta: `오늘 09:15 시작 · ${api.meta}`,
      pill: api.pill,
      pct: 35,
      wait: api.wait,
    },
  ];
  return (
    <section className={s.sec} aria-labelledby="dActH">
      <div className={s.sh}>
        <h2 id="dActH">진행 중인 점검</h2>
        <span className={s.n}>2건</span>
      </div>
      <div className={s.list} id="dActive">
        {rows.map((r) => (
          <Row key={r.id} id={r.id}>
            <span className={s["r-main"]}>
              <span className={s["r-t"]}>
                <span className={s["r-host"]}>{r.host}</span>
              </span>
              <span className={s["r-s"]}>{r.meta}</span>
              <span className={cx(s["r-bar"], r.wait && s.wait)} aria-hidden="true">
                <i style={{ width: `${r.pct}%` }} />
              </span>
            </span>
            <span className={s["r-side"]}>
              <Pill p={r.pill} />
              <span className={s["r-time"]}>{r.pct}%</span>
            </span>
          </Row>
        ))}
      </div>
    </section>
  );
}

const DONE: ReadonlyArray<{ id: DSel; host: string; meta: string; side: ReactNode }> = [
  {
    id: "done-blog",
    host: "blog.example.com",
    meta: "10/25 · 28분 · 커버리지 91%",
    side: (
      <>
        <Sev k="crit">심각 1</Sev>
        <Sev k="hi">높음 1</Sev>
        <span className={s["r-time"]}>발견 6</span>
      </>
    ),
  },
  {
    id: "done-docs",
    host: "docs.example.com",
    meta: "10/24 · 14분 · 커버리지 76%",
    side: <span className={s.pill}>발견 없음</span>,
  },
  { id: "done-app", host: "app.sample.io", meta: "10/23 · 21분", side: <span className={s.pill}>판단 불가 2</span> },
];

/** 최근 끝난 점검. 원본 renderDone. */
function DoneSection() {
  return (
    <section className={s.sec} aria-labelledby="dDoneH">
      <div className={s.sh}>
        <h2 id="dDoneH">최근 끝난 점검</h2>
        <span className={s.aside}>전체 기록 24건</span>
      </div>
      <div className={s.list} id="dDone">
        {DONE.map((r) => (
          <Row key={r.id} id={r.id}>
            <span className={s["r-main"]}>
              <span className={s["r-t"]}>
                <span className={s["r-host"]}>{r.host}</span>
              </span>
              <span className={s["r-s"]}>{r.meta}</span>
            </span>
            <span className={s["r-side"]}>{r.side}</span>
          </Row>
        ))}
      </div>
    </section>
  );
}

/** 상세 패널 맨 아래 버튼 줄 (제목 없는 isec). */
function Acts({ children }: { children: ReactNode }) {
  return (
    <Isec>
      <div className={s.acts}>{children}</div>
    </Isec>
  );
}

/** 결정 결과 상자 (ok = 체크 · no = x). */
function Result({ ok, children }: { ok: boolean; children: ReactNode }) {
  return (
    <div className={cx(s.result, ok ? s.ok : s.no)}>
      <Icon name={ok ? "check" : "x"} />
      <div>{children}</div>
    </div>
  );
}

/** 상세 머리의 작은 제목 '… · <span class="host">host</span>'. */
function Eyebrow({ label, host }: { label: string; host: string }) {
  return (
    <>
      {label} · <span className={s.host}>{host}</span>
    </>
  );
}

/** 승인 요청 (appr). 결정하면 버튼 대신 결과 상자. */
function ApprInsp() {
  const S = useWb((st) => st.S);
  const setAppr = useWb((st) => st.setAppr);
  return (
    <>
      <InspHead
        eyebrow={<Eyebrow label="승인 요청" host="api.store-dev.com" />}
        title="결제 금액 변경 요청"
        chips={
          <>
            <Pill p={todoPill("appr", S)} />
            <span className={cx(s.tag, s.ap)}>데이터를 바꾸는 요청</span>
          </>
        }
      />
      <Isec t="보낼 요청">
        <pre className={s.code}>{REQ_CHECKOUT}</pre>
      </Isec>
      <Isec t="확인하려는 것">
        <p>결제 금액을 1원으로 바꿔 보내도 주문이 만들어지는지 확인합니다. 업무 흐름 우회(WSTG-BUSL-01)입니다.</p>
      </Isec>
      <Isec t="보내기 전에 알아둘 것">
        <div className={cx(s.note, s.warn)}>
          <Icon name="alert" />
          <div>
            실제 주문이 만들어질 수 있습니다. 리셋할 수 있는 시험환경일 때만 승인하세요. 결정은 감사 로그에 남습니다.
          </div>
        </div>
      </Isec>
      {S.appr ? (
        <Isec>
          {S.appr === "ok" ? (
            <Result ok>승인했습니다. 이 요청 1건을 보내고 결제 흐름 점검을 이어 갑니다.</Result>
          ) : (
            <Result ok={false}>거부했습니다. 이 요청은 보내지 않고, 결제 금액 변경 항목은 판단 불가로 남깁니다.</Result>
          )}
        </Isec>
      ) : (
        <Acts>
          <button className={cx(s.btn, s.pri)} type="button" onClick={() => setAppr("ok")}>
            승인하고 보내기
          </button>
          <button className={s.btn} type="button" onClick={() => setAppr("no")}>
            거부
          </button>
        </Acts>
      )}
    </>
  );
}

/**
 * 새로 찾은 경로 /admin/export (path). 원본 pathDecideInsp.
 * 결정(setPath)은 스토어에 있어서 점검 진행 화면의 결정 상자·상세도 함께 바뀐다.
 */
function PathDecideInsp() {
  const S = useWb((st) => st.S);
  const setPath = useWb((st) => st.setPath);
  return (
    <>
      <InspHead
        eyebrow={<Eyebrow label="새로 찾은 경로" host="shop.example.com" />}
        title={<Ep m="GET" p="/admin/export" />}
        chips={
          <>
            <Pill p={todoPill("path", S)} />
            <span className={cx(s.tag, s.ap)}>관리자 경로</span>
          </>
        }
      />
      <Isec t="관찰한 사실">
        <Kv
          rows={[
            ["찾은 곳", "관리자 메뉴 스크립트 안 문자열"],
            ["찾은 시각", "오늘 18:31"],
            ["처리 규칙", "관리자 경로 · 처음 보는 권한 영역은 확인 요청"],
          ]}
        />
      </Isec>
      <Isec t="결정하면">
        <p>
          포함하면 계정 A, 계정 B, 비로그인으로 접근되는지 조회 요청만 보내 확인합니다. 제외하면 보내지 않고 기록만
          남깁니다.
        </p>
      </Isec>
      {S.path ? (
        <Isec>
          {S.path === "in" ? (
            <Result ok>포함했습니다. 다음 계획 단계에서 세 가지 권한으로 확인합니다.</Result>
          ) : (
            <Result ok={false}>제외했습니다. 이 경로는 보내지 않고 기록만 남깁니다.</Result>
          )}
        </Isec>
      ) : (
        <Acts>
          <button className={cx(s.btn, s.pri)} type="button" onClick={() => setPath("in")}>
            점검에 포함
          </button>
          <button className={s.btn} type="button" onClick={() => setPath("ex")}>
            제외
          </button>
        </Acts>
      )}
    </>
  );
}

/** '이 주소로 다시 점검': 새 점검 주소를 채우고 그 화면으로 (원본 data-rescan). */
function RescanButton({ url }: { url: string }) {
  const { goTo } = useShell();
  const nPrefill = useWb((st) => st.nPrefill);
  return (
    <Acts>
      <button
        className={cx(s.btn, s.pri)}
        type="button"
        onClick={() => {
          nPrefill(url);
          goTo("new");
        }}
      >
        이 주소로 다시 점검
      </button>
    </Acts>
  );
}

/** 판단 불가 2건 (und · done-app). */
function AppInsp() {
  return (
    <>
      <InspHead
        eyebrow={<Eyebrow label="결과 확인" host="app.sample.io" />}
        title="판단 불가 2건"
        chips={<span className={s.pill}>결과 확인</span>}
      />
      <Isec>
        <p>
          일부 응답이 느려 결과를 확정하지 못했습니다. 판단 불가는 안전하다는 뜻이 아닙니다. 사람이 확인하거나 다시
          점검해야 확정됩니다.
        </p>
      </Isec>
      <Isec t="점검 정보">
        <Kv
          rows={[
            ["점검일", "10/23"],
            ["소요", "21분"],
            ["판단 불가", "2건"],
          ]}
        />
      </Isec>
      <RescanButton url="https://app.sample.io" />
    </>
  );
}

/** 미검사 영역 있음 (cov · done-docs). */
function DocsInsp() {
  return (
    <>
      <InspHead
        eyebrow={<Eyebrow label="결과 확인" host="docs.example.com" />}
        title="미검사 영역 있음"
        chips={<span className={s.pill}>커버리지 76%</span>}
      />
      <Isec>
        <p>
          점검하지 못한 24%는 결과에 들어가지 않았습니다. 미검사 영역은 안전을 뜻하지 않습니다. 발견이 없다는 결과도
          점검한 76% 안에서만 맞습니다.
        </p>
      </Isec>
      <Isec t="점검 정보">
        <Kv
          rows={[
            ["점검일", "10/24"],
            ["소요", "14분"],
            ["발견", "없음"],
          ]}
        />
      </Isec>
      <RescanButton url="https://docs.example.com" />
    </>
  );
}

/** 진행 중인 점검 shop (act-shop). 점검 진행 재생을 따라 바뀐다. */
function ShopInsp() {
  const live = useWb((st) => st.live);
  const path = useWb((st) => st.S.path);
  const host = useCurHost();
  const lc = lcur(live);
  return (
    <>
      <InspHead
        eyebrow={<Eyebrow label={live.done ? "끝난 점검" : "진행 중"} host={host} />}
        title={`점검 진행 ${lpct(live)}%`}
        chips={<Pill p={livePill(live)} />}
      />
      <Isec t="지금 하는 일">
        {lc ? (
          <p>
            {lc.t}
            {lc.on ? (
              <>
                {" · "}
                <On on={lc.on} />
              </>
            ) : null}
          </p>
        ) : (
          <p>{live.done ? "점검을 마쳤습니다." : "점검을 종료했습니다."}</p>
        )}
      </Isec>
      <Isec t="점검 정보">
        <Kv
          rows={[
            ["시작", `오늘 ${hms(live.start).slice(0, 5)}`],
            ["남은 시간", lc ? `약 ${lremain(live)}분` : "—"],
            ["발견", `${live.forder.length}건`],
            ["결정 대기", path || !live.decide ? "없음" : "새 경로 1"],
          ]}
        />
      </Isec>
      <Acts>
        <a className={cx(s.btn, s.pri)} href="#live">
          진행 화면 열기
          <Icon name="arrow" />
        </a>
      </Acts>
    </>
  );
}

/** 진행 중인 점검 api.store-dev.com (act-api). 승인 전에는 승인 요청으로 가는 버튼. */
function ApiInsp() {
  const { dashPick } = useShell();
  const appr = useWb((st) => st.S.appr);
  return (
    <>
      <InspHead
        eyebrow={<Eyebrow label="진행 중" host="api.store-dev.com" />}
        title="점검 진행 35%"
        chips={<Pill p={appr ? { cls: "run", label: "점검 중" } : { cls: "wait", label: "승인 대기" }} />}
      />
      <Isec t="지금 하는 일">
        {appr ? (
          <p>결제 흐름 점검을 이어 가는 중입니다.</p>
        ) : (
          <p>
            결제 흐름 점검 전 승인 대기 · <code>POST /api/checkout</code>
          </p>
        )}
      </Isec>
      <Isec t="점검 정보">
        <Kv
          rows={[
            ["시작", "오늘 09:15"],
            ["보낸 요청", "412건"],
            ["발견", "2건 · 심각 1"],
          ]}
        />
      </Isec>
      {appr ? null : (
        <Acts>
          <button className={cx(s.btn, s.pri)} type="button" onClick={() => dashPick("appr")}>
            승인 요청 보기
          </button>
        </Acts>
      )}
    </>
  );
}

/** 끝난 점검 blog.example.com (done-blog). 보고서 링크는 보고서 대상을 blog 로 바꾼다. */
function BlogInsp() {
  return (
    <>
      <InspHead
        eyebrow={<Eyebrow label="끝난 점검" host="blog.example.com" />}
        title="발견 6건"
        chips={
          <>
            <Sev k="crit" />
            <Sev k="hi" />
            <span className={cx(s.pill, s.ok)}>점검 완료</span>
          </>
        }
      />
      <Isec t="점검 정보">
        <Kv
          rows={[
            ["점검일", "10/25 14:03"],
            ["소요", "28분"],
            ["커버리지", "91%"],
            ["판정", "확정 3 · 의심 2 · 판단 불가 1"],
          ]}
        />
      </Isec>
      <Acts>
        <ReportLink kind="blog" className={cx(s.btn, s.pri)}>
          보고서 열기
          <Icon name="arrow" />
        </ReportLink>
      </Acts>
    </>
  );
}

/** 고른 항목 → 상세. 판단 불가(und)와 app.sample.io, 미검사(cov)와 docs.example.com 은 같은 상세. */
const INSP: Readonly<Record<DSel, () => ReactNode>> = {
  appr: ApprInsp,
  path: PathDecideInsp,
  und: AppInsp,
  "done-app": AppInsp,
  cov: DocsInsp,
  "done-docs": DocsInsp,
  "act-shop": ShopInsp,
  "act-api": ApiInsp,
  "done-blog": BlogInsp,
};

/** 선택한 항목 상세. 원본 dInsp. */
function DashInsp() {
  const dSel = useWb((st) => st.dSel);
  const Body = INSP[dSel];
  return <Body />;
}

export function Dashboard() {
  // 조회 시각: 렌더 중에 시계를 읽지 않도록 마운트 뒤에 채운다 (원본은 시작할 때 한 번 + 새로고침)
  const [snap, setSnap] = useState("");
  useEffect(() => {
    setSnap(`조회 ${clock()}`);
  }, []);
  return (
    <>
      <header className={s.vh}>
        <div className={s["vh-top"]}>
          <MenuButton />
          <span className={s.crumb}>작업대</span>
        </div>
        <div className={s["vh-row"]}>
          <div className={s["vh-title"]}>
            <h1 tabIndex={-1}>대시보드</h1>
            <span className={s.snap} id="dSnap">
              {snap}
            </span>
          </div>
          <div className={s["vh-acts"]}>
            <button className={s.btn} type="button" id="dRefresh" onClick={() => setSnap(`조회 ${clock()}`)}>
              <Icon name="refresh" />
              새로고침
            </button>
            <InspOpenButton>상세</InspOpenButton>
          </div>
        </div>
      </header>
      <div className={s.vb}>
        <div className={s.vm}>
          <Strip />
          <TodoSection />
          <ActiveSection />
          <DoneSection />
        </div>
        <aside className={s.vi} id="dInsp" aria-label="선택한 항목 상세">
          <DashInsp />
        </aside>
      </div>
    </>
  );
}
