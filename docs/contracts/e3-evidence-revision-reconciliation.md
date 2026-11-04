# E③ revision의 재시작 후 과거 결과 조회

2026-11-03 · 이동찬 · **독립 read-only 코어 후속·팀 검토안, host 저장/서버 미연결**

열린 #156 `684451300875251325eb507db30c4d3b45ec4dbc`를 기준으로 한다.
기존 model/store/schema/서버 파일과 선행 PR head를 바꾸지 않고
[`reconcile.go`](../../evidencerevision/reconcile.go), 신규 PG 시험과 이 문서만 추가한다.
[initial revision 코어](./e3-initial-evidence-revision-core.md)의 natural identity·발행·mode/reader 정책은 유지한다.

## 실제 공백과 이번 범위

기존 Lookup은 원래 opaque Collected를 포함한 Request가 필요하다. Issue가 실제 commit했지만 ACK가
유실된 뒤 프로세스가 재시작되면 관찰 Input 메모리가 없어질 수 있다. journal의 Receipt만으로는 원래
ComparisonFacts·동결 정책·결과를 재구성할 수 없고, DB refs/digest도 raw Input의 복원 근거가 아니다.
원문을 영속화하거나 fake Collected를 생성하여 Issue를 재개하는 것은 허용하지 않는다.

`Describe(valid Request)`는 원래 Request가 살아 있을 때 비민감 version 1 Descriptor를 만든다.
`Reconcile(ctx, Descriptor)`는 새 Store에서도 그 operation의 **저장된 과거 Receipt만** 조회한다.
새 revision·operation·실행·Collected·Source·현재 permission을 생성하지 않는다. 기존 Lookup을 변경하지 않는다.

## Descriptor는 데이터이며 권위가 아니다

Descriptor의 8개 필드는 version, operation_ref, task_id, exploration_id, intent_id,
full Scope, 과거 mode_version, 원래 request_digest다. 정수 ID/version은 JSON 문자열로 정확히 전달하고,
request_digest는 현재 Go 타입의 32-byte 배열 JSON이다. Scope는 기존 transmit.Scope 형식을 사용한다.
이는 이 단위의 versioned metadata 형식 제안이며 service transport나 인증 계약 확정이 아니다.
raw Input·정책/관찰 원문·canary 값·계정 자격·key bytes는 포함하지 않는다.
참조가 비민감 서버 발급 ID라는 기존 host 전제는 계속 필요하다.

**caller는 Issue를 시도하기 전에 Descriptor를 보호된 host metadata에 내구 저장해야 한다.**
그 저장이 실패하면 Issue를 시작하지 않아야 한다. 재시작 후에는 그 정확한 Descriptor를 복구해 조회한다.
Describe의 성공이나 JSON 직렬화만으로 파일/DB 내구 저장이 되었다고 해석하지 않는다.
이번 변경은 실제 파일 저장·CLI·service·collector 연결을 구현하지 않는다.

Descriptor까지 유실되면 정확한 원래 operation을 복구할 수 있다고 보장하지 않는다. 같은 RunRef에 행이
있다는 것만으로 특정 operation이 commit됐다고 단정하거나 새 ref를 만들어 재발행하지 않는다.
Descriptor를 변조·복사·추측하는 행위가 reader 권위를 만들지 않는다. Descriptor→Collected/Issue 변환 API는 없다.

## 실제 조회와 SQL 결속

1. 형식·양의 ID/version·정확 task_ref를 검사하고 실제 ScopeProvider의 **현재 ReadReceipt lease**를 획득한다.
   기존 provider error/lease 소유권·철회·복원 연속성 계약을 유지하며 기본/항상 true 권위는 없다.
2. 잠금 없는 SQL 조회로 immutable operation/initial row와 binding을 소유 복사한다. 저장 binding을 strict
   canonical metadata로 파싱한다. duplicate key·trailing data·다른 형식·불완전 metadata는 거부한다.
3. 저장 행의 task/exploration/intent/execution/purpose/mode와 binding.Expected의 **full Scope/Intent/Task**를
   Descriptor와 직접 비교한다. Descriptor의 digest 문자열이 같다는 사실만으로 Scope 일치를 추정하지 않는다.
4. 저장 binding + 저장 exploration/mode + 기존 version/domain에서 원래 requestDigest를 재계산한다.
   이 값이 operation.request_digest·initial.input_digest·Descriptor.request_digest와 모두 같아야 한다.
   envelope evidence digest와 별도 저장 digest/관찰 refs의 결속도 대조한다.
5. transaction에서 실제 tasks→exploration→intent 관계를 다시 확인한다. 그 관계 lock 아래 같은 저장 행을
   재조회하여 첫 소유 snapshot과 동일한지도 확인한다. 파싱/hash/host·marker callback은 row lock 밖에서 끝난다.
6. 반환 Scope는 **저장된 binding**에서 구성한다. 외부 Descriptor.Scope를 그대로 응답에 옮기지 않는다.
   transaction은 read/rollback으로 끝나며 Issue·INSERT·sequence allocation·commit을 호출하지 않는다.

같은 requestDigest를 유지한 채 CaseRef/ResetRef/PolicyRef/RunRef/Task/Intent 등만 바꾼 Descriptor는 거부한다.
다른 op/digest·실제 SQL 소유 관계 변경·현재 reader 권위 철회도 성공으로 처리하지 않는다.
오류는 기존 고정 범주와 zero Receipt를 사용하며 DB/OS/authority 원문을 반환하지 않는다.

## 역사 metadata와 현재 실행을 분리

현재 marker나 mode를 잃어도 actual ReadReceipt lease와 SQL 관계·정확 binding이 확인되면 과거 Receipt를
조회할 수 있다. 그것은 현재 실행 허가나 finding 저장 권한이 아니며 새 Issue의 현재 controlled/version/marker
gate를 우회하지 않는다. 실제 key/epoch/root/namespace·A/B·KG·승인·완료/감사 증명도 생성하지 않는다.
향후 Source/collector 연결은 별도 소유권 조율과 현재 source 정합/저장 gate가 필요하다.

Descriptor/hash·두 조회 결과가 같다는 것만으로 실제 reader lease·DB 무결성·ABA·backup/restore 정당성을
증명하지 않는다. Provider는 조회 전체 수명을 보호해야 하며 privileged DB 침해나 오래된 DB 전체 복원 탐지를
이 코어가 새로 제공하지 않는다. 현재 host 인증/reader 정책의 실제 producer는 여전히 외부 필수다.

## 검증과 현실 구분

전용 PG·합성 scope/관찰을 사용해 실제 commit 후 ACK 유실→원본 Request/Input 삭제→Descriptor JSON→
새 Store 조회, mode marker 손실 후 historical 조회·reader 거부/취소, 같은 digest의 Scope/Intent/Task 변조,
다른 op/digest·실제 intent 소유 변경, write/sequence 증가 0 및 최소 metadata 형식을 검사한다.
추가 손상 metadata 회귀는 소유 test schema의 읽기 전용 VIEW projection만 사용한다. public history의 기존
trigger/제약을 해제하지 않고, canonical binding 변경을 원래 digest 재계산으로 거부하는지 확인한다.

이 합성 시험은 실제 프로세스/운영 인증·보호 Descriptor 저장·epoch/key/권한 발급·실제 HTTP/LLM/Juice Shop
진단이 아니다. 운영 DB·기존 서버/스키마·타 PR head는 변경하지 않는다.
