# E② HTTP 전송기 장애 징후 중지

상태: `HTTPTransmitter`에 **명시적으로 설정한 health 정책**을 연결한 구현. 등록 HTTP 경로에서 관측→[targethealth 판정](./target-health-core.md)→중단 유지→신규 admission 차단을 수행한다. 서버는 운영자의 private snapshot에 지정한 정책을 실행별로 복사·주입한다. UI 설정은 후속이다. 임의 셸·브라우저·MCP·모든 ARTEX 경로 또는 모든 전송기 인스턴스의 대상 전체 통제는 아니다.

## 활성화와 범위

- trusted Go caller가 `transmit.Config.Health`에 `*transmit.HealthConfig`를 전달한다. nil이면 기존 전송 동작이며 health 판정을 했다는 뜻이 아니다.
- `HealthConfig.Policy`는 기존 `targethealth.Config`다. 창·표본·실패/지연 임계치를 모두 명시한다. 기본 임계치나 자동 baseline 학습은 없다.
- `Policy.ScopeRef`는 이 전송기 실행 전용으로 발급한 불투명 참조다. 서버의 실행 결속·다른 인스턴스로의 재발급은 caller 책임이다. 어댑터는 외부 observation을 받지 않고 이 전송기의 실제 관측만 넣는다.
- `HealthConfig.NetworkTimeout`은 양수이며 기존 전체 `RequestTimeout`보다 작아야 한다. 지연 판단이 활성화되면 `Policy.SlowAfter < NetworkTimeout`이어야 한다. 잘못된 정책은 `NewHTTP`에서 거부하며 네트워크 요청을 보내지 않는다.
- 입력 정책은 생성 시 복사한다. 변경된 Go 포인터·LLM 출력으로 실행 중 정책을 넓힐 수 없다. 재설정/재개 API는 제공하지 않는다.

## 서버 승인 snapshot 연결

[서버 등록 흐름](./e2-server-runtime.md)의 private snapshot에 선택적인 `health` 객체를 넣는다. 객체가 없으면 nil Health로 기존 동작을 유지한다. `null`·빈 객체·누락/null 필드·중복/알 수 없는 필드·잘못된 숫자는 거부한다. 서버는 필드를 생략한 상태에서 임계치를 추정하지 않는다.

| 필드 | 의미 |
|---|---|
| `window_ms` | 양수 관측 창, 밀리초 |
| `max_samples` | 코어의 최대 표본 수, 2~10,000 |
| `min_samples` | 임계치 판단에 필요한 최소 표본 수 |
| `failure_limit` | 실패 표본 중단 임계치 |
| `slow_after_ms`·`slow_limit` | 지연 기준과 중단 임계치. 비활성화는 둘 다 명시적 0 |
| `network_timeout_ms` | 실제 연결 시작부터의 제한 시간. 전체 요청 제한 시간보다 작음 |

밀리초 곱셈은 오버플로 전에 거부하며 코어의 임계치 관계도 검사한다. `min_samples`·`failure_limit`·활성 `slow_limit`은 해당 승인의 `max_requests`를 넘길 수 없다. `max_samples`는 요청 예산을 늘리지 않으며 예산보다 클 수 있다. 조건을 통과해도 실제 창 안에 최소 표본이 모인다는 보장은 아니다.

snapshot은 `scope_ref`를 받지 않는다. 서버는 등록 검증 때 임시 검증 범위를 쓰고, 실제 준비 때 새로 발급한 `RunRef`를 `Policy.ScopeRef`에 묶는다. 두 경로는 같은 복사 helper를 사용해 pending 정책이나 다른 실행의 설정을 바꾸지 않는다. 같은 snapshot을 재등록해도 새 전송기·RunRef·빈 관측 창으로 시작하므로 운영자는 기존 재등록 규칙대로 새 승인으로 취급해야 한다.

Worker 도구 출력에는 활성화한 경우에만 안전한 `health` 상태를 추가한다. 중단을 유발한 현재 요청의 `ok`·receipt는 실제 캡처/전송 결과를 유지하고, tool `IsError`와 실행 종료 실패에는 health 중단을 반영한다. 따라서 두 번째 503이 캡처됐다고 실행을 정상 완료로 바꾸지 않는다. 비활성화한 실행은 출력 형식을 유지한다.

## 네트워크 관측

전송기는 기존 직렬 처리·승인·계정·예산·감사 gate를 유지한다. 고정 IP로 새 HTTP/1 연결을 만들기 직전 `httptrace.ConnectStart`에서 네트워크 관측을 시작한다. 중복 callback은 시작 시각·timer를 초기화하지 않는다.

| 단계 | health 처리 |
|---|---|
| queue/rate wait/intent 감사 중 거부·실패·timeout | 표본 없음 |
| 실제 dial/TLS/헤더/본문 읽기의 실패 | 정규화한 transport_error 또는 timeout |
| 최종 HTTP status 수신 및 제한된 응답 처리 | response. 429/5xx는 실패로 집계 |
| redirect/인코딩/크기 정책 거부 | 후속 요청·추가 본문 읽기 없이 이미 받은 최종 status는 관측 |
| 원래 parent 취소·수동 Stop·승인/세션 만료·전체 요청 deadline 종료 | 대상 장애 표본에서 제외 |
| 결과 감사 저장 실패 | 기존 AuditUnavailable 차단 유지. 감사 오류 자체를 target failure로 변환하지 않음 |

