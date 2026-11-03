# 대상 장애 징후 판단 코어

상태: **순수 판단 코어 구현. E②·서버 연결 전.** 대상에 추가 요청을 보내거나 실제 전송을 중지하지 않는다. 대량 요청 부하 개선안(P3)의 판단 부분이며, 대상 전체 장애나 우리 요청이 장애를 유발했다는 인과를 확정하지 않는다.

## 경계

`targethealth.Evaluate(Config, []Observation, now time.Time) (Decision, error)`

- trusted adapter가 같은 대상·같은 전송기 실행에서 수집한 관측 창 전체를 전달한다. 코어는 상태·타이머·wall clock·네트워크·로그·DB를 사용하지 않는다.
- `Config.ScopeRef`는 서버가 발급한 불투명한 실행 식별자다. 대상·reset·전송기 재생성 시 새로 발급한다. 각 관측의 ScopeRef는 이것과 같아야 하며 Attempt는 해당 실행 안에서 고유해야 한다. 기존 E②의 개별 전송기 Attempt만 전역 식별자로 쓰지 않는다.
- ScopeRef/ResourceRef는 영숫자로 시작하는 최대128바이트 영숫자·`_`·`-`·`.`·`:` 참조다. URL·계정 원문·헤더·본문·원시 error를 넣지 않는다. 반환값에는 이 참조도 없다.
- 일관된 입력의 실제 출처·누락·위조를 코어가 인증하지는 않는다. 수집 경계와 실행 결속은 adapter 책임이다.

## 입력과 정책

| 항목 | 의미 |
|---|---|
| Window | 양수 관측 기간. `(now-Window, now]` 구간만 집계 |
| MaxSamples | 전체 전달 batch의 한도. 2 이상, hard cap 10,000 이하 |
| MinSamples | 판단에 필요한 창 안의 표본 수. 2 이상, MaxSamples 이하 |
| FailureLimit | 합산 실패 수 임계치. 2 이상, MaxSamples 이하 |
| SlowAfter / SlowLimit | 완료된 HTTP 응답의 절대 경과 시간·개수 임계치. 시간 양수·개수 2 이상 또는 둘 다0으로 지연 판단 비활성 |

임계치 기본값은 없다. 운영자가 대상과 허용 부하에 맞춰 모두 지정한다. FailureLimit/SlowLimit보다 MinSamples를 크게 설정하면 최소 표본을 채울 때까지 중단을 권고하지 않는다. 요청 빈도 제한·요청 예산은 기존 E②에서 따로 집행해야 한다.

각 Observation은 ScopeRef·Attempt·ResourceRef·NetworkStarted·CompletedAt·Elapsed·Outcome·StatusCode를 가진다.

- NetworkStarted=true는 **실제 네트워크 호출을 시작한 수집 지점**에서만 기록한다. admission이나 RequestWritten만으로 추정하지 않는다. false는 입력 오류다.
- Elapsed는 호출자의 단조 시계로 측정한 네트워크 호출부터 응답 완료/네트워크 실패까지의 시간이다. 큐·rate-limit·감사 저장 대기는 제외한다. 기존 `Receipt.At-AdmittedAt`으로 대신하지 않는다. adapter는 네트워크 시작·완료 시각과 Elapsed를 같은 단조 시계 샘플에서 기록해야 하며, 이 조건을 보장할 수 없으면 health 표본으로 제출하지 않는다.
- Outcome=response는 최종 HTTP status 200~599가 필수다. timeout/transport_error는 status0 또는 헤더 수신 후 body 오류의 status 200~599를 허용한다. 외부 취소·정책 거부·큐 대기 timeout·감사 저장 실패는 대상 오류로 변환하지 않는다. 네트워크 deadline과 로컬 원인의 구분도 adapter 책임이다.
- 미래·0 완료시각, 음수 Elapsed, 알 수 없는 enum/status, 중복 Attempt, 다른 ScopeRef는 거부한다. 만료된 관측도 먼저 검증하며, 전체 batch가 MaxSamples보다 크면 거부한다. 호출자는 오래된 관측을 제거하고 한도 내 창을 유지해야 한다. 한도 초과 시 일부만 잘라 계속 진행하지 않는다. 순서 독립성은 이 검증을 모두 통과한 정상 batch에만 적용하며, 잘못된 관측·중복·만료 표본을 재배열해 유효한 창으로 만들 수는 없다.

