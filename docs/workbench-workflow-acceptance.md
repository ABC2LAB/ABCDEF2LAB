# Workbench 탐색·선택·승인 통합 수용 기록

2026-11-10 KST. 구현은 #353 → #360 위에 쌓고 최신 `dev` `dcac603f5ef4`를 통합했다. 디자인 정본은 `feature/jaesang` `065fe205ba64`의 `web/mockups/abc2lab-workbench.html`이다. 정본 HTML blob `3840003f318e927aebf5d41ff3f49e892da57812`는 종전 `030536aa5095`와 동일하며 수정하지 않았다. 오래된 디자인 브랜치 전체를 병합하지 않았다.

구현과 소유 환경의 합성 모델 종단 시험을 구분한다. 실제 유료 OpenRouter Qwen은 **NOT_RUN**이다. 새 권한 모델·선택적 승인 정책의 팀 채택, UI 담당자의 시각적 수용과 실모델 최종 수용은 완료로 주장하지 않는다. main/dev 자동 병합은 하지 않는다.

## 1. 정본과 React 화면 대응

전체 구조·CSS·상호작용 조사와 API 계약은 [구현 계약](contracts/workbench-engine-workflow.md)에 있다. 아래 대응은 원본의 고정 예시 자료를 실제 저장 DTO로 교체한 결과다. 원본의 시연 건수·진행률·타이머·예상 시간은 제품 자료로 사용하지 않는다.

| 화면 | 보존한 구성·상호작용 | 실제 데이터와 차이 |
|---|---|---|
| dashboard | 레일, 작업 목록/상세, 새 점검 버튼, 승인 알림 | 현재 URL 실행 영수증과 실제 대기 승인만 표시. 전체 과거 작업 목록·종합 통계 API는 미지원으로 안내 |
| new | URL/이름, 접이식 모델 입력, `nSend`, 제외 규칙, 범위 확인 | 설치 모델·요청 예산·대상 권한·DNS 확인. `nSend`는 버전 있는 두 모드. 로그인 A/B·깊이·Worker 수 칸은 남기고 미지원 사유 표시 |
| crawl | `cNow`, 실제 로그, 경로 그룹/상세, `cFound/cSent/cAp/cSkip`, pause/stop | 서버 endpoint/event/count. 발견과 방문, 요청 쓰기와 전송 시도, 차단과 불명을 분리. 상태 버전 충돌은 안내·GET 복구 제공 |
| scope | `sQ/sFilters/sTable/sInsp`, 그룹 일괄/개별 선택·검색·펼침 | 저장된 발견 ID·메서드·출처·방문/응답·제외·선택. 확인 이후 선택은 고정. 서버가 원 승인과 교집합을 적용 |
| live | `lPlan/lFeed/lDecide/lState`, 요청 카드·허용/거부·정지 | 실제 계획/활동·모델 usage·E② 실행 상태와 요청별 승인. 도구 입력/출력·모델 원문을 활동 요약으로 노출하지 않음 |
| report | 후보 목록·상세, 증거/수정 권고 탭, HTML 내려받기 | PostgreSQL 후보·관찰·저장 범위·승인 이력·미실행/불명. 일반 후보는 검토 필요. 그래프/접근 지도는 임의 자료로 채우지 않고 미지원 사유 표시 |

비교 이미지: [정본 새 점검](evidence/workbench-workflow/canonical-new.png), [제품 새 점검](evidence/workbench-workflow/product-new.png), [실제 선택](evidence/workbench-workflow/product-scope.png), [저장 보고서](evidence/workbench-workflow/product-report.png), [390px 모바일](evidence/workbench-workflow/product-phone.png).

236px 레일, 가운데 시트와 384px inspector, 1199px inspector 서랍·759px 모바일 레일, 기존 색상/타이포그래피/CSS/아이콘/Esc·포커스 동작을 유지했다. 제품에는 실제 권한·실행 한계·오류 안내가 추가돼 정본과 문장·높이가 다르다. 모든 화면을 픽셀 단위로 동일하다고 주장하지 않는다. 정본 여섯 화면과 실제 제품 여섯 화면의 1440×1000 스크린샷, 1000px·390px 반응형 기록을 비교한다. 최종 시각적 승인은 UI 담당자 검토 대상이다.

## 2. 재사용한 구현

