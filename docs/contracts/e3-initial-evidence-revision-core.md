# E③ initial 관찰 revision SQL 코어

2026-11-03 · 이동찬 · **독립 개발 코어·팀 검토안, 서버 미연결**

기준은 dev `88415fff88f1c66c4307ba370c360e6ad338efcc`와 열린 #151
`732a131040a2e7d9dec260e2f5280339e0cf6caa`다. #142 `abd0c2646b5ddc2d19bfbed95997b3fd17d66e81`,
#143 `abbebd56de7f79122cd085f0c6bd8a4af01e53ff`는 그 history에 포함된다.
선행 head에 직접 쓰지 않고 별도 branch에서 새 [`evidencerevision`](../../evidencerevision/store.go)만 추가한다.

## 목적·정본 identity

기존 [E③ initial 저장](./e3-initial-storage.md)은 Source의 양의 EvidenceRevision을 검사하고
`(task_id, execution_ref, evidence_revision)` manifest 유일성과
`(task/run/case/evidence_revision/initial)` operation key에 사용한다.
이번 코어는 KG version·판정·그 operation key를 변경하지 않고 실제 서버 관찰 묶음의 revision 생산부만 제안한다.

[evidencebind.Context](../../evidencebind/model.go)의 계약상 E② RunRef는 한 intent/work에 재사용하지 않는다.
따라서 이 initial-only 코어의 natural identity는 **DB 안의 하나의 execution RunRef + initial 목적**이다.
TaskID/IntentID/CaseRef를 유일키에 추가하여 같은 RunRef의 다른 결속을 새 발행으로 허용하지 않는다.
전역 unique execution_ref에 DB가 발행한 양의 BIGINT revision을 단 한 번 저장한다.
sequence에는 rollback/경합으로 빈 번호가 생길 수 있으며, 연속된 숫자나 실행 허가의 순번을 뜻하지 않는다.

같은 execution의 task/intent/case·전체 Scope/ExpectedContext·관찰 digest·필드명·exploration/mode version은
불변이다. 같은 내용의 새 operation ref도 같은 revision을 참조한다. 다른 관찰·결속이면 conflict다.
정상적인 다음 관찰 회차는 기존 E②가 별도로 허가하여 이미 생성한 **새 RunRef**로 들어와야 한다.
이 패키지는 RunRef·승인·재검증 목적·새 회차를 발급하지 않는다. retest/multi-case 확장은 이번 범위 밖이다.

## 입력과 신뢰

`Capture(expected, evidencebind.Input, canaryFieldNames)`는 기존 `evidencebind.Check`로 완결된 두 관찰,
receipt·역할·시각·결과/동결 정책의 정합을 검사하고 `e3record.EvidenceDigest`를 재계산한다.
이후 opaque Collected에는 비민감 full context·관찰 ref·digest·필드명만 남긴다.
DB에는 canonical binding과 digest/refs만 보존하며 raw Input·정책 원문·URL·headers·body·표식 값은 넣지 않는다.
필드명 256개/각 256 bytes·binding 64KiB 상한은 자원 제한 제안이며 판정 규칙이나 승인 예산이 아니다.

**실제 private collector의 trusted Go 출처가 필수**다. Check·digest·opaque 타입은 입력의 실제 실행 출처를
인증하지 않는다. 모델/일반 HTTP JSON·외부 digest 문자열·필드에 채운 bool을 받아 권위로 바꾸는 경로는 없다.
Capture/Issue를 모델 tool이나 일반 endpoint로 직접 노출하면 이 전제가 깨진다.
opaque Collected의 JSON round trip은 유효한 Collected를 만들지 못한다.

현재 DB에는 RunRef의 실제 실행 등록이나 A/B·승인·KG·fixture 출처를 인증할 row가 없다.
이 코어가 그런 참조를 SQL로 인증했다고 해석하지 않는다. 그 실제 producer, 완료 claim·감사 마감,
source 정합 및 저장 권한은 기존 E②/E③ host/collector가 계속 책임진다.

## API·외부 scope 수명

| API | 경계 |
|---|---|
| `Migrate(ctx, *sql.DB)` | 기존 앱 task/intent/mode 테이블 위 명시 DDL. global db.Open/schema는 수정하지 않음 |
| `New(db, ScopeProvider)` | 기본/항상 true provider 없음. 실제 host scope 공급이 필수 |
| `Capture(...)` | 실제 collector 입력의 정합·digest를 동결. 권한을 발급하지 않음 |
| `Issue(ctx, Request)` | 현재 controlled mode gate와 DB 소유 관계를 확인하고 불변 revision/receipt 발행 |
| `Lookup(ctx, 동일 Request)` | 별도 현재 reader scope에서 정확한 과거 receipt만 조회. 새 발행 0 |

