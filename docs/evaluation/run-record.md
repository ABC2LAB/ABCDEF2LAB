# 평가 러너 기록 v1 — pre/post·해결 귀속 입력

> **과거 버전 계약:** 이 문서는 v1 직접 관측 이벤트 형식과 당시 연결 상태를 보존한다. 본문의 “후속”·“미구현”은 현재 전체 평가 도구의 상태가 아니다. 현재 `run_record.py`·`isolation_proof.py`·`runner.py`는 [v1.1 격리 귀속 계약](run-record-v1.1.md)을 구현한다. 실행·검증 범위는 [현재 러너 안내](runner.md)를 따른다. 공식 비교에는 v1.1만 사용하며, 아래 v1의 필드·보안·수용 조건은 변경하지 않는다.

평가 규칙의 정본은 [평가 프로토콜](evaluation-protocol.md)이다. 이 문서는 저장 자료를 채점기에 전달하는 구현 계약이다.
현재 구현은 합성 자료로 검증했다. 실제 러너·관측기·traffic/coverage exporter 연결과 최민준 리뷰는 후속이다.

**적용 범위:** 아래 v1 직접 관측 이벤트 수용은 합성 시험·과거 형식 회귀 검증용이다. 공식 비교표에서는 [v1.1](run-record-v1.1.md)의 `isolated_window` 귀속만 사용한다. 현재 v1 채점기의 통과를 공식 비교 완료로 간주하지 않는다.

## 신뢰 경계

manifest는 파일 위치와 비교 행을 지정한다. 해결 주체나 요청 종료를 스스로 선언할 수 없다.
엔진과 키를 공유하지 않는 **신뢰 러너**가 실제 관측 후 `run_record.sign_run_record(payload, key)`로 기록을 발행한다.
채점기는 별도 CLI `--runner-key-file`로 선택한 키를 사용한다. 키는 최소 32-byte 랜덤 값이며 결과물과 엔진 밖에서 보관한다.
키 파일은 일반 파일·소유자 전용 권한(예: `0600`)이어야 한다. 그룹/기타 읽기·쓰기·실행 권한이나 심볼릭 링크가 있으면 채점을 중단한다.

서명은 HMAC-SHA256이다. `signature`를 제외한 JSON을 `sort_keys=True, separators=(",", ":"), ensure_ascii=False, allow_nan=False`로 UTF-8 직렬화한다.
소문자 64자리 hex signature를 `hmac.compare_digest`로 대조한다. 중복 JSON key·NaN·Infinity는 거부한다.
서명은 발행자/변조 여부를 검증한다. 실제 수집·격리·종료 또는 이벤트 관측의 진실성을 만들어 주지는 않는다.
키를 가진 운영자는 기록을 만들 수 있으므로 키를 엔진·LLM·결과 exporter 임의 입력에 노출하면 신뢰 경계가 성립하지 않는다.

## 러너 기록 필드

아래 표의 모든 필드는 필수이며 `signature`를 제외한 전부가 서명 대상이다. manifest의 조건·반복·측정값도 반드시 일치해야 한다.
측정값을 얻지 못했으면 필드를 생략하지 않고 null로 기록한다. `repeat_index`·두 요청 수는 0 이상 정수이며 bool은 허용하지 않는다.
`elapsed_seconds`는 0 이상 유한수다. 조건·측정값 부분의 예시는 다음과 같다.

```json
{"condition": "우리@35b", "repeat_index": 0, "elapsed_seconds": null,
 "total_requests": null, "blocked_attempts": null}
```

| 필드 | 내용 |
|---|---|
| `version` | 정수 1 |
| `run_id`, `engine`, `model_tag`, `reset_generation`, `condition`, `repeat_index` | manifest와 정확히 일치 |
| `elapsed_seconds`, `total_requests`, `blocked_attempts` | 러너의 실제 측정값 또는 null. manifest와 대조하여 표의 값 변경/누락을 차단 |
| `instance_id`, `execution_id` | 실제 대상 인스턴스와 엔진 실행의 고유 ID |
| `fixture_fingerprint` | [프로파일 지문 형식](profile-fingerprint.md)에 따른 준비 명세 ID/revision과 선언된 fixture 조건의 SHA-256. 비교 대상의 계정 라벨/역할·fixture 조건은 같아야 함. 비밀/표식 값을 기록하지 않음. 지문만으로 준비 완료·실제 데이터 적용을 증명하지 않음 |
| `environment_fingerprint` | 고정 모델/샘플링·대상 이미지·범위/예산 등 공통 평가 설정의 SHA-256. 엔진별 소스 SHA는 공통 지문에 섞지 않음 |
| `requests_drained` | 실제 진행 요청 종료를 러너가 확인한 true. false/누락은 채점 중단 |
| `timeline` | 아래 6단계 각각 `{"at": "timezone 포함 ISO 시각", "sequence": 0 이상 정수}` |
| `artifacts` | 아래 8개 키. 선택 자료도 키는 필수이며 미수집이면 null |
| `signature` | 외부 러너 키의 HMAC-SHA256 |

단계는 `fixture_ready → pre → engine_start → engine_end → drained → post`다.
벽시각은 비감소, sequence는 엄격 증가해야 한다. `engine_end`는 신규 송신 종료, `drained`는 진행 요청의 종료 확인이다.
벽시각만 같아도 sequence로 순서를 구분한다. 중간 reset/인스턴스 교체를 같은 실행으로 발행하면 안 된다.

## 아티팩트 필드

