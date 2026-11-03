# 평가 러너 운영 안내

`scripts/eval/runner.py`는 [평가 프로토콜](evaluation-protocol.md) §2의 전용 망 구성으로 baseline 또는 개선 후 엔진의 회차를
**한 번** 실행하고, 결과 기록(완료+서명 또는 무효+사유)을 정확히 하나 남긴다. 자동 재시도는 없다. 실패하면 사유를 남기고
끝나며, 다시 돌리면 새 `run_id`의 새 회차다. 실패 회차도 결과 기록이 남으므로 집계에서 건수와 사유를 보고한다.

> 상태: 실제 Docker 스모크 전이다. 아래 "아직 안 된 것"을 먼저 확인한다. 특히 findings 추출 경로가 연결되기 전에는
> 모든 회차가 `findings_extractor_unavailable`로 무효가 된다.

## 실행

```bash
python3 -I -B scripts/eval/runner.py --config /private/eval/runner.json --preflight /private/eval/preflight.json --side ours
```

- 종료 코드: `0` 완료(서명됨), `1` 무효(사유는 결과 기록), `2` 설정·사전검사 오류(회차를 만들지 않음).
- stdout은 `run_id 상태 사유…` 한 줄이다. docker의 진단(stderr)은 운영자 터미널로만 가고 러너 기록에는 남지 않는다.
- 회차 중간에 운영자 확인이 한 번 있다(작업 시작, 아래 순서 5). 그 밖에는 손대지 않는다.

## 준비물

| 항목 | 요구 |
|---|---|
| Docker | 서버 28.0.0 이상(§2), 평가 전용 호스트. Docker Desktop과 WSL 내장 dockerd가 소켓을 다투지 않게 하나만 쓴다 |
| 이미지 | baseline 엔진(`base/artex-import`를 깨끗한 작업 트리에서 빌드), 개선 후 엔진(빌드한 dev 커밋), 엔진 DB, 대상(사전검사 `pins.target_image`), 계측기(`deploy/eval/meter.Dockerfile`). 러너는 pull하지 않으며 설정의 내용 ID(`sha256:`)와 로컬 이미지가 같아야 한다 |
| 사전검사 선언 | [사전검사](preflight.md) 형식. 러너가 다시 검사한다 |
| 비밀 env 파일 | `deploy/eval/eval.secrets.env.example`을 복사해 레포 밖에 두고 채운다(관리자·DB 비밀번호, api 백엔드면 `EVAL_LLM_API_KEY`). 러너는 이 파일을 읽지 않고 compose가 직접 읽는다 |
| 서명 키 | `e2-audit-keygen`으로 엔진 감사 키와 **별도로** 발급, 0600, `runs_root` 밖 |
| `runs_root` | 러너 uid 소유·정확히 0700 디렉터리. 회차마다 그 아래에 새 디렉터리를 만든다 |
| CA 묶음 | api 백엔드에서 게이트웨이가 쓸 호스트의 TLS 신뢰 묶음 경로(읽기 전용 마운트) |
| LLM 고정 조건 | api: 고정할 제공자 전체 slug(사전검사 `llm.provider`)와 그 응답 표시 이름(설정 `expected_provider_name`)을 팀이 함께 정한다. local: 로컬 서버를 고정 IP에만 바인딩 |

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
| `engine.llm_provider`, `engine.llm_stream` | `openai`만(게이트웨이가 chat completions만 받음), 스트리밍 여부 |
| `engine_db.{user,name}`, `tmpfs.{engine_db,engine_data}`, `health.{interval,timeout,retries}` | 엔진 DB 계정 이름, 회차 메모리 크기 상한, DB healthcheck |
| `llm_gateway.generation_path` | api: generation 조회 경로(예: OpenRouter `/api/v1/generation`) |
| `llm_gateway.verify_attempts`, `verify_interval_seconds` | generation마다 조회 횟수(1~100)와 간격 |
| `llm_gateway.ca_bundle`, `expected_provider_name` | 호스트 CA 묶음 경로, 기대 응답 제공자 표시 이름 |
| `docker_timeout_seconds`, `probe_timeout_ms` | docker 호출 하나와 부정 연결 점검 하나의 시한 |
| `host_listen_allowlist` | loopback 밖 호스트 리슨 중 허용할 것. `{"address", "port", "reason"}`, 특정 주소만(0.0.0.0·`::`·loopback 불가), 근거 필수. 기본은 빈 목록 |
| `run.*` | compose 대기, down 시한, 대상 준비, 예산 폴링 간격, drain 시한, 스냅샷 시한·크기, LLM 검증 대기(초) |

예: WSL DNS 터널링 주소를 허용해야 하는 개발 머신이라면 근거와 함께 적는다.

```json
"host_listen_allowlist": [
  {"address": "10.255.255.254", "port": 53,
   "reason": "WSL DNS 터널링 리졸버(lo /32). 대상·관측 경로 아님. 게이트웨이 도입 후 재검토"}
]
```

## 회차 순서

```
[up 전]  project 이름 중복 없음 → 이미지 내용 ID 확인 → 회차 디렉터리(0700) 생성
[기동]   compose up(--pull never, --no-build, --wait)
[pre]    망·publish·이미지·호스트 조건 점검 → 부정 연결 점검 → 대상 준비 → pre 스코어보드
         계측기 전달 건수 == 0
[작업]   운영자가 엔진 UI(127.0.0.1:ports.engine_api_host)에서 작업 시작 → "start" 입력
         요청 예산 또는 시간 예산까지 폴링 → 계측기 seal → drain 확인
         LLM 게이트웨이 seal(새 LLM 요청 거부·진행 중 취소 → generation 조회 시작)
[post]   점검 반복 + pre와 비교(망·주소·컨테이너 교체·재시작) → 부정 연결 점검 → post 스코어보드
         관측 로그 건수 == 러너 호출 수, 계측기 순번 완결·건수 일치
         LLM 검증 마커 대기 → 응답 제공자 판정 → findings
[끝]     compose down -v(성공·실패와 무관) → 모두 통과면 서명 → runner-result.json
```

