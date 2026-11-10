/**
 * 클래스 이름 결합. 거짓 값(false·null·undefined·"")은 건너뛴다.
 * CSS 모듈에 없는 이름(`s.GET` 처럼 규칙이 없는 클래스)은 undefined 라서 함께 걸러진다.
 */
export function cx(...xs: Array<string | false | null | undefined | 0>): string {
  return xs.filter(Boolean).join(" ");
}
