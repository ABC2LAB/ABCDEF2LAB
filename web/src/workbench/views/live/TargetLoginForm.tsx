"use client";

// 대상 로그인 방식(로그인 경로 · 본문 형식 · 칸 이름 · 세션 위치). 시험 계정 A · B 상자 맨 위에 둔다(사용자 11/24: 새 점검에서
// 시험 계정 말고 로그인 방식도) — 계정 분류 화면과 새 점검이 같은 상자를 쓴다. 자격 값(아이디 · 비밀번호)은 여기 넣지 않는다.
// 서버 저장값(null = 미설정)과 입력 중 값은 상태에 둔다(자격 값이 없는 설정). 형식 밖 응답은 '미설정'이 아니라 읽기 실패다.
// 대상이 아직 승인되지 않았으면 칸은 채울 수 있고 저장만 막는다.

import { useEffect, useId, useRef, useState } from "react";

import {
  accountWriteFailureText,
  EMPTY_TARGET_LOGIN,
  getTargetLogin,
  parseCookieNames,
  setTargetLogin,
  type TargetLogin,
  validTargetLogin,
} from "../../live/accounts";
import s from "../../workbench.module.css";

export function TargetLoginForm({ targetId, onSaved }: { targetId: string | null; onSaved?: () => void }) {
  const uid = useId();
  const [busy, setBusy] = useState(false);
  const [login, setLogin] = useState<TargetLogin | null>(null);
  const [loginDraft, setLoginDraft] = useState<TargetLogin>(EMPTY_TARGET_LOGIN);
  const [cookieText, setCookieText] = useState("");
  const [loginState, setLoginState] = useState<"loading" | "ready" | "failed">("loading");
  const [loginError, setLoginError] = useState("");
  const [loginSaved, setLoginSaved] = useState(false);
  // 지금 화면 대상. 늦게 온 이전 대상 응답은 버린다
  const current = useRef<string | null>(targetId);

  // 고른 대상의 로그인 방식(읽기). 대상이 바뀌면 입력을 비우고 다시 읽는다. 형식 밖 응답은 '미설정'이 아니라 읽기 실패다.
  useEffect(() => {
    let live = true;
    current.current = targetId;
    setLogin(null);
    setLoginDraft(EMPTY_TARGET_LOGIN);
    setCookieText("");
    setLoginError("");
    setLoginSaved(false);
    setLoginState("loading");
    if (!targetId) return;
    getTargetLogin(targetId)
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
  }, [targetId]);

  const draftLogin: TargetLogin = { ...loginDraft, cookies: parseCookieNames(cookieText) };
  const editLogin = (patch: Partial<TargetLogin>) => {
    setLoginDraft((d) => ({ ...d, ...patch }));
    setLoginSaved(false);
  };

  // 로그인 방식 저장. 응답은 시작한 대상이 아직 화면 대상일 때만 반영한다(대상을 바꾼 뒤 늦게 온 응답은 버림).
  function saveLogin() {
    if (busy || !targetId) return;
    const id = targetId;
    const draft = draftLogin;
    setBusy(true);
    setLoginError("");
    setLoginSaved(false);
    setTargetLogin(id, draft)
      .then((saved) => {
        if (current.current !== id) return;
        setLogin(saved);
        setLoginDraft(saved);
        setCookieText(saved.cookies.join(", "));
        setLoginSaved(true);
        onSaved?.();
      })
      .catch((e: unknown) => {
        if (current.current === id) setLoginError(accountWriteFailureText(e));
      })
      .finally(() => setBusy(false));
  }

  return (
    <section className={s["acct-login"]} aria-label="대상 로그인 방식">
      <h3>로그인 방식</h3>
      {targetId && loginState === "loading" ? <p className={s.empty}>불러오는 중…</p> : null}
      {loginState === "failed" ? <p className={s.empty}>로그인 방식을 불러오지 못했습니다.</p> : null}
      {loginState === "ready" || !targetId ? (
        <>
          {/* 긴 설명은 줄였다(사용자 11/24 '로그인 창처럼'). 로그인 경로가 승인 경로 안인지는 서버가 저장 때 확인한다 */}
          <p className={s.help}>
            {login
              ? "저장됨 · 바꾸면 다음 점검부터"
              : "저장된 로그인 방식이 없습니다 — 점검이 시험 계정으로 로그인하지 않습니다"}
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
              disabled={busy || !targetId || !validTargetLogin(draftLogin)}
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
  );
}
