# 실제 MCP 계약 fixture 하네스

`scripts/eval/browser_fixture_harness.py`는 기존 `cmd/browser-fixture`, 별도 `cmd/exploration-contract-fixture`, 고정 Chromium/MCP 이미지와 정적 Go 통합 시험 바이너리를 연결한다. 현재 driver 프로파일은 읽기 승인(`read_only`)만 지원한다. **소유 contract_fixture**이며 제품 탐색 경계, 실제 모델 실행, R-P/R-F/R-R 보존 실측을 대체하지 않는다. 해당 항목은 `NOT_RUN`이다.

필요한 로컬 도구는 Python 3.10 이상(표준 라이브러리), Go, OpenSSL, Docker다. 하네스는 이미지를 pull하지 않는다. 사전에 로컬에 `jindo-p1-explore-browser:b679f7b`가 있어야 한다(저장소 루트에서 `docker build -f deploy/product/Dockerfile --target explore-browser -t jindo-p1-explore-browser:b679f7b .`). 이미지의 실제 내용 ID를 기록하고, 실행은 그 ID로 고정한다. Go 빌드는 `CGO_ENABLED=0`, `GOTOOLCHAIN=local`, `GOPROXY=off`로 하고 호스트의 API 키나 환경을 컨테이너로 전달하지 않는다.

## 배치

다섯 개의 internal bridge를 사용한다. 기본 블록은 `10.94.240.0/21`이며 기존 Docker 망과 겹치면 자원을 만들기 전에 거부한다. 다른 사설 `/21`을 `--subnet-base`로 지정할 수 있다. 앞의 다섯 `/24`를 control·browser·direct·upstream·link 순서로 쓰고, 각 망의 `/24` 안 주소는 고정이다.

Chromium과 MCP는 [탐색 계약](../contracts/exploration-boundary.md) §9-1 분리 배치대로 다른 컨테이너(다른 network namespace)다. 한 컨테이너에 둘을 함께 두면 같은 namespace의 native 자식이 제어망 주소의 MCP 포트에 닿는다(B16, [결과](p1-browser-fixture-results.md)). Chromium은 control 망에, MCP는 browser 망에 붙지 않는다. link는 둘만 붙는 CDP 연결망이다.

| 역할 | 붙는 망 | 주소 끝 |
|---|---|---|
| Chromium(MCP 없음) | browser, link | 각 `.20` |
| Playwright MCP(Chromium 없음) | control, link | `.20`, `.30` |
| fixture proxy | browser, direct, upstream | 각 `.10` |
| 소유 target/CDN/수신기 | upstream | `.20` |
| Go driver | control, direct | `.30`, `.20` |

proxy의 브라우저·직접 리슨은 각 망 주소의 `:8080`에만 묶인다. 대상은 `https://target.test:8443`, CDN 역할은 `https://cdn.test:8444`이며 같은 소유 바이너리의 다른 리슨이다. 내부망·메타데이터 역할 수신기는 upstream의 별도 리슨이며 프록시 허용 origin이 아니다. proxy는 `.test` 이름을 고정 IP로만 연결하고 시험 CA로 대상 TLS를 검증한다. TLS 오류 무시 옵션은 사용하지 않는다.

Chromium 컨테이너에는 공개 CA 파일 하나만 읽기 전용으로 마운트하고, MCP 컨테이너에는 아무것도 마운트하지 않는다. HOME·출력·프로필은 tmpfs다. leaf와 개인키는 proxy/target 전용이며 CA 개인키는 leaf 발급 직후 삭제한다. driver에는 정적 바이너리·공개 CA·메타데이터 관찰 스크립트·호스트 전용 소켓/결과 디렉터리만 마운트한다. 어떤 컨테이너에도 Docker 소켓·소스 트리·LLM/E②/감사/러너 키를 넣지 않는다.

