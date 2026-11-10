import { type FormEvent, useEffect, useRef, useState } from "react";

import { BrandMark } from "../components/Brand";
import { ApiError, readAuthStatus, signIn } from "../lib/api";

const labelStyle = { display: "block", fontSize: 13, fontWeight: 600, margin: "12px 0 6px" };

function failureMessage(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 401) return "아이디 또는 비밀번호가 맞지 않아요.";
    if (error.status === 429) return "시도가 너무 많아요. 잠시 뒤 다시 시도해 주세요.";
    if (error.status === 403) return error.message;
  }
  return "엔진에 연결하지 못했어요. 서버가 켜져 있는지 확인해 주세요.";
}

export default function LoginScreen({ onSignedIn }: { onSignedIn: () => void }) {
  // 엔진의 관리자 계정 이름은 하나다. 대상 앱의 계정과 무관하다.
  const [username, setUsername] = useState("ARTEX");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notInitialized, setNotInitialized] = useState(false);
  const lock = useRef(false);

  useEffect(() => {
    let live = true;
    readAuthStatus()
      .then((status) => {
        if (live) setNotInitialized(status.initialized === false);
      })
      .catch(() => {
        /* 상태 조회 실패는 로그인 시도 때 오류로 드러난다. */
      });
    return () => {
      live = false;
    };
  }, []);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (lock.current || !username || !password) return;
    lock.current = true;
    setBusy(true);
    setError(null);
    try {
      await signIn(username, password);
      onSignedIn();
    } catch (failure) {
      setError(failureMessage(failure));
    } finally {
      lock.current = false;
      setBusy(false);
    }
  }

  return (
    <div className="stage">
      <div className="frame">
        <div className="top">
          <span className="brand">
            <BrandMark />
            ABC2LAB
          </span>
        </div>
        <div style={{ maxWidth: 400, margin: "56px auto", padding: "0 24px" }}>
          <form className="card" onSubmit={(event) => void submit(event)}>
            <h3>로그인</h3>
            {notInitialized && (
              <p role="status" className="hint">
                관리자 비밀번호가 아직 설정되지 않았어요. 서버 설치 절차에서 먼저 설정해 주세요.
              </p>
            )}
            <label htmlFor="login-user" style={labelStyle}>
              아이디
            </label>
            <input
              id="login-user"
              className="inp"
              autoComplete="username"
              value={username}
              onChange={(event) => setUsername(event.target.value)}
            />
            <label htmlFor="login-pass" style={labelStyle}>
              비밀번호
            </label>
            <input
              id="login-pass"
              className="inp"
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(event) => setPassword(event.target.value)}
            />
            {error && (
              <p role="alert" style={{ color: "var(--danger)", fontSize: 14, marginTop: 12 }}>
                {error}
              </p>
            )}
            <button className="btn primary" type="submit" disabled={busy} style={{ width: "100%", marginTop: 16 }}>
              {busy ? "확인 중…" : "로그인"}
            </button>
          </form>
        </div>
      </div>
    </div>
  );
}
