# 감사 RPC 메시지 credential 관찰 코어 — 별도 검토안

상태: 신규 `auditpeer/`의 **비활성 Linux 개발 코어**.
dev `88415fff88f1c66c4307ba370c360e6ad338efcc`에서 분리했다.
기존 #146의 stream 제안·역할·세대 계약을 변경하지 않으며 이 코어를 소비하는 서비스,
listener, 권한 발급·서버 기동을 연결하지 않는다. seqpacket은 별도 팀 검토 제안이다.

## 목적과 커널 의미

연결을 만든 parent와 FD를 상속받아 실제 메시지를 보내는 child를 구분할 관찰값을 만든다.
`SO_PEERCRED`는 연결 당시 값이고 `SO_PASSCRED`의 `SCM_CREDENTIALS`는 각 메시지의
커널이 수용한 credential을 제공한다. 기본값은 sender PID·real UID/GID다.
sender가 kernel에서 허용한 real/effective/saved UID/GID를 선택할 수 있고 capability가
있으면 다른 값을 지정할 수도 있으므로 **현재 effective UID의 증명**으로 쓰지 않는다.
[Linux unix(7)](https://man7.org/linux/man-pages/man7/unix.7.html)

이 관찰은 PID 재사용·namespace mapping·현재 process handle·등록된 principal·
role/instance lease·epoch를 입증하지 않는다. privileged sender와 중계 터널도 호스트가
따로 다뤄야 한다. FD 위임을 막거나 E②/DB/감사 권한을 발급하는 API가 아니다.

## API와 소유권

```go
receiver, err := auditpeer.NewReceiver(conn, auditpeer.Limits{
    MaxPacketBytes: 4096, Timeout: time.Second,
})
packet, err := receiver.Receive(ctx)
err = receiver.Close()
```

`NewReceiver`는 **모든 non-nil conn을 consume**한다. 실패 때도 Close하므로 caller가
재사용하지 않는다. 성공 뒤 read/deadline/Close와 socket option의 독점 소유자는 receiver다.
caller의 동시 read, FD 복제·전달이나 외부 option/deadline 변경은 지원하지 않는다.
receiver는 복사하지 않고 pointer로 사용한다. 신규 socket·listen·서비스를 만들지 않는다.

`Packet.Payload`는 불투명한 제한된 바이트다. wire framing·JSON·요청 의미를 검증하지 않는다.
`Packet`과 `Credentials`는 `json:"-"`로 public JSON DTO에서 제외한다.
String/GoString이 적용되는 `%v/%+v/%#v`는 redact하지만 다른 fmt verb나 직접 field 접근까지
숨긴다고 보장하지 않는다. trusted consumer가 직접 접근한 값을 로그·LLM·공유 근거로 보내지 않아야 한다.
이 타입이나 사용자가 만든 같은 모양의 Go/JSON 데이터는 인증·실행 권한이 아니다.

## 수신·상한·실패

- 실제 AF_UNIX/SOCK_SEQPACKET을 확인하고 constructor가 SO_PASSCRED를 켠다.
  매 Receive에서도 on을 재확인하며 관찰된 off는 거부한다. 외부 공유 FD가 option을
  바꾸는 TOCTOU/ABA를 이 재확인으로 해결했다고 주장하지 않는다.
- packet은 1에서 65,536바이트까지, timeout은 0초 초과 30초 이하다. 이 값은 개발 코어의
  메모리·대기 상한 제안이며 #146의 snapshot 크기·운영 wire/API 결정을 대체하지 않는다.
- recvmsg는 `MSG_DONTWAIT|MSG_CMSG_CLOEXEC`를 사용한다. EINTR는 context/Close를
  확인하며 즉시 재시도하고 EAGAIN/EWOULDBLOCK에서만 Go poller에 대기한다.
- packet 하나에 양의 PID를 가진 정확한 SCM_CREDENTIALS 1개만 허용한다.
  누락·중복·추가/손상 control·zero PID·빈/초과 payload·MSG_TRUNC/MSG_CTRUNC는 거부한다.
  실패 시 payload나 credential의 부분 성공을 반환하지 않는다.
- 취소·timeout은 gate 대기부터 수신까지 제한한다. 취소 callback이 시작됐다면 완료를
  join한 뒤 deadline을 지우고 다음 Receive에 gate를 넘긴다.
  Close는 gate를 기다리지 않고 conn을 닫아 진행 중 read를 깨운다.
- 실패한 packet은 소비·폐기될 수 있다. context 취소/timeout은 미수신·RPC 미실행의 증거나
  자동 재시도 근거가 아니다. 메시지 replay/ACK 규칙은 이 코어가 정하지 않는다.
- 외부 오류는 고정 sentinel만 반환하며 원시 OS 오류·payload·FD를 오류에 넣지 않는다.

## 설치된 FD 폐기

RIGHTS는 허용하지 않는다. recvmsg에서 close-on-exec로 FD를 설치하고, control walker가
이미 만난 권리도 이후 malformed/truncated control 오류와 무관하게 모두 닫는다.
이를 위해 전체 parse 실패 시 앞선 control을 버리는 파서에 cleanup을 의존하지 않는다.
작은 고정 ancillary 버퍼에 들어오지 못한 RIGHTS는 Linux가 폐기한다.
[recv(2)](https://man7.org/linux/man-pages/man2/recvmsg.2.html),
[unix(7)](https://man7.org/linux/man-pages/man7/unix.7.html)

Linux의 SO_PASSPIDFD는 SCM_PIDFD를 통해 별도 FD를 설치할 수 있다.
constructor와 매 read에서 해당 option이 켜져 있으면 차단하고, ENOPROTOOPT만 구형 커널의
미지원으로 취급한다. 다른 getsockopt 실패는 거부한다. 방어적으로 실제 SCM_PIDFD를 받으면
그 descriptor도 닫고 packet을 거부한다. pidfd를 actor lease로 반환하지 않는다.
[Linux socket option 정의](https://raw.githubusercontent.com/torvalds/linux/v6.6/include/uapi/asm-generic/socket.h),
[FD 설치 구현](https://raw.githubusercontent.com/torvalds/linux/v6.6/include/net/scm.h)

현재 kernel ABI의 RIGHTS/PIDFD 폐기를 검증한다. 미래에 새 FD-installing ancillary가
추가되면 별도 호환성 검토가 필요하다. 독점 FD 소유권 위반이나 악의적인 host/root에
대한 완전한 방어를 주장하지 않는다. close-on-exec도 명시적 ExtraFiles/FD 전달을 막지 않는다.

## 검증 구분과 남은 경계

2026-11-03, Go 1.26.3 linux/amd64·WSL kernel 6.18.33.2에서 전체 race
**34 pass / 0 fail / 0 skip**을 확인했다. 후속 pidfd CLOEXEC 직접 단언을 추가한
집중 race도 **4 pass / 0 fail / 0 skip**이다. 둘을 합쳐 고유 사례 수로 세지 않는다.
독립 코드 리뷰의 EINTR 재시도·SCM_PIDFD 누수를 보완하고 재리뷰를 마쳤으며,
vet·전체 build가 통과했다. 기존 Go consumer의 auditpeer import 검색은 0건이다.

실제 로컬 socketpair·recvmsg와 같은 UID의 별도 exec child를 사용한다.
SO_PEERCRED의 parent PID와 상속 FD로 보낸 메시지의 child PID가 다른지 확인한다.
실제 RIGHTS/CTRUNC와 installed-FD 수, recv 직후 FD_CLOEXEC 및 실제 exec 미상속을 검사한다.
양성 대조에서 test descriptor의 bit만 해제했을 때 같은 child가 이를 관찰하는지도 확인한다.

실제 SO_PASSPIDFD 옵션 차단과 kernel-installed pidfd 폐기·FD count도 확인한다.
구형 커널의 이 옵션 전용 사례는 skip으로 표시하며 실행하지 않은 증거로 합산하지 않는다.
malformed/duplicate/extra control·zero PID와 일부 EINTR는 private fault injection으로
검증하고, production 수신은 실제 recvmsg에 고정한다. UID·계정·서비스·DB·기동 권한을 바꾸지 않는다.

호스트 인증·역할/namespace·현재 instance/epoch·복원 anchor·권한 회수·RPC wire·서비스
핸들러·운영 배포는 미구현이다. #146 및 CPD/KLD의 실제 공급자 의무를 계속 유지한다.