Chromium 컨테이너는 MCP·Playwright 없이 고정 실행기(`deploy/product/explore-chromium.js`)로 Chromium을 직접 띄운다. 실행기 옵션은 `--proxy-server http://<browser .10>:8080`과 `--cdp-listen <link .20>:9222` 둘뿐이다. 나머지 Chromium 옵션은 실행기에 고정돼 있다. 이전 배치에서 MCP 0.0.84 고정판 Playwright가 Chromium을 띄울 때 쓰던 기본 옵션·headless 옵션·`--no-sandbox`(MCP가 `--browser chromium`에서 Linux 샌드박스를 끄던 것과 같은 조건), `--proxy-bypass-list=<-loopback>`, tmpfs의 새 프로필이다. TLS 오류 무시 옵션은 없다. 이 이미지의 Chromium 157은 `--remote-debugging-address`를 무시하고 CDP를 127.0.0.1에만 연다(실측). 그래서 실행기가 link 주소 하나에만 리슨하는 TCP 중계를 둔다. loopback CDP는 Chromium namespace 안에서만 닿고, 페이지의 loopback 요청은 `<-loopback>` 때문에 프록시를 거쳐 막힌다.

MCP는 `--cdp-endpoint http://<link .20>:9222 --host <control .20> --port 8931 --allowed-hosts <control .20>:8931`과 공통 고정 옵션(`--headless --isolated --no-webmcp --browser chromium --proxy-server <browser 입구> --output-dir <tmpfs>`)으로 띄우고 driver가 같은 URL/Host로 연결한다. `--cdp-endpoint`에서 `--headless`·`--browser`는 Chromium 실행 옵션이라 효과가 없다(실제 실행 조건은 위 실행기가 정한다). `--isolated`는 연결마다 새 브라우저 문맥을 만들고 `--proxy-server`는 그 문맥의 프록시를 같은 경계 입구로 정한다. Docker 29의 internal 망에서는 loopback publish 요청이 실제 게시를 만들지 않으므로 `host_loopback_url=null`, `host_publish=NOT_SUPPORTED_ON_INTERNAL_NETWORK`로 남긴다. 이를 위해 외부 망을 추가하지 않는다. 이 publish 요청이 있는 MCP 컨테이너는 `docker exec` 하나가 약 3초 걸리므로(실측) MCP 컨테이너 probe의 exec 제한은 10초다. 초기 readiness는 호스트가 MCP 컨테이너 안에서 수행하는 고정 URL 상태코드 검사(Chromium CDP 중계의 `/json/version`, 그다음 MCP URL)이며 모델 도구가 아니다. READY 전에 `docker top`으로 Chromium 컨테이너에 MCP 프로세스가 없고 MCP 컨테이너에 Chromium 프로세스가 없는지 고정 표식으로 확인한다. 명령행 원문은 출력하지 않는다.

## 실제 탐색 경계 모드(`--boundary real`)

`up --boundary real`이면 계약 fixture 대신 실제 탐색 경계(`cmd/explore-boundary`)를 같은 자리(브라우저·직접 입구 `.10:8080`, 제어 소켓 `/run-fixture/boundary.sock`)에 띄운다. 경계 CA 개인키는 지우지 않고 경계 전용 입력(`boundary-in/`, 하네스 uid 0700·0600)으로 옮기고, 대상 leaf의 SPKI와 대상 주소를 context(`target_spki`·`target_connect`)로 넘긴다. driver는 `boundary_kind=real`이면 `exploreboundary.ControlBoundary`(대상 값은 서버처럼 제어 소켓으로 넘김)·`ControlReader`(관찰·순번·기록)·`DirectFetch`(정적 크롤)를 쓰고, 최종 차단 사유는 경계 기록(`records`)에서 센다. 결과의 `boundary_kind`는 `explore_boundary`다.

지금은 `full_local`만 본다. `external_allowed`(CDN 렌더링 양성)는 실제 경계로는 이 배치에서 양성이 나올 수 없다: 하네스 망은 사설 대역이라 CDN 역할 수신기도 사설 주소이고, 실제 경계는 사설 주소로 풀리는 외부 이름을 내부망(`internal_address`)으로 막는다(맞는 동작). 경계 안에 이름 풀이도 없다. 외부 자원 양성은 공개 주소의 소유 CDN이 있는 환경에서 따로 본다.

2026-11-13 결과(`full_local`): fixture·실제 경계 모두 driver `PASS`. 실제 경계는 대상 전달 23, 관찰 22·추정 4, 차단 `external_blocked_local` 20·`method_not_approved` 8·`out_of_scope_origin` 2·`risky_path` 2, 정리 뒤 MCP·Chromium 중지·소유 자원 0. 같은 이름·다른 포트 이동 단계를 driver에 더해 origin 대조를 두 경계에서 같은 사유(`out_of_scope_origin`)로 본다(실제 경계는 다른 이름을 완전 로컬에서 `external_blocked_local`로 막는다).

