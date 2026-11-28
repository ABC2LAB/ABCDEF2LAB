# 선택 후보 검증의 학습 로그인 인증 준비 (`candidate-llm-plan/v2`)

상태: **구현, 측정 뒤 반영**. 정책은 2026-11-27 팀 결정(개발 계획 "#950 학습 방법 로그인과 9칸-1 후속")을 따른다: #950은 verified 방법만 쓰고, 로그인 실패·결과 불명이면 verified를 해제하며, 같은 계정의 #950 로그인은 한 번에 하나다. 개발 계획상 계정 붙은 #950은 9칸-2에서 검증 불가로 두고 이 경로는 공식 측정 뒤에 켠다. 이 문서의 2단계(A: 준비 GET + POST, `login_prep` 목적 규칙)는 E② 실행 허용 변경이라 지정 보안 리뷰 대상이다(§4).

[선택 LLM 검증 계약](selected-candidate-llm-verification.md)의 인증된 원본 재현은 지금까지 로그인 설정 9칸(`diagnosisprep.TargetLogin`) digest에만 묶였다. 9칸이 없어지면 인증 원본은 계획을 만들 수 없다. 이 계약은 [모델 로그인 계약](model-login-attempts.md) §6의 학습 방법(`learnedlogin.Method`)으로 같은 계정에 새로 로그인해 재현하는 준비를 정한다. 9칸 폐지 자체, learned 저장(⑤-B, #996), E③ verified 조건은 바꾸지 않는다.

## 1. 인증 준비 유형과 계획 판본

| 판본 | `auth_preparation.kind` | 원천 | `login_digest` | 준비 요청 |
|---|---|---|---|---|
| `candidate-llm-plan/v1` | (없음) | 로그인 설정 9칸 | 9칸 전체 JSON SHA-256 | POST 1 |
| `candidate-llm-plan/v2` | `learned_login/v1` | 학습 방법, 같은 경로 준비 또는 준비 없음 | 방법 객체 `method_sha256` | POST 1 또는 GET 1 + POST 1 |
| `candidate-llm-plan/v2` | `learned_login/v2` | 학습 방법, 별도 경로 CSRF(#987 `field_sources`) | 방법 객체 `method_sha256` | GET 1 + POST 1 |

- v1 계획의 JSON 칸·digest는 그대로다(새 칸은 v2에서만 나오고 v1 JSON에는 없음). 저장·승인·완료된 v1 계획은 재조회만으로 뜻이 바뀌지 않는다(`TestSelectedLLMPlanV1DigestUnchanged`).
- v2 `auth_preparation` 칸: `method`(POST), `exact_path`(로그인 POST 경로), `login_digest`, `kind`, `prep_method`·`prep_path`(준비 GET이 있을 때만), `requests`(1|2), `trust`(늘 `verified`), `verified_ref`(그 방법을 검증한 E③ `login_method_verified` 줄의 비밀 없는 참조, §3). 검증이 해제되거나 다른 검증 사건으로 바뀌면 승인 계획과 달라져 실행하지 않는다.
- 계획은 그 밖에 `account_ref`(라벨·자격 판 digest), `target_id`·`target_revision`·`target_approved_at`, `max_requests`(= 행동 수 + `requests`), `duration_seconds`(90), `expires_at`과 계획 digest로 결속된다. 비밀번호·세션·CSRF 값은 계획·승인·이력·모델 입력에 없다.
- 재현 행동 상한(GET·HEAD 3)과 정상 대조 규칙은 그대로다. 무인증 대조·신원 확인 요청은 이 판본에 없다(추가하려면 별도 승인 항목·상한부터 정한다).

## 2. 방법 선택과 우선순위

계획을 만들 때 관리 API는 보호 준비 서비스에 `target_login_candidate`(대상 ID, 원본 관찰 `request_account_ref`)를 묻는다. 서비스는 다음 순서로 하나를 돌려준다(`diagnosisprep.ProductCandidateLogin`, 값 없음).

1. 계정 참조를 지금 대상 계정으로 되돌린다. 계정이 지워졌거나 자격이 바뀌면 `none/learned_login_credential_changed`, 자격 파일이 저널 해시와 다르면 `none/learned_login_credential_unavailable`.
2. 저장된 학습 방법(⑤-B 읽기 경계 `learnedLoginMethod`, 지금 대상 판·자격 판에 묶인 것만)을 읽는다.
   - 저장 경계가 없는 빌드이거나 방법이 **없을 때만**(`learned_login_none`) 9칸으로 `configured`(전환 기간 호환, 9칸-2에서 없어짐). 9칸도 없으면 `none/learned_login_none`.
   - 대상 재승인·자격 변경·기록 손상(`learned_login_target_changed`·`learned_login_credential_changed`·`learned_login_record_invalid`)은 그 사유로 `none`. 9칸·다른 계정으로 넘어가지 않는다.
3. 방법이 있으면 그 방법이 지금 것이 된 시각(`since`)을 같은 읽기로 받는다(`learnedLoginMethodState`). 이 읽기 경계가 없거나 실패하면 `none/learned_login_verification_unavailable`(9칸으로 넘어가지 않음).
4. verified인지 본다(§3). 아니면 `none/learned_login_unverified`. 같은 계정의 다른 #950 로그인이 진행 중이면 `none/candidate_login_in_progress`, 앞선 로그인의 결과·해제를 남기지 못했으면 `none/candidate_login_unresolved`(§5-1).
5. verified 방법의 원본 시도가 지금도 원본 회차 보호 기록과 맞는지 다시 본다(§3 표의 출처 재대조). 맞지 않으면 그 사유로 `none`. 통과하면 `learned`, `trust: verified`, `verified_ref`.

관리 API 응답: 쓸 방법이 아예 없으면 `409 candidate_auth_preparation_unavailable`, 학습 방법이 있지만 쓸 수 없으면 `409 {"error":"candidate_learned_login_unusable","reason":"<고정 사유>"}`(값 없음). 같은 계정의 최근 선택 실행이 인증 실패·결과 불명으로 연달아 3번(모델 로그인 계약 §5 기본값) 끝났으면 `reason: login_attempts_exhausted`로 새 계획을 만들지 않는다(9칸 계획 포함).

## 3. verified 정책과 출처 재대조

#950은 verified 방법만 쓴다(2026-11-27 팀 결정: learned는 가입·탈퇴 POST를 거르지 못함). verified는 다음을 모두 만족하는 `login_method_verified` 줄이 있다는 뜻이다(`loginMethodVerifiedFor`, 계정 목록 API의 `verified` 상태와 같은 판독).

- 지금 학습 방법과 같은 `method_sha256`, 지금 대상 판, 지금 자격 판.
- 그 방법이 지금 것이 된 시각(`since`)보다 **늦음**. 관찰 시각(`at`)이 아니라 `since`다: 같은 방법을 다시 배우면 `at`만 늦어지고 `since`는 그대로라 그 사이의 검증도 인정하고, 다른 방법으로 바뀌었다 돌아오면 `since`가 새로 정해져 옛 검증을 인정하지 않는다. 같은 시각의 검증은 순서를 추정하지 않고 인정하지 않는다.
- 선택 후보 로그인 실패·결과 불명으로 해제되지 않음(§5-1). 해제 뒤 E③가 다시 검증하면 새 검증 줄로 다시 verified가 된다(선택 후보 로그인 성공은 verified를 만들지 않음).

출처 재대조는 verified를 대신하지 않는다. verified 방법의 원본 시도가 지금도 원본 회차 보호 기록과 맞는지 보는 추가 확인이다. 회차 상태에 남은 보호 기록만 다시 읽고 실행 메모리의 E② 감사 조회는 회차 뒤에 쓰지 않는다.

| 원본 시도 | 출처 재대조 조건 | 확인하는 것 / 주장하지 않는 것 |
|---|---|---|
| 진단 단계(`e2_audited`) | 원본 회차 결속·인계 실패 표지 없음·`Select()` 같은 시도·같은 digest(아래와 같음)이고, 원본 회차 E② 감사 저널 사본(`state/events.jsonl`)을 실행 감사 키(pin)로 끝까지 검증해 같은 호출·시도 결과 사건과 `VerifyTransport`(같은 회차·모델 호출·메서드·허용·송신·응답 완료·상태·감사 완료)·`VerifyFieldSources`가 맞음 | 그 로그인 요청이 E② 승인 범위로 실제로 나가 응답을 끝까지 받았다. 경로·계정은 감사 사건에 없어 보호 기록 행의 고정 칸 대조까지만이다. 검증 키 없는 저널 절단(뒤 행 삭제)은 감지하지 못하지만 없는 송신을 만들어 내지는 못한다. |
| 탐색 단계(`exploration_observed`) | 원본 회차(`source.run_ref` = `run-<작업>`)의 결속 정책이 `exploration_login_evidence: required/v2`이고, 인계 실패 표지 없음, 보호 로그인 기록의 `Select()`가 같은 시도·같은 digest, 탐색 경계 근거 v2(작업 ID·원본 digest 일치)와 `VerifyExplorationEvidence`·`VerifyFieldSources` 통과, 원본 회차가 그 계정·자격 판을 결속 | 그 로그인 요청이 경계를 실제로 지났다. 계정 신원·응답 본문 완료·E② 감사는 주장하지 않는다. |

원본 회차 재대조에서는 선택한 시도 참조·방법 digest와 함께 시도 시각도 저장 줄과 정확히 비교한다. 원본 실행 정책의 계정 자격 파일 이름에 현재 대상 ID가 결속됐는지, 로그인 규칙의 origin이 현재 승인 origin인지, 실행 시작 시각이 현재 대상 승인보다 이르지 않은지도 확인한다. 쓰지 않는 것: 탐색 근거 정책이 없는 이전 회차(`exploration_unaudited`), 감사 저널이 없거나 검증되지 않는 진단 단계 출처, 원본 회차 결속·기록·근거 유실·손상(`learned_login_record_invalid`·`learned_login_transport_unverified`).

⑤-B에 요청할 것(담당자 전달): (b′) 읽기 경계 `learnedLoginMethodState(대상, 라벨, 판, 자격) (방법·출처, since, 사유)` — 방법과 `since`를 한 잠금 안에서 함께 주기(두 번 읽으면 그 사이 방법이 바뀔 수 있음). 이 PR은 인터페이스만 두고, #996 저장 상태(`loginLearned.since`) 위의 구현은 통합 브랜치에서 시험했다. (c) 같은 시각 사건의 순서를 시각 비교로 정하지 않도록 저장 줄에 저널 순서(세대) 칸을 두는 것(지금은 같은 시각 검증을 인정하지 않는 보수 판정). (a) 저장 때 `ReadConfirmationHistory`(인계 실패 표지)·`VerifyTransport`/`VerifyExplorationEvidence`/`VerifyFieldSources`를 거친 시도만 남기고 그 출처 수준을 저장 줄에 남기기(#996 `5d3f7550`부터 인계 실패 표지는 막지만, 실제 송신 근거 대조 없이 `Select()` 결과를 `learned`로 남긴다 — 통합 시험 `TestLearnedStoreSkipsTransportProvenanceButSelectedRefuses`로 확인), (b) 읽기 경계가 방법이 지금 것이 된 시각(`since`)도 주기. 선택 검증의 재대조는 (a)가 들어와도 원본 기록 유실·변경을 보는 두 번째 확인으로 남긴다.

## 4. 실행 경계 (E②)

- 승인 계획마다 새 task/run과 새 E② 권한을 발급한다(기존과 같음). 원본 회차 세션·권한·모델 예산은 쓰지 않는다.
- 실행 정책 `auth`: `learned_method`(방법 객체), `rule_ref`(로그인 POST 전용 규칙, `purpose: auth_prep`, 방법의 정확한 경로·POST), `prep_rule_ref`(준비가 있을 때만, `purpose: login_prep`, 정확한 준비 경로·GET), 계정 하나, `login` 9칸은 비움, `model_login` 거짓, `selected_account_ref` 필수. 정책 검증이 이 모양만 받는다(`TestSelectedCandidateLearnedPolicyValidation`).
- `login_prep` 규칙은 선택 실행(`SelectedAccountRef`)에만 실린다(E② `NewExecution`이 거부).
- 송신 호출은 `BeginSelectedLoginCall` 하나다: 귀속은 인증 준비(`program: auth_prep`, 영수증·저널 어휘 그대로), `learned_login`(POST)·`login_prep`(GET) 목적만, **계획 전용 규칙만** 맞춘다. 선택 실행의 `auth_prep`·`login_prep` 규칙은 목적별로 각각 최대 하나다(학습 로그인 정책은 `auth_prep` 하나를 요구). Worker 규칙(계획 행동), 확정 몫(E③), 모델·Worker·`refresh_session`·처음 인증 준비 호출은 이 규칙으로 보내지 못한다. E③ 다시 로그인(`BeginE3ReloginCall`)은 지금처럼 Worker 승인 규칙만 쓴다.
- 질의·조각·제외 경로·위험 동작 단어 경로는 보내지 않는다. 실행 예산(`max_requests` = 행동 + 준비 요청)을 쓰며 넘으면 `budget_exhausted`로 보내지 않는다.
- 단계 구분(개발 계획 "#950 학습 방법 로그인과 9칸-1 후속"): D단계는 준비 없는 방법(로그인 POST 하나, `auth_prep` 규칙 하나), A단계는 준비 GET + POST(`login_prep` 목적 규칙 추가)다. 이 구현은 두 단계 모두 `BeginSelectedLoginCall` 하나로 보낸다. 그래서 D단계도 "E② 허용 변경 없음"은 아니다: 새 호출 종류(선택 실행 전용, `auth_prep` 귀속)와 그 전용 규칙 대조(`matchSelectedLoginLocked`)가 E② 경계에 더해진다. A단계는 여기에 `login_prep` 목적(GET, 정확 경로, 선택 실행에서만, 목적별 하나)을 더한다. 둘 다 지정 보안 리뷰 대상이다. 기존 처음 인증 준비(`BeginProgramCall auth_prep`)·E③ 다시 로그인·Worker 경로의 허용 범위는 바뀌지 않는다(시험 `e2proxy/selected_login_test.go`).
- 요청 재구성·CSRF·쿠키 경로·응답 판정·세션 추출은 `learnedlogin`(#974·#987)과 E③ 재사용 코드(`e2HTTPLearnedLoginVia`)를 그대로 쓴다. 로그인은 한 번, 자동 재시도 없음, 결과 불명은 다시 보내지 않는다.

## 5. 재검증 시점

| 시점 | 확인 |
|---|---|
| 계획 생성 | §2·§3 선택, 지금 대상 승인으로 `candidateLLMPlanStillApproved`(v2: 준비 경로·메서드·제외·접두·위험 단어·요청 수) |
| 승인 대기 중 재요청 | 같은 판본·같은 `auth_preparation`이 아니면 `409 verification_plan_changed` |
| 실행 권한 발급 전 | 승인 계획 재검사, §2·§3 다시 골라 계획의 `auth_preparation`(`verified_ref` 포함)과 같을 때만 그 계정 하나 결속 → 다르면 송신 0, `failed/candidate_auth_preparation_changed`(진행 중·미해결 예약이면 `candidate_login_in_progress`·`candidate_login_unresolved`) |
| E② 실행 시작 직전 | 그 계정 선택 로그인 예약(§5-1). 예약을 남기지 못하면 송신 0 |
| 매 송신 직전(준비 GET·로그인 POST·행동) | E② 권한 검사 훅(`CheckAuthority`)이 원본 작업 인가·대상 승인·계획 재검사와 §2·§3 재선택 대조를 다시 본다. 이 실행이 잡은 예약이 그대로이고 그 verified가 해제·교체되지 않았을 때만 통과 |

바뀐 방법·새 방법을 자동으로 골라 보내지 않는다. 재승인이 필요하면 새 선택 요청부터 다시 한다.

남는 경쟁: 검사와 실제 송신 사이(E② 권한 확인 뒤 소켓 쓰기까지)에 해제·철회가 일어나면 그 요청 하나는 나갈 수 있다. 이미 보낸 요청은 되돌리지 않는다. 확인한 뒤의 다음 송신부터 막는다. 같은 계정의 해제는 그 계정 예약을 잡은 실행만 쓰므로, 같은 프로세스 안에서 선택 로그인끼리 해제가 엇갈리지 않는다.

### 5-1. verified 해제와 같은 계정 한 번에 하나

계정 저널(`productAccountEvent`)에 줄 셋을 더한다(값 없음, `reservation_ref` = `candidate-<요청 ID>`).

| `action` | 언제 | 칸 |
|---|---|---|
| `candidate_login_reserved` | E② 실행 시작 직전. 그 계정에 열린 예약이 없고 계획의 `verified_ref`가 지금 verified 줄과 같을 때만 | `method_sha256`, `credential_sha256`, 검증 줄의 `run_ref`·`attempt_ref`, `stage: confirmation`, `reservation_ref` |
| `candidate_login_finished` | 로그인 미전송(`not_sent`), 로그인과 첫 인증 읽기까지 막힘 없음(`authenticated`), 예약 뒤 더 새 검증이 생김(`superseded`) | `evidence`, `reservation_ref` |
| `login_method_verified_cleared` | 아래 해제 조건. 예약의 검증 줄이 지금도 그 방법의 마지막 검증일 때만(늦게 온 실패가 더 새 검증을 지우지 않음, 아니면 `superseded` 종료) | 예약과 같은 칸 + `evidence`(해제 사유) |

해제 조건과 사유(확신이 없으면 해제):

| 상황 | 처리 |
|---|---|
| 로그인 POST를 보낸 뒤 400 이상·로그인 화면 재등장(비밀번호 칸·로그인 경로 리다이렉트)·세션 없음 | 해제 `candidate_login_failed` |
| 로그인 POST를 보낸 뒤 결과 불명(전달 시작했지만 기록이 not_sent인 경우 포함) | 해제 `candidate_login_result_unknown` |
| 로그인 직후 이 계정의 첫 인증 읽기(가장 앞선 재현·대조 관찰)가 401·403 | 해제 `candidate_first_read_denied` |
| 로그인은 됐지만 첫 인증 읽기를 끝까지 받지 못함(대조 못 세움) | 해제 `candidate_auth_unconfirmed` |
| 예약을 잡은 프로세스가 결과를 남기기 전에 사라짐(재시작 뒤 남은 예약) | 해제 `candidate_login_interrupted` |
| 승인 거절·예산 부족·송신 전 권한 철회·송신 전 취소·준비 GET만 보냄 | 유지(`not_sent`) |

송신 여부는 E② 학습 로그인 결과(로그인 POST 영수증의 전달 시작·기록 칸)로 정한다. 서버가 실행 시작의 학습 로그인 바로 뒤, 권한 재확인보다 먼저 실행기에 결과를 넘긴다(`SelectedLogin` 훅). 과거 verified 줄은 지우거나 고치지 않고 현재 유효성만 바꾼다. 해제 줄이 있으면 계정 목록 API의 상태는 `verified`에서 `learned`로 돌아간다(새 `cleared_reason` 값은 없음).

같은 대상·계정(안정 식별자: 대상 ID + 라벨. 자격 판·방법 digest는 따로 대조)의 선택 로그인은 한 번에 하나다. 열린 예약이 있으면 두 번째 계획·실행은 `candidate_login_in_progress`로 멈춘다. 결과(종료·해제 줄)를 남기지 못하면 예약은 열린 채 남아 그 계정만 `candidate_login_unresolved`로 막힌다(다른 계정은 그대로). 재시작 뒤 이 프로세스가 잡지 않은 열린 예약은 결과 불명으로 보고 해제 줄(`candidate_login_interrupted`)을 남긴 뒤에만 닫는다. 그 기록도 못 남기면 계속 막는다. 계정 저널은 디렉터리 배타 잠금으로 한 프로세스만 쓰므로 "이 프로세스가 잡은 예약" 판단이 다른 프로세스의 예약을 착각하지 않는다.

## 6. 계정 결속과 판정

- 실행은 계획 `account_ref`의 계정 하나만 싣는다. 9칸이 없는 대상(일반 결속이 계정을 싣지 않음)도 그 계정의 보호 자격 파일 하나만 따로 읽어 결속하고, 다른 계정 자격 사본은 메모리에서 지운다.
- 새 세션은 실행 메모리의 보호 세션 저장소에만 있고, 재현 요청에는 E②가 그 세션을 붙인다(`SelectedSessionHeaders`). 실제 송신 헤더가 그 세션과 정확히 하나 맞을 때만 관찰 `request_account_ref`를 남긴다. 학습 방법의 세션 전달 헤더(Cookie·Authorization·지원 사용자 지정 헤더)를 귀속 대조에 쓴다(9칸 `header` 칸 의존 제거).
- 인증 준비가 그 계정을 인증하지 못하면(실패·세션 없음·결과 불명·미전송) 재현 행동을 하나도 보내지 않고 `failed/candidate_auth_failed`·`candidate_auth_result_unknown`·`candidate_auth_not_sent`로 끝난다. 로그인 성공(세션 받음)은 인증 성공 확정이 아니며 E③ `login_method_verified`를 쓰지 않는다. 실패·결과 불명이면 §5-1대로 verified를 해제한다.
- 로그인 응답 판정: 400 이상, HTML 비밀번호 칸, 같은 origin 로그인 경로 리다이렉트는 세션 쿠키가 와도 실패다(학습 방법 `learnedlogin.CheckLogin`, 9칸 인증 준비 `authExtractor` 같은 신호). 학습 방법은 배운 세션 이름을 이 응답이 새로 줘야 하고(`set_cookie`), 토큰 자리는 토큰 모양이어야 한다. 이름이 같은 익명 방문자 쿠키는 응답만으로 가르지 못하므로 첫 인증 읽기(401·403)로 한 번 더 본다. 신원 확인 요청은 계획에 없어 더하지 않는다.
- 계정 참조는 시험 계정 자격·세션 연속성까지다. 대상 내부 주체 ID의 독립 증명이 필요한 주장은 근거가 없으면 `insufficient`다(#999·#1001 판정 게이트 그대로).

## 7. 고정 사유

| 사유 | 뜻 |
|---|---|
| `candidate_auth_preparation_unavailable` | 학습 방법·9칸 모두 없음 |
| `candidate_learned_login_unusable` + `reason` | 학습 방법이 있지만 쓸 수 없음(`learned_login_*`, `login_attempts_exhausted`) |
| `verification_plan_changed` | 승인 대기 중 인증 준비 원천이 바뀜 |
| `candidate_auth_preparation_changed` | 승인 뒤 실행 전에 방법·검증 사건(`verified_ref`, 해제 포함)·계정 결속이 바뀜(송신 0) |
| `candidate_login_in_progress` · `candidate_login_unresolved` | 같은 계정의 다른 선택 로그인이 진행 중 · 앞선 선택 로그인의 결과·해제를 남기지 못함(송신 0) |
| `learned_login_unverified` · `learned_login_verification_unavailable` (`candidate_learned_login_unusable`의 `reason`) | 학습 방법은 있지만 verified가 아님(learned뿐·해제됨) · `since` 읽기 경계가 없거나 읽지 못함 |
| `candidate_auth_failed` · `candidate_auth_result_unknown` · `candidate_auth_not_sent` | 로그인 실패·결과 불명·미전송(재현 송신 0, 재시도 없음) |

## 8. 기존 기록 호환

- 저장된 v1 계획·완료 이력·판정 게이트 판본(`evidence_gate`)의 의미는 그대로다.
- 이 경로는 E③ 선언·확정 몫·verified 기록을 만들지 않는다.

## 9. 알려진 한계

- 원본 회차 디렉터리(`runs/<작업>`)를 보관 정책으로 지우면 verified 방법이라도 출처 재대조를 못 해 쓰지 않는다(안전 측).
- 첫 인증 읽기의 로그인 화면 복귀(200 + 로그인 폼, 로그인 경로 리다이렉트)는 재현 관찰 projection에 본문·Location이 없어 이 경로에서 따로 보지 않는다(401·403만). 계획한 재현 행동이 정당하게 403을 받는 경우도 해제된다(확신이 없으면 해제, E③ 다시 검증으로 회복).
- 여러 준비 GET이 필요한 로그인, MFA·OTP·캡차, Basic 로그인은 범위 밖이다.
- 화면 표시(계획의 인증 준비 유형·준비 요청 수·출처 수준, 실패 사유)는 경준님 인계: [합성 fixture](fixtures/selected-candidate-learned-login.json).