| 영역 | 재사용 파일/경로 |
|---|---|
| shell·디자인 | `web/src/workbench/Workbench.tsx`, `shell.tsx`, `ui/Rail.tsx`, `ui/`, `workbench.module.css`, `views/live/` |
| 인증·모델 키 | `live/auth.tsx`, `ExternalModelSettings.tsx`, `lib/diagnosis/diagnosis-url.ts`, 기존 제품 credential API |
| URL·대상 승인 | #360 `diagnosisprep/`, `diagnosisapi/`, `product_targets*.go`, `TargetApprovalPanel.tsx`, URL 영수증·시작/중지·owner/input 결속 |
| 엔진 | `agent/planner.go`, `worker.go`, `tools.go`, `e2_http.go`, `server/engine.go`, `first_diagnosis_supervisor.go` |
| 요청 집행 | `e2proxy/`, `e2runner/`, `e2export/`, `e2sandbox/`와 기존 요청 승인 저장·감사·단회 forward |
| 결과 | 기존 후보·E③·수정 권고·diagnosis-run·HTML 보고서 API, PostgreSQL 읽기 역할 |
| Docker | #353 `deploy/product/Dockerfile`, `compose*.yaml`, 설치·준비 entrypoint·고정 probe·최소 권한 DB |

API Recon 스크립트는 조사했으나 TLS 검증 우회·브라우저/직접 네트워크 호출을 제품 경로에 복사하지 않았다. 초기 탐색을 기존 보호 Worker와 같은 E② Bash 실행기에 연결했다. 신규 볼륨의 `/p` 소유권을 설치자가 먼저 UID1000/0700으로 준비하도록 실제 설치 오류도 수정했다.

## 3. API·DTO·상태 관리

인증된 Go API → IPC preparation service → 동일 task/run 엔진을 사용한다. 브라우저가 CLI를 만들거나 일반 `POST /api/tasks`로 우회하지 않는다.

| 경로 | 자료·명령 | 갱신·결속 |
|---|---|---|
| 기존 `/api/diagnoses/v2` catalog/draft/validate/start/status/stop | 입력 `workflow_mode`, `excluded_paths`; 실제 모델·예산·영수증·단계·usage | 기본 2초 polling. owner/input/source digest/revision. 시작·중지 자동 재전송 없음 |
| `GET /api/product/workflows/{jobID}` | `abc2lab.workbench-flow/v1` Snapshot | job/task/run/source/mode/revision 검증. 실제 endpoint/event/count/plan/activity, 2초 polling |
| `POST .../selection` | 예상 revision, 실제 발견 `selected_ids`, 단회 명령 키 | durable 저장 후 재개. 동일 키·다른 payload 거부. 이미 선택한 범위를 새 발견으로 자동 확대하지 않음 |
| `POST .../pause`, `.../resume` | 같은 버전·revision·단회 키 | 신규 전송·모델 admission 대기, 이미 admission된 작업은 마감 |
| 기존 task `request-approvals` list/detail/decision/stop | task/run/attempt/지문·만료·비민감 미리보기·결정·실행 상태 | 실제 pending에만 허용/거부. 응답 유실 후 GET 확인까지 조작 잠금. 마감 후에는 저장 HTML 이력을 조회 |
| 기존 `diagnosis-run`, remediation, HTML | 저장 run·후보·증거·workflow·승인 | read-only DB transaction, 공유본 경로/활동 비공개, 내부본 별도 선택 |

상태는 `crawling → awaiting_selection → diagnosing → completed`이며 자동 모드는 선택 대기를 건너뛴다. `stopped/failed/unknown`은 별도 상태다. 초기 크롤링만 끝난 회차를 전체 진단 완료로 만들지 않고, 실제 심화 Worker가 시작·마감되어야 한다. DB에는 현재 snapshot과 revision별 history를 저장한다. 재시작은 완료 기록을 읽으며 실행을 자동 재개하지 않는다.

프런트엔드 `live/workflow.ts`가 엄격한 DTO를 검증하고 `urlDiagnosis.tsx`가 URL polling과 workflow 명령의 세대를 독립 관리한다. 동시 상태 조회가 선택 명령 잠금·응답 유실 영수증을 지우지 않도록 회귀 시험을 추가했다. 서버 없는 필드는 미지원으로 표시하며 진행률·ETA를 만들지 않는다.

## 4. 크롤링·Planner/Worker 연결

