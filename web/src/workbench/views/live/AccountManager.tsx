"use client";

// 한 대상의 계정 관리(로그인 방식 · 계정 등록 · 시험 표시 · 자격 입력). 계정 분류 화면과 새 점검 '계정 분류' 칸이 같이 쓴다
// (사용자 11/24: 새 점검에서 다 하게). 같은 부품이라 비밀번호 처리 · 동의 기록 규칙이 두 곳에서 같다.
// ※ 자격 칸(사용자명 · 비밀번호)은 React 상태에 두지 않는 비제어 입력이다. 보낼 때 DOM 칸에서 한 번 읽고 바로 비운다(보내지
//   않는 경우도). URL · 브라우저 저장소 · 로그에 넣지 않고, 등록 여부는 서버 목록의 credential_configured 로만 보인다.
//   비밀번호 관리자에 들어가지 않게 form 요소를 쓰지 않고 autoComplete="off" · 관리자 무시 표시를 단다(#842 경준 리뷰).
// ※ §8 fail-closed: 서버 분류 'test'(승인된 시험)만 시험 계정. real · 미표시 · 판단 불가는 실계정 취급.
// ※ 쓰기: 등록(label) · 시험 표시(mark-test, 진술 동의 필요) · 표시 해제(unmark) · 자격 등록 · 로그인 방식 저장.

import { useEffect, useMemo, useRef, useState } from "react";

import { cx } from "../../lib/cx";
import {
  ACCOUNT_CLASS_TONE,
  accountWriteFailureText,
  type ClassifiedAccount,
  classLabel,
  getAccounts,
  MIN_USABLE_PASSWORD_BYTES,
  markTestAccount,
  NO_SPACE_USERNAME,
  type ProductTargetRef,
  passwordTooShortForRun,
  setAccountCredential,
  summarizeAccounts,
  unmarkAccount,
  validAccountCredential,
} from "../../live/accounts";
import { confirmationPlanOf } from "../../live/confirmationPlan";
import { getDeclaration } from "../../live/declaration";
import { Icon } from "../../ui/icons";
import s from "../../workbench.module.css";
import { pairLabels, QuickTestAccount } from "./QuickTestAccount";

const TONE_TAG: Record<"ok" | "wait" | "stop", string> = { ok: "in", wait: "ap", stop: "ex" };

/**
 * 한 대상의 계정 관리. target 이 바뀌면 다시 읽고 입력 상태를 비운다. 쓰기(등록 · 표시 · 해제 · 자격 · 로그인 방식)가
 * 성공하면 onChanged 를 부른다(새 점검이 시험 계정 유무 · 로그인 결속을 다시 읽게).
 */
