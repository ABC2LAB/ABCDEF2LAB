# 종료 회차 승인 이력 읽기 계약

## 경계

`GET /api/tasks/{taskID}/saved-request-approvals`와
`GET /api/tasks/{taskID}/saved-request-approvals/{approvalID}`는 저장된 승인 기록의
읽기 전용 투영이다. 기존 `/request-approvals` 실시간 목록·결정 API의 실행 결속과
승인 권한은 바꾸지 않는다. 제품 관리 중계는 인증된 ARTEX 관리자가 실제 작업에
결속된 초안만 조회하도록 검사한다. 보고서 호스트도 동일한 Bearer 인증과
기동 시 고정한 작업 ID를 요구한다.

작업의 `diagnosis_workflows` 저장 기록에서 `job_id`, `task_id`, `run_ref`를
확인하고, `request_approvals`의 작업 ID·소유자·회차 참조·승인 ID를 대조한다.
호출자는 회차 참조를 지정할 수 없다. 다른 작업·회차의 승인 ID는 404다.
워크플로 기록이나 승인 기록의 결속이 깨지거나 DB 조회가 실패하면 503이다.
작업에 저장된 워크플로가 없으면 404다. 워크플로는 있지만 승인 건수가 없으면
`approvals: []`를 반환한다. 목록은 1000건까지 반환하며 초과하면 422로
실패시켜 부분 목록을 전체 이력으로 오해하지 않게 한다.

목록 응답은 `{run_ref, approvals}`이고 개별 응답은 단일 승인 투영이다.
각 투영은 `id`, `attempt_ref`, `method`, `state`, `execution_state`,
`created_at`, `expires_at`, 선택적 `decided_at`, `live: false`,
`can_decide: false`만 포함한다. `state`는 `pending`, `allowed`,
`denied`, `expired`, `cancelled`이고, `execution_state`는 `not_sent`,
`awaiting_dispatch`, `dispatch_pending`, `completed`, `unknown`이다.
`allowed`만으로 대상 송신을 뜻하지 않는다. `completed`는 실행 결과가
기록됐음을 뜻하며 성공 HTTP 상태를 뜻하지 않는다.

대상 URL·경로·본문·인증 헤더·계정/세션 참조는 이 투영에 포함하지 않는다.
조회 경로에서 승인·거부·재전송을 수행할 수 없다. 화면은 종료 회차의
이력 조회에 이 경로를 사용하고 결정 버튼을 표시하지 않아야 한다.