Planner가 만든 초기 intent를 보호 Worker가 받아 프로그램 소유의 bounded 탐색을 실행한다. TLS 검증 curl → 실제 E② request/call/attempt/drain 근거 → 실제 HTML의 링크·폼·script 및 제한된 정적 API literal 해석 → PostgreSQL endpoint projection 순서다. redirect는 따라가지 않는다. 폼의 action을 GET으로 만들지 않으며 명시적 메서드가 없는 문자열은 UNKNOWN·자동 방문 제외다.

저장 항목은 method/path/kind/source/from/in_scope/change/visited/status/duplicates/selected/call_ref와 제한된 비민감 요청·응답 요약이다. 원문 body·헤더·쿠키를 snapshot으로 복사하지 않는다. 최대 32 endpoint, 페이지 1~32, 응답 4KiB~2MiB, 최근 event32/plan20/activity20 상한이 있다. UI의 요청 상한은 설치 catalog에서 받는다.

사용자 모드는 초기 탐색 완료 후 모델·심화 전송 admission을 대기시키고 선택 저장 후 같은 Planner/Worker를 재개한다. 자동 모드는 사전 확인된 읽기 범위의 적격 발견만 선택한다. 어떤 모드도 새로운 origin/IP/메서드/예산/쓰기 권한을 AI가 발급하지 못한다.

## 5. 실제 두 모드 실행

실행 facts는 [비민감 JSON](evidence/workbench-workflow/acceptance.json)에 기록한다. 대상은 고정 digest의 실제 OWASP Juice Shop v15.0.0이며 `/rest/admin/application-version` 응답으로 확인했다. `/workflow`는 소유 시험 front가 제공하는 **명시적인 정적 탐색 fixture**다. Juice Shop 원래 SPA 화면이라고 주장하지 않는다. 링크의 상품·버전 API와 POST 계정 생성은 실제 Juice Shop으로 전달된다. 모델 응답만 `owned-synthetic-workflow`로 합성했고 실제 제품 Planner/Worker/E②/DB/report를 실행했다.

최신 제품 소스 `0f89e68f47376302ae6b8780d7529563807523d0`, 이미지 `sha256:f8e80f42e9a1718d4cc4363f17bbc50cb6aa962a64c1b23c1c91bdb6a978ca7f`를 새 소유 환경에 설치했다. 바이너리 SHA256은 제품 `27ca49809380b9178ca61cc213a14e7e8afd668e9deb0cb25f3e4be63284f147`, helper `a389f844b89070c442c6bdefb55c1f661c9c7357de10957fb7aae681b698f538`다.

| 항목 | 사용자 승인 · task1 | AI 자동 · task2 |
|---|---|---|
| Chrome 종단 | 68 assertions PASS | 44 assertions PASS |
| 초기 실제 발견 | 5개, 초기 GET3 | 5개, 초기 GET3 |
| 심화 선택 | 저장된 GET상품 + POST계정 2개, 선택 전 deep0 | 사전 확인된 GET3 자동 선택, 선택 POST0 |
| 심화 Worker | 시작1/마감1 | 시작1/마감1 |
| 합성 모델 저장 usage | 13회·입력130/출력130 | 11회·입력110/출력110 |
| 실제 대상 수신 | GET4 + POST1 | GET4 + POST0 |
| E② 접수/허용/차단 | 7/5/2 · 불명0 | 5/4/1 · 불명0 |
| 개별 승인 | allowed/completed1, denied/not_sent1 | 대기 승인0·자동 쓰기0 |
| 제외·미선택 심화·잘못 만든 GET | `/excluded`, `/not-discovered`, `GET /api/Users` 모두0 | 모두0 |
| 결과 | 후보1·확정0, closed/completed, 감사·drain 완료 | 후보1·확정0, closed/completed, 감사·drain 완료 |
| 보고서 | 실제 UI·HTML 다운로드 PASS | 실제 UI·HTML 다운로드 PASS |

선택 POST는 한 번만 보냈고 저장 성공 응답을 유실시킨 뒤 GET으로 확정 선택2개를 복구했다. pause/resume 동안 대상 수신 계수가 유지되고 허용 버튼이 잠겼다. 두 상태 변경 시도에서 승인 전 수신0, 허용 뒤 수신1, 거부 뒤 수신 증가0을 대조했다.

별도 task3는 선택 대기에서 실제 UI 중지 **33 assertions PASS**, URL 영수증 `cancelled/drain_complete=true`, workflow `stopped/deep_runs=0`, 신규 HTTP0을 확인했다. 기존 감독자의 즉시 사용자 중단 저장값은 `phase=failed/outcome=failed/drain_complete=false`이며 감사는 complete다. 이 기록을 정상 완료 회차로 바꾸거나 drain 성공으로 해석하지 않는다. 보고서에서도 저장 사실을 그대로 표시한다.

