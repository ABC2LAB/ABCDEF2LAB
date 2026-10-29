# abc2lab-ui

`ABC2LAB_UI_1.html` 시안을 React + TypeScript(TSX)로 옮긴 컴포넌트예요. 화면 12개의 마크업 · 스타일 · 동작을 원본 그대로 유지했어요.

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

3. `index.html` 의 `<head>` 에 폰트 추가 (원본과 같은 Noto Sans KR)

```html
<title>ABC2LAB 점검 흐름</title>
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Noto+Sans+KR:wght@400;500;700;900&display=swap">
```

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

## 원본과 다른 점

- 발견 상세 증거 JSON의 `"address": "[REDACTED]` 에 빠진 닫는 따옴표를 추가
- 진입 화면 주소 칸에서 Enter로도 점검 시작 (한글 조합 중 Enter는 무시)
- 닫혀 있는 팝업과 "더 펼치기" 전 항목은 `hidden` 대신 아예 렌더링하지 않음 (보이는 결과는 같음)

## 참고

- 원본처럼 화면 12개를 모두 마운트해 두고 `hidden` 으로만 전환해요. 화면을 오가도 입력값 · 토글 상태가 유지돼요.
- 숫자 · 목록은 시안 데이터예요. 각 screens 파일 위쪽 상수(`AREAS`, `ACTIVITY`, `PLAN` …)를 API 응답으로 바꾸면 돼요.
- 소유 확인(DNS TXT) · 허용 목록 검증은 타이머로 성공을 흉내 내는 시안 동작이에요.
- CSS는 원본처럼 전역(`:root`, `body`, `.btn` …)이에요. 다른 페이지와 같이 쓰면 클래스 이름이 겹칠 수 있어요.
