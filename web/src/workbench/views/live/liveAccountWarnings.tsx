"use client";

// 진행 화면 계정 경고 두 가지(최민준 요청 11/25, #936 · #937 뒤 화면 몫). 회차를 멈추는 장치가 아니라 알림만이다.
// ① 시험 계정 인증 안 됨: 점검 상태의 account_auth(계정 라벨 → 결과) · account_auth_updated_at(→ 바뀐 시각). 인증 준비 직후부터
//    실행 중에도 오고, 다시 로그인으로 값이 바뀌면 시각도 바뀐다 — 점검 상태 2초 조회를 그대로 따라 갱신된다. 칸이 없으면
//    (계정을 안 쓰는 점검 · 인증 준비 전) 아무것도 띄우지 않는다. 라벨과 고정 결과만 보이고 사용자명 · 비밀번호 같은 값은 없다.
// ② 로그인 방식 없음: 대상 계정 목록의 login_method_missing(#914). 자격은 저장됐는데 로그인 방식이 없으면 시험 계정으로 로그인하지
//    않고 익명으로 점검한다 — 계정 분류 화면(로그인 방식 등록)으로 가는 버튼을 둔다. 목록을 못 읽으면 띄우지 않는다(단정하지 않음).

import type { URLAccountAuth, URLAccountAuthResult } from "@/lib/diagnosis/diagnosis-url";
import { readExternalTestAccountPresence } from "@/lib/diagnosis/external-account-consent";

import { cx } from "../../lib/cx";
import { shortTimeOf } from "../../live/map";
import { useNow } from "../../live/parts";
import { usePoll } from "../../live/usePoll";
import { useShell } from "../../shell";
import { useWb } from "../../store";
import { Icon } from "../../ui/icons";
import s from "../../workbench.module.css";

/** 인증 안 된 결과의 한국어 문구(최민준 제안). */
export const ACCOUNT_AUTH_FAIL_TEXT: Record<Exclude<URLAccountAuthResult, "authenticated">, string> = {
  login_failed: "로그인 거절",
  session_not_found: "세션을 받지 못함",
  result_unknown: "결과 모름",
  not_sent: "로그인 요청을 보내지 않음",
  // 모델 직접 로그인 회차(계약 model-login-attempts §4). 실행 중 not_attempted는 아직 시도 전이라 줄을 만들지 않는다.
  not_attempted: "로그인하지 않음",
  unconfirmed: "로그인 성공 확인 안 됨",
};

/** 인증이 안 돼 그 계정으로 요청하지 못한 결과다. 끝난 점검의 not_attempted도 여기 든다(그 계정은 쓰이지 않았다). */
const NOT_AUTHENTICATED: ReadonlyArray<URLAccountAuthResult> = ["login_failed", "session_not_found", "result_unknown", "not_sent"];

/** 계정 표시 이름: 라벨 순서로 A · B · …(점검 정보의 '시험 계정 인증' 칸과 같은 규칙). */
function accountName(i: number): string {
  return i < 26 ? String.fromCharCode(65 + i) : String(i + 1);
}

/**
 * 인증 안 된 계정 줄: "B(rh_tester_b) — 세션을 받지 못함 (14:46)". 모두 인증됐거나 칸이 없으면 빈 배열.
 * 시각은 그 계정 값이 마지막으로 바뀐 때(없으면 생략 — 칸이 생기기 전 끝난 점검).
 */
export function accountAuthWarningRows(
  auth: URLAccountAuth | undefined,
  at: Record<string, string> | undefined,
  nowMs: number,
  ended = false,
): string[] {
  if (!auth) return [];
  const labels = Object.keys(auth).sort();
  const rows: string[] = [];
  labels.forEach((label, i) => {
    const result = auth[label];
    if (result === "authenticated") return;
    // 실행 중 not_attempted는 아직 로그인 전인 정상 상태다(실패처럼 보이지 않게, #969 이경준 P2-1, 규칙 5).
    if (result === "not_attempted" && !ended) return;
    const when = at?.[label] ? shortTimeOf(at[label], nowMs) : "";
    // 결과만 둔다 — '결과 모름' · '보내지 않음'을 '로그인 실패'로 적지 않는다(#941 윤재상 P3, 규칙 5). 제목이 '인증 안 됨'이다
    rows.push(`${accountName(i)}(${label}) — ${ACCOUNT_AUTH_FAIL_TEXT[result]}${when ? ` (${when})` : ""}`);
  });
  return rows;
}

