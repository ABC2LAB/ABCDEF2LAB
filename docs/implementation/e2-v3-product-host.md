# E② v3 제품 Host 연결

## 권한과 설치 수명

신규 Compose 설치는 `product_e2_host` LOGIN을 발급하고 기존 NOLOGIN `product_e2_head_writer`의 최소 head 권한만 상속한다. Host DB DSN, v3 감사 HMAC 키, E③ 객체·신원 digest 키, epoch anchor, 저널은 Host 전용 `e2-host-state` 볼륨(`/h`, UID 1002, 0700)에 둔다. 일반 준비 프로세스(UID 1000)와 API는 이 볼륨을 마운트하지 않는다. 두 키는 각각 새 난수로 발급하며 일반 v2 화면 투영용 `/p` 감사 키와도 별개다. Host 컨테이너는 내부 `control` DB 망에만 붙는다.

`e2-host-ipc`에는 Unix 소켓만 둔다. 디렉터리는 UID 1002/GID 1000, 0750이라 준비 프로세스가 소켓에 접속할 수는 있지만 파일을 지우거나 교체할 수 없다. Host는 기동 때 이 디렉터리의 소유자·그룹·권한·심볼릭 링크 여부를 다시 확인한다. 소켓은 0660이고 Host는 `SO_PEERCRED`로 UID 1000만 받는다. API·Workbench는 이 볼륨을 마운트하지 않는다. 설치 프로세스만 일회성으로 두 볼륨에 접근하며, 설치 성공 뒤 Host 비밀 볼륨을 UID 1002로 넘긴다.

기존 설치에는 Host 자격을 자동 이관하지 않는다. 기존 launch의 `e2_host_socket`은 비어 있어 v2 실행이 유지된다. Host 서비스는 빈 비밀 볼륨일 때 `legacy-unavailable`로 대기하며 게시 소켓을 만들지 않는다. 일부만 복원된 Host 볼륨은 기동을 거부한다. 기존 설치의 이관은 별도 설치 관리자 절차와 시험이 필요하다.

## 실행·기록 수명

신규 설치의 보호된 launch에 고정 소켓 경로를 기록한다. 실행 준비의 기존 `AuthorizeExecution` 및 정책 SHA 재확인 뒤, `NewExecution`과 C14 인증 준비 **전에** Host에 실행 Scope·정책 revision을 등록한다. Host는 실행별 저널 디렉터리와 v3 헤더를 Sync·게시·재조회한 뒤 ACK한다. 일반 사건은 E②가 v3 행을 Host에 보내고, Host Recorder가 Write·Sync·현재 head 게시·재조회에 성공한 뒤에만 ACK한다. 그 뒤 동일 행의 v2 화면용 투영을 기존 저널에 쓴다. 화면용 v2 저널은 E③ 확정 근거가 아니다.

실행의 Runner 종료·active call drain 뒤 `Release`가 로컬 투영을 닫고 Host Seal을 요청한다. Host는 클라이언트의 마감 통지만 믿지 않고 현재 DB head·Host 복사본·저널 MAC 전체와 송신 `intent`별 마지막 `result`를 다시 확인한다. 정상 Seal은 `sealed`, 소켓 단절이나 Host 재기동 뒤 검증된 중단은 `interrupted`다. 두 상태는 **감사 이력의 마감**이며 진단 성공 여부가 아니다. 검증된 `result`가 `unknown`이면 HTTP 결과 불명은 그대로 보존한다. Host 연결·게시·ACK·epoch 검증 실패는 E② 감사 실패로 신규 전송을 막는다. 전송 후 ACK 불명은 정상 결과로 취급하지 않으며 자동 재전송하지 않는다.

Host 연결은 모델 계획이나 Worker 사이의 무활동만으로 끊지 않는다. 각 게시 RPC에는 실행 측 제한 시간이 있고, 준비 프로세스의 연결 종료·Host 중지는 세션 Abort로 처리한다. 30초 이상 무활동 뒤에도 같은 세션의 기록·봉인이 가능한지 시험한다.

