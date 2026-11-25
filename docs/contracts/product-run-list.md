# 제품 URL v2 진단 목록 — 서버 조회 계약

`GET /api/diagnoses/v2/runs`는 새 세션이 Job ID·최초 멱등 키 없이 **자신의 보호 Job 기록**을 다시 찾는 읽기 API다. 일반 ARTEX `/api/tasks`와 원천·권한이 다르다. 기존 Bearer 인증과 `owner_context`를 사용한다. 새 엔진 실행·예산 예약·인증 준비·대상 승인·E② 권위를 만들지 않는다.

## 요청·페이지

첫 요청은 query 없이 또는 `?limit=25`로 보낸다. 기본 25개, 최대 50개다. 다음 요청은 응답 `page.next_cursor`를 `?cursor=...`로 보낸다. limit를 다시 지정하면 cursor의 원래 limit와 같아야 한다. 본문·Idempotency-Key·다른 query·중복 query·빈 값·비정규 정수는 400이다. cursor는 최대 512바이트 정규 base64url이고 송신 권한이 아니다.

```json
{"version":"diagnosis-api.v2","owner_context":"주체 문맥의 SHA-256","page":{"runs":[{"id":"prep-…","revision":7,"display_name":"새 점검","state":"running","phase":"execution","task_id":"42","started":true,"drain_complete":false,"result_ready":false,"report_available":false,"approval_status":"not_revalidated","action_required":true}],"snapshot":"목록 snapshot의 SHA-256","limit":25,"more":false}}
```

정렬은 `preparing`·`running`·`stopping` 회차를 먼저, 같은 그룹은 Job ID 문자열 내림차순이다. **시각순이 아니다.** 생성 시각 `created_at`은 제공하지 않는다. 시작·마감 확인 시각은 기록된 회차에만 아래 선택 필드로 제공하며, 과거 기록의 누락을 추정하지 않는다. 모든 보존 회차를 페이지로 찾을 수 있고 활성 회차가 우선이다.

페이지는 최대 24KiB JSON이며 필요한 경우 개수 한도보다 먼저 자른다. 공개 HTTP envelope는 이 상한+1KiB 이하다. 보호 IPC는 기존 64KiB frame 상한도 유지한다. 전체 원천 Job 수는 기존 설치 저장소 상한을 따른다.

주체별 저장 projection을 한 서비스 잠금 아래 읽고, 제품 대상 승인 상태도 별도 한 잠금 아래 일괄 읽는다. 주체·정렬된 행·revision·승인 표시 상태의 지문을 cursor와 대조한다. 페이지 사이에 생성·실행 상태·revision·철회/만료 표시가 바뀌면 409 `snapshot_changed`다. UI는 누적 목록을 버리고 첫 페이지부터 다시 조회한다. 변한 snapshot의 페이지를 혼합하거나 중복 append하지 않는다. 서버 재시작 후 집합이 같으면 cursor를 그대로 읽고, 중단으로 Unknown이 된 경우에는 충돌로 다시 조회한다.

## 행과 안전한 의미

- `id`, `revision`, `display_name`, `state`, `phase`, 선택 `reason_code`, `task_id`는 실제 보호 Job에서 온다.
- `started`, `drain_complete`, `result_ready`는 서로 다른 저장 사실이다. unknown을 완료/미실행/취약점 없음으로 바꾸지 않는다.
- 선택 `started_at`, `finished_at`은 동일 Job에 저장된 최초 시작·drain 확인 시각이다. RFC3339 UTC(`Z`, 소수 초 허용)이며 미확인은 필드 생략이다. null·빈 문자열을 반환하지 않는다. 결과 불명에서도 확인된 시각은 유지한다. 자세한 전환·구 기록·소요 계산·배포 순서는 [시각 계약](diagnosis-run-timing.md)을 따른다.
- `report_available`은 저장된 `result_ready`와 task 참조가 있다는 뜻이다. 보고서 HTTP·저장소가 지금도 정상이라는 보증이 아니며 실제 GET의 오류를 그대로 표시한다. 실패·취소의 부분 보고서도 가능하다.
- `approval_status`: `not_revalidated`(목록은 실행 권위를 재검증하지 않음), `expired`, `revoked`, `changed`, `unavailable`. 이 필드는 실행 허가가 아니다. 기존 승인 기록과 달라졌거나 확인할 수 없으면 정상 running으로 표시하지 않는다.
- `action_required`: 활성 회차의 현재 상태 재확인, 승인 대기, 실패, 결과 불명 또는 승인 상태 문제가 있는 경우다. 활성 회차라도 목록만 보고 자동 재개하지 않는다.

비밀번호·세션·토큰·멱등 키·보호 파일·원문 URL·정책 원문·SourceDigest는 목록에 넣지 않는다. 다른 principal의 Job은 모으지 않으며 그 principal의 cursor도 재사용할 수 없다. 기존 Job status·stop 및 task/report 중계의 소유권 검사는 계속 적용한다.

