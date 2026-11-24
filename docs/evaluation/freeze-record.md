# 공식 측정 동결 기록 (틀)

공식 측정에 쓰는 고정값을 한 곳에서 보게 모은 기록이다. **값의 정본은 각 줄의 '정본' 칸에 적은 문서다.** 이 문서는 그 값을 옮겨 적고, 동결 날(공식 세션 전날) 실제 값(코드 SHA·이미지 digest·지문)을 채워 동결한다. 재조회 단가처럼 공식 세션이 시작해야 정해지는 값은 그때 채운다. 정본과 이 문서가 다르면 정본이 맞고, 이 문서를 고친다.

- 상태: **중간 기록: 공식 측정 12/9로 연기, 12/8 다시 동결**(2026-11-23 사용자 결정). 이 문서는 동결되지 않았다. 공식 측정은 엔진 개선까지 끝낸 최종 제품으로 2026-12-09에 하고(제공자 고정은 그대로), 2026-12-08에 그때의 dev로 다시 동결하고 Juice Shop 파일럿을 다시 돈다. 아래의 코드 SHA·이미지 digest·지문·설정 sha256과 두 파일럿의 값은 **11/23 기준 이력**이고, 12/8 동결 때 새 값으로 다시 채운다. 남은 `<채움>` 칸은 공식 세션이 시작해야 정해지는 값이라 그대로 둔다.
- 파일럿은 두 번 돌았다. **Go 3 파일럿(VAmPI)** = 2026-11-22 밤, 대상 VAmPI, 로컬 통합본 빌드. **Juice Shop 파일럿** = 2026-11-23 오전, 공식 대상(Juice Shop)·동결 SHA로 다시 빌드한 이미지·공식과 같은 설정(다른 칸은 `runs_root`·`preservation.series_ref`뿐)·같은 시작 명령(`run.sh`, 공식은 `MODE=official`만 다름). 공식 집계에는 공식 세션 폴더의 회차만 넣고, 두 파일럿의 회차는 넣지 않는다.
- 동결 절차(번호는 실행 절차 §0 "동결과 파일럿의 순서"와 같다. ①~④는 공식 세션 전날, 이번에는 12/8): ① 그날의 dev 머리를 동결 SHA로 고정(이 뒤로 공식 세션이 끝날 때까지 dev 머지 없음) → ② 동결 SHA로 이미지 빌드·설정 준비, 결과를 보기 전에 고정할 입력 만들기 → ③ Juice Shop 파일럿 한 쌍(장비 검증용, 공식과 같은 이미지·설정·시작 명령) → ④ 동결 기록·선언: 이 문서의 칸을 실제 값으로 채우고 정본 문서와 한 줄씩 대조한 뒤, 사용자 확인을 받아 상태를 '동결'로 바꾸고 커밋 SHA를 공식 결과 문서에 적는다 → ⑤ 공식 세션(다음 날, 12/9). 순서의 정본은 실행 절차 §0이다(개발 계획 v4.3과 함께 고친다).
- **파일럿 셋의 구분**: Go 3 파일럿(VAmPI, 11/22)과 Juice Shop 파일럿(11/23)은 `dd3385a2` 기준의 이력이다. 12/9 공식 세션의 근거가 되는 파일럿은 **12/8에 최종 이미지로 도는 Juice Shop 파일럿**이고, 그 값은 12/8에 이 문서에 따로 적는다.
- 동결 뒤 값이 바뀌면(제공자 단가 변동 등) 이 문서에 새 줄로 남기고, 이미 낸 결과를 소급해 고치지 않는다(평가 프로토콜 §5.1).

정본 문서 줄임말: **프로토콜** = `evaluation-protocol.md`, **고정 초안** = `official-protocol-fixation-draft.md`, **실행 절차** = `official-run.md`.

## 1. 모델·제공자

