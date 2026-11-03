# E③ revision·descriptor 보존 및 복구 검토안

## 1. 상태·의존·범위

팀 검토용 제안이다. 기준은 #159
`32bd87f77460953f2a60a5de3cdc391cc0b75758`이며 통합 순서는 #159 → 이 문서다.
기존 보존·삭제 차단 규칙을 전제로, SQL metadata와 보호 파일의 실패 상태를 함께
판단할 때 필요한 절차를 제안한다. 기존 archive/schema/source, 권한, 보존기간을
변경하거나 실제 archive·복원·삭제·격리·GC를 실행하지 않는다.

현재 controlled task는 [task removal/archive trigger](../../db/task_mode_schema.sql#L39)가
archive·삭제를 먼저 거부한다. [mode trigger](../../db/task_mode_schema.sql#L10)도
controlled → legacy 전환과 mode 행 삭제를 허용하지 않는다.
따라서 아래는 **현재 정상 archive의 회귀나 즉시 데이터 손실 보고가 아니다.**
향후 해당 경로를 활성화하기 전에 해결할 이관·복구 수용조건이다.

## 2. 기존 동작과 새로 검토할 공백

| 코드 근거 | 현재 동작 | 검토할 차이 |
|---|---|---|
| [archive DB snapshot](../../db/task_archives.go#L573) | task/탐색/finding/traffic 등 명시된 테이블을 수집 | revision/operation·E③ 정본·mode fence의 보존 묶음은 현재 포맷에 없음 |
| [archive 파일 포장](../../server/task_archive_package.go#L130) | task 디렉터리와 해당 탐색 transcript를 이동 | 별도 host가 제공하는 descriptor root, E② journal/checkpoint를 함께 포장한다는 계약이 없음 |
| [archive compact](../../db/task_archives_restore.go#L84), [archive purge](../../db/task_archives_restore.go#L764), [task cascade delete](../../db/tasks.go#L647) | finding/intent 노드 또는 task/탐색을 삭제 | [revision FK](../../evidencerevision/schema.sql#L1)는 task/탐색/intent를 참조하며 history/operation은 변경·삭제 불가. finding 0만으로 삭제 가능하지 않음 |
| [descriptor 게시](../../evidencerevision/descriptor_files_linux.go#L158) | pending Write/Sync → no-replace rename → directory Sync/close 후 ACK | 중간 실패·경쟁·ACK 불명 때 pending 또는 final이 남을 수 있으며 보존됨 |
| [정확한 final Load](../../evidencerevision/descriptor_files_linux.go#L251) | 알려진 operation에 해당하는 최종 파일만 보호 root에서 읽음 | 목록·pending 발견/읽기·수리·삭제 API는 없음 |

이 문서는 기존 [보존·삭제 금지 원칙](./e3-storage-gate.md#L182),
[revision FK 한계](./e3-initial-evidence-revision-core.md#L66),
[descriptor 실패 보존](./e3-evidence-revision-descriptor-files.md#L47)을 바꾸지 않는다.
그 원칙만으로 결정되지 않는 실패 상태별 처리와 보존 묶음을 아래에서 검토한다.

## 3. 알려진 operation의 상태표

전제는 실행 당시 보존한 **정확한 비민감 operation ref**다. 파일 이름·mtime·행 수·
모델 출력으로 operation이나 권위를 추측하지 않는다. 파일 상태를 확인할 권위는
실제 host가 별도로 제공해야 한다. 이 표가 directory listing 또는 pending 발견
권한/API를 새로 부여하지 않는다. 목록에 없는 조합은 미확인으로 보존한다.

SQL 조회에는 operation ref만으로 부족하며 **유효하고 정확한 Descriptor**가 필수다.
final을 읽지 못하고 별도로 보호 보존한 정확한 Descriptor 사본도 없으면, 현재 API로
receipt를 조회할 수 없으므로 SQL 상태는 ‘미확인’이다. pending 읽기·operation ref만의
직접 SQL 조회·임의 descriptor 재구성을 대체 경로로 제안하지 않는다.
SQL의 ‘있음’은 이 Descriptor와 현재 실제 ReadReceipt lease로 정확한 binding을 대조한
`Reconcile` 성공을 뜻한다. ‘없음’은 같은 현재 권위/저장소에서 정확 조회가
`ErrNotFound`를 반환한 경우다. 권한 거부·DB/파일 장애·복원 연속성 상실은 ‘미확인’이다.
‘없음’도 과거 commit의 rollback·미실행·영구 부재를 증명하지 않는다.
finding 상태는 별도 권한을 갖춘 정본/link 조회로 확인할 사항이며
`Load/Reconcile`만으로 알 수 없다. ‘저장 확인’도 현재 실행 허가나 새 검증 성공은 아니다.

| ID | 보호 파일 상태 | 정확 SQL receipt | finding 상태 | 제안하는 처리 |
|---|---|---|---|---|
| RRS01 | canonical final 정상 | 있음 | 저장 확인 | 양쪽 operation/binding 및 해당 link의 과거 기록을 보존. 재송신·재발급·현재 권한 승격 없음 |
| RRS02 | final 정상 | 있음 | 미저장 확인 또는 불명 | revision metadata만 성공했을 수 있음. 원래 ref·진단과 저장 불명을 유지하며 finding 성공/완료로 표시하지 않음 |
| RRS03 | final 정상 | 없음 | 미저장 확인 또는 불명 | descriptor ACK 후 Issue 전 중단, rollback, 다른 복원 시점 등을 구별할 추가 근거가 필요. 자동 Issue·새 ref 실행·삭제 없음 |
| RRS04 | final 정상 | 미확인 또는 binding conflict | 어떤 상태든 | 실제 reader/namespace·정확 binding 문제를 해결하기 전 중단. ‘없음’으로 치환하지 않음 |
| RRS05 | pending만 알려짐, final ACK 불명 | 사본 없으면 미확인; 정확한 별도 사본으로 조회한 경우에만 있음/없음 | 미저장 확인 또는 불명 | partial/publish 실패일 수 있으나 추정으로 승격·rename·폐기하지 않음. 기존 Load로 pending을 읽을 수 있다고 안내하지 않음 |
| RRS06 | final 손상·형식/digest 충돌 | 사본 없으면 미확인; 정확한 별도 사본으로 조회한 경우에만 있음/없음 | 어떤 상태든 | 손상 final의 Load 거부를 유지하되, 별도 정확한 Descriptor가 있으면 독립 Reconcile 결과는 별도로 판단. 원본/사유 보존, 덮어쓰기·chmod 수리·다른 digest 대입 없음 |
| RRS07 | 현재 승인된 root에서 final 유실/부재 | 사본 없으면 미확인; 정확한 별도 사본으로 조회한 경우에만 있음/없음 | 어떤 상태든 | SQL 행이 파일을 재구성하거나 신뢰 namespace를 증명하지 않음. 원래 descriptor 사본도 없으면 현재 API의 정확 복구를 보장하지 않음 |
| RRS08 | final과 pending이 함께 알려짐 | 있음·없음·미확인 | 어떤 상태든 | 게시 경쟁의 잔여일 수도 있으므로 공존 자체를 손상/침해로 단정하지 않음. final은 기존 정확 조회만 하고 pending은 별도 합의 전 보존 |

## 4. 명시적 조회와 보존 묶음

1. 실행 당시의 정확 operation ref와 고정 stage 진단을 확인한다. revision operation ref와
   finding 저장의 operation key는 서로 다른 식별자이므로 대체하지 않는다.
2. 실제 host가 현재 `ReadDescriptorFile` scope와 보호 root/장기 anchor를 제공해야 한다.
   `DescriptorFiles.Load(exactOperationRef)`가 거부되면 pending이나 다른 namespace로 우회하지 않는다.
3. 성공한 descriptor도 데이터일 뿐이다. Load 실패 때 별도로 보호 보존한 정확한
   Descriptor 사본이 있는 경우에도 실제 namespace/복원 권위를 추정하지 않는다.
   정확한 Descriptor가 없으면 SQL 조회는 미확인으로 남긴다. 독립된 현재 `ReadReceipt` lease로
   [SQL Reconcile](../../evidencerevision/reconcile.go)을 수행하여 저장된 full Scope,
   task/intent/탐색/mode와 양 digest를 직접 대조한다. 현재 mode 상실과 과거 metadata 조회는
   구분하되 이 조회를 현재 실행·finding 저장 권한으로 사용하지 않는다.
4. receipt를 확인해도 finding 저장·engine 완료를 추정하지 않는다. finding commit이 불명이면
   별도 권한과 원래 finding operation key로 정본/link를 확인해야 한다. 새로운 조회 권한이나
   CLI/API는 이 문서로 추가하지 않는다.
5. 실패·불명은 원래 ref/내용을 유지한다. 새 operation/RunRef·가짜 Collected를 만들어
   Issue하거나 실제 송신을 재실행하지 않는다.

향후 보존/복원 단위는 다음 관계를 함께 검토해야 한다. 현재 일반 archive가 이 묶음을
포함한다고 주장하지 않으며, 실제 이관은 팀 승인과 수용시험 이후 별도 구현한다.

- task/탐색/intent 원래 관계와 mode fence, SQL revision initial/operation 및 canonical binding/digest.
- E③ canonical manifest/verdict/issuance와 finding link·각 operation key/digest. revision만 남은
  RRS02도 보존 대상으로 취급하고, finding 0을 GC 허가로 사용하지 않음.
- 실제 journal·보호 checkpoint와 CPD head/history/receipt의 정확한 참조·digest·phase 및
  사용한 KeyRef/key-version metadata. 로컬 checkpoint만으로 원격 현재 head/epoch를 대신하지 않음.
- 성공한 final descriptor와 이미 알려진 pending/실패 진단의 관계. pending도 승인된 정책 없이
  이동·삭제하지 않으며 완전한 descriptor나 조회 권위로 취급하지 않음.

새 묶음에는 비민감 ref/digest와 필요한 canonical 정본만 포함하는 방향을 제안한다.
**raw key·credential을 task archive에 넣지 않는다.** 실제 키 material의 보존/회수는
별도 키 관리 주체가 담당하며 KeyRef만 남았다고 MAC 재검증 가능성을 약속하지 않는다.
새 metadata 묶음이 기존 traffic·정책/관찰 원문 보존기간을 자동 연장하거나 원문 복사를
허용하지 않는다. 불변 metadata와 민감 원문의 책임·보존 정책은 구분해서 합의한다.

## 5. 복원 및 팀의 미확정 선택

복원 자료의 checksum·서명·같은 UID/0700·같은 경로만으로 현재 권위나 연속성을 증명하지 않는다.
실제 host 공급자가 namespace/배타 writer·장기 anchor·현재 trusted epoch/복원 연속성과
정확한 key-version/reader 주체를 확인해야 한다. 새 빈 DB에서 epoch를 자동 만들거나
다른 namespace에 같은 ref를 재결속하지 않는다. key/source를 확인할 수 없으면 검증 불가를
유지한다. SQL/파일을 서로 다른 복원 시점에서 가져온 경우도 자동 재발급·수리하지 않는다.

| 결정 ID | 팀이 합의할 사항 | 합의 전 경계 |
|---|---|---|
| RRD01 | metadata·원문별 보존기간, 책임자, 비용/용량 한계와 규정상 삭제 요구 처리 | 현행 자동 TTL/GC 없음과 controlled archive/delete 차단 유지 |
| RRD02 | pending 발견·열람·격리·삭제의 실제 주체/권한과 증거 보존 절차 | 목록 API/권한을 추정하지 않고 알려진 operation의 기존 Load/Reconcile만 사용 |
| RRD03 | SQL/descriptor/audit의 일관된 backup·restore 경계 및 실제 namespace/epoch/anchor 승인 주체 | archive를 완전한 복구 자료로 인증하거나 다른 root에 자동 결속하지 않음 |
| RRD04 | 합법적 삭제/이관에서 불변 FK·정본/link·키 관리와 장기 검증 가능성을 유지할 절차 | cascade나 trigger 비활성화로 기존 차단을 우회하지 않음. 일반 삭제/복원 경로를 열지 않음 |

## 6. 계획 수용시험과 이번 문서의 검증 범위

다음은 **계획이며 이번 작업에서 실행하지 않았다.** 실제 권한·복구 정책이 합의되면
전용 환경에서 검증해야 하며, 성공 전 운영 활성화 근거로 사용하지 않는다.

| 시험 ID | 계획하는 경계 | 기대 결과 |
|---|---|---|
| RRT01 | 현재 controlled task의 archive/soft/hard delete와 revision FK | 기존 guard 거부·원자 rollback 유지, 정본/파일 변화 0 |
| RRT02 | Put의 write/Sync/publish/ACK/close 실패와 경쟁 잔여 | final/pending/불명을 구분하여 보존, 부재→rollback 추론·자동 GC 0 |
| RRT03 | 실제 SQL commit ACK 유실·재시작 후 정확 Load/Reconcile | 같은 receipt만 확인, 새 revision/ref·송신/finding 성공 추정 0 |
| RRT04 | Source B 거부 후 revision만 저장된 실행 | finding 0과 metadata 보존 구분, archive/삭제 가능으로 재분류 0 |
| RRT05 | 동일 ref의 다른 namespace/epoch/anchor 또는 SQL·파일 복원 시점 불일치 | 실제 host/정확 binding 검사 거부, 자동 재결속·새 epoch·수리 0 |
| RRT06 | 손상 descriptor·reader/key-version 철회·키 material 부재 | 검증 불가/불명 유지, raw key archive 포함·모델/공개 오류로 유출 0 |
| RRT07 | 향후 승인된 일관 이관·복원 절차와 동시 writer/조회 | 원래 scope/ref/digest·FK/link 보존과 실제 lease 연속성을 검증. 승인 전에는 현행 차단 유지 |

이번 변경은 문서 한 파일과 코드/계약 대조 리뷰, 상대 링크·상태표·중복/구조 검사만 수행한다.
이 검사를 운영 보존·복원·삭제·전원 장애 또는 실제 Juice Shop 시험으로 세지 않는다.
