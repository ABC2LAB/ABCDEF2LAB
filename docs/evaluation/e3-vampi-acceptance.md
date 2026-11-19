# E③ 실제 검증 — VAmPI (공식 측정 Go 조건 1 준비)

**상태: 준비만 해 둠(실행하지 않음).** 제품 회차라 모델 단계에서 유료 모델 호출이 든다. E② 영수증·저널 v3가 들어오는 날 바로 돌릴 수 있게 시나리오·기대 결과·집계를 고정해 둔다. 가짜 v3로 돌릴 수 있는 부분은 미리 돌렸다(§8).

근거: [개발 계획](../development-plan.md) "공식 측정 Go/No-Go 조건" 1, [E③ 확정 경로 결정](../e3-confirm-path-decision.md), [제품 확정 단계 인터페이스](../contracts/e3-product-confirmation-interface.md), [E③↔E② v3 계약](../contracts/e3-e2-v3-journal-contract.md).

## 1. 무엇을 확인하나

Go 조건 1: E③ 제품 경로 통합과 VAmPI 실제 검증, 반례 포함, 거짓 CONFIRMED 0건. 조건의 반례 목록과 시나리오(§6)의 대응:

| Go 조건 1 항목 | 시나리오 |
| --- | --- |
| 선언·시험 계정이 없을 때 확정 단계 비활성 | S03 선언 없음, S04 계정 미인증 |
| A/B 신원 확인 실패 | S05 같은 사용자, S06 신원 확인 안 됨 |
| 표식 검사 미지원·부분 응답·결과 불명 | S07, S14, S15 |
| 정책·실행·세대 불일치 | S08 정책, S16 실행 결속, S09 세대 |
| 승인 거부, 확정 단계 예산 소진 | S10, S11 |
| 다음 회차의 이전 표식 잔존, 리셋 확인 실패 | S17 표식 잔존, S12 리셋 확인 없음 |
| 거짓 CONFIRMED 0건 | 모든 시나리오(§7 집계) |
| (양성 대조) 정상 확정 | S01 |
| (반대 대조) 고쳐진 대상 | S02 |
| (저널) 관찰과 봉인 이벤트 불일치 | S13 |

## 2. 실행 전 조건

