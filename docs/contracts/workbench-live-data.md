# 작업대 실데이터 연결 계약 (화면 ↔ 서버)

상태: 결정안(최민준, 2026-11-13). 이동찬 몫이던 계약을 11/13 팀 합의로 최민준이 맡아 정한다. 리뷰는 이동찬, 화면 쪽 확인은 이경준.

작업대 화면 중 예시(mock)로만 도는 것이 서버 계약이 없어 막혀 있었다. 이 문서는 막힌 다섯 가지를 정한다. 이미 서버에 있는 것은 그 경로를 적고, 없는 것은 서버가 낼 응답 모양을 정한다. 화면은 이 모양만 믿고, 모르는 값은 지금처럼 fail-closed(모름·실계정 취급·불명)로 보인다.

| 화면 | 상태 | 이 문서 |
|---|---|---|
| 계정 분류 | **서버 있음** — 지금 연결 가능 | 1절 |
| 접근통제 지도 | **서버 있음** — 지금 연결 가능 | 2절 |
| 전송 통제 정책(읽기) | 새 API(최민준 구현) | 3절 |
| 전송 통제 흐름 | 새 API(최민준 구현), 사유 enum 정렬 | 4절 |
| 커버리지 | 기존 워크플로 snapshot으로 1단계 | 5절 |
| 전송 통제 정책 저장 | 결정: 화면에서 실행 정책을 직접 고치지 않는다 | 6절 |
| 진입 버튼 자리 | 결정 | 7절 |

공통: 3·4절의 새 API는 `GET /api/tasks/{id}/access-map`과 같은 task 경로다. 제품은 이 경로를 작업 결속(`AuthorizeTask`)을 확인한 뒤 그 task의 실행·저장 보고서 서버로 넘기므로(`urlPreparationRuntime.ServeHTTP`), 새 API도 같은 서버(`first_diagnosis_report.go`의 mux)에 둔다. 모든 응답에 명령 원문·URL query·헤더 값·본문·쿠키·토큰·비밀번호·제어 비밀(감사 키·CA 키·권한 토큰)을 넣지 않는다(CLAUDE.md 규칙 2). 승인된 origin·경로·메서드는 비밀이 아니라 운영자가 봐야 하는 승인 범위라 그대로 보인다.

## 1. 계정 분류 — 지금 연결 가능

