"use client";

// 한 대상의 계정 관리(로그인 방식 · 계정 등록 · 시험 표시 · 자격 입력). 계정 분류 화면과 새 점검 '계정 분류' 칸이 같이 쓴다
// (사용자 11/24: 새 점검에서 다 하게). 같은 부품이라 비밀번호 처리 · 동의 기록 규칙이 두 곳에서 같다.
// ※ 자격 칸(사용자명 · 비밀번호)은 React 상태에 두지 않는 비제어 입력이다. 보낼 때 DOM 칸에서 한 번 읽고 바로 비운다(보내지
//   않는 경우도). URL · 브라우저 저장소 · 로그에 넣지 않고, 등록 여부는 서버 목록의 credential_configured 로만 보인다.
//   비밀번호 관리자에 들어가지 않게 form 요소를 쓰지 않고 autoComplete="off" · 관리자 무시 표시를 단다(#842 경준 리뷰).
// ※ §8 fail-closed: 서버 분류 'test'(승인된 시험)만 시험 계정. real · 미표시 · 판단 불가는 실계정 취급.
// ※ 쓰기: 등록(label) · 시험 표시(mark-test, 진술 동의 필요) · 표시 해제(unmark) · 자격 등록 · 로그인 방식 저장.

import { useEffect, useId, useMemo, useRef, useState } from "react";