export function AccountManager({
  target,
  onChanged,
  pair = true,
  owner,
  cross,
}: {
  target: ProductTargetRef;
  onChanged?: () => void;
  /** 맨 위에 시험 계정 A · B 로그인 상자를 둘지(새 점검은 바깥에 이미 있어 false) */
  pair?: boolean;
  /** 정책 선언의 소유 · 교차 계정 이름. 없으면 이 부품이 선언을 읽어 채운다 */
  owner?: string;
  cross?: string;
}) {
  // 부르는 쪽이 렌더마다 새 객체를 넘겨도 같은 대상이면 다시 읽지 않는다
  const { id: targetId, origin: targetOrigin, state: targetState } = target;
  const tref = useMemo<ProductTargetRef>(
    () => ({ id: targetId, origin: targetOrigin, state: targetState }),
    [targetId, targetOrigin, targetState],
  );
  // 이 부품 안에서 계정이 바뀐 횟수 — 위 로그인 상자가 상태를 다시 읽게
  const [rev, setRev] = useState(0);
  // 정책 선언의 소유 · 교차 계정 이름(부르는 쪽이 안 주면 읽는다). 못 읽으면 비워 두고 상자가 기존 시험 계정 이름을 쓴다
  const [declared, setDeclared] = useState<{ id: string; owner?: string; cross?: string } | null>(null);
  useEffect(() => {
    if (owner || cross) return;
    let live = true;
    getDeclaration(targetId)
      .then((load) => {
        const plan = confirmationPlanOf(load);
        if (live) setDeclared({ id: targetId, owner: plan?.ownerAccount, cross: plan?.crossAccount });
      })
      .catch(() => {
        if (live) setDeclared({ id: targetId });
      });
    return () => {
      live = false;
    };
  }, [targetId, owner, cross]);
  const ownerLabel = owner ?? (declared?.id === targetId ? declared.owner : undefined);
  const crossLabel = cross ?? (declared?.id === targetId ? declared.cross : undefined);
  const [accounts, setAccounts] = useState<ClassifiedAccount[] | null>(null);
  const [statement, setStatement] = useState("");
  const [accountsFailed, setAccountsFailed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [writeError, setWriteError] = useState("");
  const [confirming, setConfirming] = useState<string | null>(null);
  // 자격 입력 칸을 연 계정 라벨. 칸 값은 상태가 아니라 DOM(비제어 입력)에만 있다.
  const [credentialFor, setCredentialFor] = useState<string | null>(null);
  // 자격 등록 뒤 안내(계정 라벨과 바이트 하한만 담는다, 값 없음). 서버는 짧은 비밀번호도 저장해 목록은 '자격 등록됨'이 되므로
  // 실행 때 쓰이지 않는다는 것을 따로 알린다(#842 경준 리뷰).
  const [credentialNotice, setCredentialNotice] = useState("");
  const usernameInput = useRef<HTMLInputElement>(null);
  const passwordInput = useRef<HTMLInputElement>(null);
  // 지금 화면에 결속된 대상 id. 쓰기·재조회 응답은 이 값과 같을 때만 반영한다(대상을 바꾼 뒤 늦게 온 이전 대상 응답은 버림).
  const current = useRef<string | null>(null);

  // 고른 대상의 계정 분류(읽기). 대상이 바뀌면 다시 읽고 입력 상태를 비운다.
  useEffect(() => {
    let live = true;
    setAccounts(null);
    setStatement("");
    setAccountsFailed(false);
    setConfirming(null);
    setWriteError("");
    setCredentialFor(null);
    setCredentialNotice("");
    current.current = tref.id;
    getAccounts(tref)
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
  }, [tref]);

  // 위 로그인 상자가 저장한 뒤 목록을 다시 읽는다(응답은 지금 대상일 때만)
  function reloadAccounts() {
    const t = tref;
    getAccounts(t)
      .then((r) => {
        if (current.current !== t.id) return;
        setAccounts(r.accounts);
        setStatement(r.statement);
      })
      .catch(() => {
        if (current.current === t.id) setAccountsFailed(true);
      });
  }

  // 자격 등록. 칸 값은 여기서 한 번 읽고 곧바로 비운다(보내지 않는 경우도 — 화면·DOM 에 남기지 않는다). 값은 요청 본문에만
  // 실리고, 등록 여부는 다시 읽은 목록의 credential_configured 로만 보인다.
  function submitCredential(label: string) {
    const u = usernameInput.current;
    const p = passwordInput.current;
    if (!u || !p) return;
    const username = u.value;
    const password = p.value;
    u.value = "";
    p.value = "";
    // 값이 아니라 바이트 수만 본다(실행 때 E② 보호 값 하한).
    const tooShort = passwordTooShortForRun(password);
    setCredentialNotice("");
    if (busy) return;
    if (!validAccountCredential(username, password)) {
      setWriteError(accountWriteFailureText(new Error("invalid_account_credential")));
      return;
    }
    doWrite(
      (id, l) =>
        setAccountCredential(id, l, username, password).then((configured) => {
          if (!configured) throw new Error("credential_not_confirmed");
        }),
      label,
      () => {
        setCredentialFor(null);
        if (tooShort)
          setCredentialNotice(
            `${label}: 자격은 등록했지만 비밀번호가 ${MIN_USABLE_PASSWORD_BYTES}바이트보다 짧아 실행 때 이 계정으로 로그인하지 않습니다. 더 긴 비밀번호의 시험 계정을 쓰세요.`,
          );
      },
    );
  }

  // 쓰기 공통: 작업 → 성공하면 목록 다시 읽기. 자격 값을 보내는 작업은 자격 등록(submitCredential)뿐이다.
  // 응답(성공·실패 모두)은 쓰기를 시작한 대상이 아직 화면 대상일 때만 반영한다. 그사이 대상을 바꿨으면 버린다
  // (다른 대상 목록을 보고 같은 라벨의 계정을 표시·해제하는 일을 막는다, #538 이동찬 리뷰). 실패는 서버 사유 문구로 보인다.
  function doWrite(action: (id: string, label: string) => Promise<void>, label: string, done?: () => void) {
    if (busy) return;
    const t = tref;
    const stillHere = () => current.current === t.id;
    setBusy(true);
    setWriteError("");
    action(t.id, label)
      .then(() => getAccounts(t))
      .then((r) => {
        if (!stillHere()) return;
        setAccounts(r.accounts);
        setStatement(r.statement);
        setConfirming(null);
        setRev((n) => n + 1);
        done?.();
        onChanged?.();
      })
      .catch((e: unknown) => {
        if (stillHere()) setWriteError(accountWriteFailureText(e));
      })
      .finally(() => setBusy(false));
  }

  const sum = accounts ? summarizeAccounts(accounts) : null;

  // A · B 두 계정(자격은 위 로그인 상자에서 넣는다). 목록 줄의 자격 입력은 그 밖의 계정에만 둔다
  const [labelA, labelB] = pairLabels(accounts, ownerLabel, crossLabel);
  const others = accounts ? accounts.filter((a) => a.ref !== labelA && a.ref !== labelB) : null;
  return (
    <>
      {pair ? (
        <QuickTestAccount
          target={tref}
          owner={ownerLabel}
          cross={crossLabel}
          revision={rev}
          onAdded={() => {
            reloadAccounts();
            onChanged?.();
          }}
        />
      ) : null}

      {/* 다른 계정: 시험 계정 A · B 는 위 로그인 상자에서 다 한다(자격 · 표시 해제 · 시험 표시, 사용자 11/24). 여기는 그 밖에
          등록돼 있는 계정만 — 없으면 이 칸을 두지 않는다. 숫자 칸 · 긴 설명 · 라벨 적어 등록하는 칸은 뺐다 */}
      <section className={s.isec} aria-label="다른 계정" hidden={!!others && others.length === 0 && !writeError}>
        {accountsFailed ? <p className={s.empty}>계정 분류를 불러오지 못했습니다.</p> : null}
        {!accounts && !accountsFailed ? <p className={s.empty}>불러오는 중…</p> : null}
        {accounts && sum ? (
          <>
            {credentialNotice ? (
              <div className={cx(s.note, s.warn)} role="status">
                <Icon name="alert" />
                <span>{credentialNotice}</span>
              </div>
            ) : null}
            {writeError && !credentialFor ? (
              <p className={s.empty} role="alert">
                {writeError}
              </p>
            ) : null}

            {others && others.length > 0 ? (
              <ul className={s["acct-list"]} aria-label="계정 목록">
                {others.map((a) => {
                  return (
                    <li key={a.ref} data-cls={a.cls}>
                      <span className={cx(s.tag, s[TONE_TAG[ACCOUNT_CLASS_TONE[a.cls]]])}>{classLabel(a.cls)}</span>
                      <span className={s.mono}>{a.ref}</span>
                      <span className={cx(s.tag, a.credentialConfigured ? s.in : s.ex)}>
                        {a.credentialConfigured ? "자격 등록됨" : "자격 없음"}
                      </span>
                      <span className={s["acct-acts"]}>
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
                        <button
                          className={cx(s.btn, s.sm)}
                          type="button"
                          disabled={busy}
                          onClick={() => {
                            setWriteError("");
                            setCredentialNotice("");
                            setCredentialFor(credentialFor === a.ref ? null : a.ref);
                          }}
                        >
                          {a.credentialConfigured ? "자격 다시 입력" : "자격 입력"}
                        </button>
                      </span>
                      {credentialFor === a.ref ? (
                        <div className={s.note}>
                          <span>
                            <input
                              ref={usernameInput}
                              className={s.inp}
                              type="text"
                              aria-label="사용자명"
                              {...NO_SPACE_USERNAME}
                              autoComplete="off"
                              data-1p-ignore
                              data-lpignore="true"
                              data-form-type="other"
                              spellCheck={false}
                              placeholder="사용자명"
                            />
                            <input
                              ref={passwordInput}
                              className={s.inp}
                              type="password"
                              aria-label="비밀번호"
                              autoComplete="off"
                              data-1p-ignore
                              data-lpignore="true"
                              data-form-type="other"
                              placeholder="비밀번호"
                            />
                            <br />
                            <small>
                              보내면 칸을 바로 비웁니다. 값은 서버 보호 파일에만 남고 화면 · 목록에는 '자격 등록됨'만
                              보입니다. 비밀번호가 {MIN_USABLE_PASSWORD_BYTES}바이트보다 짧으면 실행 때 보호 값으로
                              등록할 수 없어 그 계정으로 로그인하지 않습니다.
                            </small>
                            <br />
                            <button
                              className={cx(s.btn, s.sm)}
                              type="button"
                              disabled={busy}
                              onClick={() => submitCredential(a.ref)}
                            >
                              자격 등록
                            </button>
                            <button
                              className={cx(s.btn, s.sm)}
                              type="button"
                              disabled={busy}
                              onClick={() => setCredentialFor(null)}
                            >
                              취소
                            </button>
                            {writeError ? (
                              <span className={s.empty} role="alert">
                                {writeError}
                              </span>
                            ) : null}
                          </span>
                        </div>
                      ) : null}
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
            ) : null}
          </>
        ) : null}
      </section>
    </>
  );
}
