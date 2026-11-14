# OpenRouter 첫 개발 진단: 명시적 외부 모드

이 변경은 최종 온프레미스 모델 정책이나 공식 평가 모델을 바꾸지 않는다. 로컬 전용 기본값과 기존 `llmpolicy.Policy`는 그대로다. 개발용 비민감 일반 HTTP 진단에 한해 OpenRouter를 선택한다. 실제 모델·대상 회차가 없으면 합성 시험만으로 **UI 연결만 남음**이라고 판정하지 않는다.

## 통합 기준과 지원 범위

- **2026-11-08 확인 시점의 코드 스냅샷**: `dev` `d2bfec57f34dae1c17b6026366e747f3a588750b`. [#317](https://github.com/ABC2LAB/ABCDEF2LAB/pull/317)과 [#323](https://github.com/ABC2LAB/ABCDEF2LAB/pull/323)은 병합되어 있다. 코드 연결 완료와 실제 모델·대상 수용 완료는 구분한다.
- 위 SHA는 상시 최신 실행 버전을 지정하지 않는다. 새 실행 전 최신 `origin/dev`의 정확한 SHA를 확인·기록하고 해당 버전의 CLI·모델 정책과 이 계약을 다시 대조한다.
- 과거 개발 출발점(현재 checkout 지침 아님): [#317](https://github.com/ABC2LAB/ABCDEF2LAB/pull/317) `2646aa9ab3195203ab841d2cb34859c56462e788`, dev `3dfe4375d9c7b86c7d73da8a866c4a8c84e4cd72` 결합.
- 외부 모델 변경은 당시 `feature/dongchan-openrouter-first-diagnosis-20261108`에서 검토한 뒤 #323으로 병합했다. 현재 실행은 최신 `origin/dev`를 재확인한 뒤 시작하며, 병합 전 브랜치나 CI 통과를 실모델 진단 성공 근거로 쓰지 않는다.
- 외부 모드는 `general_http`와 그 `prepare-intents`만 지원한다. 등록 GET/IDOR의 기존 로컬 E③ 경로를 그대로 두며 외부 모드와 `registered_get` 조합을 거부한다.
- 프로토콜은 OpenAI 호환 Chat Completions, 모델은 `qwen/qwen3.6-35b-a3b`(2026-11-14부터, 그 전은 `qwen/qwen3.5-35b-a3b`), base는 정확히 `https://openrouter.ai/api/v1`다.
- `llmpolicy.OpenRouterBaseURL`·`OpenRouterModel`은 이번 외부 모드의 **송신 허용 목록(allowlist)** 이며 실행 기본 설정이 아니다. 실제 모델·base는 launch config·DB profile·보호 manifest에서 받고 이 목록과 정확히 일치해야 송신할 수 있다. 설정만 바꿔 임의 모델·endpoint로 허용 범위를 넓히지 않으며, 이 경계는 기존 로컬 모델의 설정 선택을 바꾸지 않는다.
- 첫 외부 모드는 **non-streaming만** 지원한다. profile의 `streaming=false`, 양수 `max_tokens`, `context_window_k=1..262`를 설정한다. 도구 선택은 SDK의 자동 선택만 지원하며 명시적 `tool_choice`는 거부한다. 스트리밍·thinking/reasoning override·proxy·session header·다른 토큰 필드·멀티모달·유료 plugin은 거부한다. 외부 스트리밍을 검증했다고 주장하지 않는다.
- 기존 provider SDK를 재사용한다. profile/task/goals/planner/worker 단일 결속, 현재 DB 설정 재검사, 승인·중지·감사·마감 순서는 유지한다. 모델·provider·다른 키로 자동 fallback하지 않으며 새 외부 요청 자동 재시도도 하지 않는다.

## 보호된 실행 입력

기존 [기동 설정](first-diagnosis-launch.md)의 일반 HTTP 설정에서 다음만 명시적으로 바꾼다.

- `model_execution_mode`: `openrouter`
- `openrouter_manifest_path`: 설치자가 준비한 절대경로의 보호 파일
- `local_llm_manifest_path`: 생략 또는 빈 문자열. 두 manifest 동시 지정은 거부한다.
- `model_format`: `openai`, `model_base_url`: 위 base, `model_ref`: 위 모델 ID
- `model_profile_id`: 실제 설치된 동일 DB 프로필 ID

기존 설정의 mode 생략은 **유효한 로컬 manifest만** 있는 경우에만 로컬 동작을 유지한다. `local_only`도 명시할 수 있다. 외부 manifest만 지정하고 mode를 생략하거나 알 수 없는 mode를 쓰면 거부한다. 공개 주소를 로컬 manifest로 허용하는 변경은 없다.

별도 OpenRouter manifest의 필수 필드는 다음과 같다. 아래는 필드 설명이며 승인된 실행 설정 예시가 아니다. 실제 승인값을 임의 합성하지 않는다.

| 필드 | 설치자 입력 |
|---|---|
| `version` | `1` 또는 `2`(`2`는 `max_consecutive_transient_errors`가 있어야 한다) |
| `profile_id`, `format`, `base_url`, `model` | 실제 DB profile과 정확히 같은 tuple |
| `provider_slug` | 승인한 정확 endpoint tag 하나 |
| `expected_provider_name` | 응답에서 검증할 실제 provider 표시 이름 |
| `approval_id`, `expires_at` | 단계별 승인 식별자와 미래 만료 시각 |
| `strict_privacy_ack` | 제공사 조건과 계정/guardrail의 ZDR-only 선택을 실제 확인한 경우에만 true |
| `account_spend_cap_ack` | 해당 키/계정의 승인된 지출 제한을 실제 확인한 경우에만 true |
| `max_requests` | 이 프로세스 단계의 최대 실제 모델 요청 횟수 |
| `max_input_tokens`, `max_output_tokens` | 단계 전체 보수적 토큰 예약 상한 |
| `max_cost_usd_micros` | 단계 전체 승인 USD 상한, 1 USD = 1,000,000 micros |
| `max_input_tokens_per_call` | 모델 전체 context인 262144. 호출 하나의 입력 예약 상한이다(실제 예약은 아래 요청 크기 상한과 이 값 중 작은 쪽) |
| `max_output_tokens_per_call` | profile/요청 출력 상한 이상인 승인된 제한 |
| `timeout_seconds`, `max_concurrent` | 요청별 1~300초 제한과 동시 요청 상한 |
| `max_input_price_usd_micros_per_million`, `max_output_price_usd_micros_per_million` | 승인된 100만 토큰당 최대 단가, provider.max_price에 결속 |
| `max_consecutive_transient_errors` | v2만. 연속 일시 오류(429·5xx·연결 실패·응답 본문 읽기 끊김)가 이 수(1~16)에 닿으면 정책을 잠근다. v1은 1로 본다(첫 일시 오류에 잠금) |

manifest와 부모는 기존 보호 파일 계약(실행 UID 소유, 파일0600/부모0700, symlink·hardlink·교체 거부)을 따른다. API 키는 manifest에 넣지 않는다. 기존 보호된 DB profile credential 공급 경로를 사용하며 채팅·명령 인자·환경 변수·PR·로그에 붙여 넣지 않는다. 설치자가 실제 키를 기존 보호 관리 화면/절차로 저장한 뒤 비밀 원문 없이 profile ID와 설정 여부만 인계한다. 이 변경은 새 키 발급·OAuth·계정 권한 확대를 하지 않는다.

## 라우팅·개인정보 조건

2026-11-14 공식 [endpoint 목록](https://openrouter.ai/api/v1/models/qwen/qwen3.6-35b-a3b/endpoints)에서 tools/tool_choice를 광고한 tag는 `primeintellect/fp8`, `darkbloom/fp4`, `akashml/fp8`, `parasail/fp8`, `deepinfra/fp8`, `dekallm`, `venice/fp8`, `atlas-cloud/fp8`였다(`atlas-cloud/fp8`는 조회 시점 상태 값 -5). 2026-11-08 Qwen3.5 목록(`darkbloom/fp4`, `parasail/fp8`, `venice`, `alibaba`, `atlas-cloud/fp8`)은 이력이다. 이 목록은 설치 승인이나 현재 수용 시험을 대신하지 않는다. required/named-function choice 지원도 제공사별로 다르므로 실행 전 최신 metadata와 선택한 도구 모드를 다시 대조한다.

신뢰된 모델 클라이언트가 다음을 고정 삽입한다: `provider.only=[승인 tag]`, `allow_fallbacks=false`, `require_parameters=true`, `data_collection=deny`, `zdr=true`, 승인 단가의 `max_price`. 실패 시 조건을 느슨하게 재요청하지 않는다. 응답의 모델과 제공사 표시 이름도 대조한다.

`zdr=true`를 늘 넣으므로 승인 tag는 그 모델의 ZDR endpoint여야 한다. 아니면 OpenRouter가 맞는 endpoint를 찾지 못해 첫 호출이 HTTP 404로 끝나고 실행은 결과 불명(`model_usage_unknown`)이 된다(2026-11-13 실제 점검 회차, Qwen3.5·`atlas-cloud/fp8`). 그날 Qwen3.5의 ZDR tag 중 tools를 광고한 것은 `parasail/fp8`·`venice`였고, 제품 점검 회차는 `parasail/fp8`로 끝까지 돌았다(이력). 2026-11-14 [ZDR endpoint 목록](https://openrouter.ai/api/v1/endpoints/zdr)에서 `qwen/qwen3.6-35b-a3b`의 ZDR tag는 `primeintellect/fp8`·`dekallm`·`deepinfra/fp8`·`coreweave/fp8`·`venice/fp8`·`siliconflow/fp8`·`akashml/fp8`·`parasail/fp8`였다. 그중 tools를 광고하고 평가 샘플링 일곱 값(`temperature`·`top_p`·`top_k`·`min_p`·`presence_penalty`·`repetition_penalty`·`seed`)까지 모두 지원 표시한 것은 `deepinfra/fp8`(표시 이름 `DeepInfra`)·`primeintellect/fp8`(`PrimeIntellect`) 둘이고, `parasail/fp8`은 `min_p` 표시가 없었다. 제품 OpenRouter 경로(ZDR + tools)도 평가 프로토콜 §6의 고정 제공자와 같은 `deepinfra/fp8`을 승인 tag로 쓴다. Qwen3.6·`deepinfra/fp8` 제품 회차는 아직 돌리지 않았다. 목록은 바뀌므로 설치 전에 두 목록을 다시 대조한다. 평가 게이트웨이는 `zdr`을 넣지 않으므로 이 제약은 제품 경로에만 해당한다.

**중요:** 현재 [ZDR 문서](https://openrouter.ai/docs/guides/features/zdr)는 `zdr:true`만으로 Safety Retention endpoint까지 제외되는 것은 아니라고 설명한다. 계정/guardrail의 ZDR-only 선택과 승인한 제공사의 실제 조건을 별도로 확인해야 한다. 공개 endpoint metadata만으로 해당 확인을 대체할 수 없다. `strict_privacy_ack`는 검증을 자동 수행하는 값이 아니라 설치자의 확인 선언이다. 비민감 연습 자료만 승인된 제공사에 보내며 실대상 인증값·고객 기밀을 prompt에 넣지 않는다.

참고: [provider routing](https://openrouter.ai/docs/guides/routing/provider-selection), [tool calling](https://openrouter.ai/docs/guides/features/tool-calling), [reasoning](https://openrouter.ai/docs/guides/best-practices/reasoning-tokens), [errors](https://openrouter.ai/docs/api_reference/errors-and-debugging).

## 송신·예산 경계

- 신뢰 모델 클라이언트만 정확한 HTTPS Chat Completions URL로 나간다. 표준 proxy 환경 변수·리다이렉트·TLS 검증 우회를 사용하지 않는다. DNS 응답의 공인 주소 여부를 확인하고 검증한 주소에 연결한 뒤 peer를 대조한다.
- Bash/자식 프로세스/MCP/사용자 도구에 OpenRouter 키나 모델 네트워크 권한을 전달하지 않는다. 기존 Linux 격리와 E② 대상 origin·method·path·승인·예산·감사는 별도다. 외부 모델 허용은 대상 인터넷 허용이 아니다.
- 요청 JSON은 SDK 입력과 라우팅 삽입·재직렬화 후 모두 **4 MiB 바이트 상한**을 적용한다. 이 자원 상한은 토큰 수가 아니다. `context_window_k=1..262`와 SDK 압축 기준은 토큰 단위로 유지한다. 각 요청의 입력 예약은 라우팅 삽입 뒤 전송할 JSON 바이트 수 + 4096(chat template 여유)이고, 262144보다 크면 262144다(P3 결정안 C28 결정 1, 토큰 하나는 1바이트 이상이라 바이트 수가 입력 토큰의 상한). 비용 예약도 이 입력 예약으로 계산한다. 응답의 실제 입력이 이 예약을, 비용이 이 비용 예약을 넘으면 상한 가정이 깨진 것이라 실패로 잠근다. 바이트 상한 이내라고 실제 tokenizer의 context 적합성이나 청구 정확성을 보증하지 않는다.
- 모든 wire 요청 전에 동시·횟수·토큰·비용을 예약한다. 실패/불명 응답의 예약을 되돌리지 않아 재시도로 예산을 늘리지 않는다. 응답은 셋으로 나눈다(P3 결정안 C06): 설정 오류(401·403·400 등 4xx, 응답 크기·형식·계정 대조 위반, 주소 정책 위반, TLS 인증서 검증 실패)는 첫 번에 policy를 잠근다. 일시 오류(429·5xx·연결 실패·시간 초과·응답 본문 읽기 끊김)는 그 호출만 실패로 두고 예약은 쓴 것으로 남기며, 연속으로 `max_consecutive_transient_errors`(v1은 1)에 닿으면 잠근다. 정상 200 응답이 연속 수를 되돌린다. 일시 오류 호출의 비용은 확인할 수 없으므로 예약 비용을 상한으로 센다. 자동 재시도하지 않으며 새 승인은 남은 지출과 실패 원인을 확인한 뒤 별도로 준비한다. 계정 지출 제한과 OpenRouter 단가 집행을 함께 사용한다. 클라이언트는 제공사의 실제 청구 시스템을 통제하지 못하므로 설정만으로 청구 정확성을 보증했다고 쓰지 않는다.
- 승인 ID마다 누적 예약 기록(`.openrouter-<승인 해시>.ledger`, 보호 manifest 디렉터리, 0600·단일 링크·추가 쓰기만)을 둔다(P3 결정안 C07). 모든 wire 요청 전에 이 파일을 배타 잠금하고 다시 읽어, 지금까지의 합계(요청 수·입력·출력·비용)에 이번 예약을 더해도 manifest의 승인 총량 안일 때만 예약 줄을 더하고 fsync한다. 합계는 정산 안 된 예약은 예약량으로, 응답을 확인한 호출은 실제 사용량으로 센다: 응답 확인 뒤 같은 잠금 안에서 그 예약과 짝이 되는 정산 줄(예약값·실제값, 실제 ≤ 예약)을 더한다(C07 "승인별 누적 사용량"·C28 결정 2). 정산 줄을 쓰지 못했거나 실패한 호출은 예약이 그대로 다 쓴 것으로 남아 총량 보장은 안전한 쪽이다. 짝 없는 정산·실제가 예약을 넘는 정산은 기록이 깨진 것으로 보고 막는다. 그래서 같은 승인을 여러 회차·재시작·동시 프로세스가 써도 승인 총량을 넘지 않는다. 기록을 읽을 수 없거나 형식·보호 조건이 틀리면 예약하지 않고 막는다. 예전 1회용 표식(`.used`)이 남은 승인은 이미 쓴 것으로 본다. 다른 디렉터리로 승인을 복사한 경우까지 전역 탐지하지 않으며 운영자는 승인 복사로 예산을 재사용하면 안 된다. 기록을 지우거나 고쳐 재시도하지 않는다. `check`와 provider 구성만으로는 예약하지 않으며, `check`는 한 번 더 예약할 자리가 없는 승인을 거부한다.
- `prepare-intents`와 `run`은 별도 프로세스다. 같은 승인을 두 단계가 함께 써도 누적 기록이 승인 총량을 지킨다. 단계별 상한을 따로 두고 싶으면 **서로 다른 승인 ID와 단계별 manifest를 준비하고 두 단계 상한의 합이 사용자 승인 총액 이하인지 확인**한다. 같은 프로필/모델/제공사/privacy 조건은 유지한다. 실패 뒤 새 승인은 남은 비용을 확인하고 운영자가 별도로 발급한다.
- 계획 준비의 기존 상한은 2분, 전체 Run은 30분이며 task 입력 timeout은 별도다. 시간 초과를 성공으로 처리하지 않는다. 모델 timeout과 전체 실행 timeout을 구분한다.
- tool-call 이름·ID·JSON과 tool-result 결속을 검증한다. 등록되지 않은 도구, 중복/누락 결과 또는 잘못된 응답은 실행으로 넘기지 않는다. 등록 schema의 필수 필드·기본 타입은 기존 SDK 검증기를 재사용해 호출 전과 결과에 적용한다. 이는 enum·수치 범위·pattern·additionalProperties까지 검증하는 완전한 JSON Schema 구현이 아니며, 실제 의미·실행 권한 검사는 기존 도구와 E② 경계가 담당한다.

URL v2 자동 준비는 [별도 통합 계약](url-openrouter-integration.md)을 따른다. URL 경로는 단일 프로세스·단일 승인·공유 예산으로 준비와 실행을 연결한다. 아래 수동 CLI의 두 단계 승인 절차와 혼동하지 않는다.

## 공식 실행 순서와 UI 인계

1. 설치자가 실제 profile, 기존 JWT/로그인, 최소권한 runtime/report DB 역할, Linux helper, 승인 origin·method·path와 E② policy/authority를 준비한다. 합성 시험용 자격이나 승인 파일을 실제 자료로 복사하지 않는다.
2. `prepare-task --config ... --task ...`: paused task를 만들고 실제 task ID를 보존한다. 파일/모델 정책 검사는 송신 없이 수행한다.
3. 준비 단계 manifest를 사용해 `prepare-intents --config ... --task-id ...`: 실제 모델 비용이 발생하는 단계다. 사용량 집계와 실제 intent ID를 보존한다. 대상 요청은 없다.
4. 별도 실행 단계 manifest와 같은 task/profile 결속으로 `check --config ... --database` 후 `run --config ...`. 확인 모드와 사전 승인 모드는 기존 E② 정책에서 선택한다. 승인 전 수신0, 허용 요청, 거절 수신0, 중지 후 신규 송신0을 실측한다.
5. 마감 후 기존 최소권한 `report --config ... --task-id ...`를 쓴다. report는 모델 manifest를 소비하거나 모델/대상 요청을 보내지 않는다.

API·인증·상태·승인·중지·HTML·복사·stale 계약은 [기존 UI 인계표](first-diagnosis-launch.md#ui-연결-계약-실행승인저장-결과)를 재사용한다. 새 임의 URL/명령 실행 API는 없다. 실제 API 서버 주소와 JWT 발급 대상은 설치자가 인계하며 이 문서의 loopback 예시는 실제 구동 서버가 아니다.

`GET /api/tasks/{id}/diagnosis-run`의 저장 결과는 선택적 실행 `model` 집계와 영속화한 계획 단계 `model_preparation` 목록을 추가한다. 실패한 계획 단계도 별도로 저장한다. 실행 전 계획 기록만 있는 경우 `run:null`, `execution:NOT_RUN`을 반환하며 성공 회차를 합성하지 않는다. 기록이 전혀 없으면 기존404다. 기존 유효 `run` 상태 의미는 바꾸지 않는다. CLI는 실패 시에도 가능한 `model_usage` 집계를 출력한다. 요청 수·예약 토큰/금액·보고된 실제 토큰/비용·지연·provider를 구분하고, 계측이 불명인 실패를 비용0 또는 진단 성공으로 표시하지 않는다. 원문 prompt/키/응답을 model 집계에 저장하지 않는다.

## 수용 판정

- 합성 SDK/transport/DB/E② 회귀 PASS는 **실제 Qwen3.6 추론 PASS가 아니다**.
- 실모델 스모크, 실모델 tool-call+tool-result+후속 판단, 실제 E② 허용/거절 수신, 전체 연습 회차, 저장 보고서/프롬프트를 각각 기록한다.
- 실제 키·사전 고지 승인·제공사 privacy 검증·비용 상한·승인 대상·설치 runtime이 부족하면 해당 단계를 NOT_RUN으로 남긴다.
- 첫 일반 HTTP 후보는 E③ 확정 취약점이 아니다. 실제 회차와 비UI 차단 해소가 모두 확인되기 전까지 백엔드 준비 완료 또는 UI 연결만 남음이라고 쓰지 않는다.