## 회차 디렉터리

```
<runs_root>/<run_id>/            0700, 러너만
  meter/                         계측기·관측 front 기록(계측기 컨테이너에만 마운트)
  llm/                           LLM 게이트웨이 기록(게이트웨이에만 마운트, 계측기와 분리)
  pre/  post/                    스코어보드 원본·정규화본(어느 컨테이너에도 마운트하지 않음, 절대규칙 7)
  pre-checks.json  post-checks.json   망 스냅샷·policy_sha256·위반 목록
  pre-probe.json   post-probe.json    경로별 기대·실제·판정
  meter-summary.json  llm-summary.json  findings.json
  runner-result.json             완료(+서명) 또는 무효(+사유·실패 단계)
```

근거 파일은 O_EXCL·0600으로 한 번만 쓴다. 무효 기록에는 측정값·근거 목록·서명을 넣지 않는다.

## 사유 코드

| 코드 | 뜻 |
|---|---|
| `project_exists` | 같은 project가 이미 있음(이 러너 것이 아니므로 up·down 모두 안 함) |
| `project_check_failed`, `image_id_failed`, `image_id_mismatch` | 사전 조회 실패, 로컬 이미지가 설정의 내용 ID와 다름 |
| `compose_up_failed` | 기동 실패(down은 실행) |
| `{pre,post}_facts_failed`, `{pre,post}_host_listeners_failed` | docker·/proc 사실 수집 실패 |
| `{pre,post}_topology_violation` | 망·구성원·publish·이미지가 기대 구성과 다름 |
| `{pre,post}_host_condition_violation` | 다른 컨테이너의 loopback 밖 publish 또는 허용 안 된 호스트 리슨 |
| `{pre,post}_probe_invalid`, `{pre,post}_probe_violation` | 부정 연결 점검 무효(양성 대조 실패·실패 종류 불일치·probe 오류) 또는 위반(막혀야 할 경로가 열림). 경로별 내용은 `*-probe.json` |
| `target_not_ready`, `pre_scoreboard_failed`, `post_scoreboard_failed` | 관측 front로 대상 준비·스코어보드 수집 실패 |
| `pre_requests_nonzero` | 작업 시작 전에 엔진이 대상으로 보냄 |
| `start_not_confirmed` | 운영자가 `start`를 입력하지 않음 |
| `seal_failed`, `drain_timeout`, `drain_incomplete` | 계측기 seal 실패, drain 시한 초과, 진행 중 요청·기록 불완전 |
| `boundary_changed` | pre와 post 사이 데몬·망·주소·컨테이너 교체·재시작 |
| `observer_count_mismatch` | 관측 로그 건수가 러너 호출 수와 다르거나 거부 기록이 있음 |
| `meter_read_failed`, `meter_log_inconsistent` | 계측기 기록 읽기 실패, 순번 누락·건수 불일치 |
| `llm_seal_failed`, `llm_read_failed`, `llm_unverified`, `llm_marker_invalid` | 게이트웨이 seal 실패, 기록 읽기 실패, 검증 마커 시한 초과, 마커 형식 오류 |
| `llm_generation_unverified`, `llm_generation_unverifiable` | 조회 안 된 generation, generation ID 없는 응답 |
| `llm_provider_mismatch`, `llm_fallback_attempted`, `llm_fallback_unverifiable` | 응답 제공자가 기대와 다름, 응답한 제공자와 다른 제공자로 간 시도, 시도 목록이 비어 확인 불가 |
| `llm_pin_mismatch`, `llm_backend_mismatch`, `llm_log_incomplete` | 게이트웨이가 다른 slug를 고정, 백엔드 구성이 섞임, 게이트웨이 기록 불완전 |
| `findings_extractor_unavailable`, `findings_failed` | findings 추출 경로 미연결(현재 항상), 수집 실패 |
| `evidence_write_failed` | 근거 파일 쓰기 실패 |
| `down_failed`, `sign_failed` | 정리·서명 실패(측정은 끝났어도 무효) |
| `interrupted`, `unexpected_error` | Ctrl-C(정리·기록 후 다시 올림), 예상 밖 예외(정리·기록함) |

`llm-summary.json`에는 LLM 사유가 전부 남고, 결과 기록의 사유는 그중 정렬 첫째다.

## 아직 안 된 것

- **findings 추출:** 원본 추출기는 upstream SHA로 고정돼 있고 개선 후 엔진용 추출기는 없다. 연결 전에는 모든 회차가 무효다(5e).
- **작업 시작:** 운영자가 엔진 UI에서 작업을 손으로 만든다. 러너가 엔진 API로 작업을 만들고 작업 내용의 해시를 기록하는 방식으로 바꿀 예정이다.
- **tmpfs 사용량:** 회차 메모리(엔진 DB·엔진 데이터)의 seal 시점 사용량을 아직 재지 않는다.
- **실측으로 확인할 것(스모크):** 부정 연결 점검의 실패 종류(Docker가 DROP인지 REJECT인지, 내장 DNS가 바깥 이름에 어떻게 답하는지), probe가 엔진의 /etc/hosts를 보는지, `docker run sha256:<id>` 동작, internal 망의 호스트 쪽 게이트웨이 주소 유무, OpenRouter generation 조회의 `provider_responses`·엔드포인트 ID 형태, 재시작·엔진 망 부착 고장 주입, probe 전후 계측기 건수 불변.
