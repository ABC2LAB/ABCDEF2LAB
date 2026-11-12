# E② Bash 격리 코어의 현재 지원 조건

이 패키지는 호출별 Linux 프로세스·파일시스템 경계다. 기본 엔진 활성화나
실제 제품 배치 수용 완료를 뜻하지 않는다. 이 코어의 독립 검토와 엔진 연결 수용은 별개다.
아래의 배치 옵션·별도 서비스 PID 결속·일반 B 증거·도구 동등성은 후속 엔진
연결(활성화) PR에서 머지 전에 합의하고 검증한다. 그 전에는 공식 비교나 제품
배치의 수용 근거로 이 코어 결과를 사용하지 않는다.

- Linux amd64에서 실제 namespace·송신 회귀를 실행한다. arm64는 크로스빌드 근거와 실제 실행을 구분한다.
- launcher는 uid 0을 거부한다. 비특권 user/mount/network/PID namespace 생성,
  private mount·보호 FD 재열기·권한 제거·seccomp가 가능한 실행 조건이 필요하다.
- Ubuntu 23.10 이상은 AppArmor의 비특권 user namespace 제한으로 기동이 거부될 수 있다.
  관리자 변경 [#221](https://github.com/ABC2LAB/ABCDEF2LAB/pull/221)은 일회용 CI 러너의 조건만 바꾼다.
  제품 host 정책을 자동 변경하지 않으며, 기동 실패는 계속 명시적 unavailable이다.
  helper용 허용 프로필과 배치 보안 옵션은 관리자와 검토해야 한다.
  sysctl 값 하나만으로 helper 프로필의 실제 허용 여부를 단정하지 않는다.
- root 엔진 컨테이너와 기본 Docker seccomp·AppArmor·cap_drop 조건은 이 코어의
  namespace 요구와 그대로 호환된다고 보장하지 않는다.
  [기존 배치 경계의 제약](../deploy/e2-network-boundary/README.md)과 실제 이미지 수용이 별도다.
- socket·socketpair의 family는 AF_UNIX/AF_INET/AF_INET6만 허용한다.
  namespace가 격리하지 않는 AF_VSOCK과 나머지 family는 syscall 단계에서 거부한다.
  io_uring_setup/enter/register도 거부해 별도 socket 생성 경로를 열지 않는다.
  family만 검사하며 socket type·protocol 제한은 별도 network namespace와 제거된 capability에 의존한다.
  허용된 INET family의 실제 대상 직통 차단은 비어 있는 network namespace가 담당한다.
- LivePeer의 SO_PEERCRED PID 대조에는 launcher와 프록시의 같은 PID namespace가 필요하다.
  별도 E² 서비스 배치에서의 PID 결속 방식은 아직 결정·수용되지 않았다.
- 일반 relay는 stream UDS와 HTTP를 쓴다. 등록 GET 경계의 SEQPACKET B 증거를 대신하지 않는다.
  일반 모드의 B 증거 정의·수집·외부 대조는 계약 조율이 남아 있으며 이 코어 결과로 발급하지 않는다.
- 이 코어 자체는 복수 호출을 만들 수 있다. 제품의 동시 Bash 1개 제한은 후행
  [Runner #219](https://github.com/ABC2LAB/ABCDEF2LAB/pull/219)의 공유 슬롯 조립 책임이다.
  별도 Runner·Worker 소유 변경과 함께 검증해야 하며 코어 단위 동시성 시험은 제품 동시 허용 선언이 아니다.
- 읽기 전용 도구 트리는 /usr/bin·/bin, 아키텍처별 공유 라이브러리, 발견된 /usr/lib/python3* 등으로 제한된다.
  /usr/local, /usr/share, /usr/lib/node_modules, git-core, /app/skills 전체를 보장하지 않는다.
  /work는 noexec scratch이며 host workspace나 기존 산출물이 자동 공유되지 않는다.
  호출자가 요청하면(`Config.WorkspaceIn`·`SaveWorkspace`, C29) 명령 시작 전에 이전 /work의 tar를 봉인 memfd(fd 8)로
  받아 풀고, 명령과 namespace 안 모든 프로세스가 끝난 뒤(PID namespace init이 `kill(-1)`·회수) /work를 묶어
  전용 pipe(fd 9)로 돌려준다. 두 fd는 명령이 물려받지 못하고(닫음·close-on-exec), helper는 dumpable이 아니라
  `/proc/1/fd/*`를 열 수 없다. 일반 파일·디렉터리만 오가고 링크·장치·FIFO·소켓·하드 링크는 버린다.
  정상 마감이 아니면 돌려주지 않는다.
  실제 ARTEX 이미지 도구 가용성의 동등성은 미검증이고 평가의 선언된 차이와 조율해야 한다.
- 메모리 전체 상한은 제공하지 않는다. 이 경계의 NE07 근거는 Bash와 그 자식의 지원 실행 경로에 한정하며,
  다른 SDK 도구·MCP·서버 송신 전체의 격리 완료를 주장하지 않는다.

회귀 시험은 소유 loopback HTTP, namespace와 합성 CA·경로만 사용한다.
AF_VSOCK 회귀는 socket 생성 거부를 확인하며 host VSOCK 서비스에 연결하지 않는다.
실제 모델·대상 스캔·Docker 제품 배치·실제 권위 공급 수용은 별도다.
