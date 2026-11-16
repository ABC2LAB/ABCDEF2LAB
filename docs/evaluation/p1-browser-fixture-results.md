# P1 실제 Chromium·MCP fixture 결과 (2026-11-12)

**기능 통합은 두 모드 PASS다. B16 제어망 검사는 아래 한 컨테이너 배치에서 FAIL이었고, Chromium·MCP를 나눈 배치(#485, 맨 아래 재측정 절)에서 망 도달 프로브 9개가 PASS다. 전체 운영 탐색 경계 수용 통과가 아니다.** 실제 고정 Chromium·MCP와 원본 Worker 루프를 사용했지만 모델 provider와 경계는 명시적 시험 fixture다. 제품의 운영 Boundary·private Reader·Docker lease 어댑터는 기본 nil이며 활성화하지 않았다. 실제 Qwen·원본 대비 보존율은 NOT_RUN이다.

## 실행 기준과 동일 조건

구현 시작 dev는 `b679f7b7e62744943f88f4334c49820265811633`, 최종 통합 dev는 `e4c601674bd2b4681094f5155599efbd1051da1d`다. 실제 브라우저 회차는 다음 소스를 고정 빌드했다. 해당 소스는 팀 리뷰 수정과 #454~#459의 보고서·설치 시간·비용 호출 칸·화면·작업 공간 변경을 포함한다. 이후 문서 요약 갱신은 실행 코드 변경이 아니다. 전체 회귀는 최종 PR CI에서 별도로 확인한다.

| 항목 | 실제 핀 |
|---|---|
| 브라우저 시험 소스 | `8a21db635c5f6b2fafa1d1438f6586dd72f0f92d` |
| image_id | `sha256:17b7b5f0fa7ed1caf03e1385deadab742b076735436a72a8aa83d05b48acc811` |
| mcp_version | `0.0.84` |
| proxy_binary_sha256 | `438fde4868af9dd4ec280361f9bc37a8e4a9943caaefeb2e2899bfc8db4fc44f` |
| target_binary_sha256 | `555b5686444929e990a16d190809a1ed8079d71194f78e6d4fede8b666895483` |
| metadata_script_sha256 | `c44327518252d586c4c93785b5a8a0921b3c8201ee4a030e5b73ecadb5427b91` |
| driver_binary_sha256 | `8340227dfc557b6269ef406ca8811de531fc49712796fd668be150102ff918e5` |
| 하네스 SHA256 | `3f03144c207e2fb38d1546c32640978a2674630fa09d58fb0e8f2646cdcd3696` |

양쪽 대상 요청 상한 128, 시간 300초, 동시 연결 16, 응답 2MiB다. 외부 자원 상한만 승인 모드 20/완전 로컬 0이다. Open 입력과 실제 proxy 상한을 정확히 대조한다. 요청 실패도 예산에서 차감하고 대상/외부 상한은 따로 집행한다. 모델은 결정적 시험 provider이며 20회 호출은 실제 Qwen 호출·사용량·비용이 아니다.

네 internal 망(control/browser/direct/upstream), 고정 MCP Host/IP/port, 읽기 전용 mount, 별도 공개 CA를 사용했다. TLS 오류 무시 플래그는 없다. 인증서 개인키·제어 키·Docker socket은 browser에 없다. 독립 양성 Chromium만 명시적인 upstream-only 시험 컨테이너에서 프록시 없이 같은 소유 fixture에 접속했다. 운영 우회 실행기가 아니다.

| 결과 | external_allowed | full_local |
|---|---:|---:|
| 실제 대상 전달/병합된 observed 근거 | 22 / 21 | 22 / 21 |
| inferred 근거 | 4 | 4 |
| JS·CSS·image·API observed status 200 | 4종 확인 | 4종 확인 |
| CSS computed style/런타임 query 키 보존 | 확인 | 확인 |
| CDN 전달 HTTP / TCP | 3 / 3 | 0 / 0 |
| 읽기 미승인 쓰기 차단 | 9 | 9 |
| 승인 밖 origin 차단 | 18 | 18 |
| 위험 경로 차단 | 1 | 1 |
| 완전 로컬 외부 차단 | 해당 없음 | 3 |
| 금지 수신기 HTTP·TCP·UDP·쓰기 도달 | 0 | 0 |
| 종료 후 연결/실행 중 browser | 0 / false | 0 / false |
| 진단 E② 시도 | 0 (탐색 회차) | 0 (탐색 회차) |
| 정리한 소유 컨테이너/망·잔여 | 9 / 4 · 0 | 9 / 4 · 0 |

이 건수는 API 발견 수나 원본 대비 탐색률이 아니다. 페이지/자원/동적 요청을 함께 센다. 전달은 반복을 포함한 실제 요청 수이고 observed는 동일 식별자의 첫·마지막 seq/횟수로 병합한 저장 근거 수다. 두 수를 같다고 보지 않는다. 저장 후 stage=awaiting_selection이며 실제 취약점 검증·E③ 확정이 아니다. 진단 시도 0은 탐색 회차에서 진단을 보내지 않았다는 뜻이며 D01의 실제 진단 회차 수용을 대체하지 않는다.

## 양성 대조와 차단

무통제 Chromium은 같은 고정 이미지·CA를 쓰고 시나리오마다 새 프로필로 실행했다. Node HTTPS/TCP 수신기 양성 1/2는 별도 보존하며 브라우저 양성 대조로 대신 세지 않는다.

| 경로 | 무통제 Chromium 실제 도달 | 경계 연결 회차 |
|---|---|---|
| B04 | 내부망 img/script/fetch 각1, metadata img/script/fetch 각1 | 해당 수신기 HTTP/TCP 0 |
| B05 | 내부망 frame/fetch 각1, metadata frame/fetch 각1 | 리다이렉트 뒤 수신기 HTTP/TCP 0 |
| B12 일부 | 내부망·metadata prefetch HTTP 각1/TCP 각1 | 수신기 HTTP/TCP 0. DNS wire는 NOT_RUN |
| B13 | 내부망 blob/data 각1, metadata blob 각1 | 수신기 HTTP/TCP 0 |
| B14 쓰기 차단 | form/fetch/beacon 각1, actual write3 | actual write0, 차단 기록 있음 |

B14 양성의 navigate 완료는 false다. 페이지가 즉시 POST로 이동해 goto가 중단되지만 실제 세 쓰기 variant 도달로 양성을 확인했다. snapshot에 예상 문자열이 있는 것만으로 통과하지 않는다. MCP가 되돌려주는 실행 코드와 `### Result` JSON 반환값을 분리하고 DOM·실제 관측·수신기 계수를 함께 본다. 자동 top-level form 이동이 다음 양성 페이지를 덮는 시험 경합은 B14를 마지막에 실행하여 바로잡았다. 디버그 실패 회차를 최종 PASS 회차로 계산하지 않는다.

별도로 browser→대상 TCP, browser→수신기 TCP, browser→direct proxy TCP, driver→대상 TCP, driver→수신기 TCP의 5개 직접 경로가 막혔다. 각 시도 뒤 수신기 증가 0을 확인했다. 직접 native Node TCP의 이 범위를 curl·DNS·모든 프로토콜 차단으로 넓혀 말하지 않는다.

## B16 실제 실패와 재현

두 모드 모두 driver를 시작하기 전에 browser 컨테이너의 Node 자식이 같은 namespace의 MCP control IP:8931로 TCP 연결했다. `same_namespace_control_reached`, **FAIL**, API 요청은 보내지 않았다. 이 실제 실패를 기능 통합 PASS에 합쳐 숨기지 않는다.

이 검사는 하네스의 기존 고정 TCP probe로 재현한다. `up` 후 driver를 시작하기 전에 호스트에서 `python3 -I`로 하네스 모듈의 `load(run_dir)`, `owned_ip(data,"browser","control")`, `tcp_probe(container,ip,8931,allowed=True)`를 호출한다. `true`면 금지해야 할 도달이 실제 발생한 FAIL이다. 임의 주소·API 명령은 주지 않는다. 사적 manifest의 ID/name/nonce label을 대조하고 소유 browser만 검사한다.

현재 21개 도구 필터·별도 망만으로 native 자식의 자기 MCP 연결을 차단했다고 주장할 수 없다. 운영 어댑터를 넣기 전에 경계 담당이 프로세스/제어 채널 접근 통제를 완성하고 같은 음성 시험을 통과해야 한다. 제품 host capability 기본 nil과 미가용 NOT_RUN 경로는 유지한다.

## 정본 수용 항목별 상태

아래 PASS는 명시한 fixture·단위 범위다. 정본 1~3단계 전체 통과는 아니다.

| ID | 상태 | 실제 범위 또는 미실행 사유 |
|---|---|---|
| P01 | PASS | 소유 SPA의 실제 DOM ready. Juice Shop 첫 렌더링은 NOT_RUN |
| P02 | PASS | JS/CSS/image/API 실제 status200, CSS 적용·API 기반 DOM 확인. 폰트 별도 자원은 미시험 |
| P03 | NOT_RUN | 운영 boundary→eval-meter explore 입구 adapter 부재 |
| P05 | PASS | 소유 TLS 인증서 검증·공개 CA trust 유지 |
| P06 | NOT_RUN | cap/실패 과금/동시 CONNECT/취소는 단위·계약 시험 PASS, 실제 Chromium cap 소진 종단은 별도 |
| P07 | PASS | browser lease 실제 정지·연결0·회차 자원 제거 |
| P08 | UNDECIDABLE | JS/CSS/SVG 렌더링 부분 PASS, 정본의 폰트 전달·렌더링은 NOT_RUN |
| P09 | PASS | 완전 로컬 CDN HTTP/TCP0 |
| B01 | PASS | 명시한 두 실행 namespace의 직접 대상 TCP 경로 |
| B02/B03 | NOT_RUN | Chromium proxy flag 제거·독자 DNS wire 회차 미실행 |
| B04/B05 | PASS | 위 양성 variant와 동일한 fixture 수신기/리다이렉트 |
| B06/B07/B08 | NOT_RUN | SW·WS·QUIC의 대응 양성/차단 묶음 미실행 |
| B09 | PASS(내부망 역할 수신기 STUN 범위) | 맨 아래 B09 절. 메타데이터 역할 수신기로 가는 STUN·TURN은 미실행, 엔진 제어망 주소 UDP는 B16 프로브(native)로만 봄 |
| B10 | NOT_RUN | 불신 TLS 단위 거부는 PASS, 실제 Chromium 플래그 주입 종단은 별도 |
| B11 | NOT_RUN | Node 직접 TCP 부분은 PASS, curl/전체 자식 프로토콜 수용 아님 |
| B12 | UNDECIDABLE | prefetch/TCP 부분은 PASS, resolver 고정이라 DNS wire 자체 미실행 |
| B13 | PASS | 위 blob/data Worker fixture variant |
| B14 | UNDECIDABLE | 쓰기 미승인 송신0 부분 PASS, 차단된 모든 경로의 발견 등록은 미검증 |
| B15/B17 | NOT_RUN | CDN 탐색 이동·직접 Node/curl 헤더 위조의 정본 종단 묶음 미실행 |
| B16 | FAIL → 분리 배치 PASS(망 도달 범위) | 한 컨테이너 배치: Node 자식의 자기 MCP 제어 TCP 도달. 키 mount 부재는 이를 대신하지 않음. 분리 배치(#485): 아래 재측정 절의 프로브 9개 PASS. 제어 비밀 파일·환경 읽기는 두 컨테이너에 비밀 mount·환경이 없다는 구성 시험(`scripts/ci/test_product_explore_browser.py`) 범위이고 실행 중 읽기 시도는 NOT_RUN |
| B18 | UNDECIDABLE | 실제 페이지의 WebMCP API가 none. 이름 충돌 등록 자체가 일어나지 않음 |
| S01 | PASS | 실제 동적 근거·필드 이름 저장과 별도 PG 재조회 시험. 전체 제품 종단은 별도 |
| S02/S03/S05/S06 | NOT_RUN | origin/경로·위험 쓰기·변조/취소 단위 범위는 PASS, 정본 전체 제품 회차는 별도 |
| S07/S08 | NOT_RUN | 계정 분류/인증 준비·로그인 수용 부재. 비인증을 인증 성공으로 세지 않음 |
| S09/S10 | NOT_RUN | redaction/페이지 비신뢰 입력 단위 시험 PASS, 자격 표식의 실제 모델 종단은 별도 |
| S11 | PASS | 공개32와 별도256/64KiB inventory·누락·PG 격리 단위 시험 |
| S12 | NOT_RUN | 기존 path-mask 회귀는 보존, 운영 경계 가린 기록 exporter 미연결 |
| S13 | NOT_RUN | 이 회차는 read_only. 승인 쓰기 종단은 실행하지 않음 |
| D01/D02 | NOT_RUN | 실제 E② TLS GET·감사/drain 전환 시험은 아래 별도 PASS. 운영 탐색 namespace·계측기 두 입구를 함께 연결한 정본 종단은 미실행 |
| U01 | NOT_RUN | 승인 UI 담당 수용 미실행, Workbench 구조 변경 없음 |
| V01 | NOT_RUN | VAmPI 인스턴스/동일 조건 비교 미실행 |
| R-P/R-F/R-R | NOT_RUN | 실제 모델·원본 짝·운영 exporter 부재. 보존율을 만들지 않음 |

P04/S04는 정본에서 삭제된 항목이다. fixture HTTP-over-Unix 제어 API를 운영 계약의 line JSON 프로토콜로 주장하지 않는다. 원자료 hash와 핀은 재검토 근거이며 이 fixture bundle은 공식 서명 run_record가 아니다. 실제 보존율의 서명·drain·마감은 PR D의 별도 검증을 따라야 한다.

## 로컬 검사와 CI

- 새 경계/가드 `go test -race ./internal/explorefixture`: 41 PASS/0 FAIL/0 SKIP.
- 제품 pipeline·MCP·gate 관련: 초기 38 race PASS(PG 재연결 포함), 최신 dev 통합 뒤 선택한 탐색/제품 시험 31 PASS/0 FAIL/0 SKIP.
- 탐색 선소진 예산 마감: private controller/실제 Supervisor/영속 마감까지 관련 race66 PASS/0 SKIP. 새 진단·receipt를 만들지 않고 partial/budget_limited를 보존.
- 발견 경로 리뷰 수정: 관련 race530 PASS(해당 회차 DB21 SKIP), 별도 PostgreSQL16 race4 PASS/0 SKIP, Node metadata5는 Go wrapper로 CI에도 연결.
- 최종 통합의 Supervisor·S09·관측·가드·cursor/closure: race144 PASS/0 SKIP, 호출 칸 정산 race10 PASS/0 SKIP, vet5패키지 PASS.
- 평가 PR D: Python1836 PASS, 계측기/pathmask race244 PASS, vet 통과.
- 최신 dev 통합본 Linux/Windows/Darwin `go build ./...`, 변경한 server/cmd/fixture `go vet` 통과.

추가 사용자 namespace를 두 겹 사용한 로컬 전체 회귀는 PASS2482/FAIL221/SKIP51이었다. 이 결과를 정상 회귀 통과로 쓰지 않는다. root 소유 `/var/tmp`가 해당 namespace에서 UID65534로 보이며 기존 보호된 상위 경로 검사가 거부한다(원래 환경 UID0). 같은 대표 파일 권한 단위시험은 원래 UID 환경에서 11 PASS였다. 보안 가드를 완화하지 않았다. 표준 CI는 추가 사용자 namespace 없이 망만 분리하므로, 최종 PR head의 PostgreSQL/race 전체 결과를 따로 확인한다. 원문 테스트 로그는 공유하지 않는다.

## 근거 보존

로컬의 보호된 `jindo.p1-fixture-safe-summary/v2`에 두 회차의 소스·실제 이미지/바이너리/CA 지문, 상태·variant 계수·원자료 hash/크기·제거 결과를 보존했다. DOM·헤더·쿠키·본문·키·원문 로그를 Git에 올리지 않는다. 다음은 공유 가능한 원자료 hash다.

| 모드 | CA DER SHA256 | 결과 JSON SHA256 | 수신기 기록 SHA256 |
|---|---|---|---|
| external_allowed | `d8c01d2f99935e358500e91e3edaaef08fbc4bbc3ff402b1a318ddd8118519c3` | `a28e9c16596a3655f7e32eefaae07a9b9b0bc1aa7699da0612ee69a04d6bfbc0` | `f4a19f61f98bb8bbd5e7d78a4b5ebc972a90d0f4ab77e14e8ebdbb2b5e8a8353` |
| full_local | `734cf879488ca6653d951f1115902f1e204bf0ee5e2ffdc846bc3fc86941dcd3` | `9740f7a74876049e1d6b75aaa3daefe5404207b5179d1d7e908035c056b3356e` | `54fe096d88e4849edbd297391a63ce7a1d539390874908d60462fc5064c43b37` |

실행 절차는 [하네스](browser-fixture-harness.md), 실제 제품 조립은 [pipeline](../implementation/exploration-product-pipeline.md), 의존/원본 차이는 [P1 구현 기록](../implementation/p1-exploration-restoration.md)을 따른다.

## 리뷰 후 초기 정지·실제 진단 전환 보완

이 시험은 위 소스8a21의 Chromium 회차와 별개의 통합 시험이다. 탐색 MCP/private Reader는 명시적인 시험 fixture이고, 진단은 production 생성자·기존 Supervisor/Worker·native sandbox·E② proxy·소유 TLS 수신기·실제 PostgreSQL LOGIN을 사용한다. 실제 모델 응답이나 같은 회차의 Chromium→운영 경계→계측기 두 입구 수용으로 계산하지 않는다.

`TestOwnedPostgresExplorationSelectionThenProtectedDiagnosis`에서 탐색 종료 뒤 수동 선택 대기 중 E② Acquire0·대상 GET0을 확인했다. 명시적 선택 뒤 실제 TLS GET1, E② call1/ForwardAttempted1/Responses1/Unknown0, DeepRuns1/DeepClosed1, 실제 intent Done1과 complete 결과, trusted 감사 Final·process/proxy drain·권한 반납·영속 마감이 통과했다. 관측3·추정2는 별도 private inventory에 보존되고 PostgreSQL 재조회에서도 유지된다. 레거시 E② 정적 크롤은 재실행되지 않았고 탐색 도구는 진단 프로필에 노출되지 않았다. E③ 취약점 확정은 이 시험 범위가 아니다.

초기 일시정지는 host가 없는 기본 경로와 시험 host 모두에서 재개를 기다리며, 대기 중 boundary/browser/model I/O와 진단 lease는 0이다. 취소도 I/O 없이 종료한다. 정상 재개 뒤 NOT_RUN 또는 실제 fixture Worker 완료를 보존하고 once gate가 오류로 굳거나 재실행되지 않는다. 완료 탐색 건너뛰기는 동일 private 결속·권한·저장 마감만 허용하며, 미선택 요청·철회·미완료·다른 회차·취소·저장 실패·손상 상태는 계속 거부한다.

리뷰 보완 뒤 관련 5패키지 race219 PASS/0 FAIL/0 SKIP, vet5패키지 PASS였다. 여기에는 위 실제 PostgreSQL 전환 및 진단 전 모델 예산 마감이 포함된다. 별도 오류 로그 회귀 race8 PASS는 factory·resolver·누락 capability·boundary/browser 시작 실패의 고정 단계·분류와 NOT_RUN 영속 상태, URL·자격 표식 비노출을 확인했다. 바쁨/lease, 첫 Worker의 stage 진입, 유효 private metadata의 요청 개수 상한도 별도 회귀로 고정했다. 중복되는 선택 시험 건수는 전체 고유 시험 수로 합산하지 않는다.

추가 가드 변이는 저장소 밖 복사본에서만 실행했다. baseline7 PASS 뒤 busy/lease 검사 제거는 4조합, 첫 Worker의 stage 호출 제거는 initial-work 검사, private seq 개수 상한 제거는 over-cap ingestion 검사에서 각각 assertion FAIL로 탐지했다. 빌드 실패를 탐지로 세지 않았고 원본 소스에는 변이를 적용하지 않았다.

## B16 분리 배치 재측정 (#485)

Chromium(`explore-browser`)과 Playwright MCP(`explore-mcp`)를 다른 컨테이너(다른 network namespace)로 나눈 배치다. Chromium은 브라우저 입구망과 CDP 연결망에만, MCP는 제어망과 CDP 연결망에만 붙는다. 하네스 망은 다섯 개(control/browser/direct/upstream/link)이고 기본 대역은 `/21`이다. 위 표의 핀과 다른 소스·이미지라 위 표의 수치를 바꾸지 않고 따로 적는다.

| 회차 | 소스 | image_id | 모드 |
|---|---|---|---|
| 윤재상(PR 본문) | #485 head `1af8b0d2` | `sha256:95d83347e8b5…`(PR 본문에 앞자리만) | external_allowed, full_local |
| 최민준(리뷰 재현) | #485 head `1af8b0d2` | `sha256:93fe64204f439e437c82f1d86eb5e459d72d916d9bf854b739b26c0bcc74eb75` | full_local |

| 결과 | 윤재상 두 모드 | 최민준 full_local |
|---|---:|---:|
| 실제 대상 전달 / observed / inferred | 22 / 21 / 4 | 22 / 21 / 4 |
| 외부 전달(external_allowed / full_local) | 3 / 0 | – / 0 |
| 읽기 미승인 쓰기 차단 / 승인 밖 origin 차단 / 위험 경로 차단 | 본문에 없음 | 9 / 18 / 1 |
| 진단 E② 시도 | 0 | 0 |
| 금지 수신기 차이 | 0 | 0 |
| B16 프로브 PASS | 9 / 9 | 9 / 9 |
| 정리한 소유 컨테이너/망 · 잔여 | 본문: 잔여 0 | 10 / 5 · 0 |

B16 프로브(driver 실행 중, 결과는 사적 실행 디렉터리의 `b16-probes.json`):

| 프로브 | 기대 | 두 회차 결과 |
|---|---|---|
| Chromium → MCP 제어 주소:8931 TCP·UDP | 경로 없음 | 경로 없음 |
| Chromium → MCP 연결망 주소:8931 TCP | 연결 없음 | 거부(그 주소에 리슨 없음) |
| Chromium → 엔진(driver) 제어망 주소 TCP | 경로 없음 | 경로 없음 |
| driver → Chromium CDP(연결망 주소:9222) | 경로 없음 | 경로 없음 |
| 경계 프록시 → Chromium 입구 주소:9222 | 연결 없음 | 거부 |
| MCP → 경계 브라우저 입구:8080 | 경로 없음 | 경로 없음 |
| MCP → Chromium CDP (양성) | 연결 | 연결 |
| driver → MCP 제어 주소 (양성) | 연결 | 연결 |

READY 전에 하네스가 `docker top`으로 Chromium 컨테이너에 MCP 프로세스가, MCP 컨테이너에 Chromium 프로세스가 없는지 확인했다. 윤재상 회차 8회 중 1회는 full_local에서 승인 밖 origin 차단이 19였다(나머지 18). 시간 차이인지 배치 영향인지는 구분하지 않았고, 금지 수신기 차이는 모든 회차 0이다.

범위: 이 프로브는 망 도달만 본다. CDP는 Chromium namespace의 loopback에도 열려 있어(이 판의 Chromium은 `--remote-debugging-address`를 무시) 같은 namespace의 native 프로세스는 Chromium을 조작할 수 있다. 그 프로세스가 닿는 망은 Chromium과 같다. 페이지에서 loopback CDP로 가는 요청은 `<-loopback>`으로 경계를 거치는데, 이 경로의 실제 페이지 시험은 NOT_RUN이다. 운영 경계·원본 보존율·B09(WebRTC)는 여전히 NOT_RUN이다.

## B09 WebRTC STUN (2026-11-13)

분리 배치(#485) 위 dev에 B09를 하네스에 넣은 회차다. 무통제 Chromium 양성 대조와 통제 회차 driver가 같은 fixture 페이지(`/bypass/B09`, `RTCPeerConnection`의 STUN 서버로 수신기 UDP)를 연다. STUN 서버는 수신기의 upstream IP 리터럴이다. Chromium WebRTC는 STUN 서버 이름을 `--host-resolver-rules`로 풀지 않아서(실측: 이름이면 UDP 0, 같은 주소의 IP면 STUN binding 도달) 이름을 쓰면 양성 대조가 성립하지 않고 통제 회차의 0도 경로 차단이 아니라 이름 풀이 실패가 된다.

| 항목 | full_local | external_allowed |
|---|---:|---:|
| image_id | `sha256:93fe6420…eb75`(#485 head 빌드, dev의 같은 Dockerfile) | 같음 |
| 무통제 Chromium B09: 수신기 UDP STUN binding | 4 (PASS) | 4 (PASS) |
| 통제 회차 금지 수신기 UDP | 0 | 0 |
| 실제 대상 전달 / observed / inferred | 23 / 22 / 4 | 23 / 22 / 4 |
| 외부 전달 | 0 | 3 |
| 나머지 양성 대조(B04·B05·B12·B13·B14) | 모두 PASS | 모두 PASS |
| B16 프로브 | 9 / 9 PASS | 9 / 9 PASS |
| 정리한 소유 컨테이너/망 · 잔여 | 11 / 5 · 0 | 11 / 5 · 0 |

전달 수가 앞 절(22)보다 하나 많은 것은 driver가 `/bypass/B09` 페이지를 하나 더 열기 때문이다. 통제 회차의 Chromium namespace에는 경계 입구망과 CDP 연결망 말고 경로가 없어 STUN UDP가 나가지 못한다. 범위는 내부망 역할 수신기 하나다. 메타데이터 역할로 가는 STUN·TURN(TCP/TLS 포함)은 미실행이다.


## 2026-11-13 U01 실제 Workbench 브라우저 수용

**U01 PASS — 실제 Workbench 화면 + 소유 준비 API fixture의 표시 범위.** 실제 엔진/대상 쓰기·E② 전송·실제 Qwen·유료 모델은 모두 NOT_RUN이다.
단위시험만으로 PASS를 매기지 않았다. production `Workbench`를 실제 Chromium에서 열고 인증/준비 영수증 API만 결정적 소유 fixture로 대조했다.
이 시험의 localhost UI origin과 화면에 표시한 `u01-owned.example.test` 승인 origin은 다르며, 표시 대상에는 접속하지 않았다.

| 항목 | 고정 조건 |
|---|---|
| 앱/하네스 소스 | `904f36330cf0c9ad28150abdfcf2401d6734c7fb` (UI 동작 소스 `4b8bc4f`, 하네스 포함 커밋) |
| 하네스 SHA256 | `d45d1a827f9ff35fdf68ed1b01da591aa1e4be2f8a3f1c230a3d5af1a4683415` |
| Chromium 이미지 | `jindo-p1-explore-browser:b679f7b`, `sha256:17b7b5f0fa7ed1caf03e1385deadab742b076735436a72a8aa83d05b48acc811` |
| 실제 브라우저 | HeadlessChrome/157.0.8103.15 · CDP 1.3 · 1440×1000 · scale 1 |
| 실행 방식 | 전용 비root·읽기 전용 컨테이너, cap-drop ALL, no-new-privileges, native CDP, MCP 비활성 |
| 브라우저 제약 | UI 시험용 `--no-sandbox`, host network에서 loopback UI/CDP만 사용. Fetch interception은 외부 자원을 거절. 이 구성을 제품 탐색 격리/전수 egress 통제 수용으로 세지 않음 |
| 웹 실행 | Node v22.23.4, lockfile의 독립 `npm ci --ignore-scripts --legacy-peer-deps`, Next 개발 서버 `--webpack`, loopback 39202 |
| 근거 저장 | 보호된 `/tmp/jindo-u01-browser-results-v4/`의 report.json/스크린샷, 원문 산출물은 커밋하지 않음 |

C21의 이미 승인된 자동 workflow 쓰기 범위를 웹 파서가 거절하던 연결 누락을 고쳤다.
`ai_auto_v1` + 서버 `preauthorized_scope`에만 자동 쓰기를 표시하고, 일반 회차·다른 모드·DELETE는 계속 거절한다.
승인 안내에는 실제 origin·경로·메서드, 예측할 수 없는 쓰기와 대상 상태 변경 가능성, 자동/건별 수동 확인을 시작 직전에 표시한다.

| 실제 Chromium 시나리오 | 결과 | 관찰 |
|---|---|---|
| auto_https | PASS | origin·/owned,/api/safe·GET/HEAD/POST/PUT/PATCH, 상태 변경 위험과 건별 확인 없는 자동 전송 문구가 화면 안에 보임 |
| manual_https | PASS | 동일 실제 승인 범위, 쓰기마다 사람이 확인·승인한 뒤 보낸다는 문구 |
| readonly_https | PASS | 승인된 쓰기 영역 없음. 당시 하네스는 정상 읽기 범위 처리·조회 위험 고지 자체를 검증하지 않음 |
| auto_http | PASS | 자동 쓰기 고지와 ‘암호화하지 않는 주소(http)’·서버 신원 미검증 고지 동시 유지 |
| missing_scope | PASS | 당시 고지 없음·활성 시작 없음만 확인. 승인 대기 처리 자체는 검증하지 않음 |
| invalid_delete_scope | PASS | 당시 고지 없음·활성 시작 없음만 확인. 계약 거부 표시 자체는 검증하지 않음 |

모든 시나리오에서 변경 API 요청·진단 시작·모델 호출 0. 실제 권한 서버는 fixture이며 이 시험은 DNS 소유 증명이나
Planner/Worker 성공을 증명하지 않는다. 초기 링크/누락 폰트 개발 환경 및 숨겨진 h1을 고르던 하네스 실패는 최종 PASS에 포함하지 않았다.
최종 스크린샷의 가시성은 DOM 존재뿐 아니라 실제 bounds/viewport로 검사했고 자동 HTTPS 이미지를 직접 확인했다.

| 시나리오 | fixture SHA256 | screenshot SHA256 |
|---|---|---|
| auto_https | `1a894f9f0e738ea2a07ca589f339c0c88838897daf83b1629f24ac33a4b8b4cd` | `a055d3a2b2a3228e11a602c6160b00060adb71592bfa19ff579eef72b9862b60` |
| manual_https | `94deba63443d7c65efd6376016e66a4c3b39a5d3bb2124ca17694398cf61f28b` | `7b42941ffc93cddf8fff93f5169d770f6e8db80daa49ff6f7553ad83b79fbbef` |
| readonly_https | `caa0dbbee371aaf6f562eb3a256a97a29fd00ae45415085292cf495ccc813782` | `f74156483d0575bad365f673f16749f891e3b77ee8d0e05cfc3783096322cb6a` |
| auto_http | `393f438035ef676ac3fd4c66a60adb5293629adfb1c2e24a1e74227b40833962` | `f27d3eb16424518df3f1847345b9587ea8c35d44ea534f19422d1b105c4342d5` |
| missing_scope | `d04666ff5276d387aba15b7eb3293c52fe5c4e37c3178ca5eb46bb6ebfe5503c` | `1f5406880911ae92d80fdf8f6bb410d7335f7125e643ec22d2e5ef2c3e556991` |
| invalid_delete_scope | `9c9c6629884cdb30154bd5a99ac729073b4c83c80d4f2fb30005a7d7bfe467d4` | `2b01120154f83a5edccd4aadd167137ddda82739a72c22dab7a03a70add91b0c` |

재현: 같은 이미지/Node/lockfile을 준비하고 소유 Workbench를 loopback에서 기동한다.
CDP `/json/version`이 준비된 뒤 아래 하네스를 실행한다. 하네스는 loopback URL만 받고 준비 fixture를 격리하며 모든 변경 API를 거절한다.

```sh
JINDO_U01_UI_URL=http://127.0.0.1:39202 \
JINDO_U01_CDP_URL=http://127.0.0.1:39421/json/version \
JINDO_U01_OUTPUT=/tmp/jindo-u01-browser-results \
node scripts/acceptance/u01-workbench-browser.mjs
```

이 절은 U01 화면 수용만 갱신한다. 다른 B/S/P 항목과 실제 모델/보존율의 기존 상태를 확대하지 않는다.

동일 UI 소스의 독립 lockfile 설치에서 `tsc --noEmit`, 전체 Vitest **742 PASS / FAIL0 / SKIP0**
(동시 worker 2), `npm run build:static`을 통과했다. 처음 무제한 병렬 시험의 기존 UI 3건 시간 초과는
최종 결과에 합산하지 않았으며, timeout 값을 늘리지 않고 worker 수를 제한해 재검증했다.

## 2026-11-14 U01 최신 dev·인계 PR 통합 재검증

#564의 접힌 도움말 UI와 U01 쓰기 위험 고지를 함께 보존한 뒤 다시 실행했다.
최신 dev `89ad03b5` 위 아래 head들을 순서대로 merge한 **로컬 검증 소스**이며, 공용 dev에 병합한 결과가 아니다.

| PR | 검증한 head |
|---|---|
| #559 C04/C33 | `ef5058dc3b949f9f5ffca6febf5c62cf72681d62` |
| #561 외부 시험 계정 동의 | `0fe4b0e92019f9257701082c949581cc5d0e4e41` |
| #562 E② 흐름 | `22bdff465c0dba836db74d3808b7b0181377afb0` |
| #563 커버리지 | `6f2ab18439f7189db8d025d89ec877d3d768aaaf` |
| #565 U01 | `b869cd6d293798434f40cd265c9aeaf21a7570dc` |
| #566 결정안(실행 코드 없음) | `7b7d9004db6cc8478e7a53f6962fe823922506b7` |

로컬 통합 commit `4b780f95e558b61b82694679d7650bab07d14573`, tree `ecbec572380b888eb58f97c010d0f568aee66ff8`.
위 head들을 같은 순서로 합쳐 동일 소스 tree를 재현할 수 있다. 독립 lockfile 설치의 `tsc --noEmit`,
전체 Vitest **826 PASS / FAIL0 / SKIP0**(worker 1, timeout 변경 없음), `npm run build:static` 통과.
처음 worker 2 + Go 동시 검증에서 기존 외부 모델 입력 시험 1건이 5초 시간 초과로 끝났다.
같은 시험의 분리 재현은 428ms에 통과했고, 최종 전체 시험은 병렬도만 낮춰 통과했다. 실패를 최종 PASS에 합산하지 않았다.
통합 준비 서비스 전체 race 134 PASS/SKIP0, darwin/arm64·windows/amd64 빌드도 통과했다.

개발 서버 대신 **빌드된 `web/out` production Workbench**를 loopback 39202 정적 서버로 제공했다.
브라우저 이미지 ID·버전·하네스 SHA256·viewport·비root/read-only/CDP/`--no-sandbox` 조건은 위 회차와 같고 MCP는 비활성이다.
`/tmp/jindo-kyeongjun-handoff-20261113/u01-static-integration/`에 report.json(0600)과 스크린샷을 보관했다.
보고서 SHA256은 `d8a668a7aefdbeb5f542e083582cb0b34cc6d30b6f21bb011c84a93eb638c80a`이다.
고정 fixture SHA256은 위 표와 동일하다.

| 실제 Chromium 시나리오 | 결과 | screenshot SHA256 |
|---|---|---|
| auto_https | PASS | `1e6b9e0db1b7721eaa56bb4e0a71745e28db8b7fa2a31edccfd8a10d6cf68ba0` |
| manual_https | PASS | `8a2d9e1ddefe735b292f8cde06debb0d46ab65261138c3c2569b004b2649e25f` |
| readonly_https | PASS | `682e718d39e3670aa1bbefc9a3563a302fe88349e811394b9b8b70de6f55d1c6` |
| auto_http | PASS | `2c23e8b3f269a101bf1acfd2f39d828e8f688edab1b0a6bef5258e1d06e9fe2d` |
| missing_scope | PASS | `f517a457b1e8d3799d94771ec0c371bce7c048367b93cb6db0cd18c15d5fbff9` |
| invalid_delete_scope | PASS | `a697d2c39e44c891d4fbd7b6b504133850cbbfbf0b8752d91900f4aae8eca50a` |

쓰기 범위·위험 고지의 실제 화면 안 가시성을 다시 확인했고 자동 HTTPS와 HTTP 스크린샷을 직접 검토했다.
6개 모두 변경 API 요청 0건, 외부 자원 요청 0건이며 시작 전 동의하지 않은 버튼은 비활성이다.
실제 권한 서버는 소유 준비 fixture이고, 엔진·대상 송신·실제 Qwen·유료 모델은 **NOT_RUN**이다.
이 결과는 U01 표시 수용이며 실제 탐색 격리·전수 egress·Planner/Worker 수용으로 확대하지 않는다.
시험 후 이 작업 전용 브라우저·정적 서버·PostgreSQL 컨테이너만 정리했다.


## 2026-11-14 U01 재리뷰 후 음성 대조 보강

이 회차는 #565 단독 브랜치의 production `web/out`을 사용했다. 이전 통합 회차의 소스와 구분한다.
기존 회차 표에서 하네스가 직접 확인하지 않았던 읽기 범위 처리·조회 위험 고지·승인 대기/계약 거부 주장은 위에 한계를 정정했다.

- UI·회귀시험 소스: `cfdc84a06b22558d2650c9be4eb1e0437fb52660`. 관련 131 PASS/FAIL0/SKIP0, 변경 파일 Biome·tsc·정적 빌드 통과.
- 최종 하네스/실행 소스: `0cf7636a7b8e8fe0530f6ce47961120e90a1b04b`, tree `9bfa624da7612d758c028cfd1753785e74427863`.
  UI 소스는 위 빌드와 같고 하네스의 승인 대기 note 선택만 보완했다. 전체 웹 749 PASS/FAIL0/SKIP0(worker 1).
- 자동 `ai_auto_v1` + `confirm_every_request` + POST 거절 시험과 등록 checkbox 위험 고지 단언을 보강했다.
  별도 소스 복사본에서 승인 모드 조건·등록 위험 문장을 각각 제거하면 해당 시험이 1건씩 실패한다(2/2 검출). 원본 소스는 변경하지 않았다.
- 표시에서 파서가 거부하는 DELETE·모든 요청 확인 모드 문구를 정리했다. 서버 권한·승인 범위는 바꾸지 않았다.
- 등록 위험 문장은 기존 쓰기 동의의 설명 보강이다. `write_consent=auto_without_confirmation_v1` 판을 유지하며
  기존 대상 등록에 재동의를 요구하지 않는다. 이 판의 동의가 없는 등록은 자동 모드에서 계속 GET·HEAD만 사용한다.
- Node `v22.23.4`, 동일 Chromium 이미지 `sha256:17b7b5f0fa7ed1caf03e1385deadab742b076735436a72a8aa83d05b48acc811`,
  실제 `Chrome/157.0.8103.15`, CDP 1.3, 1440×1000. 전용 비root·읽기 전용 컨테이너·cap-drop ALL·no-new-privileges,
  임시 설정/캐시 경로를 사용했다. UI fixture용 `--no-sandbox`·host network이며 MCP는 비활성이다.
  이 배치를 제품 탐색 격리·전수 egress 통제 수용으로 계산하지 않는다.
- loopback 정적 UI `39203`·CDP `39423`에서 실행했다. 준비 API는 결정적 소유 fixture이며 실제 대상에는 접속하지 않는다.
  산출물은 보호된 `/tmp/codex-u01-review-20261114-v2/`에 보관하고 원문 로그·이미지는 커밋하지 않는다.
- 하네스 SHA256: `19c3c6fcd8ecd915db35db7a91bc8d1a2fb5d2a898545e00a5c2e2079bff63f7`.
- report.json SHA256: `f821266f1eacf715d4cf8c1e9c8eac696e1f0edf9cb8e022c24a474653b7569f`. fixture SHA256은 위 6개 표와 동일하다.

고정 500ms 대기를 제거했다. 정상/읽기 전용은 서버 범위가 처리된 표시와 시작 버튼을, 범위 없음은 명시적 승인 대기를,
잘못된 DELETE 범위는 명시적 응답 계약 오류를 기다린 뒤 고지·시작 상태를 대조한다.
읽기 전용은 조회도 상태를 바꿀 수 있다는 실제 표시까지 확인하며 스크린샷에서도 직접 확인했다.
첫 보강 하네스는 첫 note만 골라 `missing_scope`가 시간 초과했다. 모든 note에서 승인 대기를 찾도록 고친 뒤
전체 6개를 재실행했으며 실패 회차를 최종 PASS에 합산하지 않았다.

| 실제 Chromium 시나리오 | 최종 확인 | screenshot SHA256 |
|---|---|---|
| auto_https | PASS — 실제 범위·쓰기 위험·자동 전송 고지와 미동의 시작 비활성 | `5533c18909d7f1f8a9e10912e5c17d68acd12b392cb6f02395076b6ea4a58feb` |
| manual_https | PASS — 실제 범위·쓰기 위험·건별 승인 고지와 미동의 시작 비활성 | `63ee7b1052301e75620d23c86f63d379011a57d5320611771e5a450f268545f6` |
| readonly_https | PASS — 정상 읽기 범위 처리·조회 위험 고지·쓰기 고지 없음·미동의 시작 비활성 | `c5e02c5099080d3dddea3b194b16b4bb42cbfd695a0646a35a827d6d1768d532` |
| auto_http | PASS — 자동 쓰기 고지·HTTP 평문 고지 | `ab8fd753be2e12b734e752f1763721a0a19b10e88a862cddb8db9005cea8e82e` |
| missing_scope | PASS — 명시적 승인 대기·서버 범위 없음·시작 버튼 없음 | `15b7bc7e96225817697e7559ee19411ca425d6ccffc83aec7ba2e71c8cb0b473` |
| invalid_delete_scope | PASS — 명시적 계약 오류·서버 범위 없음·시작 버튼 없음 | `26ee234905993ee9db5598db9dcc52d6440c696af677566916df9c57c6f2c822` |

모든 시나리오의 변경 API 요청·진단 시작·모델 호출은 0이다. 실제 엔진·대상 송신·Qwen/유료 모델 수용은 **NOT_RUN**이다.


## U01 — 최신 인계 9개 PR 통합 재수용 (2026-11-14)

공용 dev에 병합한 결과가 아니라 아래 head들을 순서대로 merge한 로컬 검증 소스다. 소스 SHA `a210bc857fd92371401fcf1b1935a816f4c24512`, tree `dbec393fe3a39cd93f17263a55f355da34de3d05`다. 각 PR의 기존 정상 커밋과 원격 보완을 보존했으며 통합 merge 충돌은 없었다.

| 순서 | 기준/PR | 정확한 head |
|---|---|---|
| 기준 | dev | `7187ba577c3eae57f935d1e33ebed4ffbb3975b0` |
| 1 | #559 C04·C33 | `0408f4e89cf3dc3de75e404048d7373fea99b802` |
| 2 | #561 외부 시험 계정 동의 | `53c97545ce3dda9fa7e8cf3e83cb216f03e4ae20` |
| 3 | #562 E② 흐름 | `22bdff465c0dba836db74d3808b7b0181377afb0` |
| 4 | #563 커버리지 | `6f2ab18439f7189db8d025d89ec877d3d768aaaf` |
| 5 | #565 U01 | `1b127851446c67cb2288d5ffa9f1b85846a9a51f` |
| 6 | #566 결정 | `f1d53b6cfcdbb1a56fd5bf12bb9f000a1d089497` |
| 7 | #576 C34 상한 | `d6e15190499c2cb2bd39febd4c2f1b480ad2a432` |
| 8 | #580 C34 압축 | `2f7781dc39124d9a49d7a757fb0dcb72aaf3aecd` |
| 9 | #586 C35 | `9f3cd92449f3bbdf479d0ebc91d95619c7baff4e` |

환경은 Linux 비root·Go1.26.3·Node22.23.4다. 실제 production 정적 빌드(`NEXT_PUBLIC_MOCK=0`, `NEXT_EXPORT=1`)의 Workbench를 loopback 39205에서 열었다. 브라우저는 기존 고정 이미지 `sha256:17b7b5f0fa7ed1caf03e1385deadab742b076735436a72a8aa83d05b48acc811`의 Chrome157.0.8103.15/CDP1.3이며 1440×1000이다. 비root·read-only·cap-drop ALL·no-new-privileges·native CDP, UI 시험용 --no-sandbox/host network 조건은 이전 회차와 같다. HOME/config/cache는 컨테이너 tmpfs만 사용했고 MCP는 켜지 않았다. 이 배치를 제품 탐색 격리·전수 egress 통제 수용으로 세지 않는다.

하네스는 기존 보강판 그대로(SHA256 `19c3c6fcd8ecd915db35db7a91bc8d1a2fb5d2a898545e00a5c2e2079bff63f7`)다. 준비 API는 소유 결정적 fixture이며 변경 API 요청 합계0, 실제 진단 시작·모델 호출0이다. 대상 scope와 위험 고지·자동/건별 승인 구분·HTTP 평문 고지·범위 없음/미지원 DELETE 거절을 실제 브라우저에서 확인했다.

| 시나리오 | 결과 | screenshot SHA256 |
|---|---|---|
| auto_https | PASS | `5533c18909d7f1f8a9e10912e5c17d68acd12b392cb6f02395076b6ea4a58feb` |
| manual_https | PASS | `63ee7b1052301e75620d23c86f63d379011a57d5320611771e5a450f268545f6` |
| readonly_https | PASS | `c5e02c5099080d3dddea3b194b16b4bb42cbfd695a0646a35a827d6d1768d532` |
| auto_http | PASS | `ab8fd753be2e12b734e752f1763721a0a19b10e88a862cddb8db9005cea8e82e` |
| missing_scope | PASS | `15b7bc7e96225817697e7559ee19411ca425d6ccffc83aec7ba2e71c8cb0b473` |
| invalid_delete_scope | PASS | `26ee234905993ee9db5598db9dcc52d6440c696af677566916df9c57c6f2c822` |

report.json SHA256은 `a865cbfbfaa279662fc2737cc9288e8df4e47d99f90b491769d3c2c01f239b09`다. fixture 해시는 앞선 보강 회차와 같다. 원문 report/스크린샷은 보호된 `/tmp/jindo-kyeongjun-lifecycle-20261114/integrated-u01/`에만 보존하고 커밋하지 않았다. 초기 실행은 이미지에 없는 최신 Chromium 경로를 사용해 기동 전 실패했고, 실제 이미지 경로를 확인한 뒤 read-only HOME의 CDP 준비 실패를 바로잡았다. 이 두 시도는 NOT_RUN이며 위 PASS6건에 포함하지 않는다. 전용 UI 컨테이너·정적 서버를 정리했다.

통합 회귀: 준비 서비스 race PASS209/SKIP1(대용량 측정 opt-in), 제품 명령 race PASS369/SKIP3(기존 PostgreSQL 의존 시험), FAIL0. 숫자는 패키지·상위 subtest를 포함한 go test -json pass 이벤트다. 웹 tsc·전체 Vitest833/833·정적 production 빌드·전체 Go build/vet 통과. C34 constructor I/O fallback·회계 변조 거절 보완과 C35 실제 소유 DNS/TLS 시험을 포함했다.

별도 PostgreSQL16 fixture는 네트워크/TCP를 열지 않고 tmpfs DB·소유 Unix socket만 사용했다. 실제 로그인→공개 관리 mux→작업 권한→pause/resume/활동 수신기와 모델 키 보호 시험은 race PASS8/FAIL0/SKIP0이다. 앞선 DB 환경 실패를 통과로 바꾸지 않고 socket 구성을 바로잡은 별도 실행으로 기록한다. DB fixture는 제거했다. 이 결과를 Planner/Worker·실제 Qwen·새 Docker 제품 종단 진단의 수용으로 사용하지 않는다(모두 NOT_RUN).
