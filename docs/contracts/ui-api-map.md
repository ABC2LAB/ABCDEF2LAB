# UI ↔ 엔진 API 매핑 (new_web abc2lab-ui → ARTEX Go server)

> 경계 계약. new_web 시안(12화면)을 Go 서버(`/api/*`, dev rewrite→`:8787`)에 배선하기 위한 화면별 엔드포인트 지도.
> 라우트는 `server/*.go`(246개) 기준, DTO는 `server/dto.go`·`web/src/lib/types.ts`(격리됨, 계약 레퍼런스).
> 작성: 이경준. 판정/증거 경계는 [verdict-record.md](verdict-record.md)·[e3-oracle.md](e3-oracle.md), KG 경계는 [kg-policy.md](kg-policy.md).

## ARTEX 도메인 모델 (전제)
- **task** = 대상+범위 1회 진단 단위. 생성→control(start/stop)→exploration→findings.
- **exploration** = 엔진 탐색: activity(스트림)·graph·frontier·intents·nodes.
- **findings** = 발견. 워크플로우 `status`와 **E③ 구조화 판정**(CONFIRMED/SUSPECTED/UNDECIDABLE)은 별개 — 후자는 verdict-record 계약.
- **intercept** = ARTEX 도구 호출 승인 게이트. `pending`(승인 대기)·`decide`(허용/차단)·`history`(명시적 결정/승인 기록)·`rules`(allow/deny/ask 규칙). 실제 네트워크 전송 통제 E②의 집행·송신 증명과는 별개다.
- **audit** = 감사 로그.

## 화면별 매핑

| 화면 | 주 엔드포인트 | 비고 |
|---|---|---|
| **entry** (주소 입력) | `POST /api/tasks` (대상=입력 URL) | 소유확인(DNS TXT)은 현재 UI 전용, 백엔드 라우트 없음 → 폐쇄망 정책상 재검토 |
| **target** (방식/요약) | `GET /api/tasks/{id}`, `POST /api/tasks/{id}/goals` | 대상 요약·목표 |
| **job** (작업 설정·제외) | `POST /api/tasks/{id}/scope`, `POST /api/tasks/{id}/intercept-rules` | 제외 경로 = scope 제외 또는 intercept 규칙 |
| **scope** (범위 확인·엔드포인트 on/off) | `GET /api/tasks/{id}/scope`, `GET /api/tasks/{id}/coverage`, `GET /api/assets`, `GET /api/exploration/findings/asset-tree` | 토글 저장 = scope/intercept-rules PATCH·toggle |
| **live** (진행 대시보드) | `GET /api/tasks/{id}`, **SSE** `GET /api/exploration/activity/stream`, `GET /api/exploration/findings/stats`, `GET /api/tasks/{id}/coverage` | 실시간 = SSE. control: `POST /api/tasks/{id}/control` |
| **history** (점검 기록) | `GET /api/tasks`, `GET /api/task-archives` | 목록·아카이브 |
| **report** (보고서) | `GET /api/tasks/{id}`, `GET /api/exploration/findings`, `GET /api/tasks/{id}/coverage` | 발견 목록·커버리지·위험도 4단 타일 (※ `/api/report`·`/api/stats`는 코드 미사용) |
| **finding** (발견 상세·확정) | `GET /api/exploration/findings/{id}`, `/{id}/retests`, `/{id}/lineage`, `GET /api/traffic/exchange` | 증거·HTTP 교환·재시험(E③) |
| **finding-und** (판단불가) | 위와 동일, 판정=UNDECIDABLE 분기 | 같은 상세 API, 판정값으로 분기 |
| **approval** (승인) | `GET /api/intercept/pending`, `POST /api/intercept/pending/{id}/decide` | ARTEX 도구 승인 게이트. 실제 송신 E②와의 연결 — 협의: 이동찬 |
| **settings** (통제 정책) | `GET/PUT /api/settings`, `GET/POST /api/intercept/rules`, `PUT /api/intercept/tool-config`, `GET /api/llm` | DLP·허용목록·모델 |
| **audit** (통제 기록) | `GET /api/intercept/task/{id}`(배선됨), `/api/intercept/history` | ARTEX 승인 게이트의 명시적 허용/차단 결정·승인 요청 기록(전체 송신 원장은 아님). 변조탐지·권한분리·우리 E²는 UI 연동 전 |

