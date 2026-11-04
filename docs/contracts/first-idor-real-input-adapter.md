# 실제 실행 입력의 기존 타입 결속 어댑터

## 기준과 고유 범위

기준 dev는 `893d1d771a10764591ee209814800f46420e0dc9`다. 이 단위는 dev의 기존 타입만
사용하므로 미병합 코드 의존이 없다. 신규 `server/controlled_read_real_inputs.go`와
동명 시험 파일, 이 문서만 소유하며 서버 기동·기존 로더·Source 조립은 변경하지 않는다.

실제 자료 제공 주체와 보호 참조는 **미정/미확인**이다. 새 manifest·서명·producer는 없다.
이 결과는 자료의 실제 연결 완료 또는 첫 진단 준비 완료가 아니다.
기존 입력 대장은 [first-idor-host-inputs.md](./first-idor-host-inputs.md)를 따른다.

## C가 호출하는 기존 타입 경계

`BindControlledReadRealInputs(ctx, request, expected, approved, kg, source)`는 다음을 대조한다.

- 실제 요청 task/intent와 `evidencebind.Context`, 실행별 `transmit.Config.Scope`의 정확한 일치.
- 서로 다른 A/B의 등록 AccountRef 전체, session handle의 비모호성, 현재 session/config 만료.
- 같은 resource의 명시적 ReadOnly·ReadApprovalRef·A/B 시험 허용 계정 등록.
- 전달받은 KG resolver와 C가 조립한 SourceReader의 nonnil 여부(실제 권한 확인 아님).

동일한 값이라는 사실은 출처 인증이 아니다. `approved`는 **그 실행에 실제 사용하는 동결된
E② Config**여야 한다. request의 RunRef를 임의 Config에 복사하거나 문자열 status/approval로
신뢰를 만들어서는 안 된다. 인증된 공급자가 없으면 호출자가 명시적으로 거부해야 한다.
이 함수는 비밀 Header/URL을 읽거나 출력하지 않으며 Config·KG·Source를 변경하지 않는다.

```go
// 인증된 실제 공급자의 조회를 성공한 뒤, C의 기존 FindingResolver 안에서 호출한다.
// request는 실제 서버 실행 요청, expected/config는 그 실행의 신뢰 자료다.
plan, err := server.BindControlledReadRealInputs(ctx, request, expected, config, kg, source)
return plan, err
```

반환값은 기존 `ControlledReadFindingPlan`이다. KG와 Source는 읽거나 감싸지 않고 그대로 전달한다.
전체 `transmit.Config`의 URL/origin/IP·예산·projection 등의 유효성은 기존 E② 로더/생성자가
검사한다. 만료 관찰은 호출 시점의 검사일 뿐이며 각 실제 송신은 E②의 시간·철회·예산·감사 gate를
다시 통과해야 한다. 함수가 독자적으로 조회·발급·기동·DB 저장을 하지는 않는다.

## 최종 조립에 남기는 지점

현재 Resolver 요청은 task/intent/scope/account/resource만 가진다. C는 실제 승인된 E② snapshot과
같은 실행별 Config를 얻는 보호된 조회를 제공해야 한다. `controlled_read_verification.go`의
실제 resolver 호출 시점과 `controlled_read_runtime.go`의 실행 scope 생성/승인 snapshot을
대조해야 하며, 이 단위는 기존 파일에 새 lookup이나 lifecycle hook을 추가하지 않는다.

첫 회차의 **EvalRunID는 비어 있어야 한다.** 그 필드는 `e3record.Source`에 있으므로 C의 실제
Source 공급/최종 조립에서 비평가 문맥을 확인해야 한다. 이 어댑터는 Source를 조기에 호출하거나
EvalRunID를 강제로 비워 평가 자료를 비평가로 바꾸지 않는다.
`EvidenceDigest/RecordCreatedAt/Completed/Finalized`는 기존 collector/engine 생산값,
`EvidenceRevision`은 #159의 실제 발행 경로를 그대로 따른다.

C의 최종 revision-aware 통합 기준은 #159 `32bd87f77460953f2a60a5de3cdc391cc0b75758`다.
착수 확인한 #143 `1dfc4a0fbf9f47db07eacf8df420fcbd9982e9ad` 및 #149
`8cc0d5588fb6e8b6313434006224cccd8968bab2` → #151
`edbfc8015921dd0a8bbf71ee6a6b741e84f35c11`,
#156 `684451300875251325eb507db30c4d3b45ec4dbc` → #157
`9c079d92e5728411774005cf4760124a55068bb4` → #158
`e6af2335fbd0412fccf03df589f95d754c26c41b`는 모두 #159 조상임을 확인했다.
#151·#158 이후 #159, 그 뒤 A/B를 C에서 최종 결합한다. A와 B의 새 PR 상호 의존은 없다.
선행 head 변경 시 C가 실제 공급/저장 의미의 영향을 다시 대조한다.

실제 KG reader·동결 snapshot/digest/canary는 이경준, 비평가 reset/health·generation·instance는
윤재상 등 기존 생산 경계에서 확인한다. 인증된 A/B session·정확한 자원별 승인과 실제 task/mode/model
자료도 아직 확보되지 않았다. 이 문서에 비밀 원문이나 실제 공급을 대신하는 참조를 채우지 않는다.

## 검증의 구분

합성 native Go 값으로 정상 변환, 결속 불일치·모호한 등록·만료·미제공·취소, secret Header/URL이
반환 Plan에 포함되지 않음을 검증한다. KG/Source의 거부를 성공으로 바꾸지 않는 것도 대조한다.
실제 KG 조회·인증·자료 연결·DB·대상 HTTP·Juice Shop·운영 신뢰 공급 검증은 미실행이다.
자료 확보 뒤 C는 같은 보호 snapshot/실제 계정·승인·동결 KG/fixture/model 결속, 비평가 Source 및
strict bootstrap을 검증하고 최초 회차를 순차 실행해야 한다. Source 오류 원문은 출력하지 않는다.
