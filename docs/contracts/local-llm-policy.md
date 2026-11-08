# 완전 로컬 LLM 허용 정책 코어

범위: `llmpolicy.New`·`Policy.Decide`의 순수 판별 계약. 실제 HTTP 요청·dial 검사와 명시적 `Config.NewLocalProvider` 연결은 [로컬 LLM HTTP 어댑터](./local-llm-http.md)에서 관리한다. 서버·profile 자동 배선은 PR #71 병합 이후 별도 작업이다. 이 순수 코어 단독으로 실제 외부 송신 차단이 완료됐다고 설명하지 않는다.

## 입력과 허용 기준

`New([]Endpoint)`에 운영자가 검증한 origin과 그 origin별 고정 IP를 제공한다. `Endpoint`는 `Origin`과 `Addresses`를 가지며, 정책은 입력 사본을 고정해 동시 호출에 재사용한다. CIDR·와일드카드·프록시·외부 모드 전환은 지원하지 않는다.

`Policy.Decide(Input)`은 다음 조건을 모두 충족할 때만 허용한다.

1. 유효한 정책과 명시적인 `Direct` 전송 경로가 있다. unknown/Proxy/알 수 없는 transport는 차단한다.
2. URL의 scheme·host·유효 port가 등록 origin과 정확히 일치한다. 경로·query는 origin을 확장하지 않는다.
3. 비어 있지 않은 현재 해석 결과 `ResolvedIPs`의 모든 주소가 로컬 범위이며 해당 origin의 고정 IP 목록에 있다. 공인 주소 하나라도 섞이면 차단한다. literal IP URL도 그 주소를 해석 결과에 명시한다.
4. 실제 연결에 선택한 `ConnectIP`도 로컬·등록 IP이며 같은 해석 결과에 들어 있다. 다른 origin에서 승인된 IP를 가져다 쓰지 않는다.

`localhost`·`*.local` 같은 이름 자체는 허용 근거가 아니다. 반면 `ollama.local → 등록된 127.0.0.1`처럼 이름·핀·현재 주소가 모두 일치하면 허용한다. 해석 오류·결과 누락은 빈 결과로 차단하며 legacy·직접 연결 허용으로 추정하지 않는다.

## 로컬 주소 범위

| 주소 범위 | 판정 |
|---|---|
| `127.0.0.0/8`, `::1/128` | loopback, 등록 IP인 경우만 허용 |
| `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` | 사설 IPv4, 등록 IP인 경우만 허용 |
| `fc00::/7` | IPv6 ULA, 등록 IP인 경우만 허용 |
| 그 외 모든 범위 | 차단 |

따라서 unspecified·multicast·IPv4/IPv6 link-local·CGN `100.64/10`·NAT64·문서용 주소·공인 IP는 등록을 시도해도 거부한다. zone이 붙은 주소는 범위와 무관하게 거부한다. 주소 검증·핀·연결 주소 비교에는 IPv4-mapped IPv6의 `Unmap` 결과를 사용한다.

## 정규화와 형식

- HTTP/HTTPS만 지원한다. scheme·DNS host는 소문자로 비교하고 생략된 port는 각각 80/443이다. 숫자 port는 1..65535, leading zero·빈 명시 port는 거부한다.
- 설정 origin은 path가 없거나 `/`인 경우만 받으며 query·fragment·userinfo는 거부한다. 후보 URL에는 path·query를 허용하지만 fragment·userinfo·opaque URL은 거부한다.
- DNS 이름은 ASCII 영숫자·하이픈 label을 지원한다. label은 1..63바이트, 전체는 최대 253바이트이며 label 앞뒤 하이픈·빈 label·후행 dot을 거부한다. `xn--` 이름은 명시한 ASCII 이름으로 비교하고 IDNA 변환은 하지 않는다.
- 정수·8진·16진·축약 표기의 대체 IPv4 주소는 DNS 이름으로도 받지 않는다. IPv6 URL에는 bracket이 필요하고 zone은 거부한다.
- raw URL의 공백·control·backslash, 디코딩된 path/query의 control·backslash, 잘못된 escape·UTF-8은 거부한다. escape된 일반 공백은 허용한다.
- origin 비교에서는 IPv4 URL과 IPv4-mapped IPv6 URL을 구분한다. mapped literal을 별도로 등록하면 정규화된 동일 주소의 핀으로 사용할 수 있다. literal origin에는 그 주소의 핀 하나만 등록한다.
- canonical origin 중복과 같은 origin의 핀 목록 내부에서 정규화된 IP 중복은 설정 오류다. 서로 다른 등록 origin은 같은 로컬 IP를 사용할 수 있다. 해석 결과의 중복은 허용하지만 모든 항목을 검사한다. 설정 slice를 바꿔도 기존 정책은 바뀌지 않는다.
- 구조 상한은 URL 8 KiB, origin 64개, origin당 핀 64개, 한 입력의 해석 주소 64개다. 초과 항목을 잘라 허용하지 않는다.

## 결과와 실패

nil/zero 정책과 zero `Decision`은 차단이다. `Decision.Allowed()`와 `Reason()`만 공개하고 원문 URL·IP·parser 오류를 결과에 넣지 않는다. 설정 실패는 원문 없는 `ErrInvalidPolicy`이며 부분 정책을 반환하지 않는다.

| Reason | 의미 |
|---|---|
| `policy_missing`, `unknown` | 정책/판정 초기값 |
| `transport_unknown`, `proxy_denied`, `transport_invalid` | 직접 전송 경로가 확인되지 않음 |
| `url_invalid`, `origin_denied` | URL 오류 또는 미등록 origin |
| `resolution_missing`, `address_invalid` | 해석 누락 또는 invalid/zone 주소 |
| `address_not_local`, `address_not_pinned` | 허용 범위 밖 주소 또는 다른/미등록 핀 |
| `connect_ip_invalid`, `connect_ip_mismatch` | 연결 주소 누락 또는 현재 해석 결과와 불일치 |
| `limit_exceeded` | 입력 구조 상한 초과 |
| `local_endpoint_allowed` | 위 조건을 모두 충족 |

해석 결과에 여러 문제가 있으면 invalid → nonlocal → unpinned 순서로 사유를 정한다. 주소 순서가 바뀌어도 허용 여부와 실패 사유는 바뀌지 않는다.

## 이후 provider adapter의 책임

정책·해석 결과·선택 IP는 서버가 검증해 공급하며 LLM이 작성하지 않는다. 코어는 DNS 조회·socket·환경변수·시간·로그·파일·DB를 사용하지 않는다. 전달된 해석 결과가 완전하고 최신인지 인증하는 기능도 아니다.

adapter는 최초 요청뿐 아니라 매 retry·redirect·실제 dial의 최종 URL과 주소를 다시 검사해야 한다. 명시적으로 `Transport.Proxy=nil`인 직접 전송기를 소유하고, 승인 IP에 연결하면서 원래 Host/TLS hostname·인증서 검증을 유지해야 한다. 원문 판정 뒤 다른 URL/IP에 연결하거나, 예전 `Allowed` 결과만 캐시해 재사용하면 보호되지 않는다.

이 판단은 네트워크 endpoint 허용이며 모델 응답의 안전성·DLP·실제 로컬 추론·내부 서버가 외부로 relay하지 않음의 증명이 아니다. 현재 앱에는 아직 집행되지 않는다.
