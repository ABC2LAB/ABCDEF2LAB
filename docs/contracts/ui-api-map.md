# UI ↔ 엔진 API 매핑 (new_web abc2lab-ui → ARTEX Go server)

> 경계 계약. new_web 시안(12화면)을 Go 서버(`/api/*`, dev rewrite→`:8787`)에 배선하기 위한 화면별 엔드포인트 지도.
> 현재 라우트는 `server/server.go`, DTO는 `server/dto.go`·`web/src/lib/types.ts`(격리됨, 계약 레퍼런스).
> 작성: 이경준. 판정/증거 경계는 [verdict-record.md](verdict-record.md)·[e3-oracle.md](e3-oracle.md), KG 경계는 [kg-policy.md](kg-policy.md).

## 현재 첫 통합 진단의 진입점

실제 앱은 첫 진단 모드로 진입한다. 운영자 CLI의 prepare-task → prepare-intents → check → run과 종료 후 report 흐름을 보존한다. EntryScreen의 첫 진단 분기는 서버가 검증한 실제 task에 연결하며, 일반 ARTEX POST /api/tasks를 호출하지 않는다. 실행 서버는 선택 가능한 /api/tasks 목록을 보조로 쓰고, report 서버는 목록 없이 실제 task ID로 저장 결과를 확인한다. URL에는 비민감 task ID와 서버 종류만 보존하며 재진입 시 서버 확인을 다시 거친다.

ABC2LabUI의 명시적인 mode가 첫 진단과 일반 ARTEX 화면을 분리한다. 첫 진단 모드는 연결·진행·E② 승인·보고서 화면만 마운트하여 숨은 일반 API 요청을 만들지 않는다. 기존 일반 ARTEX와 목업 화면은 별도 모드로 남는다. DNS TXT·임의 URL 실행·API 키·세션 자격 입력 시안은 실제 첫 진단 경로에서 접근할 수 없다.

새 진단 관리 UI는 사용자가 명시적으로 열었을 때만 diagnosis-api.v1 카탈로그를 조회한다. 초안 생성·검증·시작은 별도 [진단 관리 계약](diagnosis-management.md)에 따른다. 기존 실행/report 서버의 allowlist를 넓혀 이 API를 넣지 않는다. 관리 서비스나 운영자 승인이 없으면 기존 CLI 연결은 유지하고 새 실행은 거부한다. 새 API 존재만으로 실제 독립 엔진 배치나 운영자 권위 공급이 완료됐다고 간주하지 않는다.

일반 HTTP 후보는 manual_review_required로 유지한다. 진단 보고서의 고유 entry를 한 번씩 집계하며 일반 findings 수를 더하지 않는다. confirmed, manual_review_required, not_executed, unknown, negative를 별도 표시하고 미확인 식별자·판정·근거는 미확인으로 낮춘다. 실행 마감·허용 결정·HTTP 응답은 취약점 확정과 다르다.

### 첫 진단 화면의 실제 API

| 화면 | API | 의미 |
|---|---|---|
| 기존 task 연결 | GET /api/tasks(실행 서버만), GET /api/tasks/{id}/request-approvals 또는 GET /api/tasks/{id}/diagnosis-run | 목록은 보조이고 실제 단일 task 응답 검증 후 선택 |
| 진행 | 요청별 승인 GET의 감독자 상태·집계 | ETA·단계 진행률·일반 활동/토큰 상세를 추정하지 않음 |
| 승인·중지 | 기존 요청별 승인 GET/decision/stop | ARTEX intercept와 별개. 자동 모드 0행도 감독자 중지 가능 |
| 저장 보고서 | GET /api/report/diagnostic/remediation?task=ID&audience=internal | DTO adapter + 판정 ViewModel 사용, 일반 task/findings/coverage 의존 없음 |
| 종료 기록 | GET /api/tasks/{id}/diagnosis-run | 저장 준비 NOT_RUN은 현재 실행 여부를 증명하지 않음 |
| HTML·AI 프롬프트 | 기존 diagnostic download, remediation POST, prompt GET | Bearer fetch, 현재 digest 검증, stale 복사 거부 |

