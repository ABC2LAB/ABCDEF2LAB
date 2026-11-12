# P1 실제 Chromium·MCP fixture 결과 (2026-11-12)

**기능 통합은 두 모드 PASS, B16 제어망 검사는 FAIL이다. 전체 운영 탐색 경계 수용 통과가 아니다.** 실제 고정 Chromium·MCP와 원본 Worker 루프를 사용했지만 모델 provider와 경계는 명시적 시험 fixture다. 제품의 운영 Boundary·private Reader·Docker lease 어댑터는 기본 nil이며 활성화하지 않았다. 실제 Qwen·원본 대비 보존율은 NOT_RUN이다.

## 실행 기준과 동일 조건

구현 시작 dev는 `b679f7b7e62744943f88f4334c49820265811633`, 최종 통합 dev는 `57020d0827b27c7d891336b0fb49c45dc26402e5`다. 실제 브라우저 회차는 다음 소스를 고정 빌드했다. 이후 dev 병합은 #444 작업 공간과 #450 중지 결과이며, fixture/MCP 코드는 바꾸지 않았다. 해당 통합의 전체 회귀는 최종 PR CI에서 별도로 확인한다.

| 항목 | 실제 핀 |
|---|---|
| 브라우저 시험 소스 | `07bed28d0832e75e1f9964889253b7f662eaadd2` |
| image_id | `sha256:17b7b5f0fa7ed1caf03e1385deadab742b076735436a72a8aa83d05b48acc811` |
| mcp_version | `0.0.84` |
| proxy_binary_sha256 | `a4d516c6eeff0bb7c1bc27a55cb0fe65c753beb548580be3bdbd5fe64acbf320` |
| target_binary_sha256 | `555b5686444929e990a16d190809a1ed8079d71194f78e6d4fede8b666895483` |
| metadata_script_sha256 | `c44327518252d586c4c93785b5a8a0921b3c8201ee4a030e5b73ecadb5427b91` |
| driver_binary_sha256 | `ee066ed462507ada6f1a0667719bbc5fff698f53e06481b92c098023afc7038a` |
| 하네스 SHA256 | `3f03144c207e2fb38d1546c32640978a2674630fa09d58fb0e8f2646cdcd3696` |

양쪽 대상 요청 상한 128, 시간 300초, 동시 연결 16, 응답 2MiB다. 외부 자원 상한만 승인 모드 20/완전 로컬 0이다. Open 입력과 실제 proxy 상한을 정확히 대조한다. 요청 실패도 예산에서 차감하고 대상/외부 상한은 따로 집행한다. 모델은 결정적 시험 provider이며 20회 호출은 실제 Qwen 호출·사용량·비용이 아니다.

네 internal 망(control/browser/direct/upstream), 고정 MCP Host/IP/port, 읽기 전용 mount, 별도 공개 CA를 사용했다. TLS 오류 무시 플래그는 없다. 인증서 개인키·제어 키·Docker socket은 browser에 없다. 독립 양성 Chromium만 명시적인 upstream-only 시험 컨테이너에서 프록시 없이 같은 소유 fixture에 접속했다. 운영 우회 실행기가 아니다.

| 결과 | external_allowed | full_local |
|---|---:|---:|
| 실제 대상 전달/observed 근거 | 22 / 22 | 22 / 22 |
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

이 건수는 API 발견 수나 원본 대비 탐색률이 아니다. 페이지/자원/동적 요청의 관측 근거를 함께 센다. 저장 후 stage=awaiting_selection이며 실제 취약점 검증·E③ 확정이 아니다. 진단 시도 0은 탐색 회차에서 진단을 보내지 않았다는 뜻이며 D01의 실제 진단 회차 수용을 대체하지 않는다.

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

