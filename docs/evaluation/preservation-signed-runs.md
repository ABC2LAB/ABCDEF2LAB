# 탐색 입구와 서명된 보존 원자료

이 문서는 개발·브라우저 수용시험의 재계산 경로를 설명한다. 기준은 [브라우저 수용시험 §6·§7](browser-acceptance.md#6-3단계-보존-시험)이며 공식 비교의 분모·귀속·v1.1 기록 계약은 바꾸지 않는다. 고객 회차나 제품 보고서에 원본 ARTEX 비교를 넣지 않는다.

## 현재 구현과 결과

- 계측기는 실제 리슨에 따라 요청 원자료와 경로 행에 `ingress=main|explore`를 남긴다. `main`은 원본 직접 입구 또는 E② 진단 입구, `explore`는 탐색 경계 뒤 입구다. 호출자의 헤더로 입구를 선택하지 않는다. 봉인 뒤 거부 행도 입구를 남긴다.
- 전체 요청 예산·도달 경로는 기존대로 두 입구를 합산한다. `meter-summary.json.forwarded_by_ingress`는 전체 전달 행의 입구별 수, 보존 요약 `ingress`는 요청 예산 안 전달 행의 입구별 수다. 봉인 뒤 거부는 전달 수에서 빠진다.
- 실제 탐색 분리 배치에서 E② 시도별 대조는 `main`만 받는다. 현행 공식·원본 직접·`e2-legacy/v1` 배치에 `forwarded_by_ingress.explore > 0`이 있으면 배치 위반으로 회차를 무효화하고 대조에서 조용히 빼지 않는다. `explore`의 AttemptID 부재는 정상이다. 탐색 입구에서 E② ID가 보이거나 입구가 미등록·혼합되면 추정으로 고치지 않고 거부한다. 입구 칸이 없던 과거 기록은 기존 main 의미로 읽되 새 서명 계약에는 쓰지 않는다.
- 원자료 서명·변조·조건 혼입·기존 판정 경계 시험은 합성 fixture로 실행한다. 합성 자료의 `execution_source=synthetic_fixture`는 실측으로 승격하지 않는다.
- 현행 Docker 평가 러너는 개선 후 배치를 `e2-legacy/v1`로 발행한다. 설정에 새 배치 이름을 적어 우회할 수 없다. 새 실행기는 실제 배치·망·허용 도구·마감 검증 뒤 공통 발행 함수를 사용할 수 있다.
- 탐색 경계 원자료(계약 [§4.1](../contracts/exploration-boundary.md#41-평가수용시험-회차의-경계-원자료-abc2labexplore-boundary-exportv1))를 러너가 경계 종료 뒤 검증해 sidecar에 hash·카운터로 결속한다(아래 "경계 원자료"). 지금 러너는 개선 후를 `e2-legacy/v1`로만 띄워서 실제 회차의 `signed_evidence.exploration_boundary`는 아직 `NOT_RUN / verified_boundary_export_unavailable`이다(러너의 탐색 분리 배치는 [개발 계획](../development-plan.md)의 공식 측정 전 필수). 탐색 차단 건수를 0으로 채우거나 E② 진단 차단·`repeat_denials`와 합치지 않는다. 실제 MCP fixture 연결도 제품 경계 검증이나 원본 모델 실측을 대신하지 않는다.

실제 모델·원본 엔진·승인 대상·제품 탐색 경계 근거를 갖춘 비교는 **NOT_RUN**이다. 서명은 발행자의 출처와 바이트 무결성 근거이며, 실제 모델 실행이나 네트워크 격리를 증명하는 대체물이 아니다.

## 별도 sidecar

`purpose=browser_acceptance` 설정에 선택 필드 `preservation`을 넣으면 `preservation-round.json`을 발행한다. 공식 설정에서는 거부하고, 필드를 생략하면 기존 동작이다. 형식은 `abc2lab.preservation-round/v2`이다(v1에 `exploration_boundary`를 더함. v1 기록은 읽되 경계 근거가 없는 것으로 본다. 한 묶음 안에서 v1과 v2를 섞지 않는다). `run-record.json`의 v1.1·기존 HMAC 방식과 공식 채점기는 그대로다.

```json
"preservation": {
  "format": "abc2lab.preservation-options/v1",
  "series_ref": "fixture-session-01",
  "comm_mode": "full_local",
  "write_mode": "approved_writes",
  "mcp_version": "0.0.84",
  "mcp_tools_sha256": "<허용 도구 목록의 64 hex 지문>",
  "browser_image_id": "sha256:<실제 브라우저 이미지 64 hex>",
  "exploration_policy_sha256": "<적용할 탐색 정책 64 hex>",
  "declared_model_budget_sha256": "<호출·토큰·USD 예산 선언의 64 hex>",
  "execution_source": "model_run"
}
```

위 예의 지문 표시는 실제 값으로 교체해야 한다. API 키·세션·비밀번호·원문 URL은 받지 않는다. `comm_mode`는 `full_local|external_allowed`, `write_mode`는 `read_only|approved_writes`, `execution_source`는 `synthetic_fixture|model_run`만 받는다. 모델 비용 예산의 지문은 선언의 결속이며 이 옵션 자체가 예산이나 망을 집행하지 않는다. 실제 한도 집행·전송 동의·제공자 검증은 기존 실행 경로의 책임이다.

발행 자료에는 회차·짝 순번·세션 참조, 엔진 커밋/이미지, 모델·제공자·샘플링 핀 지문, 대상 이미지/초기 상태 지문, 머신 지문, 러너의 폴링/마감/작업/worker/컨텍스트 조건 지문, 요청/시간 예산, 통신/쓰기 모드, MCP 버전/도구 지문/브라우저 이미지, 탐색/E② 정책 지문, 모델 예산 선언 지문, 종료 사유를 남긴다. 원본의 두 정책 지문은 `null`이다. 원본·개선 후의 엔진 커밋/이미지는 달라도 되지만 각 조건 안에서는 고정해야 한다. R-R은 계약대로 R-F 원본과 짝을 지으므로 원본의 쓰기 모드와 개선 후 읽기 모드는 다를 수 있다.

### v3(탐색 분리 배치)

탐색 분리 배치 회차는 `abc2lab.preservation-round/v3`로 발행한다([결정안 §3.3](exploration-split-decision.md)). 칸 이름은 `preservation_record.py`(`COMMON_FIELDS_V3`·`SIDE_FIELDS_V3`·`RUN_FIELDS_V3`·`PRODUCT_FIELDS_V3`)가 정본이고, 해당 없는 칸은 더미가 아니라 `null`이다.

- `common`(B·O 전체가 같아야 짝, 다르면 `preservation_conditions_mismatch`): `request_budget`·`time_budget_seconds`·`model_pin_sha256`·`target_image_id`·`initial_state_sha256`·`machine_sha256`·`runner_settings_sha256`(공통 러너 설정만)·`goal_common_sha256`·`comm_mode`·`mcp_version`·`mcp_raw_tools_sha256`(MCP 원 목록, 도구 면 측정).
- `side_conditions`(쪽 안에서만 같아야 함, 다르면 `preservation_layout_or_policy_mixed`):
  - 배치는 `layout`(B `artex-direct/v1`, O `exploration-split/v1`)이다.
  - 그 밖의 칸: `driver_sha256`·`driver_settings_sha256`·`write_mode`·`declared_model_budget_sha256`·`cli_surface_sha256`·`mcp_exposed_tools_sha256`.
  - `browser_image_id`·`exploration_policy_sha256`·`diagnosis_policy_sha256`는 B에서 `null`이고 O에서 필수다.
- `run`(대조하지 않는 근거): `stop_reason`·`first_limit`(러너 `first_limit` 결과 그대로)·`end_signal`(`status`·`elapsed_seconds`·`closing_reason`, `closing_reason`은 O만; 끝 신호가 없으면 `null`이고 `first_limit.engine_terminal_elapsed_seconds`와 같은 읽기여야 함)·`rate_limited`(429 건수, 모르면 `null`).
- `product`(O만, B는 `null`):
  - `source_sha`·`image_ids`(product·explore_browser·fixture)
  - `file_sha256`: installation·policy·api_launch·target_policy·runtime_conditions·control_limits의 원본 SHA. 회차마다 다를 수 있어 대조하지 않는다.
  - `split_conditions`(#651), `sampling_overwrites`(실행·준비 칸별)
  - `call_records`: 장부 예약 수 = 호출 기록 줄 수여야 한다.
- 경계 원자료 요약(`exploration_boundary`)은 O 필수, B `null`이다.
- 한 묶음 안에서 v2와 v3를 섞지 않는다(`preservation_series_mismatch`). v3 발행은 러너 연결(B·O) 뒤부터다.

초기 상태 지문(`initial_state_sha256`)은 회차 신원을 뺀 대상 시작 상태다. 점수판 관측이면 pre 해결 맵, opaque 관측이면 pre 관측 원문의 SHA-256과 크기로 만든다(`abc2lab.preservation-initial-state/v1`). 관측 정규화 봉투(`normalized.json`)의 hash는 run_id·대상/엔진 컨테이너 ID를 담아 회차마다 달라지므로 쓰지 않는다. 그 hash를 쓰던 때는 같은 상태에서 시작한 짝도 `preservation_conditions_mismatch`로 멈췄다(2026-11-16 VAmPI e2-legacy 파일럿). 그 전에 서명된 sidecar는 이 칸이 회차마다 달라 짝으로 묶을 수 없으므로 다시 돌린다.

원자료는 고정 파일 네 개(`engine-requests.jsonl`, `engine-paths.jsonl`, `engine-sealed.json`, `engine-drained.json`)의 정확한 바이트 SHA-256과 크기로 결속한다. 발행·읽기 모두 봉인/drain·순번·전달 수·경로/입구 대조를 확인한다. 러너는 post 단계에 공개 발행 조건만 보관하고, 기존 격리·모델·마감 검증과 `down` 성공 뒤에 원자료를 다시 읽어 최종 바이트의 hash를 계산한 다음 sidecar를 서명하며, 결과의 서명된 근거 목록에도 넣는다. 봉인·drain 뒤부터 `down`까지 생긴 `rejected_after_seal` 행도 최종 hash에 포함된다. 이 거부는 전달 수·도달 패턴을 늘리지 않으며, 기존 meter 요약은 측정 마감 시점의 관측값이다. 개인 근거 디렉터리(러너 소유·0700), 경로 가림 키와 별도 러너 키(근거 밖 보호 파일)를 유지한다.

새 배치 실행기 연동점은 `preservation_record.build_payload(...)`이다. `conditions.layout=exploration-split/v1`은 실제 검증한 호출자만 전달한다. 이 함수는 writer(계측기·경계)가 종료된 뒤 호출해야 하며 배치를 만들거나 브라우저를 켜지 않는다. `layout` 이름과 입구 대조만으로 실제 네트워크 격리를 증명하지 않는다. `exploration-split/v1`이면 경계 원자료 인자 `boundary={"directory", "task_id", "path_key_ref"}`가 필수이고(없으면 `preservation_boundary_missing`), 다른 배치에 주면 `preservation_boundary_unexpected`다.

## 경계 원자료

경계는 러너가 만든 근거 디렉터리(러너 uid·0700, 경계 컨테이너에만 붙임)에 `boundary-records.jsonl`과 봉인 표식 `boundary-sealed.json`을 쓴다(형식·쓰는 규칙은 계약 §4.1). 경계는 키를 갖지 않는다. 러너는 `down` 뒤 `scripts/eval/boundary_export.py`로 최종 바이트를 읽어 다음을 확인하고, 요약을 sidecar의 `exploration_boundary`에 넣어 기존 러너 키로 서명한다.

- 표식의 형식·가림 판, 행 파일 hash·바이트·행 수가 표식과 같다. 행은 닫힌 칸·어휘(차단 사유 18개, 입구, 목적지)만 받고 중복 키·NaN·중복 seq·다른 세션 행을 거부한다.
- 표식의 카운터는 믿지 않고 행에서 다시 센다: 승인 대상·외부 자원 전달(입구별), 응답 없는 승인 대상 전달, 차단(합계·사유별·입구별), 외부 호스트 종류 수, 인증 헤더 붙은 전달 수, 세션 마감 뒤 행 수. 차단 수는 E② 진단 차단·`repeat_denials`와 다른 칸이고 합치지 않는다.
- 결속: 표식 세션의 `task_id`가 러너가 만든 작업과 같고, 결속 7개의 hash(`binding_sha256`)를 서명한다. `path_key_ref`가 러너 경로 키와 계측기 경로 행의 키와 같다.
- 계측기 대조(수용시험 P03): (메서드, 가린 경로)별 건수로 맞춘다. 계측기에만 있는 탐색 입구 행이 하나라도 있으면 경계 미경유로 실패다. 경계에만 있는 승인 대상 전달 행 수는 경계가 응답을 못 받은 전달 수와 계측기 봉인 뒤 거부 수(경로 행이 없어 건수로만)의 합 이하여야 하고, 응답을 받은 행은 봉인 뒤 거부 수 이하여야 한다(같은 묶음의 응답 없는 행을 먼저 뺀다). 넘으면 실패다. 경계 기록 경로는 1024바이트에서 잘리므로 그보다 긴 경로는 대조에 실패한다(조용히 통과하지 않는다).

| 회차 상태 | 뜻 | 카운터 |
|---|---|---|
| `verified` | 원자료가 완결이고 계측기와 맞음 | 행에서 센 값 |
| `not_opened` | 경계 세션이 열리지 않음(탐색 입구 트래픽도 0이어야 함) | `null` |
| `truncated` | `evidence_max_bytes`에 닿아 뒤 행을 못 씀 | `null` |

원자료가 없거나(`boundary_export_missing`), 표식 없이 행만 있거나 경계가 스스로 멈췄거나 봉인 때 진행 중 요청이 남았거나(`boundary_export_incomplete`), 쓰기가 실패했거나(`boundary_sink_failed`), 구조가 틀렸거나(`boundary_export_invalid`), 작업·키가 다르거나(`boundary_binding_mismatch`·`boundary_path_key_mismatch`), 계측기와 안 맞으면(`boundary_meter_mismatch`) 회차를 무효로 한다(러너 사유 코드). 묶음의 `signed_evidence.exploration_boundary`는 회차별 상태·카운터·대조를 따로 싣고, 다음 하나다.

| 상태 / 사유 | 언제 |
|---|---|
| `NOT_RUN / verified_boundary_export_unavailable` | 개선 후가 탐색 분리 배치가 아니거나 v1 기록 |
| `NOT_RUN / boundary_session_not_opened` | 한 회차라도 세션이 열리지 않음 |
| `UNDECIDABLE / boundary_export_truncated` | 한 회차라도 잘림 |
| `PASS / boundary_export_verified` | 모든 회차 `verified` |

`PASS`는 탐색 입구 트래픽이 경계를 지났다는 장부 대조다. 네트워크 격리 증명이 아니다(격리는 배치 검사·B16 프로브). 판정(`assessment`)은 경계가 `PASS`가 아니면 그 사유로 `NOT_RUN`이다.

## 재계산

회차 순서와 정책·제외 규칙을 먼저 고정한다. 기존 `--baseline`, `--improved`에 같은 순서의 sidecar 디렉터리, 보호 러너 키, 프로파일을 함께 지정한다. 실제 경로는 저장소 밖 운영자 근거 위치다.

```bash
python3 -I -B scripts/eval/preservation.py \
  --baseline /private/eval/b1/meter --improved /private/eval/o1/meter \
  --baseline-record-dir /private/eval/b1 --improved-record-dir /private/eval/o1 \
  --budget 100 --profile R-P \
  --exclusions /private/eval/exclusions.json \
  --path-key-file /private/eval-keys/path.key \
  --improved-boundary-dir /private/eval/o1/boundary \
  --runner-key-file /private/eval-keys/runner.key
```

반복 회차는 각 `--baseline`/`--improved`와 `--*-record-dir`(경계 원자료가 있으면 `--improved-boundary-dir`도)를 같은 짝 순서로 반복한다. 경계 원자료 요약이 있는 기록은 그 디렉터리의 최종 파일로 요약을 다시 계산해 서명된 값과 같아야 한다(없으면 `preservation_boundary_files_missing`, 다르면 `preservation_boundary_mismatch`). 공개 대상·유료 모델 호출 없이 검증하려면 다음 합성 시험을 실행한다. 원자료나 runtime 로그를 터미널에 출력할 필요가 없다.

```bash
python3 -I -B -m unittest discover -s scripts/eval/tests -p test_preservation_record.py
python3 -I -B -m unittest discover -s scripts/eval/tests -p test_boundary_export.py
go test -race ./exploreboundary ./cmd/explore-boundary
python3 -I -B -m unittest discover -s scripts/eval/tests -p test_runner_evidence.py
go test -race ./cmd/eval-meter ./pathmask
```

새 인자 중 하나를 쓰면 모두 필수다. 검증기는 서명, 정확한 JSON 구조, 원자료 바이트, 마감 상태, 경로 키, 짝 순서/회차 중복, 세션, 각 조건의 엔진 핀, 모델·머신·초기 상태·예산·통신 모드·MCP 조건, 조건 안의 배치·정책 고정을 확인한다. 다른 조건이 섞이면 종료 코드 2이며 요약은 출력하지 않는다. 서명은 소문자 64 hex만 받고 비 ASCII·다른 타입도 traceback 없이 고정 validation 오류로 거부한다. 원본/개선 후 사이에서 달라야 하는 배치·정책과 R-R의 쓰기 모드는 각 조건 안에서 따로 고정한다.

서명 검증을 켠 출력만 `abc2lab.preservation-summary/v8`이고 기존 지표에 `signed_evidence`, `assessment`가 추가된다(v8은 `signed_evidence.exploration_boundary`에 검증된 경계 원자료의 회차별 카운터·대조를 담을 수 있다. v7은 늘 `NOT_RUN`이었다). 기존 원자료 전용 CLI는 `v6` 그대로다. v6는 새 서명/조건 검증 완료로 읽지 않는다. 실제 회차는 러너의 탐색 분리 배치 전까지 경계가 `NOT_RUN`이라 완료 판정도 `NOT_RUN`이다.

## 기존 기준과 남은 검증

분자는 실제 `sent=yes` 도달 패턴, 분모는 같은 키로 변환한 검토된 제외 규칙을 적용한 원본 패턴이다. `sent=no|uncertain`, 정적 자원 비용, 합집합/짝별 평균/SD·원본끼리 참고값·빈 분모 처리 규칙은 기존 집계기를 그대로 쓴다. 새 지표나 통과선은 만들지 않는다.

- R-P는 100회 관찰·보고이며 완료 기준으로 쓰지 않는다. 기존 0.90과의 참고 비교를 유지한다.
- R-F는 짝별 평균 0.90 이상, R-R은 GET/HEAD 분모의 짝별 평균 0.95 이상이다. 각 비교의 빈 분모를 뺀 유효 짝이 3개 미만이면 `UNDECIDABLE`이다. 합집합 비율로 대신 판정하지 않는다.
- R-F·R-R은 같은 세션의 **첫 complete·time_budget 원본 예비 회차** N0의 2배를 요청 예산으로 고정한다. `full_budget_from_pilots`는 이미 검증한 실제 `abc2lab-eval-runner-result-v1` 결과를 시간순으로 받는다. `meter.stop_reason`·`meter.forwarded_requests`에서 처음 유효한 값을 고르고, 무효 회차는 `discarded_pilots`에 `reasons`·`failed_step`·당시 stop_reason을 보존한다. 무효를 `time_budget` 폐기만으로 축약하지 않는다. 이 helper의 현재 호출처는 합성 회귀 시험뿐이며 러너·CLI에 자동 예비 회차 실행/수집 연결은 없다. 원자료/서명 검증이나 예비 회차 이력의 완결성을 대신하지 않는다. 예비 회차는 본 짝에 넣지 않는다. 이후 좋은 N0를 고르지 않는 규칙을 회귀 시험으로 고정한다.
- `series_ref`는 운영자의 세션 선언이며 자체로 실제 시간순 실행·세션 완결성을 증명하지 않는다. 실제 세션의 `B O O′ | O′ O B | B O O′` 순서, 예비 회차 이력, 정책 상한과 모델 비용 집행, 최초 모델 핀 재조회, 실제 격리·도구 동등성·원본 실행은 운영자/수용 실행기의 근거가 필요하다. sidecar의 조건 일치만으로 이를 완료 처리하지 않는다.

현재 시험은 합성 계산·변조 차단·입구 대조의 구현 회귀다. 실제 Qwen·원본 보존율 R-P/R-F/R-R 및 제품 경계 종단은 해당 환경과 별도 승인이 준비된 뒤 실행하며, 그전에는 `NOT_RUN`을 유지한다.
