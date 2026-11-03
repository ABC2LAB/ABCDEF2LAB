# 공유예산 서버 연결 계약

2026-11-03 · 이동찬

상태: **서버 연결의 구현 기준 초안. 새 문서 1개이며 제품 코드·승인 발급·실제 서버 배선은 구현하지 않는다.** [누적 예산 코어](./approval-request-budget.md), [HTTP 전송기](./e2-http-transmitter.md), [서버 runtime](./e2-server-runtime.md)의 기존 책임을 유지한다. 팀의 최종 합의·운영 적용을 완료했다는 뜻은 아니다.

## 1. 현재 구현과 연결 대상

| 구분 | 확인한 범위 |
|---|---|
| dev `d51bb21` | [`budget.Ledger`](../../transmit/budget/budget.go)의 독립 메모리 코어. HTTP는 실행 인스턴스별 `MaxRequests`를 집행하며 서버 snapshot에 누적 BudgetRef는 없음 |
| 열린 [PR #110](https://github.com/ABC2LAB/ABCDEF2LAB/pull/110), 검토 head `a9f3129` | 선택적 `transmit.Config.CumulativeBudget{Ledger, Key, Binding}`과 HTTP admission 내부의 `Reserve` 추가. 서버 승인 발급·수명 관리는 제외 |
| 이 문서 | 위 HTTP 입력 경계에 연결할 신뢰 host의 승인 정본·공유 수명·ID·재시작·감사 규칙. 실제 연결은 #110 병합과 아래 수용시험 이후의 별도 구현 |

HTTP API 사실은 [검토한 config](https://github.com/ABC2LAB/ABCDEF2LAB/blob/a9f312911b2dea13c96c518967c6c15bb14c8c65/transmit/config.go)와 [전송기](https://github.com/ABC2LAB/ABCDEF2LAB/blob/a9f312911b2dea13c96c518967c6c15bb14c8c65/transmit/http.go)에 고정한다. #110이 아직 dev에 들어온 것으로 표현하지 않는다. HTTP Preview는 별도 계약이며 여기서 설계·배선하지 않는다.

## 2. 승인 발급과 보관

예산 승인은 인증된 운영자의 **명시적 결정**을 신뢰 서버가 검증해 발급한다. 모델·task 설명·자유 JSON·snapshot_ref 문자열 자체는 발급 권한이 아니다. 기존 controlled-read API의 JWT 인증이 별도 발급/철회 권한 분리까지 구현했다는 뜻은 아니다. 새 endpoint·DB schema·파일 형식은 이번에 정하지 않는다.

| 정본 값 | 발급·보관 규칙 |
|---|---|
| `Key{TaskRef, BudgetRef}` | 서버가 실제 task 소유·존재·archive/삭제 상태를 확인. BudgetRef는 예산 승인마다 새로 발급한 비밀이 아닌 불투명 ID이며 재사용하지 않음 |
| `Binding{PolicyRef, ResetRef}` | 승인한 불변 집행 정책과 대상 reset 정본에 결속. 같은 문자열의 내용 바꿔치기·reset 재해석 금지 |
| `MaxRequests`, `ValidFrom`, `ValidUntil` | 운영 한도 안의 양수 총 예약량과 `[from, until)` 기간. 실행·계정·자원을 나눠도 같은 Key의 총량을 공유 |
| 발급자·발급 시각·승인 요청 참조 | 인증된 발급자와 host 시각의 비민감 참조. 발급 요청의 재시도는 같은 확정 Grant/Key를 반환하는 멱등 처리 |
| authority epoch와 상태 이력 | Ledger 권한 수명의 서버 발급 세대와 준비/사용 가능/닫힘 이력. epoch는 host metadata이며 기존 `budget.Grant` 필드를 추가했다고 표현하지 않음 |

Grant의 Key·Binding·한도·기간은 확정 뒤 불변이다. 변경·연장·추가 예산은 기존 승인을 닫고 **새 BudgetRef의 명시 승인**으로 발급한다. 소진·오류·HTTP 재시도만으로 새 승인을 만들지 않는다. 이것은 Key별 상한이며 여러 개의 명시 승인 전체를 합친 task 평생 상한은 별도 정책이다. 추가 발급이 기존 사용량을 감추는 자동 충전이 되어서는 안 된다.

승인 요청 참조의 논리명은 `ApprovalRequestRef`이며 발급 멱등 키로 사용한다. 같은 키·발급자·task·요청 내용은 같은 확정 Grant를 반환하고, 내용이나 발급자가 다르면 충돌이다. 이 키는 BudgetRef/RunRef/Attempt와 다르며 새 API 필드를 이미 구현한 것은 아니다.

`BudgetRef`는 `Resource.ApprovalRef`와 구분한다. 전자는 누적 예산 승인, 후자는 특정 자원·계정으로 조회 시험을 수행할 허가다. 예산이 있어도 새 자원·계정·정책을 허용하지 않으며 `Config.MaxRequests`의 실행별 상한도 유지한다.

승인 저장소는 모델이 수정할 수 없는 신뢰 host 정본이어야 한다. immutable Grant와 발급/상태 이력을 보존하고 준비 중 레코드를 실행 가능한 것으로 노출하지 않는다. URL·본문·인증 헤더·쿠키·토큰·계정 원문을 예산 레코드·오류·공유 로그에 보관하지 않는다. 기존 승인 snapshot·시크릿 저장을 이 레코드로 복사하지 않는다. 저장소 구현과 권한 분리 검증은 후속이다.

발급의 논리 순서는 다음과 같다. 아래 상태명은 새 프로토콜의 요구사항이며 기존 코드의 enum/API가 아니다.

1. 발급 요청의 신원·권한·task/정책/reset·한도·기간을 확인하고 동일 요청의 확정 결과가 있는지 조회한다. 요청 참조만 맞고 발급자/내용이 다르면 충돌로 차단한다.
2. 새 발급은 Grant·현재 epoch·출처를 정본에 **준비 상태**로 확정한다. 성공 여부가 불명확하면 그 Key를 확인하기 전 다른 BudgetRef로 재발급하지 않는다.
3. 현재 공유 Ledger에 `Register(grant, hostNow)`한다. 정확히 같은 Grant의 반복 등록만 멱등이며 사용량을 보존한다. 충돌·용량·기간 오류면 실행 가능 상태로 게시하지 않는다.
4. 정본의 사용 가능 상태 확정과 host registry의 실행 가능 게시를 모두 마친 뒤에만 실행에 연결한다. 중간 실패는 미게시/차단 상태로 남기고 이미 등록한 Ledger entry를 삭제·환불하지 않는다. 같은 epoch의 확인된 동일 Grant만 재시도할 수 있다.

저장소와 메모리 사이의 crash를 원자적 commit으로 간주하지 않는다. 각 단계의 신원·epoch·상태를 최종 게시 시 다시 확인하고, 다른 caller는 준비 상태를 사용하지 못하게 한다. 불변 레코드와 상태 전이의 저장 방식은 구현 전에 확정한다.

## 3. Ledger 공유 수명과 종료

초기 지원 범위는 **단일 서버 프로세스의 단일 예산 권한자**다. 신뢰 설정의 `budget.Limits`로 서버 runtime 초기화 때 Ledger 하나를 만들고, 같은 authority 수명의 모든 task·승인·case·실행에 같은 객체를 전달한다. 서로 다른 Key는 독립 총량이다. Ledger의 mutex를 복사하거나 요청·task·Worker·HTTP 인스턴스마다 새 Ledger를 만들지 않는다.

`Pause`, 모델 재시도, 실행 종료, 일회 lease 소비, HTTP 실패, 설정 조회 실패는 Ledger 교체 사유가 아니다. 만료·철회·소진 entry도 Ledger 수명 동안 유지한다. `MaxApprovals`는 현재 활성 승인 수가 아니라 누적 등록 수의 상한이며, 가득 차면 새 발급을 거부한다. 자동 eviction·새 Ledger 생성으로 용량을 확보하지 않는다. 동적 Limits 변경도 기존 수명을 보존하지 못하면 중단/명시 재시작 절차로 취급한다.

후속 host coordinator는 승인 준비·게시·실행 연결·닫힘을 직렬화한다. 취소/철회/정책·reset 변경은 먼저 새 연결 gate를 닫고 해당 승인을 쓰는 실행을 모두 포착한 뒤, lock 밖에서 `StopAndWait`·감사 close를 완료한다. 연결을 막는 상태와 실행 등록은 같은 조율 경계를 사용해 뒤늦은 실행이 빠지지 않게 한다. 중간 실패에도 gate를 다시 열지 않는다. 종료 완료 응답 전까지 이미 admission된 요청은 도달했을 수 있으며, 중지·drain도 대상의 처리 결과를 되돌리지 않는다.

Ledger에는 철회 API가 없다. 사용 가능 상태의 철회는 host gate와 기존 실행 중지로 구현해야 하며, `Register`를 다시 호출하거나 entry를 지우는 방식이 아니다. 승인 교체는 기존 실행을 닫은 뒤 새 명시 승인을 게시한다. task 삭제·archive·restore도 예산 레코드 삭제/복원을 통한 충전을 허용하지 않는다. 실제 task-mode/저장 gate 배선은 별도 선행 조건이다.

## 4. 실행·시도 ID와 HTTP 연결

| 식별자 | 수명과 불변성 |
|---|---|
| `Scope.RunRef` | 서버가 실제 실행마다 발급하는 비민감 고유 ID. 하나의 전송기·journal에 결속. 모델 재시도는 같은 실행/전송기 유지 |
| `Scope.CaseRef` | 검사 목적의 참조. 예산 Key에 넣지 않으며 검사 분할로 총량을 늘리지 않음 |
| `Attempt{RunRef, Sequence}` | 해당 전송기의 `Receipt.Attempt` 양수 순번과 실행 참조. 거부된 호출도 번호를 쓸 수 있어 예약 수와 동일하지 않음 |
| 실행→예산 연결 정본 | host가 `RunRef`, 전체 Scope, Key/Binding, epoch, 승인 출처를 불변으로 보관. 저장된 참조만으로 호출자 권한을 추정하지 않음 |

동일 RunRef로 전송기를 재생성해 Sequence를 1부터 다시 시작하지 않는다. 새 실행은 새 RunRef이지만 기존 BudgetRef·Ledger는 유지한다. 실행/순번 충돌·overflow·출처 불명 시 차단하고 새 RunRef를 자동 발급해 거부를 우회하지 않는다. 같은 회계 연산 재시도는 동일 Attempt이며 `ErrDuplicateAttempt`는 **이전 예약의 재송신 허가가 아니다**. 새 전송은 새 시도로 기존 승인의 남은 총량을 다시 검사한다. 자동 HTTP 재시도·리다이렉트 후속 전송은 계속 지원하지 않는다.

이 초기 계약에서는 host의 실행 발급/연결 정본이 이미 발급한 RunRef의 전송기 재생성을 거절한다. HTTP 전송기가 Sequence를 영속 복구하거나 서버가 과거 RunRef를 받아 계속 실행하는 API는 없다. 별도 영속 Attempt allocator를 지원하려면 충돌·복구 계약부터 추가한다.

공유예산을 도입하는 통제 실행 경로에서는 사용 가능한 정본·현재 epoch·task/policy/reset 결속을 확인한 뒤, **같은 Ledger 포인터**와 Key/Binding을 #110의 `Config.CumulativeBudget`에 넣는다. 같은 불변 Scope로 전송기·journal·실행 연결을 만든다. `ExpiresAt`은 기존 실행 승인과 Grant 만료 중 더 이른 값 이하로 제한한다. 시작 시각 이전의 등록은 가능하지만 실제 admission은 Ledger가 거부한다.

일회 실행 lease가 사용할 BudgetRef는 서버 승인 정본에 미리 고정한다. 잔여량이 있는 다른 Grant 탐색·소진/오류 뒤 다른 BudgetRef 선택을 하지 않는다. 승인 선택이나 실행 연결이 불명확하면 차단하며, 같은 승인으로 이어가는 재등록은 기존 Key에 결속한다.

모델의 도구 입력이나 기존 공개 controlled-read 요청에 Ledger/Grant/예산 숫자를 추가하지 않는다. snapshot에서 예산 참조를 읽는 미래 구현도 서버 정본과 출처를 다시 확인해야 한다. 이 경로의 누락/오류를 `CumulativeBudget=nil` 또는 일반 Worker fallback으로 바꾸지 않는다. 독립 전송기 라이브러리의 기존 nil 선택지는 호환성 동작일 뿐, 서버 연결 실패의 대체 경로가 아니다.

예약 순서는 #110의 내부 admission을 사용한다.

1. 기존 GET·읽기 전용 등록·자원/계정/handle·시간·중지·간격·로컬 예산 조건과 durable intent 감사를 통과한다.
2. HTTP 전송기 admission mutex 아래 대기 뒤의 중지·만료·계정·로컬 예산을 재확인하고 공유 `Reserve(Key, Binding, Attempt, freshHostNow)`를 한 번 호출한다.
3. 성공한 경우에만 로컬 `used`도 증가시키고 `Admitted=true`로 만든 뒤 기존 고정 IP·Host/TLS 경계를 거쳐 송신한다. 다른 실행의 로컬 상한과는 독립이지만 같은 공유 총량을 소비한다.

서버 wrapper가 미리 `Reserve`한 뒤 #110 전송기를 호출하면 이중 차감/중복 거부가 되므로 금지한다. `Remaining`을 조회해 송신을 허가하거나 그 값으로 승인을 재발급하지 않는다. preview·조회·검증·전송기 생성도 예약하지 않는다. 초기 연결은 기존 HTTP 경로의 동기 in-memory Reserve만 사용하며, 저장소/감사 callback·새 transaction·등록 대기를 admission mutex나 Ledger mutex 안에 추가하지 않는다. coordinator lock을 잡고 `StopAndWait`하거나 역순 DB 잠금을 기다리지 않도록 실제 lock order를 검토·시험한다.

## 5. 실패·감사·재시작 정책

| 조건 | 공유 예약과 실행 처리 |
|---|---|
| scope/계정/중지/로컬 한도/intent 감사 등 예약 전 거부 | 공유 예약 0, 송신 0. 거부 사실을 보존 |
| `Reserve`의 미등록·결속 불일치·시작 전·중복·설정 오류 | 새 예약/송신 0. #110 HTTP 사유 `budget_unavailable`; 기존 예약의 중복은 환불/재송신하지 않음 |
| `ErrExhausted` / `ErrExpired` | 새 예약/송신 0, 각각 `budget_exhausted` / `expired_approval` |
| Reserve 성공 후 취소·연결/응답 실패·부분 송신 | 예약 1 유지, 환불 없음. `Admitted`는 도달/응답/취약점 확인 증거가 아님 |
| 결과 감사/최종 checkpoint 실패 | 전송 여부 불확실성을 보존하고 기존 감사 실패 중지 적용. 예약·사용량을 되돌리지 않음 |
| 상태 저장소·현재 epoch·clock 신뢰 불명 | 새 실행 게시 차단. 진행 중 실행의 안전한 중지·종료를 수행하며 일반 경로로 대체하지 않음 |

예약·송신·대상 수신·HTTP 캡처 완료 건수를 따로 센다. 남은 값 0은 성공적인 소진 관측일 수 있지만 오류는 유효한 잔여량으로 해석하지 않는다. host는 매 연산 직전 현재 시각을 공급하며 클라이언트/과거 이벤트 시각을 재사용하지 않는다. Ledger의 만료 latch는 관찰한 만료만 보존하므로 아직 관찰하지 못한 시계 역행까지 막았다고 표현하지 않는다. clock 신뢰가 깨지면 host가 gate를 닫아야 한다.

#110은 `Receipt`/`Event`에 BudgetRef를 추가하지 않는다. 기존 영수증의 `ApprovalRef`를 예산 ID로 바꾸지 않는다. 후속 감사는 보호된 실행→예산 연결 정본과 journal의 Scope/Attempt를 결합해 설명해야 한다. 이 연결 정본의 영속 성공이 실행 게시의 조건이다. 원문 URL·인증값·응답·OS/DB 원시 오류는 기록하지 않고 승인/실행 참조·상태·안전한 사유만 보존한다. sealed journal만으로 휘발성 Ledger의 정확한 현재 사용량을 복원했다고 주장하지 않는다.

**초기 재시작 정책은 기존 승인 재사용 차단이다.** 새 프로세스·Ledger 교체·authority 수명 상실은 새 epoch다. 살아남은 승인 metadata·snapshot·marker·journal을 새 빈 Ledger에 자동 재등록하지 않는다. 이전 사용량 0으로 시작해 예산을 충전할 수 있기 때문이다. 저장소가 새로 비었다는 이유로 이전 승인/사용량이 없었다고 인증하지도 않는다.

재시작 뒤 과거/출처 불명 epoch 승인은 차단 상태로 유지한다. 기존 실행의 종료·처리 불확실성을 확인한 뒤 운영자가 새 BudgetRef를 **명시 발급**해야 하며, 전 task 자동 복제·새 ID 일괄 재발급은 하지 않는다. 새 ID 발급은 과거 예산을 이어받거나 복구한 것이 아니라 별도 결정이다. 복구가 필요하면 내구성 있는 원자적 승인/예약/시도 정본과 crash/rollback 검증을 별도 설계한다. 현재 Ledger에는 초기 사용량/시도 주입·복구 API가 없다.

다중 서버·hot reload의 중첩 authority·분리된 worker 프로세스는 초기 지원에서 제외한다. 같은 승인 파일을 여러 Ledger에 등록해도 공유 총량이 되지 않는다. 단일 권한자의 독점 수명을 확인하지 못하면 활성화를 보류하며 실제 다중 프로세스 지원에는 별도 원자적 정본과 fencing이 필요하다.

## 6. 후속 연결의 수용시험

아래는 **계획된 시험**이다. 코어 단위 시험·#110의 HTTP 시험이나 이 문서 검증을 서버 통합 완료로 세지 않는다. synthetic task/승인·격리된 loopback 대상·신뢰 recorder로 수행하며 실제 모델/대상 스캔이 필요하지 않다.

| ID | 조건 | 관측할 결과 |
|---|---|---|
| SB01 | 동일 승인, 서로 다른 실행/case/자원에서 동시 전송 | 성공 예약은 총 한도 이하, 실제 수신도 그 이하. 실행 분할로 추가 예산 0 |
| SB02 | 승인 요청 재시도·같은 Grant 재등록·snapshot 재등록 | 같은 Key/Ledger 유지, 잔여량·기존 시도 유지, 새 자동 승인 0 |
| SB03 | 다른 task/policy/reset·위조 참조·누락 CumulativeBudget | 게시 또는 admission 차단, 예약/송신 0, 일반 경로 fallback 0 |
| SB04 | 로컬 한도·계정·중지·intent 감사 거부 | 공유 예약 0·수신 0, preview/검증/생성도 차감 0 |
| SB05 | 동일 Attempt 중복·같은 RunRef의 재생성 | 추가 예약/송신 0. 다른 정상 RunRef의 같은 Sequence는 남은 총량에서 한 번 예약 |
| SB06 | 예약 후 취소·연결/응답 실패·결과 감사 실패 | 이미 성공한 예약 보존, 환불/새 Ledger/자동 재발급 0 |
| SB07 | 발급 저장·Register·사용 가능 게시 각 단계 장애와 재시도 | 준비/부분 상태 실행 0. epoch가 같은 확인된 동일 Grant만 멱등 재시도 |
| SB08 | 철회/정책 변경과 실행 연결/송신의 barrier 경합 | gate 폐쇄 뒤 새 연결 0, 기존 실행 포착·중지/감사 종료. 이미 admission된 요청은 별도 불확실성으로 기록 |
| SB09 | 만료/소진·등록 용량 가득 참·관찰한 만료 뒤 시각 역행 또는 host clock 신뢰 실패 | 새 예약/송신 0, entry 제거·한도/기간 변경·자동 교체 0 |
| SB10 | 프로세스 재시작·빈 저장소·부분 승인 파일 복구 | 과거 승인 자동 재등록 0, 새 명시 승인 전 송신 0 |
| SB11 | 두 authority 또는 서로 다른 Ledger에 같은 승인 주입 | 초기 운영 활성화 거부. 다중 프로세스 전역 상한 보장으로 보고하지 않음 |
| SB12 | journal·실행 연결·승인 정본 대조와 오류 출력 | Scope/Attempt→정확한 Key/epoch 연결, 예약/송신/수신 분리, 민감 원문 0 |

서버 연결 PR은 위 상태/잠금/영속 경계를 실제 코드에 맞춰 검토하고 시험 결과의 수신·예약 건수를 제시해야 한다. 승인 저장 형식·발급/철회 권한·epoch 보관·다중 프로세스·내구 복구는 미구현이며, 이번 문서는 제품 구현 완료나 전수 통신 통제의 근거가 아니다.

**이번 문서 확인:** 기존 `transmit/budget` 최하위 사례 26개가 실패·skip 0으로 통과했다. 상대 링크 4개, PR/source 링크 3개, SB 시험 ID 12개의 유일성·표 구조·공백과 독립 문서 검토를 확인했다. SB01~SB12 서버 통합 시험·실제 승인 발급·실행은 수행하지 않았다.