| 항목 | 고정값 | 정본 | 동결 때 채울 것 |
|---|---|---|---|
| 모델 | `qwen/qwen3.6-35b-a3b` | 프로토콜 §6 | 엔드포인트 재조회 근거: 세션 기록 `provider_price`·`price_caps`. Go 3 파일럿(VAmPI): `/home/sbfmt/abc2lab-eval/pilot-20261122/session-20261122t160607z/session-log.jsonl`. Juice Shop 파일럿: `/home/sbfmt/abc2lab-eval/jspilot-20261123/session-20261123t013125z/session-log.jsonl`. 공식 세션 `<채움>` |
| 제공자 | `primeintellect/fp8` (PrimeIntellect), fallback 끔 | 프로토콜 §6 | Go 3 파일럿(VAmPI): 2026-11-22 16:06Z, `provider_price` 조회 200, 단가 0.19 / 1.70. Juice Shop 파일럿: 2026-11-23 01:31:26Z, 조회 200, tag `primeintellect/fp8`, 단가 0.19 / 1.70. 공식 세션 `<채움>` |
| 제공자 조건(O 요청) | ZDR 켬, `data_collection=deny`, `require_parameters` | `docs/openrouter-first-diagnosis.md` | — |
| 고정 샘플링 | `temperature=1.0`, `top_p=0.95`, `top_k=20`, `min_p=0.0`, `presence_penalty=1.5`, `repetition_penalty=1.0`, `seed=42` | 프로토콜 §6 | — |
| 샘플링을 넣는 곳 | B: 평가 게이트웨이, O: 승인 manifest `sampling` (엔진은 넣지 않음) | 프로토콜 §6, 실행 절차 §4 | 공식 세션 회차별 적용값 대조 결과 `<채움>` |

## 2. O 승인 manifest 값

| 항목 | 고정값 | 정본 | 동결 때 채울 것 |
|---|---|---|---|
| 가격 상한(입력/출력, USD per 1M) | 규칙: 세션 시작 직전 재조회 단가 × 1.5(올림), 세션 안 고정(`--resume`도 시작 때 값). 이어 돌릴 때의 시작 조건: 상한 ≥ 지금 단가 × 1.2(`provider_price_headroom`, 새 세션은 × 1.5라 늘 통과). 고정 숫자가 아니다. 예: 2026-11-22 단가 0.19 / 1.70 → `285000` / `2550000` | 실행 절차 §4, `official_session.py --provider-probe` | Go 3 파일럿(VAmPI): `price_caps` 285000 / 2550000(규칙 × 1.5, 여유 1.2), O 회차 `o_manifest_caps`도 같음. Juice Shop 파일럿: `price_caps` 285000 / 2550000(`source` rule, × 1.5, `headroom_ok`), O 회차 `o_manifest_caps` 같음. 공식 세션 `<채움>`(시작 직전 재조회 단가로 다시 정한다) |
| 모델 호출 하나의 시간 상한 | 170초 (`timeout_seconds`) | 실행 절차 §4 | — |
| 연속 일시 오류 상한 | 8 (`max_consecutive_transient_errors`) | 실행 절차 §4 | — |
| 연속 상한 뒤 동작 | 60초 쉬고 시험 호출 1번, 처음 쉰 때부터 5분 넘으면 잠금 | `llmpolicy/openrouter_cooldown.go` | #858, dev 머지 커밋 `dd3385a2`(2026-11-22 16:07Z) |
| 호출당 입력·출력 토큰 상한, 요청 수·비용 상한 | 실행 절차 §4 예시 값 | 실행 절차 §4 | 공식 세션 manifest의 값 `<채움>` |

## 3. 엔진·회차 상한

| 항목 | 고정값 | 정본 | 동결 때 채울 것 |
|---|---|---|---|
| 회차 요청·시간 예산 | 고정 초안 §10의 값 | 고정 초안 §10, 실행 절차 §2 | 요청·시간 예산은 러너 설정이 아니라 사전검사 선언·회차 조건에 있다. Go 3 파일럿(VAmPI): 요청 300·시간 1500초. Juice Shop 파일럿과 공식 세션: 요청 300·시간 1500초(`preflight.official.json`의 B·O `request_budget`·`time_budget_seconds`, 프로파일 `official-split-35b.json` `environment.request_budget` 300). |
| 먼저 걸리길 의도한 상한 | 고정 초안 §1 | 고정 초안 §1 | — |
| Planner 계획 한 번의 시간 상한 | 170초 | `agent/e2_http.go` | #858, dev 머지 커밋 `dd3385a2` |
| 준비 단계 전체 상한(모델 결속·목표 만들기·첫 계획) | 400초 (`FirstDiagnosisPreparationTimeout` = 목표 호출 170초 + 계획 170초 + 여유 60초, 2분에서 올림 — 2026-11-23 화면 VAmPI에서 외부 모델 계획 단계 100.5초·2분 초과 실패 2번) | `server/first_diagnosis_supervisor.go` | 이 값을 넣은 PR의 dev 머지 커밋 `<채움>` |
| Planner heartbeat | 600초 (원본 값) | `server/engine.go` | — |

