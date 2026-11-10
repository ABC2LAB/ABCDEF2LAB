# 운영자 pin manifest 엄격 파서

> **코어 도입 단계의 범위·검증 기록입니다.** 본문의 서버/profile 연결을 후속으로 둔 표현은 당시 단계의 경계입니다. 현재 provider 연결은 [서버 로컬 모드](local-llm-server-mode.md)와 [로컬 개발 안내](../local-dev-ko.md)를 확인합니다. 아래 입력·신뢰·실패 계약과 코어 단독의 한계는 유지하며, 연결 구현을 실모델 수용이나 제품 전체 망 격리 완료로 해석하지 않습니다.

2026-11-02 · 이동찬

상태: **JSON bytes → 로컬 profile resolver의 순수 파서. Linux 파일 로더는 [별도 계약](./local-llm-manifest-file.md)으로 구현했고 실제 profile/provider 연결은 후속이다.** [profile 정책 입력 계약](./local-llm-profile-policy.md)의 host-owned `ProfilePin`을 만들어 기존 `NewProfileResolver`에 전달한다. 기존 서버 설정·provider 선택·캐시·fallback은 바꾸지 않는다.

## JSON v1 계약

```json
{
  "version": 1,
  "profiles": [
    {
      "profile_id": 7,
      "format": "openai",
      "model": "local:test",
      "base_url": "http://model.internal:11434/v1",
      "addresses": ["10.0.0.1"]
    }
  ]
}
```

위 값은 합성 예시이며 실제 모델·주소를 코드에 고정하지 않는다. 이 파일에 credential을 넣지 않는다. API key·header·cookie·fallback·환경변수 확장·on/off 필드는 받지 않는다. local-only 적용/끔은 후속 host 설정의 책임이며 입력 해석 실패를 정책 끔으로 바꾸지 않는다.

envelope의 `version`, `profiles`와 각 profile의 다섯 필드는 전부 필수다. `version`은 JSON 형식 버전으로 정확한 숫자 token `1`만 허용한다. profile revision·정책 적용 세대·run/reset·승인 ID가 아니다. `profile_id`는 양의 int64 정규 십진 정수다. 문자열 ID, 소수·지수 표기, 음수·0·overflow는 거부하며 float64로 변환하지 않는다.

키는 **JSON escape를 해석한 뒤 정확한 소문자 이름**과 비교한다. 한 번 나온 `"\u0076ersion"`은 `"version"`과 같은 키다. 둘이 함께 나오면 중복이며, `Version` 같은 대소문자 변형은 알 수 없는 필드로 거부한다. 필드 순서는 자유다. 객체의 모든 계층에서 중복·알 수 없는 필드·필수 필드 누락을 거부한다. `null`, 잘못된 타입, comments·trailing comma·뒤에 붙은 다른 JSON/문자열은 허용하지 않는다. 문서 앞뒤의 정상 JSON whitespace는 허용한다.

## 상한과 의미 검증

| 항목 | 허용 범위 |
|---|---|
| 전체 JSON bytes | 1..1,048,576 bytes. whitespace 포함 |
| profiles | 1..64개. 같은 profile ID 중복 금지 |
| 각 addresses | 1..64개 IP 문자열. 정규화 후 중복 금지 |
| format | 1..64 UTF-8 bytes |
| model | 1..1024 UTF-8 bytes |
| base_url | 1..8192 UTF-8 bytes |

전체 크기는 decoder 생성 전에 검사하고 배열은 다음 원소를 읽거나 append하기 전에 상한을 확인한다. raw UTF-8 오류와 짝이 맞지 않는 UTF-16 surrogate escape는 decoder가 대체 문자로 복구하기 전에 거부한다. 정상 surrogate pair, 명시적인 U+FFFD, escape한 backslash는 허용한다. JSON 문법 검사는 표준 decoder가 수행한다.

파서는 문자열을 trim·소문자화·기본값 보충하지 않는다. URL·local IP·zone·literal origin과 pin 일치·중복 pin·profile tuple 검증은 [기존 정책 코어](./local-llm-policy.md)와 `NewProfileResolver`를 재사용한다. 공인 IP, pin 누락, 형식 오류 또는 뒤 profile 하나의 오류도 전체 등록을 거부한다. DNS를 조회해 pin을 보충하지 않는다. `format`이 실제 SDK에서 지원되거나 모델이 실제 설치됐다는 뜻은 아니다.

## 호출과 오류

[`ParseProfileManifest`](../../llmpolicy/manifest.go)는 `[]byte`를 받아 성공 시 완성된 `*ProfileResolver`만 반환한다. caller는 그 resolver에 실제 host-selected `ProfileTarget`을 exact tuple로 조회한다. 원본 `/v1` 경로 등 target 값은 그대로 보존되며 origin 정책 생성은 기존 resolver가 담당한다.

모든 실패는 `nil` resolver와 고정 `ErrInvalidProfileManifest`다. 부분 pin 목록·구체적인 필드 경로·원문 URL/IP/model·underlying JSON/주소 오류를 노출하지 않는다. 성공 객체는 입력 byte slice와 독립적이며 매번 새 registry를 만들고 동시 파싱이 가능하다. 입력을 동시에 변경하는 caller까지 동기화하는 API는 아니다.

파싱 성공은 **파일·운영자 출처 인증이 아니다.** pin과 편집 가능한 DB profile의 독립성, host 적용/끔 설정, 재로드·정책 제거·기존 provider 무효화·fallback 차단은 후속이다. [Linux 파일 로더](./local-llm-manifest-file.md)는 이 API 호출 전에 읽기 자체의 상한과 신뢰를 검증한다. 파서가 반환한 resolver를 기존 서버에 자동 설치하거나 이전 provider를 폐기하지 않는다.

## 시험 범위

[`합성 byte 시험`](../../llmpolicy/manifest_test.go)은 허용 입력의 실제 `Resolve`/`Decide`, 누락·중복·unknown·case 변형·타입·overflow·최대 경계와 초과, Unicode·trailing 입력, 전체 등록 거부, 원본 변경·동시 파싱·안전한 오류·fuzz를 확인한다. 파일 읽기·DB/profile 적용·provider 생성·실제 모델 호출 시험과 구분한다.

**확인한 결과:** 신규 단위 사례 99개와 fuzz seed 5개를 포함해 `llmpolicy` 전체 최하위 사례 342개가 race에서 통과했고 실패·skip은 0이다. 별도 fuzz는 71,827회 실행에서 실패가 없었다. 관련 vet·전체 build·gofmt/공백·상대 링크 검사와 독립 코드 리뷰를 확인했다. 실제 파일 인증·서버 적용·provider 연결은 실행하지 않았다.
