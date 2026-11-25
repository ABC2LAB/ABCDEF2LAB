# 평가 러너 운영 안내

> **단계 기록과 현재 구현:** 이 문서에는 단계별 구현 기록이 함께 있습니다. findings-only/구조화 판정 연결 후속이라는 초기 설명과 달리 현재 [runner.py](../../scripts/eval/runner.py)는 `_collect_verdicts`·`collect_verdict_export`를 호출하는 수집 경계를 구현합니다. 서버 export 준비·실제 판정 근거의 확보와 공식 비교 적격성은 별개입니다. 미준비·미수집은 성공/0건으로 바꾸지 않으며 아래 평가·수집·실패 계약을 유지합니다.

`scripts/eval/runner.py`는 [평가 프로토콜](evaluation-protocol.md) §2의 전용 망 구성으로 baseline 또는 개선 후 엔진의 회차를
**한 번** 실행하고, 결과 기록(완료+서명 또는 무효+사유)을 정확히 하나 남긴다. 자동 재시도는 없다. 실패하면 사유를 남기고
끝나며, 다시 돌리면 새 `run_id`의 새 회차다. 실패 회차도 결과 기록이 남으므로 집계에서 건수와 사유를 보고한다.

> 상태: 개발 머신에서 local 백엔드(가짜 LLM)와 api 백엔드(OpenRouter) 스모크를 마쳤다(아래 "스모크 실측"). 평가 머신 확인은 아직이다.
> 아래 "아직 안 된 것"을 먼저 확인한다.

## 실행

```bash
python3 -I -B scripts/eval/runner.py --config /private/eval/runner.json --preflight /private/eval/preflight.json --side ours \
  --condition "우리@35b" --repeat 0
```

- `--condition`·`--repeat`는 측정 매트릭스 행 이름과 반복 번호다. v1.1 러너 기록에 서명되며 채점 manifest의 값과 정확히 같아야 한다.
- 종료 코드: `0` 완료(서명됨), `1` 무효(사유는 결과 기록), `2` 설정·사전검사·작업 대상·분류 매핑·비밀번호 형식 오류(회차를 만들지 않음).
- 설정·사전검사·작업 대상·분류 매핑이 통과하면 엔진 관리자 비밀번호를 **한 번** 묻는다(getpass, 화면에 표시 안 됨). 비밀 env 파일의
  `EVAL_ENGINE_ADMIN_PASSWORD`와 같은 값이다. 비밀번호는 러너 프로세스 메모리에만 있고 argv·환경변수·docker 호출·회차 파일·
  출력·예외 문구에 넣지 않는다. 토큰은 작업 생성 직전과 findings 직전에 각각 루프백 엔진 API(`127.0.0.1:ports.engine_api_host`)로
  새로 로그인해 받고 그 호출 안에서만 쓴다.
- 터미널 없이 돌릴 때는 `--admin-password-file <파일>`로 getpass 대신 파일에서 읽는다. 러너 uid 소유·정확히 0600·링크 아닌 일반
  파일만 받고 끝 줄바꿈 하나만 뗀다. 링크를 푼 위치가 `runs_root` 안이거나 러너 서명 키·경로 가림 키·비밀 env 파일과 같은 파일이면
  읽기 전에 거부한다(종료 코드 `2`, 회차를 만들지 않음). 값은 위와 같이 메모리에만 있다. 세션이 끝나면 파일을 지운다.
- stdout은 `run_id 상태 사유…` 한 줄이다. docker의 진단(stderr)은 운영자 터미널로만 가고 러너 기록에는 남지 않는다.
- 회차 중간에 운영자가 할 일은 없다. 작업은 러너가 설정의 `task` 내용으로 엔진 API에 만든다(아래 "작업 생성").

회차의 순서·입력 준비·집계는 [회차 실행 절차](official-run.md)(공식 회차·시범)를 따른다.

채점: 완료 회차의 `run-record.json`을 채점 manifest의 `run_record_path`로 넣고 `python3 -I -B scripts/eval/score.py <manifest> --runner-key-file <러너 키>`로 채점한다(`records_path`는 records=null이면 읽지 않는다). E② 자리가 있는 조건이 있으면 성능 매트릭스 뒤에 **개선 후 통제 검증 표(프로토콜 §8.3)**가 따로 붙는다. 행은 대조 근거 없는 회차·다섯 칸·차단 후 대상 도달·E② 기록 없이 대상 도달·ID 재사용·허용 양성 근거 회차·통제 주장이고, 값은 회차 합계(회차별 값, 근거 없는 회차는 —)다. 격리 증명이 없어 대조 근거가 없는 회차도 그 조건의 분모에 남고 보류로 센다(미완 기록·미결속은 수용 보류). E② 자리를 서명 근거로 확인한 회차가 하나도 없는 조건은 표에 나오지 않는다. 통제 주장을 보류한 조건은 사유 건수를 stderr 경고로도 낸다. 가짜 E² 조건은 조건을 채워도 "통제 증명 아님"으로 표시한다.

## 준비물

| 항목 | 요구 |
|---|---|
| Docker | 서버 28.0.0 이상(§2), 평가 전용 호스트. Docker Desktop과 WSL 내장 dockerd가 소켓을 다투지 않게 하나만 쓴다 |
| 이미지 | baseline 엔진(`base/artex-import`를 깨끗한 작업 트리에서 빌드), 개선 후 엔진(빌드한 dev 커밋), 엔진 DB, 대상(사전검사 `pins.target_image`), 계측기(`deploy/eval/meter.Dockerfile`). 러너는 pull하지 않으며 설정의 내용 ID(`sha256:`)와 로컬 이미지가 같아야 한다 |
| 사전검사 선언 | [사전검사](preflight.md) 형식. 러너가 다시 검사한다 |
| 비밀 env 파일 | `deploy/eval/eval.secrets.env.example`을 복사해 레포 밖에 두고 채운다(관리자·DB 비밀번호, api 백엔드면 `EVAL_LLM_API_KEY`). 러너는 이 파일을 읽지 않고 compose가 직접 읽는다 |
| 서명 키 | `e2-audit-keygen`으로 엔진 감사 키와 **별도로** 발급, 0600, `runs_root` 밖 |
| 분류 매핑 | 평가자가 검토한 원본 분류 → 평가 `vuln_class`·자산 type 매핑 JSON(형식은 `baseline_extract.py`, 8MiB 이하·중복 키 거부). baseline·개선 후가 **같은 파일**을 쓴다. 원본 SHA-256이 `findings.json`에 남아 집계에서 대조한다. 매핑에 없는 값은 실패가 아니라 `null` + `unmapped_fields`로 남는다 |
| `runs_root` | 러너 uid 소유·정확히 0700 디렉터리. 회차마다 그 아래에 새 디렉터리를 만든다 |
| CA 묶음 | api 백엔드에서 게이트웨이가 쓸 호스트의 TLS 신뢰 묶음 경로(읽기 전용 마운트) |
| LLM 고정 조건 | api: 고정할 제공자 전체 slug(사전검사 `llm.provider`. `deepinfra/fp8` 같은 변형 tag도 접두 매칭이라 단일 엔드포인트·정밀도를 보장하지 않으며, 정밀도 고정 근거는 프로토콜 §6의 메타데이터 확인을 거쳐야 함)와 그 응답 표시 이름(설정 `expected_provider_name`, 변형 없이 예: `DeepInfra`)을 팀이 함께 정한다. local: 로컬 서버를 고정 IP에만 바인딩 |

## 설정(`runner.json`, 비밀 없음, 레포 밖)

모든 필드가 필수이고 모르는 필드는 거부한다. 값의 이유 코드는 `config.<필드>: <코드>` 형식으로만 출력한다.

