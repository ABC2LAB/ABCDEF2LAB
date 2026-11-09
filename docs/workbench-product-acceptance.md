# Workbench Docker 제품 수용 기록

2026-11-10 KST. 기준 `origin/dev`는 `8f479109af72200b665605927c18af1b5fbccb97`이다. 선행 #351·#352와 기존 제품 이미지 #353의 최신 커밋을 통합했다. 자동 병합하지 않는다. 구성도·설치·화면별 API는 [제품 문서](../deploy/product/README.md), UI 신뢰 경계는 [API 지도](contracts/ui-api-map.md)에 있다.

## 실행 출처

| 항목 | 검증 값 |
|---|---|
| 실제 실행 소스 커밋 | `29694c53dc20970510815c9f974033a1cee12da3` |
| 제품 image ID | `sha256:ba912b32f1bdc917e270b77a9cd69007a412fe89f2dcb75456d13154e3cc5b39` |
| first-controlled-diagnosis SHA256 | `b8c10a134fbf853aa4c4de53285d617b54a9f7d985d8637c2048ccaacfde88df` |
| E② helper SHA256 | `25cea9de2914f5c4d90b26441dd77a300b9f6aed13c07b9ba8934ab5e28d71e3` |
| UI·API·준비 서비스 출처 | OCI revision과 `/api/product/health`가 위 소스 SHA와 일치 |
| 시험 대상 | OWASP Juice Shop v15.0.0, 고정 digest·소유 HTTPS front |
| 모델 | 내부 합성 OpenAI 호환 서버 `product-synthetic` |

Go 바이너리·UI를 같은 제품 Dockerfile에서 빌드했다. 원본 배포 엔진 이미지나 가짜 실행 엔진을 사용하지 않았다. 실제 dev 계열 Planner·Worker·URL v2·E②·후보 저장·보고서 경로를 실행했고, 모델 응답만 합성했다. 후속 문서 커밋은 이 수용 이미지의 실행 출처를 바꾸지 않는다.

## 실제 Docker·브라우저 결과

| 검증 | 결과 |
|---|---|
| 새 설치·기동 순서·health | PASS. private 파일·최소 권한 DB 역할·보호 위임·IPC 설치 |
| 격리 사전 검사 | PASS. 실제 helper로 고정 true 실행·읽기 전용 mount·seccomp·종료 확인. 모델/대상 호출 없음 |
| 격리 미지원 차단 | PASS. 기본 Docker seccomp의 preflight는 네트워크 없는 환경에서 실패하며 후속 준비를 시작하지 않음 |
| 컨테이너 권한 | 3개 runtime 모두 UID 1000, read-only rootfs, cap_drop ALL, privileged=false, 별도 PID namespace. Docker 소켓 없음 |
| 볼륨 | workbench 0개, api `/p` 읽기 전용, preparation `/p` 쓰기. 루트 평가 Dockerfile·ignore·bench compose 유지 |
| 로그인→URL→범위 | 실제 Chrome root Workbench PASS, 23 assertions. 데모 전환 없음 |
| 시작 전 송신 | 모델 0, 대상 허용·범위 밖 HTTP 모두 0 |
| 실제 단회 시작·응답 유실 | 서버 수락 응답만 버린 시험 PASS, 10 assertions. start POST 1회, 자동 재전송·일반 task 생성 0 |
| 실제 엔진 회차 | task 1개, Planner intent 1개, Worker 1개. 합성 모델 goals 2 + Planner 3 + Worker 4 = 9회 |
| E② | attempts 2, allowed 1, denied 1, forward_attempted 1, responses 1, unknown 0, Bash calls 2 |
| 실제 대상 | 승인된 `/rest/products/search` HTTPS 수신 1·HTTP 200·Juice Shop JSON success. 범위 밖 `/outside-approved` 수신 0 |
| 저장·판정 | 후보 1, 확정 취약점 0, findings 0. 일반 HTTP 관찰은 수동 검토 필요 |
| 마감 | run phase=closed, outcome=completed, drain_complete=true, audit_complete=true |
| 보고서·HTML | 실제 Workbench PASS, 19 assertions. 저장 run·검토 집계·HTML 다운로드 확인 |
| 앱·API·준비·DB 재기동 | 같은 영수증·보고서·HTML 재조회 PASS, 19 assertions. 새 mutation 0. 모델 9·대상 1/0 유지 |

