# E③ 쓰기 판정 초안 — 수평 쓰기 IDOR

작성: 2026-11-07 · 최민준 · 상태: **설계 초안. 구현·시험 코드 없음.**

쓰기 IDOR을 12/18 범위에 둘지는 12/04에 판단한다([개발 계획](../development-plan.md)). 이 문서는 판정 쪽 입력과 규칙만 정한다. 승인안(시험 객체·소유자·허용 행동·정확한 메서드와 대상·body 템플릿·보호 세션·예산·만료·회차 결속)은 이동찬이 쓴다. 필요한 관측은 §2·§3으로 맞춘다.

연결: [조회 오라클](./e3-oracle.md) · [보상 계획기](./compensation-planner.md) · [표식 carrier](./kg-canary-carrier-stage1.md)

## 1. 판정 대상과 조회 판정과의 차이

**한 문장:** B가 승인된 쓰기 요청 하나를 A 소유 자원에 보냈을 때, 정책상 금지인데 A의 자원이 실제로 바뀌었는가.

조회 판정은 B의 응답 안에 A의 표식이 있는지 본다. 쓰기는 B의 응답으로 판정할 수 없다. 200인데 안 바뀌는 앱도 있고, 403을 주면서 바뀌는 앱도 있다. 그래서 **쓰기 판정은 B의 응답 상태를 근거로 쓰지 않고, A의 조회로 전후를 비교한다.**

범위는 수평·한 객체·한 번의 쓰기이고, 지정 필드에 값을 쓰는 행동만 다룬다. 삭제와 동작형 요청은 효과를 어느 쓰기에 귀속할 표식이 없어서 `UNDECIDABLE`로 둔다(§7).

## 2. 한 case의 관찰 순서

| 순서 | 관찰 | 주체 | 경로 |
|---|---|---|---|
| 1 | 정상 대조: 같은 쓰기 템플릿이 A의 일반 세션 요청으로 A의 객체에 반영됨 | A | 준비 단계(조건은 §7) 또는 대조 객체 |
| 2 | 사전 조회: 정상 응답, 지정 필드 값이 쓰기 표식 W와 다름 | A | 등록 GET |
| 3 | 쓰기 1회: 승인된 메서드·대상·body로, body에 W를 담아 보냄 | B | E② 승인 쓰기 |
| 4 | 사후 조회: 정상 응답, 지정 필드 값이 W와 정확히 같음 | A | 등록 GET |
| 5 | 판정 자료 동결 | 서버 | 저장 경계 |
| 6 | 보상 | 러너 보상 주체 | [별도 신뢰 경로](./compensation-planner.md) |

- **W를 쓰는 이유:** 값이 바뀌었다는 것만으로는 배경 작업과 구분되지 않는다. 이 시도에서만 쓴 값이 A의 객체에 나타나야 이 쓰기의 효과라고 말할 수 있다.
- **A의 조회로 보는 이유:** B의 응답이나 B의 조회에 W가 보여도 B 쪽 반사일 수 있다. A의 조회 요청에는 W가 없으므로 반사가 끼지 않는다.
- **순서를 고정한다.** 사후 조회와 동결이 보상보다 먼저다. 보상이 효과를 지운 뒤에는 판정 근거를 다시 얻을 수 없다.
- **재시도하지 않는다.** 쓰기를 다시 보내면 새 시도이고 새 W다. 한 case 안에서 쓰기는 한 번이다.

## 3. 입력 계약(제안)

정책·신원은 조회 판정의 `PolicyAssertion`·`SubjectContext`를 그대로 쓴다. `ComparisonFacts` 대신 쓰기 사실을 받는다.

```go
type WriteFacts struct {
    Kind              WriteKind  // 지금은 "field_update"만. 그 밖은 UNDECIDABLE
    SameResource      CheckState // 쓰기 대상과 사전·사후 조회가 같은 객체다
    ControlApplied    CheckState // 순서 1의 정상 대조가 반영됐다
    PreNormal         CheckState // 사전 조회가 정상 응답이다
    PreFieldDiffers   CheckState // 사전 조회의 지정 필드가 W와 다르다
    Sent              CheckState // E② 결과 기록이 response_received 1건이다
    PostNormal        CheckState // 사후 조회가 정상 응답이다
    PostFieldIsMarker CheckState // 사후 조회의 지정 필드가 W와 정확히 같다
}
```

`Sent`는 E② 신뢰 결과에서 옮긴다.