## 4. 무효 판정 기준

| 항목 | 고정값 | 정본 |
|---|---|---|
| 제공자 멈춤 무효(`provider_fault_stall`) | 장애(429·위쪽 끊김) 뒤 180초 동안 활동 없음 | 고정 초안 §2·§19 |
| 활동으로 세는 것 | 요청 시작과 응답 완료(오류 포함). 모델 호출 행과 대상 요청 행 모두. 진행 중인 요청 자체와 스트리밍 조각은 활동이 아니다 | `scripts/eval/runner_fault.py` |
| 멈춤 사건의 진행 중 모델 호출 | 사건마다 `inflight_model_calls_at_fault`, 세션 요약 `fault_stalls_inflight_model_call`(쪽별). 판정에는 쓰지 않고 공정성 기록에만 쓴다 | `scripts/eval/runner_fault.py`(v3), 세션 기록 |
| 일찍 끝난 회차 | 고정 초안 §26 | 고정 초안 §26, 프로토콜 |
| 쪽별 무효 상한 | 고정 초안 §3·§16 | 고정 초안 §3 |

## 5. 운영

| 항목 | 고정값 | 정본 | 동결 때 채울 것 |
|---|---|---|---|
| 측정 시간대 | 공식 세션: KST 14~18시에 시작. 파일럿: 시간대 제한 없음(제공자 429 점검만 켬) | 실행 절차 §0 | Go 3 파일럿(VAmPI) 2026-11-22 16:06:07Z(KST 11/23 01:06). Juice Shop 파일럿: `run.sh` 시작 2026-11-23 01:31:25Z, 429 점검 통과 뒤 B 시작 02:32:23Z(KST 11:32), 세션 끝 02:53:23Z. 공식 세션 `<채움>` |
| 시작 전 점검 | 포트, 메모리(Linux available 3GB 이상 + Windows 커밋 여유 5GB 이상), 제공자 429 점검 호출, 가격 상한 ≥ 지금 단가 × 1.2 | 실행 절차 §0·§5 | 세션 기록 `$SESSION/session-log.jsonl`의 사건 `start_window`·`provider_price`·`price_caps`·`usage_base`·`provider_probe`(·`provider_probe_wait`)·`ports_busy`·`heavy_wait`·`stopped_before_run`, 회차마다 `o_manifest_caps`. Go 3 파일럿(VAmPI) 세션 경로 `/home/sbfmt/abc2lab-eval/pilot-20261122/session-20261122t160607z/session-log.jsonl`(429 점검 10번 중 429 0번, 회차 중 최저 여유 Linux 4.6GB·Windows 9.8GB). Juice Shop 파일럿 세션 경로 `/home/sbfmt/abc2lab-eval/jspilot-20261123/session-20261123t013125z/session-log.jsonl`: 429 점검 세 번 — 01:31:40Z 10번 중 429 1번 → 30분 기다림 → 02:02:09Z 429 1번 → 30분 기다림 → 02:32:21Z 429 0번으로 통과(`provider_probe` 3줄·`provider_probe_wait` 2줄). 공식 세션 경로 `<채움>` |
| 측정 머신 | 고정 초안 §25 | 고정 초안 §25 | 공식 설정(`runner.official.json`)으로 2026-11-23 env_check 18/18 통과(러너 보고). 공식 세션 시작 때 결과 `<채움>` |
| 라운드 순서·반복 수 | 고정 초안 §11 | 고정 초안 §11 | — |

## 6. B와 O의 선언된 차이

같은 조건이 아닌 것을 숨기지 않고 적는다. 결과 문서는 이 표를 그대로 싣는다.

