# 탐색 실행기 ↔ 탐색 경계 최소 계약 (제안)

상태: **제안 — 이동찬님과 합의 전.** 탐색 실행기는 최민준, 탐색 경계는 이동찬이 맡는다(2026-11-11 분담). 이 문서는 두 쪽이 독립적으로 구현할 수 있도록 주고받을 것만 정한다. 구현 완료나 수용시험 통과를 뜻하지 않는다.

근거: 2026-11-11 통제 범위 재정의. 탐색(크롤링·브라우저 정찰)은 E②(실제 전송 통제)를 거치지 않고 가벼운 탐색 경계만 지나며, 진단 요청만 E②를 거친다(CLAUDE.md 절대규칙 1·3). 수용시험은 [탐색 경계·보존 수용시험](../evaluation/browser-acceptance.md)(#387)이다.

## 1. 범위

- **탐색 실행기**: 원본 Playwright MCP 브라우저(`@playwright/mcp`), API Recon 스크립트(`skills/api-recon/scripts`, Python·Node), 프로그램 소유 크롤. 이들과 그 자식 프로세스가 보내는 모든 네트워크 송신은 탐색 경계를 통해서만 나간다.
- **탐색 경계**: 승인 대상 밖 송신을 막는 가벼운 실행 경계다. E②의 요청별 승인, 감사 체인, 시도 ID, receipt는 만들지 않는다. 경계 기록은 운영 기록이고, E③(증거 판정)의 근거로 쓰지 않는다.
- **진단 Worker**: 탐색 경계에 닿을 수 없다. 대상에는 E②로만 닿는다(§5).

## 2. 세션 열기 (프로그램 → 경계)

탐색 세션은 서버(프로그램)만 열고 닫는다. 모델이나 탐색 도구는 세션을 만들거나 범위를 넘겨줄 수 없다.

| 입력 | 내용 |
|---|---|
| `session_ref` | 프로그램이 발급하는 탐색 세션 ID |
| `run_ref`·`job_id`·`task_id` | 실행 결속. 지금의 `run-<job_id>`(`diagnosisprep/product_workflow.go`)와 같은 값 |
| `target_ref`·`target_revision` | 승인된 대상(`ProductTarget.ID`·`Revision`). **경계가 대상 등록 저장소에서 직접 읽는다.** 실행기는 참조만 넘기고 origin·경로·메서드·주소·SPKI 값을 넘기지 않는다 |
| `write_mode` | `read_only`(GET·HEAD) 또는 `approved_writes`(대상의 승인 메서드, 탐색 시작 전 1회 승인). 11/11 크롤링 쓰기 결정 |
| `risky_paths`·`excluded_paths` | 대상 정책의 위험·제외 경로. 쓰기 모드에서도 DELETE와 위험 경로는 막는다 |
| `comm_mode` | `external_allowed`(외부 API 모드) 또는 `full_local`(완전 로컬, 외부 송신 전면 차단, 절대규칙 8) |
| 상한 | `time_budget_seconds`, `max_requests`(승인 대상으로 전달한 수), `max_external_requests`(외부 자원 수, 따로 셈), `max_concurrency`, `max_response_bytes` |

경계가 대상 저장소에서 읽는 값: origin, `path_prefixes`, `methods`, `addresses`·`selected_ip`, `port`, `tls_spki_sha256`, `expires_at`, 대상 정책의 `private_cidrs`. 실행 중 대상이 철회·만료되면 경계가 스스로 알아챈다(지금 실행기의 250ms 재확인과 같은 `registry.current` 기준).

## 3. 경계가 요청마다 하는 일

리다이렉트를 따라간 뒤의 새 요청도 같은 판정을 다시 받는다. 경계는 리다이렉트를 대신 따라가지 않는다.

경계는 입구를 둘로 나눈다. **브라우저 입구**는 브라우저(MCP가 띄운 Chromium) 샌드박스에서만 닿고, **직접 입구**는 Bash·Node·curl·API Recon 스크립트가 도는 샌드박스에서 닿는다. 입구는 발신 프로세스가 보낸 헤더가 아니라 네트워크 배치(어느 샌드박스·망에서 왔는지)로 정한다(§5). `Sec-Fetch-*` 같은 요청 헤더는 자식 프로세스가 마음대로 넣을 수 있어서(Node 기본 `fetch`도 `Sec-Fetch-Mode: cors`를 보낸다) 발신 주체의 근거로 쓰지 않는다.

1. **목적지 분류.** 경계가 직접 DNS를 풀고(실행기의 resolver를 믿지 않음) 연결할 IP를 정한 뒤 그 IP로만 연결한다(DNS rebinding 방지).
   - **승인 대상**: origin이 같고, 경로가 `path_prefixes` 안이고, 메서드가 `write_mode`에 맞고, 위험·제외 경로가 아니면 전달한다. 연결 IP는 대상의 승인 주소, TLS는 경계가 `tls_spki_sha256`로 검증한다.
   - **내부망·메타데이터**: 대상 등록과 같은 주소 필터(`productTargetRegistry.addressAllowed`: loopback, link-local(169.254.0.0/16 포함), multicast, `fd00:ec2::/64`, 등록 안 된 사설 대역)에 걸리면 막는다. 승인 대상의 주소만 예외다.
   - **제어 끝점**: E② 제어 소켓, LLM 게이트웨이, 관리 API, 평가 계측기 관측 포트는 막는다.
   - **외부 공개 호스트**: `full_local`이면 막는다. `external_allowed`이면 브라우저 입구로 들어온 요청 중 메서드가 GET·HEAD이고 `Sec-Fetch-Mode`가 `navigate`가 아닌 것만 허용한다. 페이지 스크립트는 이 헤더를 바꿀 수 없지만 자동화 계층(Playwright·CDP)은 바꿀 수 있다. 그래서 모델에 주는 MCP 도구를 고정 목록으로 하고, 요청 헤더·라우팅을 바꾸거나 Playwright·Node 코드를 실행하는 도구는 넣지 않는다(§5). 이 조건에서만 이 헤더를 브라우저 입구 안에서 이동과 자원을 가르는 데 쓴다. 직접 입구로 들어온 요청은 헤더와 상관없이 외부로 보내지 않는다(`external_direct`).
   - **한계**: 외부 통신 승인 모드에서 브라우저 입구의 외부 GET을 허용하면, 페이지 스크립트나 모델이 브라우저에서 실행한 스크립트가 query에 값을 실어 외부 호스트로 보낼 수 있다. 이 경로는 막지 않는다고 적어 둔다. 줄이는 방안은 §9.
2. **지원하지 않는 통신.** QUIC·UDP, WebRTC, 경계를 거치지 않는 TCP, 경계 밖 DNS는 네트워크 단에서 막힌다(§5). 승인 대상으로 가는 WebSocket은 §9-3에 따른다.
3. **상한.** 상한에 닿으면 그 요청은 막고(`cap_exhausted`) 세션은 §6대로 마감한다.
4. **대조 시험.** 구현은 다음 음성·양성 대조를 함께 통과해야 한다([수용시험](../evaluation/browser-acceptance.md) B11·B15·B17·B18·P08).
   - 음성: 직접 입구에서 Node 기본 `fetch`와 `Sec-Fetch-*`를 위조한 curl로 외부 CDN 역할 수신기를 부르면 도달 0건이다.
   - 음성: 직접 샌드박스에서 브라우저 입구로 연결하면 실패한다.
   - 음성: 자동화 계층(MCP 도구·CDP)으로 `Sec-Fetch-Mode`나 추가 헤더를 바꿔 외부로 이동·POST를 시도하면 닿지 않는다. 직접 샌드박스에서 CDP 포트에 연결하면 실패한다. 모델에 노출된 MCP 도구가 고정 목록과 같다(B18).
   - 양성: 같은 회차에 브라우저가 실제 페이지에서 외부 CDN 역할 자원을 GET하면 전달되고 렌더링에 쓰인다.

요청 하나가 막히면 그 요청만 실패한다. 탐색 세션 전체를 멈추는 것은 §6의 신호뿐이다.

## 4. 경계 기록 (경계 → 프로그램)

요청마다 한 행. 헤더 값·쿠키·본문·query 값은 남기지 않는다(절대규칙 2).

| 필드 | 내용 |
|---|---|
| `session_ref`·`seq`·`at` | 세션과 순번, 시각 |
| `decision` | `forwarded` 또는 `denied` |
| `reason` | 막은 이유. `out_of_scope_origin`, `out_of_scope_path`, `method_not_approved`, `risky_path`, `internal_address`, `metadata_address`, `control_endpoint`, `external_blocked_local`, `external_direct`, `external_navigation`, `external_method`, `cap_exhausted`, `session_closed`, `target_ended`, `ws_read_only`, `protocol_unsupported`, `login_attempts_exhausted`(같은 아이디 연속 실패 상한), `login_verification_pending`(그 계정의 예외 로그인 판정 대기 중), `browser_account_in_use`(같은 브라우저 문맥에 다른 등록 계정이 로그인해 있음) |
| `ingress` | `browser`·`direct` |
| `dest_class` | `approved`·`external`·`internal`·`metadata`·`control` |
| `method`·`status`·`bytes` | 메서드, 응답 상태, 응답 바이트 수 |
| `path_pattern` | 승인 대상 요청만. 계측기와 같은 가림(path-mask/v2). 평가·수용시험 회차에서는 계측기와 같은 키로 가린다. 제품 회차는 키 없는 표시용 패턴(값 조각만 `{id}`)이라 `{id}` 규칙에 걸리지 않는 짧은 토큰·계정명 조각이 원문으로 남을 수 있다(한계). 그래서 제품 회차의 경계 기록은 보호 저장소에만 두고 운영자 화면에서만 보이며, 모델 입력·공유 보고서·export에는 그대로 넣지 않는다(넣을 때는 DLP를 거친다) |
| `external_host` | 외부 요청만. 호스트 이름(사전 공지·보고서용) |
| `fetch_dest`·`fetch_mode` | `Sec-Fetch-Dest`·`Sec-Fetch-Mode` 값(있으면) |
| `has_auth_header` | `Authorization`·`Cookie` 유무(bool) |

이 기록이 "실제로 보냈다"의 정본이다. 탐색 경계 차단 수는 E② 진단 차단과 따로 집계한다(#387).

### 4.1 평가·수용시험 회차의 경계 원자료 (`abc2lab.explore-boundary-export/v1`)

보존 시험 서명 근거(`signed_evidence.exploration_boundary`, [서명된 보존 원자료](../evaluation/preservation-signed-runs.md))가 쓰는 원자료다. 계측기 원자료와 같은 방식으로 만든다. 경계는 서명 키를 갖지 않고, 러너가 경계 종료 뒤 최종 바이트를 읽어 hash·카운터를 계산하고 자기 키로 서명한다. 엔진은 이 원자료를 중계하지 않는다(측정 대상이 경계 근거를 바꿀 수 없게).

- **켜는 곳**: 경계 설정(`jindo.explore-boundary-config/v1`)의 `evidence_dir`·`evidence_max_bytes`를 함께 줄 때만 쓴다. 한쪽만 주면 시작하지 않는다. 바이트 상한은 회차 정책이 정하고 코드 기본값이 없다. **제품 설치는 켜지 않는다**(제품 compose의 경계는 보호 입력·제어 소켓 볼륨만 붙고, 예시 설정에 이 칸이 없다 — `scripts/ci/test_product_explore_boundary.py`). 제품 회차의 경계 기록은 export하지 않는다.
- **디렉터리 소유**(2026-11-17 개정, 결정 B): 경계는 제품 uid(1001) 그대로 뜬다. 평가 overlay(`deploy/eval/compose.split.yaml`)는 제품 서비스의 보안 칸을 바꾸지 않는다(탐색 분리 배치 결정안 §2 ②). 대신 overlay의 init(`explore-evidence-init`, root·망 없음·읽기 전용)이 원자료 볼륨 안에 경계 uid 소유 0700 빈 디렉터리(`/evidence/run`)를 만든다. 원자료 볼륨은 경계 컨테이너와 그 init에만 붙는다. 경계는 디렉터리가 정확히 0700이고 소유자가 자기 euid가 아니면, 또는 이전 회차 파일이 남아 있으면 시작하지 않는다. 엔진·브라우저·MCP·계측기에는 이 볼륨을 붙이지 않는다(렌더 시험 `scripts/ci/test_product_eval_split.py`). 이전 문장("러너가 자기 uid·0700으로 만든 디렉터리를 같은 uid로 뜬 경계가 쓴다")은 경계 uid를 러너 uid로 바꿔야 해서 버렸다. 그러면 제품 보안 칸이 달라지고, 러너 uid가 엔진 uid와 같은 머신에서는 경계/엔진 uid 분리가 사라진다.
- **꺼내기 순서**(러너): (1) 경계 컨테이너만 정지한다. 정지 신호(SIGTERM)를 받은 경계는 아래 봉인 순서대로 표식까지 쓰고 끝난다. (2) 정지를 확인한 뒤 러너 도우미 컨테이너가 원자료 볼륨을 **읽기 전용·망 없이**(경계 uid로) 붙여 두 파일을 복사한다. (3) 러너가 hash·검증·서명한다. (4) `down`. 경계가 멈춘 뒤에 복사해야 최종 바이트가 보장된다. 도우미 컨테이너는 러너 라벨로 구분하고, 구간 감시는 그 라벨이 붙은 러너 도우미만 예외로 둔다. 예외가 다른 컨테이너로 번지지 않는지는 러너 시험으로 고정한다. 실제 Docker 확인 전이다. 막히면 경계 uid를 러너 uid로 두는 안(결정안 ②에 평가 예외를 선언)과 비용을 비교해 다시 정한다.
- **파일 두 개**:
  - `boundary-records.jsonl`: 한 줄이 §4 기록 한 행 그대로다. 메모리 차단 행 상한(`max_denied_records`, 제어 소켓 `records`의 `omitted`)과 별개로 모든 행을 쓴다. 경로는 계측기와 같은 키로 가린 path-mask/v2다(평가 회차의 `path_mask_key_file`은 계측기 `-path-key-file`과 같은 키). 행마다 fsync하지 않는다. 버퍼에 쓰고 세션 마감과 봉인 때 flush·fsync한다.
  - `boundary-sealed.json`: 경계를 내릴 때(러너가 경계 컨테이너만 정지하는 SIGTERM, 또는 경계가 스스로 멈출 때) 입구·터널을 닫고, 열린 세션이 있으면 `cancelled`로 닫아 진행 중 송신을 `close_wait_ms`까지 기다린다. 그다음 아직 행을 남길 수 있는 입구 처리(송신 판정 전 본문을 읽던 핸들러, 입구를 닫기 전에 읽혀 늦게 시작한 핸들러, 터널)가 끝나길 같은 상한까지 기다린 뒤(드레인), 행 파일을 flush·fsync하고 **마지막에** 원자적으로 쓴다. 세션을 먼저 닫으므로 드레인 중 새 송신은 없고 그 요청들은 차단 행으로 남는다. 칸: `format`, `path_profile`, `path_key_ref`, `max_bytes`, `session`(결속 7개 — `session_ref`·`run_ref`·`job_id`·`task_id`·`target_ref`·`target_revision`·`source_digest` — 와 `opened_at`·`closed_at`·`closed_reason`·`rows_at_close`·`closed_at_seal`, 세션을 열지 못했으면 `null`), `sealed_at`, `sealed_by`(`shutdown`·`failure`), 실제로 쓴 `rows`·`bytes`·`rows_sha256`, `truncated`·`rows_not_written`, `inflight_at_seal`, `log_complete`.
- **세션 하나**: 원자료 디렉터리 하나에는 세션 하나만 남긴다. 세션을 한 번 받은 뒤의 `open`은 `unavailable`로 거부한다(평가는 회차마다 경계를 새로 띄운다).
- **잘림·실패**: 행 파일이 `evidence_max_bytes`에 닿으면 그 뒤 행은 쓰지 않고 `rows_not_written`만 센다. 송신은 멈추지 않는다(회차 판정은 러너가 `UNDECIDABLE`로 둔다). 쓰기·flush·fsync가 실패하면 원자료가 불완전해진 것이라 그 세션을 `boundary_unavailable`로 닫아 새 송신을 멈추고 표식에 `log_complete=false`를 남긴다. 봉인 때 끝나지 않은 송신과 드레인 상한 안에 끝나지 않은 입구 처리는 원자료에 행이 없을 수 있고, 그 합이 `inflight_at_seal`이다(0이 아니면 원자료가 불완전하다). 표식이 없으면 경계가 봉인 전에 죽은 것이다.
- **러너가 하는 일**(검증·대조 규칙·상태값은 [서명된 보존 원자료](../evaluation/preservation-signed-runs.md)와 [수용시험 P03](../evaluation/browser-acceptance.md)): 표식의 카운터를 믿지 않고 행에서 다시 센다. 결속은 러너가 만든 작업과 맞아야 하고, `path_key_ref`는 계측기 경로 행과 같아야 한다. 차단 수는 E② 진단 차단·반복 차단 수와 따로 싣고 합치지 않는다. 원자료를 못 쓴 회차의 차단 수를 0으로 채우지 않는다.
- 이 원자료와 계측기 대조는 탐색 입구 트래픽이 경계를 지났는지의 장부 대조다. 네트워크 격리 증명이 아니다(격리는 배치 검사·B16 프로브가 따로 본다).

### 4.2 평가 배치의 경계 upstream (`eval_upstream`)

평가 러너의 탐색 분리 배치(결정안 #624 `docs/evaluation/exploration-split-decision.md` §2)에서는 경계가 승인 대상에 직접 닿지 않고 계측기 탐색 입구를 거쳐야 한다. 계측기가 탐색 트래픽을 진단 트래픽(본 입구)과 나눠 세고 경계 원자료와 대조(P03)하기 위해서다.

- 경계 설정 `eval_upstream`(IP:포트 리터럴)을 주면, 판정이 승인 대상으로 보낸 **http** 요청만 대상 주소 대신 이 주소로 연결한다. 요청은 origin-form이고 `Host`는 승인 대상 그대로다. 판정·기록·상한은 바뀌지 않고 연결 주소만 바뀐다. 외부 자원은 판정 주소 그대로다.
- 평가 upstream은 평문이라, 이 모드에서 승인 대상 **https** 요청은 판정 전에 `protocol_unsupported`로 막고 기록한다. 대상 주소로 우회하지 않는다(계측기 본 입구로 새면 입구 대조가 깨진다). 평가 배치의 대상은 http로 등록한다(C22).
- `eval_upstream`은 원자료 칸(`evidence_dir`·`evidence_max_bytes`, §4.1)과 같이 줄 때만 받는다. 원자료 없이 주거나, 이름·포트 0·지정되지 않은 주소·멀티캐스트·zone·경계 입구 자신·차단할 제어 통로를 가리키면 경계가 시작하지 않는다.
- 제품 설정에는 넣지 않는다. 제품 예시 설정·README·compose에 이 칸이 없음을 `scripts/ci/test_product_explore_boundary.py`가 확인한다. 제품 경계는 지금처럼 판정 주소로만 연결하고 https 대상의 TLS·SPKI 고정을 그대로 둔다.

## 5. 실행 환경 분리

- 탐색 실행기 프로세스(브라우저, MCP, API Recon, 자식 프로세스)는 경계 하나만 닿는 네트워크 namespace에서 돈다. 브라우저는 브라우저 입구만, Bash·Node·API Recon은 직접 입구만 닿는 서로 다른 샌드박스에 둔다. 직접 샌드박스에서는 브라우저 프로세스의 명령줄·환경·프로필을 읽지 못한다.
- MCP 서버(Node) 프로세스는 Chromium과 다른 컨테이너에 두고 CDP로 Chromium을 조작한다(§9-1, #485). MCP 컨테이너는 브라우저 입구에 닿지 않으므로 브라우저 입구의 발신 주체는 Chromium뿐이다. 다만 MCP는 CDP로 Chromium이 보낼 요청을 바꿀 수 있으므로, 모델에 노출하는 MCP 도구는 실행기 설정의 고정 목록(§5.1)으로 하고, 요청 헤더·라우팅을 바꾸는 도구와 Playwright·Node 코드를 실행하는 도구는 뺀다. 페이지 안에서 스크립트를 실행하는 도구는 금지 헤더를 바꾸지 못하므로 남길 수 있다(§3 한계는 그대로). CDP는 Chromium 컨테이너와 MCP 컨테이너만 붙는 연결망에만 열리고, 직접 샌드박스·엔진·경계에서 닿지 않는다. 프록시 환경 변수에 기대지 않는다(Node `fetch`는 기본으로 프록시 변수를 따르지 않고, API Recon은 TLS 검증을 끄고 Node에서 직접 보낸다).
- 탐색 실행기 환경에는 제어 비밀(LLM API 키, E② 권한 토큰, 감사 키, 러너 서명 키, CA 개인키, 관리 API 토큰)을 넣지 않는다. 지금 원본 MCP는 서버 환경 변수를 통째로 물려받으므로(`norma` stdio `os.Environ()`) 실행기가 환경을 새로 만든다.
- 진단 Worker의 격리 실행(`e2sandbox`)에서는 경계에 닿지 않는다. 탐색 실행기에서는 E②에 닿지 않는다.
- 브라우저가 승인 대상 HTTPS를 열 수 있게 경계 CA를 브라우저와 Node에 신뢰시킨다. 경계는 대상 쪽 TLS를 SPKI로 검증하므로, 실행기 쪽의 TLS 검증 끄기(API Recon)가 대상 검증을 대신하지 않는다.

### 5.1 MCP 도구 고정 목록 (제안, 제품 핀 `@playwright/mcp@0.0.84`)

출처는 그 버전 패키지 README의 도구 절이다(제품 이미지 `deploy/product/Dockerfile`이 0.0.84로 고정). 버전을 올리면 이 목록을 다시 본다.

이 목록과 수용시험 B17·B18의 기준 이미지는 제품 이미지(0.0.84)다. 공식 평가 이미지(루트 `Dockerfile`)는 `@playwright/mcp@latest`라 빌드마다 버전이 바뀔 수 있다. 그래서 실행기는 실제 MCP 버전과 모델에 노출한 도구 목록을 회차 기록에 남기고, 버전이 0.0.84가 아니면 이 목록을 그 버전으로 다시 보기 전까지 그 회차를 보존 측정·공식 회차에 쓰지 않는다. 이름이 바뀐 허용 도구가 조용히 빠져 측정에 섞이는 것을 막기 위해서다. 루트 `Dockerfile`을 고정할지는 원본 이미지와의 도구 트리 동등성(프로토콜 §2)과 함께 따로 정한다.

- **허용**(기본 묶음 중): `browser_navigate`, `browser_navigate_back`, `browser_tabs`, `browser_click`, `browser_hover`, `browser_drag`, `browser_type`, `browser_fill_form`, `browser_select_option`, `browser_press_key`, `browser_handle_dialog`, `browser_wait_for`, `browser_snapshot`, `browser_find`, `browser_take_screenshot`, `browser_resize`, `browser_emulate_media`, `browser_console_messages`, `browser_network_requests`, `browser_evaluate`, `browser_close`.
- **제외**:
  - `browser_run_code_unsafe` — MCP 서버 프로세스에서 임의 JS를 실행한다(README: "RCE-equivalent"). 헤더를 바꾸거나 Node에서 직접 보낼 수 있어 입구 분리(§3)를 무너뜨린다. 0.0.84에서는 기본 묶음에 들어 있고 끄는 CLI 옵션이 문서에 없으므로, 실행기가 모델에 노출하기 전에 뺀다.
  - `browser_file_upload`·`browser_drop` — 샌드박스의 파일 경로를 대상에 올린다. 쓰기이고 탐색에 필요하지 않다.
  - `browser_network_request` — 요청·응답의 헤더와 본문 원문(쿠키·`Set-Cookie` 포함)을 돌려주고 파일로도 저장한다. 비인증 방문에서도 서버가 세션 쿠키를 발급하므로 S09 조건에 맞지 않는다. 요청 목록(`browser_network_requests`)만 남긴다.
  - 페이지가 WebMCP로 등록하는 도구 — 0.0.84는 기본으로 모아 노출한다(README: "Enabled by default"). 설명과 결과를 대상이 정하므로 프롬프트 주입 통로가 된다. `--no-webmcp`로 끈다.
  - `--caps`로 켜는 묶음 전부(`network`·`storage`·`config`·`devtools`·`vision`·`pdf`·`testing`). 특히 `browser_route`는 요청 헤더를 지우거나 응답을 바꾸고, 쿠키·저장소 설정은 프로그램의 인증 준비(§8)가 맡는다. `--caps`는 넘기지 않는다.
- **허용하되 알고 쓰는 것**:
  - `browser_evaluate`는 페이지 안에서 JS를 실행한다. 금지 헤더는 바꾸지 못하지만 §3의 외부 GET 한계는 그대로다.
  - 허용 도구의 출력은 가리지 않고 모델 입력·저장 기록으로 간다(2026-11-26 DLP 폐기, CLAUDE.md 절대규칙 2: 모델 입력에 대상 응답·시험 계정 자격·대상이 발급한 세션 값을 넣을 수 있고 제품 기록도 가리지 않는다). 탐색 모델이 직접 로그인하므로 로그인 응답의 토큰·쿠키를 읽어야 한다. 탐색에는 우리 제어 비밀이 없다. 비인증 회차에서도 심은 표식의 노출 0건을 확인한다.

**11/23 규칙 변경(사용자 결정)**: 대상에서 온 정보(대상 응답·도구 출력, 대상이 돌려준 비밀번호·개인정보 포함)는 외부 모델로 나가도 되고, 그 책임은 사용자에게 있다고 공지·동의 문장에 넣는다. 그래서 탐색 경계는 응답에 비친 보호 값을 이유로 응답을 보류하지 않는다(보호 값 보류 작업은 취소). 남는 일은 저장하는 곳에서 가리는 것이다. 지금 저장하는 곳과 가림은 다음과 같다.

| 저장하는 곳 | 응답 내용 | 가림 |
|---|---|---|
| 경계 기록(아래 표) | 저장하지 않음. 상태·바이트 수·형식·해시만 | 해당 없음 |
| 발견 보고(`report_discovery`) | 메서드·경로·이름만(값 없음). 프로그램이 범위를 다시 검사 | 해당 없음 |
| 탐색 활동 기록(도구 결과·모델 글·도구 입력)·감사 추적 | 있음(모델에 준 것과 같은 글) | 없음(2026-11-26 DLP 폐기). 이전에는 `dlp.CredentialText`(자격 꼴) 뒤에 이 회차 시험 계정 비밀번호 정확값 가림을 했고, 자격 꼴 가림은 모델 입력에도 걸렸다 |
| 모델 요청 기록(`llm_records`) | 없음. 설정 `llm_record`를 켜도 메시지·시스템·도구·응답 칸은 `[REDACTED]`로 바꿔 저장하고 토큰 수·`max_tokens` 같은 메타데이터만 남는다(`llmrec/dlp.go` `requestMetadata`, 시험 `llmrec/dlp_cases_test.go`) | 해당 없음. 기본 꺼짐이고, 제품 서버(`NewFirstDiagnosisManager`)는 이 설정을 읽지 않아 늘 꺼짐이며 제품 관리 경로는 설정·기록 API를 내주지 않는다(404) |

**회차별 비밀번호 목록(2026-11-24 사용자 결정, 2026-11-26 DLP 폐기로 없앰)**: 아래는 이전 기록이다. 짧은 비밀번호 계정 제외도 같이 없앴다(탐색·진단 모두 결속 계정을 모델에 준다). 제품 실행은 E② 인증 준비가 읽는 것과 같은 실행 자료(결속 계정 자격 사본, 메모리)에서 이 회차 시험 계정 비밀번호 목록을 만들어 탐색 실행기의 저장 기록 가림에 넘긴다. 조건은 E②와 같다: 메모리에만 두고, 회차 단위로 만들고, 회차가 끝나면(실행 자료 정리) 지우며, 로그·디스크에 쓰지 않는다. E②가 등록하지 않는 값(6바이트 미만 등, `e2proxy.ValidProtectedPassword`)은 넣지 않는다 — 그 계정은 E②도 로그인하지 않는다. 결속 계정의 자격 사본을 읽지 못하면 목록을 만들지 못한 것이라 탐색을 열지 않는다. 회차가 끝난 뒤 들어온 기록은 통째로 가린다. 모델 입력은 가리지 않는다(11/23 규칙).

우리 통제 비밀(모델 API 키, 관리자 비밀번호, 경로 가림 키, E③ 표식 값, 승인 ID 등)은 탐색 모델 입력에 넣지 않는다.
- **CLI 옵션**: 실행기(서버)가 정하고 모델이 바꿀 수 없는 옵션만 쓴다. 공통은 `--headless`, `--isolated`, `--no-webmcp`, `--browser chromium`, `--proxy-server <브라우저 입구>`, `--output-dir <브라우저 샌드박스 안>`이다. `--browser`의 기본값 `chrome`은 이미지 밖 Google Chrome(`/opt/google/chrome`)을 찾으므로, MCP 자신의 설치 명령(`playwright-mcp install-browser chromium`)으로 깐 그 판의 Chromium을 고른다. §9-1 배치에서는 `--port <MCP 포트>`, `--host <MCP 컨테이너의 제어망 주소>`, `--allowed-hosts <엔진이 접속하는 호스트:포트>`를 더한다. 세 값은 한 묶음이다. 엔진의 MCP `http` URL은 `http://<allowed-hosts 값>/...`이고, 그 이름이 `--host` 주소로 풀려야 한다. 셋 중 하나라도 어긋나면 MCP 컨테이너를 띄우지 않는다. MCP는 Chromium을 띄우지 않고 `--cdp-endpoint http://<Chromium 컨테이너의 CDP 연결망 IP>:<포트>`로 붙는다. 이름이 아니라 IP를 쓴다(Chromium DevTools가 IP·localhost가 아닌 Host 헤더를 거부한다). 이 배치에서 `--headless`·`--browser`는 Chromium 실행 조건을 바꾸지 않고(Chromium 실행기가 정한다), `--isolated`는 연결마다 새 브라우저 문맥을 만들며, `--proxy-server`는 그 문맥의 프록시가 된다(0.0.84가 `Target.createBrowserContext`의 `proxyServer`로 넘기고 `<-loopback>`을 붙인다). 그래서 MCP의 `--proxy-server`는 Chromium 실행기의 `--proxy-server`와 같아야 한다. Chromium 실행기(`deploy/product/explore-chromium.js`)는 `--proxy-server`와 `--cdp-listen` 두 옵션만 받는다. 이 목록 밖의 옵션은 넣지 않는다. `--ignore-https-errors`(경계 CA를 신뢰시킨다), `--init-page`(Playwright 페이지 객체에 TypeScript 실행), `--caps`는 쓰지 않는다. `--allowed-origins`·`--blocked-origins`·`--secrets`는 README도 보안 경계가 아니라고 하므로 통제 근거로 쓰지 않는다.
- **거르는 곳**: 지금 MCP 연결(`server/mcpdiscover.go`·`server/assembly.go`)에는 도구 단위 허용 목록이 없다(`allowed_tools`·`disabled_tools`·`tool_filter` 검색 0건). 실행기가 MCP 도구를 모델에 노출하기 전에 이 목록으로 거른다(구현 최민준). 처음 받은 목록뿐 아니라 MCP 서버가 도구 목록 변경을 알릴 때마다 다시 거른다. 목록에 없는 도구는 버전이 바뀌어도 노출하지 않는다(기본 거부). 허용 이름과 같은 이름을 다른 출처(WebMCP 등)가 등록해도 MCP 서버 기본 도구가 아니면 노출하지 않는다. B18은 WebMCP 도구를 등록하는 fixture 페이지를 연 뒤에도 노출된 도구가 이 목록과 같은지 확인한다.

## 6. 중지·종료 신호

| 신호 | 경계 | 실행기 |
|---|---|---|
| 사용자 중지·실행 취소 | 새 연결 거부, 유예 뒤 열린 연결 종료 | 브라우저·MCP·자식 프로세스를 프로세스 그룹째 종료 |
| 대상 철회·만료 | 스스로 감지해 `target_ended`로 거부 | 같은 종료 |
| 시간·요청 상한 도달 | `cap_exhausted`로 거부, 세션 마감 | 탐색 마감. **실행 실패가 아니다.** 찾은 것으로 진단 단계로 넘어간다 |
| 경계 장애 | 실행기에 연결 불가로 보임 | 탐색 마감(`boundary_unavailable`). 진단 단계는 E②로 계속할 수 있다 |
| 실행기 상태 조회 끊김(준비 서비스가 죽음) | 열린 세션에 `open`·`status`가 `session_heartbeat_ms`(기본 30초) 동안 오지 않으면 스스로 `runtime_unavailable`로 닫는다. 원자료 `closed_by: control_lost` | (이미 죽음) |
| 준비 서비스 재시작(#559 자동 재시작·운영자 해제 뒤) | 새 준비 서비스의 `close`(`cause: restart_cleanup`)로 이전 세션을 `cancelled`로 닫는다. 원자료 `closed_by: restart_cleanup` | 새 탐색을 열기 전에 이전 세션을 닫고 닫힘을 확인한다. 기록해 둔 MCP 세션을 지워 남은 브라우저 문맥을 닫는다 |

### 6.1 세션 수명 (2026-11-24 사용자 결정)

탐색을 모는 모델 루프는 준비 서비스 안에서 돌지만, 경계 세션과 브라우저 문맥(MCP 세션)은 다른 컨테이너에 있어 준비 서비스가 갑자기 죽어도 남는다. 경계 세션은 제어 연결이 끊겨도 혼자 닫히지 않고, MCP는 세션을 지울 때만 브라우저 문맥을 닫는다. 그래서 남은 페이지가 스스로 보내는 요청(타이머·주기 조회·자동 새로고침)이 세션 기한(시간 예산과 작업자 시간 상한 중 짧은 쪽, 제품 기본 최대 20분)까지 대상에 닿을 수 있었다. 브라우저 컨테이너는 내부 전용 망(`explore_browser_ingress`·`explore_cdp`)에만 붙어 대상에 가는 길이 경계뿐이고, 경계는 열린 세션이 없으면 CONNECT·평문 요청을 `session_closed`로 거부하며 닫힐 때 진행 중 요청과 터널을 끊는다. 따라서 경계 세션을 닫으면, 새 탐색 세션이 열리기 전까지 남은 페이지는 대상에 닿지 못한다. 경계 브라우저 입구는 세션별로 묶이지 않고 그때 열린 세션으로 판정하므로(`exploreboundary/proxy.go`), 남은 브라우저 문맥을 지우지 못한 채 새 세션이 열리면 그 페이지 요청은 새 세션의 승인 범위·상한 안에서 나가 새 회차의 요청 수·기록에 섞일 수 있다(승인 범위 밖으로는 나가지 않는다). 그래서 문맥 지우기를 새 탐색마다 다시 시도한다.

- **세션이 끝나는 경우**: 실행기의 `close`, 시간 기한(`TimeBudgetSeconds`와 대상 승인 만료 중 이른 쪽), 요청 상한, 대상 철회, 그리고 아래 둘.
- **조회 끊김**: 실행기는 모델 호출과 따로 도는 감시 고루틴(`server/exploration_session.go` watch)에서 1초마다 `status`를 보낸다. 모델 응답이 오래 걸려도 조회는 이어진다. 경계(`Manager.Heartbeat`, 설정 `session_heartbeat_ms`, 기본 30초)는 지금 세션에 `open`·`status`가 그 시간 동안 없으면 세션을 `runtime_unavailable`로 닫는다. `current`(지금 세션 조회)는 실행기의 조회가 아니라 세지 않는다. 감지는 최대 1/4 늦다.
- **재시작 정리**: 준비 서비스는 시작할 때(경계가 늦게 뜨면 몇 번 다시) 그리고 새 탐색을 열기 전마다 정리를 끝냈는지 본다. 열린 경계 세션이 있으면 `close`(`reason: cancelled`, `cause: restart_cleanup`)로 닫고 `current`로 닫힘을 확인한다. 닫지 못하면 새 탐색을 열지 않는다. 열린 탐색 MCP 세션 ID는 보호 상태 디렉터리의 `exploration/mcp-sessions.json`(0600)에 열 때 남기고 지울 때 뺀다. 재시작 정리는 남은 ID를 `DELETE`로 지운다(이미 없으면 성공). 지우지 못한 ID는 남겨 두고 새 탐색을 열 때마다 다시 지워 본다. 그래도 남으면 탐색은 막지 않고(성능) 남은 채로 연다는 로그를 남긴다(위 문단의 섞임 가능성). 지우기는 2xx·404만 성공으로 본다(405는 서버가 클라이언트 종료를 허용하지 않는다는 뜻이라 지운 것이 아니다).
- **감사**: 경계 원자료 세션 칸 `closed_by`(`restart_cleanup`·`control_lost`)에 남는다. 보통 마감은 이 칸을 쓰지 않는다(이전 기록과 같은 바이트). 평가 원자료 검사(`scripts/eval/boundary_export.py`)는 이 칸을 선택 칸으로 받는다. `session_heartbeat_ms`는 없으면 30초, 음수이거나 5초보다 짧으면 설정 오류로 경계가 시작하지 않는다. 공식 분리 배치(`runner_split.py`)는 제품 예시 경계 설정을 틀로 쓰므로 공식 측정에도 30초가 켜진다(동결 기록에 적는다). `cause`는 서버가 `restart_cleanup`만 보낼 수 있고, `control_lost`는 경계만 쓴다.

세션 마감 뒤 같은 `session_ref`로 오는 요청은 `session_closed`로 막는다. 마감 사유는 `closed_reason`으로 남긴다. 지금 원본 MCP 프로세스는 실행 문맥과 연결되지 않고 PID 하나만 죽인다(`Process.Kill`, 프로세스 그룹 없음). 그래서 실행기가 문맥 취소와 프로세스 그룹 종료를 더한다.

## 7. 발견 결과 (실행기 → 프로그램)

- 실행기는 찾은 엔드포인트를 프로그램 API로 넘긴다. 모델이 쓴 자유 문장을 그대로 저장하지 않고, 프로그램이 형식·범위를 다시 검사한다(절대규칙 4).
- 지금의 `diagnosisflow` 엔드포인트 형식(`method`·`path`·`kind`·`source`·`from`·`in_scope`·`change`·`visited`·`status`)을 쓰고, 출처 이름을 더한다: `browser_navigation`, `browser_xhr`, `browser_form`, `api_recon_static`, `api_recon_runtime`.
- 필드를 더한다: query 키 이름, JSON 본문 키 이름, 요청 content-type. 값은 넣지 않는다.
- 실제로 보낸 요청에서 나온 발견은 경계 기록의 `seq`를 함께 넘기고(`observed`), 번들·코드에서 추정한 것은 `inferred`로 표시한다.
- 탐색 결과는 발견 근거일 뿐이다. 진단 결과나 확정 취약점으로 다루지 않는다(절대규칙 5).
- 구현(추정 발견): `diagnosisflow.Controller.ReportStatic`이 `api_recon_static` 보고를 받는다. 엔드포인트 칸은 `query_keys`·`body_keys`(이름만, 하나에 64바이트·32개 이하)·`content_type`(매개변수 없는 type/subtype)이고, 종류(`kind`)는 `api`다. 경로는 이 origin의 절대 경로만 받고 정규화로 바뀌는 경로·query·fragment는 받지 않는다. 승인 메서드·경로·제외 경로·위험 경로 단어를 다시 검사해 범위 밖·형식 오류는 저장하지 않고 `counts.discoveries_dropped`로 센다. 추정이라 읽기 접두 안의 GET이어도 자동 방문·선택하지 않는다(`change`는 `possible`). 초기 탐색(`crawling`) 단계에서만 받는다. 실제 요청에서 나온 발견(`observed`, 경계 `seq`)은 경계 기록 형식 뒤에 따로 붙인다.

## 8. 계정과 인증

2026-11-26 사용자 결정으로 바뀌었다([모델 로그인 시도 기록 계약](model-login-attempts.md), CLAUDE.md 절대규칙 1·2).
- 계정을 시험·실계정으로 따로 분류하지 않는다. 대상에 등록된 계정(시험 계정만 등록하라는 공지는 사용자 책임)이 이 회차의 계정이다.
- 로그인은 모델이 브라우저(MCP)로 대상의 로그인 화면에서 직접 한다. 프로그램이 브라우저 프로필에 세션을 넣지 않는다. 탐색 지침에 등록 계정의 라벨·아이디·비밀번호를 준다.
- 같은 아이디로 연달아 실패한 로그인은 회차당 3번까지다(진단 단계와 공통). 경계가 로그인 시도를 판정·기록하고 상한을 집행하는 방법과 쓰기 승인 없는 회차의 로그인 POST 예외 범위는 위 계약 §2·§5를 따른다.
- 구현: 엔진 서버가 제어 소켓 `open`에 판정 설정(`login`: 등록 계정 라벨·아이디·비밀번호, 연속 실패 상한·대조 요청 상한·토큰 하한)을 싣는다. 경계는 세션 메모리에만 두고 기록·원자료·로그에 넣지 않는다. 판정 설정 없이는 제품 탐색을 열지 않는다.
  - 범위·제외·위험 경로 검사를 지난 승인 대상 요청만 판정기에 묻는다. 상한·판정 대기는 `login_attempts_exhausted`·`login_verification_pending`으로 막고, `read_only` 회차는 그 계정이 '로그인 안 됨'일 때 등록 아이디·비밀번호 쌍이 정확히 든 로그인 POST만 메서드 검사의 예외로 보낸다.
  - 브라우저 문맥 하나에는 등록 계정 하나만 로그인한다(`browser_account_in_use`, 직접 입구는 제외). 탐색 모델이 `switch_browser_account`를 부르면 엔진이 지금 MCP 연결을 지워(문맥이 닫힘) 새 연결을 열고, 제어 소켓 `browser_context_reset`으로 경계에 알린다. 경계는 닫힌 문맥에서 로그인한 계정을 '로그인 안 됨'으로 돌린다(위 계약 §2-2).
  - 로그인 시도 행은 제어 소켓 `login_attempts`(쪽, 세션을 닫은 뒤에도 다음 세션 전까지)로 읽는다. 엔진은 탐색 단계가 끝나면 그 행을 회차 상태 디렉터리 `login-attempts.jsonl`에 옮기고, 진단 단계가 그 행으로 연속 실패를 이어 센다. 행의 `transport_ref`는 이 경계 기록 순번(`seq`)이다.
- 실행기는 인증 헤더를 승인 origin 요청에만 붙인다. 브라우저 전체 요청에 헤더를 넣는 방식(예: Playwright `extraHTTPHeaders`)은 쓰지 않는다.

## 9. 결정과 남은 항목

실행기 안쪽(제품 흐름의 어느 단계에서 누가 어떤 도구로 탐색하는지)은 [탐색 실행기 단계 결정안](exploration-executor.md)에 둔다.

2026-11-12부터 열린 항목은 최민준이 결정안을 정하고 이동찬이 리뷰한다(개발 권한 위임). 아래 1~6은 결정안이고, 리뷰로 확정되면 "제안" 표시를 뗀다.

1. **배치 — 경계·Chromium·MCP를 별도 컨테이너로.**
   - 탐색 경계는 엔진 밖 별도 컨테이너(`explore-boundary`)다. 브라우저 입구와 직접 입구를 서로 다른 Docker 망의 리슨 주소로 둔다(구현 이동찬).
   - 브라우저 샌드박스는 컨테이너 두 개다(#485). Chromium 컨테이너(`explore-browser`)는 브라우저 입구망(경계만)과 CDP 연결망(MCP만)에만, MCP 컨테이너(`explore-mcp`, `@playwright/mcp@0.0.84` HTTP 전송)는 엔진 제어망(엔진만)과 CDP 연결망에만 붙는다. 같은 이미지를 다른 진입점으로 쓴다. 엔진은 기존 MCP `http` 전송(`server/mcpdiscover.go`)으로 `http://explore-mcp:<포트>/mcp`에 붙는다.
   - 처음에는 Chromium과 MCP를 한 컨테이너에 두고 MCP를 제어망 주소 하나에만 띄웠다. 실측에서 같은 namespace의 native 자식이 제어망 주소의 MCP 포트에 닿았다(B16 FAIL, [결과](../evaluation/p1-browser-fixture-results.md)). 같은 namespace의 프로세스는 자기 인터페이스 주소에 닿으므로 `--host` 고정만으로는 막을 수 없어서 나눴다.
   - 제어망·CDP 연결망 때문에 §5의 "경계 하나만 닿는 namespace"가 깨지지 않게 다음을 지킨다.
     - 엔진은 제어망에서 MCP 클라이언트로 나가기만 하고 아무 포트도 열지 않는다(관리 API·LLM 게이트웨이 포함). 엔진의 리슨은 제어망 인터페이스에 묶지 않는다(#407).
     - MCP 서버는 `--host`를 제어망 주소 하나로 고정한다. loopback·`0.0.0.0`·CDP 연결망 주소에는 띄우지 않는다.
     - `--allowed-hosts`는 접속하는 클라이언트를 고르는 옵션이 아니라 요청의 Host 헤더를 검사하는 옵션이다. 엔진이 MCP에 붙을 때 쓰는 이름(`explore-mcp:<포트>`)만 넣는다.
     - 이 판의 Chromium은 `--remote-debugging-address`를 무시하고 CDP를 자기 namespace의 loopback에만 연다(실측). 그래서 Chromium 실행기가 CDP 연결망 주소 하나에만 리슨하는 TCP 중계를 둔다(loopback·`0.0.0.0` 리슨은 거부). 페이지의 loopback 요청은 `--proxy-bypass-list=<-loopback>`(브라우저)과 Playwright의 문맥 기본값(`<-loopback>`)으로 경계를 거쳐 막히고, `--remote-allow-origins`를 주지 않아 페이지 origin의 CDP WebSocket도 거부된다.
     - 남는 것: Chromium namespace 안의 native 프로세스는 loopback CDP로 Chromium을 조작할 수 있다. 그 프로세스가 닿는 망은 Chromium과 같다(브라우저 입구망·CDP 연결망). CDP 연결망의 상대는 리슨이 없는 MCP 컨테이너 주소뿐이다. 이전 배치는 Playwright가 `--remote-debugging-pipe`로 띄워 CDP TCP 리슨이 없었다.
     - MCP 컨테이너의 프로세스는 엔진 제어망에 닿는다. 그 망에는 엔진 리슨이 없고, MCP 프로세스 안에서 코드를 실행하는 도구는 고정 목록(§5.1)에서 뺀다.
   - 대조 시험(소유 fixture 하네스, `scripts/eval/browser_fixture_harness.py`): Chromium 컨테이너에서 MCP 제어 주소로 TCP·UDP, 엔진 제어망으로 TCP 연결하면 경로가 없다(B16). driver(엔진 역할)·경계에서 CDP에 닿지 않고, MCP 컨테이너에서 경계 브라우저 입구에 닿지 않는다. 양성 대조로 MCP→CDP, driver→MCP 연결을 같은 프로브로 확인한다. READY 전에 Chromium 컨테이너에 MCP 프로세스가, MCP 컨테이너에 Chromium 프로세스가 없는지 본다. 대상 페이지 스크립트에서 MCP 포트로 연결하면 실패한다(B18). MCP 서버가 엔진 환경 변수(제어 비밀)를 물려받지 않고(§5), 중지는 컨테이너 종료로 한다(§6, MCP부터). 지금 원본 MCP가 엔진의 stdio 자식으로 떠 환경을 통째로 물려받고 PID 하나만 죽는 문제를 이 배치로 없앤다.
   - 직접 샌드박스는 기존 Bash 격리 실행(`e2sandbox`)을 쓴다. 탐색 단계에서는 relay가 E② 대신 경계의 직접 입구로 가고, 진단 단계에서는 지금처럼 E②로 간다. 단계는 서버가 정하고 모델·Worker가 고르지 못한다.
   - 평가 회차에서는 경계가 계측기의 탐색 입구(별칭 `eval-meter-explore`, 별도 리슨)로, E②가 진단 입구(기존 `eval-meter-upstream`)로 보낸다. 계측기는 입구별 리슨으로 전달 행에 입구를 남긴다(프로토콜 §2 경로 분리).
2. **외부 호스트 TLS — 가로챈다.** 경계가 외부 호스트도 TLS를 가로채고, 대상 쪽 인증서는 시스템 루트로 검증한다. 그래야 메서드·`Sec-Fetch-*`를 보고 §3의 외부 이동·쓰기 차단을 할 수 있다. 브라우저는 승인 대상 때문에 이미 경계 CA를 신뢰하므로 추가 신뢰 설정은 없다.
3. **WebSocket — 쓰기까지 승인한 회차의 승인 대상만.** 승인 origin·경로 안의 업그레이드는 `write_mode=approved_writes`일 때만 허용한다. WebSocket 메시지는 메서드로 읽기·쓰기를 가를 수 없어서, 읽기만 승인한 회차는 막는다(`ws_read_only`). 메시지 내용은 기록하지 않고 연결 수·바이트만 남긴다. 외부 호스트로 가는 WebSocket은 막는다.
4. **세션 제어 — 유닉스 소켓 제어 API.** 엔진 서버와 경계 사이에 줄 단위 JSON 제어 소켓(`open`·`close`·`status`·`current`, 모델 직접 로그인 회차의 `login_attempts`·`browser_context_reset`은 §8)을 둔다. `current`는 세션 참조 없이 경계의 지금 세션 상태(없으면 상태 없음)를 준다. 세션이 제어 연결과 따로 살아서 준비 서비스가 재시작하면 참조를 잃으므로, 멈춘 작업 해제(C04)가 재시작 전후로 열린 세션이 없는지 이걸로 확인한다. 소켓은 엔진 서버 프로세스만 닿고, 브라우저·직접 샌드박스는 닿지 못한다. 세션 입력은 §2 그대로다.
5. **원본 기록 프록시(`traffic/`)는 관찰 통로로 쓰지 않는다.** 대상 TLS를 검증하지 않고(`SslInsecure`), 실행 결속이 없고, 헤더를 평문으로 저장하고, 오류가 나면 기록 없이 통과시키기 때문이다. 경계 기록(§4)이 관찰의 정본이다.
6. **브라우저 입구 외부 GET 유출(§3 한계) — 지금은 한계로 두고 잰다.** 경계 기록의 `external_host` 수를 보존 측정과 함께 보고, 줄이는 규칙(승인 origin 문서가 불러온 호스트만 허용, 허용 목록)은 측정 결과를 보고 정한다.
7. **(남음, 이동찬·이경준)** `시험 계정` 분류의 저장 위치와 승인 주체. 효력 조건은 [개발 계획의 계정 항목](../archive/development-plan-v3.md#1111-개선-순서--원본-능력-보존-우선)을 따르고, 분류가 들어올 때 §8과 수용시험 S09를 함께 고친다.

## P1 구현의 Workbench 미실행 사유 (2026-11-12)

이 절은 운영 경계 프로토콜 채택이나 활성화 승인을 뜻하지 않는다. 현재 P1 host adapter가 없는 제품 경로가 저장하는 `workbench-flow/v1` 조회 필드의 구현 목록이다. `exploration_status=NOT_RUN`의 `exploration_reason`은 다음 고정값만 발급한다.

| 코드 | 의미 |
|---|---|
| `boundary_unavailable` | 운영 경계 어댑터 미공급 |
| `model_unavailable` | 승인된 task 모델 Worker를 준비하지 못함 |
| `runtime_unavailable` | 보호 runtime/private 자료 채널 조립 실패 |
| `browser_unavailable` | 고정 브라우저·MCP 세션 준비 실패 |

이 값은 사용자·모델의 임의 오류 문자열이 아니다. 화면은 "탐색 안 함(사유)"으로 표시하고 발견 없음·검사 완료로 바꾸지 않는다. 이 경우 기존 보호 정적 크롤을 수행할 수 있어도 새로운 MCP 탐색의 상태는 별도 NOT_RUN이다. 실제 결속 후에는 RUNNING, 마감은 COMPLETED/PARTIAL/STOPPED로 구분한다. 현재 B16 native Node→자기 MCP 제어 연결은 FAIL이고 운영 어댑터는 nil이므로 전체 운영 수용이 끝난 것이 아니다. 상세 실측은 [P1 fixture 결과](../evaluation/p1-browser-fixture-results.md)를 따른다.

`exploration.Failure.Stage/Kind`는 서버 진단용 고정 단계·분류이며 원문 오류·URL·자격 값·회차 참조를 담지 않는다. 화면에 발급하는 `exploration_reason`과는 다른 채널이다. 현재 Workbench 매퍼는 위 네 `NOT_RUN` 사유를 정확 일치로 처리한다. 내부 Failure를 공개 DTO에 자동 직렬화하거나 원문 오류를 덧붙이지 않는다. PARTIAL/STOPPED는 저장된 상태·마감 사유로 구분하며, live 화면 매퍼의 실제 연결은 UI 담당 수용 전까지 미완료다.
