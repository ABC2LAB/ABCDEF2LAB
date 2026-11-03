# HTTP Preview API와 수용시험 계약 (설계 초안)

작성: 2026-11-03 · 이동찬 · 팀 검토용

**상태: 문서 제안. Preview 코드·테스트·REST API·UI는 미구현이며, 아래 시험은 실행할 계획이다.** 이 문서의 머지는 기능 구현이나 실행 허가를 뜻하지 않는다.

연결: [등록 HTTP 전송기](./e2-http-transmitter.md) · [서버 실행 어댑터](./e2-server-runtime.md) · [승인별 누적예산](./approval-request-budget.md)

## 1. 목적과 현재 근거

운영자가 실행 전에 제안된 HTTP 요청의 등록 정합성을 확인하는 경계다. Preview는 **등록된 요청 형태와 일치하는지**만 알려준다. 실제 실행 가능 여부, 대상 앱의 인가 결과, 로그인 성공이나 취약점 판정은 산출하지 않는다.

| 구분 | 확인한 사실 또는 이번 제안 |
| --- | --- |
| dev 기준 | `d51bb21f0af7af594ec49a6455c498f9ea3bb1de`의 [HTTP 실행기](../../transmit/http.go)는 `Send`·`SendDetailed`·`VerifyRead`를 제공하며 Preview는 없다. `NewHTTP`는 신뢰된 설정을 복사한다. |
| 기존 실행 | `SendDetailed`는 시도 번호·결과 감사 등을 생성하고, intent 감사 후 최종 gate와 신원·예산을 재검사해 admission한다. 검사만 하려고 호출하면 부수효과가 발생한다. |
| 별도 제안 | [Draft PR #110](https://github.com/ABC2LAB/ABCDEF2LAB/pull/110), head `a9f312911b2dea13c96c518967c6c15bb14c8c65`는 HTTP에 선택적 공유 Ledger 예약을 연결한다. 이 문서 작성 기준 dev 미병합이며, 서버 승인 발급·수명·복구는 후속이다. |
| 이번 범위 | Preview의 의미·입출력·부수효과 금지·후속 수용시험을 제안한다. 기존 코드와 #110을 변경하지 않는다. |

초기 대상은 **이미 `NewHTTP`로 생성한 HTTPTransmitter의 등록 사본**이다. Preview를 위해 새 실행 ID·승인·전송기·Ledger를 만들거나 운영자 설정을 요청 입력에서 구성하지 않는다. 승인 전 설정 편집기의 검증, 전체 에이전트 dry-run, 셸·브라우저·MCP 검사는 범위 밖이다.

## 2. 제안 API와 입력

Go 경계 제안은 `(*HTTPTransmitter).Preview(proposal idor.AccessRequest) PreviewResult`다. 새 타입·메서드의 이름은 리뷰 대상이며 현재 존재하지 않는다. [idor.Transmitter](../../idor/verifier.go)에 메서드를 추가하지 않는다. REST 경로·직렬화 버전·UI 연결은 이 문서에서 만들지 않는다.

- 입력은 기존 `idor.AccessRequest`다. 서버가 현재 작업에 사용할 기존 인스턴스를 선택한다. 모델이 준 작업 ID나 상태값을 신뢰 근거로 삼지 않는다.
- `As`·`Method`·`ResourceRef`·`StateChanging`은 등록 사본과 비교할 제안이다. 요청이 선언한 verified나 handle 존재만으로 실제 신원을 확인했다고 하지 않는다.
- `Inputs`는 네트워크에 추가하지 않고 출력에도 반사하지 않는다. Preview는 canary·응답·KG를 조회하거나 IDOR 비교기를 호출하지 않는다.
- 임의 URL·헤더·본문·자격증명을 받아 새 등록을 만들지 않는다. 입력과 등록 사본은 변경하지 않는다.

### 2.1 검사 범위와 고정 순서

아래는 **정적 등록 정합성**의 순서다. receiver가 nil·미초기화이면 먼저 `unavailable`이다. 그 외에는 첫 실패를 반환한다.

| 순서 | 조건 | 제안 사유 |
| --- | --- | --- |
| 1 | 정확한 GET이 아니거나 `StateChanging=true` | `unsupported_action` |
| 2 | 전체 `ResourceRef`가 인스턴스 등록에 없음 | `unknown_resource` |
| 3 | 계정 label이 등록에 없음 | `unknown_account` |
| 4 | 자원이 읽기 전용으로 등록되지 않았거나 그 계정의 조회 허가가 없음 | `account_not_approved` |
| 5 | handle·상태가 등록 사본과 다르거나 등록 상태가 verified가 아님 | `unverified_account` |
| 통과 | 위 조건을 모두 만족 | 사유 빈 문자열, `registration_match` |

`ResourceRef`는 현재 실행기가 가진 **전체 opaque 등록 키**로 비교한다. 이 계약이 형식 변환·부분 ID 매칭·URL 해석을 새로 허용하지 않는다. `Resource.ApprovalRef`는 자원별 시험 허가 참조이며 공유예산의 `BudgetRef`와 다르다.

**시간·실행 상태는 검사하지 않는다.** 세션/집행 승인 유효기간, 현재 로그인 상태, 중지·취소·deadline, 로컬/공유예산 잔여량·만료, rate 대기, 감사 sink 가용성, health 상태와 네트워크는 실제 송신 경계의 책임이다. 등록 사본에 verified가 남아 있어도 지금 세션이 유효하다는 뜻은 아니다.

따라서 중지됐거나 예산이 소진된 인스턴스에서도 등록 조건이 같으면 Preview는 `registration_match`일 수 있다. 이는 허용 판정이 아니다. `ExpiredAccount` 등 시간 검사를 Preview에 조용히 추가하지 않고, 필요하면 별도 계약 개정으로 검사 범위·시점을 정한다.

## 3. 결과 의미와 안전한 투영

`PreviewResult`의 논리 필드는 아래처럼 제한한다. 공개 JSON 스키마를 확정하는 표가 아니다.

| 항목 | 값·규칙 |
| --- | --- |
| `status` | 닫힌 집합: `registration_match` / `registration_rejected` / `unavailable` |
| `reason` | match는 빈 문자열. rejected는 §2.1 사유 하나. unavailable은 Preview 전용 고정 사유 `preview_unavailable`이며 실행 감사 사유에 섞지 않는다. |
| `method` | 정상화된 `GET` 또는 `UNSUPPORTED`. 임의 입력 메서드를 복사하지 않는다. |
| `resource_ref`·`account_ref`·`approval_ref` | **해당 인스턴스의 등록에서 찾은 비민감 참조만** 제공한다. 일치하는 등록이 없으면 빈 문자열이며 입력으로 메우지 않는다. |

`unavailable`은 method를 `UNSUPPORTED`, 모든 참조를 빈 문자열로 반환한다. 다른 결과의 각 참조도 등록 조회가 확인된 것만 담는다. 알려진 자원의 approval ref가 표시돼도 그 요청자에게 실행을 승인했다는 뜻은 아니다.

출력에는 URL·연결 IP·헤더·세션 handle·쿠키·토큰·계정 원문·본문·canary 값·`Inputs`·원시 오류를 넣지 않는다. 예외 메시지나 미등록 문자열도 reason에 붙이지 않는다. 등록 참조가 비민감 라벨이라는 전제는 서버 설정 공급자가 책임진다.

`allowed`·`approved`·`WouldSend`·`Completed`·오라클 판정처럼 보이는 결과나 재사용 가능한 실행 허가 토큰을 발급하지 않는다. 미래 소비자는 알 수 없는 status/reason 조합을 표시 오류로 처리하며 허용으로 해석하지 않는다. 화면 연결 시에는 **“등록 일치 · 실행 시 재검사”**처럼 검사 범위를 표시한다.

## 4. 부수효과 0과 실제 송신의 경계

Preview 호출과 실패 경로 모두 다음을 지킨다.

1. HTTP·DNS·TLS·프록시·소켓·외부 모델·DB·KG·파일·로그 writer·사용자 callback을 호출하지 않는다. `Send`·`SendDetailed`·`VerifyRead`로 우회 검사하지 않는다.
2. 실제 gate나 rate 대기를 실행하지 않는다. `Attempt`, admission, inflight, `used`, `lastStart`, limiter, 중지·auditFailed·health 상태를 변경하거나 context/전송기를 생성·취소하지 않는다.
3. 감사 recorder의 intent/result 호출과 실행 영수증·관찰시각 생성을 하지 않는다. Preview는 실행 시도가 아니며, 반복 호출은 실제 첫 송신의 시도 번호를 앞당기지 않는다.
4. Ledger의 `Register`·`Reserve`·`Remaining`을 호출하지 않는다. [Remaining](../../transmit/budget/budget.go)은 만료 관측을 latch할 수 있으므로 읽기처럼 보여도 부수효과 0 검사를 대신하지 못한다. 예산 상태·남은 횟수를 추정해서 출력하지 않는다.
5. 결과는 값/사본으로 반환한다. 호출자가 결과를 바꿔도 등록·전송 상태가 바뀌지 않으며, 동시 Preview·Send·Stop에 data race가 없어야 한다.

구현 시 **정적 등록 검증만 공통 함수로 분리**해 Preview와 실제 Send가 같은 규칙을 사용하도록 제안한다. 실행기의 gate 순서, 시간 기반 `checkAccount`, intent 감사 후 재검사, 최종 admission을 유지한다. Preview와 맞추려고 실제 송신의 만료·중지·감사·예산 검사를 제거하거나 앞선 Preview 결과를 신뢰하지 않는다.

Preview 결과는 등록 사본에 대한 참고값이다. 다른 인스턴스·작업·reset·정책의 실행에 재사용하지 않는다. 실제 실행자는 **현재 요청을 현재 전송기에 다시 제출**한다. 서버 실행 문맥 검증과 [서버 어댑터](./e2-server-runtime.md)의 통제를 생략하지 않는다.

### 4.1 실제 Send와 사유 비교 조건

§2.1 사유의 동치는 receiver·요청·등록이 같고, 실제 송신의 선행 runtime gate가 정상이며, 세션/승인이 관찰 구간에 유효할 때만 시험한다. runtime gate가 먼저 거부하면 실제 사유는 그 경계의 사유를 따른다. Preview 사유를 우선하도록 Send 순서를 바꾸지 않는다.

#110의 공유예산 연결이 반영돼도 Preview는 Ledger를 조회·예약하지 않는다. Preview 직후 다른 실행이 잔여량을 소비할 수 있으며, 실제 Send는 **인스턴스 예산과 공유예산을 모두** 집행해야 한다. 이 문서는 #110의 예약 위치·실패 사유·수명/재시작 정책을 재정의하지 않는다.

## 5. 수용시험 설계 — 전부 미실행

아래는 후속 구현의 시험 명세다. 각 사례는 독립된 합성 설정·계정 라벨·로컬 fixture를 사용한다. 실제 서비스·모델·계정·운영 키/로그·DB를 쓰지 않는다. target 수신과 recorder spy를 계수하고, 공개 transport 주입 API가 있다고 가정하지 않는다. DNS·transport·callback의 금지 경로는 정적 검토와 시험 전용 진입 trap으로 확인한다. 구체 타입인 Ledger에도 호출 계수 API가 없으므로 **정적 호출 경로 검토 + 시험 전용 Go overlay의 금지 연산 진입 검출**을 사용한다. 생산 코드에 callback·interface·관측 API를 추가하는 것을 전제하지 않는다.

Ledger overlay는 fixture 등록·인스턴스 생성이 끝난 뒤 검사 구간에서만 활성화해, Register/Reserve/Remaining 진입 시 고정 sentinel로 실패시키도록 설계한다. 직접 호출 대조가 그 sentinel을 검출하는지 먼저 확인한 뒤 별도의 Preview-only 시험을 한다. 실제 Send 양성 대조는 정상 빌드에서 수행한다. 이는 시험 입력에서 금지 경로 진입을 검출하는 근거이며, 현재 API로 직접 호출 횟수를 계수했다거나 모든 입력을 검증했다는 뜻은 아니다.

| ID | 준비·행동 | 필수 확인 |
| --- | --- | --- |
| PV01 | 유효한 등록 GET을 반복 Preview | match/빈 사유. target 수신·recorder 호출 0. DNS·transport·외부 callback 금지 경로는 정적 검토·진입 trap으로 확인. 입력·설정 불변. |
| PV02 | MaxRequests=1의 같은 인스턴스에서 여러 Preview 후 실제 정상 GET, 다시 GET | Preview 직후 target/audit 0. 실제 첫 시도 번호 1·Completed·수신 1. 다음은 BudgetExhausted·추가 수신 0. |
| PV03 | POST, 상태 변경 GET, 미등록 자원·계정, 잘못된 handle·상태, 미승인 등록 계정 | 7개 정합성 사례 각각 §2.1의 정확한 사유, 모든 금지 경계 0. 정상 runtime 조건의 실제 Send와 사유 동치. |
| PV04 | PV03의 각 Preview 뒤 실제 거부 요청, 이어 정상 GET | 실제 거부는 해당 기존 감사 계약을 따르고 수신 0·예산 비차감. 정상 GET은 Completed·수신 1. Preview의 감사 0과 실제 거부 감사의 존재를 구분. |
| PV05 | 메서드·미등록 참조·Inputs에 서로 다른 합성 비밀 표식을 넣음 | 정해진 enum/등록 참조만 반환. 표식·원문·키·handle·URL·헤더가 결과·오류·쓰기 경계에 나타나지 않음. |
| PV06 | nil/미초기화 receiver | unavailable/preview_unavailable·고정 method·빈 참조. panic·callback·쓰기 0. |
| PV07 | 같은 요청을 Preview한 뒤 중지/취소, 승인·세션 만료 또는 인스턴스 예산 소진 | 등록 사본이 같으면 Preview는 계속 match 가능. 실제 Send는 현재 gate 사유로 거부하고 새 대상 수신 0. Preview를 실행 허가로 재사용하지 않음. |
| PV08 | intent 감사에서 중지 등 상태를 바꾼 실제 Send | 실제 admission 재검사가 유지돼 미허용 송신 0. 이전 match가 재검사를 우회하지 않음. |
| PV09 | #110 통합 후 유효하게 생성한 인스턴스의 공유예산을 다른 실행이 소진하거나 grant가 만료된 뒤 반복 Preview | Preview는 Ledger를 전혀 호출하지 않고 match 가능. 실제 Send는 공유예산을 집행. 이 사례는 #110/합의된 통합 코드 준비 후 수행. |
| PV10 | 유효 grant로 생성 후 만료 경계를 준비하고, 직접 호출 대조로 확인한 시험 전용 Ledger overlay를 검사 구간에 활성화 | 정적 검토와 overlay 미진입이 해당 입력의 Ledger 무호출·상태 불변 근거다. Remaining을 사후 확인용으로 호출하거나, 생성자가 거부할 설정을 우회하지 않음. |
| PV11 | Preview 전후 rate·inflight·중지·감사·health 상태를 비교 | Preview의 counter/lastStart/context/health 변경 0. 시간 경과에 따른 실제 Send 결과 차이를 Preview 부수효과로 오인하지 않음. |
| PV12 | 같은 인스턴스에서 Preview·실제 Send·Stop을 동시 실행 | race 0. 실제 admission만 예산·시도·감사를 변경. 각 Preview의 결과는 등록 사본에 정합. |
| PV13 | 반환 참조/결과 사본 변경 후 재검사·실제 Send | 등록·실행 상태 불변. 다른 작업/reset의 결과를 실행 허가로 받는 경로 없음. |

원하는 실패 검출도 함께 설계한다. Preview를 SendDetailed로 우회하는 변형은 target/audit/attempt 계수가, 예산·Remaining을 조회/차감하는 변형은 위 overlay와 정적 호출 경로 검토가, 입력 문자열 반사는 PV05가, 송신 직전 gate 생략은 PV07·PV08이 잡아야 한다. 변형이 컴파일 실패한 것을 계약 위반 검출로 세지 않는다.

시계·동기화는 후속 시험에서 제어 가능한 경계를 사용하고, 수신 건수·감사 호출·영수증·예약 수를 함께 확인한다. 실행하지 않은 사례를 통과 건수로 보고하지 않는다. #110 의존 사례와 독립 사례의 결과도 분리한다.

## 6. 후속 구현과 리뷰

첫 구현 후보는 공통 정적 검증 추출·Preview·위 시험이다. `transmit/http.go`는 현재 #110과 겹치므로 **그 PR의 변경과 API 검토를 맞춘 뒤** 별도 구현 PR로 진행한다. 이번 PR에서는 Go·기존 계약·UI·서버·Ledger·idor 인터페이스를 수정하지 않는다.

리뷰에서 먼저 확인할 것은 다음이다.

- 등록 정합성만 검사하는 v0 범위와 결과 이름이 허용 판정으로 읽히지 않는지.
- 제안 Go 경계·결과 필드와 nil/미초기화 처리, 실제 Send의 사유 순서 보존이 적절한지.
- 부수효과 0, 비민감 투영, 시간/예산을 포함한 runtime 검사 제외가 시험으로 구분되는지.
- #110 반영 후 공통 검증 경계와 PV09·PV10을 어디서 시험할지. 서버·UI 소비 방식과 사용자 권한 검증은 해당 담당과 후속 합의한다.

이 문서의 API와 시험은 리뷰 중 제안이며 최종 합의·구현·운영 검증을 선언하지 않는다.
