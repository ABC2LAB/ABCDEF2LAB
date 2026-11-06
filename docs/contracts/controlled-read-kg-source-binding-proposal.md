# 동결 KG와 최종 E③ Source의 provenance 결속 제안

상태: **기존 fixture opt-in의 서버 연결 초안 구현**. [등록 KG 결속](./controlled-read-kg-fixture-binding-proposal.md) 뒤 실제 실행에 보유한 snapshot과 최종 Source의 KG/준비 metadata가 같아야 한다는 검사다. 실제 Source의 인증·권한·감사·발급을 대체하거나 필드를 채우지 않는다. 팀 채택과 운영 수용은 별도다.

## 기준과 소유

기준 dev는 `fe09601a7c1399b7831acf404c67fa83cd3fcb4d`이며 #214·#231·#232의 병합된 코드를 소비한다. 기존 #231/#232 기능을 다시 구현하지 않는다. 변경은 strict host 연결부, 신규 Source 래퍼·회귀 및 이 문서뿐이다. 피드백 세션 소유 #215의 collector/finalization scope·server/lifecycle 파일과 #219/#220의 Runner·agent·격리 파일을 수정하거나 미병합 코드를 통합하지 않는다.

현재 E③ Prepare는 Source context·정책/표식·근거·감사·완료를 대조하고 provenance 필드의 문법/존재를 검사한다. 등록된 실제 KG snapshot 객체를 알지는 못한다. 이번 C 연결부는 그 객체와의 일치를 더하며 판정 규칙·저장 payload·새 Source 타입을 만들지 않는다.

## 설치와 실행 순서

`ControlledReadHostOptions.FixtureRegistries`가 비nil인 기존 opt-in strict/durable/revisioned 생성자에서 Findings resolver를 감싼다. 복구 작업/백그라운드 admission 전에 기존 설정과 함께 설치한다. nil 옵션은 기존 resolver를 유지한다. 새 HTTP·JSON·manifest·권한 설정은 없다.

1. 실제 요청의 private Config handle에서 `FixtureSnapshot(ctx)`을 얻는다. 잘못된 요청·취소·만료·close·무효 KG면 공급자를 호출하지 않는다.
2. 원래 신뢰 Findings resolver를 그대로 호출하고, 같은 KG pointer·nonnil Source와 `Expected.KGPolicyVersion == snapshot.PolicyVersion()`을 검사한다. resolver 반환 뒤 같은 live handle을 다시 확인한다. 버전/plan 결속 실패는 검증 송신 전에 거부된다.
3. Source는 **이 단계에서 호출하지 않는다.** 기존 collector/finalizer의 원래 호출 시점에 매번 실제 Source를 새로 호출한다.
4. Source에 전달되는 finalizer context와 Expected는 그대로 유지한다. 원래 resolver의 실행 context를 붙잡아 최종 읽기 권위를 대신하거나 context 취소를 제거하지 않는다.
5. Source 호출 전후의 취소와 snapshot 무효화를 확인하고, 반환 Source를 아래 표로 대조한다. 실패하면 고정 unavailable과 빈 Source를 반환한다. 원문 공급자 오류·파일 경로·canary/세션 내용을 새 오류나 로그로 출력하지 않는다.

| Source/실행 문맥 | 해당 등록의 동결 KG | 검사 시점 |
|---|---|---|
| `Expected.KGPolicyVersion` | `PolicyVersion()` | resolver 반환 |
| Source의 전체 `Expected` | 이 plan의 전체 Expected | Source 반환 |
| `KGSnapshotRef` | `Ref()` | Source 반환 |
| `KGSnapshotDigest` | `Digest()` | Source 반환 |
| `GenerationRef` | `GenerationRef()` | Source 반환 |
| `InstanceRef` | `InstanceRef()` | Source 반환 |
| `ResetGeneration` | `ResetGeneration()` | Source 반환 |

일치한 Source는 필드를 보정하지 않고 그대로 반환한다. ModelTag·EvalRunID·ExplorationID·mode/revision·관찰 digest·생성 시각·Completed/Finalized·감사·심각도는 기존 실제 생산 주체의 값이다. `KG.RunID`를 E② RunRef나 EvalRunID로 바꾸지 않는다. 같은 문자열이라는 사실은 공급자의 인증 근거가 아니다. 정책/표식과 실제 관찰의 대조는 기존 E③ Prepare가 계속 담당한다.

## 마감 경계와 실패 의미

#232는 close 시작에 새 Config 조회를 닫고 기존 finalization 반환 뒤 KG를 무효화한다. 따라서 Source 단계는 앞서 캡처한 **같은 snapshot**만 사용하며, 이미 닫힌 Config 조회나 입력 파일을 다시 열지 않는다. 보유한 KG의 정체성 일치는 현재 host 권한/lease·키/epoch·복원 연속성의 증명이 아니다. 실제 Source는 원래 context의 인증된 읽기 scope를 유지해야 한다.

버전 불일치는 송신 전 실패다. 나머지 Source metadata 불일치는 실제 owner/cross 조회 후 최종 읽기에서 발견될 수 있으며, 이전 송신이나 이미 발급된 중간 receipt를 되돌린 것으로 표현하지 않는다. 실패한 Source로 최종 저장을 허용하지 않는 기존 경로를 유지한다. 재시도마다 공급자를 다시 읽고, 이전 성공 metadata를 캐시해 현재 성공으로 바꾸지 않는다.

#215의 최종 Prepare/DB save 반환까지의 권한 수명은 별도 작업이다. 이 변경은 그 파일을 수정하지 않으며 실제 공급자·계정·권한·키·root/epoch·대상 실행 또는 운영 활성화를 만들지 않는다. 일반 E②와 IDOR의 같은 회차, 운영 bootstrap·실제 저장·모델/제품 배치 수용도 별도다.

## 검증 구분

합성 private 파일과 실제 runtime handle로 각 provenance 축·policy version·다른 KG pointer·요청/취소/close·읽기 전후 무효화·원문 오류 비노출·새 읽기·비KG 필드 보존을 시험한다. 실제 원본 Worker→소유 loopback owner/cross 2GET→감사 마감→최종 Source callback을 연결해 버전 실패의 송신 0건과 Source digest 실패의 조회 후 최종 거부를 대조한다. 성공 callback은 실제 DB 저장 수용의 대체물이 아니다.

전용 PostgreSQL의 합성 strict bootstrap 시험은 fixture 옵션에 따른 실제 resolver 설치 여부만 확인한다. 운영 인증·권한을 검증한 기동이 아니며, 실제 대상·유료 API·모델·운영 배포는 사용하지 않는다.