| E② `outcome` | `Sent` |
|---|---|
| `response_received` | `yes` |
| `policy_denied`, `not_sent` | `no` |
| `send_attempted`, `outcome_unknown` | `unknown` |

B 응답의 상태 코드와 본문은 입력에 없다. 없애서 판정이 그 값에 기대지 못하게 한다.

어댑터 책임은 [조회 오라클 §5](./e3-oracle.md)와 같다. 같은 reset 세대·같은 정책 버전·같은 회차의 근거만 한 case로 묶고, 섞이면 호출 전에 `UNDECIDABLE`로 떨군다. 원문과 W 값은 순수 오라클에 넘기지 않는다.

`Sent`를 `yes`로 옮기려면 이 case·시도의 승인 참조, 메서드, 정규 대상, W를 담은 body 템플릿, B의 보호 세션에 결속된 신뢰 전송 결과가 **정확히 1건**이어야 한다. 같은 회차의 다른 승인 쓰기 결과를 가져오면 안 된다. 누락·중복·불일치는 오라클을 부르기 전에 `UNDECIDABLE`로 처리한다. 대조는 어댑터 안에서 하고 원문은 밖으로 내보내지 않는다.

## 4. 판정 규칙

정책·신원 판정은 조회 판정과 같다. 쓰기 쪽 결과만 아래로 정한다. 위에서부터 처음 맞는 줄을 쓴다.

| 쓰기 사실 | 쓰기 결과 | 사유 |
|---|---|---|
| `Kind`가 `field_update` 아님 | unknown | `WRITE_KIND_UNSUPPORTED` |
| `Sent` = `no` | no | `WRITE_NOT_SENT` |
| `Sent` = `unknown` | unknown | `WRITE_OUTCOME_UNKNOWN` |
| `ControlApplied`가 `yes` 아님 | unknown | `WRITE_CONTROL_UNVERIFIED` |
| `SameResource`·`PreNormal`·`PreFieldDiffers`·`PostNormal` 중 `yes` 아닌 것 있음 | unknown | `WRITE_OBSERVATION_INCOMPLETE` |
| `PostFieldIsMarker` = `yes` | yes | `WRITE_APPLIED_TO_OWNER` |
| `PostFieldIsMarker` = `no` | no | `WRITE_NO_EFFECT` |
| 그 밖 | unknown | `WRITE_OBSERVATION_INCOMPLETE` |

최종 값은 조회 판정의 결정표(§4)에서 data 자리에 쓰기 결과를 넣어 정한다.

| 정책 | 신원 | 쓰기 결과 | 판정 |
|---|---|---|---|
| 명시 DENY | yes | yes | **CONFIRMED** |
| 명시 DENY | 그 외 | 그 외 | UNDECIDABLE |
| 허용 | — | — | ALLOWED(B 검증 시), 아니면 UNDECIDABLE |
| 내용 불명 | yes | yes | SUSPECTED |
| 부재·무효 | — | — | UNDECIDABLE |

- **`WRITE_NO_EFFECT`도 `UNDECIDABLE`이다.** "이번 요청이 반영되지 않았다"는 관찰이지 "쓰기 IDOR 없음"이 아니다(절대규칙 5). 표시할 때는 사유로 구분한다.
- **미실행과 결과 불명은 성공이 아니다.** `Sent`가 `yes`가 아니면 CONFIRMED가 나오지 않는다.

## 5. 표식 W

- 시도마다 새로 만든 무작위 비민감 값이다. 준비 때 심은 표식과 다른 값이다.
- 승인안의 body 템플릿에 들어가며 LLM이 정하지 않는다.
- 지정 필드는 등록 조회 view의 직속 문자열 필드이고, 전체 값이 정확히 같을 때만 일치다. 감싸기·이스케이프·절단·부분 포함은 불일치다([carrier C2](./kg-canary-carrier-stage1.md)와 같다).
- W 원문은 판정 입력·공유 로그·근거 파일·LLM 입력에 넣지 않는다. 서버 내부 기록에만 두고 밖으로는 시도 참조만 낸다.

## 6. 경계

- **보상 결과는 판정을 바꾸지 않는다.** 복구 실패는 환경 상태로 따로 기록한다(프로토콜 §8.2의 칸). 쓰기 효과가 확인된 뒤 복구가 실패해도 판정은 그대로이고, 복구 실패가 효과의 증거가 되지도 않는다.
- **기존 조회 진입점을 넓히지 않는다.** `idor.VerifyRead`와 조회 전송기의 GET 제한은 그대로 두고, 쓰기는 별도 진입점으로 만든다.
- **평가 정답은 입력이 아니다**(절대규칙 7). 어떤 행동을 시험할지는 앱의 인가 모델과 승인안으로 정한다.
- **실행 허가는 E②가 쥔다**(절대규칙 3). 이 판정은 일어난 일을 분류할 뿐 쓰기를 허가하지 않는다.