- E② v3(이동찬): 목적별 프로그램 요청, 검증된 v3 이벤트 조회, 정본 head 게시 뒤 반환(v3 계약 §5·§9). 일반 v3 저널 작성·검증(#763)은 dev에 있고, 현재 head 저장 코어·오프라인 연속성 검증은 리뷰 중이며, 제품 Transport·Journal 연결과 E③ 프로그램 송신은 아직 없다. 그 전에는 제품 회차가 `marker_registration_failed` 또는 `not_wired`로 끝나 이 문서의 시나리오가 의미 없다.
- E③ 쪽(최민준): v3 어댑터 `e3contract/e3v3`(#760), 세대·인스턴스 참조 발급(`e3-target-refs.json`, 확정 시작 때 `target_changed` 대조, #761)은 dev에 있다.
- 객체 ID 출처: VAmPI는 요청 본문 칸 ID(`create.id_field`)를 쓴다(2026-11-19 사용자 결정 (가), §5). 계약 #757·구현 #766이 dev에 있어 이 선언을 제품에 저장할 수 있다(선언 화면의 ID 출처 입력 칸은 이경준 후속).

## 3. 대상 준비

- 이미지: `deploy/eval/practice/vampi.Dockerfile`(원본 VAmPI에 DB를 미리 채움). 회차마다 새 컨테이너로 띄운다(상태 변경 재현 전 리셋, CLAUDE.md 규칙 6). 대상 쓰기를 남기는 회차 뒤에는 제품의 리셋 확인(`POST /api/product/targets/{id}/reset-confirmation`)을 컨테이너를 새로 띄운 뒤에만 한다(S17은 일부러 안 띄우고 확인).
- 환경 변수: `vulnerable=1`(BOLA 있음). S02는 `vulnerable=0`인 별도 인스턴스(교차 조회가 404). `tokentimetolive`는 기본 60초 그대로 둔다. 제품 E²의 세션 다시 받기(`refresh_session`)로 이어지는지가 이 검증에 포함된다. 끊기면 그 회차 기록에 남기고 값은 바꾸지 않는다(바꾸면 결정안 §14.1처럼 회차 조건에 적는다).
- 대상 등록: 제품 대상 등록·DNS 증명·승인에서 경로 접두 `/`, 메서드 GET·POST, 쓰기 동의와 리셋할 수 있는 쓰기(`reversible_writes`)를 켠다(결정 §3 조건 1). 확정 몫은 승인된 실행 정책에서 서버가 계산한다.

## 4. 시험 계정 둘 등록 절차

계정 비밀번호는 저장소 밖 보호 파일(0600)에서만 읽고 명령줄·로그·회차 기록·이 문서에 남기지 않는다(CLAUDE.md 규칙 2).

1. VAmPI에 사용자 둘을 만든다(운영자, 회차 밖): `POST /users/v1/register`에 `username`·`password`·`email`. **`admin` 칸은 넣지 않는다.** VAmPI는 이 칸으로 관리자 계정을 만들 수 있어서(mass assignment), 넣으면 두 계정의 권한 등급이 달라질 수 있다.
2. 두 계정으로 로그인해 `GET /me`의 `data.admin`이 둘 다 거짓인지 확인한다. 이것이 선언의 "같은 권한 등급" 확인(`accounts_same_privilege`)의 근거다.
3. 제품에 로그인 방식을 둔다: `PUT /api/product/targets/{id}/login`에 `{"method":"POST","path":"/users/v1/login","format":"json","username_field":"username","password_field":"password","json_field":"auth_token","header":"Authorization","prefix":"Bearer "}`.
4. 계정 라벨 둘을 등록한다: `POST /api/product/targets/{id}/accounts`에 `{"label":"vampi-a"}`, `{"label":"vampi-b"}`(처음엔 실계정으로 분류).
5. 대상 승인자가 둘을 시험 계정으로 표시한다: `POST …/accounts/{label}/mark-test`에 `{"statement_confirmed": true}`.
6. 자격을 넣는다: `PUT …/accounts/{label}/credential`에 `{"username": …, "password": …}`(보호 파일에서 읽어 요청 본문으로만).
7. 정책 선언을 저장한다: `PUT /api/product/targets/{id}/declaration`에 `{"target_revision": <지금 판>, "declaration": <scripts/eval/e3_vampi/declaration.json>}`.

## 5. 정책 선언(`scripts/eval/e3_vampi/declaration.json`)

- 신원: `GET /me`, 사용자 식별자 `data.username`.
- 자원 종류 `book`(소유자 전용, 소유자 전용 칸 `secret`): 생성 `POST /books/v1` JSON, 본문 틀 `{}`, 표식 칸 `secret`, ID 칸 `book_title`(요청 본문 칸 ID); 조회 `GET /books/v1/{id}`, 정상 판정은 HTTP 상태(상태 칸 없음).
- 역할: 소유(A) `vampi-a`, 교차(B) `vampi-b`, 같은 권한 등급 확인 참.
- **객체 ID 출처**: VAmPI 책의 ID는 생성 요청 본문의 `book_title`이고 생성 응답에는 ID가 없다(`{"status":"success","message":"Book has been added."}`). 그래서 응답 경로(`id_path`)가 아니라 요청 본문 칸(`id_field`)으로 선언한다. 확정 단계가 회차마다 표식과 따로 새 난수 ID(`e3id-…`)를 만들어 `book_title`에 넣고, 같은 ID로 `GET /books/v1/{id}`를 조회한다. ID는 조회 경로·로그·쓰기 기록에 남지만 비밀이 아니고, 표식(`secret`)은 계속 비밀이다.
- ID가 회차마다 새 난수라 같은 제목 충돌은 거의 없다. 충돌하면 VAmPI가 `Book Already exists!`(400)로 거부하고 그 종류는 UNDECIDABLE(`product_create_failed`)이다. 다른 ID로 다시 보내지 않는다(S18). 쓰기 기록에는 그 ID가 남아 리셋 확인 때 조회해 볼 수 있다.

## 6. 시나리오와 기대 결과

정본은 `scripts/eval/e3_vampi/scenarios.json`이다(가짜 v3 시험과 집계 스크립트가 같은 파일을 읽는다). 상태·사유는 정확히 맞아야 하고, 판정은 종류별 값과 사유 포함으로 본다. "요청 0"은 확정 단계 대상 요청이 0건이어야 한다는 뜻이다.

| ID | 시나리오 | 실제 회차에서 만드는 방법 | 기대 상태 / 사유 | `book` 판정 | 요청 0 | 실제 |
| --- | --- | --- | --- | --- | --- | --- |
| S01 | 정상 확정 | 기본 설정 | `completed` | confirmed | | 가능 |
| S02 | 고쳐진 대상 | `vulnerable=0` 인스턴스 | `completed` | undecidable(`data_no_match`) | | 가능 |
| S03 | 선언 없음 | 선언 저장 안 함 | `disabled` / `declaration_missing` | 없음 | ✓ | 가능 |
| S04 | 계정 미인증 | B 자격에 틀린 비밀번호 | `disabled` / `test_accounts_unavailable` | 없음 | ✓ | 가능 |
| S05 | A/B 같은 사용자 | 두 라벨에 같은 VAmPI 계정 자격 | `completed` | undecidable(`product_subjects_identical`) | | 가능 |
| S06 | 신원 확인 안 됨 | 신원 응답에서 식별자를 못 찾음 | `completed` | undecidable(`product_identity_unverified`) | | 가짜만 |
| S07 | 표식 미지원 응답 | 대상 변형 필요 | `completed` | undecidable(`product_data_encoding_unsupported`) | | 가짜만 |
| S08 | 정책 불일치 | 동결 선언과 정책 출처 digest가 다름 | `confirmation_integrity_failed` / `policy_digest_mismatch` | 없음 | ✓ | 가짜만 |
| S09 | 세대 불일치 | 실행 시작 뒤 대상 상태 변경 | `start_refused` / `target_changed` | 없음 | ✓ | 가짜만(통합 시험 있음) |
| S10 | 승인 거부 | 사용자 승인 모드에서 생성 요청 거부 | `completed` | undecidable(`product_create_approval_denied`) | | 가능 |
| S11 | 확정 몫 소진 | 몫을 줄이는 설정 없음 | `completed` | undecidable(`product_confirmation_budget_exhausted`) | | 가짜만 |
| S12 | 리셋 확인 없이 다음 회차 | 쓰기 남긴 회차 뒤 리셋 확인 없이 다시 | `start_refused` / `reset_confirmation_missing` | 없음 | ✓ | 가능(ID 모름 쓰기로도) |
| S13 | 저널 대조 실패 | E² 기록 변조 필요 | `confirmation_integrity_failed` / `journal_event_mismatch` | 없음 | | 가짜만 |
| S14 | 부분 응답 | 대상 변형 필요 | `completed` | undecidable(`product_data_partial_response`) | | 가짜만 |
| S15 | 결과 불명 | 응답을 다 받지 못함 | `completed` | undecidable(`product_request_result_unknown`) | | 가짜만 |
| S16 | 실행 결속 불일치 | E² 기록 변조 필요 | `confirmation_integrity_failed` / `binding_mismatch` | 없음 | | 가짜만 |
| S17 | 이전 표식 잔존 | 컨테이너를 새로 띄우지 않고 리셋 확인 뒤 다시 | `start_refused` / `previous_markers_present` | 없음 | ✓ | 가능(잔존 점검 = E² 조회) |
| S18 | ID 충돌 | 같은 제목이 이미 있음(난수라 실제로는 만들기 어려움) | `completed` | undecidable(`product_create_failed`), 생성 1번 | | 가짜만 |

- "가짜만"은 실제 VAmPI로 그 상황을 만들 수 없거나(대상 응답 인코딩·부분 응답), E② 기록을 바꿔야 하는 경우다. 가짜 v3 사전 실행(§8)과 제품·감독자 통합 시험으로 대신한다.
- S12는 S01 회차가 남긴 쓰기(ID와 함께)로 만든다.

## 7. 집계와 거짓 CONFIRMED

```
python3 -I scripts/eval/e3_vampi_tally.py --manifest scripts/eval/e3_vampi/scenarios.json --results <결과 폴더> --mode real
```

- 결과 배치: 회차마다 제품의 `runs/<job>/e3-confirmation.json`을 `<결과 폴더>/<시나리오 ID>/e3-confirmation.json`으로 복사한다. 회차 디렉터리의 다른 파일(로그·저널·근거)은 집계에 쓰지 않는다.
- 거짓 CONFIRMED: 기대가 confirmed가 아닌 종류의 confirmed 판정, 또는 confirmed 판정이 가리키지 않는 confirmed 후보(E③ 계약 §9). 1건이라도 있으면 종료 코드 1이고 Go 조건 1 불충족이다.
- 결과 파일이 없거나 읽지 못하면 그 시나리오는 미수집이고 통과로 세지 않는다(종료 코드 2).
- `--mode real`: `real: fake_only`는 건너뛰고, `blocked_*`(지금은 없음)는 결과가 있으면 거짓 CONFIRMED만 센다. `--mode fake`: 가짜 v3 출력 전체를 비교한다.

## 8. 가짜 v3 사전 실행

```
E3_REHEARSAL_OUT=<폴더> go test -run TestAcceptanceRehearsal ./e3contract/e3v3/
python3 -I scripts/eval/e3_vampi_tally.py --manifest scripts/eval/e3_vampi/scenarios.json --results <폴더> --mode fake
```

- `TestAcceptanceRehearsal`은 선언·시나리오 목록을 데이터 파일에서 읽어 가짜 v3 E²와 어댑터로 시나리오마다 확정 단계를 돌리고 기대와 대조한다. 선언은 `e3contract.Declaration.Validate`를 통과해야 한다.
- 결과(2026-11-19, dev(#766) 위, `id_field` 선언): 18개 시나리오 모두 기대와 같음, 집계 통과 18, 거짓 CONFIRMED 0. `id_field` 선언이면 생성·조회가 같은 ID를 싣고 조회 경로에 그 ID가 들어가며 생성은 종류마다 한 번인지도 이 시험이 본다.
- 가짜 v3가 보이지 못하는 것: 실제 E²의 목적 규칙·계정 붙이기·표식 정규화기·저널 MAC·신뢰 저장소, 실제 VAmPI 응답 모양, 토큰 만료와 세션 다시 받기, 확정 몫 계산. 이것들이 실제 회차의 몫이다.

## 9. 한계

- 고쳐진 대상(S02)은 정상 차단이 아니라 UNDECIDABLE이다(결정안 §14.1과 같은 한계). 거짓 CONFIRMED가 없다는 것까지만 보인다.
- 대상 리셋·인스턴스 교체를 서버가 무엇으로 아는지는 이동찬 확인 항목이다(v3 계약 §6). 지금은 리셋 확인 기록과 주소·TLS 키만 보므로, 같은 주소로 운영자 확인 없이 다시 띄운 VAmPI 컨테이너는 같은 인스턴스 참조가 된다.
- 실제 회차 결과 해석(특히 기대와 다른 결과)은 결과를 본 뒤 사용자에게 보고하고 정한다. 결과를 보고 기대값을 바꾸지 않는다.