/**
 * 경고 아래 결과 한 줄. 모델은 인증된 계정의 세션 줄만 받으므로(`agent/e2_http.go` e2HTTPSessionLines) 인증 안 된 계정으로는
 * 요청하지 못한다 — '익명으로 나감'이 아니다(#941 최민준 P2). 확정 단계는 시작할 때의 지금 세션 기준(#906)이라 '빠질 수 있습니다'.
 * 한 문장으로 줄이고 내부 사유 코드(test_accounts_unavailable)는 화면에 두지 않는다(#941 윤재상 P3-2).
 */
export const AUTH_CONSEQUENCE =
  "인증이 안 되어 해당 계정으로는 요청하지 못하고, 계정 간 비교와 확정 단계가 빠질 수 있습니다. 점검은 멈추지 않습니다.";

/** unconfirmed 결과 한 줄(이경준 제안). 세션을 받았을 수 있어 '요청하지 못함'이라고 하지 않는다. */
export const AUTH_UNCONFIRMED_CONSEQUENCE =
  "로그인 성공을 확인하지 못한 계정은 확정 단계에서 쓰지 않을 수 있습니다. 점검은 멈추지 않습니다.";

/** 경고 아래 결과 줄: 인증 안 된 계정이 있으면 AUTH_CONSEQUENCE, 확인 안 된 계정이 있으면 그 줄. 둘 다면 두 줄(#969 이경준 P2-3). */
export function accountAuthConsequences(auth: URLAccountAuth | undefined, ended = false): string[] {
  if (!auth) return [];
  const results = Object.values(auth);
  const out: string[] = [];
  if (results.some((r) => NOT_AUTHENTICATED.includes(r) || (ended && r === "not_attempted"))) out.push(AUTH_CONSEQUENCE);
  if (results.includes("unconfirmed")) out.push(AUTH_UNCONFIRMED_CONSEQUENCE);
  return out;
}

/** 분 단위 시각이라 이 간격으로만 다시 센다(같은 날 · 다른 날 표기). */
const AUTH_CLOCK_MS = 60_000;

/** ① 시험 계정 인증 경고. 인증 안 된 계정이 하나라도 있을 때만. */
export function AccountAuthWarning({
  auth,
  at,
  ended = false,
}: {
  auth: URLAccountAuth | undefined;
  at: Record<string, string> | undefined;
  /** 점검이 끝났는가 — 끝난 점검의 not_attempted만 줄이 된다 */
  ended?: boolean;
}) {
  const now = useNow(AUTH_CLOCK_MS);
  const rows = accountAuthWarningRows(auth, at, now, ended);
  if (!rows.length) return null;
  return (
    <div
      className={cx(s.note, s.warn)}
      role="alert"
      aria-label="시험 계정 인증 안 됨"
      data-testid="account-auth-warning"
    >
      <Icon name="alert" />
      <div className={s["lv-grow"]}>
        <p>
          <b>시험 계정 인증 안 됨</b>
        </p>
        <ul>
          {rows.map((r) => (
            <li key={r}>{r}</li>
          ))}
        </ul>
        {accountAuthConsequences(auth, ended).map((line) => (
          <p key={line}>{line}</p>
        ))}
      </div>
    </div>
  );
}

/** ② 로그인 방식 없음 경고. 대상 계정 목록을 한 번 읽는다(점검이 바뀌거나 화면에 다시 들어오면 다시). */
export function LoginMethodMissingWarning({
  targetUrl,
  active,
  authTried,
}: {
  targetUrl: string;
  active: boolean;
  /** 이 점검이 이미 로그인을 시도했는가(account_auth 있음) — 그러면 지금 대상 설정 대신 위 인증 경고가 말한다(#941 최민준 P3) */
  authTried: boolean;
}) {
  const { goTo } = useShell();
  const focusTargetOn = useWb((st) => st.focusTargetOn);
  const show = active && !authTried;
  const poll = usePoll((signal) => readExternalTestAccountPresence(targetUrl, signal), [targetUrl], {
    enabled: show,
  });
  const v = poll.data;
  if (!show || !v?.loginMethodMissing) return null;
  const targetId = v.targetId;
  return (
    <div className={cx(s.note, s.warn)} role="note" aria-label="로그인 방식 없음" data-testid="login-method-warning">
      <Icon name="alert" />
      <span className={s["lv-grow"]}>로그인 방식이 없어 시험 계정으로 로그인하지 않고 점검합니다.</span>
      {targetId ? (
        <button
          className={cx(s.btn, s.sm)}
          type="button"
          onClick={() => {
            focusTargetOn(targetId);
            goTo("classify");
          }}
        >
          로그인 방식 등록
        </button>
      ) : null}
    </div>
  );
}
