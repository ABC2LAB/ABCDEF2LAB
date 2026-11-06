# 평가 스크립트

baseline과 개선 후를 [평가 프로토콜](../../docs/evaluation/evaluation-protocol.md)의 같은 판정표로 채점한다.
GT는 평가 전용이며 엔진·탐색·판정 입력으로 전달하지 않는다. 표준 라이브러리만 사용한다.

## 실행

```bash
cd scripts/eval
python3 -I -B -m unittest discover -s tests
python3 -I score.py /path/to/manifest.json --runner-key-file /private/runner.key
```

러너 키는 엔진과 결과물 디렉터리 밖에서 보관하는 최소 32-byte 랜덤 값이다. CLI에는 경로만 전달한다.
키 값은 결과물·로그·환경변수·LLM 입력에 넣지 않는다. 키를 가진 신뢰 러너가 실제로 수집한 기록만 서명해야 한다.
HMAC은 발행자와 무결성을 검증하며 실제 격리·종료·해결 관측을 대신하지 않는다.

manifest의 회차 항목:

```json
{"runs": [{"condition": "우리@35b", "engine": "ours", "run_id": "<고유 회차>",
           "model_tag": "<정확한 태그>", "reset_generation": "<고유 reset 세대>", "repeat_index": 0,
           "records_path": "r0/records.json", "pre_snapshot_path": "r0/pre.json",
           "snapshot_path": "r0/post.json", "run_record_path": "r0/runner.json",
           "elapsed_seconds": null, "total_requests": null, "blocked_attempts": null}]}
```

경로는 manifest 기준 상대경로 또는 절대경로다. 서명된 러너 기록은 각 원자료의 경로·크기·SHA-256과
회차·모델·reset·인스턴스·실행을 묶는다. 러너 기록의 아티팩트 경로는 기록 디렉터리 안의 상대경로만 허용한다.
기존 단일 post manifest는 pre/러너 기록이 없어 채점에서 거부한다. 상세 형식은 [러너 기록 계약](../../docs/evaluation/run-record.md)을 따른다.

## 채점 동작

- pre/post 모두 GT key·bool이 완전해야 하며, 원본을 정규화한 결과와 저장 정규화 맵이 일치해야 한다.
- fixture 준비 → pre → 엔진 시작 → 신규 송신 종료 → 진행 요청 종료 확인 → post 순서와 증가 sequence를 검증한다.
- 파일·해시·회차·단계·시각 오류, 해결 역행, 중복 회차/실행, fixture·환경·pre 상태가 다른 비교는 표를 출력하지 않고 종료 코드 2로 중단한다.
- pre=true는 분모에 유지하고 판단불가 + 경고로 표시한다. 새 solve도 독립 엔진 귀속 근거가 없으면 판단불가다.
- v1은 신뢰 관측기의 직접 해결 이벤트만 지원한다. 단순 `engine_solved=true`나 독점 실행 boolean은 입력으로 받지 않는다. `--runner-key-file`은 일반 파일·소유자 전용 권한(예: `0600`)이어야 한다.
- traffic/coverage는 러너 기록에 해시로 묶인 엔진 실행 자료만 인정한다. 미수집은 명시적 null이며, 준비·차단·미완료 시도는 검사 커버리지가 아니다.
- 미측정 시간·요청 수·차단 수는 `—`다. 총 요청 수는 실제 전송 HTTP, 차단 시도는 별도 열이다.
- confirmed는 독립 귀속과 증거 3조건을 모두 충족해야 확정이다. ALLOWED는 NONE과 구분하며 판단불가 + 경고다.
- stdout에는 두 매트릭스(코어 GT 9 / BAC 10), stderr에는 경고/오류를 출력한다. 같은 입력과 키는 같은 표를 낸다.

## 모듈과 연결 상태

- `manifest.py`, `run_record.py`: 회차·신뢰 러너 기록·파일·pre/post·직접 해결 귀속 검증.
- `scoreboard.py`, `collect.py`: 정규화·판정 레코드 파싱. 채점은 해시 검증과 같은 바이트의 payload를 사용한다.
- `score.py`, `aggregate.py`, `evidence.py`: 엔진 공통 판정·집계와 증거 확인.
- `correlation.py`, `gt_correlation.json`, `ground_truth.py`: 평가 전용 상관·분모. 실제 type vocab 정렬은 후속이다.
- `control_demo.py`: 통제 효과 시연 A 표(프로토콜 §8.1). 조건별 `runs_root` 하나가 열 하나고, 열 이름은 서명된 회차 기록의 조건 이름이다. 서명과 근거 파일 해시를 확인한 뒤 전달 요청의 메서드별 건수, E② 결정별 건수, 상태 차이, 해결 기록을 건수로만 낸다. 재지 않은 값은 `—`다. 막아서 못 한 것과 원본 ②에서 새는 경로는 재지 않으므로 결과 문서에 따로 적는다.
- 원본 ② 규칙 주입: 러너 설정 `engine_rules`로 켠다. `runner_engine.py`가 파일을 검사하고 회차마다 새 엔진 DB에 넣은 뒤 다시 읽어 대조한다. 형식은 `docs/evaluation/engine-rules.md`.
- `state_diff.py`: 같은 컨테이너 pre·post SQLite 사본의 상태 차이(프로토콜 §8.2). 회차 키로 HMAC한 행 지문으로 맞대고 건수·정수 id·테이블·열 이름·sqlite_sequence 값만 낸다. `runner_state.py`가 사본을 뜬다(대상 pause → 데몬 `docker cp` → unpause, WAL이면 `-wal` 동반, 쓰기 도중 저널이면 거부, 러너 0700 디렉터리에 0600). 런타임 디렉터리는 tar로 받아 항목별 지문으로 비교하고 건수만 낸다. 메모리 저장은 등록한 비로그인 GET을 계측기 `state-read` 역할로 대상 netns에서 읽어 건수·지문으로 비교한다.
- 실제 러너/target observer·E² traffic export·커버리지 export와 이 계약의 연결은 아직 없다. 테스트는 전부 합성 임시 자료다.
- baseline 어댑터는 A5 미구현으로 에러 중단한다. 실제 baseline 비교/스캔을 수행한 결과가 아니다.

실시간 상태 reader의 설정은 `EVAL_TARGET_ORIGIN`, `ARTEX_LLM_BASE_URL`, `EVAL_MODEL_TAG`다.
대상 origin은 로컬(관측 front의 `127.0.0.1` 포트)만 허용하며, baseline/개선 후의 모델 식별자·제공자·샘플링·준비 조건은 프로토콜 §6에서 고정한다.