## 7. 열린 항목

1. **정상 대조 방식(이동찬·이경준).** 표식 심기를 대조로 재사용하면 쓰기를 한 번 줄인다. 재사용은 심기가 A의 일반 보호 세션으로, 같은 객체·필드에, B의 쓰기와 같은 요청 조건으로 반영됐을 때만 인정한다. 관리자·fixture 경로의 심기는 A의 일반 인가로 쓸 수 있다는 근거가 아니므로, 그 경우 A 소유 대조 객체로 별도 정상 대조를 한다. carrier가 정해진 뒤 고른다.
2. **결과 불명인데 W가 A에게 보이는 경우(이동찬).** 효과는 강한 근거지만 감사 기록이 불완전하다. 이 초안은 `UNDECIDABLE`로 둔다. 표시에서 일반 불명과 구분할지 정한다.
3. **view 규칙(이동찬).** 사전·사후 조회가 같은 필드를 다른 값(심은 표식, W)과 대조한다. 지금 `Marker.Field`는 view당 유일이므로 사후 조회용 규칙을 어떻게 등록할지 정한다.
4. **삭제·생성.** 삭제는 W가 없어 귀속 근거가 약하고 되돌리기 어렵다. 생성은 A의 목록 조회에 W가 나타나는 형태로 넓힐 수 있다. 첫 범위 밖이다.
5. **`ALLOWED` 분해.** [조회 오라클 §8-①](./e3-oracle.md)과 같이 정한다.

## 8. 시험 스펙

기준 case는 명시 DENY, A≠B 모두 verified, `field_update`, 쓰기 사실 전부 `yes`다. 구현할 때 table test로 옮긴다.

| # | 변형 | 기대 값 | 핵심 사유 |
|---|---|---|---|
| WR01 | 기준 그대로 | CONFIRMED | `WRITE_APPLIED_TO_OWNER` |
| WR02 | `PostFieldIsMarker`=no | UNDECIDABLE | `WRITE_NO_EFFECT` |
| WR03 | `PostFieldIsMarker`=unknown | UNDECIDABLE | `WRITE_OBSERVATION_INCOMPLETE` |
| WR04 | `Sent`=no, 나머지 yes | UNDECIDABLE | `WRITE_NOT_SENT` |
| WR05 | `Sent`=unknown, 나머지 yes(§7-2) | UNDECIDABLE | `WRITE_OUTCOME_UNKNOWN` |
| WR05b | 어댑터: 전송 결과 0건·2건, 또는 승인·대상·세션이 이 case와 다름 | 오라클 호출 없이 UNDECIDABLE | 어댑터 고정 코드 |
| WR06 | `ControlApplied`=no 또는 unknown, 나머지 yes | UNDECIDABLE | `WRITE_CONTROL_UNVERIFIED` |
| WR07 | `SameResource`·`PreNormal`·`PreFieldDiffers`·`PostNormal` 각각 no·unknown | UNDECIDABLE | `WRITE_OBSERVATION_INCOMPLETE` |
| WR08 | `Kind` 다른 값·빈 값, 나머지 yes | UNDECIDABLE | `WRITE_KIND_UNSUPPORTED` |
| WR09 | 정책 ALLOW, 쓰기 사실 전부 yes, B verified | ALLOWED | 정책 허용 |
| WR10 | 정책 ALLOW, B 미검증 | UNDECIDABLE | `SUBJECT_NOT_VERIFIED` |
| WR11 | 정책 내용 불명, 쓰기 사실 전부 yes | SUSPECTED | 내용 불명 사유 |
| WR12 | 정책 부재·무효, 쓰기 사실 전부 yes | UNDECIDABLE | `POLICY_MISSING`·`POLICY_INVALID` |
| WR13 | A==B, 쓰기 사실 전부 yes | CONFIRMED 아님 | `SUBJECTS_NOT_DISTINCT` |
| WR14 | 조합 전수: 쓰기 사실 × 정책 × 신원 | 불변식 | CONFIRMED는 DENY·신원 yes·쓰기 사실 전부 yes일 때만 / `Sent`가 yes가 아니면 CONFIRMED·SUSPECTED 없음 / 입력에 B 응답 상태가 없으므로 상태 코드로 값이 바뀌지 않음 |
