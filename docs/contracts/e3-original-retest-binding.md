# E③ 원 finding·재검증·실행 참조 결속

2026-11-03 · 이동찬

상태: **host 입력 일관성 계약·순수 검사 초안. 실제 인증·발급·DB 저장 연결은 후속이다.** 새 `e3binding` 패키지를 사용하며 기존 [evidencebind](./e3-evidence-binding.md)·[재검증 매핑](./e3-retest-mapping.md)·[저장 게이트](./e3-storage-gate.md)의 책임을 유지한다. 기존 `findings`/`finding_retests` schema나 판정 규칙은 바꾸지 않는다.

## 1. 입력과 독립 출처

[`Check(target, input)`](../../e3binding/check.go)는 caller가 별도로 선택한 Target과 세 host 정본 projection을 비교한다. 모델 입력을 세 레코드와 Target으로 복제하면 출처 인증이 되지 않는다. caller는 요청된 finding/retest·task 소유권을 확인하고 잠금 아래의 같은 정본 상태에서 입력을 확보해야 한다. 이 함수는 정본을 조회하지 않는다.

| 입력 | 필드·의미 |
|---|---|
| `Target` | `TaskRef`, 양의 `FindingID`, `RetestID`, `OriginalEvidenceRevision`. 요청/host 선택의 독립 anchor이며 비교 대상 레코드에서 역으로 추정하지 않음 |
| `Finding` | 원 finding의 `ID`, `TaskRef`, `ResourceRef`, `SourceRunRef`, `OriginalEvidenceRevision`. 서버가 보존한 원 근거 출처 |
| `Retest` | `ID`, `FindingID`, `OriginalEvidenceRevision`, `Expected evidencebind.Context`. 새 실행을 발급할 때 독립적으로 고정한 결속 |
| `Execution` | `RetestID`, `FindingID`, `OriginalEvidenceRevision`, `Context evidencebind.Context`. host가 실제 실행과 함께 보존한 연결 |

`OriginalEvidenceRevision`은 **원 finding의 host-frozen evidence generation**이며 Target·Finding·Retest·Execution이 같은 원본 snapshot을 가리키도록 복사·대조하는 양의 uint64다. 새 실행의 자체 evidence revision이나 특정 DB 컬럼, mode fence Version, KG version, timestamp를 뜻하지 않는다. 이번 논리 입력 필드의 물리 저장/발급은 미구현이다. 기존 DB row에 이 값이 이미 있다고 간주하거나 ID·시각에서 계산·기본값 1로 보충하지 않는다.

실행 식별자는 기존 `evidencebind.Context.Scope.RunRef`를 사용한다. 별도의 미구현 ExecutionID·issuance token을 발명하지 않는다. `Finding.SourceRunRef`와 새 Retest.Expected의 RunRef는 달라야 한다. 서로 다른 DB 테이블의 finding ID와 retest ID 숫자가 같은 것은 허용한다. E② RunRef는 평가 회차의 `run_id`와 다르다.

## 2. 구문과 결속 기준

Target·record ID는 양의 int64이고 original revision은 양의 uint64다. 모든 참조는 기존 evidencebind와 같은 1..128바이트 ASCII 규칙을 사용한다. 첫 문자는 영숫자, 나머지는 영숫자 또는 `_.:/-`이며 `://`는 거부한다. trim·대소문자 변환·부분 ID 비교·숫자/문자열 상호 추정은 하지 않는다. 비밀값·URL·계정 원문을 참조로 쓰지 않는다.

Retest.Expected와 Execution.Context **양쪽**에서 다음 구문을 확인한다. 기존 validator가 package-private이므로 새 모듈 경계에서 같은 조건을 검사하며 기존 모듈을 수정하지 않는다.

- Scope의 `RunRef`, `TaskRef`, `CaseRef`, `ResetRef`, `PolicyRef`와 `ResourceRef`, `ReadApprovalRef`, `KGPolicyVersion`, 양쪽 주체의 `Label`, `Handle` 참조를 모두 요구한다.
- `IntentID > 0`, owner/evaluated의 서로 다른 Label과 서로 다른 Handle을 요구한다.
- 두 주체의 Status는 `verified`, `unknown`, `expired`, `mismatch` 중 하나다. 지원 enum이라는 뜻이며 실제 신원 확인이나 실행 허용을 뜻하지 않는다.

구문 확인 뒤 원 Finding의 ID·task·revision은 Target과 같아야 한다. Retest의 ID·finding·revision도 Target과 같고, Expected의 task는 Target, 자원은 원 Finding.ResourceRef와 같아야 한다. Execution의 retest·finding·revision은 Target과 같고 **Context의 모든 필드가 Retest.Expected와 exact equality**여야 한다. Expected를 실제 Execution에서 역으로 만들면 독립 검증이 사라진다.

