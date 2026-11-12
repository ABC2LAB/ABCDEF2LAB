"use client";

// 계정 분류 화면 (운영자 전용, 목업). 시험 계정 표시 등록·승인(§8·§9-7).
// 데이터는 workbench/live/accounts seam 에서만 온다. 자격(비밀번호·토큰·세션)은 다루지 않는다(절대규칙 2 · S09).
// ※ §8 fail-closed: 승인된 '시험 계정' 표시만 탐색·모델 입력 가능. 표시 없음·승인 대기·판단 불가는 실계정.
// ※ 저장 위치·승인 주체 미정(§9-7, 이동찬 정렬 전) → 이 화면의 등록·승인은 저장되지 않는다(목업, 로컬 상태만). export 없음.

import { useEffect, useState } from "react";

import { cx } from "../../lib/cx";
import {
  ACCOUNT_CLASS_TONE,
  type AccountClass,
  type ClassifiedAccount,
  classLabel,
  getAccounts,
  summarizeAccounts,
  usableAsTestAccount,
} from "../../live/accounts";
import { Icon } from "../../ui/icons";
import s from "../../workbench.module.css";
import { UrlHead } from "./urlParts";

const TONE_TAG: Record<"ok" | "wait" | "stop", string> = { ok: "in", wait: "ap", stop: "ex" };

/** 수 칸 하나 (이름 + 값). */
function Stat({ label, value }: { label: string; value: number }) {
  return (
    <div className={s.now}>
      <span className={s.k}>{label}</span>
      <b className={s.mono}>{value}</b>
    </div>
  );
}

/** 계정 분류 (운영자 전용, 예시). */
export function AccountClassifyLive() {
  const [accounts, setAccounts] = useState<ClassifiedAccount[] | null>(null);
  const [mock, setMock] = useState(false);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let live = true;
    // TODO(이경준): 저장 위치·승인 주체가 정해지면(§9-7, 이동찬) 현재 대상 ref 를 넘겨 실 분류로 점등한다(지금은 예시).
    getAccounts(null)
      .then((r) => {
        if (live) {
          setAccounts(r.accounts);
          setMock(r.mock);
        }
      })
      .catch(() => {
        if (live) setFailed(true);
      });
    return () => {
      live = false;
    };
  }, []);

  // 목업 조작: 로컬 상태만 바꾼다(저장되지 않음). 실 등록·승인은 §9-7 결정 후 seam 에 배선.
  const setCls = (ref: string, cls: AccountClass) =>
    setAccounts(
      (prev) =>
        prev?.map((a) =>
          a.ref === ref
            ? {
                ...a,
                cls,
                approvedBy: cls === "test_approved" ? "operator:mock" : undefined,
                approvedAt: cls === "test_approved" ? new Date().toISOString() : undefined,
              }
            : a,
        ) ?? prev,
    );

  const sum = accounts ? summarizeAccounts(accounts) : null;

  return (
    <>
      <UrlHead title="계정 분류" crumb="계정 분류" />
      <div className={s.vb}>
        <div className={s.vm}>
          {/* §9-7 미정 + 목업 고지 */}
          <div className={cx(s.note, s.warn)} role="note">
            <Icon name="alert" />
            <span>
              저장 위치 · 승인 주체가 아직 정해지지 않았습니다(§9-7, 이동찬 정렬 전). 이 화면의 등록 · 승인은 저장되지
              않는 목업입니다.
            </span>
          </div>
          {/* 절대규칙 2 · S09 고지 */}
          <div className={s.note} role="note">
            <Icon name="info" />
            <span>
              계정 별칭과 분류만 다룹니다. 비밀번호 · 토큰 · 세션 값은 등록 · 표시 · 전송하지 않습니다(절대규칙 2 ·
              S09).
            </span>
          </div>
          {failed ? <p className={s.empty}>계정 분류를 불러오지 못했습니다.</p> : null}
          {!accounts && !failed ? <p className={s.empty}>불러오는 중…</p> : null}

          {accounts && sum ? (
            <section className={s.isec} aria-label="계정 분류">
              <div className={s["crwl-top"]}>
                <Stat label="전체" value={sum.total} />
                <Stat label="시험 계정(쓸 수 있음)" value={sum.usable} />
                <Stat label="승인 대기" value={sum.testPending} />
                <Stat label="실계정 취급" value={sum.unmarked} />
              </div>
              <p className={s.help}>
                §8: 승인된 '시험 계정' 표시만 탐색 · 모델 입력에 쓸 수 있습니다. 표시 없음 · 승인 대기 · 판단 불가는
                실계정으로 봅니다.
              </p>
              <ul className={s.policy} aria-label="계정 목록">
                {accounts.map((a) => {
                  const usable = usableAsTestAccount(a.cls);
                  return (
                    <li key={a.ref}>
                      <span className={cx(s.tag, s[TONE_TAG[ACCOUNT_CLASS_TONE[a.cls]]])}>{classLabel(a.cls)}</span>
                      <span className={s.mono}>{a.ref}</span>
                      <small>
                        {a.origin}
                        {a.role ? ` · ${a.role}` : ""}
                        {usable ? " · 모델 입력 가능" : " · 실계정 취급(모델 입력 불가)"}
                        {a.approvedBy ? ` · 승인 ${a.approvedBy}` : ""}
                        {a.note ? ` · ${a.note}` : ""}
                      </small>
                      <span>
                        {a.cls === "unmarked" ? (
                          <button
                            className={cx(s.btn, s.sm)}
                            type="button"
                            onClick={() => setCls(a.ref, "test_pending")}
                          >
                            시험 표시 등록
                          </button>
                        ) : null}
                        {a.cls === "test_pending" ? (
                          <>
                            <button
                              className={cx(s.btn, s.sm)}
                              type="button"
                              onClick={() => setCls(a.ref, "test_approved")}
                            >
                              승인
                            </button>
                            <button className={cx(s.btn, s.sm)} type="button" onClick={() => setCls(a.ref, "unmarked")}>
                              표시 해제
                            </button>
                          </>
                        ) : null}
                        {a.cls === "test_approved" ? (
                          <button className={cx(s.btn, s.sm)} type="button" onClick={() => setCls(a.ref, "unmarked")}>
                            표시 해제
                          </button>
                        ) : null}
                      </span>
                    </li>
                  );
                })}
              </ul>
              {mock ? (
                <p className={s.help}>예시 데이터입니다. 실제 계정 분류는 저장 위치가 정해진 뒤 연결됩니다.</p>
              ) : null}
            </section>
          ) : null}
        </div>
      </div>
    </>
  );
}
