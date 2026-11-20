// 새 점검 시작 화면의 확정 단계 안내(E③). 이 대상의 현재 정책 선언이 확정 단계를 켤 모양인지만 본다.
// 서버가 실행 시작 때 최종으로 정한다(cmd/first-controlled-diagnosis productE3PolicyRules: 쓰기 동의 · 되돌릴 수 있는 쓰기 ·
// 시험 계정 인증 · Worker 승인 범위). 화면은 그 결과를 미리 알 수 없으므로 "돌 수 있다"까지만 말하고 조건을 함께 보인다.

import { confirmable, type DeclarationLoad, roleReadiness } from "./declaration";

export type ConfirmationPlan = {
  /** 시험 객체를 만들 자원 종류 수(소유자 전용 · 표식 칸이 소유자 전용 필드). */
  types: number;
  ownerAccount: string;
  crossAccount: string;
};

/** 현재 선언이 있고, 두 계정 역할과 같은 권한 등급 확인이 있고, 시험할 종류가 하나 이상일 때만 안내한다. */
export function confirmationPlanOf(load: DeclarationLoad): ConfirmationPlan | null {
  if (load.state !== "current") return null;
  const d = load.declaration;
  if (roleReadiness(d.roles) !== "declared" || !d.roles) return null;
  const types = d.types.filter(confirmable).length;
  if (types === 0) return null;
  return { types, ownerAccount: d.roles.ownerAccountLabel, crossAccount: d.roles.crossAccountLabel };
}
