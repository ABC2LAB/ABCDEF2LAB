"use client";

// 계정 분류 화면 (운영자 전용). 시험 계정 표시 분류를 실 API 로 읽고 쓴다(§8·§9-7).
// 데이터는 workbench/live/accounts seam 에서만 온다. 토큰·세션은 다루지 않는다(절대규칙 2 · S09).
// ※ 대상을 고른 뒤의 로그인 방식 · 계정 등록 · 시험 표시 · 자격 입력은 공용 부품 AccountManager 가 그린다(새 점검 '계정
//   분류' 칸도 같은 부품, 사용자 11/24). 아래 주석은 그 부품이 지키는 규칙이다. 자격 칸(사용자명·비밀번호)은 React
//   상태에 두지 않는 비제어 입력이다. 보낼 때 DOM 칸에서 한 번 읽고 바로 비운다(보내지 않는 경우도). URL·브라우저 저장소·
//   로그에 넣지 않고, 등록 여부는 서버 목록의 credential_configured 로만 보인다. 브라우저 비밀번호 관리자에 들어가지
//   않게 form 요소를 쓰지 않고 autoComplete="off"·관리자 무시 표시(data-1p-ignore 등)를 단다 — 이 칸은 운영자 자신의
//   로그인이 아니라 대상 시험 계정 자격이다. 브라우저가 모든 경우에 저장 제안을 안 한다고 보장하지는 않는다(#842 경준 리뷰).
// ※ §8 fail-closed: 서버 분류 'test'(승인된 시험)만 시험 계정. real·미표시·판단 불가는 실계정 취급.
// ※ 이 분류가 지금 탐색·모델 입력 권한을 바로 주지 않는다(허용은 승인된 일회용 시험 계정의 대상 발급 세션에 한함).
// ※ 쓰기: 등록(label)·시험 표시(mark-test, 진술 동의 필요)·표시 해제(unmark). 자격 값은 보내지 않는다. export 없음.

import { useEffect, useRef, useState } from "react";

import { cx } from "../../lib/cx";
import { getProductTargets, type ProductTargetRef } from "../../live/accounts";
import { useShell } from "../../shell";
import { useWb } from "../../store";
import { Icon } from "../../ui/icons";
import s from "../../workbench.module.css";
import { AccountManager } from "./AccountManager";
import { UrlHead } from "./urlParts";

/** 계정 분류 (운영자 전용). */
export function AccountClassifyLive() {
  const [targets, setTargets] = useState<ProductTargetRef[] | null>(null);
  const [targetsFailed, setTargetsFailed] = useState(false);
  const [selected, setSelected] = useState<ProductTargetRef | null>(null);

  // 승인된 대상 목록(선택용). 화면에 들어올 때마다 다시 읽는다 — 작업대는 모든 화면을 시작 때 숨긴 채 붙여서, 처음 한
  // 번만 읽으면 그 뒤 승인한 대상이 "승인된 대상이 없습니다"로 남았다(사용자 2026-11-22, 1차 리허설 F2). 정책 선언 화면과
  // 같은 방식이다
  const { route, goTo } = useShell();
  const visible = route === "classify";
  // 새 점검에서 '계정 분류'로 오면 그 대상을 미리 고른다(사용자 11/23). 한 번 온 요청은 한 번만 쓴다
  const focus = useWb((st) => st.focusTarget);
  const usedFocus = useRef(0);
  useEffect(() => {
    if (!visible || !targets || !focus || focus.seq === usedFocus.current) return;
    usedFocus.current = focus.seq;
    const found = targets.find((t) => t.id === focus.id);
    if (found) setSelected(found);
  }, [visible, targets, focus]);
  useEffect(() => {
    if (!visible) return;
    let live = true;
    getProductTargets()
      .then((t) => {
        if (live) {
          setTargets(t);
          setTargetsFailed(false);
        }
      })
      .catch(() => {
        if (live) setTargetsFailed(true);
      });
    return () => {
      live = false;
    };
  }, [visible]);

  return (
    <>
      <UrlHead
        title="계정 분류"
        crumb="계정 분류"
        acts={
          <button className={cx(s.btn, s.sm)} type="button" onClick={() => goTo("new")}>
            새 점검으로
          </button>
        }
      />
      {/* 오른쪽 패널에 둘 내용이 없어 가운데 칸을 끝까지 쓴다(사용자 11/23) */}
      <div className={cx(s.vb, s.solo)}>
        <div className={s.vm}>
          {/* 절대규칙 2 · S09 고지 */}
          <div className={s.note} role="note">
            <Icon name="info" />
            <span>
              계정 별칭(label)과 분류만 다룹니다. 비밀번호 · 토큰 · 세션 값은 읽지도 보이지도 않고, 등록 · 표시에도
              보내지 않습니다(절대규칙 2 · S09). 자격 입력 칸의 값은 서버 보호 파일로만 보내고 화면에 남기지 않습니다.
            </span>
          </div>

          {/* 대상 선택 */}
          <section className={s.isec} aria-label="대상 선택">
            <h2>대상 선택</h2>
            {targetsFailed ? <p className={s.empty}>대상 목록을 불러오지 못했습니다.</p> : null}
            {!targets && !targetsFailed ? <p className={s.empty}>대상 목록 불러오는 중…</p> : null}
            {targets && targets.length === 0 ? (
              <p className={s.empty}>승인된 대상이 없습니다. 대상을 먼저 등록 · 승인하면 그 대상의 계정이 보입니다.</p>
            ) : null}
            {targets && targets.length > 0 ? (
              <select
                className={s.inp}
                aria-label="분류할 대상"
                value={selected?.id ?? ""}
                onChange={(e) => setSelected(targets.find((t) => t.id === e.target.value) ?? null)}
              >
                <option value="">대상 선택…</option>
                {targets.map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.origin}
                    {t.state ? ` · ${t.state}` : ""}
                  </option>
                ))}
              </select>
            ) : null}
          </section>

          {selected ? <AccountManager target={selected} /> : null}
        </div>
      </div>
    </>
  );
}
