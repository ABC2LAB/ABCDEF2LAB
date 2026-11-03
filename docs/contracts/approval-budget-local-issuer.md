# 공유예산 로컬 운영자 발급 경계

2026-11-03 · 이동찬

이 구현은 사용자가 선택한 **운영자 전용 로컬 명령**의 IPC·CLI 코어다. 별도 프로세스인 CLI가 DB에 직접 승인 행을 쓰거나 새 Ledger를 만들지 않는다. 서버 프로세스가 현재 epoch와 Ledger를 소유한 채 `AuthorizeAndIssue` callback에서 task/policy/reset 권한 검증과 `BudgetAuthority.Issue`를 같은 등록 동기화 경계에서 수행해야 한다.

## 신원과 파일

- Linux Unix socket 서버는 커널 `SO_PEERCRED`의 운영자 UID만 받는다. 운영자 UID 0(root)은 허용하지만 서버·모델 UID는 root가 아니어야 하고, 서버·운영자·모델 실행 UID는 모두 달라야 한다. 모델 실행 UID는 신뢰된 기동기에서 실제 하위 프로세스 신원으로 확인해 전달해야 한다. 이를 확인할 수 없으면 소켓을 열지 않는다.
- 소켓은 서버 UID 소유의 0755 디렉터리 아래에 놓인다. 상위 경로의 symlink·비신뢰 쓰기 권한을 거부한다. 소켓은 로컬 연결을 위해 0666이지만 UID가 다른 peer는 승인 callback에 들어가지 못한다. 기존 경로는 덮어쓰지 않고 종료 때 원래 inode만 제거한다.
- CLI는 운영자 UID 소유의 0700 디렉터리 안 0600 요청 JSON과 별도의 0600 신뢰 config를 읽는다. config에는 절대 `socket_path`와 설치 시 확인한 `server_uid`를 고정한다. CLI는 경로에서 서버 UID를 추측하지 않으며, 디렉터리·소켓 소유자와 연결된 서버의 `SO_PEERCRED`를 config의 UID와 각각 대조한다.
- JSON은 최대 4096바이트의 단일 객체다. 미지 필드와 중복 키를 거부한다. 요청에는 발급자 주장이 없고, 서버가 peer UID에서 `IssuerRef`를 만든다. 반환 Grant의 요청 참조·발급자·task·policy/reset·한도·기간을 모두 대조한다.

## 상태와 제한

발급 전 peer·파싱·검증 거부는 `rejected`다. callback은 **발급 전 확정 거부**에만 `ErrDenied`와 빈 record를 반환한다. callback의 그 밖의 오류·deadline·record와 오류의 동시 반환·Grant 대조 실패·전송 실패는 `unknown`이다. 원시 DB·OS 오류, URL, 비밀 헤더·키를 내보내지 않는다.

CLI는 성공 시 exit 0과 승인 메타데이터, 확정 거부 시 exit 1과 `budget approval rejected`, 결과 불명 시 exit 3과 `budget approval outcome unknown; retry the same approval_request_ref`를 출력한다. Submit 실패 시 stdout은 비어 있다. **결과가 불명확하면 같은 `approval_request_ref`로 재시도**하고 다른 참조·BudgetRef를 새로 발급하지 않는다. 응답을 잃었어도 DB에 ready 승인 행이 있을 수 있다.

서버는 읽기부터 callback까지 RPC당 5초 deadline을 공유한다. 클라이언트도 5초와 호출자 deadline 중 이른 시각을 적용하고 취소 시 연결을 닫는다. 서버 Close는 callback 취소와 연결 종료 후 종료를 기다린다. callback은 취소에 협조해야 한다. 비공개 시간 제한·Lstat 주입 헬퍼는 시험 전용이고 공개 UID 정책을 완화하지 않는다.

이 브랜치는 #119의 `e21845d4890a96162d8ec20141c115fba22517e9`를 일반 merge로 포함한다. 격리 PostgreSQL 시험은 DB 확정 후 응답 상실 시 같은 참조의 멱등 재시도와, authority probe 중 callback 만료 뒤 기존 ready Resolve·다음 승인 성공을 검증한다. #119 병합 후 dev base 전환과 재검증이 남아 있다. CLI 응답은 비민감 승인 메타데이터이지 송신 권한이 아니다. 같은 요청 참조의 발급 멱등성과 DB 확정 후 게시 규칙은 [승인 DB 정본](./approval-budget-store.md)이 집행한다.

이번 PR은 소켓 서버를 제품 서버 기동에 등록하지 않고, 실제 운영자 계정·모델 계정·디렉터리 권한·운영 DB를 변경하지 않는다. 현재 배포에서 모델 도구 실행 UID가 서버 UID와 같다면 이 경계를 활성화해서는 안 된다. 별도 신뢰 기동기와 실제 OS 신원 분리, task/policy/reset validator, 종료·재시작 처리, 서버 실행 연결은 후속 검증이 필요하다. 개발 시험의 승인 성공 경로는 동일 UID를 허용하는 **비공개 테스트 헬퍼**로 프로토콜만 검증한다. 실제 분리 OS 계정의 운영 수용시험은 완료하지 않았다.

비차단 후속: 분리 OS 계정에서 peer·소켓 owner 가드를 각각 확인하는 수용시험, 요청 파일의 openat(O_NOFOLLOW|O_NONBLOCK)·하드링크 거부, 인증 전 연결 슬롯 고갈 대응은 아직 완료하지 않았다. 현재 CLI의 일반 파일 확인은 FIFO 교체 경합을 완전히 방지하지 않는다.
