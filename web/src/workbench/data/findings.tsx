// 서버 판정에 사용하는 표시 어휘이며 예시 발견 데이터는 포함하지 않는다.
export type SevKey = "crit" | "hi" | "mid" | "lo";
export const SEVL: Readonly<Record<SevKey, string>> = { crit: "심각", hi: "높음", mid: "중간", lo: "낮음" };
export type Verdict = "확정" | "의심" | "판단 불가";
export type LiveVerdict = Verdict | "판정 중";
