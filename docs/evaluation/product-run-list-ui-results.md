# 제품 진단 목록·새 세션 재연결 UI 검증

검증일: 2026-11-17. 상태: **IMPLEMENTED / 로컬 수용 PASS / 팀 리뷰·병합 대기**.

## 코드와 범위

- 최초 기준 dev: `9b3713e9dfe3777455e4873a30ce8f35c1bdb3ee` (#619·#635 포함).
- UI·시험 커밋: `267485dfe152459ac76d19a4b96a76628e0a7af0`.
- 이후 dev `9a8e78d04dfb08da5be725a4a1dc09bd4157c2b8`를 일반 merge한 커밋: `b456790e3ceab7eb8b00b28a673844ef88266cef`. 웹 tree는 UI 커밋과 같다.
- 서버 API·설치 정책·E② 전송·평가 러너는 이번 PR에서 변경하지 않았다. #634·#636 변경은 dev의 원본 그대로다.

`diagnosis-runs.ts`가 `/api/diagnoses/v2/runs`의 owner·버전·크기·개수·식별자·순서·상태별 사실·cursor를 검증한다. `urlRunList.ts`는 한 번에 한 페이지를 읽는다. **더 불러오기**는 서버 cursor를 그대로 사용한다. `snapshot_changed`만 누적 페이지를 폐기하고 첫 페이지를 한 번 재조회한다. 같은 cursor 반복·중복 Job·snapshot 불일치는 계약 오류다.

대시보드와 진행·끝난 점검은 서버 목록이 정본이다. 활성·시작 전·결과 불명·종료 상태를 구분한다. 판정 필터는 목록에 없는 판정을 만들어야 하므로 상태 필터로 교체했다. 판정 집계는 선택한 회차의 실제 보고서를 읽은 뒤 상세에만 표시한다. 목록에 없는 URL·시각·진행률·취약점 판정은 추가하지 않는다.

선택은 `selectUrlRun` → 정확한 Job의 status GET → 기존 엄격한 Job 파서와 `matchURLRun` → `recovered` 조회 결속 순서다. 생성/시작 키를 발급하지 않는다. 복구 힌트는 `ownerContext + id`만 sessionStorage에 저장한다. 기존 생성 핸들과 외부 계정 동의 저장 계약은 유지한다. 복구한 회차의 변경 명령은 차단하며, 명시적 종료만 최신 status와 revision을 확인한 뒤 기존 stop API로 보낸다.

Job/주체 선택 세대가 바뀌면 화면의 활동·보고서·워크플로·승인 상태를 초기화한다. 늦은 목록·status·활동·보고서·로그인 응답은 폐기한다. 활동은 기존 `useActivityTail`과 `/api/exploration/activity?task=...`를, 보고서는 기존 remediation GET과 `/api/report/diagnostic?task=...` 파일 저장을 사용한다.

401은 재인증과 이전 주체 자료 폐기, 403·404·503은 조회 오류로 표시한다. 보고서 조회 실패는 보고서 부재나 안전 판정으로 바꾸지 않는다. 일반 ARTEX `/api/tasks`, mock fallback, 자동 생성·시작·승인·재개는 사용하지 않는다.

## 자동 검증

| 검증 | 결과 | 범위 |
|---|---|---|
| `tsc --noEmit` | PASS | 전체 웹 타입 검사 |
| 변경 TS/TSX 22개 `biome check` | PASS | 오류·경고 0, 포맷·import 포함 |
| `vitest run` | PASS | **966 PASS / 0 FAIL / 0 SKIP** |
| 목록 계약·실제 HTTP 가로채기 새 시험 | PASS | 51개 파서/HTTP + 28개 UI/재연결, 총 79개 |
| `npm run build` | PASS | mock 환경변수 없는 Next.js 운영 빌드 |
| 최신 dev 병합 뒤 PostgreSQL 제품 입구 시험 | PASS | `TestRunListPublicEntryReconnectStopAndRestartOwnedPostgres` 1개 |
| 독립 변이 | PASS | 별도 임시 복사본에서 차단 조건 3/3 검출 |

새 HTTP 시험은 실제 생산 파서·인증·스토어·화면을 사용한다. 보존 Job 3개, 기존 생성 핸들 동시 복구, 정확한 task 연결, 목록/Job의 owner 불일치, status/task/known fact 모순, 페이지 중복·정렬·반복 cursor, 한정된 409 재조회, 오류 상태, 보고서 GET 실패, 명시적 stop 직전 최신 revision과 이미 마감된 경우 POST 0건을 확인했다. 주체 변경 중 목록, Job 변경 중 status·활동·보고서, 로그아웃 중 로그인/스트림 JSON의 늦은 응답도 검증했다.

변이는 정상 코드를 커밋한 뒤 별도 복사본에서 시행했다. Job 결속 제거는 1 FAIL/50 PASS, 주체 세대 검사 제거는 1 FAIL/27 PASS, 복구 회차 변경 명령 차단 제거는 1 FAIL/21 PASS였다. 각각 원래 코드에서 통과한 시험이 실패했으며 원본 코드는 변경하지 않았다.

## 실제 브라우저·PostgreSQL 수용

실제 Chromium `157.0.8103.15`를 별도 브라우저 컨테이너에서 CLI 라이브러리로 실행했다. 브라우저 MCP는 활성화하지 않았다. PostgreSQL 16 일회용 DB, 실제 설치 로그인·제품 management API·보호 Unix IPC·영속 preparation service·task 활동·보고서 handler를 사용했다. **실행 backend만 합성**이다. 서버 fixture는 저장소 밖의 Go test overlay이며 운영 서버에 새 API를 추가하지 않았다.

브라우저 API fixture는 최초 dev `9b3713e9` 서버와 UI 커밋의 웹 코드를 사용했다. 최신 dev 병합 후에는 같은 실제 공개 제품 입구의 PostgreSQL 시험을 별도로 다시 통과했다.

| 흐름 | 결과 | 실제 관측 |
|---|---|---|
| 브라우저 A의 준비 | PASS | UI 로그인 후 브라우저의 인증된 제품 API로 합성 Job 2개 생성·명시적 시작. 1개 완료, 1개 실행 중 |
| 핸들 없는 새 브라우저 B | PASS | 로그인 후 두 보존 회차가 `/runs`에서 표시됨 |
| 키보드 선택 | PASS | 실행 중 행에 초점, Enter로 선택·status 재조회·조회 힌트 생성 |
| task 활동 | PASS | 실제 PostgreSQL에 저장된 activity가 제품 공개 투영을 거쳐 sequence와 고정 안전 요약으로 표시됨 |
| 완료 Job·보고서 | PASS | 완료 행 선택 후 실제 remediation GET, 증거·감사 탭에서 실제 HTML 보고서 파일 저장(약 4.3KiB) |
| 새로고침 | PASS | 생성/시작 키 없이 저장한 최소 힌트로 동일 Job status·보고서 재조회 |
| 다른 운영자 문맥 | PASS | 설치 키로 정상 서명했으나 subject가 다른 합성 JWT는 실제 catalog에서 401, Job 표시 없음 |
| 재연결 송신 | PASS | B 로그인 완료 후 **POST/PUT/PATCH/DELETE 0건**, 새 합성 실행 0건, 브라우저 오류 0건 |
| 좁은 화면 | PASS | 390×844, 끝난 점검 행 표시, document 가로 넘침 없음 |

브라우저 검증 전체 동안 합성 backend의 `Execute` 호출은 준비한 **2회**로 유지됐다. 실제 모델·진단 대상 HTTP 송신 기능이 없는 합성 backend를 사용했으며 유료 호출과 운영 대상 진단은 **NOT_RUN**이다. 이 결과는 새 점검 wizard 전체 수용이나 실제 AI 진단/E③ 정확도 수용을 뜻하지 않는다.

현재 실제 설치 인증은 관리자 subject `ARTEX` 하나만 허용한다. 별도 정상 운영자 두 명의 실제 계정 로그인은 지원 범위가 아니므로 **NOT_RUN**이다. 서로 다른 허용 owner의 로그인 전환은 HTTP 계약 시험으로 검증했고, 실제 제품에서는 승인되지 않은 다른 subject의 접근 거부를 확인했다.

합성 보고서 원문·인증 자료·DB·브라우저 네트워크 원문은 커밋하지 않았다. 임시 근거는 `/tmp/jindo-ui-reconnect`에만 두고 이 문서에는 판정과 방법만 기록한다.

## 리뷰·통합 주의

구현 중 열린 #641은 진행·탐색 화면 표현 정리이며 목록 기능을 구현하지 않는다. `UrlLiveLive.tsx`, `workflowViews.tsx`, `live-url.test.tsx`, `redesign.test.tsx`가 겹친다. 본 PR의 활동 재연결·보고서 오류 표시·복구 쓰기 차단과 #641의 표현 변경을 함께 보존하는 팀 통합 리뷰가 필요하다. 해당 브랜치는 수정하지 않았다. #639의 workflow 개수 상한 변경도 별도 PR에 남겨 두었다.

UI 소유자와 결속·보안 담당 팀원의 리뷰를 요청한다. 작성자 자체 점검은 팀원 승인으로 세지 않으며 자동 병합하지 않는다.
