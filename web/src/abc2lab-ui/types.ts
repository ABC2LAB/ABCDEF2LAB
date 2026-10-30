/** 점검 대상 (진입 화면에서 입력한 주소) */
export type Target = {
  /** 입력한 주소 그대로 (예: https://shop.example.com) */
  url: string;
  /** 호스트만 (예: shop.example.com) */
  host: string;
};

export const DEFAULT_TARGET_URL = 'https://shop.example.com';

// ARTEX 엔진과 동일한 4단(critical|high|medium|low) — 배선 시 심각도 변환이 없게 문자열을 그대로 쓴다.
export type Severity = 'critical' | 'high' | 'medium' | 'low';
export const SEVERITY_LABEL: Record<Severity, string> = {
  critical: '심각',
  high: '높음',
  medium: '중간',
  low: '낮음',
};

/** 판정: 확정 / 의심 / 판단 불가 */
export type Verdict = 'ok' | 'sus' | 'und';
export const VERDICT_LABEL: Record<Verdict, string> = { ok: '확정', sus: '의심', und: '판단 불가' };