| 필드 | 뜻 |
|---|---|
| `version` | `1` |
| `runs_root`, `secrets_env_file`, `runner_key_path` | 절대 경로. 키는 `runs_root` 안이면 거부 |
| `images.{engine_baseline,engine_ours,engine_db,meter}` | `{"ref": 태그 또는 digest, "id": "sha256:…"}`. `latest`·TODO 거부 |
| `target_image_id` | 대상 이미지 내용 ID(참조는 사전검사 `pins.target_image`) |
| `ports.engine_api_host` | 엔진 API를 publish할 호스트 루프백 포트 |
| `ports.engine_container` | 엔진 이미지가 API를 듣는 컨테이너 포트 |
| `ports.target`, `ports.obs`, `ports.obs_host` | 대상 포트(계측기 upstream), 관측 front 포트와 그 호스트 루프백 포트 |
| `ports.llm_gateway`, `ports.api_front` | LLM 게이트웨이·엔진 API front의 컨테이너 포트 |
| `observer_path` | 관측 front가 통과시킬 스코어보드 GET 경로 1개 |
| `observer_format` | 관측 응답 형식. 공식 회차는 `challenge_scoreboard`. `opaque`는 점수판 없는 연습 대상용이고 응답 원문을 남기지 않으며, 그 회차는 채점기가 거부한다([연습 대상](practice-targets.md)) |
| `observer_readonly_ref` | 그 GET이 해당 대상 버전에서 상태를 바꾸지 않음을 확인한 별도 기록의 참조(200자 이하). 러너가 만들 수 없는 근거라 설정으로 받고, 격리 근거 `observer_readonly` 행에 그대로 남긴다 |
| `engine.llm_provider`, `engine.llm_stream` | `openai`만(게이트웨이가 chat completions만 받음), 스트리밍 여부 |
| `engine.llm_policy_mode` | 엔진 LLM 정책 모드(`ARTEX_LLM_POLICY_MODE`, #125). `external`만 받는다: 평가는 LLM을 게이트웨이로만 내보내고, 엔진 기본값 local-only는 운영자 manifest·DB profile 준비가 필요해 §6 음성 시험에서 따로 쓴다. baseline(e53a79f)에는 이 스위치가 없다. 결과 기록에 선언값만 남는다(엔진이 현재 모드를 드러내지 않아 관측값 null). baseline 회차는 compose 값이 같이 넘어가도 엔진이 읽지 않으므로 선언값도 null이다 |
| `e2.ours_mode` | 개선 후 회차의 E② 자리(프로토콜 §2 개선 후의 E② 배치). `none` = E² 없음(엔진 → 계측기), `stub` = 통과 전용 가짜 E²(`deploy/eval/compose.e2-stub.yaml`, 계측기 `-role=e2-stub`)를 엔진과 계측기 사이에 둔다. baseline 회차에는 적용하지 않는다(원본에는 E²가 없다). `stub` 회차는 망·부정 연결 점검·계측기 시도 ID 대조를 세우는 fixture이며 통제를 증명하지 않으므로 시범·공식·제품 결과에 쓰지 않는다. 결과·기록에 `e2_mode`가 서명되고, `session_runs.py`·`score.py`는 `--allow-fixture-e2` 없이는 stub 회차를 거부한다. 제품 E²가 들어오면 값이 하나 늘어난다 `product` = 제품 E²(아래), 공식 비교에 쓰는 값이다. |
| `engine_rules` | 원본 ② 규칙 파일의 절대 경로 또는 `null`(프로토콜 §8.1, 형식은 [engine-rules.md](./engine-rules.md)). 원본 회차에만 쓴다. 조건마다 설정 파일을 따로 둔다 |
| `purpose` | 회차 목적. `official`은 공식 비교 회차다. 분리 배치(`split`)가 아닌 공식 회차(엔진 직접 배치)는 계측기 경로 기록이 있으면 무효이고, 분리 배치 공식 회차는 경로 기록이 없으면 무효다(2026-11-18 결정 (a): 공식 비교의 발견 집계가 B·O 계측기 전달 요청의 경로 틀이라서, 공식 측정 프로토콜 §7). `browser_acceptance`는 [브라우저 수용시험](browser-acceptance.md) 보존 회차이고 경로 기록이 없으면 무효다. `wiring_check`는 분리 배치 O 공식 흐름(점수판 관측 → pre/post 해결 맵 → `isolated_window` 귀속)을 유료 공식 회차 전에 실제 대상으로 돌려 보는 배선 확인 회차다([O 공식 run-record 설계안](split-official-run-record.md) §8 3번). 분리 배치(`split`)에서만 받고(`WIRING_CHECK_NEEDS_SPLIT`), 합성 모델 모드를 쓸 수 있다. Juice Shop 스크립트 요청 목록은 `deploy/eval/synthetic/juice-shop.json`에 고정한다(첫 화면이 부르는 공개 GET 둘, 챌린지·정답 정보 없음, 시험이 목록을 고정). 채점기는 실행 출처와 상관없이 회차 하나·비교 표 모두 거부한다. 값은 서명된 실행 조건에 남는다. 격리 증명 검증은 목적과 상관없이 하고, 채점기는 `official`이 아닌 회차가 섞인 표를 거부한다. 빠지면 `official`이다(목적 칸이 생기기 전 설정·근거도 그대로 읽는다) |
| `path_key_file` | 경로 기록이 있어야 하는 회차(`purpose=browser_acceptance`, 그리고 `split`이 있는 회차)에만 필수인 경로 가림 키 파일(절대 경로, `runs_root` 밖, 러너 uid 소유·0600·32바이트). 비교할 원본·개선 후 회차 묶음이 같은 파일을 쓴다. 러너가 비밀번호를 묻기 전에 링크를 푼 실제 위치가 `runs_root` 밖인지, 러너 서명 키·비밀 env 파일과 다른 파일인지, 계측기와 같은 조건인지 확인하고, 키 참조(`path_key_ref`, 16 hex)를 실행 조건에 서명해 남기며 계측기 경로 행의 참조와 대조한다(다르면 `meter_path_key_mismatch`). 그 회차에만 `deploy/eval/compose.acceptance.yaml`을 얹어 계측기를 경로 기록과 함께 띄우며 키를 읽기 전용으로 넘긴다. 탐색 분리 배치 O는 `compose.split.yaml`이 같은 방식으로 같은 파일을 계측기에 주고, 러너가 같은 바이트의 보호 입력 사본을 경계 `path_mask_key_file`로 준다(계약 §4.1, 경계·계측기 같은 키). 분리 배치가 아닌 공식 회차에 있으면 설정 오류다. 키는 제어 비밀이다. 값은 커밋·결과물·모델 문맥 어디에도 넣지 않고, 서명 조건에는 키 지문(`path_key_ref`)과 분리 배치 표시(`split`)만 남는다. 한 측정 세션의 B·O, 공식 측정이면 6회 전체가 같은 키 파일을 쓴다. 채점기는 분리 배치 회차가 섞인 표에서 분리 배치 표시·키 지문이 모두 같지 않으면 거부한다. 키 지문은 측정 조건 동결 목록에 넣는다 |
| `preservation` | 선택적인 보존 원자료 서명 메타데이터. 수용시험 회차와 분리 배치(`split`) 회차에서 받고(분리 배치는 공식 회차여도 필수, 2026-11-18 결정 (a)), 분리 배치가 아닌 공식 회차에서는 금지한다. 서명 기록의 `purpose`는 회차 목적(`browser_acceptance`·`official`) 그대로이고 한 묶음 안에서 섞이면 거부한다. 형식·보호 키·재계산·NOT_RUN 제한은 [서명된 보존 원자료](preservation-signed-runs.md). 생략하면 기존 동작이고, 현재 Docker 러너의 개선 후 배치는 `e2-legacy/v1`로만 발행한다 |
| `state` | 상태 차이(프로토콜 §8.1·§8.2) 설정. `null`이면 사본을 뜨지 않는다. 객체면 `sqlite_path`(대상 컨테이너 안 SQLite 절대 경로), `signal_tables`(같은 DB에 있는 해결 기록 등 신호 테이블 이름 목록, 상태 차이 판정에서 빼고 건수만 따로), `max_copy_bytes`(사본 하나의 상한), `runtime_roots`(비교할 런타임 디렉터리 절대 경로 목록, 겹치면 거부, 비어도 됨. 각 루트는 실제 디렉터리여야 하고 링크·정규 파일이면 pre에서 무효. 루트 안의 링크는 링크 자체만 비교하고 링크 대상은 보지 않음), `memory`(메모리 저장 읽기, `null`이면 안 읽음. 객체면 `paths`·`count_field`·`max_body_bytes`·`timeout_ms`)를 정확히 받는다. 앱별 값은 여기서만 받는다(Juice Shop v15: `/juice-shop/data/juiceshop.sqlite`, `["Challenges"]`, 주문 PDF가 쌓이는 `/juice-shop/ftp`와 업로드 디렉터리). 요청이나 기동만으로 바뀌는 디렉터리(접근 로그 `logs`, 기동 때 만드는 `i18n`)는 넣지 않는다. `memory.paths`에는 부작용 없음을 확인한 비로그인 GET만 넣는다. 응답의 `count_field` 배열 길이와 본문 지문을 비교한다. Juice Shop v15는 상품별 리뷰 경로가 해당하고(2회 읽기 동일·해결 신호 변화 0 확인), 주문 목록은 비로그인·일반 계정으로 읽을 수 없어(403·500) 주문은 런타임 디렉터리의 PDF 차이로만 본다. 양쪽 회차에 같게 적용한다. pre 사본은 대상 준비 확인 뒤·구간 시작 전에, post 사본은 구간이 닫히고 tmpfs를 잰 뒤에 뜬다(대상 `docker pause`가 Docker 이벤트를 만들기 때문). SQLite 사본은 회차 키(메모리에만)로 행 지문을 뜬 즉시 지우고, 런타임 디렉터리는 호스트에 파일을 만들지 않고 tar에서 바로 지문을 뜬다. 메모리 읽기는 계측기 이미지의 `state-read` 역할을 대상 netns에서 1회 실행한다(구간 밖, loopback만, 권한·마운트 없음, 로그 드라이버 끔). 등록 관측 front를 거치지 않으므로 관측 로그 대조와 섞이지 않는다 |
| `engine_db.{user,name}`, `tmpfs.{engine_db,engine_data,min_free}`, `health.{interval,timeout,retries}` | 엔진 DB 계정 이름, 회차 메모리 크기 상한과 구간 종료 뒤 남아야 할 최소 공간(두 상한보다 작아야 함), DB healthcheck. `tmpfs` 세 값은 사전검사 `run_storage`(프로파일 v3 환경 지문 입력)와 글자 그대로 같아야 시작한다 |
| `llm_gateway.generation_path` | api: generation 조회 경로(예: OpenRouter `/api/v1/generation`) |
| `llm_gateway.verify_attempts`, `verify_interval_seconds` | generation마다 조회 횟수(1~100)와 라운드 사이 간격. 조회는 라운드로 돈다(라운드마다 답이 안 정해진 것만 한 번씩 묻고 라운드 사이에만 쉰다). 전체 조회 시한은 `횟수 × 간격 + 10초`(조회 한 번의 시간 상한. 막힌 조회는 이 상한에서 끊기고 다음 라운드에서 다시 묻는다)이고, 시한이 지나면 더 묻지 않고 표식을 쓴다. `run.llm_verify_timeout_seconds`는 `횟수 × 간격 + 50초` 이상이어야 한다(아니면 `BELOW_LOOKUP_DEADLINE`) |
| `llm_gateway.ca_bundle`, `expected_provider_name` | 호스트 CA 묶음 경로, 기대 응답 제공자 표시 이름 |
| `docker_timeout_seconds`, `probe_timeout_ms` | docker 호출 하나와 부정 연결 점검 하나의 시한 |
| `host_listen_allowlist` | loopback 밖 호스트 리슨 중 허용할 것. `{"address", "port", "reason"}`, 특정 주소만(0.0.0.0·`::`·loopback 불가), 근거 필수. 기본은 빈 목록 |
| `run.*` | compose 대기, down 시한, 대상 준비, 예산 폴링 간격, drain 시한, 스냅샷 시한·크기, LLM 검증 대기(초) |
| `run.task_create_timeout_seconds` | 작업 생성(로그인 + `POST /api/tasks`) 연결·읽기 시한(초) |
| `run.llm_load_timeout_seconds` | local 서버의 모델 적재·조회 시한(초). 회차 전에 모델을 적재해 첫 회차만 적재 시간을 시간 예산에서 쓰지 않게 한다. 적재할 때 회차 길이(compose 기동·준비·작업 생성 시한 + 시간 예산 + 상태 확인·drain·정착·스냅샷·LLM 검증 시한)만큼 유지 기간을 주고, 작업 생성 직전에 같은 유지 기간으로 다시 적재·확인한다(준비 단계가 길어 그 사이 내려갔어도 적재 시간이 시간 예산에 섞이지 않는다). compose를 내린 뒤 모델을 내린다. 다음 회차도 같은 상태에서 시작한다. 내리기 결과는 결과 기록의 `llm_unloaded`에 남기고, 실패해도 그 회차를 무효로 돌리지 않는다 |
| `run.drain_grace_seconds`, `run.post_settle_seconds` | 봉인 뒤 계측기가 진행 요청을 기다리는 유예(1~600초, `drain_timeout_seconds`보다 짧아야 함 — 계측기 `-drain-grace`로 넘김)와, 유예가 지나 취소한 요청이 있을 때 post 전 정착 대기(1~3600초). 둘 다 비교 조건이라 사전검사 `run_drain`의 `grace_seconds`·`settle_seconds`와 정확히 같아야 회차가 시작됨(`preflight.<side>.run_drain: CONFIG_MISMATCH`) |
| `run.fault_stall_seconds` | 선택. 제공자 장애 멈춤 판정 T(1~3600초, 결정안 §7·고정 초안 §2). 있으면 B·O 회차가 이 T로 판정하고 멈춤이면 `provider_fault_stall`로 무효다. 없으면 판정 재료만 남기고, 세션 집계는 그런 완료 회차를 받지 않는다(스모크는 `--allow-unjudged-fault-stall`). B·O 같은 값이어야 하고 세션 집계가 서명 결과의 `fault_stall.stall_seconds`로 대조한다 |
| `run.host_watch_interval_seconds`, `run.host_watch_max_gap_seconds` | 구간 중 호스트 리슨 확인 간격과 허용 최대 공백(초). 공백 상한은 간격보다 커야 한다 |
| `task.{description,goal}` | 엔진 작업 설명·목표(각 4000자 이하, 줄바꿈 외 제어 문자 불가). 대상은 회차 대상 origin 하나만 적는다(아래) |
| `task.{timeout_seconds,plan_heartbeat_seconds}` | 엔진 작업 시한·planner 주기(초, 0~86400, 엔진 의미 그대로: 0=시한 없음·기본 주기) |
| `task.{seed_first_intent,coverage_enabled}` | 엔진 작업 옵션(bool, 생략 불가) |
| `findings.vocabulary_path` | 분류 매핑 JSON 절대 경로(레포 밖). 회차를 만들기 전에 읽어 검증한다 |
| `findings.{page_size,timeout_seconds,max_page_bytes,max_total_bytes,max_pages}` | findings 페이지 크기(1~200), 연결·읽기 시한(초), 페이지당·합계 바이트(각 8MiB 이하), 최대 페이지 수 |

예: WSL DNS 터널링 주소를 허용해야 하는 개발 머신이라면 근거와 함께 적는다.

```json
"host_listen_allowlist": [
  {"address": "10.255.255.254", "port": 53,
   "reason": "WSL DNS 터널링 리졸버(lo /32). 대상·관측 경로 아님. 게이트웨이 도입 후 재검토"}
]
```

## 회차 순서

```
[up 전]  project 이름 중복 없음 → 이미지 내용 ID 확인 → 엔진 도구 트리 해시 → 머신 사양(CPU·논리 CPU·메모리·커널·Docker 버전·머신 ID 해시) → 회차 디렉터리(0700) 생성
[기동]   compose up(--pull never, --no-build, --wait)
[pre]    망·publish·이미지·호스트 조건 점검 → 부정 연결 점검 → tmpfs 마운트 ID·사용량(구간 전) → 대상 준비 → 구간 감시 시작 → pre 스코어보드
         계측기 전달 건수 == 0
[작업]   새로 로그인 → workers·압축 기준 창 적용 → (engine_rules가 있으면) 규칙 등록·다시 읽어 대조 → engine-rules.json
         → 실행 설정 스냅샷(GET /api/settings·/api/llm, 프록시는 설정 여부로) → workers·엔진 LLM 모델·프록시 대조
         → engine-settings.json → POST /api/tasks(설정의 task, 생성=기동) → task.json
         요청 예산 또는 시간 예산까지 폴링(계측기가 R번째 전달 뒤 새 요청을 바로 거부하므로 폴링 지연은 전달 수에 영향 없음) → 계측기 seal → drain 확인(유예가 지나도 열린 요청은 계측기가 취소·건수 기록)
         → 취소가 있었으면 정착 대기
         LLM 게이트웨이 seal(새 LLM 요청 거부·진행 중 취소 → generation 조회 시작)
[post]   점검 반복 + pre와 비교(망·주소·컨테이너 교체·재시작) → 부정 연결 점검 → post 스코어보드
         구간 감시 종료 → 구간 판정(호스트 리슨 표본·데몬 이벤트) → window-watch.json
         tmpfs 사용량(계측기 이미지 statfs 역할을 대상 PID namespace에 붙여 측정) → 마운트 ID가 구간 전과 다르면 무효 → 남은 공간 < min_free면 무효
         관측 로그 건수 == 러너 호출 수, 계측기 순번 완결·건수 일치
         LLM 검증 마커 대기 → 응답 제공자·적용 샘플링 판정 → findings(새 로그인 → 페이지 수집 → 추출)
[끝]     compose down -v(성공·실패와 무관) → 모두 통과면 서명 → runner-result.json
```

## 회차 디렉터리

```
<runs_root>/<run_id>/            0700, 러너만
  meter/                         계측기·관측 front 기록(계측기 컨테이너에만 마운트)
  e2/                            e2.ours_mode=stub이면 가짜 E²의 결정 행 `e2-stub-decisions.jsonl`(가짜 E²에만 마운트, 계측기 증거와 분리)
  llm/                           LLM 게이트웨이 기록(게이트웨이에만 마운트, 계측기와 분리)
  pre/  post/                    스코어보드 원본·정규화본(어느 컨테이너에도 마운트하지 않음, 절대규칙 7)
  pre-checks.json  post-checks.json   망 스냅샷·policy_sha256·위반 목록
  pre-probe.json   post-probe.json    경로별 기대·실제·판정, probe 컨테이너 ID
  pre-probe.cid    post-probe.cid     docker --cidfile이 쓴 probe 컨테이너 ID(구간 감시 허용 목록)
  meter-summary.json  llm-summary.json
  task.json                      작업 생성 요청 본문 SHA-256·엔진 작업 ID·요청/생성 시각(본문 자체는 없음), v2: 분리 배치 B 회차의 작업 origin·목표 공통 부분 SHA(그 밖은 null)
  subnet-check.json              up 전 대역 겹침 검사: 회차가 만들 고정 대역, 호스트 기존 Docker 망과 겹친 쌍(다른 망 목록은 없음)
  engine-end.json                엔진 끝 신호: 창 동안 첫 끝 상태·시각, down 전 마지막 작업 상태 칸(값 원본 그대로)
  cli-observed-lines.json        (분리 배치 B, B 전용 CLI 이름이 관측된 때만) 관측된 명령마다 한 줄의 모양(따옴표 밖의 관측 이름과 셸 연산자만 그대로, 나머지 낱말은 값·옵션·경로·따옴표 문자열 모두 `<인자>`, 원문 없음, 사람 확인용). 셸 낱말로 못 나누거나 64KiB를 넘는 원문은 보류 표시만
  fault-stall.json               제공자 장애 멈춤(결정안 §7): 장애별 무활동 길이·창 끝까지 이어졌는지·끝 신호, 최장 무활동, T가 설정되면 판정
  end-idle.json                  (T가 설정된 회차) 끝 신호 뒤 무활동 유예: T·끝 신호·마지막 활동·끝낸 시각(창 기준 초), 모델 호출 관찰 여부·읽기 실패 수, 정리 끝 필요 여부, 끝낸 시각에 진행 중이던 호출 수
  early-end.json                 러너 상한 전에 일찍 끝난 회차의 분류(고정 초안 §26): first_limit·끝 상태·제품 사유, 분류(provider_fault·config·ended_valid·없음)와 근거 호출(순번·결정·상태·끝 모양·시각) 또는 승인 예산 대조·시작 거부 사유
  intercept-summary.json         엔진 승인 기록의 건수: 상태별, 차단 출처별(파일 규칙·내장 규칙·모델·그 밖), 도구별(도구 입력 원문 없음)
  engine-rules.json              `engine_rules`가 있을 때만: 파일 SHA-256, 주입·내장 규칙 수, 내장 통제를 앞지를 수 있는 파일 allow 수, 내장·전체 규칙 해시, 적용 전후 도구 목록(규칙 본문 없음)
  engine-settings.json           작업 생성 전 엔진 실행 설정 스냅샷(프록시는 설정 여부만, API 키 값 없음). 표시 언어를 뺀 값의 SHA-256은 서명 근거 `run_conditions`에
  window-watch.json              구간 감시 요약(호스트 리슨 표본 수·최대 공백·위반, 데몬 이벤트 종류별 건수·경계 변경·완결성)
  findings.json                  원본 수집기·추출기의 비민감 투영(아래)
  e2-service-ready.json  e2-collection.json   e2.ours_mode=product 회차만: 서비스 대조 값, 종료·수집 결과(아래)
  e2-config/  e2-control/  e2-copy/     e2.ours_mode=product 회차만: 준비(E²에 읽기 전용), 제어 socket, 러너 전용 사본
  verdict-export.json            설정 `verdict_export`가 있는 개선 후 회차만: 수집 상태·사유·요약(아래). 모았으면 verdict-pages.json·verdict-records.json도
  attempt-reconciliation.json    e2.ours_mode=stub 회차만: 가짜 E² 결정 행 × 계측기 접수 행 시도별 대조(프로토콜 §8.3 다섯 칸·대상 응답 수신·차단 후 도달·기록 없이 도달·ID 재사용·허용 양성 근거, 건수만, fixture=true). 통제 주장은 `control_claim_withheld=false`일 때만이고, 이 값은 허용 양성 근거가 없을 때도 true다. 같은 건수가 서명 근거(`isolation-evidence.json`의 `attempt_reconciliation` 행)에도 들어가고, 채점기는 그 행으로 통제 검증 표를 만든다
  state-diff.json                설정 `state`가 있을 때만: 같은 컨테이너 pre·post SQLite 상태 차이(테이블별 행 수·추가·삭제·변경 건수·정수 id, 열 구성 변경, 테이블·인덱스·트리거·뷰 정의 변화 `schema_objects`(종류·이름만), sqlite_sequence 변화, 신호 테이블은 `signals`에 따로), 런타임 디렉터리별 항목 수·추가·삭제·변경 건수(`runtime`, 항목 경로 없음), 메모리 읽기 합계·변한 경로 수·post에서 못 읽은 경로 수(`memory`, 본문·경로 없음). `content_changed`는 SQLite 쪽만이고 전체 변화 여부는 `state_changed`로 본다. 값·지문·경로 없음. `state/pre`·`state/post` 디렉터리는 비어 있어야 정상이다(사본을 지운 뒤)
  isolation-evidence.json        격리 근거 행(러너 기록 v1.1 §3.1 형식, 완료 회차만)
  solve-events.json              pre=false→post=true 키마다 isolated_window 해결 이벤트(평가 전용 key, 완료 회차만)
  preservation-round.json        preservation 선택 시: down 성공 뒤 최종 계측기 원자료 hash·공개 조건에 별도 서명한 browser_acceptance sidecar
  run-record.json                서명된 러너 기록 v1.1(채점기 입력, 완료 회차만. records=null 판정 미수집)
  runner-result.json             완료(+서명) 또는 무효(+사유·실패 단계: 사유가 나온 점검, 예상 밖 예외·중단이면 마지막으로 시작한 단계)
```

### 계측기 요청 기록 (`meter/engine-requests.jsonl`)

요청 하나가 끝날 때 한 줄씩 쓴다. 경로·query·헤더·본문은 남기지 않는다. 러너는 키가 정확히 아래와 같지 않거나 값이 모순되면 그 회차를 무효로 한다(`meter_row_invalid`).

| 키 | 값 |
|---|---|
| `seq`·`kind`·`method`·`status`·`started_at`·`completed_at`·`response_bytes`·`outcome` | 기존 의미 그대로. `kind`는 request·stream·rejected(seal 뒤 거부) |
| `sent` | `no`(대상에 안 감 — seal 거부·연결 못 얻음) / `uncertain`(연결은 됐지만 요청을 다 썼는지 확인 못 함) / `yes`(요청을 다 씀) |
| `response` | `none`(대상이 보낸 바이트 없음. 계측기가 직접 준 502·503은 대상 응답이 아님) / `headers`(프로토콜 전환 스트림) / `complete`(본문까지 넘김) / `interrupted`(응답이 시작됐지만 끝나지 않음) |
| `attempt_id_state` | E② 시도 ID 헤더(`X-ABC2LAB-E2-Attempt`) 검사: `absent` / `valid` / `invalid` / `multiple` |
| `attempt_id` | `valid`일 때만 소문자 hex 32자, 아니면 빈 문자열(잘못된 원문은 남기지 않음) |
| `ingress` | 실제 리슨 입구 `main` 또는 `explore`. 경로 행과 일치해야 하며 봉인 뒤 거부에도 남긴다. 과거 행에는 없을 수 있지만 한 회차 안 혼합은 거부한다 |

- `sent`·`response`는 같은 시도의 진행 이력이라 서로 배타적인 건수로 합산하지 않는다. "보냈는데 응답 없음"(`yes`·`none`)과 `uncertain`이 결과 불명이다. 계측 건수만으로 대상 앱이 실제 처리한 건수를 확정하지 않는다.
- 계측기는 시도 ID를 기록한 뒤 **대상에 넘기기 전에 그 헤더 하나만 지운다**(평가 프로토콜 §8.3). 형식이 틀려도 요청을 막지 않는다. baseline에는 이 헤더가 없는 것이 정상이다(`absent`).
- E② 시도별 대조는 실제 탐색 분리 배치에서만 `main` 행으로 한정한다. 현행 공식·원본 직접·기존 E② 배치에 explore 전달이 있으면 `meter_explore_ingress_unexpected`로 무효다. `explore`의 AttemptID 부재는 정상이고 E② 기록 없는 송신으로 세지 않는다. 탐색 입구에 E② ID가 있으면 배치/기록 모순으로 거부한다. 탐색 경계 차단 근거는 별도이며 미확보를 E② 차단이나 0건으로 채우지 않는다.
- `sent=no`인데 `response`가 `none`이 아니거나, 거부 행이 `no`·`none`이 아니면 모순으로 거부한다.

근거 파일은 O_EXCL·0600으로 한 번만 쓴다. 무효 기록에는 측정값·근거 목록·서명을 넣지 않는다.

### 작업 생성

러너는 `pre` 스코어보드와 계측기 전달 0을 확인한 뒤 새로 로그인하고 `POST /api/tasks`로 작업을 만든다. 이 엔드포인트는
생성과 동시에 작업을 기동한다. 요청 구조체·핸들러·기동·대상 추출(`parseTarget`)·응답 DTO·인증 미들웨어가 upstream
`fdfb7a17`·baseline `e53a79f`·개선 후에서 같다는 것을 `tests/test_runner_engine.py`가 고정한다. 그래서 두 쪽에 같은 요청을 보낸다.

- 본문은 설정 `task`의 여섯 필드만 정규 JSON(키 정렬·공백 없음·ASCII)으로 만든 바이트다. 그 SHA-256을 `task.json`과 결과 기록의
  `task_request_sha256`에 남기고, 집계에서 baseline·개선 후 값이 같은지 대조한다. LLM 프로필·출처 작업·회사·분류·가로채기
  규칙은 보내지 않는다. 회차마다 엔진 DB가 새것이고, LLM은 엔진 환경 설정(게이트웨이)을 쓴다.
- 엔진은 `설명 + " " + 목표`에서 대상을 뽑아 자산으로 심는다(첫 URL의 scheme·host·port). 러너는 같은 정규식으로 미리
  검사해서 다음을 모두 만족할 때만 보낸다(탐색 분리 배치 결정안 §6). 아니면 회차를 만들기 전에 `task_target_mismatch`로 끝난다
  (종료 코드 2).
  - 첫 URL이 회차 대상 origin(끝 `/` 허용)과 정확히 같다.
  - 나머지 URL은 같은 origin이면 경로가 붙어도 받는다(B 본문의 승인 경로 목록). 호스트 대소문자·기본 포트 명시 여부는 같은
    origin으로 보고, userinfo(`http://eval-meter:8080@evil`)·호스트 꼬리(`http://eval-meter:8080.evil.com`)·포트만 다른 URL은 거부한다.
  - 다른 origin URL이 없고, URL 밖에 IP·도메인 꼴(`index.html` 같은 파일 이름 포함)이 없다. 점이 든 경로 이름은 같은 origin의
    절대 URL로 써야 통과한다.
- B 본문의 목표를 제품 목표 문장의 공통 부분과 같게 맞출 때는 `runner_goal.common_goal(origin, 승인 경로, 계정 라벨)`로 만든다.
  제품 Go 렌더(`urlDiagnosisCommonGoal`)와 바이트까지 같고, 러너 시험이 제품이 고정한 예시 파일과 대조한다. 계정 라벨은 B에도 같은
  시험 계정 세션을 주는 조건에서만 넣는다. 러너 설정에 연결하는 것은 배치 구현 순서 4의 제품 구동과 같이 한다.
- 작업 내용에 평가 정답(챌린지 이름·목록)을 넣지 않는다(절대규칙 7). 러너는 이것을 검사하지 않으므로 작성·검토자가 지킨다.
- 예산 시계는 생성 요청 직전부터 잰다. 생성 실패는 재시도하지 않고 회차를 무효로 한다.
- **엔진 끝 신호(`engine-end.json`, 탐색 분리 배치 결정안 §3.1):** 회차 창 동안 예산 확인 간격마다 엔진 작업 목록 API
  (`GET /api/tasks`, 원본·개선 후 같은 응답 구조)에서 이 회차 작업 항목을 읽어, 상태가 처음 끝 상태(`done`·`failed`·`timeout`,
  원본 `db.IsTerminal`과 같은 집합)가 된 시각(읽기가 끝난 시각)을 남긴다. drain·LLM 게이트웨이 봉인 뒤, `down` 전(엔진 DB가 아직
  있을 때)에 한 번 더 읽는다. 게이트웨이 봉인 뒤라 이 읽기가 느려도 모델 요청을 받는 창은 늘지 않는다. 옮기는 칸은 `status`·`completed_at`·`goals_total`·`goals_met`(Planner 판정)·`in_flight`·`llm_failover_state`뿐이고
  (이름·설명·목표·다른 작업 항목은 옮기지 않음), 값은 원본 그대로 두고 해석하지 않는다. 단건 API(`GET /api/tasks/{id}`)는 집계 세
  칸을 채우지 않아 늘 0이라 쓰지 않는다. 목록 API는 집계 조회 오류를 버리고 0으로 두므로(원본 동작) 세 칸의 0은 집계 실패와 구별되지
  않는다(`status`·`completed_at`은 무관). 토큰 하나를 다시 쓰고 401이면 한 번만 다시 로그인한다.
  창 동안 읽기는 예산 감시를 막지 않는다. 읽기는 별도 스레드에서 돌고, 감시 루프는 끝난 읽기를 거두고 진행 중인 읽기가 없을 때만
  새 읽기를 띄운다. 그래서 상태 API가 느리거나 답하지 않아도 예산 종료 시각은 관찰이 없을 때와 같다. 창이 끝났을 때 진행 중인
  읽기는 읽기 시한(`task_create_timeout_seconds`)만큼 기다리고, 그래도 안 끝나면 `engine_task_status_pending`으로 세고 결과를 버린다.
  읽기 실패는 회차를 무효로 하지 않고 `window_read_failures`·`final_error`(고정 코드)로 센다. 이 파일은 서명되는 근거 목록에
  들어간다. 끝 상태만으로는 회차를 끝내지 않는다. 다음 항목의 무활동 유예만 회차를 일찍 끝낸다.
- **끝 신호 뒤 무활동 유예(`end-idle.json`, 고정 초안 §4 (다), 2026-11-25 사용자):** 정상 완료 끝 신호(B `done`·O `completed`, O 목표
  달성 마감 포함)를 읽은 뒤 `run.fault_stall_seconds`(T, 공식 180초)초 동안 대상 요청(계측기 전달 수)·모델 호출(B 게이트웨이 행 수)이
  늘지 않으면 예산 감시 루프가 회차를 끝낸다(`stop_reason` `engine_completed`, `first_limit.runner_limit` `runner_engine_idle`). 수가 바뀐
  것을 본 폴링 시각부터 다시 세고, 모델 호출 수를 못 읽은 폴링은 활동으로 본다. O는 창 동안 모델 호출을 볼 수 없어 제품 상태의
  `drain_complete`가 참일 때만 끝낸다. T가 없으면 유예 종료도 없고, 정상 완료가 아닌 끝 상태는 러너 상한까지 간다. 유예로 끝낸 회차는
  창 뒤 호출 기록(B 게이트웨이 행·O 제품 호출 기록)으로 끝낸 시각에 진행 중이던 호출(`inflight_at_stop`)을 세고, 하나라도 있거나
  셀 수 없으면 `end_idle_inflight_model_call`로 무효다(합성 O는 호출 기록이 없어 정리 끝만 근거). 판단에 쓰는 읽기는 bind 파일과
  이미 돌던 상태 읽기뿐이라 창 동안 회차 컨테이너에 손대지 않는다.
- **먼저 걸린 상한(`first_limit`, 결정안 §3 B(a) 표):** 결과 기록에 `first_limit`을 남긴다. `value`는 `engine_completed`(엔진 끝
  신호의 첫 끝 상태가 러너 상한보다 먼저) 또는 `runner_request_budget`·`runner_time_budget`(계측기 종료 사유)이다. 무활동 유예로 끝낸
  회차의 `runner_limit`은 `runner_engine_idle`이고 값은 늘 엔진 쪽(`engine_completed`·`product_limit:budget_limited`)이다. 러너 상한 시각은
  시간 예산이면 그 문턱 값, 요청 예산이면 러너가 알아챈 시각(폴링이라 최대 한 간격 늦음)이고, 엔진 끝 시각은 상태 읽기가 끝 상태를
  돌려준 시각이다. 같거나 엔진이 이르면 `engine_completed`다. 판정을 다시 계산할 수 있게 두 시각(`runner_limit_elapsed_seconds`·
  `engine_terminal_elapsed_seconds`, 작업 생성 요청 직전부터 잰 초)과 러너 쪽 상한(`runner_limit`)을 같이 남긴다. 창이 끝난 뒤 거둔
  읽기의 끝 상태는 러너 상한보다 늦어 엔진이 먼저가 아니다. 제품(O)의 `product_limit:<사유>`는 제품 구동(순서 4-7)에서 더한다.
- **제공자 장애 멈춤 기록(`fault-stall.json`, 결정안 §7):** LLM 판정이 통과한 뒤 게이트웨이 요청 행(`llm/llm-requests.jsonl`)과
  계측기 행으로 §7 무효 조건의 재료를 만든다. 제공자 장애는 위쪽 스트림 끊김(`upstream_error` + `upstream_cut_*`)과 위쪽 429 응답이다
  (호출자 쪽 끊김·게이트웨이 자신의 거부는 아님). 장애마다 그 뒤 첫 활동(모델 호출 시도·두 입구의 대상 요청 시도의 시작)까지의 무활동
  길이, 창 끝까지 활동이 없었는지(`censored`), 그 사이 엔진 끝 신호가 있었는지, 장애 시점에 진행 중이던 요청 수를 남긴다. T 후보 근거로
  창 전체의 최장 무활동(진행 중인 요청이 하나도 없는 구간)도 남긴다. 엔진이 창 안에서 먼저 끝난 회차는 엔진 끝 상태까지만 잰 활동 중 최장 무활동(`max_idle_gap_before_end_seconds`)도 남긴다(끝 신호가 없거나 창 밖이면 창 끝까지, 2026-11-19 사용자 결정 5). T는 이 값으로 정한다(끝 뒤 남은 시간은 §7 무효 조건(3)에서 빠진다). 창은 작업 생성 요청 직전부터 러너가 계측기를 봉인한 시각까지다.
  **판정**: 설정 `run.fault_stall_seconds`(T, 1~3600초, 없으면 판정 안 함)가 있으면 그 T로 판정한다(`judged: true`, `stall_seconds`,
  장애별 `stall`, `stalls`). 조건 (2)의 무활동은 장애 뒤 **다음 활동 사건까지의 길이**(`idle_seconds`)다. 활동 사건은 요청 시작과 응답 완료(오류 응답 포함)이고, 모델 호출 행·대상 요청 행 모두, B·O 같게 센다(2026-11-19 사용자 결정). 진행 중인 요청은 활동이 아니다: 제공자 쪽에서 매달린 호출을 잡는 것이 T의 목적이다. 스트리밍 조각도 세지 않는다(세면 비스트리밍인 O와 같은 길이의 호출이 B에서만 공백이 아니게 된다). 이 길이가 T초 이상이고 그 전에 엔진 끝 신호가 없으면 멈춤이고, 회차는 `provider_fault_stall`로 무효다(같이 난 모델 호출 검증 사유는 뒤에 남는다 — 세션 집계가 첫 사유로 센다). 창이 T초 전에 끝나 무활동이 T에 못 미치면 멈춤이라고 하지 않는다. T를 고를 근거로 같은 정의의 활동 중 최장 공백(`max_event_gap_before_end_seconds`)을 남긴다. 시작만 본 무활동(`silence_seconds`)은 기록에 그대로 남긴다.
  T가 없으면 행이나 시각을 읽지 못해도 회차를 무효로 하지 않고 고정 코드(`error`)만 남긴다. T가 있는데 재료를 못 만들었으면 멈춤이
  없다고 할 수 없어 `fault_stall_unjudgeable`로 무효다. 형식은 `abc2lab-eval-runner-fault-stall-v3`다(v3, 2026-11-22: 장애 사건마다 그 순간 진행 중이던 모델 호출 수 `inflight_model_calls_at_fault`를 더함 — `inflight_at_fault`는 모델 호출과 대상 요청을 합한 수다. B는 스트리밍 조각을 활동으로 세지 않아, 긴 스트리밍 호출이 진행 중일 때 생긴 멈춤을 공정성 기록에서 가르려는 칸이다). 마지막 활동 사건부터 끝(엔진 끝 상태, 없으면 창 끝)까지의 공백(나노초 정수 `trailing_event_gap_nanoseconds`, 기록용 초 `trailing_event_gap_seconds`)과 끝 시각까지 이어진 요청 수(`inflight_at_end`, 끝 전에 시작해 끝 시각이나 그 뒤에 완료)도 남긴다. 판정에는 쓰지 않고, 시간 상한 마감을 '빈 계획 대기 중'(나노초 끝 공백 ≥ T이고 진행 중 요청 0)과 '작업 중'으로 나눠 보고하는 근거다(고정 초안 §21).
  세션 집계(`session_runs.py`)는 완료 회차가 모두 같은 T로 판정됐는지 보고(아니면 멈춤, 스모크는 `--allow-unjudged-fault-stall`),
  쪽별 `provider_fault_stall` 무효 수를 0이어도 출력하고, 한쪽이라도 2회 이상이면(고정 초안 §3) 측정 전체 실패로 종료 코드 3,
  manifest를 쓰지 않는다. 게이트웨이 행 읽기는 결정·끝 모양 어휘를
  `cmd/eval-meter/gateway.go`와 같게 고정한다(시험이 원문과 대조). 제품(O) 분리 배치 회차도 같은 함수로 `fault-stall.json`을 남긴다(`runner_split_run.product_fault_stall`): 게이트웨이 행 자리에 제품 호출 기록 줄(같은 칸·어휘)을 넣고, 창은 시작 요청 직전부터 계측기 봉인 직후까지다. 호출 기록 완전성(장부 예약 수 = 기록 줄 수)을 확인한 뒤에 만들고, 결과 요약 `fault_stall`에도 싣는다.
- **일찍 끝난 회차 분류(`early-end.json`, 고정 초안 §26, 2026-11-21 사용자):** 멈춤 기록 다음에 같은 호출 행(B 게이트웨이 행, O 제품
  호출 기록 줄)과 엔진 첫 끝 상태로 분류한다(`runner_early_end.early_end_record`). 모델 호출 401~404, O 승인 예산 상한에 닿은
  `budget_limited`(장부 합계 `runner_product_llm.ledger_totals` vs manifest `max_requests`·`max_cost_usd_micros`, 비용은 남은 몫이 가장 작은 예약(입력 4097·출력 1토큰, manifest 단가)보다 작을 때만), O 시작 거부(제품
  고정 사유)는 `config_end`로 무효이고 세션이 시도로 세지 않고 멈춘다. 러너 상한 전에 O `failed`·`unknown`(B `failed`)으로 끝났고 끝 직전
  마지막 호출이 제공자 오류(끊김·429·5xx)이며 그 뒤 성공 호출이 없으면 `provider_fault_end`로 무효이고 멈춤 상한에 같이 센다. 그 밖에
  일찍 끝난 회차는 유효로 두고 분류만 남는다. 호출 행을 읽지 못하거나 없으면(합성 O) 근거 없음으로 두고 무효로 빼지 않는다. 사유
  순서는 설정 오류 → 멈춤 → 제공자 장애 끝 → 모델 호출 검증이다. 결과 요약 `early_end`에도 싣는다. O 제품이 준비 단계에서 끝나
  실행 기록이 없으면(`diagnosis-run` `NOT_RUN`·404) 실행 기록을 요구하기 전에 분류하고, 설정 오류·제공자 장애 끝이 아니면
  `product_not_executed`(분류 `ended_not_executed`)로 끝낸다(무효 시도, 사용자 확정 2026-11-21).

`findings.json`은 baseline·개선 후 모두 **같은 추출 경로**(`baseline_pages_collector` → `baseline_extract`, 수정 없음)를 거친
투영만 담는다. API 응답 원본·제목·설명·자산 이름은 저장하지 않는다(절대규칙 2). 추출기는 원본 FindingDTO 형식(upstream
`fdfb7a17`)으로 고정돼 있고, 개선 후 엔진의 `FindingDTO`·`FindingAssetDTO`·`db.FindingTrafficBinding`·findings 목록 핸들러·
로그인 핸들러가 그 커밋과 같다는 것을 `tests/test_runner_engine.py`가 고정한다(바뀌면 테스트가 깨지고 추출 경로를 다시 본다).
그래서 기록에는 두 출처를 따로 둔다.

| 필드 | 뜻 |
|---|---|
| `dto_format_source` | 추출기가 기준으로 삼은 응답 형식의 커밋(항상 upstream `fdfb7a17`). 빌드 출처가 아니다 |
| `build_source` | 실제로 돈 엔진의 소스 커밋(baseline `e53a79f…`, 개선 후는 빌드한 dev 커밋) |
| `vocabulary_sha256` | 분류 매핑 파일 원본의 SHA-256 |
| `declared_total`, `page_size`, `page_count`, `findings[]` | 엔진이 선언한 전체 건수, 수집 단위, 추출된 행(ID·분류·심각도·상태·자산 ID/type·트래픽 결속 ID/역할·`unmapped_fields`) |

개선 후 엔진의 구조화 판정 레코드는 물리 형식(#18)이 정해진 뒤 별도 신호로 붙인다. 지금은 findings만 같은 경로로 비교한다.

## 사유 코드

- 공식 측정 E③ 비활성(평가 프로토콜 §5.1, 2026-11-18 합의): `e3_share_unconfirmed`(그 회차 E② 정책 보기에 확정 몫 칸이 없음), `e3_share_not_zero`(확정 몫이 0이 아님). O 회차 흐름은 회차 목적이 `official`일 때 제품을 멈춘 뒤 그 회차가 쓴 정책 보기(`checked_control_policy`)의 상한으로 이 검사를 부른다(`runner_split_run.check_official_e3_share`). 다른 목적(수용시험)은 몫 칸을 요구하지 않는다. 확정 몫 칸이 없는 제품 빌드(#716 전)로는 공식 O 회차가 늘 `e3_share_unconfirmed`다. E③ 상태가 `disabled`/`declaration_missing`이 아니거나 상태를 못 받은 회차는 채점기가 공식 표를 거부한다(`run_record.OFFICIAL_E3_DISABLED_REASON`). 분리 배치 B는 공통 목표 SHA를 실행 조건(`goal_common_sha256`)에 서명한다.

| 코드 | 뜻 |
|---|---|
| `project_exists` | 같은 project가 이미 있음(이 러너 것이 아니므로 up·down 모두 안 함) |
| `project_check_failed`, `image_id_failed`, `image_id_mismatch` | 사전 조회 실패, 로컬 이미지가 설정의 내용 ID와 다름 |
| `tool_tree_failed` | 엔진 이미지의 도구 트리를 읽지 못함(망 없는 1회용 컨테이너, `tool_tree.py`) |
| `host_spec_failed` | 머신 사양을 읽지 못함(`/proc/cpuinfo` 모델명·`/proc/meminfo` MemTotal·`/etc/machine-id`·Docker 버전) |
| `engine_rules_failed` | 원본 ② 규칙 등록 실패(엔진이 거부, 정규식 오류 포함), 내장 규칙과 이름 충돌, 규칙을 타는 도구를 줄이는 파일, 또는 다시 읽은 규칙·도구 목록이 파일과 다름. 작업을 시작하지 않는다 |
| `engine_settings_apply_failed` | 작업 전에 압축 기준 창·worker 수를 엔진에 넣지 못함(프로파일 저장·활성화·설정 쓰기 중 하나가 실패) |
| `engine_settings_failed`, `engine_settings_mismatch` | 실행 설정 조회 실패·형식 오류, 또는 workers·`context_window_k`가 선언과 다름·`reasoning_effort`·`thinking_type`이 비어 있지 않음·엔진 LLM 모델이 선언과 다름·프록시가 설정됨(송신 경로가 계측기·게이트웨이 밖으로 바뀜) |
| `subnet_overlap`, `subnet_check_failed` | up 바로 전, 회차가 만들 고정 대역(`docker compose config`의 망 `ipam.config[].subnet`, 외부 망 제외)이 호스트의 기존 Docker 망 대역과 겹침(같은 주소 계열만 비교), 또는 렌더·망 조회 실패. 아무것도 띄우지 않았으므로 down도 안 한다. 고정 대역이 없는 망은 Docker가 빈 대역을 골라 검사할 것이 없다. 렌더 결과에는 보간된 비밀이 있어 subnet 말고는 꺼내지 않는다(탐색 분리 배치 결정안 §2) |
| `compose_up_failed` | 기동 실패(down은 실행) |
| `{pre,post}_facts_failed`, `{pre,post}_host_listeners_failed` | docker·/proc 사실 수집 실패 |
| `{pre,post}_topology_violation` | 망·구성원·publish·이미지가 기대 구성과 다름 |
| `{pre,post}_host_condition_violation` | 다른 컨테이너의 loopback 밖 publish, 회차 컨테이너의 망·PID namespace를 같이 쓰는 실행 중 컨테이너(`HostConfig.NetworkMode`·`PidMode` = `container:<회차 컨테이너 ID>`, 전체 ID가 아닌 표기도 위반), 또는 허용 안 된 호스트 리슨 |
| `{pre,post}_probe_invalid`, `{pre,post}_probe_violation` | 부정 연결 점검 무효(양성 대조 실패·실패 종류 불일치·probe 오류) 또는 위반(막혀야 할 경로가 열림). 경로별 내용은 `*-probe.json` |
| `target_not_ready`, `pre_scoreboard_failed`, `post_scoreboard_failed` | 관측 front로 대상 준비·스코어보드 수집 실패 |
| `pre_requests_nonzero` | 작업 시작 전에 엔진이 대상으로 보냄 |
| `task_start_failed` | 작업 생성 실패: 로그인 실패, 생성 요청 연결 실패·시한 초과, 201 아닌 응답, 응답에 작업 ID 없음. 다시 보내지 않는다(시한 초과면 작업이 생겼을 수 있음) |
| `seal_failed`, `drain_timeout`, `drain_incomplete` | 계측기 seal 실패, drain 시한 초과, 진행 중 요청·기록 불완전·강제 취소 수 없음/형식 오류 (응답 본문 도중 끊긴 요청은 계측기가 `forced_cancelled`·`aborted` 행으로 정산하므로 진행 중으로 남지 않음) |
| `boundary_changed` | pre와 post 사이 데몬·망·주소·컨테이너 교체·재시작 |
| `window_boundary_changed` | 구간(pre 관측 직전~post 관측 완료) 데몬 이벤트에 회차 망·회차 컨테이너의 연결·해제·생명주기·exec가 있거나, 회차 밖 컨테이너가 생성·시작·재시작됨(`--network container:<회차 컨테이너>`는 망 이벤트 없이 namespace를 같이 쓰므로). 허용: 계측기·게이트웨이 seal(USR1), engine-db의 선언된 healthcheck, 러너가 띄운 probe(cidfile ID)의 정상 생명주기(create·attach·start·die·destroy 각 1회 — 같은 ID의 exec·kill·restart·재시작은 위반) |
| `window_events_incomplete` | 구간 이벤트가 데몬 버퍼 크기(256)에 닿아 앞쪽이 버려졌을 수 있음 |
| `window_host_condition_violation`, `window_host_watch_gap` | 구간 중 허용되지 않은 호스트 리슨·읽기 실패, 표본 사이 공백이 허용값 초과 |
| `window_watch_failed`, `window_events_failed` | 감시 시작·종료 실패, `docker events` 실패·형식 밖 출력 |
| `observer_count_mismatch` | 관측 로그 건수가 러너 호출 수와 다르거나 거부 기록이 있음 |
| `meter_path_log_enabled`, `meter_path_log_missing` | 분리 배치가 아닌 `purpose=official` 회차에 계측기 경로 기록(`engine-paths.jsonl`)이 있음, 수용시험·분리 배치 회차에 없음. 계측기 기록 마감 대조(`check_meter_logs`)는 B와 분리 배치 O가 같은 함수·같은 사유다 |
| `meter_path_log_invalid` | `purpose=browser_acceptance` 회차의 경로 기록이 러너 소유 일반 파일이 아니거나, 꼬리가 잘렸거나, 가림 판(`abc2lab.path-mask/v2`)이 다르거나, 행마다 키 참조가 다르거나, 전달한 요청과 `seq`·메서드가 한 행씩 맞지 않음(봉인 뒤 거부한 요청은 행이 없어야 함) |
| `e3_status_not_collected`, `e3_confirmation_invalid`, `e3_official_not_disabled`, `e3_confirmation_copy_failed` | (분리 배치 O 공식 회차) 제품을 멈춘 뒤 회차 디렉터리 `runs/<초안 ID>/e3-confirmation.json`(제품 E③ 확정 단계 결과, e3contract.RunResult)을 복사하지 못함(`e3_status_not_collected`. 복사 도우미 정리 실패는 목적과 상관없이 `e3_confirmation_copy_failed`·`docker_helper_cleanup_failed`로 무효), 모양이 다르거나 상태와 확정 요청 수가 모순(비활성인데 `requests`가 0이 아님, `e3_confirmation_invalid`), 또는 상태·사유가 `disabled`/`declaration_missing`(선언 없음으로 비활성)이 아님(예: 연결 전 빌드의 `not_wired`). 상태·사유·확정 요청 수·원본 SHA는 `product-files.json`의 `e3_confirmation`과 결과 요약에 남는다(판정·쓰기 칸은 옮기지 않음). 공식이 아닌 목적은 기록만 하고 무효로 하지 않는다. 제품은 연결이 빠진 빌드(E② Transport·Journal 등)에서 선언 여부보다 먼저 `not_wired`를 쓴다(E③ 계약 §7 비활성 사유 구분, 2026-11-18 합의: 연결 빠진 이미지가 `declaration_missing`으로 공식 통과하지 않게). 그래서 **Transport·Journal 연결 전 제품 빌드로는 공식 O 회차를 돌릴 수 없다**(늘 `e3_official_not_disabled`). 공식 측정은 E③ 통합 뒤다 |
| `e3_traces_copy_failed` | (분리 배치 O 회차) E③ 흔적 근거(`e3-traces.json`)를 만들려고 제품 볼륨 세 곳을 복사하다 도우미 정리에 실패함(`docker_helper_cleanup_failed`와 같이, 무효). 그 밖의 복사 실패는 회차를 무효로 하지 않고 근거 파일에 `{"error": "e3_traces_copy_failed"}`로만 남는다. 파일이 없으면 그 칸만 `not_found`다. 근거 파일: E② Host v3 저널(`e2-host-state` 볼륨 `journals/<sha256("run-"+초안 ID)>/events.jsonl`, uid 1002), 제품 쪽 E② 저널 사본(`runs/<초안 ID>/state/events.jsonl`, E③ 프로그램 사건은 싣지 않음), 대상 선언 보호 저널(`state/targets/declarations/preparation.journal`, 압축돼 `preparation.current/`로 옮겨졌으면 읽지 않음)에서 사건(e2proxy 저널 줄의 `Record.Event`, 머리 줄은 `headers`로 따로)을 (단계, 프로그램, 결정, 사유) 어휘 값별로 세고, E③ 사건 수(프로그램 `e3_identity`·`e3_create`·`e3_read` 또는 E③ 감사 칸 `e3_binding`), 선언 저널 동작별 수(저널 형식 줄은 `format_records`로 따로)와 `confirmation_writes` 객체 수, 결과 파일의 쓰기 기록 수, Host 실행 디렉터리(Host 저널 옆)의 봉인 포함 확인 `seal-checks.json`(판·마감 단계·확인 수·포함 수)과 내준 근거 체크포인트 기록 `evidence-checkpoints.json`(판·체크포인트 수, 없으면 요약 0)을 남긴다(둘 다 제품 #810 뒤 생김, 선언 없는 회차는 근거 기록 없음·봉인 확인 0개여야 함). E② 저널마다 사건 시각 순서(`timeline`: 사건별 단계·프로그램·결정·사유 어휘, 모델 쪽(의도 번호 > 0, Worker) 여부, 시각만, 4096줄 상한·넘으면 `timeline_truncated`)도 남긴다(목표 달성 뒤 감독자가 닫을 때까지 모델 쪽 송신이 없는지 보는 근거). 원문·호출 참조·경로는 남기지 않고 어휘 모양이 아닌 값은 `invalid`로 센다. 저널 검증(HMAC·체크포인트)은 하지 않는다(통제 대조는 v3 수집 몫). 요약은 결과 `e3_traces`(`e3_events`·`product_journal_e3_events`·`declaration_writes`·`result_writes`·`host_seal_phase`·`host_seal_checks`·`host_evidence_checkpoints`). 개발 계획 Go 조건 2(선언 없는 회차 E③ 흔적 0건) 근거로 쓴다. 표식 등록은 저널에 따로 남지 않아(E② 실행의 메모리 값) 이 파일로 세지 않는다 |
| `meter_request_cap_mismatch`, `meter_request_cap_exceeded` | 계측기 drain 표식의 요청 하드 상한(`request_cap`)이 이 회차 공통 요청 예산과 다르거나(설정이 계측기에 닿지 않음) 상한 수(`rejected_over_cap`)가 없음, 전달 수가 예산을 넘음. B·분리 배치 O 같은 검사(`runner.check_request_cap`)이고, 러너가 compose 변수 `EVAL_METER_REQUEST_CAP`(= 선언 `request_budget`, 1~1,000,000)으로 계측기 `-request-cap`에 넘긴다. 상한으로 거부한 수는 계측기 요약 `rejected_over_cap`에 남는다 |
| `meter_path_key_mismatch` | `purpose=browser_acceptance` 회차의 경로 행 키 참조가 러너가 실행 전에 확인한 키의 참조와 다름 |
| `meter_read_failed`, `meter_log_inconsistent` | 계측기 기록 읽기 실패, 순번 누락·건수 불일치(강제 취소 행 수 ≠ drain 마커의 취소 수 포함) |
| `e2_stub_log_invalid` | `e2.ours_mode=stub` 회차에서 가짜 E² 결정 행(`e2/e2-stub-decisions.jsonl`)이 없거나 형식이 틀림: 개행으로 끝나지 않은 꼬리(잘린 행), fixture 표식 아님, 모르는 결정, 메서드·결정 시각 형식, 발급된 결정에 시도 ID 없음·발급 실패에 ID 있음, ID 중복, 순번 공백 |
| `llm_seal_failed`, `llm_read_failed`, `llm_unverified`, `llm_marker_invalid` | 게이트웨이 seal 실패, 기록 읽기 실패, 검증 마커 시한 초과, 마커 형식 오류 |
| `llm_generation_unverified`, `llm_generation_unverifiable` | 조회 안 된 generation(B는 기록상 끊긴 호출(`cancelled_by_seal`)의 미확인을 빼고 `unverified_interrupted`로 따로 셈. O는 제품이 끊긴 호출에 ID를 남기지 않아 예외 보류, 평가 프로토콜 §6), generation ID 없는 응답. O는 제품 호출 기록을 `product-calls.jsonl`로, 끊긴 호출 분류에 실제로 쓴 정지 요청 시각을 `llm-summary.json`·결과 요약의 `stop_requested_at`으로 남긴다(회차 뒤 같은 분류를 다시 계산해 예외 적용을 재검증한다. B는 게이트웨이 표식의 봉인 시각이다). 표식의 generation마다 조회 흔적 `lookup`(시도 수 `tries`, 마지막 응답 종류 `last`: `verified`·`http_<코드>`·`network_error`·`request_error`·`body_invalid`·`no_provider`, 시한 안에 한 번도 못 물은 것은 시도 0·`deadline`)이 있고, 요약 `llm-summary.json`에 미확인 건의 마지막 응답 종류별 수(`unverified_last`)와 최대 시도 수(`lookup_tries_max`)가 남는다. 값·본문은 남기지 않는다. 401·403은 다시 묻지 않는다 |
| `llm_provider_mismatch`, `llm_fallback_attempted`, `llm_fallback_unverifiable` | 응답 제공자가 기대와 다름, 응답한 제공자와 다른 제공자로 간 시도, 시도 목록이 비어 확인 불가 |
| `llm_pin_mismatch`, `llm_backend_mismatch`, `llm_log_incomplete` | 게이트웨이가 다른 slug를 고정, 백엔드 구성이 섞임, 게이트웨이 기록 불완전 |
| `llm_serving_mismatch`, `llm_serving_unverified`, `llm_serving_read_failed` | local 서버의 버전·모델 digest·컨텍스트가 선언과 다름, 모델이 적재돼 있지 않아 확인 못 함, 서버 응답을 읽지 못함. 회차 전(compose 전)·작업 생성 직전·게이트웨이 검증 뒤에 본다. 근거는 `llm-serving.json` |
| `llm_serving_not_on_gpu` | 선언 `placement`가 `gpu`인데 서버의 `size_vram`이 `size`보다 작거나, `nvidia-smi`로 읽은 GPU 사용량이 `size_vram`의 90%보다 적음. 모델 일부가 CPU로 밀리면 오류 없이 느려져 시간 예산 안의 진척이 줄어든다. 서버 값이 불명(`size_vram`>`size`, `nvidia-smi` 없음·실패·`[N/A]`)이면 `llm_serving_unverified`다. 회차 전·작업 생성 직전·게이트웨이 검증 뒤에 본다 |
| `llm_serving_keep_alive_short` | 적재 직후 서버가 알려 준 남은 유지 시간이 요청한 값보다 짧음. 서버가 유지 기간을 따르지 않으면 회차 중 모델이 내려갈 수 있어 compose 전과 작업 생성 전에 멈춘다 |
| `llm_sampling_rejected` | 게이트웨이가 엔진 본문의 샘플링 칸(temperature·top_p·top_k·min_p·presence_penalty·repetition_penalty·seed·frequency_penalty) 때문에 요청을 한 건 이상 거부함(검증 마커 `rejected_sampling` > 0). O 제품 transport는 같은 칸을 덮어쓰므로 B·O 같은 조건 전제가 깨진 회차다(평가 프로토콜 §6). 행 결정은 `rejected_sampling`, provider 칸 거부는 `rejected_provider`로 따로 세고 기록만 한다 |
| `llm_sampling_mismatch` | 게이트웨이가 실제로 넣은 샘플링(검증 마커 `sampling`)이 선언(temperature·sampling·seed)과 다름. 키 묶음이 백엔드와 달라도 이 사유 |
| `tmpfs_measure_failed` | 구간 종료 뒤 tmpfs 사용량을 읽지 못함(측정 컨테이너 실행·권한 실패, 커널 파일시스템 종류가 tmpfs 아님, 형식·크기가 맞지 않는 보고, 두 볼륨이 정확히 오지 않음). 결과의 `tmpfs_usage`는 `not_measured` |
| `pre_tmpfs_measure_failed` | 구간 전 tmpfs 마운트 ID·사용량을 읽지 못함(사유는 `tmpfs_measure_failed`와 같음). 구간을 시작하지 않는다 |
| `tmpfs_mount_changed` | 구간 뒤 잰 tmpfs의 마운트 ID가 구간 전과 다름 — 같은 경로라도 다른 마운트(부모 경로 변경·symlink·다시 마운트)를 잰 것이라 회차 저장소의 근거가 아니다. 양쪽 값은 결과에 남는다 |
| `tmpfs_full` | 엔진 DB·엔진 데이터 tmpfs 중 하나라도 남은 공간이 `tmpfs.min_free` 미만(상한 도달 — 엔진이 쓰기 실패를 삼켰을 수 있음). 잰 값과 적용 하한(`min_free_bytes`)은 결과에 남는다 |
| `state_copy_failed` | 상태 차이 사본을 뜨지 못함(대상 pause·unpause 실패, 데몬 복사 실패·크기 초과·형식 밖 tar, SQLite 아님, WAL DB인데 `-wal` 없음, 쓰기 도중 저널, 저널 확인 불가(데몬이 '파일 없음'이 아닌 이유로 복사 실패), 0600 저장·삭제 실패, 사본을 SQLite로 읽지 못함, pre에 런타임 디렉터리 없음 또는 디렉터리가 아님, 형식 밖 tar, pre에 못 읽는 메모리 경로, 계약 밖 읽기 결과). post에서 없어지거나 다른 종류로 바뀐 런타임 디렉터리와 못 읽는 메모리 경로는 실패가 아니라 상태 변화로 센다. pre면 구간을 시작하지 않는다 |
| `state_diff_failed` | pre·post 사본 지문을 비교하지 못함(설정한 신호 테이블이 사본에 없음 등) |
| `intercept_summary_failed` | 엔진 승인 기록을 세지 못함(연결·HTTP 상태·응답 형식·쪽 한도 초과). 일부만 세고 넘어가지 않는다 |
| `findings_failed` | findings 단계 실패: 로그인 실패(연결·HTTP 상태·응답 형식), 페이지 수집 실패(한도 초과·건수 불일치·형식 밖 응답), 추출 실패 |
| `evidence_write_failed` | 근거 파일 쓰기 실패 |
| `down_failed`, `sign_failed` | 정리·서명 실패(측정은 끝났어도 무효) |
| `record_build_failed`, `record_sign_failed` | v1.1 러너 기록(격리 근거·해결 이벤트·증명) 작성 실패, 그 서명·저장 실패 |
| `interrupted`, `unexpected_error` | Ctrl-C(정리·기록 후 다시 올림), 예상 밖 예외(정리·기록함) |

`llm-summary.json`에는 LLM 사유가 전부 남고, 결과 기록의 사유는 그중 정렬 첫째다.

**제품 E②(`e2.ours_mode=product`):** 개선 후 회차에서만 쓴다. 실제 엔진·`eval-e2`·계측기·Juice Shop을 묶어 스크립트 모델 회차까지 확인했고, 실제 모델 회차는 아직 안 돌렸다. 망 배치와 부정 연결 기대값은 가짜 E²와 같다. 설정은 `image`, `policy_path`, `policy_fingerprint_sha256`(시범 전 동결값), `declared_differences_path`·`declared_differences_sha256`(선언 차이 목록과 동결 해시, 다르면 `e2_declared_differences_mismatch`, 목록의 `status`도 회차 기록에 남는다), `header_mode`, 시한 다섯(`ready_timeout_seconds`, `close_ack_timeout_seconds`, `close_timeout_seconds`, `poll_seconds`, `exchange_timeout_seconds`), `verify_tool`, `verify_timeout_seconds`, 정책 생산 설정 `policy_tool`, `policy_tool_timeout_seconds`, `helper`, `helper_sha256`, `subnet`, `meter_ip`, `window_seconds`, `approval_valid_until`, `prompt_wait_seconds`다. `policy_path`는 회차 값이 없는 의미 템플릿이고([형식](../contracts/e2-evaluation-policy-format.md) A0), 시범 전 동결값은 `python3 -I scripts/eval/e2_policy_fingerprint.py --template <템플릿> --tool <e2-eval-policy> --timeout-seconds <초>`로 계산한다. `e2_net`은 `subnet` 고정 대역이고, up 뒤 계측기의 실제 주소가 `meter_ip`와 다르면 `pre_topology_violation`으로 무효다.

1. compose 전에 `e2-config/`에 설정, 템플릿에 회차 값을 채운 정책, 회차 CA, 회차 감사 키를 쓴다. `e2-state/`는 E²가 쓰는 회차 상태 디렉터리다([제어 채널 제안](../contracts/e2-runner-control-proposal.md) §4). 키 값은 파일로만 있다. 정책의 `approval_mode`가 `preauthorized_scope`가 아니면 시작을 거부한다. 평가 회차에는 요청마다 승인할 사람이 없다. 정책 사본에는 러너가 이번 회차 감사 키의 `audit_key` pin을 채운다. 원본 정책에 `audit_key`가 있으면 거부한다. 감사 키는 정책 지문에 안 들어가서 동결 지문은 회차마다 같다.
2. 작업 생성 전에 `ready`를 받아 설정 digest·전달 모드·평가 선언·동결 지문과 대조하고, `bind`로 대상 컨테이너 ID를 넘긴다. 어긋나면 POST 없이 `e2_<코드>`로 무효다.
3. 예산이 끝나면 계측기보다 E²를 먼저 닫는다. 시한을 넘기면 기다리지 않고 계측기 seal로 넘어간다.
4. 봉인 뒤 `e2/<execution_ref>`를 러너 전용 사본 `e2-copy/`로 옮겨 `e2-audit-verify`와 투영 행을 대조한다.
5. 다 맞거나 실행이 없었으면 시도별 대조를 돌린다. `no_execution`은 결정 0건으로 대조해서 계측기에 도달한 요청이 있으면 `unrecorded_observed`로 나온다.
6. E② 결과가 `complete`인 회차는 다음에 [프롬프트 투영](../contracts/e2-prompt-projection.md)을 받아 대조한다. 값은 러너가 정책에 넣은 `run_ref`로 묻는다. 수집 중(`collecting`)이나 일시 장애는 `prompt_wait_seconds` 안에서 다시 묻고, 시한을 넘기거나 `complete`가 아니면 그 회차는 `e2_prompt_unverified`로 무효다. 제품 실패·실행 없음 회차는 비교할 실행이 없어 `not_applicable`로 남기고 유효성은 아래 표대로다.

| 결과 | 회차 | 통제 주장 |
|---|---|---|
| `complete` | 유효 | 시도별 대조 조건대로 |
| `no_execution` | 유효 | 허용 양성 근거가 없어 보류 |
| `product_failure`(close 실패·`incomplete`·형식·무결성 실패) | 유효 | 대조 근거가 없어 보류 |
| 회차 값·정책 지문 불일치, 파일 소유자, 읽기·검증 명령 장애(`e2_<코드>`) | 무효 | 없음 |
| E② 결과는 완료인데 프롬프트 동일성 미확인(`e2_prompt_unverified`) | 무효 | 없음 |

기록: `e2-service-ready.json`, `e2-collection.json`, `e2-prompt-projection.json`(미확인이면 `e2-prompt-status.json`), 격리 근거의 `e2_product`·`e2_prompt` 행. 키 파일은 회차 디렉터리 `e2-config/`에 남는다. 사후 `e2-audit-verify`에 필요하고, 회차 디렉터리는 커밋 금지 결과물이다.

**E② 시도 경로 투영(수용시험 회차만):** `purpose`가 `browser_acceptance`이면 러너가 계측기와 같은 경로 가림 키(`path_key_file`)를 eval-e2 설정 v2(`abc2lab.e2-runner-config/v2`, `path.key`)로 넘긴다. eval-e2는 시도마다 `e2-attempt-paths.jsonl`(`abc2lab.e2-attempt-paths/v1`: 순번·결정·사유·메서드·가린 경로 패턴(URL이 없던 거부는 `null`)·가린 조각 수·가림 판·키 참조)을 쓰고, 마감까지 성공했을 때만 완료 표식 `e2-attempt-paths.done.json`(행 수·기록 바이트 SHA-256)을 쓴다. 러너는 두 파일이 있으면 E② 수집 사본으로 옮겨 `e2-collection.json`의 digest에 남긴다. 공식 회차(`official`, 설정 v1)에 둘 중 하나라도 있으면 `e2_attempt_paths_unexpected`로 무효다. 집계(`scripts/eval/e2_attempt_paths.py`)는 표식이 없거나, 행 수·digest가 기록과 다르거나, 행마다 정확한 8칸·같은 가림 판·같은 키 참조가 아니거나, 행의 순번·결정이 감사 내보내기(`attempts.jsonl`)의 결과 시도와 1:1이 아니면 그 회차의 투영을 쓰지 않는다(수용시험 §6 진단 요청 반복 차단 수는 `NOT_RUN`). 경로 원문은 어디에도 남지 않는다.

**판정 export:** 설정 `verdict_export`가 null이면 수집하지 않는다. 값이 있으면 개선 후 회차에서만 findings 다음, down 전에 [판정 페이지 제안](../contracts/verdict-record-page-api-proposal.md)대로 동결하고 전체 페이지를 모은다. 동결이 준비 전(409)이면 `ready_poll_seconds` 간격으로 다시 묻고, `ready_wait_seconds`를 넘기면 새 요청을 보내지 않는다.

- 다 맞으면 `verdict-pages.json`(원본 페이지)·`verdict-records.json`(채점 입력)·`verdict-export.json`을 남기고 `artifacts.records`가 채점 입력을 가리킨다.
- **미수집**(시한 초과·미구현·인증·서버 장애·이전 export 유실·연결 불가·수집 상한): `status=not_collected`와 고정 사유를 남기고 `artifacts.records`는 null이다. 회차는 유효하고 판정 칸은 판단불가로 나온다.
- **무결성 실패·입력 오류**(HTTP 422·400, 페이지 digest·회차·모델·리셋·구조 검증 실패): `status=integrity_failed`다. 회차와 해결 귀속 근거는 서명해서 보존하되, 격리 근거의 `verdict_collection` 행을 보고 채점기가 표를 멈춘다. 판단불가로 숨기지 않는다.
- 부분 수집은 어느 쪽이든 채점 입력으로 쓰지 않는다.
- baseline은 설정과 무관하게 수집하지 않는다. baseline 레코드는 A5 어댑터 몫이다.

`llm-summary.json`에는 거부 가운데 샘플링 칸·provider 칸 때문인 수(`rejected_sampling`·`rejected_provider`, `rejected_requests`에 들어 있음)와 위쪽이 429로 답한 호출 수(`rate_limited`, 게이트웨이 자신의 거부는 빼고 전달·끊김 행 모두, 탐색 분리 배치 결정안 §3.2)가 남는다. 429는 기록만 한다.

`llm-summary.json`의 `upstream_error_ends`는 `upstream_errors`를 멈춘 모양으로 나눈 값이고 합이 같다. 네 값은 `client_closed_after_done`·`client_closed_before_done`·`upstream_cut_after_done`·`upstream_cut_before_done`이다. `client_closed`는 게이트웨이가 호출자 쪽 취소나 쓰기 실패를 관찰했다는 뜻이고, `after_done`은 위쪽 응답에서 SSE 종료 줄을 읽었다는 뜻이다. 종료 줄은 호출자에게 쓰기 전에 표시한다. 그래서 엔진이 그 줄을 받았는지, 어느 쪽이 먼저 닫았는지, 끊긴 책임은 말하지 않는다. 회차 유효성에는 쓰지 않고 기록만 한다. 본문은 메모리에서 종료 줄을 찾는 데만 쓰고 저장하지 않는다.

**회차 전 계측기 이미지 점검(유료 호출 전):** 러너는 이미지 ID 대조 직후, compose를 띄우기 전에 계측기 이미지(게이트웨이·사후 검증도 같은 이미지)를 망·쓰기·권한 없이 `-role describe`로 한 번 실행한다. 출력의 표식 칸(Go 구조체의 json 칸, `llm-verified`·`engine-row`)이 이 러너가 엄격하게 검사하는 칸(`runner_llm._MARKER_FIELDS`, `runner_evidence._ENGINE_ROW`+`ingress`)과 같아야 한다. 또 빌드 커밋(`-ldflags -X main.sourceRevision`, VCS 정보가 있으면 그것도)이 사전검사 선언의 개선 후 `source_sha`와 같고 깨끗한 트리여야 한다. 빌드 칸은 계약 모양이어야 한다(개정은 40 hex 또는 빈 값, 수정 플래그는 VCS 개정이 있을 때만 불리언이고 없으면 null). 다르면 모델을 한 번도 부르지 않고 무효로 끝난다(`meter_describe_unavailable`·`meter_marker_fields_mismatch:<표식>`·`meter_build_invalid`·`meter_build_unknown`·`meter_build_mismatch`). 근거는 `meter-preflight.json`에 남고, 빌드 값은 모양 검사를 지난 것만 싣는다(아니면 null). B·O 흐름이 같다. 2026-11-17 파일럿 B 1회차가 #638 이전 이미지로 모델 비용을 쓴 뒤 `llm_marker_invalid`로 무효가 된 것을 막으려는 점검이다. git worktree에서는 Go가 VCS 정보를 넣지 않으므로 빌드 명령이 커밋을 `-ldflags`로 넣는다(official-run.md).

**탐색 분리 배치 B 회차(결정안 §6):** 러너 설정에 `split` 블록이 있으면 B(원본 직접 배치)의 작업 본문 origin을 O와 같은 대상 이름(`http://<split.target_name>:<계측기 포트>`)으로 쓰고, 엔진 망에서 그 이름을 계측기 별칭으로 단다(`deploy/eval/compose.target-alias.yaml`). 선언·지문의 `target_origin`은 `eval-meter` 그대로다. 작업 본문의 목표(`goal`)는 설정 값 대신 제품 공통 목표 문장(`runner_goal.common_goal`, 등록 경로 목록 `/`, 계정 라벨 없음)으로 렌더하고, 그 SHA를 `task.json`(`goal_common_sha256`)에 남긴다. O 실행 기록의 `goal.common_sha256`과 대조할 값이다(보존 sidecar v3에서 공통 지문으로 묶는다). 설명(`description`)은 설정 그대로이며 첫 URL이 대상 이름 origin이어야 한다. 점검은 엔진 netns에서 대상 이름이 계측기로 풀리는지 양성 대조(`meter_alias_tcp`)를 하나 더 본다. 이 경우 개선 후 쪽은 엔진 직접 배치로 돌지 않는다(`SPLIT_LAYOUT_PLAN`). 보존 회차(`preservation` 선택)면 B는 보존 sidecar v3를 낸다(결정안 §3.3). 회차 망을 띄우기 전에 원본 엔진 이미지의 도구 면을 1회용 컨테이너로 재 `tool-surface.json`에 남기고(엔진 이미지 PATH 전체 CLI, MCP 원 목록, 노출 목록은 원본 시드가 꺼져 빈 목록), 칸은 공통 대조(예산·모델 핀·대상·초기 상태·머신·공통 러너 설정·목표 공통 SHA·통신·MCP 판·MCP 원 목록)·쪽별(`artex-direct/v1`, 구동 코드·구동 설정 지문, 쓰기, 모델 예산 선언, CLI·노출 목록 지문, 브라우저·정책 칸 null)·회차별(멈춘 이유, `first_limit`, 엔진 끝 신호, 429 수)로 나눈다. 칸 모으기는 B·O 공용 `preservation_v3_fields`다.

**탐색 분리 배치 O 회차(결정안 4-7c):** 러너 설정에 `split` 블록이 있고 `--side ours`면 엔진 직접 배치 대신 제품 배치로 돈다(`runner_split_main.run_split_ours`). 관리자 비밀번호는 묻지 않는다(회차마다 새로 만들어 보호 입력에만 둔다). 사전검사 선언의 O 쪽에서 대상 이미지·계측기 포트·요청/시간 예산을 읽는다. 봉인 뒤 유예·정착 대기는 러너 설정과 같아야 하고(`run_drain: CONFIG_MISMATCH`), 모델·제공자는 승인 manifest와 같아야 한다(`llm: MANIFEST_MISMATCH`). manifest의 `expected_provider_name`도 러너 설정 `llm_gateway.expected_provider_name`과 글자까지 같아야 한다(같은 사유). 제품은 manifest 단가 상한(`max_*_price_usd_micros_per_million`)을 OpenRouter `provider.max_price`로 보내므로, 상한이 그 제공자 엔드포인트의 실제 단가보다 낮으면 첫 호출이 거절되고 승인이 잠겨 모델 호출 없이 `model_usage_unknown`으로 끝난다(2026-11-18 파일럿 O 1회, 비용 0). 단가는 러너가 확인하지 않으므로 manifest를 만들 때 엔드포인트 메타데이터로 확인한다. 모델 키는 B 게이트웨이와 같은 비밀 env 파일의 `EVAL_LLM_API_KEY`를 같은 compose 규칙으로 읽어(`Docker.env_file_value`) 제품 모델 키 API로만 넘긴다. 경계에는 `path_key_file`과 같은 바이트의 사본을 준다. 띄우기 전 실패(manifest·선언·보호 입력·렌더·키 읽기)는 B와 같이 종료 코드 2이고, 보호 입력을 먼저 지운 뒤 회차 디렉터리를 지운다. 회차 흐름은 `runner_split_run.execute_split_run`이고 무효면 1, 완료면 0이다. 흐름 끝에서 제품 호출 기록을 아래 사후 검증으로 판정한다. 조회 설정은 B 게이트웨이와 같은 값(`runner_split_main.llm_lookup_env`, 시험이 `plan_run`과 대조)이고, 샘플링 기대값은 B 게이트웨이가 넣는 선언 값이다. 사유가 있으면 첫째가 회차 사유가 되고, 요약은 `llm-summary.json`에 남는다. 보존 sidecar v3 제품 블록 근거(결정안 §3.3)도 모은다. 회차 망을 띄우기 전에 도구 면을 1회용 컨테이너로 재 `tool-surface.json`에 남긴다(제품 이미지 중 E② 샌드박스가 보는 CLI, 탐색 브라우저 MCP 원 목록·허용 목록 적용 뒤 목록, 이미지 ID가 회차와 다르면 `tool_surface_image_mismatch`). 제품을 멈춘 뒤에는 제어 정책 보기(`GET /api/tasks/{id}/control-policy`)의 E② 정책 내용 지문과 상한을 읽는다. 정책 참조는 그 초안의 것(`policy-<초안 ID>`)이고 개정은 제품 참조 규칙 안이어야 하며(`control_policy_unbound`), 지문이 비면 `diagnosis_policy_fingerprint_missing`, 상한은 여섯 칸 모두 양의 정수여야 한다(`control_policy_limits_invalid`). 근거·지문에는 그 여섯 칸만 쓴다. 같은 자리에서 product-state 볼륨 `config/`의 설정 넷을 도우미(uid 1000, 읽기 전용)로 읽어 원본 SHA만 `product-files.json`에 남긴다. 내용은 남기지 않는다(설치 입력에 비밀 핀이 있다). 실행 기록은 형식 판·작업 번호·실행 참조(`run-<초안 ID>`, 작업 승인 때 붙음)가 이 회차 것이어야 한다(`run_record_invalid`·`run_record_unbound`). 진단 실행이 붙은 회차(`run.execution`, 그 범위가 이 초안·작업·정책 개정과 같아야 함)는 `runs/<초안 ID>/state/runtime-conditions.json`도 읽는다. 초안 ID가 실행 디렉터리 이름이다. 생산자가 쓰는 여섯 칸(버전·범위·정책 개정·worker 수·프로세스 상한 여섯 칸·대기열 시간 칸)이 타입대로 다 있고 범위·개정이 이 회차와 같아야 하며(`runtime_conditions_invalid`·`runtime_conditions_unbound`), 원본 바이트 SHA를 남긴다. 탐색 중 예산이 끝나 진단 실행이 붙지 않은 회차는 이 파일이 없다. 그때만 `diagnosis_entered: false`와 null로 남기고 `first_limit`은 그대로다. 복사 도우미가 시간 초과 등으로 실패하면 그 역할·볼륨의 도우미를 지우고 없어졌는지 다시 본다. 남으면 `docker_helper_cleanup_failed`다. 운영자 중단도 다시 올리기 전에 같은 정리를 하고, 정리가 실패하면 결과에 `interrupted`와 함께 그 사유를 남긴다. 완료 회차는 B와 같은 v1.1 러너 기록을 낸다([설계안](split-official-run-record.md) R1~R3). 회차 문맥의 실행 ID는 `preparation` 컨테이너, 타임라인은 대상 준비(점검 뒤)·pre 점수판·시작 요청·계측기 봉인·drain·post 점수판이다. pre/post 배치 검사에서 호스트 리슨도 B와 같은 규칙으로 보고(`pre_host_condition_violation`), 관측기 GET은 러너가 보낸 pre·post 두 건이어야 한다(`observer_count_mismatch`). 격리 근거는 B와 같은 행 종류이고 O 출처를 쓴다: 점검 행은 출발점 이름을 ID 앞에 붙이고 판정하지 않는 기록 행(`record`)도 남긴다, E② 자리는 `split_product`(건수 null, 시도 대조 행 없음 = 대조 미수집, 차단 시도 수 null), 판정 수집은 `not_collected`, E③ 상태는 수집했으면 `e3_confirmation` 행. 실행 조건은 분리 배치 표시·실행 출처·E③ 기대(늘 참)·공통 목표 SHA와 O 값(엔진 설정 = 구동 설정·정책 상한 지문, 규칙 파일 null, 도구 트리 = 도구 면 지문, Worker 수 = 실행 조건 파일의 결속 값(진단 미진입이면 null), 표시 언어 null(러너가 읽을 경로 없음), 스트리밍 = 실제 모델 회차 거짓(제품 transport가 stream을 거부), 합성 null)이다. 기록에 필요한 선언·설정(조건 이름·지문 둘·대상 origin·관측 경로·읽기 전용 근거·모델 태그)이 비면 회차를 띄우기 전에 거부한다(실행 입력을 만들 때 `split_run_spec.<칸>: MISSING` 설정 오류로 종료 코드 2, 흐름을 직접 부르면 아무것도 띄우지 않고 `record_spec_incomplete`). 해결 상태가 역행하면 `record_build_failed`다. 내린 뒤 보존 sidecar(있으면)·러너 기록·결과를 러너 키로 서명한다(`record_sign_failed`·`sign_failed`). 결과는 B와 같은 형식(`abc2lab-eval-runner-result-v1`)이고 `layout`·`e2_mode: split_product`, O 작업 요청 지문(설치·등록 조건 지문)을 싣는다. 보존 옵션(`preservation`)이 있으면 O도 보존 sidecar v3를 낸다(결정안 §3.3). 공통 칸은 B와 같은 정의(모델 핀은 같은 함수, 샘플링은 제품이 실제로 보낸 값)이고, 쪽별 칸은 `exploration-split/v1`·제품 구동 코드·구동 설정(작업 번호 대기·폴링 간격·등록 유효 시간)·설치 예산 값 지문·탐색 브라우저 이미지·경계 설정 지문·E② 정책 내용 지문·도구 면이며, 제품 블록은 제품 커밋·이미지 셋·설치 파일 원본 SHA·설치·등록 조건·샘플링 덮어쓰기·호출 기록 완전성이다. 진단 실행이 붙지 않은 회차는 실행 조건 SHA만 null이다. 내린 뒤 최종 계측기 바이트로 빌드하면서 경계 원자료(`run/boundary`)를 계측기 경로 행과 대조하고(계약 §4.1, 같은 키 지문), 러너 키로 서명해 `preservation-round.json`에 쓴다. 실패하면 `preservation_record_failed`·`preservation_sign_failed`다. 반복 순번은 B와 같이 `--repeat`에서 받는다.

- **O 발견 출처별 건수(`discovery-sources.json`, 고정 초안 §8):** 제품을 멈춘 뒤 그 초안 Job의 워크플로 snapshot(`GET /api/product/workflows/{job}`)을 읽어 끝점 발견 출처별 건수(`by_source`), 브라우저(MCP) 출처 합(`browser_*`), API Recon 출처 합(`api_recon_*`), 공개 투영 상한으로 빠진 수(`omitted`)를 남긴다. 결과 요약 `discovery_sources`에도 싣는다. 출처 이름과 건수만 남기고 경로는 남기지 않는다. 기록만 하고 회차 유효성에 쓰지 않으며, 읽지 못하면 고정 코드(`error`)만 남긴다. `O-nomcp`를 돌릴지 정할 때 MCP 출처 발견 비중의 근거다.
- **O E② v3 저널 수집(`e2-v3-collection.json`, 선택 `split.e2_v3`):** 동찬님 답(2026-11-19)대로 러너가 회차마다 감사 키(32바이트)를 새로 만들어 보호 입력 디렉터리에 `e2-audit.key`(0600)로 두고, 제품 overlay가 읽는 compose 변수(`audit_key_variable`, 제품 주입 배선에 맞춰 정함)에 경로를 건다. 키는 경로 가림 키와 같으면 쓰지 않고, 값은 저장소·로그·결과물에 남기지 않으며(근거에는 참조 `audit_key_ref` 16 hex만), 보호 입력과 함께 늘 지운다. 제품 실행 기록을 읽은 뒤·내리기 전에 설정한 볼륨·경로(`volume`·`directory`·`subpath`, `{draft_id}` 치환)에서 저널(`journal_name`, 256 MiB)과 신뢰 저장소의 봉인된 최종 head(`head_name`, `e2-audit-verify --checkpoint` 형식, 4 KiB)를 따로 꺼내 러너 전용 사본(0700·0600)으로 옮기고 `e2-audit-verify --journal-format e2-http-v3`(`verify_tool`)로 같은 키를 넣어 검증한다. 제품이 저널·head를 내지 않았거나(꺼내기 도우미의 tar가 그 파일·디렉터리가 없어서만 실패함, `docker_helper_file_missing`) 모양이 다르거나 검증기가 무결성·현재성 실패(`audit_integrity_failed` 등)를 내면 러너 결과는 유효로 두고 통제 대조를 미수집(`uncollected`, 사유 포함)으로 남긴다. 다만 고정 초안 §20은 ACK 불명 뒤 송신이 있었거나 그 여부를 확인할 근거가 없으면 통제 위반(성능 표 제외)으로 센다. 이 구분은 제품이 게시 시도·실패 수와 그 뒤 송신 여부를 남긴 뒤 붙이므로, 그 전의 `uncollected` 회차는 이 결과만으로 성능 표에 넣지 않는다. 러너 쪽 장애는 평가 환경 원인이라 회차 무효다: 꺼내기의 시간 초과·출력 상한·tar 손상·그 밖의 복사 실패·도우미 정리 실패는 `e2_v3_export_failed`, 키 파일·사본·검증기 실행·출력은 `e2_v3_collect_failed`. 순서는 작업 끝 신호 → CLI 관측 → v3 수집 → 호출 기록 복사 → 내리기다. 지금 dev 제품은 v2 저널·Recorder 미주입이라 이 설정을 켜면 미수집으로 남는다. 저널 행을 통제 대조(시도 수·E③ 호출 없음)에 쓰는 단계는 수집이 실제 제품에서 확인된 뒤 붙인다.
- **O 제품 작업 끝 신호(`product-task-end.json`, v2):** 제품을 멈추고 실행 기록을 읽은 뒤, 제품 DB 컨테이너 안에서 읽기 전용 세션(`default_transaction_read_only`)으로 그 작업의 `tasks` 행(상태·완료 시각·탐색 번호)과 그 탐색의 목표(`goal`)·의도(`intent`) 노드 상태별 수를 읽어 남긴다(목표 수·달성 수는 목표 상태별 수에서 셈). 제품 관리 API는 작업 단건·목표 목록을 중계하지 않는다(목록 `GET /api/tasks`는 307, 단건 `GET /api/tasks/{id}`는 403, 2026-11-19 O 진단 회차). 목표 글 등 원문은 옮기지 않는다. 제품 회차가 왜 마감했는지(목표를 다 채워 자동 완료됐는지)를 가르는 근거이고 판정에 쓰지 않는다. 읽지 못하면 회차를 무효로 하지 않고 고정 코드(`error`)만 남긴다.
- **O B 전용 CLI 관측·이름 못 찾음(`cli-usage.json`·`cli-observed-lines.json`):** 설정 `split.cli_usage_names`가 있으면, 제품 작업 끝 신호 다음·내리기 전에 그 작업의 탐색 번호(제품 DB `tasks.exploration_id`, 작업 끝 신호와 같은 조회)로 제품 활동 표의 Bash 호출(도구 입력·결과·실패 표시)을 읽는다. B가 원본 `/api/commands`로 읽는 것과 같은 표이고, 제품 모드의 공개 활동 API는 명령·결과를 지우므로 제품 DB 컨테이너 안에서 읽기 전용 세션(`default_transaction_read_only`)으로 조회한다. 상한은 B 명령 기록과 같은 `findings.max_total_bytes`다. 집계는 B와 같은 함수(`count_usage`·`observed_lines`)이고, O는 결과의 종료 코드·셸 표준 줄로 이름별 `not_found`·`other_failures`와 이름과 상관없는 `not_found_calls`(이름은 남기지 않음)를 더 센다. 원문은 메모리에서만 보고 모양(이름·연산자만)만 남긴다. 읽지 못하면 B처럼 회차가 무효(`cli_usage_failed`)다.

**O 화면 관찰 로그인 파일(선택 `split.observer_password_file`, 2026-11-20 사용자):** O 회차를 제품 화면으로 지켜볼 때(읽기만, 공식 회차 절차 6절) 로그인할 회차 관리자 아이디·비밀번호를 이 경로의 파일 하나에만 쓴다(`runner_observer.py`). 경로는 절대 경로이고 `runs_root` 밖이며, 그 디렉터리 안에 다른 설정 파일(비밀 env·러너 키·경로 가림 키·엔진 규칙·어휘·manifest·합성 계획)이 없어야 하고(`SHARED_DIRECTORY`), 부모는 러너 uid 소유 0700 디렉터리여야 한다(아니면 띄우기 전 설정 오류, 종료 코드 2, 회차 디렉터리 없음). 파일은 보호 입력을 만든 직후 새로 쓰고(O_EXCL·0600, 링크는 따르지 않음, 강제 종료로 남은 전 회차 파일은 러너 uid 소유 일반 파일이고 관찰 로그인 파일 모양일 때만 지우고 새로 씀(그 밖의 파일은 지우지 않고 `observer_file_not_ours`로 멈춤)), 회차가 어떻게 끝나도 보호 입력과 같이 지운다. 값·경로는 출력·결과·서명 근거에 남기지 않고, 설정 지문(러너 설정·구동기 설정 SHA)에도 넣지 않는다. 화면 관찰 여부는 회차 조건이 아니다. 대신 이 칸을 썼는지만 회차 결과에 `observer_login_file`(참/거짓, 완료·무효 결과 모두)로 남긴다. 공식 O 회차가 모두 같은 값인지 이것으로 본다(공식 회차 절차 4절 7번).

**O E③ 비활성 요구(선택 `split.e3_disabled_required`, 2026-11-20 사용자):** 켜면 러너가 두 가지를 확인하고, 하나라도 아니면 회차를 무효로 낸다. ① 그 회차 E② 정책의 확정 몫이 0으로 적혀 있음(`e3_share_unconfirmed`·`e3_share_not_zero`). ② 확정 단계 기록이 `disabled/declaration_missing`(`e3_official_not_disabled`, 복사 못 함 `e3_status_not_collected`, 모양 오류 `e3_confirmation_invalid`). 값은 회차 조건(`run_conditions.e3_disabled_required`, 서명 근거)에 남고, 격리 검증은 참인데 기록이 다르면 거부한다. 공식 회차(`purpose: official`)는 켜야만 설정 검사를 통과한다. 확정 단계를 켠 회차(수용시험·배선 확인 등)는 끈다. `purpose`는 채점 가능 여부만 정하고 이 검사를 걸지 않는다. 없으면 거짓이다.

**O 합성 모델 모드(2026-11-18 사용자 결정 (a), 평가 전용):** `split.synthetic_model`(`plan_path` 절대 경로, `max_tokens`)이 있으면 O는 OpenRouter 대신 평가 overlay `deploy/eval/compose.split-synthetic.yaml`의 스크립트 모델(`scripts/eval/scripted_product_llm.py`)로 돈다. 제품 기본 설정·설치 화면에는 이 모드가 없고(렌더 시험이 제품 compose·예시·README·화면 코드에 이름이 없는지 본다), 제품 코드는 그대로이며 제품의 로컬 모델 설치(`local_model`)를 쓴다.
- 조건: `manifest_path`는 null, 보존 옵션 `execution_source`는 `synthetic_fixture`, 목적은 공식이 아니어야 한다. 반대로 실제 모델 O 회차는 `execution_source`가 `model_run`이어야 한다(`config.split: EXECUTION_SOURCE_NOT_MODEL_RUN`).
- 주소 핀: `local_model`의 주소·포트는 스크립트 모델 서비스 하나(`eval-scripted-model`, `10.252.2.30:8080`)뿐이다. 사전검사 선언 O 쪽 LLM은 그 주소를 가리키는 로컬 백엔드여야 하고(`llm: SYNTHETIC_MODEL_MISMATCH`), 문맥 칸은 선언 `context_window_k`다.
- 끝내기: 계획에 `finish: "prove_goals"`가 있으면 스크립트 모델이 목표 달성 선언 도구(C26)로 끝을 낸다. Worker는 계획 명령을 다 낸 뒤, 그 대화의 Bash 결과가 모두 E② 신뢰 결과 `completed`·종료 코드 0일 때만 그 의도로 고정 관찰 사실 하나를 남기고(`record_fact`, 의도 번호는 Worker 문맥의 `Intent id`, 거부·실패·불명이나 명령을 안 낸 Worker는 남기지 않음), Planner는 의도를 다 내고 계획의 명령 항목마다 완료된 호출이 있으며(같은 명령이 두 번이면 호출 둘 다, 호출은 대화와 그 대화 안 Bash 순번으로 가림) 실패한 호출이 없을 때만 `list_goals` → `list_facts` → 열린 목표마다 한 번 `prove_goal`을 부른다(하나라도 거부·실패면 목표를 open으로 둬 회차가 상한까지 간다). 모든 목표가 met가 되면 엔진이 작업을 자동 완료한다. 제품이 빈 계획에서 바로 마감하지 않고 원본처럼 알림·심장박동 때 다시 계획하게 되어도 합성 회차가 빈 계획 뒤 기다리지 않게 하려는 것이다. 레포의 합성 계획(VAmPI·Juice Shop)은 이 값을 둔다.
- 계획 파일: 리뷰받은 고정 요청 목록이다(`deploy/eval/synthetic/*.json`, 형식 `abc2lab.scripted-product-plan/v1`). 대상 origin은 `{origin}` 자리에 러너가 넣는다. 평가 정답(챌린지 목록)은 넣지 않는다. 러너가 회차 디렉터리에 0600으로 복사해 그 사본을 붙이고, 사본 SHA를 결과(`scripted_plan_sha256`)에 남긴다. 계획 모델은 선언 모델과 같아야 한다.
- 키·검증: 모델 키를 읽지 않는다. OpenRouter 장부·호출 기록 대조와 사후 검증은 하지 않고 `llm_verification: not_applicable_synthetic`으로 남긴다. 이 값은 합성 기록에서만 받고, 실제 모델 기록(`model_run`)에 나오면 sidecar 검증이 무효로 본다(`preservation_llm_verification_invalid`).
- 결과: 실행 출처는 `synthetic_fixture`다. 채점기·비교 표·보존율 계산이 이 기록을 거부한다(#707). 배선 확인용이고 수치는 효과로 쓰지 않는다.

**제품(O) 호출 기록 사후 검증(탐색 분리 배치 결정안 D(b)):** O는 게이트웨이를 거치지 않고 제품 자체 OpenRouter 경로로 모델을 부른다. 제품은 호출마다 보호 저장소에 기록을 남긴다(`llmpolicy.OpenRouterCallRecord`, 승인 장부 옆 `.openrouter-<승인 해시>.calls`). 칸·결정·끝 모양 어휘가 게이트웨이 `llm-requests` 행과 같아서 위 네 끝 모양을 같은 뜻으로 센다. 제품은 스트리밍을 받지 않아 `*_after_done`은 나오지 않는다. 러너는 회차 뒤 이 기록을 `eval-meter -role generation-verify`의 표준 입력으로 넣는다. 이 역할은 게이트웨이와 같은 조회 코드(`generationVerifier`)와 같은 셈 규칙으로 같은 모양의 `llm-verified.json`(`role: generation-verify`)을 쓴다. 기록 줄은 엄격하게 읽고, 칸·중복 키·순번·결정·끝 모양·상태·시각·id 모양이 어긋나면 표식을 쓰지 않는다. 키는 게이트웨이와 같은 `EVAL_LLM_API_KEY`로만 받는다. 판정은 `runner_llm.judge_product_llm`이 `judge_llm`과 같은 규칙으로 한다. 이때 샘플링은 선언이 아니라 같은 회차 묶음의 B 게이트웨이 적용값과 대조하고, O 쪽 값은 제품이 실제로 보낸 바이트에서 읽은 값이다(manifest v3, #630). 제품 기록에는 봉인이 없어 `sealed_at`·`drained_at`은 검증 시작 시각이다. 거부·봉인 뒤 거부·봉인 취소는 0이다. 샘플링 칸·provider 칸 거부는 게이트웨이에만 있는 판정이라 `null`이다(0으로 적힌 제품 표식은 모양 오류). 429는 같은 규칙(위쪽 상태 429인 행)으로 `rate_limited`에 센다. 제품이 기록을 남기지 못한 수(`CallRecordFailures`)는 제품 밖으로 나오지 않는다. 그래서 러너는 같은 보호 디렉터리의 승인 장부(`.openrouter-<승인 해시>.ledger`)의 예약 줄(`r1`) 수와 호출 기록 줄 수를 대조한다(`runner_product_llm.call_records_reasons`, 다르면 `llm_call_records_incomplete`). 운영 경로의 예약은 transport 한 곳뿐이고, 예약에 성공한 호출은 반환 경로 없이 기록을 하나 남기기 때문에 둘은 같아야 한다. 호출 기록 순번은 "기존 줄 수 + 1"이라 기록 실패가 순번에 빈칸을 남기지 않는다. 그래서 순번으로는 누락을 알 수 없다. `-sampling`에는 제품 실행 기록 API(`GET /api/tasks/{id}/diagnosis-run`)의 실행·준비 모델 사용량에 적힌, 제품이 실제로 보낸 값을 넣는다(`runner_product_llm.product_model_usage`, 사용량끼리 다르면 거부). 같은 사용량의 `sampling_overwrites`(제품이 엔진 temperature를 덮어쓴 요청 수)는 판정하지 않고 실행·준비 칸별로 기록한다. 검증 실행(`Docker.generation_verify`)은 B 게이트웨이와 같은 compose 값(위쪽 origin·제공자·조회 경로·재시도 횟수·간격·TLS 신뢰 묶음)을 쓴다. 키는 명령줄·파일에 싣지 않고 `-e EVAL_LLM_API_KEY`(이름만)와 그 docker 호출의 환경으로만 넘긴다(값은 러너가 env 파일을 따로 해석하지 않고, B 회차와 같은 클라이언트 환경·같은 `--env-file`로 그 이름 하나를 참조하는 `docker compose config`를 돌려 얻는다: `Docker.env_file_value`. 따옴표·주석·치환 문법이 B 게이트웨이와 어긋나지 않게 하려는 것이다). 러너가 이 부품을 실제 회차에 연결하는 것은 배치 구현 순서 4-7c다.

## 아직 안 된 것

- **개선 후 판정 레코드:** 러너 쪽 수집은 붙어 있다(위 판정 export). 서버의 동결 API와 사전 회차 등록이 아직 없어서 지금은 켜도 `not_collected`로 끝난다.
- **tmpfs 사용량 측정 방법:** 엔진 안의 `df`나 `PATH`는 엔진 도구(Bash·Write 등)가 바꿀 수 있어 믿지 않는다(#140 리뷰). 러너가 계측기 이미지(내용 ID)를
  망 없이·읽기 전용·pull 없이 띄워 대상 컨테이너의 PID namespace에 붙인다. 측정 역할은 대상의 `/proc/1/mountinfo`에서 마운트 지점이 정확히 그
  경로인 tmpfs를 찾고, `/proc/1/root/<마운트>`를 마지막 symlink를 따르지 않고 한 번 연 뒤 그 객체의 마운트 ID(statx)가 mountinfo의 ID와 같을 때만
  같은 fd로 statfs한다(커널 파일시스템 종류가 tmpfs인지 확인). 경로 문자열만으로는 부모 경로 변경이나 symlink(예: 컨테이너마다 있는 `/dev/shm`)로
  다른 tmpfs를 잴 수 있어서다(#140 리뷰). 러너는 구간 전에도 한 번 재서 마운트 ID를 기록하고 구간 뒤 같은 ID일 때만 받는다. 다른 프로세스의
  root를 따라가려고 root 사용자와 `SYS_PTRACE` 하나만 주고 나머지 권한은 뺀다. 대상 안의 실행파일은 실행하지 않는다. 결과 `tmpfs_usage`에는
  두 볼륨의 마운트 ID·크기·사용·남은 바이트, 구간 전 측정(`pre`), 적용 하한 `min_free_bytes`가 서명되어 남는다.
- **tmpfs 사용량 시점:** 구간 중 새 컨테이너 시작·회차 컨테이너 exec는 경계 변경이라 seal 시점이 아니라 구간 종료 직후에 한 번 잰다. seal 뒤에도
  엔진은 계속 돌므로 보통 그 값은 seal 시점 이상이지만, 그사이 사용량이 줄었을 가능성(예: DB 정리)은 배제하지 못한다. 측정 전에
  끝난 회차는 `tmpfs_usage: "not_measured"`이고 상한 도달 여부를 알 수 없으므로 공식 비교에 쓰지 않는다.
- **아직 실측하지 않은 것:** 평가 머신에서의 같은 항목들. api 백엔드의 엔드포인트 tag(`deepinfra/fp8`) 회차는 11/03 개발 머신에서 1회만 돌렸고,
  이 브랜치 단독이 아니라 #140·drain 유예(#153)를 합친 트리였다(완료·서명, generation 27건의 응답 제공자 전부 `DeepInfra`). 실측한 것은
  그 slug가 요청에서 수락되고 응답 제공자가 DeepInfra였다는 것까지이고, 그 회차의 단일 엔드포인트·정밀도는 확인하지 않았다(프로토콜 §6).

## 스모크 도구

API 키 없이 실제 엔진을 끝까지 돌릴 때 쓴다. 판단하는 모델이 아니므로 **요청 수·차단 수를 효과 수치로 쓰지 않는다.**

- `scripts/eval/scripted_llm.py`: 계획 파일의 명령을 Bash 도구 호출로 돌려주는 OpenAI 호환 서버다. 대상 경로는 코드에 없고 계획 파일에 있다. 계획 파일은 레포 밖에 둔다. 플래너 라운드가 반복되므로 요청 수는 시간 예산에 따라 달라진다. local 백엔드의 고정 IP에 띄운다.
  ```
  {"model": "<선언한 모델>", "intent_summary": "<의도 설명>", "commands": ["curl ...", "curl ..."]}
  python3 -I -B scripts/eval/scripted_llm.py --host <LLM 고정 IP> --port <포트> --plan <계획 JSON> [--stats <통계 파일>]
  ```
- `scripts/eval/canary_scan.py`: 회차 디렉터리에 합성 토큰 원문이 남았는지 본다. 파일 내용, 파일·디렉터리 이름, 심볼릭 링크 대상을 본다. 출력은 건수와 걸린 경로뿐이고 경로 안의 값은 가린다. 없음 0, 있음 1, 입력 오류·읽기 실패·들어가지 못한 디렉터리는 2로 끝난다.
  ```
  python3 -I -B scripts/eval/canary_scan.py <회차 디렉터리> --canary <합성 값>
  ```

## 스모크 실측 (11/03, 개발 머신)

환경: WSL2 Ubuntu 24.04, Ubuntu `docker.io` 29.1.3(Docker Desktop 통합 끔) + `docker-compose-v2` 2.40.3, local 백엔드 + 호스트
고정 IP(docker0 `172.17.0.1`)의 가짜 OpenAI 호환 LLM(정해진 짧은 응답, 외부 송신 없음), ours 엔진(웹 UI 미내장 빌드), 시간 예산 90초.
평가 머신의 결과가 아니며, 평가 머신이 정해지면 같은 항목을 다시 확인한다.

- **상태 차이(11/05, 설정 `state` 켬, baseline e53a79f):** 완료·서명. 구간 이벤트 148건 중 pause/unpause 0건(pre 사본은 구간 전 lookback, post 사본은 이벤트 스트림을 닫은 뒤), 경계 위반 0, `state/pre`·`state/post` 빈 디렉터리, `state-diff.json`이 서명 결과 근거 목록에 있음. 가짜 LLM이라 차이 0(테이블 18, 신호 `Challenges` 102→102).
- **상태 차이 세 갈래(11/06, 설정 `state`에 런타임 루트 3개·메모리 경로 5개, baseline e53a79f):** 완료·서명, 경계 위반 0. 구간 이벤트 71건 중 외부 부착 0·회차 컨테이너 재시작 0(1회용 읽기 컨테이너는 구간 전 lookback과 이벤트 스트림을 닫은 뒤에만 돈다). `state/pre`·`state/post` 파일 0개, 회차 디렉터리 전체에 메모리 경로 문자열 0건. 가짜 LLM이라 차이 0(읽기 5, 건수 4→4). 변화 감지는 1회용 Juice Shop에서 같은 코드로 따로 봤다: 같은 상태끼리 차이 0, 리뷰 1건 쓴 뒤 `changed 1`·건수 2→3.
- **가짜 E² 자리에 실제 요청(11/06, 스크립트 LLM):** 개선 후 자리 회차가 완료·서명됐다. 결정 56건이 모두 허용·관측, 대상 응답 수신 52건(계측기 전달 52건과 같음), 나머지 4건은 seal 뒤 거부, 재사용·기록 없는 도달 0이다. 이전까지 이 경로는 요청 0건으로만 돌았다. 가짜 E²라 통제 증명이 아니다.
- **합성 토큰 잔류(11/06, baseline e53a79f):** 계획의 명령에 합성 값을 `Authorization`·`Cookie`로 실어 규칙 없는 회차와 규칙 넣은 회차를 돌렸다. `canary_scan.py` 결과는 회차 파일 29개·30개 모두 0건이고, 회차 컨테이너·볼륨·망은 0개였다. 같은 값이 든 계획 파일은 스캔에 걸려서 검사가 동작하는 것도 확인했다.
- **규칙이 실제 도구 호출을 막는지(11/06, baseline e53a79f):** 정해진 curl을 Bash로 부르는 스크립트 LLM으로 실제 엔진을 돌렸다(`scripted_llm.py`, 외부 송신 없음). 같은 스크립트로 규칙 없는 회차는 전달 51(POST 17), 내장 규칙 차단 18(`curl -X DELETE`), `Users`·`Wallets` 변화였고, `curl -X POST`를 막는 합성 규칙을 넣은 회차는 전달 52(POST 0), 파일 규칙 차단 28·내장 규칙 차단 28, 상태 변화 없음이었다. 스크립트가 정한 요청이라 효과 수치가 아니다.
- **원본 ② 규칙 주입(11/06, baseline e53a79f):** 합성 규칙 2개와 기본 목록 + 도구 1개를 넣은 회차가 완료·서명됐다(내장 20개 중 19개 켜짐, 다시 읽기 대조 통과, 앞지르는 allow 1 기록). 규칙이 없는 회차는 규칙 API를 부르지 않았고, 도구를 줄인 파일과 엔진이 거부하는 정규식은 작업 생성 전에 무효(`engine_rules_failed`)가 됐다.
- **시연 A 표(11/06, `control_demo.py`):** 실제 러너 회차 2개(조건 이름만 다름, 가짜 LLM)로 표를 만들었다. 서명·근거 해시 통과, 격리 증명 없는 회차 0, 전달 0이라 메서드 칸이 모두 0이고 E² 없는 회차의 거부 칸은 `—`다. 메서드별 건수는 실제 계측기 로그로 따로 봤다(loopback 임시 서버로 10건 전달, `get`·임의 토큰은 그 밖의 메서드로 2건, 토큰 문자열 미출력).
- **완료 회차 1회(서명됨):** pre/post 망·publish·이미지·호스트 조건 위반 0, 부정 연결 13개 경로 모두 기대 종류, 관측 요청 3건 =
  러너 호출 수, 작업 생성(요청 본문 해시 기록), 시간 예산 종료, LLM 게이트웨이 2건 전달·seal 뒤 거부 0, findings 0건, 정리 후
  회차 컨테이너·망 0.
- **부정 연결 실패 종류:** 다른 망 IP 직접(대상·관측 front·LLM 원 주소) = `unreachable`(엔진 망이 internal이라 경로 없음),
  엔진 망 게이트웨이:관측 포트 = `refused`(internal 망에도 호스트 쪽 게이트웨이 주소가 있고, 관측 포트는 127.0.0.1에만 publish),
  다른 망의 이름(서비스·컨테이너 이름·짧은 ID) = `dns_error`(SERVFAIL: 내장 DNS가 모르는 이름을 바깥으로 넘기지 못함). 이름 경로의
  허용 종류를 `not_found`·`dns_error`로 정했다. `docker run sha256:<id>`(probe 이미지 내용 ID 지정)는 동작했다.
- **고장 주입(회차 도중, 작업 생성 뒤):** 엔진을 대상 망에 연결 → `post_topology_violation` / 대상 재시작 → `boundary_changed` /
  엔진 재시작 → `boundary_changed` / 계측기 재시작 → `seal_failed`(계측기가 기존 기록 파일을 덮어쓰지 않아 다시 뜨지 않음). 넷 다
  무효·미서명, 정리 후 컨테이너·망 0.
- **회차 밖 컨테이너의 namespace 공유(11/03):** 구간 중 `docker run --network container:<대상>`(망 연결 이벤트 없음) →
  `window_boundary_changed`(회차 밖 컨테이너 create·start), 무효·미서명. 같은 날 정상 회차는 러너 post probe의 컨테이너 이벤트
  5건(create·attach·start·die·destroy)만 cidfile ID로 허용되고 완료·서명.
- **제품 E² 경로를 임시 fixture로(11/07, api 4회차, 공식 결과 아님):** 제품 entrypoint가 없어서 레포 밖 일회용 fixture로 돌렸다. fixture는 제어 채널을 후보 형식대로 답하고 실제 `e2proxy.Execution`·`CreateJournal`로 요청을 보낸다. 작업 ID를 `1`로 가정하는 등 제품 동작이 아니고 통제 효과의 근거도 아니다. 모델·제공자는 아래와 같고 dev 0047f84, 회차당 90~150초다.
  - **확인한 것:** overlay 기동, 망·부정 연결 점검, bind mount된 제어 socket과 `SO_PEERCRED`, `ready` 대조와 `bind`, 계측기 봉인 전 close, 실제 `e2-audit-verify`와 투영 행 대조, 격리 근거 `e2_product` 행, 시연 표. 처음 회차는 fixture의 설정 오류로 `bind`가 거부되어 `e2_refused_unavailable`로 무효가 됐다(러너는 맞게 멈춤).
  - **허용 경로(1회):** 시도 41건 전부 허용, 계측기에 같은 ID로 41건 접수·응답, `allowed_observed` 41, 나쁜 칸 0, 통제 주장 조건 충족, 완료·서명. 이 회차만 fixture가 `Accept-Encoding`을 지우고 보냈다. 제품 E²의 동작이 아니다.
  - **차단 경로(2회):** 그대로 보내면 시도 54건·22건이 전부 `unsupported`로 막혀 계측기 도달 0건이었다. 완료·서명이고 `denied_unobserved`만 있어 허용 양성 근거가 없으니 통제 주장은 보류됐다.
  - **원인:** 헤더를 재 보니 curl 35건은 `Accept-Encoding`이 없고, WebFetch(`User-Agent: norma/*`) 6건은 `gzip`이다. `e2proxy`는 `identity` 아닌 값을 거부한다. 막힌 두 회차의 시도는 전부 `gzip` 요청이었고 시간 예산 안에 curl로 넘어가지 않았다. fixture의 거부 응답은 평문 403이고 표본도 작다.
  - **이 결과의 범위:** 이 회차들의 엔진은 **일반 worker**다. 제품 경로의 E2 worker(`agent/e2_http.go` `executeE2HTTP`)는 도구가 Bash뿐이라 WebFetch가 없고, 서버 연결 전이라 그 경로로는 아직 돌리지 못했다. 그래서 막힌 회차가 공식 회차에서 그대로 나온다는 뜻이 아니다. 제품 경로에서 다시 재야 한다.
- **generation 통계 지연(11/07):** `verify_attempts` 5·간격 2초 회차 하나가 `llm_generation_unverified`로 무효였다. 봉인 3초 전에 끝난 generation 1건의 통계가 재시도 안에 안 나왔다(35건 중 34건은 검증). 15번으로 늘린 다음 회차는 43건 전부 검증됐다. 재시도는 통계가 없을 때만 돌아서 정상 회차 시간은 늘지 않는다. 예시 값을 15로 올렸다. api 백엔드에만 해당한다.
- **generation 조회 미확인(11/18, VAmPI 유료 B·O 쌍):** 15번 × 2초에서 B 33건 전부·O 78건 중 4건이 미확인으로 무효였다. B는 건마다 약 7초에 끝나 재시도를 다 쓴 것이 아니었다(조회가 바로 정해진 실패로 끝났을 가능성). 그때 계측기는 조회 응답 종류를 남기지 않아 원인을 가를 수 없었다. 60번 × 5초로 다시 돌린 B·O는 건마다 순서대로 기다리다 러너 대기 300초를 넘어 표식 없이 무효였다(`llm_unverified`·`llm_verify_failed`). 같은 시각 1토큰 직접 호출의 통계는 약 13초 뒤에 준비됐다. 그래서 조회를 라운드로 바꾸고, 전체 시한을 두고, generation마다 조회 흔적을 남기고, 러너 대기 시한의 하한을 설정 검사에 넣었다. 원인 확인은 다음 회차의 `unverified_last`로 한다.
- **generation 통계 지연(11/25, VAmPI O 단독 진단 회차, 조회 40번 × 3초):** O 첫 시도 `ours-20261125t030938z-6f1ec602`가 `llm_generation_unverified`로 무효였다. 143건 중 7건이 11라운드 내내 404였다(생성 12:27:26~12:29:58 KST, 2분 반 안에 몰림, 모두 회차 중간의 200 응답). 조회는 봉인 뒤 시작해 생성 뒤 약 1분~3분 40초부터 약 3분 20초~5분 50초까지 돌았다. 약 43분 뒤 같은 7건을 다시 조회하니 **모두 200·PrimeIntellect**였다 — 없는 것이 아니라 OpenRouter 통계가 약 6분 넘게 늦게 잡힌 것이다. 그래서 공식 조회 창을 60번 × 10초(계측기 시한 610초), 러너 대기 700초로 넓혔다(2026-11-25 사용자 결정). 라운드로 돌아서 통계가 바로 나오는 회차는 첫 라운드에 끝나 시간이 늘지 않고, 늦은 건이 있는 회차만 최대 약 10분 더 기다린다. 10분으로도 안 잡히는 건 지금처럼 무효로 두고 2N 재시도로 처리한다(세션 끝 재조회로 유효 처리하는 규칙은 넣지 않음 — 2N 재시도와 겹쳐 유효 시도가 둘이 될 수 있음).
- **게이트웨이 조회 연결 분리(11/18):** 같은 날 다시 돌린 유료 쌍은 B 39건·O 61건이 모두 첫 시도에 확인됐다. 다만 그 B는 엔진이 일찍 끝나 마지막 호출과 조회 사이가 451초였고, 무효였던 B 1차는 0초였다. 키·조회 주소·외부 망은 B·O가 같았다. 남은 차이는 조회 시점과 연결 공유였다: B 게이트웨이는 모델 호출 프록시와 연결 풀을 같이 썼고, O는 새 프로세스로 조회했다. 그래서 게이트웨이도 봉인 뒤 조회 전용 새 연결로 조회한다. 시험은 마지막 호출 직후 봉인해도 조회가 프록시 연결로 가지 않고 404 뒤 다음 라운드에서 확인되는지 본다.
- **끝까지 못 읽은 스트림의 종료 모양(11/07, api 3회차, 서명됨):** 위와 같은 모델·제공자, 개선 후 dev af10d00(계측기만 이 분류를 넣어 빌드), E② 없음, 회차당 240초·요청 100건. 응답한 요청 326건(generation 326, 전부 `AtlasCloud`)에서 `upstream_error`가 9건(회차별 2·5·2)이었고 **9건 전부 `client_closed_after_done`**이었다. 게이트웨이가 위쪽 응답에서 종료 줄을 읽었고 호출자 쪽 취소나 쓰기 실패를 관찰했다는 뜻이다. 엔진이 그 줄을 실제로 받았는지는 이 계측으로 알 수 없다. `upstream_cut_*`과 `client_closed_before_done`은 0건이다. 이 표본에서 앞의 비율을 제공자 실패로 볼 근거는 없다. 다른 제공자·로컬 서버·더 긴 회차는 재지 않았다.
- **api 백엔드 회차 1회(11/07, Qwen3.5, 서명됨):** 모델 `qwen/qwen3.5-35b-a3b`, 고정 제공자 slug `atlas-cloud/fp8`(기대 표시 이름 `AtlasCloud`), 개선 후 dev dfb5da0, E② 없음. 스모크라 예산을 줄였다(240초, 요청 100건). 약 3분 만에 요청 예산으로 멈춰 대상 요청 102건(GET 97, POST 5, 초과 2), 완료·서명.
  - LLM 응답 200이 94건이고 generation 94건 전부 응답 제공자 `AtlasCloud`, 엔드포인트 1개, fallback 시도 0, 적용 샘플링은 선언과 같았다. 4xx는 0건이라 `require_parameters:true`로 거부된 요청이 없었다. `stop`을 지원 표시하지 않는 이 엔드포인트에서 실제로 요청이 수락됐다는 뜻이다.
  - 200 중 7건은 게이트웨이가 응답을 EOF까지 읽지 못했다(`upstream_error`, generation은 전부 검증됨). 같은 조건의 다른 회차에서도 약 4~9%였다(1/25, 3/34, 4/44). generation 조회의 제공자 상태는 전부 200이고 봉인으로 끊긴 요청의 499와 달라서, 제공자가 오류 HTTP 상태로 기록한 실패는 없었다. 이때는 본문을 보존하지 않아 스트림 중단이 제공자 쪽인지 호출자 조기 종료인지 구분하지 못했다. 아래 11/07 회차가 이 구분을 잰다.
  - 봉인 뒤 엔진 재시도는 게이트웨이가 503으로 거부했고(78건), 봉인 때 끊긴 요청은 502 2건이다. 둘 다 제공자 오류가 아니다.
- **api 백엔드 회차 1회(11/03, 서명됨):** 모델 `qwen/qwen3.6-35b-a3b`, 고정 제공자 slug `deepinfra`(기대 표시 이름 `DeepInfra`),
  fallback 끔, 개선 후 엔진(dev 9b06c05 빌드, 정책 모드 external), 예산 240초·요청 100건. 엔진이 요청 예산 100건을 약 2분 만에 써서
  그때 봉인했다(계측기 전달 100 = `total_requests`, 봉인 뒤 거부 0, drain 시 진행 0). LLM 게이트웨이는 응답 48건이 모두 generation
  ID를 가졌고 봉인 뒤 엔진의 LLM 요청 12건을 503으로 거부했다. generation 조회 48건 전부 검증: 응답 제공자 `DeepInfra`,
  `provider_responses`는 generation마다 1건(엔드포인트 ID 1종, 상태 200×45·499×3 — 499는 봉인으로 끊긴 요청), fallback 시도 0.
  부정 연결 13개 경로·구간 감시(호스트 리슨 표본 62, 최대 공백 2.0초, 데몬 이벤트 경계 변경 0) 통과. 구간 안 신규 해결 3건이
  `isolated_window`로 귀속됐고 findings 1건, 채점은 판정 미수집이라 코어 GT 9개 전부 판단불가. API 키는 비밀 env 파일로만 넘겼다.
- **실행 설정 적용 양쪽 1회(11/07):** baseline e53a79f와 개선 후 dfb5da0를 API로 각 1회 돌렸다(60초, 요청 20건, `atlas-cloud/fp8`). 작업 전에 넣은 `context_window_k` 200·workers 3이 양쪽 스냅샷에 그대로 나왔고, `reasoning_effort`·`thinking_type`은 비어 있었으며, 프로파일 저장·활성화 뒤에도 양쪽 회차가 완료·서명됐다. 스트리밍 끈 조건은 실제 엔진으로 돌리지 않았다(시험은 요청 본문까지만 본다).
- **local 백엔드 회차 1회(11/07):** 임시 Ollama 0.32.6(`172.17.0.1:11435`, `context_length` 32768, CPU), `qwen2.5:1.5b`, 개선 후 dev 450cc71, 300초·요청 30건, `context_window_k` 32·workers 1로 완료·서명. 스냅샷에 넣은 값이 그대로 나왔다. 회차 전과 작업 생성 직전 모두 남은 유지 시간이 요청값 그대로(1272초)였고, 회차 뒤 조회도 digest·컨텍스트가 일치했으며(1250초) 모델을 내렸다. 작업 직전 재적재 요청은 실제 서버에서 동작을 확인했다. 그 사이 모델이 내려간 상황을 일부러 만들어 보지는 않았다. LLM 호출 2건·대상 요청 0건이라 작은 모델의 도구 호출은 이 회차로 확인되지 않고, baseline은 안 돌렸다.
- **tmpfs 사용량 측정(11/03):** local 백엔드 완료·서명 회차에서 구간 종료 뒤 `df -P -k`로 엔진 DB tmpfs 1 GiB 중 약 50 MiB,
  엔진 데이터 tmpfs 512 MiB 중 약 0.2 MiB 사용(남은 공간 하한 8 MiB 통과). 두 이미지(엔진: GNU coreutils, postgres:16-alpine:
  busybox) 모두 같은 `df -P -k` 머리줄을 냈고, 측정 exec는 구간 밖이라 구간 경계 변경 0.
- **스모크로 고친 것:** `python3 -I` 실행 시 같은 디렉터리 모듈 import 실패, 배포판 데몬의 빈 `Platform.Name`을 출력 오류로 거부,
  internal 망 DNS 실패 종류, 무효 기록의 `failed_step`이 마지막 단계(근거 쓰기)로 남던 것.
- **이 머신 한정 설정:** 호스트 리슨 허용 목록에 WSL DNS 터널링 리졸버 `10.255.255.254:53`(근거 포함). 엔진 이미지는 호스트에
  node가 없어 웹 UI 없이 빌드했다(baseline·ours 동일, 평가는 API만 쓴다).