## 실행

실행 디렉터리는 새 이름·0700·호스트 소유여야 한다. 하네스가 직접 만든다. 기존 디렉터리를 재사용하지 않는다. API Recon 코드가 통합되기 전에는 `--metadata-script`에 검토한 B 작업의 파일 경로를 지정할 수 있다. 그 내용을 회차에 복사하고 지문을 남기므로 실행 도중 원본 파일 변경이 섞이지 않는다.

```bash
mkdir -p /tmp/jindo-p1-tests
CGO_ENABLED=0 go test -c ./server -o /tmp/jindo-p1-tests/exploration-server.test

python3 -I -B scripts/eval/browser_fixture_harness.py up \
  --run-dir /tmp/jindo-p1-external-01 --mode external_allowed
python3 -I -B scripts/eval/browser_fixture_harness.py driver \
  --run-dir /tmp/jindo-p1-external-01 \
  --binary /tmp/jindo-p1-tests/exploration-server.test \
  '--driver-arg=-test.run=^TestOwnedDockerExplorationMCP$' \
  --driver-arg=-test.v
python3 -I -B scripts/eval/browser_fixture_harness.py down \
  --run-dir /tmp/jindo-p1-external-01
```

완전 로컬 모드는 새 `--run-dir`에서 `--mode full_local`로 같은 절차를 반복한다. `up`은 `READY`와 공개 실행 메타데이터를 반환하고 `/control/context.json`을 저장한다. driver에는 `JINDO_P1_FIXTURE_CONTEXT=/run-fixture/context.json` 경로 하나만 전달한다. 제어 JSON에는 다음 인터페이스가 있다.

- `fixture_socket=/run-fixture/boundary.sock`: Go fixture의 host-owned 세션/관찰 API.
- `browser_socket=/run-fixture/browser-control.sock`: driver 실행 동안만 호스트가 여는 HTTP-over-Unix 서비스. `POST /close`만 허용하고 저장된 ID·이름·소유 label을 대조한 MCP·Chromium 컨테이너만 MCP부터 중지한다.
- `mcp_url`, `target_origin`, `cdn_origin`, `direct_proxy`, `mode`, `write_mode`: 고정 배치와 모드.
- `ca_file=/public-ca/boundary-ca.pem`, `metadata_script=/metadata/metadata_only.js`: 공개·읽기 전용 입력.
- `result_file=/run-fixture/result.json`: Go 시험의 구조화 요약 위치.

브라우저 종료에 Docker 소켓을 driver로 전달하지 않는다. 정상 세션 마감의 MCP 정리·경계 마감·브라우저 lease 종료 뒤 실제 MCP·Chromium 컨테이너가 둘 다 중지됐는지 호스트가 확인한다. Go 종료 코드 0만으로 통과하지 않고, 구조화 결과의 형식/모드/상태와 실제 브라우저 종료를 함께 확인한다.

본 시험 전에 경계 없는 Chromium을 시나리오별 별도 upstream-only 컨테이너에서 새 프로필로 연다. 고정 이미지의 같은 Playwright/Chromium을 쓰고 공개 CA를 NSS에 넣으며, 고정 resolver 규칙으로 `.test` 네 이름을 소유 upstream IP로만 푼다. TLS 오류를 무시하지 않는다. B04/B05/B13은 수신기와 메타데이터 역할 HTTP 도달, B12는 두 역할 TCP 도달, B14는 form/fetch/beacon 세 쓰기의 실제 도달, B09는 WebRTC STUN binding 요청의 수신기 UDP 도달을 각각 확인한다. B09 페이지의 STUN 서버는 수신기의 upstream IP 리터럴이다(`-receiver-stun-ip`). Chromium WebRTC는 STUN 서버 이름을 `--host-resolver-rules`로 풀지 않아서(실측: 이름이면 UDP 0, 같은 주소의 IP면 STUN binding 도달) 이름을 쓰면 양성 대조가 성립하지 않고, 통제 회차의 0도 경로 차단이 아니라 이름 풀이 실패가 된다. 통제 회차의 driver도 `/bypass/B09`를 연다. 지원되지 않거나 양성 도달을 입증하지 못하면 그 시나리오는 `UNDECIDABLE`이다. 이 대조는 통제 없는 **소유 fixture** 전용이며 제품에서 우회 실행을 여는 경로가 아니다.

