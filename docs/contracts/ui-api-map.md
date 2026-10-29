# UI ↔ 엔진 API 매핑 (new_web abc2lab-ui → ARTEX Go server)

> 경계 계약. new_web 시안(12화면)을 Go 서버(`/api/*`, dev rewrite→`:8787`)에 배선하기 위한 화면별 엔드포인트 지도.
> 라우트는 `server/*.go`(246개) 기준, DTO는 `server/dto.go`·`web/src/lib/types.ts`(격리됨, 계약 레퍼런스).
> 작성: 이경준. 판정/증거 경계는 [verdict-record.md](verdict-record.md)·[e3-oracle.md](e3-oracle.md), KG 경계는 [kg-policy.md](kg-policy.md).

## ARTEX 도메인 모델 (전제)
- **task** = 대상+범위 1회 진단 단위. 생성→control(start/stop)→exploration→findings.
- **exploration** = 엔진 탐색: activity(스트림)·graph·frontier·intents·nodes.
- **findings** = 발견. 워크플로우 `status`와 **E③ 구조화 판정**(CONFIRMED/SUSPECTED/UNDECIDABLE)은 별개 — 후자는 verdict-record 계약.
- **intercept** = E② 전송 통제 경계. `pending`(승인 대기)·`decide`(허용/차단)·`history`(전송 기록)·`rules`(통제 정책).
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
| **approval** (승인) | `GET /api/intercept/pending`, `POST /api/intercept/pending/{id}/decide` | **E² 경계** — 협의: 이동찬 |
| **settings** (통제 정책) | `GET/PUT /api/settings`, `GET/POST /api/intercept/rules`, `PUT /api/intercept/tool-config`, `GET /api/llm` | DLP·허용목록·모델 |
| **audit** (통제 기록) | `GET /api/intercept/task/{id}`(배선됨), `/api/intercept/history` | ARTEX 승인 게이트 차단/승인대기 기록(허용 원장 아님). 변조탐지·권한분리·우리 E²는 연동 전 |

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
