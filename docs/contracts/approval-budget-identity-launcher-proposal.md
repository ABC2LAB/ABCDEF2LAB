# 공유예산 신뢰 기동기·실행 신원 경계 — 팀 검토안

2026-11-03 · 이동찬

상태: **팀 검토용 제안. 기동기·프로세스 분리·소켓 활성화는 미구현이며 운영 설정을 변경하지 않는다.** 기준 dev는 `f2c062bd596940027b184a683e69b414cf0cbb0c`다. 현재 열린 PR의 코드나 병합을 전제로 삼지 않는다.

## 1. 현재 사실과 결정할 경계

[로컬 발급 코어](../../budgetlocal/local_linux.go)의 `Listen`은 서버의 실제 effective UID와 전달받은 운영자·모델 UID가 서로 다른지 검사한다. 서버·모델 UID 0은 거부하고 운영자 UID 0은 허용한다. 그러나 숫자 `modelUID` 자체는 모델이 제어하는 실제 실행 프로세스의 신원을 증명하지 않는다. [기존 로컬 발급 계약](./approval-budget-local-issuer.md)은 신뢰 기동기의 실제 신원 확인 전 활성화를 금지한다.

현재 [일반 Worker](../../agent/worker.go)는 SDK의 Bash·Read·Write·Edit 등을 도구 집합에 포함한다. UID 분리는 LLM 추론 서버의 계정만 바꿔 달성할 수 없다. 여기서 **실행자**는 모델이 지시하는 명령·파일 작업·하위 프로세스를 수행하는 주체다. controlled-read 전용 경로의 제한을 일반 Worker 전체의 분리 증거로 확대하지 않는다. 서버·기동기·실행자 사이의 새 명령 IPC 형식은 이 문서에서 정하지 않는다.

권장 방향은 신뢰 서버가 승인 DB·Ledger를 소유하고, 모델 제어 기능은 별도 비특권 실행자로 분리하는 것이다. 분리가 입증되지 않은 배포는 소켓을 비활성으로 유지하는 안을 제안한다. 이 제안의 수용과 실제 기동 방식은 §6의 팀 결정이 필요하다.

## 2. 신뢰 주체와 입력

| 주체·입력 | 권장 경계 | 활성화 전 확인 |
|---|---|---|
| 운영자 | 모델 작업을 수행하지 않는 전용 계정. 기존 CLI의 0700 디렉터리·0600 요청/config 정책을 유지 | 설치 시 고정한 operator UID, CLI의 server UID·socket path. 이름·요청 JSON의 발급자 주장으로 UID를 추정하지 않음 |
| 서버 | 비root 전용 UID. 승인 DB·epoch·Ledger와 발급 callback 소유 | `os.Geteuid()`와 설치 정본 일치. 실행자·운영자와 UID가 모두 다름 |
| 실행자 | 비root 별도 UID로 명령·파일·MCP 하위 프로세스 수행. 같은 서버 UID로 돌아가는 대체 경로를 허용하지 않음 | 실제 프로세스의 UID와 권한, 모든 자식 경로의 경계 유지. 추론 서버 UID나 env의 숫자는 증거가 아님 |
| 신뢰 기동기 | 운영자가 승인한 배포 관리 영역. 모델이 쓰기·호출·설정 교체할 수 없음 | 실행 파일·계정 매핑·단일 서버 소유·실행자 수명에 대한 관리 책임자 확인 |
| 기동 증거 | 기동기 내부의 비공개 객체. 서버 기동 세대·현재 authority epoch·실행자 세대·프로세스 handle에 결속 | 모델/API가 전달한 JSON이나 저장된 PID만으로 재구성하지 않음. 재시작 뒤 재사용 금지 |

초기 제안은 동일 호스트의 같은 user/PID namespace에서 UID/PID를 해석하는 범위로 제한한다. 다른 namespace의 UID 숫자를 그대로 비교하거나 unmapped UID를 운영자로 인정하지 않는다. 컨테이너·별도 user namespace 지원은 매핑과 peer 해석을 검증한 별도 결정 전 비활성이다.

UID 분리만으로 비밀 접근 차단을 주장하지 않는다. 실행자는 서버/운영자 private 디렉터리·DB 자격·승인 config·Ledger 제어 객체를 읽거나 바꿀 수 없어야 한다. 상속된 FD·환경·공유 volume·보조 group·capability·ptrace 경로도 점검한다. 같은 UID의 비신뢰 프로세스나 악의적 root를 방어한다는 범위는 아니다.

## 3. 기동 증거의 권장 생성 순서

