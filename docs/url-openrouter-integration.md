# URL v2 · OpenRouter 설치와 수용 계약

기준 dev는 `8f479109af72200b665605927c18af1b5fbccb97`이다. 기존 `/workbench` URL 준비 서비스, 실제 task/intent, Planner/Worker, E②, 저장 보고서를 사용한다. OpenRouter 송신은 `agent.NewOpenRouterProvider`와 `llmpolicy.NewOpenRouterClient`만 사용한다. 모델은 저장소 허용 목록의 `qwen/qwen3.5-35b-a3b`, base는 `https://openrouter.ai/api/v1`이며 제공자는 보호 manifest가 고정한다. 임의 모델·provider fallback, 스트리밍, 재시도, redirect는 허용하지 않는다.

## 예산과 승인 수명

Start 전에 기존 preparation journal/head에 **회차 전체** 요청·토큰·USD·시간 상한을 예약한다. 같은 위임의 과거 회차 예약을 합산해 누적 한도와 MaxRuns를 검사한다. 실패·중지에도 환불하지 않고 unknown 회차가 있으면 새 Start를 차단한다. 이 저널은 지우거나 새 디렉터리로 바꿔 예산을 재사용하는 복구 수단이 아니다.

회차 안에서는 `FirstDiagnosisModelBudget.callGate` 하나가 준비 Planner와 실행 Planner/Worker 모두를 직렬화한다. 기다리는 Worker는 추가 예약을 만들지 않는다. 입력은 호출당 전체 context 262144 토큰, 출력은 고정 상한을 예약한다. USD 예약은 manifest의 입력/출력 가격 상한과 같은 올림식을 사용한다. 이 예약과 unknown 상태를 동기 저장한 후에만 기존 OpenRouter transport가 승인 예산을 검사·소비한다. URL 회차 한도는 manifest 전체 한도보다 클 수 없으며 호출당 USD 예약도 회차 한도 안이어야 한다.

Transport는 유일한 OpenRouter 승인 원장이다. URL 원장은 별도 범위인 회차·위임을 집행한다. 두 금액을 더해 비용으로 표시하지 않는다. 성공 후 정확히 한 transport 호출의 검증된 사용량과 SDK 사용량이 일치해야 실제 USD를 반영한다. 응답·model/provider·usage·cost 불일치, 저장 실패, 취소/timeout으로 결과가 불명이면 예약을 유지하고 후속 모델 호출을 막는다.

준비와 실행 host는 **같은 policy 객체**를 공유한다. 따라서 handoff 때 사용량이나 승인 marker를 초기화하지 않는다. URL job의 사용량은 준비+실행 누적이고, 저장 보고서의 `model_preparation`과 `run.model`은 각각 해당 단계만 포함한다. 두 단계가 겹쳐 합산되지 않는다.

기존 one-use 승인 marker는 첫 송신 전에 fsync한다. 프로세스 재시작 후 같은 manifest 승인으로 새 유료 회차를 실행하지 않는다. 새 회차에는 실제 계정 잔여액·위임 누적 예약을 확인한 운영자의 새 승인 ID가 필요하다. marker 삭제, 승인 복사, 저널 삭제는 재승인 절차가 아니다. 진행 중 프로세스가 죽으면 회차는 unknown으로 남고 자동 재실행되지 않는다.

## 최소 설치 순서

1. 기존 [URL 준비 설치](url-diagnosis-preparation.md)의 Linux 전용 UID, PostgreSQL 최소권한 역할, JWT/로그인, helper, CA, 비변경 GET/HEAD 대상 정책, private IPC, API·보고서 서비스를 설치한다. 대상 origin/IP/path/method 권위는 모델 선택으로 늘어나지 않는다.
2. 운영자가 기존 신뢰된 DB 모델 프로필에 API 키를 등록한다. `format=openai`, 위 exact model/base, `streaming=false`, `context_window_k=256`, 양수 `max_tokens`, `max_tokens_field=max_tokens`, proxy/session header/thinking/reasoning override 없음으로 고정한다. goals/planner/worker 역할 pin은 비어 있거나 같은 profile ID여야 한다. API 키 등록용 브라우저 UI는 추가하지 않는다.
3. [기존 OpenRouter manifest](openrouter-first-diagnosis.md)의 보호 파일 형식으로 profile ID, provider slug와 정확한 응답 provider name, privacy/ZDR 확인, 계정 spend cap 확인, 만료, 요청·토큰·USD·timeout·동시성·가격 상한을 설치한다. 파일은 UID 소유 0600, 전용 디렉터리는 0700이고 symlink·교체를 거부한다. 키는 manifest에도 넣지 않는다.
4. URL launch에 `model_execution_mode: "openrouter"`, `local_llm_manifest_path: ""`, `openrouter_manifest_path: "/설치자/보호/manifest.json"`과 같은 DB profile/model/base를 설정한다. installation의 launch SHA256·manifest SHA256을 갱신한다. `input_tokens_per_call=262144`, 출력은 DB/manifest 한도 이하, 회차·누적 USD는 양수이며 승인 manifest 한도 이내로 설정한다. local_only는 기존대로 비용 0만 허용한다.
5. 보호 installation의 model label에 Qwen3.5/OpenRouter를 지정하고 기존 `prepare-service`와 `serve-url-api`를 실행한다. `AUTOPENTEST_API`가 이 API를 향하는 Workbench에서 프로필·URL을 선택한다. 범위 확인에서 대상, provider, 토큰·USD 상한과 외부 전송을 확인하고 동의한 뒤 명시적으로 시작한다.

