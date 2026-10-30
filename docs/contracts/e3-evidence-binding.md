# E③ 저장 전 실행 증거 결속 검사

상태: `evidencebind`의 독립 순수 Go 검사 구현. [검증기 seam §6](./idor-verifier-seam.md)의 서버 관리 증거 묶음 검증 책임을 작은 함수로 구체화한다. **서버 수집·DB 저장·최초 confirmed 게이트·KG reader 연결은 미구현**이며 기존 패키지의 동작과 저장 규칙을 바꾸지 않는다. 팀원이 만나는 새 경계의 구현 제안이다.

`idor.VerifyRead`는 내부에서 이미 `oracle.Evaluate`를 호출한다. 이 검사는 그 결과를 **저장하기 전 재검산**하는 용도이며, [오라클 계약 §5](./e3-oracle.md)의 Evaluate 이전 결속 전체를 완료한 것이 아니다. 검사·재검산 과정에서 HTTP 요청을 다시 보내지 않는다.

## 입력과 신뢰 경계

```go
outcome := evidencebind.Check(expectedContext, preservedInput)
if !outcome.BindingValid {
    // outcome.EffectiveValue == oracle.Undecidable
    // BindingReason을 보존하고 Recomputed를 저장하지 않는다.
    return
}
// Recomputed는 주어진 Facts/정책/주체/관찰 시각의 재검산 결과다.
// BindingValid 자체를 confirmed나 저장 권한으로 취급하지 않는다.
```

`expectedContext`는 증거와 별도로 서버가 신뢰하는 실행 문맥이다. 입력 JSON이나 모델의 ID 선언에서 정본을 추정하지 않는다. `Input`은 서버가 실제 실행에서 함께 수집·보존한 정책 snapshot, owner/cross 관찰과 `ResultSnapshot`으로 구성한다. `idor.Result`에 나중에 동일한 메타데이터를 덧붙이는 것만으로 실제 유래를 증명할 수 없다.

서버는 한 `Scope.RunRef`를 한 intent/work에만 유일하게 발급하고 다른 의도에 재사용하지 않는다. receipt에는 의도 ID가 없으므로 `EvidenceRef.IntentID`를 수집/저장 시 해당 receipt/capture와 원자적으로 발급·보존해야 한다. 과거 증거를 읽을 때 현재 의도 ID로 덧씌우면 안 된다. 이 함수는 전역 RunRef→IntentID 매핑이나 실제 저장소를 조회해 그 발급을 인증하지 않는다.

| Context 필드 | 의미 |
|---|---|
| `Scope` | 기존 E② `RunRef/TaskRef/CaseRef/ResetRef/PolicyRef` 전체 |
| `IntentID` | 해당 서버 의도 식별자 |
| `ResourceRef` | 같은 논리 자원 참조 |
| `ReadApprovalRef` | 자원에 등록된 조회 검사 허가, receipt의 `ApprovalRef`와 대조 |
| `KGPolicyVersion` | 정본 기대권한 정책 snapshot 버전 |
| `Owner/Evaluated` | 서버의 비민감 주체 라벨·opaque session handle·신원 상태 |

`Scope.PolicyRef`(E② 실행 정책), `ReadApprovalRef`(자원 조회 허가), `KGPolicyVersion`(애플리케이션 기대권한 버전)은 다른 의미다. 값이 우연히 같거나 달라도 서로 대신 채우거나 추론하지 않는다. `Scope.RunRef`는 work 실행 ID이며 평가 envelope의 상위 `run_id`와 별도다. 이 검사에는 GT·모델 태그·평가 귀속이 들어가지 않는다.

## 검사 순서