범위 밖 요청을 의도적으로 시험했으므로 해당 intent/실행 집행 상태는 policy-limited/blocked다. 이것은 E② 차단의 관측이며, run은 감사·drain을 완료해 정상 마감했다. 차단을 취약점이나 외부 도달 성공으로 표시하지 않는다.

저장 결속은 task `1`, intent `3`, run `run-prep-a60954db13eabb05d0e20c32fd17e762`, 관찰 call_ref `call_ee4df6e2e66ff82b5cfe5ca096e6c06b`로 대조했다. 허용 감사 이벤트는 response_complete=true·status_code=200, 차단 이벤트는 forward_attempted=false·policy_denied였다. 후보의 지식 fact 상태와 E③ 확정 판정은 구분한다.

실제 PostgreSQL `llm_usage` 9행에 합성 usage input 90·output 90 토큰이 저장됐다. 로컬 합성 모델이므로 실제 유료 USD 사용량은 없다. 이 토큰·시간을 실모델 성능이나 비용으로 해석하지 않는다.

다운로드한 `diagnostic-task-1-shared.html`은 4,487 bytes, SHA256 `869b6db3e4091b6c63403482df97c446fb0c7232d8715021f8e4826a0cfeab23`다. 원문·HTML·로그·DB 덤프·키·개인 브라우저 자료는 커밋하지 않았다.

## 회귀 검사

- Web: 34파일, **469 PASS / 0 FAIL**, TypeScript PASS. URL→외부 LLM 펼침→모델·키 등록→범위 조회에서 키가 진단 입력에 섞이지 않는 회귀 포함.
- 실제 소유 PostgreSQL 제품 검사: 인증·키 열만 쓰기·키 읽기 금지·미승인 모델·설치 변경·privileged writer 거부 PASS. 등록 중 preparation 호출 0.
- Go 전체: 저장소 CI runner의 패키지별 DB 초기화로 **62패키지, 6,726 PASS / 0 FAIL / 344 SKIP**. 별도 제품 5개 검사·실제 DB 키 등록·preflight 호출자 명령 거부 PASS. SKIP은 필요한 시험 입력·소유권·선택 수용 환경 미제공 등이며 통과가 아니다.
- Go vet·build, 변경 CLI의 darwin/windows 크로스빌드, gofmt·diff PASS.
- 평가 스크립트 **1,652 PASS**, E② 배치 검사 **55 PASS / SKIP 없음**, Web 변경 감지 **16 PASS**.
- 동일 Docker 권한 조건의 기존 `TestSandbox*` 검사 PASS. 실제 HTTP relay·peer 결속·파일/권한 탈출 방어·환경 비밀 부재·동시 call 분리·추가 namespace 생성 차단 포함.

PR Actions 결과는 해당 PR head의 체크를 별도로 확인한다. 로컬 검사나 과거 CI를 최신 head의 CI 성공으로 대신하지 않는다.

## 실모델 미완료 조건

**실제 OpenRouter Qwen: NOT_RUN.** 별도 유료 실행 승인·실제 USD 총액 상한·보호 키/계정/provider 승인 자료가 제공되지 않았다. 키 값은 채팅으로 받지 않으며, 브라우저 외부 LLM 입력이나 승인된 보호 파일만 사용한다. 임의 계정 spend cap·privacy/ZDR·provider 승인을 생성하지 않았다.

외부 모델 입력 UI와 서버 키 등록은 React 통합·실제 DB 검사로 검증했고, 이번 Docker 브라우저 회차는 local-only 합성 모델이다. 실제 외부 키 입력→유료 Qwen 실행 전체를 Docker에서 수용한 것으로 계산하지 않는다. 일반 HTTP 모드에서 다계정 IDOR E③ oracle·자동 크롤링·전체 취약점 확정도 수용하지 않았다.

따라서 **구현과 로컬 합성 Docker 종단은 확인됐으나, 실모델 최종 통합 수용 완료는 아니다.** 유료 조건이 준비되면 동일 E②·영속 영수증·저장 보고서 경로에서 별도 회차로 검증한다. main 직접 변경·자동 병합은 하지 않았다.
