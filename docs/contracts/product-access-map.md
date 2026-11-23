# 제품 접근통제 지도 (수평: 자원 종류 × A/B) — 계약 초안

작성: 2026-11-22 · 이경준 · **팀 리뷰용 초안**(11/23 #855 리뷰 반영: §1 양방향 검사, O1~O3 결정). 검토 기준: `dev` `164e2148`.
연결: [access-map-read](./access-map-read.md)(평가 지도, 바꾸지 않음) · [product-verdict-list-proposal](./product-verdict-list-proposal.md)(제품 판정 목록 #801) · [e3-product-confirmation-interface](./e3-product-confirmation-interface.md)(확정 단계) · [access-map-phase2-proposal](./access-map-phase2-proposal.md)(`na` 의미)

## 0. 왜·무엇을

1차 리허설에서 제품으로 실제 점검을 돌리면 지도 API(`GET /api/tasks/{id}/access-map`)가 늘 409였다. 평가 지도는 KG 고정 데이터 등록이 쓰는 결속(`exploration_nodes` origin `abc2lab.access-map.current/v1`)과 평가 판정(`source_kind='eval'`)만 읽는데, 제품 회차는 그 결속을 쓰지 않고 제품 판정 행은 스키마상 KG 칸이 비어 있다(`e3_issuance_source_fields`). 발표(12/18)에서 IDOR 지도를 제품 점검 결과로 보여야 한다.

정한 것(2026-11-22 요청자 확인):
- **읽을 때 만든다.** 저장된 제품 판정과 회차의 정책 선언 동결본으로 지도를 만들고, KG 결속이나 새 표를 쓰지 않는다. 판정·동결본이 원본이고 지도는 그 투영이다.
- **평가 지도 경로는 그대로 둔다.** 새 엔드포인트를 따로 두고 `access-map`·`coverage-graph?view=kg-v1`의 평가 경로 코드·응답은 바꾸지 않는다(공식 측정 영향 없음).
- **수평 축만.** 행 = 선언한 자원 종류, 열 = 소유(A)·교차(B) 시험 계정. 수직(관리자↔일반)은 선언에 역할 등급이 없어 만들 수 없고, 발표 뒤 과제다.
- **순서**: 지도(12/3쯤) → 보고서 KG 그래프(12/7쯤, §5). 준비 서비스 IPC 칸(§1)은 최민준, 서버 API·화면(§2·§3)은 이경준. 머지는 둘 다 측정 뒤.

비범위: 수직 축, 정책 선언에 역할 등급 넣기, 평가 지도 변경, 판정·확정 단계 동작 변경, 진행 중 실시간 갱신(지도는 확정 단계가 끝난 회차를 읽는다. 끝나기 전에는 서버가 §2.3대로 B 셀을 `observation_unavailable`로 주고, 화면은 §3대로 격자 대신 안내를 보인다).

## 1. 준비 서비스 IPC `policy_source` 칸 추가 (최민준)

지금 IPC는 동결본 digest·계정 라벨 둘·확정 단계 상태·사유만 준다. 지도의 행 축(선언 종류 전체·공유 여부)과 건너뛴 종류가 없다. 둘을 더한다. **선언 원문·경로·본문 틀·표식 칸·ID 칸은 계속 내보내지 않는다** — 종류 이름·공유 여부·사유 코드만이다.

```go
// diagnosisprep/product_declaration.go
type ProductPolicySource struct {
	Present           bool   `json:"present"`
	SHA256            string `json:"sha256,omitempty"`
	OwnerAccountLabel string `json:"owner_account_label,omitempty"`
	CrossAccountLabel string `json:"cross_account_label,omitempty"`
	// Types는 동결본 types[]의 요약이다(동결본 순서 = type 오름차순). 동결본에서만 읽고 현재 선언으로 대신하지 않는다.
	Types []ProductDeclaredType `json:"types,omitempty"`
}

type ProductDeclaredType struct {
	Type        string `json:"type"`        // e3contract typePattern
	Sharing     string `json:"sharing"`     // "shared" | "owner_only"
	Confirmable bool   `json:"confirmable"` // e3contract.TypeDeclaration.Confirmable()
}

type ProductConfirmationState struct {
	Present bool   `json:"present"`
	Status  string `json:"status,omitempty"`
	Reason  string `json:"reason,omitempty"`
	// Skipped는 확정 결과 파일 RunResult.Skipped 그대로다(종류·사유 코드). 결과에 없으면 비운다.
	Skipped []ProductSkippedType `json:"skipped,omitempty"`
}

type ProductSkippedType struct {
	ResourceType string `json:"resource_type"`
	Reason       string `json:"reason"` // confirmationReason 모양(지금은 not_owner_only 하나)
}
```

모양 검사(`validProductPolicySource`·`validProductConfirmationState`에 더함, 보내는 쪽과 받는 쪽이 같은 함수):
- `Present=false`면 `Types`는 비어 있다(지금 규칙 그대로).
- `Present=true`면 `Types`는 1개 이상 `e3contract.MaxDeclarationTypes`(16) 이하, `type` 엄격 오름차순·중복 없음, `sharing`은 두 값 중 하나, **`confirmable` ⇔ `sharing=owner_only`(양방향)**. 선언 검증(`e3contract/declaration.go` `case !t.Confirmable(): bad("create.marker_field")`)이 `owner_only` 종류에 `Confirmable()`을 요구하므로 유효한 동결본에서는 늘 성립한다. 그래도 받는 쪽(관리 API 지도 투영)은 이 칸으로 `na`/`declared_shared`를 칠하므로, 형식이 틀린 프레임(`owner_only`·`confirmable=false` 또는 `shared`·`confirmable=true`)을 모양 검사에서 거부해야 한다(fail-closed). 둘을 다 보내는 것은 투영이 `sharing` 해석을 따로 하지 않고 동결본 쪽 `Confirmable()` 결과를 그대로 쓰게 하려는 것이다.
- `Status=unreadable`이거나 `Present=false`면 `Skipped`는 비어 있다. `Skipped`는 16개 이하, `resource_type`은 `typePattern`·중복 없음, `reason`은 `confirmationReason` 모양.
- 동결본은 있는데 `types` 요약을 만들 수 없으면(형식 오류) 지금처럼 동결본을 읽지 못한 것과 같이 다룬다(`policy_source_unavailable`). 일부 종류만 싣지 않는다.

호환:
- 크기: 종류 16 × (이름 64 + 고정 칸) + 건너뜀 16개라 IPC 한 프레임(64KiB) 안이다.
- 기존 소비자(판정 상세·제품 판정 목록)는 새 칸을 쓰지 않고, 응답 바이트도 바뀌지 않아야 한다(시험으로 고정, §4).
- 지도 쪽은 `Skipped`를 행 축으로 쓰지 않고 `Types`와 대조하는 데만 쓴다(§2.3). 그래서 결과 파일이 `skipped`를 비워 두는 경로(시작 거절·중지 등 `r.result`)가 있어도 된다.

## 2. 서버 API `GET /api/tasks/{task_id}/product-access-map` (이경준, 리뷰 이동찬·최민준)

### 2.1 순서·인증

제품 판정 목록(#801 §2)과 같다: 인증(401) → 준비 서비스 `AuthorizeTask`(403 / 회차 참조 없음 409) → 제품 판정 행 읽기(읽기 전용 Repeatable Read 트랜잭션 하나) → 봉인 확인 기록 읽기 → IPC `policy_source` 한 번 → 행마다 진위 확인(목록과 같은 함수 `productVerdictListRow` 재사용) → 투영 → 응답. `Cache-Control: no-store`. 쿼리 파라미터·HEAD·쓰기 메서드는 받지 않는다. 대상 HTTP·모델 호출·판정 발급·DB 쓰기를 하지 않는다. 관리 API 프로세스는 회차 디렉터리를 직접 읽지 않는다.

### 2.2 응답 (allowlist)

```json
{
  "format": "jindo.workbench-product-access-map/v1",
  "task_id": "42",
  "source_kind": "product",
  "axis": "horizontal",
  "policy_source": { "ref": "declaration-<job>", "digest_sha256": "<64hex>" },
  "confirmation": { "status": "completed", "reason": null },
  "principals": [
    { "label": "acct-a", "role": "owner" },
    { "label": "acct-b", "role": "cross" }
  ],
  "resources": [
    { "resource_type": "baskets", "sharing": "owner_only", "confirmable": true },
    { "resource_type": "products", "sharing": "shared", "confirmable": false }
  ],
  "hidden_records": 0,
  "cells": [
    {
      "principal": "acct-b",
      "resource_type": "baskets",
      "state": "confirmed",
      "applicable": true,
      "na_basis": null,
      "verdict_id": "901",
      "record_status": "verified",
      "oracle_value": "confirmed",
      "checks": { "policy": "yes", "identity": "yes", "data": "yes" },
      "oracle_reason_codes": [],
      "product_reason_codes": [],
      "candidate_refs": ["cand-7"],
      "observed_at": "2026-11-20T01:02:03.000000Z",
      "evidence_seal": { "status": "confirmed", "reason": null },
      "projection_error": null
    }
  ]
}
```

| 칸 | 규칙 |
|---|---|
| `format`·`source_kind`·`axis` | 고정값. 평가 지도 응답과 형식이 다르다(평가 파서로 읽지 않게) |
| `policy_source` | 동결본 참조·digest. 대상 승인 판·계정 라벨은 `principals`에 있으므로 여기 두지 않는다 |
| `confirmation` | IPC 확정 단계 상태·사유 그대로. 결과 파일이 없으면 `null` |
| `principals` | 동결본 라벨 둘, 순서 고정(owner → cross). 라벨은 C14 등록 라벨이고 자격·세션이 아니다 |
| `resources` | IPC `Types` 그대로(오름차순) |
| `hidden_records` | 진위 실패로 값과 종류를 숨긴 판정 행 수. 진위 확인은 통과했지만 종류가 `resources`에 없는 행도 여기에 센다(한 행 때문에 응답 전체를 막지 않는다 — #801 행 단위 원칙) |
| `cells` | `resources` × `principals` 전부, 행 순서(종류 오름차순, 그 안에서 owner → cross). 빠진 칸 없음 |
| 셀 `checks` | 제품 판정의 세 검사만. 평가 지도의 비교 신호 6개(`same_resource` 등)는 제품 판정에 없으므로 칸을 두지 않는다(`unknown`으로 채우지 않는다) |
| 셀 `record_status` | 목록과 같은 값(`verified` / `policy_source_unavailable`은 §2.4로 셀에 오지 않음). 판정 없으면 `null` |

내보내지 않는 것: 판정 정본 바이트, 결속 칸(job·run·generation·instance), 저널 참조, 모델 태그, `resource_ref`(ID digest — 상세 API에서), 표식·ID 칸 이름, 선언 경로·본문 틀, 계정 자격·세션, 대상 URL.

### 2.3 셀 상태

**A(owner) 열** — 정상 대조 계정이라 판정 대상이 아니다.
- `state="owner_control"`, `applicable=null`, 판정 칸 전부 `null`/`[]`. 확정 단계의 A 조회 결과는 B 셀의 사유(`product_owner_marker_unobserved` 등)로 보인다.
- 이 상태는 **"선언이 이 계정을 소유 계정으로 정했다"는 뜻뿐이다.** A 조회가 성공했다거나 A가 자기 객체를 읽었다는 관찰이 아니다(그 관찰은 이 칸에 싣지 않는다).
- 평가 지도는 소유자 칸을 `observation_unavailable`/`blank`로 둔다. 제품 지도는 선언이라는 양의 근거가 있어 따로 둔다(§6 O1 결정). `owner_control`은 제품 지도 응답에만 있고 평가 지도 `state` 값에 더하지 않는다.

**B(cross) 열 × `confirmable=false`(공유 종류)** — 선언이 "교차 조회 허용"을 명시한 것이 적용불가의 양의 근거다.
- `state="na"`, `applicable=false`, `na_basis="declared_shared"`, 판정 칸 `null`/`[]`.
- 그 종류에 진위 확인을 통과한 판정이 있으면 확정 단계는 공유 종류에 판정을 만들지 않으므로 모순이다 → `unknown`·`record_invalid`.
- `confirmation.status=completed`면 `skipped`가 `confirmable=false` 종류 집합과 같아야 한다(정상 경로는 공유 종류를 모두 `not_owner_only`로 남김). 그 밖의 상태는 `skipped`가 비어 있을 수 있어(`r.result` 경로) 비어 있지 않을 때만 대조한다. 대조할 때 이 종류가 없거나 사유가 `not_owner_only`가 아니면 → `unknown`·`na_evidence_mismatch`. 반대로 `skipped`에 `confirmable=true` 종류가 있거나 `resources`에 없는 종류가 있으면 응답 전체를 422(`product_access_map_integrity_failed`)로 한다(동결본과 결과가 서로 다른 회차를 가리킴).

**B(cross) 열 × `confirmable=true`** — 위에서부터 처음 맞는 것:

| 조건 | `state` | `projection_error` |
|---|---|---|
| 그 종류의 `verified` 판정 2건 이상 | `unknown` | `selection_ambiguous` |
| `verified` 1건, `oracle_value=confirmed`, 세 검사 모두 `yes` | `confirmed` | null |
| `verified` 1건, `confirmed`인데 검사 중 `yes` 아님 | `unknown` | `record_invalid` |
| `verified` 1건, `suspected` | `suspected` | null |
| `verified` 1건, `undecidable` | `unknown` (`oracle_value=undecidable`) | null |
| `verified` 1건, `allowed` (owner_only는 기대 접근 거부라 나올 수 없음) | `unknown` | `record_invalid` |
| 판정 0건, `hidden_records > 0` | `unknown` | `record_hidden` (숨긴 행이 이 종류일 수 있어 "없음"이라 하지 않음) |
| 판정 0건, `confirmation.status=completed` (종류마다 하나여야 함) | `unknown` | `record_missing_after_attempt` |
| 판정 0건, 그 밖(진행 중·중지·꺼짐·시작 거절·결과 없음) | `unknown` | `observation_unavailable` |

- 판정이 있는 셀은 `applicable=true`, 판정 칸을 목록 항목에서 그대로 옮긴다. `evidence_seal`이 `unconfirmed`(봉인 미확인)여도 상태는 그대로 두고 셀에 봉인 표시만 싣는다. 포함 실패는 목록과 같이 진위 실패(숨김)다.
- 판정 0건 셀도 `applicable=true`다(시험 대상 종류라는 근거는 선언에 있음). `undecidable`과 `observation_unavailable`을 합치지 않는다(평가 지도와 같은 구분).
- 색을 추정하지 않는다: 상태는 서버가 이 표대로만 정하고, 화면은 받은 상태를 그대로 그린다.

### 2.4 지도를 만들 수 없을 때

축이 동결본에서만 오므로 동결본을 확인할 수 없으면 격자를 만들지 않는다. 빈 격자·현재 선언·이름순 계정으로 대신하지 않는다.

| 상태 | 코드 | 언제 |
|---|---|---|
| 400 | `invalid_product_access_map_request` | 경로·메서드·쿼리 |
| 401 | `authentication_required` | 인증 없음·만료 |
| 403 | `product_access_map_forbidden` | 작업 소유 아님 |
| 409 | `product_access_map_binding_unavailable` | 회차 참조 없음(목록과 같음) |
| 409 | `product_access_map_no_declaration` | 동결본 없음(`present=false`, 선언 없이 시작한 회차) |
| 409 | `product_access_map_roles_undeclared` | 동결본에 계정 역할 없음(확정 단계도 `account_roles_undeclared`로 꺼짐) |
| 422 | `product_access_map_integrity_failed` | 판정 행 상한 초과·`skipped`와 `types` 불일치·응답 크기 초과 |
| 503 | `product_access_map_unavailable` | DB·IPC 장애, 동결본을 읽지 못함(`policy_source_unavailable`) |

목록 응답의 `policy_source_unavailable`(값은 보이고 "당시 원본 미확인")은 지도에서는 503이다 — 축 자체를 동결본으로 확인할 수 없기 때문이다.

## 3. 화면 (이경준)

- 지도 화면(`UrlAccessMapLive`)과 보고서 탭 지도: 평가 지도가 409일 때 지금의 안내 + `ProductVerdictMap` 표 대신 제품 지도 격자를 그린다. 평가 지도가 200이면 평가 격자를 그대로 두고 제품 격자는 따로 둔다(두 출처를 한 격자에 섞지 않는다).
- seam: `lib/diagnosis/kg.ts`에 `getProductAccessMap(taskId)` + exact 파서(§2.2 allowlist, 서버가 만들 수 없는 값은 받지 않음 — #689 방어 파서 원칙). 레이아웃이 바뀌어도 이 getter만 갈아 끼우게 둔다.
- **파서 분리**: 제품 지도 파서는 평가 지도 파서(`checkAccessMapResponse`·`API_STATE_DECISION`)와 따로 둔다. `owner_control`·`na`·`na_basis`는 제품 파서에서만 받고, 평가 지도 응답에 이 값이 오면 지금처럼 fail-closed(`미등록 state`)다. 두 파서가 상태 표를 공유하지 않는다.
- 셀 표시: `owner_control` = "소유 계정(선언) · 판정 대상 아님" — 체크·성공 색을 쓰지 않는 중립 표시로, A 조회 성공으로 읽히지 않게 한다. `na` = "—(공유 선언)", 나머지는 평가 지도와 같은 라벨·색. `projection_error`는 셀 설명 문구로.
- **확정 단계 전·진행 중 안내(§6 O2 결정)**: `confirmation=null`(확정 결과 파일 없음 = 아직 확정 단계를 끝내지 않음)이면 격자를 그리지 않고 "확정 단계가 끝나면 교차 계정 결과가 채워집니다" 안내만 보인다. `confirmation`이 있으면(완료·중지·꺼짐·시작 거절·진위 실패·읽기 실패) 격자를 그리고 상태·사유를 격자 위에 보인다. 서버 응답은 그대로다(B 셀은 §2.3대로 `observation_unavailable`).
- 출처 딱지: "제품 · 정책 선언 declaration-<job> · 수평(종류 × A/B)". 운영자 전용 고지는 지금 그대로.
- 지금의 `ProductVerdictMap` 표는 제품 지도 연결 PR에서 격자로 대체하고, 보고서의 "제품 확정 단계 판정" 구획(목록)은 그대로 둔다.

## 4. 시험

IPC(최민준 PR):
- 동결본 → `types` 요약 정확히 일치(공유·소유자 전용 섞인 선언, 16개 상한), 동결본 순서 유지.
- 모양 검사 거부: 오름차순 아님·중복·잘못된 sharing·**`owner_only`인데 `confirmable=false`·`shared`인데 `confirmable=true`(두 방향 각각)**·`present=false`인데 types 있음·`unreadable`인데 skipped 있음. 보내는 쪽(준비 서비스)과 받는 쪽(관리 API `ValidateResponse`)이 같은 검사로 거부하는지 둘 다.
- 판정 상세·제품 판정 목록 응답 바이트가 칸 추가 전과 같음.

서버 API(이경준 PR):
- §2.3 표의 행마다 한 건(A 열, 공유, confirmed/suspected/undecidable, 검사 불일치, allowed, 2건, 숨김, completed 누락, 진행 중 누락).
- `skipped` 불일치 두 방향(셀 `na_evidence_mismatch` / 응답 422).
- §2.4 오류 표 전부. 숨긴 행의 종류가 응답 어디에도 나오지 않음.
- 평가 지도 `access-map`·`kg-v1` 응답이 바뀌지 않음(기존 시험 그대로 통과).
- 변이 시험: 상태 표의 조건 하나씩 뒤집어 잡히는지.

수용(이경준): 제품 설치 화면으로 VAmPI 점검 1회(공유 종류 하나 이상 포함한 선언) → 지도 격자에 confirmed·na·owner_control이 실제 판정과 맞게 보이는지, 건수로 확인. Juice Shop은 공식 측정과 그 직전 장비 검증 파일럿에만 쓰므로([개발 계획 v4.3](../development-plan.md) 원칙) 수용·회귀 확인에 쓰지 않는다. VAmPI로 공유 종류를 선언할 수 없으면 허가받은 다른 URL로 하고 대상을 PR에 적는다.

## 5. 다음: 보고서 KG 그래프 (12/7쯤)

`coverage-graph?view=kg-v1`도 같은 결속을 읽어 제품 회차에서 비어 있다. 지도 API가 들어간 뒤 같은 투영(§2.3)을 그래프 노드(계정·자원 종류)·간선(판정)으로 내보내는 제품 전용 보기를 따로 정한다. 평가 `kg-v1` 경로는 바꾸지 않는다. 칸 모양은 지도 PR 리뷰가 끝난 뒤 이 문서에 덧붙인다.

## 6. 결정 (2026-11-23, #855 리뷰 — 최민준·이동찬)

- **O1 → 제품 지도 전용 `owner_control`.** 평가 지도 상태 값과 섞지 않게 제품 지도 파서에서만 받는다(§3 파서 분리). 화면은 소유 계정 선언 근거임을 분명히 하고 A 조회 성공으로 읽히지 않게 한다(§2.3·§3).
- **O2 → 확정 단계 전·진행 중에는 안내를 먼저.** `confirmation=null`이면 격자 대신 안내(§3). 서버 계약 영향 없음.
- **O3 → 지금은 `declared_shared` 하나.** `not_owner_only`는 선언 검증상 "공유 선언"과 같은 뜻이다. 건너뜀 사유가 늘면 그때 이 문서(`na_basis`·§2.3 대조 규칙)를 같이 고친다. 고치기 전에 새 사유가 오면 §2.3대로 `na_evidence_mismatch`로 떨어진다.
