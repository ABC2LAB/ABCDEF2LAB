# 소유·권한 KG → 판정(E③)·집행(E②) 경계 계약 v0.1 (설계 초안)

작성: 2026-10-27 · 이경준 · 팀 검토용 초안
상태: **설계 초안. 저장소·어댑터·API 미구현.** [e3-oracle](./e3-oracle.md) Open #2·#3 에 대한 응답.
연결: [E③ 오라클](./e3-oracle.md) · [평가 프로토콜](../evaluation/evaluation-protocol.md) · [개선 계획 Phase 3](../development-plan.md)

## 0. 범위
이 문서가 고정하는 경계는 셋이다.
- (a) 소유·권한 KG 가 **E③ 오라클에 넘길 정책·주체 근거**(`PolicyAssertion` + 주체 ref).
- (b) **canary(시험자료) 등록** — A 소유 자원에 심는 비민감 표식의 소유·바인딩.
- (c) **접근통제 지도 시각화**의 데이터 소스(내 UI 담당).

**v0.1 = 수평(horizontal) IDOR 조회 전용** — 오라클 v0.1 범위에 정렬한다. 수직 인가는 후속(§6).
비범위: 오라클 판정 로직(최민준), 전송 실행·비교기·신원 status(이동찬 E②), 평가 채점(평가 프로토콜).

## 1. 베이스에 있는 것 / 없는 것 (코드 조사 결과)
**있는 것 (ARTEX 자산 그래프 = `db/assets.go`):**
- `assets` 테이블: `type` ∈ {company, root_domain, subdomain, ip, app, service, endpoint}. 부모-자식은 엣지가 아니라 필드(root_domain/url/port…)에서 **파생**.
- 자산 **커버리지 그래프** — `CoverageGraphNode`/`CoverageGraphEdge`(child→parent **포함** 관계: endpoint→service→host→company), `AssetStore.BuildCoverageGraph(taskID)`, `GET /api/tasks/{id}/coverage-graph`, 작업별 스코프.
- 자산 `Auth []AuthItem`(계정/크리덴셜 후보), `Params []ParamItem`(엔드포인트 파라미터 = 객체 id 가 사는 곳), finding asset tree(`db/finding_assets.go`).

**없는 것 (= 소유·권한 KG 가 새로 얹는 것):**
- first-class **principal**(계정/역할/세션), **resource**(객체 인스턴스), **소유/접근 엣지**, **기대권한 정책**.
- 자산 그래프는 **인프라 토폴로지**지 인가 모델이 아니다. 접근통제(누가 무엇을 읽/쓸 수 있나)는 베이스에 전혀 없다.

**정정:** `agent/coldgraph.go` 는 자산 그래프가 **아니다** — 탐색 그래프의 컨텍스트 압축(cold-digest: intent/fact/finding 접기) 알고리즘이다. 자산 그래프는 `db/assets.go`. (me.md 포인터 수정함)

## 2. KG 데이터 모델 (제안)
값 의미는 오라클 입력 타입(`e3-oracle` §2)에 1:1 대응시킨다 — 어댑터에서 변환 손실이 없게.

- **Principal** — 계정/역할/세션. 자산 `Auth` 승격 + 역할(권한 레벨) 라벨. `ref` = 안정 식별자(별칭/이름만, **계정 원문·비밀번호·토큰 값 금지** — 절대규칙 2; 값은 null/[REDACTED]).
- **Resource** — `(endpoint, object-id, owner-principal)` 바인딩. object-id 는 `Params` 의 id류 파라미터에서 관측. 소유자 = 등록 principal.
- **Ownership edge** — principal —`owns`→ resource.
- **PolicyRecord**(resource별 기대 접근):
  - `ExpectedAccess` ∈ {allow, deny, **unknown**}
  - `Sharing`, `Delegation` ∈ {resolved_none, resolved_allow, **unknown**}
  - `HasRegisteredSource` bool · `ValidFrom`/`ValidUntil` · `HintMatchesCanonical` bool · `PolicyVersion`
- **Canary** — resource 에 등록된 **비민감 표식**(필드·값). A 소유 자원에 심어 B 교차응답에서 매칭 → 실데이터 미조회로 접근 입증(절대규칙 8 · DLP). KG 는 canary↔resource↔owner 바인딩만 보유.

## 3. KG → E③ 오라클 생산자 계약 (Open #3 응답)
오라클은 순수함수고 KG 는 그 입력(`CaseInput`)의 **정책·주체 축**을 distill 한다. case 하나 = 동일 작업·엔드포인트·reset 세대·정책버전(오라클 §5).