| 차이 | B (원본) | O (개선 후) | 결과에 미칠 수 있는 영향 |
|---|---|---|---|
| 모델 호출 방식 | 스트리밍 | 비스트리밍 | 긴 응답에서 O는 응답 전체가 올 때까지 활동 기록이 없다 |
| 모델 호출 시간 상한 | 없음 | 170초 | 장애 뒤 B의 긴 호출 하나만 180초 넘게 진행 중이면 B만 멈춤 무효가 날 수 있다. 멈춤 사건마다 진행 중 모델 호출 수(`fault-stall.json` v3 `inflight_model_calls_at_fault`)를 남기고, 결과 문서의 공정성 칸에 세션 기록 요약 `fault_stalls_inflight_model_call`(쪽별)을 싣는다. 판정·쪽별 2회 상한 셈은 바뀌지 않는다 |
| 429·일시 오류 처리 | SDK 재시도(0.5·1·2초, 3회) + 같은 제공자 재시도 2회, 영구 잠금 없음 | 2·6·15·30초 재시도, 연속 8번이면 60초씩 쉬었다 시험 호출, 5분 뒤 잠금 | 같은 429 구간에서 포기하는 시점이 다르다. 무효 사유를 쪽별로 따로 센다 |
| 가격 상한 | 없음(제공자 이름 고정만) | manifest 가격 상한(세션 시작 단가 × 1.5) | 세션 시작 때 상한 = 단가 × 1.5로 여유를 둔다. 이어 돌릴 때(`--resume`)는 상한 ≥ 지금 단가 × 1.2를 확인한다. 세션 도중 단가가 상한을 넘으면 O만 404로 끝난다 |
| 샘플링 적용 위치 | 게이트웨이 | manifest | 적용값은 회차마다 대조한다 |
| 분리 배치·브라우저 MCP 등 | 고정 초안 §8·§18 | 고정 초안 §8·§18 | 고정 초안 참조 |

## 6.1 화면 관찰 규칙과 파일럿 관찰 결과

- 규칙(세 O 회차 같은 설정): 관찰용 로그인은 O 회차마다 그 회차의 로그인 파일로 1번만 하고, 같은 회차 안에서는 다시 하지 않는다(로그인 실패 제한이 러너와 같은 주소로 잡혀 러너가 막힐 수 있다). 러너는 O 회차마다 새 관리자 비밀번호로 관찰 로그인 파일을 새로 쓰므로 회차마다 새 로그인이 공식 절차다. 로그인 실패·브라우저 종료·로그아웃이 있어도 회차는 유효하고 `observer_failed`만 남기며, 그 회차만 관찰을 접는다.
- Go 3 파일럿(VAmPI): 로그인 성공, 승인 요청 0. 다만 `observer_failed: run_not_opened`였다. 러너 O 회차가 작업대 URL 진단 목록에 보이지 않아 '이 점검 열기'를 하지 못했고, 관찰 화면은 빈 대시보드였다. 회차 결과에는 영향이 없다.
- **공식 O는 화면 관찰을 끈다(2026-11-23 사용자 결정).** 관찰은 회차 조건이 아니고, 러너 회차가 작업대에 뜨지 않아 켜도 볼 수 없다. 끄는 방법: `run.sh`를 `OBSERVE=0`으로 띄워 `observe.py`를 실행하지 않고, 러너 설정에 `split.observer_password_file` 칸을 두지 않는다(세 O 회차 같은 설정). 위 규칙은 관찰을 켠 회차에만 해당한다. → 2026-11-24 사용자 결정으로 바뀜(아래 '확인과 공식 관찰 여부'). 11/24 VAmPI 6회차에서는 관찰된 O(O0·O2) 모두 러너 회차를 진행 화면에서 열었다(`opened_run` 참).
- **VAmPI 6회차(2026-11-24 밤, B O O B B O, dev `abf5efb8`)에서 관찰기 결함을 찾았다.** O0는 관찰했지만 회차 끝에 `observer_failed: logged_out`, O1은 관찰되지 않았고, O2는 정상 관찰(로그인·'이 점검 열기' 성공)이었다. 원인: 러너는 회차 끝에 제품을 내린(compose down) 뒤 관찰 로그인 파일을 지운다. `observe.py`는 '파일이 있는가'만 봐서, 내리는 동안의 로그인 화면을 로그아웃으로 잡았고, 바로 이어진 O1이 파일을 다시 써서 지워진 순간을 놓쳤다(O2는 앞에 B가 있어 틈이 길었다). 회차는 6/6 유효였지만 '세 O 모두 관찰하거나 모두 안 함'(2026-11-20) 규칙이 깨진다.
- **고침(2026-11-24 사용자 결정, 재시도가 아니라 회차마다 새 로그인)**: 회차를 로그인 파일 내용의 SHA-256(메모리에만)으로 가른다. 새 회차 파일이 보이면 새 브라우저에서 그 파일로 한 번 로그인한다. 로그인 화면이 보여도 180초 안에 파일이 지워지거나 바뀌거나 제품 화면이 닿지 않으면 회차 끝으로 보고 실패로 세지 않는다. 같은 파일·같은 제품에서 로그인 화면이 이어질 때만 `logged_out`이다. 다 쓰이지 않은 로그인 파일(아이디·비밀번호 두 줄이 없음)은 회차로 보지 않는다. 가짜 브라우저 시험 `test_observe.py` 4개(내림 중 로그인 화면 뒤 곧바로 다음 O·진짜 로그아웃·로그인 실패·내림 중 화면 닿지 않음) 통과, 고치기 전 판은 첫 시험에서 실패한다. 변이 5개 모두 잡혔다.
- **확인과 공식 관찰 여부(2026-11-24 사용자 결정)**: 고친 관찰기는 12/7 밤 VAmPI 6회차와 12/8 Juice Shop 파일럿에서 같이 확인한다. **12/7 밤에 O 세 번이 모두 관찰되는 것이 확인되지 않으면 공식 측정은 관찰 없이(세 O 모두 끔) 간다.** `run.sh`·`observe.py`는 12/8 Juice Shop 파일럿 뒤로 고치지 않는다.
- Juice Shop 파일럿(관찰 끔): O 회차 결과 `observer_login_file: false`, 세션 기록에 관찰 사건 0줄. `runner.json`·`runner.official.json` 둘 다 `split.observer_password_file` 칸이 없다.
- 러너 회차가 작업대에 뜨지 않는 문제는 측정 뒤 과제다.

