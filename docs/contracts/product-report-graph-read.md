# 제품 보고서의 자산 그래프·접근통제 지도 연결

UI 인계 대상은 기존 `UrlLiveLive.tsx` 보고서 탭과 `UrlAccessMapLive.tsx`다. 프론트엔드 구현은 이 변경에 포함하지 않는다.
기존 `CoverageGraphData`와 [접근통제 지도 DTO·셀 의미](access-map-read.md)를 사용한다.
KG 조회 v1은 같은 `coverage-graph` route에 `?view=kg-v1` 하나를 붙여 연다([kg-query-v1.md](kg-query-v1.md) §3·§11-3).
인증·결속 task·직접 원천 권한 확인·`X-ABC2LAB-Report-Read-Scope` 재확인은 아래 무인자 조회와 같고, 다른 점은 응답 투영뿐이다.
v1 응답·오류 본문은 kg-query-v1 §5를 따른다. 그 밖의 query(다른 `view`, 중복 키, 추가 키)와 `access-map`의 query는 지금처럼 거부한다.

## 호출과 Job 선택

```http
GET /api/tasks/41/coverage-graph
Authorization: Bearer <현재 관리자 JWT>

GET /api/tasks/41/access-map
Authorization: Bearer <현재 관리자 JWT>
```

제품 관리 서버의 동일 origin을 사용한다. `POST /api/auth/login`으로 받은 현재 JWT의 Bearer 헤더만 인증에 쓴다.
쿠키·query token, 임의 task ID, 별도 run/reset/case/필터/query(`coverage-graph`의 `view=kg-v1` 하나는 예외), HEAD·POST는 받지 않는다. ID는 정규 양의 int64 문자열이다.

보고서 전용 인증 분기는 위 두 GET에만 적용한다. 평가 러너의 `POST /api/tasks` 생성과 `GET /api/tasks` 목록/종료 신호 조회는 기존 `requireAuth` 경로를 유지한다. [인증 회귀 시험](../../server/report_graph_runner_auth_test.go)은 실제 middleware에서 두 러너 경로의 무토큰·유효 Bearer·잘못된 서명을 대조하고, 그래프 GET은 보고서 인증 분기를 사용하는지 별도로 확인한다. [러너 계약 지문 시험](../../scripts/eval/tests/test_runner_engine.py)은 갱신한 전체 본문 지문과 함께 새 보고서 분기를 제외한 본문이 기존 기준과 같은지도 검사한다.

선택한 Job의 최신 응답에서 검증된 `task_id`가 있을 때만 호출한다. task 생성 전에는 두 탭을 조회 대기로 표시한다.
화면의 이전 task, 배열 순서, 현재 작업 기본값으로 보충하지 않는다. 관리 서버는 매 조회마다 비공개 준비 서비스의
영속 `AuthorizeTask` 영수증으로 주체와 task의 소유권을 확인한다. 다른 Job의 task를 붙이는 것은 조회 권한이 아니다.
자산 그래프의 직접 원천 task도 같은 읽기 권한을 확인하며, 하나라도 입증하지 못하면 전체 조회를 403으로 거부한다.
원천의 원천은 조회하지 않는다. 자산 문맥이 직접 원천을 포함한다는 사실은 그 원천의 현재 진단 실행 권한을 발급하지 않는다.

모든 응답은 `Cache-Control: no-store`다. Job 전환·재로그인·조회 실패 시 이전 그래프·격자를 폐기한다.
이전 요청은 AbortController 등으로 취소하고, 응답 시점에도 Job과 task 선택이 같은지 확인한 뒤 화면에 반영한다.
지도는 응답 `task_id`도 선택한 ID와 대조한다. 자산 그래프에는 task/run 식별자가 없으므로 요청과 응답의 결속을 UI가 유지한다.

## 자산 그래프

비민감 합성 예시다. `edges`는 자식→부모의 기존 포함 관계다.

```json
{
  "nodes": [
    {"key":"a:101","kind":"service","label":"https://owned.test","tested":false,"in_scope":true,"asset_id":101,"url":"https://owned.test","port":443},
    {"key":"a:102","kind":"endpoint","label":"https://owned.test","tested":true,"in_scope":true,"asset_id":102,"url":"https://owned.test","port":443}
  ],
  "edges": [{"src":"a:102","dst":"a:101"}]
}
```