## 화면 인계 — 이경준·윤재상

1. 새 로그인/새로고침 후 `/runs`를 조회하고 owner_context를 기존 Workbench 주체와 대조한다.
2. 표시용 이름이 없으면 Job ID를 보여 준다. 기록되지 않은 시각을 생성하지 않는다.
3. `state`와 `approval_status`를 함께 표시한다. 미재검증·철회·만료·변경·불명은 정상 running/성공으로 표현하지 않는다.
   - 끝난 점검(완료·중지·실패)의 `expired`는 결과 유효성과 무관해 표시하지 않는다(만료는 새 점검 전에 대상을 다시 승인하라는 뜻이라 새 점검 화면이 알린다, 2026-11-24). 끝난 점검의 철회·변경·불명은 그대로 표시한다.
4. 선택한 행의 `GET /api/diagnoses/v2/drafts/{id}/status`로 최신 revision을 다시 얻는다. 목록 revision으로 자동 중지·재개하지 않는다.
5. 신뢰된 task_id로 기존 `/api/exploration/activity?task={task}` 및 `/api/report/diagnostic?task={task}`에 재연결한다. 공개 활동 API는 원문을 숨기고 고정 상태/실제 sequence만 준다.
   - 점검 진행 화면(#661, #677): 탐색 흐름이 있는 회차는 활동 기록을 두지 않는다(workflow 스냅숏 활동은 서버가 원문 대신 고정 문장만 줘서 보일 내용이 없음). 흐름이 없는 회차는 실행 중에만 이 활동 API로 보인다. 끝난 회차는 제품 서버가 보고서 전용으로 바뀌어 활동 조회를 닫는다(403).
6. 중지는 기존 `POST …/drafts/{id}/stop`의 명시적 사용자 동작과 최신 revision을 사용한다. 목록 조회가 실행 시작이나 재송신을 유발하면 안 된다.

이 브라우저가 이미 가진 생성 핸들은 조회 선택 힌트와 분리해 보존한다. 다른 회차를 조회했다 돌아오더라도, 같은 주체·Job ID의 핸들만 최신 status·입력·실행 결속 대조를 통과한 뒤 사용한다. 생성/시작 키가 없는 다른 브라우저는 계속 조회 전용이며, 목록이나 다른 Job의 핸들에서 키를 만들거나 승계하지 않는다. 시작 전 외부 시험 계정 동의는 기존처럼 메모리에만 유지한다. 주체 교체·명시적 연결 해제 때 메모리 보존분도 버린다.

활성 회차의 승인이 철회·만료·변경·확인 불가라면 상태 알약에도 승인 문제를 함께 표시한다. 복구한 회차의 요청 승인 목록 위 또는 단일 상세에는 조회 전용·원래 시작 화면에서만 승인 가능·같은 운영자 계정으로 종료 가능하다는 안내를 한 번 둔다. 이 안내는 복구 화면에 승인 권한을 추가하지 않는다.

HTTP 401은 재인증, 400은 입력 오류, 409 `snapshot_changed`는 목록 재조회, 503은 검증/저장소/서비스 불가다. 서버 오류를 예시 목록으로 대체하지 않는다. 준비 서비스가 새 command를 모르는 구버전이면 목록을 성공으로 표시하지 않는다. UI 코드와 브라우저 수용은 별도 담당 범위다.

## 검증 범위

보호 저널·주체별 페이지·상태 변경 충돌·개수/바이트 상한·Unix IPC·재시작 회귀를 검사한다. 공개 제품 입구 시험은 실제 PostgreSQL 설치 인증, 실제 보호 준비 서비스와 Unix IPC, 실제 저장 활동/HTML 보고서 handler를 사용해 새 세션 재발견→상태→중지→서비스 재시작→보고서 재연결을 확인한다. 실행 backend만 합성이며 이를 실제 AI 진단/대상 송신/Workbench 브라우저 결과로 계산하지 않는다. 실제 모델 회차와 UI 새로고침 수용은 `NOT_RUN`이다.


검증 보충(2026-11-16, 구현 SHA `bc3274fc`): 새 전용 PostgreSQL 16 컨테이너를 실제 재시작하고, 서비스 재기동과 같은 새 SQL 연결·새 관리 handler로 재로그인→보호 목록→기존 task 보고서까지 통과했다. 합성 backend의 실행 횟수는 1회로 유지되어 DB/서비스 복구가 실행을 다시 호출하지 않았다. 이 시험은 정상 커밋의 별도 소스 사본에 재시작 동기화 gate만 넣어 수행했다. 초기 시험에서 임시 Docker 포트 변경과 기존 장수 SQL 연결의 재시작 직후 503도 관측했다. 고정된 자체 loopback 포트와 새 서비스 SQL 연결로 재검증했으며, 장수 연결이 살아 있는 동안의 무중단 DB 복구나 자동 재시도 보장으로 확대하지 않는다. 503은 성공이나 빈 목록으로 숨기지 않는다.
