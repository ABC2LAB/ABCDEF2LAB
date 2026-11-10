# Workbench 정본과 실제 진단 워크플로 연결

상태: 구현 및 소유 합성 모델 종단 검증. 실제 유료 Qwen·팀 정책 채택·UI 담당자 수용은 미완료. 선행 #353 → #360에 의존하며 자동 병합하지 않는다. 대상 승인 권한 모델 및 새 요청 정책은 팀 채택 검토 대상이다.

## 디자인 정본과 분석

정본은 `feature/jaesang`의 `065fe205ba64` (HTML blob은 기존 `030536aa5095`와 동일)에 있는 `web/mockups/abc2lab-workbench.html`이다. 원본을 그대로 보존한다. 전체 HTML DOM·CSS·JavaScript AST를 분석했다. 여섯 화면, CSS selector 634개, 함수 101개가 있다. React CSS와 selector/declaration 단위의 단순 비교에서 530개가 같았다. 이는 미디어 조건·CSS module 범위를 정규화하지 않은 조사 수치이며 시각적 일치 판정이 아니다.

236px 레일, 가운데 시트·384px 상세 패널, 1199px 상세 서랍·759px 레일 서랍, 강조색·타이포그래피·Esc/포커스 동작을 유지한다. 기존 Workbench/shell/Rail/Icon/parts/CSS와 인증·URL v2·모델 키 등록·요청별 승인·보고서 어댑터를 재사용한다. 정본의 LINES 55개, EP 37개, PLAN 10개, LT 23개 및 결과/그래프 예시·타이머는 제품 실행 자료로 사용하지 않는다.

| 화면 | 정본 요소 | 현재 React 대응 | 연결 전 차이와 필요한 실제 자료 |
|---|---|---|---|
| dashboard | dTodo/dActive/dDone/dInsp, 레일 승인 알림 | UrlDashboardLive, Rail | 실제 URL 영수증·승인 대기·탐색 단계로 구성. 고정 예시 목록 사용 금지 |
| new | nForm/nUrl/nLlm/nSend/nExcl, 요약·DNS 검증 | UrlNewScanLive, ExternalModelSettings, TargetApprovalPanel | 모드·제외 범위를 서버 입력에 결속. 설치된 모델만 선택. nSend 전체 허용을 무제한 권한으로 대응하지 않음 |
| crawl | cState/cNow/cLog/cTree/cFound/cSent/cAp/cSkip, 상세·pause/cancel | UrlAssetsLive → 실제 탐색 화면 | 기존은 설치 범위만 표시. 실제 발견·방문·대기·전송/차단·멈춤 API 필요. 임의 진행률/예상 시간 금지 |
| scope | sCount/sFilters/sQ/sTable/sInsp/sStart | UrlScopeLive → 탐색 후 범위 선택 | 기존은 시작 전 승인 요약. 실제 발견 ID·메서드·출처·관찰·포함 상태, 선택 저장/재개 필요 |
| live | lPlan/lFeed/lDecide/lState/mSent/mBlk/mHold | UrlLiveLive, 기존 승인·활동/보고서 DTO | 원본 엔진 계획·모델 사용량·E② 사실 표시. 선택/일시정지 상태 추가. 실제 승인 API 재사용 |
| report | rList/rInsp, 탭·보고서 내려받기 | UrlReportLive, ReportLive, WorkflowReportPanels | 저장된 탐색 범위·선택·승인·미실행과 기존 증거 연결. 후보를 확정으로 승격하지 않음 |

로그인 A/B 입력, 모델 서버 임의 변경, 무제한 depth/worker 등 현재 서버가 지원하지 않는 시안 칸은 구조를 보존하고 비활성 상태·사유를 표시한다. 화면에서 제거하거나 새 기능을 축소할 경우 담당자 검토가 필요하다. 비밀 입력을 React/store/storage에 추가하지 않는다.

## 엔진·탐색 재사용 조사

`agent/planner.go`, `agent/worker.go`, `server/engine.go`, `agent/tools.go`와 `skills/api-recon/`을 조사했다. 원본 Planner의 intent 생성, Worker의 E② Bash 경로·안전 바닥·회차 공유 호출 한도, 실제 감독자·감사·PostgreSQL·보고서를 유지한다. 보호 Worker의 그래프 도구는 제한돼 있고 기존 자산 INSERT를 제공하지 않아 별도 프로그램 소유 탐색 projection이 필요하다.

API Recon은 정적/동적 탐색 참고 템플릿이며 로그인 우회 mock·임의 브라우저 실행을 포함한다. 제품에서 그대로 실행하지 않는다. 프로그램 소유 초기 탐색은 기존 E② Bash 실행기로 TLS 검증하는 curl을 실행하고 실제 받은 HTML 링크·폼·script 참조 및 제한된 정적 API literal을 해석한다. 폼 주소에서 GET을 만들어내지 않으며, 명시적인 메서드 호출을 확인하지 못한 문자열은 UNKNOWN으로 기록하고 자동 방문하지 않는다. HTTP 요청·call/attempt·drain 근거를 기존 E②에서 얻는다. 동적 SPA 브라우저 탐색 완성으로 주장하지 않는다.

## API와 상태 전이

URL 초안·승인·시작·상태·중지·영수증 복구는 `diagnosis-api.v2`를 유지한다. 입력에 선택적인 버전 있는 workflow 모드와 제외 경로를 추가하고 source digest에 포함한다. 설치자가 workflow를 켠 제품의 catalog만 해당 기능을 광고한다. 기존 설치·일반 서버의 권한을 자동 확대하지 않는다.

