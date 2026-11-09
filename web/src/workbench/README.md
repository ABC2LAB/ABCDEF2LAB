# 작업대 (`/workbench`)

재상 전용 작업대 화면이다. 팀 화면(사이트 루트)과 따로 둔다.

- **기본은 예시 재생(demo).** 자산 탐색과 점검 진행은 정해 둔 각본(`data/`)을 시간에 맞춰 보여 줄 뿐이고 `/api/*` 를 부르지 않는다.
- **실제 엔진 모드(live)** 를 고르면 백엔드 API 가 있는 부분만 엔진 데이터를 보인다 → 아래 '실제 엔진 모드'.
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
ui/Rail.tsx           왼쪽 레일 (live 일 때는 live/rail.tsx 조각을 끼운다)
views/                여섯 화면 (+ 화면끼리 나눠 쓰는 상세 패널)
views/live/           여섯 화면의 live 판 (DashboardLive … ReportLive)
live/                 live 기반: source · auth · api · map · mask · usePoll · useActivityTail · feeds · parts · rail
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

## 실제 엔진 모드

**바꾸는 법**: 맨 위 배너의 "실제 엔진으로 보기" / "예시 재생으로 보기", 또는 레일 아래 "데이터" 스위치. 고른 값은 이 브라우저 `localStorage["abc2lab-wb-source"]` 에 남는다. `NEXT_PUBLIC_MOCK=1` 빌드(Vercel)는 항상 예시 재생이고 버튼이 없다. 개발 서버(`next dev`)가 `/api/*` 를 `:8787` 엔진으로 넘긴다. 팀 화면(`/`)에서 로그인했으면 토큰(`abc2lab.devToken`)이 그대로 이어지고, 아니면 작업대 안 로그인 패널이 뜬다.

**무엇이 실제인가**: 레일(점검 목록 · 승인 대기 배지 · 분석 모델), 그리고 각 화면에서 엔진 API 가 있는 칸. API 가 없는 기능(소유 확인, 탐색 진행률, 범위 포함/제외 저장, 판정 집계, 종료 버튼 …)은 가짜 값 대신 `<Unsupported />`("엔진 미지원")로 둔다. 엔진에 없는 값을 화면이 짐작한 것(대상 호스트, 경로 종류, 로컬 모델)은 `<EstimateTag />`("추정")를 붙인다. 엔진은 서버 모드가 둘이라(일반 / 첫 진단) 섹션마다 따로 불러 403·404·409·501·503 이면 그 섹션만 `<ModeNote />` 로 접는다.

**화면 쓰는 법** (`views/live/*Live.tsx`, `<LiveGate>` 안이라 로그인이 끝난 뒤에만 그려진다):

```tsx
export function LiveLive() {
  const { route } = useShell();
  const visible = route === "live"; // 숨은 화면은 부르지 않는다
  const { taskId, task } = useSelectedTask(); // 숫자 칸(발견·토큰·목표)은 목록 줄(task)이 정본
  const findings = usePoll(() => fetchTaskFindings(taskId as string), [taskId], {
    intervalMs: 4000,
    enabled: visible && !!taskId,
  });
  let body: ReactNode;
  if (!taskId) body = <TaskPicker which="active" />; // 누르면 selectTask 만, 화면은 그대로
  else if (findings.unsupported) body = <ModeNote what="발견" />;
  else if (!findings.data && findings.error) body = <ErrorNote error={findings.error} onRetry={findings.reload} />;
  else if (!findings.data) body = <LoadingRow />;
  else body = findings.data.map((f) => <p key={f.id}>{maskSecrets(f.summary)}</p>);
  return (
    <>
      <header className={s.vh}>{/* MenuButton · <h1 tabIndex={-1}>점검 진행</h1> · task && <PhasePill task={task} /> */}</header>
      <div className={s.vm}>{body}</div>
    </>
  );
}
```

- 점검 목록은 `useLiveTasks()` / `useSelectedTask()` (공용 5초 조회), 만든 뒤·일시정지 뒤에는 `reloadTasks()`. 승인 대기는 `useLivePending()` · `reloadPending()`.
- 지난 활동은 `useActivityTail(taskId)` 를 쓴다(엔진은 오래된 순 + cursor 라서 그냥 부르면 오래된 300 건만 온다).
- `GET /api/tasks/{id}` 는 발견·토큰·목표 수가 늘 0 이다 → 숫자는 목록에서 읽는다. 발견 상세는 `finding_id` 로 부른다.
- finding `status` 는 처리 상태다. 판정(확정·의심·판단 불가) 딱지로 쓰지 않는다(`findingStatusLabel`).

**보안 메모**

- 가림(`maskSecrets`)은 **화면 표시용**이다. 서버 원문(트래픽 · 도구 입력 · 증거)은 가려지지 않는다 → 가린 글 곁에 `<MaskedNote />`. `live/api` 가 트래픽 원문 · 도구 입력 · 파라미터 값 · 요청 미리보기는 미리 가려 주고, 발견 요약·증거·활동 요약은 화면에서 `maskSecrets` 를 거친다.
- 서비스 자산의 `auth[]` 는 받지 않는다(엔드포인트만 부르고, 받은 칸도 고른 것만 남긴다). LLM proxy · API 키 힌트도 버린다.
- 토큰은 URL·쿼리에 넣지 않는다(`apiFetch` 가 Authorization 헤더로만). 보고서 내려받기는 `downloadDiagnostic`(object URL + `<a download>`)만 쓰고 그 주소로 이동·열기를 하지 않는다.
- 서버 글은 텍스트로만 그린다(`dangerouslySetInnerHTML` 금지). 대상 URL 은 링크로 만들지 않는다.
- 로그인 비밀번호는 패널 상태에만 잠깐 두고 제출하자마자 비운다.
