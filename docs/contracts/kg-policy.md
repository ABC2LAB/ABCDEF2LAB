# 소유·권한 KG → 판정(E③)·집행(E②) 경계 계약 v0.1 (설계 초안)

작성: 2026-10-27 · 이경준 · 팀 검토용 초안
개정: 2026-10-28 — §3.1 추가(canary/`Trivial` 등록 주체, 비교기 PR #13 정렬)
개정: 2026-10-30 — §4.1 추가(판정 레코드→접근통제 지도 읽기 필드, [verdict-record](./verdict-record.md) Open #4 응답), §6 Open #1 정렬(`policy_version`↔유효창↔정본 reader, verdict-record Open #3)
개정: 2026-11-02 — §3.2 추가(Canary 등록 계약 — 등록 시점·결속 저장·등록 키·원자적 유일성·멱등·역참조, [idor-canary-fixture](./idor-canary-fixture.md) Open #1 응답)
개정: 2026-11-02 — §4.2 추가(지도 실데이터 읽기 어댑터 — 2단계 검증·셀 상태 재정의·scope·단계. Open #6 구체화 + verdict-record Open #4 공동 응답, 최민준·이동찬 리뷰 반영)
개정: 2026-11-03 — §4.1 범위·상태 규칙을 §4.2로 정렬, 회차 메타 일치 검증·정상허용의 동일 자원 조건·레코드 ID 매핑·마스킹 참조 명확화
개정: 2026-11-10 — 구현 현행화: §4 지도 읽기 1단계 구현(서버 projection #330·클라 매핑 #337) 반영, §6 Open #6 1단계 종결·잔여를 Open #7로 분리, 1단계 `resource_ref` type `address`(#334)
상태: **§4 지도 읽기 1단계 구현됨** — 서버 projection `GET /api/tasks/{id}/access-map`([access-map-read](./access-map-read.md), #330) + 클라 매핑 `getAccessMap`·`mapApiAccessMap`(#337). 정본 정책 = fixture 레지스트리([kg-fixture-registry](./kg-fixture-registry.md)), 1단계 carrier `address`/`fullName`([kg-registry-stage1-skeleton](./kg-registry-stage1-skeleton.md)). **미구현**: KG 영속 저장소(§6 Open #3·#4), 지도 Phase 2·3 셀 상태·이력(§6 Open #7), 수직 축(§6 Open #2). [e3-oracle](./e3-oracle.md) Open #2·#3 에 대한 응답.
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

### 3.2 Canary 등록 계약 (idor-canary-fixture Open #1 응답)
> **소유·설계 = KG(이경준).** write 저장·엔드포인트 **구현**은 `db/`·`server/`(이동찬·최민준 협의·리뷰). 여기 "계약"은 의미·불변식이지 구현 소유가 아니다. 이 절은 [canary fixture §2](./idor-canary-fixture.md)의 불변식에 정렬한 KG 등록 계약 제안이며, 물리 저장/API 구현이나 관련 문서 소유자 모두의 최종 합의를 선언하지 않는다.

등록 단위 = `Canary{Field, Value, Trivial}`(§2·§3.1) + 아래 등록 키의 결속 메타 + 대상 인스턴스/reset 완료 근거·생성 규칙 버전. `resource_ref`는 [seam §1](./idor-verifier-seam.md#1-resource_ref-규약--typeid-임시확정--민준경준)의 전체 `type:id`이며 마지막 콜론으로 나눈다. type 안의 콜론을 보존하고 부분 id만으로 자원을 합치지 않는다.

- **(1) 등록 시점** — fixture 가 표식을 **심는 같은 준비 단계**에서 등록한다(심기 없는 등록·등록 없는 심기 = 준비 실패). 검사(`VerifyRead`) **전에** 끝낸다. reset 세대마다 다시 적용하며(fixture §3), 토큰 발급 → provisioning → 심기+등록 순서를 지킨다. KG 는 검사 중 값을 **실시간 생성·동기화하지 않는다**.
- **(2) 결속 저장(발급 아님)** — KG 는 하네스가 실제 reset 성공·헬스 확인 뒤 발급한 세대 토큰과 `(run_id, reset_generation)`·대상 인스턴스/reset 완료 근거의 불변 결속을 **저장만** 한다. 토큰 발급·재결속 권한은 없다(= 하네스/fixture, fixture Open #2). 출처 불명·결속 누락/불일치는 준비 실패이며, 같은 토큰이 다른 회차·세대·인스턴스를 가리키도록 바꾸는 재등록도 거부한다. `run_id` 와 `reset_generation` 은 **별도 축** — 토큰으로 둘을 대체·추론하지 않는다. 공유 근거에는 묶음 참조·세대·완료 여부만 남기고 토큰·표식 **원문 값**은 내보내지 않는다(절대규칙 2·8).
- **(3) 등록 키 · 원자적 유일성** — 등록 키 = `(run_id, reset_generation, 세대 토큰, bundle_ref, owner_ref, resource_ref, Field)`. 한 세대 안에서 `Value` 의 **전역 유일성**을 강제한다. 유일성 확인과 등록은 동시 요청에도 **원자적**이며 last-writer-wins 를 금지한다. 값 → 소유자 역참조가 이 유일성에 근거한다.
- **(4) 멱등 재시도(no-op)** — 같은 키 + 모든 불변 내용(`Value`·`Trivial`·생성 규칙 버전·(2)의 발급 결속)이 동일한 재등록은 **기존 참조를 반환하는 멱등 성공**이다. 새 등록·재생성이 아니다.
- **(5) 충돌·불변 변경 = 준비 실패** — 같은 값을 **다른 등록 키**에 쓰거나, 같은 키의 불변 내용을 바꾸려 하면 **준비 실패**다. 부분 실패는 고쳐 이어가지 않고, 진행 요청 종료·회차 폐기 확인 뒤 리셋부터 **새 묶음**으로 다시 한다(fixture §3).
- **(6) 표식 → 소유 계정 역참조** — `Value → owner_ref` 대응은 **서버가 선택한 같은 run/reset·토큰·인스턴스·bundle_ref 및 요청의 전체 resource_ref에 결속된 정본 등록에서만** 확인한다. 값 유일성은 한 세대 안의 조건이므로 범위 없이 여러 세대를 합쳐 조회하거나 다른 세대의 같은 값으로 소유자를 채우지 않는다. 조회 범위/출처를 확인하지 못하거나 결과가 없거나 여러 소유자와 충돌하면 준비 실패다. owner 응답의 HTTP 200·표식 노출만으로 소유권을 추정하지 않는다. 등록 소유자와 실제 준비 결과가 다르거나 미확인이면 준비 실패다. 외부에 전달하는 역참조 산출물은 **`owner_ref` 라벨만**(계정 원문·토큰 값 금지, 절대규칙 2)이며, 소비 서버는 내부에서 위 범위와 등록 참조를 계속 결속·검증한다. 이 역참조가 §3.1 비교기 `idor.Marker` 의 "A-고유" 근거이자 §3 오라클 `Subjects.OwnerRef` 축의 소스다.

**여기서 안 정하는 것**: 저장 위치·물리 형식(Open #3), 불변 준비 묶음의 물리 저장·부분 실패 검출 API(fixture Open #5), `resource_ref` 의 `type` 명명([seam §5](./idor-verifier-seam.md)·fixture Open #6 — 접근통제 지도와 **공유 축**이라 type 확정 시 함께 재검토. 1단계는 `address`로 확정 — C1~C6 실측, [kg-registry-stage1-skeleton](./kg-registry-stage1-skeleton.md)·#334).

> 교차참조: 이 §3.2 는 canary fixture Open #1(= "KG 등록 계약 = 이경준")의 응답 제안이다. idor-canary-fixture.md 쪽 역링크는 그 문서 소유자(최민준)에게 리뷰로 제안한다 — 직접 편집하지 않음.

## 4. 접근통제 지도 (시각화 — 내 UI 담당)
- **principal × resource 매트릭스** + 셀에 관측 결과(allow/deny/unknown/미검사) 오버레이. **수평**(같은 역할·교차 계정) 먼저, **수직**(역할 레벨차)은 후속.
- 회색 = 미검사(T1.6 커버리지 세분: 계정×기능×검사항목). "미검사"와 "검사했으나 미관찰"을 색으로 구분 — 미실행을 "취약점 없음"으로 보이지 않게(절대규칙 5).
- 데이터 소스 = KG API(신규). 배치는 기존 자산 커버리지 그래프(포함 트리) 위에 **접근 레이어**를 덧대는 방향 검토 — `function/assets` · `function/findings` 페이지와 연계.
- ⚠️ **GT 상관 언급 주의(절대규칙 7 가드)** — 지도 resource 축은 `resource_ref`의 type을 평가 GT 상관과 **공유**한다(§6 Open #1·E² type 명명). 이 문서·UI·그 외 평가 밖 코드에서 평가 GT 상관을 적을 땐 **설명 + 디렉토리(`scripts/eval/`)로만** 쓰고 그 데이터 파일명·모듈 import 리터럴을 쓰지 않는다 — eval 가드(`test_gt_not_referenced_outside_eval`)가 평가 밖의 그 리터럴을 전부 막는다(PR #26에서 걸렸던 지점).

### 4.1 판정 레코드 → 지도 읽기 필드 (verdict-record Open #4 응답)
판정 레코드([verdict-record §2](./verdict-record.md))를 접근통제 지도에 투영할 **향후 읽기 어댑터의 계약 제안**이다. 현재 Workbench 제품은 서버가 승인한 진단 범위를 표시하며, 접근통제 지도 어댑터는 미구현이다. 공용 타입·변환은 `web/src/lib/diagnosis/kg.ts`에 보존했으며 이전 목업 화면은 제거했다. 아래 필드 전체가 현재 UI 타입에 구현됐거나 레코드와 1:1로 대응한다는 뜻은 아니다. 레코드는 "셀 색"을 직접 담지 않고, 서버가 검증한 레코드·커버리지·KG 메타데이터를 어댑터가 조합한다. 응답 본문·canary **값**·자격 원문은 지도에 전달하지 않는다([verdict-record §3 마스킹](./verdict-record.md), 절대규칙 2·8).

**(a) 축·헤더 — 향후 projection이 읽을 근거(조회 범위 규칙은 §4.2 (4)로 대체):**
| 지도 요소 | 근거 | 현재 타입과의 경계 |
|---|---|---|
| 행 principal `ref` (`KgPrincipal.ref`) | `evaluated_ref` | 라벨만. `role` 표시값은 KG 등록에서 보강 |
| 행 검증 status | `evaluated_status` | **관찰 당시** 신원 상태. 현재 `KgPrincipal`에는 status 필드 없음. 현재 실행 자격은 E²가 별도 확인 |
| 열 resource `id`/`label` (`KgResource`) | 전체 `resource_ref`(`type:id`, [seam §1](./idor-verifier-seam.md#1-resource_ref-규약--typeid-임시확정--민준경준)) + KG 표시 라벨 | type만으로 인스턴스를 합치지 않음. id는 reset 세대 안에서만 유효하며 표시 라벨은 별도 보강 |
| 열 소유자 `ownerRef` | `owner_ref` | 라벨만 |
| 판정 축 | 레코드 `axis`(`horizontal`/`vertical`) | v0.1은 horizontal만. 현재 `KgPrincipal.axis`의 H/V는 목업 행 그룹이므로 직접 대입하지 않음 |
| 조회 범위 | 서버의 task 문맥 + envelope `run_id` + `reset_generation`·`policy_version`·판정 축 | §4.2 (4)의 내부 결속 검증을 따른다. 1단계는 [access-map-read](./access-map-read.md)가 고정: 입력은 task ID만, 서버가 현재 실행·세대를 내부 결속하고 응답 최상위에 `run_ref`·`reset_generation`·`policy_version`을 하나씩 싣는다 |
| 모델 헤더 메타 | `model_tag` + 회차 고정 모델 식별자·제공자·샘플링 메타 | 조회 축과 구분하되 envelope↔레코드 일치 검증은 유지(§4.2 (2) A) |
| 셀 근거 선택·이력 | `id`·`case_ref`·`observed_at` + 전체 principal/resource ref | 동일 셀에 여러 case·재검증 레코드가 있을 수 있음 |
| `policyVersion`(지도 헤더) | 선택 범위의 `policy_version` | 모든 표시 셀을 같은 snapshot으로 검증한 경우에만 단일 버전 표시. **1단계 포맷**(kg-fixture-registry §3): `kg-fixture/v1:<snapshot_id>:<digest 64hex>`(≤128바이트, 구분자 `:`). 지도 헤더는 이 문자열을 **변형·절단 없이 그대로** 표기(감사 표기와 1:1) — 이경준 확인(Open #1 (a)) |

**조회 범위·선택을 먼저 검증한다(§4.2 (2)·(4)).** 한 지도는 서버가 선택한 task/run·reset 세대·정책 snapshot·판정 축에 한정한다. 레코드 envelope와 커버리지도 같은 범위여야 한다. `model_tag`는 헤더 메타이며, 조회 축에서 제외해도 envelope↔레코드의 메타 일치 검증을 생략하지 않는다. 다른 범위의 과거 레코드는 현재 셀에 재사용하지 않고 별도 이력으로만 읽는다. 필수 연결 누락·불일치가 있으면 해당 데이터의 로딩을 거부하거나 `unknown`과 부족 사유로 표시하며 색을 추정하지 않는다. `run_id`는 envelope의 회차 키이지 `case_ref`의 대체값이 아니다.

같은 셀의 복수 레코드를 입력 순서나 마지막 항목 우선으로 덮지 않는다. 서버가 선택한 case와 레코드 id를 고정하고 그 근거·관찰시각을 표시한다. 유일한 선택을 검증할 수 없거나 같은 선택 안에서 근거가 상충하면 `unknown`과 상충 사유를 표시한다. 1단계([access-map-read](./access-map-read.md)): 같은 셀 `(principal, owner_ref, resource_ref, axis)`에 레코드가 2건 이상이면 판정이 같아도 `unknown`(`selection_ambiguous`)이며 최신·confirmed 우선 선택은 없다. 이력 조회 파라미터는 열지 않고 재검증 `evidence_revision` 선택은 후속 계약이다(§6 Open #7). 과거 confirmed를 새 세대나 근거 없는 결과로 지우지 않는다.

**(b) 셀 판정 파생 — §4.2 (2)·(3)으로 대체:**
기존 `AccessDecision` 상태표 대신 §4.2의 검증·분기와 상태표를 적용한다. `confirmed`/`suspected`는 별도 색으로 구분하고, `allow-ok`는 그 절의 추가 관찰 조건을 충족할 때만 산출한다. `oracle_value=allowed`만으로 정상 허용·차단을 산출하지 않는다. 현재 UI 타입의 변경은 §4.2 (7)의 후속 작업이다.

**(c) 파생 규칙(절대규칙 5·fail-closed):**
- `access_policy_result=위반_미관찰`은 `oracle_value=allowed`의 저장 표현이며 **실제 허용·차단 관찰이나 취약점 없음의 증명이 아니다**([seam §6.2](./idor-verifier-seam.md#62-전송-결과와-판정의-구분)). 명시적 allow이면 데이터 비교가 불명이어도 allowed가 가능하다. `expected_access=deny`여도 sharing/delegation의 `resolved_allow` 예외로 allowed가 되므로, expected_access만 보고 `deny-ok`로 분기하지 않는다.
- 정상 허용은 §4.2 (3)의 동일 실행·자원에 결속된 추가 관찰 조건을 따른다. 정상 차단의 별도 관찰 계약(근거 ref·실행 연결)은 §6 Open #7이며 §4.2의 Phase 3까지 보류한다(1단계 projection도 `deny-ok`를 산출하지 않음). E²의 송신 전 거부·예산/중지·네트워크 실패를 애플리케이션의 정상 차단으로 바꾸지 않으며 HTTP 상태코드 하나로도 결정하지 않는다. 현재 레코드에는 이 차단 관찰을 독립적으로 입증하는 필드가 없다.
- 커버리지 부재는 시도 없음이 아니다. 레코드와 커버리지가 상충해도 `untested`나 정상색으로 내리지 않는다. "검사했으나 판정/근거 불명"과 "명시적으로 미검사"를 분리한다([판정 레코드 §4](./verdict-record.md#4-평가-읽기-계약-eval-count_basis--레코드)).
- 셀 note = `reason_codes`(정책→신원→데이터 순) + canary 노출 시 `canary_fields`(응답 **필드명만**, 값 아님 — [verdict-record §3](./verdict-record.md)). 레코드의 enum 간 모순·미등록 값도 `unknown`으로 표시하고 사유를 남긴다.
- 정책 유효창은 `policy_valid_from ≤ observed_at < policy_valid_until`로 평가한다. **관찰시각 기준** 만료·`hint_matches_canonical=false`로 UNDECIDABLE 된 셀은 `unknown`과 사유로 표시한다. 현재 시각에 만료됐다는 이유만으로 과거의 유효한 관찰을 덮지 않는다. reset 세대 교체는 새 실행/지도 범위에 이전 세대의 정책·표식·판정을 재사용하지 않는 별도 경계다.
- v0.1 실제 연결은 horizontal 레코드만 판정 축으로 읽는다. vertical과 현재 목업 색/라벨은 후속 미리보기다. 실데이터 연결 상태(11/10): 서버 projection(#330)과 클라 매핑(#337, 확정/의심 별도 색·배지, 미등록 state·`projection_error`→`unknown`, 관측없음≠판단불가)까지 구현됐다. 지도 화면은 UI 재배치(Workbench 통합) 뒤 같은 매핑을 재사용해 다시 붙인다(§6 Open #7). 실제 진단 scope에서의 점등 확인은 아직 없다.

> 교차참조: 이 §4.1은 verdict-record "시각화 읽기 필드 = 이경준" 경계(verdict-record §0·Open #4)의 응답 제안이다. verdict-record.md 쪽 역링크는 그 문서 소유자(최민준)에게 리뷰로 제안한다 — 직접 편집하지 않음.

### 4.2 접근통제 지도 실데이터 읽기 어댑터 (Open #6 구체화 · verdict-record Open #4 공동 응답)
> 최민준(판정 필드)·이동찬(서버 읽기 어댑터) 리뷰 반영. 이 절은 **표시 의미·조합 규칙**을 고정하는 제안이고, 판정 필드 의미는 [verdict-record §2](./verdict-record.md), 서버 projection·저장은 이동찬 구현 소유다. "계약"은 의미이지 구현 완료가 아니다. 1단계 서버 projection은 이 절의 규칙대로 [access-map-read](./access-map-read.md)(#330)로 구현됐고, Phase 2·3은 §6 Open #7에 남는다.

**(1) 위치·책임 분리.** 읽기 어댑터 = **서버 projection(순수 read)**. 판정 발급 경로(세대 검증·정책 재resolve·Evaluate)와 분리한다. 읽기 어댑터는 저장된 판정·관측을 조회·투영만 하고, **불일치 자료는 거부하거나 unknown으로 표시**하며, 과거 판정을 현재 셀에 재사용하지 않고, GET이 누락 셀을 채우려 새 검증을 실행하지 않는다. UI는 조합·판정하지 않고 서버 검증 결과만 렌더(절대규칙 3·4·5). 응답 본문·canary 값·자격 원문은 지도에 전달하지 않는다([verdict-record §3](./verdict-record.md)).

**(2) 2단계 — 검증 → 상태 파생.** 단일 우선순위가 아니라 두 단계다.
- **A. 검증(gate):** 조회 성공 여부와 **존재하는 입력**의 scope·출처·선택을 먼저 검증한다. 판정 레코드가 있으면 envelope와 각 레코드의 `model_tag`·`reset_generation` 일치도 필수로 검사한다([verdict-record §2(a)](./verdict-record.md)). 헤더 메타라도 이 검증을 생략하지 않으며 누락·불일치는 projection-error `unknown`이다. 정상 조회의 레코드 0건은 그 자체로 출처 오류가 아니다. 조회 실패·불일치·출처 미검증·선택 모호·상충이면 `unknown` + **projection-error 사유**(오라클 사유와 별도 네임스페이스)이며 색을 추정하지 않는다.
- **B. 상태 파생:** A를 통과한 입력을 **판정 레코드가 있는 분기 / 없는 분기**로 나눈다. 레코드가 있으면 단일·scope 결속·유효 레코드(+관측)에서 상태를 도출하고 **오라클 원값을 보존**한다. 정상적 `suspected`나 `UNDECIDABLE`을 출처 오류로 지우지 않으며 "IDOR 입증 부족"과 "출처·선택 오류"는 다른 사유의 unknown이다. 레코드가 없으면 같은 범위의 검사계획·시도 상태·applicability 근거로 아래 상태를 파생한다. 실행 실패·E² 사전 거부·시도 후 레코드 누락은 `unknown`이고, **조회 성공과 해당 범위의 모든 관련 근거 부재를 확인한 경우만 `blank`**다. 조회 실패·미확인을 blank로 변환하지 않는다.

**(3) 셀 상태 (§4.1 (b) 확장·재정의):**
| 상태 | 산출 조건 | 가능 시점 |
|---|---|---|
| `confirmed` 위반확정 | `oracle_value=confirmed` ∧ 위반후보 | 토대 후 |
| `suspected` 위반의심 | `oracle_value=suspected`. **확정과 별도 색**(배지만으로 구분 X — 축소 화면 오인 방지, 평가 SUSPECTED 비승격 정렬) | 토대 후 |
| `allow-ok` 정상허용 | `oracle_value=allowed` ∧ `same_resource=yes` ∧ `cross_normal_data=yes` ∧ `identity_check=yes` ∧ 유효 evaluated 신원 ∧ 증거 ref ∧ `observed_at` 유효창 안 ∧ (명시 allow 또는 공유·위임 `resolved_allow` 해소). IDOR 검증기가 B 읽기로 생성해 레코드에 남김 | 토대 후 |
| `unknown` 판정불가 | UNDECIDABLE · 출처/선택 오류 · 근거 상충 · **잘못된 레코드/실행 실패/E² 거부**(송신 0건 ≠ 미시도). 사유 필수 | 토대 후 |
| `deny-ok` 정상차단 | **보류.** 오라클에 "차단 확인" 값 없음(유효 deny라도 data_no_match면 UNDECIDABLE). 데이터 없음만으론 앱 차단 미증명. 앱 인가 차단 **관측 계약** 전까지 산출 않고 `unknown`(위반 미관찰·차단 미증명) | ❌ Phase 3 |
| `untested` 미시도 | **검사계획 + 시도 상태**가 같은 scope·case·행동에 결속되어 명시적 미시도를 확인한 경우만. 현재 coverage(자산 scope·탐색 fact anchor 집계)로는 불가 | ❌ Phase 2 (E① 선행) |
| `na` 적용불가 | KG/검사계획의 명시적 applicability 근거. 레코드 부재·`owner==evaluated`만으론 추론 금지 | 근거 생기면 |
| `blank` 기록없음 | **조회 성공을 확인**했고 같은 범위의 레코드·관측/시도 상태·커버리지·applicability 근거가 **전부 없음**. "판정했으나 불가(unknown)"·"명시적 미시도(untested)"와 구분하는 정직한 빈칸 | 즉시(당분간 다수) |

각 셀은 A 검증을 통과한 단일 선택과 해당 B 분기의 근거로 상태 하나를 산출한다. 복수 레코드의 선택 모호·상충은 A에서 처리하며, 상태의 우선순위로 다른 근거를 덮지 않는다.

**(4) scope.** 조회 쿼리 = 조회 조건이며 **출처 증명이 아니다**. 서버는 쿼리 외에 내부적으로 task·대상 인스턴스·준비 묶음·전체 `resource_ref`·검사 행동을 결속·검증한다. `Scope.RunRef`(개별 실행)는 평가 `run_id`와, `Scope.PolicyRef`(실행 승인)는 KG `policy_version`과 다르다. **`model_tag`는 scope 축이 아니라 헤더 메타**(회차 고정 모델 식별자·제공자·샘플링 메타와 연결). v0.1은 **horizontal·읽기** 한정.

**(5) 근거 필드.** 지도 `record_id`는 판정 레코드의 정본 `id`([verdict-record §2(a)](./verdict-record.md))를 투영한 별칭이며 별도 키를 발급하지 않는다. `record_id`·`observed_at`은 **nullable**(untested/na/blank 및 레코드 없는 unknown엔 없음). untested→검사계획·시도 상태 근거, na→applicability 근거, unknown→사유. 오라클 `reason_codes`와 projection-error 사유는 **별도**. 셀 note = 사유 + `canary_fields`(필드명만, 값 아님 — [verdict-record §3](./verdict-record.md)).

**(6) 발급 주체·세대.** `run_id`: 평가=러너, 실사용=서버([verdict-record §2a](./verdict-record.md)). `reset_generation`: 평가=러너, 실사용=리셋 훅. 서버가 개별 execution을 발급·결속하고 KG는 결속 저장만(임의 발급·재결속 없음, §3.2). 세대 교체 = 새 scope — 이전 세대 정책·표식·판정·커버리지를 새 지도에 재사용 않고 새 세대에서 다시 resolve·검증한다. 과거 confirmed 보존 = 이력 보존이지 현재 색 승계 아님.

**(7) UI 측 (이경준, 일부 선행 가능).** `lib/kg.ts` `getAccessMap`만 목업→실 API 교체. **선행 하드닝(API 전 가능):** 현재 화면의 중복 셀 last-writer-wins·누락 셀→untested 동작을 제거하고 중복은 명시적 로딩 오류 또는 projection-error `unknown`으로 처리하고, 정상 조회에서 관련 근거 0건을 확인한 누락만 `blank`로 처리하며, `suspected`(별도 색)·`blank` 상태 추가, `deny-ok` 보류 표시, `oracle_value` 확정/의심 배지.

**단계(phasing).** Phase 1: confirmed·suspected·allow-ok·unknown·blank. Phase 2: untested(E① 탐색종료·재계획 + 검사계획/시도상태 선행)·na. Phase 3: deny-ok(앱 인가 차단 관측 계약 선행).

**담당(제안·공동확인 전제, "합의·구현 완료" 아님).** 조합·표시 규칙(§4.2)+UI = 이경준 · 판정 필드 의미·case 산출 = 최민준 · 서버 projection read API·관측/판정 저장 = 이동찬.

> 교차참조: 이 §4.2는 [verdict-record Open #4](./verdict-record.md)(시각화 읽는 필드)의 공동 응답이자 §6 Open #6의 구체화다. verdict-record·idor-verifier-seam 쪽 역링크는 소유자 리뷰로 제안한다 — 직접 편집하지 않음.

## 5. 분담·경계
- **UI-only (이경준, 지금 가능):** 접근통제 지도 뷰 설계·프로토타입(KG API 목업으로), 정책·소유 입력/표시 UI, canary 등록 화면.
- **Canary 등록·소비 책임 경계:** 등록·`Trivial` 분류 = KG(이경준 UI + 저장은 협의). 소비 = 비교기(최민준, PR #13 `idor.Marker`), 비교기는 재분류 안 함(§3.1). 저장 위치·물리 형식/API·부분 실패 처리·메타데이터 연결은 §3.2·§6에서 협의한다.
- **백엔드 필요 (협의 대상 — 직접 안 고침):** KG 저장소(principal/resource/policy/canary) · distill 어댑터 · KG API = `db/`·`server/` → 이동찬·최민준 협의 또는 담당 리뷰 PR.
- **집행(E②) 경계:** 접근통제 지도는 E② 에 "**읽기 전용 교차 조회만 허용**" 범위를 넘긴다(조회 한정, 상태변경은 별도 승인 시험환경 — 절대규칙 6). 실행 허용은 KG/LLM 이 아니라 E② 가 정한다(절대규칙 3).

## 6. Open — 팀 정렬 필요
1. **정본(canonical) 정책의 출처·저장 + `policy_version`↔유효창↔정본 reader** (oracle Open #2·verdict-record Open #3 페어, 이동찬↔이경준):
   - **내 제안**: 정본은 **수동 등록 fixture 레지스트리**(관측 추론 아님). 관측 추론 결과는 `ExpectedAccess=unknown` 상한(정본 아님, §3).
   - `policy_version` = 정본 reader가 resolve 시점에 준 **snapshot 버전 문자열**(§3 이미 명시). 레코드의 `policy_valid_from/until`도 같은 reader가 싣고, 오라클이 `observed_at` 기준으로 유효성 판정(만료·`hint_matches_canonical=false` → UNDECIDABLE → 지도 `unknown`, §4.1).
   - **현재 구현과 구분**: `oracle.PolicyAssertion`·`idor.PolicyResolver.ResolvePolicy`는 정책 버전/reset 메타데이터를 반환하지 않는다. 위 snapshot 전달은 서버 관리 실행 묶음/어댑터의 보강 계약([seam §6.1](./idor-verifier-seam.md#61-현재-호출과-새-실행-책임))이며 정본 저장·발급 API는 아직 미정이다.
   - **이동찬 질문**: (a) 정본 reader = fixture 레지스트리인가 server config인가 — `policy_version` 문자열 **포맷·발급 주체**(지도 헤더·감사에 그대로 표기). (b) reset 세대 교체 시 정책 유효창 **무효화 지점**(세대 섞임 → 어댑터가 Evaluate 전 UNDECIDABLE, verdict-record §2d).
   - **(a) 1단계 응답(#193 채택)**: 정본 reader = fixture 레지스트리([kg-fixture-registry](./kg-fixture-registry.md)). `policy_version` = `kg-fixture/v1:<snapshot_id>:<digest 64hex>` — resolver가 레지스트리 내용 digest로 **결정적 발급**(같은 내용=같은 값), ≤128바이트, 구분자 `:`만(§3). **지도 헤더 표기(이경준 확인)**: 이 문자열을 변형·절단 없이 그대로 표시하고 감사 표기와 1:1(§4.1 헤더 행). 포맷/길이 제약 추가 시 알려주면 반영. (b)는 kg-fixture-registry §4가 1단계 답(무효 snapshot → `ok=false` → UNDECIDABLE → 지도 `unknown`).
2. **principal 역할 레벨(수직)의 소스** — 후속 수직 오라클 전에 선설계(권한 레벨 어디서 얻나).
3. **canary 등록 저장 위치** — 자산 `Extra` 재사용 vs 신규 테이블. UX·`Trivial` 분류 주체는 KG 로 확정(§3.1), 등록 **계약 제안**(키·멱등·결속·역참조)은 §3.2 에서 정리하며 **저장 위치·물리 형식/API와 구현 연결**은 협의(최민준↔이동찬).
4. **KG 저장소 테이블명·마이그레이션 담당** — oracle Open #5(판정 레코드 테이블)와 함께 최민준↔이동찬.
5. **신원 status 와 KG ref 의 결합 지점** — 어댑터가 둘을 `SubjectContext` 로 합치는 위치(서버 바인딩 vs 어댑터).

6. **지도 실데이터 읽기 어댑터** — **1단계 종결(11/9).** [access-map-read](./access-map-read.md)(#330)와 클라 매핑(#337)으로 범위 결속(task ID 입력·서버 내부 결속)·셀 키 `(principal, owner_ref, resource_ref, axis)`·복수 레코드 `selection_ambiguous`·세대 교체 무효화·applicability(`true`/`null`, `false`는 Phase 2)·관측없음(`observation_unavailable`)≠판단불가(`undecidable`) 분리가 정해졌다. 최민준 "상태 우선순위" 이견도 복수 레코드=unknown·우선 선택 없음으로 정렬됐다. 남은 범위는 Open #7. (이경준↔최민준↔이동찬)
7. **지도 잔여 — Phase 2·3·이력·UI** (이경준↔최민준↔이동찬):
   - (a) Phase 2 `untested`·`na`: 검사 계획(레지스트리 `policies[]`의 평가 주체)과 시도 기록을 대조한 명시적 미시도, 명시적 적용불가(`applicable=false`) 근거. **이경준 계약 제안 예정.**
   - (b) Phase 3 `deny-ok`: 애플리케이션 정상 차단 관찰의 필드·서버 검증·실행 연결.
   - (c) 재검증 `evidence_revision` 선택·이력 조회.
   - (d) 지도 화면 재부착: UI 재배치 뒤 `getAccessMap`·`mapApiAccessMap`·리졸버를 재사용하고, 실제 진단 scope에서 점등을 확인한다.
