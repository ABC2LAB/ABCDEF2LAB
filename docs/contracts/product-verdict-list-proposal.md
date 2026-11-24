# 제품 판정 목록 조회 초안 (보고서·지도의 제품 판정 표시)

**상태: 결정 반영 — 구현 대기.** 작성: 이경준(2026-11-20). §4 결정·행 단위 진위 규칙은 최민준 답(#801 코멘트, 사용자 확인, 2026-11-20). 서버 구현: 최민준. 리뷰: 이경준·이동찬(제품 중계·E² 근거 결속). 화면 연결: 이경준.
코드 기준: `origin/dev 54e03799`.

[판정 레코드 §2(f)](verdict-record.md)는 "보고서·지도에서 제품 판정을 출처 표시와 함께 보이는 일은 결정 §9 화면 작업"이라고 남겼다.
화면이 그 일을 하려면 **작업 하나의 제품 판정을 한 번에 읽는 서버 읽기**가 필요한데 지금은 없다. 이 문서는 그 경계만 정한다.
판정 값·검증 규칙·공개 칸 규칙은 새로 만들지 않고 [판정 상세 결정](finding-verdict-detail-decision.md)(OD06·OD08·OD10·OD11)을 그대로 따른다.

## 1. 지금 있는 것과 빈 곳

| 항목 | 지금 | 근거 |
|---|---|---|
| 제품 판정 저장 함수 | 있음. **호출하는 곳 없음**(확정 단계 결과는 `runs/<job>/e3-confirmation.json`에만) | `db.RecordProductVerdict`, `cmd/first-controlled-diagnosis/product_e3_confirmation.go` |
| 제품 판정 목록 읽기(DB) | 있음. 정본 digest·정책 출처 칸을 대조하고 하나라도 어긋나면 전체 거부. 이 API는 행 단위로 검증하는 읽기를 따로 둔다(§2.2) | `db.ProductVerdicts(ctx, task, limit)` |
| 판정 하나 상세 API | 있음. `verdict_id` selector, 제품 정책 동결본 대조(IPC `policy_source`) | `GET /api/tasks/{id}/finding-verdict-detail` |
| 보고서 판정 행 | 평가(`source_kind='eval'`)만. `verdict-<issuance_id>` 행 | `server/diagnostic_report.go` |
| 접근통제 지도 | 평가만. KG fixture 등록 격자(principal×resource) | `server/access_map.go`, [지도 조회](access-map-read.md) |
| **작업의 제품 판정 목록 API** | **없음** | — |

그래서 화면은 지금 제품 판정이 있는지조차 알 수 없다. 상세 API는 `verdict_id`를 알아야 부를 수 있다.

## 2. 결정: 작업 단위 목록 읽기 하나

`GET /api/tasks/{task_id}/product-verdicts`

- **설치 위치**: 판정 상세와 같은 자리(OD10). 제품 관리 중계(`NewProductDiagnosisHandler`)의 작업 결속 읽기에만 둔다. 저장 보고서 모드 허용 목록은 늘리지 않는다. 실행 API로 넘기지 않는다.
- **순서**: 인증(401) → 준비 서비스 `AuthorizeTask`(Principal·Task 소유, 403 / 회차 참조 없음 409) → 제품 판정 행 읽기 → 준비 서비스 IPC(`policy_source`: 동결본 digest·계정 라벨 둘, 확정 단계 `status`·`reason`, 회차당 한 번) → 행마다 진위 확인(§2.2) → 응답. 관리 API 프로세스는 회차 디렉터리를 직접 읽지 않는다.
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
    "owner_account_label": "acct-a",
    "cross_account_label": "acct-b"
  },
  "confirmation": { "status": "completed", "reason": null },
  "items": [
    {
      "verdict_id": "901",
      "record_status": "verified",
      "resource_type": "orders",
      "resource_ref": "orders:<ID digest>",
      "oracle_value": "confirmed",
      "checks": { "policy": "yes", "identity": "yes", "data": "yes" },
      "oracle_reason_codes": [],
      "product_reason_codes": [],
      "candidate_refs": ["cand-7"],
      "evidence_calls": 2,
      "observed_at": "2026-11-20T01:02:03.000000Z",
      "evidence_seal": { "status": "confirmed", "reason": null }
    }
  ]
}
```

| 칸 | 규칙 |
|---|---|
| `source_kind` | 늘 `product`. 평가 판정은 이 응답에 절대 섞지 않는다(§2(f)) |
| `title` | 프로그램 고정 문구(OD11 제품 제목과 같음) |
| `policy_source` | 그 회차의 당시 동결본이 있을 때만 객체, 동결본이 없거나 읽지 못하면 `null`. `ref`는 `declaration-<job>`, `digest_sha256`은 IPC가 준 동결본 digest |
| `policy_source.target_approval_revision` | IPC는 주지 않는다. 진위 확인을 통과한 행들의 정책 출처가 모두 같은 판일 때만 그 값, 행이 없거나 판이 다르면 `null` |
| `policy_source.*_account_label` | 동결 선언의 소유·교차 계정 라벨(C14 등록 라벨, 자격·세션 없음). IPC `policy_source`가 digest와 함께 이 둘만 준다(선언 원문은 안 줌). 동결본에 라벨이 없거나 모양이 계약 밖이면 `null`, 화면은 "소유 계정 → 교차 계정" |
| `confirmation` | 준비 서비스 IPC로 받은 `e3-confirmation.json`의 `status`·`reason`만(`reason`이 없으면 `null`). 파일이 없으면 `confirmation` 자체가 `null`이고 화면은 이를 "꺼짐"으로 추정하지 않는다. 파일이 있는데 읽지 못하거나 결과 형식·상태 값이 계약 밖이면 `status: "unreadable"`(`reason: null`)로 "없음"과 따로 둔다 |
| `evidence_seal` | 판정 근거 체크포인트가 봉인된 저널에 포함되는지(E1 봉인 확인). 모양·조건은 `e3-product-verdict-store.md` §7: 근거 호출 없는 판정 `null`, `{"status":"confirmed","reason":null}`, `{"status":"unconfirmed","reason":"seal_not_recorded"\|"evidence_checkpoint_missing"}`(값 유지·"봉인 미확인"), `{"status":"inclusion_failed","reason":null}`(행은 `record_integrity_failed`, 값 숨김) |
| `record_status` | 행마다 진위 확인 결과(§2.2): `verified` · `record_integrity_failed` · `policy_source_unavailable` |
| `verdict_id` | `e3_oracle_verdicts.id` 정규 십진 문자열. 상세 API selector 그대로(OD02, issuance selector는 열지 않음) |
| `resource_ref` | `<종류>:<ID digest>`(OD11). 객체 ID·경로 원문 없음 |
| `oracle_value` | 정본 소문자 4값 그대로. 서버가 바꾸지 않는다. `record_integrity_failed` 행은 `null` |
| `checks` | 저장 세 검사 `yes/no/unknown`. `record_integrity_failed` 행은 `null` |
| `*_reason_codes` | 오라클 사유와 제품 사유를 **따로**. 상한 각 32(OD08) |
| `candidate_refs` | 확정 단계가 저장 때 검증한 연결만(OD05). 상한 64 |
| `evidence_calls` | 근거 호출 **개수**만. 호출 참조·시도 ID는 상세 API에서 |
| `observed_at` | 저장 정밀도(마이크로초) 그대로 |

내보내지 않는 것: 정본 바이트, 저널 참조·digest, 결속 칸(job·run·generation·instance), 모델 태그, 표식 필드 이름(상세 API에서만), 계정 자격·세션, 대상 URL.

### 2.2 행 단위 진위 확인 (OD06, 판정 상세 초안 §6.1과 같음)

- 목록이라고 건너뛰지 않는다. 행마다 정본 해시, 발급 결속(`binding_canonical`↔발급 칸), 정책 출처 칸(참조·digest·대상 승인 판), 그 회차의 당시 동결본(`e3-declaration.json` digest)을 대조한다.
- **진위 실패**(정본·결속·출처 칸 불일치, 당시 동결본 digest 불일치): 그 행의 `oracle_value`·`checks`를 `null`로 숨기고 `record_status=record_integrity_failed`. 다른 행은 그대로 보인다.
- **당시 원본 미확인**(동결본을 읽을 수 없음): 원값을 유지하고 `record_status=policy_source_unavailable`.
- 어느 경우에도 현재 등록·현재 정책으로 대신하지 않는다. 같은 행은 목록과 상세가 같은 진위 결과를 낸다.

### 2.3 오류

| 상태 | code | 뜻 |
|---|---|---|
| 401 | `authentication_required` | 인증 없음·만료 |
| 403 | `product_verdicts_forbidden` | `AuthorizeTask` 실패 전부(다른 운영자 작업·결속 없음·없는 작업). 판정 상세와 같게 작업이 있는지를 응답으로 가르지 않는다. 목록엔 판정 selector가 없어 404를 쓰지 않는다 |
| 409 | `product_verdicts_binding_unavailable` | 작업에 회차 참조가 없음(아직 시작 전 등) |
| 422 | `product_verdicts_integrity_failed` | 상한 초과만(OD08: 종류 16·사유 32·후보 64·응답 전체 크기). **잘라 보내지 않는다**. 행 진위 실패는 422가 아니라 200의 행 단위 표시(§2.2) |
| 503 | `product_verdicts_unavailable` | DB·IPC 장애 |

빈 목록(`items: []`)은 "제품 판정 없음"이지 "취약점 없음"이 아니다(절대 규칙 5). 화면은 빈 목록의 이유를 `confirmation`의 `status`·`reason`으로만 말하고, `confirmation`이 `null`이면 "제품 판정 없음 · 확정 단계 결과 미확인"으로 쓴다(꺼짐으로 추정하지 않음).

## 3. 화면 표시 (이경준 몫, 서버 계약 영향 없음)

- **보고서**: 평가 판정 행(`verdict-<id>`)과 섞지 않고 "제품 확정 단계 판정" 구획을 따로 둔다. 행마다 출처 딱지(`제품 · 정책 선언 declaration-<job>`)와 `record_status`(`record_integrity_failed`는 "판정 기록 무결성 확인 실패 · 값 숨김", `policy_source_unavailable`은 값과 함께 "당시 원본 미확인"). CONFIRMED도 finding이 아니므로 "확정 판정 · 검토 대상 후보 N건(candidate_refs)"으로 쓰고 후보 상태(`manual_review_required`)는 바꾸지 않는다. 행을 누르면 상세 API(`verdict_id`).
- **접근통제 지도**: 평가 KG 격자에 넣지 않는다(축 출처가 다름: KG fixture 대 정책 선언). 지도 탭 아래에 "제품 확정 단계" 표를 따로 둔다. 행 = 자원 종류, 열 = 소유 계정 → 교차 계정 한 칸(라벨이 `null`이면 그 글 그대로), 셀 = 판정 값. 소유자 셀은 두지 않는다(정상 대조 GET은 판정 대상이 아님, [지도 조회](access-map-read.md)와 같은 규칙).
- 422(상한 초과)는 "판정 목록이 상한을 넘어 표시하지 않음", 503은 다시 시도. 어느 쪽도 "판정 없음"으로 접지 않는다.
- 점검 진행과 보고서의 확정 요약은 제품 판정 조회 중임을 표시한다. 404는 제품 조회 경로가 없는 경우로 처리한다. 그 밖의 조회 실패 시 확인한 수와 함께 제품 판정이 합계에 빠졌다고 표시한다. 보고서의 Markdown에도 같은 안내를 표시한다.

## 4. 결정 (2026-11-20, 최민준 답·사용자 확인)

1. **별도 경로.** 보고서·지도·수정 근거·감독자 저장 확인은 `eval`만 읽는다. 이후 제품 관리 경로의 보고서 내보내기에만 추가한 경계는 §6을 따른다.
2. **계정 라벨을 싣는다.** IPC `policy_source`가 동결본 digest와 함께 `owner_account_label`·`cross_account_label`만 돌려준다(§2.1). 동결본을 못 읽으면 라벨도 `null`.
3. **확정 단계 상태를 같은 응답에 싣는다.** IPC로 `e3-confirmation.json`의 `status`·`reason`만 받아 `confirmation`에 넣는다. 파일이 없으면 `null`(§2.1·§2.3).
4. **읽기 경로를 먼저 연다.** 읽기 경로와 확정 단계 상태를 DB 고정 시험 데이터로 먼저 열어 화면 작업이 막히지 않게 한다. 확정 단계 결과를 `RecordProductVerdict`로 저장하는 연결은 "E③ 송신 활성화 전 필수" 목록(#799 코멘트)에 올리고, 송신을 켜는 PR이 그 목록을 채울 때 함께 확인한다. 그 전까지 실제 회차에서는 늘 빈 목록이다.

## 5. 시험 (구현 PR에서, 최민준)

- 평가 판정만 있는 작업 → `items: []`(평가 행이 섞이지 않음). 제품·평가 판정이 같이 있으면 제품만.
- 같은 작업에 정상 행·진위 실패 행(정본·결속·출처 칸 변조, 동결본 digest 불일치 각각)·당시 원본 미확인 행을 섞어 두고, 실패 행만 값·검사가 `null`이고 나머지는 그대로인지. 현재 등록을 바꿔도 결과가 같은지, 목록과 상세가 같은 행에 같은 진위 결과를 내는지.
- `confirmation`: 결과 파일 있음 → `status`·`reason` 그대로, 없음 → `null`. 계정 라벨: 동결본 읽음 → 두 라벨, 못 읽음 → `null`.
- 다른 운영자 작업·결속 없음·없는 작업 → 모두 같은 403 본문(작업 존재 여부를 가르지 않음), 회차 참조 없음 → 409, query·HEAD·POST → 400/405.

## 6. 제품 관리 보고서 내보내기 (#882)

- 준비 서비스가 작업 조회를 허가하고 작업·회차를 결속한 HTML·DOCX 내보내기에 한해 제품 판정 구역을 추가한다. 평가 보고서·지도·수정 근거·감독자 저장 확인은 기존 조회 경계를 따른다. 제품 행은 평가 판정·발견 목록과 별도로 표시하고 평가·제품 확정의 총합을 요약에 제공한다. 유형별 집계에서 제품 판정은 ‘제품 접근통제(IDOR)’로 표시한다.
- 보고서와 제품 판정 목록은 같은 행 검증 기준을 적용한다. 검증된 확정은 합계에 세고, 봉인 미확인은 사유·건수를 표시한다. 봉인 불일치 등 무결성 확인 실패, 당시 정책 원본 미확인, IPC 조회 실패는 각각 건수로 표시하며 확정에 세지 않는다. 공유 사본에서는 자원 종류를 가린다.
- 보고서 자료와 제품 판정·봉인 행은 하나의 PostgreSQL 읽기 스냅샷에서 가져온다. 트랜잭션 종료 뒤 준비 서비스에 동결본을 한 번 요청한다. IPC 장애가 나면 제품 판정을 읽지 못한 건수를 남긴 보고서를 제공한다. DB 읽기 오류는 503, 보고서 또는 제품 판정 목록 상한 초과는 422이며 어느 쪽도 일부만 내보내지 않는다.
- 조회 전후 판정·발급·manifest·link·finding 행 수와 대상 HTTP·모델 호출 0.
- 응답에 비밀번호·쿠키·토큰·표식 값·계정 원문·파일 경로·저널 참조가 없음(건수 검사). 상한(종류 16·사유 32·후보 64) 경계와 초과 422.
- 화면: 웹 파서가 서버가 만들 수 없는 값(모르는 `oracle_value`·`source_kind≠product`·사유 상한 초과)을 받으면 그 응답 전체를 깨진 응답으로 본다(#689 방어 파서 원칙).
