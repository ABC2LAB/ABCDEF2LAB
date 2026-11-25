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
};

/** 계정 표시 이름: 라벨 순서로 A · B · …(점검 정보의 '시험 계정 인증' 칸과 같은 규칙). */
function accountName(i: number): string {
  return i < 26 ? String.fromCharCode(65 + i) : String(i + 1);
}

/**
 * 인증 안 된 계정 줄: "B(rh_tester_b) 로그인 실패 — 세션을 받지 못함 (14:46)". 모두 인증됐거나 칸이 없으면 빈 배열.
 * 시각은 그 계정 값이 마지막으로 바뀐 때(없으면 생략 — 칸이 생기기 전 끝난 점검).
 */
export function accountAuthWarningRows(
  auth: URLAccountAuth | undefined,
  at: Record<string, string> | undefined,
  nowMs: number,
): string[] {
  if (!auth) return [];
  const labels = Object.keys(auth).sort();
  const rows: string[] = [];
  labels.forEach((label, i) => {
    const result = auth[label];
    if (result === "authenticated") return;
    const when = at?.[label] ? shortTimeOf(at[label], nowMs) : "";
    rows.push(`${accountName(i)}(${label}) 로그인 실패 — ${ACCOUNT_AUTH_FAIL_TEXT[result]}${when ? ` (${when})` : ""}`);
  });
  return rows;
}

/** 분 단위 시각이라 이 간격으로만 다시 센다(같은 날 · 다른 날 표기). */
const AUTH_CLOCK_MS = 60_000;

/** ① 시험 계정 인증 경고. 인증 안 된 계정이 하나라도 있을 때만. */
export function AccountAuthWarning({
  auth,
  at,
}: {
  auth: URLAccountAuth | undefined;
  at: Record<string, string> | undefined;
}) {
  const now = useNow(AUTH_CLOCK_MS);
  const rows = accountAuthWarningRows(auth, at, now);
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
        <p>
          인증 안 된 계정의 요청은 익명으로 나갑니다. 소유 · 교차 계정 중 하나라도 인증되지 않으면 확정 단계가 돌지
          않습니다(test_accounts_unavailable). 점검은 멈추지 않습니다.
        </p>
      </div>
    </div>
  );
}

/** ② 로그인 방식 없음 경고. 대상 계정 목록을 한 번 읽는다(점검이 바뀌거나 화면에 다시 들어오면 다시). */
export function LoginMethodMissingWarning({ targetUrl, active }: { targetUrl: string; active: boolean }) {
  const { goTo } = useShell();
  const focusTargetOn = useWb((st) => st.focusTargetOn);
  const poll = usePoll((signal) => readExternalTestAccountPresence(targetUrl, signal), [targetUrl], {
    enabled: active,
  });
  const v = poll.data;
  if (!active || !v?.loginMethodMissing) return null;
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
