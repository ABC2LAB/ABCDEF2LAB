# 제품 판정 목록 조회 초안 (보고서·지도의 제품 판정 표시)

**상태: 초안 — 협의 요청.** 작성: 이경준(2026-11-20). 협의: 최민준(서버 읽기·판정 저장), 이동찬(제품 중계·E² 근거 결속).
코드 기준: `origin/dev 54e03799`.

[판정 레코드 §2(f)](verdict-record.md)는 "보고서·지도에서 제품 판정을 출처 표시와 함께 보이는 일은 결정 §9 화면 작업"이라고 남겼다.
화면이 그 일을 하려면 **작업 하나의 제품 판정을 한 번에 읽는 서버 읽기**가 필요한데 지금은 없다. 이 문서는 그 경계만 정한다.
판정 값·검증 규칙·공개 칸 규칙은 새로 만들지 않고 [판정 상세 결정](finding-verdict-detail-decision.md)(OD06·OD08·OD10·OD11)을 그대로 따른다.

## 1. 지금 있는 것과 빈 곳

| 항목 | 지금 | 근거 |
|---|---|---|
| 제품 판정 저장 함수 | 있음. **호출하는 곳 없음**(확정 단계 결과는 `runs/<job>/e3-confirmation.json`에만) | `db.RecordProductVerdict`, `cmd/first-controlled-diagnosis/product_e3_confirmation.go` |
| 제품 판정 목록 읽기(DB) | 있음. 정본 digest·정책 출처 칸을 대조하고 하나라도 어긋나면 전체 거부 | `db.ProductVerdicts(ctx, task, limit)` |
| 판정 하나 상세 API | 있음. `verdict_id` selector, 제품 정책 동결본 대조(IPC `policy_source`) | `GET /api/tasks/{id}/finding-verdict-detail` |
| 보고서 판정 행 | 평가(`source_kind='eval'`)만. `verdict-<issuance_id>` 행 | `server/diagnostic_report.go` |
| 접근통제 지도 | 평가만. KG fixture 등록 격자(principal×resource) | `server/access_map.go`, [지도 조회](access-map-read.md) |
| **작업의 제품 판정 목록 API** | **없음** | — |

그래서 화면은 지금 제품 판정이 있는지조차 알 수 없다. 상세 API는 `verdict_id`를 알아야 부를 수 있다.

## 2. 제안: 작업 단위 목록 읽기 하나

`GET /api/tasks/{task_id}/product-verdicts`

- **설치 위치**: 판정 상세와 같은 자리(OD10). 제품 관리 중계(`NewProductDiagnosisHandler`)의 작업 결속 읽기에만 둔다. 저장 보고서 모드 허용 목록은 늘리지 않는다. 실행 API로 넘기지 않는다.
- **순서**: 인증(401) → 준비 서비스 `AuthorizeTask`(Principal·Task 소유, 403 / 회차 참조 없음 409) → `db.ProductVerdicts` → 정책 동결본 대조(IPC `policy_source`, 회차당 한 번) → 응답.
- **읽기 전용**: Repeatable Read 읽기 전용 트랜잭션. 모델·대상 HTTP·판정 발급·실행 복구 호출 0. 실행 상태(진행 중·마감)는 보지 않는다(저장 자료 조회).
- **query 없음**. 모르는 query·HEAD·쓰기 메서드는 400/405. 한 작업의 판정 수는 선언의 자원 종류 수(`e3contract.MaxDeclarationTypes` = 16) 이하라 쪽 나눔을 두지 않는다.

### 2.1 응답 (allowlist)

```json
{
  "format": "jindo.workbench-product-verdicts/v1",
  "task_id": "42",
  "source_kind": "product",
  "title": "제품 확정 단계 판정",
  "policy_source": {
    "ref": "declaration-<job>",
    "digest_sha256": "<64hex>",
    "target_approval_revision": "3",
    "frozen_check": "verified"
  },
  "items": [
    {
      "verdict_id": "901",
      "resource_type": "orders",
      "resource_ref": "orders:<ID digest>",
      "oracle_value": "confirmed",
      "checks": { "policy": "yes", "identity": "yes", "data": "yes" },
      "oracle_reason_codes": [],
      "product_reason_codes": [],
      "candidate_refs": ["cand-7"],
      "evidence_calls": 2,
      "observed_at": "2026-11-20T01:02:03.000000Z"
    }
  ]
}
```

| 칸 | 규칙 |
|---|---|
| `source_kind` | 늘 `product`. 평가 판정은 이 응답에 절대 섞지 않는다(§2(f)) |
| `title` | 프로그램 고정 문구(OD11 제품 제목과 같음) |
| `policy_source.frozen_check` | `verified` · `policy_source_unavailable`(동결본을 못 읽음, 원값 표시 유지) · `record_integrity_failed`는 응답하지 않고 422(§2.2) |
| `verdict_id` | `e3_oracle_verdicts.id` 정규 십진 문자열. 상세 API selector 그대로(OD02, issuance selector는 열지 않음) |
| `resource_ref` | `<종류>:<ID digest>`(OD11). 객체 ID·경로 원문 없음 |
| `oracle_value` | 정본 소문자 4값 그대로. 서버가 바꾸지 않는다 |
| `checks` | 저장 세 검사 `yes/no/unknown` |
| `*_reason_codes` | 오라클 사유와 제품 사유를 **따로**. 상한 각 32(OD08) |
| `candidate_refs` | 확정 단계가 저장 때 검증한 연결만(OD05). 상한 64 |
| `evidence_calls` | 근거 호출 **개수**만. 호출 참조·시도 ID는 상세 API에서 |
| `observed_at` | 저장 정밀도(마이크로초) 그대로 |

