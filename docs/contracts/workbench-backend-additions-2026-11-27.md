# 작업대 프런트엔드용 서버 응답 추가(2026-11-27)

경준님 화면 작업이 서버 추가 작업을 기다리지 않도록 이번에 넣은 응답 칸의 연동 요약이다. 정본은 각 계약 문서이고, 여기는 화면이 바로 쓰는 모양만 모았다. 모든 칸은 **선택 칸**이며, 없으면 이전 서버이거나 서버가 그 값을 모르는 것이다. 화면은 없는 값을 추정하지 않는다. 문구·배치는 화면이 정한다.

| 기능 | API | 새 칸 | 웹 파서 | 정본 계약 |
|---|---|---|---|---|
| 남은 시간 | `GET /api/diagnoses/v2/drafts/{id}/status`(그 밖의 초안 응답도) | `draft.time_limit`, 최상위 `server_time` | `parseURLJob`(→`job.time_limit`), `parseURLServerTime` | [점검 시각 §시간 상한](diagnosis-run-timing.md) |
| 새 시작 거절 안내 | `POST …/drafts/{id}/start`의 409 | `busy_reason` | `ApiError.busyReason`, `urlBusyReason()`, `URL_BUSY_REASONS` | [URL 진단 준비 '새 시작 거절 사유'](../url-diagnosis-preparation.md) |
| 로그인 차단 안내 | `GET /api/tasks/{id}/e2-flow`, `GET …/e2-flow/requests/{call}/{attempt}` | 시도 행·상세의 `login_refusal` | `E2Attempt.login_refusal`, `E2RequestDetail.login_refusal`, `E2_LOGIN_REFUSALS` | [작업대 실데이터 §4.2](workbench-live-data.md) |
| E③ 결과 상태 | `GET /api/diagnoses/v2/drafts/{id}/status`(제품 설치) | 최상위 `confirmation` | `parseURLConfirmation`, `URL_CONFIRMATION_STATUSES` | [제품 확정 단계 §7.4](e3-product-confirmation-interface.md) |
| 발견 상세 카드 | `GET /api/tasks/{id}/finding-verdict-detail?candidate_id=…`(또는 `verdict_id`·`finding_id`) | 없음(이미 있음, #733·#740·#741·#828) | `fetchVerdictDetail`(productVerdicts.ts) | [판정 상세 결정](finding-verdict-detail-decision.md) |
| 로그인 설정 폐지 | — | 선행 PR 대기 | — | 아래 §6 |

## 1. 남은 시간

```json
{"version":"diagnosis-api.v2","owner_context":"…",
 "draft":{"state":"running","started":true,"started_at":"2026-11-17T01:02:03.123456789Z",
  "time_limit":{"run_deadline_at":"2026-11-17T01:12:00Z","model_close_at":"2026-11-17T01:08:10Z",
   "model_drain_ms":90000,"confirmation_reserve_ms":120000,"shutdown_margin_ms":20000}},
 "server_time":"2026-11-17T01:05:03Z"}
```

- 받은 순간 `offset = server_time − 브라우저 now`. 서버 지금 = 브라우저 now + offset.
- `running`·`stopping`에서만: 실행 남은 시간 = `run_deadline_at − 서버 지금`, 모델 단계 남은 시간 = `model_close_at − 서버 지금`(음수면 0).
- `time_limit`이 없으면(시작 전·구 기록) 남은 시간을 그리지 않는다. `started_at + duration_seconds`로 대신 세지 않는다. 일시정지·재개해도 값은 바뀌지 않는다(상한은 멈추지 않고 흐른다).
- `server_time`은 Job 비교에 넣지 않는다(폴링마다 바뀜). 예시: [diagnosis-run-timing.responses.example.json](diagnosis-run-timing.responses.example.json) `running_with_time_limit`·`cancelled_keeps_time_limit`.

## 2. 새 시작 거절

```json
{"version":"diagnosis-api.v2","error":"busy","busy_reason":"release_available"}
```

| `busy_reason` | 화면 안내 |
|---|---|
| `run_active` | 다른 점검이 진행 중 — 끝나면 시작 |
| `run_stopping` | 다른 점검이 중지·마감 중 — 잠시 뒤 다시 조회 |
| `candidate_verification_active` | 선택 후보 검증 중 — 끝나면 시작 |
| `closure_unconfirmed` | 이전 점검 결과 불명·마감 미확인 — 기다리거나 운영자에게 알림 |
| `release_available` | 운영자 [멈춘 작업 해제] 가능 |
| `release_pending_restart` | 해제 기록됨, 재기동 대기 — 재기동 뒤 다시 조회 |
| `recovery_unsupported` | 해제를 쓸 수 없는 설치 — 운영자 수동 복구(전체 재기동) |
| `multiple_blockers` | 여러 작업이 막음 — 운영자 확인 |

칸이 없거나 목록 밖이면 기존 `busy` 안내를 쓴다. 이 사유는 시작을 허용하거나 잠금을 풀지 않는다. 운영자 해제 근거(작업 ID·시각)는 운영자 전용 `GET /api/product/preparation/release`에만 있다.

## 3. 로그인 차단

`reason: "policy_denied"`는 그대로이고, 보내지 않은 모델 로그인 POST 시도에만 `login_refusal`이 더해진다.

```json
{"attempt_id":"…","stage":"result","method":"POST","decision":"denied","reason":"policy_denied",
 "forward_attempted":false,"login_refusal":"login_attempts_exhausted"}
```

| `login_refusal` | 화면 안내 |
|---|---|
| `login_attempts_exhausted` | 같은 아이디 연속 로그인 실패 3번(회차 상한) |
| `login_history_unavailable` | 앞 단계 로그인 기록 인계를 확인하지 못해 로그인을 보내지 않음 |
| `login_verification_pending` | 같은 계정의 앞선 로그인 확인 중 |
| `browser_account_in_use` | 같은 브라우저 문맥에 다른 계정이 로그인해 있음 |

칸이 없으면 기존 `policy_denied` 표시. 탐색 단계 로그인은 e2-flow에 없다.

## 4. E③ 결과 상태

```json
{"version":"diagnosis-api.v2","owner_context":"…","draft":{"state":"completed","…":"…"},
 "confirmation":{"record":"recorded","status":"confirmation_integrity_failed","reason":"journal_integrity_failed"}}
```

| `record` | `status` | 화면 |
|---|---|---|
| `recorded` | `disabled`·`start_refused`·`completed`·`interrupted`·`confirmation_integrity_failed` + `reason`(코드 또는 null) | 상태·사유 그대로. `confirmation_integrity_failed`를 "발견 없음"으로 보이지 않는다 |
| `absent` | null | 실행 중이면 "아직 기록 전", 끝났으면 "확정 단계 결과 없음". **꺼짐(disabled)으로 추정하지 않는다** |
| `unreadable` | null | "확정 단계 결과를 읽지 못함" |

- 회차 `draft.state: completed`와 `confirmation.status: completed`는 다른 칸이다. 확정 단계 `completed`는 취약점 확정이 아니다(판정은 판정 목록·상세).
- 판정 목록 `confirmation`과 같은 판독기다: `recorded` ↔ `{status, reason}`, `absent` ↔ `null`, `unreadable` ↔ `status: "unreadable"`.
- 칸이 없으면 제품 설치가 아니거나 이전 서버. 예시: [run-confirmation.status.example.json](run-confirmation.status.example.json).

## 5. 발견 상세 카드

서버·웹 파서·카드가 이미 있다(`UrlLiveLive.tsx`의 `VerdictDetailCard`가 보고서의 후보 상세를 열 때 후보 `entry_id`로 `candidate_id` 조회. 진행 화면 후보 패널에서는 열리지 않는다). 후보와 판정은 확정 단계가 저장한 `candidate_refs`로만 이어지고, 연결이 없으면 `availability: "no_link"`다(유사도·임의 ID로 잇지 않음). 이번에 서버 변경은 없다.

- **지금 실제 회차에서는 늘 `no_link`가 정상이다.** 제품 확정 단계가 아직 모델 단계 후보를 입력으로 받지 않아 판정의 `candidate_refs`가 비어 있다. 개발 계획([development-plan.md](../development-plan.md) '모델 후보를 확정 단계에 넘기기')이 이 연결을 측정 뒤로 미뤘다(2026-11-27 사용자 확인: 보류 유지). 화면은 `no_link`를 "판정 없음"·"취약점 없음"이 아니라 "이 후보에 이어진 확정 판정 없음"으로 보인다. 판정 자체는 판정 목록(`GET /api/tasks/{id}/product-verdicts`)으로 볼 수 있다.
- 응답 구분: 연결 없음 `200`+`no_link`, 없는 후보 `404`, 조회 장애 `503`, 판정 진위 실패 `200`+`record_integrity_failed`(값 숨김), 결속·상한 위반 `422`.
- 제품 전송 근거 상세는 아직 `journal_v3_unavailable`이다(E② v3 감사 연결 후속).

## 6. 로그인 설정(9칸) 폐지 — 아직 하지 않음

`TargetLoginForm`·새 점검 로그인 칸·`login_method_missing` 경고는 지금 지우지 않는다. 계정 로그인 방법(`none`·`learned`·`verified`) 저장·조회(⑤-B, 최민준)와 확정 단계 학습 로그인 재사용·`verified` 기록(#974)이 dev에 들어온 뒤, 서버의 9칸 폐지 PR과 같은 시점에 지운다. 그 전에 지우면 안 되는 이유: 지금 서버는 대상에 로그인 방식(9칸)이 없으면 시험 계정을 회차에 결속하지 않는다([모델 로그인 시도 기록 계약](model-login-attempts.md) 효력 조건 · 계정 분류 폐지의 담당·순서 — 9칸 폐지 PR까지 유지). 화면이 9칸을 먼저 없애면 새 대상은 계정 없는 회차가 되고, 프로그램 로그인(확정 단계 다시 로그인·선택 후보 검증)도 쓸 로그인 방식이 없다.
