# abc2lab-ui

`ABC2LAB_UI_1.html` 시안에서 출발한 React + TypeScript(TSX) UI입니다. 현재 저장소는 Next.js에서 사용하며, 실 API에 연결된 화면과 예시 데이터 화면이 함께 있습니다. 아래 Vite 절차는 컴포넌트 이식 참고이며 현재 제품 기동 절차는 [로컬 개발 안내](../../../docs/local-dev-ko.md)를 따릅니다.

## 현재 연결과 남은 범위

- 작업 목록·상태 등 기존 조회는 `lib/engine.ts`, 요청별 E② 승인·거절·중지는 `lib/request-approvals.ts`와 `RequestApprovalPanel`, 저장 AI 수정 프롬프트는 `lib/remediation.ts`와 `RemediationPanel`을 사용합니다. 구현된 연결과 실제 모델·브라우저 수용 검증은 별개입니다.
- 첫 통합 진단의 공식 시작은 [CLI `prepare-task → prepare-intents → check → run`](../../../docs/first-diagnosis-launch.md)이며 종료 결과는 `report`로 제공합니다. 주소 입력·화면 전환이나 기존 `POST /api/tasks`를 이 통제 경로의 임의 URL 시작 API로 사용하지 않습니다.
- 접근통제 지도·구조화 판정 상세·E② 흐름·세분 커버리지의 `lib/kg.ts`, `finding.ts`, `e2flow.ts`, `coverage.ts` getter는 아직 예시 데이터를 반환합니다. 서버 계약/코어의 구현 여부와 이 화면의 연결 여부를 구분합니다.
- 일반 HTTP 후보는 검토 필요 상태이며 E③ 확정과 다릅니다. API별 경계·보고서 경로는 [UI API 지도](../../../docs/contracts/ui-api-map.md)를 확인합니다.

## 적용 (Vite + React)

1. 이 폴더를 `src/abc2lab-ui/` 로 복사
2. `src/main.tsx`

```tsx
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import ABC2LabUI from './abc2lab-ui';

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ABC2LabUI />
  </StrictMode>,
);
```

3. 폰트(Noto Sans KR) — 폐쇄망에서 외부 요청 없이 쓰도록 self-host 를 권장한다.
   이 레포(Next)는 `web/src/app/layout.tsx` 에서 fontsource 를 import 해 빌드타임에 번들한다(외부 요청 0).

```bash
npm i @fontsource-variable/noto-sans-kr
```
```ts
// 진입점(layout.tsx / main.tsx)에서 — "Noto Sans KR Variable" 패밀리 등록
import "@fontsource-variable/noto-sans-kr/index.css";
```
   (외부 반입이 가능한 환경이면 대신 Google Fonts `<link>` 로 붙여도 된다.)

4. Vite 템플릿 기본 CSS(`src/index.css`, `src/App.css`)는 빼세요. `#root` 너비 · 가운데 정렬이 레이아웃을 깨요.

## Props

| prop | 기본값 | 설명 |
| --- | --- | --- |
| `initialScreen` | `'entry'` | 처음 화면. URL 해시(`#report` 등)가 있으면 해시 우선 |
| `syncHash` | `true` | 화면 전환 시 URL 해시 갱신. react-router를 쓰면 `false` 추천 |
| `theme` | OS 설정 | `'light'` / `'dark'` 고정 (`<html data-theme>` 에 걸어요) |

## 구조

```
ABC2LabUI.tsx      루트: 화면 전환(원본 go()) · 점검 대상 주소 상태
abc2lab.css        원본 <style> 두 블록 그대로
nav.ts             화면 id 목록 · useGo() (원본 data-go 역할)
types.ts           Target, 심각도 · 판정 타입
lib/               cx, 내부망 판별(isInternalUrl) · hostOf · 토큰, 타이머/스크롤잠금/ESC 훅
components/        Brand, TopBar, Stepper, Switch, Choice, Tabs, Modal, CopyButton, VerifyDomainModal …
screens/           화면 12개 (Entry, Target, Job, Scope, Live, History, Report, Finding, FindingUnd, Approval, Settings, Audit)
```

## 목업 모드

`NEXT_PUBLIC_MOCK=1`로 빌드하면 엔진에 요청하지 않고(로그인도 안 함) `lib/mock-data.ts`의 예시 데이터로 화면을 채워요. Vercel 시연용이에요.
화면 맨 위에 "목업 예시 데이터" 배너가 고정되고, 일반 빌드는 예시 데이터를 불러오지 않아요(목업일 때만 동적 import).

## 원본과 다른 점

- 발견 상세 증거 JSON의 `"address": "[REDACTED]` 에 빠진 닫는 따옴표를 추가
- 진입 화면 주소 칸에서 Enter로도 점검 시작 (한글 조합 중 Enter는 무시)
- 닫혀 있는 팝업과 "더 펼치기" 전 항목은 `hidden` 대신 아예 렌더링하지 않음 (보이는 결과는 같음)

## 참고

- 원본처럼 화면 12개를 모두 마운트해 두고 `hidden` 으로만 전환해요. 화면을 오가도 입력값 · 토글 상태가 유지돼요.
- 숫자·목록 전체가 시안 데이터인 것은 아닙니다. 실 API 조회와 예시 getter·화면 상수를 구분하고, 미연결 데이터는 예시/준비 중 표기를 유지합니다. getter를 연결할 때도 승인·판정·마스킹 계약을 서버 응답에 맞춰 검증합니다.
- 소유 확인(DNS TXT) · 허용 목록 검증은 타이머로 성공을 흉내 내는 시안 동작이에요.
- CSS는 원본처럼 전역(`:root`, `body`, `.btn` …)이에요. 다른 페이지와 같이 쓰면 클래스 이름이 겹칠 수 있어요.
