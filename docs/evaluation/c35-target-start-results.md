# C35 실행별 DNS·TLS 재검증 수용 기록

검증일: 2026-11-14(KST). 정상 구현 SHA: `446f8a8c44a04e4da09dd79704a5f0cd61d6a876`.
최종 재검증 소스: `1a8b6726be3502aebc844c17daed049176fb9e40`.
기준 dev는 최초 `85b32174`에서 최신 `974ef30910a6d6a829d7fbbb16969ec81cdda17f`로 merge했다. API Recon skillDir와 C35 실행 증거 인자를 모두 보존해 시험 충돌을 해결했다.
계약: [#566 결정](https://github.com/ABC2LAB/ABCDEF2LAB/pull/566)의 C35 1~6. 최민준이 `f1d53b6c` 문서를 승인한 범위를 구현한다. 사용자가 로컬 구현·시험·PR 제출 재개를 승인했으며, **병합은 윤재상 리뷰 후 팀원이 merge commit으로 수행한다.**

## 구현

- 매 시작 TXT 소유 증명·DNS를 다시 조회한다. 중복 제거 후 최대 32개이며 모든 주소가 설치 주소 정책과 보호 endpoint 검사를 통과해야 한다. 승인 시 주소 집합과의 정확 일치는 요구하지 않는다.
- 고른 주소에서 TLS 신뢰 체인·hostname·유효 기간을 검증한다. 승인 당시 SPKI와의 정확 일치는 요구하지 않는다. TLS 검증을 생략하거나 오류를 성공으로 바꾸지 않는다.
- 검증한 IP/SPKI를 private `productTargetConnection`에 job ID와 함께 넣는다. 실제 `issue`가 쓰는 E② policy.json과 authority.json의 digest, 탐색 factory의 경계 대상에 이 값을 복사한다. 다른 job·미검증 연결은 거절한다.
- 승인 기록·revision·대상 계정·초안 scope/digest·과거 보고서용 참조는 그대로다. 이전 실행은 새 주소·인증서를 획득하지 않는다. 실제 연결의 SPKI 고정 검증은 계속 적용한다.
- 대상 승인 기간 코드 상한은 720시간이며 Workbench 기본값 4시간은 유지한다. 인증서 만료로 승인 기간을 줄이지 않고 매 시작/실제 TLS 연결에서 검증한다. 만료·철회는 신규 시작 및 실행 중 전송 검사를 닫는다.
- TLS 접촉 보호 저널 v1에 선택 필드 job_id와 tls_spki_sha256을 추가한다. 후자는 검증 성공 completed에만 쓴다. 기존 사건은 선택 필드 없이 읽힌다. 원문 인증서·키·오류를 기록하지 않는다.
- 감사 append 실패를 다른 후보 IP의 정상 연결로 가리지 않는다. 승인/시작 순회는 즉시 실패한다. DNS 또는 감사 저장 중 철회는 TLS 전에 다시 검사한다.
- 설치 상한·준비 저널 압축(C34), 추가 모델·제공자, TXT 캐시, 승인 기록의 자동 갱신·권한 확대는 포함하지 않는다.

## 실제 로컬 수신기 시험

환경: Go 1.26.3, Linux amd64, 비root. 별도 DB·브라우저·모델·Docker는 사용하지 않았다. 소유 사설 IPv4 두 개와 동적 포트를 사용했고 Go의 net.InterfaceAddrs로 두 주소의 실제 호스트 소유를 먼저 검사했다. loopback/metadata 정책 예외와 임의 네트워크 주소 추가는 없다.

DNS는 소유 주소에 바인딩한 로컬 UDP 서버이며 설치의 ResolverAddress 경로로 연결했다. TLS는 시험 CA로 서명한 서로 다른 키의 인증서와 원문을 출력하지 않는 실제 HTTPS 수신기 두 개다. 제품의 probeTLS를 stub으로 바꾸지 않았다. E②는 실제 NewExecution/Call.Do와 요청 예산·권위 검사·SPKI 고정을 쓴다. E② recorder는 시험용 수신 확인 구현이며 영속 감사 전체 수용을 대신하지 않는다. 별도의 제품 TLS 접촉 저널은 실제 보호 파일을 닫고 다시 열어 job/SPKI를 대조했다.

```sh
ARTEX_C35_OWNED_IPS=<소유_IP_A>,<소유_IP_B> go test -race -json \
  ./cmd/first-controlled-diagnosis \
  -run 'Test(C35|ProductTarget|ProductExploration)' -count=1
```

| 대조 | 결과 | 실제 관측 |
|---|---|---|
| 최초 승인·실행 A | PASS | 실제 TXT/DNS·TLS 검사 뒤 A 수신기 HTTP 1건 |
| 같은 origin의 DNS 풀·인증서 교체 후 실행 B | PASS | B 수신기 HTTP 1건, 승인 기록은 동일 |
| DNS 변경 후 실행 A 재요청 | PASS | A 수신기 누계 2건, B에는 추가 요청 없음 |
| A 인증서 교체 후 기존 실행 A | PASS | 고정 SPKI 불일치로 HTTP 추가 0건 |
| 새 실행 C | PASS | 새 인증서 검증 후 HTTP 성공 |
| 미승인 origin·허용 경로 밖·redirect 목적지 | PASS | E② 거절, 수신기 HTTP 추가 0건 |
| DNS 풀에 metadata/거부 주소 혼입·TXT 누락 | PASS | 시작 거절, 연결 증거 없음, HTTP 추가 0건 |
| hostname 불일치·불신 CA·만료 인증서 | PASS | 실제 TLS 검증 실패, 연결 증거 없음, HTTP 추가 0건 |
| 철회 후 새 실행 및 A/B/C 전송 | PASS | 새 DNS 조회 0건·HTTP 추가 0건, E② NotSent 및 forward_attempted=false |
| 회차별 실제 TLS 감사 복원 | PASS | A/B/C job ID·실제 SPKI·target ID 일치 |
| 실제 탐색 경계 | PASS | 승인 이력의 주소/SPKI를 일부러 다르게 둬도 실행 전용 증거로 소유 수신기 HTTP 1건; 다른 job/증거 없음 거절 |
| 실제 issue 파일 | PASS | 새 IP/SPKI를 policy.json에 기록, authority digest 일치; 다른 job 거절은 파일 생성 전 |

리다이렉트 시험은 302 응답 수신 뒤 미승인 Location 요청과 경로 이탈을 E②에 명시적으로 전달해 거절을 확인한다. 자동 redirect follow를 실행했다는 주장이 아니다. 실행 중 DNS 재결속이나 기존 실행의 인증서 자동 갱신은 허용하지 않는다.

## 회귀·음성 검증

- 제품 명령 패키지 전체 race: 최초 PASS 이벤트 348, 최신 dev 병합 뒤 **351**, FAIL 0, SKIP 3. 제외는 전용 PostgreSQL이 필요한 기존 검사/보고서 시험 3개다.
- 최종 관련 race: PASS 이벤트 59, FAIL 0, SKIP 0. 실제 수신기 opt-in을 포함했다. 숫자는 go test -json의 패키지·상위 subtest를 포함한 pass 이벤트다.
- DNS 중복 제거 32/33개, 0/720/721시간, 풀의 두 번째 보호 주소, DNS 도중/감사 저장 직후 철회, 감사 첫/두 번째 append 실패와 다른 IP 재시도 금지를 독립 시험했다. 오류 원문 비노출·과거 승인/보고서 참조·계정 결속 회귀도 제품 전체 시험에 포함된다.
- 전체 go build ./...·go vet ./..., darwin/arm64·windows/amd64 빌드 통과.
- 웹 tsc 통과, 대상 승인 패널 18/18 PASS, 전체 Vitest 48파일·740/740 PASS. 최초 일반 샌드박스 실행은 기존 localhost 수신기 시험 3개가 listen EPERM으로 실패했다. 동일 코드·시간 제한으로 로컬 수신기 권한을 허용한 실행에서 전부 통과했으며 timeout을 늘리지 않았다.
- 정상 커밋 뒤 별도 Go overlay 변이 **11/11 검출**: 과거 DNS 집합 정확 일치 재도입, 과거 SPKI 정확 일치 재도입, 전체 보호 주소 검사 제거, TXT 생략, TLS hostname/chain 검증 생략, 감사 직후 철회 검사 제거, issue job 결속 제거, 탐색 job 결속 제거, DNS 상한 8개로 축소, 승인 상한 24시간으로 축소, 감사 실패 뒤 다른 IP 순회 허용. 모두 컴파일 실패가 아닌 독립 시험 assertion 실패로 검출했다. TLS 검증 생략 변이는 소유 hostname 음성 fixture에만 사용했으며 제품 소스에 적용하지 않았다. 최신 dev 병합 소스 `1a8b6726`에도 같은 변이 11/11 검출과 전체 build/vet/교차 빌드 성공을 다시 확인했다.

실제 OpenRouter/Qwen·Planner/Worker 전체 진단·새 Docker 제품 종단 수용은 **NOT_RUN**이다. 이 기록은 C35 로컬 DNS/TLS·실행 정책·탐색 경계 수용으로 한정한다. 유료 호출 승인은 이 작업에 포함되지 않는다.

## 2026-11-14 재리뷰 보완

정상 소스 `6a379716`에서 승인 기간 상한을 요청 계약과 레지스트리의 공통 `MaxTargetApprovalHours=720`으로 연결했다.
HTTP 핸들러→준비 요청과 실제 productPreparationEndpoint→레지스트리에서 24·25·720시간을 허용하고 721시간을 입구에서 거절한다.
등록은 pending만 만들며 소유 증명·실제 승인 없이 실행 권위를 만들지 않는다. Workbench 기본값4시간은 유지한다.

전체 TXT/DNS/TLS 검사 기한8초를 유지하면서 주소마다 최대3초와 남은 시간/남은 주소 수 중 짧은 기한을 적용한다.
실제 probeTLS도 연결·핸드셰이크 전체를 주소별 기한으로 묶는다. 주소2개/4개 풀에서 앞 주소가 정체돼도 뒤 정상 주소를 선택하고,
이미 기한이 종료된 뒤에는 남은 주소의 attempt/completed 감사 기록을 추가하지 않는 것을 대조했다.
TLS 접촉 중 철회와 철회 뒤 TXT/DNS0도 고정했다. 미확인 감사·SPKI·설치 오류를 DNS 불일치라고 추정하지 않고
`target_connection_unavailable`로 보존하며, 후보 순회 중 기한 소진은 `target_connection_timeout`, 취소는 일반 연결 검증 불가로 구분한다. 두 새 사유는 한국어로 안내한다.

`AllowPrivateTarget`은 승인 시점 프로파일·과거 보고서 입력 검증용 값임을 주석에 명시했다. 새 실행 IP의 실제 사설망 허용은
`addressAllowed`·`checkDestinations`가 검사한다. HTTP는 TCP 연결 가능성만 확인하고 서버 신원을 인증하지 않는다는 한계를 계약에 적었다.

- 제품 명령 전체 race: 개별 test/subtest **356 PASS / FAIL0 / SKIP4**. SKIP은 기존 PG3건과 소유 주소 opt-in1건이다.
- 소유 주소 opt-in은 호스트 소유가 확인된 사설 IPv4 두 개로 별도 실행해 **7 PASS / FAIL0 / SKIP0**.
  실제 DNS/TLS·E② 수신기에서 주소/인증서 교체·이전 실행 격리·TLS 오류·철회 뒤 전송 차단을 다시 확인했다.
- HTTP 요청 경계 race **6 PASS**, 준비 계약/서비스 전체 race **125 PASS**, 각각 FAIL0/SKIP0.
- 관련 웹118 PASS/FAIL0/SKIP0, 변경 파일 Biome·tsc·정적 빌드, 전체 Go build/vet·공백 검사 통과.
- 정상 커밋을 보존한 Go overlay에서 요청 상한·레지스트리 상한·주소별 문맥·실제 핸드셰이크 기한·TLS 뒤 철회·사유 분류를
  각각 되돌려 **6/6 단언 실패로 검출**했다. 원본은 변경하지 않았다.

이 보완의 실제 모델·유료 API·새 Docker 제품 종단·브라우저 수용은 NOT_RUN이다. 기존 회차의 UI 수용을 이번 실행으로 계산하지 않는다.
