# 첫 진단 실제 브라우저 합성 수용시험

## 범위

이 시험은 실제 Go HTTP 서버, 실제 Planner/Worker와 Linux 격리 helper, 소유 loopback 대상,
일회용 PostgreSQL 16, 실제 Next 개발 서버와 설치된 Chrome을 연결한다.
합성 모델만 명령을 제안하며 API 응답이나 브라우저 fetch를 대체하지 않는다.

두 수용시험을 구분한다. 기존 승인·보고서 시험은 엔진과 Go supervisor가 같은 프로세스이며,
새 관리 흐름 시험은 실제 gateway·manage·report CLI를 별도 프로세스로 실행한다.
둘 다 소유 loopback과 합성 모델만 사용한다. 운영 설치 또는 실제 Qwen3.5 수용을 증명하지 않는다.
실모델·운영 대상 시험은 별도 승인된 입력이 없으므로 NOT_RUN이다.

## 사전 조건

- Go 1.26.3과 저장소 의존성, web/node_modules의 기존 설치.
- 비특권 Linux UID, curl, unshare user/pid/mount/net/ipc/uts namespace.
- 명시적으로 소유한 전용 일회용 PostgreSQL 16.
- Node 22 이상(WebSocket/fetch 내장)과 이미 설치된 Chrome/Chromium.
- 브라우저는 개인 프로필이 아닌 별도 일회용 프로필로 실행한다.
- 새 패키지나 브라우저를 자동 설치하지 않는다.

Windows Chrome과 WSL Next를 함께 사용할 때 localhost 전달이 가능해야 한다.
Chrome을 별도 프로필과 --headless=new --remote-debugging-port=49329로 시작한다.
CDP 포트는 loopback에만 두고 개인 브라우저나 기존 디버깅 세션에 연결하지 않는다.

## 실행

필수 환경:

- ARTEX_FINALIZATION_SCOPE_TEST_DSN: 소유 일회용 PostgreSQL DSN.
- ARTEX_FINALIZATION_SCOPE_TEST_OWNED=1
- ARTEX_FIRST_DIAGNOSIS_BROWSER_NODE: 설치된 Node 실행 파일의 절대 경로.
- ARTEX_FIRST_DIAGNOSIS_BROWSER_SCRIPT: 같은 Node가 읽을 수 있는 scripts/first-diagnosis-browser.cjs 절대 경로.
- ARTEX_FIRST_DIAGNOSIS_CDP: 선택 사항. 기본 http://127.0.0.1:49329.
- ARTEX_FIRST_DIAGNOSIS_BROWSER_OUTPUT: 필수. 브라우저를 실행하는 OS의 비공개 출력 디렉터리. 새 디렉터리는 0700으로 만들며 공용 임시 경로를 기본값으로 쓰지 않는다. 기존 디렉터리도 비공개 권한으로 준비한다.

WSL에서 Windows Node를 쓰면 NODE는 /mnt/c/.../node.exe,
SCRIPT는 해당 Windows 프로세스가 읽을 수 있는 WSL UNC 경로를 지정한다.

저장소 루트에서 다음을 실행한다.

~~~sh
go test ./server -run '^TestFirstDiagnosisRealBrowserOwnedPostgres$' -count=1 -v
~~~

환경이 없으면 명시적으로 SKIP/NOT_RUN이며 성공으로 계산하지 않는다.
일반 CI는 기존 TestFirstDiagnosisCombinedApprovalCandidateStoredReportOwnedPostgres를 계속 실행한다.

## 같은 프로세스 승인·보고서 검증

1. Go fixture가 실제 준비 task/intent, 통제 정책, 보호된 합성 인증자료를 만든다.
   CLI 명령을 직접 호출한 시험은 아니므로 CLI prepare-task/prepare-intents 수용과 구분한다.
2. 실제 Next 개발 서버를 임의 loopback 포트로 시작하고 Go 서버에 /api를 프록시한다.
3. 브라우저가 로그인하고 실제 task를 연결하여 prepared 상태를 확인한다.
4. 실제 supervisor를 시작하고 브라우저에서 /allowed 허용 및 /denied 거절을 결정한다.
   Go가 대상 수신 1, 허용 1, 거절 1, 후보 1, 일반 findings 0 및 감사/drain 완료를 검사한다.
5. 실행 서버를 닫고 같은 주소에 최소 DB 권한 report 서버를 연다.
6. 브라우저가 다시 로그인하여 후보, 판정, HTML 파일 저장, 조치 생성, 현재 버전 조회,
   실제 클립보드 복사를 검사한다.