1. Context의 필수 ref/의도/등록 주체와 enum을 검사한다. 두 주체의 라벨과 session handle은 서로 달라야 한다. 정책·각 관찰·결과 snapshot의 Context를 정본과 대조한다.
2. owner/cross receipt의 전체 scope, 계정·세션·자원·조회 허가를 대조한다. GET·Completed·admission/요청 쓰기/응답 관측이 필요하며 미완료를 성공 자료로 사용하지 않는다. HTTP 2xx만으로 접근 성공을 결정하지 않는다.
3. `AdmittedAt <= ObservedAt <= Receipt.At`와 순차 조회를 확인한다. owner attempt < cross attempt이며, 두 attempt가 연속일 필요는 없다. cross admission은 owner 최종 결과 시각보다 앞설 수 없다. 모든 시각은 호출자가 공급하며 현재 시각을 읽지 않는다.
4. 두 비민감 `EvidenceRef`의 ID가 고유한지, 저장된 `IntentID`가 정본 의도와 같은지, 각각의 scope·attempt·owner/cross 역할이 해당 receipt와 같은지, 결과 snapshot도 같은 두 참조를 보존했는지 검사한다. ref가 실제 저장소에 존재하는지는 확인하지 않는다.
5. 결과의 관찰 시각은 기존 `idor.VerifyRead`와 같은 cross 관찰 시각이어야 한다. HTTP 오류의 bounded capture도 Completed일 수 있지만 E②는 그 본문을 정상 자료로 분류하지 않으므로 non2xx receipt와 NormalData=YES의 모순은 거부한다.
6. 같은 snapshot 정책·등록 주체·기존 `ComparisonFacts`로 `oracle.Evaluate`를 재호출한다. 제공된 판정값·세 체크·사유 코드 순서가 모두 같아야 한다. 정책 유효성은 cross의 **관찰 시각** 기준이며 판정 시각으로 다시 만료시키지 않는다.

원문 응답·canary 값·인증 헤더·비밀번호·키를 입력 타입에 두지 않는다. 잘못된 ref나 임의 판정 사유를 오류에 반사하지 않으며 출력에는 고정 결속 사유와 재검산한 오라클의 enum/사유만 둔다. 비민감 ref·opaque handle의 실제 의미와 출처는 서버 책임이다.

## 결과 의미

- 결속 실패: `BindingValid=false`, `EffectiveValue=undecidable`, 고정 `BindingReason`. `Recomputed`는 비어 있고 의미가 없다. 결속 실패를 `oracle.JudgeReason`으로 가장하지 않는다.
- 결속 일치: `BindingValid=true`, `BindingReason=bound`, `EffectiveValue`와 `Recomputed`는 재검산한 원값이다. 정상 결속이어도 정책·신원·자료에 따라 suspected/undecidable/allowed가 될 수 있다.
- ALLOWED는 정책상 허용 판단이며 전송·조회 성공·취약점 없음으로 변환하지 않는다. 이 v0 경로는 owner/cross 두 완료 관찰 묶음만 지원하고, 네트워크 관찰 없이 정책만으로 판단하는 별도 레코드는 지원하지 않는다.

**검증 한계:** 이것은 서버 입력의 구조적 일관성 검사이며 receipt·Facts의 출처 인증, 실제 같은 호출의 capture에서 Facts가 파생됐다는 증명, 캡처 원문 검사, HMAC 검증, 정본 KG 조회, 세션 로그인 검증, 저장 권한 증명이 아니다. 서버/비교기가 수집 시점의 유래를 보존하고 모델·에이전트가 그것을 수정하지 못하게 하는 배선이 먼저 필요하다. 메타데이터가 일치하도록 구성한 가짜 입력을 판별하는 인증기로 쓰면 안 된다.

이 패키지는 DB나 모델 도구를 호출하지 않는다. 기존 `report_finding`/`RecordFindingTx`의 confirmed 경로도 그대로이므로 전역 확정 저장 강제 완료를 주장하지 않는다. 판정 레코드의 논리 스키마·`access_policy_result` enum·case hash·물리 저장 방식·재검증 결론 매핑은 기존 계약의 미정 항목으로 유지한다.
