# E③ 조회 재검증 상태 매핑 — 구현 초안

상태: `retest.Map` 순수 함수와 합성 단위 시험을 구현한다. **실제 재검증 저장·서버 호출 연결은 후속**이다. [저장 게이트 §6·7](./e3-storage-gate.md#6-판정실패-처리)의 일부 매핑 규칙이며 전체 저장 게이트의 완료를 뜻하지 않는다.

## 입력과 출력

```go
result, err := retest.Map(finalStatus, expectedContext, collectedEvidence)
```

- `finalStatus`는 ARTEX의 terminal 이름을 쓰는 `retest.Status`다: `completed`, `failed`, `stopped`. 서버가 실행 종료·drain 후 공급한다. 모델이 제안한 결론이나 중간 상태로 대신하지 않는다.
- `expectedContext`와 `collectedEvidence`는 기존 `evidencebind.Context/Input`이다. 원래 finding과 새 retest의 연결을 검증한 서버가 같은 실행·intent·자원·정책·계정·근거의 동결된 묶음을 공급해야 한다.
- 완료 입력은 `evidencebind.Check`로 결속을 검사하고 오라클을 재계산한다. 호출자가 임의로 조립한 `Outcome.BindingValid=true`나 `confirmed` 문자열만 받는 API를 제공하지 않는다.
- 결과의 `Verdict`는 `reproduced` 또는 `inconclusive`다. `Reason`은 매핑 사유, `Check`는 결속 검사 결과와 재계산한 오라클 판정·사유다. 오류 반환 시에는 zero Result라 저장 가능한 결론이 없다.
- 이 함수에는 staged verdict 입력이 없다. `fixed` 상수·반환 경로를 제공하지 않으며, 입력 오라클 값으로 주입한 `fixed`도 결속 검사를 통과하지 못한다.

## 우선순위와 판정표

아래 순서로 판단한다. `reproduced`도 실제 저장 권한이나 finding의 상태 변경을 뜻하지 않는다.

| 조건 | 결과 |
|---|---|
| zero·알 수 없는 status, `pending`·`running` 등 비종료 상태 | `ErrInvalidStatus`, zero Result |
| `failed` | `ErrExecutionFailed`, zero Result. 앞선 확정·fixed 제안보다 실패 우선 |
| `stopped` | `inconclusive / execution_stopped`, `Check`는 zero. 앞선 판정·영수증보다 중단 우선 |
| `completed`, owner/cross 중 필수 감사 `AuditUnavailable` | `ErrAuditUnavailable`, zero Result |
| `completed`, 결속 검사 통과 + 재계산 `CONFIRMED` | `reproduced / confirmed_evidence` |
| `completed`, 결속 검사 통과 + `ALLOWED/SUSPECTED/UNDECIDABLE` | `inconclusive / not_confirmed`, 원래 오라클 값·사유 유지 |
| `completed`, 차단·취소·전송 오류·시간 초과·송신/응답 미확인·결속 불일치 | `inconclusive / evidence_rejected`, 결속 거부 사유 유지 |

HTTP 200이나 `Admitted=true`만으로는 실행·재현을 인정하지 않는다. 양쪽의 `Completed`, `Admitted`, `RequestWritten`, `ResponseReceived`와 기존 결속·시각·순서 검사까지 요구한다. 응답의 정상 데이터 여부와 표식·반사/공통 화면 제외도 기존 오라클이 판정한다.

`TransportError/TimedOut`은 실행 불확실성이며 `fixed`가 아니다. `AuditUnavailable`은 필수 기록의 무결성을 확인할 수 없는 장애라 완료 결론으로 바꾸지 않는다. 모든 반환 오류는 고정 sentinel이며 입력 원문이나 임의 오류 문자열을 덧붙이지 않는다.

## 후속 저장 어댑터의 책임

- host의 신원·상태 진위, 실제 capture와 facts의 연결, 원래 finding→retest→새 execution의 소유·결속, 판정 issuance/참조를 검증한다. `evidencebind.Check`와 이 매퍼만으로 그 출처를 인증할 수 없다.
- DB mode fence·잠금·근거 revision·판정/link·저장 transaction을 검증한다. `Result`를 저장 허가 토큰으로 쓰거나 오류의 zero Result를 `inconclusive` 성공으로 바꾸지 않는다.
- 중단 결과를 받아도 lifecycle을 `completed`로 바꾸지 않는다. DB·발급·필수 감사 저장 오류는 실제 호출 경계에서 실패로 전파한다. 이 함수는 저장소를 호출하거나 그 오류를 처리하지 않는다.
- 기존 모델 staged verdict와 별개로 이 결과를 사용한다. 명시적 legacy 재검증의 기존 `fixed` 처리와 통제 조회 매핑을 섞지 않는다.
- 동결된 같은 입력의 재시도는 같은 매핑을 사용한다. 함수는 네트워크·DB·파일·시계·실행 콜백에 접근하지 않는다.

## 시험 범위

terminal status × 오라클 네 값, 양쪽 영수증의 reason·송수신 flag 조합, 누락·다른 실행/자원/세션/intent·위조 판정, 감사 실패, unknown/nonterminal status, 반복 결과·입력 불변/출력 비공유를 합성 입력으로 시험한다. 실제 저장·triage 변화량·발급·재시도 transaction·저장 중 요청 수의 통합 시험은 이번 범위가 아니다. [E3G42~44](./e3-storage-gate-tests.md#2-계획된-사례)의 매핑 부분만 다루며 해당 저장 통합 시험을 완료로 세지 않는다.