## 6.2 정본 문서의 남은 문구

- 평가 프로토콜 §5.1 승인 메서드 줄 끝의 "분리 배치 프로파일과 절차가 준비될 때까지 보류"는 충족됐다. 근거: ① 프로파일 `docs/evaluation/profiles/official-split-35b.json`(`dd3385a2`)이 있고 `scope.methods`가 GET·HEAD·POST·PUT·PATCH다. ② 실행 절차 §4가 그 프로파일로 사전검사 선언을 만든다(`official_preflight.py`). ③ 2026-11-23 준비가 실제로 그 경로로 `preflight.official.json`을 만들었고, Juice Shop 파일럿이 그 선언으로 B·O를 끝까지 돌았다. 문구 정리는 측정 뒤에 한다.

## 6.3 보존율 입력 (결과를 보기 전에 고정)

| 항목 | 값 |
|---|---|
| 요청 예산 | 300 (`docs/evaluation/profiles/official-split-35b.json` `environment.request_budget`, `dd3385a2`) |
| 제외 규칙 파일 | `/home/sbfmt/abc2lab-eval/evidence/preservation-exclusions-official.json`(저장소 밖 운영자 근거 위치, 읽기 전용), sha256 `0d0db86127edb484cbddc24f436e628c083c3daa1724917d8f6f468bf39a39ae` |
| 제외 규칙 내용 | `methods`: CONNECT·DELETE·OPTIONS·TRACE(표준 메서드 중 프로파일 `scope.methods` 5개 밖 전부). `path_prefixes`: 빈 목록(공식 O 등록의 제외 경로가 비어 있다). 수용시험 문서(`browser-acceptance.md`)의 '전체 보존율' 규칙에서 기계적으로 만들었고 손으로 고르지 않았다. 도출 근거는 같은 폴더의 `.note.json` |
| 만든 시점 | Juice Shop 파일럿 결과를 보기 전. 그 뒤로 바꾸지 않는다 |
| 계산 방식(2026-11-23 사용자 결정) | 계측기 기록 기준, 서명 대조 미적용, 수용시험 통과선 미적용(값만 싣는다). 서명 sidecar는 보존한다. 공식 sidecar 서명 대조는 측정 뒤 과제다 |

