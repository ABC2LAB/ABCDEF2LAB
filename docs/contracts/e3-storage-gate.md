# E③ 확정 저장 게이트 — 구현 준비 초안

작성: 2026-10-30 · 이동찬 · 최민준님과 협의할 제안

상태: **저장 게이트의 설계 초안. 공통 모드 코어와 실제 DB reader/단방향 등록·DB 통합 시험은 로컬 구현했다. Engine/최초 finding 저장·판정 issuance/link는 아직 연결하지 않았다.**
조사 시작 기준: `feature/dongchan@c6be085`. 준비 중 다른 세션이 PR #32·#33을 병합/동기화해 종료 시 `4cb1bcc`가 됐다. 아래 저장·오라클 경계 소스는 두 커밋 사이 변경이 없음을 재확인했다. `transmit/`·읽기 전용 진입점은 최신 상태를 따르며 별도 E② 서버 연결 소스는 수정하지 않았다.

연결: [순수 오라클](./e3-oracle.md) · [판정 레코드](./verdict-record.md) · [IDOR 엔진 연결](./idor-verifier-seam.md#6-엔진-연결-계약-제안--이동찬-구현용) · [시험 명세](./e3-storage-gate-tests.md)

## 1. 목적과 적용 범위

통제 작업에서 서버가 검증한 **동일 실행·동일 근거의 `CONFIRMED` 판정** 없이 자동 finding 생성/검증 확정을 하지 못하게 한다. LLM의 설명, 도구 승인, HTTP 200, 판정 ID 문자열은 자동 확정 권한이 아니다. 운영자의 수동 triage는 별도 출처로 보존하며 E③ 검증을 대신하지 않는다.

최초 저장과 재검증 저장을 모두 다루지만, 이번 문서는 **초기 수평 IDOR 조회**만 제안한다. `oracle.Evaluate`·`idor.Compare`의 판정 규칙, 팀원의 KG/검증기 코드, 진행 중인 E② 서버·Worker 연결은 변경하지 않는다. 저장을 이유로 `Verify`·`VerifyRead`·HTTP를 다시 실행하지 않는다.

두 상태를 구분한다.

- `exploration_nodes.state='confirmed'`: 탐색 그래프의 상태. 현재 `RecordFindingTx`가 finding 노드를 이 상태로 만든다.
- `findings.status`: `pending`, `confirmed`, `fixed` 등의 triage 상태. 현재 최초 INSERT는 status를 지정하지 않는다. 노드의 confirmed와 같은 필드가 아니다.

게이트는 **자동 finding 생성/검증 승격**을 다룬다. `record_fact`가 만드는 `kind='fact', state='confirmed'`는 취약점 확정이 아니므로 차단 대상에 넣지 않는다. pending finding의 triage만 confirmed로 바꿔 E③ 검증 결과처럼 사용하는 우회도 막아야 한다.

운영자가 기존 finding을 수동 confirmed로 triage하는 것은 허용하는 방향을 제안한다. 서버 인증으로 확인한 actor·reason·`confirmation_source=human`을 별도로 기록하고, E③ 검증 상태/판정 참조는 바꾸지 않는다. 모델이 `human`이라는 문자열을 보내는 것으로 이 경로를 쓸 수 없다. triage confirmed를 평가용 E③ CONFIRMED로 export하지 않으며 그래프 node·triage·E③ 검증을 각각 표시/집계한다. 이 구분을 소비하는 API/export가 준비되지 않으면 통제 모드의 새 수동 confirmed 경로도 활성화하지 않는다.

## 2. 현재 저장 경로 — 확인한 사실

아래 라인은 위 기준 커밋의 참조점이다. 구현 시 최신 대상 브랜치에서 다시 확인한다.

| 경계 | 현재 동작 | 게이트 의무 |
|---|---|---|
| [agent/tools.go:1219](../../agent/tools.go#L1219) `addFinding` | `report_finding` 입력을 받아 recorder 또는 fallback 선택 | 서버 실행 문맥을 사용. recorder 없음/실패 시 통제 finding을 기존 경로로 저장하지 않음 |
| [agent/tools.go:1258](../../agent/tools.go#L1258) fallback | recorder 없고 traffic refs도 없으면 `t.ts.RecordFinding` | 빈 traffic refs가 게이트 우회 사유가 되지 않음 |
| [evidence/store.go:182](../../evidence/store.go#L182) `Store.Record` | 증거 준비 뒤 `db.RecordFindingTx` | 기존 기록기는 finding까지 생성하므로 판정 전 캡처 보관소로 사용하지 않음 |
| [db/finding_traffic.go:342](../../db/finding_traffic.go#L342) `RecordFindingTx` | 노드 confirmed + standalone finding + 증거 연결을 동일 트랜잭션에 생성 | 최하위 저장에서 서버 모드·판정·근거를 검사 |
| [db/exploration.go:197](../../db/exploration.go#L197) `AddNode`, [:268](../../db/exploration.go#L268) `SetNodeState` | 일반 노드 생성·상태 변경 | finding으로 confirmed 생성/승격할 때 같은 불변조건 적용 |
| [db/findings.go:82](../../db/findings.go#L82) `AddFinding` | standalone finding 직접 INSERT, status 미지정 | 통제 finding 직접 생성 우회 금지. 후보는 기존 hint/fact로 보존 |
| [db/findings.go:697](../../db/findings.go#L697) `SetFindingStatus`, [db/notification.go:483](../../db/notification.go#L483) `SetFindingStatusTx` | triage 변경 | 자동 승격은 판정 검증, 수동은 인증된 actor/reason/source 기록. 기존 bare setter는 이 출처 없이 controlled confirmed 변경 금지 |
| [db/findings.go:747](../../db/findings.go#L747) `SetFindingVulnClass` | 유형을 바꾸고 node payload에도 반영 | 기존 판정을 다른 유형의 확정으로 재사용하지 않음 |
| [db/task_archives_restore.go:161](../../db/task_archives_restore.go#L161) `restoreTaskArchive` | 일반 row 복원으로 node/findings 복원 | mode·발급 판정·연결 없는 controlled 복원 금지 |
| [db/finding_retests.go:194](../../db/finding_retests.go#L194) `RecordFindingRetestResult` | conversation에 문자열 결론 저장 | server-owned execution·finding·판정 연결 검증 |
| [db/finding_retests.go:221](../../db/finding_retests.go#L221) `FinishFindingRetest` | completed+fixed일 때 triage 갱신 | stopped/failed·미확인 결과가 fixed로 승격되지 않음 |

현재 코드에는 이 문서의 구조화 판정 발급/저장 게이트가 없다. `AddFinding`의 존재 자체를 confirmed INSERT라고 표현하지 않는다. 그 행의 후속 승격까지 조사·검증한다.

## 3. 신뢰하는 입력과 새 연결 정보

아래 이름은 **논리 항목**이며 아직 확정된 Go 타입·DB 열·외부 JSON API가 아니다. 기존 ARTEX 인터페이스를 통째로 교체하지 않는다. `OracleVerdict`와 `idor.Result`만으로는 실행 출처와 증거 연결을 증명할 수 없다.

| 항목 | 공급자·검증 의무 |
|---|---|
| 작업·의도·대상 exploration/finding | 서버가 실제 문맥에서 확인. 모델이 준 task/intent ID로 대체하지 않음 |
| 검증 execution 참조·case 참조 | 서버 발급. E② `transmit.Scope.RunRef`는 실행 참조이며 평가 envelope의 `run_id`와 별개 |
| reset 세대 | owner/cross 관찰·영수증·정책·판정이 같은 대상 세대에 속함 |
| 실행 승인 참조 | E② `Scope.PolicyRef` 등 **검사를 실행할 허가**. KG 기대권한 정책 버전으로 사용하지 않음 |
| KG 기대권한 정책 버전·유효창 | 정본 reader의 snapshot. 공유·위임 해소 상태 포함. 두 관찰과 판정이 같은 snapshot을 사용 |
| 계정·세션 참조 | owner/evaluated의 등록된 비밀이 아닌 참조와 검증 상태/버전. LLM이 선언한 verified를 신뢰하지 않음 |
| 자원·검사 유형 | 전체 resource ref와 지원하는 수평 조회 유형. 부분 ID·제목 매칭 금지 |
| 실행 근거 revision·manifest digest | 서버가 동결한 비민감 관찰/영수증 참조의 버전·내용 연결. 현재 `findings.evidence_version`과 별개이며 저장 시 명시적으로 연결 |
| 영수증·관찰 시각·비교 신호 | 서버가 해당 실행에서 수집. owner 정상 대조와 cross 응답의 동일 자원·표식·반사/공통 템플릿 배제를 확인 |
| 구조화 판정·발급자 | 신뢰 서버가 순수 `oracle.Evaluate`로 발급하고 변경 불가로 저장. caller가 조립한 `Value=confirmed`는 받지 않음 |
| 저장 연산 식별자 | 서버의 작업+execution+case+근거 revision+저장 목적에서 결정. 재시도 중 바꾸지 않음 |

판정 ID를 입력받더라도 서버 저장소에서 정본을 읽어 위 항목을 대조해야 한다. ID가 존재하거나 해시가 같다는 사실만으로 발급 권한·참조 소유권이 증명되지 않는다. digest 입력에도 인증/본문/표식 값·계정 원문을 넣지 않는다.

**논리 계약 확장 제안:** 기존 `verdict-record`의 oracle/평가 필드는 유지하고, 별도 불변 **발급 레코드(issuance)**에 위 binding·발급자·실행 상태·근거 manifest 참조를 둔다. 판정 레코드와 issuance는 서버의 같은 transaction에서 저장하고, issuance마다 하나의 정본 판정만 참조한다. 이것은 기존 계약에 없는 provenance를 보완하는 새 연결 계약이며 최민준님과 합의 전이다. 기존 계약만으로 이미 충족한다고 표현하지 않는다.

물리 저장의 판정/issuance 본문에는 변경 가능한 `finding_id`를 넣지 않고 별도 link 관계를 정본으로 삼는다. `verdict-record.finding_id`는 조회/export 시 link에서 투영하는 nullable 값이다. link 생성 전 NULL, 생성 후 해당 ID이며 판정 본문을 수정하지 않는다. 같은 issuance/판정의 최초 finding 연결은 최대 하나다. 사람이 triage했다는 이유로 issuance를 발급하지 않는다.

KG snapshot은 owner/cross 실행 전에 ID·version·내용 digest를 동결하고 두 관찰과 발급에 같은 객체를 사용한다. 실행 중/관찰 뒤 최신 KG가 바뀌어도 기존 동결 관찰은 그 snapshot으로 역사적으로 판정한다. reader가 같은 version에 다른 내용이나 서로 다른 snapshot을 반환하면 섞지 않고 거부한다. 최신 상태로 재검사하려면 새 execution이다.

`CONFIRMED`에서는 정상 완료된 해당 owner/cross 영수증·관찰·필수 감사 기록을 확인한다. intent 기록만 있음, 승인만 있음, 부분 송신/응답, 다른 시도의 성공 영수증은 필요한 근거를 대신하지 못한다. 감사 close/checkpoint의 성공 증명이 필요한 시점을 실행 어댑터와 맞추고, 미완료 상태를 완결 판정으로 발행하지 않는다.

## 4. 통제 작업 판별 — 선행 조건

게이트가 읽을 서버 전용 DB 메타데이터는 `controlled / explicit_legacy / unknown`을 구분해야 한다. 모델 입력·worker 이름·origin 문자열·caller의 `controlled=false`는 모드 근거가 아니다.

진행 중인 E② 서버 구현은 **private 파일 marker**로 통제 작업을 기억한다. 현재 DB에 같은 정본이 있다고 가정하면 안 된다. 저장 게이트 도입 전에 다음 연결이 필요하다.

1. task row를 잠그는 등록/저장 프로토콜을 맞춘다. 기존 `LockTaskEvidenceTx`의 task 잠금과 충돌하지 않는 순서를 사용한다.
2. **실행 admission과 저장 모두 같은 DB mode fence를 읽는다.** 통제 등록은 이 controlled fence를 **먼저 커밋**한 뒤 파일 marker/snapshot을 등록한다. DB 성공 뒤 파일 생성 실패·재시작에서도 실행이 일반 Worker로 강등되지 않도록 E② admission 쪽 reader도 함께 연결해야 한다. 중간 실패는 controlled/차단 상태를 남기고 legacy로 되돌리지 않는다. private marker는 보조 증거이지 별도의 legacy 판정 정본이 아니다.
3. 기존 파일 marker가 있는 작업을 DB에 이관한다. DB 정보 부재·파일 확인 실패·양쪽 모순은 unknown/차단이며, 전체 기존 task를 일괄 legacy로 backfill하지 않는다.
4. explicit_legacy는 서버가 분류한 기존 일반 작업에만 허용한다. controlled 전환은 단방향이며 모델·일반 API가 모드나 연결을 지우지 못하게 한다.
5. DB 최하위 확정 저장은 같은 task 잠금 아래 모드를 다시 읽는다. task/exploration/node에서 찾은 실제 소유 문맥과 caller 입력이 다르거나 task ID를 0으로 비워도 허용하지 않는다.

**활성화 조건:** DB fence·실행/저장 공동 reader·기존 marker 이관·등록/저장 경합 시험이 끝나기 전 저장 게이트를 연결 완료로 선언하지 않는다. DB mode reader 미설정/오류는 실행과 저장 양쪽에서 unknown→deny다. 이번 문서는 진행 중인 서버 파일을 수정하지 않는다.

### 4.1 공통 판별 코어의 첫 구현 초안

[`taskmode.Decide`](../../taskmode/mode.go)는 서버가 공급한 `Record{TaskID, Mode, Version}`와 `Marker{TaskID, State}`를 검사하는 순수 함수다. `Version`은 양의 mode fence revision이며 실제 DB 열/등록 방식은 아직 정하지 않았다. marker의 clear는 **부재를 정상 확인한 값**이며 조회 실패를 false/clear로 변환하면 안 된다.

| 정본 | marker | 판별 결과 |
|---|---|---|
| 명시적 legacy, 유효 revision·같은 task | clear | Legacy 경로 |
| controlled, 유효 revision·같은 task | present | Controlled 경로 |
| controlled | clear | Block: 등록 미완료, 일반 경로로 강등 금지 |
| legacy | present | Block: mode/marker 충돌 |
| 정본 없음·unknown·잘못된 enum/revision/task | 어떤 값이든 | Block |
| 유효 정본 | unknown·잘못된 marker/task | Block |

`taskmode.Resolve(ctx, taskID, reader)`는 같은 판별을 실행/저장 경계에서 재사용하도록 reader를 매번 호출한다. nil reader·조회 오류·잘못된 task·취소는 Block과 오류를 반환한다. 읽기는 성공했지만 정책상 차단되면 error가 nil일 수도 있으므로 **호출자는 반드시 Decision.Route를 검사**한다. zero Decision은 Block이다. Decision은 외부에서 허용 필드를 채워 만들 수 없고 결과를 다음 실행/저장에 캐시해서도 안 된다.

reader 오류는 일반 문자열/JSON에 원문을 내보내지 않는 형태로 반환하고 `errors.Is/As`로 서버 진단용 원인을 보존한다. 원인 Unwrap/As 결과를 모델/로그에 보내면 안 된다. enum 사유와 허용된 metadata만 사용한다.

**분류와 권한의 구분:** Controlled 결과는 E②/E③ 통제 경로 선택일 뿐 세션·실행 허가·예산·증거 검증을 통과했다는 뜻이 아니다. Legacy도 다른 기존 실행/저장 승인 조건을 대체하지 않는다.

**DB 후속 구현:** [`RegisterTaskMode/TaskModeReader`](./task-mode-db.md)를 추가해 실제 PostgreSQL 정본·단방향 전이·task 잠금·오류 차단을 시험했다. mode 테이블의 구체 형식은 검토용 초안이다. 아직 실제 marker 파일 등록·이관과 Engine/최하위 finding 저장은 연결하지 않았다. caller는 reader의 transaction을 admission/write 완료까지 유지해야 한다. 이 slice를 실제 송신/확정 저장 강제 완료로 표현하지 않는다.

**초안 검증:** Go 1.26.3에서 `./taskmode`의 최하위 단위 사례 37개와 race 37개가 통과했고 statement coverage 100%·vet/build·gofmt를 확인했다. 독립 코드 리뷰는 actionable 0건이다. 기존 실행/저장 소스와 다른 세션의 파일은 수정하지 않았으며 팀 합의·실제 DB/엔진 배선 완료를 의미하지 않는다. 피드백은 판별 진리표, Reader의 transaction/잠금 책임, mode fence revision 입력부터 받는다.

## 5. 저장 프로토콜 제안

### 5.1 검사와 판정 발급

초기 HTTP 조회는 최신 seam 계약대로 `tx.VerifyRead(candidate, kg)`를 통해 실행한다. 이 경로가 등록 신원/허가와 KG/응답 투영 표식의 일치를 검사하고 고정 resolver로 `idor.VerifyRead`를 호출한다. 독립 KG를 넣은 `idor.VerifyRead(candidate, tx, kg)` 직접 호출을 HTTP 확정 경로로 사용하지 않는다.

엔진이 조회를 수행한 뒤 서버가 실행별 근거 묶음을 동결한다. 정본 정책·신원·동일 세대/근거 연결을 확인하고 기존 결과/`Compare`·`Evaluate`를 사용한다. 판정 저장을 위해 `tx.VerifyRead`를 재실행하지 않는다. 순수 판정에 없는 adapter 오류 사유도 별도로 보존한다.

판정 내용과 발급 근거는 변경 불가로 저장하고 finding과의 연결은 별도 관계로 둔다(§3의 issuance 확장/조회 projection). 수정 판정은 새 레코드/revision이며 이전 판정을 덮어쓰지 않는다. `SUSPECTED`·`UNDECIDABLE`·`ALLOWED`도 구조화 기록으로 남길 수 있다.

### 5.2 최초 확정 저장

**통제 전용 저장 경로**를 작은 새 경계로 추가하는 제안이다. `RecordControlledFindingTx`는 가칭이며 구현된 API가 아니다. 기존 generic API는 하위에서 controlled finding 저장을 명시적으로 거부하고, 모든 기존 API에 새 판정 필드를 추가하는 것은 피한다. 통제 전용 최하위 저장 트랜잭션은 다음을 함께 검사/커밋한다.

1. task 소유권·서버 모드·archive 상태 확인. 통제/unknown에서 기존 fallback 금지.
2. 서버가 고른 정본 판정과 근거 묶음을 읽고 잠금/불변성 확인.
3. 같은 작업·의도·execution·case·계정·자원·reset·승인·KG 버전·근거 revision/digest인지 대조.
4. 지원 유형의 정상 발급된 `CONFIRMED`, 필수 체크/비교 신호와 영수증 연결, 수명주기 완료를 확인. 역사적 정책의 유효성 기준은 **관찰 시각**이며 단순히 저장 시각에 만료되었다는 이유로 판정을 바꾸지 않음.
5. 서버 저장 연산 키의 동일 재시도는 기존 finding/node ID 반환. 같은 키에 다른 불변 입력, 다른 finding에 이미 연결된 판정은 conflict.
6. finding node·standalone finding·판정 연결·근거 버전을 원자적으로 저장. 중간 오류는 모두 rollback. 동일 판정으로 최초 finding을 2개 만들지 못하도록 DB 유일성으로 보강.

초기 standalone triage는 ARTEX 기존 pending 기본값을 유지한다. node confirmed·E③ 검증 연결과 사람이 관리하는 triage를 따로 둔다. 자동 triage confirmed 승격은 동일 issuance에 근거해야 하고, 수동 승격은 §1의 actor/reason/source를 사용하며 E③ 검증 상태를 바꾸지 않는다. notification outbox는 기존 의미를 유지하며 이를 E② 필수 감사 기록으로 대신하지 않는다.

### 5.3 일반 API·근거 수정

- `AddNode(finding, confirmed)`, `SetNodeState`, `AddFinding` 같은 generic 경로는 통제 finding의 생성/자동 확정을 명시적으로 거부하고 전용 저장 경로를 사용한다. 기존 bare status setter도 controlled confirmed 변경을 거부하고 서버의 자동/수동 출처가 검증된 경로만 사용한다. hint/fact의 일반 기록은 유지한다.
- 연결된 통제 finding의 근거를 `Store.Bind`, `EditFindingTraffic` 또는 evidence 갱신으로 바꾸면서 이전 판정을 유효하게 유지하지 못한다. **첫 구현은 이런 변경을 거부**한다. 새 근거 revision/판정을 만든 뒤 동일 트랜잭션에서 연결을 교체하는 후속 절차를 별도로 제공한다.
- `SetFindingVulnClass`로 판정 의미를 바꾸는 것도 초기 범위에서는 거부한다. name/severity 등의 표시·triage metadata와, vulnclass/resource/evidence처럼 판정에 영향을 주는 항목을 구분한다.
- 기록의 task/node 연결을 비우거나 다른 exploration으로 옮겨 모드를 우회하지 못한다. task 삭제 뒤 남는 standalone finding도 출처/모드 fence를 보존해야 한다. task에 cascade 삭제되지 않는 issuance/불변 provenance로 과거 확인 근거를 남기고, `findings.task_id/node_id`가 NULL이 되어도 이를 legacy로 재분류하지 않는다. 이 보존을 구현하기 전 controlled task/finding 삭제는 명시 거부한다.
- **초기 controlled archive/restore는 mutation 전에 거부**한다. 판정·연결·근거 revision·mode fence까지 snapshot/복원하고 digest/count 검증·원자성·유일성을 입증하는 별도 변경 전에는 일반 row restore를 사용하지 않는다. 구버전 archive의 mode를 legacy로 추정하지 않고 unknown으로 격리한다. explicit_legacy archive의 기존 회귀는 유지한다. [findings.go:429](../../db/findings.go#L429)의 INSERT는 후속 intent 생성이므로 finding 생성 우회로 분류하지 않는다.
- 게이트는 이 애플리케이션의 쓰기 경로를 대상으로 한다. DB 관리자/같은 DB 자격증명을 가진 임의 SQL 쓰기까지 막으려면 별도 역할/권한 구성이 필요하며 이 문서가 이를 구현했다고 주장하지 않는다.

### 5.4 잠금 순서

기존 `WithEvidenceTx`는 evidence advisory lock→task→finding 순서를 사용하지만 `FinishFindingRetest`는 finding→retest 순서다. 새 모드 검증을 일부 경로에만 추가하면 잠금 역전이 생길 수 있다.

확정 저장에 참여하는 경로의 제안 순서를 **evidence advisory lock → task/mode fence → 실행/판정 발급 행 → 기존 finding → retest/link/evidence**로 통일한다. 최초 저장·status 변경·bind/edit·retest 종료·삭제와 archive/restore의 허용 여부 확인에 함께 적용한다. 등록은 task fence를 커밋한 뒤 파일 처리를 하며 task를 잡은 채 역순으로 evidence advisory lock을 기다리지 않는다.

task ID를 찾으려고 finding을 먼저 읽어야 하면 잠그지 않고 찾은 뒤, task 잠금 후 finding을 잠그며 소유 관계를 재확인한다. task/finding의 다중 잠금이 필요하면 같은 종류의 행은 ID 오름차순으로 잠근다. 실제 기존 경로 전체와 lock order를 맞추고 DB 교차 transaction 시험을 통과하기 전 활성화하지 않는다. `go test -race`는 DB deadlock 검증을 대신하지 않는다.

## 6. 판정·실패 처리

| 결과 | 구조화 기록 | finding 확정 |
|---|---|---|
| 검증된 `CONFIRMED` + 모든 연결 일치 | 유지 | 게이트 통과 가능 |
| `SUSPECTED` | 충족/부족한 조건 유지 | 금지 |
| `UNDECIDABLE`·미지원·실행/증거 미확인 | 판정/adapter 사유 유지 | 금지 |
| `ALLOWED` | 원값 유지, nullable 취약점 판정/정책 결과는 기존 계약과 정렬 | 금지. 취약점 없음/접근 성공/fixed로 번역 금지 |
| 입력 출처 불일치·record 없음·위조·중복 충돌 | 안정적인 저장 거부 사유, 원문 제외 | 금지 |
| DB/판정기/필수 감사 장애 | 인프라 오류. 재시도 가능한 것과 영구 거부를 구분 | 금지. 오류를 저장된 UNDECIDABLE/성공으로 꾸미지 않음 |

거부 사유의 제안 범주는 `mode_unknown`, `record_missing`, `binding_mismatch`, `evidence_incomplete`, `not_confirmed`, `save_conflict`, `store_unavailable`다. 최종 Go 이름은 구현 시 정하고 raw DB 오류/자격값을 모델 출력에 넣지 않는다. 거부 사실도 필수 저장소 장애로 보존되지 않았다면 기록 성공으로 보고하지 않는다.

## 7. 재검증

server-owned conversation→retest→finding→원래 task와 새 검증 execution을 연결한다. 재검증을 최초 finding과 같은 의미의 새 finding 생성으로 처리하지 않는다.

- 해당 finding의 같은 증거에 대한 검증된 `CONFIRMED`는 `reproduced`로 연결할 수 있다. 별도 판정 참조와 실행 연결이 필요하다.
- `ALLOWED`·실행 차단·응답 미확인·근거 부족을 자동 `fixed`로 변환하지 않는다. 초기 조회 게이트는 fixed를 증명하는 별도 규칙을 구현하지 않으며, 이 경로는 `inconclusive`와 사유를 사용한다.
- `SUSPECTED`·`UNDECIDABLE`은 `inconclusive`에 연결하며, 인프라 저장 실패는 에러/실행 실패로 노출한다.
- staged 결론이 있어도 stopped/failed가 completed보다 우선한다. 동일 저장 재시도는 같은 결과를 사용하며 송신을 반복하지 않는다. 명시적 legacy 재검증의 기존 fixed 흐름은 별도이며 통제 작업을 legacy로 보내지 않는다.

## 8. 구현 순서·소유 경계

1. 이동찬↔최민준: 발급자/판정·retest 매핑·저장 논리 필드 합의. 경준님: 정본 KG 버전과 신원/자원 참조 합의.
2. 이동찬: 실제 최신 `transmit/`·`VerifyRead` 기준으로 입력을 맞추고, E② 담당 세션과 DB mode fence/marker 이관 프로토콜 정렬. 이 checkout의 미커밋 `transmission/`을 새 엔진 표준으로 가정하지 않음.
3. 전용 저장·판정/issuance·link·유일성·일관된 잠금 순서를 구현하고 직접 호출/경합/rollback 시험. generic API는 controlled 경로를 거부한다. DB 테스트가 skip이면 완료 아님.
4. recorder/fallback·일반 삽입/승격·근거 변경·retest 경계를 차례로 연결. 저장 코드에서 새 HTTP 요청 0건을 확인.
5. owned synthetic fixture로 owner/cross→판정→최초 저장·재검증까지 확인. 모델·실 대상 baseline 없이 검증 가능.

앞선 준비 뒤 §4.1의 공통 모드 코어와 별도 DB mode reader/등록·스키마를 로컬 초안으로 추가했다. 판정 issuance/link 저장 구조, E② 등록 API, KG·검증기 코드는 고치지 않는다. 현재 통제 Worker에 `report_finding`이 없다는 방어도 게이트 완성 전에 해제하지 않는다.

## 9. 남은 협의

- 판정/연결 테이블과 write 권한·FK·archive/delete 처리의 구체 설계(이동찬·최민준).
- 서버 execution 근거 manifest와 E② 감사 완료/관찰 참조의 생성·동결 시점(이동찬, 진행 중인 E② 세션).
- private marker와 DB mode fence의 이관·재시작·등록 경합 처리(이동찬).
- KG 버전/정본 snapshot 제공, 허용 결과 저장의 이름(최민준·이경준). 기존 `oracle` 의미는 유지.
- 수직 IDOR·상태변경·fixed 입증·근거 revision 교체는 초기 조회 게이트 이후 범위.
