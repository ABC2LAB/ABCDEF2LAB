"use client";

// 계정 분류 화면 (운영자 전용). 시험 계정 표시 분류를 실 API 로 읽고 쓴다(§8·§9-7).
// 데이터는 workbench/live/accounts seam 에서만 온다. 자격(비밀번호·토큰·세션)은 다루지 않는다(절대규칙 2 · S09).
// ※ §8 fail-closed: 서버 분류 'test'(승인된 시험)만 시험 계정. real·미표시·판단 불가는 실계정 취급.
// ※ 이 분류가 지금 탐색·모델 입력 권한을 바로 주지 않는다(허용은 승인된 일회용 시험 계정의 대상 발급 세션에 한함).
// ※ 쓰기: 등록(label)·시험 표시(mark-test, 진술 동의 필요)·표시 해제(unmark). 자격 값은 보내지 않는다. export 없음.

import { useEffect, useState } from "react";

import { cx } from "../../lib/cx";
import {
  ACCOUNT_CLASS_TONE,
  type ClassifiedAccount,
  classLabel,
  getAccounts,
  getProductTargets,
  isValidAccountLabel,
  markTestAccount,
  type ProductTargetRef,
  registerAccount,
  summarizeAccounts,
  unmarkAccount,
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

/** 계정 분류 (운영자 전용). */
export function AccountClassifyLive() {
  const [targets, setTargets] = useState<ProductTargetRef[] | null>(null);
  const [targetsFailed, setTargetsFailed] = useState(false);
  const [selected, setSelected] = useState<ProductTargetRef | null>(null);
  const [accounts, setAccounts] = useState<ClassifiedAccount[] | null>(null);
  const [statement, setStatement] = useState("");
  const [accountsFailed, setAccountsFailed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [writeError, setWriteError] = useState("");
  const [confirming, setConfirming] = useState<string | null>(null);
  const [newLabel, setNewLabel] = useState("");

  // 승인된 대상 목록(선택용).
  useEffect(() => {
    let live = true;
    getProductTargets()
      .then((t) => {
        if (live) setTargets(t);
      })
      .catch(() => {
        if (live) setTargetsFailed(true);
      });
    return () => {
      live = false;
    };
  }, []);

  // 고른 대상의 계정 분류(읽기). 대상이 바뀌면 다시 읽고 입력 상태를 비운다.
  useEffect(() => {
    let live = true;
    setAccounts(null);
    setStatement("");
    setAccountsFailed(false);
    setConfirming(null);
    setNewLabel("");
    setWriteError("");
    if (!selected) return;
    getAccounts(selected)
      .then((r) => {
        if (live) {
          setAccounts(r.accounts);
          setStatement(r.statement);
        }
      })
      .catch(() => {
        if (live) setAccountsFailed(true);
      });
    return () => {
      live = false;
    };
  }, [selected]);

  // 쓰기 공통: 작업 → 성공하면 목록 다시 읽기. 자격 값은 어떤 작업도 보내지 않는다.
  function doWrite(action: (id: string, label: string) => Promise<void>, label: string) {
    if (busy || !selected) return;
    const t = selected;
    setBusy(true);
    setWriteError("");
    action(t.id, label)
      .then(() => getAccounts(t))
      .then((r) => {
        setAccounts(r.accounts);
        setStatement(r.statement);
        setConfirming(null);
        setNewLabel("");
      })
      .catch(() => setWriteError("작업을 처리하지 못했습니다. 권한 · 대상 상태를 확인하고 다시 시도하세요."))
      .finally(() => setBusy(false));
  }

  const sum = accounts ? summarizeAccounts(accounts) : null;

  return (
    <>
      <UrlHead title="계정 분류" crumb="계정 분류" />
      <div className={s.vb}>
        <div className={s.vm}>
          {/* 절대규칙 2 · S09 고지 */}
          <div className={s.note} role="note">
            <Icon name="info" />
            <span>
              계정 별칭(label)과 분류만 다룹니다. 비밀번호 · 토큰 · 세션 값은 읽지도 보이지도 않고, 등록 · 표시에도
              보내지 않습니다(절대규칙 2 · S09).
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

          {selected ? (
            <section className={s.isec} aria-label="계정 분류">
              {accountsFailed ? <p className={s.empty}>계정 분류를 불러오지 못했습니다.</p> : null}
              {!accounts && !accountsFailed ? <p className={s.empty}>불러오는 중…</p> : null}
              {accounts && sum ? (
                <>
                  <div className={s["crwl-top"]}>
                    <Stat label="전체" value={sum.total} />
                    <Stat label="시험 계정(승인)" value={sum.usable} />
                    <Stat label="실계정 취급" value={sum.unmarked} />
                  </div>
                  <p className={s.help}>
                    ‘시험 계정’은 분류 판정입니다 — 지금 탐색 · 모델 입력 권한을 뜻하지 않습니다. 허용은 승인된 일회용
                    시험 계정에 대상이 발급한 세션만이고 비밀번호는 제외됩니다. 미표시 · 판단 불가 · 승인 만료는
                    실계정으로 봅니다.
                  </p>

                  {/* 계정 등록(분류 real) */}
                  <div className={s.now}>
                    <input
                      className={s.inp}
                      aria-label="새 계정 라벨"
                      value={newLabel}
                      onChange={(e) => setNewLabel(e.target.value)}
                      placeholder="계정 라벨 (소문자 · 숫자 · _ · -)"
                    />
                    <button
                      className={s.btn}
                      type="button"
                      disabled={busy || !isValidAccountLabel(newLabel)}
                      onClick={() => doWrite(registerAccount, newLabel)}
                    >
                      계정 등록
                    </button>
                  </div>
                  {writeError ? (
                    <p className={s.empty} role="alert">
                      {writeError}
                    </p>
                  ) : null}

                  {accounts.length === 0 ? (
                    <p className={s.empty}>이 대상에 등록된 계정이 없습니다.</p>
                  ) : (
                    <ul className={s.policy} aria-label="계정 목록">
                      {accounts.map((a) => {
                        const usable = usableAsTestAccount(a.cls);
                        return (
                          <li key={a.ref} data-cls={a.cls}>
                            <span className={cx(s.tag, s[TONE_TAG[ACCOUNT_CLASS_TONE[a.cls]]])}>
                              {classLabel(a.cls)}
                            </span>
                            <span className={s.mono}>{a.ref}</span>
                            <small>
                              {a.origin}
                              {usable ? " · 승인된 시험 표시(권한은 별도)" : " · 실계정 취급(모델 입력 불가)"}
                              {a.approvedBy ? ` · 표시 ${a.approvedBy}` : ""}
                              {a.note ? ` · ${a.note}` : ""}
                            </small>
                            <span>
                              {a.cls === "unmarked" ? (
                                <button
                                  className={cx(s.btn, s.sm)}
                                  type="button"
                                  disabled={busy}
                                  onClick={() => setConfirming(a.ref)}
                                >
                                  시험 표시
                                </button>
                              ) : null}
                              {a.cls === "test_approved" ? (
                                <button
                                  className={cx(s.btn, s.sm)}
                                  type="button"
                                  disabled={busy}
                                  onClick={() => doWrite(unmarkAccount, a.ref)}
                                >
                                  표시 해제
                                </button>
                              ) : null}
                            </span>
                            {confirming === a.ref ? (
                              <div className={cx(s.note, s.warn)}>
                                <Icon name="alert" />
                                <span>
                                  {/* 동의는 서버 진술 문장으로만 받는다(서버가 그 문장의 해시를 동의 기록으로 남김). 없으면 막는다 */}
                                  {statement ||
                                    "진술을 받지 못해 시험 표시할 수 없습니다 · 서버 진술 문장이 있어야 동의를 기록합니다"}
                                  <br />
                                  <button
                                    className={cx(s.btn, s.sm)}
                                    type="button"
                                    disabled={busy || !statement}
                                    onClick={() => {
                                      if (statement) doWrite(markTestAccount, a.ref);
                                    }}
                                  >
                                    동의하고 표시
                                  </button>
                                  <button
                                    className={cx(s.btn, s.sm)}
                                    type="button"
                                    disabled={busy}
                                    onClick={() => setConfirming(null)}
                                  >
                                    취소
                                  </button>
                                </span>
                              </div>
                            ) : null}
                          </li>
                        );
                      })}
                    </ul>
                  )}
                </>
              ) : null}
            </section>
          ) : null}
        </div>
      </div>
    </>
  );
}
