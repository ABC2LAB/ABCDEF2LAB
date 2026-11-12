# 탐색 경계 구현 (P1-A)

[탐색 경계 계약](../contracts/exploration-boundary.md)(제안) §2~§6·§9를 구현한다. 1단계(#482)는 요청별 판정 코어, 2단계는 프록시·제어 소켓·경계 바이너리다. **제품 compose·host adapter·실제 Docker 수용시험은 아직 없다.** 운영 host adapter 기본값 nil은 그대로이고, 이 문서로 탐색 경계가 운영 수용됐다고 주장하지 않는다.

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

## 2단계: 프록시·제어 소켓·바이너리

- `Proxy`: CONNECT만 받아 경계 CA로 터널마다 leaf를 만들어 TLS를 가로챈다(이름 수가 묶이지 않아 캐시하지 않음). 터널 authority와 안쪽 Host가 다르면 막는다. 안쪽 요청마다 `Admit`을 받고 `Decision.Dial` 주소로만 연결하며 승인 대상은 SPKI 고정값, 외부는 루트로 검증한다. 리다이렉트를 따라가지 않는다(Location의 새 요청은 다시 판정). 응답이 상한을 넘으면 전달하지 않는다. 평문 프록시 요청·WebSocket(전달 구현 전)·요청 본문 상한 초과는 `Refuse`로 차단 행을 남긴다. 터널 수 상한, 세션 마감 시 터널 전부 끊기.
- 제어 소켓(§9-4): 줄 단위 JSON `open`·`status`·`close`와 private `sequences`·`observation`·`records`. 모르는 칸·연산은 거부하고 오류는 고정 어휘만 돌려준다. 리눅스에서 `SO_PEERCRED` uid가 엔진 서버 uid일 때만 받는다. 세션은 한 번에 하나다. private 관찰은 공개 JSON에서 빠지는 응답 형식·해시를 이 채널로만 넘긴다(`diagnosisflow`가 정적 fetch 본문을 이 해시로 검증한다).
- 기록·제어 소켓 보강(#488 리뷰, 이어받은 PR): 탐색 쪽이 고른 값(메서드·`Sec-Fetch-*`·Host·경로 표시 값)이 기록 한 행을 키워 기록 전체를 못 읽게 만들 수 있어서, 판정 전에 메서드는 짧은 토큰, `Sec-Fetch-*`는 Fetch 표준 어휘(밖이면 `invalid`)로 맞추고, 기록 직전에 경로 표시 값 1024바이트·외부 호스트 253바이트로 묶는다(한 행 최대 2KiB). 행 수는 묶지 않는다. 탐색 요청 수를 줄이지 않도록 `records`는 쪽으로 읽는다(요청 `offset`, 응답 `next`, 0이면 끝, 한 쪽은 제어 소켓 한 줄에 담기는 행 수). 제어 JSON은 객체마다 중복 키·대소문자 다른 키·뒤에 붙은 값을 거부한다(중첩 객체 포함). 안쪽 Host 불일치·잘못된 CONNECT authority·터널 상한 거부도 기록한다. 대상이 경계 결과 헤더를 보내면 지우고, HEAD 응답의 `Content-Length`는 그대로 두고, 포트 없는 IPv6 Host는 괄호를 벗겨 비교한다. CONNECT와 같이 온 ClientHello는 hijack 버퍼부터 읽고, 안쪽 서버는 본문까지 읽기 시간 상한을 둔다.
- 엔진 쪽 `ControlBoundary`·`controlLease`·`ControlReader`가 `exploration.Boundary`·`Lease`와 private Reader·Sequences를 구현한다. host adapter(P1-D)가 이것을 조립한다.
- `cmd/explore-boundary`: 보호 설정 파일 하나(`privateinput`)로 시작한다. 두 입구를 서로 다른 인터페이스 주소에 묶어야 하고(같은 주소·loopback·0.0.0.0 거부, IPv4-mapped 표기는 IPv4로 풀어서 본다), `engine_uid`가 없거나 0이면 거부한다. 제어 소켓은 0600이고 엔진 uid가 경계와 다르면 엔진 uid 소유로 넘긴다. 넘길 수 없으면(CAP_CHOWN 없음) 권한을 넓히지 않고 시작을 멈춘다(`control_socket_owner`). 입구 서버는 쉬는 keep-alive 연결을 시간 상한으로 닫는다. 경계 CA 개인키·경로 가림 키는 보호 파일에서만 읽는다. 경계 기록 경로는 설치 키로 가린다(계약 §4의 제품 "표시용 패턴" 대신 키 가림을 쓴다. 원문 조각이 남지 않는 대신 운영자 화면에서 읽기 어렵다).

## 대상 값 전달 — 서버가 제어 소켓으로 넘긴다(2026-11-12 결정)

대상 등록 저널은 쓰는 프로세스가 디렉터리에 배타 `flock`을 계속 잡아 별도 경계 컨테이너가 직접 열 수 없다. 그래서 `open` 요청에 서버가 보호 저장소에서 같은 ref·revision으로 읽은 승인 스냅샷(`WireTarget`)을 싣는다. 이 소켓은 엔진 서버 uid만 닿고 실행기는 닿지 못하므로 "실행기는 참조만 넘기고 값을 넘기지 않는다"는 그대로다. 철회는 서버의 기존 대상 감시가 `close(target_ended)`로 알리고, 만료는 경계가 스냅샷의 만료 시각으로 스스로 감지한다. 계약 §2의 "경계가 대상 등록 저장소에서 직접 읽는다"는 이렇게 바꿔야 한다(계약 개정 필요).

## 계약 제안과 다르게 정한 것(리뷰 필요)

- 외부 자원 상한·동시 연결 상한은 세션을 닫지 않는다. §3-3은 "상한에 닿으면 세션 마감"이지만 §6의 마감 신호는 시간·대상 요청 상한이고, external_allowed에서 외부 상한 0이면 첫 CDN 요청에 탐색 전체가 끝나기 때문이다.
- 이름 풀이 실패는 `unresolved_host`로 남긴다(§4 목록에 없는 값).
- 판정 중에 요청 문맥이 끝나면(브라우저의 XHR 중단 등) 그 요청만 `request_cancelled`로 남기고 세션은 닫지 않는다(§4 목록에 없는 값). 대상 조회가 문맥 취소로 멈춘 것을 저장소 장애로 보고 `boundary_unavailable`로 닫으면, 요청 하나가 탐색 전체를 끝낸다(#482 리뷰 R4).
- 제외 경로의 조각 하나 전체인 `*`는 제품 대상 등록·진단 흐름(`diagnosisflow` `excludedWithin`)과 같은 규칙으로 맞춘다(아무 조각 하나, 그 아래 경로 포함). 등록 검사가 `*`를 받으므로 글자 그대로 비교하면 제외가 빠진다(#482 리뷰 R1).
- 외부 요청의 이름(또는 IP 리터럴)이 승인 대상의 `ConnectIP`로 풀리면 외부 자원으로 보내지 않고 `out_of_scope_origin`으로 막는다. 외부로 보내면 승인 경로·위험 경로·제외 경로·SPKI 고정과 대상 요청 수를 모두 건너뛴다(#482 리뷰 R3).
- 평문 http 요청·CONNECT 내부 메서드 누락은 `protocol_unsupported`다(승인 대상은 https만).

## 남은 일

1. host adapter(P1-D): 서버 조립에서 대상 등록 기록을 `Target`으로 바꾸는 함수(선택 IP·SPKI·접두·메서드·위험 단어·제외 경로), 서버의 대상 감시와 `close(target_ended)` 연결, `ControlBoundary`·`ControlReader`로 `ExplorationPipelineHost` 조립. 기본 nil 유지.
2. 제품 compose의 `explore-boundary` 서비스(기본 꺼짐), 망 세 개(브라우저 입구·직접 입구·제어 소켓 볼륨), 실제 Docker 양성·음성 수용시험(B01~B18). B16(브라우저·MCP 분리)과 같은 파일을 건드리므로 그 결과 뒤에 한다.
3. WebSocket 전달(approved_writes 회차), 탐색 단계 Bash relay(P1-C).