Host는 전용 epoch anchor와 DB epoch의 일치를 기동 때 확인한다. Host 전용 볼륨의 `head.json`을 게시 ACK마다 Sync하고 DB 현재 head와 대조한다. 기동 및 새 실행 전에는 기존 저널 전체 MAC·체인·스냅샷을 다시 검사한다. 연결이 끊기면 해당 소켓은 더 이상 새 송신 ACK를 줄 수 없다. Host 프로세스가 재시작했다면 독점 `host.lock`을 얻은 뒤에만 이전 저널을 복구 검사한다. 모든 송신 의도의 결과 사건, 현재 head 게시 ACK, 전체 저널 무결성이 확인된 `open` 회차만 별도 `interrupted` head로 게시·재조회한다. 미완료 `intent`는 자동 마감하지 않는다. 해당 저널의 현재 정본·전체 MAC·미완료 결속과 이전 실행의 종료가 확인된 경우에만 [운영자 격리 계약](../contracts/e2-host-operator-recovery.md)에 따른 별도 HMAC 기록을 허용한다. 원래 head는 `open`으로 남고 E③ 봉인 근거가 될 수 없다. ACK 불명에서 DB·볼륨이 일치하지 않거나 epoch 교체·저널 손상이 있으면 계속 차단한다. 준비 컨테이너 종료 표시만으로 이 증거를 대체하지 않는다. **DB와 Host 볼륨을 동시에 같은 과거 시점으로 복원한 경우**는 별도 외부 단조 anchor 없이 탐지할 수 없다.

기존 설치의 DB 스키마·권한 이관은 별도 작업이다. 이전에 만든 감사 테이블에는 `phase` 제약에 `interrupted`가 없을 수 있다. 이관할 때는 이전 스키마에서 제약을 갱신한 후 기존 행의 보존과 새 마감 상태의 저장을 함께 시험한다.

Host는 커널 peer UID와 기존 준비 서비스의 실행 권한 검사를 신뢰한다. 현재 Host가 운영자 승인 원본이나 C14 세션을 독립 조회하는 기능은 없다. 따라서 이 연결만으로 `ErrE3JournalUnavailable`을 제거하지 않는다. E③ 전용 행은 일반 v2 투영이 표현할 수 없어 거부한다. 실제 E③ Transport·Journal, 구조화 응답 검사, 계정 결속·동결 정책 검증은 별도 통합 작업이다.

## 검증과 한계

- 일회용 PostgreSQL 16에서 제한된 Host LOGIN으로 헤더·사건·봉인 게시, 최소 GRANT, 일반 역할의 head 권한 부재를 검증한다.
- 로컬 합성 HTTP 서버에서 실제 E② 실행의 한 요청이 Host Recorder와 현재 head에 결속되는지 검증한다. 실제 VAmPI·유료 모델 요청은 실행하지 않는다.
- DB head 되감기, epoch 변경, 미봉인 head, Host ACK 불명, 정책·task 불일치 및 실패 뒤 추가 기록 차단을 시험한다. 일회용 DB에서 소켓 단절 후 빈 저널·완료된 송신·결과 불명 사건은 `interrupted`로 마감하고 미완료 송신은 `open`으로 남기는지 대조한다.
- Compose 정규화 출력에서 Host 비밀 볼륨이 준비·API에 없고 Host가 `control` 망에만 있는지 검사한다.
- 현재 `e2checkpoint.Recorder`는 게시마다 최대 256MiB 저널 전체를 다시 검증한다. 기동·새 실행도 이전 저널을 전체 검증한다. 장기 운영의 처리량·RSS는 아직 측정하지 않았다.
- 제품 Compose 전체 기동·브라우저·VAmPI 종단 수용시험은 별도 보호 설치 입력과 승인된 환경이 없어 `NOT_RUN`이다.