키는 backend DB 조회에만 사용한다. 실제 task/profile chain과 역할 pin, 매 호출의 최신 DB config(키 포함)를 다시 대조한다. 키 교체·빈 키·프로필 교체는 현재 회차의 다음 호출을 차단하며 재시작/새 승인으로 설치를 다시 확인한다. provider가 반환한 401/429/5xx는 재시도하지 않고 비용 불명으로 처리한다. provider의 실제 계정 키 만료 시점은 호출 전 로컬 검사만으로 확인할 수 없다.

Start 요청은 비용·외부 전송 동의를 `external_transfer_confirmed: true`로 보내며 서버가 scope와 revision에 대조해 영속 receipt에 저장한다. 키·manifest 경로·provider 선택값은 진단 body, React state, local/session storage, URL, 결과·보고서·로그에 전달하지 않는다. 공개 model projection은 mode/model/provider와 호출당 한도뿐이다. 기존 브라우저 복원 handle에도 model 자격을 추가하지 않는다.

## 네트워크와 상호작용

모델 송신은 기존 OpenRouter public-IP pin/TLS/privacy/provider/비용 정책을 통과한다. Worker 대상 송신은 기존 E② origin·IP·GET/HEAD·path·TLS·감사·Gateway 실행 권위를 통과한다. OpenRouter API hostname을 진단 대상으로 설치하는 것도 거부한다. E② 정책·메서드·TLS 검증 구현은 변경하지 않는다.

Workbench는 시작·중지·상태 재조회·저장 결과/HTML 조회를 제공한다. 서버 단계는 Planner 준비와 Worker 실행을 구분하고, 전송 통제 화면은 허용 판정과 실제 전송 시도를 따로 표시한다. 모델 요청/실제 토큰/예약 토큰/예약 USD/확인 USD와 unknown 상태는 서버 값만 사용한다.

실행 중 채팅·추가 지시는 **미구현이며 별도 후속 PR**이다. 일반 ARTEX 채팅·steer API를 URL host에 연결하지 않는다. 필요한 후속 계약은 인증 주체, job/task/intent/권위 및 revision 결속, idempotent 명령 receipt, 감사, 취소·동시성, 원래 origin/method/path/예산을 축소만 할 수 있는 검증이다. 지시는 실행 권위를 새로 만들 수 없다.

## 수용 범위

합성 시험은 실제 SDK/transport 검증·보호 파일·URL API·실제 PostgreSQL·Planner/Worker·격리 helper·E②·결과 저장·HTML을 사용한다. 시험 바이너리의 Go overlay만 이미 허가된 transport의 물리 연결을 소유 loopback 서버로 고정한다. 제품 바이너리에 테스트 endpoint·환경 변수·TLS 우회나 범용 client 주입을 추가하지 않는다. 기존 llmpolicy/agent 회귀가 잘못된 model/provider/usage/tool-call, 예산, 재시도·redirect와 실패 후 추가 전송을 검사한다.

실제 OpenRouter 유료 수용은 **NOT_RUN**이다. 별도 명시적 유료 호출 승인, 실제 USD 총액, 실제 키·계정 spend cap/privacy/provider 설정, 소유 대상 범위를 받은 뒤에만 수행한다. 합성 응답은 Qwen3.5의 실제 추론·청구·진단 능력을 증명하지 않는다.

`https://mcp.ondemand.sparrowcloud.ai/` 진단도 **NOT_RUN**이다. MCP POST/JSON-RPC/SSE·인증 요구사항과 승인된 비변경 경로가 확인되지 않았다. 현재 GET/HEAD 계약으로 MCP 진단 가능하다고 판정하지 않으며, 별도 대상 프로필·허용 요청·인증·예산 승인이 필요하다. 이번 작업에서 그 주소에 요청하지 않는다.