원본→새 실행 사이 reset·정책·case·intent·세션이 바뀌는 재검증은 허용한다. 원 Finding은 이 함수에서 과거 전체 Context를 보관하지 않으며 원 RunRef·자원·task·근거 revision을 제공한다. 새 reset/정책 등을 승인하고 같은 논리 자원을 새 상태에 올바르게 연결하는 책임은 host에 있다. 새 Expected와 실제 Context 사이의 차이는 허용하지 않는다. 잘못된 다른 자원으로 자동 remap하지 않는다.

## 3. 결과와 우선순위

[`Decision`](../../e3binding/model.go)의 필드는 비공개이며 `Consistent()`와 `Reason()`으로 읽는다. zero Decision은 false/`unknown`이다. 성공 결과도 실행·저장 권한이 아니며 식별자·문맥·판정값을 보유하지 않는다. 아래 순서로 첫 실패를 반환하므로 다중 위반의 사유도 결정적이다.

| 순서 | 조건 | Consistent / Reason |
|---|---|---|
| 1 | Target 구문 오류·누락 | false / `target_invalid` |
| 2 | Finding 구문 오류·누락 | false / `finding_invalid` |
| 3 | Retest의 ID·FindingID·original revision 또는 Expected 구문 오류 | false / `retest_invalid` |
| 4 | Execution의 RetestID·FindingID·original revision 또는 Context 구문 오류 | false / `execution_invalid` |
| 5 | Finding ID/task/original revision이 Target과 다름 | false / `finding_mismatch` |
| 6 | Retest ID/finding/revision/task/resource 결속 불일치 | false / `retest_mismatch` |
| 7 | 원 SourceRunRef를 새 Expected.RunRef로 재사용 | false / `source_execution_reused` |
| 8 | Execution의 retest/finding/original revision 불일치 | false / `execution_mismatch` |
| 9 | 실제 Context의 필드 하나라도 Expected와 다름 | false / `execution_context_mismatch` |
| 10 | 위 조건 모두 통과 | true / `references_consistent` |

Reason에는 원문 ref·ID·URL·underlying 오류를 붙이지 않는다. 실패 결과를 성공적인 `inconclusive` 저장이나 legacy fallback으로 변환하지 않는다. 이 함수는 error/판정/fixed를 반환하지 않으며 참조 일관성만 판별한다.

## 4. 기존 E③와 실제 저장의 경계

caller는 별도 host 어댑터에서 실제 소유권·발급 출처·동결 snapshot·같은 transaction과 잠금 수명을 검증해야 한다. 그 뒤 참조 일관성과 **같은 Expected/actual 묶음**을 사용해 기존 `evidencebind.Check`/`retest.Map`의 lifecycle·영수증·facts·판정 검사를 유지한다. 새 core는 이 함수들을 호출하거나 대체하지 않는다. `unknown` 주체의 참조가 서로 같아도 신원이 검증되거나 취약점이 재현됐다는 뜻은 아니다.

현재 DB의 자유문장 finding snapshot이나 모델 staged verdict는 이 입력의 인증된 출처를 대신하지 못한다. 이 검사만으로 verdict issuance·finding 상태 변경·확정 저장·DB mode 판별·archive/restore·삭제를 허용하지 않는다. 판정 발급·원 근거 revision 저장 형식과 실제 연결은 민준님 리뷰 및 후속 저장 어댑터의 대상이다.

함수는 네트워크·DB·파일·시계·callback에 접근하지 않고 입력을 수정하지 않는다. 반복 호출은 같은 결과이며 완료/소비 기록이나 전역 replay registry를 만들지 않는다. 관찰 가능한 원 RunRef 재사용과 레코드 결속 불일치는 차단하지만, 서로 다른 host 레코드에 같은 RunRef를 재발급한 이력까지 탐지하지 못한다. 중복 발급/재사용 금지와 한 번만 저장하는 원자성은 실제 정본 저장소의 책임이다. 성공 Decision을 이후 실행/저장의 재사용 권한으로 캐시하지 않는다.

## 5. 합성 시험

[`단위 시험`](../../e3binding/check_test.go)은 정상 대조군, 독립 Target 대비 coherent foreign record, ID/task/revision/resource 불일치, 각 Context 필드의 누락·단일 변경, 구문/enum 오류, 원 실행 replay와 새 세대 허용, 다중 위반 사유 순서, zero 결과·입력 불변·반복·동시 호출·안전한 출력·fuzz를 다룬다. 실제 발급·DB·원 finding→저장→재검증 종단 시험이나 네트워크 송신은 포함하지 않는다.

**확인한 결과:** 신규 단위 사례 115개·fuzz seed 6개와 기존 evidencebind 78개·retest 111개, 총 310개가 race에서 통과했고 실패·skip은 0이다. 새 함수의 statement coverage는 100%이며 별도 fuzz 264,921회·관련 vet·전체 build·gofmt/공백·링크와 독립 리뷰 보완을 확인했다. 실제 저장·신원/발급 출처 인증·모델/대상 실행은 시험하지 않았다.
