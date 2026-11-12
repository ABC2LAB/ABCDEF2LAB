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
| `reason` | 막은 이유. `out_of_scope_origin`, `out_of_scope_path`, `method_not_approved`, `risky_path`, `internal_address`, `metadata_address`, `control_endpoint`, `external_blocked_local`, `external_direct`, `external_navigation`, `external_method`, `cap_exhausted`, `session_closed`, `target_ended`, `ws_read_only`, `protocol_unsupported` |
| `ingress` | `browser`·`direct` |
| `dest_class` | `approved`·`external`·`internal`·`metadata`·`control` |
| `method`·`status`·`bytes` | 메서드, 응답 상태, 응답 바이트 수 |
| `path_pattern` | 승인 대상 요청만. 계측기와 같은 가림(path-mask/v2). 평가·수용시험 회차에서는 계측기와 같은 키로 가린다. 제품 회차는 키 없는 표시용 패턴(값 조각만 `{id}`)이라 `{id}` 규칙에 걸리지 않는 짧은 토큰·계정명 조각이 원문으로 남을 수 있다(한계). 그래서 제품 회차의 경계 기록은 보호 저장소에만 두고 운영자 화면에서만 보이며, 모델 입력·공유 보고서·export에는 그대로 넣지 않는다(넣을 때는 DLP를 거친다) |
| `external_host` | 외부 요청만. 호스트 이름(사전 공지·보고서용) |
| `fetch_dest`·`fetch_mode` | `Sec-Fetch-Dest`·`Sec-Fetch-Mode` 값(있으면) |
| `has_auth_header` | `Authorization`·`Cookie` 유무(bool) |