실제 엔진: 인증 API → URL 준비 서비스 → 같은 task/회차의 Planner intent → 보호 Worker의 초기 탐색 → (사용자 선택 대기 또는 AI 자동 선택) → 같은 Planner/Worker의 심화 진단 → 기존 저장 보고서. 기본 POST /api/tasks fallback은 없다.

| 자료/명령 | 경로 | 갱신/결속 |
|---|---|---|
| 모델·권한·예산, URL 실행 영수증 | 기존 /api/diagnoses/v2 | 기존 2초 polling, owner/input/source digest/task |
| 실제 발견·탐색 상태·선택·관찰/명령 영수증 | /api/product/workflows/{jobID} | 인증 IPC, 2초 polling, job/task/run/source digest/revision |
| 선택 저장·진단 재개 | .../selection | 예상 revision·단회 명령 키, 서버 발견 ID와 원 승인 범위의 교집합 |
| 일시정지·재개 | .../pause, .../resume | 동일 결속, 신규 전송/모델 admission 대기, 이미 admission된 요청·응답은 마감 |
| 요청별 확인·허용/거부·중지 | 기존 /api/tasks/{id}/request-approvals 계열 | 실제 task/run/attempt/지문. 응답 유실 후 GET으로만 복구 |
| 증거·후보·HTML | 기존 보고서 API + 저장 workflow projection | 실제 저장 근거, 미검증·판단 불가 보존 |

워크플로 단계는 crawling → awaiting_selection → diagnosing → completed이다. 자동 모드는 초기 탐색 뒤 원 승인 안의 적격 발견만 선택한다. 사용자 모드는 선택 저장 전 심화 요청을 보내지 않는다. 진단 중 새로 발견한 경로는 선택 범위를 자동 확대하지 않는다. 초기 탐색만 끝나고 심화 Worker가 한 번도 실행되지 않으면 전체 완료로 처리하지 않는다. 멈춤·실패·unknown은 별도 사실이며 완료율을 만들어 표시하지 않는다.

초기 탐색과 선택/명령·이벤트는 PostgreSQL에 저장한다. 재기동은 종료되지 않은 진단을 자동 재실행하지 않으며 과거 결과만 읽는다. unknown을 없애거나 예약을 환불해 재실행하지 않는다.

## 요청 정책 제안

새 정책 이름은 `workbench_selective_v1`이다. 기존 confirm_every_request는 GET을 포함해 전부 확인하는 의미를 유지한다. 새 버전 정책은 운영자가 사전 확인한 읽기 범위·본문 없는 GET/HEAD만 자동 허용하고 나머지 허용된 요청은 기존 요청별 확인 경계로 보낸다. 메서드만으로 무해성을 주장하지 않는다. 상태 변경은 설치 상한과 대상의 복구 가능한 시험 범위에 별도 동의가 있어야 한다. 삭제·초기화·로그아웃·결제/전송 등 고위험 경로는 자동/확인 모두에서 제외한다. AI 자동 모드는 상태 변경을 자동 승인하지 않는다.

### 경로 단위 읽기 승인과 query

현재 대상 attestation의 `query_policy`는 `any`다. endpoint 식별·선택과 `requestEndpoint`의 대조는 메서드와 path 단위이며 query를 대조하지 않는다. 따라서 `GET /read/item`을 읽기 범위로 확인하면, 그 경로의 `GET /read/item?key=value`처럼 **모델이 정한 query도 건별 확인 없이 전송될 수 있다**. 사용자 승인 모드도 이 사전 확인 읽기 면제는 같다. 시작 URL 입력에서 query를 막는 것은 이후 Worker 요청의 query를 제한하는 기능이 아니다.

구현은 운영자의 경로 단위 비변경 확인을 그 경로의 허용된 모든 query 조합까지 포함하는 넓은 확인으로 취급한다. 서버는 query의 의미나 무해성을 검증하지 않으며 GET이라는 이유만으로 상태 변경이 없다고 보장하지 않는다. query로 상태를 바꾸는 경로를 이 읽기 범위에 등록하면 자동 차단되지 않을 수 있다. 그런 범위는 사전 확인 읽기에서 제외해야 한다. query key/value별 제한·개별 읽기 승인은 현재 Workbench 정책에서 지원하지 않는다.

이 한계는 대상 등록·모드 선택·시작 전 승인 범위에 표시한다. 절대 규칙6을 충족하는 운영자 확인으로 받아들일지는 정책 채택 시 팀이 결정하며, 문서·UI 설명만으로 새 권한이나 안전성 검증을 발급하지 않는다. 기존 `confirm_every_request`를 query 면제로 바꾸지 않는다. endpoint 선택 범위가 query 값을 검증했다는 뜻으로 보고서에 표시하지 않는다.

E②는 고정 origin/IP/port/TLS/path/method/예산 검사 뒤 workflow의 실제 단계·정확한 선택 메서드/경로·제외 규칙을 추가로 검사한다. UI/LLM 입력으로 이 경계를 대체하지 않는다. 허용/거부·사전 대기/실제 전송/불명은 별도 사실로 기록한다.

## 수용 기록

실행별 SHA·실측·화면 비교·미수용 범위는 [워크플로 수용 기록](../workbench-workflow-acceptance.md)을 따른다. 소유 격리 Juice Shop, 합성 모델과 실제 유료 Qwen을 구분한다. 화면 대응·두 모드·실제 발견/선택·승인 전/후/거부의 대상 도달·중지·보고서·Docker 재기동/응답 유실의 실제 검증 결과를 해당 문서에 기록했다. 유료 Qwen은 별도 계정·예산·외부 전송 승인 준비 전 NOT_RUN이다.
