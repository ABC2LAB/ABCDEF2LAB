# E② 도구 네트워크 경계(B)

이 디렉터리는 [`e2-network-enforcement.md`](https://github.com/ABC2LAB/ABCDEF2LAB/blob/745b821167a8b1b0f1843fa3d8e3ed79e8f8409f/docs/contracts/e2-network-enforcement.md)(`745b821167a8b1b0f1843fa3d8e3ed79e8f8409f`)에 따른 B의 배포 경계 초안입니다. 실행 가능한 완성 서비스나 운영 승인 자료가 아닙니다. 현재 `agent.Worker.Execute`와 신뢰된 전송부는 같은 서버 프로세스에서 실행됩니다. C가 도구를 별도 프로세스로 옮겨야 이 구성을 사용할 수 있습니다. A #184에는 이미 수락된 Linux `SOCK_SEQPACKET` 연결을 처리하는 `ServePacketConn`이 추가됐지만 listener, 소켓 ACL, 실제 host 신원 검증자는 C가 제공해야 합니다.

## 출구 목록

| 프로세스 | 경로·시도 | 집행 방식 |
| --- | --- | --- |
| 도구와 모든 자식 프로세스 | 신뢰된 A 서비스로 향하는 `/run/e2-channel` Unix 소켓 | 실행별로 보호된 외부 Docker 볼륨을 읽기 전용으로 마운트하고, Docker `network_mode: none`으로 모든 IP 네트워크를 끔 |
| 신뢰된 전송부 | 운영자가 만든 내부 대상 네트워크의 피어. 각 전송은 A가 등록된 IP·포트로 제한해야 함 | B가 네트워크 구성원과 호스트 공개 포트 부재를 검사하고, A가 요청을 집행함 |
| 도구 | 차단: 직접 IPv4/IPv6, DNS/UDP, 프록시 변경, CONNECT 및 임의 Unix 소켓 경로 | 도구에 IP 네트워크나 다른 소켓 마운트를 제공하지 않으며, B의 실행 상태 검사기가 검사 시점의 허용 구성 일탈을 거부함 |
| 두 프로세스 모두 | 차단: 호스트 Docker 소켓, 호스트 네트워크, 추가 마운트 | 이 구성에서 제공하지 않으며, B의 실행 상태 검사기가 발견하면 거부함 |

전송부의 등록 목적지와 프로토콜 제한은 A가 담당합니다. Docker 내부망만으로는 전송부가 같은 네트워크의 다른 피어에 접근하는 것을 막지 못합니다. C는 승인된 대상만 연결하고 전송부와 대상, 두 컨테이너만 구성원인지 확인해야 합니다. 네트워크는 연결 가능한 상태(`Attachable`)가 아닌 로컬 내부 Docker 브리지로 만들고, IPv4와 IPv6 모두 `com.docker.network.bridge.gateway_mode_ipv4=isolated`, `com.docker.network.bridge.gateway_mode_ipv6=isolated`를 지정해야 합니다. Docker의 일반 `--internal` 브리지는 호스트 브리지 주소가 생겨 해당 주소의 호스트 서비스에 접근할 수 있습니다. 두 `isolated` 모드는 이 주소를 없애며 B는 모드가 빠진 네트워크를 거부합니다([Docker 문서](https://docs.docker.com/engine/network/port-publishing/#gateway-modes)). 대상이 이 네트워크에 들어올 수 없다면 호스트가 소유한 별도 출구 설계를 검토해야 합니다. 일반 브리지로 바꾸어 사용하지 마세요. 호스트 공개 포트는 없습니다.

`E2_CHANNEL_VOLUME`은 C가 실행마다 새로 만드는 외부 볼륨이어야 하며, 그 안에는 인증된 A 소켓만 있어야 합니다. C는 프로세스를 시작하기 전에 볼륨의 소유권, 권한, 프로토콜, 실행 식별자, 수명주기를 검증해야 합니다. 이 초안은 UID, 키, epoch, 승인, 예산을 발급하지 않습니다. Compose 프로젝트, 두 컨테이너의 `org.abc2lab.e2.run` 레이블, 외부 내부망의 같은 레이블은 C가 이미 검증한 비밀이 아닌 실행 참조값에서 받아야 합니다. 레이블 일치는 일관성 검사일 뿐 권한이나 C의 보호된 실행 결속을 대신하지 않습니다. 도구는 채널 디렉터리를 읽기 전용으로 받습니다. 프록시 환경변수를 비우는 것은 추가 방어입니다. 도구가 값을 바꾸더라도 IP 경로는 없습니다.

## 실행 상태 검사와 남은 C 작업

`verify_runtime.py`는 C가 프로세스를 시작한 뒤 **검사 시점의** Docker 객체 스냅샷을 확인합니다. Docker Engine 28 이상을 사용해야 하며, 기본 CLI는 native `docker`이고 `E2_DOCKER_CLI` 또는 `--docker-cli`로 명시적으로 바꿀 수 있습니다. 출력에는 daemon의 제한된 신원(ID·이름·버전·OS)과 검사한 비민감 Docker ID가 기록됩니다. 도구의 네트워크 모드·마운트·권한, 전송부의 네트워크·포트, 대상 네트워크의 `Internal` 값과 양쪽 `isolated` 게이트웨이 모드, 정확한 두 구성원, 채널 볼륨 레이블을 확인합니다. 실행 중인 두 프로세스의 Docker PID와 서로 다른 명시적 비root UID·GID도 요구하고, 전체 컨테이너 목록에서 tool·sender·target의 network/PID/IPC namespace에 합류한 외부 컨테이너를 거부합니다. 보호 대상과 참조 peer의 full ID는 실제 inspect 목록에 결속하고, 불명 참조나 전체 목록 누락은 검사 실패입니다. 채널 볼륨은 tool·sender만 마운트할 수 있으며 target을 포함한 다른 컨테이너의 마운트는 거부합니다. 이름 없는 bind도 `Source`가 볼륨의 실제 `Mountpoint`와 같거나 상·하위 경로로 겹치면 거부합니다. 이 비교는 Docker daemon의 POSIX 경로를 구성 요소 단위로 대조하며, Mountpoint나 외부 bind Source가 불명확하면 검사 실패로 처리합니다. 도구와 전송부의 IPC namespace는 `private`만 허용하며 대상의 PID namespace도 기본값(비공유)과 IPC `private`를 요구하고, 대상의 추가 네트워크·공개 포트도 거부합니다. 이 검사는 권한을 부여하지 않으며 검사 시점 이후의 변경을 감시하지 않습니다. 검사기는 볼륨이 바인드 기반 `Options`가 없는 일반 로컬 볼륨인지도 확인합니다. C는 볼륨 내부를 검사하고 Unix 소켓 ACL을 집행해야 합니다. B의 메타데이터 검사만으로는 소켓의 소유자나 프로토콜 또는 살아 있는 프로세스 handle을 증명할 수 없습니다.

경로 비교는 POSIX 절대 경로만 지원합니다. 전체 컨테이너 스냅샷에는 종료된 컨테이너도 포함하므로 Windows 형식 bind 경로 등 해석할 수 없는 항목이 있으면 검사가 실패합니다. 중지된 무관 컨테이너나 오래된 alias도 전체 스냅샷의 미해결 namespace·mount 참조로 검사 실패를 만들 수 있습니다. 현재 검사기는 전체 스냅샷의 항목 제외를 지원하지 않으며, 검사 조건을 충족하는 전용 POSIX daemon에서 실행해야 합니다. 경로 문자열 대조로 symlink·별도 bind-backed volume의 실제 파일 동일성까지 확인하지는 않습니다. host PID namespace(`--pid host`)의 무관 프로세스와 host-level 공존 컨테이너는 이 verifier가 증명하는 범위가 아니며, C의 운영 보호와 host 측 mount identity 검증으로 다뤄야 합니다. C 통합 수용에서는 daemon-host의 경로 별칭 보호 조건 또는 host 측 mount identity 검증을 별도로 확인해야 합니다.

C는 계약 순서에 따라 `BlockNewFlows(run)`과 `TerminateAndWait(run)`을 제공하고, cgroup·프로세스·상속된 디스크립터의 종료 증거를 확보해야 합니다. 이어서 A의 신규 요청 차단·기존 요청 배출을 조율하고 감사 마감 뒤 권한을 해제해야 합니다. 컨테이너나 A만 중지한 사실로는 이 순서를 증명할 수 없습니다.

`test_verify_runtime.py`는 합성 inspect 데이터로 검사기를 시험합니다. `test_compose_config.py`는 합성 값으로 Compose 구성을 렌더링합니다. `test_compose_docker.py`는 폐기용 유휴 프로세스 두 개로 실제 Compose 구성을 시작해 Docker 상태를 검사한 뒤 해당 프로젝트를 제거합니다. `test_boundary_docker.py`는 긍정 대조군과 대상 수신 건수를 사용해 직접 IPv4/IPv6 TCP·UDP, DNS, 프록시 변경, 불투명 CONNECT, `/bin/sh` 자식, Python 손자 프로세스를 검사합니다. `test_channel_docker.py`는 A의 ready nonce·요청·제한 ACK와 같은 패킷 형식을 사용하는 **합성 전송부** 및 `probe_packet_channel.py`로 HTTP 대상 수신 1건, 스트림 소켓 대체 시도 차단, 커널 자격 관측을 확인합니다. `probe_ancestor_proc_docker.py`는 C의 조상 receiver·자식 private `/proc` 후보에 필요한 user/PID/mount/net namespace 생성이 기본 비root Docker 설정에서 가능한지만 확인합니다. 실제 A 서비스나 C의 `PeerAuthorizer`는 호출하지 않습니다. 최종 A/C 시험과 필요한 연결 입력은 [`ACCEPTANCE.md`](./ACCEPTANCE.md)에 적었습니다.

후보 실험의 `status: SKIP`·종료코드 0은 실험 스크립트가 정상 종료했다는 뜻입니다. 수용 여부는 `status`와 `candidate_accepted`를 함께 확인하며 SKIP을 통과로 집계하지 않습니다. `registered.sock`은 합성 fixture의 소켓 이름이고, 실제 listener 경로·ACL은 C와 연결 계약에서 정합니다.

현재 Docker Desktop의 별도 기본 PID namespace에서 합성 전송부가 관측한 도구 패킷의 `SCM_CREDENTIALS`에는 UID·GID가 예상값으로 들어왔지만 PID는 0이었습니다. A의 `auditpeer` 수신기는 PID가 0 이하인 패킷을 거부하므로, 현 Compose 구성과 A 진입점의 실제 연결은 **차단**됩니다.

폐기용 **후보 실험**에서 도구를 정확한 전송부 컨테이너 ID의 사설 PID namespace에 합류시키고 서로 다른 비root UID·GID와 각자의 네트워크 namespace를 유지하자 양의 패킷 PID가 관측됐습니다. 도구의 signal·ptrace, `/proc`의 `environ`·`mem`·`fd` 접근은 거부됐고 전송부 종료 뒤 합성 도구도 중지됐습니다. 하지만 도구가 전송부의 `/proc/.../cmdline`을 읽을 수 있어 이 후보는 **차단** 상태입니다. 후보의 볼륨 권한은 폐기용 fixture이고 운영 소켓 ACL 증거가 아닙니다. 제품 Compose는 PID namespace 공유를 적용하지 않으며 B 검사기는 정확한 전송부 ID에 대한 합류도 현재 거부합니다. C와 A는 `/proc` 노출을 해결하고 살아 있는 프로세스 handle·실행 결속·철회를 요청마다 확인하는 host `PeerAuthorizer`, 소켓 ACL과 실제 종료 증거를 합의해야 합니다. PID 0 허용이나 무검사 허가는 추가하지 않습니다.

C의 로컬 process-scope 커밋 `88959e4153fc78b83f22f531f553a206fb791e19`는 조상 receiver와 자식의 사설 user/PID/mount/net namespace·private `/proc`를 사용한 OS 시험에서 부모 `cmdline` 읽기를 차단했다고 보고합니다. 이는 Docker 배치의 증거가 아닙니다. B의 폐기용 기본 비root·capability 제거·`no-new-privileges` Docker 시험에서는 같은 namespace 생성에 대한 `unshare`와 x86_64 `clone` 호출이 모두 `EPERM`으로 거부돼 private `/proc` 장착·차단 시험을 **SKIP**했습니다. 이 환경에서 해당 후보는 **BLOCKED_OS_PERMISSION**이며 추가 capability나 seccomp 완화로 우회하지 않았습니다. 실제 Docker에서 검증된 보호 구성이 마련되기 전까지 제품 구성과 A+B 인증 시험은 미완료입니다.

`probe_network.py`는 격리된 도구 프로세스 안에서 **폐기 가능한 합성 대상**만 시험하는 제한된 프로브입니다. 페이로드나 비밀을 수집하지 않고 시도와 오류만 보고합니다. 등록된 GET의 성공, TLS, 철회, 예산, 감사에 관한 수용시험은 C와 통합한 **실제 A 서비스**를 대상으로 실행해야 합니다. 시험용 전달 결과를 NE01, NE02, NE08, NE09의 수용시험 통과로 취급하지 마세요.

기존 `deploy/engine-isolated/`, A의 `netboundary/`, C의 기동 파일은 수정하지 않았습니다. `docker compose -f deploy/e2-network-boundary/compose.yaml`에는 C가 제공하는 이미지, 보호된 채널, 내부 대상 네트워크가 필요합니다. 실행마다 고유 프로젝트를 사용하고 검증 후 해당 프로젝트의 폐기용 자원만 제거하세요.
