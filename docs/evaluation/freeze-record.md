# 공식 측정 동결 기록 (틀)

공식 측정에 쓰는 고정값을 한 곳에서 보게 모은 기록이다. **값의 정본은 각 줄의 '정본' 칸에 적은 문서다.** 이 문서는 그 값을 옮겨 적고, 파일럿이 끝난 뒤 실제 값(코드 SHA·이미지 digest·재조회 단가)을 채워 동결한다. 정본과 이 문서가 다르면 정본이 맞고, 이 문서를 고친다.

- 상태: **파일럿 값 채움(동결 전)**. 남은 `<채움>` 칸은 공식 세션에서 정해지는 값이다(재조회 단가·세션 기록 경로·시작 시각·다시 빌드한 이미지 digest·프로파일 지문·env_check).
- 동결 절차: ① 파일럿 한 쌍 끝 → ② `<채움>` 칸을 실제 값으로 → ③ 정본 문서와 한 줄씩 대조 → ④ 사용자 확인 → ⑤ 이 문서의 상태를 '동결'로 바꾸고 커밋 SHA를 공식 결과 문서에 적는다.
- 동결 뒤 값이 바뀌면(제공자 단가 변동 등) 이 문서에 새 줄로 남기고, 이미 낸 결과를 소급해 고치지 않는다(평가 프로토콜 §5.1).

정본 문서 줄임말: **프로토콜** = `evaluation-protocol.md`, **고정 초안** = `official-protocol-fixation-draft.md`, **실행 절차** = `official-run.md`.

## 1. 모델·제공자

| 항목 | 고정값 | 정본 | 동결 때 채울 것 |
|---|---|---|---|
| 모델 | `qwen/qwen3.6-35b-a3b` | 프로토콜 §6 | 엔드포인트 재조회 근거: 세션 기록 `provider_price`·`price_caps`. 파일럿: `/home/sbfmt/abc2lab-eval/pilot-20261122/session-20261122t160607z/session-log.jsonl`. 공식 세션 `<채움>` |
| 제공자 | `primeintellect/fp8` (PrimeIntellect), fallback 끔 | 프로토콜 §6 | 파일럿: 2026-11-22 16:06Z, `provider_price` 조회 200, 단가 0.19 / 1.70. 공식 세션 `<채움>` |
| 제공자 조건(O 요청) | ZDR 켬, `data_collection=deny`, `require_parameters` | `docs/openrouter-first-diagnosis.md` | — |
| 고정 샘플링 | `temperature=1.0`, `top_p=0.95`, `top_k=20`, `min_p=0.0`, `presence_penalty=1.5`, `repetition_penalty=1.0`, `seed=42` | 프로토콜 §6 | — |
| 샘플링을 넣는 곳 | B: 평가 게이트웨이, O: 승인 manifest `sampling` (엔진은 넣지 않음) | 프로토콜 §6, 실행 절차 §4 | 공식 세션 회차별 적용값 대조 결과 `<채움>` |

## 2. O 승인 manifest 값

| 항목 | 고정값 | 정본 | 동결 때 채울 것 |
|---|---|---|---|
| 가격 상한(입력/출력, USD per 1M) | 규칙: 세션 시작 직전 재조회 단가 × 1.5(올림), 세션 안 고정(`--resume`도 시작 때 값). 이어 돌릴 때의 시작 조건: 상한 ≥ 지금 단가 × 1.2(`provider_price_headroom`, 새 세션은 × 1.5라 늘 통과). 고정 숫자가 아니다. 예: 2026-11-22 단가 0.19 / 1.70 → `285000` / `2550000` | 실행 절차 §4, `official_session.py --provider-probe` | 파일럿: `price_caps` 285000 / 2550000(규칙 × 1.5, 여유 1.2), O 회차 `o_manifest_caps`도 같음. 공식 세션 `<채움>` |
| 모델 호출 하나의 시간 상한 | 170초 (`timeout_seconds`) | 실행 절차 §4 | — |
| 연속 일시 오류 상한 | 8 (`max_consecutive_transient_errors`) | 실행 절차 §4 | — |
| 연속 상한 뒤 동작 | 60초 쉬고 시험 호출 1번, 처음 쉰 때부터 5분 넘으면 잠금 | `llmpolicy/openrouter_cooldown.go` | #858, dev 머지 커밋 `dd3385a2`(2026-11-22 16:07Z) |
| 호출당 입력·출력 토큰 상한, 요청 수·비용 상한 | 실행 절차 §4 예시 값 | 실행 절차 §4 | 공식 세션 manifest의 값 `<채움>` |

## 3. 엔진·회차 상한