경과시간은 실제 connect 시작부터 Do 오류 또는 제한된 body read/close 종료까지다. 큐·rate·intent/result 감사와 JSON 표식 투영 시간은 제외한다. health를 활성화한 응답은 JSON 투영 전에 body close를 끝내며 Close 실패도 정규화한다. redirect/인코딩/크기 거부의 Close에서 독립 health deadline을 넘겨도 receipt는 기존 우선순위에 따라 timeout(또는 더 우선인 로컬 중지/만료)으로 남긴다. 원시 오류·IP·URL·본문·헤더·자격증명은 관측/상태에 보관하지 않는다.

기존 RequestTimeout에는 송신 전 대기가 포함되므로 그 deadline을 target timeout으로 세지 않는다. 대신 **ConnectStart에서 시작하는 별도 timer**가 private cause로 네트워크 context를 취소한다. 해당 cause가 원인이고 원래 parent/승인/세션 context가 살아 있을 때만 health timeout으로 집계한다. 실제 transport가 반환한 `net.Error.Timeout()`도 원래 context가 살아 있을 때 정규화한다. 시작 없는 요청, cleanup에 의한 취소, parent deadline을 대상 장애로 추론하지 않는다.

관측의 원인·종료시각·원래 context 유효성은 I/O 최종 종료 시 한 번만 동결한다. 이후 투영·정리 중 parent가 만료되어도 완료된 관측을 지우지 않는다. timer callback 전달이 늦어도 종료시각이 네트워크 deadline에 도달했으면 timeout으로 처리한다. timer는 종료 경로에서 정리하고 늦은 start/finish/body-close callback은 정리된 관측을 다시 활성화하지 못한다. 이미 수행된 HTTP 요청·대상 처리를 회수하거나 장애의 원인을 확정하는 기능은 아니다.

## 관측 창과 중단

- 실제 Attempt와 등록 자원만 사용한다. 등록 자원 ref의 SHA-256에 고정 접두어를 붙인 별칭을 사용해 `/` 등이 있는 기존 ref도 코어 형식에 맞춘다. 원문 참조는 HealthStatus에 노출하지 않는다.
- `(평가 시각-Window, 평가 시각]`의 전체 관측을 유지하고 만료된 표본만 제거한다. 아직 유효한 표본을 MaxSamples에 맞춰 잘라내지 않는다.
- MaxSamples+1이 되면 코어가 입력 한도 초과로 거부한다. `InputValid=false`, `too_many_observations`, Counts0의 중단 권고로 남기며 서비스 오류로 집계하지 않는다. 이 정책은 요청 예산을 대신하지 않는다.
- 코어의 중단 권고/입력 오류 시 health halt와 기존 stopped gate를 같은 mutex 안에서 설정하고 context 취소는 lock 밖에서 수행한다. 현재 sender가 serial을 해제하기 전에 처리하므로 대기 caller가 사이에 신규 admission을 얻지 못한다.
- 임계치 중단을 이유로 현재 요청의 receipt를 Stopped로 덮어쓰지 않는다. 현재 요청의 실제 완료/timeout/전송 실패 결과를 유지하며 이후 요청은 기존 `Stopped`로 차단한다. 진행 중 send가 자기 자신에게 `StopAndWait`를 호출하지 않으며, caller의 `StopAndWait`는 결과 감사가 끝날 때까지 기다린다.
- 창이 비워지거나 정상 응답이 있어도 health 중단은 해제되지 않는다. 승인된 재개·전송기 재생성·프로세스 재시작 후 보존은 후속 서버의 책임이다. 인스턴스 간/재시작 간 누적 상태를 보장하지 않는다.

## 상태와 감사 호환

`HTTPTransmitter.HealthStatus()`는 마지막 평가의 Enabled·Halted·InputValid·EvaluatedAt·Decision(고정 reason code와 Counts)을 복사해서 반환한다. ReasonCodes도 별도 복사본이다. 조회할 때 창을 재평가하거나 전송을 재개하지 않는다. `Halted`는 health로 인한 중단만 나타내므로 manual Stop·예산/승인 gate를 대신 조회하는 필드가 아니다. Continue/표본 부족은 건강·안전·새 요청 허가가 아니다.

`Receipt`/`Event` v1 필드와 기존 reason enum은 바꾸지 않는다. 기존 감사에는 요청 결과와 후속 Stopped가 기록되고, **HealthStatus와 관측 창 자체는 메모리 상태이며 봉인·영속 감사 기록이 아니다.** Worker 도구 상태를 영속 감사의 대체로 취급하지 않는다. health 정책/중단 사유의 영속 기록과 운영자 API/UI 노출은 후속이다.

## 검증

합성 loopback HTTP 대상에서 429/5xx·느린 응답·헤더/본문 deadline·연결 실패·정책 거부 응답·실수신 횟수와 중단 경합을 검사한다. 큐/rate/audit 대기·parent 취소·수동 Stop·승인/세션 만료·전체 요청 deadline의 제외, 표본 만료/한도/중복·상태 복사·설정 불변·timer/늦은 callback·감사 drain도 검증한다. 기존 전송기·오라클·IDOR·감사 저장기 회귀를 유지한다. 실제 운영 대상·민감 자료·LLM·DB·baseline 측정은 이 시험 범위가 아니다.
