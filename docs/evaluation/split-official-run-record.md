# 분리 배치 공식 회차 기록 — O run-record v1.1 설계(제안)

> 상태: **제안, 리뷰 뒤 구현.** §7(E③ 결과 형식·채점 처리)은 2026-11-18 사용자 확정. 근거: 2026-11-18 사용자 결정. O를 공식으로 채점하는 것이 공식 측정 Go 조건이고 러너 최우선 작업이다. 같은 날 결정 (a)(#699)로 분리 배치 공식 회차는 B·O 모두 계측기 경로 기록을 같은 키로 켠다.
> 정본 관계: 기록 형식은 [러너 기록 v1.1](run-record-v1.1.md), 배치는 [탐색 분리 배치 결정안](exploration-split-decision.md), 발견 집계는 [공식 측정 프로토콜 고정 초안](official-protocol-fixation-draft.md) §7이다. 이 문서는 그 위에서 O(제품 배치) 회차가 같은 v1.1 기록을 내는 방법과, 채점기가 B·O 분리 배치 회차를 한 표로 묶는 규칙만 정한다.

## 1. 범위와 전제

- **B(원본 엔진, `artex-direct/v1`)**: 지금 공식 B 회차의 v1.1 기록을 그대로 낸다. 분리 배치 B 회차는 #699로 서명 조건에 `split: true`와 경로 가림 키 지문을 더 남긴다. 기록 모양은 그대로다.
- **O(제품, `exploration-split/v1`)**: 지금은 서명 전 요약만 낸다(`runner_split_run.execute_split_run`). 이 문서대로 B와 같은 v1.1 기록(서명된 러너 기록 + 격리 근거 + 해결 이벤트)을 낸다.
- **첫 지표는 엔진 귀속 해결 수다**(평가 프로토콜 §5.1). 그래서 O에서 먼저 필요한 것은 점수판 pre/post, 격리 증명, 해결 이벤트(`isolated_window`)다.
- **판정 레코드는 지금·첫 비교 범위에서 양쪽 다 `records: null`(판정 미수집)이다.** 판정 내보내기 API(`/api/tasks/{id}/verdict-exports`)는 [계약 제안](../contracts/verdict-record-page-api-proposal.md)에만 있고 서버 경로가 없다. 채점기는 run-record v1.1 §3대로 그 회차 GT 판정을 판단불가(사유: 판정 미수집)로 둔다. 이것은 지금 범위의 한정이지 영구 제외가 아니다. 평가 프로토콜 §5.1대로 판정 비교가 들어오면 B는 원본 출력 어댑터(A5) 출력, O는 동결 내보내기로 `records`를 채운다.
- 범위 밖: 통제 효과 지표(E② 시도 대조·차단 시도 수, 프로토콜 §8). O의 E② 저널을 평가 내보내기로 읽는 일은 계획 v4.2 3번이다. 그 전까지 O는 E② 자리를 `e2_placement.mode = split_product`로 적고 `attempt_reconciliation` 행을 두지 않는다(대조 미수집). 차단 시도 수는 미측정(`blocked_attempts: null`)이다. 이 상태는 제품 실패도 차단 0건도 아니다(§4 표, run-record v1.1 §3.1).

## 2. 회차 문맥 다섯 칸

| 칸 | B | O |
|---|---|---|
| `run_id` | 러너 회차 ID | 같음(`ours-…`) |
| `model_tag` | 선언 모델 태그 | 같음(사전검사 선언의 O 쪽) |
| `reset_generation`·`instance_id` | 대상 컨테이너 ID | 같음(분리 배치 대상 서비스 컨테이너 ID) |
| `execution_id` | 엔진 컨테이너 ID | `preparation` 컨테이너 ID. 진단 실행과 E②가 도는 곳이다(`product-preparation-entrypoint`, `runs/`). O 점수판 수집(`runner_split_ports.scoreboard`)이 이미 이 값을 스냅숏 문맥에 쓴다 |

## 3. 타임라인(`steps`)

B와 같은 단계 이름을 쓰고, O에서 그 순간에 해당하는 곳을 적는다.

| 단계 | B | O |
|---|---|---|
| `fixture_ready` | 대상 준비·봉인 | 대상 준비 뒤, pre 점검 직전. 제품 준비(로그인~초안)는 대상 요청이 아니라 이 단계를 바꾸지 않는다. 다만 pre 뒤 대상 요청 0건(`pre_requests_nonzero`)은 그대로 본다 |
| `pre` | pre 점수판 | 같음(`ports.scoreboard("pre")`, 관측기 `eval-obs` 경유) |
| `task_created` | 엔진 작업 생성 | 제품 시작 요청 뒤 작업 번호를 받은 때(`start_round`) |
| `sealed`·`drained` | 계측기 봉인·drain | 같음 |
| `post` | post 점수판 | 같음. 경계 정지(SIGTERM)는 post 점검 뒤, 구간 끝 전이다(#665 순서 그대로) |

## 4. 격리 근거 행

행 종류와 칸은 B와 같다(`isolation_proof._ROW_FIELDS`). O에서 행을 채우는 출처만 다르다.

| 행 | B 출처 | O 출처 |
|---|---|---|
| `boundary` | `compose:<project>` | 같음 |
| `network_snapshot` | pre/post 망·컨테이너 사진 | 같음(`ports.snapshot`, 분리 배치 렌더 기준) |
| `connectivity_check` | 엔진·관측기 출발 부정 연결 점검 | 여러 출발점 점검(`runner_split_probe`, §2.1 표: 진단 실행·탐색 경계·브라우저 등 출발점별) |
| `host_condition`·`host_watch` | 호스트 리슨 감시 | 같음 |
| `docker_events_window` | 구간 이벤트 판정 | 같음(분리 배치 healthcheck 표·경계 SIGTERM·도우미 예외, #663·#648·#669) |
| `preparation` | 준비 봉인 뒤 준비 요청 0 | 같은 뜻. pre 뒤 준비 요청·변경 0건을 계측기·구간 감시로 확인 |
| `observer_readonly`·`observer_request` | 관측기 GET 로그 | 같음(`eval-obs`, 분리 배치 overlay에 있다) |
| `engine_meter_summary`·`drain` | 계측기 요약·drain | 같음. 입구별 전달 수(본 입구·탐색 입구)를 함께 싣는다 |
| `run_conditions` | §6 | §6 |
| `verdict_collection` | 판정 내보내기 수집·무결성 | 지금은 `not_collected`(§1). 뜻은 바꾸지 않는다(§7) |
| `e3_confirmation`(새 선택 행, §7) | 없음(원본 엔진) | E③ 확정 단계 결과의 `status`·`reason`. 받아야 하는지는 서명된 `run_conditions.e3_status_expected`가 정한다 |
| `e2_placement` | E② 자리(`none`·`stub`·`product`) | 새 값 `split_product`(제품 안 E②, 분리 배치 회차만). 건수 칸은 모두 null이고 `e2_product` 행은 두지 않는다(그 행은 e2-legacy 별도 E² 서비스용) |
| `attempt_reconciliation` | E② 자리 회차의 시도 대조 | 계획 v4.2 3번 전에는 행 없음 = **대조 미수집**(#700 리뷰). 허용 조건: `e2_placement.mode = split_product`이고 `run_conditions.split = true`이며 러너 기록 `blocked_attempts`가 null일 때만. 성능표(해결 수 등)에는 그대로 들어가고, 통제 검증 표에는 "대조 미수집 — 통제 주장 보류"로 따로 적는다. 제품 실패 수에 넣지 않고, 차단 시도 수를 0으로 채우지 않는다(`—`). 수집된 행은 셀 합과 대조할 독립 결정 건수의 출처(3번의 E② 영수증·저널 v3 평가 내보내기)와 검증을 정할 때 지원하고, 그 전에는 행 없음만 허용한다(#700 리뷰). 기존 `product` 배치의 `product_failure`·`no_execution` 행 없음 예외는 그대로다 |

## 5. 격리 증명 칸

- `network.mode`: `dedicated_network`. 대상은 `eval_target_back`에만 붙고 계측기만 닿는다(분리 배치 배치 검사 `check_split_layout`).
- `network.target_origin`: 대상 이름 origin(`http://<split.target_name>:<계측기 포트>`). B 분리 배치 회차와 같은 문자열이다(결정안 §6).
- `network.policy_sha256`: 분리 배치 망 표의 지문. B의 `policy_sha256(pre)`와 같은 함수로, 분리 배치 렌더의 망·서비스 소속에서 뜬다.
- `network.allowed_actors`: B와 같은 두 값 `engine`·`observer`를 쓴다. O에서 `engine`은 **제품 스택 전체가 엔진 한 행위자**라는 뜻이다(진단 실행의 E②, 탐색 경계, 탐색 브라우저·MCP). 대상에 닿는 길이 계측기 두 입구뿐이라는 것은 배치 검사와 여러 출발점 점검이 근거다. 행위자 목록을 넓히지 않는 이유는 귀속 단위가 "엔진 회차"이기 때문이다. 제품 안 구성 요소별 귀속은 하지 않는다.
- `preparation.sealed`·`actions_after_pre`·`observer.*`·`host.operating_condition_ref`: B와 같은 뜻·같은 검증이다.

## 6. 실행 조건과 채점기 비교 규칙

지금 채점기는 실행 조건 일곱 칸(엔진 설정·적용 샘플링·머신 사양·머신·LLM 스트리밍·엔진 규칙 파일·도구 트리)이 **표 전체**에서 같아야 한다고 본다. 엔진 직접 배치(e2-legacy)에서는 B·O가 같은 엔진 계열이라 성립했다. 분리 배치에서는 B(원본 엔진)와 O(제품)의 엔진 설정·규칙·도구가 설계상 다르다. 그래서 분리 배치 표(`split: true`)에서는 칸을 둘로 나눈다.

| 칸 | 분리 배치 표 규칙 | O 값 |
|---|---|---|
| 적용 샘플링 | 표 전체 같아야 함 | 제품 호출 기록 사후 검증이 읽은 실제 보낸 샘플링(#654). B는 게이트웨이 적용값 |
| 머신 사양·머신 | 표 전체 같아야 함 | 같은 호스트 읽기 |
| LLM 스트리밍 | 표 전체 같아야 함. 제품 transport와 다르면 선언된 차이로 올리고 구현 전에 다시 정한다 | 제품 transport의 실제 값(구현 PR에서 확인) |
| 엔진 설정 | 조건(B·O) 안에서만 같아야 함 | 제품 설치·런타임 상한의 지문(보존 sidecar v3의 구동 설정 지문과 같은 정의) |
| 엔진 규칙 파일 | 조건 안에서만 | O는 null(원본 ② 규칙은 B 쪽 장치) |
| 도구 트리 | 조건 안에서만. 표 전체 비교는 도구 면 공통 칸(MCP 판·원 목록)으로 바꾼다 | 도구 면 지문(`tool-surface.json`, #693) |

엔진 직접 배치 표는 지금 규칙 그대로다. 같은 표에 분리 배치 회차와 엔진 직접 배치 회차가 섞이면 #699의 키 지문 규칙이 먼저 거부한다.

## 7. E③ 확정 결과 자리

E③ 인터페이스(#698)의 확정 단계 결과 `e3-confirmation-run/v1`에는 `status`·`reason`·`verdicts`·`candidates`가 있다. 공식 회차에서 `status=confirmation_integrity_failed`(진위 실패, 판정 행 없음)가 나오면 결과 보고에 보여야 한다(사용자 조건).

- `verdict_collection`의 뜻은 바꾸지 않는다. 이 행은 판정 **내보내기** 수집·무결성이다. `integrity_failed`(내보내기 변조·불일치)면 지금처럼 채점 전체를 멈춘다(`isolation_proof.verdict_collection`, run-record v1.1 §3.1). `collected`는 판정 내보내기를 검증해 수집했을 때만이다(`records`는 그 검증된 묶음이고, 판정 행이 0건이면 빈 묶음). E③ 상태만 받고 내보내기는 아직 받지 못한 회차는 `e3_confirmation`만 기록하고 `verdict_collection`은 `not_collected`(고정 사유)·`records: null`이다(#700 리뷰: 상태만으로 `collected`를 쓰면 `records: null`과 어긋나 격리 검증기가 기록을 거부한다). 판정 행 0건인 검증된 내보내기(`collected` + 빈 묶음)와 미수집(`not_collected` + null)은 다른 결과다. 앞의 것은 E③ 상태에 따라 채점하고, 뒤의 것은 판정 미수집(프로토콜 §4 7번)이다.
- E③ 확정 결과는 **따로 서명 행** `e3_confirmation`(선택 행, `status`·`reason`)에 싣는다. 값은 E③ 계약의 닫힌 값 그대로다(`disabled`·`start_refused`·`completed`·`interrupted`·`confirmation_integrity_failed`). `reason`도 상태별 닫힌 값이다(계약 §7 표, `e3contract/result.go`의 DisabledReason·StartRefusal·IntegrityReason). `completed`·`interrupted`면 `reason`이 비어 있다. 채점기 검증(R4)은 이 목록과 대조한다. 원문 값·후보 내용은 싣지 않는다. 제품이 회차 안에서 스스로 잡은 결과라 회차는 유효하다. 내보내기 무결성과 섞지 않는다.
- 채점기 처리(2026-11-18 사용자 확정): `completed`는 그대로 채점한다. `confirmation_integrity_failed`·`disabled`·`start_refused`는 그 회차 GT 판정을 판단불가로 둔다. "발견 없음"으로 세지 않는다. `interrupted`는 받은 판정만 쓰고 나머지는 판단불가다. 어느 경우든 결과 보고의 회차 주석에 `status`·`reason`을 보인다. 해결 귀속(첫 지표)은 이 행과 무관하게 따로 검증한다.
- 행 부재 해석(#700 리뷰): 행이 없다는 것만으로 미적용을 정하지 않는다. `run_conditions`의 서명 칸 `e3_status_expected`(불리언)를 둔다.
  - 참인데 행이 없으면 수집 실패(`e3_status_not_collected`)이고, 그 회차 GT는 전부 판단불가다. 서명된 빈 판정 묶음과 해결 귀속이 있어도 미탐이 되지 않는다.
  - 거짓이거나 칸이 없으면 행이 없어야 한다(있으면 입력 오류).
  - O 러너는 E③ 단계가 들어간 제품 회차에 늘 참을 서명한다. 제품은 E③를 꺼도 `disabled` 상태를 낸다. B 러너와 칸이 생기기 전 기록은 거짓·없음이다.
- 정본도 이 PR에서 같이 고쳤다: 평가 프로토콜 §4 8번·§5 회차 주석, run-record v1.1 §3 `e3_confirmation` 행. 채점기·격리 검증 구현(행 검증·닫힌 값 대조·판단불가 처리·회차 주석)은 R4다. 실제 수집 경로(제품 API·파일)는 판정 저장 PR 뒤에 정한다.

## 8. 검증 방법(공식 측정 전)

조건: Juice Shop 실제 모델 회차는 공식 측정 때만 돌린다. 채점기는 기록 fixture로 검증한다.

1. **채점기 fixture**: 가짜 포트로 돈 B·O 분리 배치 회차가 실제로 서명한 기록 묶음을 fixture로 만든다(`run_fixture_v11.py` 방식). 이 묶음으로 `score.py`가 표를 내는지 본다. 거부되어야 할 것도 본다: 변조, 목적·배치 섞임, 키 지문 다름, 경로 기록 없음, 실행 조건 공통 칸 다름.
2. **O 공식 흐름 실제 Docker**: VAmPI + 합성 모델로 분리 배치 회차를 끝까지 돌린다. 무료이고, 계획 v4.2 2번의 합성 모델 O 회차와 같이 한다. VAmPI는 관측기가 `opaque`라 해결 상태가 없다. 수용 기준은 둘로 나눈다(#700 리뷰).
   - 격리 검증 통과: 서명 기록을 `run_record` 로더·격리 검증(`isolation_proof`)이 받는다. 서명 칸 `run_conditions.execution_source`는 `synthetic_fixture`다.
   - 공식 채점 거부: 같은 기록을 채점기에 주면 회차 하나(`score_run`)와 비교 표 모두 거부한다(#707, `test_synthetic_runs_are_refused_by_the_scorer_and_the_table`). 합성 회차가 채점기를 "지나는" 것은 실패다.
3. **Juice Shop + 합성 모델 배선 확인**(2026-11-18 사용자 결정): R3 뒤 내 머신에서 한 번 돈다. 점수판 관측 → pre/post 해결 맵 → `isolated_window` 귀속 경로를 유료 공식 회차 전에 실제로 확인한다. 조건은 다음과 같다.
   - 공식이 아닌 별도 목적(배선 확인)으로 서명하고, 채점기·비교 표가 거부한다.
   - 스크립트 요청 목록은 PR에 고정해 리뷰받는다. 평가 정답(챌린지 목록)은 스크립트·엔진 입력 어디에도 넣지 않는다.
   - 해결 귀속 확인에 해결이 꼭 필요하면 공개 화면 방문만으로 풀리는 것 하나만 쓰고, 이유를 PR에 적는다.
   - 결과는 통과/실패와 결함 목록만 남기고 수치는 남기지 않는다. 이 회차를 근거로 한 수정은 러너·계측기·채점기·overlay에만 한다.

## 9. 구현 순서

| 단계 | 내용 |
|---|---|
| A | #699: 분리 배치 공식 회차 경로 기록·키 지문(머지 대기) |
| R1 | O 회차 문맥·타임라인·pre/post 점수판 파일·관측기 GET. B 조립 함수(`_build_record`)를 배치와 무관한 입력으로 나눠 B·O가 함께 쓴다. O `verdict_collection`은 `not_collected`, `e3_confirmation` 행은 수집 경로가 생길 때까지 없음(§7) |
| R2 | O 격리 근거 행(§4)과 격리 증명(§5). 채점기 `isolation_proof`가 분리 배치 근거를 받는다. `e2_placement.mode = split_product`와 대조 미수집(행 없음·`blocked_attempts: null`) 허용 조건을 로더·격리 검증에 넣고, 통제 검증 표의 "대조 미수집" 표시·제품 실패 수 제외를 시험한다 |
| R3 | 해결 이벤트, 러너 키 서명, O 결과 서명. 보존 sidecar v3 O 조립(경계 원자료 검증 포함)도 같은 서명 경로에서 마친다 |
| R4 | 채점기 분리 배치 표 규칙(§6), `e3_confirmation`·`e3_status_expected` 검증·보고 주석과 정본 개정(§7), §8 1번 fixture 시험. 수용시험(행 부재 구분): ① 칸 없는 옛 기록 + 행 없음 → 7번대로 읽음 ② 기대 참 + 행 없음 + 서명된 빈 판정 묶음 + 해결 귀속 → `e3_status_not_collected`, GT 전부 판단불가(미탐 0) ③ 기대 참 + `completed` → 그대로 채점 ④ 기대 거짓 + 행 있음 → 입력 오류 |

## 10. 열린 항목

- O `execution_id` 서비스(§2)와 LLM 스트리밍 칸(§6)은 구현 PR에서 렌더·제품 transport로 확인한다.
- O 판정 수집: 제품 판정 내보내기 경로(E③ 통합 담당). 생기면 `records`와 `verdict_collection`을 채운다.
- O E② 시도 대조(통제 지표): 계획 v4.2 3번(E② 영수증·저널 v3 평가 내보내기) 뒤.
