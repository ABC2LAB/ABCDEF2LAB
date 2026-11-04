# E② 도구 네트워크 경계(B)

이 디렉터리는 아직 병합되지 않은 [`e2-network-enforcement.md`](https://github.com/ABC2LAB/ABCDEF2LAB/blob/a5d828a9c1860bee566d38aa932d7149642712fb/docs/contracts/e2-network-enforcement.md)(`a5d828a9c1860bee566d38aa932d7149642712fb`)에 따른 B의 배포 경계 초안입니다. 실행 가능한 완성 서비스나 운영 승인 자료가 아닙니다. 현재 `agent.Worker.Execute`와 신뢰된 전송부는 같은 서버 프로세스에서 실행됩니다. C가 도구를 별도 프로세스로 옮겨야 이 구성을 사용할 수 있습니다. A #184에는 이미 수락된 Linux `SOCK_SEQPACKET` 연결을 처리하는 `ServePacketConn`이 추가됐지만 listener, 소켓 ACL, 실제 host 신원 검증자는 C가 제공해야 합니다.

## 출구 목록

| 프로세스 | 경로·시도 | 집행 방식 |
| --- | --- | --- |
| 도구와 모든 자식 프로세스 | 신뢰된 A 서비스로 향하는 `/run/e2-channel` Unix 소켓 | 실행별로 보호된 외부 Docker 볼륨을 읽기 전용으로 마운트하고, Docker `network_mode: none`으로 모든 IP 네트워크를 끔 |
| 신뢰된 전송부 | 운영자가 만든 내부 대상 네트워크의 피어. 각 전송은 A가 등록된 IP·포트로 제한해야 함 | B가 네트워크 구성원과 호스트 공개 포트 부재를 검사하고, A가 요청을 집행함 |
| 도구 | 차단: 직접 IPv4/IPv6, DNS/UDP, 프록시 변경, CONNECT 및 임의 Unix 소켓 경로 | 도구에 IP 네트워크나 다른 소켓 마운트를 제공하지 않으며, B의 실행 상태 검사기가 구성 변경을 거부함 |
| 두 프로세스 모두 | 차단: 호스트 Docker 소켓, 호스트 네트워크, 추가 마운트 | 이 구성에서 제공하지 않으며, B의 실행 상태 검사기가 발견하면 거부함 |

전송부의 등록 목적지와 프로토콜 제한은 A가 담당합니다. Docker 내부망만으로는 전송부가 같은 네트워크의 다른 피어에 접근하는 것을 막지 못합니다. C는 승인된 대상만 연결하고 전송부와 대상, 두 컨테이너만 구성원인지 확인해야 합니다. 네트워크는 연결 가능한 상태(`Attachable`)가 아닌 로컬 내부 Docker 브리지로 만들고, IPv4와 IPv6 모두 `com.docker.network.bridge.gateway_mode_ipv4=isolated`, `com.docker.network.bridge.gateway_mode_ipv6=isolated`를 지정해야 합니다. Docker의 일반 `--internal` 브리지는 호스트 브리지 주소가 생겨 해당 주소의 호스트 서비스에 접근할 수 있습니다. 두 `isolated` 모드는 이 주소를 없애며 B는 모드가 빠진 네트워크를 거부합니다([Docker 문서](https://docs.docker.com/engine/network/port-publishing/#gateway-modes)). 대상이 이 네트워크에 들어올 수 없다면 호스트가 소유한 별도 출구 설계를 검토해야 합니다. 일반 브리지로 바꾸어 사용하지 마세요. 호스트 공개 포트는 없습니다.

`E2_CHANNEL_VOLUME`은 C가 실행마다 새로 만드는 외부 볼륨이어야 하며, 그 안에는 인증된 A 소켓만 있어야 합니다. C는 프로세스를 시작하기 전에 볼륨의 소유권, 권한, 프로토콜, 실행 식별자, 수명주기를 검증해야 합니다. 이 초안은 UID, 키, epoch, 승인, 예산을 발급하지 않습니다. Compose 프로젝트, 두 컨테이너의 `org.abc2lab.e2.run` 레이블, 외부 내부망의 같은 레이블은 C가 이미 검증한 비밀이 아닌 실행 참조값에서 받아야 합니다. 레이블 일치는 일관성 검사일 뿐 권한이나 C의 보호된 실행 결속을 대신하지 않습니다. 도구는 채널 디렉터리를 읽기 전용으로 받습니다. 프록시 환경변수를 비우는 것은 추가 방어입니다. 도구가 값을 바꾸더라도 IP 경로는 없습니다.

## 실행 상태 검사와 남은 C 작업

`verify_runtime.py`는 C가 프로세스를 시작한 뒤 **실제** Docker 객체를 검사합니다. 도구의 네트워크 모드·마운트·권한, 전송부의 네트워크·포트, 대상 네트워크의 `Internal` 값과 양쪽 `isolated` 게이트웨이 모드, 정확한 두 구성원, 채널 볼륨 레이블을 확인합니다. 이 검사 자체는 권한을 부여하지 않습니다. 검사기는 볼륨이 바인드 기반 `Options`가 없는 일반 로컬 볼륨인지도 확인합니다. C는 볼륨 내부를 검사하고 Unix 소켓 ACL을 집행해야 합니다. B의 메타데이터 검사만으로는 소켓의 소유자나 프로토콜을 증명할 수 없습니다.

C는 계약 순서에 따라 `BlockNewFlows(run)`과 `TerminateAndWait(run)`을 제공하고, cgroup·프로세스·상속된 디스크립터의 종료 증거를 확보해야 합니다. 이어서 A의 신규 요청 차단·기존 요청 배출을 조율하고 감사 마감 뒤 권한을 해제해야 합니다. 컨테이너나 A만 중지한 사실로는 이 순서를 증명할 수 없습니다.

`test_verify_runtime.py`는 합성 inspect 데이터로 검사기를 시험합니다. `test_compose_config.py`는 합성 값으로 Compose 구성을 렌더링합니다. `test_compose_docker.py`는 폐기용 유휴 프로세스 두 개로 실제 Compose 구성을 시작해 Docker 상태를 검사한 뒤 해당 프로젝트를 제거합니다. `test_boundary_docker.py`는 긍정 대조군과 대상 수신 건수를 사용해 직접 IPv4/IPv6 TCP·UDP, DNS, 프록시 변경, 불투명 CONNECT, `/bin/sh` 자식, Python 손자 프로세스를 검사합니다. `test_channel_docker.py`는 A의 ready nonce·요청·제한 ACK와 같은 패킷 형식을 사용하는 **합성 전송부** 및 `probe_packet_channel.py`로 HTTP 대상 수신 1건, 스트림 소켓 대체 시도 차단, 커널 자격 관측을 확인합니다. 실제 A 서비스나 C의 `PeerAuthorizer`는 호출하지 않습니다. 최종 A/C 시험과 필요한 연결 입력은 [`ACCEPTANCE.md`](./ACCEPTANCE.md)에 적었습니다.

현재 Docker Desktop의 별도 기본 PID namespace에서 합성 전송부가 관측한 도구 패킷의 `SCM_CREDENTIALS`에는 UID·GID가 예상값으로 들어왔지만 PID는 0이었습니다. A의 `auditpeer` 수신기는 PID가 0 이하인 패킷을 거부하므로, 현 Compose 구성과 A 진입점의 실제 연결은 **차단**됩니다. C와 A는 격리를 유지하면서 전송부에 유효한 커널 PID가 보이도록 프로세스·namespace 배치를 합의하고, 살아 있는 프로세스 handle·실행 결속·철회를 확인하는 host `PeerAuthorizer`를 제공해야 합니다. 이 문서는 PID namespace 변경이나 무검사 허가를 자동 적용하지 않습니다.

`probe_network.py`는 격리된 도구 프로세스 안에서 **폐기 가능한 합성 대상**만 시험하는 제한된 프로브입니다. 페이로드나 비밀을 수집하지 않고 시도와 오류만 보고합니다. 등록된 GET의 성공, TLS, 철회, 예산, 감사에 관한 수용시험은 C와 통합한 **실제 A 서비스**를 대상으로 실행해야 합니다. 시험용 전달 결과를 NE01, NE02, NE08, NE09의 수용시험 통과로 취급하지 마세요.

기존 `deploy/engine-isolated/`, A의 `netboundary/`, C의 기동 파일은 수정하지 않았습니다. `docker compose -f deploy/e2-network-boundary/compose.yaml`에는 C가 제공하는 이미지, 보호된 채널, 내부 대상 네트워크가 필요합니다. 실행마다 고유 프로젝트를 사용하고 검증 후 해당 프로젝트의 폐기용 자원만 제거하세요.