아래는 후속 구현의 제안 순서이며 현재 API가 아니다.

1. 모델 입력과 분리된 설치 정본에서 operator/server/executor UID와 허용 실행 경로를 읽고, 신뢰 기동기가 단일 서버 소유를 확인한다. 권한 불명·동일 UID·root 실행자는 여기서 거부한다.
2. 기동기가 직접 실행자를 생성하며 실제·effective·saved·filesystem UID/GID를 정렬하고 보조 group·capability를 제거하는 안을 검토한다. `no_new_privs`도 설정하되 그것만으로 기존 권한·FD·비밀 접근이 제거됐다고 판단하지 않는다.
3. 자식의 안정된 프로세스 handle을 유지하고, 권한 강하·exec 뒤 **새로 연결한** private 제어 채널의 커널 PID/UID를 해당 자식과 대조하는 방식을 권장한다. 그룹·capability·namespace는 `SO_PEERCRED` 값만으로 확인했다고 하지 않고 기동기가 별도로 검증한다. 기동 전 생성한 socketpair의 credential을 강하 후 신원으로 오인하지 않는다.
4. 서버는 같은 현재 기동 세대·authority epoch의 유효한 증거와 설치 정본을 대조한 뒤에만 로컬 발급 소켓의 공개를 허용한다. 공개 직전 실행자 생존·세대·설정 불변성을 재확인한다. `allowSameUID` 시험 helper를 제품 활성화 경로로 사용하지 않는다.
5. 발급 callback은 [서버 연결 계약](./approval-budget-server-wiring.md)의 task/policy/reset 검증과 [승인 DB 정본](./approval-budget-store.md)의 영속 성공 후 게시 규칙을 계속 적용한다. 기동 증거는 운영자의 승인이나 Grant가 아니다.

