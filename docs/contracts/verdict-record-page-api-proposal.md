# 판정 레코드 페이지 API 응답 제안

2026-11-03 · 이동찬 · **리뷰 결정 반영 제안. API·수집기 미구현이며 운영 활성화 근거 아님.**

검토 기준은 `dev` `b1dea7458b8ff80744991de5b5c46c54f91465ec`다. 이 문서는 러너가 구조화 판정을 수집할 읽기 API·응답 예시와 export를 동결할 명시 제어 호출을 제안한다. 판정 레코드의 논리 필드·마스킹·평가 읽기는 **최민준의 계약 영역**이며, [판정 레코드 계약](./verdict-record.md)을 수정하거나 새 필드를 합의된 것으로 간주하지 않는다. 아래에 명시한 평가 담당자의 리뷰 결정 외 경로·페이지 필드·오류 코드는 검토 대상이다.

현재 findings 수집과 별도의 신호다. 이 PR에는 handler, DB migration, 러너 수집기, KG reader, 판정 발급·저장 배선이 없다. 다른 세션의 E②·E③ 통합 구현과 병합 의존성을 만들지 않는다. 자원 이름은 [type 목록 초안](./resource-ref-types-proposal.md)을 함께 검토한다.

## 1. 전달 경계

| 단계 | 제안 |
| --- | --- |
| 내부 저장 | 서버가 발급한 판정·issuance·근거 연결을 DB에 영속화. 테이블·정본 바이트는 [저장 게이트 §5.5](./e3-storage-gate.md#55-물리-저장-스키마-권장안--리뷰-결정-일부-반영)의 별도 구현 검토를 따름 |
| 서버 export | 같은 task·평가 run·모델·reset 세대의 **최초 판정** 집합을 종료 시점에 동결한 읽기 projection |
| 러너 수집 | 인증된 엔진 API를 페이지로 수집하고 완전성·회차를 검증. 원문 findings의 triage status를 구조화 판정으로 변환하지 않음 |
| 채점 입력 | 페이지 메타데이터를 검증한 뒤 기존 `run_id`·`model_tag`·`reset_generation`·`records` envelope를 생성. 원본 판정과 export 재확인 결과를 구분하여 보존 |

검사 실행은 [seam §6](./idor-verifier-seam.md#6-엔진-연결-계약-제안--이동찬-구현용)의 `tx.VerifyRead` → 비교·오라클 → 구조화 저장 순서다. GET·페이지 재시도·`RecordFindingRetestResult`가 누락 근거를 채우려고 조회를 재실행하거나 판정을 새로 발급하지 않는다.

## 2. 요청·인증·조회 범위 제안

```http
GET /api/tasks/17/verdict-records?run_id=run-example-01&purpose=initial&export_ref=export-example-01&limit=100
Accept: application/json
```

기존 `/api` 인증이 필요하다. 예시는 인증 헤더 값을 생략했다. task 경로와 `run_id`는 검색 조건이지 발급 출처나 조회 권한의 증명이 아니다. 구현할 서버는 task와 신뢰 러너 회차의 등록 결속 및 조회 권한을 대조한다. **이 GET으로 run_id를 등록·재결속하지 않는다.**

**회차 결속 권장안 — 별도 구현 선행 조건:** 러너가 작업 생성 `POST /api/tasks` 본문에 비민감 `run_id`·`model_tag`·`reset_generation`을 넣고 서버가 task 생성 시 한 번만 불변 결속하며 이후 재결속을 거부한다. 출처는 러너 회차의 `run_id`, 사전검사 `llm.model`, 현재 대상 컨테이너 ID인 `reset_generation`이다. 러너는 실제 세 필드가 포함된 요청 본문의 SHA-256을 기존 `task.json` 기록 경로로 서명 근거에 결속해야 한다. 현재 작업 생성 필드·서버 등록·러너 배선은 미구현이며, 이 값만으로 비신뢰 caller에 조회·발급 권한이나 검사 실행 승인을 부여하지 않는다.

응답·export manifest·오류 본문에는 GT 챌린지 키·매핑·평가 정답을 넣지 않는다. 사람이 type 후보를 검토할 때 참고한 평가 자료도 runtime payload로 전달하지 않는다.

- `run_id`는 필수이며 issuance의 평가 회차와 정확히 일치해야 한다. E② `execution_ref`·`case_ref`로 대신하지 않는다. 비평가 issuance(`eval_run_id=NULL`)는 제외한다.
- v0는 `purpose=initial`만 받는 안을 제안한다. 재검증은 최초 판정을 덮어쓰거나 이 목록에 섞지 않고, 별도 읽기 계약으로 정한다.
- 러너는 `limit`을 항상 명시한다. 범위 1~500은 **제안값**이며 실제 서버 상한은 미확정이다. 판정 수집의 `page_size`·`max_page_bytes`·`max_total_bytes`·`max_pages`는 findings와 **별도 설정**으로 둔다. 서버는 요청 상한을 암묵적으로 줄이거나 잘린 페이지를 정상 반환하지 않고, 지원하는 상한이 러너 요청 상한보다 작으면 거부한다. 바이트 상한·실제 값은 레코드 정본 상한과 함께 합의하고 확정 시 최민준에게 전달한다.
- 러너는 §3의 명시 동결 호출로 받은 `export_ref`를 첫 GET부터 모든 요청에 명시한다. 준비된 집합이 하나여도 암묵 선택하지 않으며 `export_selection_required`를 정상 러너 경로로 쓰지 않는다. 일반 caller의 생략 허용 여부는 후속 검토이며 서버가 최신 export를 임의 선택하지 않는다. 이 GET은 export를 생성하지 않는다.
- 이어받기는 같은 경로·run·purpose·export_ref·limit에 `cursor`를 추가한다. cursor는 서버 검증 가능한 불투명 값이며 URL·계정·비밀을 담지 않는다. 다른 task/run/export에 재사용할 수 없고 명시한 `export_ref`와 일치해야 한다.
- `Cache-Control: no-store`를 붙인다. 접근 로그는 인증값·cursor 원문·판정 payload를 기록하지 않고 고정 경로 템플릿·상태·건수만 남기는 안을 제안한다.

현재 JWT가 모델 프로세스와 관리 자격의 OS 격리를 증명하지는 않는다. 기존 단일 계정 인증을 새 task별 RBAC로 표현하지 않으며, 운영 조회 권한·러너 자격 전달은 구현 전 확인한다.

## 3. 결과 집합 동결·페이지 완전성

1. 대상 요청 drain과 별도로 **해당 회차의 판정 writer·issuance/link transaction 종료**를 확인한다. 네트워크 요청 0만으로 판정 저장 완료를 추정하지 않는다.
2. 러너는 계측기 seal·drain과 구간 감시 종료 후, findings 수집 전에 명시 동결 호출로 `export_ref`를 받는 안을 권한다. 경로는 `POST /api/tasks/{task_id}/verdict-exports` **제안**이며, 서버가 해당 회차 writer 종료를 확인한 뒤 export 대상 ID 집합·순서·조회 projection·수동 건수·재확인 결과를 불변 manifest에 고정한다. 이 엔진 API 호출은 대상 송신이 아니다. writer 종료 확인 실패는 `export_not_ready`로 미수집 처리한다. GET은 이미 준비된 manifest만 읽고 동결하지 않으며, 없으면 같은 오류로 거부한다. 실제 호출·manifest 보관·종료 상태 연결은 후속 구현이다.
3. `export_ref` 하나는 task/run/model/reset/purpose, 정확한 판정 ID 집합, `finding_id` link projection과 총건수에 결속한다. ID 오름차순을 제안하되 **MAX(id) 경계만으로 동결했다고 주장하지 않는다.** 늦게 commit된 낮은 ID나 뒤늦은 link가 페이지 집합을 바꿀 수 있기 때문이다.
4. 모든 페이지의 회차 메타·`export_ref`·`total`·`manual_confirmation_count`가 같아야 한다. 같은 cursor 재시도는 같은 레코드·다음 cursor를 반환한다. 유효한 마지막 페이지 이외의 빈 페이지, 중복 ID·cursor 순환·누락·범위 교체는 전체 수집 실패다.
5. 러너는 마지막 `next_cursor=null`과 중복 없는 누적 건수=`total`을 함께 확인한 뒤에만 완성 envelope를 게시한다. 중단·인증 만료·상한 초과·페이지 오류 때 부분 파일을 정상 판정 아티팩트로 서명하지 않는다.
6. 같은 run을 나중에 재export할 필요가 생기면 새 `export_ref`를 명시적으로 선택한다. 이미 수집 중인 export를 새 집합으로 조용히 바꾸지 않는다. manifest 유실·변경이나 원본 무결성 실패는 기존 수집을 거부한다.

재확인 결과도 export 준비 시 동결하며 **발견 시점**에 따라 아래처럼 처리한다. 재검사는 저장된 참조의 확인이며 대상 HTTP 요청이나 오라클 재발급이 아니다.

| 발견 시점·실패 | 처리 제안 |
| --- | --- |
| manifest 동결 전, 정본 조회가 완료됐으나 참조한 KG snapshot이 없거나 보존되지 않았음을 확인 | 원래 판정은 보존하고 `record_checks.status=undecidable`·고정 사유를 같은 manifest에 동결. 모든 페이지에서 같은 판단불가 결과를 전달 |
| manifest 동결 후, GET 수집 중 이전에 확인한 KG snapshot 유실·동결 결과 변경을 발견 | 이미 받은 페이지와 새 판단불가 행을 섞지 않고 `409 export_snapshot_unavailable`로 해당 export 전체 수집 거부. 명시적으로 준비한 새 export에서만 판단불가 결과를 동결 가능 |
| 어느 시점이든 정본 digest·같은 KG snapshot ID/version의 내용·issuance/run/model/reset 불일치 | 위조/혼입과 구분할 수 없는 무결성 실패이므로 `422 verdict_integrity_failed`. 정상 판단불가로 숨기지 않음 |
| 정본 조회 자체의 DB·읽기 인프라 장애 | `503 verdict_export_unavailable`. snapshot 부재가 확인된 것으로 꾸미지 않음 |

## 4. 정상 페이지 응답 예시

아래 값은 합성 예시다. `basket`은 합의 전 type 후보이고 실측 결과가 아니다. `finding_id=null`은 판정이 있지만 아직 finding link가 없는 경우도 표현한다. `confirmed` 한 행만으로 공식 비교의 확정 수를 만들 수 없으며, 독립 격리 귀속·동일 회차 traffic·커버리지와 기존 평가 게이트를 함께 적용한다.

```json
{
  "schema_version": 1,
  "task_id": 17,
  "run_id": "run-example-01",
  "model_tag": "model-example",
  "reset_generation": "reset-example-01",
  "purpose": "initial",
  "export_ref": "export-example-01",
  "collection_status": "complete",
  "total": 1,
  "limit": 100,
  "next_cursor": null,
  "manual_confirmation_count": 1,
  "records": [
    {
      "id": 101,
      "finding_id": null,
      "case_ref": "case-example-01",
      "axis": "horizontal",
      "oracle_value": "confirmed",
      "vuln_verdict": "confirmed",
      "access_policy_result": "위반_후보",
      "policy_check": "yes",
      "identity_check": "yes",
      "data_check": "yes",
      "reason_codes": [
        "policy_deny_resolved",
        "subjects_distinct_verified",
        "data_nontrivial_match"
      ],
      "same_resource": "yes",
      "owner_normal_data": "yes",
      "cross_normal_data": "yes",
      "nontrivial_data_match": "yes",
      "reflection_excluded": "yes",
      "template_excluded": "yes",
      "canary_fields": ["marker"],
      "expected_access": "deny",
      "sharing": "resolved_none",
      "delegation": "resolved_none",
      "policy_version": "policy-example-01",
      "policy_valid_from": "2026-11-03T00:00:00.000000Z",
      "policy_valid_until": "2026-11-04T00:00:00.000000Z",
      "hint_matches_canonical": true,
      "observed_at": "2026-11-03T00:01:00.000000Z",
      "reset_generation": "reset-example-01",
      "owner_ref": "owner",
      "evaluated_ref": "evaluated",
      "owner_status": "verified",
      "evaluated_status": "verified",
      "traffic_evidence_refs": [201, 202],
      "vuln_class": "idor",
      "resource_ref": "basket:42",
      "model_tag": "model-example",
      "created_at": "2026-11-03T00:01:01.000000Z"
    }
  ],
  "record_checks": [
    {
      "record_id": 101,
      "issuance_ref": "issuance-example-01",
      "payload_sha256": "3637739a4ee284bf35fe7686a62e0d1767cc3aab6f80e297b255930cdc8bed87",
      "kg_snapshot_ref": "snapshot-example-01",
      "status": "verified",
      "reason_codes": []
    }
  ]
}
```

`schema_version` 등 페이지 제어 필드는 신규 제안이며, `records[]`만 기존 레코드 계약의 projection이다. `collection_status=complete`는 **서버의 export 집합이 확정됨**만 뜻하는 응답 상수로 러너가 검증한다. 러너의 전체 수집 완료나 판정 성공·취약점 확정을 뜻하지 않는다.

`record_checks`도 **신규 제안인 별도 메타데이터**이며 각 record ID에 정확히 한 행을 대응시킨다. caller 입력으로 issuance의 진위나 결속을 만들지 않는다. `status=verified`는 export 시 저장 근거를 재확인했다는 뜻이며, 대상 접근 성공이나 새 CONFIRMED 발급이 아니다. 실제 와이어 이름·타입은 최종 확인 대상이다.

예시 `payload_sha256`은 `records[0]`에서 변경 가능한 `finding_id`만 제외한 정본의 §5.5 직렬화 바이트 SHA-256이다. 실 구현은 저장한 canonical 바이트의 digest를 검증하며 JSONB 재직렬화로 정본을 복구하지 않는다. 예시 ID의 숫자형은 제안이며 signed 64-bit 범위·언어 간 직렬화 벡터의 합의를 대신하지 않는다.

## 5. 빈 결과·재확인 실패·수동 확정

**수집 완료 0건**의 축약 없는 페이지 예시는 다음과 같다. 이때도 서버가 그 회차의 판정 생성·저장을 수집 가능한 상태로 종료했다는 근거가 필요하다.

```json
{
  "schema_version": 1,
  "task_id": 17,
  "run_id": "run-example-01",
  "model_tag": "model-example",
  "reset_generation": "reset-example-01",
  "purpose": "initial",
  "export_ref": "export-example-empty",
  "collection_status": "complete",
  "total": 0,
  "limit": 100,
  "next_cursor": null,
  "manual_confirmation_count": 0,
  "records": [],
  "record_checks": []
}
```

- **미구현·미수집:** `200 + records=[]`로 대신하지 않는다. 준비되지 않은 서버는 아래 오류를 내며 러너는 기존 `artifacts.records=null`을 유지한다. 모든 GT를 판단불가로 두는 현재 미수집 의미를 바꾸지 않는다.
- **KG snapshot 부재를 동결 전에 확인:** §3의 첫 행에 해당한다. 저장된 oracle 판정과 canonical digest는 바꾸지 않는다. 해당 `record_checks`의 `status=undecidable`, 고정 사유 `kg_snapshot_unavailable` 등을 보존하고 아래 평가 담당자의 결정대로 평가용 파생값을 만든다. `records[]`의 원래 confirmed를 그대로 채점하는 fallback은 없다. 동결 후 발견은 §3의 두 번째 행처럼 전체 수집을 거부한다.
- **정본 digest·issuance/run/model/reset 불일치:** 신뢰할 수 없는 입력이므로 페이지 전체를 거부하고 채점 입력을 만들지 않는다. 단순 KG 보존 실패와 위조·다른 회차 혼입을 같은 정상 판단불가 응답으로 숨기지 않는다.
- **수동 확정:** `manual_confirmation_count`는 같은 회차·고정 export에 신뢰 결속된 수동 카테고리의 별도 건수만이다. 승인자·시각·근거 로그는 서버 필수 저장이며 이 API에 수동 판정 행·로그 원문을 싣지 않는다. run 결속을 검증할 수 없는 수동 항목을 임의로 이 건수에 넣지 않는다. 건수는 `total`·자동 `records[]`와 모든 판정 지표·분모에서 제외한다([저장 게이트 §1.1](./e3-storage-gate.md#11-사람-수동-확정의-세-조건)).
- **ALLOWED:** `record_checks.status=verified`인 행은 `oracle_value=allowed`, `vuln_verdict=null`을 그대로 보존한다. `status=undecidable`이면 아래처럼 두 필드를 평가용 `undecidable`로 바꾸되 원값은 서명 근거에 남긴다. 빈 결과·취약점 없음·fixed로 바꾸지 않는다.

**최민준의 평가 담당 결정:** (페이지 검증과 채점 사본 변환은 [`scripts/eval/verdict_pages.py`](../../scripts/eval/verdict_pages.py). 서버 API·러너 수집 연결 전이며 합성 페이지로만 시험했다. 채점 사본이 어긋나지 않게 `access_policy_result`도 `판정_불가`로 맞춘다.) 러너 어댑터는 원 페이지 전부와 `record_checks`를 서명 근거로 원본 보존한다. `status=undecidable`인 행은 채점용 사본의 `oracle_value`·`vuln_verdict`를 모두 문자열 `undecidable`로 바꾸고, 원값·canonical digest는 근거 원본에 유지한다. 변환 건수와 고정 사유 코드는 서명된 러너 결과에 기록한다. `verified`·`undecidable` 이외 status는 어댑터가 거부한다.

현재 `scripts/eval/collect.py`는 `oracle_value`에 `confirmed`·`suspected`·`undecidable`·`allowed`만 받고, `allowed`이면 `vuln_verdict=null`, 그 외에는 두 값의 일치를 요구하며 채점 envelope의 추가 필드를 거부한다. **이 페이지 응답을 그대로 `parse_scan_output`에 넘길 수 없다.** 후속 어댑터는 모든 페이지·메타·`record_checks` 검증과 위 변환 후 기존 `run_id`·`model_tag`·`reset_generation`·`records` 네 필드 envelope만 만든다. `record_checks`·변환 건수·사유를 이 닫힌 envelope의 새 필드로 넣지 않는다. 이 결정은 [리뷰](https://github.com/ABC2LAB/ABCDEF2LAB/pull/141#issuecomment-6602013580)에 따른 후속 수집기 계약이며, 실제 러너 수집·보존·어댑터 구현과 수용시험 전에는 공식 비교에 연결하지 않는다.

## 6. 오류 응답 제안

| 상황 | HTTP·고정 사유 제안 | 러너 처리 |
| --- | --- | --- |
| 인증 없음·실패 | 기존 인증 거부 | 부분 수집 폐기 |
| 잘못된 limit·purpose·cursor/추가 query, 요청 상한 미지원 | `400`, `invalid_export_request` | 입력 오류로 중단, 암묵 축소·잘림 금지 |
| 조회 범위 밖 또는 존재하지 않는 task/run | 같은 `404`, `verdict_export_not_found` | 빈 성공으로 변환 금지 |
| writer 종료·manifest 미확정, 판정 수집 미지원 | `409`, `export_not_ready` | 미수집 유지, 완료 0건으로 추정 금지 |
| 같은 회차에 준비된 export가 여러 개인데 명시 선택 없음 | `409`, `export_selection_required` | 러너는 항상 export_ref를 명시하며 이 경로를 사용하지 않음 |
| 이전 export manifest 유실·다른 집합 선택 | `409`, `export_snapshot_unavailable` | 기존 페이지 폐기, 새 export 명시 선택 필요 |
| 저장 정본·결속 무결성 실패 | `422`, `verdict_integrity_failed` | 채점 중단 |
| DB·읽기 인프라 장애 | `503`, `verdict_export_unavailable` | 부분 결과 성공 금지 |

오류 본문은 고정 코드만 반환하는 안이다. 원시 DB 오류·파일 경로·SQL·정본 payload·인증값·URL·canary 값은 반환·공유 로그에 넣지 않는다.

## 7. 합의 항목·후속 수용시험

[최민준 리뷰](https://github.com/ABC2LAB/ABCDEF2LAB/pull/141#issuecomment-6602013580)의 회차 결속 권장안·명시 동결·평가 파생값 결정·GT 제외·별도 수집 상한을 반영했다. 리뷰어는 요청 1~5·8 반영 뒤 합의 기록용 Ready 전환을 허용했으며, 실제 서버/러너 구현·수용시험 완료를 뜻하지 않는다. 서버 상한 값과 미정 와이어 세부는 구현 전 최민준과 확인한다. KG snapshot 공급·보존은 [저장 게이트 §9](./e3-storage-gate.md#9-남은-협의)의 대리 검토와 경준의 사후 확인 경계를 유지한다. 이 문서의 type·와이어 제안 동의가 DB 스키마·정본 바이트 상한·운영 권한 전체의 합의가 되지는 않는다.

아래는 **미실행 계획**이다. 이 PR의 문서 검증을 API/DB 통합시험 통과로 세지 않는다.

| ID | 조건 | 기대 결과 |
| --- | --- | --- |
| VRP01 | 명시 export_ref로 정상 2페이지·마지막 페이지·동일 cursor 재시도 | 고정 export·순서·전체 건수·행 일치, 중복 합산 0 |
| VRP02 | 0건 완료, 미지원, 판정 writer 진행 중 | 각각 완료 빈 배열, 미수집, 준비 거부로 구분 |
| VRP03 | 낮은 ID의 늦은 commit·link 변경·다른 run 재검증 | 이미 동결한 집합/행 불변, 다른 회차 혼입 0 |
| VRP04 | task/run/model/reset/export/total 변경·중복 ID·빈 중간 페이지·cursor 순환 | 전체 수집 거부, 부분 artifact 게시 0 |
| VRP05 | 동결 전 KG 부재·동결 후 KG 유실·digest/issuance 위조·DB 장애 | 동결된 사유 보존 판단불가, 기존 export 수집 거부, 무결성 거부, 인프라 실패를 구분 |
| VRP06 | 수동 확정만 추가·다른 회차 수동 내역 | 자동 판정/분모 불변, 같은 범위 수동 별도 건수만 변화 |
| VRP07 | 요청 취소·인증 만료·판정 전용 페이지/바이트/전체/페이지수 상한 초과·서버 상한 미지원 | 부분 결과 서명 0, 암묵 limit 축소·잘림 0, GET으로 송신·판정/issuance/원본 변경 0 |
| VRP08 | verified/undecidable/미지원 status·후속 typed adapter·언어 간 정본 벡터 | 두 평가 판정값 일치·미지원 거부, 네 필드 envelope 유지, 원 페이지/체크·변환 건수/사유 서명 보존, 비밀·GT 키 0 |
| VRP09 | 생성 본문의 run/model/reset 출처·재결속 시도·비신뢰 caller | 생성 시 한 번 결속, 요청 본문 SHA 서명 근거 일치, 재결속·입력값만으로 권한 승격 0 |
| VRP10 | seal/drain·구간 종료 후 명시 동결·writer 종료 미확인 | findings 전 export_ref 획득 또는 export_not_ready로 미수집, GET 동결·암묵 선택 0 |