| 항목 | 고정값 | 정본 | 동결 때 채울 것 |
|---|---|---|---|
| 회차 요청·시간 예산 | 고정 초안 §10의 값 | 고정 초안 §10, 실행 절차 §2 | 요청·시간 예산은 러너 설정이 아니라 사전검사 선언·회차 조건에 있다. 파일럿(VAmPI): 요청 300·시간 1500초. 공식 세션 `<채움>` |
| 먼저 걸리길 의도한 상한 | 고정 초안 §1 | 고정 초안 §1 | — |
| Planner 계획 한 번의 시간 상한 | 170초 | `agent/e2_http.go` | #858, dev 머지 커밋 `dd3385a2` |
| 준비 단계 전체 상한 | 2분 | `server/first_diagnosis_supervisor.go` | — |
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
| 측정 시간대 | 공식 세션: KST 14~18시에 시작. 파일럿: 시간대 제한 없음(제공자 429 점검만 켬) | 실행 절차 §0 | 파일럿 2026-11-22 16:06:07Z(KST 11/23 01:06). 공식 세션 `<채움>` |
| 시작 전 점검 | 포트, 메모리(Linux available 3GB 이상 + Windows 커밋 여유 5GB 이상), 제공자 429 점검 호출, 가격 상한 ≥ 지금 단가 × 1.2 | 실행 절차 §0·§5 | 세션 기록 `$SESSION/session-log.jsonl`의 사건 `start_window`·`provider_price`·`price_caps`·`usage_base`·`provider_probe`(·`provider_probe_wait`)·`ports_busy`·`heavy_wait`·`stopped_before_run`, 회차마다 `o_manifest_caps`. 파일럿 세션 경로 `/home/sbfmt/abc2lab-eval/pilot-20261122/session-20261122t160607z/session-log.jsonl`(429 점검 10번 중 429 0번, 회차 중 최저 여유 Linux 4.6GB·Windows 9.8GB). 공식 세션 경로 `<채움>` |
| 측정 머신 | 고정 초안 §25 | 고정 초안 §25 | 공식 세션 env_check 결과 `<채움>` |
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

- 규칙(세 O 회차 같은 설정): 관찰용 로그인은 1번만 하고 다시 하지 않는다. 로그인 실패·브라우저 종료·로그아웃이 있어도 회차는 유효하고 `observer_failed`만 남긴다.
- 파일럿: 로그인 성공, 승인 요청 0. 다만 `observer_failed: run_not_opened`였다. 러너 O 회차가 작업대 URL 진단 목록에 보이지 않아 '이 점검 열기'를 하지 못했고, 관찰 화면은 빈 대시보드였다. 회차 결과에는 영향이 없다.

## 6.2 정본 문서의 남은 문구

- 평가 프로토콜 §5.1 승인 메서드 줄 끝의 "분리 배치 프로파일과 절차가 준비될 때까지 보류"는 충족됐다(`profiles/official-split-35b.json`이 있고 실행 절차가 그것을 쓴다). 문구 정리는 측정 뒤에 한다.

## 7. 코드와 이미지 (동결 때 채움)

| 항목 | 값 |
|---|---|
| O 코드 커밋(dev) | `dd3385a24c0eac762566bd98b3d74ba8c3ffa4ec`(tree `12a097c0f5ac9c5088e3944b37a38210c6792093`). 파일럿은 #858 머지 직전에 시작해 로컬 통합본 `9208d7ab`(dev `ebb4b428` + #858 head `701f9945`)으로 돌았고, 통합본의 tree가 머지된 dev의 tree와 같다 |
| B 코드 커밋과 패치 | `e53a79fe4df0c95a5b09274de8db321fa038b00b` + 도구 판 고정 패치(실행 절차 §4) |
| 제품 이미지 digest (product / explore-browser / fixture) | 파일럿 빌드(태그 `9208d7ab4bc7`): product `sha256:f71f7a09da462db9800e9d545d24027eec34d67fa9f3e51ba140be9b61e1b580`, explore-browser `sha256:99376edd939bcf8dcac47b1a1402ae18f2c1227a9724f2cb9cc93a9b2f47d745`, fixture `sha256:10b8fa3a3615bbf6fd786d9af79ef1590af14e42d3120e7c3641830ffe81e2b6`. 공식 세션은 동결 SHA로 다시 빌드하므로 그때의 digest `<채움>`(tree가 같아도 이미지 ID는 빌드마다 다를 수 있다) |
| 평가 계측기 이미지 digest | 파일럿 빌드 `sha256:9d24586f0659fef50d00d0598baf1a8a0ea9fee00ad237a1f2e1ef97b705045d`. 공식 세션 `<채움>` |
| 대상 이미지 digest | 공식(Juice Shop) `sha256:fe8cecfec60f7856a02b52072c879b1baf73f5a97faafdeb12ed9e8eaccc0c3d`. 파일럿은 VAmPI였다 |
| 러너 코드 커밋 | O 코드와 같은 dev 트리의 `scripts/eval`(`dd3385a2`). 결과 정리 도구(세션 집계의 멈춤 진행 호출 칸)는 브랜치 `feature/minjun-results-stall-inflight` `651c7d07`이고 머지는 측정 뒤다 |
| 프로파일 지문 | 공식 세션 사전검사 선언의 값 `<채움>` |
| 파일럿 회차 ID(B·O) | B `baseline-20261122t160622z-80101419` complete(first_limit `runner_request_budget`, 429 3번, 멈춤 0). O `ours-20261122t161713z-88e7f6a7` complete(first_limit `runner_time_budget` 1500초, 429 4번, 멈춤 0, E③ `disabled`/`declaration_missing`). 세션 complete, 시도 B 1·O 1. 사용액 약 $1.115(팀 공용 키라 그 사이 다른 호출이 있었으면 포함) |
| 파일럿 하네스 sha256 | `run.sh` `7e249f0b2816d5f198d0422510e8e2dcc1029aba40700755e19d61eac1f97966`, `observe.py` `7e40a845d341121099a8a5eca46d502ba069edd6bd45cacde12917877474c5c3`, `heavy_gate.sh` `89e816d8e153da9d3a0ff04a3bb98e48b6b44b8a5dbf3398107390309155a8ba`. 파일럿 뒤로 고치지 않고, 공식 세션은 MODE=official만 다르다 |
| 파일럿 점검표 | 4번 ①~⑦ 모두 통과(격리 위반 0, 출발점 점검 통과, 관찰 없는 O와 같음). 메서드 수: GET 121·HEAD 2·POST 27·PUT 2·PATCH 1, DELETE 0, 거부 0. 브라우저 탐색 3, 경계 completed |