내보내지 않는 것: 정본 바이트, 저널 참조·digest, 결속 칸(job·run·generation·instance), 모델 태그, 표식 필드 이름(상세 API에서만), 계정 자격·세션, 대상 URL.

### 2.2 오류

| 상태 | code | 뜻 |
|---|---|---|
| 401 | `authentication_required` | 인증 없음·만료 |
| 403 | `product_verdicts_forbidden` | `AuthorizeTask` 실패 전부(다른 운영자 작업·결속 없음·없는 작업). 판정 상세와 같게 작업이 있는지를 응답으로 가르지 않는다. 목록엔 판정 selector가 없어 404를 쓰지 않는다 |
| 409 | `product_verdicts_binding_unavailable` | 작업에 회차 참조가 없음(아직 시작 전 등) |
| 422 | `product_verdicts_integrity_failed` | `db.ProductVerdicts` 대조 실패·정책 동결본 digest 불일치·상한 초과. **잘라 보내지 않는다**(OD08) |
| 503 | `product_verdicts_unavailable` | DB·IPC 장애 |

빈 목록(`items: []`)은 "제품 판정 없음"이지 "취약점 없음"이 아니다(절대 규칙 5). 화면은 확정 단계가 꺼졌는지·안 돌았는지를 §4-③이 정해지기 전에는 추정하지 않고 "제품 판정 없음 · 확정 단계 결과는 실행 기록 확인"으로만 쓴다.

## 3. 화면 표시 (이경준 몫, 서버 계약 영향 없음)

- **보고서**: 평가 판정 행(`verdict-<id>`)과 섞지 않고 "제품 확정 단계 판정" 구획을 따로 둔다. 행마다 출처 딱지(`제품 · 정책 선언 declaration-<job>`)와 `frozen_check`. CONFIRMED도 finding이 아니므로 "확정 판정 · 검토 대상 후보 N건(candidate_refs)"으로 쓰고 후보 상태(`manual_review_required`)는 바꾸지 않는다. 행을 누르면 상세 API(`verdict_id`).
- **접근통제 지도**: 평가 KG 격자에 넣지 않는다(축 출처가 다름: KG fixture 대 정책 선언). 지도 탭 아래에 "제품 확정 단계" 표를 따로 둔다. 행 = 자원 종류, 열 = 소유 계정 → 교차 계정 한 칸, 셀 = 판정 값. 소유자 셀은 두지 않는다(정상 대조 GET은 판정 대상이 아님, [지도 조회](access-map-read.md)와 같은 규칙).
- 422는 "판정 기록 무결성 확인 실패 · 표시하지 않음", 503은 다시 시도. 어느 쪽도 "판정 없음"으로 접지 않는다.

## 4. 열린 결정 (협의 요청)

1. **별도 경로 대 보고서 응답 확장.** 기존 보고서 응답(`/api/report/diagnostic/remediation`)에 `source_kind` 칸을 넣는 대신 **별도 경로**를 제안한다. 기존 읽기 넷(보고서·지도·수정 근거·감독자 저장 확인)은 `eval`만 읽는다는 §2(f)를 건드리지 않고, 제품 판정 진위 실패가 평가 보고서 전체를 막지 않게 하려는 것이다. — 최민준
2. **계정 라벨.** 지도 열 머리에 쓸 소유·교차 계정 라벨(`owner_account_label`·`cross_account_label`)을 응답에 실을지. 판정 정본엔 라벨이 없고 동결 선언에 있다. 실으려면 IPC `policy_source`가 동결본 digest와 함께 두 라벨만 돌려줘야 한다(원문 선언은 계속 안 줌). 안 실으면 화면은 "소유 계정 → 교차 계정"으로만 쓴다. — 최민준·이동찬
3. **확정 단계 결과 상태.** `e3-confirmation.json`의 `status`·`reason`(disabled·start_refused·interrupted·`shared` 종류 skipped 등)을 이 응답의 `confirmation` 칸으로 같이 줄지, 회차 상태 응답(#698 화면 연결)에서만 줄지. 같이 주면 빈 목록의 이유를 화면이 말할 수 있다. 이 역시 관리 API가 회차 디렉터리를 직접 읽지 않고 IPC로만 받아야 한다. — 최민준
4. **선행 조건.** 확정 단계 결과를 `RecordProductVerdict`로 저장하는 연결(판정 몫, 최민준)이 먼저다. 그 전에 이 경로를 열면 늘 빈 목록이다. 경로를 먼저 열고 시험은 DB fixture로 할지, 저장 연결과 한 PR로 갈지. — 최민준

## 5. 시험 (구현 PR에서)

- 평가 판정만 있는 작업 → `items: []`(평가 행이 섞이지 않음). 제품·평가 판정이 같이 있으면 제품만.
- 정본 digest·정책 출처 칸 변조, 동결본 digest 불일치 각각 → 422, 응답에 부분 목록 없음. 동결본 없음 → 200 + `policy_source_unavailable`.
- 다른 운영자 작업·결속 없음·없는 작업 → 모두 같은 403 본문(작업 존재 여부를 가르지 않음), 회차 참조 없음 → 409, query·HEAD·POST → 400/405.
- 조회 전후 판정·발급·manifest·link·finding 행 수와 대상 HTTP·모델 호출 0.
- 응답에 비밀번호·쿠키·토큰·표식 값·계정 원문·파일 경로·저널 참조가 없음(건수 검사). 상한(종류 16·사유 32·후보 64) 경계와 초과 422.
- 화면: 웹 파서가 서버가 만들 수 없는 값(모르는 `oracle_value`·`source_kind≠product`·사유 상한 초과)을 받으면 그 응답 전체를 깨진 응답으로 본다(#689 방어 파서 원칙).
