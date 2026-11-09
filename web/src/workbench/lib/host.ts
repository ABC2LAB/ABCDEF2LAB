/** 점검 대상 주소의 host. 주소가 이상하면 기본 예시 host. 원본 `curHost()` (전역 target 대신 인자로 받음). */
export function curHost(target: string): string {
  try {
    return new URL(target).host;
  } catch {
    return "shop.example.com";
  }
}