다음 기존 표는 일반 ARTEX 모드의 계약이다. 첫 진단 모드에서 모든 행을 호출하지 않는다.

## ARTEX 도메인 모델 (전제)
- **task** = 대상+범위 1회 진단 단위. 생성→control(start/stop)→exploration→findings.
- **exploration** = 엔진 탐색: activity(스트림)·graph·frontier·intents·nodes.
- **findings** = 발견. 워크플로우 `status`와 **E③ 구조화 판정**(CONFIRMED/SUSPECTED/UNDECIDABLE)은 별개 — 후자는 verdict-record 계약.
- **intercept** = ARTEX 도구 호출 승인 게이트. `pending`(승인 대기)·`decide`(허용/차단)·`history`(명시적 결정/승인 기록)·`rules`(allow/deny/ask 규칙). 실제 네트워크 전송 통제 E②의 집행·송신 증명과는 별개다.
- **audit** = 감사 로그.

## 화면별 매핑

| 화면 | 주 엔드포인트 | 비고 |
|---|---|---|
| **entry** (주소 입력) | 첫 통합 진단 시작은 CLI. 기존 `POST /api/tasks`는 별도 ARTEX 실행 API | 주소 입력은 현재 UI 흐름이며 통제 진단 시작 API가 아님. DNS TXT 소유확인은 시안 동작이고 백엔드 라우트 없음 |
| **target** (방식/요약) | `GET /api/tasks/{id}`, `POST /api/tasks/{id}/goals` | 대상 요약·목표 |
| **job** (작업 설정·제외) | `POST /api/tasks/{id}/scope`, `POST /api/tasks/{id}/intercept-rules` | 제외 경로 = scope 제외 또는 intercept 규칙 |
| **scope** (범위 확인·엔드포인트 on/off) | `GET /api/tasks/{id}/scope`, `GET /api/tasks/{id}/coverage`, `GET /api/assets`, `GET /api/exploration/findings/asset-tree` | 토글 저장 = scope/intercept-rules PATCH·toggle |
| **live** (진행 대시보드) | `GET /api/tasks/{id}`, **SSE** `GET /api/exploration/activity/stream`, `GET /api/exploration/findings/stats`, `GET /api/tasks/{id}/coverage` | 실시간 = SSE. control: `POST /api/tasks/{id}/control` |
| **history** (점검 기록) | `GET /api/tasks`, `GET /api/task-archives` | 목록·아카이브 |
| **report** (보고서) | 기존 목록·커버리지 조회 + `GET /api/report/diagnostic?task={id}` | 저장 자료의 종합 HTML 보고서. `audience=internal` 명시 외에는 공유용 DLP. 기존 화면 목록을 일반 HTTP 후보 전체로 간주하지 않음 |
| **AI 수정 프롬프트** | `GET/POST /api/report/diagnostic/remediation`, `GET /api/report/diagnostic/remediation/prompt` (`task` query) | 저장 번들 조회·생성·현재성 검증 후 복사. `RemediationPanel` 연결. 고객 저장소의 수정·테스트 지시문이며 보고서 생성이 코드를 실행/수정하지 않음 |
| **finding** (발견 상세·확정) | `GET /api/exploration/findings/{id}`, `/{id}/retests`, `/{id}/lineage`, `GET /api/traffic/exchange` | 증거·HTTP 교환·재시험(E③) |
| **finding-und** (판단불가) | 위와 동일, 판정=UNDECIDABLE 분기 | 같은 상세 API, 판정값으로 분기 |
| **approval** (승인) | `GET /api/tasks/{id}/request-approvals`, `GET /api/tasks/{id}/request-approvals/{approvalID}`, `POST /api/tasks/{id}/request-approvals/{approvalID}/decision` | 요청별 E② 승인·거절. `RequestApprovalPanel` 연결. 기존 `/api/intercept/pending`·`/{id}/decide`는 별도 ARTEX 도구 승인 게이트 |
| **실행 중 상태·중지** | `GET /api/tasks/{id}/request-approvals`, `POST /api/tasks/{id}/request-approvals/stop` | 조회의 `{approvals, run}`에서 live 감독자 상태·drain·마감을 확인. 일반 task control과 별도이며 이미 송신된 요청은 되돌리지 않음 |
| **저장된 첫 진단 결과** | `GET /api/tasks/{id}/diagnosis-run` | 저장된 준비/마감 상태 조회. 저장 상태가 없으면 404이며 live 감독자 조회나 성공 완료로 대체하지 않음 |
| **settings** (통제 정책) | `GET/PUT /api/settings`, `GET/POST /api/intercept/rules`, `PUT /api/intercept/tool-config`, `GET /api/llm` | DLP·허용목록·모델 |
| **audit** (통제 기록) | `GET /api/intercept/task/{id}`(배선됨), `/api/intercept/history` | ARTEX 승인 게이트의 명시적 허용/차단 결정·승인 요청 기록(전체 송신 원장은 아님). 변조탐지·권한분리·우리 E²는 UI 연동 전 |

