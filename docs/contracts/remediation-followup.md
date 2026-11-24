# 저장 보고서 이행점검 API

운영자는 원래 보고서를 열어 저장된 조치와 검증 기준을 읽고, 같은 대상의 후속 회차를 직접 선택해 판정한다. 이 API는 저장된 보고서와 담당자 판정만 읽고 쓴다.

## 조회와 회차 선택

- `GET /api/tasks/{source_task}/remediation-followup`은 원래 조치 전체를 돌려준다. `entry`를 반복해 표시할 조치를 고를 수 있다. `current_task`는 이행점검 근거를 고를 회차이고 `general_task`는 추가 발견을 읽을 일반 점검 회차다. 각 ID는 원래 작업보다 크고, 같은 대상 ID와 origin에 결속돼야 한다. 원래 작업과 선택한 각 작업에 대해 조회 권한을 확인한다.
- `items[].original_requests`는 원래 보고서의 감사 완료 관찰에서 확인한 `method`, `path`, `vuln_class`의 목록이다. 원래 조치의 현재 저장 판본과 관찰이 결속된 경우에만 값을 제공한다. 경로의 가린 조각이나 넓은 접두를 대신하는 표시값만 남은 경우에는 비교에 쓰지 않는다.
- `current.findings[].observations[]`의 `reviewable`은 현재 회차의 관찰이 완료·감사·범위 조건을 충족한다는 뜻이다. 화면에서는 `reviewable=true`이며 선택한 조치의 `original_requests`에 경로·메서드·유형이 모두 같은 관찰을 판정 근거로 제공한다. 이 대조는 기록된 경로 기준이며, 담당자는 계정·입력 등 나머지 확인 조건과 검증 기준을 별도로 대조한다.
- `items[].result`는 `fixed`(수정 확인), `reproduced`(다시 관찰됨), `inconclusive`(확인 불가) 중 하나다. 현재 회차에 후보가 없거나 비교할 원래 관찰이 없으면 `inconclusive`로 남는다. 일반 점검에서 **추가 발견 0건**은 `results_state=available`이고 해당 회차의 추가 항목이 비었을 때 표시한다.

## 판정 저장과 근거 변경

- `PUT /api/tasks/{source_task}/remediation-followup?current_task={id}`는 원래 조치의 `entry_id`, `record_id`, `original_digest`와 관찰의 `evidence_entry_id`, `step_id`, `binding_digest`를 서버에서 다시 확인한다. `fixed`와 `reproduced`는 완료된 후속 회차의 감사 완료 관찰, 현재 승인과 회차 제외 경로 검사, 원래 관찰과 같은 경로·메서드·유형, 담당자의 직접 비교를 요구한다. `conditions_comparable`은 둘 다 참이고 `all_criteria_met`은 `fixed`일 때 참이다. 근거가 맞지 않으면 409를 반환한다.
- `inconclusive`는 결과가 준비된 후속 회차에서 관찰 없이도 저장할 수 있다. `reason`은 `insufficient_evidence`, `outside_scope`, `conditions_changed`, `access_unavailable` 중 하나다. 최초 `expected_revision`은 0이며, 수정할 때는 직전 `review.revision`을 보낸다. 근거가 계속 유효한 같은 판정 요청을 다시 보내면 기존 판본을 반환한다.
- 조회할 때도 같은 대조를 적용한다. 저장 후 관찰이나 원래 조치 판본이 바뀌거나 승인 만료·철회·범위 변경으로 관찰을 다시 사용할 수 없으면 현재 결과는 `inconclusive`, 이유는 `review_evidence_changed`가 된다. 저장된 판정은 보존하며 응답의 `review`에는 판본·담당자·시각을 남긴다.

## 요청 제한과 운영 기록

쿼리와 JSON 본문은 각각 최대 8 KiB이고, 요청 처리 제한 시간은 15초다. 준비 서비스 또는 저장소 장애는 503으로 반환한다. 서버 로그에는 고정된 처리 단계와 사유를 남긴다. 이행점검 표가 없으면 `table_missing`, DB 권한이 없으면 `permission_denied`로 기록한다.
