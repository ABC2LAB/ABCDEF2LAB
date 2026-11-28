"use client";

// 계정 줄의 로그인 방법 꼬리표(배움 · 검증됨, 계약 model-login-attempts §6-1-3). 글 · 설명은 seam(loginMethodBadge)이 정하고
// 여기는 색만 고른다. 자세한 내용(어느 단계에서 · 언제 · 어떤 경로)은 마우스를 올리면 보인다. 값은 어디에도 없다.

import { cx } from "../../lib/cx";
import { type LoginMethodBadge, loginMethodBadge, type TargetAccount } from "../../live/accounts";
import s from "../../workbench.module.css";

const TONE_CLASS: Record<LoginMethodBadge["tone"], string | undefined> = {
  ok: s.in,
  learned: s.lg,
  none: s.ex,
  unknown: s.out,
};

/** 자격이 등록된 계정에만 붙인다(자격 없음은 옆 꼬리표가 이미 말함). 서버가 칸을 안 실었으면 그리지 않는다 */
export function LoginMethodTag({ account }: { account: TargetAccount | undefined }) {
  if (!account?.credentialConfigured) return null;
  const badge = loginMethodBadge(account.loginMethod);
  if (!badge) return null;
  return (
    <span className={cx(s.tag, TONE_CLASS[badge.tone])} title={badge.title}>
      {badge.text}
    </span>
  );
}
