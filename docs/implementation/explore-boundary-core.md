# 탐색 경계 판정 코어 (P1-A 1단계)

[탐색 경계 계약](../contracts/exploration-boundary.md)(제안) §2~§6의 요청별 판정·상한·기록을 `exploreboundary` 패키지로 구현한 단계다. **네트워크 리슨·TLS 가로채기·제어 소켓·대상 등록 저장소 읽기·제품 조립은 아직 없다.** 운영 host adapter 기본값 nil은 그대로이고, 이 단계로 탐색 경계가 동작한다고 주장하지 않는다.

## 있는 것

- `targetaddr`: 대상 등록(`urlTargetIPAllowed`)과 경계가 같이 쓰는 주소 분류. 규칙은 옮기기 전과 같다. `Metadata`는 link-local unicast와 알려진 메타데이터 주소(multicast 제외).
- `exploreboundary.Open`: 서버가 넘긴 결속(`exploration.Binding`, 원본 SourceDigest)으로 `TargetSource`에서 승인 대상을 직접 읽는다. ref·revision이 다르거나 만료·SPKI·경로 접두가 비정상이면 열지 않는다. 시간 상한은 결속 시간과 대상 만료 중 빠른 쪽이다.
- `Session.Admit`: 요청마다 판정한다. 요청마다 `TargetSource.Current`로 철회·만료를 다시 보고, 같은 판인데 연결 주소·SPKI가 바뀌면 경계 장애로 닫는다.
  - 승인 origin: 원문이 정규 경로여야 하고(`%`·`\`·`//`·점 조각 거부, 정리하면 범위 안이 되는 경로도 거부), 승인 접두 안·제외 접두 밖·위험 단어 없음이어야 한다. read_only는 GET·HEAD, approved_writes는 대상 승인 메서드이며 DELETE는 늘 막는다. WebSocket은 approved_writes에서만. 연결은 등록 때 고른 주소로만 하고 SPKI 고정값을 붙인다. 그 주소가 제어 끝점이면 막는다.
  - 외부 호스트: full_local은 전부 막고, external_allowed는 브라우저 입구·GET/HEAD·이동 아님만 허용한다. 경계가 직접 이름을 풀고 **풀린 주소 중 하나라도** 내부·메타데이터·제어면 막는다. 외부는 시스템 루트로 검증한다(SPKI 없음).
- 상한: 대상 요청 수(`max_requests`)와 시간은 닿으면 그 요청을 막고 세션을 닫는다. 외부 자원 수와 동시 연결 수는 그 요청만 막는다. 동시 요청 32개 경쟁에서도 대상 요청 수는 상한을 넘지 않는다.
- `Ticket.Done`: 전달한 요청의 결과(상태·바이트·이름뿐인 query/body 키·content-type·응답 해시)를 기록한다. 승인 대상 요청만 private 관찰(`diagnosisflow.ExplorationObservation`)이 된다. 외부 자원은 관찰이 아니다.
- `Session.Close`: 새 요청을 막고 진행 중 요청이 끝날 때까지 기다린다. 못 기다리면 연결이 남은 상태를 돌려주므로 마감 검증이 실패하고, 탐색 몫은 모름으로 남아 진단 예산이 열리지 않는다([P1-E](exploration-product-pipeline.md#회차-요청-예산의-탐색진단-분할)). `TargetRequests`는 admission 때 세므로 실패·결과 불명 요청도 몫에 든다. `Closed()` 신호로 네트워크 조립이 진행 중 연결을 끊는다.
- 기록(`Records`): 계약 §4 칸. 헤더·쿠키·본문·query 값 없음, 경로는 설치가 넣는 `PathPattern`(평가는 계측기 키, 제품은 표시용)으로만, 외부는 호스트 이름만. 차단 행은 `MaxDeniedRecords`까지만 남기고 나머지는 수만 센다. 보호 저장소 전용이다.
- private Reader·Sequences: 같은 결속(SourceDigest 포함)으로만 읽는다. 세션이 닫히면 Session은 오류다.

## 계약 제안과 다르게 정한 것(리뷰 필요)

- 외부 자원 상한·동시 연결 상한은 세션을 닫지 않는다. §3-3은 "상한에 닿으면 세션 마감"이지만 §6의 마감 신호는 시간·대상 요청 상한이고, external_allowed에서 외부 상한 0이면 첫 CDN 요청에 탐색 전체가 끝나기 때문이다.
- 이름 풀이 실패는 `unresolved_host`로 남긴다(§4 목록에 없는 값).
- 평문 http 요청·CONNECT 내부 메서드 누락은 `protocol_unsupported`다(승인 대상은 https만).

## 남은 일

1. 대상 등록 저장소 읽기 쪽을 패키지로 분리하고 `TargetSource`로 잇기(보호 저널 읽기 전용, 이경준 C34·C35와 조율).
2. `cmd` 경계 서버: 브라우저·직접 입구 리슨(서로 다른 Docker 망), 경계 CA로 TLS 가로채기, `Decision.Dial`·`VerifySPKI`로만 연결, 응답 크기 상한, 리다이렉트 미추종(새 요청은 다시 판정), 줄 단위 JSON 유닉스 제어 소켓 `open`·`close`·`status`, 기록 보호 저장.
3. 제품 compose의 `explore-boundary` 서비스(기본 꺼짐)와 host adapter, 실제 Docker 양성·음성 수용시험(B01~B18).
4. B16(브라우저·MCP 컨테이너 분리)은 별도 작업이다.
