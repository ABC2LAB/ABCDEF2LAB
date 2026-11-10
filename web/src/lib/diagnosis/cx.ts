/** className 조합 도우미: cx('btn', on && 'on') → "btn on" */
export function cx(...parts: Array<string | false | null | undefined>): string {
  return parts.filter(Boolean).join(" ");
}