Provider.Acquire에는 IssueInitial/ReadReceipt, task/intent/full Scope·operation ref·정확 request digest가 결속된다.
Acquire·보호 marker 파일 읽기·MarkerSnapshot·canonical hashing·Check는 SQL row lock **밖**에서 끝난다.
provider 오류의 부분 자원 정리는 provider 책임이며 lease를 반환하지 않는다. 성공한 lease만 코어가 받아
transaction/read 종료 뒤 정확히 한 번 Release한다.

실제 Lease는 root/writer/marker/복원 세대와 해당 목적의 권위를 commit/조회 끝까지 유지해야 한다.
단발 file 검사·같은 callback 두 번·0700/inode 일치가 그 lease나 ABA/복원 정당성의 증명은 아니다.
실제 인증·epoch/namespace·보호 파일 producer는 미구현 외부 전제이며 auditpeer를 재포장해 자동 생성하지 않는다.
교체·철회 주체는 lease 취소/drain을 SQL row 획득 전에 정렬해야 한다. 취소만으로 이미 진행된 commit의
rollback을 단정하지 않는다.

## 실제 SQL 검사·원자성

Issue는 task→mode→intent 순서로 DB 현재 행을 잠그고 다음 사실을 대조한다.

- `tasks.id/exploration_id/deleted_at`와 archive 상태. 신규 발행은 live task만 허용하며 기존 앱과 같이
  `db.ArchiveFailed` 이력은 허용하지만 진행 중 archive는 차단한다.
- `task_control_modes.mode/version`와 Request의 expected mode version, lease가 밖에서 읽어 고정한 marker.
  row lock 안에서는 외부 callback 없이 `taskmode.Decide`만 수행한다. unknown/clear/mismatch는 발행하지 않는다.
- `exploration_nodes.id/exploration_id/kind`로 해당 실제 intent가 그 task의 exploration에 속하는지 확인한다.

이어 operation/run별 transaction advisory lock으로 정확 재시도와 초기 관찰 경합을 직렬화한다.
revision history와 operation receipt는 같은 transaction에서 저장하고 commit 뒤에만 반환한다.
둘 모두 update/delete 거부 trigger를 두며 base task/exploration/intent FK를 유지한다.
이 FK는 보존 중 base row 삭제를 제한한다. 실제 장기 보존·archive purge/삭제·복원과의 이관 정책은 팀/운영 검토가
필요하며, 자동 cascade·물리 삭제 또는 운영 활성화를 이 코어로 추가하지 않는다.

SQL은 `synchronous_commit=on`을 transaction 안에서 강제한다. 실제 fsync/WAL/storage·backup 내구성은
운영 전제다. commit 오류는 ErrOutcomeUnknown과 **zero Receipt**이며 rollback이나 성공을 단정하지 않는다.
저장된 operation ref와 동일 전체 요청의 Lookup으로 결과를 확인한다. 새 ref/revision/승인을 자동 발급해
불명 결과를 우회하지 않는다. raw DB/OS/authority 오류는 반환하지 않는다.

## 과거 조회와 현재 실행을 분리

Issue의 정확 재시도도 현재 mode/version/marker를 잃으면 거부한다. 그러나 commit 불명 뒤 marker 손실이나
mode 변경이 있었더라도 현재 인증된 ReadReceipt lease와 실제 task→exploration→intent 관계·정확 binding을
확인한 Lookup은 과거 저장 receipt를 반환할 수 있다. soft-delete/archive 상태의 읽기 허용은 실제 reader 정책의
책임이며, base row가 실제로 없거나 관계가 다르면 코어도 거부한다.

그 Receipt의 ModeVersion/Revision/시각은 **과거 관찰 식별 metadata**다. 현재 revision 권한·전송·finding 쓰기
허가로 승격하지 않는다. 기존 RecordControlledFinding의 현재 mode/marker 및 Source gate는 그대로 필요하다.
reader lease가 없거나 철회되면 과거 조회도 차단한다.

## 검증과 이번 한계

전용 PostgreSQL·실제 task/intent/mode 행·소유 marker 파일·합성 관찰/host scope로 발행·정확 재시도·
동일 RunRef 다른 binding 거부·다른 회차·동시성·SQL 관계/mode/marker 오류·row lock 밖 scope 호출·
rollback/deferred commit 실패·실제 commit 후 ACK 유실 주입·mode 상실 후 역사 조회·불변 history를 검증한다.
이 합성 scope와 관찰은 실제 인증·A/B·KG·승인·운영 root/epoch를 인증하지 않는다.

이번에는 기존 server/collector/SourceReader나 db/db.go에 연결하지 않는다. 실제 host producer와 필수 자료를
확보한 뒤 별도 통합 단위에서 private collector에만 연결해야 한다. 실제 HTTP/LLM/Juice Shop 진단,
실제 권한/epoch/key/승인 발급·운영 DB 변경은 하지 않는다.