이 기록이 "실제로 보냈다"의 정본이다. 탐색 경계 차단 수는 E② 진단 차단과 따로 집계한다(#387).

## 5. 실행 환경 분리

- 탐색 실행기 프로세스(브라우저, MCP, API Recon, 자식 프로세스)는 경계 하나만 닿는 네트워크 namespace에서 돈다. 브라우저는 브라우저 입구만, Bash·Node·API Recon은 직접 입구만 닿는 서로 다른 샌드박스에 둔다. 직접 샌드박스에서는 브라우저 프로세스의 명령줄·환경·프로필을 읽지 못한다.
- MCP 서버(Node) 프로세스는 브라우저 샌드박스에 둔다. 이 프로세스도 브라우저 입구에 닿으므로 브라우저 입구의 발신 주체는 Chromium과 MCP 서버다. 그래서 모델에 노출하는 MCP 도구는 실행기 설정의 고정 목록(§5.1)으로 하고, 요청 헤더·라우팅을 바꾸는 도구와 Playwright·Node 코드를 실행하는 도구는 뺀다. 페이지 안에서 스크립트를 실행하는 도구는 금지 헤더를 바꾸지 못하므로 남길 수 있다(§3 한계는 그대로). CDP 포트는 직접 샌드박스에서 닿지 않는다. 프록시 환경 변수에 기대지 않는다(Node `fetch`는 기본으로 프록시 변수를 따르지 않고, API Recon은 TLS 검증을 끄고 Node에서 직접 보낸다).
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
  - 허용 도구의 출력에도 S09를 적용한다. 인증 준비(§8)가 들어오면 실행기가 MCP 도구 출력에서 프로그램이 넣은 자격 값을 정확값으로 가린 뒤 모델·공유 결과에 넘긴다(E② 도구 출력 가림과 같은 방식). 비인증 회차에서도 심은 표식의 노출 0건을 확인한다.
- **CLI 옵션**: 실행기(서버)가 정하고 모델이 바꿀 수 없는 옵션만 쓴다. 공통은 `--headless`, `--isolated`, `--no-webmcp`, `--browser chromium`, `--proxy-server <브라우저 입구>`, `--output-dir <브라우저 샌드박스 안>`이다. `--browser`의 기본값 `chrome`은 이미지 밖 Google Chrome(`/opt/google/chrome`)을 찾으므로, MCP 자신의 설치 명령(`playwright-mcp install-browser chromium`)으로 깐 그 판의 Chromium을 고른다. §9-1 HTTP 배치에서는 `--port <MCP 포트>`, `--host <브라우저 컨테이너의 제어망 주소>`, `--allowed-hosts <엔진이 접속하는 호스트:포트>`를 더한다. 세 값은 한 묶음이다. 엔진의 MCP `http` URL은 `http://<allowed-hosts 값>/...`이고, 그 이름이 `--host` 주소로 풀려야 한다. 셋 중 하나라도 어긋나면 실행기가 브라우저 컨테이너를 띄우지 않는다. §9-1 분리 대안에서는 MCP 쪽에 `--cdp-endpoint <Chromium 컨테이너의 CDP 주소>`를 더한다. 이 목록 밖의 옵션은 넣지 않는다. `--ignore-https-errors`(경계 CA를 신뢰시킨다), `--init-page`(Playwright 페이지 객체에 TypeScript 실행), `--caps`는 쓰지 않는다. `--allowed-origins`·`--blocked-origins`·`--secrets`는 README도 보안 경계가 아니라고 하므로 통제 근거로 쓰지 않는다.
- **거르는 곳**: 지금 MCP 연결(`server/mcpdiscover.go`·`server/assembly.go`)에는 도구 단위 허용 목록이 없다(`allowed_tools`·`disabled_tools`·`tool_filter` 검색 0건). 실행기가 MCP 도구를 모델에 노출하기 전에 이 목록으로 거른다(구현 최민준). 처음 받은 목록뿐 아니라 MCP 서버가 도구 목록 변경을 알릴 때마다 다시 거른다. 목록에 없는 도구는 버전이 바뀌어도 노출하지 않는다(기본 거부). 허용 이름과 같은 이름을 다른 출처(WebMCP 등)가 등록해도 MCP 서버 기본 도구가 아니면 노출하지 않는다. B18은 WebMCP 도구를 등록하는 fixture 페이지를 연 뒤에도 노출된 도구가 이 목록과 같은지 확인한다.

## 6. 중지·종료 신호

| 신호 | 경계 | 실행기 |
|---|---|---|
| 사용자 중지·실행 취소 | 새 연결 거부, 유예 뒤 열린 연결 종료 | 브라우저·MCP·자식 프로세스를 프로세스 그룹째 종료 |
| 대상 철회·만료 | 스스로 감지해 `target_ended`로 거부 | 같은 종료 |
| 시간·요청 상한 도달 | `cap_exhausted`로 거부, 세션 마감 | 탐색 마감. **실행 실패가 아니다.** 찾은 것으로 진단 단계로 넘어간다 |
| 경계 장애 | 실행기에 연결 불가로 보임 | 탐색 마감(`boundary_unavailable`). 진단 단계는 E②로 계속할 수 있다 |

세션 마감 뒤 같은 `session_ref`로 오는 요청은 `session_closed`로 막는다. 마감 사유는 `closed_reason`으로 남긴다. 지금 원본 MCP 프로세스는 실행 문맥과 연결되지 않고 PID 하나만 죽인다(`Process.Kill`, 프로세스 그룹 없음). 그래서 실행기가 문맥 취소와 프로세스 그룹 종료를 더한다.

## 7. 발견 결과 (실행기 → 프로그램)

- 실행기는 찾은 엔드포인트를 프로그램 API로 넘긴다. 모델이 쓴 자유 문장을 그대로 저장하지 않고, 프로그램이 형식·범위를 다시 검사한다(절대규칙 4).
- 지금의 `diagnosisflow` 엔드포인트 형식(`method`·`path`·`kind`·`source`·`from`·`in_scope`·`change`·`visited`·`status`)을 쓰고, 출처 이름을 더한다: `browser_navigation`, `browser_xhr`, `browser_form`, `api_recon_static`, `api_recon_runtime`.
- 필드를 더한다: query 키 이름, JSON 본문 키 이름, 요청 content-type. 값은 넣지 않는다.
- 실제로 보낸 요청에서 나온 발견은 경계 기록의 `seq`를 함께 넘기고(`observed`), 번들·코드에서 추정한 것은 `inferred`로 표시한다.
- 탐색 결과는 발견 근거일 뿐이다. 진단 결과나 확정 취약점으로 다루지 않는다(절대규칙 5).
- 구현(추정 발견): `diagnosisflow.Controller.ReportStatic`이 `api_recon_static` 보고를 받는다. 엔드포인트 칸은 `query_keys`·`body_keys`(이름만, 하나에 64바이트·32개 이하)·`content_type`(매개변수 없는 type/subtype)이고, 종류(`kind`)는 `api`다. 경로는 이 origin의 절대 경로만 받고 정규화로 바뀌는 경로·query·fragment는 받지 않는다. 승인 메서드·경로·제외 경로·위험 경로 단어를 다시 검사해 범위 밖·형식 오류는 저장하지 않고 `counts.discoveries_dropped`로 센다. 추정이라 읽기 접두 안의 GET이어도 자동 방문·선택하지 않는다(`change`는 `possible`). 초기 탐색(`crawling`) 단계에서만 받는다. 실제 요청에서 나온 발견(`observed`, 경계 `seq`)은 경계 기록 형식 뒤에 따로 붙인다.

## 8. 계정과 인증

**결정·구현 전.** `시험 계정` 분류는 아직 정본(개발 계획·프로토콜)과 코드에 없다(§9). 그 전까지 이 절은 목표 규칙이고, 실제 대상에서 인증을 쓰는 탐색은 시작하지 않는다. 수용시험 S09(심어 둔 비밀번호·토큰 표식이 모델 입력·로그·근거 파일에 없다)는 그대로 적용한다.

- 탐색에서 쓰는 계정은 `시험 계정` 표시가 붙은 계정뿐이다. 표시 여부는 모델·도구가 쓴 문자열이 아니라 프로그램이 보호된 계정 등록 정보로 확인한다. 표시가 없거나 판단할 수 없으면 실계정으로 보고 탐색에 쓰지 않는다.
- 로그인은 프로그램이 인증 준비로 하고, 브라우저 프로필에 세션을 넣는다. 모델은 비밀번호를 입력하지 않는다.
- 실행기는 인증 헤더를 승인 origin 요청에만 붙인다. 브라우저 전체 요청에 헤더를 넣는 방식(예: Playwright `extraHTTPHeaders`)은 쓰지 않는다.

## 9. 결정과 남은 항목

실행기 안쪽(제품 흐름의 어느 단계에서 누가 어떤 도구로 탐색하는지)은 [탐색 실행기 단계 결정안](exploration-executor.md)에 둔다.

2026-11-12부터 열린 항목은 최민준이 결정안을 정하고 이동찬이 리뷰한다(개발 권한 위임). 아래 1~6은 결정안이고, 리뷰로 확정되면 "제안" 표시를 뗀다.

1. **배치 — 별도 컨테이너 세 개.**
   - 탐색 경계는 엔진 밖 별도 컨테이너(`explore-boundary`)다. 브라우저 입구와 직접 입구를 서로 다른 Docker 망의 리슨 주소로 둔다(구현 이동찬).
   - 브라우저 샌드박스도 별도 컨테이너(`explore-browser`)다. Chromium과 `@playwright/mcp@0.0.84`를 HTTP 전송으로 띄우고, 엔진은 기존 MCP `http` 전송(`server/mcpdiscover.go`)으로 붙는다. 이 컨테이너는 엔진 제어망(엔진만)과 브라우저 입구망(경계만)에만 붙는다.
   - 제어망 때문에 §5의 "경계 하나만 닿는 namespace"가 깨지지 않게 다음을 지킨다.
     - 엔진은 제어망에서 MCP 클라이언트로 나가기만 하고 아무 포트도 열지 않는다(관리 API·LLM 게이트웨이 포함). 엔진의 리슨은 제어망 인터페이스에 묶지 않는다.
     - MCP 서버는 `--host`를 제어망 주소 하나로 고정한다. loopback·`0.0.0.0`에 띄우지 않는다. Chromium은 loopback만 프록시를 거치지 않으므로, 같은 컨테이너의 페이지가 MCP 포트에 직접 닿는 길을 막기 위해서다. 제어망 주소로 가는 페이지 요청은 프록시(경계)를 거치고 내부 주소라 막힌다.
     - `--allowed-hosts`는 접속하는 클라이언트를 고르는 옵션이 아니라 요청의 Host 헤더를 검사하는 옵션이다. 엔진이 MCP에 붙을 때 쓰는 이름(`explore-browser:<포트>`)만 넣는다.
     - 이 조건을 지키기 어려우면, MCP 서버와 Chromium을 다른 컨테이너로 나누고 MCP가 `--cdp-endpoint`로 Chromium에 붙는 배치로 바꾼다. 그러면 Chromium 쪽 namespace는 경계와 MCP 컨테이너(Chromium 쪽에 리슨 없음)에만 닿는다.
   - 대조 시험을 더한다. 브라우저 컨테이너에서 엔진 제어망 주소로 TCP·UDP 연결하면 실패한다(B09·B16). 대상 페이지 스크립트에서 MCP 포트로 연결하면 실패한다(B18). 그래서 MCP 서버가 엔진 환경 변수(제어 비밀)를 물려받지 않고(§5), 중지는 컨테이너 종료로 한다(§6). 지금 원본 MCP가 엔진의 stdio 자식으로 떠 환경을 통째로 물려받고 PID 하나만 죽는 문제를 이 배치로 없앤다.
   - 직접 샌드박스는 기존 Bash 격리 실행(`e2sandbox`)을 쓴다. 탐색 단계에서는 relay가 E② 대신 경계의 직접 입구로 가고, 진단 단계에서는 지금처럼 E②로 간다. 단계는 서버가 정하고 모델·Worker가 고르지 못한다.
   - 평가 회차에서는 경계가 계측기의 탐색 입구(별칭 `eval-meter-explore`, 별도 리슨)로, E②가 진단 입구(기존 `eval-meter-upstream`)로 보낸다. 계측기는 입구별 리슨으로 전달 행에 입구를 남긴다(프로토콜 §2 경로 분리).
2. **외부 호스트 TLS — 가로챈다.** 경계가 외부 호스트도 TLS를 가로채고, 대상 쪽 인증서는 시스템 루트로 검증한다. 그래야 메서드·`Sec-Fetch-*`를 보고 §3의 외부 이동·쓰기 차단을 할 수 있다. 브라우저는 승인 대상 때문에 이미 경계 CA를 신뢰하므로 추가 신뢰 설정은 없다.
3. **WebSocket — 쓰기까지 승인한 회차의 승인 대상만.** 승인 origin·경로 안의 업그레이드는 `write_mode=approved_writes`일 때만 허용한다. WebSocket 메시지는 메서드로 읽기·쓰기를 가를 수 없어서, 읽기만 승인한 회차는 막는다(`ws_read_only`). 메시지 내용은 기록하지 않고 연결 수·바이트만 남긴다. 외부 호스트로 가는 WebSocket은 막는다.
4. **세션 제어 — 유닉스 소켓 제어 API.** 엔진 서버와 경계 사이에 줄 단위 JSON 제어 소켓(`open`·`close`·`status`)을 둔다. 소켓은 엔진 서버 프로세스만 닿고, 브라우저·직접 샌드박스는 닿지 못한다. 세션 입력은 §2 그대로다.
5. **원본 기록 프록시(`traffic/`)는 관찰 통로로 쓰지 않는다.** 대상 TLS를 검증하지 않고(`SslInsecure`), 실행 결속이 없고, 헤더를 평문으로 저장하고, 오류가 나면 기록 없이 통과시키기 때문이다. 경계 기록(§4)이 관찰의 정본이다.
6. **브라우저 입구 외부 GET 유출(§3 한계) — 지금은 한계로 두고 잰다.** 경계 기록의 `external_host` 수를 보존 측정과 함께 보고, 줄이는 규칙(승인 origin 문서가 불러온 호스트만 허용, 허용 목록)은 측정 결과를 보고 정한다.
7. **(남음, 이동찬·이경준)** `시험 계정` 분류의 저장 위치와 승인 주체. 효력 조건은 [개발 계획의 계정 항목](../development-plan.md#1111-개선-순서--원본-능력-보존-우선)을 따르고, 분류가 들어올 때 §8과 수용시험 S09를 함께 고친다.

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
