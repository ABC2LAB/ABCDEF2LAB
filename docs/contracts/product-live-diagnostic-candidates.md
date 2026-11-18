# 제품 진행 중 발견 후보 조회

Workbench 진행 화면에서 현재 회차에 저장된 진단 후보를 조회한다.
제품 경로는 `GET /api/tasks/{task_id}/diagnostic-candidates`로, 기존 Bearer 토큰 하나를
`Authorization` 헤더에 보낸다. task ID는 양의 int64 범위의 정규 십진 문자열이다.
조회에는 query, 요청 본문, 인코딩한 경로를 사용하지 않는다. 응답은 `Cache-Control: no-store`다.
후속 Workbench 연결에서는 화면이 활성화된 동안 요청을 겹치지 않게 5초 간격으로 조회하고,
task가 바뀌면 남은 요청을 취소한다.

~~~http
GET /api/tasks/41/diagnostic-candidates HTTP/1.1
Authorization: Bearer <session>
~~~

~~~json
{
  "task_id": "41",
  "items": [
    {
      "candidate_id": "902",
      "state": "manual_review_required",
      "observed_at": "2026-11-18T02:20:01.481Z",
      "suggested_severity": "medium"
    }
  ],
  "returned": 1,
  "candidate_total": 1,
  "truncated": false
}
~~~

| 필드 | 의미 |
| --- | --- |
| `task_id` | 현재 제품 Job에 결속된 task의 문자열 ID |
| `items[].candidate_id` | 저장 탐색 노드의 문자열 ID. 일반 finding 또는 E③ verdict ID와 별개 |
| `items[].state` | `manual_review_required`. 검토 후보 상태를 항상 유지 |
| `items[].observed_at` | 저장 후보의 관측 시각, RFC3339 UTC. Job의 시작·마감 시각과 별개 |
| `items[].suggested_severity` | `critical`, `high`, `medium`, `low`, `info` 중 검증 조건을 충족한 Worker 추정값. 추정값이 없으면 생략 |
| `returned` | 이번 응답의 후보 행 수 |
| `candidate_total` | 같은 task·실행의 후보 건수. 행 수 또는 바이트 상한으로 일부가 생략되면 `null` |
| `truncated` | 행 수 또는 바이트 상한으로 일부가 생략됐으면 `true` |

후보 ID 내림차순으로 최근 저장 순서 200건까지 보인다. 저장 바이트 합계가 16 MiB에 닿으면
그 상한 안에 들어오는 최근 후보까지만 보인다. 생략 여부를 확인할 때 보이지 않는 행의 증거 본문을
가져오거나 검증하지 않는다. 추가 페이지는 제공하지 않는다. `truncated: true`일 때는
실제 `returned`를 써서 ‘최근 N건’으로 표시하고 전체 건수는 미상으로 둔다.
`returned`를 전체 후보 수로 복사하거나 `workflow.counts.found`의 발견 엔드포인트 수를
후보 수로 활용하지 않는다. 일반 ARTEX finding과 E③ 판정의 집계는 각 저장 근거를 사용한다.

승인된 실행에서 실제 저장 후보가 없으면 `items: []`, `returned: 0`,
`candidate_total: 0`, `truncated: false`다. 보이는 페이지의 저장 형식, 증거 digest,
intent의 `yields` 연결 검증이 실패하면 전체 조회가 실패한다. 지정 task의 저장된 행만
조회하며 직접 원천 작업에서도 후보를 상속하지 않는다.

## 상태와 오류

실행 시작이 확인된 `running`·`stopping` 상태 중 살아 있는 실행 조회 경계에서만 읽는다.
저장 결과로 전환한 회차는 기존 보고서 API로 확인한다. 오류 응답은 `{"error":"<코드>"}`다.

| HTTP | 코드 | 화면 의미 |
| --- | --- | --- |
| 400 | `invalid_candidate_request` | 경로·메서드·요청 형식 오류 |
| 401 | `authentication_required` | Bearer 인증 필요 또는 만료 |
| 403 | `candidate_scope_denied` | 소유권 미확인 또는 작업/대상 승인 변경·만료·철회 |
| 404 | `task_not_found` | 소유권 확인 뒤 task가 사라짐 |
| 409 | `candidate_run_not_active` | 시작 확인 전, 실행 조회 경계 없음, 마감 또는 결과 불명 |
| 502 | `candidate_response_invalid` | 내부 응답 형식·결속·크기 오류 |
| 503 | `candidate_read_unavailable` | 준비 서비스, 등록 기록, DB·실행 권위 또는 저장 근거 검증 오류 |

## 저장 및 중계 경계

`NewProductDiagnosisHandler` → `diagnosisManagementBridge` → `urlPreparationRuntime`의
현재 live handler → `db.ListLiveDiagnosticCandidates`를 사용한다. 제품 API는 준비 서비스의
`AuthorizeTask`와 등록 조회 `target`으로 권한을 확인한다. 실행 서버는 설치가 묶은
task/exploration, 실행 참조와 대상 revision을 사용한다. 실행 서버는 저장 조회 전후 Bearer 인증과
실행 호스트의 권위를 확인한다. 각 경계는 응답 전 권한과 task 결속을 다시 검사한다.
실행 서버의 PostgreSQL 조회는 읽기 전용 반복 가능 읽기 트랜잭션이다.
공개 중계 상한은 10초·64 KiB, 실행 조회 상한은 5초다. 저장 후보는 한 행당 2 MiB, 조회 입력 전체
16 MiB까지 검사한다. 실패 로그에는 조회 단계와 상태만 기록한다. 조회에는 기존 설치 역할을 사용한다.

서버는 정규 식별자, 고정 상태, 관측 시각, 허용된 추정 등급, 집계값만 투영한다.
제목·본문·계정·세션·URL·명령·증거·내부 결속 헤더는 응답에서 제외한다.
GET은 대상 연결 검사, 모델 호출, 실행/handle 복원, E② 송신, E③ 판정/발급,
저장 변경을 발생시키지 않는다. 기존 ARTEX `/api/exploration/findings?task=…`
계약은 별도로 유지한다.