- 실제 task와 승인된 직접 원천의 저장 scope·앵커를 `BuildCoverageGraph`와 같은 조건으로 읽는다. `assets.task_ids` 연결만 있고
  scope·앵커가 없는 자산은 기존 그래프와 마찬가지로 포함되지 않는다.
- `tested=true`는 해당 문맥의 **fact 앵커 표시**다. E③ 확정, 실제 HTTP 관측, 그 Job 실행의 검사 완료로 표시하면 안 된다.
- 그래프는 task 문맥이며 특정 실행·reset 세대의 관측 증명이 아니다. 소유·인가 관계나 Asset↔Resource 대응도 제공하지 않는다.
- `in_scope=false`는 포함 관계를 잇는 문맥 노드다. 범위 밖 루트는 이름만 있는 `r:<domain>` 가상 노드로 만들며 다른 Job의 전역
  root-domain 자산 행·PK·회사 연결을 끌어오지 않는다. 참조된 회사는 `c:<id>`와 일반 라벨로 표시한다.
- DTO 필드 이름·노드/관계 형태는 유지한다. 공개 URL과 URL 라벨은 origin만 남기고 userinfo·경로·query·fragment는 제거한다.
  앱 이름·페이지 제목·서비스 자유 문구는 생략한다. 잘못된 호스트/IP는 생략하고 잘못된 가상 루트 키는 비가역 ID로 바꾼다.
  이 때문에 같은 origin의 여러 endpoint 라벨이 같을 수 있다. 선택·중복 제거는 `key`/`asset_id`를 사용한다.
- 자산이 없으면 정상 **200**과 `{"nodes":[],"edges":[]}`다. 이 빈 값은 DB 오류나 미조회 상태를 대신하지 않는다.

제품 보고서의 공개 투영은 정렬을 고정한다. 기존 `getCoverageGraph()`가 503을 `null`로 바꾸는 동작을 그대로 재사용하지 말고,
보고서에서는 HTTP 오류를 보존해 빈 그래프와 조회 실패를 구분해야 한다.

## 접근통제 지도

KG 등록이 있고 현재 실행이 결속된 회차만 성공한다. 아래는 판정이 없는 등록 회차의 기존 DTO 예시로, 축은 예시 한 셀만 표시했다.

```json
{
  "task_id":41,
  "run_ref":"execution-41",
  "reset_generation":"reset-1",
  "axis":"horizontal",
  "policy_version":"kg-fixture/v1:map-snapshot:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "cells":[{
    "principal":"reader","owner_ref":"owner","resource_ref":"document:one","resource_label":"document:one",
    "state":"unknown","oracle_value":null,"applicable":true,
    "record_id":null,"case_ref":null,"observed_at":null,
    "policy_check":"unknown","identity_check":"unknown","data_check":"unknown",
    "same_resource":"unknown","owner_normal_data":"unknown","cross_normal_data":"unknown",
    "nontrivial_data_match":"unknown","reflection_excluded":"unknown","template_excluded":"unknown",
    "reason_codes":[],"canary_fields":[],"projection_error":"observation_unavailable"
  }]
}
```

