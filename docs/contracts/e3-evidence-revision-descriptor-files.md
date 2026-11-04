# E③ revision Descriptor 보호 파일 저장 코어

## 상태와 목적

팀 검토용 개발 코어다. 선행 PR #157의 head
`9c079d92e5728411774005cf4760124a55068bb4`를 기준으로 한다.
실제 서비스·CLI·server collector·운영 root·권한 설정을 활성화하지 않는다.

`Describe(valid Request)` 뒤, **Issue를 시도하기 전에** `Put`이 성공해야 한다.
SQL commit ACK를 잃으면 원래 operation을 유지하고, 재시작 후 `Load`와 SQL `Reconcile`로
과거 Receipt를 확인한다. 새 RunRef/op를 만들어 재실행하지 않는다.
Descriptor는 데이터이며 실행·finding·`Collected`·reader 권한이 아니다.

## 최소 API와 신뢰 공급부

`NewDescriptorFiles(DescriptorRootProvider)`, `Put(ctx, Descriptor)`,
`Load(ctx, exactOperationRef)`만 추가한다. 디렉터리 생성·목록·수리·삭제·회전·Issue 호출·
SQL migration API는 없다.

Provider는 `AcquireDescriptorRoot(ctx, DescriptorFileAccess)`에서 실제 현재 호출자,
`PersistDescriptorFile` / `ReadDescriptorFile` 목적, 정확한 operation과 host namespace를
인증하고 reader/writer/restore 연속성을 작업 종료까지 보호해야 한다.
RootScope의 Context·장기 보유 Anchor·Directory·Release는 이미 획득한 실제 lease다.
경로·0700·UID/inode 일치·같은 callback 값의 반복으로 권위나 ABA/복원 연속성을 증명하지 않는다.
실제 host producer는 외부 필수이며 이 패키지는 기본/항상 true 대체재를 제공하지 않는다.

Acquire가 error를 반환하면 부분 자원은 provider가 직접 정리하며 scope 소유권은 이전되지 않는다.
성공한 nonnil scope는 후속 검증이 실패해도 core가 정확히 한 번 Release한다.
빌린 Anchor는 닫지 않는다. core가 소유한 모든 FD를 닫은 뒤 Release한다. host는 그때까지
Anchor와 namespace를 보호해야 하며 Context 철회 통지만으로 lease 보호를 먼저 해제하면 안 된다.
일반 파일 I/O를 goroutine으로 남겨 두며 취소하지 않는다. 취소는 I/O 경계에서 확인한다.

## 형식과 실제 Linux I/O

기존 Descriptor 8필드만 canonical JSON으로 저장하며 상한은 4,096바이트다.
기존 양의 ID/version·full Scope·operation·비제로 request digest 검사를 재사용한다.
재직렬화와 완전히 같아야 하므로 중복/추가 key·trailing input·비정규 표현은 거부한다.
raw Input·관찰 값·policy·headers/body·key 원문은 저장하지 않는다. 참조 자체가 비민감한
host 발급 ID라는 기존 전제는 여전히 필요하다.

파일명은 domain-separated operation hash의 단일 구성요소이며 입력 경로를 받지 않는다.
host가 준비한 기존 0700 디렉터리와 장기 Anchor를 대조하고 Anchor에 대한 `openat`으로
자체 FD를 얻는다. 디렉터리 최종 구성요소와 각 파일에 NOFOLLOW·CLOEXEC·NONBLOCK을 적용한다.
조상 경로와 namespace의 신뢰는 host 책임이다. 디렉터리/파일은 실행 프로세스의 effective UID
소유여야 한다. 파일은 regular·0600·nlink 1·상한 이내여야 하며 디렉터리 nlink는 양수여야 한다.

쓰기 순서는 새 O_EXCL 임시 파일의 전체 Write, file Sync,
`renameat2(RENAME_NOREPLACE)`, directory Sync, FD close다. 그 전후로 root와 파일의
inode·UID·mode·nlink·크기를 확인한다. 새 O_EXCL 파일만 0600으로 보정하며 기존 root/파일은
chmod하지 않는다. no-replace를 지원하지 않는 kernel/filesystem은 성공으로 처리하지 않고,
덮어쓸 수 있는 rename으로 대체하지 않는다.

기존의 정확히 같은 Descriptor는 inode를 교체하지 않고 file과 directory를 다시 Sync한 뒤 ACK한다.
다른 내용은 conflict이며 불완전/잘못된 파일은 거부한다. 동시 쓰기에서도 기존 파일을 덮어쓰지 않는다.
중간 실패·commit/ACK 불명·게시 경쟁에서 진 임시 파일은 보존하고 자동 삭제·수리하지 않는다.
쓰기 시작 뒤 불명 결과는 `ErrDescriptorFileUnknown`과 고정 stage로 반환한다.
부분 임시 파일은 재시도에서도 지우지 않으며 새 최종 파일을 no-clobber로 게시할 수 있다.
잔여 파일 목록/GC/격리 운영 정책은 별도다. error에는 원시 경로·payload·provider/OS 오류를 넣지 않는다.

Load는 새 실제 ReadDescriptorFile scope로 상한 내 동일 inode를 검증하며 읽는다.
error면 zero Descriptor만 반환한다. 그 뒤에도 SQL Reconcile이 현재 ReadReceipt lease와 DB의
정확한 저장 binding을 독립 검증한다. 파일 보유는 권위가 아니다. 정확한 operation의 비민감 진단
참조는 caller가 유지한다. operation 발견용 인덱스나 추측 조회는 추가하지 않는다.
Linux 외 플랫폼은 명시적으로 비지원 오류를 반환하며 기본 root나 대체 저장을 만들지 않는다.

## 검증과 실제 한계

소유 tmpdir/실제 FD와 합성 host로 저장·재시작 읽기, 동일/충돌 재시도·동시 no-clobber,
symlink/FIFO/hardlink/권한/root·파일 교체, 형식·상한·취소, Sync/게시/ACK/close 단계 실패의
보존을 검증한다. 전용 PG에서는 Put 성공→Issue commit 후 ACK 유실→원본 Request/Input 제거→
새 Load→현재 Reader lease Reconcile을 확인하고 reader 거부도 확인한다.
시험 provider/관찰은 합성이며 실제 계정·키·KG·승인이나 Juice Shop 진단이 아니다.

성공 ACK는 실제 OS의 file/directory fsync와 close가 성공했다는 뜻이다. 전원 장애·매체/가상화
캐시·fsync 구현·backup/restore·관리자/privileged writer 침해를 이 시험으로 증명하거나 탐지하지 않는다.
실제 namespace/UID 분리·배타 writer·복원 anchor·host 인증·운영 root provisioning은 외부 필수다.
운영 활성화나 OS 권한 변경은 수행하지 않는다.