## 집계와 반환

- timeout·transport_error·HTTP429·HTTP5xx를 Failures에 합산한다. 관측 하나는 실패 한 번만 센다. 예를 들어 status503을 가진 body timeout은 Timeouts와 Failures에만 한 번씩 집계한다.
- 일반401·403·404·리다이렉트·나머지4xx는 그 status만으로 실패로 세지 않는다. 다만 오래 걸린 완료 응답이면 지연 신호가 될 수 있다.
- `Elapsed >= SlowAfter`인 완료 응답을 SlowResponses에 집계한다. 느린503은 실패·지연 양쪽 신호가 될 수 있다. Timeout/TransportError는 지연에 중복 집계하지 않는다. SlowLimit은 활성화 시 `2 <= SlowLimit <= MaxSamples`여야 하며 `MinSamples <= SlowLimit`도 만족해야 한다. 이는 현재 코어 설정 검증과 동일하다.
- Counts는 창 안 Samples·Failures·Timeouts·TransportErrors·RateLimited·ServerErrors·SlowResponses·DistinctResources를 담는다. 같은 자원의 반복 오류만으로도 중단 권고가 가능하며 DistinctResources는 대상 전체 장애의 증명이 아니다.

| 조건 | Recommendation / ReasonCodes |
|---|---|
| MinSamples 미충족 | continue / insufficient_samples |
| 최소 표본 충족, 모든 임계치 미만 | continue / below_threshold |
| FailureLimit 도달 | stop_recommended / failure_threshold |
| SlowLimit 도달 | stop_recommended / slow_threshold |
| 양쪽 도달 | stop_recommended / failure_threshold, slow_threshold 순서 |
| 설정 오류 | stop_recommended / invalid_config + ErrConfig |
| 관측·now 오류 | stop_recommended / invalid_observation + ErrObservation |
| 중복 Attempt | stop_recommended / duplicate_attempt + ErrObservation |
| batch 한도 초과 | stop_recommended / too_many_observations + ErrObservation |

오류 반환의 Counts는 항상0이다. 입력 경계 오류는 대상 장애 건수로 기록하지 않는다. Continue는 실행 허가·대상 정상·취약점 없음 중 어느 것도 뜻하지 않는다. 표본0개도 건강으로 판정하지 않는다.

## 후속 엔진 연결

이번 변경은 기존 transmit/server/agent/DB/UI/평가 코드를 수정하지 않는다. trusted adapter에서 실행 범위 결속과 실제 네트워크 관측을 보존한 다음 E② 중지 경계에 연결한다.

- 중단 권고 이후 **중지 상태 유지와 명시적 재개 승인**은 adapter 책임이다. 시간이 지나 실패 표본이 창 밖으로 나가도 자동으로 요청을 재개하면 안 된다. 코어에는 stop latch나 reset 동작이 없다.
- 판단 오류를 받으면 호출자는 신규 전송을 보류하고 입력/설정을 해결해야 한다. Continue를 포함한 어떤 결과도 기존 범위·계정·승인·예산·중지 gate를 우회하지 않는다.
- 동시 수집은 adapter가 일관된 창 snapshot으로 제공한다. 코어가 caller의 slice/map을 보관하거나 수정하지는 않지만, 호출 도중 caller가 입력을 수정해도 안전하다는 의미는 아니다.
- 절대 지연·설정 횟수 기반의 안전 권고다. 자동 baseline 학습, 장애 원인 진단, Retry-After 파싱, 재시도·추가 health probe, 실제 자동중지·재개는 구현 범위에 포함하지 않는다.

## 검증

합성 관측으로 혼합 오류·단일 오류·일반403/404·최소 표본·시간/지연 경계·오류/지연 동시 임계치·범위 혼합·비네트워크 timeout·중복·한도·잘못된 입력·순서 독립성·입력 불변성과 만료 후 비상태성을 검증한다. 실제 대상에 대한 장애 유발 실험이나 baseline은 이 테스트로 대체하지 않는다.
