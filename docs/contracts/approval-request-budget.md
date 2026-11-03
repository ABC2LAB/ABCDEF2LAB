# 승인별 누적 요청예산 코어

상태: `transmit/budget`의 메모리 코어와 HTTP 전송기 선택적 연결. 서버 승인 발급·실제 실행 경로 배선·영속 복구는 후속이다. 기존 인스턴스별 `transmit.Config.MaxRequests`와 자원별 조회 허가를 대체하지 않는다.

## 목적과 신뢰 경계

여러 실행이 같은 예산 승인에 속할 때 단일 공유 `Ledger`가 예약 수를 누적한다. 현재 HTTP 전송기는 실행 인스턴스마다 요청 예산을 가지며, 기존 서버는 승인 spec을 일회 소비한다. 이번 코어는 이 동작의 우회 취약점을 가정하는 수정이 아니라 추가 운영 상한의 기초다.

Grant·참조·시도 식별자·현재 시각은 신뢰된 host가 공급한다. 모델·요청 JSON·과거 이벤트 시각을 그대로 입력하면 안 된다. 이 패키지는 입력의 구문과 결속을 검사하지만 승인 발급자·호출자를 인증하지 않는다. HTTP·DB·파일·환경변수·현재 시각에 직접 접근하지 않는다.

## 식별자와 고정 조건

| 구조 | 필드 | 의미 |
| --- | --- | --- |
| Key | TaskRef, BudgetRef | 작업 안에서 재사용하지 않는 예산 승인 ID |
| Binding | PolicyRef, ResetRef | 등록 때 고정한 집행 정책·대상 reset 세대 |
| Attempt | RunRef, Sequence | 실행 범위와 그 안의 양수 송신 시도 번호 |
| Grant | Key, Binding, MaxRequests, ValidFrom, ValidUntil | 서버가 확정한 한도·유효기간 |
| Limits | MaxApprovals, MaxRequestsPerApproval | Ledger 수명 동안 등록 수와 승인별 최대 예약 수의 운영 상한 |

BudgetRef는 새 예산 승인 식별자다. `transmit.Resource.ApprovalRef`의 자원별 조회 허가를 재사용하지 않는다. RunRef·case·계정·자원은 Key에 넣지 않아 실행이나 검사 대상을 나눠도 동일 총량을 공유한다. 이들의 개별 실행 허용은 여전히 기존 전송 통제의 책임이다.

문자열을 구분자로 이어 붙이지 않고 comparable struct를 키로 사용한다. 참조는 1~128바이트 ASCII이며 선두는 영숫자, 이후는 영숫자 또는 `_.:/-`다. URL 형태 `://`는 금지한다. 비밀값·원문 URL·계정 값은 참조로 사용하지 않는다.

같은 Key에 다른 Binding·한도·기간을 등록하면 `ErrConflict`다. 정책/reset 변경은 새 BudgetRef로 명시 승인해야 한다. Key/Attempt를 실제 송신마다 새로 발급하거나, 실행마다 별도 Ledger를 만들면 누적 제한 의미가 사라지므로 host가 수명주기를 보존해야 한다.

## 연산과 차감

```go
ledger, err := budget.New(budget.Limits{
    MaxApprovals:           operatorMaxApprovals,
    MaxRequestsPerApproval: operatorMaxRequests,
})
if err != nil {
    return err
}
if err := ledger.Register(grant, hostClock()); err != nil {
    return err
}
if err := ledger.Reserve(grant.Key, grant.Binding, attempt, hostClock()); err != nil {
    return err
}
// 여기까지 왔을 때만 새 예약이다. 이후 실제 송신의 E② 조건도 충족해야 한다.
```

예시는 API 모양이며 실제 host는 각 연산 직전에 새 현재 시각을 공급하고 모든 오류를 처리해야 한다. `Reserve` 성공도 HTTP 실행 허가 전체가 아니다. 대상/신원/자원/승인 만료·중지·감사 등 기존 E② 조건이 실제 전송 시점에 별도로 성립해야 한다.

- `Register`: 미래 기간의 사전 등록을 허용한다. 같은 정본 Grant의 재등록은 사용량과 시도 집합을 보존한다. 등록 용량이 가득 찼거나 한도가 소진된 뒤에도 유효한 동일 Grant 재등록은 성공한다.
- `Reserve`: 단일 mutex 안에서 승인·결속·유효기간·중복·잔여량 확인과 예약 기록을 수행한다. 성공 즉시 한 건을 소비한다. 같은 Attempt 재호출은 추가 차감 없이 `ErrDuplicateAttempt`이며 새로운 송신 권한도 주지 않는다.
- `Remaining`: 동시 호출 중 달라질 수 있는 관측값이다. 이 값으로 먼저 검사한 뒤 별도 송신하는 방식은 허용하지 않는다. 원자적인 `Reserve` 성공이 필요하다. 소진은 `(0,nil)`, 오류는 `(0,error)`다.
- 예약 후 취소·전송 전 실패·부분 전송·응답 오류에 환불하지 않는다. 보수적으로 예약 횟수를 센다. Refund/Reset/Delete/Increase API는 없다.

중복 예약은 소진보다 먼저 검사한다. 한도1에서 같은 Attempt를 여러 caller가 예약하면 최초1건만 성공하고 이후는 소진 오류가 아닌 중복 오류다. 다른 RunRef의 같은 Sequence는 별도 시도이며 같은 총량에서 차감한다.

## 시간·용량·실패

시간은 monotonic 정보를 제거하고 UTC로 정규화한다. 동일 instant의 다른 timezone 표현은 같은 등록이다. 유효 구간은 `[ValidFrom,ValidUntil)`이며 기간/now는 year1~9999의 nonzero 시각이어야 한다. 사전 등록 뒤 시작 전 `Reserve`/`Remaining`은 `ErrNotYetValid`, 만료 경계부터 `ErrExpired`다.