7. Go가 원본 근거를 변경한 뒤 stale 프롬프트 복사 409 및 UI 안내를 검사한다.
8. 기존 Go 검사는 task 결속, 종료 권한 복구 거부, DB 저장 결과, HTML/조치 일치,
   로그 마스킹과 task/run/call 결속을 별도로 대조한다.

요청 승인, 실제 송신, 후보 판정, 실행 마감은 서로 다른 검증이다.
후보는 manual_review_required이며 확정 finding으로 승격하지 않는다.

## 독립 프로세스 관리 흐름 검증

~~~sh
go test ./server -run '^TestFirstDiagnosisManagedBrowserIndependentProcessesOwnedPostgres$' -count=1 -v
~~~

1. 실제 gateway 자식과 manage 자식을 private pipe로 연결한다. 인증키와 launch/registry는
   보호된 fixture 파일이며 브라우저에 전달하지 않는다.
2. 브라우저가 운영자 catalog를 읽고 초안을 생성한다. 승인 전 검증은 대기 상태이며,
   페이지를 다시 열어도 시작 요청을 보내지 않는다. 모델·대상 송신 수는 모두 0이다.
3. 시험 운영자가 브라우저 응답의 초안 ID·수정 번호·설정 지문을 확인한 뒤,
   보호 registry에 실제 task와 preparation 결속을 기록한다.
4. 브라우저가 다시 검증하고 단회 시작 확인란을 직접 선택한다. 실제 start 응답을 확인하며,
   중복 시작이나 일반 task/model API 우회가 없는지 검사한다.
5. 실제 엔진이 drain과 저장을 완료하고 runtime 포트가 닫히면, 별도 report CLI를 같은 주소에 연다.
6. 관리 상태 재조회 후 제한된 same-origin bridge로 저장 보고서에 연결하고 실제 HTML 파일을 받는다.
   대상 수신 1, 후보 1, 확정 findings 0을 DB/대상 fixture와 대조한다.

이 시험은 사전 승인 범위 모드다. 매 요청 허용·거절 및 stale 조치 복사는 위의 별도 68개 검증이 담당한다.
CLI prepare-task/prepare-intents는 fixture가 직접 준비하므로 이 두 브라우저 시험의 직접 호출 범위가 아니다.

## 확인된 수용 결과

2026-11-09의 소유 PostgreSQL 16·Chrome·Next 합성 환경에서:

- 같은 프로세스: prepared 11 + 승인 20 + 보고서 32 + stale 5 = **68개 PASS**.
- 독립 프로세스: 초안 21 + 단회 시작 10 + 저장 보고서 12 = **43개 PASS**.
- 실제 화면 픽셀에서 실행 상태·보고서의 한글 label과 레이아웃을 확인했다.
- 일반 전체 Go 검사에서 브라우저 환경변수가 없으면 이 두 opt-in 시험은 SKIP이다.
  별도 수용 결과와 합산하거나 SKIP을 PASS로 바꾸지 않는다.
- 실제 Qwen/유료 호출, 운영 대상, 운영 배포 수용은 **NOT_RUN**이다.
  합성 fixture만 승인·준비되어 있으며 운영 입력이나 실모델 연결을 사용하지 않았다.

## 출력과 정리

드라이버는 단계별 PASS/FAIL과 검증 수, API 경로/HTTP 상태를 요약한다.
초안 단계는 운영자 fixture 결속에 필요한 비밀이 아닌 초안 ID·수정 번호·설정 지문도 반환한다.
DOM 원문·인증 토큰·전체 로그·DB 덤프는 출력하지 않는다.
다운로드 HTML은 합성 fixture의 비공개 로컬 출력이며 Git에 추가하지 않는다.

Go 시험은 자신이 시작한 Next 프로세스와 fixture를 정리한다.
운영자는 자신이 시작한 Chrome 프로필/프로세스와 전용 PostgreSQL 컨테이너를 시험 종료 후 정리한다.
다른 테스트가 같은 DB를 쓰고 있다면 모두 종료한 뒤 정리한다.

## 남은 운영 수용

초안·HTTP receipt는 메모리 한정이며 프로세스 재시작 후 내구 복구는 후속이다.
registry 참조와 보호 launch의 의미 일치는 운영자가 확인한다.
기존 server 패키지의 물리적 엔진 의존 제거·일반 이벤트 스트림·운영 배치 자동화는 후속이다.
실제 Qwen·외부 대상·유료 호출은 실행하지 않았으며,
승인된 대상·범위·계정 참조·모델 식별자/제공자·비용/요청/시간 예산이 필요하다.
