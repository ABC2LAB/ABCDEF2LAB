# B와 A/C 통합 수용시험 인계

기준점: `dev` `f21d73fe89315ae69bd70051fb2d043d363ea1d0`, 미병합 계약 [#183](https://github.com/ABC2LAB/ABCDEF2LAB/pull/183) head `a5d828a9c1860bee566d38aa932d7149642712fb`, 미병합 A [#184](https://github.com/ABC2LAB/ABCDEF2LAB/pull/184) head `66dfebc6c4a337fac52b239f493e26302625dc7a`. 통합 전에 세 SHA를 다시 확인해야 합니다. #184는 Go의 `Open`, `SendRegisteredRead`, `CloseAdmission`, `StopAndWait` 및 수락된 Linux `SOCK_SEQPACKET` 연결용 `ServePacketConn`을 제공하지만 인증된 Unix 소켓 listener·ACL·host `PeerAuthorizer`는 제공하지 않습니다.

B 단독 시험은 폐기 가능한 합성 컨테이너를 사용합니다. 그 결과는 실제 A+B+C 수용시험과 분리해서 기록해야 합니다. 여기서는 실제 계정, UID, 승인, 예산, 감사 키·epoch, 모델 또는 대상을 발급하거나 호출하지 않았습니다.

| 항목 | 현재 준비·실행 상태 | 실제 A/C로 확보할 최종 증거 |
| --- | --- | --- |
| NE01 HTTP | A #184의 자체 합성 패킷 단위 시험은 HTTP 대상 수신 1건으로 PASS. 별도의 B 패킷 형식 **합성 전송부**도 고정 GET 1건을 전달해 수신 1건, 스트림 소켓 대체 시도의 추가 수신 0건을 관측함. 실제 A+B 연결은 미실행. | C의 보호된 소켓이 실제 A `ServePacketConn`을 거쳐 `SendRegisteredRead`를 호출해야 함. 정확히 등록된 HTTP GET 1건의 수신과 결과 ACK를 관측하고, 프록시 환경변수를 바꿔도 경로가 달라지지 않아야 함. |
| NE02 HTTPS | B와 실제 A의 연결 시험은 미실행. A #184의 자체 합성 HTTPS 단위 시험과 CI는 PASS이나 B 격리 증거는 아님. | 실제 A가 폐기용 합성 HTTPS 대상, 시험 CA, 고정 IP, 올바른 이름을 사용해야 함. 정확한 GET 1건을 수신하고 A가 검증된 응답을 보고해야 함. TLS 검증 완화 옵션은 사용하지 않음. |
| NE06 직접 출구 | 폐기용 이중 스택 대상의 긍정 대조군과 `network none` 프로브로 직접 경로 9개(IPv4/IPv6 TCP·UDP, DNS, 프록시 변경, CONNECT, `/bin/sh` 자식, Python 손자)를 시험함. HTTP4/6·UDP4/6·CONNECT의 관측된 신규 수신은 모두 0건. | **C의 실제 도구 이미지와 Compose 프로세스** 안에서 의도된 Unix 소켓, 임의 localhost·Unix 소켓 경로, 상속된 디스크립터, 각 거부 직후의 대체 경로 시도를 포함해 다시 시험해야 함. 대상 수신 건수를 독립적으로 집계해야 함. |
| NE07 다른 도구 | 미실행. | C의 실제 도구 프로세스에서 Bash, WebFetch, Search, MCP, 브라우저와 자식을 시작해야 함. 각각 비활성·부재를 확인하거나 합성 대상을 시도해 수신 0건을 보여야 함. 환경변수 복사만으로 일반 도구가 A의 보호된 소켓 권한을 얻어서는 안 됨. |
| NE08 TLS·CONNECT | 합성 응답기로 향한 직접 CONNECT 시도는 차단됐고 신규 수신은 0건. 실제 A의 TLS·MITM 경로는 미실행. | 실제 A 서비스에서 이름 불일치, 만료, 신뢰되지 않은 인증서, 핸드셰이크 실패 각각의 대상 애플리케이션 GET 수신이 0건이며 직접 대체 경로도 없어야 함. 실제 도구 프로세스의 CONNECT·MITM 통과 시도는 트래픽 캡처 설정과 무관하게 전달 0건이어야 함. |
| NE04/05/09 정지·철회·예산·감사 | 미실행. | C가 제공하는 합성 현재 권한, 공유 원장, 감사가 연결된 A 전송부를 사용해야 함. 새로운 조회·직접 흐름·자식 생성과 철회·만료·예산 소진, A `CloseAdmission/StopAndWait`, B `BlockNewFlows/TerminateAndWait`, 감사 마감·키 해제를 경쟁시켜야 함. 신규 허용, 예약, HTTP 수신, 부분·불명 전송, 감사 ACK, 남은 프로세스·소켓을 따로 보고해야 함. A의 배출만으로는 충분하지 않으며, 배출 상태가 불명이면 격리를 유지하고 성공 Seal을 금지해야 함. |
| NE10 비밀 처리 | 미실행. | 오류·응답에 합성 표식 문자열을 넣고, 모델·수신증·감사·공유 로그의 범위가 제한된 출력에서 정확한 표식 출현 횟수를 세되 출력 자체는 게시하지 않아야 함. 기대값은 0건이며 감사 실패 뒤 성공 상태로 올리면 안 됨. |

현재 Docker Desktop의 B 기본 합성 패킷 시험은 `SCM_CREDENTIALS`의 UID·GID 일치를 관측했지만 PID는 0이었습니다. A의 `auditpeer`는 PID가 0 이하이면 패킷을 거부합니다. 폐기용 후보 시험에서 정확한 전송부 컨테이너 ID의 사설 PID namespace를 공유하자 양의 패킷 PID와 서로 다른 비root UID·GID, 같은 PID namespace·다른 네트워크 namespace를 관측했습니다. signal·ptrace 및 `/proc`의 `environ`·`mem`·`fd`는 차단됐고 전송부 종료 뒤 합성 도구도 중지됐습니다. 그러나 `/proc/.../cmdline`은 읽을 수 있어 후보는 **차단**입니다. B의 제품 Compose는 기존의 별도 PID namespace를 유지하고 검사기는 임의 참여·동일 UID·GID를 거부합니다. C/A는 `/proc` 노출을 해결하고 살아 있는 프로세스·실행 결속·철회를 요청마다 확인하는 `PeerAuthorizer`를 합의·제공해야 합니다. 현재 구성이나 후보 구성으로 A/B 패킷 양성 시험을 통과했다고 기록할 수 없습니다.

최종 시험 전에 C는 한 실행의 실제 호스트 소유 프로세스 식별자, 보호된 채널 볼륨과 Unix 소켓 ACL, 정확한 대상 컨테이너·네트워크, 등록 자원 매핑, 권한·원장·감사 어댑터, 프로세스·cgroup 종료 증거를 제공하고 검증해야 합니다. 현재 Worker는 서버 프로세스에서 실행되므로 이 Compose 초안만으로 일반 도구 경계를 활성화할 수 없습니다. 실제 입력이 없는 항목은 **차단 또는 미실행**으로 기록해야 하며 합성 결과로 통과 처리해서는 안 됩니다.
