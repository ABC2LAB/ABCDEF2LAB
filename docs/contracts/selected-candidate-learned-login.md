# 선택 후보 검증의 학습 로그인 인증 준비 (`candidate-llm-plan/v2`)

상태: **구현(제안 포함)**. 아래 "출처 수준 정책"은 팀 확인 전 제안이다. 확인 전에는 이 경로를 일반 제품에 켠 것으로 보지 않는다(PR은 Draft 권장).

[선택 LLM 검증 계약](selected-candidate-llm-verification.md)의 인증된 원본 재현은 지금까지 로그인 설정 9칸(`diagnosisprep.TargetLogin`) digest에만 묶였다. 9칸이 없어지면 인증 원본은 계획을 만들 수 없다. 이 계약은 [모델 로그인 계약](model-login-attempts.md) §6의 학습 방법(`learnedlogin.Method`)으로 같은 계정에 새로 로그인해 재현하는 준비를 정한다. 9칸 폐지 자체, learned 저장(⑤-B, #996), E③ verified 조건은 바꾸지 않는다.

## 1. 인증 준비 유형과 계획 판본

| 판본 | `auth_preparation.kind` | 원천 | `login_digest` | 준비 요청 |
|---|---|---|---|---|
| `candidate-llm-plan/v1` | (없음) | 로그인 설정 9칸 | 9칸 전체 JSON SHA-256 | POST 1 |
| `candidate-llm-plan/v2` | `learned_login/v1` | 학습 방법, 같은 경로 준비 또는 준비 없음 | 방법 객체 `method_sha256` | POST 1 또는 GET 1 + POST 1 |
| `candidate-llm-plan/v2` | `learned_login/v2` | 학습 방법, 별도 경로 CSRF(#987 `field_sources`) | 방법 객체 `method_sha256` | GET 1 + POST 1 |

- v1 계획의 JSON 칸·digest는 그대로다(새 칸은 v2에서만 나오고 v1 JSON에는 없음). 저장·승인·완료된 v1 계획은 재조회만으로 뜻이 바뀌지 않는다(`TestSelectedLLMPlanV1DigestUnchanged`).
- v2 `auth_preparation` 칸: `method`(POST), `exact_path`(로그인 POST 경로), `login_digest`, `kind`, `prep_method`·`prep_path`(준비 GET이 있을 때만), `requests`(1|2), `trust`(아래 출처 수준).
- 계획은 그 밖에 `account_ref`(라벨·자격 판 digest), `target_id`·`target_revision`·`target_approved_at`, `max_requests`(= 행동 수 + `requests`), `duration_seconds`(90), `expires_at`과 계획 digest로 결속된다. 비밀번호·세션·CSRF 값은 계획·승인·이력·모델 입력에 없다.
- 재현 행동 상한(GET·HEAD 3)과 정상 대조 규칙은 그대로다. 무인증 대조·신원 확인 요청은 이 판본에 없다(추가하려면 별도 승인 항목·상한부터 정한다).

## 2. 방법 선택과 우선순위

계획을 만들 때 관리 API는 보호 준비 서비스에 `target_login_candidate`(대상 ID, 원본 관찰 `request_account_ref`)를 묻는다. 서비스는 다음 순서로 하나를 돌려준다(`diagnosisprep.ProductCandidateLogin`, 값 없음).

1. 계정 참조를 지금 대상 계정으로 되돌린다. 계정이 지워졌거나 자격이 바뀌면 `none/learned_login_credential_changed`, 자격 파일이 저널 해시와 다르면 `none/learned_login_credential_unavailable`.
2. 저장된 학습 방법(⑤-B 읽기 경계 `learnedLoginMethod`, 지금 대상 판·자격 판에 묶인 것만)을 읽는다.
   - 방법이 **없을 때만**(`learned_login_none`) 9칸으로 `configured`. 9칸도 없으면 `none/learned_login_none`.
   - 대상 재승인·자격 변경·기록 손상(`learned_login_target_changed`·`learned_login_credential_changed`·`learned_login_record_invalid`)은 그 사유로 `none`. 9칸으로 넘어가지 않는다.
3. 방법이 있으면 출처 수준을 확인한다(§3). 통과하면 `learned`와 `trust`, 아니면 그 사유로 `none`(9칸으로 넘어가지 않음).

관리 API 응답: 쓸 방법이 아예 없으면 `409 candidate_auth_preparation_unavailable`, 학습 방법이 있지만 쓸 수 없으면 `409 {"error":"candidate_learned_login_unusable","reason":"<고정 사유>"}`(값 없음). 같은 계정의 최근 선택 실행이 인증 실패·결과 불명으로 연달아 3번(모델 로그인 계약 §5 기본값) 끝났으면 `reason: login_attempts_exhausted`로 새 계획을 만들지 않는다(9칸 계획 포함).

## 3. 출처 수준 정책 (제안, 팀 확인 필요)

⑤-B 저장 줄(`login_method_learned`)은 방법 모양만 담고 출처 수준을 담지 않는다. 그래서 선택 검증은 쓰기 전에 다시 확인하고, 다음 셋만 쓴다. 실행 메모리의 E② 감사 조회는 회차 뒤에 쓰지 않고, 회차 상태에 남은 보호 기록만 다시 읽는다.

| `trust` | 조건 | 확인하는 것 / 주장하지 않는 것 |
|---|---|---|
| `verified` | 같은 `method_sha256`의 `login_method_verified`가 지금 대상 판·지금 자격 판이고, 저장된 학습 시각보다 늦음 | E③ 다시 로그인 성공(신원 열림·세션 없는 대조 막힘). 저장 경계가 "방법이 지금 것이 된 시각"을 주지 않아 학습 시각과 같은 시각 또는 그 전의 검증은 보수적으로 인정하지 않는다. |
| `e2_audited` | 진단 단계 시도: 원본 회차 결속·인계 실패 표지 없음·`Select()` 같은 시도·같은 digest(아래와 같음)이고, 원본 회차 E② 감사 저널 사본(`state/events.jsonl`)을 실행 감사 키(pin)로 끝까지 검증해 같은 호출·시도 결과 사건과 `VerifyTransport`(같은 회차·모델 호출·메서드·허용·송신·응답 완료·상태·감사 완료)·`VerifyFieldSources`가 맞음 | 그 로그인 요청이 E② 승인 범위로 실제로 나가 응답을 끝까지 받았다. 경로·계정은 감사 사건에 없어 보호 기록 행의 고정 칸 대조까지만이다. 검증 키 없는 저널 절단(뒤 행 삭제)은 감지하지 못하지만 없는 송신을 만들어 내지는 못한다. |
| `exploration_observed` | 원본 회차(`source.run_ref` = `run-<작업>`)의 결속 정책이 `exploration_login_evidence: required/v2`이고, 인계 실패 표지 없음, 보호 로그인 기록의 `Select()`가 같은 시도·같은 digest, 탐색 경계 근거 v2(작업 ID·원본 digest 일치)와 `VerifyExplorationEvidence`·`VerifyFieldSources` 통과, 원본 회차가 그 계정·자격 판을 결속 | 그 로그인 요청이 경계를 실제로 지났다. 계정 신원·응답 본문 완료·E② 감사는 주장하지 않는다. |

원본 회차 재대조에서는 선택한 시도 참조·방법 digest와 함께 시도 시각도 저장 줄과 정확히 비교한다. 원본 실행 정책의 계정 자격 파일 이름에 현재 대상 ID가 결속됐는지, 로그인 규칙의 origin이 현재 승인 origin인지, 실행 시작 시각이 현재 대상 승인보다 이르지 않은지도 확인한다. 쓰지 않는 것: 탐색 근거 정책이 없는 이전 회차(`exploration_unaudited`), 감사 저널이 없거나 검증되지 않는 진단 단계 출처, 원본 회차 결속·기록·근거 유실·손상(`learned_login_record_invalid`·`learned_login_transport_unverified`).

⑤-B에 요청할 것(담당자 전달): (a) 저장 때 `ReadConfirmationHistory`(인계 실패 표지)·`VerifyTransport`/`VerifyExplorationEvidence`/`VerifyFieldSources`를 거친 시도만 남기고 그 출처 수준을 저장 줄에 남기기(#996 `5d3f7550`부터 인계 실패 표지는 막지만, 실제 송신 근거 대조 없이 `Select()` 결과를 `learned`로 남긴다 — 통합 시험 `TestLearnedStoreSkipsTransportProvenanceButSelectedRefuses`로 확인), (b) 읽기 경계가 방법이 지금 것이 된 시각(`since`)도 주기. 선택 검증의 재대조는 (a)가 들어와도 원본 기록 유실·변경을 보는 두 번째 확인으로 남긴다.

## 4. 실행 경계 (E②)

- 승인 계획마다 새 task/run과 새 E② 권한을 발급한다(기존과 같음). 원본 회차 세션·권한·모델 예산은 쓰지 않는다.
- 실행 정책 `auth`: `learned_method`(방법 객체), `rule_ref`(로그인 POST 전용 규칙, `purpose: auth_prep`, 방법의 정확한 경로·POST), `prep_rule_ref`(준비가 있을 때만, `purpose: login_prep`, 정확한 준비 경로·GET), 계정 하나, `login` 9칸은 비움, `model_login` 거짓, `selected_account_ref` 필수. 정책 검증이 이 모양만 받는다(`TestSelectedCandidateLearnedPolicyValidation`).
- `login_prep` 규칙은 선택 실행(`SelectedAccountRef`)에만 실린다(E② `NewExecution`이 거부).
- 송신 호출은 `BeginSelectedLoginCall` 하나다: 귀속은 인증 준비(`program: auth_prep`, 영수증·저널 어휘 그대로), `learned_login`(POST)·`login_prep`(GET) 목적만, **계획 전용 규칙만** 맞춘다. 선택 실행의 `auth_prep`·`login_prep` 규칙은 목적별로 각각 최대 하나다(학습 로그인 정책은 `auth_prep` 하나를 요구). Worker 규칙(계획 행동), 확정 몫(E③), 모델·Worker·`refresh_session`·처음 인증 준비 호출은 이 규칙으로 보내지 못한다. E③ 다시 로그인(`BeginE3ReloginCall`)은 지금처럼 Worker 승인 규칙만 쓴다.
- 질의·조각·제외 경로·위험 동작 단어 경로는 보내지 않는다. 실행 예산(`max_requests` = 행동 + 준비 요청)을 쓰며 넘으면 `budget_exhausted`로 보내지 않는다.
- 요청 재구성·CSRF·쿠키 경로·응답 판정·세션 추출은 `learnedlogin`(#974·#987)과 E③ 재사용 코드(`e2HTTPLearnedLoginVia`)를 그대로 쓴다. 로그인은 한 번, 자동 재시도 없음, 결과 불명은 다시 보내지 않는다.

## 5. 재검증 시점

| 시점 | 확인 |
|---|---|
| 계획 생성 | §2·§3 선택, 지금 대상 승인으로 `candidateLLMPlanStillApproved`(v2: 준비 경로·메서드·제외·접두·위험 단어·요청 수) |
| 승인 대기 중 재요청 | 같은 판본·같은 `auth_preparation`이 아니면 `409 verification_plan_changed` |
| 실행 권한 발급 전 | 승인 계획 재검사, §2·§3 다시 골라 계획의 `auth_preparation`과 같을 때만 그 계정 하나 결속 → 다르면 송신 0, `failed/candidate_auth_preparation_changed` |
| 매 송신 직전(준비 GET·로그인 POST·행동) | E② 권한 검사 훅이 원본 작업 인가·대상 승인·계획 재검사와 §2·§3 재선택 대조를 다시 본다 |

바뀐 방법·새 방법을 자동으로 골라 보내지 않는다. 재승인이 필요하면 새 선택 요청부터 다시 한다.

## 6. 계정 결속과 판정

- 실행은 계획 `account_ref`의 계정 하나만 싣는다. 9칸이 없는 대상(일반 결속이 계정을 싣지 않음)도 그 계정의 보호 자격 파일 하나만 따로 읽어 결속하고, 다른 계정 자격 사본은 메모리에서 지운다.
- 새 세션은 실행 메모리의 보호 세션 저장소에만 있고, 재현 요청에는 E②가 그 세션을 붙인다(`SelectedSessionHeaders`). 실제 송신 헤더가 그 세션과 정확히 하나 맞을 때만 관찰 `request_account_ref`를 남긴다. 학습 방법의 세션 전달 헤더(Cookie·Authorization·지원 사용자 지정 헤더)를 귀속 대조에 쓴다(9칸 `header` 칸 의존 제거).
- 인증 준비가 그 계정을 인증하지 못하면(실패·세션 없음·결과 불명·미전송) 재현 행동을 하나도 보내지 않고 `failed/candidate_auth_failed`·`candidate_auth_result_unknown`·`candidate_auth_not_sent`로 끝난다. 로그인 성공(세션 받음)은 인증 성공 확정이 아니며 E③ `login_method_verified`를 쓰지 않는다.
- 계정 참조는 시험 계정 자격·세션 연속성까지다. 대상 내부 주체 ID의 독립 증명이 필요한 주장은 근거가 없으면 `insufficient`다(#999·#1001 판정 게이트 그대로).

## 7. 고정 사유

| 사유 | 뜻 |
|---|---|
| `candidate_auth_preparation_unavailable` | 학습 방법·9칸 모두 없음 |
| `candidate_learned_login_unusable` + `reason` | 학습 방법이 있지만 쓸 수 없음(`learned_login_*`, `login_attempts_exhausted`) |
| `verification_plan_changed` | 승인 대기 중 인증 준비 원천이 바뀜 |
| `candidate_auth_preparation_changed` | 승인 뒤 실행 전에 방법·출처 수준·계정 결속이 바뀜(송신 0) |
| `candidate_auth_failed` · `candidate_auth_result_unknown` · `candidate_auth_not_sent` | 로그인 실패·결과 불명·미전송(재현 송신 0, 재시도 없음) |

## 8. 기존 기록 호환

- 저장된 v1 계획·완료 이력·판정 게이트 판본(`evidence_gate`)의 의미는 그대로다.
- 이 경로는 E③ 선언·확정 몫·verified 기록을 만들지 않는다.

## 9. 알려진 한계

- 원본 회차 디렉터리(`runs/<작업>`)를 보관 정책으로 지우면 그 방법은 `exploration_observed`를 다시 확인할 수 없어 쓰지 않는다(`verified`면 씀).
- 여러 준비 GET이 필요한 로그인, MFA·OTP·캡차, Basic 로그인은 범위 밖이다.
- 화면 표시(계획의 인증 준비 유형·준비 요청 수·출처 수준, 실패 사유)는 경준님 인계: [합성 fixture](fixtures/selected-candidate-learned-login.json).
