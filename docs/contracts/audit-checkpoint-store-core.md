# CPD checkpoint 영속 코어 — 팀 검토용 구현안

> **코어 도입 단계의 기록과 현재 연결을 구분합니다.** 아래 Close/drain·외부 ACK·E③ 소비 연결을 후속으로 둔 표현은 당시 단계다. 현재 [서버 publisher](../../server/controlled_read_publisher.go)와 [로컬 provider](../../server/controlled_read_assembly_local_providers.go)에 전송기 drain 증언·Seal·checkpoint 저장·publisher ACK 및 sealed-head 소비 연결이 있다. 연결 범위는 [감사 publisher](audit-checkpoint-publisher.md)와 [실제 감사 공급](first-idor-audit-source.md)을 따르며, 운영 인증·복원·실제 모델/대상 수용 완료를 주장하지 않는다. 아래 코어 계약은 그대로 보존한다.

2026-11-03 · 이동찬 · **개발용 코어 구현, 운영 비활성·팀 계약 미확정**

기준은 최신 `dev` `88415fff88f1c66c4307ba370c360e6ad338efcc`와 열린 선행 #145
`b3652a180668c2752e1f8fb6130e0c3d298b9496`이다. 이 선행의
[`auditlog.VerifyExtension`](../../transmit/auditlog/extension.go)을 사용한다.
선행 head 변경 시 고유 diff와 prefix 검증 영향을 다시 확인한다. 기존 서버·DB 초기화·운영
권한과 다른 담당의 키 수명 registry는 수정하지 않는다.
구현 중 전진한 dev는 정상 merge했으며 평가 전용 변경 21파일은 이 코어의 감사/모듈 경계에
영향이 없음을 확인했다. 선행의 원래 기준 dev는 `d68d78e3`이며 history rewrite는 하지 않았다.

## 범위와 아직 필요한 결정

[`e2-audit-journal`](./e2-audit-journal.md)의 CPD03/04 중 PostgreSQL의 head·불변 이력·연산 결과와
CAS·정확 재시도 코어를 별도 [`auditcheckpoint`](../../auditcheckpoint/store.go) 패키지로 제안한다.
DDL 이름과 PostgreSQL 선택은 이 개발 구현의 구체안이며 팀의 운영 저장 형식 확정이 아니다.

실제 서비스 transport·publisher/reader 인증·UID/프로세스 격리·운영 기동·DB 역할·보존/삭제,
현재 store epoch와 독립 복원 anchor의 발급/검증, key registry 및 실제 Close/drain 증언은
**미구현·별도 팀 보안 검토 대상**이다. 다른 갈래의 `audit-state-host-auth-proposal.md`도
제안이며 이 코어의 시험 통과가 그 운영 수용 근거를 대신하지 않는다.

새 빈 DB를 신뢰하는 epoch 생성·등록 API는 제공하지 않는다. `Migrate`는 테이블·제약·불변
이력/연산의 수정·삭제 거부 trigger만 만든다. 외부에서 검증하여 provision한 epoch row와
신뢰 host lease가 없으면 게시·조회 모두 차단한다. 복원·epoch 교체 도구도 제공하지 않는다.

## Go API와 권위 수명

| API | 의미 |
|---|---|
| `Migrate(ctx, *sql.DB)` | 명시적으로 호출하는 원자 DDL. 전역 `db.Open`에서 자동 실행하지 않음 |
| `New(db, Host, ExtensionLimits)` | 이미 확보한 DB 연결과 신뢰 Go host를 조립. 권한·epoch를 생성하지 않음 |
| `Publish(ctx, Request, io.Reader)` | Register/Append/Seal/MarkIncomplete/Quarantine. 성공은 내구 커밋된 연산 receipt |
| `Head(ctx, Query)` | 별도 reader 권위를 확인한 현재 head 조회 |
| `Operation(ctx, Query)` | 동일한 범위 권위를 확인한 과거 연산 결과 조회. 최신 head라는 뜻이 아님 |

`Request`는 expected epoch·비민감 publisher/operation ref·종류·expected revision·previous/candidate
checkpoint를 받는다. checkpoint는 기존 타입 그대로이며 키·journal 원문은 레코드에 저장하지 않는다.
`Query.Binding`은 publisher·전체 Scope·JournalID·KeyRef를 모두 결속한다. 참조 문법 검사는
그 참조의 의미·실제 주체·비밀 포함 여부를 인증하지 않는다. 실제 비민감 참조 발급은 host 책임이다.

