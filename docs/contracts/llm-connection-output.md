# LLM 연결시험 출력 DLP

범위: `agent.TestConnection`의 진단 로그와 `/api/llm/test` 응답. 일반 LLM 호출·다른 설정 API·채팅 출력·외부 송신 허용 정책은 별도다. 기존 [llmrec 저장 DLP](./dlp-projection.md)를 변경하지 않는다.

## 보존하는 정보

- 연결시험 성공·실패의 기존 HTTP 응답 상태는 200이다. 잘못된 요청 JSON은 기존처럼 400이다.
- 성공의 `ok`, `latency_ms`, `model`, `reply` 필드명과 타입을 유지한다. `model`과 `reply` 값은 `[REDACTED]`다.
- provider 호출 실패에도 `latency_ms`를 반환한다. 설정 구성 실패·API key 누락은 기존 측정 기준에 따라 0이다. 호출 지연시간의 측정 시작점은 바꾸지 않는다.
- 원문 provider 오류는 고정 한국어 실패 안내로 바꾼다. 요청 파싱 오류도 고정 안내만 반환한다. API key 누락의 기존 고정 안내는 유지한다.

API의 HTTP 200/400과 provider가 실제로 응답한 HTTP 상태는 서로 다르다. provider 상태는 로그의 `http_status`에 남는다. 할당량 오류 429를 내부에서 402로 정규화하더라도 이 로그는 wire에서 관찰한 429를 유지한다.

## 로그와 Capture

로그는 아래 메타데이터만 기록한다.

```text
[llm-test] latency_ms=23 attempt=1/2 http_status=429 response_bytes=120 failed=false
[llm-test] latency_ms=23 attempt=2/2 http_status=200 response_bytes=240 failed=false
```

`failed`는 전체 연결시험 결과다. 앞선 시도가 실패했어도 마지막 재시도에서 성공하면 false다. HTTP 호출이 없으면 `attempts=0`으로 기록한다. 성공·실패 모두 로그의 지연시간은 `TestConnection`이 반환한 값을 사용한다.

설정의 provider/model/endpoint/proxy 문자열, URL userinfo·path·query, API key, 원문 응답·오류·SSE 프레임을 로그 필드로 받지 않는다. 길이 제한만으로 원문을 안전하다고 판단하지 않는다.

`Capture.Metadata()`는 mutex 아래에서 응답 attempt의 상태와 바이트 수만 읽어 독립된 사본을 반환한다. 크기는 UTF-8 rune 수가 아닌 wire 바이트 수이며 빈 응답 attempt도 0바이트 항목으로 남긴다. 연결시험의 Capture는 metadata-only 모드라 원문 body를 보관하지 않는다. `raw_request` 표시는 본문 복사가 아니라 전송 시도 존재를 뜻하며 요청 완료·송신 성공을 증명하지 않는다. 원문 getter·`Attempts()`의 기존 wire 계약과 provider 전송 내용은 별도 raw Capture 경로에서 유지한다.

`TestConnection`의 기존 시그니처와 내부 호출자에게 돌려주는 reply/error는 유지한다. HTTP handler는 reply를 버리고, 원문 error를 문자열로 변환하지 않는다. 원문은 임의 웹 응답처럼 취급해야 하므로 다른 공개 출력 경로에 연결하지 않는다.

## 검증 기준

- JSON/SSE 성공, 401, 429 재시도·할당량, 빈 응답, 취소, 잘못된 proxy에서 로그·API에 synthetic 민감값이 없어야 한다.
- API 성공/실패 latency와 로그 latency가 일치하고, provider 지연시간이 보존돼야 한다.
- 잘못된 JSON·정수 overflow 오류가 요청 값·주소·키를 반사하지 않아야 한다.
- metadata 사본의 변경이 Capture에 영향을 주지 않고, 읽는 도중 snapshot을 만들어도 일관된 상태·크기만 보여야 한다.
- 기존 Capture 원문 전달·쿼터 정규화·서버 한국어 오류·API key fallback 동작을 유지한다.