필수 키는 `pre_raw`, `pre`, `post_raw`, `post`, `records`다. 선택 키는 `traffic`, `coverage`, `solve_events`다.
각 비-null descriptor는 다음 필드만 가진다.

```json
{"path": "같은 기록 디렉터리 안의 상대경로", "sha256": "<바이트 SHA-256>", "size": 0,
 "phase": "pre 또는 post 또는 engine", "run_id": "...", "model_tag": "...",
 "reset_generation": "...", "instance_id": "...", "execution_id": "..."}
```

descriptor의 5개 신원 필드는 러너 기록과 같아야 한다. pre/raw는 phase=pre, post/raw는 phase=post, 나머지는 phase=engine이다.
경로 이탈·symlink·같은 파일의 중복 역할은 거부한다. 열린 디렉터리 FD 기준으로 각 경로 요소를 O_NOFOLLOW로 열어 검사/읽기 사이 교체도 차단한다. 안전한 dir_fd/O_NOFOLLOW를 지원하는 POSIX 환경이 필요하며 미지원 환경은 중단한다. 해시를 검증한 동일 바이트를 JSON으로 해석한다.
manifest의 records/pre/post 경로도 해당 descriptor와 일치해야 한다. 결과 디렉터리의 심볼릭 링크 별칭은 실제 경로로 정규화해 비교하지만, 아티팩트를 읽을 때는 각 경로 요소의 심볼릭 링크를 거부한다.

- `pre_raw`, `post_raw`: 대상 `/api/Challenges` 응답 행 배열 또는 `{"data": [...]}`. 대상의 raw 응답이다.
- `pre`, `post`: 위 5개 신원 필드 + `phase` + `solved`만 있는 envelope. `solved`는 `{key: bool}` 정규화 맵이다.
  각 raw의 정규화 결과가 맵과 정확히 같아야 한다. 양 시점의 전체 key 집합이 같고 GT 분모의 key가 완전해야 한다.
- `records`: 기존 `{"run_id", "model_tag", "reset_generation", "records": [...]}` envelope.
  각 판정 레코드의 모델/reset도 같아야 한다. GT key를 판정 레코드에 넣지 않는다.

선택 자료의 envelope는 위 5개 신원 필드 + `phase: "engine"` + 아래 배열 필드만 가진다.

| 자료 | 배열과 행 필드 |
|---|---|
| `traffic` | `requests`: `id`, `resource_ref`, `vuln_class`, `sent_at`, `completed_at` |
| `coverage` | `inspections`: `traffic_ref`, `completed: true` |
| `solve_events` | `events`: `id`, `challenge`, `actor`, `execution_id`, `observed_at`, `sequence` |

traffic은 실제 송신·응답 완료된 엔진 요청의 비민감 인덱스만 export한다. 준비·차단·미완료 요청은 이 인덱스에 넣지 않는다.
송신은 engine_start..engine_end, 응답 완료는 송신..drained 안이어야 한다. coverage는 그 traffic ref에 연결된 완료 검사만 인정한다.
분류는 자연 자원 참조·vuln_class를 평가 상관표로 연결하며, 상관 불가 항목을 임의로 미탐으로 만들지 않는다.

## 직접 해결 이벤트

이벤트는 엔진이 보고한 finding을 변환해서 만들지 않는다. 신뢰 target observer/runner가 직접 관측한 신규 해결만 export한다.
`actor`는 `engine`, `other`, `unknown` 중 하나다. 이벤트의 challenge는 평가 전용이며 엔진에 전달하지 않는다.

- `engine`: 실행 ID가 러너 기록과 같고 시각·sequence가 엔진 시작..요청 종료 구간 안이어야 한다.
- `other`/`unknown`: execution_id는 null이며 성과에 귀속하지 않는다.
- 모든 이벤트는 pre..post 사이의 `false→true`와 연결되어야 한다. ID/챌린지 중복이나 상태 모순은 오류다.
- 이벤트 미수집(null)·빈 배열·other/unknown만 있으면 새 해결도 귀속 미확인으로 판단불가다.
- v1에는 `exclusive_window=true`나 manifest의 자체 귀속 선언을 허용하지 않는다.

Juice Shop scoreboard의 pre/post 해결 상태만으로는 해결 주체를 알 수 없다. 실제 러너·관측기가 `actor=engine` 이벤트를 발행하지 못하면 새로 해결된 항목은 전부 귀속 미확인으로 판단불가다. 따라서 현재 합성 시험의 확정·해결 기반 미탐 수를 실측 가능 성과로 해석하면 안 된다.
baseline·개선 후에 같은 격리 보증 구간을 적용하는 [v1.1 입력 계약](run-record-v1.1.md)의 상세 필드를 합의했다. `actor=engine`·`basis=isolated_window`·서명된 격리 증명/근거·정확한 pre/post 델타를 함께 요구하며, E② traffic id를 해결 귀속의 필수 조건으로 삼지 않는다. 현재 v1 채점기는 이를 아직 수용하지 않는다. v1.1 채점기·러너 구현과 실제 격리 집행 검증은 후속이다.

## 반복 비교와 실패

한 표에서 run/reset/execution을 재사용하지 않는다. 모든 회차의 fixture/환경 지문과 채점 분모의 pre 상태가 같아야 한다.
사전 해결 항목을 분모에서 제거하지 않는다. 파일·필수 메타데이터·시각·해시·서명 오류는 종료 코드 2, stdout 빈 값으로 중단한다.
유효 자료 안에서 해결 주체/증거가 없는 경우는 판단불가다. 실제 러너와 관측기가 이 기록을 발행하기 전에는 합성 테스트 통과를 실측 완료로 보고하지 않는다.