`Host.Acquire(ctx, Access)`는 요청에서 받은 원시 bool이나 JSON 권한 대신 초기 Go 조립부가
호출하는 신뢰 경계다. host는 transport가 인증한 실제 principal·정확한 작업/실행 범위·연산 목적,
독립된 현재 epoch/anchor를 확인하고 `Lease`를 반환해야 한다. Seal에는 실제 전송기 종료/drain,
Journal.Close 성공 및 앞선 기록/게시 오류가 없다는 수명 증언도 결속해야 한다. candidate의 MAC이나
caller의 `closed=true`만으로 이 증언을 만들지 않는다.

Lease는 `Context()/Epoch()/ResolveKey()/Release()`를 제공한다. 현재 권위·epoch/anchor·키 사용
범위를 **SQL commit 또는 조회가 끝날 때까지** 보장해야 한다. 단발 callback 성공, 같은 값 두 번,
요청 epoch의 단순 반환은 lease·ABA 방지·복원 정당성 증명이 아니다. 권위 교체/철회 주체는 신규
lease를 차단하고 기존 lease를 취소·drain한 뒤 전이를 선형화하거나 유효 lease가 Release될 때까지
그 범위를 유지해야 한다. DB row를 먼저 잡고 lease/drain을 기다리는 역순 잠금은 금지한다.
context 취소만으로 이미 진행 중인 commit의 rollback을 확정하지 않는다.

`ResolveKey`는 요청 키 바이트가 아닌 검증된 KeyRef mapping과 현재 목적별 키 상태를 사용한다.
코어는 host가 빌려준 키의 자체 사본만 지우며 원본 키 사본/registry의 수명은 host 책임이다.
이 API를 모델이나 일반 HTTP JSON에 직접 노출하면 권위 분리가 성립하지 않는다.

## 게시·트랜잭션 불변식

1. 입력과 host lease/현재 epoch를 검사한다. 제한된 source를 **한 번 읽어 소유한 bytes**로 동결한다.
   MAC/전체 schema 및 이전 head prefix 포함 검증, key lookup과 모든 host callback은 SQL row/advisory
   lock 밖에서 수행한다. snapshot/키 원문은 DB·receipt·오류·공유 로그로 내보내지 않는다.
2. 불변 요청 JSON과 snapshot SHA-256을 결속한 digest를 연산 ref에 저장한다. 같은 연산 ref·다른
   내용은 충돌이다. 정확 재시도는 최초 receipt 그대로 반환한다. 불명 commit 때문에 새 operation·
   journal·RunRef·승인을 자동 발급하지 않는다.
3. READ COMMITTED transaction에서 기존 epoch row를 공유 잠금으로 고정하고 정확한
   store/epoch/generation/anchor를 다시 확인한다. 같은 operation의 동시 재시도는 DB transaction
   advisory lock으로 직렬화하며 hash 충돌은 추가 직렬화만 만든다.
4. current head의 epoch·revision·checkpoint·open phase를 재확인하고 revision CAS를 수행한다.
   이력·head·operation receipt는 한 transaction에서 commit한다. callback·키 조회·MAC·source 읽기는
   이 잠금 이후 호출하지 않는다. 외부 auth/restore 변경자는 이 DB 잠금을 잡기 전에 lease를 처리한다.
5. DDL/게시 transaction은 `SET LOCAL synchronous_commit=on`을 강제하고 commit 성공 후에만
   receipt를 반환한다. 실제 `fsync`/WAL·storage·전원 장애 내구 구성은 신뢰 host/DB 운영의
   별도 수용 전제다. 이 코어는 global 설정을 변경하거나 전원 장애 복구를 시험하지 않는다.
   commit 오류는 `ErrCommitUnknown`과 **zero receipt**다.
   실제 rollback인지 commit 후 응답 유실인지 단정하지 않고 같은 ref의 `Operation`과 별도의 `Head`로
   확인한다. 등록/쓰기의 앞 단계 실패에도 head나 고아 이력을 남기지 않는다.

`audit_checkpoint_history`는 전체 epoch/anchor와 checkpoint·phase·revision을 보존한다.
현재 epoch가 바뀌었다는 이유로 과거 이력을 새 epoch의 자료로 재해석하지 않는다. RunRef와
JournalID는 store 안에서 교체·재사용하지 않는다. 역사적 복원을 새 등록으로 우회하는 기능도 없다.
이 제약은 코어 API/DDL의 범위이며 DB 관리자 침해·역할 분리·외부 backup rollback 탐지를 증명하지 않는다.