`web/src/workbench/live/accounts.ts`가 기다리던 "저장 위치·승인 주체(탐색 경계 계약 §9-7)"는 [계정 결정안](../account-login-decision.md) 결정 1·2로 정해졌고 dev에 구현돼 있다(#514·#515).

- **저장 위치**: 대상 등록에 붙은 보호 계정 저널(`accounts/`). 계정은 대상(origin) 단위다.
- **승인 주체**: 그 대상을 승인한 운영자(대상 승인자)만 시험 계정으로 표시한다. 표시는 그 대상 판(revision)에 묶이고, 재승인하면 실계정으로 돌아간다.
- **기본값**: 실계정(`real`). 판단할 수 없으면 실계정이다.

| 요청 | 뜻 |
|---|---|
| `GET /api/product/targets/{id}/accounts` | 목록 `{"accounts": ProductAccount[], "account_statement": "..."}`(계정이 없으면 `accounts` 칸 없음). `account_statement`는 시험 계정 표시 때 화면이 그대로 띄워 동의받는 진술 문장이다 |
| `POST /api/product/targets/{id}/accounts` `{"label": "..."}` | 등록(분류 `real`). 라벨은 소문자·숫자·`_`·`-`만(사용자명·이메일 원문 금지) |
| `POST …/accounts/{label}/mark-test` `{"statement_confirmed": true}` | 시험 계정 표시(본인 진술 동의 필수, 없으면 409 `statement_required`) |
| `POST …/accounts/{label}/unmark` `{}` | 표시 해제 |
| `GET`·`PUT /api/product/targets/{id}/login` | 대상 로그인 방식(경로·형식·필드 이름·세션 위치). 자격 값 없음 |
| `PUT …/accounts/{label}/credential` `{"username","password"}` | 자격 등록(서버 보호 파일, 응답·목록에 값·해시 없음) |

`ProductAccount`: `label`, `classification`(`test`|`real`), `reason`(`target_reapproved`|`target_expired`|`manual_unmark`, 없을 수 있음), `registered_by`, `registered_at`, `classified_by`, `classified_at`, `marked_revision`, `credential_configured`(bool, 값 없음).

화면 분류 대응: `test` → `test_approved`, `real` → `unmarked`. 서버에는 "승인 대기" 상태가 없다(표시하는 사람이 곧 승인자). 화면의 `test_pending`은 서버 값으로는 나오지 않는다. `approvedBy`·`approvedAt`은 `classified_by`·`classified_at`이다. 실행에서 계정별 로그인 결과는 실행 결과 `account_auth` 칸이다([실행 결과 계약](run-outcome-states.md) 3.4).

## 2. 접근통제 지도 — 지금 연결 가능

`GET /api/tasks/{id}/access-map`([읽기 계약](access-map-read.md))이 있다. 웹 `getAccessMap(taskId)`도 이미 이 경로를 읽고 `mapApiAccessMap`으로 바꾼다. 화면(`UrlAccessMapLive`)이 지금 `null`(예시)을 넘기는 자리에 현재 점검의 task id(워크플로 snapshot의 `task_id`)를 넘기면 켜진다. 서버가 범위를 하나로 확인하지 못하면 409이고, 화면은 가짜 지도 대신 오류를 보인다.

## 3. 전송 통제 정책 읽기 — 새 API

`GET /api/tasks/{id}/control-policy`

그 task 실행에 결속된 전송 통제(E②) 정책의 표시용 투영이다. 정책은 실행마다 서버가 대상 승인 판과 설치 상한에서 만들고 지문으로 결속한 것이고, 이 응답은 그것을 읽기만 한다. 실행이 아직 결속되지 않았으면 404, 정책 파일이 결속 지문과 다르면 409이다.

```json
{
  "format": "abc2lab.control-policy-view/v1",
  "policy_ref": "scope-target-…",
  "policy_revision": "…",
  "policy_fingerprint_sha256": "…",
  "approval_mode": "preauthorized_scope",
  "header_mode": "preserve",
  "valid_from": "2026-11-13T00:00:00Z",
  "valid_until": "2026-11-13T04:00:00Z",
  "rules": [
    {"ref": "target-path-a", "purpose": "", "origin": "https://shop.example", "connect_ip": "10.0.0.5",
     "path_prefix": "/api", "path_scope_ref": "readonly-path-a", "methods": ["GET", "HEAD", "POST"], "tls_pinned": true}
  ],
  "limits": {"max_requests": 64, "max_attempts": 200, "max_concurrent": 1, "max_body_bytes": 65536,
             "max_response_body_bytes": 16777216, "request_timeout_ms": 5000},
  "accounts": ["tester-a"],
  "audit_bound": true
}
```

- `approval_mode`는 Go 값 그대로다: `preauthorized_scope`(자동·일반), `workbench_selective_v1`(사용자 승인 모드), `confirm_every_request`. 웹 `control-policy.ts`의 `confirm_every`·`workbench_v1` 키는 이 값으로 바꾼다.
- `header_mode`: `preserve`(모델 자격 헤더 거부), `test_account_passthrough`(시험 계정 실행). 인증된 계정이 하나도 없어 실행이 preserve로 묶였는지는 `account_auth`를 본다.
- `rules[].purpose`가 `auth_prep`이면 인증 준비(로그인) 전용 규칙이고 모델 요청은 쓰지 못한다. 화면은 따로 표시한다.
- `tls_pinned`: https 대상은 true(SPKI 고정), http 대상은 false.
- `accounts`: 이 실행에 결속된 시험 계정 라벨만. 자격·세션 값은 없다.
- `audit_bound`: 감사 키가 결속됐는지 여부만. 키 경로·해시는 없다.
- 넣지 않는 것: 자격 파일 pin·CA·helper·상태 디렉터리 경로, 감사 키 pin, 평가 문맥.

## 4. 전송 통제 흐름 — 새 API와 사유 enum

`GET /api/tasks/{id}/e2-flow` · `GET /api/tasks/{id}/e2-flow?after=<index>`

그 task의 전송 통제 감사 기록(실행 결속 디렉터리의 저널, HMAC 체인 검증 뒤)을 호출(call) 단위로 묶은 투영이다. 저널 검증이 실패하면 사건을 하나도 보이지 않고 `journal: "invalid"`로 답한다(일부만 보이고 "정상"으로 보이게 하지 않는다).

**커서**: 기준은 감사 저널 사건 번호 `Event.Index`다(사건마다 1씩 오른다. 시도 번호 `sequence`가 아니다 — 한 시도의 `intent`·`result` 사건은 같은 `sequence`를 쓰고 `control`·`call_closed`에는 시도 번호가 없다).

- `after` 없음(처음 조회): 가장 최근 호출 200개를 그 호출의 모든 사건으로 묶어 준다. 더 오래된 호출이 있으면 `omitted_calls`에 그 수를 적는다(이 오래된 호출은 다시 받을 방법이 없다 — 화면은 "이전 호출 n개 생략"으로 보인다). `last_index`는 검증한 마지막 사건 번호다.
- `after=N`: 번호가 N보다 큰 사건만 오래된 것부터 최대 1000개 준다. 이미 받은 호출의 이어지는 사건이면 같은 `call_ref`로 온다. `last_index`는 이번 응답에 넣은 마지막 사건 번호이고, `more: true`면 남은 사건이 있으니 바로 `after=last_index`로 다시 부른다. 넣은 사건이 없으면 `last_index`는 N 그대로다.
- 화면은 `call_ref`로 호출을 합치고, 같은 `attempt_id`의 시도 행은 더 큰 `index`의 행으로 바꾼다. 다음 조회는 늘 직전 응답의 `last_index`로 한다.

```json
{
  "format": "abc2lab.e2-flow-view/v1",
  "journal": "verified",
  "last_index": 41,
  "more": false,
  "omitted_calls": 0,
  "counts": {"attempts": 12, "allowed": 9, "denied": 2, "unsupported": 1, "forward_attempted": 9,
             "responses": 8, "unknown": 1, "transient": 0, "withheld": 0, "calls": 3},
  "calls": [
    {"call_ref": "call-…", "intent_id": 4, "closed": true, "closed_index": 41,
     "attempts": [
       {"attempt_id": "…", "index": 40, "sequence": 7, "stage": "result", "method": "POST", "rule_ref": "target-path-a",
        "path_pattern": "/api/orders/{id}", "decision": "allowed", "reason": "unknown",
        "forward_attempted": true, "request_written": true, "response_received": false, "response_complete": false,
        "status_code": 0, "response_truncated": false, "at": "2026-11-13T01:02:03Z"}
     ]}
  ]
}
```

- `index`는 그 시도 행을 만든 마지막 사건 번호다(`intent`만 있으면 그 사건, `result`가 오면 `result` 사건). `closed_index`는 호출 마감 사건 번호다(아직 열려 있으면 칸 없음).
- `status_code`가 0이면 응답을 받지 못한 것이다(화면 "응답 없음").
- `path_pattern`은 그 시도의 요청 경로 패턴이다(값처럼 보이는 조각은 `{id}`로 가림, 이름 조각은 그대로, `pathmask.Readable`). E② 경계가 시도마다 실행 상태 디렉터리의 표시용 파일(`attempt-patterns.jsonl`, 감사 기록 아님)에 시도 ID와 함께 남기고, 이 조회가 시도 ID로 붙인다. 이름 조각이라도 그 실행의 보호·가림 값(비밀번호·발급 세션)이 든 조각과 결속 시험 계정 사용자명과 같은 조각은 `{id}`로 가린다(S09·절대 규칙 2). 가림 판단을 할 수 없는 시점의 시도는 줄을 남기지 않는다. 경로를 만들 수 없던 거부·기록이 빠진 시도는 비운다. 원문 경로·query는 없다.
- `stage`: `intent`(보내기 전 판정) · `result`(송신 결과) · `control`(실행 통제 사건) · `call_closed`(호출 마감).
- `decision`: `allowed` · `denied` · `unsupported`.
- `reason`: 아래 16개 중 하나. 웹 `e2flow.ts`의 옛 enum(#183 제안 값)은 이것으로 바꾼다.

| reason | 화면 분류 | 화면 문구 |
|---|---|---|
| `complete` | 허용 | 응답 받음 |
| `withheld` | 허용(응답 보류) | 응답 보류·잘림 |
| `policy_denied` | 차단 | 정책 밖(범위·메서드·위험 요청) |
| `unsupported` | 차단 | 지원 안 하는 요청 |
| `invalid` | 차단 | 잘못된 요청 |
| `budget_exhausted` | 차단 | 예산 소진 |
| `attempt_limit` · `denial_limit` · `transient_limit` · `unknown_limit` | 차단 | 호출 상한 |
| `closed` | 차단 | 실행 닫힘 |
| `expired` | 차단 | 승인 만료 |
| `revoked` | 차단 | 승인 철회 |
| `transport_failed` | 송신 실패 | 대상 연결 실패(보내지 않음) |
| `audit_failed` | 불명 | 감사 기록 실패 |
| `unknown` | 불명 | 결과 불명(대상이 받았을 수 있음) |

분류 규칙(fail-closed): "차단"은 `forward_attempted`가 false일 때만이다. 송신을 시도한 뒤의 거부·실패는 불명으로 보인다. 모르는 `reason`·`stage` 값은 불명이다. `transport_failed`는 대상에 닿지 않은 송신 실패이고, 결과 불명과 따로 보인다(C02).

## 5. 커버리지 — 1단계는 기존 워크플로 snapshot

`GET /api/product/workflows/{job}`의 snapshot `endpoints`(방문 `visited`·선택 `selected`·상태 `status`·관찰 오류 `observation_error`·발견 출처)와 `counts`로 그린다. 새 집계 API는 snapshot으로 그릴 수 없는 칸이 생기면 그때 이 문서에 더한다.

## 6. 전송 통제 정책 저장 — 화면에서 실행 정책을 직접 고치지 않는다

결정: 통제 정책 화면은 **읽기 전용**이다. "범위 바꾸기"는 대상 등록 화면으로 보낸다.

- 실행 정책은 회차마다 서버가 **대상 승인 판**(DNS 소유 증명·본인 진술로 승인한 origin·경로·메서드·쓰기 동의·요청 수·유효 시간)과 **설치 상한**에서 만들고 지문으로 결속한다. 화면이 실행 정책을 따로 고쳐 저장하면 대상 승인을 거치지 않은 범위가 생긴다(CLAUDE.md 기조: 권한 결속이 기반, 규칙 1: 승인한 대상만).
- 바꿀 수 있는 것과 길:

| 바꾸려는 것 | 길 |
|---|---|
| 경로·메서드·쓰기 동의·요청 수·유효 시간 | 대상을 새로 등록하고 소유 확인·승인을 다시 받는다(`POST /api/product/targets` → `…/approve`) |
| 제외 경로 | 점검마다 입력(`excluded_paths`) |
| 시험 계정·로그인 방식 | 1절 계정 API |
| 설치 상한(실행 상한 `runtime_limits`, 허용 포트, 사설망 대역, 되돌릴 수 있는 쓰기 허용, 탐색 설정) | 설치 입력에서만(`update.sh`). 화면 저장 없음 |

`control-policy.ts`의 편집·저장 칸은 두지 않는다. 대상 등록 패널(`TargetApprovalPanel`)이 그 역할이다.

## 7. 진입 버튼 자리

- 결과 읽기 화면 4개(접근통제 지도·커버리지·발견 상세·전송 통제 흐름)는 지금 자산 탐색 헤더의 **'진단 결과' 묶음 버튼**을 최종 자리로 한다(헤더 과밀 해소, #507 이후 그대로).
- 전송 통제 정책(승인 범위)은 결과가 아니라 범위라 묶음에 넣지 않는다. 두 곳에서 연다. (1) 점검 화면 오른쪽 '안전 장치' 칸 제목 옆 "정책 전체 보기", (2) 전송 통제 흐름 화면 헤더의 "전송 통제 정책"(지금 자리).
- 실데이터가 없는 화면(예시)은 지금처럼 예시 배너를 유지한다.

## 순서

1. 화면(이경준): 1절 계정 분류와 2절 접근통제 지도는 지금 연결한다. 4절 enum·문구와 3절 `approval_mode` 키는 서버 API 전에 바꿔 둘 수 있다.
2. 서버(최민준): 3절 `control-policy`, 4절 `e2-flow` 읽기 API를 낸다. 응답이 이 문서와 다르면 이 문서를 먼저 고친다. 구현(2026-11-13): 제품 task 경로(`urlPreparationRuntime.ServeHTTP`)가 작업 결속을 확인한 뒤 그 작업의 실행 결속 디렉터리(`runs/<job>`)에서 직접 읽는다. 정책은 승인(`authority.json`) 해시와 같고 실행에 쓴 검증기를 통과해야 하고, 흐름은 감사 키 pin을 대조한 뒤 기록 전체를 검증해야 사건을 보인다(`e2proxy.VerifyJournalEvents`). `path_pattern`은 E② 시도 관찰 훅이 제품 실행(평가 문맥 없음)에서 남긴 표시용 패턴을 시도 ID로 붙인다.
3. 수용시험 U01(쓰기 승인 때 어떤 쓰기가 나갈지 미리 보이는지): C21(#525) 뒤 범위 확인 화면이 쓰기 메서드를 보이므로 화면 담당이 돌릴 수 있다.
