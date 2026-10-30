# 소유·권한 KG → 판정(E③)·집행(E②) 경계 계약 v0.1 (설계 초안)

작성: 2026-10-27 · 이경준 · 팀 검토용 초안
개정: 2026-10-28 — §3.1 추가(canary/`Trivial` 등록 주체, 비교기 PR #13 정렬)
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
- **Canary** — resource 에 등록된 **비민감 표식**(`Field`·`Value`·`Trivial`). A 소유 자원에 심어 B 교차응답에서 매칭 → 실데이터 미조회로 접근 입증(절대규칙 8 · DLP). KG 는 canary↔resource↔owner 바인딩 + `Trivial` 분류를 보유(→ §3.1).

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
| canary 등록 사실 + `Trivial` 분류 | (전제) check_data 의 `NontrivialDataMatch` 입력 자격 (→ §3.1) |

**핵심 규칙 — "조회 안 함 ≠ 없음"(fail-closed):**
- sharing/delegation 을 **실제로 해소하지 못했으면 `unknown`** 을 준다. 절대 `resolved_none` 으로 메우지 않는다(메우면 오라클이 CONFIRMED 로 오판).
- 등록 정책이 없으면 `HasRegisteredSource=false` → 오라클 `POLICY_MISSING` → UNDECIDABLE(부재는 SUSPECTED 아님).
- `ExpectedAccess` 는 **정본(canonical) 정책에서만** allow/deny. LLM 추정·관측 추론은 `unknown`.
- 주체 ref 는 검증 못 했으면 `""`(nil 과 동일 미검증 취급). status(verified/expired/mismatch)는 **신원(이동찬 E②)**과 조합 — KG 는 ref·소유만, "지금 검증됨"은 신원이 채운다.
- 정책 유효성은 **근거 관찰시각 기준** — KG 는 ValidFrom/Until·PolicyVersion·reset 세대 메타를 함께 실어 오라클 `observed_at` 평가와 통제변수를 보존한다(오라클 §5).

### 3.1 Canary 등록·`Trivial` 분류 주체 (= KG. 비교기는 소비만)
**등록·분류는 KG 가 한다.** 비교기(`idor.Compare`, 최민준 PR #13)는 넘겨받은 표식을 **소비만** 하고 재판단하지 않는다 — 판정·집행과 "무엇이 데이터냐"를 같은 손이 쥐지 않게(절대규칙 3·4, 오라클 순수성).

KG `Canary` → 비교기 `idor.Marker` 매핑(1:1):

| KG Canary | idor.Marker | 의미 |
|---|---|---|
| Field | `Field` | 표식이 사는 응답 필드명 |
| Value | `Value` | 심은 비민감 값(민감 원문·키 금지 — 절대규칙 2·8) |
| Trivial | `Trivial` | **순수 식별자 여부**(id/owner_id = true → 비자명 매칭에서 제외) |

- **`Trivial` 이 왜 KG 몫인가** — "이 필드가 식별자냐 A-고유 데이터냐"는 스키마·소유 지식이다. KG 는 `Params` 의 id류 파라미터·소유 바인딩에서 그걸 안다. 비교기는 응답 바이트만 봐서는 구분 못 하므로 분류를 실어 보낸다.
- **fail-closed 기본값** — A-고유 데이터인지 **확신 못 하면 `Trivial=true`**(제외). 식별자를 데이터로 오분류하면(`Trivial=false`) id 반사가 매칭을 통과해 false CONFIRMED 로 샐 수 있다. 확신 없을 때 제외하면 비자명 표식이 0개 → 오라클 `NontrivialDataMatch=Unknown` → UNDECIDABLE 로 떨어진다. 거짓 확정보다 미확정이 안전(절대규칙 5).
- **canary-substring 모델이 안 맞는 경우** — 구조적 컬렉션 누수(예: basketAccess 가 공유 카탈로그 필드만 담은 A-연관 목록 반환)는 `Value` substring 으로 A-고유를 못 가린다. 그 엔드포인트는 KG 가 소유 바인딩만 주고 `NontrivialDataMatch` 는 capture 가 직접 계산한다(A-연관 목록이 교차엔 있고 B 자기 것엔 없음). 이 분기는 compare↔capture 경계(e3-oracle §8-④, 최민준·이동찬) — KG 는 소유·표식까지만.

## 4. 접근통제 지도 (시각화 — 내 UI 담당)
- **principal × resource 매트릭스** + 셀에 관측 결과(allow/deny/unknown/미검사) 오버레이. **수평**(같은 역할·교차 계정) 먼저, **수직**(역할 레벨차)은 후속.
- 회색 = 미검사(T1.6 커버리지 세분: 계정×기능×검사항목). "미검사"와 "검사했으나 미관찰"을 색으로 구분 — 미실행을 "취약점 없음"으로 보이지 않게(절대규칙 5).
- 데이터 소스 = KG API(신규). 배치는 기존 자산 커버리지 그래프(포함 트리) 위에 **접근 레이어**를 덧대는 방향 검토 — `function/assets` · `function/findings` 페이지와 연계.
- ⚠️ **GT 상관 언급 주의(절대규칙 7 가드)** — 지도 resource 축은 `resource_ref`의 type을 평가 GT 상관과 **공유**한다(§6 Open #1·E² type 명명). 이 문서·UI·그 외 평가 밖 코드에서 평가 GT 상관을 적을 땐 **설명 + 디렉토리(`scripts/eval/`)로만** 쓰고 그 데이터 파일명·모듈 import 리터럴을 쓰지 않는다 — eval 가드(`test_gt_not_referenced_outside_eval`)가 평가 밖의 그 리터럴을 전부 막는다(PR #26에서 걸렸던 지점).

## 5. 분담·경계
- **UI-only (이경준, 지금 가능):** 접근통제 지도 뷰 설계·프로토타입(KG API 목업으로), 정책·소유 입력/표시 UI, canary 등록 화면.
- **Canary 레지스트리 경계 (확정):** 등록·`Trivial` 분류 = KG(이경준 UI + 저장은 협의). 소비 = 비교기(최민준, PR #13 `idor.Marker`), 비교기는 재분류 안 함(§3.1). 저장 위치만 Open #3.
- **백엔드 필요 (협의 대상 — 직접 안 고침):** KG 저장소(principal/resource/policy/canary) · distill 어댑터 · KG API = `db/`·`server/` → 이동찬·최민준 협의 또는 담당 리뷰 PR.
- **집행(E②) 경계:** 접근통제 지도는 E② 에 "**읽기 전용 교차 조회만 허용**" 범위를 넘긴다(조회 한정, 상태변경은 별도 승인 시험환경 — 절대규칙 6). 실행 허용은 KG/LLM 이 아니라 E② 가 정한다(절대규칙 3).

## 6. Open — 팀 정렬 필요
1. **정본(canonical) 정책의 출처·저장** (oracle Open #2 과 페어, 이동찬↔이경준): 수동 등록 fixture 인가, 관측 추론인가. 추론이면 그 결과는 `ExpectedAccess=unknown` 상한(정본 아님).
2. **principal 역할 레벨(수직)의 소스** — 후속 수직 오라클 전에 선설계(권한 레벨 어디서 얻나).
3. **canary 등록 저장 위치** — 자산 `Extra` 재사용 vs 신규 테이블. UX·`Trivial` 분류 주체는 KG 로 확정(§3.1), **저장 위치만** 협의(최민준↔이동찬).
4. **KG 저장소 테이블명·마이그레이션 담당** — oracle Open #5(판정 레코드 테이블)와 함께 최민준↔이동찬.
5. **신원 status 와 KG ref 의 결합 지점** — 어댑터가 둘을 `SubjectContext` 로 합치는 위치(서버 바인딩 vs 어댑터).