import { cx } from "../../lib/cx";
import {
  ACCOUNT_CLASS_TONE,
  accountWriteFailureText,
  type ClassifiedAccount,
  classLabel,
  EMPTY_TARGET_LOGIN,
  getAccounts,
  getTargetLogin,
  MIN_USABLE_PASSWORD_BYTES,
  markTestAccount,
  type ProductTargetRef,
  parseCookieNames,
  passwordTooShortForRun,
  setAccountCredential,
  setTargetLogin,
  summarizeAccounts,
  type TargetLogin,
  unmarkAccount,
  validAccountCredential,
  validTargetLogin,
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
  const uid = useId();
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
  // 대상 로그인 방식: 서버 저장값(null = 미설정)과 입력 중 값. 자격 값이 없는 설정이라 상태에 둔다.
  const [login, setLogin] = useState<TargetLogin | null>(null);
  const [loginDraft, setLoginDraft] = useState<TargetLogin>(EMPTY_TARGET_LOGIN);
  const [cookieText, setCookieText] = useState("");
  const [loginState, setLoginState] = useState<"loading" | "ready" | "failed">("loading");
  const [loginError, setLoginError] = useState("");
  const [loginSaved, setLoginSaved] = useState(false);
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

  // 고른 대상의 로그인 방식(읽기). 대상이 바뀌면 입력을 비우고 다시 읽는다. 형식 밖 응답은 '미설정'이 아니라 읽기 실패다.
  useEffect(() => {
    let live = true;
    setLogin(null);
    setLoginDraft(EMPTY_TARGET_LOGIN);
    setCookieText("");
    setLoginError("");
    setLoginSaved(false);
    setLoginState("loading");
    getTargetLogin(tref.id)
      .then((l) => {
        if (!live) return;
        setLogin(l);
        if (l) {
          setLoginDraft(l);
          setCookieText(l.cookies.join(", "));
        }
        setLoginState("ready");
      })
      .catch(() => {
        if (live) setLoginState("failed");
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

  const draftLogin: TargetLogin = { ...loginDraft, cookies: parseCookieNames(cookieText) };
  const editLogin = (patch: Partial<TargetLogin>) => {
    setLoginDraft((d) => ({ ...d, ...patch }));
    setLoginSaved(false);
  };

  // 로그인 방식 저장. 응답은 시작한 대상이 아직 화면 대상일 때만 반영한다(아래 doWrite 와 같은 귀속 규칙).
  function saveLogin() {
    if (busy) return;
    const t = tref;
    const draft = draftLogin;
    setBusy(true);
    setLoginError("");
    setLoginSaved(false);
    setTargetLogin(t.id, draft)
      .then((saved) => {
        if (current.current !== t.id) return;
        setLogin(saved);
        setLoginDraft(saved);
        setCookieText(saved.cookies.join(", "));
        setLoginSaved(true);
        onChanged?.();
      })
      .catch((e: unknown) => {
        if (current.current === t.id) setLoginError(accountWriteFailureText(e));
      })
      .finally(() => setBusy(false));
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
      <section className={s.isec} aria-label="대상 로그인 방식">
        <h2>대상 로그인 방식</h2>
        {loginState === "loading" ? <p className={s.empty}>불러오는 중…</p> : null}
        {loginState === "failed" ? <p className={s.empty}>로그인 방식을 불러오지 못했습니다.</p> : null}
        {loginState === "ready" ? (
          <>
            <p className={s.help}>
              {login
                ? "저장된 로그인 방식이 있습니다. 바꾸면 다음 실행부터 씁니다."
                : "저장된 로그인 방식이 없습니다 — 실행이 시험 계정으로 로그인하지 않습니다."}{" "}
              실행에는 로그인 방식이 있고, 시험 계정으로 표시되고, 자격이 등록된 계정만 쓰입니다. 로그인 경로는 이
              대상의 승인 경로 안이어야 합니다(서버 확인). 자격 값은 여기 넣지 않습니다.
            </p>
            <div className={s.two}>
              <div className={s.field}>
                <label className={s.lbl} htmlFor={`${uid}-loginPath`}>
                  로그인 경로 (POST)
                </label>
                <input
                  id={`${uid}-loginPath`}
                  className={s.inp}
                  value={loginDraft.path}
                  onChange={(e) => editLogin({ path: e.target.value })}
                  placeholder="/ 로 시작 · query 없이"
                  spellCheck={false}
                />
              </div>
              <div className={s.field}>
                <label className={s.lbl} htmlFor={`${uid}-loginFormat`}>
                  본문 형식
                </label>
                <select
                  id={`${uid}-loginFormat`}
                  className={s.inp}
                  value={loginDraft.format}
                  onChange={(e) => editLogin({ format: e.target.value === "form" ? "form" : "json" })}
                >
                  <option value="json">JSON</option>
                  <option value="form">form (x-www-form-urlencoded)</option>
                </select>
              </div>
              <div className={s.field}>
                <label className={s.lbl} htmlFor={`${uid}-loginUsernameField`}>
                  사용자명 칸 이름
                </label>
                <input
                  id={`${uid}-loginUsernameField`}
                  className={s.inp}
                  value={loginDraft.usernameField}
                  onChange={(e) => editLogin({ usernameField: e.target.value })}
                  spellCheck={false}
                />
              </div>
              <div className={s.field}>
                <label className={s.lbl} htmlFor={`${uid}-loginPasswordField`}>
                  비밀번호 칸 이름
                </label>
                <input
                  id={`${uid}-loginPasswordField`}
                  className={s.inp}
                  value={loginDraft.passwordField}
                  onChange={(e) => editLogin({ passwordField: e.target.value })}
                  spellCheck={false}
                />
              </div>
              <div className={s.field}>
                <label className={s.lbl} htmlFor={`${uid}-loginSessionKind`}>
                  세션 위치
                </label>
                <select
                  id={`${uid}-loginSessionKind`}
                  className={s.inp}
                  value={loginDraft.sessionKind}
                  onChange={(e) => editLogin({ sessionKind: e.target.value === "cookies" ? "cookies" : "header" })}
                >
                  <option value="header">응답 JSON 칸 → 요청 헤더</option>
                  <option value="cookies">쿠키</option>
                </select>
              </div>
              {loginDraft.sessionKind === "cookies" ? (
                <div className={s.field}>
                  <label className={s.lbl} htmlFor={`${uid}-loginCookies`}>
                    세션 쿠키 이름 (쉼표로 구분 · 최대 8개)
                  </label>
                  <input
                    id={`${uid}-loginCookies`}
                    className={s.inp}
                    value={cookieText}
                    onChange={(e) => {
                      setCookieText(e.target.value);
                      setLoginSaved(false);
                    }}
                    spellCheck={false}
                  />
                </div>
              ) : (
                <>
                  <div className={s.field}>
                    <label className={s.lbl} htmlFor={`${uid}-loginJsonField`}>
                      응답 JSON 칸 (점으로 구분)
                    </label>
                    <input
                      id={`${uid}-loginJsonField`}
                      className={s.inp}
                      value={loginDraft.jsonField}
                      onChange={(e) => editLogin({ jsonField: e.target.value })}
                      spellCheck={false}
                    />
                  </div>
                  <div className={s.field}>
                    <label className={s.lbl} htmlFor={`${uid}-loginHeader`}>
                      요청 헤더 이름
                    </label>
                    <input
                      id={`${uid}-loginHeader`}
                      className={s.inp}
                      value={loginDraft.header}
                      onChange={(e) => editLogin({ header: e.target.value })}
                      spellCheck={false}
                    />
                  </div>
                  <div className={s.field}>
                    <label className={s.lbl} htmlFor={`${uid}-loginPrefix`}>
                      헤더 값 앞붙임 (선택)
                    </label>
                    <input
                      id={`${uid}-loginPrefix`}
                      className={s.inp}
                      value={loginDraft.prefix}
                      onChange={(e) => editLogin({ prefix: e.target.value })}
                      spellCheck={false}
                    />
                  </div>
                </>
              )}
            </div>
            <div className={s.now}>
              <button
                className={s.btn}
                type="button"
                disabled={busy || !validTargetLogin(draftLogin)}
                onClick={saveLogin}
              >
                로그인 방식 저장
              </button>
              {loginSaved ? <span role="status">저장했습니다.</span> : null}
            </div>
            {loginError ? (
              <p className={s.empty} role="alert">
                {loginError}
              </p>
            ) : null}
          </>
        ) : null}
      </section>

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