## 6.4 Juice Shop 파일럿을 정한 경위와, 그 결과를 본 뒤 바뀐 것

- **누가·언제**: 사용자(최민준)가 2026-11-23 오전(약 01:2xZ)에 정했다. 이유: Go 3 파일럿이 VAmPI였기 때문에 공식 대상(Juice Shop)·동결 SHA로 다시 빌드한 이미지·채점 경로(주 지표·보존율)가 최종 코드로 한 번도 끝까지 돌지 않았다.
- **개발 계획과 다른 점**: 개발 계획(`development-plan.md`)은 "Juice Shop은 공식 측정에만 쓴다", "예비 측정은 VAmPI로 한다"(미리 돌리면 결과를 보고 고치는 일이 생길 수 있어서)고 적는다. 이 파일럿은 그 원칙의 예외다. 예외의 조건은 아래 목록으로 지킨다: 파일럿 결과를 본 뒤에는 이미지·설정 값·하네스·보존율 입력을 바꾸지 않았다. 2026-11-23 사용자 결정이다. 장비 검증용 한 쌍이고, 결과 세부는 보지 않으며 개발에 쓰지 않는다. 계획 문서 담당(이동찬)에게는 팀 단체 대화방으로 알렸다. 개발 계획의 원칙 문구는 "Juice Shop은 공식 측정과 그 직전 장비 검증 파일럿에만 쓴다. 파일럿 결과 세부는 개발에 쓰지 않는다"로 고친다(별도 PR).
- **파일럿 결과(02:53Z)를 보기 전에 정한 것**(회차 시작 02:32:23Z보다도 앞):
  - 보존율 제외 규칙 파일: 01:30:17Z에 만들고 읽기 전용으로 둠(`run.sh` 시작 01:31:25Z 전).
  - 공식 O 화면 관찰 끔: 같은 아침 지시. 파일럿 설정을 만들 때부터 관찰 칸이 없었다.
  - 결과 정리 도구 `4159df21`: 01:36:49Z 커밋(429 점검 대기 중). 어젯밤 VAmPI 쌍으로 확인한 집계 도구 수정(O 종료 사유를 회차 요약에서 읽음)이고 회차 동작과 상관없다.
  - 이미지 다시 빌드·공식 설정(`runner.official.json`·`preflight.official.json`): 파일럿 시작 전 준비에서 만들었다.
- **파일럿 결과를 본 뒤 바뀐 것**: 공식 세션 폴더(`/home/sbfmt/abc2lab-eval/official-20261123`)를 만든 것뿐이다(02:55Z, 러너 보고). 세션마다 새로 만드는 경로 세 칸(비밀 파일·manifest·경로 가림 키)만 다르고, 이미지·설정 값·하네스·보존율 입력은 그대로다.
- **이미지 재사용(정본과 다른 점)**: 실행 절차 §4의 1번은 세션 준비에서 이미지를 빌드한다고 적는다. 이번 공식 세션은 같은 동결 SHA(`dd3385a2`)로 Juice Shop 파일럿 준비 때 빌드한 이미지를 다시 빌드하지 않고 쓴다(2026-11-23 사용자 결정, 파일럿과 공식이 같은 이미지여야 파일럿이 공식 조건을 확인한 것이 된다). 러너가 회차마다 설정의 이미지 ID와 로컬 이미지를 대조해 다르면 `image_id_mismatch`로 멈춘다. 실행 절차 §4에 이 재사용을 적는다(별도 PR). 12/8에 다시 동결하면 이미지도 그때 다시 빌드하고, 그 파일럿과 12/9 공식 세션이 같은 이미지를 쓴다.

## 7. 코드와 이미지 (동결 때 채움)

