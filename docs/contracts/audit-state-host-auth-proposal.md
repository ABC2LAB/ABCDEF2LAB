# 감사 정본 서비스의 호스트 인증·신뢰 공급 초안

2026-11-03 · 이동찬 · **팀 검토용 제안. 인증/기동/권한/운영 활성화 구현 아님.**

## 1. 범위와 현재 코드

초안 작성 기준은 dev `d68d78e3d69be5a4d029a1ae7b274d1e6bf599ab`이며,
코드 부재를 재검토한 dev는 `88415fff88f1c66c4307ba370c360e6ad338efcc`다.
기존 [CPD](./e2-audit-journal.md)·[KLD](./e2-audit-key-generation-cli.md)·
[MD01/02/04](./task-mode-marker.md#81-결정표)의 역할과 실패 경계를 유지한다.
현재 [#142](https://github.com/ABC2LAB/ABCDEF2LAB/pull/142)의 엔진/최초 저장,
[#143](https://github.com/ABC2LAB/ABCDEF2LAB/pull/143)의 감사 Source,
[#145](https://github.com/ABC2LAB/ABCDEF2LAB/pull/145)의 snapshot prefix 검증은
실제 publisher·최신 sealed head·키 신뢰·배치/자식 종료 권위를 발급하지 않는다.
별도 영속 코어는 위 dev에는 없고 다음 열린 PR에만 있다.
[#147](https://github.com/ABC2LAB/ABCDEF2LAB/pull/147) 확인 head `8e0efd9a0f8995cf19e3421ed058dda11896a53a`의
[`auditkeys.Authority`](https://github.com/ABC2LAB/ABCDEF2LAB/blob/8e0efd9a0f8995cf19e3421ed058dda11896a53a/auditkeys/model.go#L76)와
[#148](https://github.com/ABC2LAB/ABCDEF2LAB/pull/148) 확인 head `f5515077eda692be4b212d379303d8fddcb036d8`의
[`auditcheckpoint.Host`](https://github.com/ABC2LAB/ABCDEF2LAB/blob/f5515077eda692be4b212d379303d8fddcb036d8/auditcheckpoint/types.go#L122)가
아래 typed Go Host/Authority 설명의 출처다. 실제 인증 공급자가 연결되기 전에는 운영 근거가 아니다.

이 초안은 그 빈 경계를 첫 구현으로 좁히는 선택지다. 실제 producer·데몬·DB 역할·
UID 분리·비밀 저장소·서명 형식이 이미 존재하거나 팀에서 확정됐다고 가정하지 않는다.
이 문서의 metadata 예시는 실행 허가·서명·소유권 증명이 아니다.

## 2. 검토 결정표

| ID | 권장 검토안 | 확인 창구·보류 조건 |
| --- | --- | --- |
| AH01 | 첫 transport는 **호스트 내부 Unix socket**. 연결된 peer의 커널 신원을 서비스가 확인하고, client도 설치 때 확인한 서비스 UID/instance와 연결 peer를 대조. 인증된 endpoint FD의 모델 자식 상속/전달 금지를 검증하고 권한 강하 뒤에는 새 연결. 원격 서비스는 별도 인증/회수 계약 뒤 지원. | 이동찬(서비스 조립), 실제 운영자/기동기, 최민준(무결성 리뷰). 경로·설정 UID 숫자 또는 연결 시점 신원만으로 이후 실제 발신자를 입증했다고 보지 않음. FD/instance 경계를 확인할 수 없으면 활성화 보류. |
| AH02 | **namespace·instance·operation에 결속한 권한**. publisher/reader/operator를 구분하고 인증된 peer에서 actor를 결정. 실제 등록된 instance lease와 전체 실행 Scope를 함께 검사. | 이동찬·운영자. `publisher_ref/actor_ref/namespace`를 요청 JSON에서 받았다는 이유로 권한을 부여하지 않음. 하나의 UID에 같은 namespace의 임의 writer를 모두 허용하지 않음. |
| AH03 | **운영자가 확인한 store/epoch/root 배치**만 사용. supervisor가 검증된 장기 handle·현재 배치/instance lease와 외부 복원 anchor를 공급. 기존 epoch row가 없거나 불일치하면 유지보수/차단. | MD02의 운영 책임 겸임 권장안인 이동찬과 실제 운영자. 실제 책임·provisioning·복구 승인 확정 필요. 빈 DB/새 경로를 새 신뢰 세대로 자동 승인하지 않음. |
| AH04 | registry는 **secret provider가 검증한 불변 key identity/version 결속**만 소비. 모델·일반 client는 opaque identity를 발급하지 않음. namespace당 첫 provider는 하나의 검증된 identity domain으로 제한하는 안. | 이동찬(KLD 공급), 실제 secret provider/운영자, 최민준(해석). 실제 identity·동일 key alias 방지·scope 유지 증거가 없으면 active/신뢰 소비 보류. 다른 domain 이관은 별도 검토. |
| AH05 | 인증·secret mapping·epoch 확인에서 얻은 **유효한 trust scope 동안** 코어 연산을 실행. 취소/회수는 새 scope 발급을 닫고 기존 연산을 포착·drain한 뒤 확정. | 이동찬. 단발 `true` callback이나 전후 값 일치만으로 lease·ABA 방지·복원 연속성을 주장하지 않음. 실제 scope 공급 구현 전 비활성. |
| AH06 | publisher는 path 대신 **상한이 있는 snapshot 바이트**를 전달. 서비스가 한 번 소유 복사해 전체/이전 prefix를 검사. CPD04에 따라 publisher RPC로 HMAC 키를 받거나 전달하지 않으며, 서비스가 별도 비밀 저장소의 검증된 KeyRef 매핑으로 선택. | 이동찬(wire framing/한도·비밀 저장소 연결), 최민준(무결성 리뷰). 해당 연결은 후속 검토. 서버가 client 파일 경로를 열거나 검증 중 live 파일을 자르지 않음. |

이동찬은 위 서버/어댑터 구현 담당이며 운영 책임 겸임은 기존 문서의 **권장안**이다.
최민준은 무결성·소비/평가 경계 리뷰어이며 실제 OS/DB 권한 발급자로 새로 지정하지 않는다.
이경준·윤재상의 KG/fixture 발급 영역도 이 초안으로 변경하지 않는다.

[CPD의 평가 후속](./e2-audit-journal.md)에 따라 평가 회차에서는 외부 게시 on/off를 프로파일 조건으로 고정하고,
epoch 공급·socket 경로를 [러너 격리 근거(평가 프로토콜 §2)](../evaluation/evaluation-protocol.md)에 넣는 합의 전에는 활성화하지 않는다.

## 3. 신원·권한 조립 제안

서비스·publisher(애플리케이션 서버)·model 실행 주체를 실제로 분리하는 배치를 권장한다.
운영자 역할도 명시하며 기존 [예산 로컬 발급](./approval-budget-local-issuer.md)의
server/operator/model 분리 요구를 완화하지 않는다. 해당 예산 프로토콜을 감사 RPC로
자동 재사용하거나 현재 worker가 이미 별도 UID로 실행된다고 가정하지 않는다.

커널 peer 신원만으로 새 배치 세대/실행 소유권이 전부 확인되지는 않는다. 신뢰 기동기가
기존 writer 종료·실제 model 실행 신원·허용 publisher instance·namespace를 확인하고,
service가 그 검증된 instance 수명을 연결에 결속하는 방식을 제안한다. PID 숫자나 client가
보낸 generation 문자열만 쓰지 않는다. 정확한 supervisor handle/복원 anchor 형식은 미확정이다.

`SO_PEERCRED`는 connect/listen/socketpair 시점의 신원을 보고하며, Unix socket FD는
다른 프로세스에 전달할 수 있다. 따라서 연결 인증만으로 이후 모든 RPC의 실제 송신
프로세스까지 같다고 추론하지 않는다. [Linux unix(7)](https://man7.org/linux/man-pages/man7/unix.7.html)
기동기는 인증된 socket/서비스 handle이 model 자식의 상속 FD·명시적 ExtraFiles·FD 전달
경로로 넘어가지 않고, 권한 있는 publisher가 모델 요청을 그대로 중계하는 터널로 쓰이지
않음을 확인해야 한다. close-on-exec 설정만으로 명시적 FD 전달까지 막았다고 보지 않는다.
권한 강하/instance 변경 뒤에는 기존 연결을 폐기하고 최종 신원으로 새 연결·lease를 얻는다.
매 operation의 현재 instance/trust scope·회수 상태를 확인하되, 그 재확인이 이미 위임된
live endpoint의 실제 발신자를 자동 구별하는 장치는 아니다. 비위임 경계를 입증할 수 없으면
첫 배치를 활성화하지 않는다. 메시지별 packet credential 관찰의 후속 코어는 열린
[#154](https://github.com/ABC2LAB/ABCDEF2LAB/pull/154) 확인 head `a7223e626f63a32244faa02389385e6baa5aec65`에서 제안한다.
이 초안의 인증·권한 배치가 완료됐다는 뜻은 아니다.

| 인증 뒤 권한 | 허용할 제안 범위 | 추가 필수 근거 |
| --- | --- | --- |
| publisher | 등록된 namespace/instance의 journal 최초 등록·연속 게시·마감, 자기 연산 ACK/현재 head 조회 | 전체 Scope/JournalID/KeyRef 불변 결속, 현재 store epoch, expected revision, 실제 publisher 수명 |
| consumer/reader | 허용된 실행의 현재 sealed head와 비민감 metadata 조회 | 현재 epoch/읽기 권한/키 신뢰 확인. 과거 ACK 조회를 최신 head로 대체하지 않음. |
| operator/control | 명시 승인된 key metadata 전이·설치/복구 절차 요청 | kernel/host 인증 + 실제 작업별 승인·dependency/drain 근거. 모델/worker가 operator 요청을 대행해 권한을 얻지 않음. |
| migration/provisioning | 검토된 DDL과 별도 승인된 배치/anchor provisioning | runtime API와 별도 설치 권한. 열린 [#147의 Migrate](https://github.com/ABC2LAB/ABCDEF2LAB/blob/8e0efd9a0f8995cf19e3421ed058dda11896a53a/auditkeys/store.go#L21)·[#148의 Migrate](https://github.com/ABC2LAB/ABCDEF2LAB/blob/f5515077eda692be4b212d379303d8fddcb036d8/auditcheckpoint/store.go#L25) 성공은 epoch 신뢰·실행 활성화 성공이 아님. |
| model/미등록 peer | 없음 | parsing·검증·발급 worker 슬롯을 점유하기 전 가능한 지점에서 거부. 커널 큐/CPU 고갈 방어까지 구현됐다고 주장하지 않음. |

CPD의 sealed는 순수 MAC 결과나 진단 성공 상태가 아니다. 실제 close/drain 주체의 최종
snapshot·필수 ACK·마감 근거를 인증된 publisher 수명에 결속한 뒤에만 마감 요청을 허용한다.
중지/실패한 실행도 감사 마감 자체는 가능하므로, E③ `Completed`·판정은 별도 실제 engine
근거로 유지한다. `closed=true`·`completed=true` 같은 client 값은 그 근거를 대신하지 못한다.

## 4. snapshot·secret·신뢰 scope

서로 다른 UID가 보호된 journal 디렉터리를 공유하도록 권한을 넓히는 방식을 기본으로 두지 않는다.
publisher는 이미 확정한 checkpoint와 그에 대응하는 snapshot을 전송하고, 서비스는 선언 길이와
실제 전체 길이를 제한해 소유 사본을 만든다. core의 MAC/prefix·tail 검사가 끝나기 전에는
head/receipt를 공개하지 않는다. 정확한 request frame·record 수·timeout은 설정과 검토로 정한다.
context는 임의 차단 Reader/협조하지 않는 공급자를 강제 중단하지 못한다.

registry metadata RPC에는 키 원문·hex/base64·공개 fingerprint를 넣지 않는다. 실제 secret
provider가 같은 key에 여러 version/alias를 붙여도 canonical identity를 일관되게 확인해야 한다.
열린 [#147 schema의 `UNIQUE(namespace,secret_identity)`](https://github.com/ABC2LAB/ABCDEF2LAB/blob/8e0efd9a0f8995cf19e3421ed058dda11896a53a/auditkeys/schema.sql#L12)는
**공급자가 검증한 identity의 중복**만 막으며 실제
key 바이트의 진위를 자체 증명하지 않는다. 준비 metadata·파일 존재·32바이트 길이를 active로
번역하지 않는다. journal 생성의 active 검사와 과거 검증용 키 소비 허용을 별도로 확인한다.

Host/Authority는 인증·배치·secret mapping의 scope를 **DB row lock 전에** 확보한다.
snapshot I/O/검증·외부 권위 조회도 row lock 전에 끝내고, transaction 안에서는 현재 epoch·
불변 결속·expected revision/phase를 다시 대조한다. callback이 같은 SQL row를 재진입해 잡거나,
SQL row를 잡은 회수 경로가 trust scope/프로세스 종료를 기다리게 하지 않는다.
회수는 짧은 gate 폐쇄·연산 포착 → 모든 DB/서비스 잠금 밖의 취소/drain → CAS/확정 순서를
권장한다. 실제 공급자가 이 순서를 보장할 수 없으면 해당 연결을 활성화하지 않는다.

## 5. 결과 불명·재시작·권한 회수

- commit/응답 유실은 같은 operation ref·같은 불변 입력으로 확인한다. 과거 operation receipt와
  현재 head/현재 key 상태는 각각 인증 조회한다. timeout을 미저장으로 단정하지 않는다.
- 새 프로세스·복구·권위 상실에서 기존 pending/임의 epoch·새 key identity를 자동 생성해
  정상 경로를 복원하지 않는다. 누가 어떤 외부 anchor로 복원했는지 실제 승인/증거가 필요하다.
- namespace/instance/secret 권위가 사라지면 새 admission을 막고 추적 중인 연산을 취소한다.
  이미 commit한 기록을 삭제·덮어쓰기·phase 후퇴로 숨기지 않는다. key trust를 잃은 MAC 일치를
  정상 출처로 승격하지 않는다.
- 초기 core가 단일 DB 자격증명을 쓰거나 runtime 쓰기 권한을 분리하지 않은 상태라면 DB
  관리자/임의 SQL까지 방어했다고 표현하지 않는다. 실제 역할·GRANT·UID 변경은 별도 작업이다.

## 6. 팀 리뷰의 수용 증거 — 아직 실행하지 않은 계획

| ID | 계획 시험 | 필요한 관측 |
| --- | --- | --- |
| AHT01 | 모델/미등록 peer·가짜 socket/서비스 peer·인증 FD 상속/전달을 주입한 기동 | 모델의 core callback·key 공급·DB mutation 0. FD 비위임을 검증할 수 없는 배치는 기동/활성화부터 거부. |
| AHT02 | 허용 UID지만 다른 instance/namespace/Scope·권한 강하 뒤 낡은 연결 | 같은 actor 이름/기존 endpoint로 우회 0. 연결 신원만으로 FD 실제 발신자를 알아냈다고 주장하지 않음. |
| AHT03 | 빈/복원/다른 epoch DB와 state/root 교체 | 자동 epoch 생성·정상 head/active 소비 0 |
| AHT04 | 같은 key의 다른 version/alias·provider identity 불명 | 새 identity로 회피 0, 정본 공급 실패 차단 |
| AHT05 | publish/activate/consume 중 scope 회수·instance 변경·낡은 endpoint 재사용과 동시 CAS | 새 admission 0, 기존 연산 포착·종료, 이전 scope 재사용 차단, 원자성/결과 불명 구분 |
| AHT06 | partial/extra snapshot·같은 MAC의 다른 분기·과거 ACK | 현재 head 오승격 0, 원본 보존 |
| AHT07 | Close 실패·자식 미종료·client 완료 문자열 | sealed 권위/진단 완료 오승격 0 |
| AHT08 | 실제 분리 UID·서비스 재시작·복구·키 유실 | 검증된 기동기/anchor 없이 자동 신뢰 복구 0 |

이 문서에서는 위 통합/운영 시험, service handler·wire schema·DB 역할/UID 설정,
실제 승인/신원·키/epoch 발급·Juice Shop 실행을 수행하지 않았다.