## 프로토타입 getter seam — 목업 → 실 API 원스텝 교체점 (11/06)

우리 기여 화면은 **얇은 lib getter 하나**를 데이터 seam으로 쓴다. 레이아웃이 바뀌어도 이 seam·계약은 유지되고,
실 API 연결은 getter에서 응답을 계약 shape로 변환하고 검증한다(화면의 fail-closed 규칙 유지). 표현은 presentational 컴포넌트에만,
상태·집계·판정 로직은 lib에.

| 화면 | 데이터 seam(교체점) | 목업 shape 계약 | 실 API 필요(백 소유) | 진입 경로 |
|---|---|---|---|---|
| access-map | `lib/kg.ts` `getAccessMap(taskId)` | `AccessMap` (kg-policy §4.1·§4.2) | 지도 projection read 라우트 (**Open #6**, 이동찬·최민준) | 보고서 "접근통제 지도" 탭 → "전체 … 열기" |
| e2-flow | `lib/e2flow.ts` `getE2Flow(taskId)` | `E2Flow` (#189 §5·#183 §5 제안) | E² 감사 "표시용 투영" API (이동찬; #183 §5 버전 합의 전제) | 통제 기록(audit) → "전송 통제 흐름 보기" |
| finding | `lib/finding.ts` `getFinding(id)` | `FindingRecord` (verdict-record §2 a~e) | 판정 레코드 읽기 API (verdict-record **Open #4**, 이동찬 조립) | 보고서 발견 목록 |
| finding-und | `lib/finding.ts` `getFindingHeld(id)` | `FindingRecord`(`transmission=held_pre_send` 등) | 위 + E² 전송 상태(송신 전 차단) | 보고서 발견 목록(판단불가) |
| coverage | `lib/coverage.ts` `getCoverage(taskId)` | `CoverageData` | 커버리지 집계 API(검사계획+시도상태) | 보고서 "커버리지" 탭 → "전체 … 열기" |

- **표현/데이터 분리**: `FindingDetail`·`FindingUndDetail`·`CoveragePanel`(presentational, record/data prop) ↔ 화면 wrapper(fetch). 레이아웃 재배치는 presentational만.
- **fail-closed 규칙은 lib에**: `buildCellResolver`(kg)·`decisionClass`(e2flow)·`displayVerdict`/`dataAccessProven`/`displayComparisonRow`(finding)·`heldBeforeSend`/`notTransmitted`/`allEvidenceUnknown`(finding-und)·`coverageIssues`(coverage). getter 교체해도 이 규칙은 그대로 적용.
- 교체 시 체크: 실 응답을 **목업 shape(타입)에 맞춰 매핑**하고, 마스킹(canary 값·raw URL·토큰 금지)·scope 결속(task/run/reset·policy_version)을 서버가 보장. 불일치는 fail-closed(unknown/경고)로 떨어지게 둔다.

## 데이터 모양 변환 (배선 시 주의)
- **심각도**: 프론트를 ARTEX와 동일한 `critical|high|medium|low`(4단)으로 맞춤 → **변환 없음**(확정). `abc2lab-ui/types.ts` Severity, `dto.go:316`.
- **판정(verdict)**: new_web `ok|sus|und`(확정/의심/판단불가)는 **E③ 오라클 산출물**. 베이스 `FindingDTO.status`(워크플로우)·retest `verdict`(reproduced/fixed/inconclusive)와 **다름**. 정본은 [verdict-record.md](verdict-record.md). 배선은 거기 필드에 붙인다.
- **endpoint/셀렉터/계정ID 하드코딩 금지**(절대규칙 1) — 화면 상수(`AREAS`·`PLAN`…)는 전부 API 응답으로 대체.

## 갭 = 우리 기여 (베이스 API에 없거나 확장 필요)
- **E③ 구조화 판정** 1급 노출: finding 상세/목록에 CONFIRMED/SUSPECTED/UNDECIDABLE → verdict-record 계약으로 채움. (레코드 계약: 최민준 / 엔진 API 노출: 이동찬 — #25 §6)
- **IDOR 다계정 증거**(수평/수직, 계정 A/B·setup-success): finding 증거 구조에 포함. `idor/` 검증기 ↔ finding. (최민준)
- **KG 접근통제 지도**(principal×resource 매트릭스): 전용 라우트 없음. `GET /api/tasks/{id}/coverage-graph`가 가장 근접(자산 그래프). KG 매트릭스는 신규 경계 — [kg-policy.md](kg-policy.md). (이경준)
- **canary/표식**: KG가 등록·분류, 비교기 소비 (kg-policy §3.1).
- **E② "실제 전송 통제" 범위**: intercept 경로가 실제로 통제하는 경로부터 검증 후 노출("전수 통제" 주장 금지, [E② 지원 범위](e2-proxy-transfer-scope.md)).

## 열린 항목
- **인증**: LoginScreen이 기존 auth status/login과 Bearer 세션을 재사용한다. 토큰 만료 시 화면을 해제하며 이전 세션의 늦은 응답으로 새 세션을 지우지 않는다. 개발용 자동 로그인·localStorage 보관은 최종 온프레미스 보안 수용 완료를 뜻하지 않는다.
- ~~심각도 4→3 변환~~ → 프론트 4단 유지로 확정(변환 없음).
- **생성/실행 경로 주의**: 기존 `POST /api/tasks`는 `launchTask`로 일반 엔진 탐색을 실제 개시한다. 단순 준비용 레코드 생성이나 첫 통제 진단의 시작 API로 사용하지 않는다. 첫 진단은 위 CLI와 승인된 host 자료·모델 설정·격리 조건을 사용하며 실제 모델·대상·브라우저 수용은 별도 검증한다.
- **판정(verdict) 미노출**: `FindingDTO`에 verdict(ok/sus/und) 필드 없음 → report/finding의 판정 뱃지·분포는 verdict-record 계약(최민준)+엔진 API 노출(이동찬) 연동 후. 지금은 "준비 중" 표기.
- 배선 순서: target → scope → live → report/finding → approval → settings/audit (얇은 조회 경로부터, 상태변경·승인은 뒤).

## 구현과 UI 수용의 구분

API 구현 근거는 `server/server.go`, `server/request_approvals.go`, `server/diagnostic_report.go`이며 화면 연결은 `web/src/abc2lab-ui/lib/request-approvals.ts`, `lib/remediation.ts`와 해당 Panel을 확인합니다. 요청별 승인·저장 프롬프트 연결이 존재해도 모든 보고서/후보/판정 화면의 실연동이나 실모델 수용 완료는 아닙니다. 위 getter 표의 KG·판정 상세·E② 흐름·세분 커버리지는 예시와 실제 응답을 구분해야 합니다.