동일 이미지의 DB·준비·API·UI 재시작 후 두 완료 회차의 workflow·후보·HTML과 현재 브라우저의 중지 회차 보고서 복구 **11 assertions PASS**. 실제 합성 모델 총28회와 receiver 계수 전후 동일, 추가 실행0. 정본 여섯 화면 비교, 제품 여섯 화면·tablet·phone 및 모바일 가로 넘침 없음은 **14 assertions PASS**. 서랍 전환이 끝난 뒤 화면을 촬영했다.

## 6. E②·승인 경계

`confirm_every_request`의 모든 요청 승인 의미를 유지했다. 별도 `workbench_selective_v1` 정책만 사전 확인 읽기 범위의 본문 없는 GET/HEAD를 자동 허용한다. GET의 의미적 무해성을 메서드만으로 판단하지 않는다. 사용자 모드의 복구 가능한 쓰기는 설치 상한과 대상 동의 둘 다 필요하고 실제 전송 전 별도 확인을 거친다. POST는 분류기가 읽기라고 하더라도 자동 bypass하지 않는다.

원래 E② origin/IP/port/TLS/path/method/예산 검사에 workflow 단계·선택·제외를 추가한다. 승인 후 전송 직전 다시 범위를 검사한다. DELETE 및 삭제/초기화/로그아웃/결제/송금 등의 고위험 경로는 거부한다. UI의 선택은 HTTP 송신 승인과 별개다. 수동 workflow의 새 호스트 정책은 `ApprovalDisplay.PublicPath`를 켜고 선택 화면과 같은 DLP 마스킹을 적용한 경로를 표시한다. query·본문 원문은 공개하지 않는다. 기존 비공개 정책의 마스킹을 UI에서 풀지 않으며, 결정은 exact attempt·요청 지문에 결속한다. 아래 기존 종단 실측은 이 리뷰 수정 전 SHA의 기록이다.

실제 receiver 계수와 Juice Shop SQLite의 허용 시험 계정 1·거부 계정 0을 대조한다. 합성 fixture 이메일·암호 외 실사용자 자료는 사용하지 않았다. 일반 상품 응답 후보는 수동 검토이며 E③ 확정 취약점은 0이다.

## 7. Docker·브라우저·회귀 검사

소유 격리 Compose, PostgreSQL16, Chrome157 및 분리된 브라우저 profile에서 검증한다. 제품은 UID1000, read-only rootfs, cap_drop ALL, Docker socket/privileged 없음, 보호 sandbox preflight를 유지한다. DB·진단 상태는 볼륨으로 보존한다.