| 연산 | 허용되는 전이 |
|---|---|
| Register | 인증된 header-only, sequence 0, 빈 실행 슬롯 → revision 1/open |
| 같은 등록·새 연산 ref | 아직 동일한 revision 1/open일 때만 head 변경 없는 no-op |
| Append | expected open head 포함 + sequence 증가 → revision+1/open |
| 같은 checkpoint Append·새 연산 ref | expected head가 현재와 정확히 같을 때만 no-op, 연산 결과는 저장 |
| Seal | 신뢰 host 수명 증언 + 검증된 최종 prefix → sequence가 같아도 revision+1/sealed |
| MarkIncomplete/Quarantine | 이미 수용한 prefix 그대로 + source nil → 별도 terminal revision |
| terminal 이후 새 연산 | 모두 거부. 기존 연산의 정확 재시도/인증 조회만 허용 |

incomplete/quarantine 전이는 실패한 원본을 잘라 검증한 것으로 만들지 않는다. checkpoint는
이미 수용된 prefix 그대로다. 그 뒤 더 긴 정상 prefix를 넣어도 재개·sealed 승격하지 않는다.
Seal은 실행 성공·실제 송신·취약점 판정의 정당성을 뜻하지 않는다.

## 조회와 오류 경계

Receipt에는 store/epoch/generation/anchor, operation/receipt ref, 그 연산의 revision·checkpoint·phase가
결속된다. `RecordedAt/CommittedAt`는 DB 진단 시각이며 정렬·최신성 권한이 아니다. 오래된 Register
ACK를 재조회해도 현재 head는 sealed일 수 있다. `Head`는 조회 시점의 별도 결과이며 미래까지 유효한
소비 lease·E③ issuance·export 권한을 자동 발급하지 않는다. 그 소비에는 별도 reader 권위와 계약이 필요하다.

외부에는 고정 오류 범주만 반환한다. source·DB·OS·auth 원시 오류와 키/원문을 반환하지 않는다.
MAC 검증 실패·한도 초과·취소·source 오류는 성공으로 계산하지 않는다. 일반 Reader가 blocking
Read를 무시하면 context로 강제 중단하지 못한다. 실제 IPC adapter는 취소 가능한 bounded snapshot
수집과 서비스 자원 상한을 별도로 보장해야 한다.

검증 상한은 host가 명시하며 #145의 byte/event/timeout 최대값 이내다. 이 상한은 실행 승인이나 요청
예산이 아니다. DB 보존량/등록량·서비스 큐·비용 상한은 아직 구현하지 않았으므로 운영 활성화 전 결정한다.

## 검증과 현실 구분

전용 PostgreSQL·실제 auditlog writer/MAC·합성 key/epoch/host lease를 사용하여 정상 등록/증분/마감,
정확 재시도·내용 충돌·과거 ACK/현재 head 분리, 동시 CAS/동일 연산, valid-MAC fork 거부,
row lock 밖의 key callback, transaction rollback·deferred commit 실패와 commit 후 ACK 유실 모사,
epoch 변경·권위/lease 상실·읽기 결속·한도/취소 및 terminal 경계를 검사한다.

no-op Append가 실제 head UPDATE에서 대기하는 동안 다른 transaction이 revision 3을
commit해도 CAS가 `ErrConflict`로 거부하고 head를 후퇴시키지 않는지 검사한다.
또한 cp[2] Seal에 snap[1]을 주는 MAC/prefix 거부, ResolveKey 중 lease 철회의 context 전파,
commit 직전 독립 연결의 epoch `FOR UPDATE NOWAIT`가 SQLSTATE `55P03`으로 거부되는지
각각 검사한다. Incomplete/Quarantine의 미수용 candidate·Register의 sequence 0·lease epoch
불일치도 별도로 검사하고 epoch/권위 거부는 고정 오류 범주까지 단언한다.

commit 후 ACK 유실은 테스트 전용 commit adapter가 실제 PG commit을 한 뒤 오류를 반환하는 주입이다.
실제 transport 장애·운영 복원·프로세스/UID 격리의 수용시험은 아니다. 합성 권위는 테스트 fixture가
소유하며 실제 publisher/reader 인증·실키·운영 epoch 발급·실제 Juice Shop/HTTP/LLM 실행은 하지 않는다.
기존 전송기의 외부 ACK 연결, 의도 ACK 전 송신 0 및 E③ 소비 연결은 별도 adapter 후속이다.