[unix(7)](https://man7.org/linux/man-pages/man7/unix.7.html)의 `SO_PEERCRED`는 connect/listen/socketpair 시점의 credential을 반환한다. 따라서 상속된 채널의 값과 현재 실행 신원을 동일시하지 않는다. [pidfd_open(2)](https://man7.org/linux/man-pages/man2/pidfd_open.2.html)의 handle은 대상 수명·PID 재사용 경계를 다루는 수단이며 UID 증명을 대신하지 않는다. fork 뒤 handle 획득에는 reap 조건도 있으므로 기동 시점에 handle을 안전하게 확보하는 방식의 검증이 필요하다.

[커널 no_new_privs 문서](https://docs.kernel.org/userspace-api/no_new_privs.html)는 exec로 새 권한을 얻는 것을 제한하지만 기존 권한의 모든 사용을 막는 장벽은 아니다. 이 제안은 기동기의 기존 권한 제거와 FD/환경 격리를 별도로 요구한다.

## 4. 수명 상실과 차단

기동기 연결 상실·실행자 종료/교체·검증 정보 변경은 유효한 기동 증거의 상실로 처리하는 안을 제안한다. 서버가 상실을 관측·확정해 같은 조율 경계에서 증거를 무효화하고 새 발급·실행 연결 gate를 닫은 시점을 차단의 기준으로 삼는다. 이어 소켓을 닫고 진행 중 callback 취소와 실행 중지·drain을 기다린다. 실제 사망 시점과 비동기 관측 사이를 원자화했다고 주장하지 않으며, 관측 전이나 이미 진행 중인 발급·송신 결과의 불확실성은 보존한다. `SO_PEERCRED`를 한 번 통과했다는 이유로 새 기동 세대에 권한을 넘기지 않는다. DB와 메모리의 부분 성공은 기존 `unknown` 결과와 동일 요청 참조 재시도 규칙으로 처리하며, 실패를 확정 거부나 새 BudgetRef 발급으로 바꾸지 않는다.

같은 epoch에서는 같은 승인 요청 참조로 멱등 재시도한다. 다른 epoch의 같은 참조는 현재 BudgetAuthority.Issue가 ErrBudgetApprovalStale로 거부하므로, 이를 과거 발급이 없었다는 증거로 보지 않는다. epoch 교체 뒤 신뢰된 역사적 결과를 확인하는 조회 경로는 별도 후속 요구이며 현재 IPC에 구현된 것으로 취급하지 않는다. 과거 Grant를 현재 ready 상태나 실행 권한으로 재게시하지 않고, 미확인 결과를 새 참조 자동 발급으로 덮지 않는다.

기동기/실행자 교체 시 현재 authority를 종료하고 새 epoch·새 명시 승인으로 재개하는 보수적 안을 권장한다. 정확한 종료 순서·재개 조건은 팀 확인 전 미확정이다. 이전 Grant를 새 빈 Ledger에 자동 등록하거나 증거 갱신만으로 예산을 충전하지 않는다. 중지 전에 admission된 요청의 도달·DB 발급 성공 여부가 불명확하면 그대로 기록한다.

실행자 main process의 종료만으로 자손 프로세스의 drain을 증명하지 않는다. 기동기는 모델이 빠져나갈 수 없는 실행 그룹의 수명을 관리하는 방식을 제안해야 한다. 구체적인 cgroup/service manager 적용과 권한은 배포 담당의 별도 결정이다. 자손 잔존이나 종료 확인 실패가 있으면 gate를 닫은 채 유지하고 새 실행·authority로 자동 전환하지 않는다. pidfd는 이 그룹 전체의 종료 증거가 아니다.

## 5. 계획된 수용시험

모두 **미실행 시험 계획**이다. 권한 분리 전용 개발 환경에서 담당자가 승인한 계정을 사용하고, 이 문서 검증을 실제 OS 격리 시험으로 세지 않는다.

| ID | 조건 | 확인할 결과 |
|---|---|---|
| IL01 | 서버/운영자/실행자 UID 충돌 또는 서버·실행자 root | 소켓 공개·발급 callback 0 |
| IL02 | 선언한 executor UID와 실제 자식 UID 불일치, 추론 서버 UID만 제공 | 기동 증거 거부, 일반 경로로 활성화하지 않음 |
| IL03 | 강하 전 socketpair credential·위조 readiness·다른 자식의 응답 | 강하 후 현재 자식의 증거로 수용하지 않음 |
| IL04 | 자식 종료·PID 재사용·stale handle·다른 namespace | 이전 증거로 새 프로세스 승인 0 |
| IL05 | saved UID·보조 group·capability·상속 FD/환경으로 서버 비밀 접근 가능 | 활성화 거부. no_new_privs만 보고 통과시키지 않음 |
| IL06 | 일반 Bash·Read/Write/Edit·MCP·custom tool의 대체 기동 경로 | 모델 제어 기능이 서버/운영자 신원·private 정본에 도달하지 않음. 미검증 경로는 비활성 |
| IL07 | 소켓 공개와 실행자 사망/기동기 끊김의 barrier 경합, 자손 잔존 | 조율 경계에서 증거 무효화 뒤 새 발급/연결 0, 실행 그룹의 취소·drain 확인. 실패 시 gate 폐쇄 유지·불확실 결과 보존 |
| IL08 | 발급 DB commit 후 응답 상실·기동기 교체 | 동일 epoch의 같은 참조는 멱등 재시도. 교체 후 stale 거부를 보존하고 역사적 조회는 후속으로 구분. 과거 Grant 재게시·새 참조 자동 발급 0 |
| IL09 | 서버 재시작·증거 파일/PID만 복원·빈 Ledger | 과거 기동 증거/Grant 자동 재사용 0, 새 epoch·명시 승인 요구 |
| IL10 | 운영자 접속·실행자 접속·고정 server UID 불일치 | 운영자만 검증 callback 가능. 실행자/잘못된 서버에는 승인 메타데이터 0 |

## 6. 팀 결정 요청

| ID | 권장안 | 확인 담당·확정에 필요한 근거 |
|---|---|---|
| ID01 | 모델 제어 기능을 별도 실행자로 옮긴 뒤 활성화. 검증 전에는 소켓 비활성 유지 | 이동찬·최민준: 일반/통제 Worker 및 SDK 도구·MCP의 실행 경로 목록과 IL06 결과 |
| ID02 | 운영자 관리의 신뢰 기동기가 계정/실행 파일/권한 설정을 소유 | 이동찬·배포 관리자: 실제 관리 주체·지원 Linux 환경·privilege 강하 방법. 운영 설정 변경은 별도 승인 |
| ID03 | 직접 자식의 안정된 handle과 강하 후 새 채널의 kernel credential을 함께 검증 | 이동찬·최민준: PID 재사용·namespace·credential 관찰의 구현 방식과 IL01~05/07 검증 |
| ID04 | 기동 증거 상실 시 authority 종료 후 새 epoch·명시 승인 | 이동찬·최민준: 발급/실행 gate·callback/drain 잠금 순서와 IL07~10 결과 |

이 문서는 계정 생성, setuid/setgid·service 설정, 운영 DB 역할 변경, 서버 소켓 등록, 실행 IPC 구현을 수행하지 않는다. 후속 구현은 위 결정을 확인하고 별도 코드 리뷰·격리 시험을 거쳐야 한다.