| 항목 | 값 |
|---|---|
| O 코드 커밋(dev) | `dd3385a24c0eac762566bd98b3d74ba8c3ffa4ec`(tree `12a097c0f5ac9c5088e3944b37a38210c6792093`). Juice Shop 파일럿과 공식 세션은 이 커밋으로 다시 빌드한 이미지를 쓴다. Go 3 파일럿(VAmPI)은 #858 머지 직전에 시작해 로컬 통합본 `9208d7ab`(dev `ebb4b428` + #858 head `701f9945`)으로 돌았고, 통합본의 tree가 머지된 dev의 tree와 같다 |
| B 코드 커밋과 패치 | `e53a79fe4df0c95a5b09274de8db321fa038b00b` + 도구 판 고정 패치(실행 절차 §4) |
| 제품 이미지 digest (product / explore-browser / fixture) | **공식(= Juice Shop 파일럿, 동결 SHA로 다시 빌드, 태그 `dd3385a24c0e`, 베이스 이미지는 새로 받지 않음)**: product `sha256:4160e62df9bf1b48c2cbcffda59ed198f2efbb7fa8943c30a1fead29b42d217e`, explore-browser `sha256:fdbfeb12eef581a5a26b6529840fcadb19392dcd7df2e50ecd0ed0da17efd4a7`, fixture `sha256:b09a4d5f65f43d9b4b65feb66e00edd1df959b437ebde05f0a3ef914039d38de`. 공식 세션은 다시 빌드하지 않고 이 이미지를 쓴다(`runner.official.json`의 이미지 ID가 같다). Go 3 파일럿(VAmPI) 빌드(태그 `9208d7ab4bc7`): product `sha256:f71f7a09da462db9800e9d545d24027eec34d67fa9f3e51ba140be9b61e1b580`, explore-browser `sha256:99376edd939bcf8dcac47b1a1402ae18f2c1227a9724f2cb9cc93a9b2f47d745`, fixture `sha256:10b8fa3a3615bbf6fd786d9af79ef1590af14e42d3120e7c3641830ffe81e2b6`(tree가 같아도 이미지 ID는 빌드마다 다르다) |
| 평가 계측기 이미지 digest | **공식(= Juice Shop 파일럿)** `sha256:dd847985d591d49ccb6d7efe54075e116b9aae07609e24bd1c248d87a95b70eb`. Go 3 파일럿(VAmPI) 빌드 `sha256:9d24586f0659fef50d00d0598baf1a8a0ea9fee00ad237a1f2e1ef97b705045d` |
| 엔진·DB 이미지 digest | **공식(= Juice Shop 파일럿)**: 엔진 O `sha256:88bab64bfa4ff7ea6452ece6d0bd48f7136cdb2a80170cdd04e2eac4d51ee6b1`, 엔진 B(`e53a79fe`) `sha256:655a40ab71694f0755b5fc43faebb86f597a2c968c2eb3184cf134c65b2e6433`, `postgres:16-alpine` `sha256:f2f98726820d0342055351c6e9d36e705f5bfffb3dd53ff556325114b08afb6d` |
| 대상 이미지 digest | 공식(Juice Shop) `sha256:fe8cecfec60f7856a02b52072c879b1baf73f5a97faafdeb12ed9e8eaccc0c3d`. Juice Shop 파일럿도 이 이미지다. Go 3 파일럿은 VAmPI였다 |
| 러너 코드 커밋 | O 코드와 같은 dev 트리의 `scripts/eval`(`dd3385a2`). 결과 정리 도구(세션 집계의 멈춤 진행 호출 칸)는 브랜치 `feature/minjun-results-stall-inflight` `4159df21`이고(O 종료 사유를 회차 요약에서 읽는 수정 포함, 앞의 `651c7d07`을 대체) 머지는 측정 뒤다 |
| 프로파일 지문 | 공식 사전검사 선언 `preflight.official.json`(B·O 같은 값): `fixture_fingerprint` `6a1295565577609e52d2bd57e77da02612a8a5835677adaf87d4a5f7fe3b6386`, `environment_fingerprint` `0a0034cf629a9ae2a7ed3536b647e2aedf0e023cfab2f952863e9a71a83b5157`, `scope_fingerprint` `5262454e38abf5a2cb8a6145e71dbf2bd7217ccb274918780a7a71b7ff191c9e` |
| Juice Shop 파일럿 회차 ID(B·O) | B `baseline-20261123t023223z-6b930d63` complete. O `ours-20261123t024409z-ef526aa9` complete(E③ `disabled`/`declaration_missing`, `observer_login_file` false). 세션 complete, 시도·유효 B 1·O 1, 무효 0. 장비 검증용 한 쌍이라 **결과 세부(먼저 걸린 상한·소요 시간·회차 안 오류 수·사용액·채점·보존율 값)는 이 문서에 옮기지 않고 개발에 쓰지 않는다**(2026-11-23 사용자 결정). 회차 목적은 공식과 같은 `official`이고 회차 폴더만 `jspilot-20261123`이다(채점기가 `wiring_check` 목적을 받지 않아서, 사용자 승인) |
| Go 3 파일럿(VAmPI) 회차 ID(B·O) | B `baseline-20261122t160622z-80101419` complete(first_limit `runner_request_budget`, 429 3번, 멈춤 0). O `ours-20261122t161713z-88e7f6a7` complete(first_limit `runner_time_budget` 1500초, 429 4번, 멈춤 0, E③ `disabled`/`declaration_missing`). 세션 complete, 시도 B 1·O 1. 사용액 약 $1.115(팀 공용 키라 그 사이 다른 호출이 있었으면 포함) |
| 하네스 sha256 | `run.sh` `7e249f0b2816d5f198d0422510e8e2dcc1029aba40700755e19d61eac1f97966`, `observe.py` `7e40a845d341121099a8a5eca46d502ba069edd6bd45cacde12917877474c5c3`, `heavy_gate.sh` `89e816d8e153da9d3a0ff04a3bb98e48b6b44b8a5dbf3398107390309155a8ba`. Go 3 파일럿(VAmPI) 뒤로 고치지 않았고(Juice Shop 파일럿 뒤 다시 확인, 같은 값), 공식 세션은 `MODE=official`만 다르다. **2026-11-24 관찰기 수정 뒤(§6.1)**: `run.sh` `27ddbb4ed0f391330fed75c2789c3e27b692d8fdd16e12fa3c24d0dfe7be0148`(머리말만 바뀜: 12/8 Juice Shop 파일럿 뒤로 고치지 않음), `observe.py` `2a9a6931cd511d6f12e07f8df8c335285b96cad6e5bb676faa6276062639870b`, 시험 `test_observe.py` `b7c536d4ca016120b57536b82309d6a633c692f6000bcf77d9a4742ad2a98128`, `heavy_gate.sh` 그대로. 12/8 파일럿 뒤 값으로 다시 동결한다 |
| 준비·집계 도구 sha256 (Juice Shop 파일럿에서 새로 만들거나 고침) | `prepare_js.sh` `7690dc168ae07b24a935db9151f47234fa2b3fe58764816f4a224b716341f3bf`, `pilot_config.py` `0fe0983d87f22124cc9c36be052d1518504433c27da50e4ebcaffb3b9060dceb`(관찰 칸 선택·Juice Shop 파일럿 설정), `results_js.sh` `03c85992cf2eeebfa07f73dc83e32e6a6f1ea67f305542ad11f45fe5d7c12d61` |
| 설정 파일 sha256 (`/home/sbfmt/abc2lab-eval/jspilot-20261123/`) | 공식: `runner.official.json` `646768feb5658e209ed76c15291fab96b1c5d51632ed30d0b0335f0f88fd610a`, `preflight.official.json` `f55c862c2ddf18ae10f2224a9363308dd0014e0e5d9fa2ae49d32d92c1346e42`. Juice Shop 파일럿: `runner.json` `07ea34dad93cba3a3943bde53d73fd5f054d5426774617a5b3cf4dda3b26027b`, `preflight.json` `fc6fb47c50d1477cb2bf4de1a82c6f6455b551d84da53bf60636000236919883`. 두 러너 설정에서 다른 칸은 `runs_root`·`preservation.series_ref` 둘뿐이다 |
| Go 3 파일럿(VAmPI) 점검표 | 4번 ①~⑦ 모두 통과(격리 위반 0, 출발점 점검 통과, 관찰 없는 O와 같음). 메서드 수: GET 121·HEAD 2·POST 27·PUT 2·PATCH 1, DELETE 0, 거부 0. 브라우저 탐색 3, 경계 completed |