실행 스크립트는 `scripts/workbench-workflow-browser.cjs`다. fixture 역할은 `deploy/product/compose.workflow-acceptance.yaml` overlay에 선언했고 보호 설치 입력과 포트·CIDR 조건은 [제품 절차](../deploy/product/README.md#탐색선택요청-승인-종단-시험)에 있다. Windows Node22 + 소유 Chrome CDP loopback을 사용한다. private 설치 JSON·fixture DNS 토큰 파일·출력 디렉터리를 인자로 받고 비밀은 로그로 출력하지 않는다. 순서는 새 설치 → `manual` → `auto` → `stop` → 서비스와 DB 재시작 → `recover` → `design`이다. 새로운 제품 실행/브라우저 결과를 모의하지 않으며, 응답 유실 시험은 서버가 selection을 저장한 200 응답만 CDP로 버린다. `manual-report/stop-existing`는 실패한 시험 harness의 기존 회차 후속 확인용이며 자동 재실행 경로가 아니다.

회귀: Web **502 PASS/0 FAIL/0 SKIP**, 변경 TS/TSX16개 Biome PASS. 전체 Go의 이전 스냅샷은64패키지 **7,520 PASS/0 FAIL/18 SKIP**이며 이후 변경을 해당 패키지 race로 재검증했다. 최신 전용 PostgreSQL 서버 시험은 **1,994 PASS/0 FAIL/13 SKIP**. 모델/브라우저/독립 프로세스 등 명시적 별도 환경 SKIP은 통과로 계산하지 않는다. `diagnosisflow/diagnosisprep/e2proxy/e2export/report/agent/CLI` race, 최신 dev의 `e2sandbox/e2-eval-policy` race, Go build/vet 및 darwin/arm64·windows/amd64 build PASS. 평가 Python **1,733 PASS**, E② 배치 검사 **55 PASS**. 최신 PR head Actions는 별도로 확인한다.

최초 시험의 실패도 보존했다. 새 볼륨 소유권 오류를 수정했고, proxy 2분 설정이 기존 최대1분 계약을 위반한 실패를 수정해 실제 encoder로 사전 검사한다. 해당 설치의 unknown journal·예약을 지우거나 환불하지 않고 별도 볼륨으로 보존했다. 후속 시험에서 폼 GET 오인·모드 instruction 누락·선택 세대 경합·활동 원문 노출을 수정했다. 시험 harness의 마스킹되지 않은 경로 가정과 마감 뒤 live 승인 API 조회 오류는 수정했으며 그 실패 회차를 성공 회차로 덮어쓰지 않았다.

기존 설치의 helper 지문과 다른 이미지로 업그레이드하면 저장 보고서도 fail-closed다. pin을 수정해 우회하지 않았다. 이번 수용은 **동일 설치·동일 이미지 재시작**이며 임의 이미지 교체·정책 마이그레이션 수용이 아니다.

### PR #366 내구성 리뷰 후속 검증

리뷰 #5839384822와 #6763822796을 반영한 제품 소스는 **`f42615a0df32337a588b80acb4f0a41235205493`**, 실행 이미지 ID는 **`sha256:8411c3ab83a3ec3a2a19addc0fdce1fe29d879fa5cd5bc7eda43192b8a0965d6`**이다. 새 볼륨·망의 소유 Juice Shop15.0.0, 같은 선언적 탐색 fixture, 로컬 합성 모델로 실제 실행했다. 이전 표의 실측과 구분한다.

| 후속 종단 검사 | 실측 |
|---|---|
| 사용자 승인 | **77 assertions PASS**. 발견5·선택2, 실제 POST 허용1 도달/거부1 미전송, 선택 응답 유실·pause/resume·후보·HTML |
| AI 자동 | **44 assertions PASS**. 읽기3 선택, POST0·제외/미선택0, 심화 Worker·저장 후보·HTML |
| 선택 대기 중 UI 중지 | **33 assertions PASS**. cancelled/drain_complete, workflow stopped·deep0, 후속 송신0 |
| 같은 이미지·DB 재시작 | **11 assertions PASS**. 두 완료 회차와 HTML 복구, 모델 총28회·receiver 계수 유지, 추가 실행0 |
| 실제 계정 대조 | Juice Shop SQLite 집계: 허용 계정1·거부 계정0 |

새 승인 화면에서 `POST /api/Users`가 보이고 query·본문은 공개하지 않는 것을 [화면](evidence/workbench-workflow/review-approval.png)에서 확인했다. 정본 HTML blob `3840003f318e927aebf5d41ff3f49e892da57812`는 그대로다. 제한된 [검증 수치·변이 목록](evidence/workbench-workflow/review-20261110.json)을 보존한다. 비밀 설치 입력·토큰·원문 로그·DB 덤프는 포함하지 않는다.

후속 회귀: Web **518 PASS/0 FAIL/0 SKIP**, 정적 빌드·타입·변경8파일 Biome PASS. 로컬 전체 Go race는 마지막 후속 수정 전에 **64패키지 7,589 PASS/0 FAIL/18 SKIP**이며, 후속 `agent/diagnosisflow`는 race로 별도 통과했다. 실제 provider 요청을 검사해 Planner의 대상 경로가 user 역할에만 있음을 확인했다. 전체 build/vet PASS. 실제 PG16에서 NUL·긴 method·비표준 상태값·닫힌 pool 고장을 검증했고, 소유자/revision/선택/신뢰 마감/SQL CAS/프레임·endpoint·명령 상한/설치 동의/쓰기 필터/승인 경로 DLP/Planner 역할/부분 응답 등 **26종 변이가 지정 시험에서 실제 실패**했다. 최신 head 전체 CI 수치는 PR 본문을 따른다. SKIP은 통과로 계산하지 않는다.

최초 후속 자동 모드는 큰 상품 응답의 `body_incomplete` 때문에 읽기 선택을 잘못 제외해 완료 대기를 넘겼다. 이를 수정하고 새 설치에서 위 두 모드를 다시 돌렸다. 그 실패 회차의 저널·예약은 별도로 보존하며 성공 회차에 포함하지 않는다. 새 Compose 입력 경로 오타는 진단 시작 전 거절돼 바로잡았고, 추가 agent 시험이 전체 회귀의 DB 초기화와 경합한 실행도 중단해 별도 소유 DB로 분리했다. 이 실패를 통과로 계산하지 않는다. 상세 실패 분류·프레임/endpoint/명령/바인딩 상한은 [워크플로 계약](contracts/workbench-engine-workflow.md#대상-입력과-실패-처리)을 따른다.

### 승인 후 비차단 리뷰 후속

`176e696` 구현 승인 뒤 남은 네 항목도 반영했다. `workbench_selective_v1`은 범위 확인에서 **읽기 사전 승인 · 쓰기 건별 확인**으로 표시한다. 자동 모드 경계는 선택·분류가 앞에서 잘못 허용된 상황을 따로 구성해 POST·본문 있는 GET·상태 변경 가능 GET을 거부하고 같은 요청의 수동 모드 대조는 통과하는지 확인한다. 실제 Worker → Crawl → Bash 어댑터가 만든 명령을 argv 전용 합성 실행기에 넘겨 `--globoff`와 정규 인코딩 URL 한 개의 결속을 검사한다. 이 시험은 실제 HTTP 송신 수용이 아니다.

Planner 자료도 Worker와 같은 결속 실행기의 `RedactOutput`을 거친다. 미획득·미검증·다른 task·닫힌 lease 및 마스킹 실패에서 원문으로 대체하지 않는 것을 회귀로 검증한다. 추가로 소유 PostgreSQL의 실제 task에서 `plannerLoop` → 실제 Planner → 합성 provider 요청까지 구동해 선택 경로의 자격값이 제거되고 공개 경로는 user 자료에 남는지 검증한다. 실제 실행기의 보호 자격값을 사용하지만 sandbox·대상 HTTP는 실행하지 않는다. 루프 호출부를 원문 `SelectedData()`로 되돌리는 변이가 자격 유출 assertion에 잡힌다. 실제 모델·대상·Docker 수용은 위 `f42615a` 기록이며 이 후속 수정에서 새 Docker 실행으로 계산하지 않는다. 최신 회귀·변이·CI 결과는 PR 본문을 따른다.

## 8. 미구현·미수용

- **유료 OpenRouter Qwen NOT_RUN**. 승인 계정·실제 USD 상한·외부 데이터 전송/provider 조건이 준비되지 않았다. 기존 보호 키 UI/등록 서버 경로·예산 응답은 유지하고 회귀 시험으로 검증했으나 외부 키→유료 모델 Docker 종단 수용으로 계산하지 않는다.
- 동적 SPA 전체 API·인증 A/B 세션·임의 depth/Worker/model/provider/server 설정은 미지원이다. 정적 탐색 fixture 수용을 모든 사이트의 완전한 크롤링으로 일반화하지 않는다.
- 전체 과거 진단 목록·종합 대시보드·이 URL 경로의 자산 그래프/접근 지도 API는 미지원이다. 대응 칸·사유를 보존했으며 시안 기능 축소·차이는 UI 담당자 수용이 필요하다.
- 임의 취약점 후보의 E③ 확정·다계정 IDOR·실제 canary 판정은 이번 시험 범위 밖이다. 기존 E③ 저장 판정 지원과 회귀 조건은 유지한다.
- 새 선택적 승인 정책과 #360 권한 모델은 팀 리뷰·채택 전이다. 범용 쓰기 허용·무제한 자동 진단·무승인 설치 권한 확대를 지원하지 않는다.
- 이미지 교체 시 기존 helper pin·설치 세대의 마이그레이션은 별도 범위다. unknown 실행 자동 복구/재전송도 제공하지 않는다.

## 9. PR 의존 관계

#353 Docker 제품과 #360 회차별 대상 승인은 dev에 병합됐다. 이번 PR의 base는 `dev`이며 `44a4a0a` 위로 Workbench workflow 고유 변경을 rebase했다. #367의 개발팀·설치 조직·운영자 역할 명세와 기존 dev의 #359/#361/#362/#363/#364/#365 변경을 포함한다. 과거 UI #352는 dev에 병합된 기반으로 재사용했다. 수용 기록의 소스 SHA·이미지 ID는 측정 당시 값으로 보존하며, rebase를 새 제품·실모델 수용으로 계산하지 않는다. 정책·E② 변경은 관련 담당자, 여섯 화면은 UI 담당자의 검토를 요청한다.
