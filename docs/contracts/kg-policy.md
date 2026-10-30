# 소유·권한 KG → 판정(E③)·집행(E②) 경계 계약 v0.1 (설계 초안)

작성: 2026-10-27 · 이경준 · 팀 검토용 초안
개정: 2026-10-28 — §3.1 추가(canary/`Trivial` 등록 주체, 비교기 PR #13 정렬)
개정: 2026-10-30 — §4.1 추가(판정 레코드→접근통제 지도 읽기 필드, [verdict-record](./verdict-record.md) Open #4 응답), §6 Open #1 정렬(`policy_version`↔유효창↔정본 reader, verdict-record Open #3)
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

### 4.1 판정 레코드 → 지도 읽기 필드 (verdict-record Open #4 응답)
판정 레코드([verdict-record §2](./verdict-record.md))를 접근통제 지도에 투영할 **향후 읽기 어댑터의 계약 제안**이다. 현재 `web/src/abc2lab-ui/lib/kg.ts`·`screens/AccessMapScreen.tsx`는 목업이며 판정 레코드를 읽지 않는다. 아래 필드 전체가 현재 UI 타입에 구현됐거나 레코드와 1:1로 대응한다는 뜻은 아니다. 레코드는 "셀 색"을 직접 담지 않고, 서버가 검증한 레코드·커버리지·KG 메타데이터를 어댑터가 조합한다. 응답 본문·canary **값**·자격 원문은 지도에 전달하지 않는다(§3 마스킹, 절대규칙 2·8).

**(a) 축·헤더 — 향후 projection이 읽을 근거:**
| 지도 요소 | 근거 | 현재 타입과의 경계 |
|---|---|---|
| 행 principal `ref` (`KgPrincipal.ref`) | `evaluated_ref` | 라벨만. `role` 표시값은 KG 등록에서 보강 |
| 행 검증 status | `evaluated_status` | **관찰 당시** 신원 상태. 현재 `KgPrincipal`에는 status 필드 없음. 현재 실행 자격은 E²가 별도 확인 |
| 열 resource `id`/`label` (`KgResource`) | 전체 `resource_ref`(`type:id`, [seam §1](./idor-verifier-seam.md#1-resource_ref-규약--typeid-임시확정--민준경준)) + KG 표시 라벨 | type만으로 인스턴스를 합치지 않음. id는 reset 세대 안에서만 유효하며 표시 라벨은 별도 보강 |
| 열 소유자 `ownerRef` | `owner_ref` | 라벨만 |
| 판정 축 | 레코드 `axis`(`horizontal`/`vertical`) | v0.1은 horizontal만. 현재 `KgPrincipal.axis`의 H/V는 목업 행 그룹이므로 직접 대입하지 않음 |
| 조회 범위 | 서버의 task 문맥 + envelope `run_id` + `model_tag`·`reset_generation`·`policy_version` | 현재 `AccessMap`에는 회차·세대 메타데이터 없음. 실제 API/타입 확장은 Open #6 |
| 셀 근거 선택·이력 | `id`·`case_ref`·`observed_at` + 전체 principal/resource ref | 동일 셀에 여러 case·재검증 레코드가 있을 수 있음 |
| `policyVersion`(지도 헤더) | 선택 범위의 `policy_version` | 모든 표시 셀을 같은 snapshot으로 검증한 경우에만 단일 버전 표시 |

**조회 범위·선택을 먼저 검증한다.** 한 지도는 서버가 선택한 task/run·model·reset 세대·정책 snapshot·판정 축에 한정한다. 레코드 envelope와 커버리지도 같은 범위여야 한다. 다른 범위의 과거 레코드는 현재 셀에 재사용하지 않고 별도 이력으로만 읽는다. 필수 연결 누락·불일치가 있으면 해당 데이터의 로딩을 거부하거나 `unknown`과 부족 사유로 표시하며 색을 추정하지 않는다. `run_id`는 envelope의 회차 키이지 `case_ref`의 대체값이 아니다.

같은 셀의 복수 레코드를 입력 순서나 마지막 항목 우선으로 덮지 않는다. 서버가 선택한 case와 레코드 id를 고정하고 그 근거·관찰시각을 표시한다. 유일한 선택을 검증할 수 없거나 같은 선택 안에서 근거가 상충하면 `unknown`과 상충 사유를 표시한다. 선택·이력 API의 물리 형식은 Open #6에서 정하며, 과거 confirmed를 새 세대나 근거 없는 결과로 지우지 않는다.

**(b) 셀 판정 파생 — `AccessDecision`과 근거의 경계:**
| 지도 셀(`AccessDecision`) | 파생 조건 | 제한 |
|---|---|---|
| `vuln`(위반 후보) | 서버가 검증한 같은 범위의 `access_policy_result=위반_후보` **및** `oracle_value`∈{confirmed, suspected} | 색은 공유할 수 있으나 본문/배지에 **확정·의심을 항상 구분**. note만으로 구분하지 않음 |
| `unknown`(판정 불가) | `판정_불가`/undecidable, 근거 누락·불일치·상충, 또는 현재 레코드만으로 실제 접근 결과를 정할 수 없음 | `oracle_value=allowed`만 있으면 "정책상 허용, 접근 관측 미확인" 사유로 여기에 둠 |
| `allow-ok`(정상 허용) | **현재 판정 레코드 단독으로 산출하지 않음** | 동일 실행의 검증된 정상 접근 관찰과 유효한 허용 정책을 결합하는 후속 계약이 필요. 확정 전에는 `unknown` |
| `deny-ok`(정상 차단) | **현재 판정 레코드 단독으로 산출하지 않음** | 유효한 deny·공유/위임 예외 해소와 실제 애플리케이션 인가 차단 관찰의 후속 계약이 필요. 확정 전에는 `unknown` |
| `untested`(미검사) | 유효한 판정 레코드가 없고 **동일 회차·case·세대의 커버리지가 명시적으로 시도 없음을 확인** | 레코드 없음만으로는 결정하지 않음. 커버리지 불명 또는 시도됐으나 레코드 누락이면 `unknown` |
| `na` | 신뢰할 수 있는 KG/검사 계획이 그 조합을 명시적으로 적용 불가로 분류 | 레코드 부재나 `owner_ref=evaluated_ref`만으로 추론하지 않음. 소유자 자신의 정상 접근도 정책·관찰 근거로 별도 표현 가능. 유효한 관찰과 적용 불가 근거가 상충하면 `unknown` |

**(c) 파생 규칙(절대규칙 5·fail-closed):**
- `access_policy_result=위반_미관찰`은 `oracle_value=allowed`의 저장 표현이며 **실제 허용·차단 관찰이나 취약점 없음의 증명이 아니다**([seam §6.2](./idor-verifier-seam.md#62-전송-결과와-판정의-구분)). 명시적 allow이면 데이터 비교가 불명이어도 allowed가 가능하다. `expected_access=deny`여도 sharing/delegation의 `resolved_allow` 예외로 allowed가 되므로, expected_access만 보고 `deny-ok`로 분기하지 않는다.
- 정상 허용·차단의 별도 관찰 계약(근거 ref·실행 연결·판정 우선순위)은 Open #6이다. E²의 송신 전 거부·예산/중지·네트워크 실패를 애플리케이션의 정상 차단으로 바꾸지 않으며 HTTP 상태코드 하나로도 결정하지 않는다. 현재 레코드에는 이 차단 관찰을 독립적으로 입증하는 필드가 없다.
- 커버리지 부재는 시도 없음이 아니다. 레코드와 커버리지가 상충해도 `untested`나 정상색으로 내리지 않는다. "검사했으나 판정/근거 불명"과 "명시적으로 미검사"를 분리한다([판정 레코드 §4](./verdict-record.md#4-평가-읽기-계약-eval-count_basis--레코드)).
- 셀 note = `reason_codes`(정책→신원→데이터 순) + canary 노출 시 `canary_fields`(응답 **필드명만**, 값 아님 — §3). 레코드의 enum 간 모순·미등록 값도 `unknown`으로 표시하고 사유를 남긴다.
- 정책 유효창은 `policy_valid_from ≤ observed_at < policy_valid_until`로 평가한다. **관찰시각 기준** 만료·`hint_matches_canonical=false`로 UNDECIDABLE 된 셀은 `unknown`과 사유로 표시한다. 현재 시각에 만료됐다는 이유만으로 과거의 유효한 관찰을 덮지 않는다. reset 세대 교체는 새 실행/지도 범위에 이전 세대의 정책·표식·판정을 재사용하지 않는 별도 경계다.
- v0.1 실제 연결은 horizontal 레코드만 판정 축으로 읽는다. vertical과 현재 목업 색/라벨은 후속 미리보기다. 현재 UI의 `vuln` 공통 "취약" 라벨은 확정/의심 구분 및 이 절의 조회 범위·상태 확장을 구현한 뒤 실데이터에 연결한다. 이 PR은 문서만 바꾸며 연결 완료를 뜻하지 않는다.

> 교차참조: 이 §4.1은 verdict-record "시각화 읽기 필드 = 이경준" 경계(verdict-record §0·Open #4)의 응답 제안이다. verdict-record.md 쪽 역링크는 그 문서 소유자(최민준)에게 리뷰로 제안한다 — 직접 편집하지 않음.

## 5. 분담·경계
- **UI-only (이경준, 지금 가능):** 접근통제 지도 뷰 설계·프로토타입(KG API 목업으로), 정책·소유 입력/표시 UI, canary 등록 화면.
- **Canary 레지스트리 경계 (확정):** 등록·`Trivial` 분류 = KG(이경준 UI + 저장은 협의). 소비 = 비교기(최민준, PR #13 `idor.Marker`), 비교기는 재분류 안 함(§3.1). 저장 위치만 Open #3.
- **백엔드 필요 (협의 대상 — 직접 안 고침):** KG 저장소(principal/resource/policy/canary) · distill 어댑터 · KG API = `db/`·`server/` → 이동찬·최민준 협의 또는 담당 리뷰 PR.
- **집행(E②) 경계:** 접근통제 지도는 E② 에 "**읽기 전용 교차 조회만 허용**" 범위를 넘긴다(조회 한정, 상태변경은 별도 승인 시험환경 — 절대규칙 6). 실행 허용은 KG/LLM 이 아니라 E② 가 정한다(절대규칙 3).

## 6. Open — 팀 정렬 필요
1. **정본(canonical) 정책의 출처·저장 + `policy_version`↔유효창↔정본 reader** (oracle Open #2·verdict-record Open #3 페어, 이동찬↔이경준):
   - **내 제안**: 정본은 **수동 등록 fixture 레지스트리**(관측 추론 아님). 관측 추론 결과는 `ExpectedAccess=unknown` 상한(정본 아님, §3).
   - `policy_version` = 정본 reader가 resolve 시점에 준 **snapshot 버전 문자열**(§3 이미 명시). 레코드의 `policy_valid_from/until`도 같은 reader가 싣고, 오라클이 `observed_at` 기준으로 유효성 판정(만료·`hint_matches_canonical=false` → UNDECIDABLE → 지도 `unknown`, §4.1).
   - **현재 구현과 구분**: `oracle.PolicyAssertion`·`idor.PolicyResolver.ResolvePolicy`는 정책 버전/reset 메타데이터를 반환하지 않는다. 위 snapshot 전달은 서버 관리 실행 묶음/어댑터의 보강 계약([seam §6.1](./idor-verifier-seam.md#61-현재-호출과-새-실행-책임))이며 정본 저장·발급 API는 아직 미정이다.
   - **이동찬 질문**: (a) 정본 reader = fixture 레지스트리인가 server config인가 — `policy_version` 문자열 **포맷·발급 주체**(지도 헤더·감사에 그대로 표기). (b) reset 세대 교체 시 정책 유효창 **무효화 지점**(세대 섞임 → 어댑터가 Evaluate 전 UNDECIDABLE, verdict-record §2d).
2. **principal 역할 레벨(수직)의 소스** — 후속 수직 오라클 전에 선설계(권한 레벨 어디서 얻나).
3. **canary 등록 저장 위치** — 자산 `Extra` 재사용 vs 신규 테이블. UX·`Trivial` 분류 주체는 KG 로 확정(§3.1), **저장 위치만** 협의(최민준↔이동찬).
4. **KG 저장소 테이블명·마이그레이션 담당** — oracle Open #5(판정 레코드 테이블)와 함께 최민준↔이동찬.
5. **신원 status 와 KG ref 의 결합 지점** — 어댑터가 둘을 `SubjectContext` 로 합치는 위치(서버 바인딩 vs 어댑터).

6. **지도 실데이터 읽기 어댑터** — task/run·model·reset·snapshot 범위와 case/레코드 선택·이력 API, 세대 교체의 reader/실행 무효화 시점, KG의 applicability 근거, 정상 접근/애플리케이션 차단 관찰의 필드·서버 검증·판정 우선순위. 현재 목업 타입 확장과 확정/의심 가시 표시는 실제 연결 전에 정렬한다. (이경준↔최민준↔이동찬)
