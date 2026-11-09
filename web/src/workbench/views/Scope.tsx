"use client";

// 범위 확인. 원본 renderScope(개수 · 필터 · 표) + sInsp(선택한 경로의 API 상세) + 시작 버튼.
// 필터·검색·선택은 이 화면만 쓰므로 useState, 포함 여부는 스토어 scopeOn(진행 화면으로 이어짐).

import { Fragment, useState } from "react";

import { GL } from "../data/crawl";
import { EP, epOn, SG } from "../data/scope";
import { FL, type ScopeFilter } from "../data/scopeFilters";
import { cx } from "../lib/cx";
import { useShell } from "../shell";
import { useCurHost, useWb } from "../store";
import { Icon } from "../ui/icons";
import { InspOpenButton, MenuButton, Phases, Pth } from "../ui/parts";
import s from "../workbench.module.css";
import { ApiDetail, ScopeTag } from "./ApiDetail";

export function Scope() {
  const { openInsp } = useShell();
  const host = useCurHost();
  const scopeOn = useWb((st) => st.scopeOn);
  const setEpOn = useWb((st) => st.setEpOn);
  const scopeStart = useWb((st) => st.scopeStart);
  const [filter, setFilter] = useState<ScopeFilter>("all");
  const [query, setQuery] = useState("");
  // 처음 선택은 EP[12] (GET /api/orders/{id}) — 원본 sSel=12
  const [sel, setSel] = useState(12);

  const isOn = (i: number) => epOn(EP[i], scopeOn);
  const onCount = EP.filter((e) => isOn(e.i)).length;
  const apCount = EP.filter((e) => isOn(e.i) && e.chg).length;
  const fn = (FL.find((f) => f[0] === filter) ?? FL[0])[2];
  const q = query.trim().toLowerCase();

  const groups = SG.map((g) => ({
    g,
    rows: EP.filter(
      (e) => e.g === g && fn(e, isOn(e.i)) && (!q || `${e.m} ${e.p} ${e.d}`.toLowerCase().indexOf(q) >= 0),
    ),
  })).filter((x) => x.rows.length > 0);

  return (
    <>
      <header className={cx(s.vh, s["has-ph"])}>
        <div className={s["vh-top"]}>
          <MenuButton />
          <nav className={s.crumb} aria-label="위치">
            <a href="#dashboard">작업대</a>
            <span className={s.sep}>/</span>
            <span className={cx(s.host, s["t-host"])}>{host}</span>
            <span className={s.sep}>/</span>
            <span>범위 확인</span>
          </nav>
        </div>
        <div className={s["vh-row"]}>
          <div className={s["vh-title"]}>
            <h1 tabIndex={-1}>범위 확인</h1>
            <span className={cx(s.pill, s.wait)}>탐색 완료 · 확인 대기</span>
          </div>
          <div className={s["vh-acts"]}>
            <InspOpenButton>상세</InspOpenButton>
            {/* biome-ignore lint/a11y/useValidAnchor: 해시 라우팅 링크라 이동은 href 가 하고, onClick 은 다음 진행 화면이 처음부터 시작하도록 표시만 한다 (원본 sStart) */}
            <a className={cx(s.btn, s.pri)} href="#live" id="sStart" onClick={scopeStart}>
              선택한 범위로 점검 시작
              <Icon name="arrow" />
            </a>
          </div>
        </div>
        <Phases phase="scope" />
      </header>
      <div className={s.vb}>
        <div className={s.vm}>
          <p className={s.lead}>
            자산 탐색에서 찾은 경로입니다. 점검할 경로를 고르면 그 범위로 점검을 시작합니다. 포함 여부와 승인 필요
            여부는 메서드와 경로처럼 <b>관찰한 사실</b>로만 정하고, 묶음과 설명은 AI 추정입니다.
          </p>
          <div className={s.scount} id="sCount">
            <span>
              엔드포인트<b>{EP.length}</b>
            </span>
            <span>
              포함<b>{onCount}</b>
            </span>
            <span>
              제외<b>{EP.length - onCount}</b>
            </span>
            <span className={s.ap}>
              승인 필요<b>{apCount}</b>
            </span>
            <span>
              로그인 영역<b style={{ fontFamily: "var(--sans)", fontSize: 13 }}>예시</b>
            </span>
          </div>
          {/* biome-ignore lint/a11y/useAriaPropsSupportedByRole: 원본 구조 그대로 (시작 전 점검 묶음의 이름) */}
          <div className={s.pre} aria-label="시작 전 점검">
            <span className={s.k}>시작 전 점검</span>
            <span className={s.ok}>
              <Icon name="check" />
              대상 연결 200 · 120ms
            </span>
            <span className={s.ok}>
              <Icon name="check" />
              계정 A 로그인
            </span>
            <span className={s.ok}>
              <Icon name="check" />
              계정 B 로그인 · A·B 비교 가능
            </span>
            <span className={s.ok}>
              <Icon name="check" />
              모델 연결 · 로컬
            </span>
          </div>
          <div className={s.filters}>
            {/* biome-ignore lint/a11y/useSemanticElements: 원본 구조 그대로 (버튼 묶음 div[role=group]) */}
            <div className={s.filters} role="group" aria-label="보기 필터" id="sFilters">
              {FL.map(([k, label, f]) => (
                <button
                  key={k}
                  className={s.fchip}
                  type="button"
                  data-sf={k}
                  aria-pressed={filter === k}
                  onClick={() => setFilter(k)}
                >
                  {label}
                  <span className={s.c}>{EP.filter((e) => f(e, isOn(e.i))).length}</span>
                </button>
              ))}
            </div>
            <input
              className={cx(s.inp, s.search)}
              id="sQ"
              type="search"
              placeholder="경로 검색"
              aria-label="경로 검색"
              value={query}
              onChange={(ev) => setQuery(ev.target.value)}
            />
          </div>
          <div className={s.stbl} id="sTable">
            {groups.length ? (
              groups.map(({ g, rows }) => (
                <Fragment key={g}>
                  <div className={s.sgh}>
                    {GL[g]}
                    <span className={s.c}>{rows.length}</span>
                    <span className={s.ai}>AI 추정 묶음</span>
                  </div>
                  {rows.map((e) => {
                    const on = isOn(e.i);
                    const cur = sel === e.i;
                    return (
                      <div key={e.i} className={cx(s.srow, !on && s.out, cur && s.sel)}>
                        <span className={s.ck}>
                          <input
                            type="checkbox"
                            data-son={e.i}
                            aria-label={`${e.m} ${e.p} 점검에 포함`}
                            checked={on}
                            disabled={e.lock}
                            onChange={(ev) => {
                              // 체크한 줄을 고른 줄로 (원본: 다시 그린 뒤 같은 체크 상자에 포커스 — 같은 요소라 그대로 유지)
                              setEpOn(e.i, ev.target.checked);
                              setSel(e.i);
                            }}
                          />
                        </span>
                        <button
                          className={s.sb}
                          type="button"
                          data-ssel={e.i}
                          aria-current={cur ? "true" : undefined}
                          onClick={() => {
                            setSel(e.i);
                            openInsp();
                          }}
                        >
                          <span className={cx(s.m, s[e.m])}>{e.m}</span>
                          <span className={s.p}>
                            <Pth p={e.p} />
                          </span>
                          <span className={s.d}>{e.d}</span>
                          <ScopeTag e={e} on={on} />
                        </button>
                      </div>
                    );
                  })}
                </Fragment>
              ))
            ) : (
              <p className={s.empty} style={{ padding: 14 }}>
                조건에 맞는 경로가 없습니다.
              </p>
            )}
          </div>
          <details className={s.rulebox}>
            <summary>
              <Icon name="chev" />
              점검 중 새로 찾은 경로 처리 규칙<span className={s.c}>6</span>
            </summary>
            <ul className={s.rules}>
              <li>
                <span>허용 주소 밖</span>
                <span className={cx(s.tag, s.out)}>요청 안 함 · 기록만</span>
              </li>
              <li>
                <span>제외 규칙과 같은 모양</span>
                <span className={cx(s.tag, s.ex)}>자동 제외</span>
              </li>
              <li>
                <span>GET · 보기만 하는 요청</span>
                <span className={cx(s.tag, s.in)}>자동 포함</span>
              </li>
              <li>
                <span>POST · PUT · PATCH</span>
                <span className={cx(s.tag, s.ap)}>포함 · 보낼 때 승인</span>
              </li>
              <li>
                <span>DELETE · 초기화 · 로그아웃</span>
                <span className={cx(s.tag, s.ex)}>자동 제외</span>
              </li>
              <li>
                <span>관리자 · 처음 보는 권한 영역</span>
                <span className={cx(s.tag, s.ap)}>확인 요청</span>
              </li>
            </ul>
          </details>
        </div>
        <aside className={s.vi} id="sInsp" aria-label="선택한 경로 상세">
          <ApiDetail e={EP[sel]} px="s" />
        </aside>
      </div>
    </>
  );
}
