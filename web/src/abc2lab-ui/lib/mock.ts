/**
 * 목업 모드 플래그(NEXT_PUBLIC_MOCK=1, Vercel 시연 빌드와 같은 플래그). 켜지면 엔진에 요청하지 않고
 * mock-data.ts 의 예시 응답을 쓴다. 데이터는 여기 두지 않는다 — 일반 빌드가 이 파일만 끌어와도 가볍게.
 */

export const MOCK = process.env.NEXT_PUBLIC_MOCK === "1";
/** 목업에서 처음부터 선택돼 있는 점검(진행 중) — 대시보드·보고서가 "먼저 선택" 안내로 비지 않게 */
export const MOCK_DEFAULT_TASK = "1005";
