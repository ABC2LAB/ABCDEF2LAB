/** 발견의 자유서술 vulnclass를 보고서 그래프의 표시용 계열로 묶는다.
 *
 *  왜: IDOR / BOLA/IDOR / "Insecure Direct Object Reference"처럼 같은
 *  계열의 표기가 달라도 그래프에서 함께 볼 수 있게 한다.
 *
 *  경계: **표시 전용**이다. 저장 데이터·판정(verdict)은 바꾸지 않고, 원문
 *  vulnclass는 발견 상세에 그대로 남는다. 엔진의 정본 분류 계약이 아니며,
 *  정본 분류가 도입되면 그 계약에 맞춰 표시 계층을 정렬한다. */
export type VulnFamily = { key: string; label: string };

const OTHER: VulnFamily = { key: "other", label: "기타" };

/** 첫 매치가 계열을 정한다 — 순서가 분류다.
 *  예) "JWT sensitive data exposure" 는 infodisc 보다 auth 가 먼저라 인증으로 묶인다. */
const RULES: { key: string; label: string; re: RegExp }[] = [
  {
    key: "access",
    label: "접근통제 (IDOR 포함)",
    re: /\b(?:idor|bola|object reference|broken access|access control|authori[sz]ation|cwe-639|a01)\b/i,
  },
  {
    key: "injection",
    label: "인젝션(SQL·명령)",
    re: /\b(?:sql[\s_-]*injection|sqli|cwe-89|command[\s_-]+injection|os command|cwe-78)\b/i,
  },
  { key: "xss", label: "XSS", re: /\b(?:xss|cross[\s_-]*site[\s_-]+script(?:ing)?)\b/i },
  {
    key: "auth",
    label: "인증 · 세션",
    re: /\b(?:auth(?:entication)?|jwt|session|2fa|brute ?force|weak password|credentials?|a07)\b/i,
  },
  {
    key: "bizlogic",
    label: "비즈니스 로직",
    re: /\b(?:business[\s_-]*logic|price manipulation|payment|negative quantity)\b/i,
  },
  {
    key: "infodisc",
    label: "정보 노출",
    re: /information disclosure|sensitive data|information exposure|\bcwe-200\b|data exposure/i,
  },
];

/** raw vulnclass → 표시용 정규 계열. 빈 값·미매치는 "기타". */
export function canonicalVulnClass(raw: string | null | undefined): VulnFamily {
  const s = (raw ?? "").trim();
  if (!s) return OTHER;
  for (const r of RULES) {
    if (r.re.test(s)) return { key: r.key, label: r.label };
  }
  return OTHER;
}
