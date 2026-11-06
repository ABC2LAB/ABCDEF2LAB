# 평가 러너 기록 v1.1 — 격리 보증 구간 해결 귀속

상태: **2026-11-02 귀속 필드 원칙·엔진 이벤트 정의·호스트 운영 조건 필드까지 최민준과 합의한 입력 계약.** 2026-11-03 `records` null(판정 미수집)과 §3.1 `isolation_evidence` 행 직렬화를 최민준이 제안(이동찬 리뷰). [이벤트 정의 리뷰](https://github.com/ABC2LAB/ABCDEF2LAB/pull/52#issuecomment-6524512173)와 [호스트 필드 요청](https://github.com/ABC2LAB/ABCDEF2LAB/pull/52#issuecomment-6567135772)을 반영했다. 채점기·러너 구현과 실환경 검증은 후속이다. 현재 채점기는 v1의 직접 관측 이벤트만 수용하며, 이 문서가 `isolated_window` 수용이나 실환경 격리 구현 완료를 뜻하지 않는다.

선행 관계: **[PR #51](https://github.com/ABC2LAB/ABCDEF2LAB/pull/51)은 `3414a39`로 병합됐고, #52는 그 변경을 포함한 dev 위로 정렬했다.** 대상 구동·격리 모드·같은 호스트의 운영 조건은 [평가 프로토콜 §2](evaluation-protocol.md#2-평가-대상-고정)와 §4가 정본이다. 이 문서는 그 조건을 기록·검증할 입력 계약이며 Docker 예시나 별도 격리 정책을 정의하지 않는다. 전용 internal 네트워크를 기본으로, 루프백 바인딩을 대안으로 두는 #51의 우선순위를 따른다.

연결: [현재 v1 계약](run-record.md) · [평가 규칙 §4](evaluation-protocol.md#4-판정-기준) · [측정 매트릭스 §5](evaluation-protocol.md#5-측정-매트릭스-값은-비움-틀만)

## 1. 공통 귀속 방식과 한계

baseline에는 E²가 없으므로 E² traffic ID를 해결 주체 확인의 필수 조건으로 삼지 않는다. baseline·개선 후 모두 **러너가 실제로 집행한 격리 구간**과 pre/post scoreboard 델타를 같은 규칙으로 사용한다. 전송 통제(E²)의 지원 범위나 요청별 원인 증명을 이 계약으로 확대하지 않는다. **공식 비교표에서는 양쪽 모두 v1.1의 `isolated_window`로만 귀속한다.** 기존 v1 직접 이벤트 수용은 합성 시험·과거 형식 회귀 검증용이며, 공식 비교표의 귀속 근거나 대체 경로로 사용하지 않는다. 공식 비교는 v1.1 채점기와 실제 러너 격리 검증을 완료한 뒤 수행한다.

측정 단위는 챌린지의 신규 해결 여부다. 증명이 유효한 격리 구간에서 `pre=false → post=true`인 항목만 그 회차 엔진의 해결로 귀속할 수 있다. 이 방식은 해결을 일으킨 개별 요청이나 정확한 해결 시각을 알려 주지 않는다. post 관측 시각을 실제 해결 시각으로 쓰지 않으며, 기존 `elapsed_seconds`도 독립적으로 측정한 확정 시각이 없으면 null로 둔다.

**엔진 이벤트의 정의:** 엔진과 서명 키를 공유하지 않는 신뢰 러너가 유효한 격리 증명 아래 pre→post 신규 해결 델타의 각 key마다 발행하는 귀속 레코드다. `actor=engine`은 해결의 **귀속 대상**이며 이벤트 발행 주체를 뜻하지 않는다. 엔진의 자기 선언을 해결 이벤트로 받거나 엔진에 발행 권한을 주지 않는다.

챌린지 key·정답/해결 상태는 러너·관측·채점의 **평가 전용** 자료로 유지하고 엔진에 전달하지 않는다. 엔진 프롬프트·도구 입력·검사 계획에 주입하지 않으며, 평가 델타나 이벤트 key 목록을 엔진에 되돌려 주지도 않는다. 엔진의 발견·판정 주장은 판정 레코드와 근거로만 수집해 평가 §4 판정표에서 러너의 해결 귀속 결과와 대조한다. 판정 레코드는 해결 이벤트를 대신하지 않고, 해결 이벤트도 판정 레코드·필수 증거를 대신하지 않는다.

격리 귀속은 **해결 귀속의 선행 조건**만 충족한다. 평가 §4의 판정 레코드·traffic 증거·상관·커버리지 규칙은 그대로 적용한다. 유효한 격리 증명만으로 finding이나 자동 매트릭스 CONFIRMED를 만들지 않는다. baseline의 traffic/출력 어댑터도 별도 필요하다. 사람 수동 확정은 이 이벤트의 발행 근거가 아니며 자동 집계에서 제외한다.

## 2. 러너의 실제 집행 의무

1. **회차 전용 접근 경계:** 네트워크 모드와 전용 평가 호스트 운영 조건은 평가 프로토콜 §2를 따른다. 러너는 선택한 모드·경계 식별자·적용 시각·정책 지문·검증 근거와 같은 호스트의 운영 조건 확인 근거를 보존한다. 네트워크가 다른 로컬 프로세스·사용자까지 차단한다고 주장하지 않는다. 네트워크와 운영 조건을 함께 확인해 해당 엔진과 등록 관측기 외의 접근 가능성을 해소해야 한다. 공개 listener·우회 경로·정책 유실 또는 호스트 운영 조건 불충족을 발견하면 그 구간의 격리 증명을 발행하지 않는다.
2. **pre 이후 준비 금지:** 회차별 인스턴스 생성/reset·계정·fixture 준비는 pre 전에 완료한다. pre부터 post까지 provisioning·fixture 수정·다른 사용자의 조작·추가 준비 요청을 허용하지 않는다. 이는 엔진 자신의 승인된 검사 요청을 준비 요청으로 분류한다는 뜻이 아니다. reset·인스턴스/실행 교체도 같은 구간에 섞지 않는다.
3. **관측기 제한:** 러너가 등록한 GET 목록만 사용한다. 각 경로는 해당 대상 버전에서 상태 변경·solve 유발이 없는 읽기 전용 계측임을 별도로 검증하고 그 근거를 연결한다. GET이라는 메서드만으로 읽기 전용을 추정하지 않는다. 등록 밖 요청·재시도·redirect를 통한 우회도 허용하지 않는다.
4. **실제 요청 기록과 종료:** 관측기의 실제 송신 요청을 누락 없이 세고, 각 요청의 메서드·등록 경로 참조·시각·결과를 비민감 로그로 보존한다. 요청 수는 로그 행 수와 일치해야 한다. post는 엔진 신규 송신 종료와 진행 요청 drain을 확인한 뒤 수집한다. 미완료/송신 불명·관측 로그 누락·경계 공백이 있으면 증명을 발행하지 않는다.

이 의무의 집행 주체는 엔진과 키를 공유하지 않는 신뢰 러너다. HMAC은 러너 기록의 출처와 무결성을 증명하며 네트워크 격리 자체를 생성하지 않는다. 실제 경계를 확인하지 않고 키 보유자가 필드를 채운 기록은 유효한 측정 근거가 아니다. 실환경 도입 전 우회 접근·pre 이후 준비·관측기 제한 위반을 실제로 차단하는 시험이 필요하다.

## 3. v1.1 러너 기록

합의한 필드와 검증 조건은 아래와 같다. v1의 서명·회차 문맥·아티팩트 해시·요청 종료·pre/post 정규화 검증을 유지한다. manifest에는 귀속 선언이나 격리 증명을 추가하지 않는다.

- `version`: 문자열 `"1.1"`을 사용한다. 기존 정수 `1` 기록은 합성 시험·과거 형식 회귀 검증에서 기존 직접 관측 규칙으로 읽는다. 버전을 느슨한 숫자/문자열 변환으로 비교하지 않는다.
- `isolation_proof`: 아래 구조의 객체 또는 null. 구간 증명이 없으면 null이며 `isolated_window` 이벤트를 인정하지 않는다.
- `artifacts.isolation_evidence`: 격리 집행·관측 로그의 descriptor 또는 null. 기존 descriptor와 같은 문맥·phase=engine·안전한 경로·size·SHA-256 검증을 적용한다. 증명 객체가 있으면 이 자료도 필수다.
- `artifacts.records`: v1.1에서는 판정 레코드 envelope descriptor 또는 **null**이다. null은 **판정 미수집**(그 회차의 엔진·baseline 출력 어댑터가 판정 레코드를 내지 못함)이며, 채점기는 그 회차의 모든 GT 판정을 판단불가(사유: 판정 미수집)로 두고 해결 귀속(`isolated_window`)만 검증·보고한다. null을 "판정 없음 → 미탐"으로 읽지 않고(커버리지 기록이 있어도), 레코드 0건인 envelope와 구분한다. v1(정수 `1`) 기록은 기존대로 `records` 필수다.

`isolation_proof`와 `artifacts.isolation_evidence`는 둘 다 null이거나 둘 다 유효한 객체여야 한다. 한쪽만 제출한 부분 증명은 입력 오류다.

`isolation_proof` 전체와 `isolation_evidence` descriptor는 러너 기록의 HMAC 서명 대상이다. 이벤트는 증명 전체를 복제하지 않고 canonical SHA-256을 참조한다. canonical JSON 규칙은 v1의 서명 직렬화와 같고, 증명 digest 자체가 별도의 발급 권한을 만들지는 않는다.

| 증명 필드 | 검증 의무 |
|---|---|
| `proof_id` | 회차별 고유한 비민감 증명 참조. 다른 회차에 재사용 금지 |
| `run_id`, `model_tag`, `reset_generation`, `instance_id`, `execution_id` | 서명된 러너 기록의 5개 문맥과 정확히 일치 |
| `window.pre`, `window.post` | 러너 timeline의 pre/post `{at, sequence}`와 정확히 일치. 경계 적용은 pre 수집 전에 완료하고 pre 관측부터 post 관측 완료까지 연속 유지 |
| `snapshots.pre_sha256`, `snapshots.post_sha256` | 각각 `artifacts.pre.sha256`, `artifacts.post.sha256`와 문자열이 정확히 같아야 함. 정규화 파일 바이트의 해시이며 descriptor JSON 자체의 해시가 아님. raw/정규화 대응은 기존 검증 유지 |
| `network.mode` | `loopback_only` 또는 `dedicated_network`. 모드 이름 자체를 집행 증거로 보지 않음 |
| `network.target_origin`, `network.boundary_ref`, `network.policy_sha256` | 설정의 대상 origin·회차 전용 경계와 결속. URL에 자격정보·query·fragment 금지. loopback 모드는 127.0.0.1, 전용 네트워크 모드는 등록된 대상·경계만 허용 |
| `network.allowed_actors` | 해당 실행의 엔진과 등록 관측기만 포함. 다른 actor/경계 외 접근의 가능성이 해소되지 않으면 증명 불가 |
| `host.operating_condition_ref` | 전용 평가 호스트 운영 조건의 비어 있지 않은 비민감 확인 참조. 같은 5개 문맥·참조의 `isolation_evidence` 호스트 확인 기록에 연결. 그 기록의 확인 시각·적용 구간·연속 관측 근거가 pre 관측부터 post 완료까지 같은 호스트의 미등록 접근/경계 변경 가능성을 배제했음을 검증. 네트워크 설정이나 호스트 이름만으로 대체하지 않음 |
| `enforcement_evidence_sha256` | `artifacts.isolation_evidence.sha256`와 일치. 경계 적용/유지·준비 봉인·관측 경로 읽기 전용 검증의 러너 소유 근거가 실재해야 함. 해시 문자열만 있고 자료가 없으면 오류 |
| `preparation.sealed` | 준비 완료의 `{at, sequence}`. fixture_ready와 일치하며 pre보다 앞섬 |
| `preparation.actions_after_pre` | pre 이후 준비 요청/변경 건수인 정수 0. 엔진 자신의 검사와 구분하고 읽기 전용 준비 요청도 포함. bool·음수·누락 금지. 준비 통제 로그와 대조하며 단독 자기 선언으로 인정하지 않음 |
| `observer.ref`, `observer.registered_gets` | 러너가 등록한 관측기 참조와 GET 경로 참조 목록. 각 경로의 읽기 전용 검증 근거를 isolation_evidence에 연결 |
| `observer.request_count` | pre 관측 완료 직후부터 post 관측 완료까지 실제 송신 건수. 순서 범위는 `(pre.sequence, post.sequence]`다. pre 수집 GET 제외·post 수집 GET 포함. 정상 post 수집이 있으므로 양의 정수이며 bool 금지. 미수집을 0으로 대체하지 않음 |

`isolation_evidence`는 위 5개 문맥·`phase: "engine"`과 경계 수명주기/준비 통제/관측 요청 로그를 가진 평가 전용 자료다. 실제 행 직렬화와 경계 adapter 구현은 아래 합의한 최소 검증 조건을 충족해야 한다.

- 정책 적용이 pre 이전에 끝나고 post까지 유효하며 경계 교체·해제·허용 actor 변경이 없어야 한다. 같은 회차 대상과 정책 지문이어야 한다.
- `host.operating_condition_ref`에 대응하는 호스트 운영 조건 확인 기록이 있어야 한다. 증명과 동일한 5개 문맥·참조, 확인 시각, 적용 시작/끝, 확인한 운영 조건과 충족 여부·연속 관측 근거를 비민감 자료로 연결한다. **확인 기록이 없거나 적용 구간이 pre 관측부터 post 완료까지를 덮지 못하면 증명 불가**다. 구간 중 미확인 공백·미등록 접근/경계 변경 가능성·불충족도 증명 발행을 금지한다. 한 시점의 자기 선언이나 네트워크 증거로 대체하지 않는다. 발행 불가 시 proof=null과 사유를 남기고, 증명을 제출하면서 참조/기록을 누락·위조하거나 구간이 모순되면 입력 오류로 채점을 중단한다.
- 준비 봉인 이후 pre..post에 준비 요청/변경이 0건이며 로그가 구간 전체를 관측했음을 확인해야 한다. 로그 없음과 준비 없음은 다르다.
- 구간의 관측 요청마다 ID·GET·등록 경로 참조·송신/완료 시각·순서·결과를 보존한다. 요청 ID 중복, 등록 밖 경로, 비-GET, 구간 밖 송신, 미완료·송신 불명, 누락된 결과는 거부한다. pre 수집 GET은 pre 단계에서 완료되어 진행 요청이 없어야 하며 같은 GET 등록/읽기 전용 검증을 받는다. post 수집 GET을 포함한 구간 송신 로그 수와 `observer.request_count`가 같아야 한다.
- 본문·인증값·계정 원문·표식 값·셸 명령 원문을 기록하지 않는다. 서버/네트워크 정책의 비민감 참조·digest와 관측 경로 참조만 사용한다.

### 3.1 `isolation_evidence` 행 직렬화 — 러너 형식 v1(제안)

파일은 JSON 객체 하나다: 5개 문맥(`run_id`·`model_tag`·`reset_generation`·`instance_id`·`execution_id`) + `phase: "engine"` + `format: "abc2lab-isolation-evidence-v1"` + `rows`(배열). 모든 행은 `kind`, 파일 안 순번 `seq`(1부터 엄격 증가), 러너 벽시각 `at`(timezone 포함 ISO)을 가진다. `kind`는 행 종류 구분자로만 쓰며, 행 안의 다른 필드는 `kind`라는 이름을 쓰지 않는다(평평한 객체라 중복 키가 된다). 모르는 `kind`·필드, 중복 키, 순번 공백은 입력 오류다. 값은 비민감만 쓴다: 컨테이너 내부 IP·서비스 이름·망/컨테이너 ID·digest·경로 참조는 넣고, 요청/응답 본문·헤더·비밀·계정 원문·표식 값은 넣지 않는다.

| `kind` | 필드 | 채점기 검증 |
|---|---|---|
| `boundary` | `event`(`applied`·`maintained`·`released`), `boundary_ref`, `network_ids`(정렬), `policy_sha256` | `applied`·`maintained`는 정확히 한 행씩 필수, `released`는 선택(있으면 한 행). `applied.at` ≤ pre 관측, `maintained.at` ≤ post 관측. 있는 행 전부의 `boundary_ref`·`network_ids`·`policy_sha256`이 증명의 `network.*`와 같음. 해제는 구간(pre 관측~post 완료) 뒤의 일이라 귀속 판단에 쓰지 않고, 정리 실패는 러너 결과 기록의 무효 사유(`down_failed`)로 남는다 |
| `network_snapshot` | `phase`(`pre`·`post`), `networks`(`name`·`id`·`internal`·`members`), `publishes`(`service`·`host_ip`·`host_port`·`container_port`), `services`(`id`·`image`·`started_at`·`restart_count`) | pre·post가 같고 `policy_sha256`을 다시 계산하면 증명 값과 같음. 다르면 구간 중 경계 변경 |
| `connectivity_check` | `phase`, `id`, `probe_kind`(`tcp`·`dns`), `target`, `role`(`positive`·`negative`), `expected`, `accepted`, `observed`, `code`, `verdict` | pre·post 각각 모든 행 `verdict: "match"`. 양성 대조 실패·부정 경로 성공·실패 종류 불일치가 있으면 증명 불가 |
| `host_condition` | `phase`, `operating_condition_ref`, `daemon`(`version`·`os`·`arch`·`platform`), `listeners_outside_loopback`, `allowed_listens`(`address`·`port`), `other_publishes`, `satisfied` | pre·post 두 행, `operating_condition_ref`가 증명의 `host.operating_condition_ref`와 같고 둘 다 `satisfied: true` |
| `host_watch` | `observed_from`, `observed_to`, `samples`, `max_gap_seconds`, `allowed_max_gap_seconds`, `violations` | 구간이 pre 관측부터 post 완료까지 덮고, `max_gap_seconds` ≤ 허용값, `violations` 0. 아니면 증명 불가(미확인 공백) |
| `docker_events_window` | `since`, `until`, `event_count`, `kinds`(종류별 건수), `foreign_attach`, `run_container_restarts`, `complete` | `since` ≤ pre 관측, `until` ≥ post 완료, `foreign_attach`·`run_container_restarts` 0, `complete: true`(데몬 이벤트 버퍼 한도 안) |
| `preparation` | `sealed`(`at`·`sequence`), `actions_after_pre`, `channels`, `basis` | `sealed` = timeline `fixture_ready`, `actions_after_pre` 정수 0, `channels` 빈 배열(러너에 준비 채널 없음), `basis`는 허용 actor 외 대상 접근이 없음을 보인 행(`network_snapshot`·`docker_events_window`)과 관측 로그 참조 |
| `observer_readonly` | `path_ref`, `method`, `verification_ref` | 등록 GET마다 한 행. `verification_ref`는 그 대상 버전에서 해당 GET이 상태를 바꾸지 않음을 확인한 별도 기록 참조 |
| `observer_request` | `request_seq`, `method`, `path_ref`, `sent_at`, `completed_at`, `status`, `decision`, `window`(`before_pre`·`pre`·`in_window`) | 요청마다 한 행, `request_seq` 중복 없음, 전부 GET·등록 `path_ref`·`decision: "registered"`·완료. `in_window` 행 수 = 증명의 `observer.request_count`(post 수집 GET 포함, pre GET 제외) |
| `engine_meter_summary` | `forwarded_requests`, `rejected_after_seal`, `forced_closed_streams`, `log_rows`, `sequence_complete` | `sequence_complete: true`, `forwarded_requests`는 러너 기록 `total_requests`와 같음 |
| `run_conditions` | `engine_settings_sha256`, `engine_rules_sha256`, `output_language`, `tool_tree_sha256`, `workers`, `sampling`, `host`, `machine_id_sha256`, `llm_stream` | 정확히 한 행. `engine_settings_sha256`(작업 생성 전 엔진 실행 설정 스냅샷의 정규 JSON SHA-256, `settings.output_language`는 뺀다)·`machine_id_sha256`·`tool_tree_sha256`(엔진 이미지의 패키지 버전·실행 파일·설정, `scripts/eval/tool_tree.py`)은 64자 소문자 hex, `output_language`는 그 스냅샷의 표시 언어다(프로토콜 §2 선언된 차이라 채점기가 조건 하나 안에서만 대조한다), `workers`는 1 이상 정수(선언 `worker_count`와 러너가 대조), `sampling`은 게이트웨이가 실제로 넣은 값(숫자)이다. api는 일곱 값(temperature·top_p·top_k·min_p·presence_penalty·repetition_penalty·seed), local은 다섯 값(temperature·top_p·presence_penalty·frequency_penalty·seed)이고, 예전 local 회차는 null이다, `host`는 `cpu_model`·`kernel`·`docker_server_version`(빈 문자열 아님)과 `logical_cpus`·`memory_kib`(1 이상 정수), `llm_stream`은 러너 설정의 엔진 LLM 스트리밍(bool, 엔진 설정 API에 나오지 않아 선언값), `engine_rules_sha256`은 원본 ② 규칙 파일의 SHA-256(64자 소문자 hex) 또는 규칙을 넣지 않았으면 null이다. 채점기는 비교하는 회차끼리 엔진 설정 해시·적용 샘플링·머신 사양·머신 ID 해시·LLM 스트리밍·규칙 파일 해시가 하나라도 다르면 비교하지 않는다(평가 프로토콜 §5.1·§6) |
| `e2_placement` | `mode`, `decision_rows`, `allowed`, `unsupported`, `unavailable` | 정확히 한 행. 개선 후 회차의 E② 자리(평가 프로토콜 §2 개선 후의 E② 배치, 러너 설정 `e2.ours_mode`). `mode`는 `none`(E² 없음, 건수는 전부 null), `stub`(통과 전용 가짜 E², 건수는 0 이상 정수이고 `decision_rows` = `allowed`+`unsupported`+`unavailable`) 또는 `product`(제품 E², 건수는 전부 null이고 `e2_product` 행에 있음). 건수는 가짜 E² 결정 행(`e2-stub-decisions.jsonl`)에서 러너가 형식을 검사한 뒤 센 값이다. 회차 조건과 달리 baseline·개선 후가 일부러 다르므로(선언된 차이) 채점기는 조건 하나 안에서 값이 섞이면 비교를 멈추고, `stub` 조건에는 "공식 비교 아님" 경고를 낸다. 시도별 대조 건수는 아래 `attempt_reconciliation` 행에 둔다 |
| `e2_product` | `close_state`, `close_reason`, `collection_outcome`, `collection_reason`, `rows`, `attempts`, `decisions`, `attempts_without_result`, `allowed_not_sent`, `checkpoint_sequence`, `config_sha256`, `policy_fingerprint_sha256`, `manifest_sha256`, `image_id` | `e2_placement.mode=product` 회차에만 정확히 한 행. 채점기는 조건 하나 안에서 `policy_fingerprint_sha256`·`image_id`가 다르면 비교를 중단한다(`config_sha256`은 회차 값이 들어가 회차마다 다르다). `collection_outcome`은 `complete`·`product_failure`·`no_execution`이다. `product_failure`도 유효 회차이고 사유가 있어야 하며, 이때 `attempt_reconciliation` 행은 없어서 통제 주장이 보류된다. `decisions`는 `allowed`·`denied`·`unsupported` 건수이고 결과를 못 모았으면 null이다. 제품 값은 이 행에만 있고 `e2_placement`의 건수는 null이다 |
| `attempt_reconciliation` | `cells`, `allowed_responded`, `meter_attempt_reused`, `blocked_reached`, `unrecorded_reached`, `positive_evidence`, `control_claim_withheld` | `e2_placement.mode`가 `none`이 아닌 회차에만 정확히 한 행이 있다. `none`이면 행이 없어야 한다. 러너가 E② 결정 행과 계측기 접수 행을 시도 ID로 맞댄 결과(평가 프로토콜 §8.3)이고, 건수만 담는다. 시도 ID·경로·헤더는 넣지 않는다. `cells`는 다섯 칸 `allowed_observed`·`allowed_unobserved`·`denied_unobserved`·`denied_observed`·`unrecorded_observed`이며 값은 0 이상 정수다. `allowed_responded`는 허용 시도 중 계측기 `response`가 none이 아닌(대상이 응답한) 시도 수이고 `allowed_observed`를 넘을 수 없다. 채점기는 러너 대조 코드를 쓰지 않고 계약대로 다시 확인한다: `blocked_reached`=`denied_observed`, `unrecorded_reached`=`unrecorded_observed`, `positive_evidence`=(`allowed_responded`>0), `control_claim_withheld`=(`denied_observed`>0 또는 `unrecorded_observed`>0 또는 재사용>0 또는 `allowed_responded`=0). 허용 두 칸의 합은 `e2_placement.allowed`와, 차단 두 칸의 합은 `unsupported`+`unavailable`과 같아야 한다. 어긋나면 채점을 멈춘다. 나쁜 칸이 있는 것은 오류가 아니다. 그 회차는 성능표에 그대로 들어가고, 통제 검증 표에서 통제 주장만 보류한다 |
| `verdict_collection` | `status`(`collected`·`not_collected`·`integrity_failed`), `reason` | 판정 export 수집을 켠 개선 후 회차만 한 행. `collected`는 `reason`이 null이고 `records`가 있어야 한다. 나머지는 고정 사유가 있고 `records`가 null이어야 한다. **`integrity_failed`이면 채점을 중단한다.** 판정 미수집(판단불가)으로 숨기지 않는다 |
| `drain` | `engine_end`(`at`·`sequence`), `sealed_at`, `drained_at`, `inflight`, `grace_seconds`, `settle_seconds`, `forced_cancelled`, `settle_applied_seconds` | `engine_end` = timeline `engine_end`, `inflight` 0, `drained_at` ≤ timeline `drained` ≤ post 관측. 정책: `grace_seconds`(봉인 뒤 유예)·`settle_seconds`(취소가 있을 때 post 전 정착 대기)는 1 이상 정수이고 사전검사 `run_drain`과 같은 값이며, 채점기는 비교하는 회차끼리 두 값이 다르면 비교하지 않는다. 결과: `forced_cancelled`(유예가 지나 계측기가 대상 쪽으로 취소한 요청 수)는 0 이상 정수, `settle_applied_seconds`는 취소가 있으면 `settle_seconds`, 없으면 0. 취소된 요청도 봉인 전에 전달된 요청이라 `forwarded_requests`에 들어 있고 증명은 유지한다 |

`connectivity_check` 행 예시(값은 구조 예시이며 실제 회차 자료가 아니다):

```json
{"kind":"connectivity_check","seq":7,"at":"2026-11-03T06:46:44+00:00","phase":"pre","id":"meter_tcp",
 "probe_kind":"tcp","target":"eval-meter:8080","role":"positive","expected":"success",
 "accepted":["connected"],"observed":"success","code":"connected","verdict":"match"}
{"kind":"connectivity_check","seq":8,"at":"2026-11-03T06:46:44+00:00","phase":"pre","id":"dns:target",
 "probe_kind":"dns","target":"target","role":"negative","expected":"fail",
 "accepted":["not_found","dns_error"],"observed":"fail","code":"dns_error","verdict":"match"}
```

`host_watch`는 이벤트가 없는 호스트 리슨을 구간 중 주기적으로 확인한 요약이고, `docker_events_window`는 데몬이 기록한 구간의 망 연결·해제와 컨테이너 시작·재시작을 빠짐없이 받은 요약이다. 둘 중 하나라도 없거나 구간을 덮지 못하면 러너는 증명을 발행하지 않는다(proof=null과 사유). 두 행이 있어도 같은 호스트의 다른 프로세스까지 막았다는 뜻은 아니며, 그 부분은 평가 프로토콜 §2의 호스트 운영 조건이 맡는다.

관측기 요청 수는 별도 기록이며 평가 §5의 엔진 `total_requests`에 섞지 않는다. 요청 수 집계에서 pre 수집 GET을 제외하는 것과, 격리 조건이 pre 관측부터 연속 적용돼야 하는 것은 별개다. baseline·개선 후 모두 같은 정의를 사용한다. 엔진 실제 요청 수를 측정하지 못했다면 기존 규칙대로 null이며, 임의로 0이나 추정 차감값을 넣지 않는다.

## 4. 해결 이벤트의 basis

v1.1 이벤트는 기존 필드에 `basis`와 `isolation_proof_sha256`을 추가하는 안이다. 발행자는 §1의 신뢰 러너이며, 아래 `challenge`는 러너가 평가 자료에서 확인한 key다. 엔진이 제출하는 입력 형식이 아니다. 다음은 구조 예시이며 실제 서명/증명으로 사용할 자료가 아니다.

```json
{"id":"solve-1","challenge":"evaluation-only-key","actor":"engine",
 "execution_id":"execution-1","observed_at":"2026-10-30T09:00:10+00:00","sequence":6,
 "basis":"isolated_window",
 "isolation_proof_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
```

| 버전/basis/actor | 수용 조건 |
|---|---|
| v1의 engine 이벤트 | 합성 시험·과거 형식 회귀 검증에서만 기존 문맥/시각 규칙으로 수용. 공식 비교표의 귀속 근거로 사용하거나 v1.1 격리 이벤트로 승격하지 않음 |
| v1.1의 `isolated_window` + engine | 유효한 서명·격리 증명·집행/관측 근거와 그 증명의 canonical digest가 모두 일치. 실행 ID·회차·스냅샷도 일치 |
| v1.1의 `direct_event` 또는 proof 없는 engine | 거부. v1.1 엔진 귀속은 반드시 isolated_window와 유효 proof를 요구 |
| v1.1의 other/unknown | execution_id/basis/digest는 null, 성과에 귀속하지 않음. 유효 격리 proof를 동시에 제출할 수 없음 |

`isolated_window`는 각 이벤트마다 **actor=engine·basis=isolated_window·증명 digest**를 모두 요구한다. 엔진의 solved=true, actor만 있는 행, `exclusive_window=true`, 서명되지 않은 manifest 선언은 대체물이 아니다.

- 이벤트 challenge마다 해당 구간의 정확한 `pre=false → post=true`를 확인한다. 단순 `post=true`, pre부터 해결, true→false, key 누락·인스턴스/reset 혼입은 수용하지 않는다. ID/challenge 중복도 기존 규칙대로 거부한다.
- 격리 이벤트는 델타를 확인한 **post 관측 시각·sequence**를 사용한다. 현재 직접 이벤트의 engine_start..drained 검사를 그대로 적용해 가상의 요청 시각을 만들지 않는다. 격리 경계의 끝은 post 관측 완료까지다.
- 유효한 proof를 제출하면 러너는 전체 `false→true` 델타에서 이벤트를 발행한다. 채점기는 **신규 해결 key 집합과 engine/isolated_window 이벤트 key 집합의 완전 일치**를 확인한다. 신규 해결 누락/추가·other/unknown 혼입은 입력 오류로 중단한다. 델타가 비었을 때만 빈 이벤트 배열을 허용한다. proof 없이 이벤트를 조작해 빈칸을 채우지도 않는다.
- proof=null로 명시한 미수집이고 engine 귀속 주장도 없으면 새 해결의 귀속은 미확인이다. 유효 proof를 주장하면서 이벤트 자료가 빠졌거나, 제출된 증명의 서명·문맥·필수 필드·참조·시각·해시·실제 로그 정합성이 잘못되면 입력 오류로 채점을 중단한다. 위반을 발견한 러너는 성립하지 않은 proof를 발행하지 않고 귀속 불가 사유를 보존한다.

## 5. 구현 전 필수 회귀·실측 검증

아래는 **예정 사례**이며 실행한 테스트가 아니다.

| ID | 사례 | 기대 결과 |
|---|---|---|
| IW01 | baseline·개선 후 동일 v1.1 격리 계약·유효 증명/델타 이벤트, E² traffic ID 없음; v1/v1.1 회차 혼합 | 동일 v1.1이면 같은 귀속 규칙 적용, E² 보유 여부로 갈리지 않음. 버전/귀속 방식 혼합 비교는 거부 |
| IW02 | 신뢰 러너가 유효 격리 증명 아래 전체 false→true 델타에서 이벤트를 발행, engine/basis/digest·post 관측 문맥 모두 일치 | engine_solved로 전달 가능. actor는 귀속 대상이며, 최종 확정은 평가 §4의 다른 게이트 유지 |
| IW03 | 엔진 자기 선언 solved=true 또는 actor=engine만, exclusive_window boolean, manifest 자체 귀속 선언 | 격리 귀속 거부. 엔진의 판정 레코드만으로 러너 해결 이벤트를 생성하지 않음 |
| IW04 | proof/필수 evidence/서명 누락·잘못된 digest·서명 후 proof 변조 | 오류·집계 표 미출력. null로 명시한 미수집과 구분 |
| IW05 | 다른 run/model/reset/instance/execution/proof 재사용·pre/post digest 불일치 | 오류·집계 표 미출력 |
| IW06 | pre=true인 항목의 신규 이벤트, true→false, key 불완전, 델타와 무관한 이벤트·중복 이벤트 | 입력 오류·집계 표 미출력. 이벤트 없이 pre=true인 항목은 기존 사전 해결 규칙 유지 |
| IW07 | 공개 listener·전용 네트워크 우회·정책 공백·호스트 조건 불충족, host 참조/확인 기록 누락·다른 문맥·부분 구간/관측 공백 | 호스트 확인 시각·근거와 pre 관측~post 완료 전체 구간을 대조. 확인 불가/불충족이면 proof/귀속 발행 0, 제출한 proof의 참조 누락/위조·문맥/구간 모순은 채점 중단. 네트워크만으로 same-host 차단 주장 금지 |
| IW08 | pre 이후 준비·fixture 변경·reset/인스턴스 교체 | 실제 변경 경로 차단, 구간 성립 불가 시 proof/귀속 발행 0 |
| IW09 | 관측기의 비-GET·등록 밖 GET·redirect/재시도 우회·solve 유발 GET | 실제 관측기 경계에서 차단, 미검증 GET을 등록하지 않음 |
| IW10 | 관측 요청 수 bool/음수/null·로그 수 불일치·중복 ID·누락/미완료 결과 | 채점 입력 오류. 미수집을 0으로 표시하지 않음 |
| IW11 | 직접 이벤트 시각을 격리 이벤트에 복사하거나 post 시각·sequence 불일치 | 오류. 정상 격리 이벤트는 post 관측으로 기록 |
| IW12 | pre GET·구간 관측 GET 여러 건·post GET과 엔진 송신을 함께 계측, baseline/개선 후 비교 | 관측 구간 건수에서 pre GET 제외·post GET 포함, total_requests는 엔진 실제 송신만. 미측정은 null |
| IW13 | 유효 proof의 신규 해결 이벤트 누락/추가·other/unknown 혼입, 빈 델타/빈 이벤트; 또는 proof=null인 미수집 | 불완전/모순 입력은 오류, 완전한 빈 집합은 정상, proof=null은 엔진 귀속 미확인. 정상 engine 이벤트라도 최종 확정은 기존 평가 §4 적용 |
| IW14 | v1 직접 관측 기록, v1.1 proof 없는 direct_event/engine 기록, 알 수 없는 버전 | v1은 합성 시험·과거 형식 회귀 검증에서만 수용하고 공식 비교표 귀속에는 사용하지 않음. v1.1은 격리 증명 없는 엔진 귀속 거부, 알 수 없는 버전/느슨한 타입 변환 거부 |
| IW15 | post 델타만 있고 실제 엔진 확정 시각 없음·사람 수동 확정만 추가 | elapsed_seconds 추정 생성 0, 자동 CONFIRMED 증가 0 |
| IW16 | v1.1 `records`=null(판정 미수집)·레코드 0건 envelope·v1의 records null | null은 모든 GT 판정 판단불가(판정 미수집)·커버리지가 있어도 미탐 0, 해결 귀속은 따로 보고. 0건 envelope는 기존 규칙. v1의 null은 입력 오류 |
| IW17 | §3.1 행 오류: 모르는 kind·순번 공백, `boundary`의 `applied`·`maintained` 누락이나 중복·`released` 두 행 이상(두 행·세 행 정상 수용), 한 행 안의 중복 `kind`(예: `connectivity_check`에 점검 종류를 `kind`로 넣음)·`probe_kind` 누락, `host_watch` 공백 초과·구간 미포함, `docker_events_window` 불완전·외부 연결·재시작, pre/post `network_snapshot` 불일치, `connectivity_check` 불일치, `in_window` 관측 행 수 ≠ `request_count` | 러너는 proof 발행 0(사유 보존). 제출된 proof에서 발견하면 입력 오류·집계 표 미출력 |

합의한 계약의 후속 순서는 채점기·합성 회귀 구현 → 실제 러너의 격리/준비/관측 경계 구현과 우회 시험 → baseline·개선 후 같은 조건의 실측이다. 합성 서명·해시 시험은 실제 격리 집행 검증을 대신하지 않는다. 그 전까지 현재 v1의 fail-closed 동작을 합성 시험·과거 형식 회귀 검증에서 유지하며 공식 비교 완료로 표시하지 않는다.