## 프로토타입 getter seam — 목업 → 실 API 원스텝 교체점 (11/06)

우리 기여 화면은 **얇은 lib getter 하나**를 데이터 seam으로 쓴다. 레이아웃이 바뀌어도 이 seam·계약은 유지되고,
실 API 연동 = **그 getter만 fetch로 교체**(화면·fail-closed 규칙 안 건드림). 표현은 presentational 컴포넌트에만,
상태·집계·판정 로직은 lib에.

| 화면 | 데이터 seam(교체점) | 목업 shape 계약 | 실 API 필요(백 소유) | 진입 경로 |
|---|---|---|---|---|
| access-map | `lib/kg.ts` `getAccessMap(taskId)` | `AccessMap` (kg-policy §4.1·§4.2) | 지도 projection read 라우트 (**Open #6**, 이동찬·최민준) | 보고서 "접근통제 지도" 탭 → "전체 … 열기" |
| e2-flow | `lib/e2flow.ts` `getE2Flow(taskId)` | `E2Flow` (#189 §5·#183 §5 제안) | E² 감사 "표시용 투영" API (이동찬; #183 §5 버전 합의 전제) | 통제 기록(audit) → "전송 통제 흐름 보기" |
| finding | `lib/finding.ts` `getFinding(id)` | `FindingRecord` (verdict-record §2 a~e) | 판정 레코드 읽기 API (verdict-record **Open #4**, 이동찬 조립) | 보고서 발견 목록 |
| finding-und | `lib/finding.ts` `getFindingHeld(id)` | `FindingRecord`(`transmission=held_pre_send` 등) | 위 + E² 전송 상태(송신 전 차단) | 보고서 발견 목록(판단불가) |
| coverage | `lib/coverage.ts` `getCoverage(taskId)` | `CoverageData` | 커버리지 집계 API(검사계획+시도상태) | 보고서 "커버리지" 탭 → "전체 … 열기" |

- **표현/데이터 분리**: `FindingDetail`·`FindingUndDetail`·`CoveragePanel`(presentational, record/data prop) ↔ 화면 wrapper(fetch). 레이아웃 재배치는 presentational만.
- **fail-closed 규칙은 lib에**: `buildCellResolver`(kg)·`decisionClass`/`displayVerdict`/`dataAccessProven`/`displayComparisonRow`(finding)·`heldBeforeSend`/`notTransmitted`/`allEvidenceUnknown`(finding-und)·`coverageIssues`(coverage). getter 교체해도 이 규칙은 그대로 적용.
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
- **E② "실제 전송 통제" 범위**: intercept 경로가 실제로 통제하는 경로부터 검증 후 노출("전수 통제" 주장 금지, CLAUDE.md E²).

## 열린 항목
- **인증**: new_web엔 로그인 화면 없음(entry→target 직행). 옛 가드 `proxy.ts`는 `web/_parked/`로 격리(현재 open). 새 디자인용 auth 재설계 필요 — `GET /api/auth/status`·`POST /api/auth/login` 재사용 여부 포함.
- ~~심각도 4→3 변환~~ → 프론트 4단 유지로 확정(변환 없음).
- **생성/실행 경로 주의**: `POST /api/tasks`는 레코드 생성이 아니라 `launchTask`→`engine.Run`으로 **엔진 탐색을 실제 개시**함. entry/scope "시작" 버튼은 실행 트리거 → Ollama·Juice Shop 평가환경 + E²(이동찬) 검증 선행. 지금은 읽기 화면부터 배선.
- **판정(verdict) 미노출**: `FindingDTO`에 verdict(ok/sus/und) 필드 없음 → report/finding의 판정 뱃지·분포는 verdict-record 계약(최민준)+엔진 API 노출(이동찬) 연동 후. 지금은 "준비 중" 표기.
- 배선 순서: target → scope → live → report/finding → approval → settings/audit (얇은 조회 경로부터, 상태변경·승인은 뒤).
