# DLP 구조화 데이터 투영

상태: 공통 Go 코어와 `llmrec.Recorder`의 신규 DB 기록 경계에 적용한다. 정규화 요청·응답, raw 요청·응답, 오류 내용을 투영한 뒤 저장한다. provider 입력, Capture 자체, 일반 로그·감사·리포트, 직접 DB writer와 기존 행에는 적용하지 않는다. 외부 모델 사용 허가나 완전한 DLP 적용을 의미하지 않는다.

## 목적과 신뢰 경계

민감 응답을 이미 받은 뒤 마스킹하는 것은 그 응답에 접근하지 않았다는 증명이 아니다. E②의 실행 허용 및 E③의 원본 증거 판정은 이 투영과 별도다. 판정용 원본을 마스킹된 출력으로 덮어쓰지 않는다.

이번 코어는 자유 문장에서 모든 개인정보를 찾아낸다고 가정하지 않는다. 서버가 지정한 구조와 값만 출력하고, 자격증명·개인정보·실응답은 필드 전체를 `[REDACTED]`로 바꾼다. 미분류 데이터는 출력하지 않는다. 원문을 보내고 나중에 로그만 가리는 순서로 외부 전송을 보호할 수 없다.

정책은 호스트가 고정한 코드·설정에서 만든다. LLM·대상 응답·요청 JSON이 필드 분류나 enum 허용값을 정하게 해서는 안 된다. `Enum`은 안전성을 추측하는 검사기가 아니라 호스트가 미리 선택한 상수 목록이다. 임의 문장을 enum에 넣으면 보호 전제가 깨진다. 정수 범위에도 상태 코드·시도 횟수 등 해당 필드에 맞는 최소 범위를 둔다. 정책은 출력 목적별로 최소화하고, 진단용 내부 주소 등은 외부 입력용 정책에 자동 포함하지 않는다.

## API

- `New(Limits, map[string]Rule)`은 불변 정책 사본을 만든다. 필수 한도가 없거나 유효하지 않은 규칙은 거부한다. nil/zero 정책도 출력할 수 없다.
- `Redact()`는 타입과 관계없이 값 전체를 바꾼다. 원래 값이 `null`이어도 결과는 `[REDACTED]`다.
- `Bool()`, `Integer(min, max)`, `Enum(fixed...)`은 지정한 메타데이터 값만 유지한다. Integer는 지수·소수 표현을 허용하지 않는다.
- `Object(fields)`, `Array(item)`은 중첩 구조에도 같은 정책을 적용한다. 미등록 키는 전체 문서를 차단한다. 없는 필드는 생성하지 않는다. 필수 증거의 완전성 검사는 E③ 등 호출자의 책임이다.
- `Policy.Project(ctx, input)`은 단일 JSON 객체를 검사하고 `SafeJSON`을 반환한다. 실패 시 빈 결과와 원문 없는 오류만 반환한다.
- `SafeJSON.Bytes()`는 사본을 반환한다. `SafeJSON.Write(ctx, writer)`는 유효한 결과만 writer에 한 번 전달한다. 실패한 투영의 zero 결과는 쓸 수 없다.

키는 대소문자를 구분해 조회한다. 호스트 schema 이름은 영문자로 시작하는 ASCII 영문·숫자·`_`·`-`로 제한하고 최대 64바이트다. 정규화 시 대소문자와 `_`·`-`를 무시했을 때 같은 필드는 한 schema에 중복 등록할 수 없다.

자격증명·개인정보·원문용 주요 이름은 같은 정규화 기준으로 `Redact`만 등록할 수 있다. 예: `Authorization`, `Cookie`, `api_key`, `access_token`, `password`, `email`, `account_id`, `card_number`, `body`, `raw_request`, `raw_response`, `messages`, `error`. 이 목록은 닫힌 schema에 더하는 보조 방어다. 모든 민감 필드 별칭을 찾아낸다는 보장이 아니며, 목록에 없는 이름도 미등록이면 차단된다.

## 차단과 한도

- 미등록 키, 타입·범위·enum 불일치, JSON 중복 키, 후속 문서, 문법 오류는 차단한다. `\u`로 이스케이프한 동일 키도 중복이다.
- 원문 UTF-8 오류와 잘못된 UTF-16 escape는 차단한다. Go JSON 디코더의 보정으로 입력이 달라지지 않도록 디코딩된 U+FFFD도 거부한다.
- 입력·출력 바이트, 값의 깊이, 객체당 필드 수, 배열당 원소 수 한도를 모두 명시한다. 깊이는 root 객체가 1이다. 가려질 값 내부에도 깊이·중복·크기 검사를 적용한다.
- 호스트 schema 깊이 상한은 별도로 64다. 입력 한도가 작다는 이유로 준비된 schema 자체를 바꾸지 않는다.
- 파싱 전·토큰 처리 중·직렬화 전후에 취소를 확인한다. 쓰기 직전에 다시 확인하며 취소된 작업은 writer를 호출하지 않는다.
- writer가 일부만 기록하면 오류를 반환한다. 자동 재시도·원문 fallback은 없다. 기록 시작 후 취소나 I/O 실패는 이미 기록된 바이트를 되돌리지 못하므로 원자적 파일 교체·DB transaction·전송 중지는 해당 어댑터가 책임진다.

