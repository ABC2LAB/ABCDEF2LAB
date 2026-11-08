# 로컬 LLM HTTP 어댑터

상태: 구현 초안. `llmpolicy.NewClient`와 `agent.Config.NewLocalProvider`를 명시적으로 호출하는 경로만 해당한다. 서버·profile 설정 배선은 PR #71 병합 이후 별도 작업이다. 기존 `Config.NewProvider` 호출이나 다른 클라이언트를 자동으로 통제하지 않는다.

## 입력과 소유권

호스트가 고정한 `Policy`(origin별 정확한 로컬 IP pin)와 `ClientOptions`를 받는다. 옵션은 `LookupIP`, `RootCAs`, 전체 `Timeout`뿐이다. proxy·TLS 검증 생략·SNI·dial 함수 override를 제공하지 않는다. CA pool은 생성할 때 복제하고 nil은 시스템 신뢰 저장소를 사용한다. policy 누락/zero와 음수 timeout은 고정 오류로 거부한다. timeout 0은 기존 스트리밍처럼 전체 제한을 두지 않으며 호출자 context 취소와 dial/TLS 제한은 유지한다.

`LookupIP`는 호스트가 제공하는 신뢰된 resolver다. nil이면 OS resolver로 전체 IP 결과를 조회하고, literal IP는 singleton으로 처리한다. LLM·대상 응답이 resolver나 pin을 정하지 않는다. DNS 자체의 통신 경로와 대상 모델 서비스가 다른 서비스로 relay하는지는 이 HTTP 어댑터가 증명하지 않는다.

`NewClient`가 반환하는 표준 `*http.Client`는 호스트 소유다. `Transport`·`CheckRedirect`를 바꾸거나 다른 클라이언트를 사용하는 호스트 코드까지 막는 격리 장치는 아니다. `NewLocalProvider`는 이 클라이언트를 내부에 보관하고 provider만 반환한다. 제품 코드에서 이 연결을 유지해야 한다.

## 실제 전송

1. `RoundTrip`에서 최종 URL을 core와 같은 규칙으로 검사하고 등록 origin인지 확인한다. 미등록/잘못된 URL은 DNS도 조회하지 않는다.
2. `Request.Host`가 있으면 scheme·host·effective port가 URL과 같아야 한다. 대소문자·기본 포트·IPv6는 core 정규화를 따른다. `Header`의 별도 `Host` 키는 중복·위조 경로를 줄이기 위해 거부한다.
3. 전체 현재 해석 결과와 첫 번째 선택 IP를 core `Decide`에 전달한다. 부분 성공·빈 결과·오류·공인/혼합·등록 밖 IP·64개 초과는 차단한다.
4. 요청 전용 private context에 원 URL·origin·host/port·해석 결과·선택 IP·원래 취소 context를 결속한다.
5. 실제 `DialContext`에서 요청 plan과 network/authority를 대조하고 전체 DNS를 다시 조회한다. 기존 선택 IP가 그 새 집합에서도 승인될 때만 숫자 IP로 연결한다. 새 결과에 다른 승인 IP만 남아도 이번 attempt에서 바꿔 연결하지 않는다. 다음 명시적 재시도에서 새 plan을 만들 수 있다.
6. TCP peer IP·port도 선택 값과 일치해야 한다. 원 URL/Host는 IP로 덮어쓰지 않으므로 TLS는 원래 hostname/SNI·인증서를 정상 검증한다.

전송기는 전역 `http.DefaultTransport`를 복제하지 않고 새로 소유한다. `Proxy=nil`이며 환경 proxy도 사용하지 않는다. HTTP/1.1·새 연결만 지원해 connection pool이나 HTTP/2 coalescing이 dial 검사를 생략하지 않게 한다. 선택 IP는 하나이며 다른 IP로 암묵적 failover하지 않는다. TCP dial 30초·TLS handshake 10초를 적용한다.

redirect는 동일 canonical origin 안에서만 최대 10개 요청(최초 요청 포함)을 허용한다. 별도 등록 origin이어도 cross-origin/port/scheme 변경은 거부해 자격증명을 자동 전달하지 않는다. 매 hop은 URL 검사 후 다시 `RoundTrip`/dial 검사를 거친다. SDK retry 역시 새로운 요청이므로 새 DNS 검사를 통과해야 한다. 이미 전송한 요청이나 TLS handshake를 되돌린다는 뜻은 아니다.

## Provider와 오류

`NewLocalProvider(policy, options)`는 설정의 proxy와 빈 BaseURL을 고정 오류로 거부한다. 구성된 client의 바깥에 기존 `quotaAwareTransport`를 붙여 session header·wire Capture·429 quota 변환을 유지한다. 정상 response·stream delta·usage는 바꾸지 않는다. 기존 `NewProvider`는 원래 HTTP client와 provider 설정을 사용하는 경로로 유지한다.

transport의 URL/parser/resolver/네트워크 원문 오류는 고정 sentinel로 바꾸며 context 취소/기한 오류는 표준 값으로 유지한다. 다만 표준 `http.Client.Do`는 `url.Error.URL`에 원 URL을 담을 수 있으므로 public client의 반환 오류를 그대로 공유 로그에 쓰지 않는다.

명시적 local provider는 Stream·Complete 및 factory 실패를 고정 오류로 투영해 URL·응답·기저 cause를 반환하지 않는다. 정책/transport 차단을 quota보다 먼저 분류하고, quota는 고정 오류로 유지한다. 실패한 Complete의 partial 결과도 반환하지 않는다. 이 경계는 정상 모델 응답의 개인정보 마스킹이나 LLM 기록 DLP를 대신하지 않는다.

## 검증

합성 loopback 서버 수신 건수와 실제 SDK wire/capture를 비교한다. 정상 로컬 요청은 수신되며 proxy·미등록 origin·공인/혼합/누락/오류 DNS·Host 위조는 dial/수신 0이어야 한다. 허용 뒤 DNS가 바뀌는 경우, 같은 hostname의 승인 IP 변경, redirect/SDK retry, 취소·동시 요청도 검사한다.

TLS는 신뢰된 인증서의 실제 HTTP/1 수신·원 Host·SNI·응답 body를 긍정 대조로 삼는다. wrong DNS SAN·미신뢰 CA·만료 인증서는 ClientHello가 도착해도 HTTP handler 수신 0이고 transport 오류여야 한다. 원 CA pool을 나중에 변경해도 기존 trust snapshot은 바뀌지 않아야 한다.

negative IP 시험은 별도 dial 안전 가드로 비루프백 connect를 차단하므로 시험 실패가 공인 IP로 실제 패킷을 보내지 않게 한다. 모델 API·실제 대상 스캔은 호출하지 않는다. 서버/profile 자동 연결·설정 저장·다른 SDK 통신 경로는 이 구현 완료 범위가 아니다.
