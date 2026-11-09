# 작업대 (`/workbench`)

재상 전용 작업대 화면이다. 팀 화면(사이트 루트)과 따로 둔다.

- **시안 데이터로 재생만 한다.** 자산 탐색과 점검 진행은 정해 둔 각본(`data/`)을 시간에 맞춰 보여 줄 뿐이다.
- **백엔드에 연결하지 않는다.** `/api/*` 를 부르지 않는다.
- 원본은 `web/mockups/abc2lab-workbench.html` (HTML·CSS·JS 한 파일)이다. 문구·숫자·순서·aria 는 이 원본과 같아야 한다.

## 폴더 지도

```
Workbench.tsx         루트: 노란 배너 · 레일 · 여섯 화면 section · 스크림 · 해시 라우터 · 재생 엔진 호출
shell.tsx             useShell(): route · goTo · openInsp/closeInsp · openRail/closeRail · dashPick · narrow/phone/reduce
store.ts              useWb (zustand): 화면을 넘나드는 상태와 액션
engines.ts            useCrawlEngine · useLiveEngine: 스토어의 예약(sched)을 보고 setTimeout
workbench.module.css  원본 <style> 을 옮긴 CSS 모듈 (클래스 이름은 원본과 같음)
lib/                  순수 함수: cx · kind/pkind/isEx · trie · hms/clock · curHost · decisions · crawlState · liveState
data/                 원본 상수: crawl(GL·LINES·EV·AT·PCT) · scope(EP·SG) · live(PLAN·LT·LCUM) · findings(F)
ui/icons.tsx          아이콘 (<Icon name="grid" />)
ui/parts.tsx          공통 부품: Ep · On · Sev · Verdict · InspHead · Isec · Kv · Phases · CopyButton · CodeCard …
ui/Rail.tsx           왼쪽 레일
views/                여섯 화면 (+ 화면끼리 나눠 쓰는 상세 패널)
__tests__/            vitest
```

## 지킬 것

1. 클래스는 CSS 모듈로만 쓴다. `className={s.btn}`, 하이픈 이름은 `s["vi-head"]`, 여러 개는 `cx(...)`. 문자열 클래스(`className="btn"`)는 쓰지 않는다.
2. `dangerouslySetInnerHTML` · `innerHTML` 은 쓰지 않는다. 원본이 HTML 문자열로 만들던 것은 모두 JSX 로 만든다.
3. 맨 위 노란 배너("작동하지 않는 UI 시안입니다 …")는 지우지 않는다.
4. 여러 화면이 쓰는 상태는 `store.ts` 에, 한 화면만 쓰는 상태는 그 컴포넌트의 `useState` 에 둔다. 여섯 화면은 항상 마운트돼 있고 `hidden` 으로만 바뀐다.
5. 타이머는 `useEffect` 안에서만 걸고 정리 함수에서 푼다. `window` · `location` · `localStorage` · `matchMedia` 는 렌더 중에 건드리지 않는다.
6. 원본에서 빌려 온 동작 메모
   - 화면 이동: `useShell().goTo("crawl")`. 보통의 `<a href="#live">` 도 그대로 된다.
   - 상세 서랍: 좁은 화면에서만 열린다(`openInsp`). 닫기 버튼은 `<CloseInspButton>` 또는 `<InspHead>` 를 쓴다(`data-insp-close` 표식으로 포커스를 옮긴다).
   - 보고서로 가는 링크는 `<ReportLink kind="blog" | "live">` 로 만든다(보고서 대상을 먼저 바꾼다).
   - 스토어 구독은 원시값이나 스토어 안 객체 그대로 고른다. 선택 함수에서 새 객체를 만들면 무한 렌더가 된다.