`full_local`에서 browser 컨테이너의 Node 자식이 같은 namespace의 MCP control IP:8931로 TCP 연결했다. `same_namespace_control_reached`, **FAIL**, API 요청은 보내지 않았다. 외부 승인 회차에서는 lease 종료 뒤 probe를 시도해 UNDECIDABLE(browser_already_stopped)이므로 막혔다고 쓰지 않는다. 이 실제 실패를 기능 통합 PASS에 합쳐 숨기지 않는다.

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
| P08/P09 | PASS | CDN 역할 JS/CSS/SVG 렌더링과 완전 로컬 HTTP/TCP0 |
| B01 | PASS | 명시한 두 실행 namespace의 직접 대상 TCP 경로 |
| B02/B03 | NOT_RUN | Chromium proxy flag 제거·독자 DNS wire 회차 미실행 |
| B04/B05 | PASS | 위 양성 variant와 동일한 fixture 수신기/리다이렉트 |
| B06/B07/B08/B09 | NOT_RUN | SW·WS·QUIC·STUN의 대응 양성/차단 묶음 미실행 |
| B10 | NOT_RUN | 불신 TLS 단위 거부는 PASS, 실제 Chromium 플래그 주입 종단은 별도 |
| B11 | NOT_RUN | Node 직접 TCP 부분은 PASS, curl/전체 자식 프로토콜 수용 아님 |
| B12 | UNDECIDABLE | prefetch/TCP 부분은 PASS, resolver 고정이라 DNS wire 자체 미실행 |
| B13 | PASS | 위 blob/data Worker fixture variant |
| B14 | PASS | 쓰기 미승인 송신0 범위. 차단된 모든 경로의 발견 등록은 미검증 |
| B15/B17 | NOT_RUN | CDN 탐색 이동·직접 Node/curl 헤더 위조의 정본 종단 묶음 미실행 |
| B16 | FAIL | Node 자식의 자기 MCP 제어 TCP 도달. 키 mount 부재는 이를 대신하지 않음 |
| B18 | UNDECIDABLE | 실제 페이지의 WebMCP API가 none. 이름 충돌 등록 자체가 일어나지 않음 |
| S01 | PASS | 실제 동적 근거·필드 이름 저장과 별도 PG 재조회 시험. 전체 제품 종단은 별도 |
| S02/S03/S05/S06 | NOT_RUN | origin/경로·위험 쓰기·변조/취소 단위 범위는 PASS, 정본 전체 제품 회차는 별도 |
| S07/S08 | NOT_RUN | 계정 분류/인증 준비·로그인 수용 부재. 비인증을 인증 성공으로 세지 않음 |
| S09/S10 | NOT_RUN | redaction/페이지 비신뢰 입력 단위 시험 PASS, 자격 표식의 실제 모델 종단은 별도 |
| S11 | PASS | 공개32와 별도256/64KiB inventory·누락·PG 격리 단위 시험 |
| S12 | NOT_RUN | 기존 path-mask 회귀는 보존, 운영 경계 가린 기록 exporter 미연결 |
| S13 | NOT_RUN | 이 회차는 read_only. 승인 쓰기 종단은 실행하지 않음 |
| D01/D02 | NOT_RUN | 프로필·once gate·입구 대조 단위 시험 PASS, 실제 진단/계측기 종단은 별도 |
| U01 | NOT_RUN | 승인 UI 담당 수용 미실행, Workbench 구조 변경 없음 |
| V01 | NOT_RUN | VAmPI 인스턴스/동일 조건 비교 미실행 |
| R-P/R-F/R-R | NOT_RUN | 실제 모델·원본 짝·운영 exporter 부재. 보존율을 만들지 않음 |

P04/S04는 정본에서 삭제된 항목이다. fixture HTTP-over-Unix 제어 API를 운영 계약의 line JSON 프로토콜로 주장하지 않는다. 원자료 hash와 핀은 재검토 근거이며 이 fixture bundle은 공식 서명 run_record가 아니다. 실제 보존율의 서명·drain·마감은 PR D의 별도 검증을 따라야 한다.

## 로컬 검사와 CI

- 새 경계/가드 `go test -race ./internal/explorefixture`: 41 PASS/0 FAIL/0 SKIP.
- 제품 pipeline·MCP·gate 관련: 초기 38 race PASS(PG 재연결 포함), 최신 dev 통합 뒤 선택한 탐색/제품 시험 31 PASS/0 FAIL/0 SKIP.
- 발견 경로: 225 단위 PASS, 별도 PostgreSQL16 race4 PASS, Node metadata5 PASS.
- 평가 PR D: Python1830 PASS, 계측기/pathmask race244 PASS, vet 통과.
- 최신 dev 통합본 Linux/Windows/Darwin `go build ./...`, 변경한 server/cmd/fixture `go vet` 통과.

추가 사용자 namespace를 두 겹 사용한 로컬 전체 회귀는 PASS2482/FAIL221/SKIP51이었다. 이 결과를 정상 회귀 통과로 쓰지 않는다. root 소유 `/var/tmp`가 해당 namespace에서 UID65534로 보이며 기존 보호된 상위 경로 검사가 거부한다(원래 환경 UID0). 같은 대표 파일 권한 단위시험은 원래 UID 환경에서 11 PASS였다. 보안 가드를 완화하지 않았다. 표준 CI는 추가 사용자 namespace 없이 망만 분리하므로, 최종 PR head의 PostgreSQL/race 전체 결과를 따로 확인한다. 원문 테스트 로그는 공유하지 않는다.

## 근거 보존

로컬의 보호된 `jindo.p1-fixture-safe-summary/v2`에 두 회차의 소스·실제 이미지/바이너리/CA 지문, 상태·variant 계수·원자료 hash/크기·제거 결과를 보존했다. DOM·헤더·쿠키·본문·키·원문 로그를 Git에 올리지 않는다. 다음은 공유 가능한 원자료 hash다.

| 모드 | CA DER SHA256 | 결과 JSON SHA256 | 수신기 기록 SHA256 |
|---|---|---|---|
| external_allowed | `88b70d3152aaef01588276361f94af365e3758014208f3b1c0043c6aa287348b` | `27882366d09075e884e7acffac563882e04953cfed7d3cb4b70892d77d64af6e` | `67285e0a35b02e45bc601d50ceff15aa7cde6dd85f5a8a62c0cbedd122c949f6` |
| full_local | `ffa63b49012585850fca17ff1dd3bc839dbc0098a2f61cd72c340884943863bc` | `1570cf262e5516d452cf4ea95a73de9c29e6e8987aa31624ecb8616391425371` | `9fbde75632fe6c2928da9827fc8a585988a29efb8b44d3727445d0dab1659b91` |

실행 절차는 [하네스](browser-fixture-harness.md), 실제 제품 조립은 [pipeline](../implementation/exploration-product-pipeline.md), 의존/원본 차이는 [P1 구현 기록](../implementation/p1-exploration-restoration.md)을 따른다.
