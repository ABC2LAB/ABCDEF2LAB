# 평가 러너 운영 안내

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
- stdout은 `run_id 상태 사유…` 한 줄이다. docker의 진단(stderr)은 운영자 터미널로만 가고 러너 기록에는 남지 않는다.
- 회차 중간에 운영자가 할 일은 없다. 작업은 러너가 설정의 `task` 내용으로 엔진 API에 만든다(아래 "작업 생성").

회차의 순서·입력 준비·집계는 [회차 실행 절차](official-run.md)(API 개발 측정·시범, GPU 공식 세션은 후속)를 따른다.

채점: 완료 회차의 `run-record.json`을 채점 manifest의 `run_record_path`로 넣고 `python3 -I -B scripts/eval/score.py <manifest> --runner-key-file <러너 키>`로 채점한다(`records_path`는 records=null이면 읽지 않는다).

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
| `observer_readonly_ref` | 그 GET이 해당 대상 버전에서 상태를 바꾸지 않음을 확인한 별도 기록의 참조(200자 이하). 러너가 만들 수 없는 근거라 설정으로 받고, 격리 근거 `observer_readonly` 행에 그대로 남긴다 |
| `engine.llm_provider`, `engine.llm_stream` | `openai`만(게이트웨이가 chat completions만 받음), 스트리밍 여부 |
| `engine.llm_policy_mode` | 엔진 LLM 정책 모드(`ARTEX_LLM_POLICY_MODE`, #125). `external`만 받는다: 평가는 LLM을 게이트웨이로만 내보내고, 엔진 기본값 local-only는 운영자 manifest·DB profile 준비가 필요해 §6 음성 시험에서 따로 쓴다. baseline(e53a79f)에는 이 스위치가 없다. 결과 기록에 선언값만 남는다(엔진이 현재 모드를 드러내지 않아 관측값 null). baseline 회차는 compose 값이 같이 넘어가도 엔진이 읽지 않으므로 선언값도 null이다 |
| `e2.ours_mode` | 개선 후 회차의 E② 자리(프로토콜 §2 개선 후의 E② 배치). `none` = E² 없음(엔진 → 계측기), `stub` = 통과 전용 가짜 E²(`deploy/eval/compose.e2-stub.yaml`, 계측기 `-role=e2-stub`)를 엔진과 계측기 사이에 둔다. baseline 회차에는 적용하지 않는다(원본에는 E²가 없다). `stub` 회차는 망·부정 연결 점검·계측기 시도 ID 대조를 세우는 fixture이며 통제를 증명하지 않으므로 시범·공식·제품 결과에 쓰지 않는다. 제품 E²가 들어오면 값이 하나 늘어난다 |
| `engine_db.{user,name}`, `tmpfs.{engine_db,engine_data,min_free}`, `health.{interval,timeout,retries}` | 엔진 DB 계정 이름, 회차 메모리 크기 상한과 구간 종료 뒤 남아야 할 최소 공간(두 상한보다 작아야 함), DB healthcheck. `tmpfs` 세 값은 사전검사 `run_storage`(프로파일 v3 환경 지문 입력)와 글자 그대로 같아야 시작한다 |
| `llm_gateway.generation_path` | api: generation 조회 경로(예: OpenRouter `/api/v1/generation`) |
| `llm_gateway.verify_attempts`, `verify_interval_seconds` | generation마다 조회 횟수(1~100)와 간격 |
| `llm_gateway.ca_bundle`, `expected_provider_name` | 호스트 CA 묶음 경로, 기대 응답 제공자 표시 이름 |
| `docker_timeout_seconds`, `probe_timeout_ms` | docker 호출 하나와 부정 연결 점검 하나의 시한 |
| `host_listen_allowlist` | loopback 밖 호스트 리슨 중 허용할 것. `{"address", "port", "reason"}`, 특정 주소만(0.0.0.0·`::`·loopback 불가), 근거 필수. 기본은 빈 목록 |
| `run.*` | compose 대기, down 시한, 대상 준비, 예산 폴링 간격, drain 시한, 스냅샷 시한·크기, LLM 검증 대기(초) |
| `run.task_create_timeout_seconds` | 작업 생성(로그인 + `POST /api/tasks`) 연결·읽기 시한(초) |
| `run.drain_grace_seconds`, `run.post_settle_seconds` | 봉인 뒤 계측기가 진행 요청을 기다리는 유예(1~600초, `drain_timeout_seconds`보다 짧아야 함 — 계측기 `-drain-grace`로 넘김)와, 유예가 지나 취소한 요청이 있을 때 post 전 정착 대기(1~3600초). 둘 다 비교 조건이라 사전검사 `run_drain`의 `grace_seconds`·`settle_seconds`와 정확히 같아야 회차가 시작됨(`preflight.<side>.run_drain: CONFIG_MISMATCH`) |
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
[up 전]  project 이름 중복 없음 → 이미지 내용 ID 확인 → 머신 사양(CPU·논리 CPU·메모리·커널·Docker 버전·머신 ID 해시) → 회차 디렉터리(0700) 생성
[기동]   compose up(--pull never, --no-build, --wait)
[pre]    망·publish·이미지·호스트 조건 점검 → 부정 연결 점검 → tmpfs 마운트 ID·사용량(구간 전) → 대상 준비 → 구간 감시 시작 → pre 스코어보드
         계측기 전달 건수 == 0
[작업]   새로 로그인 → 실행 설정 스냅샷(GET /api/settings·/api/llm, 프록시는 설정 여부로) → workers·엔진 LLM 모델·프록시 대조
         → engine-settings.json → POST /api/tasks(설정의 task, 생성=기동) → task.json
         요청 예산 또는 시간 예산까지 폴링 → 계측기 seal → drain 확인(유예가 지나도 열린 요청은 계측기가 취소·건수 기록)
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
  meter/                         계측기·관측 front 기록(계측기 컨테이너에만 마운트). e2.ours_mode=stub이면 가짜 E²의 결정 행 `e2-stub-decisions.jsonl`도 여기
  llm/                           LLM 게이트웨이 기록(게이트웨이에만 마운트, 계측기와 분리)
  pre/  post/                    스코어보드 원본·정규화본(어느 컨테이너에도 마운트하지 않음, 절대규칙 7)
  pre-checks.json  post-checks.json   망 스냅샷·policy_sha256·위반 목록
  pre-probe.json   post-probe.json    경로별 기대·실제·판정, probe 컨테이너 ID
  pre-probe.cid    post-probe.cid     docker --cidfile이 쓴 probe 컨테이너 ID(구간 감시 허용 목록)
  meter-summary.json  llm-summary.json
  task.json                      작업 생성 요청 본문 SHA-256·엔진 작업 ID·요청/생성 시각(본문 자체는 없음)
  engine-settings.json           작업 생성 전 엔진 실행 설정 스냅샷(프록시는 설정 여부만, API 키 값 없음). 그 SHA-256은 서명 근거 `run_conditions`에
  window-watch.json              구간 감시 요약(호스트 리슨 표본 수·최대 공백·위반, 데몬 이벤트 종류별 건수·경계 변경·완결성)
  findings.json                  원본 수집기·추출기의 비민감 투영(아래)
  isolation-evidence.json        격리 근거 행(러너 기록 v1.1 §3.1 형식, 완료 회차만)
  solve-events.json              pre=false→post=true 키마다 isolated_window 해결 이벤트(평가 전용 key, 완료 회차만)
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

- `sent`·`response`는 같은 시도의 진행 이력이라 서로 배타적인 건수로 합산하지 않는다. "보냈는데 응답 없음"(`yes`·`none`)과 `uncertain`이 결과 불명이다. 계측 건수만으로 대상 앱이 실제 처리한 건수를 확정하지 않는다.
- 계측기는 시도 ID를 기록한 뒤 **대상에 넘기기 전에 그 헤더 하나만 지운다**(평가 프로토콜 §8.3). 형식이 틀려도 요청을 막지 않는다. baseline에는 이 헤더가 없는 것이 정상이다(`absent`).
- `sent=no`인데 `response`가 `none`이 아니거나, 거부 행이 `no`·`none`이 아니면 모순으로 거부한다.

근거 파일은 O_EXCL·0600으로 한 번만 쓴다. 무효 기록에는 측정값·근거 목록·서명을 넣지 않는다.

### 작업 생성

러너는 `pre` 스코어보드와 계측기 전달 0을 확인한 뒤 새로 로그인하고 `POST /api/tasks`로 작업을 만든다. 이 엔드포인트는
생성과 동시에 작업을 기동한다. 요청 구조체·핸들러·기동·대상 추출(`parseTarget`)·응답 DTO·인증 미들웨어가 upstream
`fdfb7a17`·baseline `e53a79f`·개선 후에서 같다는 것을 `tests/test_runner_engine.py`가 고정한다. 그래서 두 쪽에 같은 요청을 보낸다.

- 본문은 설정 `task`의 여섯 필드만 정규 JSON(키 정렬·공백 없음·ASCII)으로 만든 바이트다. 그 SHA-256을 `task.json`과 결과 기록의
  `task_request_sha256`에 남기고, 집계에서 baseline·개선 후 값이 같은지 대조한다. LLM 프로필·출처 작업·회사·분류·가로채기
  규칙은 보내지 않는다. 회차마다 엔진 DB가 새것이고, LLM은 엔진 환경 설정(게이트웨이)을 쓴다.
- 엔진은 `설명 + " " + 목표`에서 대상을 뽑아 자산으로 심는다. 러너는 같은 정규식으로 미리 검사해서, URL이 전부 회차 대상 origin
  (끝 `/` 허용)이고 URL 밖에 IP·도메인 꼴(`index.html` 같은 파일 이름 포함)이 없을 때만 보낸다. 아니면 회차를 만들기 전에
  `task_target_mismatch`로 끝난다(종료 코드 2).
- 작업 내용에 평가 정답(챌린지 이름·목록)을 넣지 않는다(절대규칙 7). 러너는 이것을 검사하지 않으므로 작성·검토자가 지킨다.
- 예산 시계는 생성 요청 직전부터 잰다. 생성 실패는 재시도하지 않고 회차를 무효로 한다.

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

| 코드 | 뜻 |
|---|---|
| `project_exists` | 같은 project가 이미 있음(이 러너 것이 아니므로 up·down 모두 안 함) |
| `project_check_failed`, `image_id_failed`, `image_id_mismatch` | 사전 조회 실패, 로컬 이미지가 설정의 내용 ID와 다름 |
| `host_spec_failed` | 머신 사양을 읽지 못함(`/proc/cpuinfo` 모델명·`/proc/meminfo` MemTotal·`/etc/machine-id`·Docker 버전) |
| `engine_settings_failed`, `engine_settings_mismatch` | 실행 설정 조회 실패·형식 오류, 또는 workers가 선언 `worker_count`와 다름·엔진 LLM 모델이 선언과 다름·프록시가 설정됨(송신 경로가 계측기·게이트웨이 밖으로 바뀜) |
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
| `meter_read_failed`, `meter_log_inconsistent` | 계측기 기록 읽기 실패, 순번 누락·건수 불일치(강제 취소 행 수 ≠ drain 마커의 취소 수 포함) |
| `llm_seal_failed`, `llm_read_failed`, `llm_unverified`, `llm_marker_invalid` | 게이트웨이 seal 실패, 기록 읽기 실패, 검증 마커 시한 초과, 마커 형식 오류 |
| `llm_generation_unverified`, `llm_generation_unverifiable` | 조회 안 된 generation, generation ID 없는 응답 |
| `llm_provider_mismatch`, `llm_fallback_attempted`, `llm_fallback_unverifiable` | 응답 제공자가 기대와 다름, 응답한 제공자와 다른 제공자로 간 시도, 시도 목록이 비어 확인 불가 |
| `llm_pin_mismatch`, `llm_backend_mismatch`, `llm_log_incomplete` | 게이트웨이가 다른 slug를 고정, 백엔드 구성이 섞임, 게이트웨이 기록 불완전 |
| `llm_sampling_mismatch` | 게이트웨이가 실제로 넣은 샘플링(검증 마커 `sampling`)이 선언(temperature·sampling·seed)과 다름. local 회차에 값이 있어도 이 사유 |
| `tmpfs_measure_failed` | 구간 종료 뒤 tmpfs 사용량을 읽지 못함(측정 컨테이너 실행·권한 실패, 커널 파일시스템 종류가 tmpfs 아님, 형식·크기가 맞지 않는 보고, 두 볼륨이 정확히 오지 않음). 결과의 `tmpfs_usage`는 `not_measured` |
| `pre_tmpfs_measure_failed` | 구간 전 tmpfs 마운트 ID·사용량을 읽지 못함(사유는 `tmpfs_measure_failed`와 같음). 구간을 시작하지 않는다 |
| `tmpfs_mount_changed` | 구간 뒤 잰 tmpfs의 마운트 ID가 구간 전과 다름 — 같은 경로라도 다른 마운트(부모 경로 변경·symlink·다시 마운트)를 잰 것이라 회차 저장소의 근거가 아니다. 양쪽 값은 결과에 남는다 |
| `tmpfs_full` | 엔진 DB·엔진 데이터 tmpfs 중 하나라도 남은 공간이 `tmpfs.min_free` 미만(상한 도달 — 엔진이 쓰기 실패를 삼켰을 수 있음). 잰 값과 적용 하한(`min_free_bytes`)은 결과에 남는다 |
| `findings_failed` | findings 단계 실패: 로그인 실패(연결·HTTP 상태·응답 형식), 페이지 수집 실패(한도 초과·건수 불일치·형식 밖 응답), 추출 실패 |
| `evidence_write_failed` | 근거 파일 쓰기 실패 |
| `down_failed`, `sign_failed` | 정리·서명 실패(측정은 끝났어도 무효) |
| `record_build_failed`, `record_sign_failed` | v1.1 러너 기록(격리 근거·해결 이벤트·증명) 작성 실패, 그 서명·저장 실패 |
| `interrupted`, `unexpected_error` | Ctrl-C(정리·기록 후 다시 올림), 예상 밖 예외(정리·기록함) |

`llm-summary.json`에는 LLM 사유가 전부 남고, 결과 기록의 사유는 그중 정렬 첫째다.

## 아직 안 된 것

- **개선 후 판정 레코드:** findings만 수집한다. 구조화 판정 레코드는 물리 형식(#18) 결정 후 추가 신호로 붙인다.
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

## 스모크 실측 (11/03, 개발 머신)

환경: WSL2 Ubuntu 24.04, Ubuntu `docker.io` 29.1.3(Docker Desktop 통합 끔) + `docker-compose-v2` 2.40.3, local 백엔드 + 호스트
고정 IP(docker0 `172.17.0.1`)의 가짜 OpenAI 호환 LLM(정해진 짧은 응답, 외부 송신 없음), ours 엔진(웹 UI 미내장 빌드), 시간 예산 90초.
평가 머신의 결과가 아니며, 평가 머신이 정해지면 같은 항목을 다시 확인한다.

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
- **api 백엔드 회차 1회(11/03, 서명됨):** 모델 `qwen/qwen3.6-35b-a3b`, 고정 제공자 slug `deepinfra`(기대 표시 이름 `DeepInfra`),
  fallback 끔, 개선 후 엔진(dev 9b06c05 빌드, 정책 모드 external), 예산 240초·요청 100건. 엔진이 요청 예산 100건을 약 2분 만에 써서
  그때 봉인했다(계측기 전달 100 = `total_requests`, 봉인 뒤 거부 0, drain 시 진행 0). LLM 게이트웨이는 응답 48건이 모두 generation
  ID를 가졌고 봉인 뒤 엔진의 LLM 요청 12건을 503으로 거부했다. generation 조회 48건 전부 검증: 응답 제공자 `DeepInfra`,
  `provider_responses`는 generation마다 1건(엔드포인트 ID 1종, 상태 200×45·499×3 — 499는 봉인으로 끊긴 요청), fallback 시도 0.
  부정 연결 13개 경로·구간 감시(호스트 리슨 표본 62, 최대 공백 2.0초, 데몬 이벤트 경계 변경 0) 통과. 구간 안 신규 해결 3건이
  `isolated_window`로 귀속됐고 findings 1건, 채점은 판정 미수집이라 코어 GT 9개 전부 판단불가. API 키는 비밀 env 파일로만 넘겼다.
- **tmpfs 사용량 측정(11/03):** local 백엔드 완료·서명 회차에서 구간 종료 뒤 `df -P -k`로 엔진 DB tmpfs 1 GiB 중 약 50 MiB,
  엔진 데이터 tmpfs 512 MiB 중 약 0.2 MiB 사용(남은 공간 하한 8 MiB 통과). 두 이미지(엔진: GNU coreutils, postgres:16-alpine:
  busybox) 모두 같은 `df -P -k` 머리줄을 냈고, 측정 exec는 구간 밖이라 구간 경계 변경 0.
- **스모크로 고친 것:** `python3 -I` 실행 시 같은 디렉터리 모듈 import 실패, 배포판 데몬의 빈 `Platform.Name`을 출력 오류로 거부,
  internal 망 DNS 실패 종류, 무효 기록의 `failed_step`이 마지막 단계(근거 쓰기)로 남던 것.
- **이 머신 한정 설정:** 호스트 리슨 허용 목록에 WSL DNS 터널링 리졸버 `10.255.255.254:53`(근거 포함). 엔진 이미지는 호스트에
  node가 없어 웹 UI 없이 빌드했다(baseline·ours 동일, 평가는 API만 쓴다).