**산출 → 오라클 필드 매핑:**
| KG 산출 | oracle 필드 |
|---|---|
| PolicyRecord.ExpectedAccess | `Policy.ExpectedAccess` |
| PolicyRecord.Sharing / Delegation | `Policy.Sharing` / `Policy.Delegation` |
| PolicyRecord.HasRegisteredSource | `Policy.HasRegisteredSource` |
| PolicyRecord.ValidFrom / ValidUntil | `Policy.ValidFrom` / `ValidUntil` |
| PolicyRecord.HintMatchesCanonical | `Policy.HintMatchesCanonical` |
| owner principal.ref / 평가 principal.ref | `Subjects.OwnerRef` / `EvaluatedRef` |
| canary 등록 사실 | (전제) check_data 의 `NontrivialDataMatch` 입력 자격 |

**핵심 규칙 — "조회 안 함 ≠ 없음"(fail-closed):**
- sharing/delegation 을 **실제로 해소하지 못했으면 `unknown`** 을 준다. 절대 `resolved_none` 으로 메우지 않는다(메우면 오라클이 CONFIRMED 로 오판).
- 등록 정책이 없으면 `HasRegisteredSource=false` → 오라클 `POLICY_MISSING` → UNDECIDABLE(부재는 SUSPECTED 아님).
- `ExpectedAccess` 는 **정본(canonical) 정책에서만** allow/deny. LLM 추정·관측 추론은 `unknown`.
- 주체 ref 는 검증 못 했으면 `""`(nil 과 동일 미검증 취급). status(verified/expired/mismatch)는 **신원(이동찬 E②)**과 조합 — KG 는 ref·소유만, "지금 검증됨"은 신원이 채운다.
- 정책 유효성은 **근거 관찰시각 기준** — KG 는 ValidFrom/Until·PolicyVersion·reset 세대 메타를 함께 실어 오라클 `observed_at` 평가와 통제변수를 보존한다(오라클 §5).

## 4. 접근통제 지도 (시각화 — 내 UI 담당)
- **principal × resource 매트릭스** + 셀에 관측 결과(allow/deny/unknown/미검사) 오버레이. **수평**(같은 역할·교차 계정) 먼저, **수직**(역할 레벨차)은 후속.
- 회색 = 미검사(T1.6 커버리지 세분: 계정×기능×검사항목). "미검사"와 "검사했으나 미관찰"을 색으로 구분 — 미실행을 "취약점 없음"으로 보이지 않게(절대규칙 5).
- 데이터 소스 = KG API(신규). 배치는 기존 자산 커버리지 그래프(포함 트리) 위에 **접근 레이어**를 덧대는 방향 검토 — `function/assets` · `function/findings` 페이지와 연계.

## 5. 분담·경계
- **UI-only (이경준, 지금 가능):** 접근통제 지도 뷰 설계·프로토타입(KG API 목업으로), 정책·소유 입력/표시 UI, canary 등록 화면.
- **백엔드 필요 (협의 대상 — 직접 안 고침):** KG 저장소(principal/resource/policy/canary) · distill 어댑터 · KG API = `db/`·`server/` → 이동찬·최민준 협의 또는 담당 리뷰 PR.
- **집행(E②) 경계:** 접근통제 지도는 E② 에 "**읽기 전용 교차 조회만 허용**" 범위를 넘긴다(조회 한정, 상태변경은 별도 승인 시험환경 — 절대규칙 6). 실행 허용은 KG/LLM 이 아니라 E② 가 정한다(절대규칙 3).

## 6. Open — 팀 정렬 필요
1. **정본(canonical) 정책의 출처·저장** (oracle Open #2 과 페어, 이동찬↔이경준): 수동 등록 fixture 인가, 관측 추론인가. 추론이면 그 결과는 `ExpectedAccess=unknown` 상한(정본 아님).
2. **principal 역할 레벨(수직)의 소스** — 후속 수직 오라클 전에 선설계(권한 레벨 어디서 얻나).
3. **canary 등록 저장 위치** — 자산 `Extra` 재사용 vs 신규 테이블. UX 는 내가, 저장은 협의.
4. **KG 저장소 테이블명·마이그레이션 담당** — oracle Open #5(판정 레코드 테이블)와 함께 최민준↔이동찬.
5. **신원 status 와 KG ref 의 결합 지점** — 어댑터가 둘을 `SubjectContext` 로 합치는 위치(서버 바인딩 vs 어댑터).