유효한 동일 등록 또는 결속이 맞는 조회가 만료를 관찰하면 해당 entry의 만료를 latch한다. 이후 과거 now를 넣어도 부활하지 않는다. 이는 **만료를 이미 관찰한 뒤**의 시간 역행만 막는다. 아직 관찰하지 못한 실제 시계 역행을 탐지하거나 host의 현재 시각 공급을 인증하는 기능은 아니다.

Limits는 모두 양수이고 승인별 상한은 `MaxInt` 이하여야 한다. Grant의 MaxRequests도 양수이며 설정 상한 이내다. 승인 map은 MaxApprovals, 각 시도 map은 해당 Grant의 MaxRequests로 제한된다. 큰 한도만큼 map을 선할당하지 않고 최초 성공 때 생성한다. 사용량은 시도 map 크기에서 유도한다.

만료 entry도 제거하지 않아 재등록으로 예산을 초기화하지 못한다. 등록 용량은 Ledger 수명 전체의 상한이며 새 Key 등록이 가득 차면 `ErrCapacity`다. nil/zero Ledger는 `ErrConfig`, 잘못된 인자는 `ErrInvalid`, 미등록은 `ErrUnknownApproval`, Binding 불일치는 `ErrBindingMismatch`로 거부한다. 에러는 참조/요청/키/OS 오류를 포함하지 않는 고정 sentinel이다.

검사 순서는 입력 구문/시각 → 정본 조회 → 등록 또는 Binding 충돌 → 만료 → 시작 시각 → 중복 → 소진 → 새 예약이다. 잘못된 Binding으로는 정본의 만료 상태도 바꾸지 않는다. 거부는 요청 수를 소비하지 않으며 유효한 만료 관찰만 되돌릴 수 없는 만료 상태를 남긴다.

## 재시작과 후속 연결

이 코어는 **단일 프로세스의 단일 공유 Ledger 객체**에서만 누적한다. 여러 객체/프로세스의 전역 상한이나 프로세스 재시작 뒤의 누적량은 보존하지 않는다. 새 Ledger는 미등록 승인 예약을 거부하지만 `Register`는 host를 신뢰하므로, 예전 승인 파일을 자동 재등록하면 과거 사용량을 복구하지 못한다.

초기 API에는 복구/초기 사용량 주입 기능이 없다. 재시작 후 기존 승인을 자동 재등록하는 운영은 지원하지 않는다. 새 BudgetRef의 명시 승인 또는 별도 durable 구현의 정본 복구 절차가 필요하다. 이를 이번 메모리 코어가 해결했다고 표현하지 않는다.

후속 서버 adapter는 예산 승인 정본 발급, 서버 수명 동안 공유 객체 유지, 실행·시도 식별자의 불변성, HTTP 전송기의 송신 직전 예약·기존 집행 조건·감사 기록을 실제 서버 실행 경로에 연결하는 배선, 영속/재시작·다중 프로세스 정책을 별도로 구현해야 한다. 현재 HTTP 전송기는 선택적 예약을 지원하며, server·DB·UI·LLM 경로에는 누적 예산을 아직 배선하지 않았다.

## HTTP 전송기 선택적 연결

신뢰된 host가 동일 `Ledger`에 Grant를 등록한 뒤, 각 실행의 `transmit.Config.CumulativeBudget`에 같은 객체와 Key/Binding을 전달할 수 있다. `Key.TaskRef`, `Binding.PolicyRef`, `Binding.ResetRef`는 실행 Scope와 일치해야 한다. 생성 시 바인딩을 복사해 호출자의 이후 변경은 실행에 영향을 주지 않는다. 누적 예산을 설정하지 않은 기존 전송 경로는 인스턴스별 `MaxRequests`를 그대로 적용한다.

등록된 누적 예산은 감사 intent 기록과 기존 범위·계정·중지 검사를 통과한 뒤, HTTP admission 직전에 `Reserve`한다. RunRef와 전송기의 양수 attempt 번호를 시도 식별자로 사용한다. 성공하면 기존 인스턴스별 한도에서도 한 건을 차감한다. 두 조건 중 하나라도 실패하면 송신하지 않는다. 미등록·결속 불일치·시작 전 등 예약 오류는 고정 사유 `budget_unavailable`, 소진은 `budget_exhausted`, 만료는 `expired_approval`로 result 감사 기록에 남긴다. HTTP 응답 실패·취소가 발생해도 이미 예약한 건은 환불하지 않는다.

이 연결만으로는 서버가 공유 승인 객체를 발급·보관하지 않는다. 현재 서버의 실행 스냅샷에는 누적 BudgetRef가 없으므로 운영 경로는 아직 기존 등록별 한도만 집행한다. 서버 배선 전에는 누적 상한을 운영 보장으로 주장하지 않는다. 프로세스 재시작·다중 프로세스에 걸친 누적도 여전히 지원하지 않는다.

같은 Ledger·Key에서 RunRef를 재사용하면 새 전송기의 attempt 번호가 다시 1부터 시작해 기존 예약과 중복될 수 있다. 이 경우 `ErrDuplicateAttempt`는 `budget_unavailable`로 송신을 거부하며 추가 차감하지 않는다. host는 새 실행마다 고유한 RunRef를 유지해야 한다.

현재 감사 Event에는 BudgetRef가 없고 인스턴스 한도와 누적 한도의 소진 모두 `budget_exhausted`를 사용하므로, 감사 기록만으로 어느 예산이 소진됐는지 구분할 수 없다. 서버 감사 계약의 확장 여부는 별도 결정이 필요하며 이번 선택적 연결은 이를 제공하지 않는다.
