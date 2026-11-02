/** 발견의 vulnclass(LLM 자유서술이라 비정규)를 표시용 정규 계열로 묶는다.
 *
 *  왜: 실 35B 런에서 45건에 vulnclass 가 35종이었다(예: IDOR / BOLA/IDOR /
 *  "Insecure Direct Object Reference" / CWE-639 가 전부 따로). 그대로 두면
 *  유형별 그래프가 거의 1건=1막대로 쪼개져 의미가 없다.
 *
 *  경계: **표시 전용**이다. 저장 데이터·판정(verdict)은 바꾸지 않고, 원문
 *  vulnclass 는 발견 상세에 그대로 남는다. 정규 취약점 분류 자체는 E④(중복·
 *  재사용, 이동찬) 소유라, 그쪽 정규 taxonomy 가 나오면 이 함수를 교체한다. */
export type VulnFamily = { key: string; label: string };

const OTHER: VulnFamily = { key: "other", label: "기타" };

/** 첫 매치가 계열을 정한다 — 순서가 분류다.
 *  예) "JWT sensitive data exposure" 는 infodisc 보다 auth 가 먼저라 인증으로 묶인다. */
const RULES: { key: string; label: string; re: RegExp }[] = [
  {
    key: "access",
    label: "접근통제 · IDOR",
    re: /idor|bola|object reference|broken access|access control|cwe-639|\ba01\b/i,
  },
  {
    key: "injection",
    label: "인젝션(SQL·명령)",
    re: /sql ?injection|sqli|cwe-89|command injection|os command|cwe-78/i,
  },
  { key: "xss", label: "XSS", re: /xss|cross.?site script/i },
  { key: "auth", label: "인증 · 세션", re: /auth|jwt|session|2fa|brute ?force|weak password|credential|\ba07\b/i },
  { key: "bizlogic", label: "비즈니스 로직", re: /business.?logic|price manipulation|payment|negative quantity/i },
  {
    key: "infodisc",
    label: "정보 노출",
    re: /information disclosure|sensitive data|information exposure|cwe-200|data exposure/i,
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
