# E② 평가 정책 생산·소비 형식 v1 — 확정 제안

기준: `origin/dev` `19f1288e75bdaa3033c902103ac7145a95dcd6d1` (#340·#341 병합 후). 이 문서는 최민준의 평가 생산자 리뷰와 PR 병합 전까지 **확정 제안**이다. 정책 정본은 `server.E2HTTPFilePolicy`와 `e2proxy`의 실제 검증이다. 새 정책 버전은 만들지 않는다. 이 문서의 검증 API는 오프라인 형식 검사이며 실행 허가가 아니다.

## 1. 산출물과 책임

| 단계 | 형식과 소유 | 변경되는 값 | 검증 |
|---|---|---|---|
| A0. 검토한 의미 템플릿 | JSON 객체, 러너 설정 `e2.policy_path`의 파일 | 내용 지문에 들어가는 필드와 회차 불변 참조만 둔다. 회차 값(`run_ref`, `evaluation.run_ref`, `valid_from`, `valid_until`, `state_dir`, `helper`, `helper_sha256`, `ca_cert`, `ca_key`, `rules[].connect_ip`)과 `audit_key`가 있으면 거부한다 | Python `render_policy` |
| A. 회차 기본 정책 | `abc2lab.e2-http-host-policy/v1`, 러너 `render_policy`가 A0에 회차 값을 채운 결과 | 집행 범위·상한·전달·평가 선언과 보호 자원 pin. **`audit_key` 키 자체 없음** | `server.ValidateE2HTTPPolicyDocument(raw, false)`로 닫힌 JSON·정적 의미. 자원 실재/권한은 별도 |
| B. 회차별 준비 정책 | 같은 형식, Python `prepare_e2_config` | 새 32바이트 `audit.key`를 만들고 `audit_key: {"file":"audit.key","sha256":…}`만 추가. 정확한 준비 바이트 SHA-256을 반환 | `e2config.Load`는 보호 파일·정확 바이트·최소 정책 구조, `ValidateE2HTTPPolicyDocument(raw, true)`는 닫힌 정책, `LoadE2HTTPRuntimeOptions`와 `NewE2StandaloneBackend`는 pin·CA·helper·집행 의미·권위 |
| C. 내용 지문 | `abc2lab.e2-policy-content/v1`, Go `e2export.PolicyFingerprint` | 정책 의미가 같으면 회차마다 같음 | 준비 후 실제 factory 산출값을 사전 검토·동결한 예상 지문과 비교. 서비스 응답을 예상값으로 자동 채택하지 않음 |

`config_sha256`은 준비 설정 JSON 파일의 정확한 바이트, `policy_sha256`은 준비 정책 JSON 파일의 정확한 바이트, `policy_fingerprint_sha256`은 C의 의미 투영이다. 앞의 두 지문은 순서·공백·회차 키 pin이 바뀌면 달라질 수 있다. 내용 지문은 세 값을 대신하거나 접근 권한을 주지 않는다.

**회차 값의 공급:** 러너가 준비 단계에서 A0에 채운다. `run_ref`는 회차마다 새로 만들고 평가 `eval_run_id`와 **달라야** 하며(`NewE2StandaloneBackend`) `evaluation.run_ref`는 같은 값이다. `valid_from`은 준비 시각, `valid_until`은 거기에 `e2.window_seconds`를 더한 값이고, `e2.approval_valid_until`을 넘으면 준비를 멈춘다(자동 연장 없음). `state_dir`는 `/run/e2-state`(회차별 0700, E② 쓰기 가능), `helper`·`helper_sha256`은 러너 설정 값이다. CA는 `e2.policy_tool`의 `ca`가 회차마다 새로 만들고 실제 파일 바이트로 pin을 채운다. `rules[].connect_ip`는 `e2_net` 고정 subnet의 계측기 주소 `e2.meter_ip`이고, up 뒤 Docker가 알려 주는 실제 주소와 다르면 그 회차를 쓰지 않는다. 회차별 `reset_generation`은 정책의 `reset_ref`와 다른 값이며 준비 뒤 `BindReset`이 소문자 64자리 컨테이너 ID를 최초 한 번 결속한다. 같은 값만 멱등 재호출 가능하다. 실제 Docker ID와 사전 정책 참조를 같다고 가정하지 않는다.

## 2. 정책 JSON 필드

표에서 **필수**는 A와 B의 닫힌 형식에서 키가 있어야 한다는 뜻이다. `audit_key`만 A에서 금지, B에서 필수다. 모든 필수 필드의 `null`은 거부하며 아래 표의 예외만 허용한다. `ValidateE2HTTPPolicyDocument`는 형식·정적 의미를 검사한다. **실행 검증** 열의 factory는 `LoadE2HTTPRuntimeOptions`의 파일·권위 검사, proxy는 `e2proxy.ValidatePolicyTemplate` 및 실제 요청 경계다. 시각·파일·권위의 유효성은 지문과 별도로 매회 검사한다.

| JSON 필드 | 타입·필수/생략/null·범위 | 공급·고정·검증 | 내용 지문 |
|---|---|---|---|
| `format` | 문자열 필수, `abc2lab.e2-http-host-policy/v1` | 생산자, A. 문서 API·factory | 형식 자체 대신 content format v1 |
| `approval_mode` | 문자열 필수, 평가에서는 `preauthorized_scope` | 생산자, A. 문서 API·factory·proxy | 포함 |
| `run_ref`, `reset_ref`, `policy_ref`, `case_ref` | 각각 비어 있지 않은 참조 문자열 필수, `[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}` | 신뢰 host/생산자, 준비 전. factory·실제 scope/BindReset 재검증 | 제외 |
| `policy_revision`, `budget_ref`, `owner_ref`, `account_ref`, `session_ref` | 같은 참조 규칙, 각각 필수 | 신뢰 host/생산자, 준비 전. factory·실행/예산 결속 | 제외 |
| `valid_from`, `valid_until` | RFC3339 시각 문자열 필수. 시작 ≤ 현재 < 종료, 시작 < 종료 | 승인 공급자, 준비 전. 문서 API·factory·실행 만료 검사 | 제외 |
| `max_requests` | `uint64` 정수 필수, 1…`math.MaxInt` | 생산자, A. 문서 API·factory·Ledger | 포함 |
| `header_mode` | 문자열 필수, `preserve`, `evaluation_caller_passthrough`, `protected_session_proposal` 중 하나. 빈 문자열 거부 | 생산자, A. 문서 API·proxy·준비 설정과 backend 대조 | 포함 |
| `evaluation` | 객체 또는 `null` **필수**. 평가 전달 모드는 객체 필수, 그 외는 `null` | 생산자, A. 문서 API·proxy·준비 설정/실행 대조 | `llm_backend`, `target_kind` 포함, `run_ref` 제외 |
| `state_dir` | 비어 있지 않은 절대 보호 디렉터리 경로 문자열 필수 | 신뢰 배치, 준비 전. factory는 현재 UID 소유 0700·calls 경로 검사 | 제외, 사용 직전 별도 검사 |
| `helper`, `helper_sha256` | 비어 있지 않은 helper 절대 경로와 소문자 hex 64자리 필수 | 신뢰 배치, 준비 전. factory는 일반 실행 파일·소유/권한·최대 64MiB·정확한 digest 검사, 획득 시 재검사 | 제외, 실제 바이너리 pin 별도 |
| `ca_cert`, `ca_key` | 아래 파일 pin 객체 각각 필수 | 신뢰 배치, 준비 전. factory는 보호 파일·digest·동일 CA 인증서/개인키·CA 유효기간·서명 용도 검사 | 제외, 실제 TLS 자료 별도 |
| `audit_key` | A에서 **키 금지**, B에서 파일 pin 객체 필수. pin 대상은 정확히 32바이트 | Python 준비기가 회차마다 생성. `e2config`와 standalone은 경로·바이트·pin 대조, factory는 보호 파일 검사 | 제외 |
| `upstream_ca` | 파일 pin 객체 선택. 생략 또는 `null` 허용 | 신뢰 배치, 준비 전. 있으면 factory가 PEM root를 로드 | 제외, TLS 검증 별도 |
| `rules` | 객체 배열 필수, 1…128, 첫 일치 순서 보존 | 생산자, A. 문서 API·proxy·요청마다 실제 규칙 | 순서대로 포함 |
| `limits`, `proxy`, `runtime` | 아래 객체 각각 필수, `null`/빈 객체 거부 | 생산자, A. 문서 API·proxy/factory | 값 포함 |

참조 문자열에는 URL·슬래시·공백을 넣지 않는다. `policy_ref` 같은 참조의 **값**은 지문에서 빠져도 실제 승인 scope·예산·감사에서 대조한다. `approval_display`는 현재 확인 UI의 표시 데이터이고 평가의 `preauthorized_scope`에 승인 역할을 하지 않는다.

| 중첩 JSON 필드 | 타입·필수/생략/null·범위 | 검증·집행 | 내용 지문 |
|---|---|---|---|
| `*.file`, `*.sha256` (pin) | 문자열 각각 필수, 파일은 단일 basename·참조 규칙, digest는 소문자 hex 64자리 | 문서 API 형식, factory 보호 파일의 정확 바이트 SHA-256. symlink·hardlink·잘못된 권한 거부 | 제외 |
| `evaluation.run_ref` | 참조 문자열 필수, 정책 `run_ref`와 동일 | proxy; 준비 설정에는 이 필드 없음 | 제외 |
| `evaluation.llm_backend`, `target_kind` | 문자열 필수, 각각 `local`/`api`, `public_practice` | proxy·backend와 준비 설정 대조 | 포함 |
| `rules[].ref`, `path_scope_ref` | 참조 문자열 각각 필수, `ref`는 규칙 사이 중복 금지 | proxy | 제외 |
| `rules[].origin` | 문자열 필수, `http` 또는 `https`의 정확한 host[:port] origin. userinfo·query·fragment·경로(루트 `/` 표기 예외) 금지; 포트 1…65535 | proxy가 host 소문자·기본 포트를 정규화해 승인 대상과 대조 | 포함(정규화된 scheme·host·port) |
| `rules[].connect_ip` | 문자열 필수, 유효한 단일 IP. unspecified·multicast·zone 금지 | proxy는 IP pin으로 실제 dial; factory가 파싱 | 제외, 매회 별도 검증 |
| `rules[].path_prefix` | 문자열 필수, `/` 또는 clean 절대 경로. 역슬래시·`%`·CR/LF·`//`·dot segment 금지; 끝 `/`은 정규화 | proxy는 요청의 clean path를 exact 또는 **세그먼트 경계** prefix로 비교 | 포함(원문 prefix) |
| `rules[].methods` | 비어 있지 않은 문자열 배열 필수, 중복 금지. `GET HEAD POST PUT PATCH DELETE OPTIONS`만 허용 | proxy는 실제 method를 확인. GET도 비변경 보장 아님 | 배열 순서대로 포함 |
| `rules[].credentials` | 파일 pin 객체 선택, 생략/`null` 허용. 현재 `protected_session_proposal`에서만 유효 | factory는 최대 32KiB 보호 JSON `{"headers":{…}}`을 읽고 proxy는 헤더 이름·값·충돌 검사 | 헤더 **이름만** 정규화·정렬해 포함, 값 제외 |
| `rules[].approval_display` | 객체 필수. `public_path` bool 필수, `query_keys`/`json_fields` 문자열 배열 필수(빈 배열 또는 `null` 가능) | 확인 UI 표시용. 평가 자동 승인 권한 아님 | 제외 |

규칙은 `ref` 순서가 아닌 **배열 순서**로 평가한다. `/a`는 `/a`와 `/a/…`에 일치하며 `/ab`에는 일치하지 않는다. `/`는 같은 origin의 모든 clean 경로를 뜻한다. 요청의 `RawPath`가 있거나 `%` 등 해석 차이가 있으면 거부한다. QUERY는 경로 prefix와 별개로 실제 요청에 남고, 지원 요청의 Host·path·query·본문·일반 헤더는 임의로 재작성하지 않는다. 프록시는 전송 framing과 E② attempt 표식만 통제한다. 자동 리다이렉트 추종/재시도는 없고 이후 새 요청은 다시 정책을 통과해야 한다. TLS 대상 인증서 검증 오류는 우회 없이 실패한다. HTTP/1 요청·응답만 지원하며 무검사 CONNECT 터널, Upgrade/WebSocket, HTTP/2 및 미지원 도구 출구는 허용하지 않는다. HTTPS CONNECT는 E②가 TLS를 종료해 내부 HTTP 요청을 검사하는 한정 경로다.

### 한도와 단위

| JSON 필드 | 타입·허용 범위 | 실행 검사·지문 |
|---|---|---|
| `limits.max_attempts` | `uint64` 1…40000 | proxy, 포함 |
| `limits.max_calls` | 정수 1…1000 | proxy, 포함 |
| `limits.max_concurrent` | 정수 1…100 | proxy, 포함 |
| `limits.max_body_bytes` | 정수 1…16777216 **바이트** | proxy, 포함 |
| `limits.request_timeout_ms` | 정수 1…120000 **ms** | factory에서 Duration 변환, proxy, 지문에는 ns |
| `limits.call_timeout_ms` | 정수 1…3600000 **ms** | factory·proxy, 지문에는 ns |
| `limits.max_denials`, `max_transient`, `max_unknown` | `uint64` 각각 1 이상 | proxy, 포함. 시도 상한이 추가로 유효 |
| `proxy.header_timeout_ms`, `request_timeout_ms` | 정수 각각 1…60000 **ms**, header ≤ request | factory·proxy, 지문에는 ns |
| `proxy.max_header_bytes` | 정수 1…1048576 **바이트** | factory·proxy, 포함 |
| `proxy.max_connections` | 정수 1…128 | factory·proxy, 포함 |
| `runtime.max_worker_turns`, `max_tool_calls` | 정수 각각 1…1000 | factory·지문. 독립 backend는 현재 `max_tool_calls`만 명령 단위 예약 |
| `runtime.worker_timeout_ms` | 정수 1…3600000 **ms** | factory·지문 ns |
| `runtime.max_planner_rounds` | `uint64` 1…100 | factory·지문 |
| `runtime.max_intent_runs` | `uint64` 1…10 | factory·지문 |
| `runtime.drain_timeout_ms` | 정수 1…120000 **ms** | factory·지문 ns |
| `runtime.output_limit` | 정수 1…4194304 **바이트** | factory·지문 |

`max_requests`와 `max_attempts`는 별개다. 서로의 값으로 자동 대체하지 않는다. 독립 backend의 실제 Planner round·Worker turn·intent-run 상한 연결은 아직 없으므로 **지문에 포함됨**과 **실제 집행됨**을 구분한다.

## 3. 준비 설정 `abc2lab.e2-runner-config/v1`

12개 키가 **모두 필수**이고 미지·중복 키·금지된 `null`을 거부한다. `evaluation`만 비평가 전달 모드에서 명시적 `null`을 허용한다. `e2config.Load`가 읽는 보호 설정은 최대 64KiB, 정책은 최대 256KiB다.

| JSON 필드 | 타입과 값 | 공급·검증 |
|---|---|---|
| `format` | 고정 문자열 `abc2lab.e2-runner-config/v1` | Python, Go Load |
| `eval_run_id`, `reservation_ref`, `model_tag` | 참조 문자열, `[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}`, `://` 금지 | 러너, Go Load |
| `header_mode` | 정책과 일치하는 위 3개 문자열 중 하나 | 러너, Go Load·backend |
| `evaluation` | 전달 모드는 정확히 `{"llm_backend":"local|api","target_kind":"public_practice"}`, 그 외 `null` | 러너, Go Load·backend. **`run_ref`를 넣지 않음** |
| `policy_file`, `audit_key_file` | 서로 다른 단일 basename, `[A-Za-z0-9][A-Za-z0-9_.-]{0,127}`, config 이름과도 다름 | Python, Go Load·backend |
| `listen` | `:<1…65535>` 정규 10진 포트 | Python, Go Load |
| `upstream` | 정확히 `http://eval-meter-upstream:<같은 포트>` | Python, Go Load |
| `evidence_dir`, `control_dir` | 현재 UID 소유 0700 보호 디렉터리 경로 | 배치, Go Load |

설정·정책·키는 현재 UID 소유의 0600 일반 파일, 준비·제어·증거 디렉터리는 0700이고 서로 중첩하지 않아야 한다. 로더는 symlink/hardlink·불안전한 조상·파일 교체와 원본 SHA 변화를 거부한다. Python 준비기는 기본 정책을 읽을 때 symlink·중복 JSON 키·NaN·크기 초과를 거부하고 **이미 있는 `audit_key`를 거부**한다. 그러나 Python은 기본 정책의 모든 Go 필드·상한·자원 pin을 검사하지 않는다. 생산자는 A 검사와 B의 실제 Go 소비를 둘 다 통과시켜야 한다.

## 4. 내용 지문 정규화

`e2export.PolicyFingerprint(cfg, maxRequests, behavior)`만 쓴다. `ValidatePolicyTemplate`·한도 검사를 통과하지 않으면 digest가 없다. 내부 `canonicalPolicy`는 `encoding/json.Encoder`의 map 키 사전순 정렬, HTML escape 끔, UTF-8 Go JSON 문자열 escape, 줄바꿈 없음, 정수 10진수로 `abc2lab.e2-policy-content/v1` 객체를 인코딩하고 SHA-256 소문자 hex를 낸다. 서버는 정책의 `*_ms`를 `time.Duration`으로 바꾼 뒤 지문에 **ns 정수**로 넣는다. 빈 HeaderMode의 Go 기본값은 `preserve`지만 평가 문서는 명시값만 허용한다.

규칙·메서드·일반 헤더 값 배열은 입력 순서를 보존한다. origin은 scheme·소문자 host·숫자 포트(기본 80/443)로, 일반/자격 헤더 이름은 Go HTTP canonical name으로 정규화한다. 자격 헤더 이름은 정렬하고 값은 제외한다. 일반 헤더의 제약 **값**은 포함한다. 고정 transport/tool 목록과 버전도 투영된다. `run_ref`·task/reset/참조·절대 시각·IP pin·CA/helper/감사 키·자격 원문은 제외하며 실제 집행 시 따로 확인한다. 경로 prefix·origin·규칙 순서·메서드 순서/값·모드·평가 선언·예산·proxy/runtime 상한이 바뀌면 지문을 다시 계산하고 예상값을 재검토·동결한다. `Restrict`의 정책 변경은 현재 버전에서 ready 지문만으로 자동 인증되지 않는다.

합성 Go 벡터 `e2export/policy_test.go:policyFixture`의 지문은 `e9f33de9a7d59ca273a97abe3e9e33c20beef552b6b4e3687acc7a53f35d6f0c`다. 이 벡터는 Go `TestPolicyFingerprintGoldenVector`가 실제 encoder로 고정한다. 실제 회차의 비밀·경로가 섞일 수 있는 canonical 입력 바이트를 공유 산출물에 게시하지 않는다.

## 5. 재현 가능한 생산자 벡터와 실패 사례

`go test ./server -run 'TestE2EvaluationPolicyDocumentStages|TestE2EvaluationPythonPrepareGoConsumer' -count=1`은 실제 Go 타입이 만든 **합성 CA·helper·정책**을 사용한다. 첫 시험은 A에서 `audit_key`를 제거하고 A/B 형식, 미지·중복 키, 필수 누락, 금지 `null`, 타입, 승인 모드, 잘못된 method/path/origin, 상한·크기 초과를 검사한다. 둘째는 Python `e2_prepare.py`가 A를 읽어 키·pin·B·준비 설정을 쓰고, Go `e2config.Load` → 문서 API → factory → 독립 backend가 이를 소비하고 정확한 바이트 digest·감사 pin·예상 지문 불일치를 확인한다. 시험 프로세스의 보호 mount 경로만 로컬 임시 디렉터리로 바꿔 `e2config.Load`에 공급한다. 실제 배치의 `/evidence`·`/run/e2-control` mount와 동일하다는 주장은 아니다. 합성 키·CA 원문은 저장소에 넣지 않는다.

`go test ./server -run TestE2EvaluationPolicyProducerGoConsumer -count=1`은 A0에서 시작해 러너 생산자·실제 `e2-eval-policy`·`e2config`·factory·독립 backend까지 잇고, 오프라인 예상 지문이 실제 factory 값과 같은지 대조한다.

기존 `e2config` 시험은 잘못된 설정 키·타입·권한·symlink·정책 중복 키·감사 키 길이·교체를, `server` standalone 시험은 감사 pin mismatch와 단회 bind를, `e2export` 시험은 내용 지문 안정성·의미 변경·비밀 값 제외를 추가로 검증한다. 정책 파일 SHA가 같아도 승인 권위·실제 IP/CA·task/intent·Docker ID와 서비스 격리가 맞지 않으면 시작/송신을 거부한다.

## 6. 구현 상태와 충돌 정리

- `e2export/policy.go`의 “검토용 제안” 주석과 연결 제안 §4.1의 “후속 encoder 구현”은 **현행 코드보다 오래된 상태 설명**이었다. 이번 PR은 같은 `PolicyFingerprint` encoder를 golden 벡터로 고정하고 주석을 계약에 맞췄다. 제안 문서 전체를 구현 완료로 승격하지 않는다.
- 기존 `e2config.Load`는 정책 JSON의 중복 키와 `approval_mode`만 검사했다. `server.ValidateE2HTTPPolicyDocument`가 A/B의 정확 키·null·정적 범위를 검사한다. 실제 보호 파일·인증서·helper·권위 검사는 여전히 factory와 backend 책임이다.
- Python 준비기의 예전 합성 `{"approval_mode":…,"rules":["synthetic"]}` fixture는 **Go 정책 소비자의 유효 예시가 아니다**. 이번 Go/Python 벡터는 실제 Go 타입에서 생성한 유효 정책을 사용한다. 준비기 자체의 제한적인 입력 검사 시험은 그 역할대로 유지한다.
- 평가 회차는 `confirm_every_request`를 자동 승인으로 바꾸지 않고 `preauthorized_scope`를 사용한다. `protected_session_proposal`은 기존 parser 어휘이나 공식 평가용 기능 채택·배치 허가가 아니다.
- 독립 `eval-e2` 바이너리/이미지, 인증된 launcher↔E② OpenCall/CloseCall, 실제 task/Planner 연결, Worker/Planner 상한 집행, 공식 계측·GT·모델/대상 수용은 별도 작업이다. #340/#341이나 이 문서의 정적 검증 통과가 이 항목을 완료시키지 않는다.

예상 지문은 `scripts/eval/e2_policy_fingerprint.py`로 A0에서 오프라인으로 계산해 공식 프로파일의 `e2.policy_fingerprint_sha256`에 둔다. 서비스 응답을 예상값으로 쓰지 않는다. 자리값은 지문에 들어가지 않는 필드에만 쓴다. 11월 23일 인계 전에 인증 채널, Planner/Worker 한도 연결의 담당과 수용 시험을 확정해야 한다.