오류 문자열에는 입력값·필드 경로·parser offset·writer 오류 원문을 넣지 않는다. writer·context 오류는 `errors.Is/As`로 원인을 검사할 수 있지만, unwrap한 원인을 공유 로그에 출력하지 않는다. 마스킹은 실행·판정·DB 저장 권한이 아니다.

## 최소 연결 예

```go
policy, err := dlp.New(limitsFromHostConfig, map[string]dlp.Rule{
    "status":   dlp.Integer(100, 599),
    "decision": dlp.Enum("allow", "block"),
    "body":     dlp.Redact(),
})
if err != nil {
    return err
}
safe, err := policy.Project(ctx, structuredEvidence)
if err != nil {
    return err // 원문 fallback 금지
}
_, err = safe.Write(ctx, destinationWriter)
return err
```

입력 `{"status":200,"decision":"allow","body":{"private":"fixture"}}`의 결과는 `{"body":"[REDACTED]","decision":"allow","status":200}`다. `{"decision":"미등록 문장"}`은 writer 호출 없이 차단한다.

## llmrec 저장 경계

`Wrap`은 기존 시그니처를 유지하며 내부에서 불변 호스트 정책을 만든다. recording을 켜도 원문 기록으로 되돌릴 수 없다. provider 요청과 호출자에게 전달되는 응답·이벤트·오류는 그대로다. DLP는 그 결과를 DB에 복제하는 경계에 적용한다.

| DB 필드 | 신규 Recorder 저장값 |
|---|---|
| `request_body` | 유효한 JSON. system/messages/tools/stop/thinking/temperature는 `[REDACTED]`, max_tokens/tools_count는 범위 검사한 정수 |
| `response_body` | 유효한 JSON. text/thinking은 `[REDACTED]`, usage는 범위 검사한 정수, stop_reason은 고정된 알려진 값 또는 `[REDACTED]` |
| `raw_request` | 비어 있지 않은 요청 capture가 있으면 `[REDACTED]`, 없으면 빈 값 |
| `raw_response` | HTTP 응답 attempt가 있으면 빈 응답이어도 `[REDACTED]`, attempt가 없으면 빈 값 |
| `error` | 호출 오류·취소가 있으면 `[REDACTED]`, 없으면 빈 값 |

호스트가 소유하는 model/profile/session/task/worker 참조, 상태, 지연시간, 사용량 메타데이터는 유지한다. 여기에 대상 원문이나 자격증명을 넣으면 안 된다. stop_reason은 고정 switch로 먼저 분류하고 고정 enum으로 검증한다. 제공자가 준 문자열을 enum에 동적으로 등록하지 않는다.

정규화 본문은 원문을 직렬화하기 전에 최소화하고, raw 본문은 Capture lock 안에서 존재 여부만 읽는다. 원문 wire를 DB용 문자열로 복사하거나 Stream의 text/thinking을 누적하지 않는다. 잘못된 메시지 내 RawMessage, 대형 본문, 임의 SSE framing도 이 기록 경계를 우회하지 않는다. Capture의 일시적인 원문과 provider 통신은 별도다.

request/response/raw/error wrapper 전체가 `dlp.Project`를 통과한 뒤에만 각 컬럼으로 분해해 동기 INSERT한다. 정책 누락·오류·투영 실패는 `llm_records` 행을 저장하지 않으며 원문 fallback은 없다. `llm_usage` 기록과 provider 결과 전달은 유지한다. 실패 로그는 원문 없는 고정 문구다. 취소된 호출의 최종 안전 기록을 남기기 위해 투영은 bounded in-memory 작업에서 독립 context를 사용한다.

기록용 정책 한도는 최소화된 메타데이터에 적용하는 내부 구조 상한이다: 입력·출력 각 32 KiB, 값 깊이 8, 객체당 16필드, 배열당 1원소, 정수는 PostgreSQL INTEGER 범위의 비음수다. 원문 크기와 관계없이 저장 대상은 이 최소 메타데이터뿐이다.

과거 원문 행을 삭제·이관하지 않는다. archive/restore와 `db.InsertLLMRecord` 직접 호출은 별도 책임이며 이 PR이 DB 전체에 보편적인 DLP를 강제하는 것은 아니다.

## 후속 연결

LLM 입력 전 검사, 일반 로그·감사·리포트는 별도 호스트 분류와 어댑터가 필요하다. 각 adapter는 성공한 투영만 목적지로 보낸다. 이 저장 경계에서 가렸다는 이유로 이미 모델에 보낸 데이터가 보호됐다고 설명해서는 안 된다.

완전 로컬·외부 모델 API 송신 0 원칙은 유지한다. 이후 외부 모델 연동을 선택하더라도 전송 전 검사를 별도 배선·검증해야 한다. 문자열 정규식 마스킹만으로 외부 전송을 허용하지 않는다.