기존 `mapApiAccessMap()`과 격자·판정 매핑을 유지한다. `confirmed`는 저장 근거가 충분한 위반 후보이며,
`suspected`, `unknown`, `allow-ok`, `blank`는 [기존 상태 계약](access-map-read.md#셀-선택상태)을 따른다.
`observation_unavailable`는 관측 근거 없음이고, `oracle_value=undecidable`은 저장된 판단불가 판정이다.
복수 판정은 `selection_ambiguous`로 남기며 최신·확정 판정을 임의로 고르지 않는다.

일반 URL Job에 KG 등록이 없으면 **409**다. 빈 격자·가상의 정상 접근을 만들거나 `getAccessMap(null)`의 MOCK fallback을
제품 보고서에 쓰지 않는다. 현재 API는 일반적인 소유 그래프, 수직 인가, 접근 이력, 미실행/적용불가 판정, 새 E③ 증명을 제공하지 않는다.

## 기존 설치의 조회 권한 점검

신규 설치는 `productGrantRoles`에서 `product_api`의 `task_scope` SELECT를 부여한다. 기존 설치의 `update.sh`와 설치 재사용 분기는 이 GRANT를 다시 실행하지 않는다. 포트나 컨테이너 이름만으로 실제 연결 역할을 판단하지 않는다.

제품 관리 서버는 `api.dsn`의 `product_api`로 인증과 task·원천 작업의 조회 문맥을 확인한다. 현재 제품의 실행 서버와 준비 서비스가 `openURLReport`로 여는 저장 보고서 서버는 실행 프로필에 결속된 `runtime.dsn`의 `product_runtime`을 사용한다. 별도로 조립된 저장 보고서 서버는 설치자가 넘긴 pool을 사용한다. `product_api`로 그래프 본문을 읽는 배치에서만 이 역할의 `task_scope` 누락이 그래프 GET의 503으로 드러난다. 실제 그래프 조회 역할을 먼저 확인한다.

| 조회 | 필요한 읽기 |
|---|---|
| 인증·작업 문맥 | `settings`, `tasks`, 그래프에 한해 `task_relations`. 준비 영수증은 보호 서비스에서 확인 |
| 자산 그래프 본문 | `assets`, `companies`, `exploration_nodes`, `exploration_anchors`, `task_scope`의 `task_id, kind, company_id, domain, net, value`, `try_inet(text)`의 유효 EXECUTE |
| 지도 | `exploration_nodes`, `task_control_modes`, `e3_verdict_issuances`, `e3_oracle_verdicts`, `e3_evidence_manifests`. 조회는 현재 등록·세대에 결속 |

설치 관리자는 보호된 접속 경로를 사용해 연결 DB·역할과 스키마 USAGE, 실제 SQL에서 사용하는 각 열의 SELECT, 함수 EXECUTE를 점검한다. 열 권한만 부여한 경우 `has_table_privilege(..., 'SELECT')`가 false여도 정상이다.

```sql
SELECT current_database(), current_user,
       has_schema_privilege(current_user, 'public', 'USAGE') AS schema_access,
       has_function_privilege(current_user, 'public.try_inet(text)', 'EXECUTE') AS inet_access;

SELECT col, has_column_privilege(current_user, 'public.task_scope', col, 'SELECT') AS can_read
FROM unnest(ARRAY['task_id','kind','company_id','domain','net','value']) AS cols(col);
```

활성 회차가 끝난 점검 시간에 설치 관리자가 설치 DB, 사용 중인 역할, 기존 열 ACL을 확인한다. 본문 조회가 `product_api`를 사용하고 위 6개 열이 누락된 경우에 한해 아래 명령을 검토한다. 이미 필요한 권한이 있는 설치는 그대로 유지한다.

```sql
BEGIN;
GRANT SELECT (task_id, kind, company_id, domain, net, value)
ON public.task_scope TO product_api;
-- 설치 관리자가 별도 세션에 공개하기 전, 같은 트랜잭션에서 유효 권한을 확인한다.
SET LOCAL ROLE product_api;
SELECT task_id, kind, company_id, domain, net, value FROM public.task_scope LIMIT 0;
COMMIT;
```

트랜잭션 내 확인에 실패하면 롤백한다. 함수 권한도 빠진 설치는 함수 소유자·스키마·정의의 설치 일치를 확인하고 해당 역할과 함수 하나의 EXECUTE를 별도로 검토한다. 다른 표나 역할로 권한을 넓혀 복구하지 않는다.

커밋 후 동일 자격으로 다시 접속해 유효 열 권한과 실제 제품 GET을 확인한다. 범위가 있는 작업의 그래프는 200과 실제 노드를, KG가 등록된 작업의 지도는 200과 저장 자료를 확인한다. KG 미등록 409와 권한 부족 503은 서로 다른 결과다. 이관 후 되돌려야 하면 이 작업에서 추가한 열의 직접 GRANT만 기존 ACL과 대조해 REVOKE한다. 기존 테이블 권한, 상속 권한, DSN과 역할 암호, 보호 설치 파일·이미지 지문과 Job/보고서 데이터를 보존한다. 구 이미지로 돌아갈 때도 기존 이미지–설치 지문 호환성을 별도로 확인한다.

## 오류와 수용 경계

| HTTP | UI 처리 |
|---|---|
| 200 | 저장 결과 표시. 빈 자산 그래프는 별도 정상 빈 상태 |
| 400 | 내부 실행 핸들러의 형식·결속 오류. 요청을 고쳐야 함 |
| 401 | JWT 부재·만료·회수·잘못된 주체. 재로그인 후 선택한 Job부터 다시 조회 |
| 403 | Job/task·직접 원천 권한 거부 또는 관리 중계의 미허용 메서드·query·경로. 이전 결과 폐기 |
| 404 | 읽기 권한이 있는 영수증의 task가 삭제·부재. 선택 정보 갱신 |
| 409 | KG 미등록/실행 전/세대·mode 무효 또는 조회 중 task 문맥 변경. 빈 데이터로 대체하지 않음 |
| 503 | DB·인증 저장소·비공개 권한 조회·내부 읽기 서비스 장애. 오류 상태와 재조회 제공 |
| 502 | 기존 중계의 응답 크기/읽기 실패·비허용 redirect. 정상 데이터로 처리하지 않음 |
| 413 | `view=kg-v1`만: 그래프 상한 초과(`kg_graph_too_large`, 재조회 불가). 중계가 본문 그대로 넘김. 무인자 조회·지도의 413은 503 |

관리 중계는 잘못된 입력도 기존 계약처럼 403으로 막는다. 내부 직접 핸들러의 400과 구분한다. 알 수 없는/다른 주체 task는
존재 확인 전에 403이며, 허용된 영수증의 삭제 task만 404다. 기존 관리자 인증이 ARTEX 이외 JWT 주체를 거부하는 401도 유지한다.

호출 흐름은 `NewProductDiagnosisHandler → NewURLDiagnosisManagementHandler → diagnosisManagementBridge → 실행/저장 보고서 서버 → PostgreSQL`이다.
새 요청 헤더는 중계가 DB 문맥에서 만든 내부 scope 지문 하나뿐이며, 브라우저가 보낸 같은 이름의 값도 전달하지 않는다.
중계 전·후에 영속 Job/직접 원천 권한과 task·탐색 결속을 재검사하고, 지도는 현재 포인터·mode도 대조한다.
실행 서버는 같은 repeatable-read snapshot에서 결속과 데이터를 읽으며 scope 지문 불일치 또는 응답 전 문맥 변경을 거부한다.
성공 응답에도 같은 내부 지문을 돌려주고 관리 중계가 이를 확인한다. 확인되지 않는 구버전/다른 문맥의 실행 서버 응답은 503으로 거부한다.
이 내부 헤더는 브라우저 응답으로 복사하지 않는다.
한 번의 조회는 저장 시점의 결과다. 마지막 권한 확인 이후의 변경까지 실시간 동기화하는 API는 아니다.

기존 중계 응답 상한과 redirect 금지, 중계 전체 10초·DB 읽기 5초를 유지한다. 직접 원천은 최대 256개이며 초과하면 409로 거부한다.
GET은 모델·Planner/Worker·대상 HTTP·E②·E③ 발급·보호 handle을 열지 않고 읽기 전용 트랜잭션만 사용한다.
설치자는 `product_api`에 `task_scope` SELECT를 함께 부여한다. GET에서 DDL/GRANT/seed를 수행하지 않는다.
기존 설치의 권한이 빠졌다면 승인된 설치 마이그레이션에서 이 읽기 권한을 준비해야 하며 조회 실패를 빈 그래프로 숨기지 않는다.
`update.sh`와 기존 설치 재사용 분기는 역할 권한을 자동 이관하지 않는다. 단순 이미지 갱신으로 이 권한까지 추가됐다고
보고하지 않는다. 기존 보호 설치의 이미지/pin 호환성과 권한 이관은 운영자 검토 대상이며 이번 작업에서 운영 DB에 적용하지 않는다.

검증은 소유 일회용 PG16, 실제 제품/보고서 생성자·로그인·HTTP 중계, 재시작한 실제 영속 준비 서비스와 합성 완료 receipt를 사용한다.
fixture 생성만 합성 backend로 수행하며 GET 전후 모든 fixture DB 표·Job 저널 지문과 backend 실행/Inspect 횟수를 대조한다.
실제 설치 역할 시험은 설치 코드의 GRANT를 별도 일회용 DB에 적용해 조회한다. 운영 DB·실제 대상·유료 모델·프론트엔드 수용은 이 시험과 별개다.
