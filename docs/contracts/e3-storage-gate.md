# E③ 확정 저장 게이트 — 구현 준비 초안

작성: 2026-10-30 · 수정: 2026-11-03 · 이동찬 · 계약 리뷰 반영·물리 저장안 검토

상태: **재검증 매핑·저장 불변식의 리뷰 합의와 발급 레코드 보완 요청을 반영한 문서 검토본이다. 판정 레코드는 최민준님의 계약 영역이다.** [리뷰 코멘트](https://github.com/ABC2LAB/ABCDEF2LAB/pull/48#issuecomment-6524511615)의 두 보완 항목(하네스 발급 세대 토큰·전체 `resource_ref`)을 §3에 명시했다. 문서 최종 확인 뒤 병합을 검토하며, 잠금 순서의 구현·경합 검증은 이동찬의 엔진 책임이다. 사람 수동 확정은 §1.1의 세 조건을 유지한다. 공통 모드 코어와 DB reader·단방향 등록은 별도 PR #43의 범위이며, Engine/최초 finding 저장·판정 issuance/link는 아직 연결하지 않았다.

연결: [순수 오라클](./e3-oracle.md) · [판정 레코드](./verdict-record.md) · [IDOR 엔진 연결](./idor-verifier-seam.md#6-엔진-연결-계약-제안--이동찬-구현용) · [시험 명세](./e3-storage-gate-tests.md)

## 1. 목적과 적용 범위

통제 작업에서 서버가 검증한 **동일 실행·동일 근거의 `CONFIRMED` 판정** 없이 자동 finding 생성/검증 확정을 하지 못하게 한다. LLM의 설명, 도구 승인, HTTP 200, 판정 ID 문자열은 자동 확정 권한이 아니다. 운영자의 수동 triage는 별도 출처로 보존하며 E③ 검증을 대신하지 않는다.

최초 저장과 재검증 저장을 모두 다루지만, 이번 문서는 **초기 수평 IDOR 조회**만 제안한다. `oracle.Evaluate`·`idor.Compare`의 판정 규칙, 팀원의 KG/검증기 코드, 진행 중인 E② 서버·Worker 연결은 변경하지 않는다. 저장을 이유로 `Verify`·`VerifyRead`·HTTP를 다시 실행하지 않는다.

두 상태를 구분한다.

- `exploration_nodes.state='confirmed'`: 탐색 그래프의 상태. 현재 `RecordFindingTx`가 finding 노드를 이 상태로 만든다.
- `findings.status`: `pending`, `confirmed`, `fixed` 등의 triage 상태. 현재 최초 INSERT는 status를 지정하지 않는다. 노드의 confirmed와 같은 필드가 아니다.

게이트는 **자동 finding 생성/검증 승격**을 다룬다. `record_fact`가 만드는 `kind='fact', state='confirmed'`는 취약점 확정이 아니므로 차단 대상에 넣지 않는다. pending finding의 triage만 confirmed로 바꿔 E③ 검증 결과처럼 사용하는 우회도 막아야 한다.

### 1.1 사람 수동 확정의 세 조건

운영자가 기존 finding을 수동 확정하는 경로는 다음 세 조건을 모두 만족하는 경우에 한해 제안한다. 아래는 저장·집계의 논리 요구사항이며, 필드명·타입·물리 저장 형식은 최민준님과 확인 후 확정한다.

1. **별도 카테고리 저장:** 오라클의 증거 기반 `CONFIRMED`와 구별되는 **사람 수동 확정** 카테고리로 저장한다. `findings.status='confirmed'`만으로 두 결과를 합치지 않는다. 수동 확정은 기존 E③ 검증 상태·판정 참조를 바꾸거나 oracle 판정/issuance를 새로 발급하지 않는다. `confirmation_source=human`은 출처 표기의 후보이며, 모델이 그 문자열을 보내는 것만으로 수동 권한을 얻지 못한다.
2. **자동 매트릭스 집계 제외:** [평가 프로토콜 §5](../evaluation/evaluation-protocol.md#5-측정-매트릭스-값은-비움-틀만)의 자동 매트릭스 `CONFIRMED` 집계(확정 수)에 사람 수동 확정을 넣지 않는다. API/export·평가 어댑터는 서버가 검증한 저장 카테고리·출처를 확인하고, triage confirmed를 oracle confirmed로 변환하지 않는다. 수동 확정만 추가해도 자동 확정 수·확정까지 시간과 기존 판정 칸·GT 분모는 바뀌지 않아야 한다. 기존 자동 판정은 프로토콜 §4를 그대로 따르고, 수동 내역은 평가 export에서도 별도 카테고리의 **건수만** 보고하며, 확정·의심·판단불가·오탐 등 어느 판정 지표·분모에도 포함하지 않는다. oracle 판정이 없는 finding을 수동 확정으로 보충하지 않는다. 출처를 확인하지 못한 기록은 자동 CONFIRMED 입력으로 승격하지 않는다.
3. **승인자·시각·근거 로그 필수:** 서버 인증으로 확인한 승인자의 비민감 참조, 서버가 기록한 승인 시각, 판단 사유와 검토 근거의 참조를 필수 로그로 보존한다. 근거 로그에는 원문·토큰·계정 값을 넣지 않는다. 승인자·시각·근거 중 하나가 누락/위조됐거나 필수 로그 저장이 실패하면 수동 확정을 거부한다. 수동 카테고리 기록·triage 변경·필수 로그는 같은 트랜잭션에서 원자적으로 저장해 부분 성공을 남기지 않는다.

저장·API/export·평가 어댑터에서 세 조건을 검증하기 전에는 이 수동 경로를 활성화하지 않는다. 문서의 계약 합의와 실제 수동 경로·저장 게이트의 구현/시험 완료는 별개다. 합의 반영과 남은 구현 협의는 §9에서 구분한다.

## 2. 현재 저장 경로 — 확인한 사실

아래 소스 위치는 설계 조사를 위한 참조점이다. 구현 시 최신 대상 브랜치에서 다시 확인한다.

| 경계 | 현재 동작 | 게이트 의무 |
|---|---|---|
| [agent/tools.go:1219](../../agent/tools.go#L1219) `addFinding` | `report_finding` 입력을 받아 recorder 또는 fallback 선택 | 서버 실행 문맥을 사용. recorder 없음/실패 시 통제 finding을 기존 경로로 저장하지 않음 |
| [agent/tools.go:1258](../../agent/tools.go#L1258) fallback | recorder 없고 traffic refs도 없으면 `t.ts.RecordFinding` | 빈 traffic refs가 게이트 우회 사유가 되지 않음 |
| [evidence/store.go:182](../../evidence/store.go#L182) `Store.Record` | 증거 준비 뒤 `db.RecordFindingTx` | 기존 기록기는 finding까지 생성하므로 판정 전 캡처 보관소로 사용하지 않음 |
| [db/finding_traffic.go:342](../../db/finding_traffic.go#L342) `RecordFindingTx` | 노드 confirmed + standalone finding + 증거 연결을 동일 트랜잭션에 생성 | 최하위 저장에서 서버 모드·판정·근거를 검사 |
| [db/exploration.go:197](../../db/exploration.go#L197) `AddNode`, [:268](../../db/exploration.go#L268) `SetNodeState` | 일반 노드 생성·상태 변경 | finding으로 confirmed 생성/승격할 때 같은 불변조건 적용 |
| [db/findings.go:82](../../db/findings.go#L82) `AddFinding` | standalone finding 직접 INSERT, status 미지정 | 통제 finding 직접 생성 우회 금지. 후보는 기존 hint/fact로 보존 |
| [db/findings.go:697](../../db/findings.go#L697) `SetFindingStatus`, [db/notification.go:483](../../db/notification.go#L483) `SetFindingStatusTx` | triage 변경 | 자동 승격은 판정 검증, 수동은 §1.1의 별도 카테고리·집계 제외·필수 승인 로그 적용. 기존 bare setter는 출처 없이 controlled confirmed 변경 금지 |
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
| 하네스 발급 세대 토큰 | [canary fixture §2](./idor-canary-fixture.md)의 하네스가 실제 reset 성공·헬스 확인 뒤 발급한 불투명 식별자. `(run_id, reset_generation)`·대상 인스턴스의 reset 완료 근거와 불변 결속하며, fixture→KG 등록→캡처→판정/issuance가 같은 토큰과 결속을 보존·대조 |
| 실행 승인 참조 | E② `Scope.PolicyRef` 등 **검사를 실행할 허가**. KG 기대권한 정책 버전으로 사용하지 않음 |
| KG 기대권한 정책 버전·유효창 | 정본 reader의 snapshot. 공유·위임 해소 상태 포함. 두 관찰과 판정이 같은 snapshot을 사용 |
| 계정·세션 참조 | owner/evaluated의 등록된 비밀이 아닌 참조와 검증 상태/버전. LLM이 선언한 verified를 신뢰하지 않음 |
| `resource_ref`·검사 유형 | [seam §1](./idor-verifier-seam.md#1-resource_ref-규약--typeid-임시확정--민준경준)의 `type:id` 형식. **마지막 콜론** 앞이 `type`, 뒤가 `id`이며 type에는 콜론이 들어갈 수 있으므로 첫 콜론으로 나누지 않는다. 전체 참조로 소유·기대 권한·캡처·판정 연결을 대조. 지원하는 수평 조회 유형만 허용하며 부분 ID·제목 매칭 금지 |
| 실행 근거 revision·manifest digest | 서버가 동결한 비민감 관찰/영수증 참조의 버전·내용 연결. 현재 `findings.evidence_version`과 별개이며 저장 시 명시적으로 연결 |
| 영수증·관찰 시각·비교 신호 | 서버가 해당 실행에서 수집. owner 정상 대조와 cross 응답의 동일 자원·표식·반사/공통 템플릿 배제를 확인 |
| 구조화 판정·발급자 | 신뢰 서버가 순수 `oracle.Evaluate`로 발급하고 변경 불가로 저장. caller가 조립한 `Value=confirmed`는 받지 않음 |
| 저장 연산 식별자 | 서버의 작업+execution+case+근거 revision+저장 목적에서 결정. 재시도 중 바꾸지 않음 |

판정 ID를 입력받더라도 서버 저장소에서 정본을 읽어 위 항목을 대조해야 한다. ID가 존재하거나 해시가 같다는 사실만으로 발급 권한·참조 소유권이 증명되지 않는다. digest 입력에도 인증/본문/표식 값·계정 원문을 넣지 않는다.

**발급 레코드의 논리 계약:** 기존 `verdict-record`의 oracle/평가 필드는 유지하고, 별도 불변 **발급 레코드(issuance)**에 위 binding·발급자·실행 상태·근거 manifest 참조를 둔다. 판정 레코드와 issuance는 서버의 같은 transaction에서 저장하고, issuance마다 하나의 정본 판정만 참조한다. 리뷰에서 확인된 불변식과 요청된 세대 토큰·`resource_ref` 보완을 반영한 계약이며, 구체 Go 타입·DB 열·메타데이터 API와 실제 배선은 후속이다. 기존 계약/코드만으로 이미 충족한다고 표현하지 않는다.

세대 토큰은 `run_id`나 `reset_generation` 자체가 아니며 어느 하나를 대체·추론하는 데 쓰지 않는다. 같은 세대·같은 불변 준비 묶음의 재시도는 같은 토큰을 재사용하고, 새 reset은 새 세대·새 토큰을 사용한다. KG는 결속을 저장하며 발급·재결속하지 않는다. 서버는 fixture→KG 등록→캡처→판정/issuance의 토큰·회차·reset 세대·인스턴스 결속을 함께 검증한다. 누락·출처 불명·어느 단계의 불일치도 현재 실행의 근거로 재사용하지 않으며, 판정 전에는 판단불가, 확정 저장에서는 연결 불일치로 거부한다. 토큰을 캡처 본문·canary 값에 삽입하지 않고 서버 메타데이터로 전달한다. `resource_ref` 역시 전체 `type:id`를 유지하며 같은 `id`만 일치하는 다른 자원의 근거로 대체하지 않는다.

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

`taskmode.Decide`는 서버가 공급한 `Record{TaskID, Mode, Version}`와 `Marker{TaskID, State}`를 검사하는 순수 함수다. `Version`은 양의 mode fence revision이다. `task_control_modes` 테이블과 등록 API는 별도 [PR #43](https://github.com/ABC2LAB/ABCDEF2LAB/pull/43)에서 구현해 `3c454a2`로 dev에 병합했다. marker의 clear는 **부재를 정상 확인한 값**이며 조회 실패를 false/clear로 변환하면 안 된다.

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

**별도 DB 구현([PR #43](https://github.com/ABC2LAB/ABCDEF2LAB/pull/43)):** `RegisterTaskMode/TaskModeReader`로 PostgreSQL 정본·단방향 전이·task 잠금·오류 차단을 시험했다. mode 테이블의 구체 형식은 검토용 초안이다. 아직 실제 marker 파일 등록·이관과 Engine/최하위 finding 저장은 연결하지 않았다. caller는 reader의 transaction을 admission/write 완료까지 유지해야 한다. 이 범위를 실제 송신/확정 저장 강제 완료로 표현하지 않는다.

**별도 구현 검증:** PR #43의 `./taskmode` 최하위 단위 사례 37개와 race 37개가 Go 1.26.3에서 통과했고 statement coverage 100%·vet/build·gofmt를 확인했다. 이는 팀 합의나 실제 서버 admission/저장 배선 완료를 의미하지 않는다. 피드백은 판별 진리표, Reader의 transaction/잠금 책임, mode fence revision 입력부터 받는다.

## 5. 저장 프로토콜 제안

### 5.1 검사와 판정 발급

초기 HTTP 조회는 최신 seam 계약대로 `tx.VerifyRead(candidate, kg)`를 통해 실행한다. 이 경로가 등록 신원/허가와 KG/응답 투영 표식의 일치를 검사하고 고정 resolver로 `idor.VerifyRead`를 호출한다. 독립 KG를 넣은 `idor.VerifyRead(candidate, tx, kg)` 직접 호출을 HTTP 확정 경로로 사용하지 않는다.

엔진이 조회를 수행한 뒤 서버가 실행별 근거 묶음을 동결한다. 정본 정책·신원·동일 세대/근거 연결을 확인하고 기존 결과/`Compare`·`Evaluate`를 사용한다. 판정 저장을 위해 `tx.VerifyRead`를 재실행하지 않는다. 순수 판정에 없는 adapter 오류 사유도 별도로 보존한다.

판정 내용과 발급 근거는 변경 불가로 저장하고 finding과의 연결은 별도 관계로 둔다(§3의 issuance 확장/조회 projection). 수정 판정은 새 레코드/revision이며 이전 판정을 덮어쓰지 않는다. `SUSPECTED`·`UNDECIDABLE`·`ALLOWED`도 구조화 기록으로 남길 수 있다.

### 5.2 최초 확정 저장

**통제 전용 저장 경로**를 작은 새 경계로 추가하는 제안이다. `RecordControlledFindingTx`는 가칭이며 구현된 API가 아니다. 기존 generic API는 하위에서 controlled finding 저장을 명시적으로 거부하고, 모든 기존 API에 새 판정 필드를 추가하는 것은 피한다. 통제 전용 최하위 저장 트랜잭션은 다음을 함께 검사/커밋한다.

1. task 소유권·서버 모드·archive 상태 확인. 통제/unknown에서 기존 fallback 금지.
2. 서버가 고른 정본 판정과 근거 묶음을 읽고 잠금/불변성 확인.
3. 같은 작업·의도·execution·case·계정·전체 `resource_ref`·run/reset·하네스 발급 세대 토큰·대상 인스턴스·승인·KG 버전·근거 revision/digest인지 대조.
4. 지원 유형에서 오라클이 증거 기반으로 발급한 `CONFIRMED`, 필수 체크/비교 신호와 영수증 연결, 수명주기 완료를 확인. 역사적 정책의 유효성 기준은 **관찰 시각**이며 단순히 저장 시각에 만료되었다는 이유로 판정을 바꾸지 않음.
5. 서버 저장 연산 키의 동일 재시도는 기존 finding/node ID 반환. 같은 키에 다른 불변 입력, 다른 finding에 이미 연결된 판정은 conflict.
6. finding node·standalone finding·판정 연결·근거 버전을 원자적으로 저장. 중간 오류는 모두 rollback. 동일 판정으로 최초 finding을 2개 만들지 못하도록 DB 유일성으로 보강.

초기 standalone triage는 ARTEX 기존 pending 기본값을 유지한다. node confirmed·E③ 검증 연결과 사람이 관리하는 triage를 따로 둔다. 자동 triage confirmed 승격은 동일 issuance에 근거해야 한다. 수동 확정은 §1.1의 별도 카테고리·승인자/시각/근거 로그를 원자적으로 저장하고 E③ 검증 상태나 자동 매트릭스 집계를 바꾸지 않는다. notification outbox의 best-effort 성공은 수동 승인 필수 로그나 E② 필수 감사 기록의 저장 성공을 대신하지 않는다.

### 5.3 일반 API·근거 수정

- `AddNode(finding, confirmed)`, `SetNodeState`, `AddFinding` 같은 generic 경로는 통제 finding의 생성/자동 확정을 명시적으로 거부하고 전용 저장 경로를 사용한다. 기존 bare status setter도 controlled confirmed 변경을 거부한다. 자동 경로는 판정 출처를, 수동 경로는 §1.1의 카테고리·승인 로그를 검증한 전용 경로만 사용한다. hint/fact의 일반 기록은 유지한다.
- 연결된 통제 finding의 근거를 `Store.Bind`, `EditFindingTraffic` 또는 evidence 갱신으로 바꾸면서 이전 판정을 유효하게 유지하지 못한다. **첫 구현은 이런 변경을 거부**한다. 새 근거 revision/판정을 만든 뒤 동일 트랜잭션에서 연결을 교체하는 후속 절차를 별도로 제공한다.
- `SetFindingVulnClass`로 판정 의미를 바꾸는 것도 초기 범위에서는 거부한다. name/severity 등의 표시·triage metadata와, vulnclass/resource/evidence처럼 판정에 영향을 주는 항목을 구분한다.
- 기록의 task/node 연결을 비우거나 다른 exploration으로 옮겨 모드를 우회하지 못한다. task 삭제 뒤 남는 standalone finding도 출처/모드 fence를 보존해야 한다. task에 cascade 삭제되지 않는 issuance/불변 provenance로 과거 확인 근거를 남기고, `findings.task_id/node_id`가 NULL이 되어도 이를 legacy로 재분류하지 않는다. 이 보존을 구현하기 전 controlled task/finding 삭제는 명시 거부한다.
- **초기 controlled archive/restore는 mutation 전에 거부**한다. 판정·연결·근거 revision·mode fence까지 snapshot/복원하고 digest/count 검증·원자성·유일성을 입증하는 별도 변경 전에는 일반 row restore를 사용하지 않는다. 구버전 archive의 mode를 legacy로 추정하지 않고 unknown으로 격리한다. explicit_legacy archive의 기존 회귀는 유지한다. [findings.go:429](../../db/findings.go#L429)의 INSERT는 후속 intent 생성이므로 finding 생성 우회로 분류하지 않는다.
- 게이트는 이 애플리케이션의 쓰기 경로를 대상으로 한다. DB 관리자/같은 DB 자격증명을 가진 임의 SQL 쓰기까지 막으려면 별도 역할/권한 구성이 필요하며 이 문서가 이를 구현했다고 주장하지 않는다.

### 5.4 잠금 순서

기존 `WithEvidenceTx`는 evidence advisory lock→task→finding 순서를 사용하지만 `FinishFindingRetest`는 finding→retest 순서다. 새 모드 검증을 일부 경로에만 추가하면 잠금 역전이 생길 수 있다.

확정 저장에 참여하는 경로의 제안 순서를 **evidence advisory lock → task/mode fence → 실행/판정 발급 행 → 기존 finding → retest/link/evidence**로 통일한다. 최초 저장·status 변경·bind/edit·retest 종료·삭제와 archive/restore의 허용 여부 확인에 함께 적용한다. 등록은 task fence를 커밋한 뒤 파일 처리를 하며 task를 잡은 채 역순으로 evidence advisory lock을 기다리지 않는다.

task ID를 찾으려고 finding을 먼저 읽어야 하면 잠그지 않고 찾은 뒤, task 잠금 후 finding을 잠그며 소유 관계를 재확인한다. task/finding의 다중 잠금이 필요하면 같은 종류의 행은 ID 오름차순으로 잠근다. 실제 기존 경로 전체와 lock order를 맞추고 DB 교차 transaction 시험을 통과하기 전 활성화하지 않는다. `go test -race`는 DB deadlock 검증을 대신하지 않는다.

### 5.5 물리 저장 스키마 권장안 — 리뷰 결정 일부 반영

아래는 §3·§5의 **구현 검토안**이다. 테이블명·열 이름은 가칭이며 마이그레이션이나 서버 배선이 아니다. 최민준님의 `verdict-record` 필드 의미·평가 읽기 계약을 바꾸지 않고, 이경준님의 KG snapshot 발급 형식도 여기서 확정하지 않는다. [최민준님 리뷰](https://github.com/ABC2LAB/ABCDEF2LAB/pull/123#pullrequestreview-5747067709)의 canonical 정본·평가 회차 결속·수동 export·재검증 읽기 결정을 아래에 반영했다. v1 숫자 범위·판정 시각·정본 바이트 상한의 #142 결정은 아래와 §9.1에 반영했다. 그 밖의 payload 세부 schema, KG snapshot 공급과 보존의 남은 운영 정책은 미결정으로 유지한다. 현재 [db/schema.sql](../../db/schema.sql)의 `tasks`·`exploration_nodes`·`findings`·`finding_retests` 키는 모두 `BIGINT`다. `findings.task_id/node_id`는 `ON DELETE SET NULL`, `finding_retests.finding_id`는 `ON DELETE CASCADE`이므로, 출처·연결 정본을 그 nullable 열이나 재검증 자유텍스트에만 두지 않는다.

| 가칭 테이블 | 필수 열·제약 | 목적 |
|---|---|---|
| `e3_evidence_manifests` | `id BIGSERIAL PK`, `task_id BIGINT NOT NULL`, `execution_ref TEXT`, `evidence_revision BIGINT > 0`, `digest BYTEA`, KG snapshot ID/version/digest, owner/cross 관찰·완결 영수증·필수 감사 checkpoint의 **비민감 참조**, `frozen_at TIMESTAMPTZ`; `(task_id, execution_ref, evidence_revision)` 유일 | 서버가 검증 후 동결한 참조 묶음. 요청/응답·표식 값·자격 원문은 열/JSON에 넣지 않음. 같은 키에 다른 digest는 충돌 |
| `e3_oracle_verdicts` | `id BIGSERIAL PK`, `record_version SMALLINT`(DB 열에만 저장, payload 필드 아님), `finding_id`를 제외한 typed adapter 정형 바이트 `payload_canonical BYTEA NOT NULL`(v1 `octet_length` 1~65536 바이트), 그 바이트의 조회용 사본 `payload JSONB`, `oracle_value` 허용 enum, `payload_digest BYTEA`, `created_at`; `finding_id` 열 없음 | 순수 판정과 평가 필드를 수정 불가 본문으로 저장. `payload_digest = SHA-256(payload_canonical)`. JSONB는 정본 바이트에서만 파생하며 export·재확인은 canonical 기준 |
| `e3_verdict_issuances` | `id BIGSERIAL PK`, `verdict_id BIGINT NOT NULL UNIQUE`, `manifest_id BIGINT NOT NULL`, `task_id/exploration_id/intent_id BIGINT NOT NULL`, `purpose IN (initial,retest)`, retest이면 `retest_id BIGINT NOT NULL`, `mode_version BIGINT > 0`, `eval_run_id`(평가 발급은 필수·비평가는 NULL 허용)와 별도 E② `execution_ref`, `case_ref`, 전체 `resource_ref`, 모델·reset 세대·하네스 발급 세대 토큰 **참조**·인스턴스·승인·KG snapshot 참조/버전/digest·owner/evaluated 비민감 계정/세션 참조, `observed_at`, `operation_key BYTEA UNIQUE`, `input_digest BYTEA`, `issued_at`; 필수 열 `NOT NULL` | 신뢰 서버의 발급 출처·동일 실행/근거 결속. 토큰 값·canary 값 대신 서버가 검증한 참조만 보존. 같은 발급에 판정 하나, 판정에 발급 하나. 평가 회차·모델·reset은 아래 동일 transaction 대조 적용 |
| `e3_verdict_links` | `id BIGSERIAL PK`, `issuance_id BIGINT NOT NULL UNIQUE`, `task_id/finding_id BIGINT NOT NULL`, `purpose IN (initial,retest)`, `retest_id BIGINT NULL`, `evidence_version BIGINT NOT NULL`, `linked_at`; `initial`은 retest NULL, `retest`는 issuance의 retest와 일치 | 판정 본문을 바꾸지 않고 최초 finding 또는 같은 finding의 재검증에 연결. `finding_id` 조회/export는 여기서 투영 |
| `e3_human_confirmations` | `id BIGSERIAL PK`, `task_id/finding_id BIGINT NOT NULL`, 서버 인증 `actor_ref`, 서버 `approved_at`, 제한된 사유 코드·검토 근거 **참조**, `operation_key BYTEA UNIQUE`, `input_digest BYTEA`, `created_at`; 전부 필수·불변 | §1.1의 controlled finding 사람 수동 카테고리와 필수 승인 로그를 한 행에 보존. triage 변경과 같은 transaction. oracle 판정/issuance를 만들지 않으며, 평가 export는 별도 건수만 보고하고 모든 판정 지표·분모에서 제외 |

**정본 바이트·조회 사본.** typed adapter는 `verdict-record`의 필수 필드·세 체크·신호·비밀 제외를 검사한 뒤 정형 바이트를 만들고 `payload_canonical`에 그대로 저장한다. 빈 바이트를 거부하고 v1은 `CHECK (octet_length(payload_canonical) BETWEEN 1 AND 65536)`에 해당하는 상한을 적용한다(65536 포함). §9.1의 signed int64·UTC 6자리 Z와 함께 평가 담당 리뷰에서 확정한 값이다. payload의 그 밖의 세부 schema는 별도 확인하며, 이 문구 전체를 실행 가능한 migration으로 보지 않는다. `payload JSONB`는 그 바이트에서 파생한 조회용 사본이며 독립 입력으로 받지 않는다. export·재확인은 저장한 canonical 바이트의 SHA-256을 `payload_digest`와 대조하고 그 바이트를 읽는다. JSONB를 다시 직렬화해 원래 digest를 재구성하지 않는다.

**정형 직렬화 v1 규칙:** [러너 기록의 서명 직렬화](../evaluation/run-record.md)와 같은 `json.dumps(typed_value, sort_keys=True, separators=(",", ":"), ensure_ascii=False, allow_nan=False).encode("utf-8")` 바이트 규칙을 사용한다. 즉 객체 키 재귀 정렬·배열 순서 유지, 공백 없는 쉼표/콜론 구분자, BOM·말미 개행 없는 UTF-8, 문자열 escape는 해당 직렬화를 따르며, v1 수치는 signed int64 범위의 정확한 정수만 받는다. 유한 실수도 포함해 부동소수점·NaN/Infinity와 범위 밖 정수는 거부한다. 원문 JSON의 중복 키·숫자 token을 보존하는 방식은 사용하지 않는다. 이 바이트 규칙이 기존 판정 필드의 의미를 바꾸거나 새 payload 필드를 추가하지는 않는다. 다른 언어의 encoder가 자동으로 같은 바이트를 만든다고 가정하지 않고 공유 합성 벡터로 일치를 검증한 뒤 adapter를 연결한다.

**평가 회차 결속.** controlled 작업에서 평가 회차용으로 발급할 때 `eval_run_id`는 필수이며, 신뢰 러너 회차 기록의 `run_id`와 정확히 같아야 한다. E② `execution_ref`나 `case_ref`로 회차 ID를 대신하거나 모델/reset 일치만으로 추론하지 않는다. 비평가 실행의 발급은 `eval_run_id=NULL`을 명시적으로 허용하되 그 행은 평가 export에서 제외한다. canonical 판정 본문의 `model_tag`·`reset_generation`과 issuance의 모델·reset 참조가 가리키는 불변값을 판정/issuance 저장의 **같은 transaction**에서 대조하고, 회차 또는 모델/reset 불일치는 `save_conflict`로 거부한다. 하네스 세대 토큰은 reset 세대 문자열을 대체하지 않는다. 현재 러너는 엔진에 `run_id`를 전달하지 않으므로 서버 설정 또는 작업 생성 필드 등 신뢰 전달 경로가 별도로 필요하다. 러너 쪽은 최민준님이 별도 개발하며 이 문서에서 필드/API를 구현·확정하거나 배선 완료로 표현하지 않는다.

**FK·유일성.** 모든 SHA-256 digest/key `BYTEA`에는 열마다 `CHECK (octet_length(column_name) = 32)`를 둔다. manifest→`tasks`, issuance→manifest/verdict/`tasks`/`explorations`/intent node/(retest이면)`finding_retests`, link→issuance/`findings`/(retest이면)`finding_retests`, human→`findings`/`tasks`는 모두 `ON DELETE RESTRICT`, `ON UPDATE RESTRICT`를 제안한다. `e3_oracle_verdicts`는 finding을 참조하지 않는다. `tasks(id,exploration_id)`, `exploration_nodes(id,exploration_id)`, `findings(id,task_id)`, `finding_retests(id,finding_id)`, manifest `(id,task_id)`, issuance `(id,task_id)`의 복합 유일 키와 복합 FK로 task→exploration→intent, task→manifest→issuance→finding, retest→finding의 소유 관계를 DB에서도 보강한다. intent `kind='intent'`, controlled mode, KG/세대/계정 진위는 FK만으로 증명할 수 없어 같은 transaction의 서버 검사를 유지한다. link에는 `finding_id WHERE purpose='initial'`과 `retest_id WHERE purpose='retest'`의 부분 유일 인덱스를 둔다. link의 purpose는 issuance와 대조한다. 다른 finding에 같은 issuance를 연결하거나 한 retest에 판정을 중복 연결할 수 없다. 실제 DDL의 복합 FK·인덱스는 전용 PostgreSQL에서 기존 nullable 행과 archive 경로를 검증한 뒤 적용한다.

**멱등키.** 서버가 길이 구분된 정형 tuple `(version=1, task_id, execution_ref, case_ref, evidence_revision, purpose, retest_id 또는 0)`의 SHA-256으로 `operation_key`를 만든다. 모델/클라이언트가 준 키를 받지 않는다. `input_digest`는 판정 본문 digest와 issuance/manifest의 모든 불변 binding을 정형 직렬화한 SHA-256이다. 같은 operation key·같은 digest의 발급/최초 저장/재검증 재시도는 기존 ID와 결론을 반환하고 송신·재발급을 하지 않는다. 같은 키·다른 digest는 `save_conflict`이며 last-writer-wins 갱신은 없다. 동시 insert는 유일 제약으로 한 행에 수렴한 뒤 기존 행을 잠가 digest와 link를 대조한다. manifest는 `(task_id, execution_ref, evidence_revision)` 충돌에서도 digest를 비교한다. 사람 수동 확정의 operation key는 서버가 발급한 승인 요청 ID와 finding ID의 정형 tuple에서 만들고, 같은 키·다른 승인자/사유/근거는 충돌로 거부한다. 판정+issuance는 한 transaction, 최초 node/finding/link/근거 버전과 수동 triage+필수 승인 로그는 각자 한 transaction이다. 발급 성공 뒤 finding 저장 실패는 발급 레코드만 남을 수 있지만 link/node/finding의 부분 성공은 남기지 않는다.

**보존·쓰기 권한.** 초기 구현은 controlled task·연결 finding·retest의 hard/soft delete와 archive/restore를 mutation 전에 거부하고, 새 정본 행·비민감 manifest·수동 승인 로그를 자동 TTL/GC로 지우지 않는다. 이 행들은 runtime에서 INSERT 후 UPDATE/DELETE를 금지하는 trigger와 권한을 제안한다. 위 `RESTRICT` FK와 현재 `task_control_modes`의 controlled 삭제/아카이브 guard가 방어선을 이룬다. 기존 `finding_traffic_bindings`의 cascade나 `findings.task_id`의 SET NULL에 의지하지 않는다. 기존 explicit_legacy 수동 triage·삭제 동작은 이 초기 controlled 설계로 바꾸지 않으며, 수동 출처 정책을 legacy까지 확대할지는 별도 합의한다. 안전한 별도 보존/삭제·archive snapshot, 재식별 방지와 기간 정책을 합의·시험하기 전에는 일반 삭제/복원 경로를 열지 않는다. 기존 traffic snapshot의 URL/헤더 원문을 새 E③ 테이블에 복사하지 않으며, 원문 보존 정책을 이 설계가 연장한다고 주장하지 않는다. migration owner만 DDL을 하고 모델·일반 도구에는 정본 테이블 쓰기 권한을 주지 않는 방향이다. 현재 단일 DB 자격증명만 쓰는 경로에서 별도 DB 역할로 발급/연결을 강제했다고 주장할 수 없으므로 권한 분리는 구현 검토 항목이다.

**잠금·재시도.** `taskmode`와 같이 `READ COMMITTED` transaction을 쓴다. 모든 새 저장/승격/근거 변경·삭제 거부 경로는 기존 `WithEvidenceTx`의 evidence advisory lock → `tasks` row → 별도 명령의 mode fence/marker 판별 → manifest/issuance(키 순서) → finding(여러 행은 ID 오름차순) → retest → link/evidence 순서로 맞춘다. task/finding/retest ID를 찾기 위한 선행 조회는 `FOR UPDATE` 없이 하고 잠금 뒤 소유 관계를 재확인한다. `FinishFindingRetest`의 현재 finding→retest 순서는 controlled 경로에서 앞의 evidence/task 단계를 추가해야 하며, 일반 경로와 잠금 역전이 남지 않는지 교차 transaction 시험이 필요하다. 등록은 task fence를 커밋하고 파일 marker를 처리하며 task 잠금 안에서 evidence lock을 역순 요청하지 않는다. 유일 제약 충돌/직렬화 실패만 제한된 재시도 대상이고 매번 새 transaction에서 정본을 다시 읽는다. 이 설계만으로 deadlock 부재·DB 권한 분리·실제 저장 게이트 완료를 주장하지 않는다.

**확정 전 확인.** 최민준님의 canonical 정본·회차 결속·수동 평가 제외·재검증 읽기 결정과 v1 상한·정수·시각 형식은 위와 §1.1·§7·§9.1에 반영했다. payload의 나머지 세부 schema·평가 reader 연결은 후속으로 확인하고, 이경준님과 KG snapshot ID/version/digest 공급을 확인한다. 서버 실행 어댑터의 manifest·관찰·감사 checkpoint 참조 발행 시점, DB 역할·보존 기간/삭제 절차도 실제 배선 전 정해야 한다. 이 제안은 E3G37~40·47~52·55~61의 DB 경합/rollback·삭제·수동 집계 시험을 통과해야 확정할 수 있다.

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

server-owned conversation→retest→finding→원래 task와 새 검증 execution을 연결한다. 재검증마다 **별도 issuance·verdict·link**를 만들며 최초 판정/발급/연결을 덮어쓰지 않는다. 재검증을 최초 finding과 같은 의미의 새 finding 생성으로 처리하지 않는다.

평가 export는 해당 `eval_run_id`에서 발급한 판정만 읽고, 다른 회차의 재검증 결과로 해당 회차 판정을 바꾸지 않는다. 같은 finding에 최초·재검증 판정이 함께 있으면 [verdict-record §4의 count_basis](./verdict-record.md#4-평가-읽기-계약-eval-count_basis--레코드)를 따르며, 최신값 덮어쓰기(last-writer-wins)로 집계하지 않는다. 같은 회차의 증거·커버리지·해결 귀속을 함께 보는 기존 평가 판정 기준을 유지한다.

- 해당 finding의 같은 증거에 대한 검증된 `CONFIRMED`는 `reproduced`로 연결할 수 있다. 별도 판정 참조와 실행 연결이 필요하다.
- `ALLOWED`·실행 차단·응답 미확인·근거 부족을 자동 `fixed`로 변환하지 않는다. 초기 조회 게이트는 fixed를 증명하는 별도 규칙을 구현하지 않으며, 이 경로는 `inconclusive`와 사유를 사용한다.
- `SUSPECTED`·`UNDECIDABLE`은 `inconclusive`에 연결하며, 인프라 저장 실패는 에러/실행 실패로 노출한다.
- staged 결론이 있어도 stopped/failed가 completed보다 우선한다. 동일 저장 재시도는 같은 결과를 사용하며 송신을 반복하지 않는다. 명시적 legacy 재검증의 기존 fixed 흐름은 별도이며 통제 작업을 legacy로 보내지 않는다.

## 8. 구현 순서·소유 경계

1. 이동찬↔최민준: 합의한 발급/판정 불변식·retest 매핑과 §3 보완 항목을 기준으로 문서를 최종 확인하고 물리 저장·메타데이터 형식을 정렬. 경준님: 정본 KG 버전과 신원/자원 참조의 구현 연결 정렬.
2. 이동찬: 실제 최신 `transmit/`·`VerifyRead` 기준으로 입력을 맞추고, E② 서버 담당자와 DB mode fence/marker 이관 프로토콜 정렬.
3. 전용 저장·판정/issuance·link·유일성·일관된 잠금 순서를 구현하고 직접 호출/경합/rollback 시험. generic API는 controlled 경로를 거부한다. DB 테스트가 skip이면 완료 아님.
4. recorder/fallback·일반 삽입/승격·근거 변경·retest 경계를 차례로 연결. 저장 코드에서 새 HTTP 요청 0건을 확인.
5. owned synthetic fixture로 owner/cross→판정→최초 저장·재검증까지 확인. 모델·실 대상 baseline 없이 검증 가능.

§4.1의 공통 모드 코어와 DB mode reader/등록·스키마는 별도 PR #43으로 dev에 병합됐다. 이 문서는 판정 issuance/link 저장 구조, E② 등록 API, KG·검증기 코드를 바꾸지 않는다. 현재 통제 Worker에 `report_finding`이 없다는 방어도 게이트 완성 전에 해제하지 않는다.

## 9. 남은 협의

### 9.1 팀 결정을 위한 저장 경계 제안

다음 표는 §5.5의 리뷰 결정과 남은 **제안**을 구분한다. 첫 행의 v1 바이트·정수·시각 값은 평가 담당 결정이며, 다른 행은 담당자의 확인과 수용 증거가 갖춰지기 전 확정 계약이나 구현 완료로 취급하지 않는다. 기존 판정 필드의 의미, KG 발급 책임, 평가 러너 API는 이 표로 바꾸지 않는다.

| 결정 항목 | 권장 검토안과 보류 조건 | 확인 담당 · 수용 증거 |
|---|---|---|
| 판정 정본 바이트 | **v1 결정** — [최민준 리뷰](https://github.com/ABC2LAB/ABCDEF2LAB/pull/142#pullrequestreview-5751671737)에 따라 `verdict-record`의 기존 필드만 직렬화한다. `record_version`은 DB 열에만 두고 payload에 추가하지 않는다. 수치는 signed int64 범위의 정확한 정수이며 부동소수점은 받지 않는다. **판정 정본 시각만** UTC RFC 3339 소수 6자리·`Z`로 정규화한다. 평가 러너 v1.1 나노초 기록은 대상이 아니다. `payload_canonical`은 **1~65536 바이트(64 KiB 포함)**다. 이 값들의 합의 보류는 해제하며, 바이트 불일치·범위 밖 입력은 계속 거부한다. | [공용 v1 벡터](../../e3record/testdata/canonical-v1.json) 27개(정상 바이트 15·거부 12)를 Go 회귀와 별도 Python 교차 검사에서 확인했다. HTML·Unicode·C1·배열·UTC·signed int64·65536/65537 경계와 실제 Prepare 산출물을 포함한다. 리뷰어가 보고한 원본 3009개 벡터 파일은 제공되지 않아 그 전체를 재현했다고 주장하지 않는다. 평가 Python 회귀의 같은 파일 연결과 나머지 schema 확인은 최민준 담당 후속이다. |
| KG snapshot 공급·보존 | owner/cross 전에 같은 불변 snapshot의 ID·version·내용 digest를 정본 reader가 발급하고, 판정·issuance가 그 세 값과 관찰시각을 결속한다는 안이다. **snapshot ID는 task·run·reset 세대 안에서 유일**하며 다른 세대의 같은 ID/version을 재사용하지 않고 세대 교체 시 새 snapshot을 발급한다. **digest 범위는 case의 정책·주체 축 해소 결과 전체**다: 대상 `resource_ref`, 비민감 주체 참조와 계정별 `ExpectedAccess`, `HasRegisteredSource`, `ValidFrom`/`ValidUntil`, `PolicyVersion`, `HintMatchesCanonical`, 공유·위임 해소 상태, reset 세대 결속을 §5.5 정형 직렬화 v1 바이트로 만든 SHA-256이다. canary는 값 없이 등록 참조 `bundle_ref`만 포함한다([KG 계약](./kg-policy.md) §3·§3.1·§3.2). 같은 ID/version의 다른 digest나 보존 중 snapshot 유실은 신규 발급·근거 재확인을 거부한다. 이미 저장된 불변 판정의 읽기는 유지하되 검증 성공으로 표시하지 않는다. **평가 export·채점 완료까지는 보존 하한**이며, controlled 판정이 참조하는 snapshot은 판정과 함께 TTL 없이 보존한다. 상한·파기 절차는 판정 보존 정책과 함께 정하며 합의 전 자동 TTL·파기를 두지 않는다. export 재확인 실패는 확정으로 세지 않고 사유를 보존한 판단불가(`UNDECIDABLE`)로 전달한다. digest만 남기는 방식은 재현 가능성 입증 후에만 재검토한다. | 최민준: KG 소유·권한 항목을 대리 검토했고 이경준 복귀 후 사후 확인을 요청할 예정이다. ID·version·digest와 세대 메타를 함께 공급하는 정본 reader/API 구현은 KG 쪽 후속이다. 발급/재조회·동일 version 이종 내용·세대 재사용·snapshot 유실·export 재확인 실패·보존 경계 시험은 구현 후 검증한다. 평가 어댑터는 최민준 담당이며 이 행이 구현·시험 완료를 뜻하지 않는다. |
| 실행 근거 manifest 동결 | E② owner/cross 관찰과 완결 영수증, **필수 감사 checkpoint의 영속 성공 후**, oracle issuance 전에 서버가 비민감 참조를 한 revision으로 동결하는 안을 검토한다. 일부 근거/감사가 실패하면 manifest를 발급하지 않는다. 동일한 불변 근거로 재시도하면 같은 revision에 멱등 수렴하고, 관찰·근거가 달라졌다면 새 revision을 발급한다. 같은 revision의 다른 digest는 충돌이며 과거 revision을 덮지 않는다. | 이동찬·E② 담당: 관찰·영수증·감사 참조의 발행자와 순서를 확인하고, 중간 실패·재시작·동시 재시도·누락 checkpoint 시험으로 미완결 근거가 `CONFIRMED` 저장에 닿지 않음을 증명. |
| DB 작성 권한·보존 | 최초에는 서버 전용 transaction과 불변 trigger/제약으로 쓰기 경계를 만들고, DB 역할 분리 전에는 단일 자격증명 경로의 임의 SQL 차단을 주장하지 않는다. controlled 정본·수동 승인 로그에 자동 TTL을 두지 않고 삭제·archive/restore는 §5.3처럼 거부한다. 보존 기간과 적법한 삭제·복원 절차가 합의되기 전 운영 활성화 범위를 별도로 제한한다. | 이동찬·최민준: migration/runtime/읽기 역할과 보존 책임자를 정하고 권한 거부, 원자 rollback, 삭제·복원 거부, 보존 기간 경계 시험을 승인. 실제 DB 역할·운영 권한 변경은 별도 작업. |

[최민준의 첫 리뷰](https://github.com/ABC2LAB/ABCDEF2LAB/pull/130#issuecomment-6598432852)의 정본 바이트 벡터·평가 읽기 경계 보완은 유지한다. 64 KiB·signed int64·UTC 6자리 Z는 위 #142 평가 담당 결정으로 v1 값이 확정됐다. 이는 다른 KG/권한/보존 제안의 확정이나 실제 운영 활성화를 뜻하지 않는다. [최신 대리 검토](https://github.com/ABC2LAB/ABCDEF2LAB/pull/130#issuecomment-6601175022)에서 최민준은 이경준의 11/03~11/04 부재 중 KG 소유·권한 항목을 대리 확인하고, 위 digest 범위·ID 세대 결속·보존 하한을 명시하면 Ready로 전환하도록 허용했다. 이는 최민준의 대리 검토이며 **이경준의 직접 확인을 완료한 것은 아니다.** 복귀 후 사후 확인은 별도로 남긴다. 정본 reader/API와 실제 저장·보존 배선 및 수용시험도 후속이며 문서의 Ready가 구현·운영 활성화 완료를 뜻하지 않는다.

### 9.2 그 밖의 남은 협의

- 재검증 매핑과 동일 실행/근거·완결 영수증·판정/issuance 동일 transaction·finding/link 원자 저장 불변식은 리뷰에서 합의했다. 발급 레코드의 두 보완 항목은 §3과 시험 명세에 반영했으며 수정본의 최종 확인을 받는다. 잠금 순서 자체는 이동찬이 기존 엔진 경로와 맞춰 구현·DB 경합 시험으로 검증한다.
- 최민준님 리뷰의 canonical 바이트 정본·회차 결속·수동 export 별도 건수/모든 판정 지표·분모 제외·별도 재검증 읽기 결정은 §5.5·§1.1·§7에 반영했다. 승인자/시각/근거 로그 필드와 API/export, payload의 나머지 세부 schema와 평가 reader 연결은 후속이며, v1 숫자·시각·상한과 공용 바이트 검증은 §9.1에 구분했다. 이 결정들을 전체 물리 저장 계약의 합의나 구현 완료로 확대하지 않는다.
- §5.5의 판정/발급/연결·수동 승인 테이블, write 권한·FK·멱등키·보존/삭제·잠금 순서는 **이동찬의 권장 검토안**이다. 위 리뷰 결정 밖의 세부 형식 확인과 실제 DDL·경합 시험 전에는 확정 저장 규격이나 구현 완료가 아니다.
- private marker와 DB mode fence의 이관·재시작·등록 경합 처리(이동찬).
- 허용 결과 저장의 이름은 최민준·이경준과 확인한다. 기존 `oracle` 의미는 유지. 수직 IDOR·상태변경·fixed 입증·근거 revision 교체는 초기 조회 게이트 이후 범위.