그 다음 upstream-only target namespace에서 receiver의 B04-fetch HTTPS 1회와 독립 TCP 1회를 보낸다. 공개 CA 검증과 실제 수신기 기록으로 HTTP 1·TCP 2의 양성 계수를 따로 보존한다(HTTPS 자체의 TCP 1도 포함). 그 뒤 Chromium namespace에서 소유 대상/수신기/직접 프록시 IP로, driver namespace에서 소유 대상/수신기 IP로 직접 TCP 연결을 시도해 차단을 확인한다. IP는 소유 ID·label을 대조한 Docker inspect에서만 얻고 공개 대상으로 보내지 않는다.

이어서 B16 분리 대조(`b16_probes`)를 같은 gate 시점에 실행한다. 각 시도를 `connected`/`refused`(상대 namespace 도달, 리슨 없음)/`unreachable`(경로 없음)/`timeout`으로 분류한다. 차단 쪽은 둘로 나뉜다. Chromium→MCP control `:8931` TCP·UDP, Chromium→driver(엔진 역할) control, driver→Chromium link CDP `:9222`, MCP→proxy 브라우저 입구 `:8080`은 `unreachable`이어야 한다. Chromium→MCP link `:8931`과 proxy(경계 역할)→Chromium 입구 주소의 CDP 포트는 연결이 없어야 한다. MCP→Chromium CDP와 driver→MCP control은 `connected`여야 하는 양성 대조다. 그래야 같은 probe가 실제 연결을 잡는다는 것을 보인다. 하나라도 어긋나면 driver를 시작하지 않고 `fixture_b16_probe_failed`로 실패한다. 분류 결과는 실행 디렉터리 `b16-probes.json`(0600)에 남고 driver 결과의 `b16_probes`에도 실린다. 이 대조는 native Node TCP·UDP 범위다. 모든 프로토콜·제품 망 정책의 차단으로 넓혀 말하지 않는다.

Go driver는 gate에서 대기하고, 음성 연결 뒤 수신기 baseline을 잡은 다음 정적 Go 바이너리로 exec한다. 음성 연결 자체와 본 시험 각각의 수신기 차이를 별도로 보고한다. `receiver`, `receiver_conn`, 메타데이터 HTTP/TCP, UDP, 대상 쓰기 도달은 차이가 0이어야 한다. 완전 로컬 모드는 CDN HTTP/TCP 차이도 0이어야 한다. 양성 계수를 원자료에서 지우거나 전체 수신기 수 0으로 표시하지 않는다.

표준 출력에는 고정 상태·카운터·실패 코드만 나온다. driver 원문 출력은 실행 디렉터리의 `driver-output.log`(0600, 컨테이너에 미마운트)에만 받는다. 이를 통째로 터미널·PR·보고서에 복사하지 않는다. Go 결과의 관찰/추정·전달/차단 수를 사용하고, 모델 프롬프트·페이지 원문·헤더·본문 값은 출력하지 않는다.

`down`은 manifest의 정확한 ID·이름과 무작위 소유 label이 모두 맞는 `jindo-p1-<nonce>-...` 자원만 중지/삭제한다. 이름 접두사만으로 다른 작업의 자원을 제거하거나 `docker prune`을 실행하지 않는다. 실패 후에도 `down`을 실행하고 `remaining_owned_resources=0`을 확인한다. 시험 원자료 디렉터리는 검토를 위해 남고 Docker 볼륨은 만들지 않는다.

## 판정 범위

이 하네스의 PASS는 실제 고정 MCP·Chromium·Go 실행기·소유 TLS fixture에서 해당 통합 시험이 통과했다는 뜻이다. 보안 경계의 전체 음성 목록, 지원되지 않는 WebSocket/QUIC/인증 경로, 제품 등록/승인/망 정책의 실효성은 각 수용시험 근거가 필요하다. fixture proxy의 차단을 제품 경계 PASS로 표시하지 않는다. 실제 모델 호출은 수행하지 않으며 결정적 시험 provider는 `model_run=NOT_RUN`이다.
